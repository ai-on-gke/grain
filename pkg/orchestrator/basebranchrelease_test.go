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

// The publishing a merge sets off, which is no longer the verdict about
// the tree it publishes (grain/task-461).
//
// What is pinned here is the separation itself. A merge into a repository
// grain releases produces two runs at the same tip -- an untrusted push
// that judges and a trusted release that publishes -- and the only one
// the merge queue can see is the first. So the assertions come in pairs:
// the release still happens and still carries what a release carries, and
// no conclusion of it reaches model.BaseBranchCI, holds the queue or
// files a repair.
package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// runBaseBranchReleaseReconciler runs the cycle's own
// "base-branch-release" reconciler, found by name in Reconcilers() so
// that a change dropping it from the cycle fails here rather than
// quietly stopping a deployment publishing anything.
func runBaseBranchReleaseReconciler(t *testing.T, ctx context.Context, store *model.Store,
	client github.Client, runner ci.Runner, now time.Time) {

	t.Helper()
	deps := orchestrator.Deps{Store: store, Client: client, CI: runner}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "base-branch-release" {
			if err := r.Reconcile(ctx, deps, now); err != nil {
				t.Fatalf("releasing after a merge: %v", err)
			}
			return
		}
	}
	t.Fatal(`no "base-branch-release" reconciler in the cycle: a merge would judge the tree and ` +
		`publish nothing, which is not what taking the release out of the gate was supposed to do`)
}

// releaseRuns is every run this fake was asked for under `release` -- the
// publishing, as against the task branches' own push runs and the base
// branch's judging run, which every test here has going on around it.
func (f *fakeCI) releaseRuns() []ci.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ci.Request
	for _, req := range f.requests {
		if req.Event == ci.EventRelease {
			out = append(out, req)
		}
	}
	return out
}

// releasingRepo is a repository grain releases: grain CI on, and nothing
// said about who runs its release, which is the default every
// repository falls into.
func releasingRepo(t *testing.T, ctx context.Context, store *model.Store, repo model.RepoRef) {
	t.Helper()
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
}

// The whole of it: a change merges into a repository grain releases, the
// branch is judged by an untrusted push run, and the publishing is a
// second run at the same tip which the merge queue cannot see.
func TestAMergeReleasesInARunThatIsNotTheVerdict(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}
	tip := headSHA(t, client, repo, "main")

	// What judges the branch.
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	judging, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging == nil || judging.RunID == "" {
		t.Fatalf("main's row is %+v (%v), want a run started against the tip the merge left", judging, err)
	}
	if req := runner.lastRequest(t); req.Event != ci.EventPush || req.TrustedRelease {
		t.Fatalf("main is judged by %+v, want an untrusted push: a repository grain releases is "+
			"judged by its own tests now, not by whether an image published (grain/task-461)", req)
	}

	// And, beside it, what publishes it.
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Second))
	released := runner.releaseRuns()
	if len(released) != 1 {
		t.Fatalf("grain started %d release runs after the merge, want exactly one: taking the "+
			"release out of the gate must not take the release away", len(released))
	}
	req := released[0]
	if req.Ref != "main" || req.Commit != tip {
		t.Fatalf("the release is against %s at %s, want main's own tip %s", req.Ref, req.Commit, tip)
	}
	if !req.TrustedRelease {
		t.Fatal("the release run is not a trusted release, so it would be handed none of the " +
			"repository's release secrets and would publish nothing")
	}

	row, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("the release was recorded nowhere: %+v (%v)", row, err)
	}
	if row.Commit != tip || row.RunID == "" || !row.InFlight() {
		t.Fatalf("the release row is %+v, want it naming the run just started against %s", row, tip)
	}

	// The two are separate runs, which is the whole point: the judging
	// row must not have been overwritten by the publishing one.
	if judging.RunID == row.RunID {
		t.Fatal("the judging run and the release run are the same run, so the publisher's " +
			"conclusion is the merge queue's verdict again")
	}
}

// A release that fails changes nothing about the branch. This is the
// property the whole change exists for: before it, `release images`
// going red stopped every merge in the repository and spent an agent on
// a repair task, for a failure that says nothing about the tree.
func TestAFailedReleaseDoesNotHoldTheMergeQueue(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, secondBranch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, firstBranch) {
		t.Fatal("the first change did not merge")
	}

	// The branch's own run passes; the publishing fails.
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	judging, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging == nil {
		t.Fatalf("main was judged by nothing: %+v (%v)", judging, err)
	}
	runner.finish(judging.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Second))
	release, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release == nil {
		t.Fatalf("nothing published main: %+v (%v)", release, err)
	}
	runner.finish(release.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "images", Conclusion: ci.ConclusionFailure,
	})
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute))

	// Written down, and that is all that happens to it.
	release, err = store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release == nil {
		t.Fatalf("the failed release was recorded nowhere: %+v (%v)", release, err)
	}
	if release.Conclusion != string(ci.ConclusionFailure) || release.FinishedAt == nil {
		t.Fatalf("the release row reads %q (finished %v), want the failure recorded so that "+
			"somebody can be told about it (grain/task-453)", release.Conclusion, release.FinishedAt)
	}

	judging, err = store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging == nil {
		t.Fatalf("main's row is %+v (%v)", judging, err)
	}
	if judging.Broken() {
		t.Fatalf("main reads %q after a failed *release*: a publisher that breaks must not be a "+
			"red base branch", judging.Conclusion)
	}
	if judging.RepairTaskID != "" {
		t.Fatalf("a failed release filed repair task %q: nobody is being sent to fix a tree that "+
			"is not broken", judging.RepairTaskID)
	}

	// A triage task is filed for the release failure itself.
	triageTag := orchestrator.BaseReleaseTriageTag(repo, "main", release.Commit)
	hasTriage, err := store.HasTaskWithTag(ctx, triageTag)
	if err != nil || !hasTriage {
		t.Fatalf("expected triage task tagged %q for failed release, got hasTriage=%v err=%v", triageTag, hasTriage, err)
	}

	// And the queue goes on merging, which is the sentence the operator's
	// ruling comes down to.
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("nothing merged after a failed release: the release is gating the merge queue again")
	}
}

// Releases off means no release run, and nothing else. The judging run
// happens either way -- that is grain/task-458's rule, and this pins
// that grain/task-461 did not quietly re-couple the two by making the
// release unconditional.
func TestReleasesOffStopsThePublishingAndNotTheJudging(t *testing.T) {
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

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Second))

	if got := runner.releaseRuns(); len(got) != 0 {
		t.Fatalf("grain started %d release runs for a repository with releases off: %+v", len(got), got)
	}
	row, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row != nil {
		t.Fatalf("a release row exists for a repository that publishes nothing: %+v (%v)", row, err)
	}
	judging, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging == nil || judging.RunID == "" {
		t.Fatalf("main's row is %+v (%v), want the branch judged whatever its releases are doing",
			judging, err)
	}
}

// One release in flight per branch, model.BaseBranchDispatch's rule and
// for its reason: a publish is long, merges land faster than it
// finishes, and the newest tip is the tree anybody wants published.
func TestOnlyOneReleaseIsInFlightPerBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	first, firstBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	first = grantCI(t, ctx, store, first)
	second, secondBranch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	second = grantCI(t, ctx, store, second)
	greenGrainCI(t, ctx, store, client, runner, first, repo, firstBranch)
	greenGrainCI(t, ctx, store, client, runner, second, repo, secondBranch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second))
	if got := len(runner.releaseRuns()); got != 1 {
		t.Fatalf("the first merge started %d release runs, want one", got)
	}
	// A second merge lands while that one is still going, and the tip
	// moves -- but nothing new starts until it concludes.
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	judging, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging == nil {
		t.Fatalf("main was judged by nothing: %+v (%v)", judging, err)
	}
	runner.finish(judging.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(3*time.Second))
	if !mergedBranch(sim, secondBranch) {
		t.Fatal("the second change did not merge, so this test never asked the question")
	}
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(4*time.Second))
	if got := len(runner.releaseRuns()); got != 1 {
		t.Fatalf("%d release runs are in flight for one branch, want one: publishing two trees at "+
			"once is how a branch that merges faster than it builds never finishes a release", got)
	}

	// Once it concludes, the cycle after starts one against the tip as it
	// then stands -- the intermediate commit is skipped, not queued.
	release, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release == nil {
		t.Fatalf("nothing published main: %+v (%v)", release, err)
	}
	runner.finish(release.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "images", Conclusion: ci.ConclusionSuccess,
	})
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(5*time.Second))
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(6*time.Second))
	runs := runner.releaseRuns()
	if len(runs) != 2 {
		t.Fatalf("%d release runs in all, want a second one against the tip the second merge left", len(runs))
	}
	if tip := headSHA(t, client, repo, "main"); runs[1].Commit != tip {
		t.Fatalf("the second release is against %s, want main's tip %s", runs[1].Commit, tip)
	}
}

func TestBaseBranchReleaseSkipsOffAllowlistRepo(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)

	syncWithCI(t, ctx, store, client, runner, baseTime)

	// Run release reconciler with restricted TargetRepos excluding acme/widgets
	store.SetTargetRepos(ctx, []string{"other/repo"})
	deps := orchestrator.Deps{
		Store:  store,
		Client: client,
		CI:     runner,
		Config: orchestrator.Config{},
	}
	for _, r := range orchestrator.Reconcilers() {
		if r.Name == "base-branch-release" {
			if err := r.Reconcile(ctx, deps, baseTime.Add(time.Second)); err != nil {
				t.Fatalf("releasing after a merge: %v", err)
			}
			break
		}
	}

	if got := len(runner.releaseRuns()); got != 0 {
		t.Fatalf("started %d release runs for off-allowlist repo, want 0", got)
	}
}

func TestBaseBranchReleaseFailureFilesTriageTaskWithDeduplication(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	judging, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging == nil {
		t.Fatalf("main was judged by nothing: %+v (%v)", judging, err)
	}
	runner.finish(judging.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})

	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Second))
	release, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release == nil {
		t.Fatalf("nothing published main: %+v (%v)", release, err)
	}

	// Release run fails
	runner.finish(release.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "docker", Conclusion: ci.ConclusionFailure,
	})

	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute))

	// Check that release was recorded as failed
	release, err = store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release == nil {
		t.Fatalf("release row missing: %v", err)
	}
	if release.Conclusion != string(ci.ConclusionFailure) {
		t.Fatalf("release conclusion = %q, want failure", release.Conclusion)
	}

	// Verify triage task was filed
	triageTag := orchestrator.BaseReleaseTriageTag(repo, "main", release.Commit)
	triageID, err := store.TaskIDWithTag(ctx, triageTag)
	if err != nil || triageID == "" {
		t.Fatalf("expected triage task tagged %q, got id=%q err=%v", triageTag, triageID, err)
	}
	triageTask, err := store.GetTask(ctx, triageID)
	if err != nil || triageTask == nil {
		t.Fatalf("failed to get triage task %q: %v", triageID, err)
	}
	if triageTask.Intent != model.IntentImplement {
		t.Errorf("triage task intent = %q, want %q", triageTask.Intent, model.IntentImplement)
	}
	if !triageTask.AutoMerge {
		t.Errorf("triage task autoMerge = false, want true")
	}
	if triageTask.Approval == nil || triageTask.Approval.Actor != orchestrator.BaseReleaseTriager {
		t.Errorf("triage task approval = %+v, want actor %v", triageTask.Approval, orchestrator.BaseReleaseTriager)
	}
	if !grainci.Granted(triageTask.Grants) {
		t.Errorf("triage task grants %+v, want grain-ci granted", triageTask.Grants)
	}
	if !strings.Contains(triageTask.Body, release.RunID) {
		t.Errorf("triage task body %q does not contain run ID %s", triageTask.Body, release.RunID)
	}

	// Re-running the release reconciler for the same finished run must not create a second triage task
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var matchingCount int
	for _, tk := range tasks {
		for _, tg := range tk.Tags {
			if tg == triageTag {
				matchingCount++
			}
		}
	}
	if matchingCount != 1 {
		t.Fatalf("found %d triage tasks tagged %q, want exactly 1 (deduplicated)", matchingCount, triageTag)
	}

	// Now test a failed release run on a new commit while triage task 1 is active.
	// A failure rather than an errored run: an errored one is an infra verdict,
	// re-run before it is counted or triaged (TestAnInfraReleaseIsReRunBeforeItIsTriaged).
	// Since startBaseBranchRelease gates new releases while triage is open, we put
	// an in-flight release row directly to test deduplication and comment appending on failure.
	task2, branch2 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	task2 = grantCI(t, ctx, store, task2)
	greenGrainCI(t, ctx, store, client, runner, task2, repo, branch2)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(3*time.Minute))
	if !mergedBranch(sim, branch2) {
		t.Fatal("the second change did not merge")
	}

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	judging2, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging2 == nil {
		t.Fatalf("main was judged by nothing: %+v (%v)", judging2, err)
	}
	runner.finish(judging2.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})

	commit2 := headSHA(t, client, repo, "main")
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: commit2, RunID: "rel-2", StartedAt: baseTime.Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	runner.finish("rel-2", ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "docker", Conclusion: ci.ConclusionFailure,
	})

	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(6*time.Minute))

	release2, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release2 == nil {
		t.Fatalf("release row missing: %v", err)
	}
	if release2.Conclusion != string(ci.ConclusionFailure) {
		t.Fatalf("release conclusion = %q, want failure", release2.Conclusion)
	}

	triageTag2 := orchestrator.BaseReleaseTriageTag(repo, "main", release2.Commit)
	triageID2, err := store.TaskIDWithTag(ctx, triageTag2)
	if err != nil || triageID2 != "" {
		t.Fatalf("expected no new triage task while task 1 open, got id=%q err=%v", triageID2, err)
	}
	comments, err := store.Comments(ctx, triageTask.ID)
	if err != nil {
		t.Fatalf("reading comments on triage task: %v", err)
	}
	if len(comments) == 0 {
		t.Fatalf("expected comment added to existing triage task %s, got none", triageTask.ID)
	}
	if !strings.Contains(comments[0].Body, release2.RunID) {
		t.Errorf("comment body %q does not contain run ID %s", comments[0].Body, release2.RunID)
	}

	// Now close triageTask and verify a subsequent failed release creates a new triage task
	closedAt := baseTime.Add(7 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: triageTask.ID, ClosedAt: &closedAt}); err != nil {
		t.Fatalf("closing triage task: %v", err)
	}

	task3, branch3 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t3", repo)
	task3 = grantCI(t, ctx, store, task3)
	greenGrainCI(t, ctx, store, client, runner, task3, repo, branch3)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime.Add(8*time.Minute))
	if !mergedBranch(sim, branch3) {
		t.Fatal("the third change did not merge")
	}

	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	judging3, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging3 == nil {
		t.Fatalf("main was judged by nothing: %+v (%v)", judging3, err)
	}
	runner.finish(judging3.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})

	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(10*time.Minute))
	release3, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || release3 == nil {
		t.Fatalf("nothing published main on third merge: %+v (%v)", release3, err)
	}

	runner.finish(release3.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "docker", Conclusion: ci.ConclusionFailure,
	})

	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(11*time.Minute))
	triageTag3 := orchestrator.BaseReleaseTriageTag(repo, "main", release3.Commit)
	triageID3, err := store.TaskIDWithTag(ctx, triageTag3)
	if err != nil || triageID3 == "" {
		t.Fatalf("expected new triage task tagged %q after old one closed, got id=%q err=%v", triageTag3, triageID3, err)
	}
	if triageID3 == triageTask.ID {
		t.Fatalf("expected fresh task ID for third release failure, got previous ID %s", triageID3)
	}
}

// The counting a reader needs, and nothing more than counting
// (grain/task-511). One row per branch replaced on every release means
// the newest attempt is the only history there is, so the run of
// failures is carried across the replace -- otherwise "this repository
// has shipped nothing since Tuesday" is a sentence nothing can say, and
// "the last release failed" flickers on and off as fast as merges land.
//
// What it must not become is anything else. The assertions come in pairs
// again: the streak moves, and the branch's own row, its repair and the
// queue's verdict are exactly where they were.
func TestAReleaseThatKeepsFailingIsCountedAndStillHoldsNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	tip := headSHA(t, client, repo, "main")
	judged := baseTime.Add(-time.Hour)
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
		Repo: repo, Branch: "main", Commit: tip, RunID: "judge-1",
		StartedAt: judged, FinishedAt: &judged, Conclusion: string(ci.ConclusionSuccess),
	}); err != nil {
		t.Fatal(err)
	}

	// A release for an older tip, in flight, which is about to fail:
	// exactly the row startBaseBranchRelease leaves behind.
	first := baseTime.Add(-30 * time.Minute)
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "0000old", RunID: "rel-1", StartedAt: first,
	}); err != nil {
		t.Fatal(err)
	}
	runner.finish("rel-1", ci.ConclusionFailure)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime)

	row, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("the release row is %+v (%v)", row, err)
	}
	if row.Failures != 1 || row.FailingSince == nil || !row.FailingSince.Equal(first) {
		t.Fatalf("after one failed release the row reads failures=%d since=%v, want one failure "+
			"dated from when that release was started", row.Failures, row.FailingSince)
	}

	// Close the triage task filed for the failure so releases can resume.
	triageTag := orchestrator.BaseReleaseTriageTag(repo, "main", "0000old")
	triageID, err := store.TaskIDWithTag(ctx, triageTag)
	if err != nil || triageID == "" {
		t.Fatalf("expected triage task, got id=%q err=%v", triageID, err)
	}
	closedAt := baseTime.Add(30 * time.Second)
	if err := store.Observe(ctx, model.Observation{TaskID: triageID, ClosedAt: &closedAt}); err != nil {
		t.Fatal(err)
	}

	// The next merge's release starts against the tip, and the streak is
	// the branch's rather than the attempt's: it survives the replace.
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute))
	row, err = store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row == nil || row.Commit != tip || !row.InFlight() {
		t.Fatalf("the release row is %+v (%v), want a fresh release in flight against %s", row, err, tip)
	}
	if row.Failures != 1 || row.FailingSince == nil || !row.FailingSince.Equal(first) {
		t.Fatalf("the fresh release row reads failures=%d since=%v, want the branch's run of "+
			"failures carried across the replace -- a row that forgets it is a row that can only "+
			"ever say \"the last one failed\"", row.Failures, row.FailingSince)
	}

	runner.finish(row.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "images", Conclusion: ci.ConclusionFailure,
	})
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
	row, err = store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("the release row is %+v (%v)", row, err)
	}
	if row.Failures != 2 || row.FailingSince == nil || !row.FailingSince.Equal(first) {
		t.Fatalf("after a second failure the row reads failures=%d since=%v, want two dated from "+
			"the first of them", row.Failures, row.FailingSince)
	}

	// And none of that has reached the branch's own row, which is the
	// invariant this whole file exists for.
	judging, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging == nil {
		t.Fatalf("main's row is %+v (%v)", judging, err)
	}
	if judging.Broken() || judging.RepairTaskID != "" {
		t.Fatalf("main reads %q with repair task %q after two failed *releases*: counting them "+
			"must not be a way for the publisher to reach the merge queue",
			judging.Conclusion, judging.RepairTaskID)
	}

	// A release that publishes ends the run of failures outright: what
	// the reading says is "nothing has shipped since", and something has.
	third := baseTime.Add(3 * time.Minute)
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: "main", Commit: "0000newer", RunID: "rel-9", StartedAt: third,
		Failures: row.Failures, FailingSince: row.FailingSince,
	}); err != nil {
		t.Fatal(err)
	}
	runner.finish("rel-9", ci.ConclusionSuccess)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(4*time.Minute))
	row, err = store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("the release row is %+v (%v)", row, err)
	}
	if row.Failures != 0 || row.FailingSince != nil {
		t.Fatalf("after a release that published, the row still reads failures=%d since=%v",
			row.Failures, row.FailingSince)
	}
}

func TestANewerTipIsReleasedWhileItsTriageIsStillOpen(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	// 1. First commit merges and passes judging CI
	task1, branch1 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task1 = grantCI(t, ctx, store, task1)
	greenGrainCI(t, ctx, store, client, runner, task1, repo, branch1)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch1) {
		t.Fatal("first change did not merge")
	}

	at := baseTime.Add(time.Second)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	judging1, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging1 == nil {
		t.Fatalf("main was judged by nothing: %+v (%v)", judging1, err)
	}
	runner.finish(judging1.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})

	// Start and fail the release for commit 1
	at = at.Add(time.Second)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)
	rel1, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel1 == nil || !rel1.InFlight() {
		t.Fatalf("expected release 1 in flight: %+v (%v)", rel1, err)
	}

	runner.finish(rel1.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "docker", Conclusion: ci.ConclusionFailure,
	})
	at = at.Add(time.Minute)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)

	// Triage task is filed
	triageTag := orchestrator.BaseReleaseTriageTag(repo, "main", rel1.Commit)
	triageID, err := store.TaskIDWithTag(ctx, triageTag)
	if err != nil || triageID == "" {
		t.Fatalf("expected triage task filed, got id=%q err=%v", triageID, err)
	}

	// 2. Second commit merges and passes judging CI while triage task is still OPEN
	task2, branch2 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t2", repo)
	task2 = grantCI(t, ctx, store, task2)
	greenGrainCI(t, ctx, store, client, runner, task2, repo, branch2)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, at.Add(time.Minute))
	if !mergedBranch(sim, branch2) {
		t.Fatal("second change did not merge")
	}

	at = at.Add(2 * time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	judging2, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging2 == nil {
		t.Fatalf("main judging 2 missing: %+v (%v)", judging2, err)
	}
	runner.finish(judging2.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})
	at = at.Add(time.Minute)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}

	// The triage of commit 1's release is queued, and commit 2 is
	// released anyway: an open triage used to hold every release of the
	// branch, which is how one failed release left main's image behind
	// every merge after it (grain/task-4432).
	if open, err := store.OpenTaskWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main")); err != nil || open == nil {
		t.Fatalf("the triage of release 1 is not open (%+v, %v), so this proves nothing", open, err)
	}
	at = at.Add(time.Minute)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)

	rel2, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel2 == nil {
		t.Fatalf("release row missing: %v", err)
	}
	tip2 := headSHA(t, client, repo, "main")
	if rel2.Commit != tip2 || !rel2.InFlight() {
		t.Fatalf("expected release 2 in flight for %s while release 1's triage is open, got %+v", tip2, rel2)
	}
	if rel2.Attempts != 1 {
		t.Errorf("release 2 is attempt %d, want 1: a new commit starts its count again", rel2.Attempts)
	}
}

func TestBaseBranchReleaseTriageCustomTemplate(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	// Set repo custom repair template
	if err := store.PutTemplate(ctx, model.Template{
		ID:            "custom-release-triage",
		Name:          "Custom Release Triage",
		Title:         "Custom Triage for {{repo}} on {{branch}}",
		Body:          "Custom body: failure at {{commit}}, details: {{detail}}",
		AutoMerge:     false,
		NetworkAccess: model.NetworkNone,
	}); err != nil {
		t.Fatal(err)
	}
	repoCfg, err := store.GetRepoConfig(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	repoCfg.RepairTemplateID = "custom-release-triage"
	if err := store.PutRepoConfig(ctx, *repoCfg); err != nil {
		t.Fatal(err)
	}

	task1, branch1 := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task1 = grantCI(t, ctx, store, task1)
	greenGrainCI(t, ctx, store, client, runner, task1, repo, branch1)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)

	at := baseTime.Add(time.Second)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}
	judging1, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || judging1 == nil {
		t.Fatal("missing judging 1")
	}
	runner.finish(judging1.RunID, ci.ConclusionSuccess, ci.JobRun{
		WorkflowPath: ".grain/workflows/tests.yml", JobID: "go", Conclusion: ci.ConclusionSuccess,
	})
	at = at.Add(time.Second)
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, at); err != nil {
		t.Fatal(err)
	}

	at = at.Add(time.Second)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)
	rel1, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel1 == nil || !rel1.InFlight() {
		t.Fatal("missing release 1")
	}
	runner.finish(rel1.RunID, ci.ConclusionFailure, ci.JobRun{
		WorkflowPath: ".grain/workflows/release.yml", JobID: "docker", Conclusion: ci.ConclusionFailure,
	})

	at = at.Add(time.Minute)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)

	triageTag := orchestrator.BaseReleaseTriageTag(repo, "main", rel1.Commit)
	triageID, err := store.TaskIDWithTag(ctx, triageTag)
	if err != nil || triageID == "" {
		t.Fatalf("expected triage task filed, got id=%q err=%v", triageID, err)
	}
	triageTask, err := store.GetTask(ctx, triageID)
	if err != nil || triageTask == nil {
		t.Fatal("failed to get triage task")
	}

	if triageTask.Title != "Custom Triage for acme/widgets on main" {
		t.Errorf("triage task title = %q, want %q", triageTask.Title, "Custom Triage for acme/widgets on main")
	}
	if !strings.Contains(triageTask.Body, "Custom body: failure at") {
		t.Errorf("triage task body = %q, want custom body", triageTask.Body)
	}
	if triageTask.AutoMerge != false {
		t.Errorf("triage task autoMerge = %v, want false", triageTask.AutoMerge)
	}
	if triageTask.NetworkAccess != model.NetworkNone {
		t.Errorf("triage task networkAccess = %v, want %v", triageTask.NetworkAccess, model.NetworkNone)
	}
}

// A release the daemon was running when it restarted never failed: the
// process running it went away. So it files no triage task -- which
// would send an agent to repair a pipeline that is fine, and hold the
// branch's releases until somebody closed it -- counts toward no run of
// failures, and the branch is released again at the same tip on the
// cycle after. The same goes for a run the new daemon has never heard of.
func TestAnInterruptedReleaseFilesNoTriageAndIsStartedAgain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		lose func(runner *fakeCI, id string)
	}{
		{"interrupted", func(runner *fakeCI, id string) { runner.interrupt(id) }},
		{"lost", func(runner *fakeCI, id string) {
			runner.mu.Lock()
			defer runner.mu.Unlock()
			delete(runner.runs, id)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store, ctx := openStore(t)
			_, client := newSim(t, "acme", "widgets", "main")
			repo := model.RepoRef{Owner: "acme", Name: "widgets"}
			runner := newFakeCI()
			releasingRepo(t, ctx, store, repo)

			tip := headSHA(t, client, repo, "main")
			judged := baseTime.Add(-time.Hour)
			if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
				Repo: repo, Branch: "main", Commit: tip, RunID: "judge-1",
				StartedAt: judged, FinishedAt: &judged, Conclusion: string(ci.ConclusionSuccess),
			}); err != nil {
				t.Fatal(err)
			}
			// A streak already under way, which the restart must leave as
			// it found it.
			since := baseTime.Add(-2 * time.Hour)
			if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
				Repo: repo, Branch: "main", Commit: "0000old", RunID: "rel-0",
				StartedAt: since, FinishedAt: &since, Conclusion: string(ci.ConclusionFailure),
				Failures: 1, FailingSince: &since,
			}); err != nil {
				t.Fatal(err)
			}

			runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime)
			row, err := store.GetBaseBranchRelease(ctx, repo, "main")
			if err != nil || row == nil || row.Commit != tip || !row.InFlight() {
				t.Fatalf("the release row is %+v (%v), want a release in flight against %s", row, err, tip)
			}
			first := row.RunID

			tc.lose(runner, first)
			runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Minute))

			for _, tag := range []string{
				orchestrator.BaseReleaseTriageTag(repo, "main", tip),
				orchestrator.BaseReleaseTriageBranchTag(repo, "main"),
			} {
				has, err := store.HasTaskWithTag(ctx, tag)
				if err != nil {
					t.Fatal(err)
				}
				if has {
					t.Fatalf("a %s release filed triage task %q: nothing about the release pipeline "+
						"failed, the daemon running it went away", tc.name, tag)
				}
			}
			row, err = store.GetBaseBranchRelease(ctx, repo, "main")
			if err != nil || row == nil {
				t.Fatalf("the release row is %+v (%v)", row, err)
			}
			if row.InFlight() || row.Conclusion != "" {
				t.Fatalf("the release row reads %+v after the run was %s, want it forgotten: neither "+
					"in flight nor concluded", row, tc.name)
			}
			if row.Failures != 1 || row.FailingSince == nil || !row.FailingSince.Equal(since) {
				t.Fatalf("after a %s release the row reads failures=%d since=%v, want the streak "+
					"left as it was (1 since %v)", tc.name, row.Failures, row.FailingSince, since)
			}

			// Nothing has merged since, and the branch is released again at
			// the same tip all the same.
			runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(2*time.Minute))
			row, err = store.GetBaseBranchRelease(ctx, repo, "main")
			if err != nil || row == nil || row.Commit != tip || !row.InFlight() || row.RunID == first {
				t.Fatalf("the release row is %+v (%v), want a fresh release in flight against %s", row, err, tip)
			}
			if got := len(runner.releaseRuns()); got != 2 {
				t.Fatalf("grain started %d release runs, want 2: the one the restart took and its replacement", got)
			}
			if row.Failures != 1 {
				t.Fatalf("the fresh release row reads failures=%d, want the streak carried across as 1", row.Failures)
			}
		})
	}
}

// A release that fails while its commit is still the branch's tip is
// retried once, and then left: the tip catch-up of grain/task-4432. One
// flaky twenty-minute release used to leave the branch's moving image tag
// on an older commit until somebody happened to merge again; retrying it
// forever would spend a deployment's CI on a tree that cannot publish.
func TestAFailedReleaseOfTheTipIsRetriedOnceAndThenLeft(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	greenGrainCI(t, ctx, store, client, runner, task, repo, branch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, runner, baseTime)
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}
	tip := headSHA(t, client, repo, "main")

	at := baseTime.Add(time.Second)
	fail := func(want int) {
		t.Helper()
		rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
		if err != nil || rel == nil || !rel.InFlight() || rel.Commit != tip {
			t.Fatalf("want a release of %s in flight, got %+v (%v)", tip, rel, err)
		}
		if rel.Attempts != want {
			t.Fatalf("the release in flight is attempt %d, want %d", rel.Attempts, want)
		}
		runner.finish(rel.RunID, ci.ConclusionFailure, ci.JobRun{
			WorkflowPath: ".grain/workflows/release.yml", JobID: "images", Conclusion: ci.ConclusionFailure,
		})
		at = at.Add(time.Minute)
		runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at) // collects it
	}

	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)
	fail(1)
	if got := len(runner.releaseRuns()); got != 1 {
		t.Fatalf("%d release runs after the first failed, want 1 (the collecting cycle starts nothing)", got)
	}

	// The cycle after the failure is collected: nothing has merged, the
	// tip has no published image and nothing is in flight, so it is
	// released again -- while the triage of the failure is still queued.
	at = at.Add(time.Minute)
	runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)
	if got := len(runner.releaseRuns()); got != 2 {
		t.Fatalf("%d release runs, want the failed tip retried once", got)
	}
	fail(2)

	// Twice is the bound: the tip waits for a merge or a human.
	for i := 0; i < 3; i++ {
		at = at.Add(time.Minute)
		runBaseBranchReleaseReconciler(t, ctx, store, client, runner, at)
	}
	if got := len(runner.releaseRuns()); got != 2 {
		t.Fatalf("%d release runs, want no more than %d for one commit", got, model.MaxReleaseAttempts)
	}
	rel, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || rel == nil {
		t.Fatalf("release row missing: %v", err)
	}
	if rel.Failures != 2 {
		t.Errorf("the run of failures is %d, want both attempts counted", rel.Failures)
	}
}

// A repository on grain CI that declares no `release:` workflow -- every
// new one, until it writes one -- publishes nothing when a change merges,
// and that is not a failed release. Driven through the real grain CI
// service, whose answer to a release no workflow answers is a skipped
// run rather than a red one: what is pinned is that grain reads that as
// nothing to publish, files no triage task and does not retry the tip
// (grain/task-4575).
func TestARepositoryWithNoReleaseWorkflowFilesNoReleaseTriage(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	releasingRepo(t, ctx, store, repo)
	svc, sandboxes := realCI(t) // one workflow, answering push and pull_request
	sandboxes.reset(t, 0)

	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	task = grantCI(t, ctx, store, task)
	startRealCI(t, ctx, store, client, svc, task, repo, branch)
	settleCI(t, ctx, store, client, svc, baseTime)
	greenOnGitHub(sim, branch)
	setMergeable(sim, true)
	syncWithCI(t, ctx, store, client, svc, baseTime.Add(time.Minute))
	if !mergedBranch(sim, branch) {
		t.Fatal("the change did not merge")
	}
	tip := headSHA(t, client, repo, "main")
	if err := runBaseBranchCIReconciler(t, ctx, store, client, svc, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	releaseRuns := func() []ci.Run {
		var out []ci.Run
		for _, run := range svc.List() {
			if run.Event == ci.EventRelease {
				out = append(out, run)
			}
		}
		return out
	}
	for i := 0; i < 4; i++ {
		runBaseBranchReleaseReconciler(t, ctx, store, client, svc, baseTime.Add(time.Duration(3+i)*time.Minute))
		awaitRuns(t, svc)
	}

	released := releaseRuns()
	if len(released) != 1 {
		t.Fatalf("grain started %d release runs of %s, want exactly one: a tip with nothing to "+
			"publish is not retried", len(released), shortSHAForTest(tip))
	}
	if !ci.NoWorkflowAnswered(released[0]) {
		t.Fatalf("the release run is %+v, want grain CI's own answer to a release no workflow answers", released[0])
	}
	if released[0].Conclusion != ci.ConclusionSkipped {
		t.Fatalf("the release run concluded %q, want skipped: nothing was published and nothing failed, "+
			"so it is no red run in the CI list", released[0].Conclusion)
	}
	row, err := store.GetBaseBranchRelease(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("the release was recorded nowhere: %+v (%v)", row, err)
	}
	if row.Commit != tip || row.Conclusion != string(ci.ConclusionSkipped) || row.Failing() {
		t.Fatalf("the release row is %+v, want %s recorded as skipped with no failure counted", row, shortSHAForTest(tip))
	}
	open, err := store.OpenTaskWithTag(ctx, orchestrator.BaseReleaseTriageBranchTag(repo, "main"))
	if err != nil {
		t.Fatal(err)
	}
	if open != nil {
		t.Fatalf("grain filed release triage task %s %q for a repository that declares no release", open.ID, open.Title)
	}
}

// A task's branch is never released, even when grain has a base-branch
// row for it (grain/task-949). A stacked review's merge into the reviewed
// task's branch used to write exactly that row, and the release
// reconciler walked every row: so the task's own .grain/workflows ran as
// a trusted release, with the repository's release secrets mounted. The
// repository's own default branch, beside it, still is released.
func TestATaskBranchWithABaseRowIsNeverReleased(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	taskBranch := "grain/049f1e09/task-123"
	pushBranch(t, sim.BareRepo, taskBranch)
	for _, branch := range []string{taskBranch, "main"} {
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{
			Repo: repo, Branch: branch, StartedAt: baseTime,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for i := range 3 {
		runBaseBranchReleaseReconciler(t, ctx, store, client, runner, baseTime.Add(time.Duration(i+1)*time.Second))
	}
	var main int
	for _, req := range runner.releaseRuns() {
		if req.Ref == taskBranch {
			t.Fatalf("grain started a release of the task branch %s (%+v): its workflows are its "+
				"author's, and a release run mounts the repository's release secrets", taskBranch, req)
		}
		if req.Ref == "main" && req.TrustedRelease {
			main++
		}
	}
	if main != 1 {
		t.Fatalf("main was released %d times, want once: refusing task branches must not stop the "+
			"default branch publishing", main)
	}
	row, err := store.GetBaseBranchRelease(ctx, repo, taskBranch)
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("a release row was recorded for the task branch: %+v", row)
	}
}

// The judging reconciler clears the rows a stacked merge wrote before
// grain/task-949, so an enrolled task branch stops being judged, repaired
// and offered as a branch to release from.
func TestTheBaseBranchReconcilerForgetsTaskBranches(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	runner := newFakeCI()
	releasingRepo(t, ctx, store, repo)

	taskBranch := "grain/049f1e09/task-123"
	if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: taskBranch, StartedAt: baseTime}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{
		Repo: repo, Branch: taskBranch, Commit: "abc", RunID: "r1", StartedAt: baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := runBaseBranchCIReconciler(t, ctx, store, client, runner, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if row, err := store.GetBaseBranchCI(ctx, repo, taskBranch); err != nil || row != nil {
		t.Fatalf("the task branch is still watched: %+v (%v)", row, err)
	}
	if row, err := store.GetBaseBranchRelease(ctx, repo, taskBranch); err != nil || row != nil {
		t.Fatalf("the task branch still has a release row: %+v (%v)", row, err)
	}
	runner.mu.Lock()
	requests := append([]ci.Request(nil), runner.requests...)
	runner.mu.Unlock()
	for _, req := range requests {
		if req.Ref == taskBranch {
			t.Fatalf("a run was started on the task branch: %+v", req)
		}
	}
}
