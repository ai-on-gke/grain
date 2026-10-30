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
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// SandboxSnapshot is one live sandbox's status, as GET /api/sandboxes
// reports it -- one per run currently in flight, so an idle deployment
// reports none rather than one row per slot, most of them idle.
// Deliberately its own type
// rather than orchestrator.SandboxHealth itself: this package does not import
// pkg/orchestrator (a presentation-layer package importing core dispatch
// logic runs the wrong way), so pkg/cli/daemon.go's own
// sandboxHealthAdapter is the one place both types are ever in scope,
// converting one into the other field for field.
type SandboxSnapshot struct {
	Sandbox       string `json:"sandbox"`
	Kind          string `json:"kind"`
	Backend       string `json:"backend"`
	Name          string `json:"name"`
	CPUs          int    `json:"cpus,omitempty"`
	MemoryMB      int    `json:"memoryMB,omitempty"`
	DiskGB        int    `json:"diskGB,omitempty"`
	NetworkAccess string `json:"networkAccess,omitempty"`
	Ready         bool   `json:"ready"`
	Error         string `json:"error,omitempty"`
	// TerminationReason is "running" for a live sandbox, or the backend's
	// failure detail when it can no longer be reached.
	TerminationReason string `json:"terminationReason"`
	LoadAverage       string `json:"loadAverage,omitempty"`
	MemoryUsedMB      int    `json:"memoryUsedMB,omitempty"`
	MemoryTotalMB     int    `json:"memoryTotalMB,omitempty"`
	SwapUsedMB        int    `json:"swapUsedMB,omitempty"`
	SwapTotalMB       int    `json:"swapTotalMB,omitempty"`
	DiskUsedMB        int    `json:"diskUsedMB,omitempty"`
	DiskTotalMB       int    `json:"diskTotalMB,omitempty"`
	// ProbeMS is how long the probe behind this row took, in
	// milliseconds -- orchestrator.SandboxHealth.ProbeMS, carried through
	// for the reason that field's own comment gives: an Error on its own
	// cannot tell a guest that is down from one that is merely slow, and
	// the duration can. 0 for a backend that runs no probe at all
	// (HostSandboxes), which the pane shows by saying nothing about the
	// probe on that row rather than by printing "0ms".
	//
	// The pane reads it in the Status cell: always beside the error chip,
	// where the difference between a probe that was refused in 40 ms and
	// one that spent the whole budget unanswered is the difference
	// between a guest that is gone and a guest that is buried, and on a
	// healthy row only once it crosses the pane's own slow threshold --
	// so a healthy deployment's rows do not all grow a number that is
	// always "40ms" (grain/task-390).
	ProbeMS int64 `json:"probeMS,omitempty"`
	// ProbeAgeMS is how much older than the sweep this row is, in
	// milliseconds -- orchestrator.SandboxHealth.ProbeAgeMS. 0 for a row
	// whose probe ran in this sweep, which is every row except one a sick
	// guest's breaker answered from memory.
	//
	// SandboxReading.AgeMS below says how old the whole response is; this
	// says how old one row inside a response of any age is, which that
	// cannot -- a remembered row is what makes a sweep return quickly, so
	// it arrives in a sweep marked fresh. The pane says "as of 12s ago"
	// beside such a row, because the alternative is an error chip and a
	// duration that both look like they were measured just now and will
	// not change for up to fifteen seconds after the guest recovers.
	ProbeAgeMS int64 `json:"probeAgeMS,omitempty"`
	// NestedVirt is whether this sandbox can run virtual machines of its
	// own -- orchestrator.SandboxHealth.NestedVirt's own four states
	// ("ready", "denied", "no-device", "unsupported"), carried through
	// as the string it is rather than reduced to a boolean, since the
	// three ways of not being ready each want a different fix. Empty
	// when the backend gave no reading, which the pane shows as a dash.
	NestedVirt string `json:"nestedVirt,omitempty"`
	// StartedAt is when the backend acquired this sandbox --
	// orchestrator.SandboxHealth.StartedAt. Nil when the backend did not
	// say, which the pane shows as a dash. The pane reads it for the
	// "Running for" cell, and to name the longest-running CI sandbox in
	// the summary row the CI rows fold behind.
	StartedAt *time.Time `json:"startedAt,omitempty"`
}

// SandboxReading says when the sandbox rows in this response were
// actually taken. Every row in one response comes from the same sweep --
// Config.Sandboxes.Health probes every live guest in one call and
// answers for all of them at once -- so this is a fact about the
// response and not about any one row, and a per-row "stale" marker would
// be the same marker repeated on every row (grain/task-262).
//
// It exists because a response is no longer always a reading taken just
// now. A sweep costs one guest round trip when every guest answers, but
// a guest that has stopped answering spends orchestrator's whole
// 20-second healthTimeout failing to, and every caller shares that one
// sweep (sandboxHealthTTL). Rather than hold the pane blank for 20
// seconds each refresh -- while one sandbox is sick, which is exactly
// what the pane is for -- a caller is handed the last completed sweep
// and told how old it is, with the next sweep already running behind it.
type SandboxReading struct {
	// AgeMS is how long ago the sweep these rows came from finished, in
	// milliseconds. Under a second on a healthy deployment, where the
	// sweep a caller starts answers before it gives up waiting for it.
	AgeMS int64 `json:"ageMS"`
	// Stale is whether these rows were served from the previous sweep
	// because the current one had not finished in time -- i.e. whether
	// the pane should say "as of 8s ago" rather than showing the numbers
	// as current. A reading older than sandboxHealthMaxAge is not served
	// at all, so a Stale response is old by seconds, not by minutes.
	Stale bool `json:"stale"`
}

// SandboxImage is which container image this deployment's sandboxes are
// really created from, beside the one this build of grain was stamped to
// go with -- GET /api/sandboxes' own "sandboxImage" object, reported in
// the host section because it is a fact about this deployment rather
// than about any one live sandbox.
//
// It exists because those two can differ and nothing said so. A granule
// deployment runs grain and a guest built from the same commit
// (pkg/cli/sandboximage.go), and an upgrade keeps them together by
// pulling the guest the new build names -- unless the host's own unit
// pins one in -granule-create-arg, which wins. Then every upgrade pulls a
// guest it never runs, the daemon and its sandboxes drift a commit or
// more apart, and the only symptom is whatever the older guest lacks. The
// daemon warns about it at every start, but a startup log line is not
// where anybody looks when a sandbox behaves oddly weeks later, which is
// what this pane is for (grain/task-306).
type SandboxImage struct {
	// Image is the reference every sandbox is actually created from.
	Image string `json:"image"`
	// Expected is what this build of grain is stamped for
	// (pkg/cli/sandboximage.go's defaultSandboxImage). Equal to Image
	// on a deployment that pins nothing, which is the whole point of the
	// stamp.
	Expected string `json:"expected"`
	// Pinned is whether Image came from this deployment's own
	// -granule-create-arg rather than from the stamp -- which is what
	// makes a difference between the two something an operator has to go
	// and change in the host's systemd unit, rather than something the
	// next upgrade fixes on its own.
	Pinned bool `json:"pinned"`
}

// SandboxHealth is implemented by whatever can report every live
// sandbox's current status -- pkg/cli/daemon.go's own
// sandboxHealthAdapter over orchestrator.GranuleSandboxes/HostSandboxes'
// own Health methods, in a real deployment. See Config.Sandboxes' own
// doc comment for the nil-means-unavailable contract this interface's
// zero value (nil) satisfies.
type SandboxHealth interface {
	Health(ctx context.Context) []SandboxSnapshot
	// SandboxCount returns the number of live sandboxes without taking
	// the resource readings Health gathers from each one. The sidebar
	// polls this through GET /api/config, where making a guest connection
	// every three seconds just to count the pool would be needless work.
	SandboxCount() int
}

// DiskUsage is one filesystem's own size and usage, as one entry of
// HostPressure.Disks below.
//
// A deployment has more than one disk worth watching (grain/task-148):
// terraform/gcp gives the store a small volume of its own and puts
// everything a *sandbox* writes -- docker's data root, so the sandbox
// image and every granule VM's qcow2 overlay, plus HostSandboxes' per-run
// checkouts -- on a second, much larger one. That second volume is the
// one a runaway build actually fills while the store's sits near-empty,
// so a single "disk" figure taken from the store's own filesystem reads
// as healthy at exactly the moment the deployment is running out.
type DiskUsage struct {
	// Holds names what of the daemon's own state sits on this filesystem
	// -- "store" (-data-dir), "sandboxes" (-sandbox-dir), "docker"
	// (docker's data root). More than one when two of those turn out to
	// be the same disk, which is the ordinary case on a developer's
	// machine and on any deployment without terraform/gcp's own separate
	// sandbox volume: they are folded into one entry rather than
	// reported as two identical rows (pkg/cli/daemon.go's hostDisks).
	Holds []string `json:"holds"`
	// Path is the path the reading was taken through -- the first of
	// Holds' own paths, since a filesystem answers the same for all of
	// them. Shown so an operator can run `df` against the same thing.
	Path string `json:"path"`
	// UsedMB and TotalMB are the numbers `df` prints for that
	// filesystem. 0/0 means no reading, which is also what Error being
	// set implies: the pane shows a dash rather than a 0 that would read
	// as an empty disk.
	UsedMB  int `json:"usedMB"`
	TotalMB int `json:"totalMB"`
	// FloorMB is how much of this filesystem this deployment keeps free:
	// the disk half of an admission, below which a new CI job is held and
	// an agent dispatch deferred (model.DiskFloor, grain/task-196).
	//
	// It is on the disk row rather than said once for the deployment
	// because it is a figure per volume -- a share of the volume with a
	// minimum and a cap, so the 20 GB store disk and the 443 GB sandbox
	// volume beside it have different floors -- and because this is where
	// it is read: "443 GB, 181 GB used" answers "what is eating my disk"
	// and says nothing at all about how much of the rest is actually
	// available to a job, which is the question an operator clearing
	// space is really asking (grain/task-214).
	//
	// 0 is a deployment that holds nothing back as its volume fills, and
	// a filesystem with no reading to apply a floor to. The pane says
	// which.
	FloorMB int `json:"floorMB,omitempty"`
	// ReclaimMB is how much free space a sweep of this filesystem aims to
	// get back to once it has crossed the floor above -- half as much
	// again as the floor (model.DiskFloor.ReclaimTargetMB,
	// grain/task-213).
	//
	// Here because it is the threshold the pane's quieter level is drawn
	// at, and drawing one at all is what keeps this row from speaking only
	// once it is too late: a volume between this figure and its floor is
	// still admitting every job, and is close enough to the line that the
	// next few GB written stop the deployment. The same two-level
	// vocabulary ui.SandboxCommitment reports its own dimensions in --
	// caution for "worth seeing beside the figures", warning for "runs are
	// suffering for it now".
	//
	// A figure this deployment already acts on rather than a threshold
	// invented for the pane, and resolved in Go for FloorMB's reason: a
	// share of a floor that is itself a capped share of the volume is not
	// arithmetic worth having a second implementation of in JavaScript.
	// 0 wherever FloorMB is.
	ReclaimMB int `json:"reclaimMB,omitempty"`
	// Error is why this one filesystem has no figure -- a path that has
	// stopped answering statfs, which for a disk that was reported a
	// moment ago means a volume that is no longer mounted. Carried per
	// disk rather than failing the whole host reading, the same way
	// HostError below keeps a failing host reading from taking the
	// sandbox list with it: the other disks' figures are unaffected, and
	// "the sandbox volume went away" is the answer this pane exists to
	// give rather than a reason to show nothing.
	Error string `json:"error,omitempty"`
}

// HostPressure is one point-in-time reading of the machine this
// deployment's own daemon process runs on, as GET /api/sandboxes' own
// host section reports it -- see Config.HostStats' own doc comment on why
// this is reported separately from a sandbox's own usage above.
type HostPressure struct {
	LoadAverage1  float64 `json:"loadAverage1"`
	LoadAverage5  float64 `json:"loadAverage5"`
	LoadAverage15 float64 `json:"loadAverage15"`
	// CPUs is how many logical processors that machine has -- the
	// denominator the three load averages above are only meaningful
	// over, since a load of 8 is half an idle 32-core host and a badly
	// starved 4-core one, and the pane exists to answer "is this host
	// starved right now" (sysstat.Snapshot.CPUs, counted from
	// /proc/stat).
	//
	// A count, not a verdict: the pane says the load "of 4 vCPU" and
	// colours the figures that have passed it, and how much
	// overcommitment is right is still the operator's call. Deliberately
	// not the same question SandboxCommitment.HostCPUs asks with the
	// same number, which is what this deployment's *settings* promise
	// the machine sandboxes are built on if every slot fills -- that is
	// a standing commitment and this is a reading, and they are not even
	// always about the same machine (Config.SandboxHostCapacity).
	//
	// 0 means the count is unavailable: a non-Linux host, or a daemon
	// older than this field answering a newer pane. The pane shows the
	// bare load averages then, the way it did before this existed,
	// rather than dividing by zero or claiming a single core.
	CPUs           int     `json:"cpus,omitempty"`
	CPUUtilization float64 `json:"cpuUtilization"`
	MemoryUsedMB   int     `json:"memoryUsedMB"`
	MemoryTotalMB  int     `json:"memoryTotalMB"`
	SwapUsedMB     int     `json:"swapUsedMB"`
	SwapTotalMB    int     `json:"swapTotalMB"`
	// Disks is one entry per filesystem the daemon's own state sits on,
	// in the order the pane shows them: the store's first, then the
	// sandbox root, then docker's data root when it is a filesystem
	// neither of those already covered. It replaces the single
	// disk figure this carried while -data-dir's own volume was assumed
	// to be the one that fills (grain/task-41, then grain/task-148 once
	// terraform/gcp gave sandboxes a volume of their own).
	//
	// Empty when no reading could be taken at all, which is how a
	// non-Linux host reads: the pane shows the load and memory beside it
	// rather than going down over a number it could not get.
	Disks []DiskUsage `json:"disks,omitempty"`
	// NestedVirt and NestedVirtDetail are whether this machine's own KVM
	// will let the VMs it boots run VMs in turn -- sysstat's own
	// "enabled"/"disabled"/"unavailable" and the sysfs reading behind it
	// ("kvm_intel nested=Y").
	//
	// The host half of the same question SandboxSnapshot.NestedVirt
	// answers per sandbox, and shown beside it because neither is much
	// use alone: a sandbox reporting no CPU virtualization flag is
	// either a host with nesting off or a guest configured without it,
	// and this is what tells those apart.
	NestedVirt       string `json:"nestedVirt,omitempty"`
	NestedVirtDetail string `json:"nestedVirtDetail,omitempty"`
}

// DaemonHealth is what the process serving this API can say about its
// own life, as GET /api/sandboxes' own "daemon" object -- reported
// beside the host reading because a restart loop is a fact about this
// deployment that none of the other figures can hold: they are all taken
// by a daemon that is running, and every one of them looks healthy in
// the four seconds between two crashes.
//
// It exists because of grain/task-233. This deployment restarted 52
// times in eight minutes on 2026-09-18 -- entirely down, dispatching
// nothing -- and nothing said so anywhere a person looks; it was found
// two hours later by reading the journal by hand, and the journal had
// been half-buried by the crash's own output by then. "Started 52 times
// in the last 30 minutes" is the one sentence that would have said it,
// and this is where it is said.
//
// Two deployment shapes report it, and they do not have the same figure
// to report (grain/task-367). A systemd deployment counts its unit's
// starts in the journal, over a window; a Kubernetes one has no journal
// and counts the kubelet's restarts of its own pod's container, which
// has no window and goes back to zero every time the pod is replaced.
// StartsScope is how those stay distinguishable: the count travels with
// the words for what it is a count *of*, rather than with a window field
// that only one of the two can fill. The same goes for the advice --
// FlagSource, FlagFix and LogCommand -- because the fix for a flag the
// daemon is ignoring is `scripts/setup.sh` on one shape and an edit and
// an apply on the other, and only the server knows which shape it is.
type DaemonHealth struct {
	// StartedAt is when this process started. The pane turns it into an
	// uptime, which is the form that reads as a restart loop: a
	// deployment that has been up for four seconds at three in the
	// afternoon is a deployment in trouble.
	StartedAt time.Time `json:"startedAt"`
	// Starts is how many times this daemon has started over StartsScope,
	// *including* this one -- so 1 is the ordinary answer for a
	// deployment that started once and stayed up, and anything above it
	// is a restart that happened.
	//
	// Counted at startup rather than per request: it is a question about
	// the recent past, and the only process able to answer it is one
	// that is currently up, which during a loop is a new one every few
	// seconds. 0 means it was not counted at all (see Error).
	Starts int `json:"starts,omitempty"`
	// RequestedStarts is how many of Starts followed a restart this
	// deployment asked for itself -- an upgrade's own restart, the
	// restart or reboot button -- rather than one nobody asked for. Two
	// upgrades inside the window are two starts and no loop (grain task
	// 9), and the pane says which it was.
	RequestedStarts int `json:"requestedStarts,omitempty"`
	// Restarting is the server's verdict on the count: whether any start
	// after the first in StartsScope was one nobody asked for, which is
	// what the pane raises as a restart loop. Not omitted when false,
	// because a pane falls back to "Starts above one" for a daemon too
	// old to send it.
	Restarting bool `json:"restarting"`
	// StartsScope is what Starts is a count of, in the words the pane
	// and the alert say it in: "in the last 30 minutes" where the count
	// came from a unit's journal over WindowMinutes, "since this pod was
	// created" where it came from a kubelet's restartCount, which has no
	// window at all.
	//
	// It travels with the count because the two readings mean different
	// things and the same number means different things in each. An
	// unwindowed 4 is a pod that crash-looped at some point in its life
	// and may be perfectly healthy now; a windowed 4 is a deployment in
	// trouble this minute. Reporting either as the other makes the pane
	// lie, so the pane says which it has.
	StartsScope string `json:"startsScope,omitempty"`
	// WindowMinutes is how far back Starts looked where it looked back a
	// fixed distance, so the pane can say "N times in the last M
	// minutes" without either number being written down twice. Absent on
	// a reading with no window -- the Kubernetes one -- where StartsScope
	// is the whole of what can be said about the span.
	WindowMinutes int `json:"windowMinutes,omitempty"`
	// Error is why Starts is 0 -- no journal to read, no journalctl in
	// this image, a unit name this host does not have, a pod that cannot
	// read its own name out of its environment. Reported rather than
	// swallowed, since "restarted 0 times" and "nobody could tell" are
	// the same figure and very different facts.
	Error string `json:"error,omitempty"`
	// UnknownFlags are the flags this daemon was started with and does
	// not define, which it ignored in order to come up at all
	// (pkg/cli/daemon_unknown_flags.go). Each one is a setting this
	// deployment asks for and is not getting, and the pane says so: a
	// start that survives a unit/image disagreement is better than a
	// crash loop, and is still a deployment running something other than
	// what it was configured to run.
	UnknownFlags []string `json:"unknownFlags,omitempty"`
	// FlagSource is what passed those flags, as the subject of the
	// sentence the pane writes about them: "this host's
	// grain-daemon.service", or "container grain's args in
	// deployment/grain". A pane cannot work this out -- it is the same
	// distinction pkg/upgrade draws between imageFlagFix and
	// KubeConfig.flagFix, and the daemon is the only end that knows
	// which side of it this deployment is on.
	FlagSource string `json:"flagSource,omitempty"`
	// FlagFix is what to do about them here: re-run scripts/setup.sh on
	// the host, or edit the Deployment's args and re-apply. A sentence
	// rather than a shape name, for the same reason pkg/upgrade's two
	// fixes are sentences -- what an operator needs is the next action,
	// and it is not the same action.
	FlagFix string `json:"flagFix,omitempty"`
	// LogCommand shows this daemon's own log, and is where the restart
	// alert points: `journalctl -u grain-daemon.service` on a host,
	// `kubectl -n grain logs deploy/grain` in a cluster. Whatever a
	// crash-looping daemon is failing on is in there, and it is the one
	// place it is, since the UI that would have shown it is down with the
	// daemon.
	LogCommand string `json:"logCommand,omitempty"`
}

// SandboxHealthResponse is GET /api/sandboxes' whole body. Enabled is
// false, with nothing else set, when this deployment's UI has neither
// Config.Sandboxes nor Config.HostStats configured -- the same
// nil-means-unavailable convention logSourcesResponse's own Enabled
// already establishes for the Logs pane the System overlay
// (bwsalmon/agents#536) puts this one alongside.
type SandboxHealthResponse struct {
	Enabled   bool              `json:"enabled"`
	Sandboxes []SandboxSnapshot `json:"sandboxes,omitempty"`
	// SandboxReading is how old those rows are, and whether they came
	// from a sweep that has already been superseded by one still running
	// (grain/task-262). nil on a deployment with no sandbox backend
	// wired, and on a caller that gave up before any sweep answered it --
	// both of which are also an absent Sandboxes.
	//
	// It covers SandboxStorage below as well, which is summed from these
	// same rows and so is exactly as old as they are.
	SandboxReading *SandboxReading `json:"sandboxReading,omitempty"`
	Host           *HostPressure   `json:"host,omitempty"`
	// HostExecutor is Config.HostExecutor (daemon.HostExecutorStatus):
	// for a daemon beside grain hostd, whether hostd answers and which of
	// grain's own allowlist entries it lacks, so the pane says so.
	HostExecutor any `json:"hostExecutor,omitempty"`
	// SandboxImage is which image those sandboxes are built from, and
	// which one this build expects (Config.SandboxImage). Absent on a
	// deployment with no sandbox image to name at all -- a host-directory
	// backend, or a UI not colocated with one -- where the pane shows one
	// line fewer.
	SandboxImage *SandboxImage `json:"sandboxImage,omitempty"`
	// StateRepoSync is Config.StateRepoSyncFailure: set when this
	// deployment's settings have stopped reaching its state repository.
	// Here so read_deployment_health reports it.
	StateRepoSync *StateRepoSyncFailure `json:"stateRepoSync,omitempty"`
	// SandboxGuest is whether anything is still publishing the guest
	// SandboxImage names, and what it costs when nothing is
	// (sandbox_guest.go, grain/task-397).
	//
	// It rides beside the image rather than on the CI pane because the
	// image is what it is about, and because this response is also what
	// read_deployment_health hands an agent: the reading that said "the
	// sandbox image is guest:sha-7746d62" and nothing else is exactly
	// the one that had to say, and could not, that nothing had published
	// a guest for a day.
	SandboxGuest *SandboxGuestPublishing `json:"sandboxGuest,omitempty"`
	// BaseBranchGates is every base branch this deployment's merge queue
	// is merging onto that nothing is judging, or that nothing has judged
	// for longer than it should have been (basebranchgate.go,
	// grain/task-453). Empty on a deployment whose gates are all working,
	// which is most of them most of the time.
	//
	// It rides here for SandboxGuest's reason, and out of the same
	// incident: this response is what read_deployment_health hands an
	// agent and what the one pane an operator opens when something is
	// wrong is drawn from, and "nothing has tested `main` since
	// Saturday" was true here for 33 hours while every surface a person
	// looks at said nothing at all. A switch nobody can see the
	// consequence of is a switch that stays off.
	BaseBranchGates []BaseBranchGate `json:"baseBranchGates,omitempty"`
	// BaseBranchPublishing is every base branch this deployment has
	// stopped publishing from -- merges landing, releases failing, and
	// nothing shipping (basebranchrelease.go, grain/task-511). Empty on a
	// deployment whose releases are working, which is what keeps this
	// pane quiet on the ordinary day.
	//
	// It is the reading beside BaseBranchGates above rather than part of
	// it, because the two findings are about different things: that one
	// is a merge queue merging onto an untested tree, and this one is a
	// repository that merges perfectly well and ships nothing. A failed
	// release deliberately holds nothing since grain/task-461, which
	// removed the only feedback a broken publisher used to have -- the
	// queue stopping -- so this line is the whole of what replaces it.
	BaseBranchPublishing []BaseBranchPublishing `json:"baseBranchPublishing,omitempty"`
	// HostCredentials is this host's credentials having expired, read
	// off the runs that could not start a tool for it
	// (model.EnvironmentOutcome): one warning for the deployment, saying
	// what to run, where each affected task only says it was parked.
	// Absent on the ordinary day, and again from the first run that gets
	// past starting its tools.
	HostCredentials *HostCredentialsWarning `json:"hostCredentials,omitempty"`
	// Daemon is the health of the process answering this request
	// (Config.Daemon). Absent where nothing can speak for it.
	Daemon *DaemonHealth `json:"daemon,omitempty"`
	// HostError carries Config.HostStats' own error (e.g. this process is
	// not running on Linux, see pkg/sysstat's doc comment) without taking
	// the rest of the pane down with it -- a caller with a configured
	// Config.Sandboxes but a failing HostStats reading still gets its
	// sandbox list.
	HostError string `json:"hostError,omitempty"`
	// PackageCache is what the proxy cache holds right now, per
	// ecosystem, and nil on a process that runs none (a UI built without
	// one, `grain demo`).
	//
	// It rides here, rather than on the settings response it used to ride
	// on (grain/task-266), because this is the pane that draws the disk
	// bars it is filling and this is the poll those bars are read by. Two
	// panels on one pane must not disagree about how full the volume is,
	// which is what a second endpoint on its own timer would have them do.
	PackageCache []PackageCacheStats `json:"packageCache,omitempty"`
	// CIHistory is what this deployment's stored CI step output occupies
	// right now, against the bounds that sweep it (cistorage.go,
	// grain/task-290). nil on a UI with no store behind it, and on a
	// measurement that failed -- the Storage section shows one entry
	// fewer rather than costing the pane its disk bars.
	//
	// Here for PackageCache's reason and not a second one: it is another
	// thing filling the volume those bars draw, read from the same
	// moment, and an operator deciding whether to raise the step-output
	// ceiling is looking at the same disk.
	CIHistory *CIHistoryStorage `json:"ciHistory,omitempty"`
	// CIJobCache and CIArtifacts are the two directories grain CI keeps
	// on the volume those bars draw (cistorage.go, grain/task-276), each
	// nil where there is none to measure. They are here rather than on
	// the CI pane for PackageCache's reason and CIHistory's: an operator
	// asking what is filling /var/lib/grain/data is asking one question about
	// one volume, and the answer has to be four figures read from one
	// moment rather than four panes polling separately.
	CIJobCache  *CIJobCacheStorage `json:"ciJobCache,omitempty"`
	CIArtifacts *CIArtifactStorage `json:"ciArtifacts,omitempty"`
	// SandboxStorage is what the live sandboxes themselves have written,
	// which is the largest consumer of that volume by far and the one
	// that had no figure anywhere: this deployment's own went from 70 GB
	// to 181 GB in an hour on 2026-09-17 with about a dozen sandboxes
	// live (model.DiskFloor's own doc comment).
	//
	// nil on a deployment with no sandbox backend wired, which is the
	// same thing an empty Sandboxes means and is said the same way.
	SandboxStorage *SandboxStorage `json:"sandboxStorage,omitempty"`
	// Lifetime is how many sandboxes this deployment has built since it
	// started counting, and how long they lived (sandboxusage.go,
	// grain/task-315). nil on a UI with no store behind it and on a
	// reading that failed.
	//
	// It rides here because this is where the sandboxes are: everything
	// else in this response is what is up *now*, and "what has this cost
	// altogether" was the question that had no home -- an operator
	// looking at three live sandboxes and asking how many there have been
	// was reading log lines for it. This response is also what
	// read_deployment_health hands an agent, so the two get the same
	// answer from the same poll.
	Lifetime *SandboxLifetime `json:"lifetime,omitempty"`
	// StandingSandboxes is every standing sandbox (grain/config's
	// standing_sandbox tables) the daemon is supervising, sorted by name
	// (Config.StandingSandboxes). Absent where nothing supervises them
	// here, and on a deployment running none.
	//
	// It rides here for Lifetime's reason: these are sandboxes too, and
	// this response is what read_deployment_health hands an agent, so a
	// standing service that has been failing to launch since its
	// definition landed is somewhere an operator or an agent can see it.
	StandingSandboxes []StandingSandboxStatus `json:"standingSandboxes,omitempty"`
	// PersistentSandboxes is every persistent sandbox (model.PersistentSandbox)
	// and its state (Config.PersistentSandboxes), for StandingSandboxes'
	// reason: their VMs run beside the dispatch pool rather than in one of
	// its max_concurrent_runs slots, so this is where their resources are
	// accounted for -- to an operator and to read_deployment_health alike.
	PersistentSandboxes []model.PersistentSandboxStatus `json:"persistentSandboxes,omitempty"`
	// Budget is this deployment's sandbox admission budget and what the
	// ledger currently has committed against it across agent, CI, persistent,
	// and standing pools (grain/task-487).
	Budget *SandboxBudgetReport `json:"budget,omitempty"`
	// Overcommit is the auto-overcommit controller's live state when wired
	// (Config.Overcommit), mirrored onto Budget.Overcommit when Budget is
	// present so the Sandboxes health pane and read_deployment_health see the
	// mode, effective ratios, recent adjustments and OOM kills on one poll.
	Overcommit *model.OvercommitState `json:"overcommit,omitempty"`
	// Reconciliation is the latest comparison between the accounted ledger
	// commitments and the live sandboxes reported by the backend, including
	// any discrepancies detected and orphans reaped (grain/task-487).
	Reconciliation *SandboxReconciliationReport `json:"reconciliation,omitempty"`
}

// SandboxBudgetPool is what one pool (agent, ci, persistent, standing) is
// currently holding in the sandbox ledger.
type SandboxBudgetPool struct {
	Pool     string `json:"pool"`
	Count    int    `json:"count"`
	CPUs     int    `json:"cpus"`
	MemoryMB int    `json:"memoryMB"`
}

// SandboxBudgetReport is this deployment's sandbox admission budget and what
// the ledger currently has committed against it, exposed on GET /api/sandboxes
// and read_deployment_health (grain/task-487).
type SandboxBudgetReport struct {
	Known                 bool                   `json:"known"`
	Mode                  string                 `json:"mode,omitempty"`
	HostCPUs              int                    `json:"hostCPUs,omitempty"`
	HostMemoryMB          int                    `json:"hostMemoryMB,omitempty"`
	ReserveCPUs           int                    `json:"reserveCPUs,omitempty"`
	ReserveMemoryMB       int                    `json:"reserveMemoryMB,omitempty"`
	CPUOvercommit         float64                `json:"cpuOvercommit,omitempty"`
	MemoryOvercommit      float64                `json:"memoryOvercommit,omitempty"`
	BudgetCPUs            int                    `json:"budgetCPUs,omitempty"`
	BudgetMemoryMB        int                    `json:"budgetMemoryMB,omitempty"`
	RepairReserveCPUs     int                    `json:"repairReserveCPUs,omitempty"`
	RepairReserveMemoryMB int                    `json:"repairReserveMemoryMB,omitempty"`
	CommittedCPUs         int                    `json:"committedCPUs"`
	CommittedMemoryMB     int                    `json:"committedMemoryMB"`
	CommittedCount        int                    `json:"committedCount"`
	WithinCap             bool                   `json:"withinCap"`
	OverCapByException    bool                   `json:"overCapByException,omitempty"`
	Summary               string                 `json:"summary,omitempty"`
	Pools                 []SandboxBudgetPool    `json:"pools,omitempty"`
	Overcommit            *model.OvercommitState `json:"overcommit,omitempty"`
	// Cluster is a Kubernetes deployment's own reading of its nodes' free
	// capacity, shown beside the budget above because the two can
	// disagree: the budget is arithmetic on allocatable and overcommit,
	// while this is what the scheduler would actually still place, and
	// is what each sandbox pod is admitted against (grain/task-805). nil
	// on every other backend.
	Cluster *SandboxClusterFree `json:"cluster,omitempty"`
}

// SandboxClusterFree is k8s.FreeSummary for the health pane: the
// schedulable nodes' allocatable and free requests in total, and the
// single node with the most free cpu -- the one that decides whether
// one more pod fits, since a pod cannot be split across nodes.
type SandboxClusterFree struct {
	Known               bool       `json:"known"`
	Nodes               int        `json:"nodes"`
	AllocatableCPUs     float64    `json:"allocatableCPUs"`
	AllocatableMemoryMB int        `json:"allocatableMemoryMB"`
	FreeCPUs            float64    `json:"freeCPUs"`
	FreeMemoryMB        int        `json:"freeMemoryMB"`
	LargestFreeNode     string     `json:"largestFreeNode,omitempty"`
	LargestFreeCPUs     float64    `json:"largestFreeCPUs"`
	LargestFreeMemoryMB int        `json:"largestFreeMemoryMB"`
	PendingPods         int        `json:"pendingPods,omitempty"`
	Partial             bool       `json:"partial,omitempty"`
	ReadAt              *time.Time `json:"readAt,omitempty"`
	Error               string     `json:"error,omitempty"`
	Summary             string     `json:"summary,omitempty"`
}

// SandboxReconciliationReport is the latest comparison of accounted ledger
// commitments against the live sandboxes reported by the backend
// (grain/task-487).
type SandboxReconciliationReport struct {
	CheckedAt          *time.Time `json:"checkedAt,omitempty"`
	AccountedCount     int        `json:"accountedCount"`
	LiveCount          int        `json:"liveCount"`
	LiveCPUs           int        `json:"liveCPUs,omitempty"`
	LiveMemoryMB       int        `json:"liveMemoryMB,omitempty"`
	InSync             bool       `json:"inSync"`
	Discrepancies      []string   `json:"discrepancies,omitempty"`
	OrphansReaped      int        `json:"orphansReaped,omitempty"`
	TotalOrphansReaped int        `json:"totalOrphansReaped"`
}

// StandingSandboxStatus is one standing sandbox as its supervisor last
// recorded it. It carries the definition's name and image and nothing
// else of it: never its environment, and never a secret value, which the
// supervisor does not hold past a launch anyway. LastError is passed
// through Config.LogRedactor where one is configured, as the Logs pane's
// lines are, since a launch error can quote what it was launching.
type StandingSandboxStatus struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// Sandbox is the backend sandbox the instance runs as -- not Name,
	// which need not fit a granule VM name -- so an operator can find its
	// VM ("g-" and this) on the host.
	Sandbox string `json:"sandbox,omitempty"`
	// StartedAt is when the current instance launched, or when the first
	// attempt at one failed for a sandbox that has never run.
	StartedAt time.Time `json:"startedAt"`
	// Restarts counts relaunches after a failure; a restart for a changed
	// definition or rotated secret is not one.
	Restarts  int    `json:"restarts"`
	Failed    bool   `json:"failed"`
	LastError string `json:"lastError,omitempty"`
	// Failures is how many launches in a row have failed, and NextAttempt
	// when the supervisor will try again: it backs off rather than
	// retrying a launch that keeps failing on every reconcile.
	Failures    int        `json:"failures,omitempty"`
	NextAttempt *time.Time `json:"nextAttempt,omitempty"`
}

// standingSandboxes asks Config.StandingSandboxes, redacting each
// LastError. A redactor that cannot be built drops the errors rather
// than serving them unredacted.
func (c *Client) standingSandboxes() []StandingSandboxStatus {
	if c.Config.StandingSandboxes == nil {
		return nil
	}
	statuses := c.Config.StandingSandboxes()
	if c.Config.LogRedactor == nil || !slices.ContainsFunc(statuses, func(s StandingSandboxStatus) bool { return s.LastError != "" }) {
		return statuses
	}
	redactor, err := c.Config.LogRedactor()
	for i := range statuses {
		if statuses[i].LastError == "" {
			continue
		}
		if err != nil || redactor == nil {
			statuses[i].LastError = "(withheld: the redaction list could not be read)"
			continue
		}
		statuses[i].LastError, _ = redactor.Line(statuses[i].LastError)
	}
	return statuses
}

// SandboxStorage is what this deployment's live sandboxes are occupying,
// summed out of the same sweep the sandbox table below is drawn from.
//
// Summed from the snapshots rather than measured on the volume, for two
// reasons. It costs nothing: the readings are already taken, and walking
// the sandbox volume to size it would be a directory walk of hundreds of
// gigabytes on the host whose disk pressure is why anybody is looking.
// And it is the honest number: a granule sandbox's disk is a qcow2
// overlay that grows as it is written, and each guest's own `df` is what
// has actually been written into it -- which is the figure the disk floor
// is about, and not the same as what the pool was promised.
//
// The bound that applies to it is the disk floor on the volume, not a
// ceiling of its own: summing every live sandbox's declared disk and
// refusing work when the sum passed the volume would refuse jobs on a
// host with plenty of room (model.DiskFloor). So the pane reads this
// against the floor on the bars above it, and Declared is here to be
// read beside Used rather than as a limit.
type SandboxStorage struct {
	// Sandboxes is how many are live, and Measured how many of them
	// answered with a disk reading. They differ for a guest that is still
	// starting or has stopped answering, and the difference is why Used
	// can fall while the volume does not.
	Sandboxes int `json:"sandboxes"`
	Measured  int `json:"measured"`
	// UsedMB is what those measured guests have written into their own
	// disks, and DeclaredGB what the live pool was promised -- the figure
	// UsedMB grows towards, one overlay at a time, as the runs write.
	UsedMB     int `json:"usedMB"`
	DeclaredGB int `json:"declaredGB,omitempty"`
}

// sandboxStorageOf sums one sweep's worth of snapshots.
//
// A sandbox that reports no total is not measured: 0/0 is how the
// backend spells "no reading", and counting it as an empty disk would
// make a pool of unreachable guests look like a volume with nothing on
// it -- the one reading this section must not produce.
func sandboxStorageOf(sandboxes []SandboxSnapshot) SandboxStorage {
	storage := SandboxStorage{Sandboxes: len(sandboxes)}
	for _, s := range sandboxes {
		storage.DeclaredGB += s.DiskGB
		if s.DiskTotalMB <= 0 {
			continue
		}
		storage.Measured++
		storage.UsedMB += s.DiskUsedMB
	}
	return storage
}

// sandboxHealthTTL is how long one sweep's readings are served to further
// callers before another sweep is run.
//
// A sweep is not a cheap read: Config.Sandboxes.Health runs a command in
// every live guest (orchestrator.GranuleSandboxes.Health does it over
// `docker exec ... granule exec`, one exec per live sandbox, up to
// orchestrator's own 20-second per-sandbox budget each). That cost is per
// *caller*, and callers multiply: this pane polls every 5 seconds, two
// operators with it open are two pollers, and the deployment-health MCP
// tool (pkg/cli/mcpserver.go's daemonDeploymentHealth) is a third, over
// the same HTTP endpoint. They arrive on the host whose overload is
// generally the reason anyone is looking, so the work they multiply is
// the work that machine can least afford (grain/task-223).
//
// Two seconds, against a pane that refreshes every five: every reading
// here is a point-in-time sample of a figure that moves continuously --
// a load average, a memory total, a `df` -- so serving one taken two
// seconds ago is indistinguishable, to an operator, from having taken it
// again. Long enough that several viewers on the same 5-second cadence
// fall into one sweep between them, short enough that a sandbox that has
// just gone unreachable still shows up within one refresh of the pane.
const sandboxHealthTTL = 2 * time.Second

// sandboxHealthSlowSweep is when a sweep is slow enough to say so in the
// daemon's log. It is deliberately the only instrumentation here: the
// question this cache was sized against -- whether a probe that
// orchestrator gives 20 seconds actually gets anywhere near them on a
// loaded deployment, or answers in milliseconds -- had no answer from
// outside, because a sweep that is merely slow is invisible until it
// crosses a timeout and paints an error chip. One line per slow sweep is
// affordable at this TTL (at most one sweep every two seconds, and only
// the slow ones log).
const sandboxHealthSlowSweep = 5 * time.Second

// sandboxHealthWait is how long a caller waits for the sweep it just
// started before answering from the last completed one instead.
//
// This is what keeps a healthy deployment reading exactly as it did
// before stale-while-revalidate existed. A sweep of guests that answer
// costs one guest round trip -- milliseconds, and a good deal less than
// this even on a loaded host -- so the ordinary caller still leaves with
// readings taken just now and nothing to say about their age. Serving
// the previous sweep *immediately*, as a plain stale-while-revalidate
// cache would, would instead mean every response on every deployment
// carried an age of about one poll interval and a stale marker, since
// the pane polls every 5 seconds against a 2-second freshness window:
// the marker would be permanently on and would stop meaning anything.
//
// One second, against a pane that polls every five and a probe budget of
// twenty: long enough that a sweep only fails to make it when a guest is
// genuinely not answering, short enough that a sick deployment costs the
// pane a beat rather than a refresh cycle.
const sandboxHealthWait = time.Second

// sandboxHealthMaxAge is how old the last sweep may be and still be
// served to a caller whose own sweep has not come back.
//
// There has to be a floor under it: a reading that keeps being handed
// out while sweep after sweep fails to complete stops being a delayed
// sample of what is happening and becomes a claim about a deployment
// that has since moved on -- and a load average from a minute ago
// dressed up as a reading is worse than no reading, because the operator
// cannot tell the difference. Sixty seconds is three of orchestrator's
// own 20-second per-sandbox probe budgets: one sweep held to the full
// budget by a dead guest is the case this cache exists for and is served
// from the sweep before it, but three in a row means the probe is not
// coming back at all, and a caller is made to wait for a real answer
// like the very first caller does.
const sandboxHealthMaxAge = time.Minute

// sandboxHealthEntry is one sweep: in flight while ready is open, and the
// readings it produced once ready is closed. takenAt is zero until then,
// and is when the sweep *finished* -- see the assignment in
// startSandboxHealthSweep for why the finish and not the start. startedAt
// is when it began, which is how much of sandboxHealthWait a caller
// arriving mid-sweep has left to spend on it.
type sandboxHealthEntry struct {
	ready     chan struct{}
	sandboxes []SandboxSnapshot
	startedAt time.Time
	takenAt   time.Time
}

// sandboxHealth reports every live sandbox's status, coalescing
// concurrent and closely-spaced callers onto one Config.Sandboxes.Health
// sweep -- the same cache-entry-with-a-ready-channel shape
// ListGitHubBranches uses, for the same reason: the first caller runs the
// work, everyone who arrives while it is running shares it rather than
// starting their own, and everyone who arrives within sandboxHealthTTL of
// it finishing is handed it as it stands.
//
// What coalescing alone does not fix is latency, which is what the
// second reading here is for. One caller's sweep is now every caller's
// sweep, so a single guest that has stopped answering -- twenty seconds
// of orchestrator's healthTimeout spent retrying, against milliseconds
// for a guest that answers -- held the pane blank for twenty seconds on
// every refresh, in exactly the state the pane exists to show
// (grain/task-262). So a caller that has waited sandboxHealthWait for
// the sweep it started gives up on it and is answered from the last
// completed sweep instead, marked with its age (SandboxReading), while
// that sweep goes on running for whoever asks next. A caller with
// nothing cached at all, or with a reading older than
// sandboxHealthMaxAge, still waits for a real answer.
//
// The sweep itself runs on a context detached from the caller's
// (context.WithoutCancel) and in a goroutine, because its result is
// shared: a browser tab closed mid-poll would otherwise cancel the sweep
// that two other viewers and the MCP tool are waiting on, and turn a
// perfectly healthy deployment into a pane full of cancelled-probe
// errors. Nothing is unbounded by that -- each backend bounds its own
// probes (orchestrator's healthTimeout) -- and a caller that does go away
// stops waiting immediately, leaving the sweep to fill the entry for
// whoever asks next.
func (c *Client) sandboxHealth(ctx context.Context) ([]SandboxSnapshot, *SandboxReading) {
	health := c.Config.Sandboxes
	if health == nil {
		return nil, nil
	}

	c.sandboxHealthMu.Lock()
	last := c.sandboxHealthLast
	if last != nil && c.now().Sub(last.takenAt) < sandboxHealthTTL {
		c.sandboxHealthMu.Unlock()
		return c.readSandboxHealth(last, false)
	}
	sweep := c.sandboxHealthSweep
	if sweep == nil {
		sweep = c.startSandboxHealthSweep(ctx, health)
	}
	// What is left of the wait, rather than the whole of it: the sweep
	// may have been running for some time already while an earlier
	// caller waited on it, and a sick deployment polled every five
	// seconds should not be charged the full wait again on each poll for
	// the same sweep it has already waited on once.
	wait := sandboxHealthWait - c.now().Sub(sweep.startedAt)
	c.sandboxHealthMu.Unlock()

	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-sweep.ready:
			return c.readSandboxHealth(sweep, false)
		case <-ctx.Done():
			// Nobody left to answer: the sweep goes on to fill the entry
			// for the next caller regardless.
			return nil, nil
		case <-timer.C:
		}
	}

	if last != nil && c.now().Sub(last.takenAt) <= sandboxHealthMaxAge {
		return c.readSandboxHealth(last, true)
	}
	// Nothing recent enough to hand over as a reading, so this caller
	// waits the sweep out the way the very first one does.
	select {
	case <-sweep.ready:
		return c.readSandboxHealth(sweep, false)
	case <-ctx.Done():
		return nil, nil
	}
}

// startSandboxHealthSweep runs one Config.Sandboxes.Health sweep in the
// background and returns the entry it will fill. The caller must hold
// sandboxHealthMu: it is what makes "is one already running" and "start
// one" a single decision, so two callers arriving together cannot put two
// sweeps onto the same guests.
func (c *Client) startSandboxHealthSweep(ctx context.Context, health SandboxHealth) *sandboxHealthEntry {
	entry := &sandboxHealthEntry{ready: make(chan struct{}), startedAt: c.now()}
	c.sandboxHealthSweep = entry

	sweepCtx := context.WithoutCancel(ctx)
	go func() {
		started := time.Now()
		sandboxes := health.Health(sweepCtx)
		if elapsed := time.Since(started); elapsed >= sandboxHealthSlowSweep {
			log.Printf("ui: probing %d sandbox(es) for the health pane took %s", len(sandboxes), elapsed.Round(time.Millisecond))
		}
		c.sandboxHealthMu.Lock()
		// Dated from when the sweep *finished*, not from when it
		// started: a sweep that took ten seconds has readings ten
		// seconds newer than its own start, and dating them from the
		// start would age them out on arrival and put the next caller
		// straight into another ten-second sweep.
		entry.sandboxes, entry.takenAt = sandboxes, c.now()
		c.sandboxHealthLast, c.sandboxHealthSweep = entry, nil
		close(entry.ready)
		c.sandboxHealthMu.Unlock()
	}()
	return entry
}

// readSandboxHealth hands back one completed sweep's readings and says
// how old they are. stale is whether they are being served in place of a
// sweep that is still running, which is the one thing the age alone does
// not say: a fresh answer from a slow sweep and a stale answer from a
// fast one can carry the same number of milliseconds.
//
// The copy is what lets several callers be served the same entry: each
// marshals its own slice, and none of them can reach the one the cache is
// still holding.
func (c *Client) readSandboxHealth(entry *sandboxHealthEntry, stale bool) ([]SandboxSnapshot, *SandboxReading) {
	// A clock that has gone backwards between the sweep and now would
	// otherwise report a reading from the future, which reads as a bug in
	// the pane rather than as one in the clock.
	age := max(c.now().Sub(entry.takenAt), 0)
	return append([]SandboxSnapshot(nil), entry.sandboxes...), &SandboxReading{AgeMS: age.Milliseconds(), Stale: stale}
}

func (s *Server) handleGetSandboxHealth(w http.ResponseWriter, r *http.Request) {
	// Config.Daemon counts here too: a deployment whose sandbox pool and
	// host readings are both unavailable can still be one that has
	// restarted forty times this hour, and that is the pane's most
	// important line, not its least.
	var syncFailure *StateRepoSyncFailure
	if s.tasks.Config.StateRepoSyncFailure != nil {
		syncFailure = s.tasks.Config.StateRepoSyncFailure()
	}
	// Read before the early return below for the same reason as the sync
	// failure: it is about the host, not about the sandbox pool, and a
	// deployment with no pool to report still has to say it.
	hostCredentials := s.tasks.hostCredentialsWarning(r.Context())
	if s.tasks.Config.Sandboxes == nil && s.tasks.Config.HostStats == nil && s.tasks.Config.Daemon == nil && s.tasks.Config.StandingSandboxes == nil && s.tasks.Config.PersistentSandboxes == nil && s.tasks.Config.Overcommit == nil {
		writeJSON(w, http.StatusOK, SandboxHealthResponse{Enabled: false, StateRepoSync: syncFailure, HostCredentials: hostCredentials})
		return
	}
	resp := SandboxHealthResponse{
		Enabled:              true,
		StateRepoSync:        syncFailure,
		HostCredentials:      hostCredentials,
		SandboxImage:         s.tasks.sandboxImage(),
		SandboxGuest:         s.tasks.sandboxGuestPublishing(r.Context()),
		BaseBranchGates:      s.tasks.baseBranchGates(r.Context()),
		BaseBranchPublishing: s.tasks.baseBranchPublishing(r.Context()),
		PackageCache:         s.tasks.packageCacheStats(),
		CIHistory:            s.tasks.ciHistoryStorage(r.Context()),
		Lifetime:             s.tasks.sandboxLifetime(r.Context()),
		StandingSandboxes:    s.tasks.standingSandboxes(),
	}
	if ps := s.tasks.Config.PersistentSandboxes; ps != nil {
		if list, err := ps.List(r.Context()); err == nil {
			resp.PersistentSandboxes = list
		}
	}
	resp.CIJobCache, resp.CIArtifacts = s.tasks.ciDiskUsage(r.Context())
	if s.tasks.Config.HostExecutor != nil {
		resp.HostExecutor = s.tasks.Config.HostExecutor(r.Context())
	}
	if s.tasks.Config.Daemon != nil {
		daemon := s.tasks.Config.Daemon()
		resp.Daemon = &daemon
	}
	if s.tasks.Config.Sandboxes != nil {
		resp.Sandboxes, resp.SandboxReading = s.tasks.sandboxHealth(r.Context())
		// From the same sweep the table below is drawn from, so that the
		// total and the rows it is the total of cannot disagree.
		storage := sandboxStorageOf(resp.Sandboxes)
		resp.SandboxStorage = &storage
		if reporter, ok := s.tasks.Config.Sandboxes.(interface {
			BudgetAndReconciliation() (*SandboxBudgetReport, *SandboxReconciliationReport)
		}); ok {
			resp.Budget, resp.Reconciliation = reporter.BudgetAndReconciliation()
		}
	}
	if s.tasks.Config.Overcommit != nil {
		st := s.tasks.Config.Overcommit()
		resp.Overcommit = &st
	}
	if resp.Budget != nil {
		if resp.Overcommit != nil {
			resp.Budget.Mode = resp.Overcommit.Mode
			resp.Budget.Overcommit = resp.Overcommit
			if resp.Overcommit.Mode == model.OvercommitAuto {
				if resp.Overcommit.CPUOvercommit > 0 {
					resp.Budget.CPUOvercommit = resp.Overcommit.CPUOvercommit
				}
				if resp.Overcommit.MemoryOvercommit > 0 {
					resp.Budget.MemoryOvercommit = resp.Overcommit.MemoryOvercommit
				}
			}
		}
		if resp.Budget.Mode == "" && s.tasks.Store != nil {
			if cfg, err := s.tasks.Store.GetConfig(r.Context()); err == nil {
				resp.Budget.Mode = cfg.SandboxOvercommit.Resolved().Mode
			}
		}
		if resp.Budget.Mode == "" {
			resp.Budget.Mode = model.OvercommitFixed
		}
	}
	if s.tasks.Config.HostStats != nil {
		if host, err := s.tasks.Config.HostStats(); err != nil {
			resp.HostError = err.Error()
		} else {
			resp.Host = &host
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
