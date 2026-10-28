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
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/capability/portforward"
	"github.com/ai-on-gke/grain/pkg/capability/selfdebug"
	"github.com/ai-on-gke/grain/pkg/cifault"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelproxy"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/policy"
	"github.com/ai-on-gke/grain/pkg/sandboxaddr"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// LocalAuditPath is where a deployment keeps its local audit log of
// every task its local policy refused at dispatch
// (orchestrator.Deps.PolicyAudit). No HTTP route reads, writes or
// removes it (TestLocalAuditLogIsReachedByNoRoute). The file keeps the
// name it had when a Silo hub's messages were logged there too
// (docs/hub-removed.md), so a deployment's existing log is appended to
// rather than left behind.
func LocalAuditPath(dataDir string) string {
	return filepath.Join(dataDir, "hub-audit.log")
}

// LogIgnoredRelaySettings logs one line when a data directory still holds
// relay-settings.json or the relay/token secret from before the rendezvous
// relay was removed.
func LogIgnoredRelaySettings(dataDir string, secretStore *secrets.Store, logf func(string, ...any)) {
	if dataDir == "" {
		return
	}
	var found []string
	settingsPath := filepath.Join(dataDir, "relay-settings.json")
	if _, err := os.Stat(settingsPath); err == nil {
		found = append(found, settingsPath)
	}
	hasSecret := false
	if secretStore != nil {
		if ok, err := secretStore.Has("relay", "token"); err == nil && ok {
			hasSecret = true
		}
	}
	if !hasSecret {
		if _, err := os.Stat(filepath.Join(dataDir, "secrets", "relay", "token")); err == nil {
			hasSecret = true
		}
	}
	if hasSecret {
		found = append(found, "secret relay/token")
	}
	if len(found) == 0 {
		return
	}
	if logf == nil {
		logf = log.Printf
	}
	logf("grain daemon: ignoring stored %s -- the rendezvous relay has been removed (docs/relay-removed.md)", strings.Join(found, " and "))
}

// run wires every piece pkg/orchestrator needs from real, on-disk material
// under cfg.dataDir (the store, secrets) and, for local sandboxing,
// cfg.sandboxDir (HostSandboxes' own per-run directories -- deliberately
// not under cfg.dataDir, see -sandbox-dir's own flag doc comment), and
// starts the reconcile loop; it returns only once
// ctx is cancelled. With -ui-addr set, a failure in the rest of the
// daemon -- runDaemon, below -- no longer counts as "ctx cancelled" for
// this purpose (bwsalmon/agents#550): only a failure to open the store,
// load configuration, or start the UI server itself still returns an
// error here early, since those leave nothing running worth keeping the
// process up for. See runDaemon's own doc comment.
func Run(ctx context.Context, cfg Config) error {
	// Before anything binds: how long the restart that started this
	// process left each listener unreachable is measured from the times
	// the previous process wrote down, against when each is bound below
	// (upgrade.GapRecorder, docs/non-disruptive-upgrade.md Stage 1).
	// Opened here so its start time is this process's, but it writes
	// nothing until the first listener is up: a start refused before then
	// -- the store guard, a bad flag -- leaves restart-gap.json as the
	// process that last served wrote it.
	if cfg.DataDir != "" {
		cfg.restartGaps = upgrade.OpenGapRecorder(upgrade.GapFile(cfg.DataDir),
			upgrade.DeploymentModeName(deploymentMode(cfg)), nil)
	}
	cfg.shuttingDown = new(atomic.Bool)
	cfg.shutdownStarted = ctx.Done()
	gaps, shuttingDown := cfg.restartGaps, cfg.shuttingDown
	// Bounded by Run itself, not only by ctx: a start that returns early
	// (a store at another schema, say) must not leave this behind to write
	// restart-gap.json into the data directory whenever ctx is cancelled
	// later -- by then the caller may be removing that directory, which is
	// exactly what made cmd/grain's schema tests flake on TempDir cleanup.
	runReturned, gapWatchStopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(gapWatchStopped)
		select {
		case <-ctx.Done():
			shuttingDown.Store(true)
			gaps.ShuttingDown()
		case <-runReturned:
		}
	}()
	defer func() {
		close(runReturned)
		<-gapWatchStopped
	}()

	// A data dir from before the single-tenant reset is refused before
	// anything is created in it, even when its store is somewhere this
	// start was not told about (checkStateRepoBaseline).
	if err := checkStateRepoBaseline(cfg.DataDir); err != nil {
		return err
	}

	// openStore, plus the one recovery a store this build cannot open has:
	// the note an in-place upgrade leaves when it cut over to a build with
	// a different schema, which authorises this start to move that store
	// aside and rebuild it from the rows the upgrade exported
	// (schemaupgrade.go). Without such a note this is openStore exactly,
	// and a store at the wrong schema still stops the process.
	store, db, schemaMigrated, err := openStoreForSchemaUpgrade(ctx, cfg.DbDriver, cfg.DbDSN, cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()
	// Recorded once the store has opened, so a mistyped DSN never replaces
	// a good one: a daemon started with -postgres-url (or the environment)
	// otherwise leaves nothing in its data dir saying where its store is.
	if err := RecordDatabaseConfig(cfg.DataDir, cfg.DbDriver, cfg.DbDSN); err != nil {
		log.Printf("grain daemon: recording the database in %s: %v", cfg.DataDir, err)
	}

	// Run hosts (pkg/runhost), when this deployment asks for them. Built
	// here, as soon as the store is, because every framework built below
	// launches into it.
	// Before any run is dispatched, where each CLI will run is said once
	// (clirun.go).
	dispatchCfg := DispatchConfig(ctx, store, cfg)
	logCLIRuns(ctx, dispatchCfg)
	// And whether this is a host-mode install no installer has migrated,
	// which is why a CLI above may have nowhere to run (installnotice.go).
	checkInstallMode(ctx, dispatchCfg)
	if cfg.RunHost, err = NewRunHost(ctx, cfg, store); err != nil {
		return err
	}
	// Host CLIs through grain hostd, started detached so a daemon
	// upgrade leaves them running (hostdruns.go).
	cfg.hostdRuns = newHostdRuns(cfg, store)
	// And the upgrade protocol's hold on them: flushed before an
	// upgrade's restart, reported on by the check after it.
	cfg.runHostUpgrade = newRunHostUpgrade(cfg.RunHost, cfg.hostdRuns, cfg.Ci).withStopping(cfg.shutdownStarted)

	// The state repository is opened and loaded into the database before
	// anything reads a row out of it: what is in the repository is what
	// this deployment is configured with, including whatever an agent's
	// merged pull request changed since the last start, and loadConfig
	// immediately below is the first thing that would read a stale
	// answer. See staterepo.go for the direction of travel and why the
	// *whole* import only happens here rather than on every tick -- the
	// timer below pulls too, and imports the settings tables of whatever
	// it finds, but nothing else replaces task and run rows once there
	// are runs live to hold them.
	stateRepo, err := OpenStateRepo(ctx, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("opening the state repository: %w", err)
	}
	if notice := RetiredTeamStateReposNotice(cfg.DataDir); notice != "" {
		log.Printf("grain daemon: %s", notice)
	}
	// And this deployment's encrypted secrets are reconciled with the
	// repository that backs them up (secretsrepo.go) before anything
	// opens the secret store below, because on a host restored onto a
	// fresh data directory this is where the ciphertext comes back from
	// -- and a store opened first would have minted a key over the
	// absence of one. Nothing is configured on most deployments and
	// nothing happens then.
	secretsRepo := StartupSecretsRepoSync(ctx, cfg.DataDir)
	loadErr := loadStateRepo(ctx, cfg.DataDir, stateRepo, db, schemaMigrated)
	if fatalStateRepoLoad(loadErr) {
		return fmt.Errorf("loading the config repository: %w", loadErr)
	}
	// Init asked the startup guard (model.Store.CheckSingleTenant) of the
	// store as it was opened. The load above can bring a multi-tenant
	// deployment's rows into it, from its state repository, so the same
	// question is asked of what it holds now, before anything serves it.
	if err := store.CheckSingleTenant(ctx); err != nil {
		return err
	}
	// Both halves of the schema change are done -- the store rebuilt at
	// this build's schema above, the repository's dump rewritten from it
	// in the load -- so the note that authorised them is spent. Kept
	// until here, rather than removed as soon as the store was rebuilt,
	// because a process that died in between would otherwise leave a
	// repository nothing was allowed to rewrite and a deployment that
	// could not start for it.
	if schemaMigrated {
		discardSchemaUpgrade(cfg.DataDir)
	}
	if loadErr != nil {
		log.Printf("grain: starting on the database this host already has: %s is as the last fetch left "+
			"it, the sync loop retries every %s, and the UI's State pane says so until one works. "+
			"The load reported: %v", stateRepo.Dir(), stateSyncInterval, loadErr)
	}
	// The encrypted-secrets backup is refused to every sandbox. The set is
	// live so changing that backup through the UI takes effect immediately.
	forbidden := gitproxy.NewForbiddenSet()
	// One manager over the local repository, shared by the timer and UI.
	//
	// The secrets database is opened once, here, and shared: the state
	// manager syncs it, and the model proxies below and runDaemon's own
	// dispatches resolve agent credentials through it.
	secretStore := OpenSecrets(cfg.DataDir)
	LogIgnoredRelaySettings(cfg.DataDir, secretStore, log.Printf)
	// Every secret a run resolves through it is audited against its team
	// (the Audit / Security tab). ui.NewServer attaches the store only to
	// the UI's own open of the secrets (ui_server.go), never to this one,
	// so without this no run's resolution was ever recorded (task 3140).
	secretStore.SetAuditLogger(store)
	stateManager := NewStateManager(cfg.DataDir, db, stateRepo, secretStore, secretsRepo, forbidden)
	stateManager.noteLoadFailure(loadErr)
	stateManager.loadAPIScopeSets(ctx)
	stopStateSync := startStateSync(ctx, stateManager.sync, stateManager.syncAll, stateManager.nudge)
	// And one reading of this machine a minute, folded into the hourly
	// rollups GET /api/metrics serves as its "host" section (hostLoad).
	// Started here rather than inside the UI server so that the series is
	// continuous whether or not anybody has the pane open: a history that
	// only exists while somebody is looking is no history at all. Nothing
	// waits for it on the way out -- it holds no resource and writes
	// nothing outside this process.
	go sampleHostLoad(ctx, hostLoad)
	// Subscription logins are renewed by grain, ahead of the codex CLI's
	// own refresh, which no lock guards across the processes sharing a
	// login (codex.Refresher). Like the sampler, it holds nothing anybody
	// waits for: a refresh cut off by shutdown leaves the file as it was.
	if cfg.DataDir != "" {
		// A copy: Run goes on to reassign cfg.
		loginCfg := Config{DataDir: cfg.DataDir, CodexAuthFile: cfg.CodexAuthFile}
		go (&codex.Refresher{Files: func() []string { return codexLoginFiles(loginCfg) }}).Run(ctx)
	}
	// Waited for here, and deliberately after `defer db.Close()` above so
	// that it runs *before* it: the loop's last act on the way out is one
	// final export, and an export against a closed database writes
	// nothing and reports why in the journal of a process that is already
	// leaving.
	defer stopStateSync()

	cfg, err = LoadConfig(ctx, store, cfg)
	if err != nil {
		return fmt.Errorf("loading deployment configuration: %w", err)
	}
	// This build is single-tenant only: the store resolves every task's
	// git credential as the default team's (model.Store.SingleTenant).
	store.SingleTenant = true
	// A team git connection written without a host is on the host the
	// git proxy forwards to (docs/git-hosts.md).
	store.GitDefaultHost = cfg.GithubHost
	// A pull request against the state repository is judged by what it
	// would grant once applied (orchestrator's stateRepoGuard).
	store.SettingsRepo = stateManager.settingsRepo
	// And so is a repository written without one (model.RepoRef.Host).
	model.SetDefaultGitHost(cfg.GithubHost)
	// The state and secrets repositories' tokens resolve through the
	// deployment's git connections from here on (RepoToken).
	defer SetRepoTokenStore(cfg.DataDir, store)()
	// -policy, read once for dispatch.
	if cfg.policy, err = loadLocalPolicy(ctx, cfg, store); err != nil {
		return err
	}
	// The local audit log of every task local policy refuses at
	// dispatch. One that cannot be opened is logged rather than fatal:
	// dispatch still refuses what the policy refuses.
	if cfg.DataDir != "" {
		if cfg.audit, err = policy.OpenAuditLog(LocalAuditPath(cfg.DataDir)); err != nil {
			log.Printf("grain daemon: %v; local policy refusals are not audited", err)
			err = nil
		} else {
			defer cfg.audit.Close()
		}
	}
	backupCtx, stopBackups := context.WithCancel(ctx)
	backupStopped := startScheduledBackups(backupCtx, db, cfg)
	defer func() {
		stopBackups()
		<-backupStopped
	}()
	// The sandbox backend is whichever one the flags (or the stored
	// Settings choice loadConfig just read) selected: one local
	// container, granule VM or Kubernetes pod per run, or none at all on a
	// deployment whose sandboxes are all remote. Whichever it is, the
	// result is just Deps.Sandboxes: the one thing runDaemon wants beyond
	// that interface -- the startup sweep for whatever a previous process
	// left behind -- every backend implements, so it asks for it by
	// interface (orphanReaper) rather than by carrying a concrete backend
	// alongside.
	var sandboxes orchestrator.Sandboxes
	// The image new sandboxes are created from: the stock one this
	// process resolves for its backend (stockSandboxImageFor), or the
	// custom OCI image this deployment names in Settings in its place
	// (model.Config.SandboxImage, customSandboxImageFor). The granule
	// backend asks sandboxImages on each create; every other backend is
	// constructed with the answer and handed changes through
	// orchestrator.SandboxImageSetter. Either way liveConfig keeps it in
	// step with the setting without waiting for a restart.
	var sandboxImages *sandboximage.Source
	stockSandboxImage := stockSandboxImageFor(cfg)
	customSandboxImage := customSandboxImageFor(cfg)
	startImage := firstNonEmpty(customSandboxImage, stockSandboxImage)
	if customSandboxImage != "" {
		go logCustomSandboxImage(ctx, cfg, customSandboxImage, stockSandboxImage, sandboxImageLabels())
	}
	// The sandboxes with no network, by address: what the granule
	// backend names them to, and what the package cache's resolver and
	// HTTP service hold to it (startPackageCache).
	restrictedSandboxes := &pkgcache.ClientSet{}
	// Filled in below, once the package cache itself exists
	// (startPackageCache) -- nil until then, which GranuleConfig's own
	// DNS field treats exactly like "the cache is off". Declared this
	// early so a granule VM's "-dns" can read it on every create,
	// including the first, without this block waiting on a cache that
	// is built well after the sandbox backend is.
	var packageCacheDNS func(context.Context) string
	// The ports a NetworkNone guest may reach on the docker gateway,
	// filled in below once the services listening on them are up.
	servicePorts := &SandboxServicePorts{}
	if cfg.DisableNativeSandboxes {
		sandboxes = orchestrator.NewDisabledSandboxes()
		log.Print("grain daemon: native sandboxes are disabled -- only remote execution sandboxes can exist")
	} else if cfg.GranuleSandboxes {
		// The image chooseSandboxImage settled on -- this build's own
		// stamp, an operator's -sandbox-image, or a pin kept -- with a
		// custom image from Settings resolved over it per create. Empty
		// only for the one case that function leaves alone (a
		// -kontur-image with no value at all, granule's to complain
		// about), and then the backend is handed no resolver.
		granuleCreateArgs := sandboxImageCreateArgs(cfg)
		var resolveSandboxImage func(context.Context) (string, error)
		if stockSandboxImage != "" {
			sandboxImages = &sandboximage.Source{}
			sandboxImages.Set(stockSandboxImage, customSandboxImage)
			resolveSandboxImage = sandboxImages.Image
		}
		// Nil unless this deployment named a pool, which is every
		// deployment before -granule-sandbox-cidr existed: grain then
		// names no address and docker assigns one, exactly as before.
		var addresses *sandboxaddr.Pool
		if cfg.SandboxCIDR != "" {
			addresses, err = sandboxaddr.NewPool(cfg.SandboxCIDR)
			if err != nil {
				return fmt.Errorf("-granule-sandbox-cidr: %w", err)
			}
			log.Printf("grain daemon: every sandbox VM takes an address from %s on docker network %q -- "+
				"%d families of %d, one family per sandbox and everything it creates",
				addresses.Prefix(), cfg.SandboxNetwork, addresses.Families(), sandboxaddr.FamilySize)
		}
		// NetMode, IP and Port are deliberately not set: all three are
		// deprecated in the granule this build vendors and are read
		// nowhere now (GranuleConfig's own comment). Assigning an address
		// is what Addresses does instead, by the one route that granule
		// still leaves open.
		granuleSandboxes := orchestrator.NewGranuleSandboxes(orchestrator.GranuleConfig{
			StateDir:        cfg.GranuleStateDir,
			Command:         cfg.GranuleCommand,
			CreateArgs:      granuleCreateArgs,
			SandboxImage:    resolveSandboxImage,
			SSHUser:         cfg.GranuleSSHUser,
			ExecKeyPath:     cfg.GranuleExecKey,
			Workspace:       cfg.GranuleWorkspace,
			ReadyTimeout:    cfg.GranuleReadyTimeout,
			Addresses:       addresses,
			DockerNetwork:   cfg.SandboxNetwork,
			DefaultCPUs:     cfg.SandboxCPUs,
			DefaultMemoryMB: cfg.SandboxMemoryMB,
			DefaultDiskGB:   cfg.SandboxDiskGB,
			// The deployment-wide network default, in force from the
			// first sandbox this process builds rather than from the
			// first reconcile tick (liveConfig only hands on what
			// *changed*), and the address a sandbox with no default
			// route still has to reach for a checkout to work at all.
			DefaultNetworkAccess: cfg.networkAccess,
			GitProxyHost:         cfg.GranuleGitProxyHost,
			// The package cache's own resolver, read through the
			// indirection above because the cache itself does not exist
			// yet at this point in startup. nil-checked rather than
			// called directly so a deployment with no package cache at
			// all -- packageCacheDNS never assigned -- creates VMs
			// exactly as it did before this field existed.
			DNS: func(ctx context.Context) string {
				if packageCacheDNS == nil {
					return ""
				}
				return packageCacheDNS(ctx)
			},
			// The package cache, told which guests have NetworkNone.
			DNSEgress: restrictedSandboxes,
			// And the only ports such a guest reaches at the gateway.
			SandboxServicePorts: servicePorts.Ports,
		})
		sandboxes = granuleSandboxes
	} else if cfg.ContainerSandboxes {
		sandboxes = orchestrator.NewContainerSandboxes(orchestrator.ContainerConfig{
			Image: startImage, Workspace: "/home/agent",
			DefaultCPUs: cfg.SandboxCPUs, DefaultMemoryMB: cfg.SandboxMemoryMB,
			DefaultDiskGB: cfg.SandboxDiskGB, DefaultNetworkAccess: cfg.networkAccess,
			Network: cfg.ContainerNetwork, CapAdd: cfg.ContainerCapAdd,
			// The same copy orchestrator.Config.GrainSourceDir is given
			// below, mounted into self-debug runs' containers.
			GrainSourceDir: sourceDir(cfg.UpgradeSrcDir),
		})
		log.Printf("grain daemon: container sandboxing -- one local Docker container per run from image %q", startImage)
	} else if cfg.K8sSandboxes {
		k8sNamespace := firstNonEmpty(cfg.K8sNamespace, model.DefaultK8sNamespace)
		k8sInstance := k8sInstanceLabel(ctx, store)
		k8sKubeconfig := EffectiveK8sKubeconfig(cfg)
		k8sImage := startImage
		k8sWorkspace := EffectiveK8sWorkspace(cfg)
		cpuOvercommit, memOvercommit := cfg.overcommitRatios()
		sandboxes = orchestrator.NewK8sSandboxes(orchestrator.K8sConfig{
			Instance:       k8sInstance,
			Namespace:      k8sNamespace,
			Kubectl:        cfg.K8sKubectl,
			Kubeconfig:     k8sKubeconfig,
			Image:          k8sImage,
			ServiceAccount: cfg.K8sServiceAccount,
			Workspace:      k8sWorkspace,
			NodeSelector:   ParseKeyValues(cfg.K8sNodeSelector),
			Privileged:     cfg.K8sPrivileged,
			// Which pods the scheduler may preempt for which, on a full
			// cluster (deploy/kubernetes/'s PriorityClasses).
			PriorityClasses: cfg.K8sPriorityClasses,
			// The same deployment-wide seeds the granule backend takes,
			// applied to a pod's resource requests instead of to
			// `granule vm create` flags -- and replaced while the
			// daemon runs by the same liveConfig call, since
			// K8sSandboxes implements SetDefaultShape too.
			DefaultCPUs:           cfg.SandboxCPUs,
			DefaultMemoryMB:       cfg.SandboxMemoryMB,
			DefaultDiskGB:         cfg.SandboxDiskGB,
			CPUOvercommit:         cpuOvercommit,
			MemoryOvercommit:      memOvercommit,
			DiskOvercommit:        cfg.DiskOvercommit,
			DefaultNetworkAccess:  cfg.networkAccess,
			NetworkPolicyEnforced: cfg.K8sNetworkPolicy,
		})
		log.Printf("grain daemon: Kubernetes sandboxing -- one pod per run in namespace %q from image %q. "+
			"A pod shares its node's kernel with everything else scheduled there, so this is weaker isolation "+
			"than -granule-sandboxes; it is the cluster's own primitives doing the work, not a hypervisor.",
			k8sNamespace, k8sImage)
	} else if cfg.K8sGranuleSandboxes {
		k8sNamespace := firstNonEmpty(cfg.K8sNamespace, model.DefaultK8sNamespace)
		k8sInstance := k8sInstanceLabel(ctx, store)
		k8sKubeconfig := EffectiveK8sKubeconfig(cfg)
		k8sGranuleImage := startImage
		k8sWorkspace := EffectiveK8sWorkspace(cfg)
		var forwardedPortsFunc func() []int
		if EffectiveK8sGitProxyHost(cfg) == "127.0.0.1" {
			forwardedPortsFunc = servicePorts.EndpointPorts
		}
		cpuOvercommit, memOvercommit := cfg.overcommitRatios()
		sandboxes = orchestrator.NewK8sGranuleSandboxes(orchestrator.K8sGranuleConfig{
			Instance:   k8sInstance,
			Namespace:  k8sNamespace,
			Kubectl:    cfg.K8sKubectl,
			Kubeconfig: k8sKubeconfig,
			RelayAddr:  cfg.K8sGranuleRelayAddr,
			// Per-sandbox access specs carry each pod's bearer token:
			// keep them under grain's own data directory rather than the
			// shared os.TempDir default.
			AccessDir:          k8sGranuleAccessDir(cfg.DataDir),
			Image:              k8sGranuleImage,
			ServiceAccount:     cfg.K8sServiceAccount,
			Workspace:          k8sWorkspace,
			NodeSelector:       ParseKeyValues(cfg.K8sNodeSelector),
			PriorityClasses:    cfg.K8sPriorityClasses,
			ForwardedPortsFunc: forwardedPortsFunc,
			// The same deployment-wide seeds every other sized backend
			// takes, applied to CHV_CPUS/CHV_MEMORY_MB rather than to a
			// plain pod's resource requests or `granule vm create`
			// flags.
			DefaultCPUs:          cfg.SandboxCPUs,
			DefaultMemoryMB:      cfg.SandboxMemoryMB,
			DefaultDiskGB:        cfg.SandboxDiskGB,
			CPUOvercommit:        cpuOvercommit,
			MemoryOvercommit:     memOvercommit,
			DiskOvercommit:       cfg.DiskOvercommit,
			DefaultNetworkAccess: cfg.networkAccess,
		})
		log.Printf("grain daemon: Kubernetes granule sandboxing -- one privileged pod per run in namespace %q "+
			"from image %q, each booting a real cloud-hypervisor VM over /dev/kvm. This is the same isolation "+
			"-granule-sandboxes gives on a single host, scheduled by the cluster instead.",
			k8sNamespace, k8sGranuleImage)
	} else if cfg.HostSandboxes {
		runs := HostSandboxRunsDir(cfg)
		if err := os.MkdirAll(runs, 0o755); err != nil {
			return fmt.Errorf("creating %s for host sandboxes: %w", runs, err)
		}
		env, unset, err := ResolveHostSandboxEnv(cfg.HostSandboxEnv, os.LookupEnv)
		if err != nil {
			return err
		}
		hide, err := hostSandboxHiddenPaths(ctx, cfg)
		if err != nil {
			return err
		}
		sandboxes = orchestrator.NewHostSandboxes(runs).WithEnv(env).WithHidden(hide)
		log.Printf("grain daemon: host sandboxing -- one plain directory per run under %s, on this host, with NO "+
			"isolation: every run's commands execute as this daemon's own user, with this host's own network, "+
			"tools and filesystem. HOME is pinned to the run's directory and only an allowlist of environment "+
			"variables is passed through, which keeps this daemon's own secrets out of a run's environment but "+
			"is not a security boundary.", runs)
		if len(env) > 0 {
			// Names only: a value can be a proxy URL with a password in it.
			names := make([]string, 0, len(env))
			for _, kv := range env {
				name, _, _ := strings.Cut(kv, "=")
				names = append(names, name)
			}
			log.Printf("grain daemon: -host-sandbox-env passes %s into every host-sandbox run on top of that "+
				"allowlist: those runs can act with whatever identity these carry (an SSH agent, a gcloud "+
				"login, a proxy)", strings.Join(names, ", "))
		}
		if len(unset) > 0 {
			log.Printf("grain daemon: -host-sandbox-env names %s, which this daemon's own environment does not "+
				"set; runs will not get them", strings.Join(unset, ", "))
		}
	} else if LocalSandboxes != nil {
		if sandboxes, err = LocalSandboxes(cfg.SandboxDir); err != nil {
			return err
		}
	} else {
		// checkConfig refuses this before run() is reached; said again
		// here so a caller that skipped it cannot end up with no backend.
		return errors.New("no sandbox backend selected: pass -container-sandboxes, -granule-sandboxes, -k8s-sandboxes, -k8s-granule-sandboxes, -host-sandboxes, or -disable-native-sandboxes")
	}
	// How much machine there is to commit, read once here: everything
	// below that rations a sandbox -- dispatch's own limits and CI's gate
	// alike -- is held against this reading as well as against its count
	// (config.budget, grain/task-173).
	cfg = cfg.withHostCapacity(sandboxes)
	if cfg.Ledger != nil && cfg.Ledger.Wake == nil {
		cfg.Ledger.Wake = wakeCycle
	}
	LogSandboxBudget(cfg)

	// This deployment's GitHub credential ladder, read once here and
	// carried on live below so that everything downstream of it -- the
	// picker the UI offers, the providers the reconcile loop resolves
	// grants against, the Settings pane that writes a token, and the
	// check that tests one -- is built from one reading of the
	// credential directory rather than several that could disagree if a
	// file appeared between them.
	githubCredentials, err := GitHubCredentialLadder(cfg.DataDir)
	if err != nil {
		return err
	}
	// The credential material is named by the deployment's git
	// connections, which are what every caller resolving a repository's
	// credential through this set asks (docs/single-tenant-reset.md).
	githubCredentials.SetConnections(storeGitConnections(ctx, store))

	// One liveConfig, shared by the two halves of this process: the
	// reconcile loop refreshes it once per tick and applies what changed
	// (its own doc comment), and the UI server reads it back to report
	// what this deployment is actually running with, as opposed to what
	// is merely stored. Built here because it needs both the config
	// loadConfig just resolved and the sandbox backend above.
	// The auto overcommit controller (model.OvercommitController): one for
	// the life of the process, carried on every Config the live one
	// derives, and stepped by runOvercommit -- only where this machine is
	// the one the sandboxes are built on, since anywhere else its
	// pressure says nothing about them.
	if localHostSandboxes(sandboxes) {
		cfg.Overcommit = model.NewOvercommitController(cfg.SandboxOvercommit, cfg.CpuOvercommit, cfg.MemoryOvercommit)
	}
	live := NewLiveConfig(store, sandboxes, cfg, githubCredentials).
		WithSandboxImages(sandboxImages, stockSandboxImage, sandboxImageLabels()).
		WithCapabilityServers(startCapabilityServers(ctx, store, secretStore, cfg.DataDir, cfg.capabilityHostd(), gcpKeyPool(cfg)))
	if cfg.Overcommit != nil {
		go runOvercommit(ctx, live, cfg.Overcommit)
	}

	// transcriptDir is where a run's own agent.Framework may mirror its
	// transcript-in-progress live, and where the UI server below reads one
	// back from for a still-running attempt -- the two sides of
	// bwsalmon/agents#467's live tailing (agent/gemini's own share of it
	// added by bwsalmon/agents#513), agreeing on this
	// directory (and, within it, the run-ID-named file
	// orchestrator.RunDispatch and each framework's LiveTranscriptDir
	// independently compute) is what lets them talk to each other without
	// either package importing the other. It must exist before any run
	// can write into it.
	//
	// 0700, and chmodded too so a directory an older grain made 0755 is
	// tightened: each file in it is a run's raw tool output, and nothing
	// outside this process reads them (the frameworks open them 0600).
	transcriptDir := filepath.Join(cfg.DataDir, "state", "transcripts")
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		return fmt.Errorf("creating transcript directory: %w", err)
	}
	if err := os.Chmod(transcriptDir, 0o700); err != nil {
		return fmt.Errorf("restricting transcript directory: %w", err)
	}

	// The package cache (pkg/cli/packagecache.go), started here rather
	// than beside the git proxy down in runDaemon because both halves of
	// this process want it: the reconcile loop points each new sandbox at
	// it, and the UI server -- which starts first, deliberately -- reports
	// what it holds and offers the button that empties it.
	//
	// A cache that will not start is logged and left nil, which every
	// caller reads as "this deployment has no package cache": sandboxes
	// are pointed at nothing and download their packages from the
	// internet, exactly as they did before this existed. It is not a
	// reason to refuse to run.
	// One fabric for every service a sandbox has to reach, built here so
	// that all of them answer the same question about where this
	// deployment's sandboxes are -- see sandboxnet.go, and the bug that
	// file's header describes, which was four services answering it
	// separately and one of them answering it wrong.
	sandboxFabric, err := NewDirectWithPortBlock(SandboxAdvertiseHost(cfg), cfg.SandboxPortBase)
	if err != nil {
		return fmt.Errorf("sandbox port block: %w", err)
	}
	if err := AdvertiseViaK8sService(ctx, cfg, sandboxFabric, net.DefaultResolver.LookupHost); err != nil {
		return err
	}
	servicePorts.Serve(sandboxFabric)
	// Said once, at startup, because it is the one fact an operator needs
	// to let anything reach these services: a firewall rule, a security
	// group, a VPN ACL. Adding a seventh service does not change it.
	if first, last := sandboxFabric.PortBlock(); first != 0 {
		log.Printf("grain daemon: every sandbox-facing service binds a reserved port in %d-%d -- "+
			"whatever has to reach them needs that one range (the package cache's interception "+
			"listeners and resolver are outside it, on 443, 80 and 53)", first, last)
	} else {
		log.Print("grain daemon: -sandbox-port-base=0: every sandbox-facing service takes an ephemeral " +
			"port, renumbered on each restart -- a sandbox that outlives a restart holds addresses that have moved")
	}

	pkgCache, stopPackageCache, err := StartPackageCache(cfg, sandboxFabric, restrictedSandboxes)
	pkgCache.SetGitConnections(storeGitConnections(ctx, store))
	// Safe to assign whether or not startPackageCache succeeded:
	// sandboxDNS answers "" for a nil *packageCache exactly as it does
	// for one whose resolver is not serving, so a granule VM's -dns is
	// left off in both cases (GranuleConfig.DNS's own doc comment).
	packageCacheDNS = pkgCache.SandboxDNS(live)
	if err != nil {
		log.Printf("grain daemon: not running a package cache: %v -- sandboxes will download their "+
			"packages, and their Docker Hub images, from the internet", err)
	} else {
		live.withPackageCache(pkgCache)
		servicePorts.WithPackageCache(pkgCache)
		log.Printf("grain daemon: the package cache is serving at %s, holding at most %d GiB under %s",
			pkgCache.Url, cfg.PackageCacheGB, pkgCache.dir)
	}
	defer stopWithGrace(stopPackageCache)
	// A Kubernetes sandbox pod gets its cache configuration under /etc
	// when it is built, since nothing can write it there afterwards --
	// so it asks the same two questions each dispatch does, per pod.
	if k8sSandboxes, ok := sandboxes.(*orchestrator.K8sSandboxes); ok {
		k8sSandboxes.SetPackageCache(pkgCache.sandboxURL(live), pkgCache.sandboxCA(live))
	}

	// The Actions cache (pkg/cli/actionscache.go): what a CI job
	// exports with `cache-from: type=gha`, kept on this machine instead
	// of inside the sandbox that is destroyed when the job ends.
	//
	// Nil on a deployment that asked for none, and nil again where it
	// could not be opened -- in both cases a job's act keeps its own
	// cache server and CI is exactly as cold as it was before.
	actionsCache, stopActionsCache, err := startActionsCache(cfg, sandboxFabric)
	if err != nil {
		log.Printf("grain daemon: not keeping an Actions cache: %v -- every CI job will build from cold", err)
	} else if actionsCache != nil {
		live.withActionsCache(actionsCache)
		stats := actionsCache.cache.Stats()
		// Two facts, and the second is the one worth reading: the cache
		// can be full and serving nobody, which is what a deployment
		// that has turned the switch off looks like from here
		// (model.Config.ActionsCacheEnabled). A line saying only "the
		// Actions cache is serving at ..." would read as CI being warm
		// when it is not.
		used := "every CI job is handed it, and every branch of a repository shares one"
		if !cfg.ActionsCacheEnabled {
			used = "no CI job is handed it -- the switch is off in Settings -> Sandbox, and what is " +
				"stored below stays there until somebody flushes it"
		}
		// The bound the cache resolved for itself rather than the stored
		// number, which is 0 on a deployment that has chosen nothing and
		// would print as "at most 0 GiB" (actionscache.DefaultMaxBytes).
		log.Printf("grain daemon: the Actions cache is serving at %s, holding at most %d GiB under %s "+
			"(%d entries, %d MiB, carried over from before this restart): %s",
			actionsCache.Url, stats.MaxBytes>>30, actionsCache.cache.Dir(), stats.Entries, stats.Bytes>>20, used)
	}
	defer stopWithGrace(stopActionsCache)

	// The other proxies a sandbox can see, on the same fabric as the git
	// proxy: a run holding the claude-proxy, openai-proxy or gemini-proxy
	// capability calls that vendor through one of these rather than
	// holding this deployment's own credential for it
	// (pkg/cli/modelproxy.go, pkg/modelproxy).
	//
	// Started here, beside the two caches, rather than in runDaemon after
	// the git proxy. A taken port fails only that one service -- the
	// promise the sandbox port block makes -- and the git proxy is no
	// exception: it used to be, because its failure returned out of
	// runDaemon before this call was ever reached, and the deferred stop
	// would have torn these down with it if it had been. The git proxy
	// still halts dispatch on its own (every dispatch needs it); it no
	// longer takes three unrelated listeners with it.
	//
	// A failure to bind is logged rather than fatal: nothing dispatched
	// needs these to start, and the capabilities that do refuse cleanly
	// for as long as an endpoint has no URL to hand anybody.
	stopModelProxies, err := StartModelProxies(cfg, sandboxFabric, ModelProxies, secretStore, store)
	for _, vendor := range modelproxy.Vendors() {
		if ep := ModelProxies[vendor.Name]; ep != nil && ep.URL() != "" {
			cfg.restartGaps.Up(modelProxyGapService(vendor))
		}
	}
	defer func() {
		for _, vendor := range modelproxy.Vendors() {
			if ep := ModelProxies[vendor.Name]; ep != nil && ep.URL() != "" {
				cfg.restartGaps.Down(modelProxyGapService(vendor))
			}
		}
		stopWithGrace(stopModelProxies)
	}()
	if err != nil {
		log.Printf("grain daemon: not serving every model proxy: %v -- a task granted the capability "+
			"for one that is missing will be refused", err)
	}
	for _, vendor := range modelproxy.Vendors() {
		if url := ModelProxies[vendor.Name].URL(); url != "" {
			log.Printf("grain daemon: the %s proxy is serving at %s", vendor.Name, url)
		}
	}

	// One Upgrader, shared by the two halves of this process, for the
	// reason liveConfig above is shared: both of them can now start an
	// upgrade -- the UI when somebody presses the button, and the
	// reconcile loop when a scheduled one finally comes due
	// (orchestrator's reconcileScheduledUpgrade) -- and "one upgrade at
	// a time" is a claim an Upgrader makes about itself, in its own
	// memory (upgrade.ErrUpgradeInProgress). Two of them would each be
	// telling the truth about an upgrade the other was also running.
	//
	// nil on a deployment configured for neither pipeline, which both
	// halves already have to handle: the UI hides the pane and the
	// reconciler applies nothing.
	upgrader := newUpgrader(cfg, db, store)

	// Which filesystems this deployment's own state sits on, resolved once
	// for the two halves that read them: the host status pane's disk rows
	// (hostStats, through startUIServer below) and the free-space floor
	// that holds work back on the tightest of them (newDiskWatch, inside
	// runDaemon). Once rather than twice because resolving it can cost a
	// `docker info`, and because a floor held against volumes the pane
	// does not draw is a deployment gone quiet for a reason that is not on
	// the screen -- see config.volumes.
	//
	// And not at all where neither half will read it, since that
	// subprocess is the whole cost of this line: a deployment serving no
	// UI and holding no floor has nobody to tell which volumes it writes
	// to.
	if cfg.UiAddr != "" || cfg.DiskFloor().Known() {
		cfg.Volumes = hostDisks(cfg)
	}

	execSrv, execAddr, stopExecutor, err := startExecutorServer(cfg, store)
	if err != nil {
		return fmt.Errorf("starting remote executor listener: %w", err)
	}
	defer stopWithGrace(stopExecutor)
	cfg.executorServer = execSrv
	cfg.executorListenAddr = execAddr
	if live != nil {
		live.SetExecutorServer(execSrv)
	}

	// The UI/API server starts here, as early as its own dependencies (the
	// store, sandboxes, and transcriptDir above) allow -- deliberately
	// before the git proxy, the Gemini agent
	// framework, and RunCycle's own reconcile loop, none of which the UI
	// needs a working copy of to serve the store's existing tasks, runs,
	// and logs (bwsalmon/agents#550: "make sure ui with logs stays up even
	// if the daemon has failed"). Everything from here down used to run
	// inline in this same function, so a `return err` from any one of
	// those steps -- a bad -gemini-api-key-file, a git proxy that failed
	// to bind -- or an unrecovered panic anywhere beneath them (a single
	// dispatch's own goroutine included -- see reconcileDispatch's doc
	// comment in pkg/orchestrator/cycle.go) unwound run() itself, which
	// tore this UI server down right along with it via the very defer
	// below. It now runs in runDaemon, in its own goroutine, so that no
	// longer happens: runDaemon's own failure is logged, not fatal, and
	// run() itself only returns once ctx is actually cancelled.
	if cfg.UiAddr != "" {
		stopUI, err := startUIServer(cfg, store, db, transcriptDir, sandboxes, live, pkgCache, actionsCache,
			stateManager, stateManager.settingsRepo, upgrader)
		if err != nil {
			return fmt.Errorf("starting the UI/API server: %w", err)
		}
		cfg.restartGaps.Up(upgrade.ServiceAPI)
		defer func() {
			cfg.restartGaps.Down(upgrade.ServiceAPI)
			stopWithGrace(stopUI)
		}()

		done := make(chan struct{})
		go func() {
			defer close(done)
			if err := runDaemon(ctx, cfg, store, sandboxes, transcriptDir, live, sandboxFabric, pkgCache, actionsCache, forbidden,
				stateRepoHooksFor(stateManager), upgrader, secretStore); err != nil {
				// reconcilerDown is what turns this log line into
				// something GET /api/config (and, through it, the UI
				// itself) can also see -- bwsalmon/agents#576: before
				// this, a runDaemon failure here was visible only to
				// whoever happened to be reading this process's log,
				// while the UI it left running kept looking perfectly
				// healthy.
				ReconcilerDown.Store(true)
				log.Printf("grain daemon: %v -- the UI/API server above is still up, but nothing is "+
					"dispatching or reconciling tasks until this is fixed and the process is restarted", err)
			}
		}()
		select {
		case <-ctx.Done():
		case <-done:
			// runDaemon gave up (or panicked) before ctx was ever
			// cancelled -- already logged above. The UI stays up
			// regardless: wait for a real shutdown signal instead of
			// falling through to the deferred stopUI/db.Close below, which
			// would tear it down right after bringing it up for exactly
			// this reason.
			<-ctx.Done()
		}
		<-done
		return nil
	}

	return runDaemon(ctx, cfg, store, sandboxes, transcriptDir, live, sandboxFabric, pkgCache, actionsCache, forbidden,
		stateRepoHooksFor(stateManager), upgrader, secretStore)
}

// orphanReaper is the startup sweep both sandbox backends implement:
// destroy whatever sandboxes a previous process of this deployment left
// behind (orchestrator.GranuleSandboxes.ReapOrphans deletes VMs,
// orchestrator.HostSandboxes.ReapOrphans removes directories) and say
// how many. Declared here, where it is consumed, rather than in
// pkg/orchestrator: nothing in the orchestrator itself calls it -- a
// sweep is only ever correct at startup, before any run is live -- so it
// is this binary's requirement of a backend, not part of the Sandboxes
// contract RunCycle works through.
type orphanReaper interface {
	ReapOrphans(ctx context.Context) (int, error)
}

// runDaemon is everything that makes cfg's deployment actually dispatch
// and reconcile tasks: the git proxy, the per-dispatch agent framework
// factory (agentFrameworks), orphaned-run and orphaned-sandbox recovery, and
// RunCycle's own reconcile loop. Sandbox tokens and git credentials are
// not among them any more:
// both are per run now, minted and configured as each run's sandbox is
// built (orchestrator's runOne). run() calls it exactly once, either
// inline (-ui-addr disabled, so there is no UI server worth
// keeping up on its own -- a setup failure here is still this process's
// only job, so it is still fatal the way it always was) or in a goroutine
// recovered from panics (-ui-addr set), so that nothing in here -- a
// config problem this returns as an error, or a bug that panics -- can
// take the UI server run() already started down with it
// (bwsalmon/agents#550). It returns once ctx is cancelled, the same as
// reconcile itself does; a non-nil error means it never got that far.
//
// stateHooks is what this loop needs from the state manager, passed as
// the functions they are rather than as values read here: run() opens
// the state repository long before this, but the UI can point the
// installation at a different one while this loop runs, and every run
// dispatched after that has to be told about the repository grain reads
// now. They travel no further than orchestrator.Config's own StateRepo,
// CheckImage, StateRepoCheck and StateRepoMerged, which is where what
// each of them means is decided.
func runDaemon(ctx context.Context, cfg Config, store *model.Store, sandboxes orchestrator.Sandboxes, transcriptDir string, live *LiveConfig, fabric Fabric, pkgCache *PackageCache, actionsCache *ActionsCache, forbidden *gitproxy.ForbiddenSet, stateHooks stateRepoHooks, upgrader *upgrade.Upgrader, secretStore *secrets.Store) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		}
	}()

	// The git proxy is started before anything can dispatch onto it, but
	// nothing is minted or configured here any more. A sandbox token used
	// to be minted per slot, right here, specifically because the proxy
	// read sandbox-tokens.json once at startup and never again -- so a
	// token minted after it started was one it could never recognise, and
	// every push through it failed closed with a 401 (cmd/graind/
	// live_test.go's TestRunLiveDispatchesAndOpensAPullRequest caught
	// exactly that, live). With a sandbox per run there is no set of
	// sandboxes to mint for ahead of time: each run mints its own as it
	// prepares its sandbox (orchestrator's runOne), and the proxy re-reads
	// the file when shown a token it does not know (gitproxy.
	// SandboxTokens' own doc comment), which is what makes that safe.
	tokens := gitproxy.NewSandboxTokenStore(filepath.Join(cfg.DataDir, "secrets", "sandbox-tokens.json"))

	// What this deployment's own disk has left, for the two places that
	// decide whether more work may start: CI's gate below and the
	// reconcile loop's dispatch (model.DiskFloor, grain/task-196). One
	// watcher, so both are held by the same reading of the same volume
	// and say the same thing about it in the journal.
	// Asking liveConfig for the floor rather than handing it this
	// process's own: the floor is a stored setting, re-read each
	// reconcile tick, and the whole point of storing it is that an
	// operator can lower it while a full volume is holding every
	// dispatch (grain/task-372).
	disks := NewDiskWatch(cfg, live.diskFloor)
	LogDiskFloor(disks)
	// And what it does about a volume it finds under that floor, which
	// is what makes the floor worth having rather than merely honest
	// (diskReclaim, grain/task-213). Wired here rather than inside
	// newDiskWatch because the steps need the sandbox backend, and read
	// through liveCI because the CI service is not built until the first
	// reconcile tick below.
	ReclaimWith(disks, ctx, NewDiskReclaim(sandboxes, &DockerPruner{
		ActiveSandboxImage: chooseSandboxImage(cfg).image,
		ActiveGrainImage:   DefaultGrainImage,
	}))

	proxyURL, proxyHandler, stopProxy, err := startGitProxy(cfg.DataDir, store, cfg.GithubHost, cfg.GithubInsecureHTTP, fabric, forbidden, cfg.shutdownStarted)
	if err != nil {
		// Fatal to this loop, and only to it: every dispatch and every CI
		// job clones through this proxy, so there is nothing to reconcile
		// without it. The UI and every other sandbox-facing service --
		// both caches and the model proxies -- were started by run()
		// before this, and stay up (TestAGitProxyPortTakenFailsOnlyTheGitProxy).
		return fmt.Errorf("starting git proxy: %w", err)
	}
	cfg.restartGaps.Up(upgrade.ServiceGitProxy)
	defer func() {
		cfg.restartGaps.Down(upgrade.ServiceGitProxy)
		stopWithGrace(stopProxy)
	}()
	cfg.gitProxyHandler = proxyHandler

	// The relay a capability grant's ports reach a sandbox through
	// (docs/capabilities-spec.md §5.3), on the same fabric as the git
	// proxy, so a sandbox with no network reaches it as it reaches that.
	// Not fatal: without it, a run whose grants return ports is refused
	// at materialization, and every other run is unaffected.
	capabilityPorts := portforward.NewRelay()
	if hd := cfg.capabilityHostd(); hd != nil {
		capabilityPorts.InodeOwner = hd.SocketOwner
	}
	capabilityPortsURL := ""
	if endpoint, stopRelay, err := fabric.Serve(CapabilityPortsService, capabilityPorts); err != nil {
		log.Printf("grain daemon: starting the capability port relay: %v -- runs whose capability grants forward ports will be refused", err)
	} else {
		capabilityPortsURL = endpoint.URL
		defer stopWithGrace(stopRelay)
	}

	// The listener a CI job holding api grants (release-gate-read among
	// them) calls grain's API through (pkg/capability/ciapi), on the same
	// fabric. Not fatal: without it those grants are refused at resolve,
	// naming this, and nothing else is affected.
	if endpoint, stopGate, err := fabric.Serve("ci api", CIAPI); err != nil {
		log.Printf("grain daemon: starting the CI API listener: %v -- CI jobs that declare api grants "+
			"(%s among them) will be refused", err, model.ReleaseGateReadCapability)
	} else {
		CIAPI.SetURL(endpoint.URL)
		defer stopWithGrace(stopGate)
	}

	// Built per dispatch now rather than here (agentFrameworks' own doc
	// comment), so nothing about a missing or not-yet-pasted agent
	// credential can stop this loop from starting: a task needing one
	// fails as its own setup-failed run instead, which is a state the UI
	// shows and an operator can fix in Settings without a restart. What
	// is worth saying at startup is that the default framework cannot
	// run yet, since otherwise the first symptom is a task that fails
	// the moment anyone files one.
	pool := NewAgentPool(cfg, store, secretStore)
	// The pool passes over a connection whose credential is paused after
	// a usage limit, and rotates its team's work onto the rest
	// (AgentPool.Pause).
	pool.Pause = agentPause
	// Before the first dispatch: the pauses the last daemon had in force
	// are read back, so a restart inside a provider's window does not
	// dispatch onto a credential already found spent (Pause.Restore).
	if store != nil {
		restoreCtx, cancelRestore := context.WithTimeout(ctx, 30*time.Second)
		if n, err := agentPause.Restore(restoreCtx, store, time.Now()); err != nil {
			log.Printf("grain daemon: %v -- usage-limit pauses from before this restart are not restored", err)
		} else if n > 0 {
			log.Printf("grain daemon: restored %d usage-limit pause(s) from before this restart", n)
		}
		cancelRestore()
	}
	buildFramework := pool.Build
	// Bounded rather than given ctx itself: this probe exists to log a
	// sentence at startup rather than to hold the loop up, and the
	// dispatch that needs a framework does the real work of building
	// one.
	probeCtx, cancelProbe := context.WithTimeout(ctx, 5*time.Second)
	if probe, err := buildFramework(probeCtx, orchestrator.AgentChoice{}); err != nil {
		log.Printf("grain daemon: the default agent framework is not ready: %v", err)
	} else if held, ok := probe.(*BackendFramework); ok {
		// The probe never runs, so it must not keep a connection slot.
		held.Lease.Release()
	}
	cancelProbe()

	credentials, err := gitproxy.LoadCredentialSet(filepath.Join(cfg.DataDir, "secrets", "github"))
	if err != nil {
		return fmt.Errorf("loading GitHub credential ladder: %w", err)
	}
	// The one REST client this deployment posts with: every pull request
	// it opens, every review and comment it leaves, and every read the
	// reconcile loop makes go out over it, each authenticated as the
	// owning team's own credential, or the credential ladder's per-repo
	// answer for a repository no team with a credential owns -- and each
	// sent to the forge of the repository's own host (forgeRouter), so a
	// repository on a GitHub Enterprise host behind a host tool is asked
	// there, through that tool, rather than on -github-host.
	githubRESTClient, githubTransport, _ := newForgeClient(cfg, store, credentials)
	var githubClient github.Client = githubRESTClient
	if stateHooks.PullRequests != nil {
		githubClient = stateHooks.PullRequests(githubClient, store)
	}

	// The reporting service this deployment reads verdicts from, if it
	// has one (pkg/checkpost, and docs/leaving-the-github-api.md).
	//
	// A failure to configure it is fatal rather than a warning that
	// leaves the deployment quietly polling: an operator who set these
	// flags meant to stop asking GitHub, and finding out from a rate
	// limit weeks later is how the last one went. Not configuring it at
	// all is not a failure -- that is every deployment without a service,
	// and the client stays nil.
	verdicts, err := checkpostClient(cfg)
	if err != nil {
		return err
	}
	if verdicts != nil {
		log.Printf("grain daemon: reading check verdicts from %s for %d repositories; GitHub is "+
			"still asked for anything that has not been reported in full",
			cfg.CheckpostURL, len(verdicts.Expect))
	}

	webhookConsumer, err := newGitHubWebhookConsumer(cfg, credentials)
	if err != nil {
		return err
	}
	if webhookConsumer != nil {
		if err := webhookConsumer.Start(ctx, wakeCycle, func(repo string) {
			if githubTransport != nil && repo != "" {
				githubTransport.InvalidatePrefix("/repos/" + repo)
			}
		}); err != nil {
			return err
		}
		if cfg.GithubWebhookURL != "" {
			log.Printf("grain daemon: long-polling verified GitHub App webhook events from %s", cfg.GithubWebhookURL)
		} else {
			log.Printf("grain daemon: listening for GitHub App webhooks on %s", cfg.GithubWebhookAddr)
		}
	}

	// Published for the UI/API server started above this one (see
	// livePullRequests): from here on, a dispatched run asking for its own
	// pull request through POST /api/tasks/{id}/pull-request reaches this
	// same client, and so opens exactly the pull request this loop's own
	// finish path would have opened for it.
	LivePullRequests.Store(&PullRequestOpener{Store: store, Client: githubClient})

	// The same publication for the same reason, one question narrower:
	// AddTargetRepo asks GitHub how a repository spells its own name
	// before writing it into the allowlist (ui.Config.RepoNames), and
	// that question needs the REST client this function owns.
	liveRepoNames.Store(githubRESTClient)

	// And the transport underneath it, for GET /api/metrics' own "github"
	// section: how many calls this daemon has made, how many of them were
	// free, and how much of the hour's budget GitHub last said was left
	// (github.RealTransport.Stats). Published the same way and for the
	// same reason as the two above -- the UI server is already up by the
	// time this function builds the thing it would need.
	liveGitHubCalls.Store(githubTransport)

	// And grain's own CI, for the UI/API server started ahead of this
	// one to reach (ciGate): from here on, POST /api/ci/runs plans a run
	// and starts it in this deployment's own sandboxes. Behind -ci, so
	// an operator can turn the whole thing off without editing any
	// capability set; with it off, ui.Config.CI is nil and the API says
	// this deployment has no CI at all, which is the truth.
	//
	// After the proxy above, necessarily: every job's checkout is a
	// clone from it, and it is where the read-only scope a CI sandbox
	// holds is enforced (pkg/cli/ci.go).
	registry := newCapabilityRegistryWithStore(store, cfg.LocalScriptsConfig(), CapabilityProviders(cfg, store, ModelProxies, live.GitHubTokens(), live.gitHubCredentialSource())...)

	// Recovering any run a previous process left running (bwsalmon/agents#425)
	// has to happen here, once, before reconcile's first tick -- see
	// orchestrator.RecoverOrphanedRuns's own doc comment for why it is a
	// startup pass rather than something reconcile also runs on a timer.
	//
	// Run hosts go first (orchestrator.RecoverRunHosts): it finishes
	// the rows whose run host is gone with the listing's own reason and
	// frees the run hosts no row claims, and RecoverOrphanedRuns then
	// finishes whatever live rows are left.
	//
	// A matched run host whose run's sandbox this backend can claim back
	// is adopted: its sandbox is claimed before the reap below, so the
	// reap spares it, and the run is resumed once the orchestrator's
	// dependencies exist (orchestrator.ResumeAdoptedRun). One whose
	// sandbox cannot be claimed back is finished as interrupted, as every
	// match was before adoption existed.
	//
	// Host CLIs a previous daemon left running detached in grain hostd
	// go through the same pass beside them (orchestrator.RecoverHostd),
	// so an upgrade never re-dispatches a run hostd is still running.
	adopted, report, err := adoptAll(ctx, store, githubClient, cfg.RunHost, cfg.hostdRuns, sandboxes)
	if err != nil {
		log.Printf("grain daemon: %v", err)
	}
	// For the post-upgrade check: how many runs came through the
	// restart, and why any did not (orchestrator.Config.Adoption).
	cfg.runHostUpgrade.setReport(report)
	adoptedRuns := adopted.took
	if err := orchestrator.RecoverOrphanedRunsExcept(ctx, store, githubClient, time.Now().UTC(), adoptedRuns); err != nil {
		log.Printf("grain daemon: recovering orphaned runs: %v", err)
	}
	orchestrator.SyncSandboxLedger(ctx, orchestrator.Deps{
		Store:      store,
		Ledger:     cfg.Ledger,
		AgentShape: cfg.AgentShape(),
	})
	if cfg.Ci {
		if strings.TrimSpace(cfg.GritURL) != "" {
			gritRunner := BuildGritBackendRunner(ctx, cfg, live.ciRetention, store)
			LiveCI.Store(gritRunner)
			log.Printf("grain daemon: grain CI is wired to remote grit server at %s", cfg.GritURL)
		} else {
			ciProviders := func() *model.CapabilityRegistry {
				return newCapabilityRegistryWithStore(store, live.Current().LocalScriptsConfig(), CapabilityProviders(live.Current(), store, ModelProxies, live.GitHubTokens(), live.gitHubCredentialSource())...)
			}
			ciService := BuildCIService(ctx, cfg, LimitsWithDisk(live.limits, disks), live.ciRetention, live.ciStorageBounds, store, sandboxes, credentials, proxyURL, tokens, stateHooks.CheckImage, forbidden, ciProviders, secretStore, pkgCache.sandboxURL(live), pkgCache.sandboxCA(live), pkgCache.SandboxDNS(live), actionsCache.grantFor(live))
			ciService.SandboxImage = live.CurrentSandboxImage
			// A trusted release runs only from a branch the repository has
			// reviewed into, whoever asked for it (grain/task-949).
			ciService.ReleaseRefRefusal = CiReleaseRefRefusal(store, githubClient)
			// The attached capability servers a job's capability references
			// are minted through, the same ones a task's are.
			WithCICapabilityServers(ciService, func() orchestrator.CapabilityServers { return capabilityServerLookup(live) })
			// And the relay their ports and mcp_endpoints reach a job's
			// sandbox through, the same one a task's do.
			WithCICapabilityPorts(ciService, capabilityPorts, capabilityPortsURL)
			LiveCI.Store(ciService)
			defer ciService.Detach()
			log.Printf("grain daemon: grain CI is on for repos whose CI mode is \"Use Grain CI\" (Settings -> Repos); "+
				"attaching the %q capability alone does not turn it on. How many jobs run at once is "+
				"bounded by %s",
				grainci.CapabilityName, BudgetSentence(cfg.Limits(), KubernetesSandboxes(cfg)))
		}
	}
	// The tokens those runs held came back with the registry's file
	// (modelproxy.Tokens.PersistTo); the runs did not.
	revokeFinishedRunTokens(ctx, store, ModelProxies)

	// The other half of the same cleanup, and the far side of the hold an
	// upgrade takes over its own restart: the process that closed the
	// gate ended by exec'ing the restart that brought this one up, so it
	// could not open it again and nothing else ever would
	// (orchestrator.ReleaseUpgradeHold's own doc comment). Here for the
	// same reason as the line above -- both clean up after the process
	// that used to hold this data directory, and both have to happen
	// before the first cycle dispatches anything.
	if err := orchestrator.ReleaseUpgradeHold(ctx, store); err != nil {
		log.Printf("grain daemon: %v", err)
	}

	// The sandbox-side half of that same recovery, and at the same moment
	// for the same reason: a run's sandbox is destroyed when the run ends,
	// so at startup -- before this process has dispatched anything --
	// every sandbox this deployment owns belongs to a process that died
	// before it could do that. A granule VM left running costs memory; a
	// host directory left behind costs a whole checkout of disk, and
	// enough of them fill the filesystem the next run's own sandbox has
	// to be created on (orchestrator.HostSandboxes.ReapOrphans). Both
	// backends implement the sweep, so this reaps whichever one is
	// configured.
	//
	// Logged rather than fatal: what cannot be reaped costs resources,
	// where refusing to start costs the whole deployment, and every run
	// this process dispatches builds its own sandbox regardless.
	if reaper, ok := sandboxes.(orphanReaper); ok {
		if reaped, err := reaper.ReapOrphans(ctx); err != nil {
			log.Printf("grain daemon: reaping orphaned sandboxes: %v", err)
		} else if reaped > 0 {
			log.Printf("grain daemon: reaped %d sandbox(es) left behind by a previous process", reaped)
		}
	}

	// The capability-side half of that same recovery: a run's capability
	// resources (minted GCP service account keys, Gemini API keys, GitHub sandbox
	// repos) are revoked when the run finishes, so at startup -- before this
	// process has dispatched anything -- every external resource created before
	// this moment belongs to a run whose process died before it could revoke it.
	// Leaving them behind exhausts quotas (such as GCP's 10 user-managed keys per
	// service account) and blocks subsequent dispatches.
	//
	// Because startup recovery runs before any run is dispatched, every resource
	// created before now belongs to a dead process and can be reaped immediately
	// without waiting out a trailing grace period.
	//
	// Except what an adopted run still holds: with runs adopted, the
	// boundary is the one the periodic sweep uses, derived from the live
	// work -- which, at this moment, is the adopted runs and nothing else.
	reapBefore := time.Now().UTC()
	if len(adopted.took) > 0 || cfg.runHostUpgrade.hasAdoptedCI() {
		if b, err := ReapBoundary(ctx, store, reapBefore); err != nil {
			log.Printf("grain daemon: computing the capability reap boundary around adopted runs, so reaping none: %v", err)
			reapBefore = time.Time{}
		} else {
			reapBefore = b
		}
	}
	if !reapBefore.IsZero() {
		ReapCapabilitiesBefore(ctx, registry, secretStore, reapBefore)
	}

	// The tree a self-debug run reads, resolved once -- and checked once,
	// out loud, because the way it breaks is quiet. A staged copy whose
	// modes this account cannot use answers every read with a per-path
	// "permission denied" months after the install that caused it
	// (grain/task-160, grain/task-250); nothing watches those tool calls,
	// and the runs that make them are the ones least able to tell a broken
	// capability from a path they got wrong. One line in the journal at
	// startup is where an operator can actually see it.
	//
	// Logged rather than fatal: a deployment whose self-debug tools cannot
	// read anything is still a deployment that should dispatch work.
	grainSrcDir := sourceDir(cfg.UpgradeSrcDir)
	if err := selfdebug.CheckSourceReadable(grainSrcDir); err != nil {
		log.Printf("grain daemon: %v", err)
	}

	localPolicy := cfg.policy

	teamGitCredential, teamGitCredentialOn := TeamGitCredentialLookups(store)
	inFlight := &orchestrator.InFlight{}
	var policyAudit func(policy.AuditEntry)
	if cfg.audit != nil {
		policyAudit = cfg.audit.Write
	}
	reservePersistentPorts(cfg)
	defer persistentSandboxes.Close()
	deps := orchestrator.Deps{
		// The attached capability servers a task's capability
		// references are minted through at admission.
		CapabilityServers: capabilityServerLookup(live),
		Policy:            localPolicy,
		Store:             store, Client: githubClient, Sandboxes: sandboxes,
		// Sandbox reconciliation sweeps the run hosts no live run
		// claims through it every tick.
		RunHost:   cfg.RunHost,
		Framework: buildFramework,
		// Every refusal of the policy, written to the local audit log.
		PolicyAudit: policyAudit,
		// No GitReach: a single-tenant daemon's teams are labels, not
		// tenants, so the tasks this loop files itself are parked for
		// nothing a team's credential cannot reach.
		// A task waits, saying why, while no connection can serve its
		// model, rather than dispatching into a setup failure.
		ModelAvailability: pool.Availability,
		// The same client again, memoized: a verdict comment that names
		// the open pull request already fixing a shared fault asks what
		// every open change touches, and a deployment gone red on
		// several branches at once asks it several times within
		// minutes (pkg/cifault).
		CIFaultFiles: cifault.NewFileCache(githubClient),
		// The supervisor startUIServer's ui.Config.StandingSandboxes
		// reads, so GET /api/sandboxes reports the instances this loop
		// is actually running (standingSandboxes' own doc comment).
		StandingSandboxes: standingSandboxes,
		// Persistent sandboxes run from repository checkouts, started
		// here on every daemon start and kept running across exits
		// (persistentSandboxes' own doc comment).
		PersistentSandboxes: persistentSandboxes,
		Config: orchestrator.Config{
			// Every agent sandbox found OOM-killed is a decrease signal
			// for auto overcommit (orchestrator.applySandboxOOM).
			NoteOOM: overcommitNoteOOM(cfg.Overcommit),
			// The mechanical first attempt at a port, through the git
			// proxy started above (porter.go); nil files every port as
			// an agent task.
			Porter: NewPorter(cfg.DataDir, proxyURL, store, tokens, live.gitHubCredentials()),
			// nil on a deployment with no reporting service, which
			// leaves the merge queue reading checks exactly as it always
			// did (checkpostClient, and forge.Verdicts).
			Verdicts:      verdictSource(verdicts),
			Capabilities:  registry,
			Credentials:   secretStore,
			MaxAgentTurns: cfg.MaxAgentTurns,
			TranscriptDir: transcriptDir,
			// The same proxy URL the credential files above are written
			// for, now also the address each dispatched run's own
			// checkout is cloned from (orchestrator.prepareCheckout).
			// Nothing else ever told a sandbox where its repo lives.
			GitRemoteBase: proxyURL,
			// Where a capability grant's ports are forwarded through.
			CapabilityPorts:    capabilityPorts,
			CapabilityPortsURL: capabilityPortsURL,
			GrantTools:         grantTools(grainSrcDir, SecretsDir(cfg.DataDir)),
			// Where a sandbox reaches this deployment's package
			// cache -- asked per dispatch, and answering "" while the
			// setting is off, so the switch in Settings reaches the next
			// run rather than the next restart (pkg/cli/packagecache.go).
			PackageCache:    pkgCache.sandboxURL(live),
			PackageCacheCA:  pkgCache.sandboxCA(live),
			PackageCacheDNS: pkgCache.SandboxDNS(live),
			// The same checkout grantTools above builds its in-process
			// source tools over, told to the run itself this time: it
			// travels to the forked "grain mcpserver" a subprocess
			// Framework starts, which is where a self-debug run's tools
			// are actually served from (agent.RunConfig.GrainSourceDir).
			GrainSourceDir: grainSrcDir,
			// Where the forked "grain mcpserver" of a grain-log run
			// reads the credentials it redacts out of the journal
			// before answering with any of it. It is not material for
			// the run and never reaches a sandbox -- the process told
			// about it runs here, on the controller, where these files
			// already are (agent.RunConfig.GrainSecretsDir,
			// pkg/capability/grainlog).
			GrainSecretsDir: SecretsDir(cfg.DataDir),
			GrainStateDir:   filepath.Join(cfg.DataDir, "state-repo"),
			// Where a run granted local-scripts:<name> finds that
			// declaration (pkg/capability/localscripts).
			LocalScripts: cfg.LocalScriptsConfig(),
			// The image this deployment's sandboxes are really built
			// from, asked of chooseSandboxImage exactly as the sandbox
			// health pane asks (uiSandboxImage), so that the one
			// repository publishing that guest goes on having its guest
			// cache filled after a merge even with its releases switched
			// off (orchestrator.Config.SandboxGuestImage).
			SandboxGuestImage: sandboxGuestImage(cfg),
			// The same registry startUIServer above already handed the
			// UI, so a run that registers itself here is one
			// POST /api/tasks/{id}/sandbox/recreate can actually find.
			SandboxRecreations: sandboxRecreations,
			// The same manager startUIServer hands the UI below, which
			// is what lets somebody attach to a session this loop's own
			// console runs start (pkg/console).
			Consoles: consoleSessions,
			// The same registry again, for the other thing a run asks
			// the daemon for while it is still running: credit for the
			// time it is spending blocked on CI, which moves the very
			// timer that would otherwise cancel it mid-wait
			// (orchestrator.RunWaitCredits, RunWaitCreditCap).
			RunWaitCredits: runWaitCredits,
			// The registry of live runs that can be cancelled when a user
			// clicks Stop in the UI.
			TaskCancellers: taskCancellers,
			// What a base-branch repair the budget's reserve cannot hold
			// may stop to make room: grain CI on branches blocked by the
			// same red base first, then the newest agent run waiting on
			// it (orchestrator.preempter, grain/task-488).
			Preemptions:        orchestrator.NewPreemptions(),
			CapabilityRestarts: orchestrator.NewCapabilityRestarts(),
			CIPreempter:        ciPreempter{store: store, now: time.Now},
			// One gate for the whole deployment, scoped per framework once
			// it closes: the first run to meet its agent's own usage
			// limit cancels the other runs spending the same credential
			// and stops this loop dispatching that framework until the
			// provider's window resets (orchestrator.Pause). The same
			// object startUIServer above already handed the UI, so the
			// banner an operator sees -- and the lift button on it -- is
			// about the gate this loop actually consults (agentPause's own
			// doc comment).
			Pause: agentPause,
			// Which framework a task with no override of its own is
			// dispatched onto, so the gate above can tell whose credential
			// a run is spending. The same -agent-framework this
			// process's own framework factory starts from
			// (agentFrameworks), and re-read from grain_config every tick
			// by RunCycle exactly as that factory re-reads it per
			// dispatch.
			AgentFramework: cfg.AgentFramework,
			// Which repository this deployment's own settings live in,
			// so that a run dispatched into it is told it is changing
			// the grain that dispatched it, and every other run is told
			// where the settings it might want changed actually are.
			// Nothing in a sandbox can work either out: the git proxy
			// serves every repo from one address, and no file in a
			// checkout says which grain reads it.
			StateRepo: stateHooks.Repo,
			// And which image such a run is told to check its change
			// with -- the same one grain writes into that repository's
			// CI step, so that a green answer in the sandbox is the
			// green tick on the pull request. There is no grain binary
			// in a sandbox for the shorter command to have been
			// (orchestrator's stateRepoSection).
			CheckImage: stateHooks.CheckImage,
			// And, on a deployment that checks its own settings changes
			// rather than leaving it to Actions, the name of the check it
			// posts there -- which is what makes the merge queue refuse
			// to call a pull request against that repository clean
			// without one. Without this half, posting the check would be
			// a regression rather than a feature: a missing check reads
			// as a clean pull request, so a deployment that was down for
			// the life of a settings change would merge it unvalidated
			// (pkg/staterepo/postcheck.go).
			StateRepoCheck: stateHooks.Check,
			// And who to tell when one of that repository's pull requests
			// merges, so this deployment pulls the settings it has just
			// been given rather than going on exporting the ones it had
			// until the next tick.
			StateRepoMerged: stateHooks.Merged,
			// And what this deployment's last upgrade of itself did, so
			// that the cycle can file a task to go and check the
			// deployment that upgrade brought up
			// (orchestrator.reconcileUpgradeCheck). It reads the status
			// file pkg/upgrade wrote just before the restart that
			// replaced the process which wrote it -- nothing else could
			// carry that fact across, since the process that performed
			// the upgrade is not this one.
			UpgradeStatus: upgradeStatus(upgrader),
			// And how long that restart left grain unreachable, measured
			// by this process and the one it replaced (upgrade.GapRecorder).
			RestartGap: cfg.restartGaps.Last,
			// And what the adoption pass at this process's startup did
			// with the runs the previous one left in run hosts.
			Adoption: cfg.runHostUpgrade.adoption(),
			// And which CLIs this install's own run locations leave to
			// be lost on a restart, so the check does not call those
			// runs a regression (a cloudtop's claude in the container).
			UpgradeInterrupts: func(ctx context.Context) map[string]string {
				return cfg.upgradeInterruptNotes(ctx, store)
			},
			// And how an upgrade somebody scheduled for later actually
			// happens: the same Upgrader the UI's own button drives
			// (run() builds exactly one), so that a scheduled upgrade
			// coming due while an operator is already upgrading by hand
			// is refused rather than run twice.
			StartUpgrade: startUpgrade(upgrader),
			// And whether the daily automatic upgrade's channel has
			// anything new on it, asked of the registry or origin before
			// the queue is held for it.
			UpgradeUnchanged:   upgradeUnchanged(upgrader),
			UpgradeDrainWindow: cfg.UpgradeDrainWindow,
		},
		MintSandboxToken:   tokens.EnsureToken,
		RevokeSandboxToken: tokens.Revoke,
		// Kept fresh for as long as each run holds its sandbox, so a run
		// outliving the token's TTL keeps its git access; the TTL is
		// left as the backstop for a run whose daemon died under it.
		RefreshSandboxToken: tokens.RefreshToken,
		// A team's tasks wait for the team's own GitHub credential:
		// there is no deployment credential for them to fall back to
		// (grain/task-1592). Not on a single-tenant daemon, whose one
		// tenant's runs push with the daemon's own (SingleTenant below
		// switches this off in the orchestrator).
		RequireTeamGitCredential: true,
		// One backlog with no team scoping
		// (orchestrator.Deps.SingleTenant).
		SingleTenant: true,
		// A run commits as the account its git credential acts as --
		// a PAT's own GitHub login, an App's bot account, or for an
		// ambient connection this host's own login -- rather than as
		// `grain agent <grain-agent@localhost>`, which no GitHub
		// account owns (git_identity.go).
		CredentialGitIdentity: credentialGitIdentity(live.gitHubCredentials(), store),
		GitHubEvents:          githubEventSource(webhookConsumer),
		AppBotIdentity:        appBotIdentity(live.gitHubCredentials(), store),
		// And for a provider connection of the team's own: there is no
		// deployment-wide connection, framework or model behind a team's
		// (grain/task-1601), so a task in a team with none waits for one
		// rather than failing setup at dispatch (agentFrameworks).
		TeamHasAgentBackend: teamHasAgentBackend(cfg, store),
		MaxWorkers:          cfg.MaxWorkers,
		MaxMergers:          cfg.MaxMergers,
		Budget:              cfg.Budget(),
		AgentShape:          cfg.AgentShape(),
		Ledger:              cfg.Ledger,
		// And the one part of that budget a startup reading cannot give:
		// what the volume has left right now. Called once a cycle, so a
		// dispatch onto a filling disk is deferred on the tick the disk
		// fills rather than at the next restart (grain/task-196). nil
		// where this deployment has no floor configured.
		DiskSpace: DiskSpaceOrNil(disks),
		// A run outlives the cycle that started it (orchestrator.Deps.Runs):
		// without this the loop below could not tick again -- and so could
		// not dispatch into the rest of -max-workers, nor sync a single
		// pull request -- until every agent a cycle started had finished.
		Runs: inFlight,
		// The same ring startUIServer above already handed the UI, so
		// what GET /api/metrics reports about this deployment's tick is
		// what this loop actually measured (cycleTimes' own doc comment).
		CycleTimes: cycleTimes,
		// Where the templates, suites and schedules a repository declares
		// in its own `.grain` directory are read from -- this host, with
		// the credential this deployment already holds for that
		// repository (repoDeclarationSource, grain/task-60). It is wired
		// unconditionally, unlike CI above, because it costs nothing at
		// all until an operator turns a repository's declarations on:
		// with none on, the reconciler reads repo_config and stops.
		Declarations: RepoDeclarationSource{Clone: CiWorkflowSource{
			ConfigRepoDir:       StateRepoDir(cfg.DataDir),
			Credentials:         credentials,
			GitHubHost:          cfg.GithubHost,
			InsecureHTTP:        cfg.GithubInsecureHTTP,
			Forbidden:           forbidden,
			TempDir:             filepath.Join(cfg.DataDir, "state"),
			TeamGitCredential:   teamGitCredential,
			TeamGitCredentialOn: teamGitCredentialOn,
		}},
	}
	// The same gate the UI/API server was given, so the CI a branch is put
	// through by the cycle (orchestrator.reconcileCI) is the CI an
	// operator sees in GET /api/ci/runs. Wired only behind -ci, because
	// orchestrator.Deps.CI being nil is how the cycle is told this
	// deployment runs none -- the same contract ui.Config.CI holds to,
	// and what keeps a daemon started without it from asking GitHub for a
	// branch head on every finished run.
	if cfg.Ci {
		deps.CI = CiGate{}
	}
	// The runs RecoverRunHosts adopted, carried to their ends here
	// (orchestrator.ResumeAdoptedRun): on the in-flight set, so the
	// shutdown drain below waits for them as for any run this daemon
	// started -- and a stop detaches them again for the next daemon.
	resumeAdopted := func(adopted adoptionPass) {
		resume := func(host orchestrator.AdoptHost, runs []orchestrator.AdoptedRun) {
			for _, a := range runs {
				inFlight.Go(a.Claim.Run, a.Claim.Task, func() {
					if err := orchestrator.ResumeAdoptedRun(ctx, deps, host, a); err != nil {
						log.Printf("grain daemon: adopted run %s (task %s): %v", a.Claim.Run, a.Claim.Task, err)
					}
				})
			}
		}
		resume(cfg.RunHost, adopted.runHost)
		resume(cfg.hostdRuns, adopted.hostd)
	}
	resumeAdopted(adopted)
	// And the same again if an upgrade flushed these runs for a restart
	// that then left this process running (runHostUpgrade.resume): the
	// runs are this daemon's to adopt back, from the cursors it wrote.
	cfg.runHostUpgrade.setReadopt(func() {
		if ctx.Err() != nil {
			return
		}
		again, _, err := adoptAll(ctx, store, githubClient, cfg.RunHost, cfg.hostdRuns, sandboxes)
		if err != nil {
			log.Printf("grain daemon: %v", err)
		}
		orchestrator.SyncSandboxLedger(ctx, deps)
		resumeAdopted(again)
	})
	// Not cfg.MaxWorkers / cfg.MaxMergers: those are the deprecated,
	// ignored flags, and no count bounds a run.
	log.Printf("grain daemon: reconciling every %s (re-read from the store each tick). How many runs and CI jobs "+
		"go at once is bounded by this deployment's sandbox budget: %s",
		cfg.PollInterval, BudgetSentence(cfg.Limits(), KubernetesSandboxes(cfg)))
	reconcile(ctx, deps, live)
	// Detach in-flight CI runs as soon as reconcile stops rather than waiting
	// for DrainInFlight to finish, so CI stops starting new jobs or retries
	// while the daemon unwinds.
	if ciService := LiveCI.embedded(); ciService != nil {
		ciService.Detach()
	}
	// reconcile only returns once ctx is done, which is also what tells
	// every live run to wind up -- so this is the shutdown drain, not a
	// wait for work still to be done.
	DrainInFlight(inFlight)
	return nil
}

// shutdownServerGrace bounds how long a deferred HTTP listener shutdown
// waits after DrainInFlight for idle or straggling in-flight connections
// (such as an adopted sandbox's still-open port-forward tunnel) to close.
const shutdownServerGrace = 10 * time.Second

func stopWithGrace(stop func(context.Context) error) {
	if stop == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownServerGrace)
	defer cancel()
	_ = stop(ctx)
}

// shutdownDrain bounds how long the daemon waits, after its reconcile
// loop has stopped, for the runs still in flight to unwind.
//
// It is worth waiting at all because unwinding is not nothing: a
// cancelled run still finishes its own row and still releases its
// sandbox, both on contexts deliberately detached from the cancellation
// (orchestrator.runCleanupTimeout, which bounds each of those two at 2
// minutes). Exiting the instant the loop stops would leave a granule VM
// running and a run row live for the next process to recover.
//
// It is bounded because a shutdown that never ends is worse than one
// that leaves something behind: a VM this gives up on is reaped at the
// next startup (GranuleSandboxes.ReapOrphans), and a run row is recovered
// there too (orchestrator.RecoverOrphanedRuns). Generous enough for both
// cleanup steps of a single run, and no more.
//
// It is only worth anything if whatever stops the daemon waits for it,
// and for a long time nothing did: the container unit gave the whole
// container `docker stop --time 30`, so the drain was cut off at thirty
// seconds on every upgrade. The fix is the stop timeouts rather than this
// number, deliberately. A drain short enough to fit inside thirty seconds
// would routinely give up on a VM deletion or a row write, and the cost of
// a long bound is only paid when there is something to unwind -- a
// daemon with nothing in flight exits as soon as its loop stops, and a
// scheduled upgrade waits for exactly that (orchestrator's
// scheduledupgrade.go). So the unit's `docker stop --time` and
// TimeoutStopSec (scripts/setup.sh) and Kubernetes'
// terminationGracePeriodSeconds (deploy/kubernetes) are all at least
// shutdownStopGrace, and TestStopTimeoutsCoverTheShutdownDrain fails the
// moment one of them drifts below it.
const shutdownDrain = 4*time.Minute + 30*time.Second

// shutdownStopGrace is the least any supervisor stopping the daemon must
// allow it: shutdownDrain, plus slack for what happens around the drain
// -- the reconcile loop finishing its tick, the store closing, the
// process exiting.
const shutdownStopGrace = shutdownDrain + 30*time.Second

// drainInFlight waits for runs to finish unwinding, logging what it is
// waiting on and what it gave up on -- the two things an operator
// watching a slow SIGINT wants to know -- and ends on one line saying
// what the shutdown cost: how many runs it interrupted, and how many of
// those it gave up waiting on (the next start recovers those as
// interrupted too, orchestrator.RecoverOrphanedRuns).
func DrainInFlight(runs *orchestrator.InFlight) {
	abandoned := drainInFlight(runs)
	// The whole process's count rather than one taken from the start of
	// the drain: a daemon's own shutdown is the only thing that finishes
	// a run as interrupted, and a run cancelled quickly enough finishes
	// before the drain has begun.
	finished := orchestrator.InterruptedRuns()
	log.Printf("grain daemon: shutdown interrupted %d run(s): %d finished as interrupted, %d left for the next start to recover; "+
		"none counts against its task", finished+int64(abandoned), finished, abandoned)
}

// drainInFlight is DrainInFlight's wait, reporting how many runs were
// still in flight when it gave up (0 when every run finished).
func drainInFlight(runs *orchestrator.InFlight) int {
	if runs.Len() == 0 {
		return 0
	}
	log.Printf("grain daemon: shutting down; waiting up to %s for %d run(s) to release their sandboxes: %v",
		shutdownDrain, runs.Len(), runs.Runs())
	ctx, cancel := context.WithTimeout(context.Background(), shutdownDrain)
	defer cancel()
	if err := runs.Wait(ctx); err != nil {
		abandoned := runs.Len()
		log.Printf("grain daemon: gave up waiting on %d run(s) still in flight (%v); "+
			"the next start recovers their rows and reaps their VMs", abandoned, runs.Runs())
		return abandoned
	}
	log.Printf("grain daemon: every run in flight at shutdown has finished")
	return 0
}
