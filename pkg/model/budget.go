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

import "fmt"

// Commitment is how much of a machine one sandbox takes out of it while
// it exists: the two dimensions of a sandbox shape that are really
// shared, in the units the shape itself is written in.
//
// Disk is here as the size a sandbox's disk is promised (DiskGB), not as
// what it occupies: a sandbox VM's disk is an overlay that grows as it is
// written rather than a slice taken out of the host when the VM is
// created, so the sum is held against the volume times
// Budget.DiskOvercommit -- a ratio well above one -- rather than against
// the volume itself, which would refuse work on a host with plenty of
// room. What actually fills that volume is what the runs write, and
// bounding *that* is a floor under the free space -- Budget.DiskFloor,
// which grain/task-173 filed separately and grain/task-196 built.
type Commitment struct {
	// CPUs is vCPU, the same number a sandbox shape asks for.
	CPUs int
	// MemoryMB is MiB, likewise.
	MemoryMB int
	// DiskGB is GiB of sandbox disk, likewise. Zero for a shape whose
	// disk nobody sized, which commits none.
	DiskGB int
}

// IsZero reports whether this commits nothing at all -- what a job that
// named no shape carries, and what a caller with no shape to charge
// passes.
func (c Commitment) IsZero() bool { return c.CPUs == 0 && c.MemoryMB == 0 && c.DiskGB == 0 }

// Plus is the two commitments held at once.
func (c Commitment) Plus(o Commitment) Commitment {
	return Commitment{CPUs: c.CPUs + o.CPUs, MemoryMB: c.MemoryMB + o.MemoryMB, DiskGB: c.DiskGB + o.DiskGB}
}

// Minus is c with o given back, floored at nothing: a ledger that has
// released more than it took is a bug, and reporting negative capacity
// to the admission arithmetic would turn it into an overcommitment.
func (c Commitment) Minus(o Commitment) Commitment {
	return Commitment{CPUs: max(c.CPUs-o.CPUs, 0), MemoryMB: max(c.MemoryMB-o.MemoryMB, 0), DiskGB: max(c.DiskGB-o.DiskGB, 0)}
}

// Times is n sandboxes of this shape.
func (c Commitment) Times(n int) Commitment {
	if n <= 0 {
		return Commitment{}
	}
	return Commitment{CPUs: c.CPUs * n, MemoryMB: c.MemoryMB * n, DiskGB: c.DiskGB * n}
}

// String is the shape as an operator sizing a host reads it. Disk is
// named only when the shape has some, so a shape nobody sized a disk for
// reads as it always has.
func (c Commitment) String() string {
	if c.DiskGB > 0 {
		return fmt.Sprintf("%d vCPU / %s GiB / %d GiB disk", c.CPUs, gib(c.MemoryMB), c.DiskGB)
	}
	return fmt.Sprintf("%d vCPU / %s GiB", c.CPUs, gib(c.MemoryMB))
}

// Budget is how much machine this deployment is willing to commit to
// sandboxes at once -- the total that was missing when every limit grain
// schedules by was a *count* (grain/task-173).
//
// The failure that produced it: max_ci_jobs 15 and max_workers 3 are
// both defensible numbers, and neither knows what a sandbox is made of.
// CI jobs on this repository ask for 16 GiB each and agent sandboxes for
// 8, so a host with a dozen sandboxes on it had committed about 136 GiB
// of a 128 GiB machine, was 12 GiB into swap, and was serving the UI
// from what was left. Nothing was wrong with either limit; what was
// missing was a total to hold the shapes against.
//
// The arithmetic is deliberately small enough to say in a sentence:
//
//	memory budget = (host memory - reserve) x memory overcommit
//	cpu budget    = (host cpus - reserve)   x cpu overcommit
//
// Reserve is what is kept back for everything that is not a sandbox: the
// daemon, the container runtime, each VM's own qemu overhead, and the
// page cache all of them read through. It is subtracted before the ratio
// rather than after, so raising the overcommit ratio never eats into it
// -- a deployment that swaps has already lost more throughput than the
// extra job was worth.
//
// Overcommit is the operator's own answer to "how much more than the
// machine has are we willing to promise". The two dimensions get
// separate ratios because they fail differently, which is the same
// distinction ui.SandboxCommitment's thresholds are drawn on:
// contention for a core costs a run time, while memory a host does not
// have costs it the run, or costs the host its responsiveness once the
// kernel starts swapping to find it.
//
// The zero value bounds nothing, the same contract Limits' zero value
// has: a dimension with no host figure, or with a ratio of zero, is not
// enforced at all rather than admitting nothing. A budget that refused
// every sandbox because nobody had measured the host would be a worse
// failure than the one this exists to prevent.
type Budget struct {
	// Host is what the machine actually has -- sysstat.Snapshot's own
	// CPUs and MemTotalMB, which is where cmd/grain reads it.
	Host Commitment
	// ReserveCPUs is kept back from Host.CPUs before anything is committed
	// to sandboxes, reserving cores for the grain controller daemon so it
	// stays responsive under load.
	ReserveCPUs int
	// ReserveMemoryMB is kept back from Host.MemoryMB before anything is
	// committed. DefaultReserveMemoryMB is what cmd/grain puts here when
	// an operator has not chosen.
	ReserveMemoryMB int
	// CPUOvercommit and MemoryOvercommit are how much more than the host
	// has this deployment will promise, 1 being "exactly what it has".
	// Zero or less leaves that dimension unenforced.
	CPUOvercommit    float64
	MemoryOvercommit float64
	// DiskOvercommit is the same ratio for sandbox disk: the sum of every
	// live sandbox's DiskGB is held against (disk - floor) x this, where
	// disk is Host.DiskGB when the backend reported one (a cluster's
	// allocatable ephemeral storage) and otherwise the live reading of the
	// volume in Disk. Zero or less leaves the sum unenforced; the floor
	// below still holds either way.
	DiskOvercommit float64
	// There is deliberately nothing here for a reservation. A budget used
	// to carry what another pool had already taken out of it (Held, and
	// the words a refusal said about it), because the agent pool held one
	// standing claim on the machine for every slot it was configured for
	// and CI was admitted against whatever was left. On the night of
	// grain/task-506 that was half a host reserved for six agent
	// sandboxes sitting at load 0.00, and every queued CI job was refused
	// against it. The claim is gone rather than better worded: what is
	// charged now is the shape of a sandbox that exists, both pools share
	// one total, and the total is held against the live ledger
	// (Ledger, grain/task-28). A configured maximum is never added into
	// this budget or taken out of it.
	//
	// DiskFloor is the third dimension, and the one that is not a sum at
	// all: how much of the volume this deployment's sandboxes are written
	// to is kept free (grain/task-196). See DiskFloor for why disk is a
	// floor under what is left rather than a ceiling over what is
	// promised.
	DiskFloor DiskFloor
	// Disk is that volume as it was read for this admission -- a live
	// reading, unlike Host above, because what fills a disk is what the
	// runs write rather than anything a setting says. The zero value is
	// "not read", and leaves disk unbounded.
	//
	// The agent pool and CI do not get a share of the free space each:
	// they are both held by the same floor on the same volume. Two pools
	// writing to one filesystem is not an overcommitment to divide up, it
	// is one number both of them have to respect.
	Disk DiskSpace
	// ClusterFit, on a Kubernetes deployment, is the cluster's own answer
	// to the question this budget answers from arithmetic: whether a pod
	// of the asked-for shape would actually be scheduled now, on some
	// node with room for its requests (orchestrator's k8s backends,
	// k8s.FitChecker). A budget sized from the nodes' allocatable times an
	// overcommit ratio can promise more than the nodes have free once
	// system pods, other workloads and the gap between capacity and
	// allocatable are taken out, and every pod past that point sat
	// Pending (grain/task-805). nil -- every other backend -- asks nothing.
	//
	// Consulted only on an admission the arithmetic has already made, so
	// it can only hold work back, never admit what the budget refuses.
	ClusterFit ClusterFit
	// RepairReserve is the slice of the memory and vCPU budget that only
	// a base-branch repair may spend (grain/task-488): a task marked
	// fixes_base_branch, and the CI it runs to show the branch green
	// again. Admits keeps it clear for everything else and AdmitsRepair
	// does not, so a deployment saturated by ordinary work still has room
	// for the one run that unblocks it.
	//
	// The failure it closes: a red main holds a repository's whole merge
	// queue, and its repair waited more than three hours at the front of
	// the backlog behind dozens of runs and jobs that were themselves
	// waiting on that red main. Order decides who is dispatched first, not
	// whether anything fits.
	//
	// The zero value reserves nothing, which is what a deployment that
	// sets -repair-reserve-sandboxes=0 asks for. The reserve is part of
	// the budget rather than added to it: an empty reserve is idle
	// headroom, not overcommitment.
	RepairReserve Commitment
}

// DefaultRepairReserveSandboxes is how many standard agent sandboxes'
// worth of the budget is kept for base-branch repairs (RepairReserve)
// when a deployment has chosen nothing: one, which is enough for the one
// repair per base branch that may use it at a time and costs a
// deployment that never goes red one sandbox of idle headroom.
const DefaultRepairReserveSandboxes = 1

// DefaultCPUOvercommit is how much more vCPU than the host has a
// deployment promises by default.
//
// Two, because some CPU overcommitment is right rather than merely
// tolerated: an agent run spends most of its wall time waiting on a
// model rather than on a core, and a CI job spends a good deal of its
// own waiting on a network. It is also where ui.SandboxCommitment's own
// caution threshold starts, drawn from a deployment's measured run
// history -- so the point this stops admitting work is the point the
// panes have been calling worth looking at all along.
const DefaultCPUOvercommit = 2.0

// DefaultMemoryOvercommit is one: memory is promised at most once.
//
// Not because a Linux host cannot overcommit memory -- it does it by
// default -- but because the machines here are VMs whose memory is
// touched rather than merely requested, and the way a host finds memory
// it has already promised is to swap. The observed cost of doing that is
// the whole of grain/task-173: 12 GiB of swap in use, a load average at
// the core count, and an operator reporting that typing into the UI had
// become slow.
const DefaultMemoryOvercommit = 1.0

// DefaultDiskOvercommit is two: a sandbox's disk may be promised twice
// over the volume (less its floor) by default.
//
// More than one because a sandbox disk is a thin-provisioned overlay: a
// 40 GiB disk on which a run writes 1-3 GiB takes 1-3 GiB out of the
// volume, and promising each one in full would refuse work on a host
// with plenty of room -- on Kubernetes, where the ratio also scales each
// pod's ephemeral-storage request, it is what kept a third 40 GiB
// sandbox off a node with 100 GiB to give. Not more than two by
// default, because what does fill the volume is what nobody bounded in
// advance, and the floor (DiskFloor) is then the only thing between the
// runs and a full disk.
const DefaultDiskOvercommit = 2.0

// MinReserveCPUs is how many cores are kept back for the host and the
// grain controller daemon by default, so the controller stays responsive
// even when the system is heavily loaded with sandboxes.
const MinReserveCPUs = 2

// DefaultReserveCPUs is the CPU reserve for a host of this size.
func DefaultReserveCPUs(hostCPUs int) int {
	if hostCPUs <= 0 {
		return 0
	}
	return MinReserveCPUs
}

// DefaultReserveShare and MinReserveMemoryMB are the default memory reserve:
// an eighth of the machine, and never less than 8 GiB (to ensure the grain
// controller stays responsive even under heavy load) -- except that it is
// never more than MaxReserveShare of the machine either, so a host smaller
// than 16 GiB still has sandbox memory to give.
//
// A share rather than a constant because what is not a sandbox scales
// with the machine -- more sandboxes mean more qemu processes, more
// overlay writeback and more page cache to read the guests' images
// through -- and 4 GiB held back from a 128 GiB host would be the same
// mistake in smaller print. On the host grain/task-173 was measured on
// it comes to 16 GiB, leaving 112 GiB of the 128 to commit, against the
// 136 GiB that deployment had actually promised.
const (
	DefaultReserveShare = 0.125
	MinReserveMemoryMB  = 8192
)

// MaxReserveShare caps the default reserve at half the machine. Without
// it the 8 GiB floor was the whole of a 7.8 GiB host: a budget of
// nothing, which admits no run at all, and every approved task stayed
// queued on a deployment that otherwise looked healthy (task 1940).
const MaxReserveShare = 0.5

// DefaultReserveMemoryMB is the reserve for a host of this size.
func DefaultReserveMemoryMB(hostMemoryMB int) int {
	if hostMemoryMB <= 0 {
		return 0
	}
	reserve := max(int(float64(hostMemoryMB)*DefaultReserveShare), MinReserveMemoryMB)
	return min(reserve, int(float64(hostMemoryMB)*MaxReserveShare))
}

// ReserveMemoryDefault is what Config.SandboxMemoryReserveMB holds when
// nobody has chosen a reserve: the share above, resolved against
// whatever host the daemon turns out to be on.
//
// Negative rather than zero because zero is a choice somebody can make
// -- a host that keeps nothing back for itself -- and a setting whose
// "unset" and whose tightest legal value are the same number is one an
// operator cannot get back out of.
const ReserveMemoryDefault = -1

// NewBudget is the budget those settings and one host reading make
// together: what the machine has, less the reserve, times whatever
// overcommitment this deployment has chosen.
//
// The one place a negative reserve is resolved to DefaultReserveMemoryMB,
// so the daemon admitting work and the pane showing the arithmetic
// cannot disagree about what "unset" came to on this host.
//
// The zero Budget -- which bounds nothing at all -- for a host neither
// of whose dimensions could be read. It is deliberately the fallback
// rather than a guessed total: refusing sandboxes because nobody could
// count the machine would be a worse failure than the overcommit a
// budget exists to close.
func NewBudget(host Commitment, reserveMemoryMB int, cpuOvercommit, memoryOvercommit float64) Budget {
	if host.CPUs <= 0 && host.MemoryMB <= 0 {
		return Budget{}
	}
	if reserveMemoryMB < 0 {
		reserveMemoryMB = DefaultReserveMemoryMB(host.MemoryMB)
	}
	return Budget{
		Host:             host,
		ReserveCPUs:      DefaultReserveCPUs(host.CPUs),
		ReserveMemoryMB:  reserveMemoryMB,
		CPUOvercommit:    cpuOvercommit,
		MemoryOvercommit: memoryOvercommit,
	}
}

// Known reports whether this budget bounds anything at all. False for
// the zero value, and for a deployment whose host could not be read.
func (b Budget) Known() bool {
	_, cpu := b.CPUBudget()
	_, mem := b.MemoryBudgetMB()
	return cpu || mem || b.DiskBounded() || b.Host.DiskGB > 0 && b.DiskOvercommit > 0
}

// DiskBounded reports whether the disk floor has both a rule and a
// reading, and so has something to say about an admission.
func (b Budget) DiskBounded() bool {
	return b.DiskFloor.Known() && b.Disk.Known() && b.DiskFloor.FloorMB(b.Disk.TotalMB) > 0
}

// DiskBudgetGB is the total sandbox disk (in GiB) this budget allows to be
// promised at once, and whether that sum is bounded at all: the disk less
// the floor kept free on it, times DiskOvercommit. The disk is Host.DiskGB
// where the backend reported one -- a cluster's allocatable ephemeral
// storage -- and otherwise the live reading of the sandbox volume (Disk),
// so a local host needs no figure beyond the one the floor already reads.
// Unbounded with a ratio of zero or less, or with no disk figure at all.
func (b Budget) DiskBudgetGB() (int, bool) {
	if b.DiskOvercommit <= 0 {
		return 0, false
	}
	var usableMB int
	switch {
	case b.Host.DiskGB > 0:
		totalMB := b.Host.DiskGB * 1024
		usableMB = totalMB - b.DiskFloor.FloorMB(totalMB)
	case b.Disk.Known():
		usableMB = b.Disk.TotalMB - b.DiskFloor.FloorMB(b.Disk.TotalMB)
	default:
		return 0, false
	}
	if usableMB <= 0 {
		return 0, true
	}
	return int(float64(usableMB) / 1024 * b.DiskOvercommit), true
}

// CPUBudget is the total vCPU this budget allows across every sandbox at
// once, and whether CPU is bounded at all. The reserve is taken off the top;
// a reserve larger than the machine leaves a budget of nothing.
func (b Budget) CPUBudget() (float64, bool) {
	if b.Host.CPUs <= 0 || b.CPUOvercommit <= 0 {
		return 0, false
	}
	usable := b.Host.CPUs - max(b.ReserveCPUs, 0)
	if usable <= 0 {
		return 0, true
	}
	return float64(usable) * b.CPUOvercommit, true
}

// MemoryBudgetMB is the total sandbox memory this budget allows at once,
// and whether memory is bounded at all. The reserve is taken off the top;
// a reserve larger than the machine leaves a budget of nothing, which is
// a misconfiguration this reports honestly rather than rounding away.
func (b Budget) MemoryBudgetMB() (int, bool) {
	if b.Host.MemoryMB <= 0 || b.MemoryOvercommit <= 0 {
		return 0, false
	}
	usable := b.Host.MemoryMB - max(b.ReserveMemoryMB, 0)
	if usable <= 0 {
		return 0, true
	}
	return int(float64(usable) * b.MemoryOvercommit), true
}

// BudgetVerdict is what a Budget says about one more sandbox.
type BudgetVerdict struct {
	// OK is whether it may be built now.
	OK bool
	// Reason, on a refusal, is the one line that says what is short and
	// by how much -- written to be shown to whoever is waiting for the
	// job rather than only to a log (ci.JobRun.Activity).
	Reason string
	// Sole is set on an admission that was made only because nothing else
	// holds this budget: a single sandbox bigger than the whole budget
	// still has to run, since waiting for capacity that cannot arrive
	// would wedge the deployment rather than protect it. A caller that
	// logs anything logs this.
	Sole bool
	// Exception is set on an admission that was allowed to exceed the
	// budget ceiling by at most one sandbox under the explicit exception
	// policy (a console/director session or a deadlock-breaking CI job
	// admitted while existing commitments were still within the budget
	// cap). Once an exception pushes live commitments above the cap,
	// neither ordinary work nor a second exception is admitted until
	// committed drops back within the cap.
	Exception bool
}

// ClusterFit is a cluster that can say whether one more sandbox of a
// shape would be scheduled now (Budget.ClusterFit). FitsNow answers from
// a cached reading and must be cheap: it is asked on every admission. A
// shape that could never fit any node is answered true, so the sandbox
// is built and fails at once with the cluster's own reason rather than
// waiting in a queue for room that is not coming.
type ClusterFit interface {
	FitsNow(want Commitment) (ok bool, reason string)
}

// clusterRefuses is ClusterFit's refusal of want, if it has one.
func (b Budget) clusterRefuses(want Commitment) (BudgetVerdict, bool) {
	if b.ClusterFit == nil || want.IsZero() {
		return BudgetVerdict{}, false
	}
	if ok, reason := b.ClusterFit.FitsNow(want); !ok {
		return BudgetVerdict{Reason: "cluster: " + reason}, true
	}
	return BudgetVerdict{}, false
}

// WithinCap reports whether live does not exceed the budget's memory or
// vCPU ceiling. An unbounded dimension is always within cap.
func (b Budget) WithinCap(live Commitment) bool {
	if memMB, ok := b.MemoryBudgetMB(); ok && live.MemoryMB > memMB {
		return false
	}
	if cpus, ok := b.CPUBudget(); ok && float64(live.CPUs) > cpus {
		return false
	}
	if diskGB, ok := b.DiskBudgetGB(); ok && live.DiskGB > diskGB {
		return false
	}
	return true
}

// AdmitsException reports whether one more sandbox of shape want may be
// built under the explicit exception policy (a director/chat console
// session or a deadlock-breaking CI job).
//
// Policy: an exception may exceed the memory or vCPU budget ceiling by at
// most one sandbox, provided the disk floor is satisfied and existing
// commitments (live) are still within the budget cap (WithinCap). Once an
// exception sandbox has pushed committed above the cap, all further
// work -- including additional exception sandboxes -- waits until committed
// is back under the cap.
func (b Budget) AdmitsException(live, want Commitment) BudgetVerdict {
	if verdict := b.DiskFloor.Admits(b.Disk); !verdict.OK {
		return verdict
	}
	if want.IsZero() {
		return BudgetVerdict{OK: true}
	}
	if verdict := b.Admits(live, want); verdict.OK {
		return verdict
	}
	if b.WithinCap(live) {
		if refusal, refused := b.clusterRefuses(want); refused {
			return refusal
		}
		return BudgetVerdict{OK: true, Exception: true}
	}
	return b.AdmitsRepair(live, want)
}

// Admits reports whether one more sandbox of shape want may be built
// while live is already committed.
//
// live is everything this deployment is committing right now, both pools
// and every team of them (Ledger.Live) -- not the asking pool's own
// share, which is what it used to be, and which is what let the agent
// side and CI each spend the same gigabyte. want is the shape being
// asked for, and a zero shape is charged nothing because there is
// nothing to charge -- a caller that knows what its backend's default
// shape is should resolve it before asking.
//
// The Sole rule is what keeps this from wedging a deployment: a sandbox
// asking for more than the whole budget is admitted when the asking pool
// holds nothing, because no amount of waiting will make room that the
// budget was never going to have. A budget too small for one job costs
// that deployment its parallelism, which an operator can see and fix;
// refusing outright would cost it its CI, silently, with the jobs queued
// behind a wait that never ends.
//
// And what a budget that moves under a live deployment does to the
// sandboxes already on the host: nothing. The three settings behind it
// are re-read each reconcile tick (cmd/grain's config.withModelConfig),
// so an operator tightening a ratio in the pane is asking a *question
// about the next admission* -- this is the only place a budget is
// consulted, and it is consulted when something wants to be built. A run
// or a CI job that was admitted under the old, larger number keeps its
// sandbox and finishes: killing it would throw away work already paid
// for, to reclaim memory the host has by then already found. What the
// smaller budget does instead is refuse the next one, since live is
// counted at the shapes actually held rather than at the budget they
// were admitted under -- so a deployment that is over its new budget
// simply admits nothing more until enough of it drains below the new
// figure, and nothing has to be interrupted for that to be true. The
// Sole rule above keeps even a shrink past one sandbox's shape from
// wedging it.
func (b Budget) Admits(live, want Commitment) BudgetVerdict {
	return b.admits(live, want, b.RepairReserve)
}

// AdmitsRepair is Admits for a base-branch repair: the whole budget,
// RepairReserve included. Everything else about the verdict -- the disk
// floor, the Sole rule, the words of a refusal -- is Admits' own.
func (b Budget) AdmitsRepair(live, want Commitment) BudgetVerdict {
	return b.admits(live, want, Commitment{})
}

// AdmitsAs is Admits or AdmitsRepair, as repair says.
func (b Budget) AdmitsAs(live, want Commitment, repair bool) BudgetVerdict {
	if repair {
		return b.AdmitsRepair(live, want)
	}
	return b.Admits(live, want)
}

// ReservedFor is the part of RepairReserve that actually comes out of
// this budget: never more than the budget has in either dimension, and
// nothing in a dimension the budget does not bound.
func (b Budget) ReservedFor() Commitment { return b.reserved(b.RepairReserve) }

func (b Budget) reserved(reserve Commitment) Commitment {
	var out Commitment
	if memMB, ok := b.MemoryBudgetMB(); ok {
		out.MemoryMB = min(max(reserve.MemoryMB, 0), memMB)
	}
	if cpus, ok := b.CPUBudget(); ok {
		out.CPUs = min(max(reserve.CPUs, 0), int(cpus))
	}
	if diskGB, ok := b.DiskBudgetGB(); ok {
		out.DiskGB = min(max(reserve.DiskGB, 0), diskGB)
	}
	return out
}

func (b Budget) admits(live, want, reserve Commitment) BudgetVerdict {
	verdict := b.admitsArithmetic(live, want, reserve)
	if verdict.OK {
		if refusal, refused := b.clusterRefuses(want); refused {
			return refusal
		}
	}
	return verdict
}

func (b Budget) admitsArithmetic(live, want, reserve Commitment) BudgetVerdict {
	// Disk first, and before the zero-shape short circuit below: a
	// sandbox that commits no memory and no vCPU this deployment can
	// count -- a host sandbox, a job whose backend applies no shape --
	// still writes to the volume, and free space is the one dimension
	// that is short for everybody at once rather than per shape.
	if verdict := b.DiskFloor.Admits(b.Disk); !verdict.OK {
		return verdict
	}
	if want.IsZero() {
		return BudgetVerdict{OK: true}
	}
	held := live
	kept := b.reserved(reserve)
	if memMB, ok := b.MemoryBudgetMB(); ok && held.MemoryMB+want.MemoryMB > memMB-kept.MemoryMB {
		if live.MemoryMB <= 0 {
			return BudgetVerdict{OK: true, Sole: true}
		}
		return BudgetVerdict{Reason: b.memoryRefusal(live, want, memMB) + reserveClause(kept.MemoryMB > 0)}
	}
	if cpus, ok := b.CPUBudget(); ok && float64(held.CPUs+want.CPUs) > cpus-float64(kept.CPUs) {
		if live.CPUs <= 0 {
			return BudgetVerdict{OK: true, Sole: true}
		}
		return BudgetVerdict{Reason: b.cpuRefusal(live, want, cpus) + reserveClause(kept.CPUs > 0)}
	}
	if diskGB, ok := b.DiskBudgetGB(); ok && want.DiskGB > 0 && held.DiskGB+want.DiskGB > diskGB-kept.DiskGB {
		if live.DiskGB <= 0 {
			return BudgetVerdict{OK: true, Sole: true}
		}
		return BudgetVerdict{Reason: b.diskRefusal(live, want, diskGB) + reserveClause(kept.DiskGB > 0)}
	}
	return BudgetVerdict{OK: true}
}

// reserveClause is what a refusal adds when part of the budget it was
// held against is RepairReserve, so that a reader adding up the numbers
// in the sentence is not left a sandbox's worth short.
func reserveClause(reserved bool) string {
	if !reserved {
		return ""
	}
	return " (the budget's repair reserve is kept for base-branch repairs)"
}

// memoryRefusal and cpuRefusal are the one line a refusal in that
// dimension says.
//
// One sentence, and one budget in it. It used to be two whenever another
// pool was holding part of this one, because there were then two numbers
// a reader might act on -- the share left to the asking pool, and the
// deployment-wide total -- with an invisible standing reservation between
// them. There is no reservation and no share: what is committed is every
// live sandbox this deployment has, of either pool, and what it is
// committed against is the whole overcommit-derived budget. The one
// number in the sentence that is not the host is the overcommit ratio,
// which is now genuinely the lever, since nothing else is subtracted.
//
// What the sentence cannot say is which *team* is holding the live
// commitment, because a budget knows nothing about teams. Whoever refuses
// the work adds that (Ledger.Describe, and the team limit its own caller
// names).
func (b Budget) memoryRefusal(live, want Commitment, memMB int) string {
	return fmt.Sprintf(
		"memory: %s GiB is committed by this deployment's live sandboxes of its %s GiB sandbox budget "+
			"(%s GiB host, %s GiB reserved for the host itself), and this one wants %s GiB",
		gib(live.MemoryMB), gib(memMB), gib(b.Host.MemoryMB), gib(b.ReserveMemoryMB), gib(want.MemoryMB)) +
		wholeBudgetClause(want.MemoryMB > memMB)
}

// wholeBudgetClause is what a refusal adds when the one sandbox asking is
// bigger than the whole budget -- a task whose OOM kills have doubled its
// memory past it (orchestrator.raiseMemoryAfterOOM), or one sized that
// way by hand. No amount of other work finishing makes it fit; it is
// admitted only once nothing else is live (admits' Sole rule), and the
// reader waiting on it should be told so rather than left to expect a
// slot.
func wholeBudgetClause(bigger bool) string {
	if !bigger {
		return ""
	}
	return ", more than the whole budget, so it starts only once no other sandbox is live"
}

func (b Budget) cpuRefusal(live, want Commitment, cpus float64) string {
	var total string
	if b.ReserveCPUs > 0 {
		total = fmt.Sprintf("its %.0f vCPU sandbox budget (%d host vCPU less %d reserved, at %.1fx)",
			cpus, b.Host.CPUs, b.ReserveCPUs, b.CPUOvercommit)
	} else {
		total = fmt.Sprintf("its %.0f vCPU sandbox budget (%d host vCPU at %.1fx)",
			cpus, b.Host.CPUs, b.CPUOvercommit)
	}
	return fmt.Sprintf("cpu: %d vCPU is committed by this deployment's live sandboxes of %s, and this one wants %d",
		live.CPUs, total, want.CPUs) + wholeBudgetClause(float64(want.CPUs) > cpus)
}

func (b Budget) diskRefusal(live, want Commitment, diskGB int) string {
	return fmt.Sprintf(
		"disk: %d GiB is promised to this deployment's live sandboxes of its %d GiB sandbox disk budget "+
			"(%s, less the free-space floor, at %.1fx), and this one wants %d GiB",
		live.DiskGB, diskGB, b.diskSource(), b.DiskOvercommit, want.DiskGB)
}

// diskSource says which disk figure DiskBudgetGB was taken from.
func (b Budget) diskSource() string {
	if b.Host.DiskGB > 0 {
		return fmt.Sprintf("%d GiB allocatable", b.Host.DiskGB)
	}
	return fmt.Sprintf("%s GiB volume", gib(b.Disk.TotalMB))
}

// Summary is this budget as one line of prose, for the daemon's own
// startup log: what the host has, what is kept back for the host itself,
// and what is left to promise to sandboxes.
func (b Budget) Summary() string {
	if !b.Known() {
		return "no host reading, so nothing bounds how much machine this deployment's sandboxes commit " +
			"-- only the deployment's worker, merger and CI job counts"
	}
	mem := "unbounded memory"
	if memMB, ok := b.MemoryBudgetMB(); ok {
		mem = fmt.Sprintf("%s GiB of sandbox memory (%s GiB host less %s GiB reserved, at %.2fx)",
			gib(memMB), gib(b.Host.MemoryMB), gib(b.ReserveMemoryMB), b.MemoryOvercommit)
	}
	cpu := "unbounded vCPU"
	if cpus, ok := b.CPUBudget(); ok {
		if b.ReserveCPUs > 0 {
			cpu = fmt.Sprintf("%.0f vCPU (%d host vCPU less %d reserved, at %.2fx)", cpus, b.Host.CPUs, b.ReserveCPUs, b.CPUOvercommit)
		} else {
			cpu = fmt.Sprintf("%.0f vCPU (%d host vCPU at %.2fx)", cpus, b.Host.CPUs, b.CPUOvercommit)
		}
	}
	out := mem + " and " + cpu
	if diskGB, ok := b.DiskBudgetGB(); ok {
		out += fmt.Sprintf(", %d GiB of promised sandbox disk (%s, less the free-space floor, at %.2fx)",
			diskGB, b.diskSource(), b.DiskOvercommit)
	}
	if kept := b.ReservedFor(); !kept.IsZero() {
		out += ", of which " + kept.String() + " is kept for base-branch repairs"
	}
	if b.DiskBounded() {
		out += ", with " + b.DiskFloor.Describe(b.Disk)
	}
	return out
}

// gib renders a MiB figure as the GiB an operator sizing a host thinks
// in -- ui.gibibytes' own conversion, repeated here rather than imported
// because pkg/ui is a layer above this one.
func gib(mb int) string { return fmt.Sprintf("%.1f", float64(mb)/1024) }
