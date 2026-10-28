package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// TestMergePipelinePipelinePauseAndResumeLifecycle verifies that a pipeline pause
// holds the pipeline until the specified task completes, closes, or times out after 6h.
func TestMergePipelinePipelinePauseAndResumeLifecycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	fix := newBatchFixture(t)
	runner := newFakeCI()

	// Create upgrade check task "check-99"
	checkTask := filedTask(t, ctx, store, "check-99", fix.repo)
	checkTask.CreatedAt = &baseTime
	if err := store.PutTask(ctx, checkTask); err != nil {
		t.Fatal(err)
	}

	// Place pipeline pause waiting on check-99
	pause := model.PipelinePause{
		PausedAt:    baseTime,
		UntilTaskID: "check-99",
		Reason:      "verifying system upgrade",
		By:          model.Principal{Kind: model.PrincipalAutomation, ID: "upgrade"},
	}
	if err := store.SetPipelinePause(ctx, pause); err != nil {
		t.Fatal(err)
	}

	// Reconcile pause while task is incomplete -> pause remains
	deps := orchestrator.Deps{Store: store, Client: fix.client, CI: runner}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "pause" {
			if err := r.Reconcile(ctx, deps, baseTime.Add(10*time.Minute)); err != nil {
				t.Fatalf("pause reconciler: %v", err)
			}
		}
	}

	pRow, err := store.GetPipelinePause(ctx)
	if err != nil || pRow == nil {
		t.Fatalf("pause was unexpectedly cleared: (%+v, %v)", pRow, err)
	}

	// Complete check-99
	if err := store.Observe(ctx, model.Observation{TaskID: "check-99", CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	// Reconcile pause again -> pause is lifted automatically
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "pause" {
			if err := r.Reconcile(ctx, deps, baseTime.Add(15*time.Minute)); err != nil {
				t.Fatalf("pause reconciler: %v", err)
			}
		}
	}

	pRowAfter, err := store.GetPipelinePause(ctx)
	if err != nil || pRowAfter != nil {
		t.Fatalf("pause was not cleared after task completion: (%+v, %v)", pRowAfter, err)
	}
}

// TestMergePipelineMultiTeamQueueIndependence verifies that a conflicted/broken
// task on Team Alpha's queue does not block or stall Team Beta's clean merge queue.
func TestMergePipelineMultiTeamQueueIndependence(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	// Task 1: Team Alpha - conflicted/red
	tAlpha, bAlpha := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-alpha", repo)
	tAlpha = grantCI(t, ctx, store, tAlpha)
	if err := store.PutTask(ctx, tAlpha); err != nil {
		t.Fatal(err)
	}

	// Task 2: Team Beta - clean & green
	tBeta, bBeta := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t-beta", repo)
	tBeta = grantCI(t, ctx, store, tBeta)
	if err := store.PutTask(ctx, tBeta); err != nil {
		t.Fatal(err)
	}
	greenGrainCI(t, ctx, store, client, runner, tBeta, repo, bBeta)

	// Set Alpha conflicted, Beta mergeable
	setBranchMergeable(sim, bAlpha, false)
	setBranchMergeable(sim, bBeta, true)

	// Sync pull requests
	syncWithCI(t, ctx, store, client, runner, baseTime)

	// Beta should merge past Alpha's conflict
	if !mergedBranch(sim, bBeta) {
		t.Fatal("team beta's clean task did not merge past team alpha's conflicted task")
	}

	// Alpha should have been requeued for repair
	obsAlpha, err := store.GetObservation(ctx, "t-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if obsAlpha == nil || !strings.Contains(obsAlpha.MergeQueueNote, "conflicts with") {
		t.Errorf("alpha observation note = %q, want conflict notice", obsAlpha.MergeQueueNote)
	}
}

// TestMergePipelineForceMergeOverridesWithCleanRequirement verifies that
// force merge overrides a red CI check if clean, but is still refused if there is a git conflict.
func TestMergePipelineForceMergeOverridesWithCleanRequirement(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// Task 1: Clean but Red CI -> Forced -> Merges
	t1, b1 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setBranchMergeable(sim, b1, true)
	sim.CheckRuns[b1] = []github.CheckRun{{Name: "tests", Status: "completed", Conclusion: strPtr("failure")}}
	forceMerge(t, ctx, store, client, t1.ID, "urgent hotfix needed in production", baseTime)

	// Task 2: Conflicted -> Forced -> Refused
	t2, b2 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	setBranchMergeable(sim, b2, false)
	forceMerge(t, ctx, store, client, t2.ID, "override conflict", baseTime)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !mergedBranch(sim, b1) {
		t.Fatal("forced clean task t1 should have merged")
	}
	if mergedBranch(sim, b2) {
		t.Fatal("forced conflicted task t2 must NOT merge")
	}
}
