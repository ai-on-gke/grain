package orchestrator_test

import (
	"testing"

	"github.com/bwsalmon/grain/pkg/github"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

func TestBudgetExhaustedActionsRunsDoNotBlockMergeQueueWhenGrainCIPassed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, newFakeCI(), task, repo, branch)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{
			Name:          "tests",
			Status:        "completed",
			Conclusion:    strPtr("failure"),
			FailureReason: "The job was not started because an Actions budget is preventing further use.",
			StepsExecuted: 0,
		},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !mergedBranch(sim, branch) {
		t.Fatal("a pull request where Grain CI passed was blocked by an external GitHub Actions budget exhaustion failure")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a branch repair was triggered for an external GitHub Actions budget exhaustion failure when Grain CI passed")
	}
}

func TestBudgetExhaustedActionsRunsStillBlockWhenGrainCIHasNotPassed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	_, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)

	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{
			Name:          "tests",
			Status:        "completed",
			Conclusion:    strPtr("failure"),
			FailureReason: "The job was not started because an Actions budget is preventing further use.",
			StepsExecuted: 0,
		},
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, branch) {
		t.Fatal("a pull request merged without passing CI")
	}
}
