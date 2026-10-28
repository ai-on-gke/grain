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

package orchestrator_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
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
