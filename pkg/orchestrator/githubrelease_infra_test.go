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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// checkout502Release is a GitHub Actions `release images` run on main
// whose checkout was answered 502 -- the failure of 2026-11-01/02.
func checkout502Release(sha string) githubsim.WorkflowRun {
	return githubsim.WorkflowRun{
		ID: 1301, Name: "release", Path: ".github/workflows/release.yml",
		HeadBranch: "main", HeadSHA: sha, Event: "push", Status: "completed", Conclusion: "failure", RunAttempt: 1,
		Jobs: []githubsim.WorkflowJob{{
			Name: "release images", Conclusion: "failure", FailingStep: "Checkout",
			Log: "2026-11-02T03:04:05.0000000Z remote: Internal Server Error\n" +
				"2026-11-02T03:04:05.0000000Z fatal: unable to access 'https://github.com/acme/widgets/': " +
				"The requested URL returned error: 502\n",
		}},
	}
}

func releaseTriaged(t *testing.T, store *model.Store) []string {
	t.Helper()
	ids, err := store.OpenTasksWithTag(t.Context(), orchestrator.BaseReleaseTriageBranchTag(
		model.RepoRef{Owner: "acme", Name: "widgets"}, "main"))
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

// A GitHub Actions release that failed on its checkout's 502 has its
// failed jobs re-run by GitHub, up to maxInfraRetries times, with no
// failure counted and no triage filed; the attempt after the last re-run
// is the failed release it is, and its triage says it was infra, retried.
func TestAGitHubReleaseThatFailedOnInfraIsReRunUpToTheCap(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	sim.WorkflowRuns = []githubsim.WorkflowRun{checkout502Release(headSHA(t, client, repo, "main"))}
	deps := orchestrator.Deps{Store: store, Client: client}

	at := baseTime
	for n := 1; n <= orchestrator.MaxInfraRetries; n++ {
		runGitHubCITails(t, ctx, deps, at)
		if want := slices.Repeat([]int64{1301}, n); !slices.Equal(sim.RerunFailedJobs, want) {
			t.Fatalf("attempt %d: re-runs asked for %v, want %v", n, sim.RerunFailedJobs, want)
		}
		if ids := releaseTriaged(t, store); len(ids) != 0 {
			t.Fatalf("attempt %d: triage %v filed before the re-runs were spent", n, ids)
		}
		rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
		if err != nil || rel == nil || rel.Failures != 0 || !strings.Contains(rel.Detail, "re-running the failed jobs") {
			t.Fatalf("attempt %d: release row %+v (%v), want no failure counted and the re-run named", n, rel, err)
		}
		// GitHub starts the next attempt, and it fails the same way.
		sim.WorkflowRuns[0].RunAttempt = n + 1
		at = at.Add(10 * time.Minute)
	}

	runGitHubCITails(t, ctx, deps, at)
	if got := len(sim.RerunFailedJobs); got != orchestrator.MaxInfraRetries {
		t.Fatalf("%d re-runs asked for, want the cap of %d", got, orchestrator.MaxInfraRetries)
	}
	rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel == nil || rel.Failures != 1 || rel.Conclusion != "failure" {
		t.Fatalf("release row %+v (%v), want a failure counted once the re-runs were spent", rel, err)
	}
	ids := releaseTriaged(t, store)
	if len(ids) != 1 {
		t.Fatalf("triage = %v, want one task once the re-runs were spent", ids)
	}
	task, err := store.GetTask(ctx, ids[0])
	if err != nil || task == nil {
		t.Fatalf("GetTask(%s) = %+v, %v", ids[0], task, err)
	}
	if !strings.Contains(task.Body, "infra, retried 2 times") ||
		!strings.Contains(task.Body, "the requested url returned error: 502") {
		t.Fatalf("triage body does not say it was infra, retried:\n%s", task.Body)
	}
}

// A connection without `actions: write` is refused the re-run: the
// release is recorded and triaged at once, saying why it was not re-run.
func TestAGitHubReleaseWhoseReRunIsRefusedIsTriaged(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	sim.RerunRefused = 403
	sim.WorkflowRuns = []githubsim.WorkflowRun{checkout502Release(headSHA(t, client, repo, "main"))}
	runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime)

	ids := releaseTriaged(t, store)
	if len(ids) != 1 {
		t.Fatalf("triage = %v, want one task for a release GitHub would not re-run", ids)
	}
	task, _ := store.GetTask(ctx, ids[0])
	if task == nil || !strings.Contains(task.Body, "infra, retried 0 times") || !strings.Contains(task.Body, "refused the re-run") {
		t.Fatalf("triage body does not say the re-run was refused:\n%+v", task)
	}
}

// The same 502 printed by a test step is the tree's: nothing is re-run.
func TestAGitHubReleaseThatFailedInATestIsNotReRun(t *testing.T) {
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	defer orchestrator.SetGitHubCIBudgetLow(false)()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	run := checkout502Release(headSHA(t, client, repo, "main"))
	run.Jobs[0].FailingStep = "Run the container e2e suite against what is about to be published"
	sim.WorkflowRuns = []githubsim.WorkflowRun{run}
	runGitHubCITails(t, ctx, orchestrator.Deps{Store: store, Client: client}, baseTime)

	if len(sim.RerunFailedJobs) != 0 {
		t.Fatalf("re-runs asked for %v, want none for a test failure", sim.RerunFailedJobs)
	}
	if ids := releaseTriaged(t, store); len(ids) != 1 {
		t.Fatalf("triage = %v, want one task", ids)
	}
}
