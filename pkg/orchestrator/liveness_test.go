package orchestrator_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The other half of grain/task-314 and grain/task-358: a transition that
// is *possible* is not the same as one that is *driven*.
//
// liveness_internal_test.go proves every state either lane can write is
// classified and that a driven one names a component that still exists.
// Neither of those would have caught grain/task-264: reconcileTaskCI
// existed, was named in nobody's table, and simply did nothing about a
// cancelled run. So for every state the classification calls driven, this
// file requires a case that puts a task in that state and shows the named
// driver actually firing -- and a state whose driver does not fire fails
// here. One map per lane, each keyed by state, each with a test of its
// own.
//
// "A new commit would clear it" is not a driver. It is true of every stuck
// state in existence, which is why the cases below all end with the wait
// ending on its own: a replacement run grain started, an escalation grain
// made, a merge grain performed.

// driverCoverage is how this repository proves one state's driver fires:
// either a case written here, or the test elsewhere in this package that
// already does it.
//
// The `elsewhere` arm is a deliberate trade, not a gap. Eleven of these
// twelve states have had an end-to-end test since before this file existed
// -- TestARedBaseBranchStopsTheQueueAndEarnsARepair drives
// reconcileBaseBranchCI through a whole cycle, and re-staging that here
// would be a second copy of a 130-line fixture to assert the same thing.
// What the reference buys is the link: the test named here is checked to
// exist, so deleting the coverage for a driven state fails the build in the
// place that claims the state is driven. What it does not buy is a check
// that the named test is still *about* that state, which is why the three
// cases this task was filed for are written out here in full rather than
// cited.
type driverCoverage struct {
	// run is a case in this file. It is given a name by the state it
	// covers and run as a subtest.
	run func(t *testing.T)
	// elsewhere is the name of a test in this package that drives the
	// state's driver end to end, with a word on what it shows.
	elsewhere string
	shows     string
}

// mergeDriverCoverage is that map, and
// TestEveryDrivenMergeStateHasADriverThatFires fails for a driven state
// missing from it.
var mergeDriverCoverage = map[string]driverCoverage{
	// The three known dead ends, written out here because they are what
	// this was built for.
	"waiting_ci": {run: driveWaitingCI},

	// waiting_queue is left by the entry in front of it moving, which is
	// every other state in this table doing its own job.
	"waiting_base": {
		elsewhere: "TestAWaitingEntryIsReArmedWhenTheBaseGoesGreen",
		shows:     "the base going green and the entry merged with it and put back on the queue",
	},
	"waiting_queue": {
		elsewhere: "TestSyncPullRequestsKeepsMergingWhileTheHeadIsRepaired",
		shows:     "the head being repaired, the entries behind it merging, and the queue rotating",
	},
	"waiting_review": {
		elsewhere: "TestSyncPullRequestsHoldsTheMergeUntilTheReviewHasLanded",
		shows:     "the review's own pull request landing and the merge going ahead",
	},
	"merging": {
		elsewhere: "TestSyncPullRequestsAutoMergesACleanPullRequest",
		shows:     "syncEntry making the merge call on the cycle that writes the state",
	},
	"updating_branch": {
		elsewhere: "TestARepositoryCanStillRequireBranchesToBeUpToDate",
		shows:     "bringHeadUpToDate writing the base branch into the head branch",
	},
	"batching": {
		elsewhere: "TestEnforceRechecksAnOverlappingChangeBeforeItMerges",
		shows:     "advanceMergeRechecks judging the overlap re-check and merging the change",
	},
	"gathering": {
		elsewhere: "TestSyncPullRequestsAutoMergesACleanPullRequest",
		shows:     "syncEntry evaluating the queue head for merge without holding it for a gather window",
	},
	"base_broken": {
		elsewhere: "TestAChangeWaitingItsTurnIsToldTheBaseBranchIsRed",
		shows: "a change waiting its turn wearing the sentence about the branch, which is all this " +
			"state is since grain/task-471 -- it holds nothing, so what fires is the queue in " +
			"front of it (and TestTheRepairOfABrokenBaseNeedsNoExemptionToMerge, the same red " +
			"branch with both changes merging through it)",
	},
	"checks_unfixable": {
		elsewhere: "TestTheQueueConcludesAnUnfixableRepositoryOnceForEveryEntry",
		shows:     "escalateUnfixableChecks handing each entry to a person rather than an agent",
	},
	"repair_needed": {
		elsewhere: "TestSyncPullRequestsSendsAConflictedQueueHeadBackForRepair",
		shows:     "requeueForRepair filing the repair on the cycle the entry is queue head",
	},
	"repair_queued": {
		elsewhere: "TestSyncPullRequestsGivesUpOnAQueueHeadWhoseRepairNeverFinishes",
		shows:     "escalateToUser ending the wait at RepairDeadline when no worker ever takes it",
	},
	"repairing": {
		elsewhere: "TestSyncPullRequestsEscalatesWhenTheRepairFinishesButThePrIsStillBroken",
		shows:     "the run ending and the queue acting on what it pushed",
	},
	"agent_working": {
		elsewhere: "TestSyncPullRequestsDoesNotMergeWhileAnAgentIsOnTheBranch",
		shows:     "the run a person started ending and the next cycle merging",
	},
}

// taskDriverCoverage is the same map for the task's own lane
// (grain/task-358), and TestEveryDrivenTaskStateHasADriverThatFires fails
// for a driven task state missing from it.
//
// Three states rather than the merge lane's twelve, because a task's life
// cycle is mostly not grain's to drive: six of its ten states are a
// person's and carry a control rather than a driver, and the whole of the
// wait a merge queue does happens inside the third of these.
var taskDriverCoverage = map[string]driverCoverage{
	"queued": {
		elsewhere: "TestACycleDispatchesATaskFiledWhileAnotherRunIsLive",
		shows: "reconcileDispatch taking a task out of the backlog and starting a run for it, " +
			"including one filed while the workers were busy",
	},
	"running": {
		elsewhere: "TestProcessResultOpensAPullRequestForAPushedBranch",
		shows: "ProcessResult acting on what the run produced and leaving the task in the post-run " +
			"state that follows from it",
	},
	// Written out in full rather than cited: this is where the two lanes
	// meet, and it is the one state whose driver belongs to the other one.
	"completed": {run: driveCompletedOffTheMergeQueue},
}

func TestEveryDrivenTaskStateHasADriverThatFires(t *testing.T) {
	tests := packageTests(t)
	for _, state := range orchestrator.TaskLaneDrivenStates() {
		coverage, ok := taskDriverCoverage[state]
		if !ok {
			_, driver, _, _ := orchestrator.TaskLaneClaimOf(state)
			t.Errorf("%q is classified as driven by %s and nothing here shows that driver firing: "+
				"add a case to taskDriverCoverage. A state named, explained and driven by nothing "+
				"is what grain/task-264 was.", state, driver)
			continue
		}
		switch {
		case coverage.run != nil:
			t.Run(state, coverage.run)
		case coverage.elsewhere != "":
			if !tests[coverage.elsewhere] {
				t.Errorf("%q says %s covers its driver and this package has no such test: either it "+
					"was renamed (say so here) or the coverage for that state is gone",
					state, coverage.elsewhere)
			}
		default:
			t.Errorf("%q has an empty coverage entry", state)
		}
	}
}

// driveCompletedOffTheMergeQueue is the one place the two lanes meet, in
// both of the shapes it has.
//
// A task reads 'completed' from the moment its run ends until its pull
// request merges or closes, and the whole of the merge lane happens inside
// that wait. So the task lane's answer for it is the merge queue --
// syncEntry merging, recordPullRequestEvents stamping ClosedAt off the
// merged reading -- and the second case is the one the table's comment
// claims and nothing else here would catch: when the queue gives up, the
// task does not move. It stays 'completed' wearing merge_failed beside it,
// which is why the way out is named in the merge lane and not this one.
func driveCompletedOffTheMergeQueue(t *testing.T) {
	t.Run("the queue lands it and the task closes", completedIsClosedByTheMergeQueue)
	t.Run("a queue that gives up leaves the task where it is", completedOutlivesAQueueThatGaveUp)
}

func completedIsClosedByTheMergeQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)

	// The run is over and the change is on the queue: 'completed' is the
	// state, and the classification says the merge queue is what leaves it.
	if got := taskState(t, ctx, store, task.ID); got != model.StateCompleted {
		t.Fatalf("state = %q, want completed once the run is over and the pull request is open", got)
	}
	class, driver, _, patience := orchestrator.TaskLaneClaimOf(string(model.StateCompleted))
	if class != "driven" || driver != "syncEntry" {
		t.Fatalf("completed is classified %q driven by %q, want driven by the merge queue", class, driver)
	}
	if patience <= 0 {
		t.Fatal("completed has no patience: a change the queue stops driving would never be flagged")
	}

	// And it does. One cycle of the queue with a clean pull request merges
	// it, and the task leaves the state by the door the table names.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if len(sim.PullRequests) != 1 || !sim.PullRequests[0].Merged {
		t.Fatalf("pull request = %+v, want it merged by the queue", sim.PullRequests)
	}
	if got := taskState(t, ctx, store, task.ID); got != model.StateClosed {
		t.Fatalf("state = %q, want closed: recordPullRequestEvents stamps ClosedAt off the merge", got)
	}
}

func completedOutlivesAQueueThatGaveUp(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	// The one repair this pull request gets is asked for, runs, and the
	// branch is still conflicted -- the queue is out of moves.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("asking for the repair: %v", err)
	}
	finished := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, task.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{finished, finished.Add(time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("SyncPullRequests at %v: %v", at, err)
		}
	}

	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != orchestrator.MergeStateFailed {
		t.Fatalf("merge-queue state = %q, want %s", obs.MergeQueueState, orchestrator.MergeStateFailed)
	}
	// The task itself has not moved and will not: escalateToUser ends the
	// queue's driving, not the task's state. That is why 'completed' is
	// classified driven with the queue's own escalation as its backstop --
	// the wait is bounded, the state is not -- and why the row that says
	// what to press is the merge queue's.
	if got := taskState(t, ctx, store, task.ID); got != model.StateCompleted {
		t.Fatalf("state = %q, want the task still completed after the queue gave up", got)
	}
	if !strings.Contains(obs.MergeQueueNote, "Retry merge") {
		t.Errorf("the merge-queue row does not say what to press:\n%s", obs.MergeQueueNote)
	}
	_, _, control, _ := orchestrator.TaskLaneClaimOf(string(model.StateCompleted))
	if control != "" {
		t.Errorf("the task lane offers %q for completed: the control for a change the queue gave up "+
			"on belongs to the merge lane, on the row that knows it gave up", control)
	}
}

// taskState is model.StateOf's own answer, read the way the daemon reads
// it (the task_state view) rather than recomputed here.
func taskState(t *testing.T, ctx context.Context, store *model.Store, id string) model.State {
	t.Helper()
	state, err := store.State(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestEveryDrivenMergeStateHasADriverThatFires(t *testing.T) {
	tests := packageTests(t)
	for _, state := range orchestrator.MergeLaneDrivenStates() {
		coverage, ok := mergeDriverCoverage[state]
		if !ok {
			_, driver, _, _ := orchestrator.MergeLaneClaimOf(state)
			t.Errorf("%q is classified as driven by %s and nothing here shows that driver firing: "+
				"add a case to mergeDriverCoverage. A state named, explained and driven by nothing "+
				"is what grain/task-264 was.", state, driver)
			continue
		}
		switch {
		case coverage.run != nil:
			t.Run(state, coverage.run)
		case coverage.elsewhere != "":
			if !tests[coverage.elsewhere] {
				t.Errorf("%q says %s covers its driver and this package has no such test: either it "+
					"was renamed (say so here) or the coverage for that state is gone",
					state, coverage.elsewhere)
			}
		default:
			t.Errorf("%q has an empty coverage entry", state)
		}
	}
}

// packageTests is every Test function this package declares, for the
// reference arm above.
func packageTests(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", entry.Name(), err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				tests[fn.Name.Name] = true
			}
		}
	}
	if len(tests) == 0 {
		t.Fatal("read no tests at all out of this package: this test needs updating")
	}
	return tests
}

// driveWaitingCI is waiting_ci's driver, in the three shapes the state has
// actually been found dead in. Each is pinned as a named case so the next
// person can see what this was built for.
func driveWaitingCI(t *testing.T) {
	t.Run("task-264: a cancelled grain CI run is replaced", waitingCIAfterACancelledGrainRun)
	t.Run("task-149: cancelled GitHub checks reach a person", waitingCIAfterCancelledGitHubChecks)
	t.Run("the replacements are spent", waitingCIWithItsRetriesSpent)
}

// grain/task-264. The row was perfect and the lane was dead: waiting_ci
// with "waiting on a `grain CI` run that was cancelled without a verdict,
// which nothing but another run will replace", and nothing replacing one.
// The state's driver is reconcileTaskCI, and what this asserts is that it
// fires -- a replacement run against the same commit, and the wait ending
// when that run reports.
//
// Written before grain/task-312 landed this would have failed at the
// "runs after the retry delay" line, which is the whole point of it.
func waitingCIAfterACancelledGrainRun(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	setMergeable(sim, true)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)

	// A job that ran past its timeout: cancelled, nobody's name on it,
	// nothing written about why -- which is what the two jobs 264 was stuck
	// on looked like.
	runner.cancelBySystem(status.RunID, "", ci.JobRun{
		Workflow: "CI", JobID: "image", Name: "image", Conclusion: ci.ConclusionCancelled,
	})
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}

	// The row 264 sat on, and the sentence that made it legible without
	// making it live.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "waiting_ci" {
		t.Fatalf("merge queue state = %q, want waiting_ci", obs.MergeQueueState)
	}
	if !strings.Contains(obs.MergeQueueNote, "cancelled") {
		t.Fatalf("note = %q, want the cancellation named on the row", obs.MergeQueueNote)
	}

	// And the driver fires. Not on the same cycle -- a deployment that
	// restarted once is not answered with a run per cycle -- but on the
	// next one past ciRetryDelay.
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != 2 {
		t.Fatalf("runs after the retry delay = %d, want the replacement: reconcileTaskCI is what "+
			"leaves waiting_ci for a repository on grain CI, and it did not fire", got)
	}

	// The way out is taken, not merely available: the replacement reports
	// and the pull request merges with nobody having touched it.
	replacement := ciStatusOf(t, ctx, store, task.ID)
	runner.finish(replacement.RunID, ci.ConclusionSuccess)
	at := baseTime.Add(6 * time.Minute)
	if err := runCIReconcilerAt(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
		t.Fatalf("SyncPullRequests after the replacement: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("the pull request never merged on the replacement run's own green verdict")
	}
}

// grain/task-149, which is the same dead end reached from GitHub's side:
// checks cancelled without a verdict, on a repository where grain starts
// nothing and therefore has no run to replace. The driver there is the
// backstop -- escalateStalledChecks, at forge's own deadline -- and what
// this asserts is that the state is left, and left for a person with the
// control that restarts the cycle named on the row.
//
// It also pins the relationship between the two clocks. waiting_ci's
// patience is that deadline plus a cycle's grace, so a lane whose backstop
// works never earns the runtime flag: the flag is for the lane where it
// does not.
func waitingCIAfterCancelledGitHubChecks(t *testing.T) {
	deadline := 2 * time.Hour
	defer orchestrator.SetCheckStallDeadline(deadline)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("cancelled")},
		{Name: "frontend", Status: "completed", Conclusion: strPtr("success")},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "waiting_ci" || obs.MergeQueueStateAt == nil {
		t.Fatalf("state = %q at %v, want waiting_ci with a stamp", obs.MergeQueueState, obs.MergeQueueStateAt)
	}
	if _, stalled := orchestrator.MergeLaneOverdue(obs.MergeQueueState, obs.MergeQueueStateAt, baseTime); stalled {
		t.Error("flagged as stalled on the first cycle of the wait")
	}

	// The backstop fires inside the patience, which is what the patience
	// being a grace period on it means.
	_, _, _, patience := orchestrator.MergeLaneClaimOf("waiting_ci")
	if patience <= deadline {
		t.Fatalf("waiting_ci's patience is %s against a %s deadline: the runtime flag would fire "+
			"before the escalation that is supposed to end this", patience, deadline)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests past the deadline: %v", err)
	}
	obs = mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != orchestrator.MergeStateFailed {
		t.Fatalf("state = %q, want %s: escalateStalledChecks is what leaves waiting_ci when no "+
			"verdict is ever coming, and it did not fire",
			obs.MergeQueueState, orchestrator.MergeStateFailed)
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue is still driving a pull request it has given up on")
	}

	// Terminal by design, and the classification's second clause is that
	// such a state says so and names the control. The row is where a person
	// reads it.
	class, _, control, _ := orchestrator.MergeLaneClaimOf(obs.MergeQueueState)
	if class != "awaiting a person" {
		t.Fatalf("%s is classified %q, want awaiting a person", obs.MergeQueueState, class)
	}
	if !strings.Contains(control, "Retry merge") {
		t.Fatalf("control = %q, want the button a person presses", control)
	}
	if !strings.Contains(obs.MergeQueueNote, "Retry merge") && !strings.Contains(obs.MergeQueueNote, "cancelled") {
		t.Fatalf("note = %q, want what it died of on the row", obs.MergeQueueNote)
	}
	if bodies := commentBodies(t, ctx, store, task.ID); len(bodies) != 1 {
		t.Fatalf("want exactly one comment handing this to a person, got %q", bodies)
	}
}

// The third known shape, and the one that is a dead end by design rather
// than by omission: a commit whose runs keep being cancelled has its four
// attempts and then grain owes it nothing (ciMaximumAttempts,
// forge.GrainCIReplacementSpent). reconcileTaskCI has stopped driving, so
// this asserts what is left driving -- the backstop -- and that the row
// stops promising a replacement that is not coming.
func waitingCIWithItsRetriesSpent(t *testing.T) {
	deadline := 2 * time.Hour
	defer orchestrator.SetCheckStallDeadline(deadline)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	grantCI(t, ctx, store, task)
	setMergeable(sim, true)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)

	// The stall clock is started by the first cycle that reads PENDING and
	// measures one unbroken run of them (forge.ChecksStalled), so the wait
	// has to actually begin before it can be waited out.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	// Every attempt cancelled, five minutes apart: the job that cannot fit
	// inside its timeout at all.
	for attempt := 1; attempt <= orchestrator.CIMaximumAttempts; attempt++ {
		runner.cancelBySystem(status.RunID, "")
		at := baseTime.Add(time.Duration(attempt-1) * 5 * time.Minute)
		if err := runCIReconcilerAt(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
		if attempt < orchestrator.CIMaximumAttempts {
			if err := runCIReconcilerAt(t, ctx, store, client, runner, at.Add(5*time.Minute)); err != nil {
				t.Fatal(err)
			}
			status = ciStatusOf(t, ctx, store, task.ID)
		}
	}
	if err := runCIReconcilerAt(t, ctx, store, client, runner, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := runner.started(); got != orchestrator.CIMaximumAttempts {
		t.Fatalf("runs = %d, want %d: the retry is meant to be bounded", got, orchestrator.CIMaximumAttempts)
	}
	verdict := grainCIVerdictFor(t, ctx, store, client, task, repo, branch)
	if verdict.Replacement != forge.GrainCIReplacementSpent {
		t.Fatalf("replacement = %q, want spent", verdict.Replacement)
	}

	// The wait ends at once rather than at the stall deadline: nothing is
	// in flight and nothing will be started, so two hours of "Waiting for
	// CI" would be two hours of a row saying something untrue
	// (grain/task-480).
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != orchestrator.MergeStateFailed || obs.MergeQueueBlockedAt == nil {
		t.Fatalf("state = %q blocked=%v, want %s well inside the %s stall deadline: with the "+
			"replacements spent, the queue hands it to a person straight away",
			obs.MergeQueueState, obs.MergeQueueBlockedAt, orchestrator.MergeStateFailed, deadline)
	}
	if bodies := strings.Join(commentBodies(t, ctx, store, task.ID), "\n"); !strings.Contains(bodies, "after 4 attempts") {
		t.Errorf("comments = %q, want one saying grain stopped after its attempts", bodies)
	}
}

// The runtime half, end to end: a lane whose driver has stopped firing
// altogether, and the row a person is looking at while it does.
//
// This is what would have caught grain/task-264 within the hour rather than
// at one in the morning. The state is statically driven, the driver is
// named, and nothing is happening -- so the row stops calling it a wait and
// says which component should have left it.
//
// Reached by switching giving up off (forge.SetCheckStallDeadline(0)),
// which is the one supported way to have a merge lane with no backstop at
// all: a deployment that has asked to wait forever on CI. That leaves
// waiting_ci with nothing but reconcileTaskCI driving it, and this test
// runs no CI reconciler, so nothing drives it at all -- which is exactly
// the shape of every fault this file is about.
func TestAMergeStateThatOutlivesItsDriverIsMarkedStalled(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "in_progress"}}

	// A patience to measure against while giving up is switched off. Both
	// clocks are read live, so this test does not restate either.
	restore := orchestrator.SetCheckStallDeadline(90 * time.Minute)
	_, _, _, patience := orchestrator.MergeLaneClaimOf("waiting_ci")
	restore()
	defer orchestrator.SetCheckStallDeadline(2 * time.Hour)()
	orchestrator.SetCheckStallDeadline(0)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != "waiting_ci" {
		t.Fatalf("state = %q, want waiting_ci", obs.MergeQueueState)
	}
	if obs.MergeQueueStalledAt != nil {
		t.Fatalf("a fresh wait is stamped as stalled at %v", obs.MergeQueueStalledAt)
	}

	// Well into the wait and still nothing: no verdict, no run started.
	// The cycle that finds the stall marks it (MergeQueueStalledAt, the
	// Inbox's merge_stalled group) and does no more: escalating it -- to
	// the task's agent or to a person -- is system:pipeline-steward's
	// decision now, not the daemon's.
	stalled := baseTime.Add(patience + time.Minute)
	orchestrator.SetCheckStallDeadline(90 * time.Minute)
	if err := orchestrator.SyncPullRequests(ctx, store, client, stalled); err != nil {
		t.Fatalf("SyncPullRequests once the wait is overlong: %v", err)
	}
	obs = mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueStalledAt == nil {
		t.Fatalf("state = %q with no stall stamp once the wait is overlong:\n%s",
			obs.MergeQueueState, obs.MergeQueueNote)
	}
	if obs.MergeQueueBlockedAt != nil || obs.RetryRequestedAt != nil {
		t.Fatalf("blocked = %v, retry = %v: the daemon escalated a stall on its own",
			obs.MergeQueueBlockedAt, obs.RetryRequestedAt)
	}

	// And it is taken back off the moment the wait is over. The verdict
	// arrives, the state changes, and the new state starts on its own
	// clock -- a row that went on saying "stalled" into the state after it
	// would be the same lie as a stuck row wearing the live mark, told the
	// other way round.
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("success")}}
	if err := orchestrator.SyncPullRequests(ctx, store, client, stalled.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests once the verdict lands: %v", err)
	}
	cleared := mergeQueueStatus(t, ctx, store, task.ID)
	if cleared.MergeQueueState == "waiting_ci" {
		t.Fatalf("state = %q, want the wait to have ended once CI passed", cleared.MergeQueueState)
	}
	if cleared.MergeQueueStalledAt != nil {
		t.Errorf("the stalled stamp survived into %q, stamped %v",
			cleared.MergeQueueState, cleared.MergeQueueStalledAt)
	}
}

// A repair that exhausted its attempts is the third case this was filed
// for, and it is the classification's awaiting-a-person clause rather than
// a driver: the queue asks for exactly one repair per pull request, so a
// change that comes out of one still broken has nothing further coming. The
// state has to say so and name the way out -- because a terminal state
// that reads like a wait is the same fault as a wait nothing is driving,
// from the other side.
func TestARepairThatExhaustedItsAttemptsIsAPersonsAndTheRowSaysSo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("asking for the repair: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("requeueForRepair did not file the one repair this pull request gets")
	}

	// The repair runs, pushes what it can, and the branch is still
	// conflicted. One more cycle and the round is over.
	finished := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, task.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
		// And the round the queue is about to give up on had been
		// flagged for standing still, which is the ordinary way one of
		// these ends up here.
		o.MergeQueueStalledAt = &baseTime
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{finished, finished.Add(time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("SyncPullRequests at %v: %v", at, err)
		}
	}

	obs := mergeQueueStatus(t, ctx, store, task.ID)
	if obs.MergeQueueState != orchestrator.MergeStateFailed || obs.MergeQueueBlockedAt == nil {
		t.Fatalf("state = %q blocked=%v, want %s: the repair is spent and nothing else is coming",
			obs.MergeQueueState, obs.MergeQueueBlockedAt, orchestrator.MergeStateFailed)
	}
	class, driver, control, patience := orchestrator.MergeLaneClaimOf(obs.MergeQueueState)
	if class != "awaiting a person" || driver != "" {
		t.Fatalf("%s is classified %q driven by %q, want awaiting a person and nothing",
			obs.MergeQueueState, class, driver)
	}
	if !strings.Contains(control, "Retry merge") {
		t.Fatalf("control = %q, want the button that restarts the cycle", control)
	}
	if !strings.Contains(obs.MergeQueueNote, "Retry merge") {
		t.Fatalf("note = %q, want the way out on the row a person is looking at", obs.MergeQueueNote)
	}

	// And it is never flagged for standing still, however long it stands.
	// merge_failed not moving is merge_failed working.
	if patience != 0 {
		t.Fatalf("%s has a patience of %s: a state that is terminal by design does not stall",
			obs.MergeQueueState, patience)
	}
	// Which is also why giving up takes the flag off the row: the stamp
	// the last round carried would otherwise put a "Stalled" chip beside
	// a row whose whole message is that a person owns this now
	// (Observation.MergeQueueStalledAt, blockMergeQueue).
	if obs.MergeQueueStalledAt != nil {
		t.Errorf("stalled stamp = %v, want it cleared by the queue giving up", obs.MergeQueueStalledAt)
	}
	week := finished.Add(7 * 24 * time.Hour)
	if _, stalled := orchestrator.MergeLaneOverdue(obs.MergeQueueState, obs.MergeQueueStateAt, week); stalled {
		t.Error("a week in merge_failed was flagged as a stall")
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, week); err != nil {
		t.Fatalf("SyncPullRequests a week later: %v", err)
	}
	if got := mergeQueueStatus(t, ctx, store, task.ID); strings.Contains(got.MergeQueueNote, "stood here") {
		t.Errorf("the row calls a terminal state a stall:\n%s", got.MergeQueueNote)
	}
}
