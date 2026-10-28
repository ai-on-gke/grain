package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/version"
)

// runHostGrain is the grain binary's path inside the deployment image
// (Dockerfile), which is what a run host runs.
const runHostGrain = "/usr/local/bin/grain"

// RunHostImageSelf is the -run-host-image value that means "the image
// this daemon's own Deployment runs", read back from the Deployment at
// startup. An upgrade repoints the Deployment at a verified digest and
// restarts the daemon, so new run pods follow every upgrade with no
// second image reference to keep in step -- and never a mutable tag
// that a push to the registry could change under a running deployment.
const RunHostImageSelf = "self"

// CheckRunHostBackend refuses -run-host-image together with
// -container-sandboxes. A run host's `grain mcpserver` is handed
// `-container grain-sandbox-<run>` and runs every sandbox tool as
// `docker exec` into it (mcp.ContainerExecRunner), and a run host has no
// docker socket -- nor, in the deployment image, a docker CLI -- so every
// sandbox tool would fail and every run with it
// (docs/test-runs/2026-10-14-run-hosts-daemon.md, finding 6).
//
// Mounting the socket would make it work, and would hand each run host
// root on the machine, which is a trust boundary a flag should not move
// quietly. Routing tool calls through the daemon instead would not
// survive the daemon restarting, which is what a run host is for. Until
// one of those is chosen on purpose, the combination does not start.
// Granule sandboxes are reached through the exec key NewRunHost mounts,
// and Kubernetes ones through the API, so neither is refused.
//
// A Settings choice of the container backend is not refused here: it is
// not offered while run hosts are on (sandboxBackendAvailable), and a
// stored one falls back to the flags' backend with a note (LoadConfig).
func CheckRunHostBackend(cfg Config) error {
	if cfg.RunHostImage != "" && cfg.ContainerSandboxes {
		return errors.New("-run-host-image cannot be used with -container-sandboxes: a run host's mcpserver " +
			"reaches a container sandbox with `docker exec`, and a run host has no docker socket, so every " +
			"sandbox tool would fail -- drop -run-host-image to run agents as children of the daemon, or use " +
			"-granule-sandboxes or -k8s-sandboxes, whose sandboxes a run host can reach")
	}
	return nil
}

// NewRunHost builds the runhost.Host a deployment with -run-host-image
// drives its claude runs in (docs/non-disruptive-upgrade.md, Stage 2),
// or nil when the flag is unset, which leaves every run a child of the
// daemon under agent.LocalHost exactly as before.
//
// The run host is given whole directories only: its spool and staging
// roots under the data directory, the shared credential directory
// read-write (-run-host-credential-dir), and, with granule sandboxes,
// the directory holding the exec key read-only. It is never given the
// data directory itself, which holds the store and the secrets. With
// container sandboxes it would have no way to reach its sandbox at all,
// which is why checkConfig refuses that pair (CheckRunHostBackend).
func NewRunHost(ctx context.Context, cfg Config, store *model.Store) (*runhost.Host, error) {
	if cfg.RunHostImage == "" {
		return nil, nil
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("-run-host-image needs a data directory for the run hosts' spools")
	}
	deployment, err := store.InstanceID(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading this installation's identity, for its run-host labels: %w", err)
	}
	rootDir := cfg.RunHostDir
	if rootDir == "" {
		rootDir = filepath.Join(cfg.DataDir, "runhosts")
	}
	root, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	var mounts []runhost.Mount
	if cfg.RunHostCredentialDir != "" {
		if err := os.MkdirAll(cfg.RunHostCredentialDir, 0o700); err != nil {
			return nil, fmt.Errorf("-run-host-credential-dir: %w", err)
		}
		mounts = append(mounts, runhost.Mount{Path: cfg.RunHostCredentialDir})
	}
	if cfg.GranuleSandboxes && cfg.GranuleExecKey != "" {
		mounts = append(mounts, runhost.Mount{Path: filepath.Dir(cfg.GranuleExecKey), ReadOnly: true})
	}
	generation := version.Get().Release
	if generation == "" {
		generation = version.Get().Revision
	}
	image := cfg.RunHostImage
	docker := runhost.Docker{}
	if cfg.RunHostCredentialDir != "" {
		// The credential directory is where the design puts ~/.claude,
		// and the one directory of the daemon's that a run host has
		// read-write, as newKubeRunHostDriver's HOME is.
		docker.Env = map[string]string{"HOME": cfg.RunHostCredentialDir}
	}
	var driver runhost.Driver = docker
	if cfg.K8sSandboxes {
		kube, err := newKubeRunHostDriver(ctx, cfg)
		if err != nil {
			return nil, err
		}
		kube.Client.Instance = k8sInstanceLabel(ctx, store)
		driver = kube
		if image == RunHostImageSelf {
			deployment, container := K8sSelfNames(cfg)
			if image, err = kube.Client.DeploymentImage(ctx, deployment, container); err != nil {
				return nil, fmt.Errorf("-run-host-image=%s: reading the image this daemon runs: %w", RunHostImageSelf, err)
			}
		}
	} else if image == RunHostImageSelf {
		return nil, fmt.Errorf("-run-host-image=%s is only understood under -k8s-sandboxes; name the image", RunHostImageSelf)
	}
	return &runhost.Host{
		Driver:      driver,
		Deployment:  deployment,
		Generation:  generation,
		Image:       image,
		Grain:       runHostGrain,
		User:        fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		SpoolRoot:   filepath.Join(root, "spool"),
		StagingRoot: filepath.Join(root, "staging"),
		Mounts:      mounts,
		// A run whose daemon is stopping is left running, its cursor
		// persisted, for the next daemon to adopt (RecoverRunHosts,
		// orchestrator.ResumeAdoptedRun); every other cancellation --
		// its deadline, its task closed -- still destroys it.
		Detach: orchestrator.StoppedByDaemon,
		OnCursor: func(ctx context.Context, run string, cursor int64) {
			if err := store.SetSpoolCursor(ctx, run, cursor, time.Now().UTC()); err != nil {
				log.Printf("grain daemon: run %s: persisting its spool cursor: %v", run, err)
			}
		},
	}, nil
}

// newKubeRunHostDriver is the run-pod driver for a daemon under
// -k8s-sandboxes (docs/non-disruptive-upgrade.md, "The shared credential
// directory", Kubernetes). Each -k8s-run-host-volume claim is checked
// before any run is dispatched onto it: a gcsfuse-backed claim, a
// ReadWriteOncePod one, or (under -k8s-run-host-rwx) one that is not
// ReadWriteMany is refused at startup rather than at the first run.
//
// Run pods run under the daemon pod's own securityContexts, read back
// from its Deployment as the upgrade's check pod is, so they are the uid
// that owns the volume; HOME is the credential directory, which is where
// the design puts ~/.claude.
func newKubeRunHostDriver(ctx context.Context, cfg Config) (runhost.Kubernetes, error) {
	client := k8s.Client{Kubectl: cfg.K8sKubectl, Kubeconfig: EffectiveK8sKubeconfig(cfg), Namespace: cfg.K8sNamespace}
	if len(cfg.K8sRunHostVolumes) == 0 {
		return runhost.Kubernetes{}, fmt.Errorf("-run-host-image under -k8s-sandboxes needs at least one " +
			"-k8s-run-host-volume CLAIM:PATH: a run pod sees the spool and the credential directory only through a " +
			"claim the daemon pod has mounted")
	}
	var volumes []runhost.KubeVolume
	for _, v := range cfg.K8sRunHostVolumes {
		claim, path, ok := strings.Cut(v, ":")
		if !ok || claim == "" || !filepath.IsAbs(path) {
			return runhost.Kubernetes{}, fmt.Errorf("-k8s-run-host-volume %q: want CLAIM:/absolute/path", v)
		}
		info, err := client.ClaimInfo(ctx, claim)
		if err != nil {
			return runhost.Kubernetes{}, fmt.Errorf("-k8s-run-host-volume %s: %w", claim, err)
		}
		warning, err := runhost.CheckClaim(info, claim, cfg.K8sRunHostRWX)
		if err != nil {
			return runhost.Kubernetes{}, err
		}
		if warning != "" {
			log.Printf("grain daemon: warning: %s", warning)
		}
		volumes = append(volumes, runhost.KubeVolume{Claim: claim, MountPath: filepath.Clean(path)})
	}
	driver := runhost.Kubernetes{
		Client:         client,
		Volumes:        volumes,
		ServiceAccount: cfg.K8sRunHostServiceAccount,
	}
	if !cfg.K8sRunHostRWX {
		driver.DaemonSelector = map[string]string{
			"app.kubernetes.io/name":      "grain",
			"app.kubernetes.io/component": runhost.KubeDaemonComponent,
		}
	}
	deployment, container := K8sSelfNames(cfg)
	if security, err := client.DeploymentSecurity(ctx, deployment, container); err == nil {
		driver.Security = security
	} else {
		log.Printf("grain daemon: run pods: reading deployment %s's securityContext (%v); running them as uid %d instead",
			deployment, err, os.Getuid())
	}
	if driver.Security.Empty() {
		driver.Security.Pod = k8s.SecurityContext{"runAsUser": os.Getuid(), "runAsGroup": os.Getgid()}
	}
	home := cfg.RunHostCredentialDir
	if home == "" {
		home = os.Getenv("HOME")
	}
	if home != "" {
		driver.Env = map[string]string{"HOME": home}
	}
	return driver, nil
}

// k8sInstanceLabel is the k8s.InstanceLabel value this deployment's pods
// are created with: its effective deployment id (store.DeploymentID),
// the {deployment} its task branches are named by. Unreadable, it is
// empty -- pods unlabelled, and every grain sandbox pod in the namespace
// listed, as before the label existed -- rather than a reason not to
// start; LiveConfig.applySandboxInstance retries it on every tick.
func k8sInstanceLabel(ctx context.Context, store *model.Store) string {
	id, err := store.DeploymentID(ctx)
	if err != nil {
		log.Printf("grain daemon: reading the deployment id for Kubernetes pod labels: %v", err)
		return ""
	}
	return id
}
