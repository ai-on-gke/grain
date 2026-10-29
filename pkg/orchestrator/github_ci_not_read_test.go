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

// The merge queue's half of a repository that has declared GitHub's
// check runs are not a source of verdicts for it
// (model.RepoConfig.IgnoreGitHubCI). Every case here has its twin with
// the declaration off somewhere in ci_test.go or mergequeue_test.go --
// "with it off, everything behaves exactly as now" is half of what the
// setting has to be true of, and the existing suites are that half.

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// declareGrainCIGate is the stored declaration, per repository -- the
// thing an operator sets on the repository pane, and the thing this
// whole change reads.
func declareGrainCIGate(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) {
	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, IgnoreGitHubCI: true}); err != nil {
		t.Fatal(err)
	}
}

// redOnGitHub is this repository as it has stood since GitHub Actions
// was blocked at the account level: every check red, on the branch and
// on main alike, for a reason no commit can fix.
func redOnGitHub(sim *githubsim.Sim, branch string) {
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("failure")},
	}
}

// The headline: with the declaration on, a red GitHub check neither
// makes the pull request unhealthy nor holds the queue, and the merge
// happens on grain CI's verdict alone.
func TestARedGitHubCheckDoesNotHoldTheQueueWhereItIsNotRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareGrainCIGate(t, ctx, store, repo)
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, newFakeCI(), task, repo, branch)
	redOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !mergedBranch(sim, branch) {
		t.Fatal("a pull request grain CI passed was held by a GitHub check this repository " +
			"has declared it does not read")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("an agent was sent to repair a check nobody read")
	}
}

// The control, and it has to be a task *without* grain CI to be one: a
// task that holds grain-ci was already judged on grain CI alone, so the
// declaration changes nothing for it (forge.HealthOf, and "Make Grain CI
// authoritative for merges"). What the declaration changes is the
// repository whose tasks have no grain CI grant of their own, and for
// that repository, with the declaration off, GitHub's red check holds
// the queue and earns the one automatic repair exactly as before.
func TestWithoutTheDeclarationTheSameRedCheckStillHoldsTheQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	redOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, branch) {
		t.Fatal("an ordinary repository merged with a required check red")
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("an ordinary repository's red check earned no repair")
	}
	// And the repair comment still carries the standing sentence, which
	// is right everywhere GitHub's checks are read.
	bodies := commentBodies(t, ctx, store, task.ID)
	if !strings.Contains(bodies[len(bodies)-1], "already red on the base") {
		t.Errorf("the ordinary repair comment lost its scope sentence:\n%s", bodies[len(bodies)-1])
	}
}

// The repair comment. Its standing sentence -- "a check already red on
// the base still stops this pull request merging, so fix it here" -- is
// a false instruction in a repository whose gate is grain CI: nobody
// read that check, nothing is waiting on it, and a repair spent chasing
// it is a repair spent on nothing. This deployment's agents argued with
// that sentence in their closing notes for a week.
func TestTheRepairCommentDoesNotSendAnAgentAfterAnUnreadCheck(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareGrainCIGate(t, ctx, store, repo)
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	redGrainCI(t, ctx, store, client, task)
	redOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a red grain CI run earned no repair")
	}

	bodies := commentBodies(t, ctx, store, task.ID)
	repair := bodies[len(bodies)-1]
	if strings.Contains(repair, "already red on the base") {
		t.Errorf("the repair comment still claims an unread GitHub check holds the merge:\n%s", repair)
	}
	for _, want := range []string{"gate is grain CI", "not read here", branch} {
		if !strings.Contains(repair, want) {
			t.Errorf("the repair comment does not say %q:\n%s", want, repair)
		}
	}
}

// budgetBlockedActions is what this account's Actions have looked like
// since the budget ran out: a workflow run against the commit that
// GitHub concluded before any job of it ran a step, which is the one CI
// shape no commit pushed to the branch can clear.
func budgetBlockedActions(sim *githubsim.Sim, branch string) {
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{{
		Name:          "tests",
		Conclusion:    "failure",
		NeverStarted:  true,
		FailureReason: "The job was not started because an Actions budget is preventing further use.",
	}}
}

// The evidence section under that comment. Saying "GitHub's check runs
// are not read here" and then heading the longest section of the same
// message "What CI says" -- filled with Actions runs, and ending in an
// emphatic "act on that sentence rather than on the list above" -- is a
// message that contradicts itself and points at the half to ignore. It
// cost grain/task-355's bounce, which is the last automatic attempt a
// pull request gets, two screens of its prompt on an exhausted billing
// account.
func TestTheRepairCommentQuotesNoGitHubRunWhereGitHubIsNotRead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareGrainCIGate(t, ctx, store, repo)
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	redGrainCI(t, ctx, store, client, task)
	redOnGitHub(sim, branch)
	budgetBlockedActions(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	repair := bodies[len(bodies)-1]

	// What is left is the verdict that actually holds the merge, and the
	// one sentence that orients a reader who can see a red pull request
	// on GitHub and needs telling why nobody is chasing it.
	for _, want := range []string{"not read here", ".grain/workflows"} {
		if !strings.Contains(repair, want) {
			t.Errorf("the repair comment does not say %q:\n%s", want, repair)
		}
	}
	for _, unwanted := range []string{
		"What CI says",
		"What CI printed",
		"Actions budget",
		"Act on that sentence",
		"workflow run",
	} {
		if strings.Contains(repair, unwanted) {
			t.Errorf("the repair comment still quotes GitHub Actions (%q):\n%s", unwanted, repair)
		}
	}
	// Not read, rather than read and then left out: the reads cost this
	// deployment a round-trip each, on a provider it has said it does
	// not listen to.
	for _, call := range sim.Calls {
		if strings.Contains(call.Path, "/actions/") {
			t.Errorf("the queue asked GitHub Actions about a repository that does not read it: %s %s",
				call.Method, call.Path)
		}
	}
}

// The control for it: with the declaration off, the same build still
// hands the repair everything GitHub said, explainer included. This is a
// routing fix, and most repositories are on the other side of the route.
func TestWithoutTheDeclarationTheRepairCommentStillQuotesTheRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	redOnGitHub(sim, branch)
	sim.WorkflowJobs[branch] = []githubsim.WorkflowJob{{
		Name: "tests", Conclusion: "failure", Log: "--- FAIL: TestSomething",
	}}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("an ordinary repository's red check earned no repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	repair := bodies[len(bodies)-1]
	for _, want := range []string{"What CI printed", "TestSomething"} {
		if !strings.Contains(repair, want) {
			t.Errorf("an ordinary repository's repair lost %q, the job output it is for:\n%s",
				want, repair)
		}
	}
}

// The decision: a repository that turns this on and holds no grain CI
// merges nothing rather than everything. Nothing has judged the commit,
// so the queue waits -- visibly, on the ordinary clock -- instead of
// reading an empty check list as a clean bill of health.
func TestTheGateWithNoGrainCIMergesNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declareGrainCIGate(t, ctx, store, repo)
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	// No grantCI and no verdict: the task was filed without grain CI in
	// a repository whose only gate is grain CI.
	greenOnGitHub(sim, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if mergedBranch(sim, branch) {
		t.Fatal("a repository that reads no checks and runs no grain CI merged with no " +
			"verdict at all -- the setting has become a way to merge ungated")
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("an agent was sent to repair a branch nothing has found fault with")
	}
}

// The configuration repository is served by grain itself and never
// reaches GitHub, so its red is grain CI's whatever the repository's
// declaration says. Its repair used to carry, under the grain CI note,
// "What CI says: GitHub Actions has no workflow run at all against this
// commit ... Buildkite, CircleCI" -- the native client's empty Actions
// answer rendered as evidence -- and the note itself sent the run to a
// comment quoting the failing step's output that the task's terse
// verdict comment has not carried since 43a612cbb
// (docs/test-runs/2026-10-15-team-config-ci-check.md).
func TestAConfigRepositoryRepairPointsAtGrainCIAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "grain", "config", "main")
	repo := model.ConfigRepo()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	redGrainCI(t, ctx, store, client, task)
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a red grain CI run on a config repository earned no repair")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	repair := bodies[len(bodies)-1]
	for _, want := range []string{".grain/workflows", "grain ci log", branch} {
		if !strings.Contains(repair, want) {
			t.Errorf("the repair comment does not say %q:\n%s", want, repair)
		}
	}
	for _, unwanted := range []string{
		"What CI says",
		"GitHub Actions has no workflow run",
		"Buildkite",
		"quotes the failing step's output",
	} {
		if strings.Contains(repair, unwanted) {
			t.Errorf("the config repository's repair says %q:\n%s", unwanted, repair)
		}
	}
	for _, call := range sim.Calls {
		if strings.Contains(call.Path, "/actions/") {
			t.Errorf("the queue asked GitHub Actions about a config repository: %s %s", call.Method, call.Path)
		}
	}
}
