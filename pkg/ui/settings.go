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

package ui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/actionscache"
	"github.com/ai-on-gke/grain/pkg/agent/antigravity"
	"github.com/ai-on-gke/grain/pkg/agent/codex"
	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/iapreconciler"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/pkgcache"
	"github.com/ai-on-gke/grain/pkg/sandboximage"
	"github.com/ai-on-gke/grain/pkg/upgrade"
)

// Settings is the JSON shape of a deployment's store-backed
// configuration (model.Config, bwsalmon/agents#320) -- the knobs
// cmd/grain's "daemon" subcommand reads out of grain_config instead of
// only from its own flags (daemon.go's loadConfig). This package is what
// lets a UI or a CLI actually be the thing that changes them, the same
// "the store is the record" shape pkg/ui already gives tasks.
//
// Changing one takes effect on the running deployment, without a
// restart, for everything but the settings RestartRequired names:
// the daemon re-reads its stored configuration once per reconcile tick
// and applies whatever changed (pkg/cli/daemon.go's liveConfig). The
// handful that genuinely cannot be swapped under a live deployment come
// back from the API as settings.restartRequired, and the ones changed but
// not yet running as settings.pendingRestart -- both keyed by the same
// JSON field name the form's own inputs are named after, so annotating a
// field is a lookup rather than a second list to keep in step.
//
// Configured is false when nothing has written a row yet -- before any
// daemon has ever started against this store -- so a caller can tell
// that apart from "every field happens to be a zero value" the same way
// Store.GetConfig's own nil does.
type Settings struct {
	Configured bool `json:"configured"`
	// PollInterval is a Go duration string ("30s"), the same syntax
	// -poll-interval itself parses.
	PollInterval string `json:"pollInterval"`
	// There is deliberately no maxWorkers, maxMergers or maxCIJobs here.
	// They were this deployment's three concurrency settings until
	// grain/task-28: how many ordinary runs could be live, how much
	// further capacity only the merge queue's own fix tasks could reach,
	// and how many grain CI jobs. No count bounds a run now, and the only
	// capacity control is the sandbox budget on the Sandbox tab -- the host's cores and memory, less what is kept
	// back for the host itself, times this deployment's overcommit
	// ratios, held against every sandbox that is actually live.
	//
	// The three columns are still in grain_config so that an upgrade over
	// a row that has them does not fail; SandboxCommitment below states
	// what they would commit, and nothing schedules on them.
	MaxWorkers       int `json:"maxWorkers"`
	MaxMergesPerRepo int `json:"maxMergesPerRepo"`
	// MergeBatchSize is how many queued changes the merge queue tests
	// together on a throwaway ref before merging the lot
	// (model.Config.MergeBatchSize). 1 turns batching off and leaves the
	// one-at-a-time queue.
	MergeBatchSize int `json:"mergeBatchSize"`
	// MergeBatchSizeChosen is the batch size this deployment has stored,
	// or 0 when it has chosen none and MergeBatchSize is grain's default.
	// The Admin pane's box shows this one, so a box somebody cleared reads
	// blank after a reload rather than the default as though it had been
	// typed in (docs/test-runs/2026-10-12-suite-release-batch-fixes.md).
	MergeBatchSizeChosen int `json:"mergeBatchSizeChosen"`
	// MergeGatherWindow is how long the queue holds a lone ready change
	// at the front of a branch's queue, waiting to see whether a second
	// becomes ready and the two can be tested together
	// (model.Config.MergeGatherWindow), as a Go duration string ("5m") --
	// the same syntax PollInterval and the CI gate's two windows use.
	//
	// Empty means this deployment has chosen nothing, which is grain's
	// own window shown beside it as a placeholder
	// (MergeGatherWindowDefault). A negative duration is how the wait is
	// turned off altogether, the spelling CIReserveAfter uses for the
	// same kind of trade.
	MergeGatherWindow        string `json:"mergeGatherWindow"`
	MergeGatherWindowDefault string `json:"mergeGatherWindowDefault"`
	// MergeBatching is what the batches that size has produced have
	// actually cost and saved (grain/task-300), or nil on a deployment
	// that has never formed one. It sits beside the setting because the
	// setting is a bet, and this is the record of how the bet has gone.
	MergeBatching *MergeBatching `json:"mergeBatching,omitempty"`
	// MergeGathering is what the holds that window has taken have
	// actually done (grain/task-440), or nil on a deployment whose queue
	// has never held anything. It sits beside MergeGatherWindow for
	// MergeBatching's reason, and for a sharper one: a hold leaves no
	// other trace at all, so without it the window above is the one
	// setting in this pane with no evidence behind it.
	MergeGathering *MergeGathering `json:"mergeGathering,omitempty"`
	// MaxAutoSubmitDepth and MaxAutoSubmitFanOut are the bounds on work
	// grain queues with nobody having approved it -- model.Config's own
	// fields of the same name, and model.AutoSubmitLimits for what the
	// pair means and why it exists. Reported resolved: a stored zero is
	// "nobody chose", and what this pane shows is the bound actually
	// being enforced rather than the zero behind it.
	MaxAutoSubmitDepth  int `json:"maxAutoSubmitDepth"`
	MaxAutoSubmitFanOut int `json:"maxAutoSubmitFanOut"`
	// GeminiEfforts and CodexEfforts are the effort vocabularies a
	// per-model setting is saved from -- agent/antigravity.Efforts and
	// agent/codex.Efforts -- reported so a settings pane can offer the
	// choices. Which of them a given model actually offers is that
	// model's own business, so these are the widest lists.
	GeminiEfforts          []string `json:"geminiEfforts"`
	CodexEfforts           []string `json:"codexEfforts"`
	MaxAgentTurns          int      `json:"maxAgentTurns"`
	GitHubHost             string   `json:"githubHost"`
	GitHubInsecureHTTP     bool     `json:"githubInsecureHttp"`
	GCPProject             string   `json:"gcpProject"`
	GCPServiceAccountEmail string   `json:"gcpServiceAccountEmail"`
	GCPServiceAccountCount int      `json:"gcpServiceAccountCount"`
	IAPBackendService      string   `json:"iapBackendService"`
	// IAPResource is the IAP web resource whose IAP access is reconciled
	// from the teams and access list; "none" turns that off.
	IAPResource string `json:"iapResource"`
	// NewestFirst is model.Config's own field of the same name
	// (bwsalmon/agents#476): false, the default, files a new task at the
	// back of the dispatch queue, which is the bottom of the task list;
	// true files it at the front instead, which is the top of the same
	// list -- the list being dispatch order, not a second ordering of its
	// own (ui.Client.ListTasks).
	NewestFirst bool `json:"newestFirst"`
	// SandboxBackend is the isolation mechanism configured for this
	// deployment. SandboxBackends is the set this daemon can construct from
	// the host-side arguments it was started with. SandboxBackendOptions lists
	// every backend in model.SandboxBackends() with available: bool and reason
	// when unavailable so the UI can show unavailable backends disabled with a
	// clear explanation instead of silently omitting them. Changing
	// SandboxBackend is restart-only; the current daemon keeps using the
	// running value until it restarts.
	SandboxBackend        string                       `json:"sandboxBackend"`
	SandboxBackends       []string                     `json:"sandboxBackends"`
	SandboxBackendOptions []model.SandboxBackendOption `json:"sandboxBackendOptions"`
	// SandboxCPUs, SandboxMemoryMB and SandboxDiskGB
	// (bwsalmon/agents#534, grain/task-41) are the deployment-wide
	// default shape a granule-managed sandbox VM is created with --
	// model.Config's own fields of the same name. Zero (the default for
	// all three) means "use grain's own default shape" (the three
	// *Default fields below) rather than a deliberately tiny VM.
	// Meaningless, and simply unused,
	// under a deployment running the default local-directory sandboxing
	// (no -granule-sandboxes); GetSettings still reports whatever is
	// stored either way, the same as every other granule* setting here --
	// and reports SandboxShapeIgnored below alongside them, so that
	// "unused here" is something the pane showing the numbers can say
	// rather than something an operator has to already know.
	SandboxCPUs     int `json:"sandboxCpus"`
	SandboxMemoryMB int `json:"sandboxMemoryMb"`
	SandboxDiskGB   int `json:"sandboxDiskGb"`
	// SandboxCPUsDefault, SandboxMemoryMBDefault and SandboxDiskGBDefault
	// are grain's own default VM shape (granule.DefaultCPUs/
	// DefaultMemoryMB/DefaultDiskGB) -- the shape actually in effect
	// whenever the stored value above is 0, surfaced so a caller can show
	// that real current shape instead of a bare, misleadingly literal 0
	// (bwsalmon/agents#610). Constant, never read from or written to the
	// store.
	//
	// Disk gained one here with the rest: it used to have no default to
	// name, because an unset disk was however large the guest image a
	// deployment built happened to be. grain names all three sizes now
	// and passes all three on every create
	// (orchestrator.GranuleConfig.createArgs), so an unset disk has a
	// number to show like the other two.
	SandboxCPUsDefault     int `json:"sandboxCpusDefault"`
	SandboxMemoryMBDefault int `json:"sandboxMemoryMbDefault"`
	SandboxDiskGBDefault   int `json:"sandboxDiskGbDefault"`
	// SandboxCPUOvercommit, SandboxMemoryOvercommit and
	// SandboxMemoryReserveMB are model.Config's own fields of the same
	// name: the three knobs of this deployment's sandbox budget, which
	// is the total every shape above is admitted against -- agent runs
	// and CI jobs alike (model.Budget, grain/task-173; editable rather
	// than daemon flags since grain/task-197).
	//
	// Each ratio is how much more than the host has may be promised, 1
	// being exactly what it has, and 0 leaving that dimension bounded by
	// the run counts alone. The reserve is MiB kept back for everything
	// on that host that is not a sandbox, taken off the top before the
	// memory ratio; a negative reserve is "grain's own", an eighth of
	// the machine and never less than 2 GiB, which is what the
	// commitment below reports resolved against the host it was read on.
	//
	// The defaults beside them are grain's own numbers
	// (model.DefaultCPUOvercommit/DefaultMemoryOvercommit), reported for
	// the reason the shape defaults above are: a pane showing a field an
	// operator has not touched can say what it is instead of a bare
	// blank.
	SandboxCPUOvercommit           float64 `json:"sandboxCpuOvercommit"`
	SandboxMemoryOvercommit        float64 `json:"sandboxMemoryOvercommit"`
	SandboxMemoryReserveMB         int     `json:"sandboxMemoryReserveMb"`
	SandboxCPUOvercommitDefault    float64 `json:"sandboxCpuOvercommitDefault"`
	SandboxMemoryOvercommitDefault float64 `json:"sandboxMemoryOvercommitDefault"`
	// SandboxDiskOvercommit is model.Config.SandboxDiskOvercommit, the
	// same ratio for sandbox disk (and, on Kubernetes, what each pod's
	// ephemeral-storage request is scaled by), with grain's own default
	// beside it for the reason the two above have theirs.
	SandboxDiskOvercommit        float64 `json:"sandboxDiskOvercommit"`
	SandboxDiskOvercommitDefault float64 `json:"sandboxDiskOvercommitDefault"`
	// SandboxOvercommit is model.Config.SandboxOvercommit: fixed (the
	// two ratios above, as stored) or auto (the auto overcommit
	// controller tunes them inside these bounds), resolved so every bound
	// reads as the figure in effect. The controller's live ratios, recent
	// adjustments and OOM kills are SandboxCommitment.Budget.Overcommit.
	SandboxOvercommit model.OvercommitSettings `json:"sandboxOvercommit"`
	ExternalAgentAPI  bool                     `json:"externalAgentApi"`
	// SessionSandboxIdleTTL is model.Config.SessionSandboxIdleTTL: how long
	// an interactive chat/session task's sandbox (and in-process agent run)
	// stays warm awaiting a reply after a turn ends before being released,
	// as a Go duration string ("30m"). Empty means grain's own default
	// (SessionSandboxIdleTTLDefault); a negative duration ("-1s") turns warm
	// retention off.
	SessionSandboxIdleTTL        string `json:"sessionSandboxIdleTtl"`
	SessionSandboxIdleTTLDefault string `json:"sessionSandboxIdleTtlDefault"`
	// SandboxMemoryReserveMBDefault is what a negative reserve comes to
	// on the host this process can speak for (model.DefaultReserveMemoryMB),
	// so the pane's blank field can say what blank means. Zero, and so
	// omitted, where no host could be read.
	SandboxMemoryReserveMBDefault int `json:"sandboxMemoryReserveMbDefault,omitempty"`
	// SandboxDiskFloorShare and SandboxDiskFloorMinMB are that budget's
	// third dimension, and model.Config's own fields of the same names:
	// how much of each volume this deployment writes to is kept free,
	// below which a new CI job is held and an agent dispatch deferred
	// (model.DiskFloor, grain/task-196; a stored setting rather than two
	// daemon flags since grain/task-372).
	//
	// The share is a fraction of the volume and the minimum is the least
	// it is ever worth in MiB, because a fixed size is wrong on both a
	// 20 GB store disk and a 4 TB sandbox volume. Zero in both is a
	// deployment that holds nothing back as its disk fills -- a setting
	// an operator can want, and the one this pane makes reversible
	// without a restart.
	//
	// The defaults beside them are grain's own
	// (model.DefaultDiskFloorShare, model.MinDiskFloorMB), reported for
	// the reason the ratios' are, and SandboxDiskFloorMaxShare is the
	// cap the arithmetic applies whatever is stored
	// (model.MaxDiskFloorShare): a floor of 90% typed into the pane
	// keeps a quarter of the volume free rather than wedging the
	// deployment, and the pane can say so.
	SandboxDiskFloorShare        float64 `json:"sandboxDiskFloorShare"`
	SandboxDiskFloorMinMB        int     `json:"sandboxDiskFloorMinMb"`
	SandboxDiskFloorShareDefault float64 `json:"sandboxDiskFloorShareDefault"`
	SandboxDiskFloorMinMBDefault int     `json:"sandboxDiskFloorMinMbDefault"`
	SandboxDiskFloorMaxShare     float64 `json:"sandboxDiskFloorMaxShare"`
	// SandboxDiskFloors is what those two come to on this deployment's
	// own volumes -- one entry per filesystem, with FloorMB and
	// ReclaimMB resolved against each one's size (Config.SandboxVolumes).
	//
	// A share is not a figure until it is applied to a disk, and the two
	// volumes a deployment sized the way terraform/gcp is has get
	// different answers from the same setting: a tenth of the 443 GB
	// sandbox volume is 44 GB, while the 20 GB store disk beside it is
	// held by the 5 GiB minimum instead. Resolved in Go rather than in
	// the pane for ui.DiskUsage.FloorMB's own reason -- the arithmetic
	// has a minimum and a cap in it, and a second implementation of it
	// in JavaScript would be a second answer to "why is this deployment
	// holding work".
	//
	// Empty where there is nothing to resolve against: a UI not
	// colocated with this deployment's host, or a deployment whose
	// volumes were never resolved. The pane shows the setting without
	// the figures then.
	SandboxDiskFloors []DiskUsage `json:"sandboxDiskFloors,omitempty"`
	// SandboxImage is model.Config's own field of the same name: the OCI
	// reference of this installation's custom sandbox image on the active
	// backend, built FROM SandboxBaseImage. Empty, the default, creates
	// sandboxes from SandboxImageDefault, the image this build is stamped
	// for on that backend.
	SandboxImage        string `json:"sandboxImage"`
	SandboxImageDefault string `json:"sandboxImageDefault"`
	SandboxImageMirror  string `json:"sandboxImageMirror"`
	// SandboxImageBackend is the backend those two are answered for --
	// the one chosen in Settings, else the one running.
	SandboxImageBackend string `json:"sandboxImageBackend"`
	// SandboxBaseImage is the minimal image a custom one is built FROM on
	// that backend: guest-base for Granule microVM backends,
	// k8s-sandbox-base for container and Kubernetes pod backends, at this
	// build's own tag.
	SandboxBaseImage string `json:"sandboxBaseImage"`
	// SandboxContractVersion and SandboxMinimumContractVersion are the
	// sandbox contract this build produces and the oldest it still drives
	// (pkg/sandboximage.Contract, MinimumContract); a custom image needs
	// rebuilding only when its own contract label falls outside them.
	// SandboxGranuleContractVersion is Granule's guest contract, which a
	// VM backend's image carries as well.
	SandboxContractVersion        int `json:"sandboxContractVersion"`
	SandboxMinimumContractVersion int `json:"sandboxMinimumContractVersion"`
	SandboxGranuleContractVersion int `json:"sandboxGranuleContractVersion"`
	// SandboxImageWarning is what is wrong with the configured custom
	// image's contract, or that it could not be verified -- "" for no
	// custom image, or one at the current contract. SandboxImageCheck is
	// the structured verdict behind it, null with no custom image. Both
	// are always sent, empty included: the pane merges a save's response
	// over what it holds, and an omitted field would leave a cleared
	// image's warning on screen.
	SandboxImageWarning string                         `json:"sandboxImageWarning"`
	SandboxImageCheck   *sandboximage.CustomImageCheck `json:"sandboxImageCheck"`
	// SandboxContract is the whole contract summary: labels, files, base
	// images and the checklist of what a sandbox image must carry.
	SandboxContract *sandboximage.ContractInfo `json:"sandboxContract,omitempty"`
	// Kubernetes sandbox settings (model.Config.K8s*). All are restart-only:
	// the sandbox backend is constructed once when grain daemon starts.
	K8sNamespace           string   `json:"k8sNamespace"`
	K8sNamespaceDefault    string   `json:"k8sNamespaceDefault"`
	K8sKubectl             string   `json:"k8sKubectl"`
	K8sKubectlDefault      string   `json:"k8sKubectlDefault"`
	K8sSandboxImage        string   `json:"k8sSandboxImage"`
	K8sSandboxImageDefault string   `json:"k8sSandboxImageDefault"`
	K8sGranuleImage        string   `json:"k8sGranuleImage"`
	K8sGranuleImageDefault string   `json:"k8sGranuleImageDefault"`
	DaemonBuildVersion     string   `json:"daemonBuildVersion,omitempty"`
	K8sSandboxImageWarning string   `json:"k8sSandboxImageWarning,omitempty"`
	K8sGranuleImageWarning string   `json:"k8sGranuleImageWarning,omitempty"`
	K8sWorkspace           string   `json:"k8sWorkspace"`
	K8sWorkspaceDefault    string   `json:"k8sWorkspaceDefault"`
	K8sGitProxyHost        string   `json:"k8sGitProxyHost"`
	K8sSandboxService      string   `json:"k8sSandboxService"`
	K8sServiceAccount      string   `json:"k8sServiceAccount"`
	K8sNodeSelector        []string `json:"k8sNodeSelector"`
	K8sPrivileged          bool     `json:"k8sPrivileged"`
	K8sNetworkPolicy       bool     `json:"k8sNetworkPolicy"`
	K8sDeployment          string   `json:"k8sDeployment"`
	K8sDeploymentDefault   string   `json:"k8sDeploymentDefault"`
	K8sContainer           string   `json:"k8sContainer"`
	K8sContainerDefault    string   `json:"k8sContainerDefault"`
	// K8sPriorityClassInteractive, K8sPriorityClassAgent and
	// K8sPriorityClassCI are model.Config's own fields: the PriorityClass
	// each kind of Kubernetes sandbox pod runs at, "" for none. Their
	// *Default siblings are the names deploy/kubernetes/ ships, which the
	// page offers back to a deployment that cleared one.
	K8sPriorityClassInteractive        string            `json:"k8sPriorityClassInteractive"`
	K8sPriorityClassAgent              string            `json:"k8sPriorityClassAgent"`
	K8sPriorityClassCI                 string            `json:"k8sPriorityClassCi"`
	K8sPriorityClassInteractiveDefault string            `json:"k8sPriorityClassInteractiveDefault"`
	K8sPriorityClassAgentDefault       string            `json:"k8sPriorityClassAgentDefault"`
	K8sPriorityClassCIDefault          string            `json:"k8sPriorityClassCiDefault"`
	Sources                            map[string]string `json:"sources,omitempty"`
	SettingSources                     map[string]string `json:"settingSources,omitempty"`
	// PackageCacheEnabled is model.Config's own field of the same name:
	// whether every sandbox's package managers are pointed at this
	// deployment's package cache (pkg/pkgcache). True on a deployment
	// that has stored no configuration at all, for ApprovedByDefault's
	// reason above -- off is not what "never chosen" means here.
	PackageCacheEnabled bool `json:"packageCacheEnabled"`
	// PackageCacheSizeGB is the maximum disk space (in GiB) the package cache
	// may use before evicting oldest entries (0 means default).
	PackageCacheSizeGB int `json:"packageCacheSizeGb"`
	// PackageCacheSizeGBDefault is the default cache max size (in GiB).
	PackageCacheSizeGBDefault int `json:"packageCacheSizeGbDefault"`
	// ActionsCacheEnabled is model.Config's own field of the same name:
	// whether this deployment's CI jobs are handed the GitHub Actions
	// cache the controller keeps (pkg/actionscache), so that a
	// `cache-from: type=gha` build is warm across runs instead of
	// starting cold in a sandbox that is about to be destroyed. True on
	// a deployment that has stored no configuration at all, for
	// PackageCacheEnabled's reason above.
	//
	// The one thing an operator is deciding here is not disk: every
	// branch of a repository shares that repository's cache, so anything
	// one branch's CI writes another branch's CI can read back. The pane
	// says so beside the switch, as does model.Config.ActionsCacheEnabled
	// and pkg/actionscache's package doc.
	ActionsCacheEnabled bool `json:"actionsCacheEnabled"`
	// ActionsCacheSizeGB is the maximum disk space (in GiB) that cache
	// may use before it evicts the entries nothing has read for longest,
	// and ActionsCacheSizeGBDefault is grain's own (0 means that
	// default) -- the same raw-plus-default shape PackageCacheSizeGB has
	// above, and for the same reason.
	ActionsCacheSizeGB        int `json:"actionsCacheSizeGb"`
	ActionsCacheSizeGBDefault int `json:"actionsCacheSizeGbDefault"`
	// CIRunHistory, CIStepOutputMB, CIStepOutputDays and CIRunOutputKB
	// are model.Config's own fields of the same names: how long this
	// deployment's grain CI history lives, records and step output
	// bounded separately (ci.StoredRunRetention, grain/task-269).
	//
	// Reported raw, so a 0 in the pane reads as "grain's own" and shows
	// the *Default beside it as a placeholder -- the same shape
	// PackageCacheSizeGB above has, and for the same reason: an operator
	// who has chosen nothing should see what they are getting without
	// that choice being written down as though they had made it.
	CIRunHistory     int `json:"ciRunHistory"`
	CIStepOutputMB   int `json:"ciStepOutputMb"`
	CIStepOutputDays int `json:"ciStepOutputDays"`
	CIRunOutputKB    int `json:"ciRunOutputKb"`
	// The four defaults behind them, in the same units.
	CIRunHistoryDefault     int `json:"ciRunHistoryDefault"`
	CIStepOutputMBDefault   int `json:"ciStepOutputMbDefault"`
	CIStepOutputDaysDefault int `json:"ciStepOutputDaysDefault"`
	CIRunOutputKBDefault    int `json:"ciRunOutputKbDefault"`
	// CICacheMB and the six below it are model.Config's own fields of the
	// same names: what grain CI's job cache and its collected artifacts
	// may occupy on this deployment's data disk (ci.StorageBounds,
	// grain/task-317). They were daemon flags until then, which is why
	// the System pane could report what each was holding and offer no way
	// to move the ceiling it was holding it against.
	//
	// Reported raw beside their defaults, exactly as the CI history's
	// four above are, and read the same way: 0 is "grain's own" and the
	// pane shows the *Default as a placeholder. Unlike those four, a
	// negative value is refused rather than meaning "no bound at all" --
	// ci.StorageBoundsFrom has why a directory on the volume this
	// deployment dispatches out of has no unbounded reading.
	CICacheMB        int `json:"ciCacheMb"`
	CICacheEntries   int `json:"ciCacheEntries"`
	CICacheDays      int `json:"ciCacheDays"`
	CIArtifactMB     int `json:"ciArtifactMb"`
	CIArtifactRunMB  int `json:"ciArtifactRunMb"`
	CIArtifactFileMB int `json:"ciArtifactFileMb"`
	CIArtifactDays   int `json:"ciArtifactDays"`
	// The seven defaults behind them, in the same units -- what a blank
	// field is showing the placeholder of, and what the sweep is applying
	// where nobody has chosen (ci.DefaultStorageBounds).
	CICacheMBDefault        int `json:"ciCacheMbDefault"`
	CICacheEntriesDefault   int `json:"ciCacheEntriesDefault"`
	CICacheDaysDefault      int `json:"ciCacheDaysDefault"`
	CIArtifactMBDefault     int `json:"ciArtifactMbDefault"`
	CIArtifactRunMBDefault  int `json:"ciArtifactRunMbDefault"`
	CIArtifactFileMBDefault int `json:"ciArtifactFileMbDefault"`
	CIArtifactDaysDefault   int `json:"ciArtifactDaysDefault"`
	// CIJobsPerRun is model.Config's own field of the same name: an
	// optional ceiling on how many sandboxes a single grain CI run may
	// hold at once (0 means no per-run limit, leaving the deployment's
	// sandbox budget and any team max_ci_jobs ceiling as the only bounds).
	CIJobsPerRun int `json:"ciJobsPerRun"`
	// CIAgeBoost and CIReserveAfter are how the CI gate orders the jobs
	// its capacity is holding back -- model.Config's own fields of the
	// same names, as Go duration strings ("2m"), the same syntax
	// PollInterval above uses.
	//
	// Empty means this deployment has chosen neither, which is pkg/ci's
	// own window shown beside it as a placeholder (CIAgeBoostDefault,
	// CIReserveAfterDefault) -- the CI history's shape one section up,
	// for the same reason: what an operator is getting should be visible
	// without being written down as a choice they made.
	CIAgeBoost            string `json:"ciAgeBoost"`
	CIReserveAfter        string `json:"ciReserveAfter"`
	CIAgeBoostDefault     string `json:"ciAgeBoostDefault"`
	CIReserveAfterDefault string `json:"ciReserveAfterDefault"`
	// NetworkAccess is the deployment-wide default for how much of the
	// internet a run's sandbox may reach -- model.Config's own field of
	// the same name (grain/task-269), for every task that names none of
	// its own. Reported resolved rather than raw: a stored "" is
	// answered here as model.NetworkOpen, so a pane shows the network
	// sandboxes actually get rather than a blank that reads as "off".
	//
	// NetworkAccesses is the vocabulary a picker offers, in grain's own
	// order (model.NetworkAccesses()), for the same reason
	// AgentFrameworks is reported beside AgentFramework.
	//
	// Meaningless, like the shape above, under host-directory
	// sandboxing, and annotated by the same SandboxShapeIgnored: a
	// directory on this host has the host's own network, and nothing
	// here can take it away.
	NetworkAccess   string   `json:"networkAccess"`
	NetworkAccesses []string `json:"networkAccesses"`
	// SandboxShapeIgnored is true when the three numbers above shape
	// nothing on this deployment, because the sandbox backend it runs has
	// no shape at all -- Config.SandboxShapeIgnored's own doc comment for
	// which backend that is and how the daemon decides. A pane (and
	// `grain settings`) says so beside the numbers rather than showing
	// them bare: an unannotated "2 vCPUs / 2048 MiB" reads as the cap
	// runs are held to, and under host-directory sandboxing there is no
	// cap (grain/task-9).
	//
	// The values themselves are still reported, and still editable: they
	// are stored either way, exactly like every other granule-only
	// setting here, and an operator setting a shape up before switching
	// a deployment to -granule-sandboxes is doing something reasonable.
	//
	// omitempty, and false meaning "nothing to say": a UI that cannot
	// speak for a backend leaves this out, and so does an older daemon
	// that predates the field -- both of which should read as the
	// unannotated pane there has always been rather than as a claim that
	// the shape applies.
	SandboxShapeIgnored bool `json:"sandboxShapeIgnored,omitempty"`
	// SandboxCommitment is what MaxWorkers, MaxMergers and the two shape
	// settings above commit this deployment's sandbox host to when every
	// slot is full, held against what that machine has -- its own type's
	// doc comment for the whole of it, and for why the question those
	// four fields ask together was going unasked while each of them
	// individually was checked (grain/task-223).
	//
	// Or, where the backend applies no shape at all (SandboxShapeIgnored
	// above), what MaxWorkers and MaxMergers commit it to on their own:
	// that many concurrent runs on that machine with nothing capping any
	// of them, which SandboxCommitment.Uncapped says and every reader of
	// one has to branch on (grain/task-285).
	//
	// nil is "nothing to say", which covers every deployment this
	// arithmetic does not apply to: a UI with no backend to speak for, a
	// host that cannot be read, a deployment with no worker slots
	// configured at all. Reported on a
	// deployment whose numbers are fine as well as on one whose are not
	// -- the levels inside it are how a caller decides whether to say
	// anything, and an operator who wants to see the arithmetic before
	// it goes wrong should be able to.
	SandboxCommitment *SandboxCommitment `json:"sandboxCommitment,omitempty"`
	// ShowClosedByDefault is model.Config's own field of the same name
	// (bwsalmon/agents#537): the deployment-wide default for whether a
	// task list starts out with closed tasks in it. Also mirrored onto
	// GET /api/config (handleConfig, tasks.go) so a list has it before
	// Settings has ever been opened this session -- it is what the
	// condition every view opens narrowed by leaves out
	// (taskFilters.js's defaultFilters).
	ShowClosedByDefault bool `json:"showClosedByDefault"`
	// EnvironmentName is model.Config's own field of the same name: what
	// this deployment is called on screen ("staging", "dev", whatever an
	// operator running more than one of these needs to tell them apart).
	// Empty is an unnamed deployment, and the UI shows nothing for it.
	// Also mirrored onto GET /api/config (configResponse, tasks.go) the
	// same way ShowClosedByDefault is, since the frontend needs it on
	// first paint rather than only once Settings has been opened.
	//
	// Deliberately not omitempty, like DefaultCapabilities and
	// PendingRestart above: the frontend merges an update response over
	// the settings it already has, so a name cleared back to nothing has
	// to arrive as present-and-empty rather than as an absent key leaving
	// the old one on screen.
	EnvironmentName string `json:"environmentName"`
	// PromptExtension is model.Config's own field of the same name
	// (grain/task-114): the standing instructions every run dispatched
	// here is given on top of the prompt grain builds for it. Empty adds
	// nothing. Deployment-wide only -- a repo adds its own on the repos
	// pane (RepoDefaults.PromptExtension) and a task overrides both on
	// the new-task form, neither of which is folded in here, for the same
	// reason DefaultCapabilities does not fold in the per-repo layer: a
	// field that mixed the two would describe a deployment-wide setting
	// that only some runs actually get.
	//
	// Deliberately not omitempty, like EnvironmentName above and for the
	// same reason: the frontend merges an update response over the
	// settings it already has, so text cleared back to nothing has to
	// arrive as present-and-empty rather than as an absent key leaving
	// the old value on screen.
	PromptExtension  string `json:"promptExtension"`
	RepairTemplateID string `json:"repairTemplateID"`
	// TaskBranchTemplate controls names assigned to newly created tasks.
	TaskBranchTemplate string `json:"taskBranchTemplate"`
	// DeploymentID is model.Config's own: the id {deployment} expands to
	// in TaskBranchTemplate, empty until a system admin sets one.
	// DeploymentIDDefault is the random one used meanwhile, derived from
	// the installation id, shown so the box can say what empty means.
	DeploymentID        string `json:"deploymentId"`
	DeploymentIDDefault string `json:"deploymentIdDefault"`
	// TaskBranchTemplateDefault is model.DefaultTaskBranchTemplate, the
	// template a deployment seeded since grain/task-3091 starts with.
	// System -> Agents & Task Branches offers it to a deployment still on
	// the empty template (grain/task-{id}) as a one-line opt-in.
	TaskBranchTemplateDefault string `json:"taskBranchTemplateDefault"`
	// Capabilities is every capability grain ships a provider for, with
	// this deployment's own readiness computed against it -- capability_
	// status.go's own CapabilityStatus, bwsalmon/agents#611. Always
	// populated, even before Settings has ever been saved (GetSettings),
	// since self-debug/self-repair/bootstrap-playbooks need no
	// configuration to be ready and an operator setting up a fresh
	// deployment still benefits from seeing that
	// gcp-key/gemini-key/github-sandbox are not yet.
	Capabilities []CapabilityStatus `json:"capabilities"`
	// DefaultCapabilities is model.Config's own field of the same name:
	// the capability ids every task filed on this deployment starts out
	// holding, unless whoever files it says otherwise. Reported as
	// stored, including an id this build no longer offers -- CreateTask
	// skips such an entry rather than failing (that field's own doc
	// comment), and an operator can only clear one they can see.
	//
	// Each entry is also flagged on the Capabilities list above
	// (CapabilityStatus.Default), so the pane that says whether a
	// capability is ready and grantable says in the same view whether
	// every new task is being filed with it -- "not ready" matters a
	// great deal more for a capability every task holds than for one
	// nobody has ticked.
	//
	// Deployment-wide only. A repo can default capabilities of its own on
	// top of these (model.RepoConfig.DefaultCapabilities, edited on the
	// repos pane), which is reported here as the repos each capability is
	// defaulted on (CapabilityStatus.DefaultRepos) rather than folded
	// into this list -- a set that mixed the two would describe a
	// deployment-wide default that only some tasks actually get.
	//
	// Deliberately not omitempty, for the same reason PendingRestart is
	// not: the frontend merges an update response over the settings it
	// already has, so a set cleared back to nothing has to arrive as
	// present-and-null rather than as an absent key leaving the old one
	// on screen.
	DefaultCapabilities []string `json:"defaultCapabilities"`
	// ApprovedByDefault and AutoMergeByDefault are model.Config's own
	// fields of the same name (bwsalmon/agents#612): deployment-wide
	// defaults for whether a new task's "Queue immediately" and
	// "Auto-merge once checks pass" checkboxes start checked in
	// NewTaskOverlay.jsx. Also mirrored onto GET /api/config
	// (configResponse, tasks.go) the same way ShowClosedByDefault is, so
	// that form has them to seed its checkboxes with before Settings has
	// ever been opened this session.
	//
	// Both are true on a deployment that has stored no configuration at
	// all, not false: unlike ShowClosedByDefault, off is not what "never
	// chosen" means here (model.DefaultConfig), and a pane describing an
	// unconfigured deployment should say what filing a task there would
	// actually do.
	ApprovedByDefault  bool `json:"approvedByDefault"`
	AutoMergeByDefault bool `json:"autoMergeByDefault"`
	// UpgradeCheckMode and UpgradeCheckPausesPipeline are
	// model.Config's own fields of the same name (grain/task-38): what
	// grain does with an upgrade of itself once it has installed one and
	// restarted -- whether it files a task to check the deployment that
	// came back up, whether that task lands queued or as a proposal, and
	// whether the rest of the queue is held until the check is done.
	//
	// Never empty coming out of here: an unset stored value reads back as
	// model.UpgradeCheckAuto, the same defaulting AgentFramework above
	// does, so a pane describing a deployment that has never chosen says
	// what an upgrade there would actually do.
	UpgradeCheckMode string `json:"upgradeCheckMode"`
	// UpgradeCheckModes is that setting's whole vocabulary, in the order
	// a picker should offer it -- sent so the pane does not carry its own
	// copy of a list this package already has (the same reason
	// GeminiEfforts rides beside GeminiEffort).
	UpgradeCheckModes          []string `json:"upgradeCheckModes"`
	UpgradeCheckPausesPipeline bool     `json:"upgradeCheckPausesPipeline"`
	// AutoUpgradeChannel and AutoUpgradeTime are model.Config's own
	// fields: the branch this deployment upgrades itself onto once a day,
	// "" for none, and the "HH:MM" on TimeZone's clock it does it at
	// (orchestrator's reconcileAutoUpgrade). AutoUpgradeTime is never
	// empty coming out of here -- an unset one reads back as
	// model.DefaultAutoUpgradeTime, which is what choosing a channel
	// without a time would save.
	AutoUpgradeChannel string `json:"autoUpgradeChannel"`
	AutoUpgradeTime    string `json:"autoUpgradeTime"`
	// TimeZone is model.Config.TimeZone: the IANA zone every timestamp the
	// frontend prints is shown in. Never empty coming out of here -- an
	// unset stored value reads back as model.DefaultTimeZone.
	TimeZone      string               `json:"timeZone"`
	AgentBackends []model.AgentBackend `json:"agentBackends"`
	DefaultModel  string               `json:"defaultModel,omitempty"`
	// AgentCLIDefaults is what a connection with no cliPath of its own
	// runs, by binary ("claude", "codex", "agy"): the deployment flag's
	// path, or "" for the daemon's own PATH (Config.AgentCLIDefaults).
	// The connection page's hint for its CLI path field.
	AgentCLIDefaults map[string]string `json:"agentCliDefaults,omitempty"`
	// AgentKeysEnabled mirrors secretsResponse's own Enabled: whether
	// this UI has a secrets store to write an agent credential into at
	// all. False (a UI with no colocated store) is what tells the pane
	// to explain that rather than offer two fields whose every use
	// would 404.
	AgentKeysEnabled bool `json:"agentKeysEnabled"`
	// These report whether each framework's own credential is actually
	// in this deployment's secrets database -- agent_keys.go's own
	// agentKeysSet, mirrored onto Settings so the pane that picks a
	// framework can say, in the same view, whether the one being picked
	// can run at all. Presence only, never the value; all false for a UI
	// with no secrets store (Config.Secrets nil), the same
	// nil-means-unavailable reading every other check built on it takes.
	GeminiAPIKeySet     bool `json:"geminiApiKeySet"`
	ClaudeOAuthTokenSet bool `json:"claudeOAuthTokenSet"`
	OpenAIAPIKeySet     bool `json:"openaiApiKeySet"`
	CodexAuthJSONSet    bool `json:"codexAuthJSONSet"`
	// AgentKeyChecksEnabled is whether this deployment can go and ask the
	// vendor whether one of those credentials still works
	// (Config.AgentKeyChecks, POST /api/agent-keys/{framework}/check) --
	// CapabilityStatus.Checkable's own role for the Capabilities tab,
	// reported once here because the answer is the same for all three
	// frameworks. False means no pane should offer a button that could
	// only ever 404.
	AgentKeyChecksEnabled bool `json:"agentKeyChecksEnabled"`
	// RestartRequired names every setting on this pane that a running
	// daemon cannot adopt on its own, by the JSON field it is called
	// above -- restartOnlySettings' own list. Constant, reported even on
	// a deployment that has never saved settings at all, since the point
	// of it is to annotate a field *before* anyone changes it: a pane
	// that only said so afterwards would be telling an operator about a
	// restart they have already earned.
	//
	// Everything not named here takes effect without a restart, which is
	// grain's default and the reason this list is worth reporting at all
	// (pkg/cli/daemon.go's liveConfig has the full accounting of what
	// applies each of the rest, and when).
	RestartRequired []string `json:"restartRequired"`
	// PendingRestart is the subset of RestartRequired whose stored value
	// differs from what the daemon is actually running with -- saved, but
	// not in effect until someone restarts it. Always empty on a UI with
	// no Config.RunningConfig to compare against (that field's own doc
	// comment), which is the same answer as "nothing is pending" because
	// there is nothing actionable to tell those two apart on.
	//
	// Deliberately not omitempty: the frontend merges an update response
	// over the settings it already has, so this has to be present-and-
	// null when a change is undone rather than absent and leaving a stale
	// warning on screen.
	PendingRestart []string `json:"pendingRestart"`
	// Unrunnable is every setting above whose stored value no daemon can
	// run on, and which this one has therefore substituted a usable value
	// for (pkg/cli/daemon.go's runnableStoredConfig). It is the third
	// state beside PendingRestart, and a different one: PendingRestart is
	// "stored, and it will be in force once somebody restarts", while
	// this is "stored, and it is never going to be in force, because
	// grain cannot run on it at all" -- a restart would substitute the
	// same value again.
	//
	// The fields above report the substituted value rather than the
	// stored one whenever this names them, so what this answer says about
	// the deployment stays what the deployment is actually doing (the
	// same reading NetworkAccess, AgentFramework and the sandbox shape
	// defaults already take), and so a pane pre-filled from it holds
	// something a save would accept -- ui.UpdateSettings refuses to store
	// a poll interval of 0s, which is the value the pane itself would
	// otherwise have put in the form. Each entry carries the stored value
	// it replaced, so nothing is hidden: it is reported beside the
	// setting rather than in place of it.
	//
	// Always empty on a UI with no Config.RunningConfig, exactly as
	// PendingRestart is: with no daemon to speak for, there is no
	// substitution in force to report and no value to report in place of
	// the stored one. A settings row like this is caught before it ever
	// merges by `grain state check` (pkg/staterepo/statecheck.
	// UnrunnableSettings), which is the reading that needs no daemon.
	//
	// Not omitempty, for the same reason PendingRestart is not: the
	// frontend merges an update response over the settings it already
	// holds, so a save that fixes the row has to send the emptiness that
	// clears the warning.
	Unrunnable []UnrunnableSetting `json:"unrunnable"`
	// ExternalConfigRepo is the external Git repository URL when configuration
	// is managed in read-only Infrastructure-as-Code mode (state-repo.json).
	ExternalConfigRepo   string `json:"externalConfigRepo,omitempty"`
	ExternalConfigBranch string `json:"externalConfigBranch,omitempty"`
	// SettingsLocked is model.Config.SettingsLocked: whether non-bootstrap
	// settings are locked in the UI and REST API in favour of config
	// repository pull requests. False by default.
	SettingsLocked bool `json:"settingsLocked"`
}

type UnrunnableSetting struct {
	Key     string `json:"key"`
	Stored  string `json:"stored"`
	Running string `json:"running"`
}

const maxEnvironmentNameLen = 32

type restartOnlySetting struct {
	Key     string
	Differs func(stored, running model.Config) bool
}

var restartOnlySettings = []restartOnlySetting{
	{
		Key: "sandboxBackend",
		Differs: func(stored, running model.Config) bool {
			return stored.SandboxBackend != running.SandboxBackend
		},
	},
	{
		Key: "k8sNamespace",
		Differs: func(stored, running model.Config) bool {
			return firstNonEmptyString(stored.K8sNamespace, model.DefaultK8sNamespace) !=
				firstNonEmptyString(running.K8sNamespace, model.DefaultK8sNamespace)
		},
	},
	{
		Key: "k8sKubectl",
		Differs: func(stored, running model.Config) bool {
			return firstNonEmptyString(stored.K8sKubectl, model.DefaultK8sKubectl) !=
				firstNonEmptyString(running.K8sKubectl, model.DefaultK8sKubectl)
		},
	},
	{
		Key: "k8sSandboxImage",
		Differs: func(stored, running model.Config) bool {
			return firstNonEmptyString(stored.K8sSandboxImage, model.DefaultK8sSandboxImage) !=
				firstNonEmptyString(running.K8sSandboxImage, model.DefaultK8sSandboxImage)
		},
	},
	{
		Key: "k8sGranuleImage",
		Differs: func(stored, running model.Config) bool {
			return firstNonEmptyString(stored.K8sGranuleImage, model.DefaultSandboxImage) !=
				firstNonEmptyString(running.K8sGranuleImage, model.DefaultSandboxImage)
		},
	},
	{
		Key: "k8sWorkspace",
		Differs: func(stored, running model.Config) bool {
			return firstNonEmptyString(stored.K8sWorkspace, k8sWorkspaceDefault(stored.SandboxBackend)) !=
				firstNonEmptyString(running.K8sWorkspace, k8sWorkspaceDefault(running.SandboxBackend))
		},
	},
	{
		Key:     "k8sGitProxyHost",
		Differs: func(stored, running model.Config) bool { return stored.K8sGitProxyHost != running.K8sGitProxyHost },
	},
	{
		Key:     "k8sSandboxService",
		Differs: func(stored, running model.Config) bool { return stored.K8sSandboxService != running.K8sSandboxService },
	},
	{
		Key:     "k8sServiceAccount",
		Differs: func(stored, running model.Config) bool { return stored.K8sServiceAccount != running.K8sServiceAccount },
	},
	{
		Key: "k8sNodeSelector",
		Differs: func(stored, running model.Config) bool {
			return !slices.Equal(stored.K8sNodeSelector, running.K8sNodeSelector)
		},
	},
	{
		Key:     "k8sPrivileged",
		Differs: func(stored, running model.Config) bool { return stored.K8sPrivileged != running.K8sPrivileged },
	},
	{
		Key:     "k8sNetworkPolicy",
		Differs: func(stored, running model.Config) bool { return stored.K8sNetworkPolicy != running.K8sNetworkPolicy },
	},
	{
		Key: "k8sDeployment",
		Differs: func(stored, running model.Config) bool {
			return firstNonEmptyString(stored.K8sDeployment, model.DefaultK8sDeployment) !=
				firstNonEmptyString(running.K8sDeployment, model.DefaultK8sDeployment)
		},
	},
	{
		Key: "k8sContainer",
		Differs: func(stored, running model.Config) bool {
			return firstNonEmptyString(stored.K8sContainer, model.DefaultK8sContainer) !=
				firstNonEmptyString(running.K8sContainer, model.DefaultK8sContainer)
		},
	},
	{
		Key: "k8sPriorityClassInteractive",
		Differs: func(stored, running model.Config) bool {
			return stored.K8sPriorityClassInteractive != running.K8sPriorityClassInteractive
		},
	},
	{
		Key: "k8sPriorityClassAgent",
		Differs: func(stored, running model.Config) bool {
			return stored.K8sPriorityClassAgent != running.K8sPriorityClassAgent
		},
	},
	{
		Key: "k8sPriorityClassCi",
		Differs: func(stored, running model.Config) bool {
			return stored.K8sPriorityClassCI != running.K8sPriorityClassCI
		},
	},
	{
		Key:     "githubHost",
		Differs: func(stored, running model.Config) bool { return stored.GitHubHost != running.GitHubHost },
	},
	{
		Key: "githubInsecureHttp",
		Differs: func(stored, running model.Config) bool {
			return stored.GitHubInsecureHTTP != running.GitHubInsecureHTTP
		},
	},
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v := strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func k8sWorkspaceDefault(backend model.SandboxBackend) string {
	if backend == model.SandboxBackendKubernetesGranule {
		return model.DefaultK8sGranuleWorkspace
	}
	return model.DefaultK8sWorkspace
}

func restartRequiredKeys() []string {
	keys := make([]string, 0, len(restartOnlySettings))
	for _, s := range restartOnlySettings {
		keys = append(keys, s.Key)
	}
	return keys
}

func (c *Client) pendingRestart(stored model.Config, sandboxImgDefault, granuleImgDefault string) []string {
	if c.Config.RunningConfig == nil {
		return nil
	}
	if sandboxImgDefault == "" {
		sandboxImgDefault = model.DefaultK8sSandboxImage
	}
	if granuleImgDefault == "" {
		granuleImgDefault = model.DefaultSandboxImage
	}
	running := c.Config.RunningConfig()
	var pending []string
	for _, s := range restartOnlySettings {
		var differs bool
		switch s.Key {
		case "k8sSandboxImage":
			differs = firstNonEmptyString(stored.K8sSandboxImage, sandboxImgDefault) !=
				firstNonEmptyString(running.K8sSandboxImage, sandboxImgDefault)
		case "k8sGranuleImage":
			differs = firstNonEmptyString(stored.K8sGranuleImage, granuleImgDefault) !=
				firstNonEmptyString(running.K8sGranuleImage, granuleImgDefault)
		default:
			differs = s.Differs(stored, running)
		}
		if differs {
			pending = append(pending, s.Key)
		}
	}
	return pending
}

// durationSetting is a stored duration as this pane reports one: the Go
// syntax a save takes it back in, and the empty string for a setting
// nobody has chosen.
//
// Empty rather than "0s", which is the reading every other unset number
// here gets: a pane shows the default beside it as a placeholder, and
// writing "0s" into the box would turn the next save of any other field
// into a choice this one never made.
func durationSetting(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}

// parseDurationSetting is durationSetting backwards: the Go duration a
// save carries, with the empty string -- and a bare "0" -- meaning "give
// this setting back to grain's own default", which is what the stored
// zero behind it means.
func parseDurationSetting(key, value string) (time.Duration, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(trimmed)
	if err != nil {
		return 0, validationErrorf("%s: %v", key, err)
	}
	return d, nil
}

func (c *Client) runnableSettings(stored model.Config) (model.Config, []UnrunnableSetting) {
	if c.Config.RunningConfig == nil {
		return stored, nil
	}
	running := c.Config.RunningConfig()
	var out []UnrunnableSetting
	note := func(key, storedVal, runningVal string) {
		out = append(out, UnrunnableSetting{Key: key, Stored: storedVal, Running: runningVal})
	}
	if stored.PollInterval <= 0 && running.PollInterval > 0 {
		note("pollInterval", stored.PollInterval.String(), running.PollInterval.String())
		stored.PollInterval = running.PollInterval
	}
	if stored.SandboxBackend == "" {
		stored.SandboxBackend = running.SandboxBackend
	} else if (!model.ValidSandboxBackend(stored.SandboxBackend) ||
		!slices.Contains(c.availableSandboxBackends(), stored.SandboxBackend)) &&
		model.ValidSandboxBackend(running.SandboxBackend) {
		note("sandboxBackend", string(stored.SandboxBackend), string(running.SandboxBackend))
		stored.SandboxBackend = running.SandboxBackend
	}
	return stored, out
}

// ciStorageDefaults is grain's own bounds for CI's two directories, in
// the units the pane edits them in: what a blank field shows as its
// placeholder, and what the sweep is applying where nobody has chosen.
//
// Resolved through pkg/ci rather than written down here, so that the
// number the form offers is the number in force (ci.StorageBounds), and
// converted once rather than at each of the two places a response is
// built.
type ciStorageBoundDefaults struct {
	cacheMB      int
	cacheEntries int
	cacheDays    int

	artifactMB     int
	artifactRunMB  int
	artifactFileMB int
	artifactDays   int
}

var ciStorageDefaults = func() ciStorageBoundDefaults {
	bounds := ci.DefaultStorageBounds()
	return ciStorageBoundDefaults{
		cacheMB:        int(bounds.Cache.MaxBytes >> 20),
		cacheEntries:   bounds.Cache.MaxEntries,
		cacheDays:      int(bounds.Cache.MaxAge / (24 * time.Hour)),
		artifactMB:     int(bounds.Artifacts.MaxBytes >> 20),
		artifactRunMB:  int(bounds.Artifacts.MaxRunBytes >> 20),
		artifactFileMB: int(bounds.Artifacts.MaxArtifactBytes >> 20),
		artifactDays:   int(bounds.Artifacts.MaxAge / (24 * time.Hour)),
	}
}()

func (c *Client) settingsFrom(ctx context.Context, cfg model.Config, repoConfigs []model.RepoConfig) Settings {
	cfg, unrunnable := c.runnableSettings(cfg)
	geminiKeySet, claudeTokenSet, openaiKeySet, codexAuthSet := c.agentKeysSet()
	sandboxImgDefault, granuleImgDefault, buildVersion := c.resolveK8sImageDefaults(ctx, &cfg, repoConfigs)
	sandboxWarn, granuleWarn := c.resolveK8sImageWarnings(
		ctx,
		firstNonEmptyString(cfg.K8sSandboxImage, sandboxImgDefault),
		c.k8sGranuleImageForWarn(cfg, granuleImgDefault),
	)
	sandboxWarn = joinWarnings(sandboximage.StaleStockPin(cfg.K8sSandboxImage, sandboxImgDefault, nil), sandboxWarn)
	granuleWarn = joinWarnings(sandboximage.StaleStockPin(cfg.K8sGranuleImage, granuleImgDefault, nil), granuleWarn)
	out := Settings{
		Configured:               true,
		PollInterval:             cfg.PollInterval.String(),
		MaxWorkers:               cfg.MaxWorkers,
		MaxMergesPerRepo:         cfg.MaxMergesPerRepo,
		MergeBatchSize:           cfg.MergeBatchSizeOr(),
		MergeBatchSizeChosen:     max(cfg.MergeBatchSize, 0),
		MergeGatherWindow:        durationSetting(cfg.MergeGatherWindow),
		MergeGatherWindowDefault: model.DefaultMergeGatherWindow.String(),
		MaxAutoSubmitDepth:       cfg.AutoSubmitLimits().Depth,
		MaxAutoSubmitFanOut:      cfg.AutoSubmitLimits().FanOut,
		GeminiEfforts:            antigravity.Efforts(),
		CodexEfforts:             codex.Efforts(),
		MaxAgentTurns:            cfg.MaxAgentTurns,
		GitHubHost:               cfg.GitHubHost,
		GitHubInsecureHTTP:       cfg.GitHubInsecureHTTP,
		GCPProject:               cfg.GCPProject,
		GCPServiceAccountEmail:   cfg.GCPServiceAccountEmail,
		GCPServiceAccountCount:   cfg.GCPServiceAccountCount,
		IAPBackendService:        cfg.IAPBackendService,
		IAPResource:              cfg.IAPResource,

		NewestFirst:            cfg.NewestFirst,
		SandboxBackend:         string(cfg.SandboxBackend),
		SandboxBackends:        c.sandboxBackendNames(),
		SandboxBackendOptions:  c.sandboxBackendOptions(),
		SandboxCPUs:            cfg.SandboxCPUs,
		SandboxMemoryMB:        cfg.SandboxMemoryMB,
		SandboxDiskGB:          cfg.SandboxDiskGB,
		K8sNamespace:           cfg.K8sNamespace,
		K8sNamespaceDefault:    model.DefaultK8sNamespace,
		K8sKubectl:             cfg.K8sKubectl,
		K8sKubectlDefault:      model.DefaultK8sKubectl,
		K8sSandboxImage:        cfg.K8sSandboxImage,
		K8sSandboxImageDefault: sandboxImgDefault,
		K8sGranuleImage:        cfg.K8sGranuleImage,
		K8sGranuleImageDefault: granuleImgDefault,
		SandboxImageMirror:     cfg.SandboxImageMirror,
		DaemonBuildVersion:     buildVersion,
		K8sSandboxImageWarning: sandboxWarn,
		K8sGranuleImageWarning: granuleWarn,
		K8sWorkspace:           cfg.K8sWorkspace,
		K8sWorkspaceDefault:    k8sWorkspaceDefault(cfg.SandboxBackend),
		K8sGitProxyHost:        cfg.K8sGitProxyHost,
		K8sSandboxService:      cfg.K8sSandboxService,
		K8sServiceAccount:      cfg.K8sServiceAccount,
		K8sNodeSelector:        cfg.K8sNodeSelector,
		K8sPrivileged:          cfg.K8sPrivileged,
		K8sNetworkPolicy:       cfg.K8sNetworkPolicy,
		K8sDeployment:          cfg.K8sDeployment,
		K8sDeploymentDefault:   model.DefaultK8sDeployment,
		K8sContainer:           cfg.K8sContainer,
		K8sContainerDefault:    model.DefaultK8sContainer,

		K8sPriorityClassInteractive:        cfg.K8sPriorityClassInteractive,
		K8sPriorityClassAgent:              cfg.K8sPriorityClassAgent,
		K8sPriorityClassCI:                 cfg.K8sPriorityClassCI,
		K8sPriorityClassInteractiveDefault: model.DefaultK8sPriorityClassInteractive,
		K8sPriorityClassAgentDefault:       model.DefaultK8sPriorityClassAgent,
		K8sPriorityClassCIDefault:          model.DefaultK8sPriorityClassCI,
		PackageCacheEnabled:                cfg.PackageCacheEnabled,
		PackageCacheSizeGB:                 cfg.PackageCacheSizeGB,
		PackageCacheSizeGBDefault:          int(pkgcache.DefaultMaxBytes >> 30),
		ActionsCacheEnabled:                cfg.ActionsCacheEnabled,
		ActionsCacheSizeGB:                 cfg.ActionsCacheSizeGB,
		ActionsCacheSizeGBDefault:          int(actionscache.DefaultMaxBytes >> 30),
		CIRunHistory:                       cfg.CIRunHistory,
		CIStepOutputMB:                     cfg.CIStepOutputMB,
		CIStepOutputDays:                   cfg.CIStepOutputDays,
		CIRunOutputKB:                      cfg.CIRunOutputKB,
		CIRunHistoryDefault:                ci.DefaultStoredRunHistory,
		CIStepOutputMBDefault:              int(ci.DefaultStoredRunOutputBytes >> 20),
		CIStepOutputDaysDefault:            int(ci.DefaultStoredRunOutputAge / (24 * time.Hour)),
		CIRunOutputKBDefault:               ci.MaxStoredRunOutput >> 10,
		CICacheMB:                          cfg.CICacheMB,
		CICacheEntries:                     cfg.CICacheEntries,
		CICacheDays:                        cfg.CICacheDays,
		CIArtifactMB:                       cfg.CIArtifactMB,
		CIArtifactRunMB:                    cfg.CIArtifactRunMB,
		CIArtifactFileMB:                   cfg.CIArtifactFileMB,
		CIArtifactDays:                     cfg.CIArtifactDays,
		CICacheMBDefault:                   ciStorageDefaults.cacheMB,
		CICacheEntriesDefault:              ciStorageDefaults.cacheEntries,
		CICacheDaysDefault:                 ciStorageDefaults.cacheDays,
		CIArtifactMBDefault:                ciStorageDefaults.artifactMB,
		CIArtifactRunMBDefault:             ciStorageDefaults.artifactRunMB,
		CIArtifactFileMBDefault:            ciStorageDefaults.artifactFileMB,
		CIArtifactDaysDefault:              ciStorageDefaults.artifactDays,
		CIJobsPerRun:                       cfg.CIJobsPerRun,
		CIAgeBoost:                         durationSetting(cfg.CIAgeBoost),
		CIReserveAfter:                     durationSetting(cfg.CIReserveAfter),
		CIAgeBoostDefault:                  ci.DefaultAgeBoost.String(),
		CIReserveAfterDefault:              ci.DefaultReserveAfter.String(),
		SandboxCPUsDefault:                 granule.DefaultCPUs,
		SandboxMemoryMBDefault:             granule.DefaultMemoryMB,
		SandboxDiskGBDefault:               granule.DefaultDiskGB,
		SandboxCPUOvercommit:               cfg.SandboxCPUOvercommit,
		SandboxMemoryOvercommit:            cfg.SandboxMemoryOvercommit,
		SandboxMemoryReserveMB:             cfg.SandboxMemoryReserveMB,
		SandboxCPUOvercommitDefault:        model.DefaultCPUOvercommit,
		SandboxMemoryOvercommitDefault:     model.DefaultMemoryOvercommit,
		SandboxDiskOvercommit:              cfg.SandboxDiskOvercommit,
		SandboxDiskOvercommitDefault:       model.DefaultDiskOvercommit,
		SandboxMemoryReserveMBDefault:      c.sandboxMemoryReserveDefault(),
		SandboxOvercommit:                  cfg.SandboxOvercommit.Resolved(),
		ExternalAgentAPI:                   cfg.ExternalAgentAPI,
		SessionSandboxIdleTTL:              durationSetting(cfg.SessionSandboxIdleTTL),
		SessionSandboxIdleTTLDefault:       model.DefaultSessionSandboxIdleTTL.String(),
		SandboxDiskFloorShare:              cfg.SandboxDiskFloorShare,
		SandboxDiskFloorMinMB:              cfg.SandboxDiskFloorMinMB,
		SandboxDiskFloorShareDefault:       model.DefaultDiskFloorShare,
		SandboxDiskFloorMinMBDefault:       model.MinDiskFloorMB,
		SandboxDiskFloorMaxShare:           model.MaxDiskFloorShare,
		SandboxDiskFloors:                  c.sandboxDiskFloors(cfg.SandboxDiskFloor()),
		NetworkAccess:                      string(model.NormalizeNetworkAccess(cfg.NetworkAccess)),
		NetworkAccesses:                    networkAccessNames(),
		SandboxShapeIgnored:                c.Config.SandboxShapeIgnored,
		SandboxCommitment:                  c.sandboxCommitment(cfg),
		ShowClosedByDefault:                cfg.ShowClosedByDefault,
		SettingsLocked:                     cfg.SettingsLocked,
		EnvironmentName:                    cfg.EnvironmentName,
		PromptExtension:                    cfg.PromptExtension,
		RepairTemplateID:                   cfg.RepairTemplateID,
		TaskBranchTemplate:                 cfg.TaskBranchTemplate,
		DeploymentID:                       cfg.DeploymentID,
		Capabilities:                       c.capabilityStatuses(cfg, repoConfigs),
		DefaultCapabilities:                cfg.DefaultCapabilities,
		ApprovedByDefault:                  cfg.ApprovedByDefault,
		AutoMergeByDefault:                 cfg.AutoMergeByDefault,
		UpgradeCheckMode:                   model.NormalizeUpgradeCheckMode(cfg.UpgradeCheckMode),
		UpgradeCheckModes:                  model.UpgradeCheckModes(),
		UpgradeCheckPausesPipeline:         cfg.UpgradeCheckPausesPipeline,
		AutoUpgradeChannel:                 cfg.AutoUpgradeChannel,
		AutoUpgradeTime:                    autoUpgradeTimeOrDefault(cfg.AutoUpgradeTime),
		TimeZone:                           model.TimeZoneOrDefault(cfg.TimeZone),
		AgentBackends:                      cfg.AgentBackends,
		AgentCLIDefaults:                   c.Config.AgentCLIDefaults,
		DefaultModel:                       cfg.DefaultModel,
		AgentKeysEnabled:                   c.Config.Secrets != nil,
		GeminiAPIKeySet:                    geminiKeySet,
		ClaudeOAuthTokenSet:                claudeTokenSet,
		OpenAIAPIKeySet:                    openaiKeySet,
		CodexAuthJSONSet:                   codexAuthSet,
		AgentKeyChecksEnabled:              c.Config.AgentKeyChecks != nil,
		RestartRequired:                    restartRequiredKeys(),
		PendingRestart:                     c.pendingRestart(cfg, sandboxImgDefault, granuleImgDefault),
		Unrunnable:                         unrunnable,
	}
	c.resolveSandboxImageFields(ctx, cfg, sandboxImgDefault, granuleImgDefault).apply(&out)
	sources := computeSettingSources(cfg, out)
	out.Sources = sources
	out.SettingSources = sources
	return out
}

// sandboxDiskFloors is what floor comes to on each of this deployment's
// own volumes -- Config.SandboxVolumes' reading, and nil where there is
// nobody to ask (a UI not colocated with the host, `grain demo`).
//
// The floor is passed in rather than read from the row here for the
// reason that seam's own doc comment gives: this resolves the setting
// being *reported*, which right after a save is the one the daemon will
// apply on its next reconcile tick rather than the one it is applying
// now.
func (c *Client) sandboxDiskFloors(floor model.DiskFloor) []DiskUsage {
	if c.Config.SandboxVolumes == nil {
		return nil
	}
	return c.Config.SandboxVolumes(floor)
}

func defaultUnavailableSandboxBackendReason(backend model.SandboxBackend) string {
	switch backend {
	case model.SandboxBackendContainer:
		return "needs a container image and a git proxy host"
	case model.SandboxBackendGranule:
		return "needs SSH user/workspace/git proxy host for granule"
	case model.SandboxBackendHost:
		return "needs -host-sandboxes at daemon startup"
	case model.SandboxBackendNone:
		return "needs -disable-native-sandboxes at daemon startup"
	default:
		return "not configured on this deployment"
	}
}

func (c *Client) availableSandboxBackends() []model.SandboxBackend {
	if len(c.Config.SandboxBackends) > 0 {
		return c.Config.SandboxBackends
	}
	if len(c.Config.SandboxBackendOptions) > 0 {
		var out []model.SandboxBackend
		for _, opt := range c.Config.SandboxBackendOptions {
			if opt.Available {
				out = append(out, opt.Backend)
			}
		}
		return out
	}
	return nil
}

func (c *Client) sandboxBackendNames() []string {
	available := c.availableSandboxBackends()
	names := make([]string, len(available))
	for i, backend := range available {
		names[i] = string(backend)
	}
	return names
}

func (c *Client) sandboxBackendOptions() []model.SandboxBackendOption {
	if len(c.Config.SandboxBackendOptions) > 0 {
		return append([]model.SandboxBackendOption(nil), c.Config.SandboxBackendOptions...)
	}
	if len(c.Config.SandboxBackends) == 0 {
		return nil
	}
	all := model.SandboxBackends()
	out := make([]model.SandboxBackendOption, 0, len(all))
	for _, backend := range all {
		if slices.Contains(c.Config.SandboxBackends, backend) {
			out = append(out, model.SandboxBackendOption{
				Backend:   backend,
				Available: true,
			})
		} else {
			out = append(out, model.SandboxBackendOption{
				Backend:   backend,
				Available: false,
				Reason:    defaultUnavailableSandboxBackendReason(backend),
			})
		}
	}
	return out
}

func (c *Client) runningSandboxBackend() string {
	if c.Config.RunningConfig == nil {
		return ""
	}
	return string(c.Config.RunningConfig().SandboxBackend)
}

func networkAccessNames() []string {
	accesses := model.NetworkAccesses()
	names := make([]string, len(accesses))
	for i, a := range accesses {
		names[i] = string(a)
	}
	return names
}

func (c *Client) gitHubCredentialNames() []string {
	if c.Config.Credentials == nil {
		return nil
	}
	return c.Config.Credentials.Names()
}

// isGitHubCredentialSelector is whether a team may name name as its git
// credential: one of gitHubCredentialNames, or "anonymous" -- no
// credential at all, for public repositories, which is the selector the
// team git editor's "None (public)" saves (saveTypedCredential) and
// which no credential file ever carries. A deployment that lists no
// credentials checks nothing, as before.
func (c *Client) isGitHubCredentialSelector(name string) bool {
	if name == "anonymous" {
		return true
	}
	names := c.gitHubCredentialNames()
	return len(names) == 0 || slices.Contains(names, name)
}

// deploymentIDDefault is the deployment id branch names use while no
// system admin has set one. Empty if the installation id cannot be read,
// which leaves the box without a placeholder rather than failing the
// whole settings read over it.
func (c *Client) deploymentIDDefault(ctx context.Context) string {
	id, err := c.Store.InstanceID(ctx)
	if err != nil {
		return ""
	}
	return model.EffectiveDeploymentID("", id)
}

func (c *Client) GetSettings(ctx context.Context) (Settings, error) {
	cfg, err := c.Store.GetConfig(ctx)
	if err != nil {
		return Settings{}, err
	}
	repoConfigs, err := c.Store.ListRepoConfigs(ctx)
	if err != nil {
		return Settings{}, err
	}
	if cfg == nil {
		geminiKeySet, claudeTokenSet, openaiKeySet, codexAuthSet := c.agentKeysSet()
		def := capability.NewDeploymentConfig()
		sandboxImgDefault, granuleImgDefault, buildVersion := c.resolveK8sImageDefaults(ctx, &def, repoConfigs)
		sandboxWarn, granuleWarn := c.resolveK8sImageWarnings(
			ctx,
			firstNonEmptyString(def.K8sSandboxImage, sandboxImgDefault),
			firstNonEmptyString(def.K8sGranuleImage, granuleImgDefault),
		)
		out := Settings{
			MaxWorkers:                     def.MaxWorkers,
			DeploymentIDDefault:            c.deploymentIDDefault(ctx),
			TaskBranchTemplate:             def.TaskBranchTemplate,
			TaskBranchTemplateDefault:      model.DefaultTaskBranchTemplate,
			SandboxBackend:                 c.runningSandboxBackend(),
			SandboxBackends:                c.sandboxBackendNames(),
			SandboxBackendOptions:          c.sandboxBackendOptions(),
			SandboxCPUsDefault:             granule.DefaultCPUs,
			SandboxMemoryMBDefault:         granule.DefaultMemoryMB,
			SandboxDiskGBDefault:           granule.DefaultDiskGB,
			SandboxCPUOvercommit:           def.SandboxCPUOvercommit,
			SandboxMemoryOvercommit:        def.SandboxMemoryOvercommit,
			SandboxMemoryReserveMB:         def.SandboxMemoryReserveMB,
			SandboxCPUOvercommitDefault:    model.DefaultCPUOvercommit,
			SandboxMemoryOvercommitDefault: model.DefaultMemoryOvercommit,
			SandboxDiskOvercommit:          def.SandboxDiskOvercommit,
			SandboxDiskOvercommitDefault:   model.DefaultDiskOvercommit,
			SandboxOvercommit:              def.SandboxOvercommit.Resolved(),
			ExternalAgentAPI:               def.ExternalAgentAPI,
			SessionSandboxIdleTTL:          durationSetting(def.SessionSandboxIdleTTL),
			SessionSandboxIdleTTLDefault:   model.DefaultSessionSandboxIdleTTL.String(),
			SandboxDiskFloorShare:          def.SandboxDiskFloorShare,
			SandboxDiskFloorMinMB:          def.SandboxDiskFloorMinMB,
			SandboxDiskFloorShareDefault:   model.DefaultDiskFloorShare,
			SandboxDiskFloorMinMBDefault:   model.MinDiskFloorMB,
			SandboxDiskFloorMaxShare:       model.MaxDiskFloorShare,
			SandboxDiskFloors:              c.sandboxDiskFloors(def.SandboxDiskFloor()),
			SandboxShapeIgnored:            c.Config.SandboxShapeIgnored,
			K8sNamespace:                   def.K8sNamespace,
			K8sNamespaceDefault:            model.DefaultK8sNamespace,
			K8sKubectl:                     def.K8sKubectl,
			K8sKubectlDefault:              model.DefaultK8sKubectl,
			K8sSandboxImageDefault:         sandboxImgDefault,
			K8sGranuleImageDefault:         granuleImgDefault,
			SandboxImageMirror:             def.SandboxImageMirror,
			DaemonBuildVersion:             buildVersion,
			K8sSandboxImageWarning:         sandboxWarn,
			K8sGranuleImageWarning:         granuleWarn,
			K8sWorkspaceDefault:            k8sWorkspaceDefault(model.SandboxBackend(c.runningSandboxBackend())),
			K8sDeployment:                  def.K8sDeployment,
			K8sDeploymentDefault:           model.DefaultK8sDeployment,
			K8sContainer:                   def.K8sContainer,
			K8sContainerDefault:            model.DefaultK8sContainer,

			K8sPriorityClassInteractive:        def.K8sPriorityClassInteractive,
			K8sPriorityClassAgent:              def.K8sPriorityClassAgent,
			K8sPriorityClassCI:                 def.K8sPriorityClassCI,
			K8sPriorityClassInteractiveDefault: model.DefaultK8sPriorityClassInteractive,
			K8sPriorityClassAgentDefault:       model.DefaultK8sPriorityClassAgent,
			K8sPriorityClassCIDefault:          model.DefaultK8sPriorityClassCI,
			Capabilities:                       c.capabilityStatuses(model.Config{DefaultCapabilities: def.DefaultCapabilities}, repoConfigs),
			DefaultCapabilities:                def.DefaultCapabilities,
			AgentKeysEnabled:                   c.Config.Secrets != nil,
			GeminiAPIKeySet:                    geminiKeySet,
			ClaudeOAuthTokenSet:                claudeTokenSet,
			OpenAIAPIKeySet:                    openaiKeySet,
			CodexAuthJSONSet:                   codexAuthSet,
			AgentKeyChecksEnabled:              c.Config.AgentKeyChecks != nil,
			ApprovedByDefault:                  def.ApprovedByDefault,
			AutoMergeByDefault:                 def.AutoMergeByDefault,
			PackageCacheEnabled:                def.PackageCacheEnabled,
			PackageCacheSizeGB:                 def.PackageCacheSizeGB,
			PackageCacheSizeGBDefault:          int(pkgcache.DefaultMaxBytes >> 30),
			ActionsCacheEnabled:                def.ActionsCacheEnabled,
			ActionsCacheSizeGB:                 def.ActionsCacheSizeGB,
			ActionsCacheSizeGBDefault:          int(actionscache.DefaultMaxBytes >> 30),
			CIRunHistory:                       def.CIRunHistory,
			CIStepOutputMB:                     def.CIStepOutputMB,
			CIStepOutputDays:                   def.CIStepOutputDays,
			CIRunOutputKB:                      def.CIRunOutputKB,
			CIRunHistoryDefault:                ci.DefaultStoredRunHistory,
			CIStepOutputMBDefault:              int(ci.DefaultStoredRunOutputBytes >> 20),
			CIStepOutputDaysDefault:            int(ci.DefaultStoredRunOutputAge / (24 * time.Hour)),
			CIRunOutputKBDefault:               ci.MaxStoredRunOutput >> 10,
			CICacheMB:                          def.CICacheMB,
			CICacheEntries:                     def.CICacheEntries,
			CICacheDays:                        def.CICacheDays,
			CIArtifactMB:                       def.CIArtifactMB,
			CIArtifactRunMB:                    def.CIArtifactRunMB,
			CIArtifactFileMB:                   def.CIArtifactFileMB,
			CIArtifactDays:                     def.CIArtifactDays,
			CICacheMBDefault:                   ciStorageDefaults.cacheMB,
			CICacheEntriesDefault:              ciStorageDefaults.cacheEntries,
			CICacheDaysDefault:                 ciStorageDefaults.cacheDays,
			CIArtifactMBDefault:                ciStorageDefaults.artifactMB,
			CIArtifactRunMBDefault:             ciStorageDefaults.artifactRunMB,
			CIArtifactFileMBDefault:            ciStorageDefaults.artifactFileMB,
			CIArtifactDaysDefault:              ciStorageDefaults.artifactDays,
			MergeGatherWindow:                  durationSetting(def.MergeGatherWindow),
			MergeGatherWindowDefault:           model.DefaultMergeGatherWindow.String(),
			CIJobsPerRun:                       def.CIJobsPerRun,
			CIAgeBoost:                         durationSetting(def.CIAgeBoost),
			CIReserveAfter:                     durationSetting(def.CIReserveAfter),
			CIAgeBoostDefault:                  ci.DefaultAgeBoost.String(),
			CIReserveAfterDefault:              ci.DefaultReserveAfter.String(),
			UpgradeCheckMode:                   model.NormalizeUpgradeCheckMode(def.UpgradeCheckMode),
			UpgradeCheckModes:                  model.UpgradeCheckModes(),
			UpgradeCheckPausesPipeline:         def.UpgradeCheckPausesPipeline,
			AutoUpgradeChannel:                 def.AutoUpgradeChannel,
			AutoUpgradeTime:                    autoUpgradeTimeOrDefault(def.AutoUpgradeTime),
			TimeZone:                           model.TimeZoneOrDefault(def.TimeZone),
			NetworkAccess:                      string(model.NormalizeNetworkAccess(def.NetworkAccess)),
			NetworkAccesses:                    networkAccessNames(),
			SettingsLocked:                     def.SettingsLocked,
			RestartRequired:                    restartRequiredKeys(),
		}
		c.resolveSandboxImageFields(ctx, def, sandboxImgDefault, granuleImgDefault).apply(&out)
		sources := computeSettingSources(def, out)
		out.Sources = sources
		out.SettingSources = sources
		out.ExternalConfigRepo, out.ExternalConfigBranch = c.externalConfigRepo(ctx)
		return out, nil
	}
	settings := c.settingsFrom(ctx, *cfg, repoConfigs)
	settings.MergeBatching = c.mergeBatching(ctx, *cfg)
	settings.MergeGathering = c.mergeGathering(ctx)
	settings.DeploymentIDDefault = c.deploymentIDDefault(ctx)
	settings.TaskBranchTemplateDefault = model.DefaultTaskBranchTemplate
	settings.ExternalConfigRepo, settings.ExternalConfigBranch = c.externalConfigRepo(ctx)
	return settings, nil
}

type UpdateSettingsRequest struct {
	PollInterval            *string  `json:"pollInterval"`
	MaxWorkers              *int     `json:"maxWorkers"`
	MaxMergesPerRepo        *int     `json:"maxMergesPerRepo"`
	MergeBatchSize          *int     `json:"mergeBatchSize"`
	MergeGatherWindow       *string  `json:"mergeGatherWindow"`
	MaxAutoSubmitDepth      *int     `json:"maxAutoSubmitDepth"`
	MaxAutoSubmitFanOut     *int     `json:"maxAutoSubmitFanOut"`
	MaxAgentTurns           *int     `json:"maxAgentTurns"`
	GitHubHost              *string  `json:"githubHost"`
	GitHubInsecureHTTP      *bool    `json:"githubInsecureHttp"`
	GCPProject              *string  `json:"gcpProject"`
	GCPServiceAccountEmail  *string  `json:"gcpServiceAccountEmail"`
	GCPServiceAccountCount  *int     `json:"gcpServiceAccountCount"`
	IAPBackendService       *string  `json:"iapBackendService"`
	IAPResource             *string  `json:"iapResource"`
	NewestFirst             *bool    `json:"newestFirst"`
	SandboxBackend          *string  `json:"sandboxBackend"`
	SandboxCPUs             *int     `json:"sandboxCpus"`
	SandboxMemoryMB         *int     `json:"sandboxMemoryMb"`
	SandboxDiskGB           *int     `json:"sandboxDiskGb"`
	SandboxCPUOvercommit    *float64 `json:"sandboxCpuOvercommit"`
	SandboxMemoryOvercommit *float64 `json:"sandboxMemoryOvercommit"`
	SandboxDiskOvercommit   *float64 `json:"sandboxDiskOvercommit"`
	// SandboxOvercommit replaces the whole overcommit mode and its bounds
	// (model.OvercommitSettings); zero bounds are grain's defaults.
	SandboxOvercommit      *model.OvercommitSettings `json:"sandboxOvercommit"`
	ExternalAgentAPI       *bool                     `json:"externalAgentApi"`
	SessionSandboxIdleTTL  *string                   `json:"sessionSandboxIdleTtl"`
	SandboxMemoryReserveMB *int                      `json:"sandboxMemoryReserveMb"`
	SandboxDiskFloorShare  *float64                  `json:"sandboxDiskFloorShare"`
	SandboxDiskFloorMinMB  *int                      `json:"sandboxDiskFloorMinMb"`
	SandboxImage           *string                   `json:"sandboxImage"`
	SandboxImageMirror     *string                   `json:"sandboxImageMirror"`
	K8sNamespace           *string                   `json:"k8sNamespace"`
	K8sKubectl             *string                   `json:"k8sKubectl"`
	K8sSandboxImage        *string                   `json:"k8sSandboxImage"`
	K8sGranuleImage        *string                   `json:"k8sGranuleImage"`
	K8sWorkspace           *string                   `json:"k8sWorkspace"`
	K8sGitProxyHost        *string                   `json:"k8sGitProxyHost"`
	K8sSandboxService      *string                   `json:"k8sSandboxService"`
	K8sServiceAccount      *string                   `json:"k8sServiceAccount"`
	K8sNodeSelector        *[]string                 `json:"k8sNodeSelector"`
	K8sPrivileged          *bool                     `json:"k8sPrivileged"`
	K8sNetworkPolicy       *bool                     `json:"k8sNetworkPolicy"`
	K8sDeployment          *string                   `json:"k8sDeployment"`
	K8sContainer           *string                   `json:"k8sContainer"`

	K8sPriorityClassInteractive *string `json:"k8sPriorityClassInteractive"`
	K8sPriorityClassAgent       *string `json:"k8sPriorityClassAgent"`
	K8sPriorityClassCI          *string `json:"k8sPriorityClassCi"`

	PackageCacheEnabled *bool   `json:"packageCacheEnabled"`
	PackageCacheSizeGB  *int    `json:"packageCacheSizeGb"`
	ActionsCacheEnabled *bool   `json:"actionsCacheEnabled"`
	ActionsCacheSizeGB  *int    `json:"actionsCacheSizeGb"`
	CIRunHistory        *int    `json:"ciRunHistory"`
	CIStepOutputMB      *int    `json:"ciStepOutputMb"`
	CIStepOutputDays    *int    `json:"ciStepOutputDays"`
	CIRunOutputKB       *int    `json:"ciRunOutputKb"`
	CICacheMB           *int    `json:"ciCacheMb"`
	CICacheEntries      *int    `json:"ciCacheEntries"`
	CICacheDays         *int    `json:"ciCacheDays"`
	CIArtifactMB        *int    `json:"ciArtifactMb"`
	CIArtifactRunMB     *int    `json:"ciArtifactRunMb"`
	CIArtifactFileMB    *int    `json:"ciArtifactFileMb"`
	CIArtifactDays      *int    `json:"ciArtifactDays"`
	CIJobsPerRun        *int    `json:"ciJobsPerRun"`
	CIAgeBoost          *string `json:"ciAgeBoost"`
	CIReserveAfter      *string `json:"ciReserveAfter"`
	NetworkAccess       *string `json:"networkAccess"`
	ShowClosedByDefault *bool   `json:"showClosedByDefault"`
	EnvironmentName     *string `json:"environmentName"`
	// TimeZone is model.Config.TimeZone: the IANA zone every timestamp the
	// frontend prints is formatted in (ui/src/time.js). Empty means
	// model.DefaultTimeZone, which is how a deployment goes back to UTC.
	TimeZone                   *string `json:"timeZone"`
	ApprovedByDefault          *bool   `json:"approvedByDefault"`
	AutoMergeByDefault         *bool   `json:"autoMergeByDefault"`
	UpgradeCheckMode           *string `json:"upgradeCheckMode"`
	UpgradeCheckPausesPipeline *bool   `json:"upgradeCheckPausesPipeline"`
	// AutoUpgradeChannel is the branch to upgrade onto daily, "" to turn
	// automatic upgrades off; AutoUpgradeTime is when, as "HH:MM".
	AutoUpgradeChannel  *string               `json:"autoUpgradeChannel"`
	AutoUpgradeTime     *string               `json:"autoUpgradeTime"`
	AgentBackends       *[]model.AgentBackend `json:"agentBackends"`
	DefaultModel        *string               `json:"defaultModel"`
	PromptExtension     *string               `json:"promptExtension"`
	RepairTemplateID    *string               `json:"repairTemplateID"`
	TaskBranchTemplate  *string               `json:"taskBranchTemplate"`
	DeploymentID        *string               `json:"deploymentId"`
	DefaultCapabilities *[]string             `json:"defaultCapabilities"`
	SettingsLocked      *bool                 `json:"settingsLocked"`
}

func (r *UpdateSettingsRequest) UnmarshalJSON(data []byte) error {
	type plain UpdateSettingsRequest
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = UpdateSettingsRequest(p)
	var snake struct {
		SettingsLocked      *bool     `json:"settings_locked"`
		DefaultCapabilities *[]string `json:"default_capabilities"`
	}
	if err := json.Unmarshal(data, &snake); err == nil {
		if r.SettingsLocked == nil && snake.SettingsLocked != nil {
			r.SettingsLocked = snake.SettingsLocked
		}
		if r.DefaultCapabilities == nil && snake.DefaultCapabilities != nil {
			r.DefaultCapabilities = snake.DefaultCapabilities
		}
	}
	return nil
}

func (c *Client) UpdateSettings(ctx context.Context, req UpdateSettingsRequest) (Settings, error) {
	current, err := c.Store.GetConfig(ctx)
	if err != nil {
		return Settings{}, err
	}
	cfg, err := c.plannedConfig(ctx, req)
	if err != nil {
		if guard := c.checkGrainConfigWritable(ctx, current, nil); guard != nil {
			return Settings{}, guard
		}
		return Settings{}, err
	}
	// A request changing only the Models and Sandboxes settings writes
	// them here; any other field is the config repository's to change.
	if err := c.checkGrainConfigWritable(ctx, current, &cfg); err != nil {
		return Settings{}, err
	}
	if err := c.Store.PutConfig(ctx, cfg); err != nil {
		return Settings{}, err
	}
	c.triggerIAPSync()
	if req.AgentBackends != nil {
		var before []model.AgentBackend
		if current != nil {
			before = current.AgentBackends
		}
		c.auditCLIPathChanges(ctx, before, cfg.AgentBackends, "")
		c.liftEditedConnectionPauses(before, cfg.AgentBackends)
		c.kickModelDiscovery()
	}
	repoConfigs, err := c.Store.ListRepoConfigs(ctx)
	if err != nil {
		return Settings{}, err
	}
	settings := c.settingsFrom(ctx, cfg, repoConfigs)
	settings.MergeBatching = c.mergeBatching(ctx, cfg)
	settings.MergeGathering = c.mergeGathering(ctx)
	settings.DeploymentIDDefault = c.deploymentIDDefault(ctx)
	settings.TaskBranchTemplateDefault = model.DefaultTaskBranchTemplate
	settings.ExternalConfigRepo, settings.ExternalConfigBranch = c.externalConfigRepo(ctx)
	return settings, nil
}

func (c *Client) plannedConfig(ctx context.Context, req UpdateSettingsRequest) (model.Config, error) {
	current, err := c.Store.GetConfig(ctx)
	if err != nil {
		return model.Config{}, err
	}
	cfg := capability.NewDeploymentConfig()
	firstTime := current == nil || current.ConnectionsOnly()
	if current != nil {
		cfg = *current
		if firstTime {
			// Only `grain connections import` (or another
			// Store.UpdateConfig) has written this row: seed it as a
			// missing one, keeping what it recorded.
			cfg = capability.NewDeploymentConfig().WithConnectionsOf(*current)
		}
	}

	if req.PollInterval != nil {
		d, err := time.ParseDuration(*req.PollInterval)
		if err != nil {
			return model.Config{}, validationErrorf("pollInterval: %v", err)
		}
		if d <= 0 {
			return model.Config{}, validationErrorf("pollInterval must be positive")
		}
		cfg.PollInterval = d
	}
	if req.MaxWorkers != nil {
		if *req.MaxWorkers < 1 {
			return model.Config{}, validationErrorf("maxWorkers must be at least 1")
		}
		cfg.MaxWorkers = *req.MaxWorkers
	}
	if req.MaxMergesPerRepo != nil {
		if *req.MaxMergesPerRepo < 1 {
			return model.Config{}, validationErrorf("maxMergesPerRepo must be at least 1")
		}
		cfg.MaxMergesPerRepo = *req.MaxMergesPerRepo
	}
	if req.MergeBatchSize != nil {
		// 1 is off rather than a floor to reject: a deployment whose
		// changes break each other constantly should be able to say so.
		// The ceiling is model.MaxMergeBatchSize, because a batch holds
		// every member out of the queue until it reports.
		//
		// 0 is not a size but "give it back to grain's default", which
		// is what a stored zero already means (model.Config's
		// MergeBatchSizeOr) and what the Admin pane sends for a cleared
		// box. Refusing it left no way back to the default from the UI:
		// clearing the box was a 400 and the old value stayed.
		if *req.MergeBatchSize < 0 {
			return model.Config{}, validationErrorf("mergeBatchSize must be at least 1 (1 turns batching off), or 0 for grain's default")
		}
		if *req.MergeBatchSize > model.MaxMergeBatchSize {
			return model.Config{}, validationErrorf("mergeBatchSize cannot be more than %d",
				model.MaxMergeBatchSize)
		}
		cfg.MergeBatchSize = *req.MergeBatchSize
	}
	if req.MergeGatherWindow != nil {
		d, err := parseDurationSetting("mergeGatherWindow", *req.MergeGatherWindow)
		if err != nil {
			return model.Config{}, err
		}
		// Negative is allowed, and is how an operator declines the trade
		// entirely: the wait buys at most one suite and costs latency on
		// a change that is ready now, which a deployment with CI capacity
		// to spare should be able to refuse. The ceiling is
		// model.MaxMergeGatherWindow, because past it the latency is
		// worth more than any suite the batch could save.
		if d > model.MaxMergeGatherWindow {
			return model.Config{}, validationErrorf("mergeGatherWindow cannot be more than %s",
				model.MaxMergeGatherWindow)
		}
		// And refused rather than rounded if it cannot survive being
		// stored. The column is milliseconds (grain_config's
		// merge_gather_window_ms, like the poll interval and the CI
		// gate's windows), so anything under one lands as a zero -- which
		// means "this deployment has never chosen" and reads back as
		// grain's own five minutes. A "-100µs" meant as "turn the wait
		// off" would therefore turn it on, which is the one direction an
		// operator must not be surprised in.
		if d != 0 && d > -time.Millisecond && d < time.Millisecond {
			return model.Config{}, validationErrorf("mergeGatherWindow is stored in whole "+
				"milliseconds, so %s would be kept as 0 -- which means grain's own default rather "+
				"than what you asked for. Use a negative duration (-1s) to turn the wait off, or "+
				"blank for the default", d)
		}
		cfg.MergeGatherWindow = d
	}
	// Both bounds take 1 as their floor rather than 0. Zero is the value
	// the store reads as "nobody chose" (model.AutoSubmitLimits.
	// Resolved), so accepting it here would store a choice that reads
	// back as the default and leave whoever made it looking at a number
	// they did not pick -- and a deployment that wants nothing queued
	// unreviewed takes auto-merge away, which is the permission these
	// are a bound on.
	if req.MaxAutoSubmitDepth != nil {
		if *req.MaxAutoSubmitDepth < 1 {
			return model.Config{}, validationErrorf("maxAutoSubmitDepth must be at least 1")
		}
		cfg.MaxAutoSubmitDepth = *req.MaxAutoSubmitDepth
	}
	if req.MaxAutoSubmitFanOut != nil {
		if *req.MaxAutoSubmitFanOut < 1 {
			return model.Config{}, validationErrorf("maxAutoSubmitFanOut must be at least 1")
		}
		cfg.MaxAutoSubmitFanOut = *req.MaxAutoSubmitFanOut
	}
	if req.MaxAgentTurns != nil {
		if *req.MaxAgentTurns < 0 {
			return model.Config{}, validationErrorf("maxAgentTurns cannot be negative")
		}
		cfg.MaxAgentTurns = *req.MaxAgentTurns
	}
	if req.GitHubHost != nil {
		host, err := gitHubHostSetting(*req.GitHubHost)
		if err != nil {
			return model.Config{}, err
		}
		cfg.GitHubHost = host
	}
	if req.GitHubInsecureHTTP != nil {
		cfg.GitHubInsecureHTTP = *req.GitHubInsecureHTTP
	}
	if req.GCPProject != nil {
		cfg.GCPProject = *req.GCPProject
	}
	if req.GCPServiceAccountEmail != nil {
		cfg.GCPServiceAccountEmail = *req.GCPServiceAccountEmail
	}
	if req.GCPServiceAccountCount != nil {
		count := *req.GCPServiceAccountCount
		if count < 1 {
			count = 1
		}
		cfg.GCPServiceAccountCount = count
	}
	if req.IAPBackendService != nil {
		cfg.IAPBackendService = strings.TrimSpace(*req.IAPBackendService)
	}
	if req.IAPResource != nil {
		v := strings.TrimSpace(*req.IAPResource)
		if v != "" && v != iapreconciler.ResourceNone {
			if err := iapreconciler.ValidateWebResource(v); err != nil {
				return model.Config{}, validationErrorf("iapResource: %v", err)
			}
		}
		cfg.IAPResource = v
	}
	if req.DefaultCapabilities != nil {
		seen := map[string]bool{}
		ids := make([]string, 0, len(*req.DefaultCapabilities))
		for _, rawID := range *req.DefaultCapabilities {
			normalizedIDs, err := c.normalizeCapabilityIDs(rawID)
			if err != nil {
				return model.Config{}, err
			}
			for _, id := range normalizedIDs {
				if _, ok := c.capabilityByID(id); !ok {
					// Refused rather than dropped, and named with
					// capabilityRefusal's own words: this set seeds every
					// task filed on the deployment (CreateTask), so a
					// CI-only id landing here would fail every one of them
					// at model.ResolveGrants at once.
					return model.Config{}, validationErrorf("defaultCapabilities: %s", capabilityRefusal(id))
				}
				if seen[id] {
					continue
				}
				seen[id] = true
				ids = append(ids, id)
			}
		}
		cfg.DefaultCapabilities = ids
	}
	if req.NewestFirst != nil {
		cfg.NewestFirst = *req.NewestFirst
	}
	if req.SandboxBackend != nil {
		backend := model.SandboxBackend(strings.TrimSpace(*req.SandboxBackend))
		if !model.ValidSandboxBackend(backend) {
			return model.Config{}, validationErrorf("sandboxBackend must be one of %s", model.SandboxBackendNames())
		}
		if !slices.Contains(c.availableSandboxBackends(), backend) {
			for _, opt := range c.sandboxBackendOptions() {
				if opt.Backend == backend && opt.Reason != "" {
					return model.Config{}, validationErrorf("sandboxBackend %q is not available on this deployment: %s", backend, opt.Reason)
				}
			}
			return model.Config{}, validationErrorf("sandboxBackend %q is not available on this deployment", backend)
		}
		cfg.SandboxBackend = backend
	}
	if req.SandboxCPUs != nil {
		if *req.SandboxCPUs != 0 && *req.SandboxCPUs < 1 {
			return model.Config{}, validationErrorf("sandboxCpus must be 0 (unset) or at least 1")
		}
		cfg.SandboxCPUs = *req.SandboxCPUs
	}
	if req.SandboxMemoryMB != nil {
		if *req.SandboxMemoryMB != 0 && *req.SandboxMemoryMB < 128 {
			return model.Config{}, validationErrorf("sandboxMemoryMb must be 0 (unset) or at least 128")
		}
		cfg.SandboxMemoryMB = *req.SandboxMemoryMB
	}
	if req.SandboxDiskGB != nil {
		if *req.SandboxDiskGB < 0 {
			return model.Config{}, validationErrorf("sandboxDiskGb must be 0 (unset) or at least 1")
		}
		cfg.SandboxDiskGB = *req.SandboxDiskGB
	}
	// The sandbox budget's own three (model.Budget, grain/task-197).
	// Zero is accepted on either ratio and means what it means to the
	// budget itself -- "leave this dimension to the run counts alone" --
	// while a negative one is refused, since a negative ratio is not a
	// ratio and the budget would read it as that same unbounded zero
	// without anybody being told.
	if req.SandboxCPUOvercommit != nil {
		if *req.SandboxCPUOvercommit < 0 {
			return model.Config{}, validationErrorf("sandboxCpuOvercommit must be 0 (unbounded) or more")
		}
		cfg.SandboxCPUOvercommit = *req.SandboxCPUOvercommit
	}
	if req.SandboxMemoryOvercommit != nil {
		if *req.SandboxMemoryOvercommit < 0 {
			return model.Config{}, validationErrorf("sandboxMemoryOvercommit must be 0 (unbounded) or more")
		}
		cfg.SandboxMemoryOvercommit = *req.SandboxMemoryOvercommit
	}
	if req.SandboxDiskOvercommit != nil {
		if *req.SandboxDiskOvercommit < 0 {
			return model.Config{}, validationErrorf("sandboxDiskOvercommit must be 0 (unbounded) or more")
		}
		cfg.SandboxDiskOvercommit = *req.SandboxDiskOvercommit
	}
	if req.SandboxOvercommit != nil {
		if err := req.SandboxOvercommit.Validate(); err != nil {
			return model.Config{}, validationErrorf("sandboxOvercommit: %v", err)
		}
		cfg.SandboxOvercommit = *req.SandboxOvercommit
	}
	if req.ExternalAgentAPI != nil {
		cfg.ExternalAgentAPI = *req.ExternalAgentAPI
	}
	if req.SessionSandboxIdleTTL != nil {
		d, err := parseDurationSetting("sessionSandboxIdleTtl", *req.SessionSandboxIdleTTL)
		if err != nil {
			return model.Config{}, err
		}
		if d != 0 && d > -time.Millisecond && d < time.Millisecond {
			return model.Config{}, validationErrorf("sessionSandboxIdleTtl is stored in whole "+
				"milliseconds, so %s would be kept as 0 -- which means grain's own default rather "+
				"than what you asked for. Use a negative duration (-1s) to turn warm retention off, "+
				"or blank for the default", d)
		}
		cfg.SessionSandboxIdleTTL = d
	}
	// A negative reserve is grain's own (an eighth of the machine), so
	// what there is to refuse here is the other end: a reserve at or
	// above what the host has leaves a memory budget of nothing, which
	// does not stop the deployment but runs it one sandbox at a time
	// with everything else queued behind the budget (model.Budget.Admits'
	// Sole rule). Refused only where this process can actually speak for
	// a host -- a UI not colocated with a backend has no size to hold
	// the number against, and inventing one would refuse a setting that
	// is fine on the machine it is meant for.
	if req.SandboxMemoryReserveMB != nil {
		if host, ok := c.sandboxHostCapacity(); ok && host.MemoryMB > 0 && *req.SandboxMemoryReserveMB >= host.MemoryMB {
			return model.Config{}, validationErrorf(
				"sandboxMemoryReserveMb must be less than this host's %d MiB -- reserving all of it leaves no sandbox memory at all",
				host.MemoryMB)
		}
		cfg.SandboxMemoryReserveMB = *req.SandboxMemoryReserveMB
	}
	// The budget's third dimension (model.DiskFloor, grain/task-372).
	// Zero in either is accepted and means what it means to the floor
	// itself -- zero in both leaves a filling disk holding nothing back,
	// which is what every deployment did before the floor existed --
	// while a negative one is refused for the ratios' reason above: it
	// is not a share or a size, and DiskFloor would read it as the same
	// unbounded zero without anybody being told.
	//
	// The top end is not refused, because it cannot wedge anything: a
	// share above model.MaxDiskFloorShare is capped at a quarter of the
	// volume by the arithmetic itself (DiskFloor.FloorMB), and the pane
	// shows what it resolves to per volume (Settings.SandboxDiskFloors).
	// What is refused is a share over 1, which is not a share of
	// anything and is nearly always a percentage typed as one.
	if req.SandboxDiskFloorShare != nil {
		if *req.SandboxDiskFloorShare < 0 || *req.SandboxDiskFloorShare > 1 {
			return model.Config{}, validationErrorf(
				"sandboxDiskFloorShare is a share of each volume, so it must be between 0 (hold nothing back) and 1 -- "+
					"never more than %.0f%% of a volume is kept free however it is stored", model.MaxDiskFloorShare*100)
		}
		cfg.SandboxDiskFloorShare = *req.SandboxDiskFloorShare
	}
	if req.SandboxDiskFloorMinMB != nil {
		if *req.SandboxDiskFloorMinMB < 0 {
			return model.Config{}, validationErrorf("sandboxDiskFloorMinMb must be 0 (no minimum) or more")
		}
		cfg.SandboxDiskFloorMinMB = *req.SandboxDiskFloorMinMB
	}
	if req.SandboxImage != nil {
		image := strings.TrimSpace(*req.SandboxImage)
		if image != "" {
			if err := sandboximage.ValidateReference(image); err != nil {
				return model.Config{}, validationErrorf("sandboxImage must be one OCI image reference, such as registry.example/team/sandbox:v1: %v", err)
			}
		}
		cfg.SandboxImage = image
	}
	if req.SandboxImageMirror != nil {
		mirror := strings.TrimSpace(*req.SandboxImageMirror)
		if mirror != "" {
			if err := sandboximage.ValidateReference(mirror); err != nil {
				return model.Config{}, validationErrorf("sandboxImageMirror must be an OCI repository reference, such as us-docker.pkg.dev/my-project/grain/guest: %v", err)
			}
		}
		cfg.SandboxImageMirror = mirror
	}
	if req.K8sNamespace != nil {
		ns := strings.TrimSpace(*req.K8sNamespace)
		if ns != "" && !k8sLabelName.MatchString(ns) {
			return model.Config{}, validationErrorf("k8sNamespace %q is not a Kubernetes namespace name: at most 63 lowercase letters, digits and '-', starting and ending with a letter or digit", ns)
		}
		cfg.K8sNamespace = ns
	}
	if req.K8sKubectl != nil {
		cfg.K8sKubectl = strings.TrimSpace(*req.K8sKubectl)
	}
	if req.K8sSandboxImage != nil {
		cfg.K8sSandboxImage = strings.TrimSpace(*req.K8sSandboxImage)
	}
	if req.K8sGranuleImage != nil {
		cfg.K8sGranuleImage = strings.TrimSpace(*req.K8sGranuleImage)
	}
	if req.K8sWorkspace != nil {
		cfg.K8sWorkspace = strings.TrimSpace(*req.K8sWorkspace)
	}
	if req.K8sGitProxyHost != nil {
		host := strings.TrimSpace(*req.K8sGitProxyHost)
		if host != "" && !k8sAddress.MatchString(host) {
			return model.Config{}, validationErrorf("k8sGitProxyHost %q is not a host name or IP address", host)
		}
		cfg.K8sGitProxyHost = host
	}
	if req.K8sSandboxService != nil {
		name, err := k8sObjectName("k8sSandboxService", *req.K8sSandboxService, k8sLabelName, "Service")
		if err != nil {
			return model.Config{}, err
		}
		cfg.K8sSandboxService = name
	}
	if req.K8sServiceAccount != nil {
		name, err := k8sObjectName("k8sServiceAccount", *req.K8sServiceAccount, k8sSubdomainName, "ServiceAccount")
		if err != nil {
			return model.Config{}, err
		}
		cfg.K8sServiceAccount = name
	}
	if req.K8sNodeSelector != nil {
		selectors := make([]string, 0, len(*req.K8sNodeSelector))
		for _, entry := range *req.K8sNodeSelector {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			k, v, ok := strings.Cut(entry, "=")
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if !ok || k == "" {
				return model.Config{}, validationErrorf("k8sNodeSelector %q must be key=value", entry)
			}
			selectors = append(selectors, k+"="+v)
		}
		cfg.K8sNodeSelector = selectors
	}
	if req.K8sPrivileged != nil {
		cfg.K8sPrivileged = *req.K8sPrivileged
	}
	if req.K8sNetworkPolicy != nil {
		cfg.K8sNetworkPolicy = *req.K8sNetworkPolicy
	}
	if req.K8sDeployment != nil {
		name, err := k8sObjectName("k8sDeployment", *req.K8sDeployment, k8sSubdomainName, "Deployment")
		if err != nil {
			return model.Config{}, err
		}
		cfg.K8sDeployment = name
	}
	if req.K8sContainer != nil {
		name, err := k8sObjectName("k8sContainer", *req.K8sContainer, k8sLabelName, "container")
		if err != nil {
			return model.Config{}, err
		}
		cfg.K8sContainer = name
	}
	if req.K8sPriorityClassInteractive != nil {
		name, err := k8sPriorityClassName("k8sPriorityClassInteractive", *req.K8sPriorityClassInteractive)
		if err != nil {
			return model.Config{}, err
		}
		cfg.K8sPriorityClassInteractive = name
	}
	if req.K8sPriorityClassAgent != nil {
		name, err := k8sPriorityClassName("k8sPriorityClassAgent", *req.K8sPriorityClassAgent)
		if err != nil {
			return model.Config{}, err
		}
		cfg.K8sPriorityClassAgent = name
	}
	if req.K8sPriorityClassCI != nil {
		name, err := k8sPriorityClassName("k8sPriorityClassCi", *req.K8sPriorityClassCI)
		if err != nil {
			return model.Config{}, err
		}
		cfg.K8sPriorityClassCI = name
	}
	if req.PackageCacheEnabled != nil {
		cfg.PackageCacheEnabled = *req.PackageCacheEnabled
	}
	if req.PackageCacheSizeGB != nil {
		if *req.PackageCacheSizeGB < 0 {
			return model.Config{}, validationErrorf("packageCacheSizeGb must be 0 (unset) or at least 1")
		}
		cfg.PackageCacheSizeGB = *req.PackageCacheSizeGB
	}
	if req.ActionsCacheEnabled != nil {
		cfg.ActionsCacheEnabled = *req.ActionsCacheEnabled
	}
	if req.ActionsCacheSizeGB != nil {
		if *req.ActionsCacheSizeGB < 0 {
			return model.Config{}, validationErrorf("actionsCacheSizeGb must be 0 (unset) or at least 1")
		}
		cfg.ActionsCacheSizeGB = *req.ActionsCacheSizeGB
	}
	// The CI history's four take any number at all, and that is
	// deliberate rather than a missing check (grain/task-269): a positive
	// one is the bound, 0 is "grain's own default", and a negative one is
	// an operator saying "no bound, keep it forever" about their own
	// disk. What each of them costs is arithmetic a person can do -- the
	// pane says how many days the numbers come to -- and none of them can
	// make this deployment answer wrongly about a run, only remember it
	// for a shorter or longer time.
	if req.CIRunHistory != nil {
		cfg.CIRunHistory = *req.CIRunHistory
	}
	if req.CIStepOutputMB != nil {
		cfg.CIStepOutputMB = *req.CIStepOutputMB
	}
	if req.CIStepOutputDays != nil {
		cfg.CIStepOutputDays = *req.CIStepOutputDays
	}
	if req.CIRunOutputKB != nil {
		cfg.CIRunOutputKB = *req.CIRunOutputKB
	}
	// And the two disk groups' seven, which take 0 ("grain's own
	// default") or a positive bound and nothing else (grain/task-317).
	// A negative one is refused rather than read as the history's "keep
	// it all": what these bound is a directory on the volume this
	// deployment dispatches out of, and a data disk with no ceiling on it
	// stops the daemon admitting work at all -- which is the failure
	// ci.DirCache and ci.DirArtifacts were bounded to prevent, not a
	// trade an operator can usefully make. Lowering one is free: it binds
	// the next sweep, five minutes out at most, and the CI pane's Clear
	// is there for an operator who wants the disk back now.
	for _, bound := range []struct {
		field string
		value *int
		into  *int
	}{
		{"ciCacheMb", req.CICacheMB, &cfg.CICacheMB},
		{"ciCacheEntries", req.CICacheEntries, &cfg.CICacheEntries},
		{"ciCacheDays", req.CICacheDays, &cfg.CICacheDays},
		{"ciArtifactMb", req.CIArtifactMB, &cfg.CIArtifactMB},
		{"ciArtifactRunMb", req.CIArtifactRunMB, &cfg.CIArtifactRunMB},
		{"ciArtifactFileMb", req.CIArtifactFileMB, &cfg.CIArtifactFileMB},
		{"ciArtifactDays", req.CIArtifactDays, &cfg.CIArtifactDays},
	} {
		if bound.value == nil {
			continue
		}
		if *bound.value < 0 {
			return model.Config{}, validationErrorf(
				"%s must be 0 (grain's own default) or a bound above it -- CI's own disk has no unbounded setting",
				bound.field)
		}
		*bound.into = *bound.value
	}
	if req.CIJobsPerRun != nil {
		if *req.CIJobsPerRun < 0 {
			return model.Config{}, validationErrorf("ciJobsPerRun must be 0 (no per-run limit) or a positive count")
		}
		cfg.CIJobsPerRun = *req.CIJobsPerRun
	}
	// The gate's two windows, in the Go duration syntax the poll interval
	// takes (grain/task-325). Empty is how either is given back to
	// grain's own number, so a pane's blank box means what a blank box
	// means everywhere else here rather than "zero seconds".
	if req.CIAgeBoost != nil {
		d, err := parseDurationSetting("ciAgeBoost", *req.CIAgeBoost)
		if err != nil {
			return model.Config{}, err
		}
		// Negative is refused here even though ci.SharedGate understands
		// it: a boost that is off is a pure priority order, which starves
		// the bottom of the queue until the job times out, and that is
		// the failure this whole ordering was filed for
		// (grain/task-316). Nothing an operator wants is on the other
		// side of that, and a very long boost says the same thing
		// without the wedge.
		if d < 0 {
			return model.Config{}, validationErrorf("ciAgeBoost cannot be negative: it is the floor that " +
				"stops a low-ranked job waiting for ever, and turning it off starves the bottom of the queue")
		}
		cfg.CIAgeBoost = d
	}
	if req.CIReserveAfter != nil {
		d, err := parseDurationSetting("ciReserveAfter", *req.CIReserveAfter)
		if err != nil {
			return model.Config{}, err
		}
		// Negative is allowed here, unlike the boost above, because it is
		// a trade rather than a wedge: reservation off is the ordering
		// grain had before grain/task-316, where a job bigger than the
		// holes capacity frees in waits for one to happen to appear. A
		// deployment whose jobs are all the same size loses nothing by
		// it and gets the capacity back.
		cfg.CIReserveAfter = d
	}
	if req.NetworkAccess != nil {
		access := model.NetworkAccess(strings.TrimSpace(*req.NetworkAccess))
		if access != "" && !model.ValidNetworkAccess(access) {
			return model.Config{}, validationErrorf("networkAccess must be %s, or empty for %q",
				model.NetworkAccessNames(), model.NetworkOpen)
		}
		cfg.NetworkAccess = access
	}
	if req.ShowClosedByDefault != nil {
		cfg.ShowClosedByDefault = *req.ShowClosedByDefault
	}
	if req.SettingsLocked != nil {
		cfg.SettingsLocked = *req.SettingsLocked
	}
	if req.EnvironmentName != nil {
		name := strings.TrimSpace(*req.EnvironmentName)
		if utf8.RuneCountInString(name) > maxEnvironmentNameLen {
			return model.Config{}, validationErrorf("environmentName cannot be longer than %d characters", maxEnvironmentNameLen)
		}
		if strings.ContainsFunc(name, func(r rune) bool { return r == '\n' || r == '\r' || r == '\t' }) {
			return model.Config{}, validationErrorf("environmentName cannot contain line breaks or tabs")
		}
		cfg.EnvironmentName = name
	}
	if req.TimeZone != nil {
		zone := strings.TrimSpace(*req.TimeZone)
		// Validated here rather than trusted, because model.LoadLocation
		// cannot fail -- it falls back to UTC for a name it does not know,
		// which would store a typo and silently print every time in the
		// wrong zone. time.LoadLocation does fail, and the IANA database
		// is compiled into this binary (model's tzdata import), so this
		// answers the same on every host grain runs on.
		if zone != "" && zone != model.DefaultTimeZone {
			if _, err := time.LoadLocation(zone); err != nil {
				return model.Config{}, validationErrorf(
					"timeZone %q is not an IANA zone name (e.g. America/Los_Angeles, Europe/London); "+
						"empty means %s", zone, model.DefaultTimeZone)
			}
		}
		cfg.TimeZone = zone
	}
	if req.PromptExtension != nil {
		cfg.PromptExtension = strings.TrimSpace(*req.PromptExtension)
	}
	if req.RepairTemplateID != nil {
		cfg.RepairTemplateID = strings.TrimSpace(*req.RepairTemplateID)
	}
	if req.TaskBranchTemplate != nil {
		template := strings.TrimSpace(*req.TaskBranchTemplate)
		// Tried both with and without a title, so a {slug} has to make a
		// valid name whether it expands to words or to nothing. Either
		// call also refuses a {slug} anywhere but at the end, after {id}
		// and a hyphen, where a title could make another task's
		// branch name (model.checkSlugPlacement): a valid name is not
		// enough when it may be another task's.
		for _, title := range []string{"", "Fix the header parser"} {
			if _, err := model.FormatTaskBranchName(template, strings.Repeat("a", 32), "a", "123", title); err != nil {
				return model.Config{}, validationErrorf("taskBranchTemplate %v", err)
			}
		}
		cfg.TaskBranchTemplate = template
	}
	if req.DeploymentID != nil {
		id := strings.TrimSpace(*req.DeploymentID)
		if err := model.ValidateDeploymentID(id); err != nil {
			return model.Config{}, validationErrorf("deploymentId %v", err)
		}
		cfg.DeploymentID = id
	}
	if req.ApprovedByDefault != nil {
		cfg.ApprovedByDefault = *req.ApprovedByDefault
	}
	if req.AutoMergeByDefault != nil {
		cfg.AutoMergeByDefault = *req.AutoMergeByDefault
	}
	if req.UpgradeCheckMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*req.UpgradeCheckMode))
		if !model.ValidUpgradeCheckMode(mode) {
			return model.Config{}, validationErrorf("upgradeCheckMode must be one of %s",
				strings.Join(model.UpgradeCheckModes(), ", "))
		}
		cfg.UpgradeCheckMode = mode
	}
	if req.UpgradeCheckPausesPipeline != nil {
		cfg.UpgradeCheckPausesPipeline = *req.UpgradeCheckPausesPipeline
	}
	if req.AutoUpgradeChannel != nil {
		channel, err := c.validAutoUpgradeChannel(*req.AutoUpgradeChannel)
		if err != nil {
			return model.Config{}, err
		}
		cfg.AutoUpgradeChannel = channel
	}
	if req.AutoUpgradeTime != nil {
		at := strings.TrimSpace(*req.AutoUpgradeTime)
		if at != "" {
			if _, _, err := model.ParseAutoUpgradeTime(at); err != nil {
				return model.Config{}, validationErrorf("autoUpgradeTime must be HH:MM, from 00:00 to 23:59: %v", err)
			}
		}
		cfg.AutoUpgradeTime = at
	}
	// A channel is a daily upgrade at some time; one saved without a
	// time gets the default rather than a schedule that never fires.
	if cfg.AutoUpgradeChannel != "" && strings.TrimSpace(cfg.AutoUpgradeTime) == "" {
		cfg.AutoUpgradeTime = model.DefaultAutoUpgradeTime
	}
	if req.DefaultModel != nil {
		cfg.DefaultModel = model.NormalizeDefaultModel(*req.DefaultModel)
	}
	if req.AgentBackends != nil {
		seen := map[string]bool{}
		for i, backend := range *req.AgentBackends {
			backend.ID = strings.TrimSpace(backend.ID)
			backend.Name = strings.TrimSpace(backend.Name)
			backend.Framework = model.NormalizeAgentFrameworkName(strings.TrimSpace(backend.Framework))
			var models []string
			for _, m := range backend.Models {
				m = strings.TrimSpace(m)
				if m != "" && !slices.Contains(models, m) {
					models = append(models, m)
				}
			}
			backend.Models = models
			var excluded []string
			for _, m := range backend.ExcludedModels {
				m = strings.TrimSpace(m)
				if m != "" && !slices.Contains(excluded, m) {
					excluded = append(excluded, m)
				}
			}
			backend.ExcludedModels = excluded
			backend.Command = strings.TrimSpace(backend.Command)
			backend.CredentialBackendID = strings.TrimSpace(backend.CredentialBackendID)
			backend.Team = strings.TrimSpace(backend.Team)
			if backend.AuthMode != "" {
				valid := backend.Framework == model.AgentFrameworkCodex &&
					(backend.AuthMode == model.CodexAuthAPIKey || backend.AuthMode == model.CodexAuthSubscription)
				valid = valid || backend.Framework == model.AgentFrameworkAntigravity &&
					(backend.AuthMode == model.CodexAuthAPIKey || backend.AuthMode == model.AntigravityAuthAmbient)
				valid = valid || backend.Framework == model.AgentFrameworkClaude &&
					(backend.AuthMode == model.CodexAuthAPIKey || backend.AuthMode == model.ClaudeAuthVertex || backend.AuthMode == model.AntigravityAuthAmbient)
				if !valid {
					return model.Config{}, validationErrorf("agentBackends[%d].authMode is not valid for %s", i, backend.Framework)
				}
			}
			if backend.ID == "" || strings.ContainsAny(backend.ID, " /\\") {
				return model.Config{}, validationErrorf("agentBackends[%d].id must be a non-empty identifier", i)
			}
			if seen[backend.ID] {
				return model.Config{}, validationErrorf("agent backend id %q is duplicated", backend.ID)
			}
			if !model.ValidAgentFramework(backend.Framework) {
				return model.Config{}, validationErrorf("agentBackends[%d].framework must be %s", i, model.AgentFrameworkNames())
			}
			if backend.Framework == model.AgentFrameworkCustom && backend.Command == "" {
				return model.Config{}, validationErrorf("agentBackends[%d].command is required for a custom backend", i)
			}
			backend.CLIPath = strings.TrimSpace(backend.CLIPath)
			if backend.CLIPath != "" {
				if model.AgentCLIBinary(backend.Framework) == "" {
					return model.Config{}, validationErrorf("agentBackends[%d].cliPath is only for claude, codex and antigravity connections; "+
						"a custom backend names its executable in command", i)
				}
				// Checked only when it changes, so a binary removed after
				// it was saved fails that connection's runs (naming it)
				// rather than every unrelated settings save after.
				if backend.CLIPath != storedCLIPath(cfg.AgentBackends, backend.ID) {
					if err := CheckCLIPath(backend.CLIPath); err != nil {
						return model.Config{}, validationErrorf("agentBackends[%d].cliPath: %v", i, err)
					}
				}
			}
			backend.ModelTurnTimeout = strings.TrimSpace(backend.ModelTurnTimeout)
			backend.ModelRetryCutoff = strings.TrimSpace(backend.ModelRetryCutoff)
			if backend.ModelTurnTimeout != "" || backend.ModelRetryCutoff != "" {
				if model.NormalizeAgentFrameworkName(backend.Framework) != model.AgentFrameworkAntigravity {
					return model.Config{}, validationErrorf("agentBackends[%d]: modelTurnTimeout and modelRetryCutoff are only for antigravity connections", i)
				}
				turnTimeout, err := model.ParseModelTurnLimit(backend.ModelTurnTimeout)
				if err != nil {
					return model.Config{}, validationErrorf("agentBackends[%d].modelTurnTimeout: %v", i, err)
				}
				retryCutoff, err := model.ParseModelTurnLimit(backend.ModelRetryCutoff)
				if err != nil {
					return model.Config{}, validationErrorf("agentBackends[%d].modelRetryCutoff: %v", i, err)
				}
				if turnTimeout > 0 && retryCutoff > turnTimeout {
					return model.Config{}, validationErrorf("agentBackends[%d]: modelRetryCutoff (%s) is longer than modelTurnTimeout (%s); "+
						"a turn agy is retrying should not be allowed longer than a slow one", i, retryCutoff, turnTimeout)
				}
			}
			seen[backend.ID] = true
			(*req.AgentBackends)[i] = backend
		}
		byID := make(map[string]model.AgentBackend, len(*req.AgentBackends))
		for _, backend := range *req.AgentBackends {
			byID[backend.ID] = backend
		}
		for i, backend := range *req.AgentBackends {
			if backend.CredentialBackendID == "" {
				continue
			}
			source, ok := byID[backend.CredentialBackendID]
			if !ok || source.ID == backend.ID || source.Framework != backend.Framework || source.CredentialBackendID != "" {
				return model.Config{}, validationErrorf("agentBackends[%d].credentialBackendId must name a directly authenticated backend of the same type", i)
			}
		}
		stored := cfg.AgentBackends
		cfg.AgentBackends = append([]model.AgentBackend(nil), (*req.AgentBackends)...)
		carryDiscovered(stored, cfg.AgentBackends)
	}

	if firstTime {
		if cfg.PollInterval <= 0 {
			return model.Config{}, validationErrorf("pollInterval is required the first time settings are saved")
		}
		if strings.TrimSpace(cfg.GitHubHost) == "" {
			return model.Config{}, validationErrorf("githubHost is required the first time settings are saved")
		}
	}

	return cfg, nil
}

// validAutoUpgradeChannel checks the branch a daily upgrade would go
// onto the way scheduling an upgrade does (Upgrader.ValidateBranch), so
// a branch this deployment would refuse to upgrade onto is refused now
// rather than at the time of day with nobody watching. A deployment
// with no upgrader -- one that cannot upgrade itself, or a settings
// import running offline -- gets the branch-name check alone. "" is off.
func (c *Client) validAutoUpgradeChannel(channel string) (string, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		return "", nil
	}
	if c.Config.Upgrader != nil {
		branch, err := c.Config.Upgrader.ValidateBranch(channel)
		if err != nil {
			return "", validationErrorf("autoUpgradeChannel: %v", err)
		}
		return branch, nil
	}
	branch, err := upgrade.ValidBranchName(channel)
	if err != nil {
		return "", validationErrorf("autoUpgradeChannel: %v", err)
	}
	return branch, nil
}

// autoUpgradeTimeOrDefault is the time a Settings reports: the stored
// one, or model.DefaultAutoUpgradeTime for a deployment that has not
// chosen.
func autoUpgradeTimeOrDefault(at string) string {
	if at = strings.TrimSpace(at); at != "" {
		return at
	}
	return model.DefaultAutoUpgradeTime
}

func (c *Client) PlanSettings(ctx context.Context, req UpdateSettingsRequest) (Settings, error) {
	cfg, err := c.plannedConfig(ctx, req)
	if err != nil {
		return Settings{}, err
	}
	repoConfigs, err := c.Store.ListRepoConfigs(ctx)
	if err != nil {
		return Settings{}, err
	}
	settings := c.settingsFrom(ctx, cfg, repoConfigs)
	settings.MergeBatching = c.mergeBatching(ctx, cfg)
	settings.MergeGathering = c.mergeGathering(ctx)
	settings.DeploymentIDDefault = c.deploymentIDDefault(ctx)
	settings.TaskBranchTemplateDefault = model.DefaultTaskBranchTemplate
	settings.ExternalConfigRepo, settings.ExternalConfigBranch = c.externalConfigRepo(ctx)
	return settings, nil
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.tasks.GetSettings(r.Context())
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req UpdateSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	settings, err := s.tasks.UpdateSettings(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handlePlanSettings(w http.ResponseWriter, r *http.Request) {
	var req UpdateSettingsRequest
	if !readJSON(w, r, &req) {
		return
	}
	settings, err := s.tasks.PlanSettings(r.Context(), req)
	if err != nil {
		writeClientError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// gitHubHostSetting is the githubHost a settings save may store: a bare
// host with an optional :port, which is all the daemon ever does anything
// with -- the git proxy forwards to it as it stands and github.APIHost
// maps exactly "github.com" to the API's own host. A URL, a path or a
// stray space saved here is stored, reported as waiting for a restart,
// and then breaks every GitHub call the daemon makes after that restart,
// so it is refused at the save instead. Surrounding space is dropped.
func gitHubHostSetting(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", validationErrorf("githubHost cannot be empty")
	}
	if strings.Contains(host, "://") {
		if u, err := url.Parse(host); err == nil && u.Host != "" {
			return "", validationErrorf("githubHost is a host name, not a URL: give %q rather than %q (plain http is the separate githubInsecureHttp switch)", u.Host, host)
		}
		return "", validationErrorf("githubHost is a host name, not a URL")
	}
	if strings.ContainsAny(host, "/?#@") || strings.IndexFunc(host, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return "", validationErrorf("githubHost must be a bare host name with an optional :port, such as github.com or ghe.example.com:8443, not %q", host)
	}
	if strings.LastIndex(host, ":") > strings.LastIndex(host, "]") {
		name, port, err := net.SplitHostPort(host)
		n, perr := strconv.Atoi(port)
		if err != nil || name == "" || perr != nil || n < 1 || n > 65535 {
			return "", validationErrorf("githubHost %q has no usable :port -- give a host name with an optional port from 1 to 65535, such as ghe.example.com:8443", host)
		}
	}
	return host, nil
}

// k8sLabelName is a Kubernetes DNS-1123 label, what a namespace name
// must be; k8sSubdomainName a DNS-1123 subdomain, what a PriorityClass
// name must be. A name outside them is one the API server refuses on
// every pod grain creates, so it is refused here, where it is typed,
// instead (the connections and infrastructure settings walkthrough of
// 2026-10-29 saved "Bad_Class" without a word).
var (
	k8sLabelName     = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$`)
	k8sSubdomainName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
)

// k8sAddress is what the git proxy host a sandbox pod is handed may be:
// a host name or an IP address, with no scheme, path or space in it.
var k8sAddress = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.:-]*[A-Za-z0-9])?$`)

// k8sObjectName trims a setting naming a Kubernetes object of kind and
// refuses a name re says the API server never accepts (the 2026-10-30
// walkthrough saved the ServiceAccount "Bad SA"). Empty is valid: the
// setting's default or none applies.
func k8sObjectName(key, raw string, re *regexp.Regexp, kind string) (string, error) {
	name := strings.TrimSpace(raw)
	if name != "" && (len(name) > 253 || !re.MatchString(name)) {
		chars := "lowercase letters, digits and '-'"
		if re == k8sSubdomainName {
			chars = "lowercase letters, digits, '-' and '.'"
		}
		return "", validationErrorf("%s %q is not a Kubernetes %s name: %s, starting and ending with a letter or digit", key, name, kind, chars)
	}
	return name, nil
}

// k8sPriorityClassName trims a priority class setting and refuses a
// name Kubernetes could never accept. Empty is valid: it sets none.
func k8sPriorityClassName(key, raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name != "" && (len(name) > 253 || !k8sSubdomainName.MatchString(name)) {
		return "", validationErrorf("%s %q is not a Kubernetes PriorityClass name: lowercase letters, digits, '-' and '.', starting and ending with a letter or digit", key, name)
	}
	return name, nil
}
