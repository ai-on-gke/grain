// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/secrets"
)

// NewHostdRemote is the acceptor for a grain hostd dialing in with the token
// in tokenFile, or $GRAIN_HOSTD_TOKEN, as hostd name owned by owner; nil
// when neither names a token.
func NewHostdRemote(tokenFile, name, owner string) (*hostd.Remote, error) {
	token := strings.TrimSpace(os.Getenv("GRAIN_HOSTD_TOKEN"))
	if tokenFile != "" {
		t, err := ReadTokenFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("reading -hostd-token-file: %w", err)
		}
		token = t
	}
	if token == "" {
		return nil, nil
	}
	if name == "" {
		name = hostd.DefaultSharedName
	}
	if err := model.ValidateHostdName(name); err != nil {
		return nil, fmt.Errorf("-hostd-token-name: %w", err)
	}
	if name == model.LocalHostd {
		return nil, fmt.Errorf("-hostd-token-name: %q is the co-located host's name", name)
	}
	owner, err := model.NormalizeHostdOwner(owner)
	if err != nil {
		return nil, fmt.Errorf("-hostd-token-owner: %w", err)
	}
	log.Printf("grain daemon: -hostd-token-file is deprecated and goes in the next release: register hostd %s in the hostd registry (POST /api/hostds) and give it the token it answers with", name)
	return &hostd.Remote{Token: token, SharedName: name, SharedOwner: owner, Logf: log.Printf}, nil
}

// CheckDaemonFlags validates every "these flags cannot be started with" rule
// for `grain daemon` over the parsed Config.
//
// What is *not* here is anything about a credential. An agent key is set
// from the UI now and read per dispatch (agentFrameworks), so a daemon
// with none is a daemon whose runs end as setup-failed saying which key
// is missing, and whose UI is up for somebody to paste one into --
// refusing to start over it would take away the screen the fix is made
// on. This checks only what cannot be repaired without a restart anyway:
// a missing directory to keep state in, two sandbox backends asked for
// at once, a granule deployment with no way to reach its VMs, an upgrade
// path half-configured, a concurrency that is not a number of runs, and
// an interval that is not a length of time.
func CheckDaemonFlags(cfg Config, hasLocalSandboxes bool, checkPackageCache func(Config) error) error {
	if cfg.DataDir == "" {
		return errors.New("-data-dir is required")
	}
	switch cfg.DbDriver {
	case "", "sqlite":
	case "postgres":
		if cfg.DbDSN == "" && !postgres.IsAvailable() {
			return errors.New("-db-dsn is required when -db-driver is postgres")
		}
	default:
		return fmt.Errorf("-db-driver must be \"sqlite\" or \"postgres\", got %q", cfg.DbDriver)
	}
	if err := CheckSecretsConfig(cfg.SecretsBackend, cfg.SecretsSMPrefix); err != nil {
		return err
	}
	if cfg.ReleaseApprovalKMSKey != "" {
		if err := secrets.ValidateKMSKeyVersionName(cfg.ReleaseApprovalKMSKey); err != nil {
			return fmt.Errorf("-release-approval-kms-key: %w", err)
		}
	}
	if err := CheckObjectStoreURI(cfg); err != nil {
		return err
	}
	if err := CheckSandboxBackend(cfg, hasLocalSandboxes); err != nil {
		return err
	}
	if err := CheckHostExecutorFlags(cfg); err != nil {
		return err
	}
	if err := CheckCLIRunFlags(cfg); err != nil {
		return err
	}
	// Four backends now, so this is a count rather than a pair: any two
	// of them together is the same misconfiguration, and naming which
	// two were passed is what tells an operator which flag to drop.
	if selected := SelectedSandboxBackends(cfg); len(selected) > 1 {
		return fmt.Errorf("%s select different sandbox backends -- pass one of them, not several",
			strings.Join(selected, " and "))
	}
	if cfg.ContainerSandboxes {
		if cfg.ContainerImage == "" {
			return errors.New("-container-sandbox-image is required with -container-sandboxes")
		}
		if cfg.ContainerGitProxyHost == "" {
			return errors.New("-container-git-proxy-host is required with -container-sandboxes")
		}
	}
	if cfg.K8sSandboxes {
		if cfg.K8sWorkspace == "" {
			return errors.New("-k8s-workspace is required with -k8s-sandboxes")
		}
		if cfg.K8sGitProxyHost == "" {
			return errors.New("-k8s-git-proxy-host is required with -k8s-sandboxes (in-cluster, set GRAIN_POD_IP " +
				"from the downward API instead and it is taken from there -- deploy/kubernetes/deployment.yaml does)")
		}
	}
	if cfg.K8sSandboxes || cfg.K8sGranuleSandboxes {
		for _, entry := range cfg.K8sNodeSelector {
			if !strings.Contains(entry, "=") {
				return fmt.Errorf("-k8s-node-selector %q is not a \"key=value\" pair", entry)
			}
		}
	}
	if cfg.K8sGranuleSandboxes {
		if cfg.K8sGranuleImage == "" {
			return errors.New("-k8s-granule-image is required with -k8s-granule-sandboxes")
		}
		if cfg.K8sWorkspace == "" {
			return errors.New("-k8s-workspace is required with -k8s-granule-sandboxes -- the guest account's " +
				"home, the same field -k8s-sandboxes names its container's")
		}
		if cfg.K8sGitProxyHost == "" {
			return errors.New("-k8s-git-proxy-host is required with -k8s-granule-sandboxes (in-cluster, set " +
				"GRAIN_POD_IP from the downward API instead and it is taken from there)")
		}
		if !cfg.DisableSelfUpgrade && cfg.UpgradeImage != "" && cfg.UpgradeImageRefFile == "" {
			return errors.New("-upgrade-image is not supported yet with -k8s-granule-sandboxes: this backend's " +
				"self-upgrade would need a privileged, /dev/kvm-mounted health-check pod rather than the plain " +
				"one -k8s-sandboxes checks a candidate image with, and nothing here builds that yet -- repoint " +
				"this Deployment's image by hand instead (`kubectl set image deployment/<name> " +
				"<container>=<image>`)")
		}
	}
	if checkPackageCache != nil {
		if err := checkPackageCache(cfg); err != nil {
			return err
		}
	}
	if !cfg.DisableSelfUpgrade {
		if cfg.UpgradeSrcDir != "" && cfg.UpgradeInstallPath == "" && cfg.UpgradeImage == "" {
			return errors.New("-upgrade-install-path is required with -upgrade-src-dir")
		}
		// A ref file is how a *host* deployment tells its unit which image to
		// start, so an -upgrade-image with nowhere to record its answer
		// upgrades nothing. A Kubernetes deployment has no unit and no such
		// file: the Deployment's own spec is the record, and repointing it is
		// the upgrade (upgrade.ModeKube).
		if cfg.UpgradeImage != "" && cfg.UpgradeImageRefFile == "" && !cfg.K8sSandboxes {
			return errors.New("-upgrade-image-ref-file is required with -upgrade-image (except with -k8s-sandboxes, " +
				"where an upgrade repoints this deployment's own Deployment instead of writing a file)")
		}
		if cfg.K8sSandboxes && cfg.UpgradeImage != "" {
			if cfg.K8sDeployment == "" {
				return errors.New("-k8s-deployment is required with -k8s-sandboxes and -upgrade-image: an upgrade " +
					"has to know which Deployment to repoint")
			}
			if cfg.K8sContainer == "" {
				return errors.New("-k8s-container is required with -k8s-sandboxes and -upgrade-image")
			}
		}
	}
	if cfg.GranuleSandboxes {
		if cfg.GranuleSSHUser == "" {
			return errors.New("-granule-ssh-user is required with -granule-sandboxes")
		}
		if cfg.GranuleWorkspace == "" {
			return errors.New("-granule-workspace is required with -granule-sandboxes")
		}
		if cfg.GranuleGitProxyHost == "" {
			return errors.New("-granule-git-proxy-host is required with -granule-sandboxes")
		}
	}
	if cfg.MaxWorkers < 1 {
		return errors.New("-max-workers must be at least 1")
	}
	// Refused for the same reason as -max-workers above, and for one
	// more: this is the value loadConfig falls back to when the stored
	// interval is one no reconcile loop can be paced by
	// (runnableStoredConfig), so a flag that is not itself runnable would
	// leave that fallback with nothing to offer -- and a non-positive
	// interval reaching time.NewTicker panics the loop while leaving the
	// UI server up, which is the failure that is hardest to notice
	// (grain/task-316).
	if cfg.PollInterval <= 0 {
		return errors.New("-poll-interval must be positive")
	}
	if cfg.BackupInterval < 0 {
		return errors.New("-backup-interval cannot be negative")
	}
	if cfg.MaxMergers < 0 {
		return errors.New("-max-mergers cannot be negative")
	}
	if err := CheckUIAllowedPrincipals(cfg); err != nil {
		return err
	}
	if err := CheckUIRequireVerifier(cfg); err != nil {
		return err
	}
	if err := CheckCICacheBounds(cfg); err != nil {
		return err
	}
	if err := CheckManagementURL(cfg); err != nil {
		return err
	}
	return CheckGritURL(cfg)
}
