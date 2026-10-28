package model

import "time"

// Limits is how much work may be in flight at once: the counts that bound
// a deployment's live runs (grain/task-63, grain/task-296), and the
// resource budget every sandbox is held against (grain/task-173,
// grain/task-28).
//
// No team has counts of its own any more (the single-tenant reset's stage
// 4b removed Team.MaxWorkers, MaxMergers and MaxCIJobs). The counts here
// are whatever ceiling the caller imposes across all of the work, and
// zero counts mean "no count bound here", not "admit nothing"
// (Unlimited). grain's own daemon passes none, which leaves the budget,
// held against the live Ledger, as the whole of what bounds the machine.

// A *merger* is a run that repairs a pull request that will not land --
// a task the merge queue has sent back to work on its own branch
// (Observation.MergeQueueRepairAt), or one of the separate fix tasks it
// used to file for that (Origin.Reason == ReasonFix). Store.mergerTaskSQL
// is the one definition. A *worker* is a run of anything else: the
// ordinary backlog, a schedule, a qualification, a suite pass, a task
// somebody just filed.
//
// The split exists because the two compete for the same capacity while
// being worth very different amounts. A merger is the last step of work
// that is already done -- committed, pushed, reviewed -- and the longer
// it waits the more likely something else lands on the branch it repairs
// and the repair has to start over (Store.Ready's own doc comment on how
// the merge queue's own work reaches the head of the backlog). A deployment whose
// every slot was spent on new work would starve exactly the runs that
// finish the old, so Mergers is capacity kept back for them.
//
// Kept back, not walled off: the two numbers are not symmetric.
//
//   - No more than Workers runs of ordinary work are ever live.
//   - No more than AgentRuns ordinary runs are ever live at all, which
//     is Workers+Mergers.
//
// A merger is bounded only by the second rule, so it may take a worker's
// slot when one is free -- Workers 3 and Mergers 2 admits five mergers,
// but never a fourth worker, and never a sixth run of either kind. That
// asymmetry is the whole point: work that is nearly landed may borrow
// capacity from work not yet started, and never the other way round.
//
// Console sessions -- the director and every other configured session,
// on any team -- are deliberately outside both numbers, and outside any
// count at all. Each is identified by the console-session kind and tag
// the console-session reconciler writes, and each is admitted whenever it
// is ready, even when every ordinary agent slot is occupied. There is no
// ceiling on them: a task has at most one live run, so each session is
// limited to one run by construction, and a
// shared ceiling only made every other session wait out whichever one
// was live -- for hours, since a session's run blocks in chat
// (grain/task-2323). A session never preempts ordinary work, and ordinary
// interactive tasks cannot use this path. What its sandbox commits is
// charged to the deployment's ledger for as long as its run lasts
// (Limits.ChargeDirectorSandbox), so everything else admitted meanwhile
// sees the machine it is using.
//
// CIJobs is a third number, and a different kind of one: grain CI runs
// one sandbox per job. It is its own concurrency limit rather than a
// reservation carved out of Workers+Mergers, so the whole count rule is:
//
//   - No more than Workers+Mergers ordinary agent sandboxes exist at once.
//   - Every console session may additionally hold one sandbox of its own.
//   - No more than CIJobs CI job sandboxes exist at once.
//
// The agent counts are enforced transactionally in the store (StartRun)
// and CI's by an in-process gate in pkg/ci, which is where the two kinds
// of work are actually started.
//
// Every one of those numbers is a count, and a count is not what a
// sandbox costs. Budget is the other half of the rule (grain/task-173),
// a total of memory and vCPU that every live sandbox is held against, so
// that eight CI jobs means eight jobs *that fit* rather than eight times
// whatever each one asked for. Both bounds apply, and whichever binds
// first is the one that holds the work. See Budget for the arithmetic and
// Ledger for what is charged against it.
//
// And a third bound that is neither a count nor a sum: Budget.DiskFloor,
// the free space kept on the volume the sandboxes are written to
// (grain/task-196). It holds work while the volume is too full,
// whatever the counts and the shapes say, because a disk is filled by
// what the runs write rather than by what they were promised.
//
// And two settings that bound nothing: CIAgeBoost and CIReserveAfter are
// the order the CI gate spends its capacity in once every bound above
// has refused somebody -- which job goes first, and when capacity is
// held for one that does not fit rather than given to one that does.
//
// The zero value enforces no count at all (Unlimited), which is what
// grain's own daemon carries.
type Limits struct {
	// Workers is the ceiling on ordinary runs, and the floor under how
	// much of the total a merger can ever be denied. 0 means no worker
	// ceiling: the budget is the whole of what holds the work.
	Workers int
	// Mergers is the extra capacity only a merge-queue repair may
	// reach. 0 -- a meaningful value, not an unset one -- means mergers
	// contend for Workers alongside everything else, which is what every
	// deployment did before this split existed.
	Mergers int
	// CIJobs is the ceiling on live grain CI job sandboxes. 0 means no CI
	// ceiling: jobs are bounded by the deployment's budget alone.
	CIJobs int
	// Durable is the ceiling on live runs of durable tasks
	// (Task.IsDurable, docs/design/durable-tasks.md#DT-1). They are
	// admitted outside Workers and Mergers, the way every task with
	// priority dispatch is, so this is the one count that holds them: a
	// safety ceiling on how many sandboxes may be held for as long as
	// their work lasts, not a queue. 0 means no ceiling.
	Durable int
	// Budget is how much of the machine every live sandbox on this host
	// may commit between them, and it is the deployment-wide half of every
	// rule above (grain/task-173). The counts say how many sandboxes there
	// may be; this says how big all of them together are allowed to be,
	// which is the question a count cannot ask. The zero value bounds
	// nothing, which is what a caller with no host reading gets.
	//
	// It is the only deployment-wide capacity control there is: nothing
	// multiplies a configured maximum into it and nothing subtracts a
	// reservation from it (Ledger).
	Budget Budget
	// Ledger is what this deployment's live sandboxes have already
	// committed of that budget -- shared by every admission there is, so
	// that an agent run and a CI job racing for the last of the machine
	// cannot both be given it (Ledger.Admit).
	//
	// nil is a caller with no deployment to charge: a test driving
	// dispatch directly, or a one-shot cycle. Every method on it tolerates
	// that, and admission then behaves as it did before a ledger existed
	// -- the budget against an empty host.
	Ledger *Ledger
	// CIJobsPerRun is an optional ceiling on how many sandboxes one grain
	// CI run may hold at once (Config.CIJobsPerRun,
	// ci.Engine.ConcurrencyLimit). 0 means no per-run limit, leaving the
	// deployment's sandbox budget (Budget) as the only bound on a run's
	// concurrency.
	CIJobsPerRun int
	// CIAgeBoost and CIReserveAfter are how the CI gate orders the jobs
	// those counts and that budget are holding back (ci.SharedGate,
	// grain/task-316). They are limits in the same sense the numbers
	// above are -- a deployment's own reading of how to spend capacity it
	// has not got enough of -- and they are here, rather than as
	// constants in pkg/ci, because an operator watching a queue is the
	// one who can tell that the reservation is too eager or the boost too
	// slow.
	//
	// CIAgeBoost is how long a job queues to gain one step of rank: the
	// anti-starvation floor, so a job ranked N steps below the work ahead
	// of it overtakes that work after N boosts. CIReserveAfter is how
	// long the budget has to have been refusing a job before capacity is
	// held for it rather than spent on smaller jobs that fit, which is
	// what keeps an expensive job from starving while capacity frees in
	// slivers.
	//
	// Zero in either means "grain's own", which is ci.DefaultAgeBoost and
	// ci.DefaultReserveAfter -- resolved there rather than here, since
	// pkg/ci owns the rule and pkg/model cannot import it. A negative
	// CIReserveAfter turns reservation off; a negative CIAgeBoost turns
	// the boost off, which is the starvation it exists to prevent and is
	// refused by the settings path that writes these.
	CIAgeBoost     time.Duration
	CIReserveAfter time.Duration
	// AgentShape is what one agent sandbox commits -- the deployment-wide
	// shape, since that is what every ordinary run gets unless its task
	// overrides it. It is what an agent admission charges to the Ledger.
	// Zero on a backend that applies no shape at all (host sandboxing),
	// where there is nothing to commit and the counts are again the whole
	// rule.
	AgentShape Commitment
}

// DefaultMaxCIJobs is Limits.CIJobs for a deployment running grain CI
// that has not chosen its CI concurrency -- Config.MaxCIJobs'
// stored default, named here beside the rule it feeds.
//
// One, for the same reason DefaultMaxMergers is one: a single sandbox is
// enough to keep a branch's checks moving without surprising a small
// deployment with several additional sandboxes.
const DefaultMaxCIJobs = 1

// Total is the ordinary ceiling on live agent sandboxes: workers plus
// mergers. Console sessions are not in it: they are admitted outside
// every count (see Limits).
func (l Limits) Total() int { return l.Workers + l.Mergers }

// CILimit is the ceiling on live CI job sandboxes for this scope, and 0
// where the scope sets none. Negative values are treated as "none",
// matching the defensive reading of the other limits outside their
// validated settings path.
func (l Limits) CILimit() int { return max(l.CIJobs, 0) }

// AgentRuns is the ordinary ceiling on this scope's live worker and
// merger runs, 0 meaning it sets none. Live console sessions, which no
// count bounds, may raise the live agent total above it.
func (l Limits) AgentRuns() int { return l.Total() }

// Unlimited reports whether these limits bound no *count* -- the zero
// value, or anything else with no positive worker or merger capacity.
// Both StartRun and dispatch.Cycle read it the same way: "there is no
// count here to enforce", leaving the deployment's resource budget as the
// whole of what holds the work.
//
// The two used to read it oppositely, because max_workers was a
// deployment-wide setting and a deployment configured for no workers had
// nothing it could sensibly start. Nothing applies that setting now
// (grain/task-28): no counts is the ordinary state of the deployment, and
// reading it as "admit nothing" would stop every deployment dead.
func (l Limits) Unlimited() bool { return l.Workers <= 0 && l.Mergers <= 0 }

// Admits reports whether this scope's *counts* admit one more agent run
// with live already in flight -- a merger when merger is true, an
// ordinary worker otherwise.
//
// Counts and nothing else. A caller asks about the counts here and the
// deployment's ledger about the machine (Limits.SandboxVerdict), and the
// two answers are about two different things. Both have to say yes.
//
// This is still the whole count rule in one place, so that the check
// dispatch.Cycle makes before it asks for capacity and the check StartRun
// makes inside the transaction that records the run cannot drift apart.
// See StartRun on why the second one is the one that actually enforces
// anything. CI's count is CILimit, enforced by the gate cmd/grain hands
// ci.Engine.
func (l Limits) Admits(live RunCounts, merger bool) bool {
	if l.Unlimited() {
		return true
	}
	if live.OrdinaryTotal() >= l.AgentRuns() {
		return false
	}
	if merger {
		return true
	}
	return live.Workers < l.Workers
}

// TaskShape is the Commitment one run of task makes on this deployment:
// AgentShape with any per-task CPU, memory or disk override applied. On a
// backend that applies no shape (AgentShape.IsZero()), TaskShape is zero
// as well.
func (l Limits) TaskShape(task Task) Commitment {
	if l.AgentShape.IsZero() {
		return Commitment{}
	}
	out := l.AgentShape
	if task.SandboxCPUs > 0 {
		out.CPUs = task.SandboxCPUs
	}
	if task.SandboxMemoryMB > 0 {
		out.MemoryMB = task.SandboxMemoryMB
	}
	if task.SandboxDiskGB > 0 {
		out.DiskGB = task.SandboxDiskGB
	}
	return out
}

// SandboxVerdict is what the deployment-wide budget says about one more
// agent sandbox, given everything live on this host right now -- every
// agent run and every CI job, from the one ledger they all charge
// (Ledger.Verdict).
//
// A read, not a claim: two callers can both be told yes and only one of
// them can have it, which is what ChargeSandbox is for. This is the
// look-before-you-leap a cycle makes before it decides to ask, and the
// words a refusal says when it is being held.
//
// A zero AgentShape is not a way out: a deployment whose backend applies
// no shape commits no memory and no vCPU anything here can add up, but
// its runs still write to the volume, and the disk floor is held against
// every admission rather than against a shape (Budget.Admits,
// grain/task-196). Budget.Admits charges a zero shape nothing in the two
// dimensions it is a sum of, so passing it through costs such a
// deployment exactly what it cost before.
func (l Limits) SandboxVerdict() BudgetVerdict {
	return l.SandboxVerdictFor(l.AgentShape)
}

// SandboxVerdictFor is SandboxVerdict for a specific sandbox shape.
func (l Limits) SandboxVerdictFor(want Commitment) BudgetVerdict {
	if !l.Budget.Known() {
		return BudgetVerdict{OK: true}
	}
	return l.Ledger.Verdict(l.Budget, want)
}

// SandboxAdmits is SandboxVerdict's boolean.
func (l Limits) SandboxAdmits() bool { return l.SandboxVerdict().OK }

// SandboxAdmitsShape is SandboxVerdictFor's boolean.
func (l Limits) SandboxAdmitsShape(want Commitment) bool { return l.SandboxVerdictFor(want).OK }

// RepairSandboxVerdict is SandboxVerdict for a base-branch repair's run:
// the same budget with its RepairReserve open to it (Budget.AdmitsRepair,
// grain/task-488).
func (l Limits) RepairSandboxVerdict() BudgetVerdict {
	return l.RepairSandboxVerdictFor(l.AgentShape)
}

// RepairSandboxVerdictFor is RepairSandboxVerdict for a specific shape.
func (l Limits) RepairSandboxVerdictFor(want Commitment) BudgetVerdict {
	if !l.Budget.Known() {
		return BudgetVerdict{OK: true}
	}
	return l.Ledger.VerdictAs(l.Budget, want, true)
}

// ChargeRepairSandbox is ChargeSandbox for a base-branch repair's run.
func (l Limits) ChargeRepairSandbox(id string) (BudgetVerdict, func()) {
	return l.ChargeRepairSandboxShape(id, l.AgentShape)
}

// ChargeRepairSandboxShape is ChargeRepairSandbox for a specific shape.
func (l Limits) ChargeRepairSandboxShape(id string, want Commitment) (BudgetVerdict, func()) {
	if !l.Budget.Known() {
		return BudgetVerdict{OK: true}, l.Ledger.Charge(PoolAgent, id, want)
	}
	return l.Ledger.AdmitAs(l.Budget, PoolAgent, id, want, true)
}

// ChargeSandbox is SandboxVerdict and the charge together, under the
// ledger's own lock: the admission an agent sandbox actually makes. On
// success the shape is committed under (PoolAgent, id) until somebody
// gives it back -- Ledger.Release(PoolAgent, id), since a run's sandbox
// outlives the cycle that started it.
//
// A refused admission charges nothing and returns the refusal's words.
func (l Limits) ChargeSandbox(id string) (BudgetVerdict, func()) {
	return l.ChargeSandboxShape(id, l.AgentShape)
}

// ChargeSandboxShape is ChargeSandbox for a specific sandbox shape.
func (l Limits) ChargeSandboxShape(id string, want Commitment) (BudgetVerdict, func()) {
	if !l.Budget.Known() {
		return BudgetVerdict{OK: true}, l.Ledger.Charge(PoolAgent, id, want)
	}
	return l.Ledger.Admit(l.Budget, PoolAgent, id, want)
}

// DirectorSandboxVerdict is what the deployment-wide budget says about one
// more sandbox for a run with priority dispatch -- a console session's, or
// a task's holding PriorityDispatchCapability -- under the exception
// policy.
func (l Limits) DirectorSandboxVerdict() BudgetVerdict {
	return l.DirectorSandboxVerdictFor(l.AgentShape)
}

// DirectorSandboxVerdictFor is DirectorSandboxVerdict for a specific shape.
func (l Limits) DirectorSandboxVerdictFor(want Commitment) BudgetVerdict {
	if !l.Budget.Known() {
		return BudgetVerdict{OK: true}
	}
	return l.Ledger.VerdictException(l.Budget, want)
}

// DirectorSandboxAdmits reports whether a console/director session may
// start now under the exception policy.
func (l Limits) DirectorSandboxAdmits() bool { return l.DirectorSandboxVerdict().OK }

// AdmitDirectorSandbox is ChargeSandbox for a console/director session
// under the explicit exception policy (Budget.AdmitsException): a director
// or chat session may exceed the budget by at most one sandbox while
// existing commitments are within the cap. Once committed exceeds the cap,
// additional sessions wait until committed is back under the cap.
func (l Limits) AdmitDirectorSandbox(id string) (BudgetVerdict, func()) {
	return l.AdmitDirectorSandboxShape(id, l.AgentShape)
}

// AdmitDirectorSandboxShape is AdmitDirectorSandbox for a specific shape.
func (l Limits) AdmitDirectorSandboxShape(id string, want Commitment) (BudgetVerdict, func()) {
	if !l.Budget.Known() {
		return BudgetVerdict{OK: true}, l.Ledger.Charge(PoolAgent, id, want)
	}
	return l.Ledger.AdmitException(l.Budget, PoolAgent, id, want)
}

// ChargeDirectorSandbox is AdmitDirectorSandbox returning only the release
// closure (or a no-op if the exception cap is already exceeded).
func (l Limits) ChargeDirectorSandbox(id string) func() {
	_, release := l.AdmitDirectorSandbox(id)
	if release == nil {
		return func() {}
	}
	return release
}

// LiveCommitment is what this deployment's live sandboxes commit right
// now, from the shared ledger -- the figure a refusal or a status line
// reports in place of the standing reservation it used to report.
func (l Limits) LiveCommitment() Commitment { return l.Ledger.Live() }

// SetAgentWaiting records on the shared ledger whether a dispatch cycle has
// at least one eligible agent task held back by the sandbox resource budget.
func (l Limits) SetAgentWaiting(waiting bool) {
	l.Ledger.SetAgentDemand(waiting, l.AgentShape)
}

// AgentWaiting reports whether the shared ledger currently has an agent task
// waiting for room in the sandbox budget.
func (l Limits) AgentWaiting() bool {
	return l.Ledger.AgentWaiting()
}

// RunCounts is how many runs are live right now, of each kind Limits
// bounds -- Store.LiveRunCounts' own answer, and the tally
// dispatch.Cycle spends down as it starts runs.
type RunCounts struct {
	Workers int
	Mergers int
	// Directors is the live count of runs with priority dispatch
	// (Task.HasPriorityDispatch): every console session's on every team,
	// and every task's holding PriorityDispatchCapability. It is kept
	// apart so it neither consumes nor disguises ordinary capacity; no
	// limit is held against it.
	Directors int
}

// Total is how many runs are live regardless of kind -- what
// Store.LiveRunCount returns on its own.
func (c RunCounts) Total() int { return c.OrdinaryTotal() + c.Directors }

// OrdinaryTotal is how many worker and merger runs occupy the ordinary
// agent limit, excluding console sessions.
func (c RunCounts) OrdinaryTotal() int { return c.Workers + c.Mergers }

// Add counts one more live run of the given kind.
func (c RunCounts) Add(merger bool) RunCounts {
	if merger {
		c.Mergers++
		return c
	}
	c.Workers++
	return c
}

// AddDirector counts one more run with priority dispatch.
func (c RunCounts) AddDirector() RunCounts {
	c.Directors++
	return c
}

// Merger reports whether a run of a task with this origin reason counts
// against the merger half of Limits by its reason alone: a task the merge
// queue filed to repair another task's branch (ReasonFix) and nothing
// else.
//
// It is no longer the whole of the question. A repair now runs as another
// attempt of the task whose branch is broken (Observation.
// MergeQueueRepairAt), whose reason is whatever it was filed as, so the
// store answers "is this run a merger" with both halves at once
// (mergerTaskSQL) and nothing here reads this. It stays as the reason
// half's own name, for a caller holding an origin and no database.
func (r OriginReason) Merger() bool { return r == ReasonFix }
