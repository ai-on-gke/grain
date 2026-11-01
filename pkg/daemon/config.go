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
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ai-on-gke/grain/pkg/policy"
	"github.com/ai-on-gke/grain/pkg/runhost"
	"github.com/ai-on-gke/grain/pkg/upgrade"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/localscripts"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/hostd"
	"github.com/ai-on-gke/grain/pkg/iapreconciler"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/modelexec"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

type Config struct {
	DataDir  string
	DbDriver string
	DbDSN    string
	// unitFlags are the names of the flags this process was actually
	// started with, undefined ones included -- which on a deployment
	// scripts/setup.sh installed is what grain-daemon.service's ExecStart
	// passes, since that ExecStart is what started this process.
	//
	// Not configuration: it is configuration's own shadow, kept so that
	// two things can be said about it that nothing could say before
	// grain/task-233. An upgrade asks the image it has pulled whether it
	// would accept these flags before cutting over to it
	// (upgrade.ImageConfig.UnitFlags), and the UI reports the ones this
	// build is already ignoring (ui.DaemonHealth.UnknownFlags).
	UnitFlags []string
	// unknownFlags is the subset of unitFlags this build does not define
	// and is therefore ignoring, in the order they were passed.
	UnknownFlags []string
	// health is what this process can say about its own life -- when it
	// started, how many times this service has started lately, and which
	// flags it is ignoring (daemon_restarts.go). Carried on config
	// because every surface that reports it is reached through one: the
	// UI's own pane (ui.Config.Daemon) and the warning logged at
	// startup. nil in a config built by a test, where there is no
	// process life to speak of.
	Health *daemonHealth
	// MaxWorkers and MaxMergers are this deployment's global worker and
	// merger concurrency ceilings (model.Limits), enforced by dispatch.Cycle
	// alongside the host sandbox resource budget (Config.Budget).
	MaxWorkers int
	MaxMergers int
	MaxCIJobs  int
	// ledger is what this deployment's live sandboxes commit right now --
	// one for the whole process, charged by every admission there is
	// (model.Ledger). It is a pointer carried on config so that the two
	// places that need it read the same one: config.limits, which is what
	// dispatch and the CI gate are both handed.
	//
	// nil in a config a test built, which is a deployment with no ledger
	// to charge and the behaviour every such test already had (every
	// method on a nil *model.Ledger is a no-op).
	Ledger *model.Ledger
	// cpuOvercommit, memoryOvercommit and memoryReserveMB are the other
	// half of those three limits: what the sandboxes they admit are
	// allowed to weigh, rather than how many there may be
	// (model.Budget). Flags rather than stored settings, for now --
	// grain/task-173 closed the overcommit with the numbers it could
	// reach from here, and moving them into grain_config is a change to
	// the schema, the Settings pane and this deployment's own config
	// repository.
	//
	// memoryReserveMB is -1 for "an eighth of the machine, at least 8
	// GiB, at most half" (model.DefaultReserveMemoryMB), which is the only value where
	// a negative number means something other than nonsense.
	CpuOvercommit    float64
	MemoryOvercommit float64
	MemoryReserveMB  int
	// DiskOvercommit is grain_config.sandbox_disk_overcommit: the same
	// ratio for sandbox disk (model.Budget.DiskOvercommit), and on
	// Kubernetes what each pod's ephemeral-storage request is scaled by.
	// Never tuned by the auto overcommit controller, which watches CPU
	// and memory pressure only.
	DiskOvercommit float64
	// SandboxOvercommit is grain_config.sandbox_overcommit: fixed, which
	// admits against the two ratios above as stored, or auto, which
	// admits against Overcommit's effective ratios instead (the two above
	// are then only where that controller started).
	SandboxOvercommit model.OvercommitSettings
	// Overcommit is this process's auto overcommit controller
	// (model.OvercommitController, runOvercommit). One for the life of the
	// process, set by Run before the first tick; nil in tests and in
	// anything that builds a Config without a daemon, which admits at the
	// fixed ratios whatever the mode says.
	Overcommit *model.OvercommitController
	// RepairReserveSandboxes is -repair-reserve-sandboxes: how many agent
	// sandboxes' worth of the budget only base-branch repairs may spend
	// (model.Budget.RepairReserve, grain/task-488).
	RepairReserveSandboxes int
	// diskFloorShare and diskFloorMinMB are the third dimension, and the
	// one that is a floor rather than a sum: how much of the volume this
	// deployment writes to is kept free before it stops admitting work
	// (model.DiskFloor, grain/task-196). Stored settings for the same
	// reason the three above are, and re-read off the row each reconcile
	// tick by withModelConfig -- what the flags of the same names leave
	// behind is the seed a fresh database is written with
	// (grain/task-372).
	DiskFloorShare float64
	DiskFloorMinMB int
	// hostCPUs and hostMemoryMB are what the machine these sandboxes are
	// built on actually has, read once at startup (sandboxHostCapacity)
	// and left at zero where it could not be read or where the backend
	// builds nothing this process can size -- which leaves the budget
	// bounding nothing and the counts doing what they always did.
	HostCPUs     int
	HostMemoryMB int
	// HostDiskGB is the disk the backend reported alongside those -- a
	// cluster's allocatable ephemeral storage -- and zero on a local
	// host, whose sandbox volume the budget reads live instead.
	HostDiskGB int
	// ClusterFit is the backend's own answer to whether one more sandbox
	// would be scheduled now -- a Kubernetes backend's reading of its
	// nodes' free capacity (model.Budget.ClusterFit, grain/task-805) --
	// and nil on every backend that has no cluster to ask.
	ClusterFit model.ClusterFit
	// sandboxShapeIgnored is the backend's own answer to whether the
	// deployment-wide shape reaches a sandbox at all
	// (sandboxShapeIgnored). Under host sandboxing it does not, so there
	// is no per-run commitment to add up and the budget charges agent
	// runs nothing.
	SandboxShapeIgnored bool
	PollInterval        time.Duration
	// Backup settings remain host-side rather than store-backed: the Google
	// credential path describes this process's filesystem, and a schedule
	// must be known before the UI is available to edit anything.
	BackupInterval              time.Duration
	BackupGCSBucket             string
	BackupGoogleDriveFolder     string
	BackupGoogleCredentialsFile string
	// sandboxDir is where this deployment's disposable sandbox-side data
	// lives -- see -sandbox-dir's own flag doc comment for why this is not
	// just a subdirectory of dataDir.
	SandboxDir string
	// dockerRootDir is where the local docker engine keeps its data, if
	// this deployment would rather say than have hostDisks ask `docker
	// info` -- see -docker-root-dir's own flag doc comment. Nothing reads
	// it directly except hostDisks: grain never writes there itself,
	// dockerd does, on grain's behalf.
	DockerRootDir string
	// volumes are the filesystems this deployment's own state sits on, as
	// hostDisks resolved them once for this process: the store, the
	// sandbox root, and docker's data root -- whether an operator named
	// that one or `docker info` had to be asked for it, and with a root
	// this process cannot stat already dropped.
	//
	// Resolved once and carried, rather than resolved by each half that
	// wants it, because the discovery costs a subprocess and a timeout
	// (dockerRootDir) and because the two halves must not disagree about
	// which volumes this deployment writes to: the pane draws a bar and a
	// floor per volume (hostStats), and the floor holds work back on the
	// tightest of them (diskWatch). A volume on one list and not the
	// other is either work held for a disk the pane never shows or a bar
	// nothing is actually judged against.
	//
	// Set by run() before either half is built, the same way health is.
	// nil in a config a test built, where each half resolves what it can
	// for itself -- and where nothing execs `docker info`.
	Volumes []HostDisk

	// uiAddr, uiOpen, actor and defaultTargetRepo configure the in-process
	// pkg/ui.Server this daemon serves alongside RunCycle (bwsalmon/agents#363);
	// uiAddr empty disables it. actor and defaultTargetRepo build the same
	// ui.Config the old standalone "ui" subcommand's -as/-default-target-repo
	// flags did.
	UiAddr string
	// uiAllowedHosts is -ui-allowed-hosts: the extra Host names
	// ui.HostGuard lets through on the uiAddr listener.
	UiAllowedHosts []string
	// UiAccessSessionLifetime is -ui-access-session-lifetime
	// (ui.Config.AccessSessionLifetime).
	UiAccessSessionLifetime time.Duration
	// uiIAPAudience, uiIAPIssuer and uiIAPKeysURL are -ui-iap-audience,
	// -ui-iap-issuer and -ui-iap-keys-url: what a signed assertion must
	// carry, and where its keys are, for the UI/API to accept the
	// identity in it (ui.Config.IAPAudience and the rest).
	UiIAPAudience     string
	UiIAPIssuer       string
	UiIAPKeysURL      string
	UiOpen            bool
	Actor             string
	DefaultTargetRepo string
	// PolicyFile is -policy: this workstation's local policy
	// (grain_policy.yml, pkg/policy), which every task's claims are
	// checked against before it is given a sandbox. Empty enforces none.
	// A file that cannot be read or parsed stops the daemon from
	// starting rather than leaving it running unenforced.
	PolicyFile string
	// policy is PolicyFile as Run loaded it (loadLocalPolicy), once, with
	// -repo already added: the one *policy.Policy dispatch enforces. nil
	// with no -policy.
	policy *policy.Policy
	// audit is the local audit log (LocalAuditPath) as Run opened it,
	// once: dispatch writes every local-policy refusal to it
	// (orchestrator.Deps.PolicyAudit). nil without a -data-dir, or when
	// it could not be opened.
	audit *policy.AuditLog
	// LocalRepos are -repo (LocalSettings): the repositories this
	// workstation works on, attached at start (attachLocalRepos) to the
	// local policy and the default team.
	LocalRepos []model.RepoRef

	GeminiAPIKeyFile string
	GeminiModel      string
	// geminiEffort is store-backed alongside geminiModel
	// (model.Config.GeminiEffort): the other half of the model selection
	// agy is given, -gemini-effort seeding it only the first time.
	GeminiEffort  string
	MaxAgentTurns int

	// AgentFramework is the framework a run is dispatched onto when
	// nothing else names one: only a workstation's -model-backend sets it
	// now. Every run is otherwise dispatched onto a model connection,
	// which names its own framework.
	AgentFramework           string
	AgentBackends            []model.AgentBackend
	defaultModel             string
	AgentBackendID           string
	agentCredentialBackendID string
	AgentAuthMode            string
	codexAuthMode            string
	ClaudeAuthMode           string
	// connectionCLIPath is the selected connection's own CLIPath, already
	// copied over AgyPath, ClaudePath or CodexPath by
	// withConnectionCLIPath; kept so a binary that is not there can be
	// blamed on the connection rather than on the flag.
	connectionCLIPath string
	// agyTurnTimeout, agyRetryCutoff and agyConnection are the selected
	// antigravity connection's model-turn limits (model.AgentBackend's
	// TurnLimits; zero for the default) and the name its timeout
	// messages call it by.
	agyTurnTimeout    time.Duration
	agyRetryCutoff    time.Duration
	agyConnection     string
	ExecutorAddr      string
	ExecutorTokenFile string
	ExecutorWait      time.Duration
	ExecutorCertFile  string
	ExecutorKeyFile   string
	executorServer    *modelexec.Server
	// executorListenAddr is where the remote executor listener actually
	// bound (ExecutorAddr resolved, so ":0" says which port it got), or
	// "" when this daemon runs none. It is what the UI's bootstrap
	// command points a worker at (ui.Config.ExecutorAddr).
	executorListenAddr string

	// restartGaps measures how long this process's restart left each of
	// its listeners unreachable (upgrade.GapRecorder), set by run() and
	// nil -- which records nothing -- everywhere else.
	restartGaps *upgrade.GapRecorder
	// runHostUpgrade is the upgrade protocol's hold on the runs in run
	// hosts (runhostupgrade.go): set by run() beside RunHost, nil -- which
	// flushes and reports nothing -- without one.
	runHostUpgrade *runHostUpgrade
	// hostdRuns drives runs' host CLIs through grain hostd, detached
	// (hostdruns.go): set by run() where the host executor is hostd, nil
	// -- every host CLI a plain start -- elsewhere.
	hostdRuns *runhost.Hostd
	// shuttingDown is set once run()'s context is cancelled: the process
	// is draining and its API is about to stop answering. GET
	// /api/config reports it as restarting. nil outside run().
	shuttingDown *atomic.Bool
	// shutdownStarted is run()'s ctx.Done(): closed at the same moment
	// shuttingDown is set, for GET /api/restart-watch to answer on
	// rather than poll. nil outside run().
	shutdownStarted <-chan struct{}
	gitProxyHandler http.Handler

	// agyPath is flags-only, like claudePath below: where a binary lives
	// is a property of the machine, not of the deployment's stored
	// configuration.
	AgyPath string
	// claudePath and claudeOAuthTokenFile are flags-only, like
	// geminiAPIKeyFile -- see -claude-path/-claude-oauth-token-file's own
	// flag doc comments for why neither is store-backed.
	ClaudePath           string
	ClaudeOAuthTokenFile string
	// claudeModel is store-backed (model.Config.ClaudeModel), the exact
	// counterpart of geminiModel above: -claude-model only seeds it the
	// first time a deployment's store has none.
	ClaudeModel string
	// codexPath, openAIAPIKeyFile and codexAuthFile are agent/codex's
	// own half of the two lines above -- flags-only for the same two
	// reasons, a path on this host and secret material -- and codexModel
	// is store-backed like the two models beside it.
	CodexPath        string
	OpenAIAPIKeyFile string
	CodexAuthFile    string
	CodexModel       string
	CodexEffort      string

	// ClaudeRun, CodexRun and AgyRun are -claude-run, -codex-run and
	// -agy-run: where each CLI runs (host, container) when Settings ->
	// Models has not said (clirun.go). CLIRun is that stored setting
	// (model.Config.CLIRun).
	ClaudeRun, CodexRun, AgyRun string
	CLIRun                      map[string]model.CLIRunSetting
	// HostdSocket is -hostd-socket: grain hostd's socket, for a daemon in
	// a container whose host CLIs run through hostd. HostExec is what
	// "host" means for this deployment, detected once at start
	// (DetectHostExecutor); nil is worked out from the rest of Config
	// (hostexec.go).
	HostdSocket string
	HostExec    *HostExecutor
	// HostdRemote is set by -hostd-token-file (or $GRAIN_HOSTD_TOKEN):
	// the session of a grain hostd on another machine that dials in to
	// /api/hostd/connect with that token (hostd -server), through which
	// host CLIs and run: host capability servers start where the daemon
	// has no hostd socket of its own -- on Kubernetes, say.
	HostdRemote *hostd.Remote

	// modelProxyUnrestricted is -model-proxy-unrestricted: the model
	// proxies forward every vendor path rather than only the
	// model-inference endpoints (modelproxy.Proxy.Unrestricted).
	ModelProxyUnrestricted bool
	// ModelProxyMaxConcurrent is -model-proxy-max-concurrent: how many
	// requests one run may have open through each model proxy at once
	// (modelproxy.Proxy.MaxConcurrent). 0 means that package's default.
	ModelProxyMaxConcurrent int
	// ModelProxySlotWait is -model-proxy-slot-wait: how long a request
	// over ModelProxyMaxConcurrent waits for a slot before it is refused
	// (modelproxy.Proxy.SlotWait). 0 means that package's default.
	ModelProxySlotWait time.Duration

	GithubHost         string
	GithubInsecureHTTP bool

	CheckpostURL    string
	CheckpostExpect string

	GithubWebhookURL  string
	GithubWebhookAddr string

	GcpProject             string
	GcpServiceAccountEmail string
	GcpServiceAccountCount int
	IapBackendService      string
	// IapResource is -iap-resource / grain_config.iap_resource: the IAP
	// web resource reconciled alongside IapBackendService; "none"
	// (iapreconciler.ResourceNone) turns that off.
	IapResource string
	// iapClient is an optional IAMClient override for tests.
	IapClient iapreconciler.IAMClient
	// IapWebClient is an optional IAPWebClient override for tests.
	IapWebClient iapreconciler.IAPWebClient

	// upgradeSrcDir, upgradeInstallPath and upgradeRestartCmd configure
	// pkg/upgrade.Upgrader (bwsalmon/agents#396); upgradeSrcDir empty
	// disables it, the same "empty disables" shape gcpProject uses for
	// gcp-key/gemini-key above. upgradeSrcDir is also sourceDir's
	// fallback for the self-debug capability on a deployment with no
	// image to carry its own source.
	UpgradeSrcDir      string
	UpgradeInstallPath string
	UpgradeRestartCmd  []string
	// upgradeImage/upgradeImageRefFile select pkg/upgrade's image path
	// over the build path above -- what an upgrade means on a host that
	// runs grain from a container (bwsalmon/agents#645).
	UpgradeImage        string
	UpgradeImageRefFile string
	// upgradeVerifyCmd checks an image's signature before either image
	// path runs it (upgrade.ImageConfig.VerifyCmd); empty verifies
	// nothing and every upgrade says so.
	UpgradeVerifyCmd []string
	// upgradeAllowedBranches is the only branches either path may upgrade
	// to (upgrade.Config.AllowedBranches); empty means its default.
	UpgradeAllowedBranches []string
	// UpgradeDrainWindow bounds how long a scheduled upgrade waits for active
	// runs to finish before proceeding with the upgrade anyway.
	UpgradeDrainWindow time.Duration

	// rebootCmd is what the UI's reboot-host button runs; empty means
	// rebootHost's own `sudo systemctl reboot` default.
	RebootCmd []string

	// shutdownCmd and restartCmd are the same for the two buttons beside
	// it (grain/task-40): powering the host off, and restarting this
	// daemon alone. Empty means hostCommand's own defaults below.
	ShutdownCmd []string
	RestartCmd  []string

	// ci is whether this daemon runs grain's own CI at all (-ci, default
	// on). It is the operator's switch above each repository's CI mode,
	// not a replacement for it: with it on, a repository still needs its
	// CI mode set to "Use Grain CI" before a single workflow of its runs;
	// with it off, there is no engine, no CI API and no way for a CI mode
	// to turn one on (buildCIService, and ui.Config.CI's own contract
	// that nil means "this deployment has no grain CI").
	Ci              bool
	GritURL         string
	GritClientToken string
	// GritExperimental is -grit-experimental: the opt-in -grit-url is
	// refused without, because the remote grit path runs no workflows
	// yet (design/design/grit-split.design.md, GS-3 to GS-5 deferred).
	GritExperimental bool

	// ciCacheMaxAge, ciCacheMaxBytes, ciCacheMaxEntries and
	// ciCacheMaxEntryBytes are the CI job cache's bounds
	// (ci.DirCacheOptions), read once by ciJobCache when the engine is
	// built. 0 in any of them means the ci package's own default.
	//
	// Seeds and then mirrors, like the CI history's four below: the
	// flags are stored on the config row the first time a deployment
	// starts (toModelConfig, in the columns' own MiB and days), the row
	// wins from then on (withModelConfig, which converts back), and
	// ciStorageBounds is what turns them into the bounds ci.DirCache
	// re-reads on every sweep.
	//
	// They were flags alone until grain/task-317, on two arguments that
	// are worth recording because this is where they were answered.
	//
	// The first was that a bound on the -data-dir disk is a fact about
	// the machine, like -sandbox-dir, rather than about how a deployment
	// works. What changed is that the System pane now reports what each
	// of these is actually holding (ui.CIJobCacheStorage, grain/task-276):
	// an operator reading "200 MiB of 256 MiB" is being shown a ceiling
	// and asked to act on it, and "edit the systemd unit and restart" is
	// the wrong next step -- it is the one that put this deployment into
	// a 52-restart loop once (grain/task-233).
	//
	// The second was that live editing was a promise the cache could not
	// keep, since DirCache read its bounds once at OpenDirCache and
	// making them live meant a store read on the path a finished job
	// takes. It does not: the bounds are re-read through a provider
	// closure (ci.StorageBounds) that reads liveConfig's already-refreshed
	// copy of the row -- a mutex, not a query -- and the row itself is
	// re-read once a reconcile tick like every other live setting. What
	// remains true is that a lowered ceiling frees nothing until the next
	// sweep, within five minutes; `grain ci cache clear` and the button
	// beside it in the CI pane (grain/task-304) are still the answer for
	// an operator who needs the disk back now.
	//
	// ciCacheMaxEntryBytes is the exception and stays a flag alone: it
	// refuses one oversized result rather than sizing the directory, and
	// the Storage section shows no reading against it, so there is
	// nothing in the pane for a setting to sit beside
	// (ci.CacheBounds.MaxEntryBytes).
	CiCacheMaxAge        time.Duration
	CiCacheMaxBytes      int64
	CiCacheMaxEntries    int
	CiCacheMaxEntryBytes int64

	// ciArtifactMaxAge, ciArtifactMaxBytes, ciArtifactMaxRunBytes and
	// ciArtifactMaxEntryBytes are the CI artifact store's bounds
	// (ci.DirArtifactsOptions), read once by ciArtifactStore when the
	// engine is built. 0 in any of them means the ci package's own
	// default.
	//
	// Seeds and then mirrors, exactly as the cache's bounds above are and
	// for the same reasons, down to the per-artifact cap: unlike the
	// cache's per-entry bound, that one is reported in the Storage
	// section beside the rest and is a setting like them
	// (model.Config.CIArtifactFileMB). What they hold is more disposable
	// than a cache entry, not less -- an artifact is evidence about a
	// failure, and the failure itself is in the run either way.
	CiArtifactMaxAge        time.Duration
	CiArtifactMaxBytes      int64
	CiArtifactMaxRunBytes   int64
	CiArtifactMaxEntryBytes int64

	// ciRunHistory, ciStepOutputMB, ciStepOutputDays and ciRunOutputKB
	// are how long this deployment's CI history lives
	// (ci.StoredRunRetention), in the units their flags take them in.
	//
	// Settings rather than flags, unlike the two groups above, and these
	// fields are the seed and then the mirror: the flags are stored on
	// the config row the first time a deployment starts
	// (toModelConfig), the row wins from then on (withModelConfig), and
	// ciRetention is what turns the four into the policy the run store
	// reads afresh on every sweep. They bound the database and the state
	// repository it is exported into rather than a directory under
	// -data-dir, which is the difference that puts them in the pane
	// (grain/task-269): a deployment whose configuration is a pull
	// request has no business editing a systemd unit to keep a log a day
	// longer.
	//
	// 0 in any of them means grain's own default and a negative number
	// means no bound at all -- model.Config.CIRunHistory has the whole
	// of that convention.
	CiRunHistory     int
	CiStepOutputMB   int
	CiStepOutputDays int
	CiRunOutputKB    int

	// ciAgeBoost and ciReserveAfter are how the CI gate orders the jobs
	// it is holding back (model.Limits.CIAgeBoost, ci.SharedGate): how
	// fast a waiting job's rank rises, and how long the budget has to
	// have been refusing one before capacity is held for it.
	//
	// Stored settings with no flag behind them at all, unlike the four
	// above: there is nothing a deployment has to say about its queue
	// before it has a config row, and a window nobody has chosen is
	// pkg/ci's own (0 here means exactly that, and travels through to the
	// gate as 0). Mirrored off the row every reconcile tick like the
	// budget's three, because the gate re-reads them per admission and a
	// job that is queueing now is the one a changed window is for.
	// CiJobsPerRun is an optional ceiling on how many sandboxes a single
	// CI run may hold at once (model.Limits.CIJobsPerRun). 0 means no
	// per-run limit.
	CiJobsPerRun   int
	ciAgeBoost     time.Duration
	CiReserveAfter time.Duration

	DisableNativeSandboxes        bool
	startupDisableNativeSandboxes bool
	// HostSandboxes is -host-sandboxes: every run in a plain directory
	// under SandboxDir, on this host, with no isolation from it
	// (orchestrator.HostSandboxes). AllowUnisolatedHostSandboxes is
	// -allow-unisolated-host-sandboxes, the operator's say-so that a
	// daemon others can reach may run that way anyway
	// (CheckHostSandboxes).
	HostSandboxes                bool
	startupHostSandboxes         bool
	AllowUnisolatedHostSandboxes bool
	// HostSandboxEnv is -host-sandbox-env, repeatable: NAME (this
	// daemon's own value of it) or NAME=VALUE (that value), added to a
	// host sandbox's run_command environment on top of mcp's allowlist
	// (ResolveHostSandboxEnv). Only with HostSandboxes.
	HostSandboxEnv        []string
	ContainerSandboxes    bool
	ContainerImage        string
	ContainerGitProxyHost string
	ContainerNetwork      string
	ContainerCapAdd       []string
	// sandboxCIDR and sandboxNetwork are the address pool every granule
	// sandbox VM is given an address out of and the docker network those
	// addresses are on -- see -granule-sandbox-cidr. An empty CIDR is every
	// deployment before this existed: no address is named and docker
	// assigns one.
	//
	// Distinct from containerNetwork above, which belongs to the local
	// Docker backend: two backends, two networks, worth unifying once both
	// have settled.
	SandboxCIDR    string
	SandboxNetwork string
	// sandboxPortBase is the first port of the block every sandbox-facing
	// service binds in -- see -sandbox-port-base, and sandboxnet.go for
	// the offsets within it. 0 means no block: ephemeral ports, as before.
	SandboxPortBase int

	// granuleSandboxes selects orchestrator.GranuleSandboxes over the
	// default orchestrator.HostSandboxes when non-empty; the rest of the
	// granule* fields are only consulted then. See run()'s own comment on
	// sandboxes.
	GranuleSandboxes bool
	GranuleStateDir  string
	// LocalScripts is -local-scripts, repeatable NAME=/absolute/path
	// entries naming host-managed local-scripts declarations
	// (localscripts.Config.Host). Every other declaration is read from
	// the state repository's checkout (LocalScriptsConfig).
	LocalScripts []string
	// RunHostImage is -run-host-image: when set, claude runs are driven
	// in run hosts of this image (NewRunHost) rather than as children of
	// the daemon. RunHostCredentialDir is -run-host-credential-dir, the
	// shared credential directory every run host mounts read-write.
	// RunHost is what NewRunHost built from them, set once the store is
	// open; nil leaves every run under agent.LocalHost.
	RunHostImage         string
	RunHostCredentialDir string
	RunHost              *runhost.Host
	// RunHostDir is -run-host-dir, where the spool and staging roots go
	// (<data dir>/runhosts when empty). RunHostDaemonURL is
	// -run-host-daemon-url, the API address a run host's mcpserver is
	// given in place of this process's own listen address.
	RunHostDir       string
	RunHostDaemonURL string
	// K8sRunHostVolumes (-k8s-run-host-volume, CLAIM:PATH),
	// K8sRunHostRWX and K8sRunHostServiceAccount configure the
	// Kubernetes run-host driver (runhost.Kubernetes), which NewRunHost
	// uses in place of docker under -k8s-sandboxes.
	K8sRunHostVolumes        []string
	K8sRunHostRWX            bool
	K8sRunHostServiceAccount string
	GranuleCommand           granule.Command
	GranuleSSHUser           string
	GranuleExecKey           string
	GranuleWorkspace         string
	// granuleReadyTimeout is -granule-ready-timeout: 0 leaves
	// GranuleConfig's own built-in default.
	GranuleReadyTimeout time.Duration
	GranuleCreateArgs   []string
	GranuleNet          string
	// sandboxImage is -sandbox-image: the sandbox container this
	// deployment pins on purpose, empty on one that lets this build's own
	// stamp answer for it. See chooseSandboxImage, which is where the two
	// -- and the third case, a pin an older install left behind -- are
	// told apart.
	SandboxImage    string
	GranuleBaseIP   string
	GranuleBasePort int
	// granuleGitProxyHost is the address startGitProxy advertises to a
	// granule-managed sandbox in place of the loopback address it binds to
	// by default -- see -granule-git-proxy-host's own flag doc comment for
	// why a granule VM cannot reach that default (bwsalmon/agents#567).
	// Required whenever granuleSandboxes is set; unused otherwise.
	GranuleGitProxyHost string
	// CustomSandboxImage is model.Config.SandboxImage: the custom OCI
	// image this installation names in Settings for new sandboxes on the
	// active backend, built FROM guest-base or k8s-sandbox-base. Not
	// SandboxImage above, which is the -sandbox-image flag and wins over
	// this one (customSandboxImageFor).
	//
	// Store-backed and store-backed only -- there is no flag behind it --
	// and live: liveConfig hands each change to the sandbox backend, so it
	// reaches the next sandbox created.
	CustomSandboxImage string
	// SandboxImageMirror is -sandbox-image-mirror / model.Config.SandboxImageMirror:
	// an optional OCI repository (for example,
	// us-docker.pkg.dev/<cluster-project>/grain/guest) that off-cluster
	// deployments mirror the build-stamped sandbox guest image into on
	// upgrade and startup so a cluster in another GCP organization can
	// pull the exact commit-matched guest image from its own registry.
	SandboxImageMirror string
	// packageCacheEnabled is model.Config's own field of the same name:
	// whether each sandbox this deployment builds is pointed at the
	// controller's package cache (pkg/pkgcache). Live -- re-read
	// per dispatch through liveConfig, so the switch in Settings reaches
	// the next run.
	PackageCacheEnabled bool
	// packageCacheDir is the root directory for the package cache.
	// Empty means default: <sandbox-dir>/cache/packages if sandboxDir is set,
	// otherwise <data-dir>/cache/packages.
	PackageCacheDir string
	// packageCacheGB is how much disk that cache may fill before it
	// starts evicting. A flag rather than a stored setting; see the
	// -package-cache-size-gb flag's own doc comment.
	PackageCacheGB int
	// packageCacheAptUpstream is one "archive-host=base-url" entry per
	// -package-cache-apt-upstream: an archive this cache serves, and the
	// mirror its fetches are actually made from. Empty on a deployment
	// that fetches each archive from itself, which is most of them.
	// checkPackageCacheUpstreams is what refuses a malformed one, and
	// packageCacheUpstreamsFromConfig is what carries these seven into
	// pkgcache.Config.
	PackageCacheAptUpstream []string
	// packageCachePyPIIndex, packageCacheNPMRegistry,
	// packageCacheGoProxy, packageCacheDockerRegistry,
	// packageCacheGHCRRegistry and packageCacheGCRRegistry are the same
	// fact for the other six ecosystems: the index, registry or proxy
	// this cache fills itself from, empty meaning the public one. See
	// the flags' own help text.
	PackageCachePyPIIndex      string
	PackageCacheNPMRegistry    string
	PackageCacheGoProxy        string
	PackageCacheDockerRegistry string
	PackageCacheGHCRRegistry   string
	PackageCacheGCRRegistry    string
	// actionsCacheDir is the root directory for the Actions cache.
	// Empty means default: <sandbox-dir>/cache/actions if sandboxDir is set,
	// otherwise <data-dir>/cache/actions.
	ActionsCacheDir string
	// actionsCacheGB is the same fact about the Actions cache
	// (pkg/actionscache): how much disk it may fill, 0 meaning grain's
	// own default. Store-backed and live, like packageCacheGB's stored
	// twin: the flag seeds it and grain_config wins from then on, and a
	// change reaches the running cache through SetMaxBytes on the next
	// reconcile tick. See -actions-cache-size-gb.
	ActionsCacheGB int
	// actionsCacheEnabled is model.Config's own field of the same name:
	// whether each CI job this deployment dispatches is handed a URL for
	// that cache. Live -- re-read per job through liveConfig, so the
	// switch in Settings reaches the next job -- and the one decision
	// here that is about trust rather than disk: every branch of a
	// repository shares that repository's entries.
	ActionsCacheEnabled bool

	// k8sSandboxes selects orchestrator.K8sSandboxes -- one Kubernetes
	// pod per run, reached with `kubectl exec` -- over the default
	// orchestrator.HostSandboxes; the rest of the k8s* fields are only
	// consulted then. See run()'s own comment on sandboxes.
	//
	// It is also what puts this process into "grain runs inside a
	// cluster" mode more broadly: the host-facing controls that assume a
	// machine this daemon owns (reboot, power off, the `top` snapshot,
	// the host's own disk and load figures) are not
	// wired at all under it, because none of them means anything from
	// inside a pod -- see kubernetesMode.
	K8sSandboxes      bool
	K8sNamespace      string
	K8sKubectl        string
	K8sKubeconfig     string
	K8sImage          string
	K8sSandboxImage   string
	K8sServiceAccount string
	K8sWorkspace      string
	K8sNodeSelector   []string
	K8sPrivileged     bool
	K8sNetworkPolicy  bool
	// k8sDeployment and k8sContainer name what an upgrade repoints:
	// grain's own Deployment and the container within it. Only consulted
	// when k8sSandboxes and upgradeImage are both set, which is what
	// selects upgrade.ModeKube (newUpgrader).
	K8sDeployment string
	K8sContainer  string
	// K8sPriorityClasses names the PriorityClass each kind of sandbox
	// pod runs at (model.Config.K8sPriorityClassInteractive and its
	// siblings); a "" field sets none on that kind's pods.
	K8sPriorityClasses orchestrator.K8sPriorityClasses
	// k8sGitProxyHost is the address startGitProxy advertises to a
	// sandbox pod in place of the loopback address it binds to by
	// default -- k8sGitProxyHostOrPodIP resolves it from $GRAIN_POD_IP
	// when the flag names nothing. Required whenever k8sSandboxes is
	// set; unused otherwise.
	K8sGitProxyHost string
	// K8sSandboxService names a ClusterIP Service in front of this
	// Deployment's pods carrying the git proxy's and model proxies'
	// ports. When set, sandboxes are handed its cluster IP for those
	// (K8sSandboxServiceHost) rather than K8sGitProxyHost, so that a
	// sandbox outliving this pod still reaches them. Empty keeps every
	// service on this pod's own address.
	K8sSandboxService string

	// k8sGranuleSandboxes selects orchestrator.K8sGranuleSandboxes --
	// one privileged pod per run, each booting a real cloud-hypervisor
	// VM over /dev/kvm, scheduled by the cluster instead of built on
	// this process's own host -- in place of either k8sSandboxes
	// (plain, unprivileged pods) or granuleSandboxes (VMs on a single
	// docker host). See docs/k8s-granule-design.md for why this is a
	// separate backend from k8sSandboxes rather than a flag on it: the
	// two platforms' pods disagree about whether privileged is allowed
	// at all.
	//
	// It reuses every k8s* field above that means the same thing on
	// either platform -- which cluster (k8sNamespace, k8sKubectl,
	// k8sContext/k8sKubeconfig where those exist), which Deployment an
	// upgrade would repoint (k8sDeployment, k8sContainer, though see
	// checkConfig's own refusal: this platform's self-upgrade is not
	// built yet), the guest's working directory (k8sWorkspace), its
	// node pool (k8sNodeSelector) and the address a guest reaches this
	// process at (k8sGitProxyHost) -- and adds only what is genuinely
	// different: the image (k8sGranuleImage is a self-contained packed
	// guest image with its own disk baked in, not the thin exec target
	// k8sImage names, and not "granule:vendored" either -- see
	// k8sGranuleImage's own doc comment below) and nothing about
	// -k8s-network-policy or
	// -k8s-sandbox-privileged, neither of which means anything here (a
	// granule pod is always privileged, and its network isolation is
	// the guest's own missing network device, not a NetworkPolicy
	// object -- K8sGranuleSandboxes.IsolatesNetwork).
	K8sGranuleSandboxes bool
	// k8sGranuleImage is the packed guest image every pod this backend
	// creates runs: a self-contained image with its own guest disk and
	// kernel already baked in (docs/k8s-granule-design.md §4), not the
	// plain sandbox image k8sImage names, and not
	// hostinit.DefaultGranuleRuntimeImage ("granule:vendored") either,
	// which has no guest disk and cannot boot at all. Required whenever
	// k8sGranuleSandboxes is set; there is no context-free default this
	// package could pick, the same reason k8sImage's own CI-stamped
	// default does not extend here (nothing publishes a packed guest
	// image yet).
	K8sGranuleImage string
	// SandboxAccessAddr, when set with K8sGranuleSandboxes, runs the
	// sandbox-access acceptor there and gives every sandbox pod
	// dial-out credentials (sandbox_access.go). SandboxAccessDial is
	// what wrappers dial (grain-access.<ns>.svc:4244 when empty), and
	// SandboxAccessCASecret the Secret Manager secret the CA is kept in
	// (a file in DataDir when empty). Startup-only, not store-backed.
	SandboxAccessAddr     string
	SandboxAccessDial     string
	SandboxAccessCASecret string
	// K8sGranuleAccess is how grain reaches a k8s-granule sandbox's guest
	// once its pod is Ready: orchestrator.K8sGranuleAccessKubectl (the
	// default, `granule exec` over `kubectl exec`) or
	// orchestrator.K8sGranuleAccessDialout (the wrapper's dial-out
	// channel, which needs SandboxAccessAddr and an in-cluster daemon).
	// K8sGranuleReattachTimeout is how long a step waits for a dropped
	// channel to come back (DefaultK8sGranuleReattachTimeout when zero).
	// design/design/grain-on-gke.design.md#GKE-3. Startup-only.
	K8sGranuleAccess          string
	K8sGranuleReattachTimeout time.Duration

	// sandboxCPUs, sandboxMemoryMB and sandboxDiskGB are store-backed
	// (model.Config.SandboxCPUs/SandboxMemoryMB/SandboxDiskGB,
	// bwsalmon/agents#534 and grain/task-41),
	// like poll-interval and the rest of the seedOnly flags above --
	// only consulted with -granule-sandboxes or -k8s-sandboxes, the two
	// backends that build a sandbox with a size of its own.
	SandboxCPUs     int
	SandboxMemoryMB int
	SandboxDiskGB   int
	// networkAccess is the deployment-wide default for how much of the
	// internet a run's sandbox reaches (model.Config.NetworkAccess,
	// grain/task-269) -- store-backed like the three above, but with no
	// flag of its own: there is nothing an operator would want to say
	// about it at startup that Settings cannot say better, and a
	// deployment that has never chosen gets model.NetworkOpen, which is
	// the network its sandboxes already had.
	networkAccess model.NetworkAccess
}

// limits is the sandbox pool this deployment is rationing right now --
// current().limits(), and so re-read rather than pinned: -max-workers is
// live-editable in Settings, and a gate holding a job against a number
// this process was started with would keep CI's share at whatever the
// deployment used to be.
//
// Safe to call from any goroutine, like current() itself, which is what
// lets a CI job ask it from inside the engine.
func (l *LiveConfig) limits() model.Limits { return l.Current().Limits() }

// ciRetention is how long this deployment keeps its CI history right now
// -- current().ciRetention(), and so re-read rather than pinned, which
// is what lets an operator widen it in Settings without restarting the
// daemon (grain/task-269). ci.StoredRunStore calls it on every sweep and
// every listing, from whichever goroutine concluded a run, which is safe
// for the reason limits() above is.
func (l *LiveConfig) ciRetention() ci.StoredRunRetention { return l.Current().ciRetention() }

// ciStorageBounds is what CI's job cache and artifact store may occupy
// right now -- current().ciStorageBounds(), and so re-read rather than
// pinned, for the reason ciRetention above is. This is the provider
// ci.DirCache and ci.DirArtifacts are handed, and they call it on every
// sweep, every Save and every Stats, so a ceiling saved in the Settings
// pane is what the next sweep holds the directory within.
func (l *LiveConfig) ciStorageBounds() ci.StorageBounds { return l.Current().CiStorageBounds() }

// diskFloor is how much of each volume this deployment is keeping free
// right now -- current().diskFloor(), and so re-read rather than pinned,
// which is what lets an operator lower it from the Settings pane while a
// filling disk is holding every dispatch (grain/task-372). The disk
// watcher asks per reading and the host pane per poll, from whichever
// goroutine took it, which is safe for the reason limits() above is.
func (l *LiveConfig) diskFloor() model.DiskFloor { return l.Current().DiskFloor() }

// changesFrom names every store-backed setting that differs between this
// config and the one previously in effect, in the same "-flag: old ->
// new" vocabulary logStoreOverrides uses -- so one log line matches what
// an operator just changed in Settings.
//
// github-host and github-insecure-http are deliberately absent: they can
// never differ here, since withLiveModelConfig never adopts them.
func (c Config) ChangesFrom(prev Config) []string {
	var changes []string
	note := func(name string, from, to any) {
		if fmt.Sprint(from) != fmt.Sprint(to) {
			changes = append(changes, fmt.Sprintf("%s %v -> %v", name, from, to))
		}
	}
	note("poll-interval", prev.PollInterval, c.PollInterval)
	note("max-workers", prev.MaxWorkers, c.MaxWorkers)
	note("max-mergers", prev.MaxMergers, c.MaxMergers)
	note("max-ci-jobs", prev.MaxCIJobs, c.MaxCIJobs)
	note("agent-framework", prev.AgentFramework, c.AgentFramework)
	note("default-model", prev.defaultModel, c.defaultModel)
	note("gemini-model", prev.GeminiModel, c.GeminiModel)
	note("gemini-effort", prev.GeminiEffort, c.GeminiEffort)
	note("claude-model", prev.ClaudeModel, c.ClaudeModel)
	note("codex-model", prev.CodexModel, c.CodexModel)
	note("codex-effort", prev.CodexEffort, c.CodexEffort)
	note("max-agent-turns", prev.MaxAgentTurns, c.MaxAgentTurns)
	note("gcp-project", prev.GcpProject, c.GcpProject)
	note("gcp-agent-service-account", prev.GcpServiceAccountEmail, c.GcpServiceAccountEmail)
	note("gcp-agent-account-count", prev.GcpServiceAccountCount, c.GcpServiceAccountCount)
	note("iap-backend-service", prev.IapBackendService, c.IapBackendService)
	note("iap-resource", prev.IapResource, c.IapResource)
	note("sandbox-cpus", prev.SandboxCPUs, c.SandboxCPUs)
	note("sandbox-memory-mb", prev.SandboxMemoryMB, c.SandboxMemoryMB)
	note("sandbox-disk-gb", prev.SandboxDiskGB, c.SandboxDiskGB)
	// Written with the reading a sandbox actually gets rather than with
	// the stored value, so "" -> "open" is the non-change it is and a
	// journal line about this setting always names a network.
	note("sandbox-network-access",
		model.NormalizeNetworkAccess(prev.networkAccess), model.NormalizeNetworkAccess(c.networkAccess))
	note("sandbox-image", orStampedImage(prev.CustomSandboxImage), orStampedImage(c.CustomSandboxImage))
	note("sandbox-image-mirror", prev.SandboxImageMirror, c.SandboxImageMirror)
	note("package-cache-size-gb", prev.PackageCacheGB, c.PackageCacheGB)
	note("actions-cache-size-gb", prev.ActionsCacheGB, c.ActionsCacheGB)
	// Worth a journal line where the package cache's own switch is not:
	// turning this one on is a deployment deciding that one branch's CI
	// may read what another branch's CI wrote, which is the kind of
	// thing somebody reading a journal months later wants dated.
	note("actions-cache", prev.ActionsCacheEnabled, c.ActionsCacheEnabled)
	note("k8s-namespace", prev.K8sNamespace, c.K8sNamespace)
	note("k8s-kubectl", prev.K8sKubectl, c.K8sKubectl)
	note("k8s-sandbox-image", firstNonEmpty(prev.K8sSandboxImage, prev.K8sImage), firstNonEmpty(c.K8sSandboxImage, c.K8sImage))
	note("k8s-granule-image", prev.K8sGranuleImage, c.K8sGranuleImage)
	note("k8s-workspace", prev.K8sWorkspace, c.K8sWorkspace)
	note("k8s-git-proxy-host", prev.K8sGitProxyHost, c.K8sGitProxyHost)
	note("k8s-sandbox-service", prev.K8sSandboxService, c.K8sSandboxService)
	note("k8s-sandbox-service-account", prev.K8sServiceAccount, c.K8sServiceAccount)
	note("k8s-node-selector", prev.K8sNodeSelector, c.K8sNodeSelector)
	note("k8s-sandbox-privileged", prev.K8sPrivileged, c.K8sPrivileged)
	note("k8s-network-policy", prev.K8sNetworkPolicy, c.K8sNetworkPolicy)
	note("k8s-deployment", prev.K8sDeployment, c.K8sDeployment)
	note("k8s-container", prev.K8sContainer, c.K8sContainer)
	note("k8s-priority-class-interactive", prev.K8sPriorityClasses.Interactive, c.K8sPriorityClasses.Interactive)
	note("k8s-priority-class-agent", prev.K8sPriorityClasses.Agent, c.K8sPriorityClasses.Agent)
	note("k8s-priority-class-ci", prev.K8sPriorityClasses.CI, c.K8sPriorityClasses.CI)
	return changes
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// orStampedImage is a custom sandbox image as a log line names it, with
// none configured read as the stamped default it then is.
func orStampedImage(image string) string {
	if strings.TrimSpace(image) == "" {
		return "stamped default"
	}
	return image
}

// loadConfig resolves cfg's store-backed fields (bwsalmon/agents#320)
// against store's own grain_config row: on a database with no row yet --
// a fresh -data-dir, or a store no daemon has ever started against --
// flagCfg's own values (the daemon's flags, as parsed) are the seed,
// written once so a UI or a CLI has something to read and edit; on a
// database that already has a row, every field below is read back from
// it instead, discarding whatever the flags said. That is deliberate,
// not a bug: bwsalmon/agents#320 asked for the store to become the
// record the same way it already is for tasks (README, "Input is a
// model update, not a GitHub issue"), and a flag whose value silently
// depended on how many times the daemon had already started would be a
// worse surprise than one that is simply ignored after the first.
//
// This reads grain_config exactly once, at startup, and is only the
// starting point: liveConfig re-reads the same row once per reconcile
// tick and applies whatever a running daemon can apply (its own doc
// comment lists which settings that is, and which two are left needing a
// restart), so almost every field below now matters only as the value a
// fresh database is seeded with, or as the fallback for a row that has
// no value of its own for it.
//
// Only the fields with no bearing on reaching the store in the first
// place move through here: -data-dir and -gemini-api-key-file (a
// secret, not configuration -- bwsalmon/agents#320's own "but not the
// secrets") stay flags-only, read by run() before loadConfig has a store
// to call.
func LoadConfig(ctx context.Context, store *model.Store, flagCfg Config) (Config, error) {
	stored, err := store.GetConfig(ctx)
	if err != nil {
		return Config{}, fmt.Errorf("reading stored configuration: %w", err)
	}
	if stored == nil || stored.ConnectionsOnly() {
		seed := flagCfg.ToModelConfig()
		if stored != nil {
			// `grain connections import` ran before this first start:
			// keep what it recorded (model.Config.ConnectionsOnly).
			seed = seed.WithConnectionsOf(*stored)
		}
		if err := store.PutConfig(ctx, seed); err != nil {
			return Config{}, fmt.Errorf("seeding stored configuration from flags: %w", err)
		}
		if stored != nil {
			return flagCfg.WithModelConfig(seed), nil
		}
		return flagCfg, nil
	}
	runnable, notes := flagCfg.RunnableStoredConfig(*stored)
	for _, note := range notes {
		log.Printf("loadConfig: %s", note)
	}
	// The substituted row, not the stored one, so that a value this
	// process is *not* running with is never reported as one it preferred
	// over a flag: "ignoring -poll-interval=30s, stored config already
	// has 0s" was true of the old behaviour and is exactly backwards
	// under this one.
	flagCfg.logStoreOverrides(runnable)
	return flagCfg.WithModelConfig(runnable), nil
}

// runnableStoredConfig returns mc with every stored value this process
// cannot actually run on replaced by the flag value it started with, plus
// a line about each substitution worth telling an operator about.
//
// The poll interval is what made this necessary (grain/task-316). A
// grain_config row storing 0 there starts a daemon that looks healthy and
// does nothing: time.NewTicker panics on a non-positive interval, so the
// reconcile loop dies on its way into its first sleep while the UI/API
// server started before it stays up and keeps serving (run's own
// goroutine, and reconcilerDown beside it). Worse, the flag that would
// have supplied a usable number is deliberately ignored on the way past,
// because a stored value beats a flag once a row exists (loadConfig) --
// so the deployment is in a browser, looks alive, dispatches nothing, and
// the one line explaining why is a warning about the flag it declined to
// use.
//
// Reading such a value as "nobody has chosen one" rather than as a choice
// somebody made is what already happens on every later tick -- this is
// the whole of what withLiveModelConfig used to do inline -- and it is
// the reading the value deserves. ui.UpdateSettings refuses to store a
// non-positive interval or a max-workers below 1, so a row carrying one
// can only have come from somewhere with no validation in front of it: a
// settings pull request against the state repository (whose import writes
// rows, not settings), a hand-edited database, an older build's column,
// or model.DefaultConfig's own zeroes, which are "nothing chosen" rather
// than a decision. checkConfig has already refused a flag that is not
// runnable either, so what this falls back to is always something a
// ticker can be built from and a cycle can dispatch into.
//
// `grain state check` says the same thing about the same row before it is
// ever merged (statecheck.UnrunnableSettings), which is where a settings
// pull request is meant to find out.
func (c Config) RunnableStoredConfig(mc model.Config) (model.Config, []string) {
	var notes []string
	note := func(setting, flag string, stored, flagVal any) {
		notes = append(notes, fmt.Sprintf("stored %s is %v, which no daemon can run on -- "+
			"running with -%s=%v instead; store one grain can use with 'grain settings' or the Settings pane",
			setting, stored, flag, flagVal))
	}
	if mc.PollInterval <= 0 {
		note("poll interval", "poll-interval", mc.PollInterval, c.PollInterval)
		mc.PollInterval = c.PollInterval
	}
	// max_workers, max_mergers and max_ci_jobs are deliberately not
	// checked: they are legacy columns that schedule nothing
	// (grain/task-28), so a row carrying 0, or 300, is not a row "no
	// daemon can run on" any more -- it is a row with a number in it that
	// nobody reads.
	// A negative overcommit ratio is not a ratio, and neither dimension
	// of the budget can be built from one: model.Budget reads anything
	// at or below zero as "unenforced", so a row carrying -2 would
	// quietly take the bound off a dimension an operator believes is
	// bounded. Zero itself is left alone -- that one is a deliberate
	// "bound this by the run counts alone", which is what every
	// deployment did before the budget existed (grain/task-197).
	if mc.SandboxCPUOvercommit < 0 {
		note("sandbox cpu overcommit", "sandbox-cpu-overcommit", mc.SandboxCPUOvercommit, c.CpuOvercommit)
		mc.SandboxCPUOvercommit = c.CpuOvercommit
	}
	if mc.SandboxMemoryOvercommit < 0 {
		note("sandbox memory overcommit", "sandbox-memory-overcommit", mc.SandboxMemoryOvercommit, c.MemoryOvercommit)
		mc.SandboxMemoryOvercommit = c.MemoryOvercommit
	}
	if mc.SandboxDiskOvercommit < 0 {
		note("sandbox disk overcommit", "sandbox-disk-overcommit", mc.SandboxDiskOvercommit, c.DiskOvercommit)
		mc.SandboxDiskOvercommit = c.DiskOvercommit
	}
	// And a reserve larger than the machine leaves a memory budget of
	// nothing, which admits one sandbox at a time and queues everything
	// behind it (Budget.Admits' Sole rule). ui.UpdateSettings refuses to
	// store one, so a row holding it came from somewhere with no
	// validation in front of it -- and this daemon is the only reader
	// that knows how big the host actually is.
	if c.HostMemoryMB > 0 && mc.SandboxMemoryReserveMB >= c.HostMemoryMB {
		notes = append(notes, fmt.Sprintf("stored sandbox memory reserve is %d MiB of a %d MiB host, which leaves no sandbox "+
			"memory at all -- running with grain's own reserve instead; store one grain can use with 'grain settings' or the Settings pane",
			mc.SandboxMemoryReserveMB, c.HostMemoryMB))
		mc.SandboxMemoryReserveMB = model.ReserveMemoryDefault
	}
	if mc.SandboxBackend != "" {
		if !model.ValidSandboxBackend(mc.SandboxBackend) {
			notes = append(notes, fmt.Sprintf("stored sandbox backend is %q, which this daemon cannot build -- "+
				"running with %q instead; choose one of the available backends in the Settings pane",
				mc.SandboxBackend, c.SandboxBackend()))
			mc.SandboxBackend = c.SandboxBackend()
		} else if ok, reason := c.sandboxBackendAvailability(mc.SandboxBackend); !ok {
			notes = append(notes, fmt.Sprintf("stored sandbox backend is %q, which this daemon cannot build (%s) -- "+
				"running with %q instead; choose one of the available backends in the Settings pane",
				mc.SandboxBackend, reason, c.SandboxBackend()))
			mc.SandboxBackend = c.SandboxBackend()
		}
	}
	for _, entry := range mc.K8sNodeSelector {
		if !strings.Contains(entry, "=") {
			note("k8s node selector", "k8s-node-selector", mc.K8sNodeSelector, c.K8sNodeSelector)
			mc.K8sNodeSelector = append([]string(nil), c.K8sNodeSelector...)
			break
		}
	}
	// The rest are substituted silently, because for them an unusable
	// value is the ordinary case rather than a settings mistake: a row
	// written before a column existed reads that column back as its zero
	// value, and every deployment upgrading across the migration that
	// added one does that exactly once. "" is not a model to call, nor a
	// framework any agent.Framework switch can match on -- and the flag
	// defaults behind each of them are what a
	// deployment that has never chosen has always run with. A line per
	// setting per start (and, through withLiveModelConfig, per tick)
	// would be noise about something nobody did wrong.
	return mc, notes
}

// logStoreOverrides logs one line for every seedOnly flag whose
// command-line value doesn't match what's already stored -- withModelConfig
// is about to silently prefer the stored value over it. Without this, an
// operator re-running scripts/setup.sh (or `grain daemon` by hand) with
// a changed flag sees no effect and no explanation until they find `grain
// settings`, the actual way to change a seeded value (bwsalmon/agents#574).
func (c Config) logStoreOverrides(mc model.Config) {
	warn := func(flag string, flagVal, storedVal any) {
		if fmt.Sprint(flagVal) != fmt.Sprint(storedVal) {
			log.Printf("loadConfig: ignoring -%s=%v, stored config already has %v -- use 'grain settings' to change it", flag, flagVal, storedVal)
		}
	}
	warn("poll-interval", c.PollInterval, mc.PollInterval)
	warn("max-agent-turns", c.MaxAgentTurns, mc.MaxAgentTurns)
	warn("github-host", c.GithubHost, mc.GitHubHost)
	warn("github-insecure-http", c.GithubInsecureHTTP, mc.GitHubInsecureHTTP)
	warn("gcp-project", c.GcpProject, mc.GCPProject)
	warn("gcp-agent-service-account", c.GcpServiceAccountEmail, mc.GCPServiceAccountEmail)
	warn("gcp-agent-account-count", c.GcpServiceAccountCount, mc.GCPServiceAccountCount)
	warn("iap-backend-service", c.IapBackendService, mc.IAPBackendService)
	warn("iap-resource", c.IapResource, mc.IAPResource)
	warn("sandbox-cpus", c.SandboxCPUs, mc.SandboxCPUs)
	warn("sandbox-memory-mb", c.SandboxMemoryMB, mc.SandboxMemoryMB)
	warn("sandbox-disk-gb", c.SandboxDiskGB, mc.SandboxDiskGB)
	// The two the disk floor stopped being flags as of (grain/task-372).
	// Worth a line each precisely because they were flags for so long:
	// an operator whose deployment is holding work over a full volume is
	// likely to reach for the unit file first, and this says where the
	// setting actually lives before they restart the daemon into exactly
	// the same behaviour.
	warn("sandbox-disk-floor-share", c.DiskFloorShare, mc.SandboxDiskFloorShare)
	warn("sandbox-disk-floor-min-mb", c.DiskFloorMinMB, mc.SandboxDiskFloorMinMB)
	warn("package-cache-size-gb", c.PackageCacheGB, mc.PackageCacheSizeGB)
	warn("actions-cache-size-gb", c.ActionsCacheGB, mc.ActionsCacheSizeGB)
	// The one flag here whose old meaning was not a number: -actions-cache-
	// size-gb 0 used to be how a deployment switched the Actions cache
	// off altogether (grain/task-302), and it is a stored switch now.
	// An operator who still passes it on a deployment with a row is
	// otherwise told only that a size was ignored, and would be left
	// believing their CI jobs share no cache when they do.
	if c.ActionsCacheGB <= 0 && mc.ActionsCacheEnabled {
		log.Printf("loadConfig: -actions-cache-size-gb=0 no longer switches the Actions cache off -- " +
			"stored config has it on, so every CI job is handed it and every branch of a repository " +
			"shares one cache. Turn it off in Settings -> Sandbox ('Actions cache') or with " +
			"'grain settings -actions-cache=false'")
	}
	warn("ci-run-history", c.CiRunHistory, mc.CIRunHistory)
	warn("ci-step-output-mb", c.CiStepOutputMB, mc.CIStepOutputMB)
	warn("ci-step-output-days", c.CiStepOutputDays, mc.CIStepOutputDays)
	warn("ci-run-output-kb", c.CiRunOutputKB, mc.CIRunOutputKB)
	// The two CI disk groups' seven, compared as bounds rather than as
	// the raw numbers on either side: the flags are bytes and a duration
	// while the columns are MiB and days, and both sides spell "grain's
	// own default" as a zero, so resolving both is what makes a line here
	// mean "this unit file asked for something else" rather than "the row
	// has not chosen" (grain/task-317).
	flagBounds, storedBounds := c.CiStorageBounds().Resolved(), ci.StorageBoundsFrom(mc).Resolved()
	warn("ci-cache-max-bytes", flagBounds.Cache.MaxBytes, storedBounds.Cache.MaxBytes)
	warn("ci-cache-max-entries", flagBounds.Cache.MaxEntries, storedBounds.Cache.MaxEntries)
	warn("ci-cache-max-age", flagBounds.Cache.MaxAge, storedBounds.Cache.MaxAge)
	warn("ci-artifact-max-bytes", flagBounds.Artifacts.MaxBytes, storedBounds.Artifacts.MaxBytes)
	warn("ci-artifact-max-run-bytes", flagBounds.Artifacts.MaxRunBytes, storedBounds.Artifacts.MaxRunBytes)
	warn("ci-artifact-max-entry-bytes", flagBounds.Artifacts.MaxArtifactBytes, storedBounds.Artifacts.MaxArtifactBytes)
	warn("ci-artifact-max-age", flagBounds.Artifacts.MaxAge, storedBounds.Artifacts.MaxAge)
	warn("k8s-namespace", firstNonEmpty(c.K8sNamespace, model.DefaultK8sNamespace), firstNonEmpty(mc.K8sNamespace, model.DefaultK8sNamespace))
	warn("k8s-kubectl", c.K8sKubectl, mc.K8sKubectl)
	warn("k8s-sandbox-image", firstNonEmpty(c.K8sSandboxImage, c.K8sImage, DefaultK8sSandboxImage), firstNonEmpty(mc.K8sSandboxImage, DefaultK8sSandboxImage))
	warn("k8s-granule-image", firstNonEmpty(c.K8sGranuleImage, DefaultSandboxImage), firstNonEmpty(mc.K8sGranuleImage, DefaultSandboxImage))
	warn("sandbox-image-mirror", c.SandboxImageMirror, mc.SandboxImageMirror)
	warn("k8s-workspace", c.K8sWorkspace, mc.K8sWorkspace)
	warn("k8s-git-proxy-host", c.K8sGitProxyHost, mc.K8sGitProxyHost)
	warn("k8s-sandbox-service", c.K8sSandboxService, mc.K8sSandboxService)
	warn("k8s-sandbox-service-account", c.K8sServiceAccount, mc.K8sServiceAccount)
	warn("k8s-node-selector", c.K8sNodeSelector, mc.K8sNodeSelector)
	warn("k8s-sandbox-privileged", c.K8sPrivileged, mc.K8sPrivileged)
	warn("k8s-network-policy", c.K8sNetworkPolicy, mc.K8sNetworkPolicy)
	warn("k8s-deployment", firstNonEmpty(c.K8sDeployment, model.DefaultK8sDeployment), firstNonEmpty(mc.K8sDeployment, model.DefaultK8sDeployment))
	warn("k8s-container", firstNonEmpty(c.K8sContainer, model.DefaultK8sContainer), firstNonEmpty(mc.K8sContainer, model.DefaultK8sContainer))
	warn("k8s-priority-class-interactive", c.K8sPriorityClasses.Interactive, mc.K8sPriorityClassInteractive)
	warn("k8s-priority-class-agent", c.K8sPriorityClasses.Agent, mc.K8sPriorityClassAgent)
	warn("k8s-priority-class-ci", c.K8sPriorityClasses.CI, mc.K8sPriorityClassCI)
}

// limits is this configuration's whole sandbox pool, as everything that
// rations one reads it: the resource budget and the live ledger that
// dispatch admits agent runs against (through orchestrator.Deps) and that
// grain CI admits its jobs against (buildCIService's own gate). One
// method so those two can never be assembled differently -- which is the
// point, since sharing one budget and one ledger is the whole of
// grain/task-28.
//
// A deployment with -ci=false needs nothing special here any more. It used
// to matter: CI's count was zeroed so the agent pool would not reserve
// capacity for an engine that did not exist. Nothing is reserved for
// anything now, so a deployment with no CI simply never charges the
// ledger for a job.
func (c Config) Limits() model.Limits {
	return model.Limits{
		Workers: c.MaxWorkers, Mergers: c.MaxMergers,
		Budget: c.Budget(), AgentShape: c.AgentShape(), Ledger: c.Ledger,
		// Not bounds on the deployment's budget, but the optional per-run
		// CI job cap and the order the CI gate spends what the bounds
		// leave it, carried on the same value so that whatever rations a
		// sandbox and whatever ranks the queue for one are read from one
		// place (grain/task-325). Unresolved: 0 means "this deployment
		// has chosen nothing", which is pkg/ci's question to answer.
		CIJobsPerRun: c.CiJobsPerRun, CIAgeBoost: c.ciAgeBoost, CIReserveAfter: c.CiReserveAfter,
	}
}

// budget is how much of this machine those counts may commit between
// them (model.Budget, grain/task-173): the host as it was read at
// startup, less the reserve, times whatever overcommitment this
// deployment has chosen.
//
// The zero Budget -- which bounds nothing at all -- is what a deployment
// with no host reading gets, and it is deliberately the fallback rather
// than a guessed total: refusing sandboxes because nobody could count
// the machine would be a worse failure than the overcommit this closes.
// The three settings themselves are re-read off grain_config every
// reconcile tick (withModelConfig), so this is the budget as the
// deployment is configured *now* rather than as it was started: a
// reserve raised in the Settings pane binds the next job admitted
// through the gate.
func (c Config) Budget() model.Budget {
	cpu, mem := c.overcommitRatios()
	budget := model.NewBudget(
		model.Commitment{CPUs: c.HostCPUs, MemoryMB: c.HostMemoryMB},
		c.MemoryReserveMB, cpu, mem)
	// Disk is promised against the cluster's allocatable ephemeral
	// storage where the backend reported one, and otherwise against the
	// live reading of the sandbox volume the floor below already takes.
	budget.Host.DiskGB = c.HostDiskGB
	budget.DiskOvercommit = c.DiskOvercommit
	// The disk floor is carried whatever that came to, including on the
	// zero budget a host whose cores and memory could not be read
	// produces: a volume on such a host fills exactly as fast, and the
	// floor needs no reading of the machine to be true -- only a reading
	// of the disk, which whoever admits the work takes for itself
	// (diskWatch, grain/task-196).
	budget.DiskFloor = c.DiskFloor()
	budget.RepairReserve = c.RepairReserve()
	budget.ClusterFit = c.ClusterFit
	return budget
}

// overcommitRatios is the CPU and memory overcommit the budget admits
// against now: the stored ratios in fixed mode (and always on Kubernetes,
// where /proc auto-overcommit does not run), and the auto overcommit
// controller's effective ones in auto mode on a local-host backend.
// Settings that do not validate are fixed mode, which is what statecheck
// tells whoever wrote them.
func (c Config) overcommitRatios() (cpu, mem float64) {
	if !KubernetesSandboxes(c) && c.Overcommit != nil && c.SandboxOvercommit.Auto() && c.SandboxOvercommit.Validate() == nil {
		return c.Overcommit.Effective()
	}
	return c.CpuOvercommit, c.MemoryOvercommit
}

// RepairReserve is RepairReserveSandboxes at this deployment's agent
// shape: what model.Budget keeps for base-branch repairs.
func (c Config) RepairReserve() model.Commitment {
	return c.AgentShape().Times(c.RepairReserveSandboxes)
}

// ciRetention is how long this deployment keeps its CI history, as
// pkg/ci reads it (ci.StoredRunRetention): the four settings above with
// their units applied, and nothing resolved -- 0 travels through as 0,
// because "grain's own default" is a question the ci package answers and
// the daemon only carries.
//
// Read per sweep rather than once, through liveConfig.ciRetention, so a
// history widened in the Settings pane binds the next run that concludes
// rather than the next restart. A negative setting stays negative for
// the same reason: it means no bound at all, and multiplying it by a
// unit keeps it meaning that.
func (c Config) ciRetention() ci.StoredRunRetention {
	// The arithmetic itself lives beside the sweep that applies it
	// (ci.StoredRunRetentionFrom), so that what the Storage section holds
	// its reading against is the same ceiling this hands the sweeper.
	return ci.StoredRunRetentionFrom(model.Config{
		CIRunHistory:     c.CiRunHistory,
		CIStepOutputMB:   c.CiStepOutputMB,
		CIStepOutputDays: c.CiStepOutputDays,
		CIRunOutputKB:    c.CiRunOutputKB,
	})
}

// ciStorageBounds is what grain CI's two directories may occupy, as
// pkg/ci reads it (ci.StorageBounds): the cache's bounds and the
// artifact store's, in bytes and durations, with nothing resolved -- a 0
// travels through as 0, because "grain's own default" is a question the
// ci package answers and the daemon only carries.
//
// These fields are the stored settings as withModelConfig last copied
// them off the row (or, before the first tick, the flags that seeded it),
// so reading them per sweep through liveConfig.ciStorageBounds is what
// makes a ceiling moved in the Settings pane bind the next sweep rather
// than the next restart. One value for both directories, and one
// provider handed to both stores, so the two halves of one operator
// decision cannot be wired up differently.
func (c Config) CiStorageBounds() ci.StorageBounds {
	return ci.StorageBounds{
		Cache: ci.CacheBounds{
			MaxAge:     c.CiCacheMaxAge,
			MaxBytes:   c.CiCacheMaxBytes,
			MaxEntries: c.CiCacheMaxEntries,
			// The one bound in either group with no column behind it, and
			// so the one that is still only ever what the flag said
			// (config.ciCacheMaxEntryBytes).
			MaxEntryBytes: c.CiCacheMaxEntryBytes,
		},
		Artifacts: ci.ArtifactBounds{
			MaxAge:           c.CiArtifactMaxAge,
			MaxBytes:         c.CiArtifactMaxBytes,
			MaxRunBytes:      c.CiArtifactMaxRunBytes,
			MaxArtifactBytes: c.CiArtifactMaxEntryBytes,
		},
	}
}

// diskFloor is how much of this deployment's volume it keeps free
// (model.DiskFloor). The reading it is held against is not here: free
// disk is what the runs have written, so it is taken at each admission
// by diskWatch rather than resolved once into a config.
func (c Config) DiskFloor() model.DiskFloor {
	return model.DiskFloor{Share: c.DiskFloorShare, MinMB: c.DiskFloorMinMB}
}

// agentShape is what one agent sandbox takes out of that budget: the
// deployment-wide shape, with each dimension it leaves unset resolved to
// the default the backend would apply anyway (granule's own), because a
// sandbox charged nothing is exactly the arithmetic this is here to fix.
//
// Nothing at all under a backend that drops the shape on the floor: what
// a host sandbox takes is what its run touches, which is not a figure
// anything can add up in advance. ui.NewUncappedSandboxCommitment says
// the same of the same deployments.
func (c Config) AgentShape() model.Commitment {
	if c.SandboxShapeIgnored {
		return model.Commitment{}
	}
	shape := model.Commitment{CPUs: c.SandboxCPUs, MemoryMB: c.SandboxMemoryMB, DiskGB: c.SandboxDiskGB}
	if shape.CPUs <= 0 {
		shape.CPUs = granule.DefaultCPUs
	}
	if shape.MemoryMB <= 0 {
		shape.MemoryMB = granule.DefaultMemoryMB
	}
	if shape.DiskGB <= 0 {
		shape.DiskGB = granule.DefaultDiskGB
	}
	return shape
}

// withHostCapacity is the one reading of this machine the budget is
// built on, taken at startup: the host is a fact about the hardware
// rather than a setting, and sysstat.Read costs a sampling interval, so
// asking per admission would be paying for an answer that does not
// change. A deployment moved to a bigger machine is a restart away
// either way.
//
// A backend with no host to read -- or a reading that failed -- leaves
// the figures at zero, and the budget then bounds nothing. That is
// logged where it matters (logSandboxBudget) rather than being a startup
// failure: this is a bound on how much work runs at once, not a
// precondition for running any. A reading that failed is taken again on
// every LiveConfig.Refresh until one succeeds, so a cluster whose nodes
// were briefly unreadable at startup gets its budget without a restart.
func (c Config) withHostCapacity(sandboxes orchestrator.Sandboxes) Config {
	c.SandboxShapeIgnored = SandboxShapeIgnored(sandboxes)
	if fit, ok := sandboxes.(model.ClusterFit); ok {
		c.ClusterFit = fit
	}
	capacity := sandboxHostCapacity(sandboxes)
	if capacity == nil {
		return c
	}
	host, err := capacity()
	if err != nil {
		log.Printf("grain daemon: reading this host's own cores and memory: %v -- "+
			"sandboxes will be bounded by their counts alone", err)
		return c
	}
	// A cluster is re-read on every reconcile tick (LiveConfig.Refresh),
	// so a reading of nothing -- every node briefly NotReady, or an empty
	// list from an API server mid-restart -- is far likelier to be a
	// blip than a cluster that really shrank to zero. Keep the figure
	// already known rather than let one tick unbound the budget.
	if (host.CPUs <= 0 || host.MemoryMB <= 0) && c.HostCPUs > 0 && c.HostMemoryMB > 0 {
		log.Printf("grain daemon: reading sandbox capacity returned %d CPUs and %d MiB -- keeping the last "+
			"reading of %d CPUs and %d MiB", host.CPUs, host.MemoryMB, c.HostCPUs, c.HostMemoryMB)
		return c
	}
	c.HostCPUs, c.HostMemoryMB = host.CPUs, host.MemoryMB
	// Disk is kept on the same terms: a reading of none is a node list
	// without the field rather than a cluster with no disk.
	if host.DiskGB > 0 || c.HostDiskGB <= 0 {
		c.HostDiskGB = host.DiskGB
	}
	return c
}

// toModelConfig is the flag-parsed subset of config that mirrors
// model.Config -- the seed loadConfig writes when a deployment has never
// stored one.
//
// It starts from model.DefaultConfig rather than a zero model.Config:
// the settings with no flag behind them here (ApprovedByDefault and
// AutoMergeByDefault, whose default is on) still have to be seeded as
// what a deployment that has never chosen them runs, and PutConfig binds
// every column, so anything left at its Go zero value here is stored as a
// deliberate-looking value nobody chose.
func (c Config) ToModelConfig() model.Config {
	mc := capability.NewDeploymentConfig()
	mc.PollInterval, mc.MaxWorkers, mc.MaxMergers = c.PollInterval, c.MaxWorkers, c.MaxMergers
	mc.DefaultModel = c.defaultModel
	mc.MaxAgentTurns = c.MaxAgentTurns
	mc.GitHubHost, mc.GitHubInsecureHTTP = c.GithubHost, c.GithubInsecureHTTP
	mc.GCPProject, mc.GCPServiceAccountEmail, mc.GCPServiceAccountCount = c.GcpProject, c.GcpServiceAccountEmail, c.GcpServiceAccountCount
	mc.IAPBackendService = c.IapBackendService
	mc.IAPResource = c.IapResource
	mc.SandboxBackend = c.SandboxBackend()
	mc.SandboxCPUs, mc.SandboxMemoryMB, mc.SandboxDiskGB = c.SandboxCPUs, c.SandboxMemoryMB, c.SandboxDiskGB
	mc.SandboxCPUOvercommit, mc.SandboxMemoryOvercommit = c.CpuOvercommit, c.MemoryOvercommit
	mc.SandboxDiskOvercommit = c.DiskOvercommit
	mc.SandboxMemoryReserveMB = c.MemoryReserveMB
	mc.SandboxOvercommit = c.SandboxOvercommit
	mc.SandboxDiskFloorShare, mc.SandboxDiskFloorMinMB = c.DiskFloorShare, c.DiskFloorMinMB
	mc.NetworkAccess = c.networkAccess
	mc.PackageCacheSizeGB = c.PackageCacheGB
	// The switch, not the size, is where "no Actions cache" is seeded:
	// -actions-cache-size-gb 0 leaves the size at grain's own and turns
	// the switch off, so an operator who later turns it back on gets a
	// sensible bound rather than a zero nobody chose.
	mc.ActionsCacheEnabled = c.ActionsCacheEnabled
	if c.ActionsCacheGB > 0 {
		mc.ActionsCacheSizeGB = c.ActionsCacheGB
	}
	mc.CIRunHistory, mc.CIStepOutputMB = c.CiRunHistory, c.CiStepOutputMB
	mc.CIStepOutputDays, mc.CIRunOutputKB = c.CiStepOutputDays, c.CiRunOutputKB
	// What CI's two directories may occupy, seeded out of the
	// -ci-cache-max-* and -ci-artifact-max-* flags in the columns' own
	// units -- whole MiB and whole days, which is what the pane edits and
	// what StoredAs rounds a finer flag down to (ci.StorageBounds.StoredAs
	// has the rounding and why it goes that way).
	mc = ci.StorageBoundsStoredAs(c.CiStorageBounds(), mc)
	if c.K8sNamespace != model.DefaultK8sNamespace {
		mc.K8sNamespace = c.K8sNamespace
	}
	if c.K8sKubectl != model.DefaultK8sKubectl {
		mc.K8sKubectl = c.K8sKubectl
	}
	sandboxImg := firstNonEmpty(c.K8sSandboxImage, c.K8sImage)
	if sandboxImg != DefaultK8sSandboxImage && sandboxImg != model.DefaultK8sSandboxImage {
		mc.K8sSandboxImage = sandboxImg
	}
	if c.K8sGranuleImage != DefaultSandboxImage && c.K8sGranuleImage != model.DefaultSandboxImage {
		mc.K8sGranuleImage = c.K8sGranuleImage
	}
	mc.SandboxImageMirror = c.SandboxImageMirror
	if c.K8sWorkspace != model.DefaultK8sWorkspace {
		mc.K8sWorkspace = c.K8sWorkspace
	}
	mc.K8sGitProxyHost = c.K8sGitProxyHost
	mc.K8sSandboxService = c.K8sSandboxService
	mc.K8sServiceAccount = c.K8sServiceAccount
	mc.K8sNodeSelector = append([]string(nil), c.K8sNodeSelector...)
	mc.K8sPrivileged = c.K8sPrivileged
	mc.K8sNetworkPolicy = c.K8sNetworkPolicy
	if c.K8sDeployment != model.DefaultK8sDeployment {
		mc.K8sDeployment = c.K8sDeployment
	}
	if c.K8sContainer != model.DefaultK8sContainer {
		mc.K8sContainer = c.K8sContainer
	}
	mc.K8sPriorityClassInteractive = c.K8sPriorityClasses.Interactive
	mc.K8sPriorityClassAgent = c.K8sPriorityClasses.Agent
	mc.K8sPriorityClassCI = c.K8sPriorityClasses.CI
	return mc
}

// withLiveModelConfig is withModelConfig restricted to what a *running*
// daemon can adopt: github-host and github-insecure-http keep the value
// this process started with (liveConfig's own doc comment for why they
// cannot change under a live deployment), so what this returns stays a
// true description of what the process is running rather than of what is
// merely stored.
//
// A stored field with no usable value is skipped rather than adopted,
// which is the whole of runnableStoredConfig -- see it for why a row can
// carry one at all, and for what each fallback is. The values it
// substitutes here are this daemon's *current* ones rather than the flags
// it started with, since c is the configuration in effect
// (liveConfig.applied): a row that goes on saying nothing usable about
// the poll interval leaves the loop pacing itself exactly as it already
// was.
//
// The notes it produces are dropped rather than logged, unlike at
// startup: this runs once a reconcile tick, so a row nobody has fixed
// would repeat the same line every interval for as long as the
// deployment is up. loadConfig said it once, at the top of the journal,
// and `grain state check` said it before the row was ever merged.
func (c Config) withLiveModelConfig(mc model.Config) Config {
	runnable, _ := c.RunnableStoredConfig(mc)
	runningBackend := c.SandboxBackend()
	live := c.WithModelConfig(runnable)
	live = live.withSandboxBackend(runningBackend)
	live.GithubHost, live.GithubInsecureHTTP = c.GithubHost, c.GithubInsecureHTTP
	live.K8sNamespace = c.K8sNamespace
	live.K8sKubectl = c.K8sKubectl
	live.K8sImage = c.K8sImage
	live.K8sSandboxImage = c.K8sSandboxImage
	live.K8sGranuleImage = c.K8sGranuleImage
	live.K8sWorkspace = c.K8sWorkspace
	live.K8sGitProxyHost = c.K8sGitProxyHost
	live.K8sSandboxService = c.K8sSandboxService
	live.K8sServiceAccount = c.K8sServiceAccount
	live.K8sNodeSelector = append([]string(nil), c.K8sNodeSelector...)
	live.K8sPrivileged = c.K8sPrivileged
	live.K8sNetworkPolicy = c.K8sNetworkPolicy
	live.K8sDeployment = c.K8sDeployment
	live.K8sContainer = c.K8sContainer
	live.K8sPriorityClasses = c.K8sPriorityClasses
	return live
}

// withModelConfig returns c with every store-backed field replaced by
// mc's -- everything loadConfig reads back out of grain_config once a
// row exists.
func (c Config) WithModelConfig(mc model.Config) Config {
	c.CLIRun = mc.CLIRun
	c.PollInterval = mc.PollInterval
	c.MaxWorkers = mc.MaxWorkers
	c.MaxMergers = mc.MaxMergers
	c.MaxCIJobs = mc.MaxCIJobs
	c.AgentBackends = append([]model.AgentBackend(nil), mc.AgentBackends...)
	c.defaultModel = mc.DefaultModel
	c.MaxAgentTurns = mc.MaxAgentTurns
	c.GithubHost = mc.GitHubHost
	c.GithubInsecureHTTP = mc.GitHubInsecureHTTP
	c.GcpProject = mc.GCPProject
	c.GcpServiceAccountEmail = mc.GCPServiceAccountEmail
	c.GcpServiceAccountCount = mc.GCPServiceAccountCount
	c.IapBackendService = mc.IAPBackendService
	c.IapResource = mc.IAPResource
	if mc.SandboxBackend != "" {
		c = c.withSandboxBackend(mc.SandboxBackend)
	}
	c.SandboxCPUs = mc.SandboxCPUs
	c.SandboxMemoryMB = mc.SandboxMemoryMB
	c.SandboxDiskGB = mc.SandboxDiskGB
	// The budget's own three, copied here for the same reason the shape
	// above is: config.budget() is built from these fields, and the gate
	// and the dispatch both ask for it per job (pkg/cli/ci.go's
	// Budget/Limit closures, orchestrator's per-cycle limits), so a
	// budget widened in the Settings pane is honoured by the next job
	// rather than at the next restart (grain/task-197).
	//
	// Narrowing one is the same operation and needs no more care: a
	// smaller budget binds the next admission and nothing already
	// running, since a sandbox is only ever held against a budget on the
	// way in (model.Budget.Admits' own note on a budget that moves under
	// a live deployment). Nothing here kills a run to make a new number
	// true.
	c.CpuOvercommit = mc.SandboxCPUOvercommit
	c.MemoryOvercommit = mc.SandboxMemoryOvercommit
	c.DiskOvercommit = mc.SandboxDiskOvercommit
	c.MemoryReserveMB = mc.SandboxMemoryReserveMB
	c.SandboxOvercommit = mc.SandboxOvercommit
	// And the budget's third dimension, which needs one thing more than
	// the ratios do to be genuinely live: the readings it is judged
	// against are taken by a watcher built once at startup, so that
	// watcher asks for the floor per reading rather than holding the one
	// it was constructed with (diskWatch.floor, grain/task-372). A floor
	// lowered in the pane while a full volume holds every dispatch is
	// then what the next admission is judged against -- which is the
	// moment an operator actually reaches for this setting.
	c.DiskFloorShare, c.DiskFloorMinMB = mc.SandboxDiskFloorShare, mc.SandboxDiskFloorMinMB
	c.networkAccess = mc.NetworkAccess
	c.CustomSandboxImage = mc.SandboxImage
	c.SandboxImageMirror = mc.SandboxImageMirror
	c.PackageCacheEnabled = mc.PackageCacheEnabled
	c.PackageCacheGB = mc.PackageCacheSizeGB
	c.ActionsCacheEnabled = mc.ActionsCacheEnabled
	c.ActionsCacheGB = mc.ActionsCacheSizeGB
	// The CI history's four, copied here for the reason the budget's
	// three above are: ci.StoredRunStore asks liveConfig for them on
	// every sweep, so a retention raised in the Settings pane is what
	// the next run that concludes is kept under (grain/task-269).
	c.CiRunHistory, c.CiStepOutputMB = mc.CIRunHistory, mc.CIStepOutputMB
	c.CiStepOutputDays, c.CiRunOutputKB = mc.CIStepOutputDays, mc.CIRunOutputKB
	// And CI's two directories' seven, copied back into the fields the
	// flags seeded -- through the ci package's own reading of the columns,
	// so the MiB and the days are turned into bytes and a duration exactly
	// once (ci.StorageBoundsFrom). A stored 0 stays a zero here, which is
	// how "grain's own default" travels the rest of the way down
	// (ci.CacheBounds.Resolved).
	//
	// ciCacheMaxEntryBytes is deliberately not among them: it has no
	// column, so a row can say nothing about it and this must not
	// overwrite the flag the process started with.
	bounds := ci.StorageBoundsFrom(mc)
	c.CiCacheMaxAge, c.CiCacheMaxBytes = bounds.Cache.MaxAge, bounds.Cache.MaxBytes
	c.CiCacheMaxEntries = bounds.Cache.MaxEntries
	c.CiArtifactMaxAge, c.CiArtifactMaxBytes = bounds.Artifacts.MaxAge, bounds.Artifacts.MaxBytes
	c.CiArtifactMaxRunBytes = bounds.Artifacts.MaxRunBytes
	c.CiArtifactMaxEntryBytes = bounds.Artifacts.MaxArtifactBytes
	// And the gate's two ordering windows, copied here for the reason the
	// budget's three are: ci.SharedGate asks cmd/grain for them on every
	// admission attempt (buildCIService's own closures), so a boost
	// shortened in the Settings pane is what the jobs queueing right now
	// are ranked by rather than what the next process starts with
	// (grain/task-325).
	c.CiJobsPerRun, c.ciAgeBoost, c.CiReserveAfter = mc.CIJobsPerRun, mc.CIAgeBoost, mc.CIReserveAfter
	c.K8sNamespace = firstNonEmpty(mc.K8sNamespace, model.DefaultK8sNamespace)
	c.K8sKubectl = mc.K8sKubectl
	c.K8sImage = mc.K8sSandboxImage
	c.K8sSandboxImage = mc.K8sSandboxImage
	c.K8sGranuleImage = mc.K8sGranuleImage
	c.K8sWorkspace = mc.K8sWorkspace
	c.K8sGitProxyHost = mc.K8sGitProxyHost
	c.K8sSandboxService = mc.K8sSandboxService
	c.K8sServiceAccount = mc.K8sServiceAccount
	c.K8sNodeSelector = append([]string(nil), mc.K8sNodeSelector...)
	c.K8sPrivileged = mc.K8sPrivileged
	c.K8sNetworkPolicy = mc.K8sNetworkPolicy
	c.K8sDeployment = firstNonEmpty(mc.K8sDeployment, model.DefaultK8sDeployment)
	c.K8sContainer = firstNonEmpty(mc.K8sContainer, model.DefaultK8sContainer)
	c.K8sPriorityClasses = orchestrator.K8sPriorityClasses{
		Interactive: mc.K8sPriorityClassInteractive,
		Agent:       mc.K8sPriorityClassAgent,
		CI:          mc.K8sPriorityClassCI,
	}
	return c
}

// LocalScriptsConfig is where this daemon's runs find local-scripts
// declarations: the state repository's checkout under DataDir, and the
// host-managed paths -local-scripts names. An entry that does not parse
// was refused when the flag was read (cmd/grain), so it is skipped here.
func (c Config) LocalScriptsConfig() localscripts.Config {
	cfg := localscripts.Config{StateDir: filepath.Join(c.DataDir, "state-repo")}
	for _, v := range c.LocalScripts {
		_ = cfg.ParseHost(v)
	}
	return cfg
}
