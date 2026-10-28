package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/gitproxy"
	"github.com/bwsalmon/grain/pkg/installtrack"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
	"github.com/bwsalmon/grain/pkg/upgrade"
)

// startUIServer serves pkg/ui.Server -- the JSON API plus the static
// frontend -- on addr, over the same store RunCycle dispatches against,
// returning a shutdown func the same shape startGitProxy's own is.
//
// db is that same store's connection, and it is here for one thing: the
// Upgrade button this server offers has to be able to write this
// deployment's rows out when the build it is cutting over to knows a
// different schema (newUpgrader, and schemaupgrade.go for why an upgrade
// that cannot do that used to take the deployment down).
// Running it in-process rather than as a separate "ui" binary/service is
// exactly what bwsalmon/agents#363 asked for: one store connection, no
// second process needed just to let a daemon and a UI coexist (see this
// file's own doc comment).
//
// uiCfg.Secrets always points at this same process's own secrets
// directory (dataDir/secrets, the exact root run() hands orchestrator.Deps'
// own Credentials, above): bwsalmon/agents#357 added write-only secrets
// access for a UI colocated with the server, gated behind a
// -server-data-dir flag on the old standalone "ui" subcommand naming the
// server's own -data-dir from a second process. Now that the UI only
// ever runs inside the daemon that owns the store (the old standalone
// mode is gone -- see this file's own doc comment), it always has that
// directory to hand; there is no longer a cross-process case where it
// would not.
func startUIServer(cfg Config, store *model.Store, db *sql.DB, transcriptDir string, sandboxes orchestrator.Sandboxes, live *LiveConfig, pkgCache *PackageCache, actionsCache *ActionsCache, stateRepo ui.StateRepoManager, settingsRepo func() model.RepoRef, upgrader *upgrade.Upgrader) (stop func(context.Context) error, err error) {
	// The credential ladder run() already loaded, not a second copy of
	// it: this is the one Settings checks targetRepos against to flag
	// bwsalmon/agents#427's drift before a push ever reaches the proxy
	// with a confusing 500, *and* the one the tokens pane writes through
	// -- and writing through it is what makes it forget the token it had
	// cached (gitproxy.CredentialSet.forget). A check made through a
	// different copy would go on testing the token an operator has just
	// replaced on this very pane, which is the moment the button exists
	// for. So both halves are handed the same ladder.
	uiCredentials := live.gitHubCredentials()
	githubClient, githubTransport, githubTokens := newForgeClient(cfg, store, uiCredentials)
	// Charged to "ui" in the metrics' per-caller breakdown (github.WithCaller).
	githubClient = githubClient.As("ui")
	disks := cfg.hostVolumes()
	// One open of this deployment's secrets, used twice below: the
	// secrets fields on the Capabilities tab write into it, and the
	// "test this credential" action on that same tab resolves the
	// standing credential it is about to authenticate as out of it.
	uiSecrets := OpenSecrets(cfg.DataDir)
	// The local API token: how this host's grain CLI, and the `grain
	// mcpserver` each run starts here, reach the API. Named in this
	// process's environment so every child it starts can find it
	// without being told the data directory (ui/localtoken.go).
	var localAPIToken, localAPITokenFile string
	if cfg.DataDir != "" {
		path := ui.LocalAPITokenPath(cfg.DataDir)
		tok, err := ui.EnsureLocalAPIToken(path)
		if err != nil {
			log.Printf("grain daemon: WARNING: writing the local API token %s: %v -- "+
				"the grain CLI on this host will have to sign in", path, err)
		} else {
			localAPIToken, localAPITokenFile = tok, path
			os.Setenv(ui.LocalAPITokenFileEnv, path)
		}
	}
	// Whether the manager handed in can say when a setting last changed.
	// Asked of the interface rather than assumed, exactly as
	// localRepoComparer below is: startUIServer takes a
	// ui.StateRepoManager, and a caller with no state repository at all
	// (`grain demo`, a test) hands one that is neither.
	var settingsHistory ui.SettingsHistoryReader
	if reader, ok := stateRepo.(ui.SettingsHistoryReader); ok {
		settingsHistory = reader
	}
	uiCfg := ui.Config{
		Actor:                 ui.DefaultActor(ActorID(cfg.Actor)),
		LocalAPIToken:         localAPIToken,
		LocalAPITokenFile:     localAPITokenFile,
		AccessSessionLifetime: cfg.UiAccessSessionLifetime,
		IAPAudience:           cfg.UiIAPAudience,
		IAPIssuer:             cfg.UiIAPIssuer,
		IAPKeysURL:            cfg.UiIAPKeysURL,
		DBDriver:              cfg.DbDriver,
		DBDSN:                 cfg.DbDSN,
		DataDir:               cfg.DataDir,
		SourceDB:              db,
		// The fixed set grain ships providers for, plus one row per named
		// GitHub token this deployment has beyond its default one
		// (grain/task-117) -- the same names capabilityProviders builds
		// the matching providers from, so every id the picker offers is
		// one a dispatch can actually resolve.
		Capabilities: append(append(ui.OfferedCapabilities(), ui.GitHubTokenCapabilities(live.GitHubTokens())...),
			ui.APICapabilities()...),
		Consoles: consoleSessions,
		Secrets:  uiSecrets,
		// The registry run() attaches capability servers into, for the
		// status column of Settings' Capability servers pane.
		CapabilityServers: capabilityServerStatuses(live),
		// The package cache this process is serving, so Settings can say
		// how much it is holding and its flush button has something to
		// empty. nil on a deployment whose cache would not start.
		PackageCache: pkgCache.uiPackageCache(),
		ActionsCache: actionsCache.uiActionsCache(),
		// Unlike -codex-auth-file, this path is owned by the UI: replacing
		// or clearing it can never touch an operator-managed file.
		CodexAuthFile:         ManagedCodexAuthFile(cfg.DataDir),
		CodexAuthFallbackFile: cfg.CodexAuthFile,
		ExecutorServer:        cfg.executorServer,
		// Where that listener bound, and whether it wants TLS, so the
		// Teams pane's bootstrap command names the port this daemon
		// really serves rather than guessing 8422 (grain/task-2396).
		ExecutorAddr: cfg.executorListenAddr,
		ExecutorTLS:  cfg.executorListenAddr != "" && cfg.ExecutorCertFile != "",
		// The "test this credential" action on that same tab
		// (ui.Config.CapabilityChecks): the pane that says **Ready**
		// because a secret is set, given a way to ask the far end
		// whether the key inside it still works. Wired here rather than
		// behind a gate like PullRequests below, because it needs
		// nothing the reconcile loop builds -- the providers come from
		// the live configuration and the credentials from this same
		// secrets directory (and, for a named GitHub token, from the
		// ladder above), all available before (and regardless of
		// whether) that loop ever starts.
		CapabilityChecks: CapabilityCheckAdapter{Live: live, Creds: uiSecrets},
		// What every task read asks whether a task's own state has stood
		// for longer than that state should take (ui.Config.Stall). The
		// answer is the orchestrator's -- it is the package that knows
		// which component ought to have ended each wait and how long
		// each one is given (its liveness.go) -- and the UI is what puts
		// it on the row, so the two are joined here rather than by
		// either reaching for the other. Wired unconditionally, like
		// CapabilityChecks above: it reads nothing but the state, the
		// timestamps the store already holds and the clock, none of
		// which waits on the reconcile loop.
		Stall: orchestrator.TaskStallNote,
		// And the sub-case of that state, with its control
		// (ui.Config.Reasons) -- joined here for the same reason.
		Reasons: ReasonClassifier{},
		// The same action for the other kind of credential this
		// deployment holds (ui.Config.AgentKeyChecks): the key each agent
		// framework runs as, which the Agents tab can otherwise only
		// report as "set". Wired here for the same reasons as the two
		// around it -- it needs the live configuration (for the
		// -*-key-file fallbacks) and this same secrets directory, and
		// nothing the reconcile loop builds.
		AgentKeyChecks: AgentKeyCheckAdapter{Live: live, Creds: uiSecrets},
		// The Gemini model field's own picker (ui.Config.AgentModels):
		// the model names agy will accept are agy's to say, and this
		// deployment has the binary right here. Wired unconditionally,
		// like CapabilityChecks above and for the same reason -- it
		// needs nothing the reconcile loop builds, just the live
		// configuration and this same secrets directory -- and a
		// deployment whose image somehow has no agy gets that sentence
		// on the field rather than a pane that will not load.
		AgentModels: &AgyModelLister{Live: live, creds: uiSecrets},
		CodexModels: &CodexModelLister{Live: live, Creds: uiSecrets},
		// And the same picker for Claude, filled from Anthropic's own
		// listing rather than from a binary, since the claude CLI has no
		// listing to run (claudeModelLister, claude.Catalog).
		ClaudeModels: &ClaudeModelLister{Live: live, Creds: uiSecrets},
		// What a connection with no CLI path of its own falls back to,
		// shown as that field's hint: the flags, which only a restart
		// changes, so read once here.
		AgentCLIDefaults: map[string]string{"claude": cfg.ClaudePath, "codex": cfg.CodexPath, "agy": cfg.AgyPath},
		// The bootstrap pane: this process owns the state repository its
		// store is exported to, so the UI it serves is the one place that
		// can offer the choice of where state lives (pkg/ui/staterepo.go).
		StateRepo: stateRepo,
		// And the same manager's answer to which repository that is, in
		// owner/name form (ui.Config.StateRepoRef): what "Run now" on one
		// of grain's own scheduled jobs fires with, since the template it
		// fires reads this deployment's state repository and nothing in
		// the template can know which that is. The reconcile loop is
		// handed the identical function for the identical reason
		// (orchestrator.Config.StateRepo above).
		StateRepoRef: settingsRepo,
		// The three host-power actions, nil under Kubernetes: there is
		// no machine here that is this deployment's to reboot or power
		// off (the node runs other people's workloads), and restarting
		// grain is the Deployment controller's job -- `kubectl rollout
		// restart` or a deleted pod -- rather than a command this
		// process runs against itself. The UI reports all three
		// unavailable rather than offering buttons that could only 404
		// (ui.Config.Reboot's nil-means-unavailable contract, and
		// kubernetesMode for why the whole group goes together).
		Reboot:      HostAction(cfg, newRestartRequests(cfg.DataDir).wrap("the reboot button", rebootHost(cfg.RebootCmd))),
		Shutdown:    HostAction(cfg, shutdownHost(cfg.ShutdownCmd)),
		Restart:     HostAction(cfg, newRestartRequests(cfg.DataDir).wrap("the restart button", restartDaemon(cfg.RestartCmd))),
		RepoNames:   repoNameGate{},
		Credentials: uiCredentials,
		// What each of those credentials reaches on GitHub, so a task is
		// refused at filing against a repository its team cannot reach
		// and the New task pickers can offer what it can (grain/task-1850).
		GitReach:                  &gitproxy.Reach{Credentials: uiCredentials, API: githubTransport, Host: cfg.GithubHost},
		GitHubBranches:            githubClient,
		GitHubPullRequestFiles:    githubClient,
		GitHubPullRequests:        githubClient,
		GitHubCredentialAvailable: githubTokens.HasCredential,
		// The System pane's Logs tab: which streams this deployment can
		// show is a property of the shape it runs as, so logSources
		// decides it rather than a literal here.
		Logs:        LogSources(cfg),
		LogRedactor: logRedactor(SecretsDir(cfg.DataDir)),
		// liveTranscriptDir reads back whatever the Framework driving a
		// still-running attempt has mirrored so far into
		// transcriptDir/<runID> (transcriptDir's own doc comment on the
		// shared directory convention). antigravity.LiveTranscriptDir and
		// claude.LiveTranscriptDir read two different file formats
		// (RunConfig.TranscriptPath's own doc comment), and with a task
		// able to override this deployment's framework both formats can
		// be in this one directory at once -- so the reader wired here
		// picks between them per file rather than per deployment.
		LiveTranscripts: LiveTranscriptDir(transcriptDir),
		// orchestrator.ChecksUnavailable reads process-lifetime state
		// RunCycle's own reconcile loop sets the first time GitHub 403s a
		// check-runs read against this deployment's credential -- see its
		// own doc comment and Config.AutoMergeDegraded's.
		AutoMergeDegraded: orchestrator.ChecksUnavailable,
		// An unmigrated host-mode install, found by run() at start
		// (installnotice.go).
		InstallNotice: InstallNotice,
		// sandboxHealthAdapter wraps whichever of orchestrator.
		// GranuleSandboxes/HostSandboxes run() built as sandboxes (exactly
		// one, per run()'s own doc comment) -- both implement Health, just
		// not the interface ui.Config.Sandboxes actually names, since ui
		// does not import pkg/orchestrator (see ui/sandbox_health.go's own
		// doc comment). The sandbox health pane (bwsalmon/agents#536).
		Sandboxes: SandboxHealthAdapter{
			Inner:  sandboxes,
			Limits: func() model.Limits { return live.Current().Limits() },
		},
		// And which image those sandboxes are created from, beside the
		// one this build is stamped for: a pane is where an operator
		// looks when a sandbox is behaving like an older commit's, and
		// the startup warning about the two disagreeing has scrolled out
		// of the journal by then (uiSandboxImage, grain/task-306).
		SandboxImage: uiSandboxImage(cfg),
		// Whether the three sandbox-shape settings on that same pane
		// actually shape anything here, which is a property of the
		// backend built above rather than of the row they are stored in
		// (sandboxShapeIgnored's own doc comment). Under the default
		// host-directory sandboxing they shape nothing at all, and
		// saying so beside them is the whole of grain/task-9.
		SandboxShapeIgnored: SandboxShapeIgnored(sandboxes),
		// And how much host there is for the shape those settings ask
		// for, times every slot this deployment allows -- the question
		// no pane was asking, since max_workers is on one tab and the
		// shape is on another and the machine's own size is on a third
		// (ui.SandboxCommitment, grain/task-223). nil unless the
		// backend built above is one whose sandboxes really are on this
		// process's own machine; sandboxHostCapacity's own doc comment
		// for why that is asserted rather than assumed.
		SandboxHostCapacity: sandboxHostCapacity(sandboxes),
		// The auto overcommit controller's live state, for the same panes
		// (model.OvercommitController); there is one wherever there is a
		// host capacity to scale.
		Overcommit: overcommitState(live, sandboxes),
		// hostStats reads this same process's own machine, not any one
		// sandbox -- see pkg/sysstat's own doc comment on why that's a
		// separate reading from Sandboxes above. It takes a list of
		// paths because a disk figure has to name a filesystem, and this
		// deployment has more than one worth naming (hostDisks' own doc
		// comment on which). Resolved once, out here, rather than per
		// poll: which filesystems they are is fixed for the life of the
		// process, and finding docker's own asks its engine.
		// nil under Kubernetes, like the power actions above: /proc in
		// this container is the node's, so what this would report is
		// the pressure on a machine shared with workloads that are
		// nothing to do with grain, labelled as though it were this
		// deployment's own. The sandbox rows beside it stay -- those
		// are pods this deployment really does own -- and the node's
		// real figures are `kubectl top node`'s to give.
		HostStats: HostStatsOrNil(cfg, disks, live.diskFloor),
		// The same volumes, for the pane that is editing the floor rather
		// than watching it: Settings -> Sandbox resolves the share it is
		// showing against each of them, since a floor of "a tenth" is not
		// a figure until it meets a disk (ui.Settings.SandboxDiskFloors,
		// grain/task-372).
		SandboxVolumes: HostVolumesOrNil(cfg, disks),
		// hostTop is the per-process half of that same reading -- which
		// processes are spending the machine HostStats above says is
		// busy (pkg/hosttop's own doc comment). Same machine, and in a
		// container deployment the same PID namespace this process is
		// in: `docker run` is given no --pid=host (scripts/setup.sh's
		// own docker_run_args), so what this lists is the daemon and
		// everything it forked, which is where daemon-side load comes
		// from. A sandbox's own processes belong to its VM and are not
		// in it -- that is what the sandbox rows beside this report.
		// Also nil under Kubernetes, and for HostStats' reason narrowed
		// to processes: this container's PID namespace holds the daemon
		// and what it forked, but the machine whose load an operator is
		// chasing is the node, whose other pods are invisible from here
		// -- so the pane would be at its most misleading exactly when it
		// was most wanted.
		HostTop: HostTopOrNil(cfg),
		// And the reading that is about this process rather than about
		// the machine under it: its uptime, how many times this service
		// has started in the last half hour, and the flags it was
		// started with and does not define. A deployment in a restart
		// loop is one whose every other figure here looks healthy in the
		// seconds it is up (ui.DaemonHealth, grain/task-233).
		Daemon: daemonHealthOrNil(cfg),
		// A fourth reading sat here (ui.Config.RootShell,
		// grain/task-13): one command, as root on this same machine,
		// for the failure the three panes above could not explain. It
		// is gone (grain/task-193) -- what it handed whoever reached
		// this UI was unrestricted root on the host, which is more than
		// the whole of the rest of this config put together, and no
		// debugging pane is worth it. An operator still has whatever
		// way in they deployed the host with.
		//
		// ReconcilerDown mirrors this same process's own package-level
		// reconcilerDown (daemon.go), the same way AutoMergeDegraded above
		// mirrors orchestrator.ChecksUnavailable -- bwsalmon/agents#576.
		ReconcilerDown: func() bool { return ReconcilerDown.Load() },
		// Restarting: this process has been told to stop, or the
		// upgrader is running an upgrade that ends in a restart.
		Restarting: daemonRestarting(cfg, upgrader),
		// And the moment the first half of that starts, for the long
		// poll that has to hear it before the API goes away.
		ShutdownStarted: cfg.shutdownStarted,
		// The other half of this process's own cycleWakes: a reply that
		// un-parks a task asks the reconcile loop for a cycle now instead
		// of leaving the person to wait out -poll-interval. Wired
		// unconditionally, since a wake written before that loop starts
		// (this server is up first, deliberately) sits in the buffer and
		// is read when it does.
		Wake:                  wakeCycle,
		TriggerIAPSync:        TriggerIAPSync,
		SandboxBackends:       cfg.availableSandboxBackends(),
		SandboxBackendOptions: cfg.sandboxBackendOptions(),
		// And whether a merged settings change reset something on this
		// deployment that grain had nowhere to say out loud -- the same
		// marker, for a fact that reaches nobody without one
		// (ui.Config.StateRepoResetNotice, grain/task-197). Answered off
		// an atomic the manager keeps up to date, so this poll neither
		// waits on that lock nor runs git.
		StateRepoResetNotice: stateRepoResetNotice(stateRepo),
		// And whether the settings have stopped reaching the state
		// repository at all, for the standing banner and for
		// read_deployment_health (StateManager.syncFailure).
		StateRepoSyncFailure: stateRepoSyncFailure(stateRepo),
		// RunningConfig is what this process actually has in effect right
		// now (liveConfig's own doc comment), which the Settings pane
		// compares the stored row against to say which of the two
		// restart-only settings have been saved but not yet applied.
		RunningConfig: live.ModelConfig,
		// Where each agent CLI runs, resolved from the flags this daemon
		// started with and the settings the pane is showing (clirun.go).
		CLIRuns: func(ctx context.Context, stored map[string]model.CLIRunSetting) any {
			resolving := cfg
			resolving.CLIRun = stored
			return resolving.ResolveCLIRuns(ctx)
		},
		CLIRunCheck:  cfg.CheckCLIRunSettings,
		HostExecutor: func(ctx context.Context) any { return cfg.HostExecutorStatus(ctx) },
		// PullRequests is how a dispatched run opens its own pull request
		// before it exits (ui.Config.PullRequests' own doc comment): its
		// mcpserver asks this API, which asks the daemon's GitHub client.
		// The gate, rather than that client directly, because this server
		// starts before runDaemon has built one -- deliberately, so the
		// UI survives a reconcile loop that never comes up at all -- and
		// livePullRequests is what closes that gap once it does.
		PullRequests: PullRequestGate{},
		// PullRequestComments is how closing a task says so on the pull
		// request that close just orphaned (ui.Config.PullRequestComments'
		// own doc comment). Behind the same gate, and for the same reason
		// -- and where PullRequests turns a missing client into a refused
		// request, this one turns it into a line in the note left on the
		// task saying the pull request itself was not told.
		PullRequestComments: PullRequestGate{},
		// PullRequestCloser is the other half of that same close, and the
		// only route in a grain deployment by which a pull request is
		// ever closed rather than merged: a human who ticked the box
		// beside Close asking for this task's pull request to be closed
		// with it (ui.CloseOptions.ClosePullRequest). Behind the same
		// gate as the two above. A deployment that wanted its UI able to
		// say things on GitHub but never to shut anything would leave
		// exactly this field nil, which is why it is a field of its own.
		PullRequestCloser: PullRequestGate{},
		// SandboxRecreate is how a dispatched run gets out of a sandbox
		// it has broken beyond what it can fix from inside one
		// (ui.Config.SandboxRecreate's own doc comment): its mcpserver
		// asks this API, which asks the registry the run put itself in
		// when it was dispatched. No gate, unlike PullRequests above --
		// see sandboxRecreations' own doc comment.
		SandboxRecreate: sandboxRecreateAdapter{sandboxRecreations},
		// StandingSandboxes is what the reconcile loop's own supervisor
		// is running, for GET /api/sandboxes. No gate, for the reason
		// SandboxRecreate needs none (standingSandboxes' doc comment).
		StandingSandboxes: standingSandboxStatuses,
		// PersistentSandboxes is the same supervisor the reconcile loop
		// runs them through: their status, controls and /staging/ proxy.
		PersistentSandboxes: persistentSandboxAdapter{store: store, sup: persistentSandboxes},
		// WaitCredit is how a run waiting on its own CI keeps the clock
		// from cancelling it halfway through the wait: the same hop,
		// asking the same kind of registry, and needing no gate for the
		// same reason (ui.Config.WaitCredit, runWaitCredits).
		WaitCredit: waitCreditAdapter{runWaitCredits},
		// TaskCanceller is how the Stop button in the UI cleanly interrupts
		// an in-flight turn (ui.Config.TaskCanceller, taskCancellers).
		TaskCanceller: taskCancellerAdapter{taskCancellers},
		// The reconcile loop's own tick, for GET /api/metrics' "cycles"
		// section. No gate needed, unlike PullRequests above: cycleTimes
		// is allocated at process start and runDaemon writes into that
		// same ring once it gets there (cycleTimes' own doc comment).
		Cycles: cycleTimesAdapter{cycleTimes},
		// And when this deployment's own parameters last moved, for the
		// "changes" section beside them: the state manager reads it off the
		// repository it owns (its own SettingsChanges). nil for a UI
		// handed no manager, which the section reports as "nobody can say"
		// rather than as "nothing has changed".
		SettingsHistory: settingsHistory,
		// And the machine underneath all of it, over time (hostLoad's own
		// doc comment). No gate, for the reason Cycles needs none: the ring
		// is allocated at process start and the sampler writes into that
		// same ring once runDaemon gets there.
		HostLoad:    hostLoadAdapter{hostLoad},
		GitHubCalls: gitHubCallStats{},
		// The dispatch gate an agent's usage limit closes, for the
		// banner that says why a queue of ready tasks has nothing
		// running and for the operator who wants it open again
		// (ui.Config.AgentPause's own doc comment). No adapter: unlike
		// Sandboxes above there is no shape to convert, and
		// *orchestrator.Pause satisfies ui.AgentPause as it stands. No
		// gate either, for the reason Cycles above needs none --
		// agentPause is allocated at process start, and reading it
		// before runDaemon gets there reports nothing paused.
		AgentPause: agentPause,
		InstallTrackProbe: &installtrack.Probe{
			IncludeHost:    true,
			AgyPath:        cfg.AgyPath,
			UIAllowedHosts: cfg.UiAllowedHosts,
			DataDir:        cfg.DataDir,
		},
		CheckImageManifest: func(ctx context.Context, imageRef string) error {
			if LocalSandboxes != nil && !strings.HasPrefix(imageRef, "127.0.0.1:") && !strings.HasPrefix(imageRef, "localhost:") {
				return nil
			}
			if err := ui.CheckImageManifest(ctx, imageRef); errors.Is(err, ui.ErrImageNotFound) {
				return err
			}
			return nil
		},
	}
	var localRepoComparer ui.RepoComparer
	if comparer, ok := stateRepo.(ui.RepoComparer); ok {
		localRepoComparer = comparer
	}
	uiCfg.RepoComparer = repoComparisonGate{local: localRepoComparer}
	branchGate := repoBranchGate{remote: githubTokens}
	if lister, ok := stateRepo.(configRepoBranchLister); ok {
		branchGate.local = lister
	}
	uiCfg.RepoBranches = branchGate
	// grain's own CI, behind the same kind of gate PullRequests above is
	// behind and for the same reason: the engine belongs to runDaemon
	// (it needs the git proxy every job clones through), and this server
	// starts before it. Assigned only when this deployment runs CI at
	// all, because a nil ui.Config.CI is how the API says so -- a gate
	// wired unconditionally would have GET /api/ci/runs answering
	// `enabled: true` on a daemon started with -ci=false.
	if cfg.Ci {
		uiCfg.CI = CiGate{}
	}
	if cfg.DefaultTargetRepo != "" {
		repo, err := model.ParseRepo(cfg.DefaultTargetRepo)
		if err != nil {
			return nil, fmt.Errorf("-default-target-repo: %w", err)
		}
		uiCfg.DefaultTarget = &repo
	}
	// Assigned only when there is one: uiCfg.Upgrader is an interface,
	// and a nil *upgrade.Upgrader put in one is not nil -- which is the
	// very check every upgrade route makes to decide whether this
	// deployment offers upgrading at all.
	if upgrader != nil {
		uiCfg.Upgrader = upgrader
		uiCfg.LastRestart = lastRestart(store, upgrader, cfg.runHostUpgrade.adoption())
	}
	if cfg.executorServer != nil {
		uiCfg.Executors = cfg.executorServer
	}
	// And one line in the journal saying which of the two pipelines this
	// deployment's upgrades actually run, or that they are off. Here
	// rather than inside newUpgrader, so that it is said where the
	// upgrader is put to use rather than wherever it happens to be
	// built.
	switch {
	case cfg.UpgradeImage != "":
		log.Print(upgradeImageNote(cfg))
		if len(cfg.UpgradeVerifyCmd) == 0 {
			log.Print("grain daemon: warning: upgrades do not verify image signatures (no -upgrade-verify-cmd), so they run whatever the registry serves")
		}
	case cfg.UpgradeSrcDir != "":
		log.Printf("grain daemon: upgrades build in %s and install to %s%s",
			cfg.UpgradeSrcDir, cfg.UpgradeInstallPath, restartNote(cfg.UpgradeRestartCmd))
	default:
		log.Print("grain daemon: upgrading from the UI is off -- neither -upgrade-image nor -upgrade-src-dir is set")
	}

	uiClient := ui.NewClient(uiCfg, store)
	// The sandbox proxy's acting /cli/ routes act through this same
	// client (sandbox_cli.go).
	liveSandboxCLI.Store(uiClient)
	srv := ui.NewServerWithClient(uiClient)
	// A CI job's api token is answered by this same handler, in process,
	// held to its route table (pkg/capability/ciapi).
	CIAPI.SetUpstream(srv)
	// And the sandbox proxy's task://, deploy:// and /cli/ reads ask the
	// same handler, in process, what the MCP tools ask it over the wire
	// (sandbox_namespace.go).
	LiveAPI.Set(srv)

	var httpSrv *http.Server
	if cfg.UiAddr != "" {
		ln, err := net.Listen("tcp", cfg.UiAddr)
		if err != nil {
			return nil, fmt.Errorf("listening on %s: %w", cfg.UiAddr, err)
		}
		url := "http://" + ln.Addr().String()
		log.Printf("grain daemon: serving the UI/API on %s as %s", url, uiCfg.Actor.ID)
		if cfg.UiOpen {
			OpenBrowser(url)
		}
		if !ui.HostGuardEnforced(cfg.UiAddr, cfg.UiAllowedHosts) {
			log.Printf("grain daemon: -ui-addr %s listens on every interface and -ui-allowed-hosts is unset, so only requests "+
				"arriving over loopback have their Host header checked (localhost, IP literals and this machine's own names); "+
				"name the hosts this UI is reached by with -ui-allowed-hosts to check every request", cfg.UiAddr)
		}
		httpSrv = &http.Server{Handler: ui.HostGuard(srv, cfg.UiAddr, cfg.UiAllowedHosts)}
		go func() {
			if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("grain daemon: UI/API server: %v", err)
			}
		}()
	}

	log.Printf("grain daemon: single-tenant: every caller, this host's loopback included, presents the access code in %s -- "+
		"a browser on the UI's \"Enter access code\" page, the grain CLI as a bearer token; `grain access-code` prints it",
		ui.LocalAPITokenPath(cfg.DataDir))

	// Every connection whose provider can be asked keeps what it lists
	// up to date (ui.Client.AdvertiseDiscoveredModels), so adding a
	// provider puts every model its key reaches in the Model pickers, and
	// a model it adds or drops later follows on its own.
	discoveryCtx, cancelDiscovery := context.WithCancel(context.Background())
	go uiClient.RunModelDiscovery(discoveryCtx, ui.ModelDiscoveryInterval)

	return func(ctx context.Context) error {
		cancelDiscovery()
		if httpSrv != nil {
			return httpSrv.Shutdown(ctx)
		}
		return nil
	}, nil
}

// daemonRestarting is ui.Config.Restarting for this process: true once
// run()'s context is cancelled (cfg.shuttingDown) and while upgrader
// reports an upgrade running, since the one that succeeds ends by
// restarting this process.
func daemonRestarting(cfg Config, upgrader *upgrade.Upgrader) func() bool {
	return func() bool {
		if cfg.shuttingDown != nil && cfg.shuttingDown.Load() {
			return true
		}
		if upgrader == nil {
			return false
		}
		status, err := upgrader.Status()
		return err == nil && status.Phase == upgrade.PhaseRunning
	}
}

// lastRestart is ui.Config.LastRestart: the report the post-upgrade check
// is told (orchestrator.UpgradeAdoption), read again for each request so
// the upgrade pane and the check say the same thing.
func lastRestart(store *model.Store, upgrader *upgrade.Upgrader,
	adoption func() *orchestrator.AdoptionReport) func(context.Context) *ui.RestartAdoption {
	return func(ctx context.Context) *ui.RestartAdoption {
		status, err := upgrader.Status()
		if err != nil {
			log.Printf("ui: reading the upgrade status for the last restart's report: %v", err)
			return nil
		}
		return restartAdoption(orchestrator.UpgradeAdoption(ctx, store, adoption, status, time.Now().UTC()))
	}
}

// restartAdoption is report in the shape the upgrade pane reads, or nil.
func restartAdoption(report *orchestrator.AdoptionReport) *ui.RestartAdoption {
	if report == nil {
		return nil
	}
	notes := func(in []orchestrator.AdoptedRunNote) []ui.RestartRun {
		var out []ui.RestartRun
		for _, n := range in {
			out = append(out, ui.RestartRun{Run: n.Run, Task: n.Task, Framework: n.Framework, Setup: n.Setup, Reason: n.Reason, Session: n.Session})
		}
		return out
	}
	return &ui.RestartAdoption{At: report.At, Adopted: notes(report.Adopted), Lost: notes(report.Lost), Freed: report.Freed}
}
