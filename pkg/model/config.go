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

package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

// Config is a deployment's tunable configuration -- the knobs
// bwsalmon/agents#320 asked to move off the daemon's own flags and into
// the store, so a UI or a CLI can change them without a redeploy. None
// of it is secret: a new field holding a credential belongs in
// pkg/secrets instead, not here.
//
// What stays a flag, deliberately, is anything a deployment needs before
// it can even reach the database this names, or that names secret
// material rather than being one: -data-dir, -gemini-api-key-file and
// -kontur-ssh-key chief among them. Config is everything left over on
// cmd/grain's "daemon"
// subcommand's own flag set -- see daemon.go's own doc comment on how
// the two combine.
//
// GetConfig/PutConfig are its persistence, one row, keyed the same way
// grain_write and grain_schema are: there is exactly one of these per
// deployment. GetConfig returns a nil Config, with no error, for a fresh
// database with no row yet -- a caller seeding one for the first time
// (daemon.go's run, on a database it just opened) tells that apart from
// "the row says every field is a zero value" this way, where a bare
// zero-value Config could not.
type Config struct {
	// AgentBackends is the ordered pool dispatch rotates through. An empty
	// pool preserves the legacy AgentFramework/model/credential settings.
	AgentBackends []AgentBackend
	// DefaultModel is the deployment-wide default model for tasks that do not
	// specify one. When empty, it falls back to the first supported model of
	// configured connections, or the default model for AgentFramework.
	//
	// Schema 22 folded the default team's own default model
	// (team.default_model) into it: that was the one dispatch ran on, so
	// a dump that held both comes across with the team's
	// (docs/single-tenant-reset.md, 6-ii-b2).
	DefaultModel string
	// AgentBackendID is the connection a task that chose no model, no
	// connection and no framework is pinned to, empty for the rotation.
	// It was the default team's agent_backend_id until schema 22.
	AgentBackendID string
	// ModelSettings is the deployment's own per-model configuration --
	// the reasoning effort each model it runs is asked for
	// (model_selection.go). It was the default team's model_settings
	// until schema 22.
	ModelSettings []ModelSetting
	// GitConnections is every credential the deployment clones and
	// pushes with, each on its own host and optionally scoped to an
	// owner or a repository (team_git_connections.go,
	// docs/git-hosts.md). It was the default team's git_connections
	// until schema 22.
	GitConnections []TeamGitConnection
	// GitCredential is the deployment's single credential from before it
	// held several: still read, as the default connection for the
	// deployment's default host (EffectiveGitConnections). It was the
	// default team's git_credential until schema 22.
	GitCredential string
	// PollInterval is how often pkg/orchestrator's RunCycle runs. Read
	// back out of this row once per tick, so changing it retimes the
	// loop that is already running rather than the next one
	// (cmd/grain/daemon.go's liveConfig).
	PollInterval time.Duration
	// MaxWorkers is how many runs of ordinary work dispatch.Cycle lets be
	// in flight at once -- the same count -max-workers parses.
	//
	// bwsalmon/agents#461 replaced named slots (an operator-chosen list,
	// each entry its own sandbox directory or kontur VM name) with this
	// plain count, but kept generating identifiers from it, so a "slot"
	// still existed as the thing a sandbox was named after and reused
	// under. With a sandbox created and deleted per run, there is nothing
	// left for such an identifier to name: this is now read as a limit
	// and nothing else, by dispatch.Cycle counting live runs against it
	// and by nobody else at all.
	//
	// It was called MaxConcurrent, and was the whole limit, until
	// grain/task-63 split the merge queue's own repair runs out from
	// under it -- see MaxMergers below and Limits, which is the pair of
	// them as everything that enforces a limit reads them. Its column was
	// renamed with it, max_concurrent to max_workers, by
	// Store.ensureConfigWorkerMergerColumns.
	MaxWorkers int
	// MaxMergers is capacity on top of MaxWorkers that only the merge
	// queue's own fix tasks may reach (model.Limits' own doc comment has
	// why they are worth reserving, and exactly how the two numbers
	// combine: workers never exceed MaxWorkers, nothing exceeds
	// MaxWorkers+MaxMergers, and a merger may take a free worker slot
	// while the reverse never happens).
	//
	// 0 is a meaningful value rather than an unset one: mergers then
	// contend for MaxWorkers alongside everything else, exactly as every
	// deployment behaved before this field existed. DefaultConfig's 1 is
	// what a deployment that has never chosen gets instead -- one run of
	// headroom is enough to keep a queue head's repair from waiting out
	// whatever else is running, and cheap enough that a single-worker
	// deployment can afford it.
	MaxMergers int
	// MaxMergesPerRepo is how many ready pull requests may land per cycle
	// into one base branch of one repository -- one merge queue lane --
	// when the overlap re-check is not enforced (for example in a
	// GitHub-check-only repository or when RepoConfig.MergeRecheck is off
	// or observe). Under MergeRecheckEnforce, each entry is checked
	// against the live branch tip before merging, so non-overlapping
	// changes merge in the same cycle without spending this limit.
	MaxMergesPerRepo int
	// MergeBatchSize is retired (grain/task-1023). Stored values in
	// grain_config and settings dumps remain readable and are ignored.
	MergeBatchSize int
	// MergeGatherWindow is retired (grain/task-1023). Stored values in
	// grain_config and settings dumps remain readable and are ignored.
	MergeGatherWindow time.Duration
	// MaxCIJobs is how many grain CI job sandboxes may run at once,
	// independently of MaxWorkers and MaxMergers. Limits.SandboxTotal is
	// the machine commitment when both CI and agent capacity are full.
	MaxCIJobs int
	// MaxAgentTurns caps model/tool round trips per run; 0 leaves the
	// agent framework's own default in place, which for every framework
	// is no cap at all (agent/claude's defaultMaxTurns has why). A run's
	// real ceiling is orchestrator.Config.MaxRunRuntime. Re-read by
	// orchestrator.RunCycle every cycle, the same as MaxWorkers, so a
	// changed cap reaches the next run dispatched.
	MaxAgentTurns int
	// GitHubHost is the GitHub API host -- overridable to point at a mock
	// for local testing.
	//
	// This, and GitHubInsecureHTTP below, are the only two settings here
	// that a running daemon cannot adopt: both are baked into the git
	// proxy's forwarder, the GitHub REST transport and the
	// github-sandbox capability provider at startup, each read
	// unsynchronised by requests already in flight. ui.Settings reports
	// them as restartRequired for exactly that reason, and reports a
	// change to one as pending until the daemon is restarted --
	// ui.restartOnlySettings is the list, cmd/grain/daemon.go's
	// liveConfig the other end of it.
	GitHubHost string
	// GitHubInsecureHTTP speaks plain HTTP to GitHubHost instead of
	// HTTPS -- mock servers only. Needs a restart, like GitHubHost above.
	GitHubInsecureHTTP bool
	// GCPProject is the GCP project the gcp-key/gemini-key capabilities
	// mint into; empty disables both. Changing it rebuilds the
	// model.CapabilityRegistry the next cycle resolves a task's grants
	// against (cmd/grain/daemon.go's liveConfig), so configuring a
	// project is what enables those two capabilities -- not the restart
	// that used to be needed after it.
	GCPProject string
	// GCPServiceAccountEmail is the narrow agent service account gcp-key
	// mints keys for. Applied with GCPProject above, and the same way.
	GCPServiceAccountEmail string
	// GCPServiceAccountCount is the number of agent service accounts to use
	// with a fixed numbering scheme for gcp-key. 0 or 1 means a single account.
	GCPServiceAccountCount int
	// IAPBackendService is the GCP backend service name for dynamic IAP IAM reconciliation;
	// empty disables IAP reconciliation.
	IAPBackendService string
	// IAPResource is the IAP web resource whose IAP IAM policy is
	// reconciled as well (or instead), such as a Cloud Run relay's
	// projects/<NUMBER>/iap_web/cloud_run-<REGION>/services/<SERVICE>.
	// Empty derives it from a Cloud Run relay's user IAP audience; "none"
	// turns that off.
	IAPResource string
	// NewestFirst decides which end of the backlog new work joins
	// (bwsalmon/agents#476): false, the default, keeps grain's original
	// shape -- Store.OrderKeyForNewTask files a new task behind
	// everything already queued, so it is dispatched last and sits at the
	// bottom of the list. true files it ahead of everything queued
	// instead, so it dispatches next and sits at the top. A list is
	// top-to-bottom dispatch order either way (ui.Client.ListTasks,
	// grain/task-201); this only decides which end a new task joins.
	NewestFirst bool
	// SandboxBackend is the isolation mechanism this deployment builds every
	// agent and CI sandbox with. Empty means the deployment predates this
	// setting, so cmd/grain resolves it to the backend selected by its startup
	// flags. A non-empty value is one of SandboxBackends and takes effect on the
	// next daemon start; the daemon can only select a backend whose required
	// host-side plumbing was supplied at startup.
	SandboxBackend SandboxBackend
	// SandboxCPUs and SandboxMemoryMB (bwsalmon/agents#534) are the
	// deployment-wide default VM shape orchestrator.KonturConfig passes
	// `konturctl vm create` as `-cpus`/`-memory-mb` when creating a
	// slot's VM -- an alternative, store-backed way to set what an
	// operator could already pass by hand as -kontur-create-arg=-cpus
	// -kontur-create-arg=<n>, surfaced in the settings UI instead of a
	// daemon flag. Zero, the default for both, leaves grain's own default
	// shape in place (kontur.DefaultCPUs/DefaultMemoryMB, 2 vCPU and
	// 8192 MiB) rather than passing a literal "-cpus 0" that
	// VMSpec.Validate would refuse -- the size is still passed on every
	// create, it is just grain's number rather than this deployment's.
	// Both are
	// meaningless under the default orchestrator.HostSandboxes backend
	// (local directories have no CPU/memory shape of their own) and
	// simply go unread there, the same way the kontur* daemon flags do.
	//
	// A change to either reaches the backend while the daemon runs
	// (orchestrator.GranuleSandboxes.SetDefaultShape, called by
	// cmd/grain/daemon.go's liveConfig), so it applies to the next
	// sandbox built rather than to the next process. Sandboxes already
	// running keep the size they were created at -- a VM's shape is
	// decided once, when it is created.
	SandboxCPUs int
	// SandboxMemoryMB is SandboxCPUs' memory counterpart, in MiB.
	SandboxMemoryMB int
	// SandboxDiskGB is the third dimension of that same shape, in GiB:
	// how large a root disk `konturctl vm create` gives the VM, passed as
	// `-disk-size-mb` (in MiB: orchestrator.KonturConfig.createArgs
	// converts). Zero means the same thing the other two mean -- take
	// grain's own default, kontur.DefaultDiskGB. It used to mean "pass no
	// flag", which for disk left a VM as large as the guest image its
	// overlay is backed by (the Granulefile's `VM PACK` sizes disk.img
	// to the rootfs plus 20% headroom), a few hundred megabytes of slack that a build-heavy
	// checkout spends part way through a run.
	//
	// Unlike CPUs and memory, this one needs something of the guest as
	// well as of the hypervisor: a bigger virtual disk is empty space
	// past the end of the filesystem packed into the image until
	// something grows that filesystem onto it, which the Granulefile's
	// grain-growfs unit does on each boot.
	SandboxDiskGB int
	// SandboxCPUOvercommit, SandboxMemoryOvercommit and
	// SandboxMemoryReserveMB are the three knobs of this deployment's
	// sandbox Budget -- how much machine every sandbox shape above may
	// commit between them, agent runs and CI jobs alike (Budget's own doc
	// comment for the arithmetic, and NewBudget for where these three go
	// into it).
	//
	// They were daemon flags when the budget was introduced
	// (grain/task-173's -sandbox-cpu-overcommit and its two neighbours)
	// and are columns now for the reason max_ci_jobs stopped being a flag
	// (grain/task-197): on a deployment whose configuration is a pull
	// request against a state repository, nobody edits a systemd unit,
	// and a limit an operator cannot move from the Settings pane is one
	// they work around by raising a count instead -- which is the very
	// thing the budget exists to stop meaning more machine.
	//
	// Read live: cmd/grain's config.withModelConfig copies all three off
	// the row each reconcile tick, so a budget widened in the pane is the
	// one the next job is admitted against rather than the one the
	// daemon started with.
	//
	// The two ratios are "how much more than the host has may be
	// promised", 1 being exactly what it has -- DefaultCPUOvercommit and
	// DefaultMemoryOvercommit are what a deployment that has never chosen
	// gets, and both of those constants say why they are the number they
	// are. Zero leaves that dimension unbounded by the budget, which is a
	// value an operator may deliberately want: the run counts are then
	// the whole limit, exactly as they were before the budget existed.
	SandboxCPUOvercommit    float64
	SandboxMemoryOvercommit float64
	// SandboxDiskOvercommit is the same ratio for sandbox disk
	// (SandboxDiskGB): how many times over the sandbox volume -- or, on
	// Kubernetes, the cluster's allocatable ephemeral storage -- this
	// deployment may promise its sandboxes' disks, less the disk floor.
	// A sandbox's disk is a thin overlay that is mostly never written, so
	// promising the full size once each is the wrong bound; on Kubernetes
	// the same ratio scales each pod's ephemeral-storage request down
	// (its limit stays the full size), which is what lets more than two
	// 40 GiB sandboxes onto a 100 GiB node. DefaultDiskOvercommit is
	// what a deployment that has never chosen gets, and zero leaves disk
	// unbounded by the sum, held only by the floor.
	SandboxDiskOvercommit float64
	// SandboxMemoryReserveMB is what is kept back from that budget for
	// everything on the sandbox host that is not a sandbox -- the daemon,
	// the container runtime, each VM's own qemu overhead, and the page
	// cache they all read through. Taken off the top, before the memory
	// ratio, so raising the ratio never eats into it.
	//
	// Negative means "grain's own", which is DefaultReserveMemoryMB: an
	// eighth of the machine and never less than 2 GiB. -1 rather than 0
	// because 0 is a reserve somebody chose -- a host that keeps nothing
	// back for itself -- and the two have to stay tellable apart.
	SandboxMemoryReserveMB int
	// SandboxDiskFloorShare and SandboxDiskFloorMinMB are the budget's
	// third dimension, and the one that is not a sum at all: how much of
	// each volume this deployment writes to is kept free, below which a
	// new CI job is held and an agent dispatch deferred (DiskFloor,
	// grain/task-196). A share of the volume with an absolute minimum
	// under it, for the reason DiskFloor's own doc comment gives -- a
	// fixed size is wrong on both a 20 GB store disk and a 4 TB sandbox
	// volume -- and capped at MaxDiskFloorShare of the volume however
	// they are stored, so a floor typed into the pane can never wedge
	// the deployment it was meant to protect.
	//
	// Columns for exactly the reason the three above are, only more so.
	// They were the daemon flags -sandbox-disk-floor-share and
	// -sandbox-disk-floor-min-mb (grain/task-372 made them settings, and
	// those two flags now seed a fresh database the way
	// -sandbox-cpu-overcommit does), and the moment an operator wants to
	// lower this one is the moment the deployment is already in trouble:
	// a volume under its floor holds every dispatch, so the lever that
	// gets work moving again was a systemd unit edit and a daemon
	// restart, on a deployment nobody wants to restart just then.
	//
	// Read live, like the ratios: cmd/grain's config.withModelConfig
	// copies both off the row each reconcile tick, and the disk watcher
	// asks for the floor per reading rather than holding the one it was
	// built with (cmd/grain's diskWatch), so a floor lowered in the pane
	// admits the next job rather than the next restart.
	//
	// Zero in both leaves a filling disk to hold nothing back, which is
	// what every deployment did before the floor existed. That is a
	// value an operator can deliberately want -- and it is also what a
	// row written before these columns existed would read as, which is
	// why the migration adds them with grain's own defaults rather than
	// with an unqualified ADD COLUMN (Store.ensureConfigColumn's callers).
	SandboxDiskFloorShare float64
	SandboxDiskFloorMinMB int
	// NetworkAccess is the deployment-wide default for how much of the
	// internet a run's sandbox may reach (NetworkAccess's own doc
	// comment, grain/task-269), for every task that names none of its
	// own (Task.NetworkAccess). Empty reads back as NetworkOpen
	// (NormalizeNetworkAccess), which is what every sandbox this
	// deployment ever built had before the setting existed.
	//
	// Like the sandbox shape above, it is meaningless under the default
	// orchestrator.HostSandboxes backend -- a directory on the host has
	// the host's own network and nothing here can take it away -- and it
	// is annotated as not in effect there rather than being refused
	// (ui.Settings.SandboxShapeIgnored), the same as the three numbers
	// beside it. What is refused there is a *task* asking for
	// NetworkNone, because that one is a promise to the task rather than
	// a preference of the deployment's.
	//
	// A change reaches the backend while the daemon runs
	// (orchestrator.GranuleSandboxes.SetDefaultShape, cmd/grain/daemon.go's
	// liveConfig), so it applies to the next sandbox built rather than to
	// the next process; a sandbox already running keeps the network it
	// was created with.
	NetworkAccess NetworkAccess
	// SandboxImage is the OCI reference of this installation's own
	// custom sandbox image, for whichever sandbox backend is active: a
	// packed Granule guest on granule and kubernetes-granule, a container
	// image on kubernetes and container.
	//
	// It exists because the stock image is one artifact for every
	// deployment, and what a task needs installed in it is not the same
	// everywhere: one deployment's agents work on Rust, another's need a
	// private CA. Such an image is built FROM one of grain's minimal base
	// images (ghcr.io/bwsalmon/grain/guest-base for VM backends,
	// ghcr.io/bwsalmon/grain/k8s-sandbox-base for pods and containers),
	// which carry only what grain itself needs inside a sandbox -- and
	// that boundary is versioned (pkg/sandboximage.Contract), so a custom
	// image needs rebuilding only when the contract it was built at falls
	// below sandboximage.MinimumContract, not on every grain upgrade.
	//
	// Empty, the default, means the image this build of grain is stamped
	// for on the active backend (DefaultSandboxImage or
	// DefaultK8sSandboxImage, or the backend's own k8s_* override). An
	// explicit daemon flag naming an image (-sandbox-image, a
	// -granule-create-arg -kontur-image pin, -container-sandbox-image)
	// still wins over it.
	//
	// It replaced SandboxSetupScript, a shell script the daemon ran
	// inside the stock guest at dispatch time to derive a local image --
	// granule-only, and minutes of blocked dispatch after each upgrade.
	SandboxImage string
	// SandboxImageMirror is an optional cluster-accessible OCI repository
	// (for example us-docker.pkg.dev/<cluster-project>/grain/guest) into
	// which grain mirrors the build-stamped sandbox guest image on upgrade
	// and startup. When set, off-cluster kubernetes-granule deployments
	// pull the stamped DefaultSandboxImage on the host, push it to this
	// mirror repository under its sha-<short> tag, and schedule sandbox
	// pods against the mirror's pinned digest without treating the image
	// as a custom image override.
	SandboxImageMirror string
	// PackageCacheEnabled is whether this deployment runs its package
	// caches in front of every sandbox's package managers
	// (pkg/pkgcache): with it on, each sandbox is handed an
	// /etc/apt/apt.conf.d file, an /etc/pip.conf and a registry mirror in
	// /etc/docker/daemon.json naming the controller's cache, and has its
	// npm and Go toolchain pointed at the registry and module proxy the
	// same cache serves, so the second run that installs a package --
	// or pulls an image -- gets it from the machine next door.
	//
	// On by default, here and in the schema, and backfilled on for a
	// deployment upgrading into it: what it changes is where a download
	// comes from, not what is downloaded -- apt still verifies the
	// archive's signatures and pip still checks each artifact against the
	// hash its index gave, both of which travel through the cache
	// untouched.
	//
	// Turning it off stops sandboxes being pointed at the cache from the
	// next dispatch onwards. A run already under way keeps the
	// configuration its sandbox was handed, which is deliberate: the
	// files are written once, when the sandbox is created, and rewriting
	// them underneath a running agent would break an install mid-command
	// to no one's benefit. The cached bytes are left alone by the toggle
	// and are thrown away by a flush instead (ui's own
	// POST /api/package-cache/flush).
	PackageCacheEnabled bool
	// PackageCacheSizeGB is how much disk space, in GiB, the package cache
	// may use before evicting the oldest entries (pkg/pkgcache.DefaultMaxBytes).
	// 0 means grain's own default (20 GiB).
	PackageCacheSizeGB int
	// ActionsCacheEnabled is whether this deployment's CI jobs are
	// pointed at the GitHub Actions cache the controller keeps
	// (pkg/actionscache): with it on, every job is handed an
	// ACTIONS_CACHE_URL naming a cache that outlives its sandbox, so
	// `cache-from: type=gha` and `actions/cache` read back what an
	// earlier run of that repository wrote instead of starting cold.
	//
	// On by default, here and in the schema, and backfilled on for a
	// deployment upgrading into it -- what it changes is how fast a
	// build is, never what it builds: an entry is only ever handed to a
	// lookup naming the same key *and* the same version, and a miss
	// costs a cold build and nothing else.
	//
	// It is a switch of its own rather than a size of zero because the
	// two are different questions, and only this one has an answer that
	// is not about disk. What every branch of a repository shares one
	// cache means is the whole of it: anything one branch's CI writes,
	// another branch's CI can read back, so a deployment that runs
	// unreviewed branches on shared machines has already granted that
	// much (a branch's own workflow file is a file the branch can
	// rewrite) and one that has not should turn this off rather than
	// reason about cache keys. See pkg/actionscache's package doc, which
	// says it at length.
	//
	// Turning it off stops the next job being handed a URL. A job
	// already running keeps the one its sandbox was built with, for the
	// reason PackageCacheEnabled leaves a live sandbox alone: the
	// environment is written once, when the sandbox is created. The
	// stored entries are left where they are and are thrown away by a
	// flush instead (ui's own POST /api/actions-cache/flush).
	ActionsCacheEnabled bool
	// ActionsCacheSizeGB is how much disk space, in GiB, that cache may
	// use before it evicts the entries nothing has read for longest
	// (pkg/actionscache.DefaultMaxBytes). 0 means grain's own default
	// (20 GiB).
	//
	// A stored setting rather than the daemon flag it started life as
	// (-actions-cache-size-gb), for PackageCacheSizeGB's reason: on a
	// deployment whose configuration is a pull request against a state
	// repository, changing a flag is an edit to a systemd unit. The flag
	// remains as a first-boot seed and the stored value wins from then
	// on, reaching the running cache through actionscache.SetMaxBytes
	// without a restart.
	ActionsCacheSizeGB int
	// CIRunHistory, CIStepOutputMB, CIStepOutputDays and CIRunOutputKB
	// are how long this deployment's own CI history lives
	// (ci.StoredRunRetention, which holds the arithmetic and the
	// defaults).
	//
	// Two bounds rather than one, because the halves cost different
	// things. CIRunHistory keeps the *records* -- id, ref, commit, and
	// every job's and step's verdict -- which are small and are what "was
	// this job red last week too?" is answered from. The other three
	// bound the *output* those steps printed, which is expensive and is
	// the only thing that makes a red run diagnosable rather than merely
	// countable. A record outlives its own output by design.
	//
	// They were constants until grain/task-269, and are columns now for
	// the reason the sandbox budget's three are (SandboxCPUOvercommit
	// above): on a deployment whose configuration is a pull request
	// against a state repository, changing a constant is a release and
	// changing a daemon flag is an edit to a systemd unit -- which is
	// exactly what put this deployment into a restart loop once. The
	// flags remain as first-boot seeds (-ci-run-history and its three
	// neighbours) and the stored value wins from then on.
	//
	// Read live: cmd/grain's config.withModelConfig copies all four off
	// the row each reconcile tick and ci.StoredRunStore reads them again
	// on every sweep, so a history widened in the Settings pane binds the
	// next run that concludes rather than the next restart.
	//
	// Each takes three readings, exactly as ci.StoredRunRetention
	// documents: a positive number is the bound, 0 is grain's own
	// default, and a negative number is no bound at all -- keep every
	// run, or every byte, forever. Negative rather than 0 for
	// "unbounded", so that a column no migration has written yet cannot
	// mean it by accident.
	CIRunHistory   int
	CIStepOutputMB int
	// CIStepOutputDays is the age half of the output bound: no run's
	// output outlives it, whatever room there is under CIStepOutputMB.
	CIStepOutputDays int
	// CIRunOutputKB is the per-entry cap -- how much output any one run
	// may store, spent on the steps a reader opens first. It is what
	// stops one chatty workflow filling the ceiling on its own; a run
	// that would be larger than the whole ceiling has its output declined
	// rather than stored (ci.declineRunOutput).
	CIRunOutputKB int
	// CICacheMB, CICacheEntries and CICacheDays are what grain CI's job
	// cache may occupy on this deployment's data disk, and CIArtifactMB,
	// CIArtifactRunMB, CIArtifactFileMB and CIArtifactDays the same for
	// the artifacts a failing job left behind (ci.StorageBounds, which
	// holds the arithmetic and the defaults):
	//
	//	CICacheMB         the whole ci-cache directory, in MiB (256)
	//	CICacheEntries    how many entries it holds (4096)
	//	CICacheDays       how long an unasked-for entry is kept (30)
	//	CIArtifactMB      the whole ci-artifacts directory, in MiB (2048)
	//	CIArtifactRunMB   what one run's artifacts may come to (128)
	//	CIArtifactFileMB  the largest single artifact kept (32)
	//	CIArtifactDays    how long a run's artifacts are kept (14)
	//
	// They were the -ci-cache-max-* and -ci-artifact-max-* flags until
	// grain/task-317, and are columns now for the reason the CI history's
	// four above are: on a deployment whose configuration is a pull
	// request against a state repository, changing a daemon flag means
	// editing a systemd unit and restarting -- which is what put this
	// deployment into a 52-restart loop once. The flags remain as
	// first-boot seeds and the stored value wins from then on.
	//
	// Sharper here than for the history, because the System pane's
	// Storage section now reports what each of these is holding
	// (ui.CIJobCacheStorage, ui.CIArtifactStorage, grain/task-276): an
	// operator reading "200 MiB of 256 MiB" and deciding to move the
	// ceiling should not have to edit a unit file to do it.
	//
	// Read live: cmd/grain's config.ciStorageBounds is handed to
	// ci.DirCache and ci.DirArtifacts as a provider both re-read on every
	// sweep, every Save and every Stats, so a ceiling raised in the
	// Settings pane binds the next sweep rather than the next restart. A
	// lowered one frees disk when that sweep runs (within five minutes);
	// the CI pane's own "Clear" is there for an operator who needs it
	// back now.
	//
	// Two readings each, not the history's three: a positive number is
	// the bound and 0 is grain's own default. There is deliberately no
	// negative "keep everything" reading -- ci.StorageBoundsFrom says why
	// a directory on the volume this deployment dispatches out of has no
	// unbounded shape, and ui.UpdateSettings refuses to store one.
	CICacheMB        int
	CICacheEntries   int
	CICacheDays      int
	CIArtifactMB     int
	CIArtifactRunMB  int
	CIArtifactFileMB int
	CIArtifactDays   int
	// CIJobsPerRun is an optional ceiling on how many sandboxes a single
	// grain CI run may hold at once (Limits.CIJobsPerRun,
	// ci.Engine.ConcurrencyLimit). 0, the default, imposes no per-run
	// limit, so all ready jobs in a run proceed directly to the
	// deployment's sandbox budget and any team max_ci_jobs ceiling.
	CIJobsPerRun int
	// CIAgeBoost and CIReserveAfter are the two windows the CI gate
	// orders its waiting jobs by -- Limits.CIAgeBoost and
	// Limits.CIReserveAfter, which is where what they mean is written
	// down, and ci.SharedGate, which applies them.
	//
	// Columns for the reason the four bounds above are: they were
	// compile-time constants (ci.DefaultAgeBoost, ci.DefaultReserveAfter)
	// until grain/task-325, and a deployment that finds the reservation
	// too eager or the boost too slow should not have to ship a binary to
	// say so. Zero in either means grain's own number; they have no
	// first-boot flag, unlike the retention four, because there is
	// nothing about a machine that a deployment would have to say before
	// its first row exists.
	//
	// Read live: cmd/grain's config.withModelConfig copies both off the
	// row each reconcile tick and the gate asks for them on every
	// admission attempt, so a window changed in the Settings pane is one
	// the jobs already queueing are ordered by.
	CIAgeBoost     time.Duration
	CIReserveAfter time.Duration
	// ShowClosedByDefault is the deployment-wide default for whether a
	// task list starts out showing closed tasks (bwsalmon/agents#537):
	// false, the default, matches "hide closed tasks by default" -- a
	// task view opens narrowed by "State is none of Deferred, Closed"
	// (taskFilters.js's defaultFilters), and a viewer edits or deletes
	// that condition to see them. true leaves closed out of it instead.
	// Either way it is only ever the list's *starting* state --
	// like NewestFirst, nothing here forces it to stay that way, and
	// nothing in the daemon's own dispatch loop reads it.
	ShowClosedByDefault bool
	// DefaultFilters is the query-string encoding of the conditions every
	// task list starts with. It uses the same repeated `f` parameters as a
	// task-list URL, so the value in the config repository can be copied
	// from the address bar and reviewed as ordinary text. Empty explicitly
	// means no default filters.
	DefaultFilters string
	// ApprovedByDefault is the deployment-wide default for whether a new
	// task's "Queue immediately" checkbox starts checked (bwsalmon/
	// agents#612): true, the default (DefaultConfig), starts it checked,
	// so filing a task queues it for dispatch rather than parking it for
	// an Approve nobody was waiting to give -- a deployment that approves
	// nearly everything it files should not have to say so twice. false
	// starts it unchecked instead, grain's original shape, where a task
	// files as a proposal until someone approves it.
	//
	// Unlike NewestFirst and ShowClosedByDefault, whose default is also
	// their Go zero value, false here is a zero value that does not mean
	// "nobody has chosen". Nothing has to tell those two apart from the
	// field alone: every path that builds a Config with no stored row
	// behind it goes through DefaultConfig, and the store's own column
	// defaults say the same thing in SQL.
	//
	// Like NewestFirst and ShowClosedByDefault, this only ever seeds the
	// form's *starting* state -- whoever is filing a task can still
	// uncheck it, and CreateTaskRequest.Approved is what actually decides
	// each task's own Approval, not this.
	ApprovedByDefault bool
	// AutoMergeByDefault is ApprovedByDefault's counterpart for
	// NewTaskOverlay.jsx's "Auto-merge once checks pass" checkbox
	// (bwsalmon/agents#612): true, the default (DefaultConfig), starts a
	// new task's Task.AutoMerge on, so a run whose checks pass lands
	// without a human clicking Merge on it; false keeps it off unless
	// someone checks it. Same "starting state only" caveat as
	// ApprovedByDefault -- CreateTaskRequest.AutoMerge, not this, is what
	// a filed task actually gets.
	AutoMergeByDefault bool
	// MaxAutoSubmitDepth and MaxAutoSubmitFanOut are
	// AutoSubmitLimits.Depth and .FanOut for this deployment: how deep a
	// chain of self-approved proposals may get below the last person who
	// approved anything, and how many such proposals one task may have.
	// AutoSubmitLimits' own doc comment is the argument for bounding
	// them at all; Config.AutoSubmitLimits is how everything that
	// enforces them reads this pair.
	//
	// Zero on either means "nobody chose", not "none allowed", and reads
	// back as DefaultAutoSubmitDepth/DefaultAutoSubmitFanOut -- so a
	// deployment upgrading across these columns keeps grain's own
	// bounds, exactly as its column defaults and DefaultConfig say. A
	// deployment that wants no self-approved work at all withholds
	// auto-merge, which is the permission this is a bound on rather than
	// a replacement for.
	//
	// Deployment-wide because deployment-wide is the only configuration
	// surface there is today. docs/data-model.md argues a policy knob of
	// this kind belongs on a Folder -- loose in a scratch area, strict
	// where deploys live -- and it is right; that tree is unbuilt (see
	// ui/repos.go), and when it exists a folder's own pair overrides
	// this one the way every other folder policy will.
	MaxAutoSubmitDepth  int
	MaxAutoSubmitFanOut int
	// BaseCIRedStreak is how many consecutive judged tips of a base branch
	// have to fail the same job before grain treats the branch as
	// confirmed red without waiting for a confirmation re-run
	// (model.BaseBranchCI.RedStreak, grain/task-1034). 0 means
	// DefaultBaseCIRedStreak; read it through BaseCIRedStreakOr.
	BaseCIRedStreak int
	// TimeZone is the IANA zone every timestamp the frontend prints is
	// formatted in (ui/src/time.js), empty meaning DefaultTimeZone.
	//
	// It is a deployment setting rather than a per-viewer one on purpose:
	// the frontend reads it off GET /api/config, which every view already
	// waits for, so the first paint is in the right zone. A per-browser
	// default would be right until somebody looked at the same deployment
	// from a second machine and compared two screens.
	//
	// Until this field existed the whole setting was inert: the column was
	// read into a local that was discarded, PutConfig wrote the constant
	// DefaultTimeZone over whatever was there, and the UI response said
	// "UTC" outright -- so a deployment could neither set a zone nor keep
	// one.
	TimeZone string
	// EnvironmentName is what this deployment is called, for whoever is
	// looking at it: "staging", "dev", a hostname, anything. Empty, the
	// default, means an unnamed deployment and the UI shows nothing at
	// all -- the shape grain has always had, and the right one for an
	// operator running a single deployment who has nothing to tell it
	// apart from.
	//
	// It names nothing the daemon itself does: no dispatch, no sandbox,
	// no credential is chosen by it, and nothing outside the UI reads it.
	// It exists for the one failure a single-operator cluster invites --
	// approving, merging or rebooting on the deployment you thought was
	// the other one -- which is a question of what the screen says, not
	// of what the daemon enforces. A deployment that should refuse to
	// touch a repo wants TargetRepos above; this is a label.
	//
	// Free text, deliberately, rather than an enum of "staging"/"prod":
	// what environments a deployment sits among is the operator's own
	// vocabulary, and grain has no list of them to validate against.
	// ui.UpdateSettings bounds its length so it can be rendered somewhere
	// prominent without a paragraph pasted in taking the pane over, and
	// trims it so a stray space is not the difference between named and
	// unnamed.
	EnvironmentName string
	// DefaultCapabilities is the set of capability ids a new task is
	// filed holding, by id -- the deployment-wide answer to "what should
	// every task here start out able to do", and the reason gcp-key need
	// not be ticked by hand on every task that wants a service-account
	// key in its sandbox (grain/task-14, following task-10's picker
	// rows).
	//
	// It seeds a task's own Grants at creation (ui.CreateTask, which
	// records each as GrantByDefault) rather than being read again at
	// dispatch. That is the whole difference between this and v1's
	// unconditional per-dispatch mint: a seeded grant is on the task,
	// visible in its capability list, and detachable through the same
	// picker as any other, so whoever files a task can drop one they do
	// not want and an operator can take one off a task that is failing on
	// it. The cost of that choice is that turning an entry off here does
	// not disarm the tasks already holding it -- they keep the grant they
	// were filed with, which is the same thing that makes it modifiable
	// in the first place.
	//
	// Distinct, too, from docs/data-model.md's "Attaching capabilities to
	// repos and folders" -- those offers are floors, unioned in at
	// resolution and not droppable by the task. This is a seed.
	//
	// RepoConfig.DefaultCapabilities is the per-repo layer of this same
	// seed (grain/task-24), and composes with it exactly that way: more
	// ids in the set a new task starts with, unioned deployment-first in
	// ui.(*Client).defaultCapabilities. This layer is the one that
	// applies wherever a task points, including a task with no repo at
	// all; a repo can add to it and never subtract from it.
	//
	// An id here that this build's picker does not offer is skipped at
	// creation rather than failing it -- ui.UpdateSettings validates the
	// set on the way in, so that can only be a build that has since
	// retired a capability, and a stale settings row must not become a
	// deployment that can file no tasks at all.
	//
	// A brand-new deployment starts this set with whatever its build
	// registers as on by default (capability.Registration.DefaultOn,
	// capability.NewDeploymentConfig) -- nothing for core grain. That is
	// a seed of the first stored row only: a stored set is never extended
	// by a build default afterwards, so one an operator removed stays
	// removed across restarts and upgrades.
	DefaultCapabilities []string
	// PromptExtension is the standing instruction text every run
	// dispatched on this deployment is given on top of the prompt grain
	// builds for it (grain/task-114) -- prompt_extension.go's own doc
	// comment has what it is for and how the three layers of it compose.
	// Empty, the default, adds nothing at all, which is what every
	// deployment did before this field existed.
	//
	// Unlike DefaultCapabilities above, this is *not* seeded onto a task
	// at creation: it is read at dispatch (orchestrator.RunCycle
	// refreshes orchestrator.Config.PromptExtension from this row every
	// tick, the same as MaxAgentTurns), so editing it changes what the
	// next run is told rather than only what the next task is filed
	// with. That is the right direction for text an operator tunes by
	// watching runs go wrong: a seed would leave every task already
	// queued carrying the wording that was wrong.
	//
	// Free text, unvalidated beyond being trimmed -- it is prose handed
	// to an agent, exactly like a task's own Body, and grain has no more
	// business bounding one than the other.
	PromptExtension string
	// RepairTemplateID is the ID of the template used to file repair tasks
	// when base branch CI fails or when release triage is filed. Schema 24
	// carried the default team's own over it where that named one: the
	// team's was asked first.
	RepairTemplateID string
	// AgentFramework is the framework a task that chose no model, no
	// connection and no framework runs on (pkg/daemon's AgentPool), empty
	// for the default. It was the default team's agent_framework until
	// schema 24.
	AgentFramework string
	// AgentGitName and AgentGitEmail are the commit identity a run is
	// given where its git credential names nobody
	// (orchestrator/git_identity.go), each empty for the sandbox's
	// default. They were the default team's until schema 24.
	AgentGitName  string
	AgentGitEmail string
	// TargetRepos is the repositories this deployment dispatches work
	// into, empty for all of them (RepoAllowed, repo_scope.go). It was
	// the default team's target_repos until schema 24.
	TargetRepos []string
	// TaskBranchTemplate names branches for newly created tasks. Empty keeps
	// the historical grain/task-{id} form. {id} is required when set,
	// {deployment} expands to DeploymentID and {instance} to this
	// installation's durable identity, and {slug} to a few words of
	// the task's title (BranchSlug).
	//
	// A deployment seeded since grain/task-3091 starts with
	// DefaultTaskBranchTemplate here (NewDeploymentConfig), so two grains
	// pointed at the same repository stop sharing grain/task-N without
	// anyone configuring it. A deployment whose row predates that keeps
	// its empty template, and so its grain/task-{id} branches, until a
	// system admin opts in on System -> Agents & Task Branches.
	TaskBranchTemplate string
	// DeploymentID is the short name this deployment puts in its task
	// branches ({deployment} in TaskBranchTemplate), so that two grains
	// pointed at the same repository do not push to each other's
	// grain/.../task-12. Empty means the random default derived from the
	// installation id (EffectiveDeploymentID); a system admin can replace
	// it with something readable. Changing it renames only the branches
	// of tasks created afterwards -- every task keeps the branch it was
	// given.
	DeploymentID string
	// UpgradeCheckMode decides what grain does with the upgrade it has
	// just installed (pkg/upgrade, and orchestrator's own upgrade-check
	// reconciler): whether it files a task to go and check that the
	// deployment the new binary brought up is actually healthy, and
	// whether that task is queued straight away or waits for a person to
	// approve it.
	//
	// One of UpgradeCheck{Off,Approve,Auto}, and read through
	// NormalizeUpgradeCheckMode so that an empty column -- a row written
	// before this setting existed, or by a build that did not know it --
	// reads as the default rather than as "off". That direction is
	// deliberate: an upgrade nobody checked is the failure this exists to
	// catch, and a deployment that has never expressed an opinion is far
	// likelier to want the check than to want silence.
	//
	// An upgrade is checked at most once, whatever this says: the task
	// carries a tag naming the upgrade it is about, and a check already
	// filed for that upgrade is never filed again -- so turning this from
	// "off" to "auto" long after an upgrade landed does not suddenly
	// produce a check of it (see orchestrator's own upgradeCheckWindow,
	// which is the other half of that).
	UpgradeCheckMode string
	// UpgradeCheckPausesPipeline asks for the queue to be held while that
	// check runs: grain pauses the pipeline (PipelinePause) as it files
	// the check, exempting the check itself, and resumes the moment the
	// check finishes.
	//
	// Off by default, because it is the expensive half of the feature: a
	// deployment that holds its queue for a check will do no other work
	// until that check is done, which is exactly what an operator who
	// does not trust an upgrade to be harmless wants and exactly what an
	// operator who does not want to notice an upgrade at all does not.
	// It is honoured only when UpgradeCheckMode actually files something
	// -- there is nothing to wait for otherwise -- and a pause taken for
	// a check that never finishes is lifted anyway once the deadline
	// orchestrator puts under it has passed, so this cannot strand a
	// deployment.
	UpgradeCheckPausesPipeline bool
	// AutoUpgradeChannel is the branch or channel this deployment
	// upgrades itself onto once a day ("latest", "candidate", "main", or
	// any other branch -upgrade-allowed-branches admits), and "" -- the
	// default -- for no automatic upgrades at all. It is checked against
	// the upgrader's own allow-list when it is saved, the way POST
	// /api/upgrade checks a branch, so a typo is refused then rather than
	// at three in the morning.
	AutoUpgradeChannel string
	// AutoUpgradeTime is when, each day, that upgrade is scheduled: a
	// 24-hour "HH:MM" on this deployment's own clock (TimeZone). Read it
	// through ParseAutoUpgradeTime. The upgrade itself goes through the
	// same drain a scheduled one does (orchestrator's
	// reconcileAutoUpgrade), so this is when the queue shuts, not
	// necessarily when the restart happens.
	AutoUpgradeTime string
	// JudgementModel is the model the symbolic model JudgementModelName
	// ("judgement") resolves to: the model a durable judgement agent --
	// the pipeline steward -- runs on (docs/design/durable-tasks.md#DT-11,
	// ResolveJudgementModel). Empty, the default, resolves it to the
	// strongest model this deployment's connections offer, by
	// JudgementModelRanking.
	JudgementModel string
	// ConsoleSessions are the standing interactive sessions this
	// deployment keeps open -- the director, and whatever else it wants a
	// permanent chat with (ConsoleSession's own doc comment, grain/task-415).
	//
	// nil is not the empty list here, and the difference matters: nil is
	// a deployment that has never chosen, which gets the director
	// (DefaultConsoleSessions); an empty non-nil slice is a deployment
	// that chose to have none, and keeps having none. Every reader goes
	// through ConsoleSessionsOr rather than reading this field directly,
	// so that rule is written once.
	//
	// Stored as JSON in one column (EncodeConsoleSessions) rather than a
	// table, for the reason that function gives.
	ConsoleSessions []ConsoleSession
	// Kubernetes sandbox settings (kubernetes and kubernetes-granule
	// backends), store-backed so an operator can configure and switch to
	// Kubernetes sandboxes in Settings -> Sandboxes or the config repo
	// without editing daemon startup flags.
	K8sNamespace      string
	K8sKubectl        string
	K8sSandboxImage   string
	K8sGranuleImage   string
	K8sWorkspace      string
	K8sGitProxyHost   string
	K8sSandboxService string
	K8sServiceAccount string
	K8sNodeSelector   []string
	K8sPrivileged     bool
	K8sNetworkPolicy  bool
	K8sDeployment     string
	K8sContainer      string
	// K8sPriorityClassInteractive, K8sPriorityClassAgent and
	// K8sPriorityClassCI name the PriorityClass a Kubernetes sandbox pod
	// of each kind runs at (orchestrator.K8sPriorityClasses): an
	// interactive session's, an ordinary agent run's (and a persistent
	// sandbox's), and a CI job's. Unlike the settings above, "" is not
	// "the default" but "no priority class" -- what a deployment whose
	// cluster has not applied deploy/kubernetes/'s PriorityClasses
	// wants -- so the defaults (DefaultK8sPriorityClass*) are written in
	// by DefaultConfig, the daemon's flags and the column's own default.
	K8sPriorityClassInteractive string
	K8sPriorityClassAgent       string
	K8sPriorityClassCI          string
	// MCPServers are the capability servers this deployment attaches,
	// keyed by capability_id, in the shape of an Antigravity mcpServers
	// object (protocol.ServerConfig, docs/capabilities-spec.md §3.1).
	// Stored as JSON in one column and exported to grain/config with
	// the rest of this row, so it is plain text: a sensitive env or
	// headers value is a $secret:<name> reference into pkg/secrets, and
	// nothing here ever holds what one resolves to (§3.1.2).
	//
	// Decoded without validation (DecodeMCPServers): an entry a merged
	// grain/config change got wrong is refused on its own when the
	// daemon attaches it and shown in Settings, rather than making the
	// whole row unreadable. Writes through the UI and CLI validate with
	// protocol.ParseServers, and so does `grain state check`.
	MCPServers map[string]protocol.ServerConfig
	// CapabilityPolicy is which repositories, run kinds and trigger
	// events may hold which capability reference (§4.2.2), checked at
	// admission before anything is minted. Stored as JSON in one column
	// and exported to grain/config with the rest of this row.
	CapabilityPolicy CapabilityPolicy
	// CLIRun is where each agent CLI runs, keyed by framework name
	// (claude, codex, antigravity, custom): on the host, in a per-run
	// container of an image, or whichever of those the host allows
	// (cli_run.go, docs/design/cli-run-location.md). A framework with no
	// entry runs where the daemon's flags, and then its built-in default,
	// say.
	CLIRun map[string]CLIRunSetting
	// SandboxOvercommit is whether the sandbox budget's two overcommit
	// ratios are SandboxCPUOvercommit and SandboxMemoryOvercommit as
	// stored (fixed, the default) or are tuned by the auto overcommit
	// controller inside these bounds (overcommit.go). In auto mode the
	// two stored ratios are where the controller starts.
	SandboxOvercommit OvercommitSettings
	// ExternalAgentAPI enables the daemon's /mcp endpoint and bearer-token
	// authentication for external agents (docs/agent-api.md, "External
	// agents"). Off by default so a deployment must opt in before any
	// external credential is accepted.
	ExternalAgentAPI bool
}

// Default Kubernetes sandbox configuration values.
const (
	DefaultK8sNamespace        = "grain"
	DefaultK8sKubectl          = "kubectl"
	DefaultK8sDeployment       = "grain"
	DefaultK8sContainer        = "grain"
	DefaultK8sWorkspace        = "/home/agent"
	DefaultK8sGranuleWorkspace = "/home/debian"
	DefaultK8sSandboxImage     = "ghcr.io/bwsalmon/grain/k8s-sandbox:latest"
	DefaultSandboxImage        = "ghcr.io/bwsalmon/grain/guest:latest"

	// The PriorityClasses deploy/kubernetes/ ships, highest first --
	// k8s.PriorityClassInteractive and its siblings, restated here
	// because this package does not import that one.
	DefaultK8sPriorityClassInteractive = "grain-interactive"
	DefaultK8sPriorityClassAgent       = "grain-agent"
	DefaultK8sPriorityClassCI          = "grain-ci"
)

// UpgradeCheckMode's own vocabulary -- what a deployment does with the
// upgrade it just installed. TEXT in the schema, enum here, for the
// reason schema.go's own doc comment gives for every other enum column.
const (
	// UpgradeCheckOff files nothing: an upgrade lands and grain says
	// nothing further about it, which is what every deployment did before
	// this setting existed.
	UpgradeCheckOff = "off"
	// UpgradeCheckApprove files the check as a proposal. It sits at the
	// head of the backlog with every other queued task behind it, and
	// runs when somebody approves it -- for a deployment that wants to
	// decide, upgrade by upgrade, whether spending a run on a check is
	// worth it.
	UpgradeCheckApprove = "approve"
	// UpgradeCheckAuto files it approved, at the head of the backlog, so
	// it is the next thing dispatched. The default.
	UpgradeCheckAuto = "auto"
)

// DefaultAutoUpgradeTime is the time of day an automatic upgrade is
// scheduled for when the operator picks a channel without saying when:
// early morning on the deployment's own clock, when it is least likely
// to have somebody waiting on it.
const DefaultAutoUpgradeTime = "03:00"

// ParseAutoUpgradeTime reads Config.AutoUpgradeTime: a 24-hour "HH:MM"
// from 00:00 to 23:59, two digits each. Anything else is an error, so a
// settings save can refuse it rather than leave a schedule that never
// fires.
func ParseAutoUpgradeTime(hhmm string) (hour, minute int, err error) {
	hhmm = strings.TrimSpace(hhmm)
	if len(hhmm) != 5 || hhmm[2] != ':' {
		return 0, 0, fmt.Errorf("auto-upgrade time %q is not HH:MM", hhmm)
	}
	h, herr := strconv.Atoi(hhmm[:2])
	m, merr := strconv.Atoi(hhmm[3:])
	if herr != nil || merr != nil || h < 0 || h > 23 || m < 0 || m > 59 ||
		hhmm[0] == '+' || hhmm[0] == '-' || hhmm[3] == '+' || hhmm[3] == '-' {
		return 0, 0, fmt.Errorf("auto-upgrade time %q is not a time of day between 00:00 and 23:59", hhmm)
	}
	return h, m, nil
}

// UpgradeCheckModes is the vocabulary as a list, in the order a picker
// should offer it -- least to most that grain does on its own.
func UpgradeCheckModes() []string {
	return []string{UpgradeCheckOff, UpgradeCheckApprove, UpgradeCheckAuto}
}

// NormalizeUpgradeCheckMode folds a stored (or typed, or flagged) mode
// into the vocabulary above: empty and anything unrecognised read as
// UpgradeCheckAuto, for the reason Config.UpgradeCheckMode's own doc
// comment gives. Case and surrounding space are not a choice anybody
// meant to make, so both are ignored.
func NormalizeUpgradeCheckMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case UpgradeCheckOff:
		return UpgradeCheckOff
	case UpgradeCheckApprove:
		return UpgradeCheckApprove
	default:
		return UpgradeCheckAuto
	}
}

// ValidUpgradeCheckMode reports whether mode is one this build knows --
// what ui.UpdateSettings validates a save against, so that a typo is
// refused at the moment somebody makes it rather than silently read back
// as the default on every tick afterwards.
func ValidUpgradeCheckMode(mode string) bool {
	switch mode {
	case UpgradeCheckOff, UpgradeCheckApprove, UpgradeCheckAuto:
		return true
	}
	return false
}

// DefaultConfig is the configuration a deployment that has never chosen
// one runs: every field's own zero value, except the handful whose
// default is not it.
//
// Every path that builds a Config with nothing stored behind it starts
// here -- cmd/grain/daemon.go's flag seed for a fresh database,
// ui.UpdateSettings' first save, and the two API responses that have to
// describe a deployment whose grain_config row does not exist yet
// (ui.GetSettings, ui's handleConfig) -- so "on unless somebody turned it
// off" is one fact in one place rather than a literal true repeated at
// each of them. schema.go's grain_config DDL, and the store migrations
// that add these columns to a database predating them, carry the same
// defaults in SQL for the rows this constructor never touches.
func DefaultConfig() Config {
	return Config{
		PollInterval:        DefaultPollInterval,
		ApprovedByDefault:   true,
		AutoMergeByDefault:  true,
		PackageCacheEnabled: true,
		ActionsCacheEnabled: true,
		MaxAutoSubmitDepth:  DefaultAutoSubmitDepth,
		MaxAutoSubmitFanOut: DefaultAutoSubmitFanOut,
		MaxWorkers:          DefaultMaxWorkers,
		MaxMergers:          DefaultMaxMergers,
		MaxMergesPerRepo:    DefaultMaxMergesPerRepo,
		MergeBatchSize:      DefaultMergeBatchSize,
		MergeGatherWindow:   DefaultMergeGatherWindow,
		MaxCIJobs:           DefaultMaxCIJobs,
		// The sandbox budget's own three, so a deployment that has never
		// opened the Settings pane is admitted against grain's numbers
		// rather than against a zero ratio, which would bound nothing.
		// The reserve's -1 is "an eighth of this machine" rather than a
		// figure chosen here, since what to keep back depends on a host
		// this constructor has not seen (DefaultReserveMemoryMB).
		SandboxCPUOvercommit:    DefaultCPUOvercommit,
		SandboxMemoryOvercommit: DefaultMemoryOvercommit,
		SandboxDiskOvercommit:   DefaultDiskOvercommit,
		SandboxMemoryReserveMB:  ReserveMemoryDefault,
		// And the budget's third dimension, on the same terms: a
		// deployment that has chosen nothing keeps grain's own tenth of
		// each volume free rather than nothing at all, which is what a
		// zero here would mean (DiskFloor).
		SandboxDiskFloorShare: DefaultDiskFloorShare,
		SandboxDiskFloorMinMB: MinDiskFloorMB,
		UpgradeCheckMode:      UpgradeCheckAuto,
		DefaultFilters:        DefaultTaskFilters,

		K8sPriorityClassInteractive: DefaultK8sPriorityClassInteractive,
		K8sPriorityClassAgent:       DefaultK8sPriorityClassAgent,
		K8sPriorityClassCI:          DefaultK8sPriorityClassCI,
	}
}

// NewDeploymentConfig is DefaultConfig as a brand-new deployment stores
// it in its first grain_config row -- cmd/grain daemon's flag seed
// (daemon.Config.ToModelConfig), the first user's account (PutUser) and
// the first Settings save (ui.UpdateSettings) -- with the one setting a
// new deployment starts on and an existing one does not: the
// deployment-unique task branch template (grain/task-3091).
//
// Not in DefaultConfig itself, which also describes a deployment that
// has stored nothing and is what tests and the demo build their rows
// from: the column's own DEFAULT is the empty string, so a deployment
// whose row predates this keeps naming branches grain/task-{id} and the
// open pull requests on them are left where they are.
func NewDeploymentConfig() Config {
	c := DefaultConfig()
	c.TaskBranchTemplate = DefaultTaskBranchTemplate
	return c
}

// DefaultTaskBranchTemplate is the task branch template a deployment
// seeded since grain/task-3091 starts with: its deployment id in the
// branch, which EffectiveDeploymentID makes distinct for every
// installation from the moment its store is created, and -- since
// grain/task-56 -- a few words of the task's title after its number
// ({slug}, FormatTaskBranchName), so a branch and its pull request say
// what they are about rather than only which task they are.
const DefaultTaskBranchTemplate = "grain/{deployment}/task-{id}-{slug}"

// DefaultPollInterval is Config.PollInterval for a deployment that has
// never chosen one -- named here rather than repeated at each of the
// places that have to agree on it: DefaultConfig above, cmd/grain
// daemon's own -poll-interval flag default, scripts/setup.sh, and
// terraform.
const DefaultPollInterval = 10 * time.Second

// DefaultMaxWorkers is Config.MaxWorkers for a deployment that has never
// chosen one -- named here rather than repeated at each of the places
// that have to agree on it: DefaultConfig above, cmd/grain daemon's own
// -max-workers flag default, and scripts/setup.sh.
const DefaultMaxWorkers = 10

// DefaultMaxMergers is Config.MaxMergers for a deployment that has never
// chosen one -- named here rather than repeated at each of the three
// places that have to agree on it: DefaultConfig above, cmd/grain
// daemon's own -max-mergers flag default, and schema.go's grain_config
// DDL (which is also what Store.ensureConfigWorkerMergerColumns
// backfills a database predating the column with).
const DefaultMaxMergers = 1

// DefaultAgentBackendConcurrency is the per-connection concurrency limit
// (AgentBackend.Concurrency) when a backend connection does not specify
// one (Concurrency <= 0).
const DefaultAgentBackendConcurrency = 4

// DefaultTaskFilters is the task list's useful starting view: work that has
// been set aside, and live chats that somebody is already driving, stay out
// of the ordinary backlog. Deployments can replace it through
// grain_config.default_filters.
const DefaultTaskFilters = "f=state:none:deferred|closed&f=kind:none:interactive"

// DefaultMaxMergesPerRepo preserves the merge queue's original behavior.
const DefaultMaxMergesPerRepo = 1

// DefaultBaseCIRedStreak is Config.BaseCIRedStreak where nothing has
// been chosen: two consecutive red tips on the same job, which is the
// same two samples an ordinary confirmation takes.
const DefaultBaseCIRedStreak = 2

// BaseCIRedStreakOr is Config.BaseCIRedStreak as the base-branch
// reconciler reads it: the stored number where one has been chosen,
// DefaultBaseCIRedStreak where nothing has, and never fewer than two --
// a streak of one is a single red, which is what the confirmation exists
// to challenge. A nil Config reads as the default.
func (c *Config) BaseCIRedStreakOr() int {
	n := DefaultBaseCIRedStreak
	if c != nil && c.BaseCIRedStreak != 0 {
		n = c.BaseCIRedStreak
	}
	return max(n, 2)
}

// MergeBatchSizeOr is Config.MergeBatchSize as the queue reads it: the
// stored number where one has been chosen, DefaultMergeBatchSize where
// nothing has, and never more than MaxMergeBatchSize however a row got
// written. A nil Config -- a deployment with no row yet -- reads as the
// default, the same way every other setting here does.
func (c *Config) MergeBatchSizeOr() int {
	n := DefaultMergeBatchSize
	if c != nil && c.MergeBatchSize != 0 {
		n = c.MergeBatchSize
	}
	if n < 1 {
		return 1
	}
	if n > MaxMergeBatchSize {
		return MaxMergeBatchSize
	}
	return n
}

// MergeGatherWindowOr is Config.MergeGatherWindow as the queue reads it:
// the stored duration where one has been chosen,
// DefaultMergeGatherWindow where nothing has, and zero -- no wait at all
// -- where an operator has turned it off with a negative one. Never more
// than MaxMergeGatherWindow, however a row got written, since the cost of
// this setting is latency on every change it holds.
//
// A nil Config reads as the default, the same way MergeBatchSizeOr does.
func (c *Config) MergeGatherWindowOr() time.Duration {
	d := DefaultMergeGatherWindow
	if c != nil && c.MergeGatherWindow != 0 {
		d = c.MergeGatherWindow
	}
	if d < 0 {
		return 0
	}
	if d > MaxMergeGatherWindow {
		return MaxMergeGatherWindow
	}
	return d
}

// AgentFramework's own vocabulary -- the three agent.Framework
// implementations pkg/agent has today (pkg/agent/antigravity,
// pkg/agent/claude, pkg/agent/codex). Named here, not in any of those
// packages, so this file can reference them without pkg/model depending
// on pkg/agent or any of its implementations.
const (
	AgentFrameworkAntigravity = "antigravity"
	AgentFrameworkClaude      = "claude"
	AgentFrameworkCodex       = "codex"
	// AgentFrameworkCustom runs a deployment-owned executable from the
	// checked-out state repository using docs/custom-agent-backends.md's
	// versioned JSON protocol.
	AgentFrameworkCustom = "custom"

	// LegacyAgentFrameworkGemini is what AgentFrameworkAntigravity used
	// to be called, back when it named a home-grown in-process Gemini
	// API loop rather than the Antigravity CLI that replaced it. It is
	// not a framework anyone can select any more -- only a value
	// NormalizeAgentFramework still recognizes on the way in, so an
	// upgrade does not strand a deployment on a word with no
	// implementation behind it.
	LegacyAgentFrameworkGemini = "gemini"
)

// AgentBackend is one connection: an independently authenticated agent
// installation (docs/connections-and-models.md). ID is stable across edits
// and is also the quota/pause identity; Framework chooses the CLI
// implementation. Models lists the models this connection advertises -- a
// capability, not a choice: which model a run uses is the task's or its
// team's (Task.Model, Team.DefaultModel).
type AgentBackend struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Framework string `json:"framework"`
	// Models, when it lists anything, is the operator's pin: the
	// connection advertises exactly these and nothing its provider adds
	// later. Empty on a connection whose provider can be asked
	// (DiscoversModels) means "advertise whatever the provider serves":
	// Discovered, minus ExcludedModels.
	Models []string `json:"models,omitempty"`
	// ExcludedModels are models a discovering connection does not
	// advertise even though its provider lists them. It is read only
	// while Models is empty.
	ExcludedModels []string `json:"excludedModels,omitempty"`
	// Discovered is the last list the connection's provider reported,
	// kept by ui.Client.AdvertiseDiscoveredModels. It is a cache, not
	// configuration: a settings save never writes it (the stored value is
	// carried over), and it is replaced every time the provider answers.
	Discovered *DiscoveredModels `json:"discovered,omitempty"`
	// AuthMode selects provider authentication. Empty preserves each provider's
	// legacy behavior.
	AuthMode string `json:"authMode,omitempty"`
	// CredentialBackendID reuses authentication from another backend of the
	// same framework. The secret remains owned by the source backend.
	CredentialBackendID string `json:"credentialBackendId,omitempty"`
	// Disabled keeps the backend configured but removes it from dispatch
	// rotation. False is deliberately the default for existing configs.
	Disabled bool `json:"disabled,omitempty"`
	// Command is the executable for a custom backend. Relative paths are
	// resolved from the deployment's checked-out state repository.
	Command string `json:"command,omitempty"`
	// CLIPath is the executable a claude, codex or antigravity connection
	// runs, overriding the deployment's -claude-path / -codex-path /
	// -agy-path for this connection alone. It is a separate field from
	// Command, which belongs to a custom backend and is read relative to
	// the state repository; this one is an absolute path, or a bare name
	// resolved on the daemon's own PATH, on the machine (or inside the
	// container) the daemon runs in. Empty means the deployment flag, then
	// PATH -- what every connection did before the field existed.
	//
	// Setting it is effectively "run this binary as the daemon": the
	// daemon execs it with its own account and hands it the connection's
	// credential. So only a system admin or an admin of the connection's
	// own team -- whoever may edit its credential -- may set it, and every
	// change is recorded in that team's audit trail (pkg/ui's
	// auditCLIPathChanges).
	CLIPath string `json:"cliPath,omitempty"`
	// Team is the team a connection was saved under before grain had one
	// deployment. Nothing reads it: every connection is the deployment's
	// (DeploymentAgentBackends). It is kept so a stored connection that
	// carries it round-trips unchanged.
	Team string `json:"team,omitempty"`
	// Concurrency is the maximum number of concurrent agent runs permitted
	// on this backend/executor connection (0 = default / DefaultAgentBackendConcurrency, which is 4).
	Concurrency int `json:"concurrency,omitempty"`
	// ModelTurnTimeout and ModelRetryCutoff tune an antigravity
	// connection's model-turn deadline (pkg/agent/antigravity's
	// turndeadline.go), as Go durations ("20m", "90s"). ModelTurnTimeout
	// is the slow-turn ceiling: how long one model turn may run while
	// agy logs no provider errors. ModelRetryCutoff is how long a turn
	// may run once agy's log shows it retrying a provider error, and the
	// whole limit when that log cannot be read. Empty means the default
	// (15m and 100s); ParseModelTurnLimit holds the bounds. A provider
	// that is legitimately slow is what the first is for.
	ModelTurnTimeout string `json:"modelTurnTimeout,omitempty"`
	ModelRetryCutoff string `json:"modelRetryCutoff,omitempty"`
}

// EffectiveConcurrency returns the maximum number of concurrent agent runs
// permitted on this backend connection, defaulting to
// DefaultAgentBackendConcurrency (4) when Concurrency is unset (<= 0).
func (b AgentBackend) EffectiveConcurrency() int {
	if b.Concurrency <= 0 {
		return DefaultAgentBackendConcurrency
	}
	return b.Concurrency
}

// The bounds a connection's ModelTurnTimeout and ModelRetryCutoff are held
// to. Below the floor a turn that is merely thinking hard is cut; above
// the ceiling a refused turn is left retrying for longer than a run
// should spend waiting on one provider call.
const (
	MinModelTurnLimit = 30 * time.Second
	MaxModelTurnLimit = 60 * time.Minute
)

// ParseModelTurnLimit reads one of a connection's model-turn limits. The
// empty string is the default and parses as zero.
func ParseModelTurnLimit(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration such as 90s or 20m", value)
	}
	if d < MinModelTurnLimit || d > MaxModelTurnLimit {
		return 0, fmt.Errorf("%s is outside %s to %s", d, MinModelTurnLimit, MaxModelTurnLimit)
	}
	return d, nil
}

// TurnLimits is the connection's model-turn ceiling and retry cutoff,
// zero for either that is unset. A value that does not parse -- one
// written by hand into the store, past the validation a settings save
// applies -- is the default too, rather than a connection that cannot
// dispatch.
func (b AgentBackend) TurnLimits() (turnTimeout, retryCutoff time.Duration) {
	turnTimeout, _ = ParseModelTurnLimit(b.ModelTurnTimeout)
	retryCutoff, _ = ParseModelTurnLimit(b.ModelRetryCutoff)
	return turnTimeout, retryCutoff
}

// AgentCLIBinary is the CLI a connection of framework runs -- "claude",
// "codex" or "agy" -- or "" for a framework with no CLI of grain's own
// to point at (a custom command names its own). It is what CLIPath
// overrides.
func AgentCLIBinary(framework string) string {
	switch NormalizeAgentFrameworkName(framework) {
	case AgentFrameworkClaude:
		return "claude"
	case AgentFrameworkCodex:
		return "codex"
	case AgentFrameworkAntigravity:
		return "agy"
	}
	return ""
}

// DeploymentAgentBackends is every connection in backends, in order:
// model connections are the deployment's, and dispatch, the model proxy
// and model resolution read them all (docs/single-tenant-reset.md, stage
// 1). disabled ones are kept only when includeDisabled is set.
func DeploymentAgentBackends(backends []AgentBackend, includeDisabled bool) []AgentBackend {
	var out []AgentBackend
	for _, b := range backends {
		if includeDisabled || !b.Disabled {
			out = append(out, b)
		}
	}
	return out
}

// DiscoveredModels is what a connection's provider last said it serves
// (AgentBackend.Discovered).
type DiscoveredModels struct {
	// Models is the last list the provider answered with. It survives a
	// provider that stops answering: the connection keeps advertising
	// it, with Error saying why it could not be refreshed.
	Models []string `json:"models,omitempty"`
	// RefreshedAt is when the provider last answered with Models.
	RefreshedAt time.Time `json:"refreshedAt"`
	// Error is why the latest attempt to ask the provider failed, or ""
	// when it answered.
	Error string `json:"error,omitempty"`
}

// DiscoversModels reports whether b's provider can be asked which
// models it serves -- every framework but a custom command.
func (b AgentBackend) DiscoversModels() bool {
	switch NormalizeAgentFrameworkName(b.Framework) {
	case AgentFrameworkAntigravity, AgentFrameworkClaude, AgentFrameworkCodex:
		return true
	}
	return false
}

// PinnedModels is the operator's own list of what b serves (Models),
// deduplicated and in order -- empty when they named none.
func (b AgentBackend) PinnedModels() []string {
	var list []string
	for _, m := range b.Models {
		m = strings.TrimSpace(m)
		if m != "" && !slices.Contains(list, m) {
			list = append(list, m)
		}
	}
	return list
}

// AdvertisesDiscovered reports whether b advertises what its provider
// reports rather than a list the operator pinned.
func (b AgentBackend) AdvertisesDiscovered() bool {
	return b.DiscoversModels() && len(b.PinnedModels()) == 0
}

// ExcludesModel reports whether the operator excluded model from what b
// advertises of its provider's list.
func (b AgentBackend) ExcludesModel(model string) bool {
	model = strings.TrimSpace(model)
	return slices.ContainsFunc(b.ExcludedModels, func(m string) bool { return strings.EqualFold(strings.TrimSpace(m), model) })
}

// NoLongerDiscovers reports whether b advertises its provider's list
// and that list -- once it has one -- does not have model: the provider
// stopped reporting it, or never did. A model the operator excluded is
// not counted here.
func (b AgentBackend) NoLongerDiscovers(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" || !b.AdvertisesDiscovered() || b.Discovered == nil || len(b.Discovered.Models) == 0 || b.ExcludesModel(model) {
		return false
	}
	return !slices.ContainsFunc(b.Discovered.Models, func(m string) bool { return strings.EqualFold(m, model) })
}

// SupportedModels returns the deduplicated list of models supported by
// this connection: the operator's pinned list when there is one; else,
// for a connection whose provider can be asked, what the provider last
// reported minus ExcludedModels. It is empty when neither says anything:
// grain does not guess a model name for a connection (HasModels).
func (b AgentBackend) SupportedModels() []string {
	list := b.PinnedModels()
	if len(list) > 0 {
		return list
	}
	if b.DiscoversModels() && b.Discovered != nil {
		for _, m := range b.Discovered.Models {
			m = strings.TrimSpace(m)
			if m != "" && !slices.Contains(list, m) && !b.ExcludesModel(m) {
				list = append(list, m)
			}
		}
	}
	return list
}

// HasModels reports whether b advertises any model at all. A connection
// that does not -- a provider that has never answered, a custom command
// with an empty list -- has "no models -- set one", and a task that
// would run on it waits rather than being dispatched onto a guessed
// model name (docs/connections-and-models.md).
func (b AgentBackend) HasModels() bool { return len(b.SupportedModels()) > 0 }

// ProviderModelAuto is what agy is asked for to let it pick the model
// itself -- its own default, and the one a connection runs while its
// team has not chosen a default model.
const ProviderModelAuto = "auto"

// EffectiveModel is the model b runs a task that chose none, and whose
// team has not chosen a default model either: the first model the
// operator pinned, else the provider's own default -- "auto" for agy,
// otherwise the first model the provider lists. It is "" for a
// connection with no models (HasModels), which such a task waits on.
// The UI labels it "provider default (unset)", since nobody chose it.
func (b AgentBackend) EffectiveModel() string {
	if pinned := b.PinnedModels(); len(pinned) > 0 {
		return pinned[0]
	}
	models := b.SupportedModels()
	if len(models) == 0 {
		return ""
	}
	if NormalizeAgentFrameworkName(b.Framework) == AgentFrameworkAntigravity {
		return ProviderModelAuto
	}
	return models[0]
}

// SupportsModel reports whether this connection supports the requested model.
func (b AgentBackend) SupportsModel(model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	for _, m := range b.SupportedModels() {
		if strings.EqualFold(m, model) {
			return true
		}
	}
	return false
}

// ConnectionsOnly reports whether c is a row `grain connections import`
// wrote into a data dir no daemon has started on yet: nothing in it but
// the deployment's connections and default model, none of the settings
// a daemon's first start seeds from its flags (no poll interval, no
// worker count, no GitHub host -- a seeded row always names one). The
// daemon seeds such a row from its flags as it would a missing one, keeping what the import put there
// (WithConnectionsOf), since an install records its git connections
// before or after that first start as it pleases.
func (c Config) ConnectionsOnly() bool {
	return c.PollInterval == 0 && c.MaxWorkers == 0 &&
		c.MaxMergers == 0 && c.GitHubHost == "" && c.DeploymentID == ""
}

// WithConnectionsOf is c with the connections and model choices from
// imported: what ConnectionsOnly's row holds, carried onto the seed that
// replaces it.
func (c Config) WithConnectionsOf(imported Config) Config {
	c.AgentBackends = imported.AgentBackends
	c.GitConnections = imported.GitConnections
	c.GitCredential = imported.GitCredential
	c.AgentBackendID = imported.AgentBackendID
	c.ModelSettings = imported.ModelSettings
	if imported.DefaultModel != "" {
		c.DefaultModel = imported.DefaultModel
	}
	if imported.ConsoleSessions != nil {
		c.ConsoleSessions = imported.ConsoleSessions
	}
	return c
}

// EffectiveDefaultModel resolves the active default model from Config:
// the first model in the deployment's own DefaultModel list that an
// enabled (then any) connection advertises, else the first entry of
// DefaultModel, else the first model an enabled (then any) connection
// advertises. It is "" when none of those names one -- the provider
// default -- and never a model name grain made up.
func (c Config) EffectiveDefaultModel() string {
	if defaults := c.DefaultModels(); len(defaults) > 0 {
		for _, m := range defaults {
			for _, b := range c.AgentBackends {
				if !b.Disabled && b.SupportsModel(m) {
					return m
				}
			}
		}
		for _, m := range defaults {
			for _, b := range c.AgentBackends {
				if b.SupportsModel(m) {
					return m
				}
			}
		}
		return defaults[0]
	}
	for _, b := range c.AgentBackends {
		if !b.Disabled {
			models := b.SupportedModels()
			if len(models) > 0 {
				return models[0]
			}
		}
	}
	for _, b := range c.AgentBackends {
		models := b.SupportedModels()
		if len(models) > 0 {
			return models[0]
		}
	}
	return ""
}

const (
	CodexAuthAPIKey        = "api_key"
	CodexAuthSubscription  = "subscription"
	AntigravityAuthAmbient = "ambient"
	ClaudeAuthVertex       = "vertex"
)

func EncodeAgentBackends(v []AgentBackend) (string, error) {
	if len(v) == 0 {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("encode agent backends: %w", err)
	}
	return string(b), nil
}

func DecodeAgentBackends(v string) ([]AgentBackend, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	var out []AgentBackend
	if err := json.Unmarshal([]byte(v), &out); err != nil {
		return nil, fmt.Errorf("decode agent backends: %w", err)
	}
	return out, nil
}

// NormalizeAgentFrameworkName maps the legacy "gemini" spelling onto the
// framework it now names, and leaves everything else -- including "" --
// exactly as it found it. This is the form the per-task override wants
// (model.Task.AgentFramework), where "" is a meaningful value in its own
// right: "no override, use whatever the deployment is set to". Turning
// that into a framework name would silently pin every task ever created
// to one.
func NormalizeAgentFrameworkName(v string) string {
	if v == LegacyAgentFrameworkGemini {
		return AgentFrameworkAntigravity
	}
	return v
}

// NormalizeAgentFramework is NormalizeAgentFrameworkName plus the
// deployment-wide reading of "": there is no "no framework" for a
// deployment, so an unset Config.AgentFramework is AgentFrameworkAntigravity.
// Everything else is returned unchanged, including a value that names
// nothing -- the caller (ui.UpdateSettings, or daemon.go's own flag
// check) is the one to reject that.
//
// Every place that reads Config.AgentFramework goes through one of these
// two, so "which spellings are accepted, and what does empty mean here"
// is answered once rather than re-derived at each site.
func NormalizeAgentFramework(v string) string {
	if v == "" {
		return AgentFrameworkAntigravity
	}
	return NormalizeAgentFrameworkName(v)
}

// AgentFrameworks is the vocabulary above as a list, in the order a
// human should be offered it: the deployment default first.
//
// It exists because "which names are there" was written out by hand at
// every place that validates one -- ui.UpdateSettings, ui's own per-task
// check, cmd/grain/daemon.go's flag guard and its dispatch switch -- each
// as a pair of constants and a two-slot error message. That was already
// four copies to keep in step while there were two frameworks; adding a
// third is exactly the change that would have left one of them quietly
// rejecting a framework the others accept.
func AgentFrameworks() []string {
	return []string{AgentFrameworkAntigravity, AgentFrameworkClaude, AgentFrameworkCodex, AgentFrameworkCustom}
}

// ValidAgentFramework reports whether v names a framework grain can
// actually dispatch onto, after NormalizeAgentFrameworkName has folded
// in the legacy spelling. "" is not one: a caller that means "no
// override, use the deployment's own" has to say so itself, since the
// two readings differ by caller (Config.AgentFramework's zero value is a
// framework, Task.AgentFramework's is not).
func ValidAgentFramework(v string) bool {
	v = NormalizeAgentFrameworkName(v)
	for _, name := range AgentFrameworks() {
		if v == name {
			return true
		}
	}
	return false
}

// AgentFrameworkNames renders the vocabulary for an error message a
// human reads -- `"antigravity", "claude" or "codex"` -- so the sentence
// that rejects a bad value cannot name a shorter list than the check
// that rejected it.
func AgentFrameworkNames() string {
	names := AgentFrameworks()
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	if len(quoted) == 1 {
		return quoted[0]
	}
	return strings.Join(quoted[:len(quoted)-1], ", ") + " or " + quoted[len(quoted)-1]
}

// EncodeMCPServers is grain_config.mcp_servers' text: the empty string
// for no servers, otherwise the JSON object keyed by capability_id
// (encoding/json sorts the keys, so an export diffs stably).
func EncodeMCPServers(servers map[string]protocol.ServerConfig) (string, error) {
	if len(servers) == 0 {
		return "", nil
	}
	data, err := json.Marshal(servers)
	if err != nil {
		return "", fmt.Errorf("encoding mcp_servers: %w", err)
	}
	return string(data), nil
}

// DecodeMCPServers reads grain_config.mcp_servers back. It refuses text
// that is not a JSON object of entries and nothing more: whether each
// entry is a valid capability server is protocol.ServerConfig.Validate's
// question, asked per entry when it is attached (Config.MCPServers).
func DecodeMCPServers(text string) (map[string]protocol.ServerConfig, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var out map[string]protocol.ServerConfig
	if err := json.NewDecoder(bytes.NewReader([]byte(text))).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding mcp_servers: %w", err)
	}
	return out, nil
}
