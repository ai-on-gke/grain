package orchestrator_test

// The other end of ui.Client.RetryMerge: what the merge queue does with
// a pull request a human has asked it to have another go at. The queue
// gives up exactly once and never revisits that on its own, so a retry
// is the only thing that ever puts one of these back in front of it --
// and a retry that the very next cycle undid would be no retry at all.

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/ui"
)

// retryMerge is a human clicking "Retry merge" -- through ui.Client
// itself rather than through a hand-written store write, so that what
// these tests put in front of the queue is exactly what the button does.
func retryMerge(t *testing.T, ctx context.Context, store *model.Store, taskID string, now time.Time) {
	t.Helper()
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store)
	client.Now = func() time.Time { return now }
	if err := client.RetryMerge(ctx, taskID); err != nil {
		t.Fatalf("RetryMerge(%s): %v", taskID, err)
	}
}

// A head the queue gave up on after its one repair did not take, retried
// by hand: the queue takes it back, and it gets a whole fresh round --
// the base merged in again first, and a second repair run if that does
// not settle it. Keeping either stamp would make the retry a no-op that
// escalated again on the same cycle it was asked for.
func TestSyncPullRequestsGivesARetriedHeadAnotherRepairRound(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	// Round one, in full: repair asked for, repair finishes, pull request
	// still conflicted, queue gives up.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("asking for the first repair: %v", err)
	}
	firstRepair := repairAskedAt(t, ctx, store, task.ID)
	finished := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, task.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{finished, finished.Add(time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("SyncPullRequests settling the first round: %v", err)
		}
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue never gave up, so there is nothing for a retry to undo")
	}

	// The human has had a look, done whatever they were going to do, and
	// asked for another attempt.
	retryAt := finished.Add(2 * time.Hour)
	retryMerge(t, ctx, store, task.ID, retryAt)

	if err := orchestrator.SyncPullRequests(ctx, store, client, retryAt.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests on the cycle after the retry: %v", err)
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatal("the queue gave up again on the first cycle after a retry, without trying anything")
	}
	if !obs.RepairInFlight() {
		t.Fatalf("observation = %+v, want a second repair asked for: a retry buys a whole round", obs)
	}
	if second := repairAskedAt(t, ctx, store, task.ID); !second.After(firstRepair) {
		t.Fatalf("MergeQueueRepairAt = %v, want a stamp newer than the first round's %v", second, firstRepair)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued: the repair is the task itself going back to work", st)
	}

	// And the bound still holds on the other side of the retry: the
	// second repair gets exactly the one round the first did.
	secondFinished := retryAt.Add(3 * time.Hour)
	if err := store.ObserveField(ctx, task.ID, secondFinished, func(o *model.Observation) {
		o.CompletedAt = &secondFinished
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{secondFinished, secondFinished.Add(time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("SyncPullRequests settling the second round: %v", err)
		}
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("a retried head that stayed broken was never handed back to a person")
	}
}

// The stall clock is in memory and keyed by pull request, so a retry has
// nothing of its own to clear there -- which is why a blocked entry is
// not timed at all (queueEntry.observe). Without that, a task given up
// on for checks that never finished would come back with a clock already
// reading hours, and the very next cycle would give up on it again
// before anybody could see it try.
func TestSyncPullRequestsGivesARetriedStalledHeadAWholeDeadline(t *testing.T) {
	deadline := 2 * time.Hour
	defer orchestrator.SetCheckStallDeadline(deadline)()

	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{{Name: "deploy approval", Status: "queued"}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests on the cycle the checks went pending: %v", err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests once the deadline elapsed: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatal("the queue never gave up on checks that never finished")
	}

	// Somebody has poked whatever the checks were waiting on and asked
	// for another go. The checks are still unfinished -- that is the
	// point: the wait starts again rather than resuming where it was.
	retryAt := baseTime.Add(deadline + time.Minute)
	retryMerge(t, ctx, store, task.ID, retryAt)

	for _, at := range []time.Time{retryAt, retryAt.Add(deadline / 2), retryAt.Add(deadline - time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("SyncPullRequests inside the retried deadline: %v", err)
		}
		obs, err = store.GetObservation(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if obs.MergeQueueBlockedAt != nil {
			t.Fatalf("gave up again %s into a retry, on a clock the retry should have restarted",
				at.Sub(retryAt))
		}
	}

	// Past the new deadline it gives up again, once, and says so a second
	// time -- a retry buys another full wait, not an unbounded one.
	if err := orchestrator.SyncPullRequests(ctx, store, client, retryAt.Add(deadline)); err != nil {
		t.Fatalf("SyncPullRequests past the retried deadline: %v", err)
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueBlockedAt == nil {
		t.Fatal("a retried head whose checks never finished was waited on forever")
	}
}

// A task that undergoes a merge-queue repair attempt and finishes cleanly
// (with its PR updated and CI green) must have Observation.RetryRequestedAt
// cleared so it is not immediately re-dispatched while awaiting merge.
func TestRepairCompletionDoesNotTriggerRedundantRedispatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	// Cycle 1: PR is conflicted. SyncPullRequests triggers requeueForRepair.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("triggering repair: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.MergeQueueRepairAt == nil || obs.RetryRequestedAt == nil || obs.CompletedAt != nil {
		t.Fatalf("expected repair requested: %+v", obs)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want %q during repair requeue", st, model.StateQueued)
	}

	// The repair run is dispatched and executed.
	repairTime := baseTime.Add(30 * time.Minute)
	pushFile(t, sim.BareRepo, branch, "fix.txt", "fixed")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenChecks()

	// ProcessResult finishes the run and opens/links PR, setting CompletedAt and clearing RetryRequestedAt.
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-2", repairTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set after repair run completion")
	}
	if obs.RetryRequestedAt != nil {
		t.Fatalf("expected RetryRequestedAt to be cleared after repair run completion, got: %v", obs.RetryRequestedAt)
	}
	if obs.RepairInFlight() {
		t.Fatal("expected RepairInFlight() to be false after repair run completion")
	}

	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateCompleted {
		t.Fatalf("state = %q, want %q while awaiting merge", st, model.StateCompleted)
	}

	// Verify dispatch.Cycle does NOT re-dispatch this task.
	limits := model.Limits{Workers: 5, Mergers: 5}
	dispatches, err := dispatch.Cycle(ctx, store, limits, repairTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("dispatch.Cycle: %v", err)
	}
	if len(dispatches) > 0 {
		t.Fatalf("expected 0 dispatches, got %d: %+v", len(dispatches), dispatches)
	}

	// Verify the merge queue reconciles and lands the clean settled repair PR.
	settleTime := repairTime.Add(2 * time.Minute)
	if err := orchestrator.SyncPullRequests(ctx, store, client, settleTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	st, err = store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want %q after PR merge", st, model.StateClosed)
	}
	obs, err = store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs.PrMergedAt == nil {
		t.Fatal("expected PrMergedAt to be set after PR merge")
	}
}
