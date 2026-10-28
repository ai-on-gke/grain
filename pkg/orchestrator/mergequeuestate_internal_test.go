package orchestrator

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A merge-queue cycle holds the observation it read before looking at the
// pull request; a repair run finishing in between marks the task complete
// and clears its retry request. Writing the queue's state from the older
// copy must not undo that: a task read as queued again is dispatched a
// second time rather than merged (ui/e2e/team-config-repo.spec.js).
func TestSetMergeQueueStateLeavesAFinishedRunsFieldsAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openTestStore(t)
	const taskID = "7"
	repairAt := time.Date(2026, 10, 17, 19, 18, 24, 0, time.UTC)
	if err := store.ObserveField(ctx, taskID, repairAt, func(o *model.Observation) {
		markRepairAsked(o, repairAt, "", "", "")
	}); err != nil {
		t.Fatal(err)
	}
	stale, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}

	finished := repairAt.Add(time.Second)
	if err := store.ObserveField(ctx, taskID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
		o.RetryRequestedAt = nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := setMergeQueueState(ctx, store, stale, taskID,
		queueStatus{state: mergeStateRepairing, note: "an agent is repairing this branch now"},
		finished.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletedAt == nil || !got.CompletedAt.Equal(finished) {
		t.Errorf("CompletedAt = %v, want the finish's %v", got.CompletedAt, finished)
	}
	if got.RetryRequestedAt != nil {
		t.Errorf("RetryRequestedAt = %v, want the finish's clear to stand", got.RetryRequestedAt)
	}
	if got.MergeQueueState != mergeStateRepairing {
		t.Errorf("MergeQueueState = %q, want %q", got.MergeQueueState, mergeStateRepairing)
	}
	if stale.MergeQueueState != mergeStateRepairing {
		t.Errorf("the caller's copy has MergeQueueState %q, want %q", stale.MergeQueueState, mergeStateRepairing)
	}
}
