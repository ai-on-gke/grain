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

// A branch that is behind its base and merges cleanly is merged as it
// stands, on the full-suite verdict it already holds
// (grain/task-198). These tests hold the two halves of that apart: what
// the queue no longer does to a green branch when somebody else's change
// lands on `main`, and what it still does -- a real conflict, a real
// failure, and a repository that has asked for the strict rule back.
//
// Like the refresh tests beside them they run against githubsim over a
// real bare repository, so "behind", "up to date" and "conflicts" are
// answered by real git rather than by a flag a test set.
package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// greenGrainCI is a task whose grain CI has run against its branch's
// current head and passed -- the verdict the merge queue requires, about
// the exact commit it requires it about.
func greenGrainCI(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	runner *fakeCI, task model.Task, repo model.RepoRef, branch string) {

	t.Helper()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionSuccess)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatalf("collecting the grain CI verdict: %v", err)
	}
}

// requireUpToDate turns the strict rule on for repo -- the per-repository
// option that is off by default (model.RepoConfig.RequireBranchUpToDate).
func requireUpToDate(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) {
	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, RequireBranchUpToDate: true}); err != nil {
		t.Fatal(err)
	}
}

// The change this whole task is about: `main` moves while a branch holds
// a green full-suite verdict, and the queue merges the branch rather than
// bringing it up to date first. No new head is made, so no new suite is
// started, so the verdict the branch already holds is still the verdict
// it merges on.
func TestMergeQueueMergesABranchThatIsBehindOnTheVerdictItAlreadyHolds(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	tested := headSHA(t, client, repo, branch)

	// Somebody else's change lands on main. The branch is now behind it
	// and still merges cleanly.
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)
	started := runner.started()

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests over a branch that is behind: %v", err)
	}

	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("the queue made %d branch merges, want none: a branch that is behind but "+
			"merges cleanly is merged as it stands", n)
	}
	if now := headSHA(t, client, repo, branch); now != tested {
		t.Fatalf("the head moved from %s to %s: the verdict the queue had was thrown away", tested, now)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("the branch did not merge, though it was clean and held a green full-suite verdict")
	}
	// And nothing asked for another suite: the reconciler is level
	// triggered off the branch head, and the head did not move.
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	if runner.started() != started {
		t.Fatalf("grain CI was started %d more times; want none -- the head never changed",
			runner.started()-started)
	}
}

// The exact-head rule is untouched by any of this: a verdict about some
// other commit is not a pass, however cleanly the branch merges and
// however recently that other commit was the head.
func TestABranchThatIsBehindStillNeedsAVerdictAboutItsOwnHead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)

	// The branch moves on after that verdict, and main moves too.
	pushFileToBranch(t, sim.BareRepo, branch, "LATE.md", "one more commit", "another commit on the branch")
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("merged on a verdict about a commit that is no longer the head")
	}
}

// The one case where a branch really does have to be updated, and the one
// this change leaves exactly as it was: a branch that no longer merges
// cleanly goes back to an agent to resolve.
func TestABranchThatConflictsStillGoesBackToAnAgent(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)

	// The same file, written differently on both sides: a real conflict,
	// which real git will refuse when the queue asks it to merge.
	pushFileToBranch(t, sim.BareRepo, branch, "SHARED.md", "the branch's answer", "branch edit")
	pushFileToBranch(t, sim.BareRepo, "main", "SHARED.md", "main's answer", "main edit")
	setMergeable(sim, false)
	// The verdict has to be about the branch's new head for the queue to
	// be judging anything but staleness.
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests over a conflicted head: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("merged a branch that does not merge cleanly")
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("a conflicting branch was not sent back to an agent")
	}
}

// The option, in the direction nobody gets without asking: a repository
// that wants its branches up to date has the queue merge the base in and
// wait for CI on the commit that makes, instead of merging what it has.
func TestARepositoryCanStillRequireBranchesToBeUpToDate(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	requireUpToDate(t, ctx, store, repo)
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	tested := headSHA(t, client, repo, branch)

	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests under the strict rule: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want exactly one", n)
	}
	if !branchContains(t, sim.BareRepo, branch, "main") {
		t.Fatal("the branch does not contain main: nothing was actually brought up to date")
	}
	if mergedBranch(sim, branch) {
		t.Fatal("merged on the old verdict after deciding the branch had to be up to date first")
	}
	if now := headSHA(t, client, repo, branch); now == tested {
		t.Fatal("the head did not move, so nothing will be re-tested")
	}
	bodies := commentBodies(t, ctx, store, task.ID)
	if len(bodies) == 0 || !strings.Contains(bodies[len(bodies)-1], "require_branch_up_to_date") {
		t.Fatalf("the queue did not say why it wrote to somebody's branch: %q", bodies)
	}

	// And the branch merges on the next round, once CI has answered about
	// the commit that merge made.
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after the update: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 2 {
		t.Fatalf("the queue made %d branch merges, want two -- the second answering 204", n)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("the branch never merged, though it is now up to date and green")
	}
}

// A head grain's own CI failed is not offered a base merge: that verdict
// is about the branch's exact commit, so no merge of the base into it can
// make the verdict wrong, and spending a second twenty-minute suite to
// find that out is exactly what this change is for. The repair is asked
// for on the same cycle instead.
func TestAGrainCIFailureIsNotGivenASpeculativeBaseMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionFailure)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests over a head grain CI failed: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 0 {
		t.Fatalf("the queue made %d branch merges of a head whose own commit was tested and "+
			"failed, want none", n)
	}
	if !repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("no repair was asked for a head that genuinely failed")
	}
}

// The same head, in a repository that has asked for the strict rule: it
// wants its branches carrying their base, and a broken branch is not an
// exception, so the base merge is made exactly as it always was.
func TestAStrictRepositoryStillRefreshesAFailingHead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	requireUpToDate(t, ctx, store, repo)
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	runner := newFakeCI()
	status := startRequiredCI(t, ctx, store, client, runner, task, repo, branch)
	runner.finish(status.RunID, ci.ConclusionFailure)
	if err := runCIReconciler(t, ctx, store, client, runner); err != nil {
		t.Fatal(err)
	}
	pushFileToBranch(t, sim.BareRepo, "main", "MAIN.md", "unrelated work", "unrelated change on main")
	setMergeable(sim, true)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests under the strict rule: %v", err)
	}
	if n := mergeWrites(sim, repo); n != 1 {
		t.Fatalf("the queue made %d branch merges, want exactly one", n)
	}
	if repairInFlightNow(t, ctx, store, task.ID) {
		t.Fatal("asked for a repair before the refresh it made had been judged")
	}
}

// When a pull request lands on the base branch (or when waiting behind another
// entry in the merge queue), any queued pull request behind the head that
// develops merge conflicts is immediately transitioned to repair_needed and
// dispatched for repair without waiting to reach the front of the queue.
func TestQueuedPullRequestBehindHeadIsRepairedImmediatelyWhenConflicted(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	head, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	behind, behindBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	// Head PR is clean but checks are in progress, holding its head position.
	sim.CheckRuns[headBranch] = []github.CheckRun{{Name: "tests", Status: "in_progress"}}
	// Behind PR has merge conflicts against base.
	setBranchMergeable(sim, behindBranch, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	// Head remains pending and is not repaired.
	if repairInFlightNow(t, ctx, store, head.ID) {
		t.Fatal("expected head to not be sent for repair while checks are running")
	}
	if mergedBranch(sim, headBranch) {
		t.Fatal("head PR should not have merged while checks are running")
	}

	// Behind PR (which is not head) is immediately sent for repair!
	if !repairInFlightNow(t, ctx, store, behind.ID) {
		t.Fatal("queued PR behind head was not immediately sent back for repair upon conflict")
	}

	st, err := store.State(ctx, behind.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateQueued {
		t.Fatalf("state = %q, want queued: conflicted non-head task should be queued for repair", st)
	}
}

// Lifecycle test: A queued pull request develops merge conflicts against base,
// is immediately repaired in parallel, and once repaired and green, returns to the
// queue and cleanly merges when its turn comes.
func TestConflictedQueuedPullRequestLifecycleAfterRepair(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	_, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	behind, behindBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)

	setMergeable(sim, true)
	sim.CheckRuns[headBranch] = greenCheck
	sim.CheckRuns[behindBranch] = greenCheck
	// Behind PR develops merge conflict against updated base.
	setBranchMergeable(sim, behindBranch, false)

	// Cycle 1: head PR merges cleanly, behind PR is immediately queued for repair.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("first SyncPullRequests: %v", err)
	}

	if !mergedBranch(sim, headBranch) {
		t.Fatal("head PR should have merged cleanly")
	}
	if !repairInFlightNow(t, ctx, store, behind.ID) {
		t.Fatal("behind PR was not sent for repair immediately")
	}

	// Simulate repair completion: agent resolves conflicts, pushes fix, and marks task complete.
	repairTime := baseTime.Add(2 * time.Minute)
	pushFileToBranch(t, sim.BareRepo, behindBranch, "RESOLVED.md", "clean resolution", "resolved conflict")
	setBranchMergeable(sim, behindBranch, true)
	sim.CheckRuns[behindBranch] = greenCheck
	if err := store.Observe(ctx, model.Observation{
		TaskID:             behind.ID,
		MergeQueueRepairAt: &baseTime,
		CompletedAt:        &repairTime,
	}); err != nil {
		t.Fatal(err)
	}

	// Cycle 2: behind PR is now at the head of the queue, clean and green, and merges.
	if err := orchestrator.SyncPullRequests(ctx, store, client, repairTime.Add(time.Minute)); err != nil {
		t.Fatalf("second SyncPullRequests: %v", err)
	}

	if !mergedBranch(sim, behindBranch) {
		t.Fatal("repaired PR did not merge cleanly once resolved")
	}
	st, err := store.State(ctx, behind.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed: repaired PR should have merged and closed", st)
	}
}

// Multiple queued pull requests that develop conflicts are repaired in parallel.
func TestMultipleQueuedPullRequestsWithConflictsRepairInParallel(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	_, headBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	third, thirdBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t3", repo)

	setMergeable(sim, true)
	sim.CheckRuns[headBranch] = []github.CheckRun{{Name: "tests", Status: "in_progress"}}
	setBranchMergeable(sim, secondBranch, false)
	setBranchMergeable(sim, thirdBranch, false)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if !repairInFlightNow(t, ctx, store, second.ID) {
		t.Fatal("expected second queued task to have repair in flight")
	}
	if !repairInFlightNow(t, ctx, store, third.ID) {
		t.Fatal("expected third queued task to have repair in flight")
	}
}
