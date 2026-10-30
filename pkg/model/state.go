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
	"sort"
	"time"
)

// MaxConsecutiveFailures is how many of a task's most recent runs, in a
// row, may end without succeeding before StateOf stops calling it
// 'queued' and starts calling it 'failed' -- see StateFailed and
// task_streak (schema.go), which apply the same cutoff to
// task_run.outcome so a task cannot be retried forever with no cap and
// no visible signal (bwsalmon/agents#403).
//
// A fixed constant rather than a grain_config knob: the tasks this
// bounds are already the ones producing nothing useful attempt after
// attempt, so getting the exact number right matters far less than
// having a number at all. Fold it into grain_config the day an operator
// actually needs a different one.
const MaxConsecutiveFailures = 5

// PausedOutcome is the task_run.outcome of a run grain itself stopped
// because the agent's own usage limit was reached -- the credential it
// runs as having no budget left in its current window
// (agent.UsageLimitError, orchestrator.Pause). One word, named here
// rather than in the package that writes it, because two readers have to
// agree on it: computeStreakForTask (the count task_state reads) and
// Store.FailureStreak, both of which skip it.
//
// Skipping is the whole reason it is not simply "cancelled". Every other
// non-"succeeded" ending is evidence about the task -- its agent failed,
// its sandbox would not build, its result could not be turned into a
// pull request -- and counting those toward MaxConsecutiveFailures is
// what stops a task being retried forever. A usage limit is evidence
// about the deployment: it says nothing whatsoever about the task, it
// arrives at whatever tasks happen to be running when the window runs
// out, and it would otherwise spend the retry budget of every one of
// them on an outage none of them caused. The same argument
// orchestrator.outcomeOf makes for no longer reading an errored tool
// call as a failed run.
const PausedOutcome = "paused"

// CapacityOutcome is the task_run.outcome of a run the agent's provider
// refused for want of capacity -- an overloaded model, a 503, a gateway
// between the CLI and the model that gave up (agent.CapacityError). One
// word, named here for the same reason PausedOutcome is:
// computeStreakForTask and Store.FailureStreak both have to agree on it,
// and both skip it.
//
// Skipped by the same argument, and stopping in a different place. A
// capacity refusal is evidence about the provider and none about the
// task, so counting it toward MaxConsecutiveFailures would retire tasks
// that were never wrong -- and it would do it to whichever tasks happened
// to be running during the outage, which is nobody's doing. But it is
// unlike a usage limit in what it deserves in return: a window that
// reopens in hours is worth stopping dispatch onto that credential for
// (orchestrator.Pause), and a model that is overloaded for thirty seconds
// is not. Nothing pauses for this. The run is simply not held against its
// task, and dispatch's own retry backoff -- which counts this outcome,
// see FailureStreak.BackoffCount -- carries the retry.
const CapacityOutcome = "capacity"

// InterruptedOutcome is the task_run.outcome of a run that was stopped
// because the daemon driving it was interrupted -- during a graceful
// upgrade drain, process shutdown, or restart. Like PausedOutcome and
// CapacityOutcome, it is skipped by computeStreakForTask and
// Store.FailureStreak: stopping a daemon is evidence about the deployment's
// lifecycle and none at all about the task, so it must not penalise the
// task's failure streak or count toward MaxConsecutiveFailures.
const InterruptedOutcome = "interrupted"

// EnvironmentOutcome is the task_run.outcome of a run that could not
// start its agent, or a tool it needed, because something on the host
// running it needs a human -- today one thing: the account's host
// credentials have expired, so a tool it needs will not start
// (authfix.Error, with the fix a registered classifier names). Like the three above it is
// skipped by computeStreakForTask and Store.FailureStreak: it is evidence
// about the host and none about the task, and every task dispatched while
// it lasts would otherwise be retired for it. The task is parked on a
// comment saying what to run (orchestrator.stopOnExpiredHostCredentials),
// so nothing re-dispatches it into the same refusal until somebody
// replies, and BackoffCount is not incremented either.
const EnvironmentOutcome = "environment"

// OOMOutcome is the task_run.outcome of a run whose sandbox the kernel
// OOM-killed: the granule VM's process was killed or its guest kernel
// reported an OOM kill, a container exited with docker's OOMKilled set,
// or a pod's container status reads OOMKilled
// (orchestrator.SandboxOOMReporter). Under memory overcommit
// (docs/design/auto-overcommit.md) that is a risk grain takes on
// purpose, trading a few killed sandboxes for more concurrent work, so
// like InterruptedOutcome it is evidence about the deployment and none
// about the task: computeStreakForTask and Store.FailureStreak skip it,
// and the task is queued again to resume its conversation.
//
// Not forever. Store.ConsecutiveOOMRuns counts them, the second asks
// for a bigger sandbox (orchestrator.raiseMemoryAfterOOM), and a run
// past MaxOOMRetries is recorded as an ordinary failure instead, so a
// task that genuinely needs more memory than the host has still reaches
// MaxConsecutiveFailures rather than being retried for ever.
const OOMOutcome = "oom"

// MaxOOMRetries is how many OOM kills in a row a task's runs may end in
// and still be recorded as OOMOutcome. The next is an ordinary failure.
const MaxOOMRetries = 3

// OOMDetailPrefix starts the detail of every OOMOutcome run, and is the
// sentence the conversation note opens with too.
const OOMDetailPrefix = "sandbox was OOM-killed under memory overcommit; retrying"

// InterruptedDetail is the default explanation attached to an interrupted run.
const InterruptedDetail = "interrupted by daemon shutdown or upgrade; re-queued for next dispatch"

// StateOf computes a task's state. Never stored, never written.
//
// Order is precedence, not preference: a completed task whose issue was
// then closed reads closed, and a task with a live run reads running
// whatever its approval says.
//
// failureStreak is task_streak's own count (Store.FailureStreak.Count) --
// passed in rather than computed here because it takes a database query
// over task_run to answer, which is exactly what this function's own
// contract ("available to code holding a Task and no database") rules
// out doing itself.
//
// schema.go computes the same thing as a SQL view. The duplication is
// deliberate — a view is what makes the invariant structural in the
// store, and this is what makes it available to code holding a Task and
// no database — and state_test.go holds the two to the same precedence,
// because two implementations of one rule drift silently: the symptom
// would be a wrong state only when two conditions are true at once.
func StateOf(task Task, obs *Observation, activeRun bool, failureStreak int) State {
	if obs != nil {
		switch {
		case obs.ClosedAt != nil:
			return StateClosed
		case obs.CompletedAt != nil && AwaitsSubmit(task):
			return StateAwaitingSubmit
		case obs.CompletedAt != nil && AwaitsAck(task):
			return StateAwaitingReply
		case obs.CompletedAt != nil:
			return StateCompleted
		case obs.PendingQuestionCommentID != nil:
			return StateAwaitingReply
		}
	}
	if activeRun {
		return StateRunning
	}
	if failureStreak >= MaxConsecutiveFailures {
		return StateFailed
	}
	// Above both of the branches below, and below every branch above:
	// deferring is a declaration about what should happen next, so it
	// outranks the other two declaration-shaped states (a deferred task
	// neither queues nor waits to be approved) and never overrides
	// grain's own account of what already happened. A task cannot be
	// deferred into any of those states in the first place --
	// ui.Client.Defer refuses anything but proposed and queued -- so the
	// order only decides what a row with both facts on it reads as, and
	// "it ran" is the more honest answer there.
	if task.DeferredAt != nil {
		return StateDeferred
	}
	if task.Approval == nil {
		return StateProposed
	}
	return StateQueued
}

// AwaitsSubmit reports whether a task that has finished running is
// waiting on a human's Submit click rather than on the merge queue --
// the condition that separates StateAwaitingSubmit from StateCompleted.
// It says nothing about whether the run is over: StateOf is what pairs
// it with Observation.CompletedAt, and this deliberately never reads an
// Observation so a caller holding a Task alone can still ask.
//
// Two halves, both necessary. AutoMerge is what Submit sets
// (ui.Client.Submit reuses it rather than adding a second field), so an
// already-submitted task is on the queue and waiting on nobody. And a
// task with no LinkFixes pull request has nothing to submit at all --
// an analyze task that answered in a comment is finished, not parked,
// and offering "Submit" as its next step would name a button it will
// never grow.
//
// A changelist CL (LinkChangelist) is a task's change exactly as a pull
// request is (docs/design/changelists.md#G3-2): a finished task
// with a live CL waits on somebody's Submit, however many CLs it has.
//
// Nothing here asks whether the pull request is still open. A merged or
// closed one closes the task itself (orchestrator.recordPullRequestEvents
// sets ClosedAt), and StateClosed already outranks this in StateOf.
func AwaitsSubmit(task Task) bool {
	if task.AutoMerge {
		return false
	}
	for _, l := range task.Links {
		if l.Kind.IsChange() {
			return true
		}
	}
	return false
}

// AwaitsAck reports whether a task that has finished running is waiting
// on somebody reading what it said rather than on anything landing --
// the condition that separates StateAwaitingAck from StateCompleted. It
// is AwaitsSubmit's neighbour in every way: it says nothing about
// whether the run is over (StateOf is what pairs it with
// Observation.CompletedAt), and it reads the task alone so a caller
// holding no Observation can still ask.
//
// Two halves, both necessary. A task with no LinkFixes pull request has
// produced no branch to merge -- its run ended on a comment_on_issue
// call or on posted review findings (orchestrator.ProcessResult), which
// is words and nothing else -- so no queue is ever going to move it and
// the only ending it has is somebody closing it.
//
// And a review grain filed itself is excluded even when it left no pull
// request behind, because its findings are not delivered here: relayReviewFeedback
// posts them on the pull request under review and on the task that owns
// it, and takes that task off automatic merge so the wait lands there,
// on the change somebody has to decide about. Counting the review task
// as a second thing to acknowledge would put two rows in the inbox for
// one decision, one of them about a task whose whole output is on
// another task's page -- and grain files a review task per reviewed
// change (orchestrator.SyncReviews), so it would be the inbox's largest
// group and the least actionable. Such a task is not left sitting in
// 'completed' either, which is what it used to do and what nothing could
// ever move it out of: orchestrator.SyncReviews closes it once its run is
// over and it has nothing to land, so it reads 'closed', which is what it
// is.
//
// Origin.Reason, and only that -- a task that merely declares
// IntentReview is not one of these, however it describes itself. Grain's
// own reviews are filed as implement tasks (a review that finds something
// worth fixing pushes the fix) and are recognisable by nothing but the
// provenance, which is why this reads the origin; and the "delivered
// somewhere else" argument above holds for nothing else, because
// orchestrator.reviewedPullRequest looks for a pull request to attach
// findings to only for a task with that origin. A run that declared
// IntentReview and is not grain's review of a change has its findings
// relayed into its own conversation instead
// (orchestrator.relayFindingsToConversation), so its whole product is
// words on this task -- which is this state exactly. Excluding it left it
// reading 'completed', badged "Queued for merge", about a task with no
// pull request and no queue: the very reading this state and
// StateAwaitingSubmit were each split out to correct.
func AwaitsAck(task Task) bool {
	if task.Origin.Reason == ReasonReview {
		return false
	}
	for _, l := range task.Links {
		if l.Kind.IsChange() {
			return false
		}
	}
	return true
}

// Transition is one point where a task's derived state changed --
// StateOf's own answer, not a value anything stores.
type Transition struct {
	State State
	At    time.Time
	// Inferred marks an entry whose moment the record does not hold and
	// Transitions has stood in for with the nearest one it does: today
	// only a requeue after an attempt, placed at that attempt's own
	// FinishedAt when nothing recorded when the task actually went back
	// in front of dispatch. The state is certain; the time is a lower
	// bound, and a reader shown it should be told so.
	Inferred bool
	// By, Reason and Detail are set on a step somebody asked for -- a
	// close or a reopen (MergeTaskEvents): who, what they said, and what
	// grain did because of it. nil and "" on every step Transitions
	// derives from the record.
	By     *Principal
	Reason string
	Detail string
}

// Transitions reconstructs, as far as the record allows, when a task
// passed through each state in State's vocabulary -- the history behind
// StateOf's single snapshot, for a UI that wants to show "-> queued at
// 10:04, -> running at 10:06" rather than only the pill on top.
//
// Like StateOf, this reads what it is handed and touches no database;
// unlike StateOf, it is necessarily incomplete in one place. Observation
// only ever holds the *pending* question (Client.AddComment clears
// PendingQuestionCommentID the moment a human replies rather than
// keeping a record of the one that was just answered), so a past
// awaiting_reply period a task has already moved on from leaves no
// timestamp behind -- askedAt is that comment's own CreatedAt while the
// question is still outstanding, and nil once it is not, the same as
// Observation.PendingQuestionCommentID itself.
//
// It is approximate in another. A requeue between two attempts is proved
// by the second attempt existing, but nothing stamps the moment it
// happened: a question answered, a failure retried, a run that simply
// ended and went back in line. Where the record does hold that moment --
// Observation.RetryRequestedAt or Task.UndeferredAt falling between the
// attempt's finish and the next one's start (or after the last finish,
// for a task queued now) -- the requeue is placed there. Otherwise it is
// placed at the finished attempt's own FinishedAt and marked Inferred,
// because it happened then or later and the record cannot say which; a
// requeue that followed an answered question is the usual case, its true
// moment the reply's, which Observation does not keep either.
//
// runs must be oldest first, the order Store.Runs already returns, and
// streak must be the same one Store.FailureStreak would give this task
// right now. StateFailed's own moment is streak.LastFinishedAt rather
// than a replay of task_streak's reset rules against the run list: sound
// because task_ready never selects a failed task, so the streak that
// tripped StateFailed can never grow past the run that tripped it.
func Transitions(task Task, obs *Observation, runs []Run, streak *FailureStreak, askedAt *time.Time) []Transition {
	var out []Transition
	add := func(s State, at *time.Time) {
		if at != nil {
			out = append(out, Transition{State: s, At: *at})
		}
	}

	add(StateProposed, task.CreatedAt)
	add(StateQueued, task.ApprovedAt)

	// requeue adds the requeue that followed an attempt finishing at
	// finished, before next (nil for none yet). A recorded moment inside
	// that gap is the requeue itself; an undeferral there already gets an
	// entry of its own below, so it is not added twice.
	within := func(at *time.Time, finished time.Time, next *time.Time) bool {
		return at != nil && !at.Before(finished) && (next == nil || !at.After(*next))
	}
	requeue := func(finished time.Time, next *time.Time) {
		if within(task.UndeferredAt, finished, next) {
			return
		}
		if obs != nil && within(obs.RetryRequestedAt, finished, next) {
			add(StateQueued, obs.RetryRequestedAt)
			return
		}
		out = append(out, Transition{State: StateQueued, At: finished, Inferred: true})
	}

	activeRun := false
	for i := range runs {
		r := &runs[i]
		add(StateRunning, &r.StartedAt)
		if r.FinishedAt == nil {
			activeRun = true
			continue
		}
		// An attempt that was not the last one proves the task queued
		// again afterward -- nothing else could have dispatched the next
		// one. The last attempt's own finish is only a requeue if nothing
		// terminal claimed it, which StateOf (fed this same run list's
		// tail) already knows how to decide.
		if i < len(runs)-1 {
			requeue(*r.FinishedAt, &runs[i+1].StartedAt)
		}
	}

	failureCount := 0
	if streak != nil {
		failureCount = streak.Count
	}
	if current := StateOf(task, obs, activeRun, failureCount); current == StateQueued && len(runs) > 0 {
		if last := runs[len(runs)-1].FinishedAt; last != nil {
			requeue(*last, nil)
		}
	}

	add(StateAwaitingReply, askedAt)
	// Guarded the same way StateOf itself guards it: obs.CompletedAt and
	// obs.ClosedAt outrank a failure streak there, so once either is set
	// the task can never again read 'failed' no matter what task_streak
	// says. Transitions has to apply the same precedence rather than
	// adding StateFailed off streak.Count alone, because a task can reach
	// StateCompleted without task_streak ever resetting: a run salvaged
	// into a pull request after erroring keeps its own outcome "failed"
	// forever (orchestrator.salvagePushedBranch never corrects it -- see
	// orchestrator.RunCycle's "only the ending failed"), so streak.Count
	// can sit at or above MaxConsecutiveFailures permanently for a task
	// that has, in every other respect, completed. Left unguarded, every
	// later read of this task's timeline showed a bogus "Failed" entry
	// immediately before "Completed", forever, even though the task
	// plainly succeeded (bwsalmon/agents#502).
	if obs == nil || (obs.CompletedAt == nil && obs.ClosedAt == nil) {
		if streak != nil && streak.Count >= MaxConsecutiveFailures {
			lastFinishedAt := streak.LastFinishedAt
			add(StateFailed, &lastFinishedAt)
		}
	}
	// Only while it is still deferred, the same way askedAt above is only
	// the question still outstanding: Task.DeferredAt is cleared when
	// somebody picks the task back up (Store.SetDeferred), so the start
	// of a period a task has already come back from leaves no timestamp
	// behind to reconstruct. The one it is in now is worth an entry,
	// because "set aside three months ago" is the whole of what a reader
	// wants to know about a task sitting there.
	add(StateDeferred, task.DeferredAt)
	// The end of such a period does leave one (Task.UndeferredAt), and it
	// is the half worth more here: "picked back up this morning" is what
	// tells a reader that a task approved in January has not been sitting
	// in the queue since January. The deferral it ended is still not on
	// the timeline -- there is one deferred_at column and it holds the
	// deferral a task is in -- so this is a return with nothing visible
	// before it, which is the truest thing the record can say.
	//
	// Into whichever of 'proposed' and 'queued' the task's own approval
	// implied at that moment, because that is what undeferring does
	// (Store.SetDeferred leaves approval exactly as it was). The approval
	// itself is what decides, the way StateOf decides it; ApprovedAt only
	// orders the two, so that an approval granted *after* the undeferral
	// -- a task that came back a proposal and was approved afterwards --
	// does not turn the return into a queueing that had not happened yet.
	if task.UndeferredAt != nil {
		returned := StateProposed
		if task.Approval != nil && (task.ApprovedAt == nil || !task.ApprovedAt.After(*task.UndeferredAt)) {
			returned = StateQueued
		}
		add(returned, task.UndeferredAt)
	}
	if obs != nil {
		// One moment, one entry, under whichever of the three names
		// StateOf would give it right now. The record holds no separate
		// "and then somebody submitted it" timestamp -- AutoMerge is a
		// field on the declaration with no history of its own -- so a task
		// submitted after the fact shows its completion as 'completed',
		// one still parked shows the same moment as 'awaiting_submit', and
		// one that only ever had words to give shows it as
		// 'awaiting_reply', rather than the timeline ending on a state the
		// badge above it disagrees with.
		completedState := StateCompleted
		switch {
		case AwaitsSubmit(task):
			completedState = StateAwaitingSubmit
		case AwaitsAck(task):
			completedState = StateAwaitingReply
		}
		add(completedState, obs.CompletedAt)
		add(StateClosed, obs.ClosedAt)
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// StateTimes are the instants a task's current state can have begun at,
// gathered from the record in one place so that "how long has this been
// like this?" has one answer however it is asked.
//
// It is the task lane's counterpart to Observation.MergeQueueStateAt,
// and deliberately not the same shape. The merge queue stamps the moment
// an entry enters a state because that state is *stored*: something
// decided it, and the decision needs a time. A task's own state is
// derived (StateOf: "Never stored, never written", and Task's own "No
// State field and no StateSince"), so there is no moment of decision to
// stamp -- what there is instead is the handful of timestamps the record
// already keeps for other reasons, which between them say when the
// derived answer last changed. Storing a stamp beside them would be a
// second source of truth for a fact that is already implied by the
// first, and it would have to be written by something looping over every
// task; this is read off what is there.
//
// Since is what turns them into that one answer, and it answers for
// three states only -- see its own doc comment.
type StateTimes struct {
	// ApprovedAt is Task.ApprovedAt: the moment a task became something
	// dispatch could take. nil for a task nobody has approved, and for
	// one approved before that column existed.
	ApprovedAt *time.Time
	// RequeuedAt is the latest of Observation.RetryRequestedAt,
	// Observation.RunNowAt and Task.UndeferredAt -- a task put back in
	// front of dispatch after it had stopped waiting: a person pressing
	// Retry or Run now, or a deferral ending, by hand or at its own wake
	// time. It is not a state of its own (the task reads 'queued' either
	// way) but it does restart the wait, and a row flagged for standing
	// still ten seconds after somebody picked it back up would be flagged
	// for the one thing that is not standing still.
	RequeuedAt *time.Time
	// LiveRunStartedAt is when the run that is happening now began --
	// Run.StartedAt of the task's one unfinished run, which is the whole
	// of what makes StateRunning. nil for every task that is not running.
	LiveRunStartedAt *time.Time
	// LastRunFinishedAt is when the most recent finished attempt ended,
	// which for a task that is queued again is when it went back in the
	// queue: nothing else moves a task out of 'running'.
	LastRunFinishedAt *time.Time
	// CompletedAt is Observation.CompletedAt, which is both when the run
	// ended and when the merge queue's own wait began (StateCompleted).
	CompletedAt *time.Time
}

// Since is when a task in `state` entered it, as far as the record can
// say, and nil when it cannot.
//
// Three states, and no more, because only three are ones grain itself is
// working through: queued, running, and completed with a change on the
// merge queue (orchestrator's taskLane, which carries a patience for
// each of those and none for the rest). The other six are waiting on a
// person by design, and how long one of those has stood is not evidence
// of anything -- a proposal nobody has approved in a month is a
// proposal, not a fault -- so nothing asks this about them and this
// declines to guess.
//
// nil is the honest answer for a thin record and is treated as one
// everywhere it goes: a task approved before Task.ApprovedAt existed has
// no queued-since, and a reader measuring a patience against nil says
// nothing rather than measuring against the zero time and calling
// everything stuck.
//
// A deferral somebody has already picked the task back up from used to
// be the one thing it could not see: undeferring cleared Task.DeferredAt
// and left nothing behind, so a task approved in January, put aside, and
// picked up today read as queued since January and was flagged for
// standing still on the first read after it came back. Store.SetDeferred
// now stamps Task.UndeferredAt as it clears the deferral, and that stamp
// arrives here through RequeuedAt, so the wait is measured from the
// return. pkg/metrics measures the backlog's oldest wait from the same
// moment (metrics.backlogOf), so a flagged row and "the oldest thing
// still waiting" agree about which task that is.
//
// What remains unseeable is a deferral that ended before that column
// existed, which left no trace to read: such a task goes on being
// measured from its approval until the next time somebody defers and
// undefers it.
func (t StateTimes) Since(state State) *time.Time {
	switch state {
	case StateQueued:
		// The latest of the three, because each is a moment this task
		// (re-)entered the queue and the most recent one is the wait
		// that is actually going on: approval for a task that has never
		// run, the last attempt's ending for one dispatch has already
		// had a go at, and a retry, a run-now or an undeferral for one
		// somebody has put back.
		return latest(t.ApprovedAt, t.LastRunFinishedAt, t.RequeuedAt)
	case StateRunning:
		return t.LiveRunStartedAt
	case StateCompleted:
		return t.CompletedAt
	}
	return nil
}

// StateTimesFrom gathers a task's StateTimes from what a caller already
// holds -- Store.StateTimes is the same gathering in SQL, for a caller
// rendering a whole list rather than one task, and the two are edited
// together the way StateOf and task_state are.
//
// runs may be in any order: the live run is the one that has not
// finished (the store allows a task at most one), and the last finished
// attempt is found by its own timestamp rather than by position, since a
// caller holding runs from anywhere but Store.Runs has promised no
// order.
func StateTimesFrom(task Task, obs *Observation, runs []Run) StateTimes {
	times := StateTimes{ApprovedAt: task.ApprovedAt, RequeuedAt: task.UndeferredAt}
	if obs != nil {
		times.CompletedAt = obs.CompletedAt
		times.RequeuedAt = latest(obs.RetryRequestedAt, obs.RunNowAt, task.UndeferredAt)
	}
	for i := range runs {
		r := &runs[i]
		if r.FinishedAt == nil {
			started := r.StartedAt
			times.LiveRunStartedAt = &started
			continue
		}
		times.LastRunFinishedAt = latest(times.LastRunFinishedAt, r.FinishedAt)
	}
	return times
}

// latest is the most recent of the instants it is given, ignoring the
// ones that are not there, and nil when none of them is.
func latest(times ...*time.Time) *time.Time {
	var out *time.Time
	for _, t := range times {
		if t != nil && (out == nil || t.After(*out)) {
			out = t
		}
	}
	return out
}

// IsBlocked reports whether any blocking link names a task that has not
// closed.
//
// closed is passed in rather than looked up because whether a dependency
// is still open changes with nothing about this task changing — which is
// why this is re-evaluated every cycle rather than pinned at dispatch,
// the one deliberate exception to "decide once, never re-read".
func IsBlocked(task Task, closed map[string]bool) bool {
	for _, link := range task.Links {
		if link.Kind.Blocks() && !closed[link.Target] {
			return true
		}
	}
	return false
}
