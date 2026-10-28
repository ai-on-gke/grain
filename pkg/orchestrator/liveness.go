package orchestrator

import (
	"fmt"
	"strings"
	"time"

	"github.com/bwsalmon/grain/pkg/forge"
	"github.com/bwsalmon/grain/pkg/model"
)

// Liveness: not just what a state is, but what gets a task out of it.
//
// # The rule
//
// At every moment every task is in exactly one of three conditions
// (docs/design/task-liveness.md):
//
//   - waiting for a named resource that something will free -- a sandbox
//     or worker slot, a model connection's usage-limit window, another
//     task, a grain CI run that is actually queued or running, GitHub's
//     answer -- where "something" is a driver in this package or an
//     outside event grain has a clock on;
//   - executing -- a run is live;
//   - needing a person -- in the inbox, with the control that ends the
//     wait on the row.
//
// A task waiting on something nobody will produce, in nobody's inbox, is
// the fault this file exists to make impossible, and the UI must always be
// able to say which of the three a task is in and why. The "why" is the
// sub-case: livenessreasons.go classifies every reason a state can be held
// for, and escalates a sub-case that outlives its state's patience to the
// task's agent or to a person rather than describing it.
//
// queuestatus.go's invariant is *totality* -- every combination of
// pull-request health, check outcome, review state, repair state and queue
// position renders as some named state with a sentence, and
// TestStatusOfIsTotal enumerates the cross-product to prove it. That
// invariant was worth having and it is not enough. It says every input maps
// to some state; it says nothing about whether there is a way out of that
// state, or whether anything is going to take it.
//
// grain/task-264 is the worked example. It sat in waiting_ci with an
// impeccable sentence -- "waiting on a `grain CI` run that was cancelled
// without a verdict, which nothing but another run will replace" -- while
// reconcileTaskCI treated the cancellation as a final verdict and started
// nothing. Named, explained, and dead. grain/task-149 was the same shape
// two days earlier, before the totality test existed, and 149 was the row
// that said nothing at all rather than the row that said this.
//
// # Why that keeps happening
//
// The state is computed in one place (statusOf) and the *driving* is done
// in several others that do not know about it: reconcileTaskCI,
// reconcileBaseBranchCI, reconcileMergeBatches, reconcileDispatch, the
// repair path, and the stall clocks. Nothing held the two halves to each
// other, so a change on the driving side silently orphaned a state on the
// display side -- and the display side went on describing the wait
// convincingly. This file is what holds them together: every state the
// daemon can write is classified here, and for a state grain drives, the
// component responsible for producing the event that leaves it is named.
//
// # The classification
//
// For every state, exactly one of:
//
//   - livenessDriven -- some component of grain is responsible for
//     producing an event that leaves this state, and it will. driver names
//     it, and backstop names what ends the wait if the driver's own event
//     never comes.
//   - livenessAwaitingPerson -- terminal by design. It says so, and
//     control names the control that restarts the cycle.
//   - livenessAwaitingExternal -- the event comes from outside grain:
//     GitHub's answer to a read, or a check that is genuinely still
//     running.
//
// The second clause of the first is the whole value. A transition that is
// *possible* is not the same as one that is *driven*: "a new commit would
// clear it" is true of every stuck state in existence and is not a driver.
// So liveness_internal_test.go does not stop at the table -- for each
// driven state it pins a case that puts an entry in that state and asserts
// the named driver actually fires.
//
// # The runtime half
//
// A state can be statically driven and practically stuck: the driver has
// spent its attempts (ciMaximumAttempts), or is itself blocked behind
// something, or the deployment was down for the window in which it should
// have fired. So each non-terminal state also carries a patience -- how
// long that state can plausibly last -- and overdueNote says so on the row
// once an entry has been in one state longer than that. Saying so was the
// whole of it until grain/task-419, which had the merge queue's cycle
// escalate the entry as well; that escalation is system:pipeline-steward's
// now, and the daemon only marks the stall (Observation.MergeQueueStalledAt)
// so the Inbox and the steward can see it.
//
// An age alone was already on the row (Observation.MergeQueueStateAt;
// 264's row read "since 00:52:40 (7m ago)"), and an age alone is not a
// stall detector: nobody reading it knows whether seven minutes, or seven
// hours, is normal for the state it is beside. An age plus an expectation
// is, and the expectation has to live next to the driver it is about --
// here -- rather than in the reader's head.
//
// # Two lanes
//
// The merge queue is where the three known cases are, which is why it was
// the lane that got a table first (grain/task-314). The frame was never
// merge-queue-specific, and grain/task-358 filled in the other lane: a
// task's own states (model.State) are the same question with different
// answers -- `queued` is driven by reconcileDispatch, `running` by
// ProcessResult, `completed` with an open pull request by the merge queue
// itself, and the rest of the post-run states are person-owned.
// laneLiveness is therefore a value with a name and a source file rather
// than a package-level map, and the guards are written over a slice of
// those rather than over one table, so a third lane is a third table and
// no new machinery.
//
// The two lanes are about one task from different sides and they meet in
// exactly one place: a task reads 'completed' from the moment its run
// ends until its pull request merges or closes, and the whole of the merge
// lane is what happens inside that wait. So `completed`'s patience is the
// merge lane's own deadlines added up, and what a person should do about a
// flagged one is read the merge-queue row, which says which of the merge
// lane's states it is sitting in.
//
// The two lanes take their "since" from different places, and that is
// the one asymmetry between them. The merge lane has
// Observation.MergeQueueStateAt -- the instant an entry entered its
// current state, stamped by setMergeQueueState, because that state is
// stored and something decided to write it. A task's own state is derived
// (model.StateOf: "No State field and no StateSince"), so there is no
// decision to stamp; model.StateTimes reads the instant back off the
// timestamps the record already keeps for other reasons, and TaskStallNote
// below is what hands it to overdue.
//
// Deriving it rather than stamping it is a decision and not a shortcut.
// A stored stamp would be a second source of truth for something StateOf
// already refuses to store, it would need something looping over every
// task every cycle to keep it true, and that loop would be writing a row
// for a fact that had not changed -- the thing noteWithStall's own doc
// comment is careful about, arrived at from the other side. What it costs
// is fidelity where the record is thin: model.StateTimes.Since names the
// gaps it has, and each of them answers nil, which overdue reads as "say
// nothing" rather than as "stuck".
//
// That is also why only one of the two lanes stores its verdict.
// stalledAt below writes Observation.MergeQueueStalledAt because the
// merge queue already has a per-entry cycle writing that row anyway
// (syncEntry), so the flag costs nothing a cycle was not already paying;
// the task lane has no such cycle, and inventing one to keep a field
// true would be the whole cost of the feature. So the task lane's
// verdict is computed on the read instead (TaskStallNote), and its
// readers branch on the sentence being there rather than on a stamp.
type livenessClass string

const (
	// livenessDriven is a state grain itself leaves: some component
	// produces the event that ends it. driver names that component.
	livenessDriven livenessClass = "driven"
	// livenessAwaitingPerson is a state that is terminal by design. No
	// component is going to leave it and none should; control names what a
	// person presses to start the cycle again.
	livenessAwaitingPerson livenessClass = "awaiting a person"
	// livenessAwaitingExternal is a state whose event comes from outside
	// grain altogether: GitHub answering a read it has been refusing, a
	// check that is genuinely still running. grain drives nothing here,
	// which is exactly why these states carry a patience -- an external
	// answer that has not come in hours is a person's problem, and nothing
	// else is going to notice it.
	livenessAwaitingExternal livenessClass = "awaiting an external answer"
)

// stateLiveness is one state's way out.
type stateLiveness struct {
	class livenessClass
	// driver is the component responsible for producing the event that
	// leaves this state, as the identifier it is declared under in this
	// package. Required for livenessDriven and empty otherwise. It is an
	// identifier rather than prose so the guard can check it still exists:
	// a driver renamed or deleted without this table being updated is the
	// exact change that orphaned waiting_ci.
	driver string
	// backstop is what ends the wait when the driver's own event never
	// comes -- a clock, or the escalation that hands the pull request to a
	// person. Also an identifier in this package, and empty where the
	// driver needs no backstop because it cannot fail to fire.
	backstop string
	// control is what a person presses to leave a livenessAwaitingPerson
	// state, in the words on the button, and controlBy is the handler
	// behind it. Both are required for that class and empty otherwise.
	//
	// control is the button's own words rather than a sentence because the
	// row is built out of it (noteWithWayOut): a terminal state that does
	// not say how to leave it is the same fault as a wait nothing is
	// driving, approached from the other side, and the sentence that says
	// so should come from the same place the classification does.
	control   string
	controlBy string
	// patience is how long an entry may plausibly sit in this state before
	// the row should say it is stuck rather than waiting. Zero switches the
	// runtime half off for that state, which is right for one that is
	// terminal by design and wrong for everything else.
	//
	// A function rather than a duration because two of these are derived
	// from clocks a test (or, one day, a setting) can move, and a patience
	// that disagreed with the deadline it is a grace period on would flag
	// every entry in a suite that shortens the deadline.
	patience func() time.Duration
	// why is the sentence appended to the row when patience runs out, after
	// "this has stood here for more than <patience>". It says what a person
	// should conclude and where to look, because the note is the only place
	// this reaches anybody.
	why string
}

// laneLiveness is one lane's states and where they are declared. A lane is
// a set of states one subsystem writes onto a task: the merge queue's
// (mergeLane) and the task's own (taskLane).
type laneLiveness struct {
	// name is the lane in a person's words, for failure messages.
	name string
	// source is the file the states are declared in, relative to the
	// package directory -- another package's file for a lane whose
	// vocabulary is declared elsewhere (the task lane's is model.State) --
	// and prefix is the constant-name prefix they are declared under. The
	// guard reads them to find the states rather than trusting a second
	// hand-written list: a list is what went stale.
	source string
	prefix string
	// states is every state the lane can write, by its stored value.
	states map[string]stateLiveness
	// reasonPrefix is the constant-name prefix this lane's sub-cases are
	// declared under in livenessReasonSource, and reasons classifies each
	// of them by its value ("<state>/<reason>"). See livenessreasons.go.
	reasonPrefix string
	reasons      map[string]reasonLiveness
}

// livenessReasonSource is the file every lane's sub-cases are declared in.
const livenessReasonSource = "livenessreasons.go"

// The clocks the patiences are built out of. Two are grace periods on
// deadlines that already exist and one is an expectation with no deadline
// behind it, which is worth knowing when reading a flag: over a deadline
// means the escalation that owns that deadline did not fire, and over an
// expectation means only that this is taking longer than the shape of the
// thing suggests.
const (
	// livenessCycleGrace is one reconciliation cycle plus room for a
	// GitHub call, which is how long a state that should end on the very
	// next cycle is given. It is also the grace added to the deadlines
	// below: an entry exactly on its deadline is an entry whose escalation
	// is about to run, not one nothing is driving.
	livenessCycleGrace = 15 * time.Minute
	// livenessQueueRotation is how long an entry may wait for its turn at
	// the front of its repository's queue. There is no clock on queue
	// position and there should not be -- the entry is fine and waiting is
	// correct -- but every entry ahead of it is itself bounded by the
	// patiences here, so a change that has not reached the front in a
	// working day's morning is behind something that is stuck rather than
	// slow.
	livenessQueueRotation = 6 * time.Hour
	// livenessReadPatience is how long grain may fail to read a pull
	// request before the row stops calling it a wait. forge.ReadFailure
	// gates the *sentence* and bounds nothing (queuestatus.go's "Where the
	// cycle's bound lives"), so this is the only thing that will ever say
	// so about a credential that has stopped working.
	livenessReadPatience = 2 * time.Hour
)

// checkStallPatience is the wait on CI, which ends either with a verdict or
// with escalateStalledChecks at forge's own deadline. Zero where giving up
// is switched off (forge.SetCheckStallDeadline(0)): a deployment that has
// asked to wait forever is not told it is stuck.
func checkStallPatience() time.Duration {
	deadline := forge.CheckStallDeadline()
	if deadline <= 0 {
		return 0
	}
	return deadline + livenessCycleGrace
}

// repairPatience is the wait on an agent -- for a worker, for the run, for
// the review's own round -- which defaultRepairDeadline bounds.
func repairPatience() time.Duration { return defaultRepairDeadline + livenessCycleGrace }

func cycleGracePatience() time.Duration    { return livenessCycleGrace }
func queueRotationPatience() time.Duration { return livenessQueueRotation }
func readPatience() time.Duration          { return livenessReadPatience }
func noPatience() time.Duration            { return 0 }

// approvalPatience is how long a wait for a person's approval on GitHub
// may stand before the row calls it stuck: a working day, since an
// approval is somebody's attention rather than anything grain drives.
func approvalPatience() time.Duration { return 24 * time.Hour }

// livenessBacklogExpectation is how long a task may sit in 'queued'
// before the row stops calling it a wait -- the task lane's one number
// with no deadline behind it at all, and the one that had to be chosen
// rather than derived. Nothing anywhere bounds a queued task: task_ready
// selects them, reconcileDispatch takes what fits in max_workers, and a
// task that does not fit waits with no clock on it.
//
// Three days rather than a working day, because the thing this must not
// become is a capacity alarm. Sitting behind max_workers for hours is
// 'queued' working exactly as intended -- a deployment with two workers
// and a morning's backlog is not stuck, and a flag on every such row
// would be a flag nobody reads. What three days is sized against is the
// drain rate rather than the depth: every run ahead of this task is
// itself bounded (Config.MaxRunRuntime, two hours by default, a day for
// a console session), so a deployment that is dispatching at all clears
// even a deep backlog well inside it, and a task still here has not been
// waiting its turn -- nothing is dispatching, or its turn is never going
// to come.
//
// The two ways the turn never comes are worth naming because neither is
// about capacity: a dependency that is itself parked, deferred or
// proposed blocks this task for as long as it stays there (Store.Ready
// is task_ready, which is approved, not running and with no open
// blocker), and a deployment filing new work at the front of the backlog
// faster than it runs it (model.Config.NewestFirst -- Store.Ready orders
// by order_key and nothing ages a task up it) leaves whatever is at the
// back there.
const livenessBacklogExpectation = 72 * time.Hour

// dispatchPatience is the wait for a worker. An expectation, not a
// deadline: see livenessBacklogExpectation.
func dispatchPatience() time.Duration { return livenessBacklogExpectation }

// runPatience is the wait on a live run, which RunDispatch itself bounds
// -- Config.MaxRunRuntime cancels the framework call outright.
//
// Derived from the longest of the three clocks a run can be given rather
// than from the ordinary one, because the state does not say which kind
// of run it is: an agent nobody is watching gets DefaultMaxRunRuntime's
// two hours, a conversation gets DefaultMaxInteractiveRuntime's eight,
// and a console session somebody left open gets a day
// (DefaultMaxConsoleRuntime). Measuring a console session against two
// hours would flag every one of them, which is the capacity-alarm fault
// in its other form: a patience that fires on a healthy case is worth
// less than no patience at all.
//
// What is left once the longest clock has passed is a run that is not
// running: the process driving it died without finishing its row, and
// nothing periodic reaps that. RecoverOrphanedRuns does, at daemon
// startup and nowhere else, so this is the only thing that would ever
// say so about a deployment that has not been restarted since.
func runPatience() time.Duration { return DefaultMaxConsoleRuntime + livenessCycleGrace }

// mergeHandoffPatience is the wait a finished change spends on the merge
// queue: the whole of the merge lane, from the task lane's side.
//
// Added up out of the merge lane's own patiences rather than chosen,
// because the two lanes must not disagree about the same wait. A change
// may legitimately wait its turn behind its repository's queue
// (livenessQueueRotation) and then be repaired once (repairPatience),
// and each of those states carries its own flag on the merge-queue row.
// Past the sum, the queue's own row is already saying something this one
// is not -- which is why 'completed' is flagged at all rather than left
// to the lane that knows the detail.
func mergeHandoffPatience() time.Duration { return livenessQueueRotation + repairPatience() }

// mergeLane is the merge queue's states, classified. Every state
// queuestatus.go declares is here, and TestEveryStateIsClassified fails
// the build for one that is not -- which is the guard that keeps this
// true rather than making it true once.
var mergeLane = laneLiveness{
	name:   "merge queue",
	source: "queuestatus.go",
	prefix: "mergeState",
	// Every reason an entry can be held in one of these states, and its
	// escalation (livenessreasons.go).
	reasonPrefix: "mergeReason",
	reasons:      mergeReasons,
	states: map[string]stateLiveness{
		// Phase 1: grain is driving.
		mergeStateWaitingCI: {
			class: livenessDriven,
			// reconcileTaskCI owns the run for a repository on grain CI:
			// it starts one, collects its verdict, and -- since
			// grain/task-312 -- replaces a verdict that can never become a
			// pass (ciReplacementFor). Before that it did not, which is
			// what made this state the dead one.
			driver: "reconcileTaskCI",
			// And for a repository on GitHub's checks, where grain starts
			// nothing, the wait is ended by the clock instead: two hours
			// of unbroken PrPending reads and a person is told
			// (forge.ChecksStalled). That is also the backstop for a grain
			// CI run whose replacements are spent.
			backstop: "escalateStalledChecks",
			patience: checkStallPatience,
			why: "no verdict is coming and nothing has given up on it either -- the run to look at " +
				"is the last one on this commit, and \"Retry merge\" is what restarts the round",
		},
		mergeStateWaitingQueue: {
			class: livenessDriven,
			// The entry ahead of this one merging, failing or being given
			// up on is what moves the head along, and every one of those is
			// a state in this table with a driver of its own.
			driver:   "queueOrder",
			patience: queueRotationPatience,
			why: "the entry at the front of this branch's queue is not moving -- look at that " +
				"task rather than this one",
		},
		mergeStateWaitingReview: {
			class: livenessDriven,
			// The review's own pull request landing back on this branch,
			// which is the review task going round this same cycle.
			driver:   "reviewStateOf",
			backstop: "escalateToUser",
			patience: repairPatience,
			why:      "the review task this is waiting on is not finishing -- look at that task",
		},
		mergeStateMerging: {
			class:    livenessDriven,
			driver:   "syncEntry",
			patience: cycleGracePatience,
			why: "the merge call is made on the cycle this is written, so a row still saying it " +
				"means the cycle that wrote it never got to the call",
		},
		mergeStateUpdatingBranch: {
			class:    livenessDriven,
			driver:   "bringHeadUpToDate",
			patience: cycleGracePatience,
			why: "the base branch is written into the head branch on the cycle this is written, so " +
				"a row still saying it means that write never returned",
		},
		mergeStateBatching: {
			class:    livenessDriven,
			driver:   "advanceMergeRechecks",
			patience: checkStallPatience,
			why: "the batch run this change was tested in has been running longer than a CI suite " +
				"should take -- look at the grain-merge-queue run on this branch",
		},
		mergeStateGathering: {
			class:    livenessDriven,
			driver:   "syncEntry",
			patience: cycleGracePatience,
			why: "the gather window has stood longer than a cycle should hold it -- the next cycle " +
				"evaluates the head for merge",
		},
		mergeStateChecksUnfixable: {
			class: livenessDriven,
			// Escalated on the cycle that reads it once the entry is its
			// repository's queue head; the entries behind it wear this
			// until their turn comes, which is the queue rotation again.
			driver:   "escalateUnfixableChecks",
			patience: queueRotationPatience,
			why: "the check nothing on a branch can turn green is still red and nobody has been " +
				"handed this -- the repository's unfixable_checks setting, or the check itself, is " +
				"where this ends",
		},
		mergeStateWaitingGitHubApproval: {
			// A person approving the pull request on GitHub, which nothing
			// in grain can do for them; the queue notices it by reading the
			// reviews (holdRefusedMerge) and merges.
			class:    livenessAwaitingExternal,
			patience: approvalPatience,
			why: "GitHub still wants the approving review its rules require -- the people the note " +
				"names are who can give it, on the pull request's own page",
		},
		mergeStateGitHubQueue: {
			// GitHub's merge queue building and checking the merge group,
			// then merging it; grain notices the merge by reading the pull
			// request, and a drop from the queue by polling it
			// (enqueueOnGitHub).
			class:    livenessAwaitingExternal,
			patience: queueRotationPatience,
			why: "GitHub's merge queue has not merged this -- its merge group's checks, or the " +
				"entries ahead of it in GitHub's queue, are where to look",
		},
		// Phase 2: an agent has it.
		mergeStateRepairNeeded: {
			class:    livenessDriven,
			driver:   "requeueForRepair",
			backstop: "escalateUnfixableChecks",
			patience: queueRotationPatience,
			why: "the repair has not been filed -- which, past this long, means this entry has not " +
				"reached the front of its repository's queue",
		},
		mergeStateRepairQueued: {
			class:    livenessDriven,
			driver:   "reconcileDispatch",
			backstop: "escalateToUser",
			patience: repairPatience,
			why: "no worker has picked the repair up -- look at how many runs this deployment is " +
				"allowed at once, and at what is holding them",
		},
		mergeStateRepairing: {
			class:    livenessDriven,
			driver:   "ProcessResult",
			backstop: "escalateToUser",
			patience: repairPatience,
			why:      "the run on this branch has neither finished nor been reaped",
		},
		mergeStateWorking: {
			class: livenessDriven,
			// A run nobody here asked for still ends the way every run
			// ends, and the merge is held for exactly that long.
			driver:   "ProcessResult",
			patience: repairPatience,
			why:      "the run somebody started on this branch has neither finished nor been reaped",
		},
		// Phase 4, and the one state that is meant to stand still.
		mergeStateFailed: {
			class: livenessAwaitingPerson,
			// Clears what the queue remembers -- the blocked stamp, the
			// repair and the refresh -- and starts the cycle again from
			// phase 1.
			control:   "Retry merge",
			controlBy: "ui.Client.RetryMerge",
			patience:  noPatience,
		},
		// And the one state that is grain failing to ask rather than
		// waiting for an answer.
		mergeStateUnreadable: {
			class:    livenessAwaitingExternal,
			patience: readPatience,
			why: "grain has not been able to read this pull request for hours, which is a " +
				"credential or a GitHub outage rather than anything about the branch",
		},
	},
}

// taskLane is a task's own states, classified -- model.State's whole
// vocabulary, the words on the badge a person reads first.
//
// Where the merge lane is one subsystem's account of one wait, this is the
// task's whole life cycle, and the shape of the answers differs because of
// it: three states grain drives, six a person owns, and no external one at
// all (a task waiting on GitHub is waiting inside one of the three). That
// six is not a gap. A task grain has finished with and a person has not
// looked at is the ordinary, healthy end of a run, and the liveness
// question for it is not "what is coming" but "does the row say what to
// press" -- which is the awaiting-a-person clause, and why every one of
// the six names the control in the words on its own button.
//
// The three driven ones are where a task can be dead rather than waiting,
// and each names what would notice.
var taskLane = laneLiveness{
	name:   "task",
	source: "../model/task.go",
	prefix: "State",
	// And a task's, likewise (livenessreasons.go).
	reasonPrefix: "taskReason",
	reasons:      taskReasons,
	states: map[string]stateLiveness{
		// The two states grain is working on the task in.
		string(model.StateQueued): {
			class: livenessDriven,
			// reconcileDispatch is the whole of it: it reads what is
			// dispatchable (Store.Ready), takes what fits in max_workers,
			// and starts a run. There is no backstop, and that is the
			// finding rather than an omission -- nothing anywhere gives up
			// on a queued task, a queued task ages into no other state, and
			// this patience is the only thing that will ever say so.
			driver:   "reconcileDispatch",
			patience: dispatchPatience,
			why: "no run has started for this task in days, which is not the ordinary wait behind " +
				"`max_workers` -- look at what it depends on, at whether anything at all is " +
				"dispatching, and at where this sits in the backlog's own order",
		},
		string(model.StateRunning): {
			class: livenessDriven,
			// ProcessResult is what a finished run lands in: it decides
			// which state the task comes out in -- completed, parked on a
			// question, answered, or back in the queue -- so it is the
			// component responsible for leaving this one.
			driver: "ProcessResult",
			// And when the process holding the run dies before it gets
			// there, the live run row outlives it and task_state goes on
			// reading 'running' forever. RecoverOrphanedRuns finishes such
			// a row as "orphaned", which drops the task back to 'queued' --
			// but only at daemon startup, by its own deliberate design, so
			// a deployment that is up and has lost a run waits for a
			// restart that may be days away.
			backstop: "RecoverOrphanedRuns",
			patience: runPatience,
			why: "no process is finishing this run -- past the longest clock any run gets, a live " +
				"run row is one whose process is gone, and restarting the daemon is what reaps it",
		},
		// The one state the two lanes share, and the only place they meet.
		string(model.StateCompleted): {
			class: livenessDriven,
			// The run is over and the change is on the merge queue
			// (AutoMerge set, a LinkFixes pull request open). syncEntry is
			// that queue's per-entry cycle: it merges the pull request, and
			// recordPullRequestEvents -- inside the same cycle -- stamps
			// Observation.ClosedAt off the merged or closed reading, which
			// is what moves the task to 'closed'.
			driver: "syncEntry",
			// When the queue cannot land it, escalateToUser ends the
			// queue's driving and hands the pull request to a person. Note
			// what that does *not* do: the task stays 'completed', wearing
			// merge_failed on the merge-queue row beside it. So the backstop
			// here bounds the wait rather than the state, and the row that
			// says what to press is the merge lane's (mergeStateFailed's
			// "Retry merge").
			backstop: "escalateToUser",
			patience: mergeHandoffPatience,
			why: "this change has been on the merge queue longer than waiting its turn and one " +
				"repair account for -- the merge-queue row on this same task says which of the " +
				"queue's own states it is sitting in",
		},
		// The six a person owns. Every one of these is a state a task does
		// not leave by itself and should not: the work is done, or the work
		// is waiting on a decision nothing here can make.
		string(model.StateProposed): {
			class: livenessAwaitingPerson,
			// An agent proposed it and nobody has said yes. task_ready only
			// ever selects 'queued', so nothing dispatches a proposal --
			// deliberately, since that is the whole of what approval is for.
			control:   "Approve",
			controlBy: "ui.Client.Approve",
			patience:  noPatience,
		},
		string(model.StateAwaitingReply): {
			class: livenessAwaitingPerson,
			// A run stopped mid-sentence to ask something
			// (Observation.PendingQuestionCommentID). The answer is prose
			// rather than a verb, which is why the control is the reply box
			// on the inbox row rather than a button that fires a request --
			// and posting it is what dispatches the next run.
			control:   "Reply",
			controlBy: "ui.Client.AddComment",
			patience:  noPatience,
		},
		string(model.StateAwaitingSubmit): {
			class: livenessAwaitingPerson,
			// The run is over, its pull request is open, and AutoMerge is
			// not set: the merge queue is not driving it and nothing will
			// land it on its own. grain/task-296 is what made that legible
			// -- the state used to be a chip beside a badge reading "Queued
			// for merge", which said the opposite of the truth about the one
			// task on the list that needed somebody -- and Submit is the
			// button that ends it, on the inbox row that state put it on.
			control:   "Submit",
			controlBy: "ui.Client.Submit",
			patience:  noPatience,
		},
		string(model.StateFailed): {
			class: livenessAwaitingPerson,
			// model.MaxConsecutiveFailures runs in a row ended without
			// succeeding. Unlike every other pre-run state this one does not
			// retry itself -- task_ready selects only 'queued' -- so a
			// person setting Observation.RetryRequestedAt is the only way
			// back into the queue.
			control:   "Retry",
			controlBy: "ui.Client.Retry",
			patience:  noPatience,
		},
		string(model.StateDeferred): {
			class: livenessAwaitingPerson,
			// Put aside on purpose: still work grain is meant to do, not
			// work anybody is waiting on today.
			//
			// The one entry in this table that is coarser than the truth. A
			// deferral with a wake time on it (ui.DeferOptions.Until) is
			// driven -- reconcileWake undeferrs it on the first cycle at or
			// after that instant -- and a deferral without one, which is the
			// default and the common case, is exactly what it looks like: a
			// human decision with no clock behind it. One state, two
			// answers, and the class has to be the one that is true of a
			// task nobody has put a date on, or every parked task in the
			// deployment would be waiting on a reconciler that has nothing
			// to do with it.
			control:   "Undefer",
			controlBy: "ui.Client.Undefer",
			patience:  noPatience,
		},
		string(model.StateClosed): {
			class: livenessAwaitingPerson,
			// The end, and the one state here that is not a wait at all.
			// It is classified with the person-owned ones because the
			// question this table asks is what leaves a state, and the only
			// thing that leaves this one is somebody deciding the task is
			// worth having back.
			control:   "Reopen",
			controlBy: "ui.Client.Reopen",
			patience:  noPatience,
		},
	},
}

// of is one state's classification, and whether this lane has one at all.
func (l laneLiveness) of(state string) (stateLiveness, bool) {
	liveness, ok := l.states[state]
	return liveness, ok
}

// overdue reports whether an entry that has been in state since `since` has
// outlived that state's patience, and by how much.
//
// A state this lane does not know is never overdue: a daemon mid-upgrade
// writing a state an older build has no classification for should degrade
// to saying nothing, the way the phrase maps already do (queuestatus.go's
// "Adding to this list"). The guard is what stops that being indefinite.
func (l laneLiveness) overdue(state string, since *time.Time, now time.Time) (time.Duration, bool) {
	liveness, ok := l.of(state)
	if !ok || since == nil || liveness.patience == nil {
		return 0, false
	}
	patience := liveness.patience()
	if patience <= 0 {
		return 0, false
	}
	// Compared against the deadline rather than by subtracting, so a clock
	// that has jumped backwards reads as "not overdue yet" -- the
	// direction every other wait in this package errs in.
	if now.Before(since.Add(patience)) {
		return 0, false
	}
	return patience, true
}

// stalledAt is the runtime half as a fact rather than a sentence: the
// moment an entry in state since `since` stopped being a wait, or nil
// while it is still inside its patience.
//
// It exists because the sentence below reaches only a person who opens
// the task. The row's own *mark* -- the live dot that says grain is
// working on this now -- is chosen from the state alone
// (ui/src/state.js's MERGE_QUEUE_LIVE), so a flagged entry went on
// reading as progress at a glance, which is the exact thing that
// vocabulary was invented to stop. Written onto the observation
// (Observation.MergeQueueStalledAt), it gives the UI and pkg/mcp the same
// judgement the note is made of, to branch on rather than to parse.
//
// Off overdue, so that the flag and the sentence cannot disagree: one
// classification, read twice.
//
// The answer is the deadline the entry crossed (since + patience) rather
// than now, for the reason noteWithStall says the patience rather than
// the age: a value that moved every cycle would be a store write per
// stalled entry per cycle for a fact that has not changed. This one is
// fixed for as long as the entry sits in the state, and it says since
// when the entry has been stuck rather than when a cycle last looked.
func (l laneLiveness) stalledAt(state string, since *time.Time, now time.Time) *time.Time {
	patience, ok := l.overdue(state, since, now)
	if !ok {
		return nil
	}
	at := since.Add(patience)
	return &at
}

// noteWithStall is the runtime half in one sentence, appended to whatever
// the row was already saying.
//
// It says the patience rather than the age on purpose. The age is already
// on the row beside the state (Observation.MergeQueueStateAt, rendered as
// "since ... (7m ago)"), and a note that counted the age up would differ
// on every cycle -- which, since setMergeQueueState writes when the note
// changes, would be a store write per stalled entry per cycle for a fact
// that has not changed. The patience is a constant, so the row changes once
// when it is crossed and then stands still, which is also the truthful
// shape: what a person needs to know is that this is past what it should
// take, not to the second by how much.
func (l laneLiveness) noteWithStall(state, note string, since *time.Time, now time.Time) string {
	patience, ok := l.overdue(state, since, now)
	if !ok {
		return note
	}
	liveness, _ := l.of(state)
	stall := fmt.Sprintf("this has stood here for more than %s, which is longer than %s should take, "+
		"so read it as stuck rather than as waiting", humanDuration(patience), l.driverWords(liveness))
	if liveness.why != "" {
		stall += ": " + liveness.why
	}
	if note == "" {
		return stall
	}
	return note + " -- and " + stall
}

// noteWithWayOut is the other half of the awaiting-a-person clause: a
// state that is terminal by design has to *say* so, and name the control
// that restarts the cycle, on the row a person is actually looking at.
//
// It was not doing that, which grain/task-314 found: gaveUpNote -- the
// fallback for an entry blocked by a build too old to have recorded a
// reason -- ends with "\"Retry merge\" starts the round again", and every
// sentence the four escalations actually write ends without it. So the
// common case read "the repair ran and did not take -- it's still
// conflicted, and one automatic repair is all a pull request gets": true,
// terminal, and silent about the one thing a person reading it has to do.
//
// Appended rather than written into each of those sentences so the words
// and the classification cannot drift: the button named here is the button
// the row offers.
func (l laneLiveness) noteWithWayOut(state, note string) string {
	liveness, ok := l.of(state)
	if !ok || liveness.class != livenessAwaitingPerson || liveness.control == "" {
		return note
	}
	if strings.Contains(note, liveness.control) {
		return note
	}
	wayOut := fmt.Sprintf("%q starts the round again", liveness.control)
	if note == "" {
		return wayOut
	}
	return note + " -- " + wayOut
}

// TaskStallNote is the task lane's runtime half for one task, said in a
// sentence -- or "" for a task that is inside its state's patience, in a
// state that has none, or one whose record cannot say when the state
// began (model.StateTimes.Since).
//
// It is exported because the only place this reaches anybody is a row a
// person is reading, and those are rendered outside this package: pkg/ui
// builds every task's JSON, and cmd/grain hands this to it
// (ui.Config.Stall) rather than pkg/ui reaching in here, which would
// invert the layering -- the orchestrator is what knows about drivers and
// deadlines, and the UI is what draws.
//
// Computed on the read rather than written onto the task. The note is a
// pure function of the record and the clock, it changes exactly once per
// state -- when the patience is crossed -- and the alternative is a
// stored field something has to keep true (see this file's "Two lanes",
// and noteWithStall on why the sentence names the patience rather than
// counting the age up).
func TaskStallNote(state model.State, since, now time.Time) string {
	return taskLane.noteWithStall(string(state), "", &since, now)
}

// driverWords names what ought to have left the state, in the words the
// note can carry: the component for a state grain drives, and what is
// being waited on for one it does not.
func (l laneLiveness) driverWords(liveness stateLiveness) string {
	switch liveness.class {
	case livenessDriven:
		if liveness.backstop != "" {
			return fmt.Sprintf("`%s` (with `%s` behind it)", liveness.driver, liveness.backstop)
		}
		return fmt.Sprintf("`%s`", liveness.driver)
	case livenessAwaitingExternal:
		return "an answer from GitHub"
	default:
		return "this state"
	}
}
