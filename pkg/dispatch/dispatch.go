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

// Package dispatch decides which tasks run now: what one controller
// cycle starts, and nothing more. It does not loop — a deployment's own
// timer does, through orchestrator.RunCycle, which calls Cycle once per
// tick.
//
// Cycle calls StartRun and nothing else — no sandbox is created, no
// GitHub is touched, no agent runs. Those are side effects bwsalmon/
// agents#219 deliberately defers: this package exists to pin down that
// the *decisions* — which task runs, when, and how often — are correct
// on their own, against a real store, before anything external is wired
// to react to them. pkg/orchestrator is the side-effecting counterpart
// that grew around it, and nothing here had to change shape when it did,
// since a Dispatch already says everything that side effect needs to
// know.
//
// It used to decide which task took which *slot*, drawing from a fixed
// pool of generated identifiers that a long-lived sandbox was named
// after and reused under. A sandbox is now created for a run and
// destroyed with it, so there is no pool to assign out of and nothing
// for an identifier to name: concurrency is a count of live runs against
// model.Limits, and the run's own ID is the only name anything
// downstream needs.
//
// There is almost no scheduling policy here: no fairness, no preemption.
// Ordering is whatever task_ready yields, and Cycle takes its prefix,
// skipping over (never reordering past its own turn) a task still backing
// off after a recently failed run -- see retryEligible -- or one of a
// kind whose own half of model.Limits is already full, which is the same
// skip for the same reason. The one other exception is Store.Ready's own:
// a fix task the merge queue filed for a repo's stuck head sorts before
// ordinary ready tasks (task ID is still the tiebreak within each group),
// which is a fact about task_ready rather than a policy Cycle itself
// makes -- see Store.Ready's doc comment (bwsalmon/agents#389) for why. A
// package that ranked ready tasks against each other on some richer
// notion of priority would be a scheduler; this one drains a queue into
// whatever headroom there is.
//
// Which headroom, though, is two numbers rather than one since
// grain/task-63: a merge-queue fix task draws on capacity ordinary work
// cannot reach (model.Limits' own doc comment for what that buys and why
// the two are not symmetric). That is a fact about how much of the
// deployment each kind may occupy, not about their order -- the backlog
// still says which task goes first.
//
// Tasks with priority dispatch (model.Task.HasPriorityDispatch) are one
// deliberate exception to those ordinary limits: console sessions -- the
// director and every other configured session, on any team -- and any
// task a person granted model.PriorityDispatchCapability, such as the
// pipeline steward, whose job is to unstick the very congestion that
// would otherwise starve it. Such a task is admitted whenever it is
// ready, even while the worker and merger pool is full, so an operator
// can always reach the deployment, and the job that manages the queue is
// never queued behind it, without cancelling or preempting work already
// in flight. It is ordered ahead of mergers and ordinary work.
// Store.ReadyPriorityDispatch supplies the identity; Interactive or a
// task title alone never does. No count, deployment-wide or per team, is
// held against them: a task has one live run at most, so each is limited
// to one run already, and the single shared slot there used to be left
// every other team's and user's session waiting out whichever one was
// live (grain/task-2323). They still charge the sandbox budget, under the
// exception policy (model.Limits.AdmitDirectorSandboxShape). A pipeline
// pause does not hold them either (PriorityPassesPause), with one
// exception: an upgrade's own hold, whose restart would end any run it
// let start.
//
// Neither of those numbers is the whole machine, either, and grain's own
// daemon sets neither: no team has counts of its own any more, and what
// bounds the deployment is the sandbox resource budget, and every run this package starts charges its
// shape to the one ledger every grain CI job charges too
// (model.Limits.ChargeSandbox, grain/task-28). That is how CI is visible
// here, and the only way: nothing in this package counts a CI job or
// waits for one, but a cycle admits nothing the machine has no room for
// once CI has taken its share of it.
//
// One exception to "ordering is whatever task_ready yields", beyond the
// backoff above: a caller may pass Busy to have Cycle pass over a task
// it is itself still finishing with. That is not a scheduling policy
// either -- it is the caller supplying the one fact task_ready cannot
// know, that a run whose row is already finished has not yet had its
// result acted on. See Busy.
//
// The other, Paused, is the deployment-wide pause a person or grain
// itself has taken (model.PipelinePause): a cycle that runs while one is
// in force starts nothing except the tasks the pause exempts and, under
// any pause but an upgrade's hold, tasks with priority dispatch. That is
// not a policy about which task goes first either -- it is a deployment
// declining to pick work up at all, expressed here rather than as a
// caller's early return only because a pause taken to check an upgrade
// has to let that one check through. See Paused.
//
// The one thing here that is not a fact about the queue at all is a
// run-now request (model.Observation.RunNowAt, ui.Client.RunNow): a
// person naming the task they want worked on next. Cycle starts those
// before it reads the ready list, and without consulting task_ready
// about them -- so a task that is blocked, deferred, unapproved, failed
// or finished with a pull request already open is started all the same,
// which is the whole point of asking. It is not a priority either, since
// nothing about it competes with the ready order: it is one run, asked
// for once, spent by the run it asks for.
//
// What a request does not override is anything that is not the state
// machine's opinion: capacity (a run needs a sandbox, so a request waits
// for one exactly as ready work does), a pause of either kind below, and
// Busy. A request the cycle cannot honour stays outstanding for the next
// one rather than being dropped. The one pause a caller may let requests
// through is the deployment-wide one, and only by saying so
// (RunNowPassesPause): that is what makes a paused deployment one that
// runs exactly what it is told to.
//
// The one thing that does get past a full budget is a base-branch
// repair, requested or not: it may spend the budget's repair reserve, and
// ask for lower-priority work to be pre-empted when even that is not
// enough (repair.go, grain/task-488). That is the task's own kind
// deciding, not the request.
//
// PausedFor is the same idea for a pause that covers part of the queue:
// the agent's own usage limit, which stops the tasks driven by the
// framework whose credential met it and leaves the rest dispatchable.
// Which tasks those are is the caller's answer, not this package's.
package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Dispatch is one decision a cycle made: task TaskID was started as its
// Attempt'th run. The store write is already durable by the time a
// Dispatch is returned — this is a record of what happened, for a
// side-effecting layer to act on, not a request for one to decide.
//
// It named a Slot until slots stopped existing. Nothing replaced that
// field: the layer that acts on a Dispatch acquires a sandbox for it and
// records the name itself (Store.SetRunSandbox), so there is nothing
// about a sandbox for this package to have an opinion on.
type Dispatch struct {
	TaskID  string
	RunID   string
	Attempt int
}

// Candidate is the run Cycle is about to record, while it can still be
// declined without spending an attempt or any sandbox capacity.
type Candidate struct {
	TaskID  string
	RunID   string
	Attempt int
}

// Admission is a caller-owned prerequisite acquired immediately before a run
// is recorded. Leases are committed atomically with the run; Release undoes
// the external reservation if that store write loses a race or fails.
type Admission struct {
	Admitted bool
	Leases   []model.Lease
	Release  func()
}

// Admitter reserves prerequisites that must not consume run capacity while
// unavailable. It is deliberately outside Store.StartRun: capability
// providers call external services, while the store transaction must remain
// short and local.
type Admitter func(context.Context, Candidate) (Admission, error)

// RunID names a run deterministically from its task and attempt number,
// the same reasoning as task.go's BranchName: two callers computing a
// run's name must agree without coordinating, and a name that already
// encodes its own attempt is self-describing in a log with nothing to
// look up.
//
// The separator carries no letter of its own. It used to read
// "<task>-r<attempt>", which was a byte better spent: a run id is what a
// kontur VM is named after, and that name has 11 bytes to live in
// (orchestrator.maxVMNameLen), so the "r" cost real headroom -- one
// decimal digit of task id -- to say something the position of the field
// already says. Task ids never contain "-" (Store.NewTaskID hands out
// decimal counters), so the last "-" still splits the two halves
// unambiguously.
func RunID(taskID string, attempt int) string {
	return fmt.Sprintf("%s-%d", taskID, attempt)
}

// baseRetryBackoff and maxRetryBackoff bound how long Cycle waits after a
// task's run ends without succeeding before dispatching it again:
// baseRetryBackoff after the first such run in a row, doubling
// each further one, capped at maxRetryBackoff -- so a task whose agent
// hits a transient failure gets a handful of prompt retries, while one
// that is wrong in a way no retry fixes stops hammering a real API and a
// real sandbox every single poll interval (bwsalmon/agents#403).
//
// baseRetryBackoff sits below the default poll interval's own 30s
// (cmd/grain daemon's own default) on purpose: the very first retry
// after a failure should not itself add a visible delay on top of the
// next tick, since a poll interval already spaces attempts out. It is
// the doubling that does the real work, not the base.
//
// Once a task's own streak reaches model.MaxConsecutiveFailures,
// task_state (schema.go) stops calling it 'queued' at all, so it drops
// out of Ready before Cycle ever gets here -- these constants only ever
// gate the streak counts below that cutoff.
const (
	baseRetryBackoff = 30 * time.Second
	maxRetryBackoff  = 30 * time.Minute
)

// maxSetupRetryBackoff caps retryBackoff for a task with priority
// dispatch whose last run never got past setup (model.SetupFailedOutcome:
// a sandbox that could not be prepared, a transient Kubernetes API
// timeout writing its credentials). Such a failure says nothing about
// the task, and the task is one that exists to be running -- the
// pipeline steward, a console session -- so it is tried again within a
// cycle or two rather than left doubling out to half an hour. The
// streak still counts toward model.MaxConsecutiveFailures as before, so
// a sandbox that never comes up does not retry forever.
//
// A durable task's setup failure is a suspension instead
// (model.SuspendSetup), counts toward nothing, and waits this long too
// (DurableSetupBackoff).
const maxSetupRetryBackoff = 2 * time.Minute

// DurableSetupBackoff is how long a durable task's setup suspension holds
// it before it is tried again: maxSetupRetryBackoff, the same wait task
// 834 gave a priority task's setup failure.
const DurableSetupBackoff = maxSetupRetryBackoff

// retryBackoff is how long Cycle waits after the streakCount'th run in a
// row to end without succeeding before treating the task as ready again.
// streakCount <= 0 (never failed, or succeeded most recently) needs no
// wait at all.
func retryBackoff(streakCount int) time.Duration {
	if streakCount <= 0 {
		return 0
	}
	backoff := baseRetryBackoff * time.Duration(uint64(1)<<uint(streakCount-1))
	if backoff > maxRetryBackoff || backoff <= 0 {
		return maxRetryBackoff
	}
	return backoff
}

// retryEligible reports whether taskID's own failure history lets it be
// dispatched at now -- true outright for a task that has never finished a
// run, or whose most recent one succeeded, and otherwise true only once
// retryBackoff's own wait has elapsed since that run finished. priority
// is a task with priority dispatch, whose wait after a setup failure is
// capped at maxSetupRetryBackoff.
//
// Spaced by model.FailureStreak.BackoffCount rather than by Count, which
// is the wider of the two: it includes the runs a provider's own capacity
// ended (model.CapacityOutcome). Those deliberately do not count toward
// MaxConsecutiveFailures -- nothing about the task earned them -- but
// they are exactly the runs worth waiting a moment before repeating,
// since retrying an overloaded provider on the next poll is how a task
// spends an outage rebuilding sandboxes.
//
// A suspension that names when its task may restart (model.Suspension's
// ResumeAt -- a durable task's setup suspension, which waits out
// maxSetupRetryBackoff with no strike to back off from) holds the task
// until then as well.
func retryEligible(ctx context.Context, store *model.Store, taskID string, now time.Time, priority bool) (bool, error) {
	resumeAt, err := store.SuspensionResumeAt(ctx, taskID)
	if err != nil {
		return false, err
	}
	if resumeAt != nil && now.Before(*resumeAt) {
		return false, nil
	}
	streak, err := store.FailureStreak(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("checking %s's failure streak: %w", taskID, err)
	}
	if streak == nil || streak.BackoffCount == 0 {
		return true, nil
	}
	backoff := retryBackoff(streak.BackoffCount)
	if priority && streak.LastOutcome == model.SetupFailedOutcome && backoff > maxSetupRetryBackoff {
		backoff = maxSetupRetryBackoff
	}
	return !now.Before(streak.LastFinishedAt.Add(backoff)), nil
}

// runNowEligible is retryEligible for a run-now request, and it applies
// only to a request a run has already been started for and given back
// (model.RunNowSurvives: the run never reached its agent, or the
// deployment stopped it) -- one made before the task's last run began.
// That request waits out the same backoff a ready task would, still
// ahead of the ready list once it has, so a sandbox that cannot be built
// or a provider with no capacity is not retried every poll on a
// person's say-so. A request made since the last run is the person
// asking again, after seeing how it ended, and is started straight away
// as a request always was.
func runNowEligible(ctx context.Context, store *model.Store, req model.RunNowRequest, now time.Time) (bool, error) {
	streak, err := store.FailureStreak(ctx, req.TaskID)
	if err != nil {
		return false, fmt.Errorf("checking %s's failure streak: %w", req.TaskID, err)
	}
	if streak == nil || req.At.After(streak.LastStartedAt) {
		return true, nil
	}
	resumeAt, err := store.SuspensionResumeAt(ctx, req.TaskID)
	if err != nil {
		return false, err
	}
	if resumeAt != nil && now.Before(*resumeAt) {
		return false, nil
	}
	if streak.BackoffCount == 0 {
		return true, nil
	}
	backoff := retryBackoff(streak.BackoffCount)
	if req.Priority && streak.LastOutcome == model.SetupFailedOutcome && backoff > maxSetupRetryBackoff {
		backoff = maxSetupRetryBackoff
	}
	return !now.Before(streak.LastFinishedAt.Add(backoff)), nil
}

// Option is one caller's adjustment to a single Cycle call. Variadic
// rather than a parameter of its own because the overwhelming majority
// of callers -- every test that drives a cycle against a store, and
// every one-shot cycle -- want none of it, and a nil argument at every
// one of those call sites would say less than their absence does.
type Option func(*options)

type options struct {
	busy  func(taskID string) bool
	admit Admitter
	// paused and exempt are Paused's own pair: whether this cycle may
	// start anything at all, and the tasks the pause does not apply to.
	// Two fields rather than a nil map standing in for "not paused",
	// since a pause with nothing exempted -- an operator's own -- is a
	// perfectly ordinary pause and has to read differently from no pause.
	paused bool
	exempt map[string]bool
	// runNowPasses is RunNowPassesPause's own: a run-now request is not
	// held by paused.
	runNowPasses bool
	// priorityPasses is PriorityPassesPause's own: a task with priority
	// dispatch is not held by paused.
	priorityPasses bool
	// covered is PausedFor's own predicate: whether a pause that covers
	// part of the queue rather than all of it covers this task. nil when
	// no such pause is in force, which is every cycle of an unpaused
	// deployment.
	covered func(taskID string) bool
	// held is Hold's own: why the caller declines to start this task
	// for a reason of its own that is no pause -- a team with no GitHub
	// credential (grain/task-1592), a branch not cut yet, local policy --
	// or "" when it does not.
	held func(taskID string) string
	// preempt is Preempt's own: what frees room for a base-branch repair
	// the budget's reserve cannot hold. nil stops nothing.
	preempt Preempter
}

// Admit asks admit immediately before each candidate's run is recorded. A
// declined candidate is skipped for this cycle and work later in the ready
// order is still considered.
func Admit(admit Admitter) Option {
	return func(o *options) { o.admit = admit }
}

// Busy tells Cycle to pass over any task busy reports true for, without
// spending capacity on it -- exactly the way a task still backing off
// after a failure is passed over (retryEligible).
//
// It exists for one thing the store cannot answer on its own: a run's
// row is finished (Store.FinishRun, inside RunDispatch) a moment before
// the caller has turned that run's result into the effects it implies --
// the observation that says the task completed, the pull request it
// opened. In between, task_state sees no live run and no completion, so
// the task reads 'queued' and this package would dispatch it a second
// time, running the same work twice over. Nothing was ever wrong with
// the view: what it lacks is knowledge that only the process still
// holding that result has, which is what this option lets that process
// supply (orchestrator.InFlight.Busy).
//
// It closed no gap while a cycle waited for the runs it started -- there
// was no tick to fall in that window -- and it is required now that a
// run outlives its cycle (orchestrator.InFlight's own doc comment).
//
// A caller that passes this is saying only "not yet, not from me": a
// task passed over here is dispatched by a later cycle as soon as busy
// stops saying so, on whatever the store says about it then -- and on
// what the store says *then*, not on what it said at the top of the
// cycle, which is what stillReady is for. The two are one guard: busy
// covers the window while the result is being applied, and the readiness
// re-read covers a cycle that entered that window before it and left it
// after it.
func Busy(busy func(taskID string) bool) Option {
	return func(o *options) { o.busy = busy }
}

// Paused tells Cycle to start nothing at all, except the tasks exempt
// names -- the deployment-wide pause (model.PipelinePause) as this
// package sees it.
//
// It is not a scheduling policy either, by the same reading as Busy: a
// paused deployment is not choosing between tasks, it is declining to
// pick any up. What it adds over the caller simply not calling Cycle is
// the exemption, and the exemption is the whole point. grain pauses
// itself to check an upgrade it has just installed, and the check is a
// task like any other: it has to be dispatched by the very cycle that is
// dispatching nothing else, so "paused" has to be expressible here,
// where the ready list is read, rather than as a caller's early return.
//
// A task passed over here is passed over for as long as the pause lasts
// and dispatched by whichever later cycle finds the pause gone. Nothing
// about it is spent -- no capacity, no attempt, no place in its own
// backoff -- which is what makes a pause cost a deployment time and
// nothing else.
//
// Tasks with priority dispatch are passed over too unless the caller
// also passes PriorityPassesPause, which it does under every pause but an
// upgrade's own hold.
func Paused(exempt ...string) Option {
	allowed := make(map[string]bool, len(exempt))
	for _, id := range exempt {
		allowed[id] = true
	}
	return func(o *options) { o.paused, o.exempt = true, allowed }
}

// RunNowPassesPause lets a run-now request through Paused: the cycle
// starts what somebody asked to run now and nothing else -- "manual
// mode", an operator pausing the deployment to hand-pick its work
// (grain/task-399).
//
// It is a separate option rather than Paused's own behaviour because not
// every pause may be passed. One an upgrade takes is shutting the queue
// in front of a restart that would discard any run started now, and a
// run started under it is lost whoever asked for it; the caller, which
// knows which pause this is, leaves this off for that one
// (orchestrator's own wiring). Nothing else is passed: PausedFor, Hold
// and Busy are not the deployment declining work but a provider, a
// policy or this process refusing a task, and capacity is spent as
// usual. A run-now request for a task with priority dispatch is let
// through by PriorityPassesPause as well, so under every pause but an
// upgrade's hold such a task starts whether or not anybody asked for it.
func RunNowPassesPause() Option {
	return func(o *options) { o.runNowPasses = true }
}

// PriorityPassesPause lets tasks with priority dispatch -- the tasks
// Store.ReadyPriorityDispatch names, console sessions and holders of
// model.PriorityDispatchCapability, ready or asked for by a run-now
// request -- through Paused, the same exception to the ordinary limits
// they already are (see the package comment): an operator can always
// reach the deployment, and a paused one is exactly where they are most
// likely to need to; the job that manages the pipeline is most needed
// while it is held. The pause grain takes to check its own upgrade is
// one of those.
//
// The caller leaves it off for the same pause it leaves
// RunNowPassesPause off for, an upgrade's own hold, and for the same
// reason: the restart would end any run started under it. A task held
// then is skipped with a reason that says so. Like RunNowPassesPause, it
// passes only Paused -- PausedFor, Hold and Busy hold such a task as they
// hold anything else.
func PriorityPassesPause() Option {
	return func(o *options) { o.priorityPasses = true }
}

// PausedFor is Paused for a pause that covers part of the queue rather
// than all of it: Cycle passes over every task covered reports true for
// and starts the rest as usual.
//
// The pause that needs this is the agent's own usage limit
// (orchestrator.Pause). It used to stop the whole deployment, which is
// exactly right for one running a single credential and wrong for one
// running several: one team's exhausted key says nothing about another
// team's, and stopping their dispatch over it costs a second outage on
// every one of them. So the caller decides, per task, whether the pause
// covers it -- it is the only side that knows which credential a task is
// dispatched onto -- and this package still knows nothing about
// frameworks or connections.
//
// Two options rather than a predicate for both, because the two pauses
// differ in what they mean and not just in whom they cover: Paused is a
// deployment declining to pick work up at all, with an exemption for the
// one task the pause is waiting on, and this is a provider refusing to
// serve some of it. A cycle can be under both at once, and a task under
// either is passed over.
//
// A task passed over here is passed over for as long as its own
// credential's window lasts and dispatched by whichever later cycle finds
// that window reset. Nothing about it is spent -- no capacity, no
// attempt, no place in its own backoff -- exactly as under Paused.
func PausedFor(covered func(taskID string) bool) Option {
	return func(o *options) { o.covered = covered }
}

// Hold passes over every task held gives a reason for, the way PausedFor
// does and with the same cost to the task -- none: it stays queued and
// the first cycle that finds it no longer held starts it. Separate from
// PausedFor because it is no pause: the orchestrator holds a task whose
// team has no GitHub credential to clone and push with, since there is
// no deployment credential its sandbox could fall back to
// (grain/task-1592), and starting it would only spend an attempt on a
// clone the git proxy is bound to refuse.
//
// held returns "" for a task it does not hold, and otherwise the reason
// it does ("held by local policy: ..."), which the skip is logged with: a log saying only that a task
// was passed over leaves its operator to guess which of the holds it was
// (grain/task-4501).
func Hold(held func(taskID string) string) Option {
	return func(o *options) { o.held = held }
}

func newOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// ConnectionPausedWhy is skip's reason for a task a usage-limit pause
// covers (PausedFor), and the quota wait recorded for it where the pause
// itself has not written a fuller one -- which is how a reader of that
// wait (orchestrator.TaskReason) tells it from a wait for capacity.
const ConnectionPausedWhy = "its provider connection is paused"

// pausedForUpgrade is the skip a task with priority dispatch is given
// under a pause PriorityPassesPause was left off for, which is only ever
// an upgrade's own hold: "paused" alone would leave an operator wondering
// why the one kind of task a pause never holds is being held.
const pausedForUpgrade = "dispatch is paused: held by an upgrade, whose restart would end the run"

// skip reports why taskID is one the caller has asked Cycle to pass over
// this time round, or "" when it is not. priority is true for a task with
// priority dispatch (Store.ReadyPriorityDispatch), which
// PriorityPassesPause lets past the pause.
func (o options) skip(taskID string, priority bool) string {
	return o.skipFor(taskID, false, priority)
}

// skipFor is skip for a task that may have been asked for by name:
// requested is true for a run-now request, which RunNowPassesPause lets
// past the pause and past nothing else.
func (o options) skipFor(taskID string, requested, priority bool) string {
	if o.paused && !o.exempt[taskID] && !(requested && o.runNowPasses) && !(priority && o.priorityPasses) {
		if priority {
			return pausedForUpgrade
		}
		return "dispatch is paused"
	}
	if o.covered != nil && o.covered(taskID) {
		return ConnectionPausedWhy
	}
	if o.held != nil {
		if why := o.held(taskID); why != "" {
			return why
		}
	}
	if o.busy != nil && o.busy(taskID) {
		return "busy: this process is still finishing its last run"
	}
	return ""
}

// Cycle is one pass: start the next ready, backed-off tasks in
// task_ready's own order until limits admits no more of either kind, and
// start nothing else. It is the entire dispatch decision for now — no
// polling, no completion detection, no side effect beyond the store
// writes StartRun already makes durable.
//
// limits is the whole limit, not the remaining headroom. Cycle works out
// how much of it is already spent itself, from the store, on every call,
// rather than trusting a caller's idea of it left over from the last one
// — a run can finish and free capacity with nothing about this cycle's
// caller changing, the same "re-read, never pin" discipline IsBlocked's
// docstring argues for and for the same reason.
//
// Those counts are read once, up front, and then spent down as this loop
// starts runs; they are not the thing that enforces the limit. StartRun
// re-checks them inside the transaction that records each run, and
// returns model.ErrAtCapacity if another caller took the last of the
// headroom in between -- which Cycle treats as "no more room this tick"
// and stops on, returning what it did manage to start. See StartRun's own
// doc comment: the check has to happen there to be a check at all, and
// this one exists only to avoid asking for capacity that is obviously not
// there.
//
// Which ordinary limit a ready task spends is model.Limits' own
// distinction: a merge-queue fix task is a merger (Store.ReadyMergers),
// everything else a worker, except tasks with priority dispatch
// identified by Store.ReadyPriorityDispatch, which spend neither. A candidate whose own kind is full is passed
// over rather than stopping the cycle, exactly the way one still backing
// off is, since the other kind may still have room and the ready order is
// not a queue of interchangeable items any more. The cycle stops when
// nothing of either kind could start.
//
// limits' counts are a ceiling this *caller* imposes, and grain's own
// daemon imposes none: no team has a count of its own either, and what
// bounds the deployment is the resource budget in
// limits.Budget held against the live ledger in limits.Ledger
// (grain/task-28). Counts that bound nothing (model.Limits.Unlimited --
// the zero value) therefore mean "no ceiling of mine", which is what
// StartRun has always read them as; this used to read the same state as
// "nothing may start", which was right while max_workers was a
// deployment-wide setting and is wrong now that there is no such setting
// to be unset.
//
// The budget is asked per candidate rather than once, because it moves
// while a cycle runs: each run this cycle starts charges its shape to the
// ledger, and so does every CI job admitted alongside it.
//
// A task already running never appears in Ready — task_ready requires
// state = 'queued', and a live run makes the state 'running' — so Cycle
// needs no check of its own to avoid dispatching one twice; that
// invariant lives in the view, not here. A task still backing off after a
// failed run does appear in Ready (task_ready has no notion of time), so
// Cycle itself skips it via retryEligible -- without consuming the
// capacity it would otherwise have taken, so a task further down the
// ready order is not made to wait behind one that is not actually
// eligible yet.
//
// What the view cannot do on its own is stay true for the length of a
// cycle: Ready is read once, up front, and a run of a candidate further
// down that list can end while the cycle is still working through it.
// Each candidate's readiness is therefore re-read immediately before its
// run is recorded (stillReady, whose doc comment has the interleaving),
// so a task the caller finished with mid-cycle is passed over rather
// than started a second time.
func Cycle(ctx context.Context, store *model.Store, limits model.Limits, now time.Time, opts ...Option) (out []Dispatch, retErr error) {
	o := newOptions(opts)
	waitingOnSandbox := false
	var waitingShape model.Commitment
	noteWaiting := func(want model.Commitment) {
		if !waitingOnSandbox {
			waitingOnSandbox = true
			waitingShape = want
		}
	}
	defer func() {
		if retErr == nil {
			if waitingOnSandbox {
				if limits.Ledger != nil {
					limits.Ledger.SetAgentDemand(true, waitingShape)
				}
			} else {
				limits.SetAgentWaiting(false)
			}
		}
	}()
	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("dispatch: counting live runs: %w", err)
	}

	requested, err := store.RunNow(ctx)
	if err != nil {
		return nil, fmt.Errorf("dispatch: reading run-now requests: %w", err)
	}

	// The run-now requests go first, and they are the one thing here that
	// does not read task_ready at all: see Store.RunNow, and RunNowAt for
	// what a request is. Everything below this loop is unchanged by it --
	// a request that could not be started (no capacity, a pause, a task
	// this process is still finishing with) stays outstanding and the
	// next cycle tries it again.
	started := make(map[string]bool, len(requested))
	repairs := &repairBases{store: store}
	liveDurable := 0
	liveDurableRead := false
	ceilingRaised := false
	for _, req := range requested {
		if why := o.skipFor(req.TaskID, true, req.Priority); why != "" {
			recordSkipQuotaWait(ctx, store, req.TaskID, why, now)
			continue
		}
		if ok, err := runNowEligible(ctx, store, req, now); err != nil {
			return out, fmt.Errorf("dispatch: %w", err)
		} else if !ok {
			continue
		}
		want := taskShape(ctx, store, limits, req.TaskID)
		repair := false
		durable := false
		if req.Priority && limits.Durable > 0 {
			if !liveDurableRead {
				if liveDurable, err = store.LiveDurableRuns(ctx); err != nil {
					return out, fmt.Errorf("dispatch: %w", err)
				}
				liveDurableRead = true
			}
			if durable, err = store.IsDurable(ctx, req.TaskID); err != nil {
				return out, fmt.Errorf("dispatch: %w", err)
			}
			if durable && liveDurable >= limits.Durable {
				setQuotaWait(ctx, store, req.TaskID, durableCeilingReason(limits.Durable), now)
				if !ceilingRaised {
					ceilingRaised = true
					raiseDurableCeiling(req.TaskID, liveDurable, limits.Durable)
				}
				continue
			}
		}
		if req.Priority {
			if !limits.DirectorSandboxVerdictFor(want).OK {
				if sandboxShapeShortFor(limits, true, want) {
					noteWaiting(want)
				}
				setQuotaWait(ctx, store, req.TaskID, capacityReasonFor(limits, live, req.Merger, true, want), now)
				continue
			}
		} else {
			if limits.Admits(live, req.Merger) && !limits.SandboxAdmitsShape(want) {
				var wait string
				repair, wait = repairAdmission(ctx, store, limits, repairs, o.preempt, req.TaskID, want)
				if wait != "" {
					setQuotaWait(ctx, store, req.TaskID, wait, now)
					continue
				}
			}
			if !repair && (!limits.Admits(live, req.Merger) || !limits.SandboxAdmitsShape(want)) {
				if limits.Admits(live, req.Merger) && sandboxShapeShortFor(limits, false, want) {
					noteWaiting(want)
				}
				setQuotaWait(ctx, store, req.TaskID, capacityReasonFor(limits, live, req.Merger, false, want), now)
				continue
			}
		}
		d, admitted, err := startTask(ctx, store, req.TaskID, now, limits, o.admit, req.Priority, repair)
		if err != nil {
			// Unlike the ready loop below, a capacity refusal here is
			// passed over rather than ending the cycle: this list is in
			// the order the requests were made, not the order capacity
			// is spent in, so the next entry may well be of the kind
			// there is still room for -- and the ready list behind it
			// certainly may be.
			if errors.Is(err, model.ErrDurableCeiling) {
				setQuotaWait(ctx, store, req.TaskID, durableCeilingReason(limits.Durable), now)
				if !ceilingRaised {
					ceilingRaised = true
					raiseDurableCeiling(req.TaskID, limits.Durable, limits.Durable)
				}
				continue
			}
			if errors.Is(err, model.ErrAtCapacity) {
				if (req.Priority || limits.Admits(live, req.Merger)) && sandboxShapeShortFor(limits, req.Priority, want) {
					noteWaiting(want)
				}
				setQuotaWait(ctx, store, req.TaskID, capacityReasonFor(limits, live, req.Merger, req.Priority, want), now)
				continue
			}
			return out, fmt.Errorf("dispatch: starting requested run for %s: %w", req.TaskID, err)
		}
		if !admitted {
			continue
		}
		started[req.TaskID] = true
		repairs.started(req.TaskID)
		out = append(out, d)
		if durable {
			liveDurable++
		}
		if req.Priority {
			live = live.AddDirector()
		} else {
			live = live.Add(req.Merger)
		}
	}

	ready, err := store.Ready(ctx)
	if err != nil {
		return out, fmt.Errorf("dispatch: reading ready candidates: %w", err)
	}
	if len(ready) == 0 {
		return out, nil
	}

	mergerIDs, err := store.ReadyMergers(ctx)
	if err != nil {
		return out, fmt.Errorf("dispatch: reading ready merge-queue tasks: %w", err)
	}
	mergers := make(map[string]bool, len(mergerIDs))
	for _, id := range mergerIDs {
		mergers[id] = true
	}
	priorityIDs, err := store.ReadyPriorityDispatch(ctx)
	if err != nil {
		return out, fmt.Errorf("dispatch: reading the ready tasks with priority dispatch: %w", err)
	}
	priorities := make(map[string]bool, len(priorityIDs))
	for _, id := range priorityIDs {
		priorities[id] = true
	}

	// Prefer priority dispatch and repairs/mergers over taking new work,
	// while preserving backlog order (Ready's own) within each group.
	var priorityCandidates, mergerCandidates, ordinary []string
	for _, id := range ready {
		if started[id] {
			continue
		}
		if priorities[id] {
			priorityCandidates = append(priorityCandidates, id)
		} else if mergers[id] {
			mergerCandidates = append(mergerCandidates, id)
		} else {
			ordinary = append(ordinary, id)
		}
	}

	allCandidates := append(priorityCandidates, append(mergerCandidates, ordinary...)...)

	// Durable tasks are priority candidates held to one count of their
	// own, Limits.Durable (docs/design/durable-tasks.md#DT-1). Read once
	// and spent down, like live; StartRun is what enforces it.
	if limits.Durable > 0 && len(priorityCandidates) > 0 && !liveDurableRead {
		if liveDurable, err = store.LiveDurableRuns(ctx); err != nil {
			return out, fmt.Errorf("dispatch: %w", err)
		}
		liveDurableRead = true
	}

	const maxCapacityLogsPerCycle = 3
	var skippedWorkerMergerCapacity int
	defer func() {
		if skippedWorkerMergerCapacity > maxCapacityLogsPerCycle {
			log.Printf("dispatch: skipped %d additional candidates: no capacity for worker/merger", skippedWorkerMergerCapacity-maxCapacityLogsPerCycle)
		}
	}()

	for _, candidate := range allCandidates {

		// A task the loop above started is running now, whatever the
		// ready list read a moment ago said about it.
		if started[candidate] {
			continue
		}

		priority := priorities[candidate]
		merger := mergers[candidate]

		eligible, err := retryEligible(ctx, store, candidate, now, priority)
		if err != nil {
			return out, fmt.Errorf("dispatch: %w", err)
		}
		if !eligible {
			clearQuotaWait(ctx, store, candidate)
			log.Printf("dispatch: candidate %s skipped: backing off after failed run", candidate)
			continue
		}
		if why := o.skip(candidate, priority); why != "" {
			recordSkipQuotaWait(ctx, store, candidate, why, now)
			log.Printf("dispatch: candidate %s skipped: %s", candidate, why)
			continue
		}
		still, err := stillReady(ctx, store, candidate)
		if err != nil {
			return out, err
		}
		if !still {
			clearQuotaWait(ctx, store, candidate)
			log.Printf("dispatch: candidate %s skipped: no longer ready", candidate)
			continue
		}
		want := taskShape(ctx, store, limits, candidate)
		repair := false
		durable := false
		if priority && limits.Durable > 0 {
			if durable, err = store.IsDurable(ctx, candidate); err != nil {
				return out, fmt.Errorf("dispatch: %w", err)
			}
			if durable && liveDurable >= limits.Durable {
				reason := durableCeilingReason(limits.Durable)
				setQuotaWait(ctx, store, candidate, reason, now)
				if !ceilingRaised {
					ceilingRaised = true
					raiseDurableCeiling(candidate, liveDurable, limits.Durable)
				}
				continue
			}
		}
		if priority {
			if !limits.DirectorSandboxVerdictFor(want).OK {
				if sandboxShapeShortFor(limits, true, want) {
					noteWaiting(want)
				}
				reason := capacityReasonFor(limits, live, merger, true, want)
				setQuotaWait(ctx, store, candidate, reason, now)
				if skippedWorkerMergerCapacity < maxCapacityLogsPerCycle {
					log.Printf("dispatch: candidate %s skipped: no sandbox for priority dispatch -- %s",
						candidate, reason)
				}
				skippedWorkerMergerCapacity++
				continue
			}
		} else {
			if limits.Admits(live, merger) && !limits.SandboxAdmitsShape(want) {
				var wait string
				repair, wait = repairAdmission(ctx, store, limits, repairs, o.preempt, candidate, want)
				if wait != "" {
					setQuotaWait(ctx, store, candidate, wait, now)
					continue
				}
			}
			if !repair && (!limits.Admits(live, merger) || !limits.SandboxAdmitsShape(want)) {
				if limits.Admits(live, merger) && sandboxShapeShortFor(limits, false, want) {
					noteWaiting(want)
				}
				reason := capacityReason(limits, live, merger)
				if want != limits.AgentShape {
					reason = capacityReasonFor(limits, live, merger, false, want)
				}
				setQuotaWait(ctx, store, candidate, reason, now)
				if skippedWorkerMergerCapacity < maxCapacityLogsPerCycle {
					log.Printf("dispatch: candidate %s skipped: no capacity for worker/merger -- %s",
						candidate, reason)
				}
				skippedWorkerMergerCapacity++
				continue
			}
		}

		d, admitted, err := startTask(ctx, store, candidate, now, limits, o.admit, priority, repair)
		if err != nil {
			if errors.Is(err, model.ErrDurableCeiling) {
				setQuotaWait(ctx, store, candidate, durableCeilingReason(limits.Durable), now)
				if !ceilingRaised {
					ceilingRaised = true
					raiseDurableCeiling(candidate, limits.Durable, limits.Durable)
				}
				continue
			}
			if errors.Is(err, model.ErrAtCapacity) {
				if (priority || limits.Admits(live, merger)) && sandboxShapeShortFor(limits, priority, want) {
					noteWaiting(want)
				}
				setQuotaWait(ctx, store, candidate, capacityReasonFor(limits, live, merger, priority, want), now)
				log.Printf("dispatch: candidate %s skipped: at capacity during start", candidate)
				continue
			}
			return out, fmt.Errorf("dispatch: starting run for %s: %w", candidate, err)
		}
		if !admitted {
			log.Printf("dispatch: candidate %s skipped: admission refused", candidate)
			continue
		}
		started[candidate] = true
		repairs.started(candidate)
		out = append(out, d)
		if durable {
			liveDurable++
		}
		if priority {
			live = live.AddDirector()
		} else {
			live = live.Add(merger)
		}
	}
	return out, nil
}

// durableCeilingReason is the wait recorded on a durable task the
// durable-run ceiling holds back.
func durableCeilingReason(ceiling int) string {
	return fmt.Sprintf("the durable-run ceiling is full: %d durable runs are live (max_durable_runs)", ceiling)
}

// raiseDurableCeiling says, once a cycle, that a durable task could not
// start because the ceiling is full. The design has this raise the
// operator notification model.DurableCeilingSubject
// (docs/design/durable-tasks.md#DT-5); until notifications exist
// (DT-S6) it is logged under that subject. The ceiling is the
// deployment's, and no team holds a count of its own, so the subject
// names the default team.
func raiseDurableCeiling(taskID string, live, ceiling int) {
	log.Printf("dispatch: %s: durable task %s cannot start: %d of %d durable runs are live",
		model.DurableCeilingSubject(model.DefaultTeamID), taskID, live, ceiling)
}

func taskShape(ctx context.Context, store *model.Store, limits model.Limits, taskID string) model.Commitment {
	if limits.AgentShape.IsZero() || store == nil || taskID == "" {
		return limits.AgentShape
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return limits.AgentShape
	}
	return limits.TaskShape(*task)
}

// sandboxShapeShort reports whether an agent sandbox cannot fit in the
// host resource budget because of its CPU/memory shape rather than a
// host-wide disk floor violation.
func sandboxShapeShort(limits model.Limits) bool {
	return sandboxShapeShortFor(limits, false, limits.AgentShape)
}

func sandboxShapeShortFor(limits model.Limits, priority bool, want model.Commitment) bool {
	if priority {
		if limits.DirectorSandboxVerdictFor(want).OK {
			return false
		}
	} else if limits.SandboxAdmitsShape(want) {
		return false
	}
	return limits.Budget.Admits(limits.LiveCommitment(), model.Commitment{}).OK
}

// stillReady is the last look before a run is recorded: whether taskID
// is *still* what the ready list said it was when this cycle read it
// (Store.IsReady).
//
// A cycle reads task_ready once and then spends real time on each
// candidate -- a busy check, a streak read, a run started for whatever
// came earlier in the order -- and the run of a candidate can end during
// any of it. When it does, the store's answer changes twice in quick
// succession: the run's row is finished first (the task reads 'queued'
// again, which is the window Busy covers), and the completion its result
// implies is recorded a moment later, after which the task is not ready
// at all. A cycle that read the list before the second write and asked
// busy after the caller had finished with the task saw "queued" and "not
// busy" -- both true when they were read, neither true together -- and
// started a second run of a task that was already done.
//
// Ordering is the whole of why this works: busy going false is the
// caller saying it has finished turning that run's result into what it
// implies, so a readiness read taken *after* that (this one, at the end
// of the loop body, rather than the bulk read at the top) sees every
// effect of the run. What is left is the ordinary race any two processes
// share -- a human closing the task in the instant before StartRun's own
// transaction -- which is not this one, and which costs a run nobody
// asked for rather than a duplicate of one already made.
func stillReady(ctx context.Context, store *model.Store, taskID string) (bool, error) {
	ready, err := store.IsReady(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("dispatch: re-reading whether %s is still ready: %w", taskID, err)
	}
	return ready, nil
}

// capacityReason is the one clause a "no capacity" log line adds: which
// of the two bounds is holding this candidate. The budget's own words
// where the machine is what is short, since that sentence names the host,
// the reserve and the ratio; the counts otherwise.
func capacityReason(limits model.Limits, live model.RunCounts, merger bool) string {
	return capacityReasonFor(limits, live, merger, false, limits.AgentShape)
}

func capacityReasonFor(limits model.Limits, live model.RunCounts, merger, priority bool, want model.Commitment) string {
	if priority {
		if verdict := limits.DirectorSandboxVerdictFor(want); !verdict.OK {
			return verdict.Reason
		}
	} else if verdict := limits.SandboxVerdictFor(want); !verdict.OK {
		return verdict.Reason
	}
	if merger {
		return fmt.Sprintf("%d worker and %d merger runs are live of the %d this cycle allows",
			live.Workers, live.Mergers, limits.AgentRuns())
	}
	// Admits refuses a worker on the shared total as well as on the worker
	// count, and mergers may take the whole pool: name that, or the line
	// reads "0 of 3" with nothing starting.
	if live.Workers < limits.Workers && live.OrdinaryTotal() >= limits.AgentRuns() {
		return fmt.Sprintf("%d worker and %d merger runs are live of the %d this cycle allows (mergers fill the shared pool)",
			live.Workers, live.Mergers, limits.AgentRuns())
	}
	return fmt.Sprintf("%d worker runs are live of the %d this cycle allows", live.Workers, limits.Workers)
}

func setQuotaWait(ctx context.Context, store *model.Store, taskID, reason string, at time.Time) {
	if store == nil || taskID == "" || reason == "" {
		return
	}
	if existing, err := store.GrantWaitOf(ctx, taskID); err == nil && existing != nil &&
		existing.Capability != model.QuotaWaitCapability {
		return
	}
	_ = store.SetGrantWait(ctx, model.GrantWait{
		TaskID:     taskID,
		Capability: model.QuotaWaitCapability,
		Resource:   reason,
		WaitingAt:  at,
	})
}

func clearQuotaWait(ctx context.Context, store *model.Store, taskID string) {
	if store == nil || taskID == "" {
		return
	}
	if existing, err := store.GrantWaitOf(ctx, taskID); err == nil && existing != nil &&
		existing.Capability == model.QuotaWaitCapability {
		_ = store.ClearGrantWait(ctx, taskID)
	}
}

func recordSkipQuotaWait(ctx context.Context, store *model.Store, taskID, why string, at time.Time) {
	if store == nil || taskID == "" {
		return
	}
	if why != ConnectionPausedWhy && why != pausedForUpgrade {
		clearQuotaWait(ctx, store, taskID)
		return
	}
	if existing, err := store.GrantWaitOf(ctx, taskID); err == nil && existing != nil &&
		existing.Capability == model.QuotaWaitCapability && existing.Resource != "" {
		return
	}
	setQuotaWait(ctx, store, taskID, why, at)
}

// startTask records taskID's next run (Store.Attempts + 1) and starts
// it, against limits the same way StartRun itself interprets them --
// limits that bound no count disable that check entirely. Kept apart from
// Cycle's own loop so the attempt bookkeeping reads as one thing.
//
// It is also where the run's sandbox is charged to the deployment's
// ledger (model.Limits.ChargeSandbox), immediately before the row that
// records it: the charge and the budget test happen together under the
// ledger's own lock, so two cycles -- or a cycle and a CI job -- racing
// for the last of the machine cannot both be given it. A charge whose
// StartRun then fails is given straight back; one whose run is recorded
// is held until whoever finishes the run releases it by id
// (model.Ledger.Release), since a run outlives the cycle that started it.
//
// priority is a task with priority dispatch, whose sandbox is admitted
// under the exception policy console sessions' are. repair is a
// base-branch repair the budget's reserve was opened to
// (repairAdmission): its sandbox is charged against the whole budget
// rather than the part of it ordinary work may spend.
func startTask(ctx context.Context, store *model.Store, taskID string, now time.Time, limits model.Limits, admit Admitter, priority, repair bool) (Dispatch, bool, error) {
	attempts, err := store.Attempts(ctx, taskID)
	if err != nil {
		return Dispatch{}, false, fmt.Errorf("counting attempts for %s: %w", taskID, err)
	}
	attempt := attempts + 1
	candidate := Candidate{TaskID: taskID, RunID: RunID(taskID, attempt), Attempt: attempt}
	admission := Admission{Admitted: true}
	if admit != nil {
		clearQuotaWait(ctx, store, taskID)
		admission, err = admit(ctx, candidate)
		if err != nil {
			return Dispatch{}, false, err
		}
		if !admission.Admitted {
			return Dispatch{}, false, nil
		}
	}
	// After the admitter, so that a candidate declined for want of a
	// capability grant never spends a byte of the budget while it waits.
	want := taskShape(ctx, store, limits, taskID)
	var verdict model.BudgetVerdict
	var releaseSandbox func()
	if priority {
		verdict, releaseSandbox = limits.AdmitDirectorSandboxShape(candidate.RunID, want)
	} else if repair {
		verdict, releaseSandbox = limits.ChargeRepairSandboxShape(candidate.RunID, want)
	} else {
		verdict, releaseSandbox = limits.ChargeSandboxShape(candidate.RunID, want)
	}
	if !verdict.OK {
		if admission.Release != nil {
			admission.Release()
		}
		return Dispatch{}, false, fmt.Errorf("%w: %s", model.ErrAtCapacity, verdict.Reason)
	}
	// Sandbox is deliberately left empty: no sandbox exists for this run
	// yet, and inventing a name here for one nothing has built would be a
	// claim this package is in no position to make. The orchestrator
	// acquires one and records it (Store.SetRunSandbox).
	run := model.Run{
		ID:        candidate.RunID,
		TaskID:    taskID,
		Attempt:   attempt,
		StartedAt: now,
		Leases:    admission.Leases,
	}
	if err := store.StartRun(ctx, run, limits); err != nil {
		releaseSandbox()
		if admission.Release != nil {
			admission.Release()
		}
		return Dispatch{}, false, err
	}
	return Dispatch{TaskID: taskID, RunID: run.ID, Attempt: attempt}, true, nil
}
