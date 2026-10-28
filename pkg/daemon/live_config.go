package daemon

import (
	"context"
	"log"
	"strings"
	"sync"

	"github.com/ai-on-gke/grain/pkg/capability/githubtoken"
	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/servers"
	"github.com/ai-on-gke/grain/pkg/gitproxy"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelexec"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
)

// logSandboxCommitment is gone, with the arithmetic it printed
// (grain/task-28). It said what max_workers + max_mergers + the Director
// bypass + max_ci_jobs sandboxes of the deployment-wide shape committed
// against the machine they were built on, and warned when that product
// outgrew the host. There is no such product: those four numbers are not
// deployment-wide settings any more, no team has counts of its own either,
// and what holds the host is the budget -- which logSandboxBudget says on
// every start and again whenever one of its three settings moves, and
// which refuses the sandbox that would exceed it rather than warning
// about it afterwards. The Settings pane still does the same arithmetic
// over the deployment's stored counts (ui.Client.sandboxCommitment).

// liveConfig is the store-backed configuration this process actually has
// in effect: what loadConfig read at startup, plus every later change to
// it that a *running* daemon can apply on its own.
//
// bwsalmon/agents#320 left every setting but max-workers needing a
// restart -- loadConfig read grain_config once and nothing ever re-read
// it -- which is the wrong shape for a pane whose whole promise is that
// changing a value changes what the deployment does. refresh is called
// once per reconcile tick and either applies a change itself or leaves
// it to whichever piece already re-reads the store for it:
//
//	poll-interval                     this loop's own ticker (refresh)
//	gcp-project, gcp-agent-service-account
//	                                  the capability registry the next cycle
//	                                  resolves a task's grants against (refresh)
//	sandbox-cpus, sandbox-memory-mb   the default shape the next sandbox is
//	                                  built at (refresh, via defaultShaper)
//	max-workers, max-mergers, max-agent-turns
//	                                  orchestrator.RunCycle's own per-cycle re-read
//	agent-framework, gemini-model, gemini-effort, claude-model,
//	codex-model                       dispatchConfig's own per-dispatch re-read
//	prompt-extension                  the deployment-wide standing instructions
//	                                  RunCycle refreshes every cycle
//	                                  (orchestrator.resolvePromptExtension)
//	target-repos, default-capabilities, environment-name,
//	newest-first, show-closed-by-default,
//	approved-by-default, auto-merge-by-default
//	                                  pkg/ui, which reads grain_config per request
//
// That list is prose, and prose about a growing struct goes stale: pkg/ui's
// own settings_restart_drift_test.go is the check, requiring every
// ui.UpdateSettingsRequest field to be accounted for as one of these or as
// restart-only.
//
// What is left is github-host and github-insecure-http, which are baked
// into the git proxy's forwarder, the GitHub REST transport and the
// github-sandbox capability provider when this process starts -- each of
// them read without synchronisation by whatever request is already in
// flight, so swapping one under a live deployment would be a data race
// rather than a setting change. Those two keep their startup value here
// (config.withLiveModelConfig), which is what makes current() an honest
// answer to "what is this process running with" rather than a copy of
// what is merely stored -- the comparison the Settings pane makes to say
// a change has been saved but not yet applied (ui.Settings.PendingRestart,
// whose own restartOnlySettings list is the other end of this one).
type LiveConfig struct {
	store *model.Store
	// sandboxes is this deployment's sandbox backend, asked to adopt a
	// changed default VM shape when it is one that can (defaultShaper).
	// nil, or a backend that is not one, simply means that pair of
	// settings has nothing to apply to.
	sandboxes orchestrator.Sandboxes
	// githubCredentials is this deployment's GitHub credential ladder,
	// and githubTokens the named tokens in it beyond the default one
	// (gitproxy.CredentialSet.ExtraNames), each of which is a capability
	// of its own (pkg/capability/githubtoken).
	//
	// Fixed for this process's lifetime, unlike everything below it: the
	// ladder is loaded once at startup and is not hot-reloaded
	// (pkg/gitproxy/credentials.go's own doc comment), so adding a token
	// means restarting the daemon the same way changing the default one
	// already does. The names are held here anyway because refresh
	// rebuilds the capability registry from scratch, and a rebuild that
	// forgot them would quietly drop every named token's provider the
	// first time somebody changed the GCP project.
	//
	// The ladder itself is held for what it, and only it, can answer:
	// the material one of those tokens authenticates with right now,
	// which is what a credential check for a named token is made through
	// (githubtoken.CredentialSource). One ladder rather than a second
	// copy, deliberately -- it caches what it resolves, and the copy the
	// Settings pane writes a new token through is the copy that forgets
	// the old one, so a check made through any other would go on testing
	// the token an operator has just replaced.
	githubCredentials *gitproxy.CredentialSet
	githubTokens      []string
	// sandboxImages is the granule backend's image source and
	// stockSandboxImage the image this process resolved at startup for
	// its backend, whichever that is -- what a cleared custom image goes
	// back to. sandboxImages is nil on every other backend, which is
	// handed the image through orchestrator.SandboxImageSetter instead.
	//
	// refresh hands on whatever grain_config.sandbox_image says on each
	// tick, which is what makes a custom image changed in Settings, or
	// merged into the state repository, reach the next sandbox created
	// rather than the next restart.
	sandboxImages     *sandboximage.Source
	stockSandboxImage string
	// sandboxImageLabels reads a custom image's labels for the contract
	// line logged when it changes; nil logs nothing.
	sandboxImageLabels sandboximage.LabelReader
	pkgCache           *PackageCache
	// actionsCache is this process's Actions cache, held here for the
	// one thing a live setting does to it: resize its bound. Whether a
	// job is handed it at all is read the other way round, by the grant
	// asking this liveConfig (actionscache.go's grantFor).
	actionsCache *ActionsCache
	// capabilityServers is the registry of attached capability servers
	// (startCapabilityServers); Refresh hands it each mcp_servers it
	// reads. Nil where nothing attaches them (tests, `grain demo`).
	capabilityServers *servers.Registry

	executorServer *modelexec.Server

	Mu      sync.Mutex
	Applied Config
}

// newLiveConfig starts a liveConfig off at what loadConfig resolved --
// the configuration this process is about to run with, plus the GitHub
// credential ladder (githubCredentials above) it was started with, whose
// extra names are the named tokens this deployment offers. A nil ladder
// is a process with no GitHub credentials wired up at all (the tests
// below): no named tokens, and nothing for a check to authenticate as.
func NewLiveConfig(store *model.Store, sandboxes orchestrator.Sandboxes, cfg Config, github *gitproxy.CredentialSet) *LiveConfig {
	var tokens []string
	if github != nil {
		tokens = github.ExtraNames()
	}
	return &LiveConfig{
		store: store, sandboxes: sandboxes,
		githubCredentials: github, githubTokens: tokens, Applied: cfg,
	}
}

// WithSandboxImages gives this liveConfig the granule backend's image
// source (nil on any other backend), the stock image this process
// resolved at startup, and how to read a custom image's labels.
//
// A method rather than more newLiveConfig parameters: every other caller
// of that constructor is a test that has no sandbox image at all, and
// only the daemon's own start has one to give.
func (l *LiveConfig) WithSandboxImages(src *sandboximage.Source, stock string, labels sandboximage.LabelReader) *LiveConfig {
	l.sandboxImages, l.stockSandboxImage, l.sandboxImageLabels = src, stock, labels
	return l
}

// WithCapabilityServers hands Refresh the registry it keeps in step with
// grain_config.mcp_servers.
func (l *LiveConfig) WithCapabilityServers(r *servers.Registry) *LiveConfig {
	l.capabilityServers = r
	return l
}

// CapabilityServers is the registry of attached capability servers, nil
// when this process attaches none.
func (l *LiveConfig) CapabilityServers() *servers.Registry {
	if l == nil {
		return nil
	}
	return l.capabilityServers
}

func (l *LiveConfig) withPackageCache(c *PackageCache) *LiveConfig {
	l.pkgCache = c
	return l
}

func (l *LiveConfig) withActionsCache(c *ActionsCache) *LiveConfig {
	l.actionsCache = c
	return l
}

func (l *LiveConfig) SetExecutorServer(srv *modelexec.Server) {
	if l == nil {
		return
	}
	l.Mu.Lock()
	defer l.Mu.Unlock()
	l.executorServer = srv
}

func (l *LiveConfig) GetExecutorServer() *modelexec.Server {
	if l == nil {
		return nil
	}
	l.Mu.Lock()
	defer l.Mu.Unlock()
	return l.executorServer
}

// actionsCacheUsable is the stored switch as the grant reads it: may
// the next CI job be handed this deployment's Actions cache
// (model.Config.ActionsCacheEnabled)? Asked per job, so turning it off
// in Settings reaches the next job rather than the next restart.
func (l *LiveConfig) actionsCacheUsable() bool { return l.Current().ActionsCacheEnabled }

// gitHubTokens is the named GitHub tokens this process started with --
// see the field's own doc comment on why they need no lock: they are set
// once, before either half of this process is running, and never
// change.
func (l *LiveConfig) GitHubTokens() []string { return l.githubTokens }

// gitHubCredentials is the ladder those names came from, for the two
// callers that need the credentials themselves rather than their names:
// the Settings pane that writes and lists them, and the credential check
// below.
func (l *LiveConfig) gitHubCredentials() *gitproxy.CredentialSet { return l.githubCredentials }

// gitHubCredentialSource is that ladder in the shape the capability
// providers reach its material through -- see githubCredentialSource,
// and githubtoken/check.go for why a check is handed the ladder rather
// than growing a client of its own.
func (l *LiveConfig) gitHubCredentialSource() githubCredentialLadder {
	return GithubCredentialSource{Set: l.githubCredentials}
}

// githubCredentialLadder is the ladder as the two capabilities that ask
// it anything name it: a named token's own check
// (githubtoken.CredentialSource, one name in) and package publication's
// login lookup (packagepublish.CredentialSource, one repository in).
// One value satisfies both, because there is one ladder; a nil one is a
// process with no GitHub credentials at all, and leaves both answering
// that there is none rather than being wired to something that pretends
// otherwise.
type githubCredentialLadder interface {
	githubtoken.CredentialSource
	packagepublish.CredentialSource
}

// githubCredentialSource adapts the credential ladder onto
// githubtoken.CredentialSource: one name in, the material a push through
// it would carry right now out, and which of the two file forms it came
// from. The one place those two packages meet, the same way
// credentialTokenSource below is for pkg/github's own token lookup.
//
// A nil ladder answers "no such credential" rather than panicking: a
// process with no GitHub credentials configured offers no named tokens
// to check in the first place.
type GithubCredentialSource struct{ Set *gitproxy.CredentialSet }

func (s GithubCredentialSource) GitHubCredential(name string) (githubtoken.Credential, bool) {
	if s.Set == nil {
		return githubtoken.Credential{}, false
	}
	cred, ok := s.Set.Get(name)
	if !ok {
		return githubtoken.Credential{}, false
	}
	// A nil Token is the ladder's own "anonymous": an empty or unreadable
	// <name>.token, or a <name>.app.json whose installation token could
	// not be minted. Carried across as an empty token, which is a state
	// the check has its own sentence for -- and, for the App half of it,
	// with GitHub's own reason for refusing the token exchange, so that
	// sentence names the thing to fix (grain/task-158).
	token := ""
	if cred.Token != nil {
		token = *cred.Token
	}
	mintError := ""
	if cred.MintFailure != nil {
		mintError = cred.MintFailure.Describe()
	}
	return githubtoken.Credential{Token: token, App: s.Set.IsApp(name), MintError: mintError}, true
}

// PackageCredential is the other half of the same adaptation: the login
// a release job publishes this repository's packages with, resolved by
// the ladder's own owner/repo walk and App minting
// (gitproxy.CredentialSet.PackageCredential) and restated in the shape
// the capability that places it names.
//
// The ladder's error is passed through rather than replaced. It is
// already written for whoever is looking at a refused job -- which
// repository has no credential, or which App would not mint one and
// what GitHub said about it -- and this is the last place that could
// lose it.
func (s GithubCredentialSource) PackageCredential(registry, owner, repo string) (packagepublish.Credential, error) {
	credential, err := s.Set.PackageCredential(registry, owner, repo)
	if err != nil {
		return packagepublish.Credential{}, err
	}
	return packagepublish.Credential{
		Registry: credential.Registry, Username: credential.Username,
		Token: credential.Token, Name: credential.Name,
	}, nil
}

// current is the configuration in effect right now. Safe to call from
// any goroutine -- the UI server calls it to answer GET /api/settings
// while the reconcile loop is refreshing it.
func (l *LiveConfig) Current() Config {
	l.Mu.Lock()
	defer l.Mu.Unlock()
	return l.Applied
}

// modelListingConfig is the configuration a model picker's listing runs
// on: what this process has applied, with the stored connections laid
// over it exactly as a dispatch reads them (DispatchConfig). Not
// Current() alone: Applied picks up a connection saved through the UI
// only on the next reconcile tick (-poll-interval, 10s by default), and
// the connection page asks for its models the moment it is saved -- so
// a connection's own CLI (CLIPath) was not yet the one asked, and the
// pane said "the Codex CLI is not installed" or listed the old binary's
// models (grain/task-3945).
func (l *LiveConfig) modelListingConfig(ctx context.Context) Config {
	return DispatchConfig(ctx, l.store, l.Current())
}

// storedConfig is grain_config as it stands right now, resolved into the
// same shape current() answers in -- what this process *would* be
// running with if it applied the row this instant, as against what it
// has actually applied.
//
// The one caller is a credential check that found no provider
// registered and has to say why (capabilityCheckAdapter.unregistered):
// everything else in a running daemon reads current(), which is the
// honest answer to what this deployment is doing, and reading the store
// per request is not something this type invites. false means there was
// nothing to compare against -- no store wired up (the tests below), a
// database that would not answer, or a deployment with no configuration
// row yet -- and leaves the caller with only what it already knew.
//
// Built on the same withLiveModelConfig refresh applies, so the two
// cannot disagree about which fields a running process adopts: whatever
// the next tick would put into applied is what this returns now.
func (l *LiveConfig) storedConfig(ctx context.Context) (Config, bool) {
	if l.store == nil {
		return Config{}, false
	}
	stored, err := l.store.GetConfig(ctx)
	if err != nil {
		log.Printf("grain daemon: re-reading stored configuration: %v", err)
		return Config{}, false
	}
	if stored == nil {
		return Config{}, false
	}
	l.Mu.Lock()
	defer l.Mu.Unlock()
	return l.Applied.withLiveModelConfig(*stored), true
}

// modelConfig is current() in the shape pkg/ui compares the stored row
// against (ui.Config.RunningConfig).
func (l *LiveConfig) ModelConfig() model.Config { return l.Current().ToModelConfig() }

// refresh re-reads grain_config and applies whatever changed to the
// pieces of this running daemon that can take it, mutating deps in place
// for the ones a cycle carries. It reports whether the poll interval
// itself changed, which is the one thing its caller has to act on rather
// than something applied here.
//
// A store that cannot be read is logged and otherwise ignored: keeping
// the configuration already in effect is the right answer to a momentary
// read failure, and the next tick tries again.
func (l *LiveConfig) Refresh(ctx context.Context, deps *orchestrator.Deps) (pollIntervalChanged bool) {
	if l.store == nil {
		return false
	}
	stored, err := l.store.GetConfig(ctx)
	if err != nil {
		log.Printf("grain daemon: re-reading stored configuration: %v", err)
		return false
	}
	if stored == nil {
		return false
	}

	l.Mu.Lock()
	was := l.Applied
	l.Mu.Unlock()
	now := was.withLiveModelConfig(*stored)
	// The host reading startup took (withHostCapacity) is retried for as
	// long as it has none: on Kubernetes it is a kubectl call that a
	// slow API server, or a ClusterRoleBinding applied after the daemon
	// started, can fail, and without it the budget bounds nothing until
	// a restart. Read outside the lock, since it may wait on the
	// cluster; this goroutine is the only writer of Applied.
	//
	// A cluster is re-read on every tick even once it has a figure: a
	// cluster autoscaler adds and removes nodes under a running daemon,
	// and the budget should follow it rather than stay at the node count
	// startup saw. A failed or empty reading keeps the figure already
	// known (withHostCapacity). Local hosts are read once: sysstat.Read
	// sleeps for a sampling interval, and a machine does not resize.
	if l.sandboxes != nil && (now.HostCPUs <= 0 || now.HostMemoryMB <= 0 || !localHostSandboxes(l.sandboxes)) {
		now = now.withHostCapacity(l.sandboxes)
	}
	// And the cluster's free capacity, which unlike its size moves with
	// every pod: read once a tick (at most every k8s.DefaultFitTTL), so
	// the admissions this tick makes are held against one reading rather
	// than one kubectl call each (model.Budget.ClusterFit).
	if r, ok := l.sandboxes.(clusterFitRefresher); ok {
		r.RefreshClusterFit(ctx)
	}
	l.Mu.Lock()
	l.Applied = now
	l.Mu.Unlock()

	// Handed on before the "nothing changed" return below, and on every
	// tick rather than only on the tick that changed it: it is a
	// comparison when the image is what it already was.
	l.applySandboxImage(ctx, now)
	// And the instance label a Kubernetes backend's next pod is given,
	// for the same reason.
	l.applySandboxInstance(ctx)
	// Every tick, like the image above: Update only records the
	// configuration, and the registry's own loop tells a reload from an
	// unchanged one and attaches, refreshes and detaches off this
	// goroutine (pkg/capability/servers).
	if l.capabilityServers != nil {
		l.capabilityServers.Update(registerBuiltinCapabilityServers(ctx, l.store, now.DataDir, stored.MCPServers))
	}

	wasCPUOvercommit, wasMemOvercommit := was.overcommitRatios()
	nowCPUOvercommit, nowMemOvercommit := now.overcommitRatios()
	overcommitChanged := nowCPUOvercommit != wasCPUOvercommit || nowMemOvercommit != wasMemOvercommit ||
		now.DiskOvercommit != was.DiskOvercommit
	if overcommitChanged {
		if shaper, ok := l.sandboxes.(overcommitShaper); ok {
			shaper.SetOvercommit(nowCPUOvercommit, nowMemOvercommit, now.DiskOvercommit)
		}
	}
	budgetChanged := overcommitChanged || now.CpuOvercommit != was.CpuOvercommit || now.MemoryOvercommit != was.MemoryOvercommit ||
		now.MemoryReserveMB != was.MemoryReserveMB || now.SandboxOvercommit != was.SandboxOvercommit ||
		now.HostCPUs != was.HostCPUs || now.HostMemoryMB != was.HostMemoryMB || now.HostDiskGB != was.HostDiskGB

	changes := now.ChangesFrom(was)
	if len(changes) == 0 && !budgetChanged {
		return false
	}
	if len(changes) > 0 {
		log.Printf("grain daemon: applying changed settings without a restart: %s", strings.Join(changes, ", "))
	}
	hostChanged := now.HostCPUs != was.HostCPUs || now.HostMemoryMB != was.HostMemoryMB
	if hostChanged {
		log.Printf("grain daemon: sandbox capacity changed from %d CPUs and %d MiB to %d CPUs and %d MiB",
			was.HostCPUs, was.HostMemoryMB, now.HostCPUs, now.HostMemoryMB)
	}

	deps.MaxWorkers = now.MaxWorkers
	deps.MaxMergers = now.MaxMergers
	deps.Budget = now.Budget()
	deps.AgentShape = now.AgentShape()

	if now.GcpProject != was.GcpProject || now.GcpServiceAccountEmail != was.GcpServiceAccountEmail || now.GcpServiceAccountCount != was.GcpServiceAccountCount {
		// Rebuilt rather than edited: capabilityProviders is the one
		// place that decides which providers a given configuration has,
		// and a dispatch goroutine still holding the old registry keeps
		// resolving against it (deps is copied per cycle and per
		// dispatch) rather than seeing one change underneath it.
		deps.Config.Capabilities = newCapabilityRegistryWithStore(
			l.store, now.LocalScriptsConfig(),
			CapabilityProviders(now, l.store, ModelProxies, l.githubTokens, l.gitHubCredentialSource())...)
	}
	if now.GcpProject != was.GcpProject || now.GcpServiceAccountEmail != was.GcpServiceAccountEmail || now.IapBackendService != was.IapBackendService ||
		now.IapResource != was.IapResource {
		TriggerIAPSync()
	}
	if now.SandboxCPUs != was.SandboxCPUs || now.SandboxMemoryMB != was.SandboxMemoryMB ||
		now.SandboxDiskGB != was.SandboxDiskGB || now.networkAccess != was.networkAccess {
		if shaper, ok := l.sandboxes.(defaultShaper); ok {
			shaper.SetDefaultShape(orchestrator.Shape{
				CPUs: now.SandboxCPUs, MemoryMB: now.SandboxMemoryMB, DiskGB: now.SandboxDiskGB,
				// The deployment-wide network default reaches the
				// backend the same way a changed shape does, and it is
				// the only route it has: there is no flag to seed it
				// from at construction (config.networkAccess), so the
				// first tick after startup is what puts it in force.
				NetworkAccess: now.networkAccess,
			})
		}
	}
	if now.PackageCacheGB != was.PackageCacheGB {
		if l.pkgCache != nil && l.pkgCache.cache != nil {
			var maxBytes int64
			if now.PackageCacheGB > 0 {
				maxBytes = int64(now.PackageCacheGB) << 30
			} else {
				maxBytes = pkgcache.DefaultMaxBytes
			}
			l.pkgCache.cache.SetMaxBytes(maxBytes)
		}
	}
	if now.ActionsCacheGB != was.ActionsCacheGB {
		// SetMaxBytes evicts down to the new bound there and then, which
		// is safe with jobs in flight for the reason a flush is: the
		// worst it can do is turn a hit somebody has not made yet into a
		// miss (pkg/actionscache's Flush and evict). A zero or negative
		// stored value is grain's own default, the same reading NewCache
		// gives it.
		if l.actionsCache != nil && l.actionsCache.cache != nil {
			l.actionsCache.cache.SetMaxBytes(int64(now.ActionsCacheGB) << 30)
		}
	}
	// A changed shape or a changed slot count is a changed commitment
	// against the host, and this is the moment it arrives on a
	// deployment whose settings are edited by merging a pull request
	// rather than in the pane that would have said so
	// (logSandboxCommitment's own doc comment).
	if budgetChanged {
		// The total those counts are held against, said again for the
		// same reason it is said at startup: it is the figure an operator
		// needs before they raise anything, it is a setting now rather
		// than a flag (grain/task-197), and on a deployment configured by
		// merging a pull request this tick is the first moment the new
		// one exists anywhere.
		LogSandboxBudget(now)
	}
	// A cluster that grew -- a node the autoscaler brought up, often for
	// the very run or CI job now waiting on the budget -- has room the
	// waiting dispatch and CI gates should try for now, not on whatever
	// tick or release would next have woken them.
	if hostChanged && (now.HostCPUs > was.HostCPUs || now.HostMemoryMB > was.HostMemoryMB) &&
		now.Ledger != nil && now.Ledger.Wake != nil {
		now.Ledger.Wake()
	}
	return now.PollInterval != was.PollInterval
}

// applySandboxInstance hands a Kubernetes sandbox backend
// (orchestrator.SandboxInstanceSetter) the deployment's effective id
// again, so a deployment_id edited in Settings labels the next pod built
// -- and narrows the next orphan sweep -- under the new id rather than
// after a restart. A backend that is not one has no pods to label.
func (l *LiveConfig) applySandboxInstance(ctx context.Context) {
	setter, ok := l.sandboxes.(orchestrator.SandboxInstanceSetter)
	if !ok {
		return
	}
	id, err := l.store.DeploymentID(ctx)
	if err != nil {
		log.Printf("grain daemon: re-reading the deployment id for sandbox pod labels: %v", err)
		return
	}
	if was := setter.CurrentInstance(); was != id {
		log.Printf("grain daemon: sandbox pods are now labelled %s=%s (was %q); pods already running keep their label",
			k8s.InstanceLabel, id, was)
		setter.SetInstance(id)
	}
}

// applySandboxImage points this deployment's sandbox backend at the
// custom image grain_config now names (model.Config.SandboxImage), or
// back at the stock image this process resolved at startup when it names
// none -- through the granule backend's sandboximage.Source, or any other
// backend's orchestrator.SandboxImageSetter. A backend with neither (host,
// none, remote-only) has no image, and the setting is inert there.
//
// Nothing is built or pulled here: the backend pulls the image when it
// next creates a sandbox, exactly as it pulls the stock one. A change is
// logged with its contract verdict; CI job fingerprints read the active
// image via CurrentSandboxImage, so jobs planned under another image get a
// distinct cache key without wiping existing entries.
func (l *LiveConfig) applySandboxImage(ctx context.Context, cfg Config) {
	if l.stockSandboxImage == "" {
		return
	}
	custom := customSandboxImageFor(cfg)
	var changed bool
	switch setter, ok := l.sandboxes.(orchestrator.SandboxImageSetter); {
	case l.sandboxImages != nil:
		_, changed = l.sandboxImages.Set(l.stockSandboxImage, custom)
	case ok:
		image := firstNonEmpty(custom, l.stockSandboxImage)
		changed = setter.CurrentImage() != image
		setter.SetImage(image)
	default:
		return
	}
	if changed {
		if custom == "" {
			log.Printf("grain daemon: custom sandbox image cleared; new sandboxes are created from %s", l.stockSandboxImage)
		} else if l.sandboxImageLabels != nil {
			go logCustomSandboxImage(context.WithoutCancel(ctx), cfg, custom, l.stockSandboxImage, l.sandboxImageLabels)
		}
	}
	if ciSvc := LiveCI.embedded(); ciSvc != nil {
		ciSvc.SandboxImage = l.CurrentSandboxImage
	}
}

// CurrentSandboxImage is the OCI image new sandboxes are created from right
// now on this process's active backend, or "" for a backend with no image
// (host, disabled).
func (l *LiveConfig) CurrentSandboxImage() string {
	if l == nil {
		return ""
	}
	if l.sandboxImages != nil {
		return l.sandboxImages.Current()
	}
	if setter, ok := l.sandboxes.(orchestrator.SandboxImageSetter); ok {
		if img := setter.CurrentImage(); img != "" {
			return img
		}
	}
	cfg := l.Current()
	return firstNonEmpty(customSandboxImageFor(cfg), firstNonEmpty(l.stockSandboxImage, stockSandboxImageFor(cfg)))
}

// clusterFitRefresher is a Kubernetes backend's fit check, refreshed by
// LiveConfig.Refresh once a tick.
type clusterFitRefresher interface {
	RefreshClusterFit(ctx context.Context)
}
