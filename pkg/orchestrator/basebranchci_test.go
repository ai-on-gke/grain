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

// The compensating control for a merge queue that merges branches which
// are behind their base (grain/task-198): grain runs its own CI on the
// base branch after every merge, and these tests are what it now does
// about the answer -- stop merging into a branch that is red, say so, and
// file the one repair that names the merge which broke it.
package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// runBaseBranchCIReconciler runs the cycle's own "base-branch-ci"
// reconciler, found by name in Reconcilers() so that a change dropping it
// from the cycle fails here rather than quietly turning the gate off.
func runBaseBranchCIReconciler(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner ci.Runner, now time.Time) error {

	t.Helper()
	deps := orchestrator.Deps{Store: store, Client: client, CI: runner}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "base-branch-ci" {
			return r.Reconcile(ctx, deps, now)
		}
	}
	t.Fatal(`no "base-branch-ci" reconciler in the cycle: a red base branch is watched by nothing`)
	return nil
}

// syncWithCI is the pull-request reconciler with a CI runner wired, which
// is what makes a merge record the base branch's new tip
// (recordPostMergeCI).
func syncWithCI(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	runner ci.Runner, now time.Time) {

	t.Helper()
	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil,
		orchestrator.StateRepoGate{CI: runner}, model.DefaultMaxMergesPerRepo, now); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
}

// syncWithCIOneMergePerCycle is syncWithCI with each lane held to one
// merge a cycle, as model.DefaultMaxMergesPerRepo was before schema 84:
// for a test about what the cycle after a merge does with the entry left
// behind it.
func syncWithCIOneMergePerCycle(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	runner ci.Runner, now time.Time) {

	t.Helper()
	if err := orchestrator.SyncPullRequestsGuarding(ctx, store, client, nil,
		orchestrator.StateRepoGate{CI: runner}, 1, now); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
}

// baseBranchRunAfterMerge is the merge path as it now works: the merge
// records the branch's new tip and the base-branch reconciler is what
// starts the run against it, one per branch at a time (grain/task-334).
// It returns the row that run is recorded on.
func baseBranchRunAfterMerge(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner ci.Runner, repo model.RepoRef,
	branch string, now time.Time) *model.BaseBranchCI {

	t.Helper()
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, now); err != nil {
		t.Fatalf("starting %s's own run: %v", branch, err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, branch)
	if err != nil || row == nil {
		t.Fatalf("no run was recorded for %s: %+v (%v)", branch, row, err)
	}
	if row.RunID == "" || row.Conclusion != "" {
		t.Fatalf("%s's row reads run %q, verdict %q, want a fresh run with no verdict yet",
			branch, row.RunID, row.Conclusion)
	}
	return row
}

func mergeQueueStateOf(t *testing.T, ctx context.Context, store *model.Store, taskID string) (string, string) {
	t.Helper()
	obs, err := store.GetObservation(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil {
		return "", ""
	}
	return obs.MergeQueueState, obs.MergeQueueNote
}

// The whole of it: one change merges, the base branch's own suite goes
// red and is confirmed, no repair is filed by the daemon (that is
// system:pipeline-steward's), and the queue carries on merging. The last of those changed in grain/task-471 -- the run is
// information, not a gate -- and it is asserted here rather than only in
// the test below it, because "did the red hold anything?" is the question
// this test used to answer the other way round.
func TestARedBaseBranchIsConfirmedAndHoldsNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, secondBranch)
	setMergeable(sim, true)

	// The first change merges, and the next cycle starts main's own run
	// against the tip that merge produced.
	syncWithCIOneMergePerCycle(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("the first change did not merge")
	}
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Second))
	if row.MergedTaskID != first.ID {
		t.Fatalf("the run records %q as the merge it is the answer to, want %q", row.MergedTaskID, first.ID)
	}

	// It fails. Nothing is decided by the failure alone -- the reconciler
	// is what reads it.
	runner.finish(row.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Name: "go",
		Conclusion: ci.ConclusionFailure,
	})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("collecting main's own verdict: %v", err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Broken() {
		t.Fatalf("main's verdict reads %q, want a failure", row.Conclusion)
	}
	// The rows say so at once, but nobody is sent anywhere until the
	// failure has been reproduced (grain/task-202).
	if row.RepairTaskID != "" {
		t.Fatal("a repair was filed on the first failure, before anything checked it reproduces")
	}
	if row.Confirmation != model.BaseCIConfirming {
		t.Fatalf("main's row says %q, want a confirmation in flight", row.Confirmation)
	}
	confirm := runner.lastRequest(t)
	if !confirm.Recheck {
		t.Fatal("the confirming run may be served from the cache, so it would replay the same failure")
	}
	if len(confirm.Jobs) != 1 || confirm.Jobs[0] != ".grain/workflows/tests.yml:go" {
		t.Fatalf("the confirming run asks for %v, want only the job that failed", confirm.Jobs)
	}

	// It fails again. Now the failure is the branch's own.
	runner.finish(row.ConfirmRunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Name: "go",
		Conclusion: ci.ConclusionFailure,
	})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(90*time.Second)); err != nil {
		t.Fatalf("collecting the confirming run: %v", err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Confirmed() {
		t.Fatalf("main's row says %q after a second failing run, want it confirmed", row.Confirmation)
	}

	// No repair is filed by the daemon: what to do about a confirmed red
	// is system:pipeline-steward's call (confirmBaseBranchCI wakes it).
	if row.RepairTaskID != "" {
		t.Fatalf("the daemon filed repair %s itself; filing is the pipeline steward's", row.RepairTaskID)
	}

	// And the second change, green and clean and next in line, merges --
	// the red branch is a fact about the branch, not about this change's
	// eligibility (mergepredicate.go). Before grain/task-471 this merge was
	// held, and the hold was a deadlock: the only route to fixing the
	// branch ran through the gate its own failure had closed.
	syncWithCIOneMergePerCycle(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("a green, clean change was held out of the queue by a red base branch")
	}

	// Nor on any later cycle.
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repairs := 0
	for _, task := range tasks {
		if strings.HasPrefix(task.Title, "Fix grain CI on") {
			repairs++
		}
	}
	if repairs != 0 {
		t.Fatalf("%d repairs were filed for one broken branch, want none from the daemon", repairs)
	}
}

// The flake. This deployment has a job that fails under load and passes
// on the next commit (grain/task-182), and before grain/task-202 one such
// failure on `main` stopped every merge in the repository and spent an
// agent on a branch that was not broken. Now the hold still starts at
// once -- a branch that may be broken is not one to merge onto -- and the
// second run is what decides whether it is real.
func TestAFailureASecondRunDoesNotReproduceReopensTheQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, secondBranch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Second))

	// The flaky job fails, on a run whose other job passed.
	runner.finish(row.RunID, ci.ConclusionFailure,
		ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess},
		ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "ui-e2e", Conclusion: ci.ConclusionFailure})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Broken() {
		t.Fatal("the queue was left open while grain had no idea whether main was broken")
	}
	if got := row.ConfirmJobs; len(got) != 1 || got[0] != ".grain/workflows/tests.yml:ui-e2e" {
		t.Fatalf("the confirmation would re-run %v, want only the job that failed", got)
	}

	// While it is being checked the queue carries on -- a failure grain is
	// still deciding about holds no more than a confirmed one does
	// (grain/task-471) -- and what the row of a change waiting its turn
	// says is that grain is checking, rather than only that something is
	// wrong.
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("a change was held while grain was finding out whether main was broken")
	}

	// The re-run passes. The branch is not broken, nobody is sent to fix
	// it, and the queue moves again.
	runner.finish(row.ConfirmRunID, ci.ConclusionSuccess,
		ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "ui-e2e", Conclusion: ci.ConclusionSuccess})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Broken() {
		t.Fatal("a failure that did not reproduce is still holding the queue")
	}
	if row.RepairTaskID != "" {
		t.Fatalf("a repair (%s) was filed for a failure that did not reproduce", row.RepairTaskID)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if strings.HasPrefix(task.Title, "Fix grain CI on") {
			t.Fatalf("an agent was spent on a flake: %s %q", task.ID, task.Title)
		}
	}

	syncWithCI(t, ctx, store, client, runner, baseTime.Add(4*time.Minute))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("the queue stayed shut after the failure was cleared")
	}
}

// A confirmation that never produces a sample -- CI itself is the thing
// that is broken -- must not reopen the queue and must not ask forever.
// It is bounded, and at the end of it the failure stands: a steward wake
// on a branch that turns out to be fine costs little, while a red branch
// nobody is told about is what this sensor exists to stop.
func TestAConfirmationThatNeverAnswersEventuallyConfirmsTheFailure(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)

	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure, Detail: "failure: go",
		MergedTaskID: "t0", ConfirmJobs: []string{".grain/workflows/tests.yml:go"},
	}); err != nil {
		t.Fatal(err)
	}

	// Every confirmation this deployment starts dies without judging the
	// tree -- no sandbox, on a host with nothing left.
	at := baseTime
	for cycle := 0; cycle < 8; cycle++ {
		at = at.Add(time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		row, err := store.GetBaseBranchCI(ctx, repo, "main")
		if err != nil {
			t.Fatal(err)
		}
		if !row.Broken() {
			t.Fatal("the queue reopened on a confirmation that never judged anything")
		}
		if row.ConfirmRunID != "" {
			runner.finish(row.ConfirmRunID, ci.ConclusionErrored,
				ci.JobRun{WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionErrored})
		}
	}

	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Confirmed() {
		t.Fatalf("after %d cycles main reads %q, want a confirmed failure", 8, row.Confirmation)
	}
	if runner.rechecks() != 2 {
		t.Fatalf("%d confirming runs were started for one unanswerable failure, want the bound of 2",
			runner.rechecks())
	}
}

// A failure with no jobs to re-run -- a workflow that would not parse, or
// a row written by a build older than the confirmation -- is taken as it
// stands rather than cleared by a re-run of nothing.
func TestAFailureWithNoJobsToRerunIsConfirmedAsItStands(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure,
		Detail: "grain CI could not run: .grain/workflows/tests.yml: yaml: line 4: did not find expected key",
	}); err != nil {
		t.Fatal(err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Confirmed() {
		t.Fatalf("main reads %q, want a failure confirmed without a re-run", row.Confirmation)
	}
	if runner.started() != 0 {
		t.Fatalf("%d runs were started to re-run no jobs", runner.started())
	}
}

// A change that is waiting its turn on a branch that is red says what it
// is waiting for -- its turn -- rather than having its row overwritten
// with the base branch's state: the base is the pipeline steward's to
// coordinate, and a repeated "base branch is failing" on every queued
// row was noise nobody could act on from there.
func TestAChangeWaitingItsTurnIsNotRelabelledForARedBase(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, "grain/task-t2")
	setMergeable(sim, true)

	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure, Detail: "the `go` job failed",
		Confirmation: model.BaseCIConfirmed, MergedTaskID: "t0", RepairTaskID: "t9",
	}); err != nil {
		t.Fatal(err)
	}

	// One merge per repository per cycle, so the second change is the one
	// waiting its turn.
	syncWithCI(t, ctx, store, client, runner, baseTime)
	state, note := mergeQueueStateOf(t, ctx, store, second.ID)
	if state == "base_broken" || strings.Contains(note, "is failing on `main`") {
		t.Fatalf("the waiting change's row was relabelled for the red base: %q (%s)", state, note)
	}
}

// What used to be the gate's own exception, and the reason the gate was
// not *quite* a deadlock: the repair merged into the branch nothing else
// could merge into. There is no gate now, so there is nothing to be
// exempt from -- both changes merge, and the repair no longer depends on
// grain having filed it and matched its id to get through.
func TestTheRepairOfABrokenBaseNeedsNoExemptionToMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	// A change queued ahead of the repair, so that the repair is not the
	// queue head and could only merge by being let through.
	ahead, aheadBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	ahead = grantCI(t, ctx, store, ahead)
	greenGrainCI(t, ctx, store, client, runner, ahead, repo, aheadBranch)
	repair, repairBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	repair = grantCI(t, ctx, store, repair)
	greenGrainCI(t, ctx, store, client, runner, repair, repo, repairBranch)
	setMergeable(sim, true)

	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure, Detail: "the `go` job failed",
		MergedTaskID: "t0", RepairTaskID: repair.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// Two cycles, because a repository merges one change per cycle: the
	// point is that both of them land, the repair included, and that
	// neither needed the row to name it.
	syncWithCI(t, ctx, store, client, runner, baseTime)
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(time.Minute))
	if !mergedBranch(sim, aheadBranch) {
		t.Fatal("the change ahead of the repair was held by a red base branch")
	}
	if !mergedBranch(sim, repairBranch) {
		t.Fatal("the repair could not merge, so nothing will ever fix this branch")
	}
}

// A failure about a commit the branch has moved past is an answer about a
// tree that has gone: somebody pushed a fix by hand. The queue does not
// simply believe them -- it asks grain CI again about the tip that is
// actually there, and that run's verdict is what reopens the branch.
func TestABaseBranchThatHasMovedOnIsAskedAgainRatherThanHeldForever(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)

	broke := headSHA(t, client, repo, "main")
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: broke, StartedAt: baseTime,
		Conclusion: model.BaseBranchCIFailure, Detail: "the `go` job failed", MergedTaskID: "t0",
	}); err != nil {
		t.Fatal(err)
	}

	// Somebody fixes main by hand.
	pushFileToBranch(t, sim.BareRepo, "main", "FIX.md", "fixed by hand", "fix main by hand")
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %v", err)
	}
	if row.Commit == broke {
		t.Fatal("the queue is still holding an answer about a commit main has moved past")
	}
	if row.Conclusion != "" {
		t.Fatalf("the fresh run already has a verdict (%q): nothing can have run yet", row.Conclusion)
	}

	// While that run is going the branch is not broken, so the queue
	// moves again -- a run in flight is not a failure.
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
	if !mergedBranch(sim, branch) {
		t.Fatal("the queue stayed shut while the base branch's fresh run was still going")
	}
}

// The hole grain/task-320 closes: a base branch that moves without grain
// merging anything. Until this, the only thing that had ever started a
// base-branch run was the cycle on which the pull-request reconciler
// observed grain's own merge of a task's pull request -- so a pull request
// merged by hand to unblock the queue, a push straight to `main`, or a
// pull request nobody filed as a task moved the branch silently and left
// the row describing a commit several merges back. gke-labs/kontur took
// five commits on `main` that way with no run against any of them.
//
// Who moved the branch decides nothing here. That the tree under it is
// untested decides everything, because it is what the queue merges onto
// either way.
func TestABaseBranchMovedWithoutGrainMergingIsStillTested(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	queued, queuedBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	queued = grantCI(t, ctx, store, queued)
	greenGrainCI(t, ctx, store, client, runner, queued, repo, queuedBranch)
	setMergeable(sim, true)

	// As far as grain knows, main is green: its last merge's own run
	// passed, and nothing has merged since.
	tested := headSHA(t, client, repo, "main")
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: tested, StartedAt: baseTime,
		Conclusion: "success", MergedTaskID: "t0", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Somebody merges a pull request by hand to get the queue moving.
	pushFileToBranch(t, sim.BareRepo, "main", "HAND.md", "merged by hand",
		"Merge pull request #22 by hand")
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %v", err)
	}
	if row.Commit == tested {
		t.Fatal("main's row still describes the commit grain last tested, so the commits pushed " +
			"over it were never judged by anything")
	}
	if row.Commit != headSHA(t, client, repo, "main") {
		t.Fatalf("the run is judging %s, want main's actual tip", row.Commit)
	}
	if row.Conclusion != "" || row.RunID == "" {
		t.Fatalf("main's row reads %q on run %q, want a fresh run with no verdict yet",
			row.Conclusion, row.RunID)
	}
	if row.MergedTaskID != "" {
		t.Fatalf("the row blames task %q for a commit grain did not merge", row.MergedTaskID)
	}
	req := runner.lastRequest(t)
	if req.Ref != "main" || req.Commit != row.Commit || req.Event != ci.EventPush || req.TrustedRelease {
		t.Fatalf("the run asked for is %+v, want main's tip judged by an untrusted push the way a "+
			"merge's own run judges it (grain/task-461)", req)
	}

	// And it is a verdict with teeth: the branch it judges is the one the
	// queue merges onto, so a failure stops it exactly as a post-merge
	// failure does.
	runner.finish(row.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionFailure,
	})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Broken() {
		t.Fatalf("main's verdict reads %q, want a failure", row.Conclusion)
	}
	// And the queue carries on: the verdict is information about the
	// branch, not a condition on this change (grain/task-471).
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(3*time.Minute))
	if !mergedBranch(sim, queuedBranch) {
		t.Fatal("a green, clean change was held out of the queue by a red base branch")
	}
}

// A post-merge run that judged nothing -- no sandbox, or the daemon
// stopping under it -- leaves the branch as untested as one nothing was
// started for. Before grain/task-320 that was the end of it: the row read
// "errored", errored holds nothing, and the next merge was the only thing
// that would ever ask again. gke-labs/kontur's last run on `main` died
// exactly this way, in an upgrade shutdown.
//
// So it is asked again, and bounded, because a deployment whose CI cannot
// get a job onto a machine at all must not answer that with a fresh suite
// every cycle forever.
func TestABaseBranchRunThatJudgedNothingIsAskedAgainAndBounded(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "lost", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, MergedTaskID: "t0", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// The run is gone with the daemon that was running it.
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Conclusion != string(ci.ConclusionErrored) {
		t.Fatalf("main's row reads %q, want the lost run recorded as errored", row.Conclusion)
	}
	if runner.started() != 0 {
		t.Fatal("a replacement was started on the same cycle the loss was recorded, so a broken " +
			"deployment would spend every cycle starting runs")
	}

	// Not at once: a deployment that is broken for a minute is not
	// answered with four runs in four cycles (ciRetryDelay).
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 0 {
		t.Fatalf("%d runs were started inside the retry delay", runner.started())
	}

	// Then it is asked again, about the same commit, and the merge it came
	// from is still recorded against it.
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Conclusion != "" || row.RunID == "lost" {
		t.Fatalf("main's row reads %q on run %q, want a second run of the same commit",
			row.Conclusion, row.RunID)
	}
	if row.Commit != headSHA(t, client, repo, "main") || row.Attempts != 2 {
		t.Fatalf("the second run judges %s on attempt %d, want main's tip on attempt 2",
			row.Commit, row.Attempts)
	}
	if row.MergedTaskID != "t0" {
		t.Fatalf("the retry forgot which merge it is about (%q), so a failure would name nobody",
			row.MergedTaskID)
	}

	// Every one of them dies the same way. It stops: three runs for one
	// commit (orchestrator.BaseCIMaximumAttempts, the run and its
	// automatic re-runs) and then the row stands as it is, for somebody to
	// read.
	at := baseTime.Add(10 * time.Minute)
	for cycle := 0; cycle < 8; cycle++ {
		at = at.Add(10 * time.Minute)
		row, err = store.GetBaseBranchCI(ctx, repo, "main")
		if err != nil {
			t.Fatal(err)
		}
		if row.Conclusion == "" {
			runner.finish(row.RunID, ci.ConclusionErrored)
		}
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	if want := orchestrator.BaseCIMaximumAttempts - 1; runner.started() != want {
		t.Fatalf("%d replacement runs were started for one commit, want the bound of %d on top of "+
			"the run the row already had", runner.started(), want)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Attempts != orchestrator.BaseCIMaximumAttempts || row.Conclusion != string(ci.ConclusionErrored) {
		t.Fatalf("main's row reads %q after %d attempts, want an errored run that has stopped being "+
			"retried", row.Conclusion, row.Attempts)
	}
}

// Publishing and judging are two switches now (grain/task-330). A
// repository that has turned its release workflows off -- because the
// publish job is broken, which is the case this is for -- keeps the gate
// by naming the jobs that judge its base branches: those jobs run, under
// `push` rather than `release`, nothing is published, and a failure holds
// the queue exactly as a post-merge failure does.
//
// Before this, switching a repository's release off suppressed the base-branch run
// entirely, so an operator switching publishing off silently lost the
// compensating control that stops the queue merging onto a red `main` --
// and the last red row on such a branch could never be refreshed either,
// because nothing would start another run for it.
func TestARepositoryWithReleasesOffIsStillJudgedByTheJobsItNames(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		Work:             model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
		BaseBranchCIJobs: []string{".grain/workflows/tests.yml:go"},
	}); err != nil {
		t.Fatal(err)
	}

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, secondBranch)
	setMergeable(sim, true)

	// The merge records the tip, and the cycle after it starts a run about
	// the branch rather than about publishing it.
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("the first change did not merge")
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("releases are off, so main's merge was recorded by nothing at all: %+v (%v)", row, err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil || row.RunID == "" {
		t.Fatalf("main's row is %+v (%v), want a run started against the tip the merge left", row, err)
	}
	req := runner.lastRequest(t)
	if req.Event != ci.EventPush || req.TrustedRelease {
		t.Fatalf("main is judged by %+v, want an untrusted push run: a repository that has turned "+
			"releases off must not be published by the run that tests it", req)
	}
	if len(req.Jobs) != 1 || req.Jobs[0] != ".grain/workflows/tests.yml:go" {
		t.Fatalf("the run asks for %v, want only the jobs the repository named as judging it", req.Jobs)
	}

	// And the verdict has the same teeth a release run's has.
	runner.finish(row.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionFailure,
	})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Broken() {
		t.Fatalf("main's verdict reads %q, want a failure", row.Conclusion)
	}
	confirm := runner.lastRequest(t)
	if confirm.Event != ci.EventPush || confirm.TrustedRelease || !confirm.Recheck {
		t.Fatalf("the second opinion is %+v, want the same untrusted push run asked again with the "+
			"cache bypassed: a confirmation planned under another event is a different question", confirm)
	}
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("a green, clean change was held out of the queue by a red base branch")
	}

	// The row is refreshable too: somebody pushes a fix, and the tip
	// moving is enough to have it judged again.
	pushFileToBranch(t, sim.BareRepo, "main", "FIX.md", "fixed by hand", "fix main by hand")
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %v", err)
	}
	if row.Commit != headSHA(t, client, repo, "main") || row.Conclusion != "" {
		t.Fatalf("main's row reads %q at %s, want a fresh run against the tip somebody pushed the fix "+
			"to", row.Conclusion, row.Commit)
	}
}

// Releases off and no judging jobs named used to be a refusal, and is
// now the whole push plan (grain/task-458). A switch named for releases
// turns off releases: the branch is still judged, under `push`, untrusted
// and publishing nothing -- and a row left red by an earlier run is
// refreshable again, which is the half of the old behaviour that could
// strand a repository's merge queue on a verdict nothing could replace.
func TestAReleasesOffRepositoryNamingNoJobsIsJudgedByThePushPlan(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	broke := headSHA(t, client, repo, "main")
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: broke, StartedAt: baseTime,
		Conclusion: model.BaseBranchCIFailure, Detail: "the `go` job failed", MergedTaskID: "t0",
		Confirmation: model.BaseCIConfirmed, RepairTaskID: "t9",
	}); err != nil {
		t.Fatal(err)
	}

	pushFileToBranch(t, sim.BareRepo, "main", "FIX.md", "fixed by hand", "fix main by hand")
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 1 {
		t.Fatalf("%d runs were started for a repository whose releases are off and which names no "+
			"judging jobs, want the one that judges the tip: turning publishing off is not turning "+
			"the tests off", runner.started())
	}
	req := runner.lastRequest(t)
	if req.Event != ci.EventPush || req.TrustedRelease || len(req.ReleaseSecrets) != 0 {
		t.Fatalf("main is judged by %+v, want an untrusted push run: a repository that has turned "+
			"releases off must not be published by the run that tests it", req)
	}
	if len(req.Jobs) != 0 {
		t.Fatalf("the run asks for %v, want the whole push plan: a repository that has named no "+
			"narrower set is judged by everything a push would run", req.Jobs)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %v", err)
	}
	if row.Commit == broke || row.Conclusion != "" {
		t.Fatalf("main's row reads %q at %s, want a fresh run against the commit somebody pushed the "+
			"fix to: a red row nothing can refresh is how this branch used to be stranded",
			row.Conclusion, row.Commit)
	}
}

// The second opinion follows the same policy the first run did, so a
// repository whose releases are off is asked again under `push` and not
// under the release event grain has been told not to emit -- with the
// failing jobs re-run and nothing published. It used to be refused
// outright when no judging jobs were named, which left the failure
// standing on one run's word (grain/task-458).
func TestAFailureOnAReleasesOffRepositoryIsConfirmedUnderPush(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure, Detail: "the `go` job failed",
		ConfirmJobs: []string{"go"}, MergedTaskID: "t0", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if runner.started() != 1 {
		t.Fatalf("%d runs were started to confirm the failure, want the one second opinion",
			runner.started())
	}
	req := runner.lastRequest(t)
	if req.Event != ci.EventPush || req.TrustedRelease || !req.Recheck {
		t.Fatalf("the second opinion is %+v, want the same untrusted push run asked again with the "+
			"cache bypassed", req)
	}
	if len(req.Jobs) != 1 || req.Jobs[0] != "go" {
		t.Fatalf("the second opinion asks for %v, want the jobs that failed", req.Jobs)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %v", err)
	}
	if row.Confirmation != model.BaseCIConfirming {
		t.Fatalf("main's row says %q, want the failure waiting on the second opinion rather than "+
			"confirmed on one run's word", row.Confirmation)
	}
}

// A base-branch run made of no job at all is not a pass (grain/task-508).
//
// ci.PlanRun keeps one empty plan green on purpose -- a repository that
// declares no workflow has nothing red about it, and its pull requests
// should not be held pending on a suite nobody wrote. On this row that
// green is read as "the tree the queue is merging onto has been tested",
// and it is the one state in which a base branch goes unjudged with
// every surface reporting success: no failure, no repair, nothing for
// pkg/ui's base-branch gate reading to find. It is recorded as errored
// instead -- grain's word for a verdict about nothing -- which holds no
// merge, is re-asked while a suite might appear, and is what that
// reading reports.
func TestABaseBranchRunMadeOfNoJobIsNotRecordedAsAPass(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "run1", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, MergedTaskID: "t0", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	runner.remember(ci.Run{ID: "run1", Repo: repo.String(), Ref: "main"})
	// The whole suite of a repository whose `.grain/workflows` answer no
	// `push`: no job, and over in seconds.
	runner.finish("run1", ci.ConclusionSuccess)

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Conclusion != string(ci.ConclusionErrored) {
		t.Fatalf("main's row reads %q after a run that ran nothing, want it recorded as errored: "+
			"a gate that passes over every tree whatever must not read as a tested tree",
			row.Conclusion)
	}
	if !strings.Contains(row.Detail, "no job at all") {
		t.Errorf("detail = %q, want it to say the run was made of no job at all", row.Detail)
	}
	if row.Broken() {
		t.Error("a repository with no suite is holding its own merge queue, which is not its fault")
	}

	// And a run that did judge the tree is still a pass.
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "run2", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, MergedTaskID: "t0", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	runner.remember(ci.Run{ID: "run2", Repo: repo.String(), Ref: "main"})
	runner.finish("run2", ci.ConclusionSuccess, ci.JobRun{JobID: "go", Conclusion: ci.ConclusionSuccess})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if row, err = store.GetBaseBranchCI(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if row.Conclusion != string(ci.ConclusionSuccess) {
		t.Fatalf("main's row reads %q after a suite that ran, want success", row.Conclusion)
	}
}

// The run before that one: the one grain asked for and was refused
// (grain/task-509). A base-branch job list naming a job no workflow of
// the branch declares any more is rejected by the planner rather than
// guessed at, so grain CI starts nothing and there is no run to conclude.
//
// Until this was written on the row that error went to the reconciler
// and nowhere else: the row stood untouched, the next cycle asked the
// same question and was refused the same way, and the merge queue merged
// onto the branch throughout with nothing anybody looks at saying so. It
// is now a verdict about nothing at all -- errored, which holds no merge
// -- so the attempt is counted, the re-asking is bounded, and the
// refusal is in words on the row for ui.baseGateJudgedNothing to report.
func TestARunGrainCouldNotEvenAskForIsRecordedOnTheBranchesRow(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		BaseBranchCIJobs: []string{".grain/workflows/tests.yml:go"},
	}); err != nil {
		t.Fatal(err)
	}
	// A branch grain is watching whose last merge is waiting to be judged
	// (noteBaseBranchMerge), and a job list that has fallen behind the
	// workflows it names.
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", StartedAt: baseTime, PendingTaskID: "t0",
	}); err != nil {
		t.Fatal(err)
	}
	runner.refuse(errors.New(
		`ci: selected job ".grain/workflows/tests.yml:go" does not exist in the matching workflows`))

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner,
		baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("a refused request failed the cycle: %v", err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Conclusion != string(ci.ConclusionErrored) || row.RunID != "" {
		t.Fatalf("main's row reads %q on run %q, want a verdict about nothing and no run at all",
			row.Conclusion, row.RunID)
	}
	if !strings.Contains(row.Detail, "could not be asked") ||
		!strings.Contains(row.Detail, "does not exist in the matching workflows") {
		t.Errorf("detail = %q, want it to carry the refusal, which is the whole diagnosis",
			row.Detail)
	}
	if row.Broken() || row.Commit != headSHA(t, client, repo, "main") {
		t.Fatalf("row = %+v, want the tip recorded as unjudged and nothing held: a question that "+
			"could not be asked must not block the only route to fixing it", row)
	}
	if row.MergedTaskID != "t0" {
		t.Errorf("the refusal forgot which merge it is about (%q), so the merge that moved this "+
			"branch is attributed to nobody from here on", row.MergedTaskID)
	}

	// It is re-asked, because a refusal can be a hiccup -- and bounded,
	// because this one is a setting and will be refused every time.
	at := baseTime.Add(time.Minute)
	for cycle := 0; cycle < 8; cycle++ {
		at = at.Add(10 * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	// orchestrator.BaseCIMaximumAttempts: the first ask and its re-asks,
	// and then it stops rather than costing a refused request every cycle
	// for ever.
	if want := orchestrator.BaseCIMaximumAttempts; runner.started() != want {
		t.Fatalf("grain asked %d times for a run it is refused every time, want it bounded at %d",
			runner.started(), want)
	}
	if row, err = store.GetBaseBranchCI(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if row.Attempts != orchestrator.BaseCIMaximumAttempts || row.Conclusion != string(ci.ConclusionErrored) {
		t.Fatalf("main's row reads %q after %d attempts, want a refusal that has stopped being "+
			"re-asked and stands where somebody can read it", row.Conclusion, row.Attempts)
	}

	// And the repair is in the repository being judged: once the list
	// names a job that exists, the next cycle starts an ordinary run.
	runner.allow()
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: row.Commit, StartedAt: at,
		Conclusion: row.Conclusion, Detail: row.Detail, FinishedAt: row.FinishedAt,
		MergedTaskID: "t0", Attempts: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner,
		at.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if row, err = store.GetBaseBranchCI(ctx, repo, "main"); err != nil {
		t.Fatal(err)
	}
	if row.RunID == "" || row.Conclusion != "" {
		t.Fatalf("main's row reads %q on run %q, want a real run judging the branch again",
			row.Conclusion, row.RunID)
	}
}

// The same refusal met by the operator's own button (grain/task-480):
// pressing it consumed the request only by a run being started, so a
// repository whose job list grain CI refuses had a button that did
// nothing, said nothing, and could be pressed for ever.
func TestAByHandRunThatGrainCIRefusesAnswersTheOperator(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	asked := baseTime.Add(-time.Minute)
	ranAt := baseTime.Add(-time.Hour)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: headSHA(t, client, repo, "main"),
		RunID: "run-0", StartedAt: ranAt, FinishedAt: &ranAt,
		Conclusion: "cancelled", Detail: "cancelled: not worth testing", Attempts: 4,
		RunRequestedAt: &asked, RunRequestedBy: "bwsalmon",
	}); err != nil {
		t.Fatal(err)
	}
	runner.refuse(errors.New(`ci: selected job "go" does not exist in the matching workflows`))

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime); err != nil {
		t.Fatalf("a refused request failed the cycle: %v", err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.RunRequestedAt != nil {
		t.Error("the request is still pending, so the next cycle asks again and the button " +
			"goes on doing nothing visible however often it is pressed")
	}
	if row.Conclusion != string(ci.ConclusionErrored) ||
		!strings.Contains(row.Detail, "could not be asked") {
		t.Fatalf("main's row reads %q / %q, want the person who pressed the button given the "+
			"refusal as the answer", row.Conclusion, row.Detail)
	}
	if row.Attempts != 1 {
		t.Errorf("attempts = %d, want a person's own request to start the count over", row.Attempts)
	}
}

// A green base branch holds nothing at all, which is the state every
// deployment is in nearly all of the time.
func TestAGreenBaseBranchHoldsNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: "success", MergedTaskID: "t0",
	}); err != nil {
		t.Fatal(err)
	}

	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("a change was held back by a base branch that is green")
	}
}

// The bound (grain/task-334). A base-branch run is a release run -- 22
// minutes to an hour and a half of image building on bwsalmon/grain --
// and merges land far faster than that, so a run per merge stacked them:
// two full image builds at once was ordinary, and the contention is what
// made the rest of CI slow and the release's own pre-publish gate flake.
//
// So merges that land while one is going start nothing. They move the
// tip; the run finishes; one run is started against the tip as it then
// stands, and the commits in between are skipped rather than queued,
// because the only image worth building is the newest one.
func TestMergesDuringABaseBranchRunCoalesceIntoOneRunAtTheTip(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	var merged []model.Task
	for _, id := range []string{"t1", "t2", "t3", "t4"} {
		task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, id, repo)
		task = grantCI(t, ctx, store, task)
		greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
		merged = append(merged, task)
	}
	setMergeable(sim, true)
	at := baseTime

	// The first merge, and the run it earns.
	syncWithCIOneMergePerCycle(t, ctx, store, client, runner, at)
	at = at.Add(time.Minute)
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", at)
	if row.MergedTaskID != merged[0].ID {
		t.Fatalf("the run is recorded against %q, want the merge it answers (%s)",
			row.MergedTaskID, merged[0].ID)
	}
	if row.Covers != 1 || row.PendingMerges != 0 {
		t.Fatalf("the first run covers %d merges with %d waiting, want one and none",
			row.Covers, row.PendingMerges)
	}
	inFlight, judging := row.RunID, row.Commit
	startedBefore := runner.started()

	// Three more merges land while it is building. Each one moves main,
	// and none of them starts anything.
	for i := 1; i < len(merged); i++ {
		at = at.Add(time.Minute)
		syncWithCIOneMergePerCycle(t, ctx, store, client, runner, at)
		at = at.Add(time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
		if runner.started() != startedBefore {
			t.Fatalf("merge %d started a second release run while one was still building: "+
				"%d runs, want %d", i+1, runner.started(), startedBefore)
		}
	}
	tip := headSHA(t, client, repo, "main")
	if tip == judging {
		t.Fatal("no merge moved main, so this test is not testing anything")
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.RunID != inFlight || row.Commit != judging {
		t.Fatalf("main's row moved to run %q at %s while that run was still going",
			row.RunID, shortSHAForTest(row.Commit))
	}
	if row.PendingTaskID != merged[len(merged)-1].ID {
		t.Fatalf("the row is waiting on %q, want the last merge that landed (%s)",
			row.PendingTaskID, merged[len(merged)-1].ID)
	}
	if row.PendingMerges != len(merged)-1 {
		t.Fatalf("%d merges are waiting for the next run, want the %d that landed while this one ran",
			row.PendingMerges, len(merged)-1)
	}

	// It finishes. The verdict is collected, and then exactly one run is
	// started -- against the tip as it stands now, not against any of the
	// commits it passed on the way there.
	runner.finish(inFlight, ci.ConclusionSuccess)
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	if runner.started() != startedBefore+1 {
		t.Fatalf("%d runs were started for three coalesced merges, want one",
			runner.started()-startedBefore)
	}
	req := runner.lastRequest(t)
	if req.Commit != tip {
		t.Fatalf("the follow-up run judges %s, want main's tip %s",
			shortSHAForTest(req.Commit), shortSHAForTest(tip))
	}
	if req.Event != ci.EventPush || req.TrustedRelease {
		t.Fatalf("the follow-up run is %+v, want the untrusted push run that judges a merged tip", req)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Commit != tip || row.Conclusion != "" {
		t.Fatalf("main's row reads %s / %q, want the fresh run at the tip",
			shortSHAForTest(row.Commit), row.Conclusion)
	}
	if row.MergedTaskID != merged[len(merged)-1].ID || row.PendingTaskID != "" {
		t.Fatalf("the run answers %q with %q still waiting, want the last merge taken up",
			row.MergedTaskID, row.PendingTaskID)
	}
	// And it says how many merges it is the answer to, which is the whole
	// of what the coalescing costs the merge queue's arithmetic: three
	// changes landed here for one suite, so three post-merge suites were
	// never going to be spent on them in the first place
	// (model.BaseBranchCI.Covers, grain/task-501).
	if row.Covers != len(merged)-1 || row.PendingMerges != 0 {
		t.Fatalf("the coalesced run covers %d merges with %d still waiting, want %d and none",
			row.Covers, row.PendingMerges, len(merged)-1)
	}

	// And a further cycle with nothing new merged starts nothing at all:
	// the tip is the commit being judged, which is the whole condition.
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	if runner.started() != startedBefore+1 {
		t.Fatalf("a cycle with nothing to judge started %d more runs",
			runner.started()-startedBefore-1)
	}
}

// The failure case, which is where a level-triggered design bites: "the
// tip is not the commit that was judged" would be true forever after a
// red run if the row did not name the tip it went red at, and every cycle
// would burn another 22-90 minute release run on the same tree.
//
// A failure is a verdict about that tree. It is not re-run -- what it
// waits for is a new commit, which then gets its own run exactly as any
// other movement of the branch does.
func TestAFailedBaseBranchRunIsNotReRunButALaterCommitStillIs(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Minute))
	red := row.Commit
	runner.finish(row.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionFailure,
	})

	// Ten cycles of a red `main` that nobody has pushed over. The failure
	// holds the queue and earns its confirmation, and not one of those
	// cycles asks for the whole suite again.
	at := baseTime.Add(2 * time.Minute)
	for cycle := 0; cycle < 10; cycle++ {
		at = at.Add(10 * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	if n := fullRunsAt(runner, red); n != 1 {
		t.Fatalf("%d full runs were started for one red commit, want the one that found it red: "+
			"a failure is a verdict about that tree, not a reason to build it again", n)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Broken() {
		t.Fatalf("main's verdict reads %q, want the failure still holding the queue", row.Conclusion)
	}

	// Then somebody lands the fix. A new commit is a new question, and it
	// gets its own run.
	pushFileToBranch(t, sim.BareRepo, "main", "FIX.md", "fixed", "fix main")
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	fixed := headSHA(t, client, repo, "main")
	if n := fullRunsAt(runner, fixed); n != 1 {
		t.Fatalf("%d runs were started for the commit that may have fixed main, want one", n)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Commit != fixed || row.Broken() {
		t.Fatalf("main's row reads %s / %q, want the fresh run at the fixed tip holding nothing",
			shortSHAForTest(row.Commit), row.Conclusion)
	}
}

// The restart, which is the other half of what this buys (grain/task-334).
// Twenty of the twenty-five cancelled `release images` runs in the week to
// 2026-09-19 were grain stopping for an upgrade and destroying the run
// that would have built its own next image, and nothing started another
// until some later merge happened to.
//
// Now the trigger is the state of the branch rather than the event of a
// merge, so the cycle after the daemon comes back sees a tip nothing has
// judged and starts one by itself.
func TestARunLostWithTheDaemonIsStartedAgainAtTheTip(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Minute))

	// grain restarts under it, and main moves on while it is down.
	runner.interrupt(row.RunID)
	pushFileToBranch(t, sim.BareRepo, "main", "MORE.md", "more", "another change")
	at := baseTime.Add(2 * time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Conclusion != string(ci.ConclusionErrored) {
		t.Fatalf("main's row reads %q, want the lost run recorded as errored -- a verdict about "+
			"nothing, which must not hold the queue", row.Conclusion)
	}
	if row.Broken() {
		t.Fatal("a run the daemon destroyed is holding the merge queue")
	}

	// The next cycle asks again, about the tip as it now stands, without
	// waiting for a merge to come along and ask for it.
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	tip := headSHA(t, client, repo, "main")
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row.Commit != tip || row.Conclusion != "" || row.RunID == "" {
		t.Fatalf("main's row reads %s / %q on run %q, want a fresh run at the tip",
			shortSHAForTest(row.Commit), row.Conclusion, row.RunID)
	}
	if row.Attempts != 1 {
		t.Fatalf("the run at a new commit counts %d attempts, want a new question asked once",
			row.Attempts)
	}
}

// fullRunsAt is how many whole suites have been asked for against one
// commit -- the expensive kind, as opposed to the narrowed confirmation
// of a failure (RequestedBy "base-confirm:..."), which is what the bound
// on re-asking is about. A whole suite a person asked for again is a
// recheck too (uncached), and still counts.
func fullRunsAt(runner *fakeCI, commit string) int {
	n := 0
	for _, req := range runner.requests {
		if req.Commit == commit && !strings.HasPrefix(req.RequestedBy, "base-confirm:") {
			n++
		}
	}
	return n
}

// shortSHAForTest is orchestrator.shortSHA's job for a failure message,
// spelled here because these tests live outside the package.
func shortSHAForTest(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func TestSwitchingRepoToGitHubCIModeCancelsBaseBranchCIAndSkipsPostMergeCI(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	runner := newFakeCI()

	// Pre-existing task t1 (with GrantByDefault grain-ci) merges while CIMode is grain,
	// starting an in-flight base-branch CI run.
	t1, branch1 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	if err := store.UpdateTask(ctx, t1.ID, func(tk *model.Task) error {
		tk.Grants = []model.Grant{{Capability: "grain-ci", Via: model.GrantByDefault}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t1Ptr, _ := store.GetTask(ctx, t1.ID)
	greenGrainCI(t, ctx, store, client, runner, *t1Ptr, repo, branch1)
	setMergeable(sim, true)

	at := baseTime
	syncWithCI(t, ctx, store, client, runner, at)
	at = at.Add(time.Minute)
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", at)
	inFlightRunID := row.RunID
	if inFlightRunID == "" {
		t.Fatal("expected an in-flight base-branch CI run while CIMode is grain")
	}

	// Switch repo to CIModeGitHub while the base-branch CI run is in flight.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:   repo,
		CIMode: model.CIModeGitHub,
	}); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatalf("runBaseBranchCIReconciler after switch: %v", err)
	}
	if got := runner.reasons[inFlightRunID]; got != "cancelled: repository switched to GitHub CI" {
		t.Fatalf("base-branch CI run %s cancel reason = %q, want %q", inFlightRunID, got, "cancelled: repository switched to GitHub CI")
	}
	rowAfter, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if rowAfter != nil && (rowAfter.RunID != "" && rowAfter.Conclusion == "") {
		t.Fatalf("base-branch CI row still in flight after switching to GitHub CI: %+v", rowAfter)
	}

	// Now merge a second pre-existing task t2 (which still carries a stale GrantByDefault grain-ci grant)
	// after the repo switched to CIModeGitHub. Verify recordPostMergeCI does NOT start a base-branch CI run.
	startedBefore := runner.started()
	t2, branch2 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	if err := store.UpdateTask(ctx, t2.ID, func(tk *model.Task) error {
		tk.Grants = []model.Grant{{Capability: "grain-ci", Via: model.GrantByDefault}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	greenOnGitHub(sim, branch2)
	setMergeable(sim, true)
	at = at.Add(time.Minute)
	if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
		t.Fatalf("SyncPullRequests for t2: %v", err)
	}
	if !mergedBranch(sim, branch2) {
		t.Fatal("t2 did not merge under CIModeGitHub")
	}
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatalf("runBaseBranchCIReconciler after t2 merge: %v", err)
	}
	if got := runner.started(); got != startedBefore {
		t.Fatalf("base-branch CI runs started after merging t2 on CIModeGitHub = %d, want 0", got-startedBefore)
	}
}

// watchDefaultBranch is an operator ticking "watch this repository's
// default branch" on a repository grain CI is enabled for
// (model.RepoConfig.WatchDefaultBranch, grain/task-331) -- the whole of
// what turns seeding on.
func watchDefaultBranch(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) {
	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:               repo,
		CIMode:             model.CIModeGrain,
		WatchDefaultBranch: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// secondRepoClient answers for one repository beside the simulated one --
// its default branch and that branch's head, which is everything watching
// a branch asks of a repository nothing has merged into. Everything else
// goes to the client it wraps, so a test can have two repositories
// without a second simulated GitHub.
type secondRepoClient struct {
	github.Client
	repo   model.RepoRef
	branch string
	sha    string
}

func (c secondRepoClient) DefaultBranch(owner, repo string) (string, error) {
	if owner == c.repo.Owner && repo == c.repo.Name {
		return c.branch, nil
	}
	return c.Client.DefaultBranch(owner, repo)
}

func (c secondRepoClient) GetBranchHead(owner, repo, branch string) (*github.BranchHead, error) {
	if owner == c.repo.Owner && repo == c.repo.Name {
		if branch != c.branch {
			return nil, nil
		}
		return &github.BranchHead{SHA: c.sha}, nil
	}
	return c.Client.GetBranchHead(owner, repo, branch)
}

// The hole grain/task-320 left open on purpose and grain/task-331 closes:
// a repository whose pull requests are all merged by hand has no
// base_branch_ci row, because a merge grain watched was the only thing
// that ever created one -- so the tip-watching watched nothing, and its
// `main` could be red with the queue merging onto it exactly as before
// the control existed.
//
// Ticked, the branch is enrolled from the repository instead: grain
// resolves its default branch, writes the same runless row a first merge
// writes, and judges the tip from there -- after which it is an ordinary
// watched row with all of this file's teeth.
func TestAWatchedRepositoryHasItsDefaultBranchJudgedWithoutAnyMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	watchDefaultBranch(t, ctx, store, repo)
	queued, queuedBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	queued = grantCI(t, ctx, store, queued)
	greenGrainCI(t, ctx, store, client, runner, queued, repo, queuedBranch)
	setMergeable(sim, true)

	// The first cycle enrols the branch: a row, and no run yet -- the
	// same state a first merge leaves behind (noteBaseBranchMerge).
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("nothing watches main, so a red main would hold nothing: %+v (%v)", row, err)
	}
	if row.RunID != "" {
		t.Fatalf("the enrolment started run %q itself, which is reconcileBaseBranchCI's to start "+
			"and to bound", row.RunID)
	}

	// The second judges it, exactly as it judges a branch a merge
	// enrolled.
	row = baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Minute))
	if row.Commit != headSHA(t, client, repo, "main") {
		t.Fatalf("the run judges %s, want main's actual tip", row.Commit)
	}
	if row.MergedTaskID != "" || row.PendingTaskID != "" {
		t.Fatalf("the row blames task %q/%q for a commit no merge of grain's produced",
			row.MergedTaskID, row.PendingTaskID)
	}
	req := runner.lastRequest(t)
	if req.Ref != "main" || req.Commit != row.Commit || req.Event != ci.EventPush || req.TrustedRelease {
		t.Fatalf("the run asked for is %+v, want main's tip judged the way a merge's own run "+
			"judges it: an untrusted push", req)
	}

	// And it is the same verdict with the same teeth: red main stops the
	// queue merging onto it, which is the whole point of watching a
	// branch nothing of grain's has ever merged into.
	runner.finish(row.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionFailure,
	})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !row.Broken() {
		t.Fatalf("main's verdict reads %q, want a failure", row.Conclusion)
	}
	// And the queue carries on: the verdict is information about the
	// branch, not a condition on this change (grain/task-471).
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(3*time.Minute))
	if !mergedBranch(sim, queuedBranch) {
		t.Fatal("a green, clean change was held out of the queue by a red base branch")
	}
}

// What the tick buys is precisely that this does not happen by itself. A
// suite on every repository grain has a config row for -- and a repair
// task for every one of them that turns out to be red -- is a bill
// arriving with a deployment upgrade rather than with anybody's decision,
// so an unticked repository is left exactly as it was: watched by nothing
// until grain merges into it.
func TestARepositoryNobodyAskedForIsNotSeeded(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	// grain CI enabled for the repository, and nobody has asked for its
	// default branch to be watched.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		at := baseTime.Add(time.Duration(cycle) * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
	}
	if runner.started() != 0 {
		t.Fatalf("%d suites were started for a repository nobody ticked", runner.started())
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("main was enrolled (%+v) without anybody asking for it", row)
	}

	// And the other way round: ticked, but grain CI is not enabled for
	// the repository at all, so there is no suite to run and nothing is
	// enrolled. The setting says so in the log rather than doing nothing
	// silently.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, WatchDefaultBranch: true}); err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		at := baseTime.Add(time.Duration(cycle+2) * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
	}
	if runner.started() != 0 {
		t.Fatalf("%d suites were started for a repository grain CI is not enabled for", runner.started())
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("main was enrolled (%+v) for a repository grain CI cannot judge", row)
	}
}

// The cost control that makes watching safe to turn on: an operator who
// ticks several repositories pays for them one suite after another rather
// than all at once. This deployment's CI host has been over its sandbox
// budget twice in a week, and a feature that starts N release runs on the
// cycle it is turned on is how that happens again. The per-branch bound
// (grain/task-334) does not cover this -- six branches judged once each
// are six runs it is perfectly happy with.
func TestWatchedRepositoriesAreEnrolledOneSuiteAtATime(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, base := newSim(t, "acme", "widgets", "main")
	first := model.RepoRef{Owner: "acme", Name: "widgets"}
	second := model.RepoRef{Owner: "acme", Name: "gadgets"}
	client := secondRepoClient{
		Client: base, repo: second, branch: "main",
		sha: "1111111111111111111111111111111111111111",
	}
	runner := newFakeCI()
	watchDefaultBranch(t, ctx, store, first)
	watchDefaultBranch(t, ctx, store, second)

	// One cycle enrols one repository -- whichever comes first -- and the
	// next judges it. No more are enrolled while that run is going,
	// however many cycles pass.
	for cycle := 0; cycle < 4; cycle++ {
		at := baseTime.Add(time.Duration(cycle) * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
	}
	if runner.started() != 1 {
		t.Fatalf("%d suites are going at once, want the second repository to wait for the first",
			runner.started())
	}
	watched, waiting := watchedRow(t, ctx, store, first, second)

	// The first is judged, and the repository that was waiting is taken
	// on: enrolled on the cycle that reads the verdict, judged on the
	// next.
	runner.finish(watched.RunID, ci.ConclusionSuccess)
	for cycle := 0; cycle < 2; cycle++ {
		at := baseTime.Add(time.Duration(cycle+10) * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatal(err)
		}
	}
	if runner.started() != 2 {
		t.Fatalf("%d suites have been started, want the second repository picked up once the "+
			"first was judged", runner.started())
	}
	row, err := store.GetBaseBranchCI(ctx, waiting, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row == nil || row.RunID == "" {
		t.Fatalf("%s's main is still watched by nothing: %+v", waiting, row)
	}
}

// watchedRow is whichever of these repositories the reconciler enrolled
// first, with the one still waiting beside it. Which of them won is
// ListRepoConfigs' business; the test that uses this cares that exactly
// one did.
func watchedRow(t *testing.T, ctx context.Context, store *model.Store,
	repos ...model.RepoRef) (model.BaseBranchCI, model.RepoRef) {

	t.Helper()
	var watched *model.BaseBranchCI
	var waiting model.RepoRef
	for _, repo := range repos {
		row, err := store.GetBaseBranchCI(ctx, repo, "main")
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case row == nil:
			waiting = repo
		case watched != nil:
			t.Fatalf("%s and %s were both enrolled, so their suites are going at once",
				watched.Repo, repo)
		default:
			watched = row
		}
	}
	if watched == nil {
		t.Fatal("no repository was enrolled at all")
	}
	return *watched, waiting
}

// The deployment's own configuration repository is judged after a merge
// -- with nobody having opted it in (grainci.RequiredFor) -- and judged
// under `push`.
//
// It used to be asked for under `release`, like every other base branch.
// Grain publishes nothing out of its own configuration, so the workflow
// it supplies for that repository answers `push` and `workflow_dispatch`
// and nothing else: a release run there matched no workflow, concluded
// errored -- a verdict about nothing, re-asked and then left -- and the
// "grain can load this" check that is the whole reason this repository is
// checked without anybody ticking a box never ran once (grain/task-396).
func TestTheConfigRepositoryIsJudgedUnderPushRatherThanReleased(t *testing.T) {
	store, ctx := openStore(t)
	repo := model.ConfigRepo()
	_, client := newSim(t, repo.Owner, repo.Name, "main")
	runner := newFakeCI()

	// The runless row a first merge into this repository leaves behind
	// (noteBaseBranchMerge): from here the reconciler owns the run.
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(30*time.Second))

	req := runner.lastRequest(t)
	if req.Event != ci.EventPush || req.TrustedRelease {
		t.Fatalf("%s's main is judged by %+v, want an untrusted push run: the workflow grain "+
			"supplies for its own configuration answers push, and a release run there judges nothing",
			repo, req)
	}
	if len(req.Jobs) != 0 {
		t.Fatalf("the run asks for %v, want the whole matching suite: the check grain requires here "+
			"is the whole of that repository's CI", req.Jobs)
	}
	if req.Commit != headSHA(t, client, repo, "main") {
		t.Fatalf("the run judges %s, want main's tip", shortSHAForTest(req.Commit))
	}
}

// A run an operator cancelled on a base branch is retried like one grain
// cancelled itself (task 285): whoever stops a wedged or duplicate run is
// stopping that run, and leaving the tip unjudged for good because of it
// is a dead end. It waits ciRetryDelay, and stops at baseCIMaximumAttempts.
//
// Which leaves the operator control the red-base-branch sentence never
// had (grain/task-480) for the commit grain has stopped re-asking about:
// a person asks for a run, on the row, and the reconciler starts it on
// its next cycle.
func TestAnOperatorCanAskForARunOnACommitGrainWillNotReAskAbout(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Minute))
	tip := row.Commit

	// A person stops each run, with a reason; each is replaced after the
	// retry delay and not before, until the attempts are spent.
	at := baseTime.Add(2 * time.Minute)
	for attempt := 1; attempt <= orchestrator.BaseCIMaximumAttempts; attempt++ {
		runner.cancelByHand(row.RunID, "not now, the CI host is full")
		at = at.Add(time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		got, err := store.GetBaseBranchCI(ctx, repo, "main")
		if err != nil || got == nil {
			t.Fatalf("main's row went missing: %+v (%v)", got, err)
		}
		if got.Conclusion != string(ci.ConclusionErrored) || !strings.Contains(got.Detail, "a person stopped this run") {
			t.Fatalf("attempt %d: main's row reads %q (%q), want an operator's cancel recorded as a "+
				"verdict about nothing", attempt, got.Conclusion, got.Detail)
		}
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at.Add(4*time.Minute)); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if n := fullRunsAt(runner, tip); n != attempt {
			t.Fatalf("attempt %d: %d runs before the retry delay, want %d", attempt, n, attempt)
		}
		at = at.Add(5 * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		want := attempt + 1
		if attempt == orchestrator.BaseCIMaximumAttempts {
			want = attempt
		}
		if n := fullRunsAt(runner, tip); n != want {
			t.Fatalf("attempt %d: %d runs after the retry delay, want %d", attempt, n, want)
		}
		if row, err = store.GetBaseBranchCI(ctx, repo, "main"); err != nil || row == nil {
			t.Fatalf("main's row went missing: %+v (%v)", row, err)
		}
	}
	for cycle := 0; cycle < 5; cycle++ {
		at = at.Add(10 * time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	spent := orchestrator.BaseCIMaximumAttempts
	var err error
	if n := fullRunsAt(runner, tip); n != spent {
		t.Fatalf("%d runs were started at the cancelled commit, want grain to stop at %d", n, spent)
	}

	// A person asks again. The ask is a request on the row and
	// nothing more -- no run is started by the writing of it.
	at = at.Add(time.Minute)
	if _, err := store.RequestBaseBranchCIRun(ctx, repo, "main", at, "alice"); err != nil {
		t.Fatal(err)
	}
	if n := fullRunsAt(runner, tip); n != spent {
		t.Fatalf("%d runs at the cancelled commit after merely asking for one: the request is a "+
			"row, and the reconciler is what starts runs", n)
	}

	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	if n := fullRunsAt(runner, tip); n != spent+1 {
		t.Fatalf("%d runs at the commit an operator asked about, want the one they asked for", n)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %+v (%v)", row, err)
	}
	if row.Commit != tip || row.Conclusion != "" || row.RunID == "" {
		t.Fatalf("main's row reads %s / %q on run %q, want the requested run judging the same tree",
			shortSHAForTest(row.Commit), row.Conclusion, row.RunID)
	}
	if row.RunRequestedAt != nil || row.RunRequestedBy != "" {
		t.Fatalf("main's row still carries a request (%v, %q): a request taken up must not start a "+
			"run every cycle forever", row.RunRequestedAt, row.RunRequestedBy)
	}
	if row.MergedTaskID != task.ID {
		t.Fatalf("the requested run names %q as the merge it judges, want %q: it is the same tree",
			row.MergedTaskID, task.ID)
	}

	// And the request is a question asked once: the cycles after it start
	// nothing further.
	for cycle := 0; cycle < 3; cycle++ {
		at = at.Add(time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	if n := fullRunsAt(runner, tip); n != spent+1 {
		t.Fatalf("%d runs at that commit, want the request to have been asked once", n)
	}
}

// The bound the button must not be a way round (grain/task-334): one
// base-branch run in flight per branch. A request that arrives while one
// is going is kept rather than refused or honoured on the spot -- the
// cycle that records the running run's verdict is the cycle after which
// the requested one may start.
func TestARequestedBaseBranchRunWaitsForTheOneInFlight(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	row := baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", baseTime.Add(time.Minute))
	tip, inFlight := row.Commit, row.RunID

	at := baseTime.Add(2 * time.Minute)
	if _, err := store.RequestBaseBranchCIRun(ctx, repo, "main", at, "alice"); err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 3; cycle++ {
		at = at.Add(time.Minute)
		if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
	}
	if n := fullRunsAt(runner, tip); n != 1 {
		t.Fatalf("%d runs are judging one branch at once, want the request to wait for the one in "+
			"flight -- a release run is 22 minutes to an hour and a half of this deployment's CI", n)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %+v (%v)", row, err)
	}
	if row.RunID != inFlight || row.Conclusion != "" {
		t.Fatalf("main's row reads run %q / %q, want the run in flight untouched", row.RunID, row.Conclusion)
	}
	if row.RunRequestedAt == nil {
		t.Fatal("the request was dropped while a run was in flight: it is what the next cycle starts")
	}

	// The run in flight finishes. One cycle records it, and the next
	// honours the request that has been waiting for it.
	runner.finish(inFlight, ci.ConclusionSuccess)
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	if n := fullRunsAt(runner, tip); n != 2 {
		t.Fatalf("%d runs at that commit, want the requested one started once the first concluded", n)
	}
	// And it is asked for uncached: the same commit asked again is
	// otherwise one content-addressed answer, replayed rather than run
	// (the 2026-10-13 §2 walkthrough: a red main could not be turned
	// green from the button). The automatic run before it was not.
	var atTip []ci.Request
	for _, req := range runner.requests {
		if req.Commit == tip {
			atTip = append(atTip, req)
		}
	}
	if len(atTip) != 2 || atTip[0].Recheck || !atTip[1].Recheck {
		t.Fatalf("requests at %s: %+v; want the merge's run cached and the one asked for by hand a recheck",
			shortSHAForTest(tip), atTip)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("main's row went missing: %+v (%v)", row, err)
	}
	if row.RunID == inFlight || row.Conclusion != "" || row.RunRequestedAt != nil {
		t.Fatalf("main's row reads run %q / %q with request %v, want the requested run in flight and "+
			"the request taken up", row.RunID, row.Conclusion, row.RunRequestedAt)
	}
}

// TestBaseBranchCIDemandDrivenLifecycle verifies that:
// 1. Idle state (no tasks, all merges verified) consumes 0 compute / 0 CI runs.
// 2. Pending task demand on unjudged base branch triggers single-flight test run on tip.
// 3. Single-flight execution lock prevents duplicate concurrent runs.
// 4. Once verified green, reconciler consumes 0 compute.
// 5. Landed merge triggers post-merge verification run on tip.
func TestBaseBranchCIDemandDrivenLifecycle(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	// 1. Idle state: with no tasks and no unverified merges, reconciler consumes 0 runs.
	at := baseTime
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("expected 0 CI runs when idle, got %d", len(runner.requests))
	}

	// 2. Pending task exists targeting base branch when base branch is unjudged -> starts single-flight CI run on tip.
	task1, branch1 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task1 = grantCI(t, ctx, store, task1)

	// Seed base branch CI row (unjudged)
	head, err := client.GetBranchHead(repo.Owner, repo.Name, "main")
	if err != nil || head == nil {
		t.Fatal("missing head for main")
	}
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: head.SHA, StartedAt: at,
	}); err != nil {
		t.Fatal(err)
	}

	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatal("missing base branch ci row")
	}
	if row.RunID == "" {
		t.Fatal("expected base branch CI run started when pending task demand exists on unjudged base")
	}
	firstRunID := row.RunID

	// 3. Single-flight lock: running reconciler again while run is in flight does not start a second run.
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatal("missing base branch ci row")
	}
	if row.RunID != firstRunID {
		t.Fatalf("expected same run ID %s due to single flight lock, got %s", firstRunID, row.RunID)
	}

	// Finish the run as success
	runner.finish(firstRunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})

	// Collect the verdict
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	row, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil || row.Conclusion != string(ci.ConclusionSuccess) {
		t.Fatalf("expected successful conclusion on main, got %+v", row)
	}

	// 4. Now main is verified green, running reconciler consumes 0 new runs.
	runCount := len(runner.requests)
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != runCount {
		t.Fatalf("expected no new CI runs when base is already verified, got %d vs %d", len(runner.requests), runCount)
	}

	// 5. Merge task1 into main -> Uncovered merge triggers post-merge CI on tip of main.
	greenGrainCI(t, ctx, store, client, runner, task1, repo, branch1)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, at)
	if !mergedBranch(sim, branch1) {
		t.Fatal("task1 did not merge")
	}

	at = at.Add(time.Minute)
	row = baseBranchRunAfterMerge(t, ctx, store, client, runner, repo, "main", at)
	if row.RunID == "" || row.RunID == firstRunID {
		t.Fatalf("expected fresh post-merge CI run on main after merge, got %s", row.RunID)
	}
}

func TestBaseBranchRepairTemplateHierarchy(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}

	// 1. Put templates
	for _, id := range []string{"tmpl-repo", "tmpl-global"} {
		if err := store.PutTemplate(ctx, model.Template{
			ID:    id,
			Name:  id,
			Title: "Title " + id,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 2. Put global config
	if err := store.PutConfig(ctx, model.Config{
		RepairTemplateID: "tmpl-global",
	}); err != nil {
		t.Fatal(err)
	}

	// 4. Put repo config
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:             repo,
		RepairTemplateID: "tmpl-repo",
	}); err != nil {
		t.Fatal(err)
	}

	// The repository's own template wins, whichever team asks: the
	// default team's per-repository template is repo_config's since
	// schema 23.
	for _, team := range []string{"team-a", "team-b"} {
		tmpl, err := orchestrator.ResolveRepairTemplate(ctx, store, model.RepoRef{}, repo, team)
		if err != nil || tmpl == nil || tmpl.ID != "tmpl-repo" {
			t.Fatalf("%s: got tmpl = %+v (%v), want tmpl-repo", team, tmpl, err)
		}
	}

	// Clear RepoConfig -> the deployment's tmpl-global wins, which took
	// the default team's own over at schema 24.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:             repo,
		RepairTemplateID: "",
	}); err != nil {
		t.Fatal(err)
	}
	tmpl, err := orchestrator.ResolveRepairTemplate(ctx, store, model.RepoRef{}, repo, "")
	if err != nil || tmpl == nil || tmpl.ID != "tmpl-global" {
		t.Fatalf("got tmpl = %+v (%v), want tmpl-global", tmpl, err)
	}

	// Clear global -> nil (falls back to system template in caller)
	if err := store.PutConfig(ctx, model.Config{
		RepairTemplateID: "",
	}); err != nil {
		t.Fatal(err)
	}
	tmpl, err = orchestrator.ResolveRepairTemplate(ctx, store, model.RepoRef{}, repo, "")
	if err != nil || tmpl != nil {
		t.Fatalf("got tmpl = %+v (%v), want nil", tmpl, err)
	}
}

// A red stretch is still recorded once it is over (model.BaseHoldPass,
// grain/task-986), but the queue holds nothing for it any more: the
// change waiting its turn says it is waiting for the queue, not
// base_broken, and the pass closes having held no change.
func TestARedBaseBranchIsRecordedButHoldsNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, "grain/task-t2")
	setMergeable(sim, true)

	red := model.BaseBranchCI{
		Repo: repo, Branch: "main", RunID: "old", Commit: headSHA(t, client, repo, "main"),
		StartedAt: baseTime, Conclusion: model.BaseBranchCIFailure, Detail: "the `go` job failed",
		Confirmation: model.BaseCIConfirmed, MergedTaskID: "t0", RepairTaskID: "t9",
	}
	if err := store.PutBaseBranchCI(ctx, red); err != nil {
		t.Fatal(err)
	}

	// The first change merges and the second waits its turn as it would on
	// a green branch, while a pass opens on main holding nothing.
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if state, _ := mergeQueueStateOf(t, ctx, store, second.ID); state == "base_broken" {
		t.Fatal("the waiting change's row says base_broken; nothing is held for a red base any more")
	}
	open, err := store.OpenBaseHoldPasses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Branch != "main" || open[0].Held != 0 || open[0].RepairTaskID != "t9" {
		t.Fatalf("after one red cycle the open passes are %+v, want one on main for t9 holding nothing", open)
	}

	// Half an hour later main is still red: the pass stays open and the
	// half hour is credited to the change it held.
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(30*time.Minute))
	if open, err = store.OpenBaseHoldPasses(ctx); err != nil || len(open) != 1 {
		t.Fatalf("main is still red and the open passes are %+v (%v), want one", open, err)
	}

	// main goes green: the next cycle closes the pass.
	green := red
	green.RunID, green.Conclusion, green.Confirmation, green.Detail = "new", "success", "", ""
	if err := store.PutBaseBranchCI(ctx, green); err != nil {
		t.Fatal(err)
	}
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(time.Hour))
	if open, err = store.OpenBaseHoldPasses(ctx); err != nil || len(open) != 0 {
		t.Fatalf("main is green and the open passes are %+v (%v), want none", open, err)
	}
	passes, err := store.BaseHoldPasses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(passes) != 1 {
		t.Fatalf("one red stretch left %d pass(es) behind, want 1", len(passes))
	}
	p := passes[0]
	if p.FinishedAt == nil || !p.FinishedAt.Equal(baseTime.Add(time.Hour)) {
		t.Fatalf("the pass finished at %v, want the cycle that found main green", p.FinishedAt)
	}
	if p.Peak != 0 || p.Total() != 0 || p.PRHeld() != 0 {
		t.Fatalf("the pass held peak %d, %v, %s of PR-time; want nothing held", p.Peak, p.HeldTasks, p.PRHeld())
	}
}
