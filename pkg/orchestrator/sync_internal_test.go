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

package orchestrator

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/forge"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

type postMergeCI struct {
	ci.NopRunner
	requests []ci.Request
}

func (f *postMergeCI) Start(_ context.Context, req ci.Request) (ci.Run, error) {
	f.requests = append(f.requests, req)
	return ci.Run{ID: "release", Repo: req.Repo, Ref: req.Ref, Commit: req.Commit, Event: req.Event}, nil
}
func (*postMergeCI) Get(string) (ci.Run, bool)                   { return ci.Run{}, false }
func (*postMergeCI) Cancel(string, string, ci.CancelSource) bool { return false }

type postMergeHeadClient struct {
	github.Client
	head *github.BranchHead
}

func (c postMergeHeadClient) GetBranchHead(_, _, _ string) (*github.BranchHead, error) {
	return c.head, nil
}

// A merge records the branch's new tip and starts nothing: one release
// run per branch is in flight at a time, and reconcileBaseBranchCI is the
// only thing that starts one (grain/task-334).
func TestMergedPullRequestRecordsTheBaseBranchsNewTip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	runner := &postMergeCI{}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{
		ID: "42", Target: &repo,
		Grants: []model.Grant{{Capability: grainci.CapabilityName}},
	}
	ref := model.PullRequestRef{Repo: repo, Number: 7}
	err := recordPostMergeCI(ctx, store, postMergeHeadClient{
		head: &github.BranchHead{SHA: "0123456789abcdef"},
	}, runner, task, false, ref, github.PullRequestDetail{BaseRef: "main"}, time.Now())
	if err != nil {
		t.Fatalf("recording the post-merge tip: %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("the merge started %d runs itself; starting one is the reconciler's, so that "+
			"merges landing during a release build coalesce instead of stacking", len(runner.requests))
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("nothing was recorded for main, so no run will ever be started for it: %+v (%v)", row, err)
	}
	if row.PendingTaskID != "42" {
		t.Fatalf("the row names %q as the merge waiting for a verdict, want the task that merged",
			row.PendingTaskID)
	}
	if row.RunID != "" || row.Conclusion != "" {
		t.Fatalf("the row reads run %q, verdict %q: it should hold neither yet", row.RunID, row.Conclusion)
	}
}

// A stacked merge -- a review or fix task's pull request into the
// reviewed task's own branch -- moves no base branch, so it must not
// enrol that task branch as one (grain/task-949). It used to: the row it
// wrote made the agent-authored branch something grain judged, repaired
// and released with the repository's release secrets.
func TestStackedMergeDoesNotEnrolTheTaskBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}
	taskBranch := "grain/049f1e09/task-123"
	for _, tc := range []struct {
		name    string
		task    model.Task
		stacked bool
	}{
		{"fix", model.Task{ID: "200", Target: &repo, Base: taskBranch,
			Origin: model.Origin{Reason: model.ReasonFix}}, true},
		{"review", model.Task{ID: "201", Target: &repo, Base: taskBranch,
			Origin: model.Origin{Reason: model.ReasonReview}}, true},
		// Whatever the task says about itself, the branch is a task's.
		{"unmarked", model.Task{ID: "202", Target: &repo, Base: taskBranch}, false},
	} {
		// syncEntry's own reading of the task, so this is the flag a
		// real merge of it passes.
		if want := tc.task.Origin.Reason == model.ReasonFix || isReviewTask(tc.task); want != tc.stacked {
			t.Fatalf("%s: syncEntry would read stacked = %v, the case says %v", tc.name, want, tc.stacked)
		}
		err := recordPostMergeCI(ctx, store, postMergeHeadClient{
			head: &github.BranchHead{SHA: "0123456789abcdef"},
		}, &postMergeCI{}, tc.task, tc.stacked, model.PullRequestRef{Repo: repo, Number: 7},
			github.PullRequestDetail{BaseRef: taskBranch}, time.Now())
		if err != nil {
			t.Fatalf("%s: recording the merge: %v", tc.name, err)
		}
		row, err := store.GetBaseBranchCI(ctx, repo, taskBranch)
		if err != nil {
			t.Fatal(err)
		}
		if row != nil {
			t.Fatalf("%s: the merge enrolled the task branch %s as a watched base branch: %+v",
				tc.name, taskBranch, row)
		}
	}
}

func TestMergedPullRequestWithoutGrainCIRecordsNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	runner := &postMergeCI{}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := model.Task{ID: "42", Target: &repo}
	err := recordPostMergeCI(ctx, store, postMergeHeadClient{}, runner, task, false,
		model.PullRequestRef{Repo: repo, Number: 7}, github.PullRequestDetail{BaseRef: "main"},
		time.Now())
	if err != nil || len(runner.requests) != 0 {
		t.Fatalf("ungranted post-merge CI = %v, requests %+v", err, runner.requests)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if row != nil {
		t.Fatalf("a branch nobody asked grain to judge was enrolled anyway: %+v", row)
	}
}

// The base branch is tested on the repository's own settings, not on a
// capability ticked on whichever task happened to merge (grain/task-320).
// A task filed before the repo was opted into grain CI, or one somebody
// unticked it on, used to merge and leave `main` untested with nothing
// said anywhere -- which is how gke-labs/kontur's `main` went five commits
// without a run.
func TestMergedPullRequestRecordsTheTipOnTheRepositorysSettings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
	}); err != nil {
		t.Fatal(err)
	}
	runner := &postMergeCI{}
	// No grant on the task at all: an old one, or one somebody unticked.
	task := model.Task{ID: "42", Target: &repo}
	err := recordPostMergeCI(ctx, store, postMergeHeadClient{
		head: &github.BranchHead{SHA: "0123456789abcdef"},
	}, runner, task, false, model.PullRequestRef{Repo: repo, Number: 7},
		github.PullRequestDetail{BaseRef: "main"}, time.Now())
	if err != nil {
		t.Fatalf("recording the post-merge tip: %v", err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("no post-merge record was made for main: %+v (%v): the repository is opted into "+
			"grain CI, so its base branch is judged whatever the merged task was granted", row, err)
	}
}

// Releases off and no judging jobs named: the merge is still recorded,
// because the switch is named for releases and does only that
// (grain/task-458). It used to record nothing at all -- which is how this
// deployment's own `main` went 33 hours and ~100 merges unjudged with the
// queue merging onto it the whole time.
func TestMergedPullRequestWithReleasesOffAndNoJobsStillRecordsTheTip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, Work: model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
	}); err != nil {
		t.Fatal(err)
	}
	runner := &postMergeCI{}
	task := model.Task{
		ID: "42", Target: &repo,
		Grants: []model.Grant{{Capability: grainci.CapabilityName}},
	}
	err := recordPostMergeCI(ctx, store, postMergeHeadClient{
		head: &github.BranchHead{SHA: "0123456789abcdef"},
	}, runner, task, false, model.PullRequestRef{Repo: repo, Number: 7},
		github.PullRequestDetail{BaseRef: "main"}, time.Now())
	if err != nil {
		t.Fatalf("recording the post-merge tip: %v", err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("the merge started a run here rather than leaving it to the reconciler: %+v",
			runner.requests)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("no post-merge record was made for main: %+v (%v): turning publishing off is not "+
			"turning the base-branch check off, whether or not any judging jobs are named", row, err)
	}
}

// openInternalStore is a store on its own temporary database, for the
// tests here that need one rather than a whole simulated forge.
func openInternalStore(t *testing.T) *model.Store {
	t.Helper()
	store, _ := sqlitetest.Open(t)
	return store
}

// Releases off and judging jobs named: the merge is recorded and the
// branch goes on being judged (grain/task-330). What the run itself looks
// like -- a push rather than a release, untrusted, only the jobs the
// repository named -- is reconcileBaseBranchCI's, and is pinned there.
func TestMergedPullRequestWithReleasesOffStillRecordsTheTip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openInternalStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, CIMode: model.CIModeGrain,
		Work:             model.ItemOwnership(model.ReleaseItem, model.OwnershipOff),
		BaseBranchCIJobs: []string{"go", "lint"},
	}); err != nil {
		t.Fatal(err)
	}
	runner := &postMergeCI{}
	task := model.Task{ID: "42", Target: &repo}
	err := recordPostMergeCI(ctx, store, postMergeHeadClient{
		head: &github.BranchHead{SHA: "0123456789abcdef"},
	}, runner, task, false, model.PullRequestRef{Repo: repo, Number: 7},
		github.PullRequestDetail{BaseRef: "main"}, time.Now())
	if err != nil {
		t.Fatalf("recording the post-merge tip: %v", err)
	}
	row, err := store.GetBaseBranchCI(ctx, repo, "main")
	if err != nil || row == nil {
		t.Fatalf("no post-merge record was made for main: %+v (%v): turning publishing off is not "+
			"turning the base-branch check off", row, err)
	}
	if row.PendingTaskID != task.ID {
		t.Fatalf("the row names %q as the merge waiting for a verdict, want %q", row.PendingTaskID, task.ID)
	}
}

func TestHealthFromClosedStateReadsClosedRegardlessOfChecks(t *testing.T) {
	t.Parallel()
	failure := "failure"
	got := healthFrom(github.PullRequestDetail{State: "closed"}, []github.CheckRun{
		{Status: "completed", Conclusion: &failure},
	}, "", true, true)
	if got != model.PrClosed {
		t.Fatalf("got %q, want closed", got)
	}
}

func TestHealthFromClosedAndMergedReadsMerged(t *testing.T) {
	t.Parallel()
	got := healthFrom(github.PullRequestDetail{State: "closed", Merged: true}, nil, "", true, true)
	if got != model.PrMerged {
		t.Fatalf("got %q, want merged", got)
	}
}

func TestHealthFromUnknownMergeabilityWithNoFailingChecksIsUnknown(t *testing.T) {
	t.Parallel()
	got := healthFrom(github.PullRequestDetail{State: "open"}, nil, "", true, true)
	if got != model.PrUnknown {
		t.Fatalf("got %q, want unknown", got)
	}
}

func TestHealthFromNotMergeableIsConflicted(t *testing.T) {
	t.Parallel()
	no := false
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &no}, nil, "", true, true)
	if got != model.PrConflicted {
		t.Fatalf("got %q, want conflicted", got)
	}
}

func TestHealthFromAFailedCompletedCheckIsFailing(t *testing.T) {
	t.Parallel()
	yes := true
	failure := "failure"
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "build", Status: "completed", Conclusion: &failure},
	}, "", true, true)
	if got != model.PrFailing {
		t.Fatalf("got %q, want failing", got)
	}
}

// The merge gate's whole point: a check that has not finished is not a
// check that passed. Reading an in-progress run as clean is how a merge
// queue lands a change before its tests have said anything at all about
// it, so this must read pending -- an answer syncEntry neither merges on
// nor files a fix for, and one that resolves itself a cycle or two later
// when CI finishes.
func TestHealthFromAnInProgressCheckIsPending(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "build", Status: "in_progress"},
	}, "", true, true)
	if got != model.PrPending {
		t.Fatalf("got %q, want pending (an unfinished check must never read as clean)", got)
	}
}

// A queued check has not even started, which is if anything further from
// "passed" than an in-progress one -- and it is the state a check spends
// its first seconds in, exactly when a freshly opened PR is first synced.
func TestHealthFromAQueuedCheckIsPendingEvenAlongsideAPassingOne(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("success")},
		{Name: "tests", Status: "queued"},
	}, "", true, true)
	if got != model.PrPending {
		t.Fatalf("got %q, want pending: one green check says nothing about the one still queued", got)
	}
}

// A known failure outranks checks that are still running. The verdict can
// no longer become clean, so the merge queue can start its repair while
// the remaining CI work finishes.
func TestHealthFromAFailedCheckAlongsideARunningOneIsFailing(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "tests", Status: "in_progress"},
	}, "", true, true)
	if got != model.PrFailing {
		t.Fatalf("got %q, want failing as soon as one check has failed", got)
	}
}

// A conflict is read off the PR itself and needs no CI at all, so it
// still answers straight away -- a PR that cannot merge into its base
// will not start merging because its tests eventually go green.
func TestHealthFromConflictedBeatsPending(t *testing.T) {
	t.Parallel()
	no := false
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &no}, []github.CheckRun{
		{Name: "tests", Status: "in_progress"},
	}, "", true, true)
	if got != model.PrConflicted {
		t.Fatalf("got %q, want conflicted", got)
	}
}

// A check that ran out of time, or a workflow too broken to start at all
// (the Actions fallback's own conclusion for that), is a check that ran
// and did not pass -- the same thing a fix task exists to repair as an
// outright "failure".
func TestHealthFromATimedOutOrUnstartableCheckIsFailing(t *testing.T) {
	t.Parallel()
	for _, conclusion := range []string{"failure", "timed_out", "startup_failure"} {
		t.Run(conclusion, func(t *testing.T) {
			yes := true
			got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
				{Name: "tests", Status: "completed", Conclusion: strPtr(conclusion)},
			}, "", true, true)
			if got != model.PrFailing {
				t.Fatalf("got %q, want failing", got)
			}
		})
	}
}

// The passing conclusions. "neutral" and "skipped" are not failures, and
// reading them as failures would have the queue file a fix task against a
// job the workflow deliberately did not run. "cancelled", "stale" and
// "action_required" are not in this list -- none is a failure either, but
// none is a verdict at all; see below.
func TestHealthFromThePassingConclusionsStayClean(t *testing.T) {
	t.Parallel()
	for _, conclusion := range []string{"success", "neutral", "skipped"} {
		t.Run(conclusion, func(t *testing.T) {
			yes := true
			got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
				{Name: "tests", Status: "completed", Conclusion: strPtr(conclusion)},
			}, "", true, true)
			if got != model.PrClean {
				t.Fatalf("got %q, want clean", got)
			}
		})
	}
}

// A cancelled check is the third answer: not a pass, because the tree it
// was asked about was never judged, and not a failure, because nothing
// about it is broken and no agent has anything to repair. PENDING is
// what "no verdict yet" already means everywhere else in healthFrom, and
// it is what the queue waits on -- bounded, like every other PENDING, by
// defaultCheckStallDeadline, past which a person is told rather than the
// pull request merged. Decided 2026-09-08; healthFrom's own doc comment
// carries the argument.
func TestHealthFromACancelledCheckIsPending(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "workflows", Status: "completed", Conclusion: strPtr("success")},
		{Name: "go", Status: "completed", Conclusion: strPtr("cancelled")},
	}, "", true, true)
	if got != model.PrPending {
		t.Fatalf("got %q, want pending", got)
	}
}

// The completed runs that delivered no verdict read PENDING beside a
// passing check, not CLEAN: a workflow run waiting on a maintainer's
// approval ("action_required"), a check GitHub gave up on after fourteen
// days ("stale"), and a completed run with no conclusion at all. Each used
// to fall through as a pass, which let a pull request whose only CI was
// waiting for approval merge untested. None of them is a failure either:
// no agent can approve a workflow run.
func TestHealthFromTheNoVerdictConclusionsArePending(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		conclusion *string
	}{
		{"action_required", strPtr("action_required")},
		{"stale", strPtr("stale")},
		{"cancelled", strPtr("cancelled")},
		{"no conclusion", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			yes := true
			got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
				{Name: "workflows", Status: "completed", Conclusion: strPtr("success")},
				{Name: "go", Status: "completed", Conclusion: tc.conclusion},
			}, "", true, true)
			if got != model.PrPending {
				t.Fatalf("got %q, want pending", got)
			}
		})
	}
}

// A known failure still beats a run waiting on approval: the repair can
// start while the approval is outstanding.
func TestHealthFromAFailingCheckBeatsOneAwaitingApproval(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "deploy", Status: "completed", Conclusion: strPtr("action_required")},
	}, "", true, true)
	if got != model.PrFailing {
		t.Fatalf("got %q, want failing", got)
	}
}

// A fail-fast matrix takes its own siblings down when one job goes red, so
// "one failure beside several cancellations" is an ordinary GitHub
// Actions shape rather than an exotic one. The known failure starts a
// repair on the cycle it is read, not two hours later by a person.
func TestHealthFromAFailingCheckBeatsACancelledOne(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "go (1.24)", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "go (1.25)", Status: "completed", Conclusion: strPtr("cancelled")},
	}, "", true, true)
	if got != model.PrFailing {
		t.Fatalf("got %q, want failing", got)
	}
}

// With no known failure, a check still running keeps the verdict pending:
// neither a cancellation nor unfinished work is evidence that the branch
// needs repair.
func TestHealthFromAnUnfinishedCheckStillBeatsACancelledOne(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "go", Status: "completed", Conclusion: strPtr("cancelled")},
		{Name: "frontend", Status: "in_progress"},
	}, "", true, true)
	if got != model.PrPending {
		t.Fatalf("got %q, want pending", got)
	}
}

// The fix task's body is the whole brief an agent gets, so it names the
// jobs that went red rather than leaving it to go and find them.
func TestHealthReasonNamesTheFailingChecks(t *testing.T) {
	t.Parallel()
	checks := []github.CheckRun{
		{Name: "lint", Status: "completed", Conclusion: strPtr("success")},
		{Name: "unit tests", Status: "completed", Conclusion: strPtr("failure")},
		{Name: "e2e", Status: "completed", Conclusion: strPtr("timed_out")},
	}
	got := healthReason(model.PrFailing, github.PullRequestDetail{BaseRef: "main"}, checks, grainCIVerdict{})
	for _, want := range []string{"unit tests", "e2e"} {
		if !strings.Contains(got, want) {
			t.Errorf("healthReason = %q, want it to name %q", got, want)
		}
	}
	if strings.Contains(got, "lint") {
		t.Errorf("healthReason = %q, want it to leave the check that passed out", got)
	}
}

// The general sentence stays as the floor. Nothing reaches it today --
// syncEntry passes healthReason the very checks health was computed from
// -- but a fix task whose body said only "its checks are failing ()"
// would read as a bug to whoever picked it up, so the empty list keeps
// the sentence it had before check names went in.
func TestHealthReasonStillExplainsFailingWithNoNamedChecks(t *testing.T) {
	t.Parallel()
	got := healthReason(model.PrFailing, github.PullRequestDetail{BaseRef: "main"}, nil, grainCIVerdict{})
	if !strings.Contains(got, "checks") {
		t.Fatalf("healthReason = %q, want it to still say the checks are failing", got)
	}
}

func TestHealthFromMergeableWithNoFailingChecksIsClean(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "build", Status: "completed", Conclusion: strPtr("success")},
	}, "", true, true)
	if got != model.PrClean {
		t.Fatalf("got %q, want clean", got)
	}
}

func strPtr(s string) *string { return &s }

// --- an empty check list -----------------------------------------------

// The race this closes. A pull request opened the instant its branch
// landed is read before GitHub has created the workflow run's check runs,
// so the Checks API answers with nothing at all -- and nothing at all is
// also exactly what a repo with no CI answers. Believing it straight away
// merges the change before CI has said a word about it, which is the same
// failure an unfinished check already covers, one step earlier.
func TestHealthFromAnEmptyCheckListIsPendingUntilItSettles(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, nil, "", true, false)
	if got != model.PrPending {
		t.Fatalf("got %q, want pending: no checks reported *yet* is not the same as no checks", got)
	}
}

// And the other side of it, which is why this cannot simply block on an
// empty list forever: a repo with no CI configured is a real, supported
// deployment, and its pull requests must still merge. Once the head
// commit has sat there long enough for CI to have shown up, the empty
// list means what it says.
func TestHealthFromASettledEmptyCheckListIsClean(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, nil, "", true, true)
	if got != model.PrClean {
		t.Fatalf("got %q, want clean: a repo with no CI at all must still be mergeable", got)
	}
}

// checksSettled is only ever consulted about an empty list. A check that
// has reported answers for itself, and letting an unelapsed window hold a
// finished green check back would delay every merge by it.
func TestHealthFromIgnoresTheSettleWindowOnceAnyCheckExists(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, []github.CheckRun{
		{Name: "build", Status: "completed", Conclusion: strPtr("success")},
	}, "", true, false)
	if got != model.PrClean {
		t.Fatalf("got %q, want clean: a reported check needs no window to wait out", got)
	}
}

func TestEmptyChecksSettledWaitsOutTheWindow(t *testing.T) {
	defer SetCheckRegistrationWindow(2 * time.Minute)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if emptyChecksSettled(ref, "sha1", start) {
		t.Fatal("the first read of an empty check list settled it immediately")
	}
	if emptyChecksSettled(ref, "sha1", start.Add(90*time.Second)) {
		t.Error("settled inside the window")
	}
	if !emptyChecksSettled(ref, "sha1", start.Add(2*time.Minute)) {
		t.Error("still unsettled once the window had elapsed -- a CI-less repo would never merge")
	}
}

// A push to a branch that already has a pull request open -- a fix task
// merging into it, a human pushing by hand -- empties the check list
// again and starts CI again. Inheriting the previous commit's settled
// verdict would merge the new commit against the old one's silence.
func TestEmptyChecksSettledRestartsTheWindowOnANewHeadCommit(t *testing.T) {
	defer SetCheckRegistrationWindow(2 * time.Minute)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	emptyChecksSettled(ref, "sha1", start)
	if !emptyChecksSettled(ref, "sha1", start.Add(3*time.Minute)) {
		t.Fatal("the first commit never settled")
	}
	if emptyChecksSettled(ref, "sha2", start.Add(3*time.Minute)) {
		t.Error("a freshly pushed commit inherited the previous commit's settled verdict")
	}
	if !emptyChecksSettled(ref, "sha2", start.Add(5*time.Minute)) {
		t.Error("the new commit's own window never elapsed")
	}
}

// Forgetting a closed pull request is only housekeeping -- it is what
// keeps the sighting map from growing for the life of the process -- but
// it has to forget the whole sighting rather than half of it. Starting
// the window again is the safe direction, and the one this pins.
func TestForgetEmptyChecksStartsTheWindowAgain(t *testing.T) {
	defer SetCheckRegistrationWindow(2 * time.Minute)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	emptyChecksSettled(ref, "sha1", start)
	forgetEmptyChecks(ref)
	if emptyChecksSettled(ref, "sha1", start.Add(3*time.Minute)) {
		t.Error("a forgotten pull request kept its old sighting")
	}
}

// GitHub fills the head sha in asynchronously, like Mergeable. A read
// without one has not told us which commit the empty list belongs to, so
// there is no commit whose CI can be concluded absent.
func TestEmptyChecksSettledNeverSettlesWithoutAHeadSHA(t *testing.T) {
	defer SetCheckRegistrationWindow(2 * time.Minute)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if emptyChecksSettled(ref, "", start) {
		t.Error("settled a pull request GitHub had not named a head commit for")
	}
	if emptyChecksSettled(ref, "", start.Add(time.Hour)) {
		t.Error("waiting does not turn a missing head commit into a settled one")
	}
}

// Zero means off, all the way off: the tests in tests/e2e and cmd/grain
// drive a simulated GitHub with no CI in it at all, and every one of them
// would otherwise wait out a window in real wall-clock time for a check
// run that is never coming.
func TestSetCheckRegistrationWindowZeroSettlesImmediately(t *testing.T) {
	defer SetCheckRegistrationWindow(0)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if !emptyChecksSettled(ref, "sha1", start) {
		t.Error("a disabled window still made the first read wait")
	}
	if !emptyChecksSettled(ref, "", start) {
		t.Error("a disabled window still withheld a pull request with no head sha")
	}
}

// The restore function is what every caller of this uses to put the
// deployment's own window back, so it has to actually restore rather than
// reset to the default.
func TestSetCheckRegistrationWindowRestores(t *testing.T) {
	restoreOuter := SetCheckRegistrationWindow(5 * time.Minute)
	defer restoreOuter()

	restoreInner := SetCheckRegistrationWindow(0)
	restoreInner()

	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if emptyChecksSettled(ref, "sha1", start) {
		t.Fatal("the window came back off rather than back to five minutes")
	}
	if emptyChecksSettled(ref, "sha1", start.Add(3*time.Minute)) {
		t.Error("settled after three minutes -- the restored window is shorter than five")
	}
	if !emptyChecksSettled(ref, "sha1", start.Add(5*time.Minute)) {
		t.Error("still unsettled after five minutes -- the restored window is longer than five")
	}
}

// --- the deadline on PENDING -------------------------------------------

// The clock the merge queue gives up on: PENDING is right for CI that
// finishes and unbounded for CI that does not, so an unfinished check is
// waited on for the deadline and no longer.
func TestChecksStalledWaitsOutTheDeadline(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if stalled, _ := checksStalled(ref, "sha1", start); stalled {
		t.Fatal("the first pending read was stalled immediately -- every pull request would escalate")
	}
	if stalled, _ := checksStalled(ref, "sha1", start.Add(119*time.Minute)); stalled {
		t.Error("stalled inside the deadline, on CI that is merely slow")
	}
	stalled, deadline := checksStalled(ref, "sha1", start.Add(2*time.Hour))
	if !stalled {
		t.Error("never stalled -- an unfinished check would hold its queue head forever")
	}
	if deadline != 2*time.Hour {
		t.Errorf("deadline = %s, want the one in effect: it is what the comment tells the task", deadline)
	}
}

// A push restarts CI, so it restarts the clock: the new commit's checks
// have not been unfinished for the old commit's however-long.
func TestChecksStalledRestartsTheClockOnANewHeadCommit(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	checksStalled(ref, "sha1", start)
	if stalled, _ := checksStalled(ref, "sha2", start.Add(3*time.Hour)); stalled {
		t.Error("a freshly pushed commit inherited the previous commit's elapsed deadline")
	}
	if stalled, _ := checksStalled(ref, "sha2", start.Add(5*time.Hour)); !stalled {
		t.Error("the new commit's own deadline never elapsed")
	}
}

// Forgetting is what makes the clock measure one unbroken run of pending
// reads rather than the span between two of them: syncEntry drops the
// sighting on every cycle that reads anything else, so a check re-run
// hours later is timed from the re-run.
func TestForgetPendingChecksStartsTheClockAgain(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	checksStalled(ref, "sha1", start)
	forgetPendingChecks(ref)
	if stalled, _ := checksStalled(ref, "sha1", start.Add(3*time.Hour)); stalled {
		t.Error("a re-run was timed from the commit's first ever pending read")
	}
}

// Unlike the registration window, a missing head sha is timed rather than
// excused. There the sha is the thing being reasoned about; here the pull
// request is stuck whatever GitHub is calling its head, and a sha that
// never arrives is one more way for PENDING to last forever.
func TestChecksStalledTimesAPullRequestWithNoHeadSHA(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	if stalled, _ := checksStalled(ref, "", start); stalled {
		t.Fatal("stalled on the first read")
	}
	if stalled, _ := checksStalled(ref, "", start.Add(2*time.Hour)); !stalled {
		t.Error("a pull request with no head sha waited forever")
	}
}

// Zero is off, and off here means the opposite direction to off on the
// registration window: never give up, which is what this package did
// before the deadline existed.
func TestSetCheckStallDeadlineZeroNeverGivesUp(t *testing.T) {
	defer SetCheckStallDeadline(0)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	checksStalled(ref, "sha1", start)
	if stalled, _ := checksStalled(ref, "sha1", start.Add(30*24*time.Hour)); stalled {
		t.Error("a disabled deadline still gave up, a month later")
	}
}

// The two clocks are separate, and setting one must not disturb the
// other: tests/e2e and cmd/grain switch the registration window off for
// every one of their runs.
func TestSetCheckRegistrationWindowLeavesTheStallDeadlineAlone(t *testing.T) {
	defer SetCheckStallDeadline(2 * time.Hour)()
	defer SetCheckRegistrationWindow(0)()
	ref := testPullRequestRef()
	start := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	checksStalled(ref, "sha1", start)
	if stalled, _ := checksStalled(ref, "sha1", start.Add(2*time.Hour)); !stalled {
		t.Error("switching the registration window off switched the stall deadline off too")
	}
}

// The deadline goes into a comment a person reads, and Duration's own
// String spells two hours "2h0m0s".
func TestHumanDuration(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{2 * time.Hour, "2h"},
		{90 * time.Minute, "1h30m"},
		{45 * time.Minute, "45m"},
		{30 * time.Second, "30s"},
		{2*time.Hour + 30*time.Second, "2h0m30s"},
	} {
		if got := humanDuration(tc.in); got != tc.want {
			t.Errorf("humanDuration(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// --- closing a PR whose head branch is already gone --------------------

// A merged PR's head branch is routinely deleted by the time this runs
// (GitHub's own "automatically delete head branches" setting, or a human
// tidying up after closing without merging), which leaves ListCheckRuns
// unable to resolve it. syncEntry must still close the task out: it does
// not need checks to know a closed PR is done, so it must never even ask
// for them once detail.State already says "closed" -- see the comment in
// syncEntry itself for why asking anyway used to leave the task never
// closing, cycle after cycle.
func TestSyncEntryClosesAPullRequestEvenWhenItsHeadBranchIsGone(t *testing.T) {
	t.Parallel()
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	completedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "fix the thing", Body: "please",
		Origin:   model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval: &model.Attribution{Actor: human},
		Target:   &repo,
		Binding:  model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing task: %v", err)
	}
	obs := model.Observation{TaskID: task.ID, CompletedAt: &completedAt}
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatalf("recording completion: %v", err)
	}

	entry := queueEntry{
		task: task, obs: &obs,
		ref: model.PullRequestRef{Repo: repo, Number: 7},
	}
	client := &deletedBranchClient{}

	if err := syncOneEntry(ctx, store, client, entry, map[string]string{}, completedAt); err != nil {
		t.Fatalf("syncEntry: %v", err)
	}
	if client.checkRunsCalled {
		t.Error("syncEntry read check runs for a PR already known to be closed")
	}

	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateClosed {
		t.Fatalf("state = %q, want closed", st)
	}
}

// deletedBranchClient stands in for a real GitHub credential once a PR's
// head branch is gone: GetPullRequest still answers fine (a closed PR
// still exists), but any ref-scoped read like ListCheckRuns 404s because
// there is no longer a branch or commit that name resolves to.
type deletedBranchClient struct {
	github.Client
	checkRunsCalled bool
}

func (c *deletedBranchClient) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	return github.PullRequestDetail{State: "closed", HeadRef: "grain/t1", BaseRef: "main"}, nil
}

func (c *deletedBranchClient) ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error) {
	c.checkRunsCalled = true
	return nil, &github.Error{Status: 404, Body: []byte("Not Found")}
}

// A push landing between the pull-request read and the check read is a
// single instant between two sequential HTTP calls, and it used to be
// enough to merge a commit nothing had run tests against: the checks came
// back for the branch (so, for the new commit, which CI had not
// registered for yet -- an empty list), while the window that decides
// whether an empty list means "no CI here" was keyed on the *old* commit,
// whose window had long since elapsed. Empty plus settled reads clean,
// and clean at the head of a queue merges.
//
// Scoping the check read to the commit whose health is being decided
// closes it: the checks, the verdict and the sighting all describe one
// commit, and the commit that has just landed gets judged on its own CI
// next cycle.
func TestSyncEntryDoesNotMergeOnChecksReadForADifferentCommit(t *testing.T) {
	restoreWindow := SetCheckRegistrationWindow(2 * time.Minute)
	defer restoreWindow()

	store, ctx, entry, heads := autoMergeQueueHead(t)
	completedAt := *entry.obs.CompletedAt
	client := &racingPushClient{tip: "aaaaaaa", checks: map[string][]github.CheckRun{}}

	// A first cycle, with nothing reported against aaaaaaa yet: that
	// starts its registration window, which is the sighting the bug then
	// went on to read as an answer about another commit entirely.
	if err := syncOneEntry(ctx, store, client, entry, heads, completedAt); err != nil {
		t.Fatalf("syncEntry: %v", err)
	}
	if len(client.merges) != 0 {
		t.Fatalf("merged before any CI had reported: %v", client.merges)
	}

	// CI registers for aaaaaaa and is still running. Meanwhile a push
	// lands the instant the next cycle has read the pull request -- so
	// this cycle's detail still names aaaaaaa, and the head branch
	// already points at bbbbbbb, which no CI has reported for.
	client.checks["aaaaaaa"] = []github.CheckRun{{Name: "ci", Status: "in_progress"}}
	client.pushOnRead = "bbbbbbb"

	if err := syncOneEntry(ctx, store, client, entry, heads, completedAt.Add(3*time.Minute)); err != nil {
		t.Fatalf("syncEntry: %v", err)
	}
	if len(client.merges) != 0 {
		t.Fatalf("merged %v with aaaaaaa's checks still running", client.merges)
	}
	if client.merged {
		t.Fatal("merged a commit whose checks this cycle never read")
	}

	// The task is left exactly where it was -- still its repo's queue
	// head, still open, waiting to be judged again next cycle.
	st, err := store.State(ctx, entry.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 'awaiting_ack' is the post-run state of a task whose run left no
	// pull request of its own (model.AwaitsAck); what this asserts is
	// that nothing here moved it, not which of the post-run states it is
	// sitting in.
	if st != model.StateAwaitingReply {
		t.Fatalf("state = %q, want it left where it was", st)
	}
}

// The registration window times an *empty* check list, so a cycle that
// reads a full one has nothing left to time and drops the sighting --
// exactly what forgetPendingChecks does for the other clock. Without
// that, a commit's window could go on elapsing underneath check runs
// that had long since registered, and be spent on some later emptiness
// it was never started for.
func TestSyncEntryForgetsTheEmptyChecksSightingOnceChecksRegister(t *testing.T) {
	restoreWindow := SetCheckRegistrationWindow(2 * time.Minute)
	defer restoreWindow()

	store, ctx, entry, heads := autoMergeQueueHead(t)
	completedAt := *entry.obs.CompletedAt
	client := &racingPushClient{tip: "aaaaaaa", checks: map[string][]github.CheckRun{}}

	// Nothing has reported yet: aaaaaaa's window starts here.
	if err := syncOneEntry(ctx, store, client, entry, heads, completedAt); err != nil {
		t.Fatalf("syncEntry: %v", err)
	}
	// CI registers, so there is no longer an empty list to be timing.
	client.checks["aaaaaaa"] = []github.CheckRun{{Name: "ci", Status: "in_progress"}}
	if err := syncOneEntry(ctx, store, client, entry, heads, completedAt.Add(time.Minute)); err != nil {
		t.Fatalf("syncEntry: %v", err)
	}
	// And the list goes empty again on the same commit -- a check run
	// deleted, a provider withdrawing one. That is a fresh emptiness,
	// timed from now, not one two minutes old.
	delete(client.checks, "aaaaaaa")
	if err := syncOneEntry(ctx, store, client, entry, heads, completedAt.Add(3*time.Minute)); err != nil {
		t.Fatalf("syncEntry: %v", err)
	}
	if len(client.merges) != 0 {
		t.Fatalf("merged %v on a window that had been running under registered checks", client.merges)
	}
}

// syncOneEntry is syncEntry for a test with one entry in it: a cycle of
// its own, so the entry starts with its repo's merge slot open. heads is
// who the walk already has at the front of each lane (mergeWalk.heads),
// a bare repository name standing for every lane in it.
func syncOneEntry(ctx context.Context, store *model.Store, client github.Client,
	e queueEntry, heads map[string]string, now time.Time) error {

	return syncEntry(ctx, store, client, e, StateRepoGate{}, &mergeWalk{heads: maps.Clone(heads)},
		newRepoFinder(client), now)
}

// autoMergeQueueHead is the store, task and queue entry a syncEntry test
// needs to be exercising the merge arm at all: one completed task with
// /auto-merge on, at the head of its repo's queue, its pull request open.
func autoMergeQueueHead(t *testing.T) (*model.Store, context.Context, queueEntry, map[string]string) {
	t.Helper()
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()

	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	completedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	task := model.Task{
		ID: "t1", Intent: model.IntentImplement, Title: "fix the thing", Body: "please",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
		Approval:  &model.Attribution{Actor: human},
		Target:    &repo,
		Binding:   model.BindingDirective,
		AutoMerge: true,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing task: %v", err)
	}
	obs := model.Observation{TaskID: task.ID, CompletedAt: &completedAt}
	if err := store.Observe(ctx, obs); err != nil {
		t.Fatalf("recording completion: %v", err)
	}

	entry := queueEntry{task: task, obs: &obs, ref: model.PullRequestRef{Repo: repo, Number: 7}}
	return store, ctx, entry, map[string]string{repo.String(): task.ID}
}

// The queue giving up takes the stall flag off the row with it
// (Observation.MergeQueueStalledAt). merge_failed is the one state with
// no patience at all -- it is meant to stand still -- so a row that kept
// the flag would carry a "Stalled" chip beside a line whose whole message
// is that a person owns this now: two alarms for one fact, and the second
// one wrong.
//
// blockMergeQueue is called from the four escalations rather than through
// setMergeQueueState, so it clears the flag itself; this is what pins
// that, since every path into it starts from a state that could have
// been flagged.
func TestGivingUpTakesTheStallFlagOffTheRow(t *testing.T) {
	t.Parallel()
	store, ctx, entry, _ := autoMergeQueueHead(t)
	entered := time.Date(2026, 8, 27, 9, 0, 0, 0, time.UTC)
	stalled := entered.Add(2 * time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID:           entry.task.ID,
		MergeQueueState:  mergeStateWaitingCI,
		MergeQueueReason: mergeReasonCIRunning,
		MergeQueueNote: "waiting on `go` -- and this has stood here for more than 2h15m, " +
			"so read it as stuck rather than as waiting",
		MergeQueueStateAt: &entered, MergeQueueStalledAt: &stalled,
	}); err != nil {
		t.Fatalf("recording a flagged wait: %v", err)
	}

	now := stalled.Add(time.Hour)
	if err := blockMergeQueue(ctx, store, entry.task.ID,
		"nothing ever reported on this commit", "no verdict ever came", now); err != nil {
		t.Fatalf("blockMergeQueue: %v", err)
	}

	obs, err := store.GetObservation(ctx, entry.task.ID)
	if err != nil || obs == nil {
		t.Fatalf("get observation: (%+v, %v)", obs, err)
	}
	if obs.MergeQueueState != mergeStateFailed {
		t.Fatalf("state = %q, want %q", obs.MergeQueueState, mergeStateFailed)
	}
	if obs.MergeQueueReason != mergeReasonGaveUp {
		t.Errorf("reason = %q, want %q: giving up replaces the wait's sub-case with merge_failed's",
			obs.MergeQueueReason, mergeReasonGaveUp)
	}
	if obs.MergeQueueStalledAt != nil {
		t.Errorf("stalled stamp = %v, want it cleared: merge_failed standing still is it working",
			obs.MergeQueueStalledAt)
	}
	// And the state's own clock restarts, since this is a state the entry
	// has just entered rather than the wait it was in going on.
	if obs.MergeQueueStateAt == nil || !obs.MergeQueueStateAt.Equal(now) {
		t.Errorf("state stamped %v, want %v", obs.MergeQueueStateAt, now)
	}
}

// racingPushClient is GitHub with a push landing mid-cycle: the tip moves
// the instant the pull request has been read, which is the gap between
// syncEntry's own GetPullRequest and everything it does next.
//
// Its check runs are keyed by commit, as GitHub's own are -- the point of
// the test being that reading them for the wrong commit is what merged
// untested code -- and its merge honours the head sha the caller pins,
// answering 409 for a branch that has moved.
type racingPushClient struct {
	github.Client
	tip        string
	pushOnRead string
	checks     map[string][]github.CheckRun
	merges     []string
	merged     bool
}

func (c *racingPushClient) GetPullRequest(owner, repo string, number int) (github.PullRequestDetail, error) {
	mergeable := true
	detail := github.PullRequestDetail{
		State: "open", HeadRef: "grain/t1", BaseRef: "main",
		HeadSHA: c.tip, Mergeable: &mergeable,
	}
	if c.merged {
		detail.State, detail.Merged = "closed", true
	}
	if c.pushOnRead != "" {
		c.tip, c.pushOnRead = c.pushOnRead, ""
	}
	return detail, nil
}

func (c *racingPushClient) ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error) {
	return c.checks[ref], nil
}

func (c *racingPushClient) MergePullRequest(owner, repo string, number int, headSHA string) error {
	c.merges = append(c.merges, headSHA)
	if headSHA != "" && headSHA != c.tip {
		return &github.Error{Status: 409, Body: []byte("Head branch was modified")}
	}
	c.merged = true
	return nil
}

// --- checks the credential cannot read ----------------------------------

// The dangerous reading of an unreadable Checks API is "no checks came
// back, so nothing is failing" -- identical, at this function, to a
// genuinely green PR. A deployment on a scoped PAT gets that answer for
// every PR forever, so reading it as clean would auto-merge PRs with CI
// red.
func TestHealthFromUnknownChecksIsNeverClean(t *testing.T) {
	t.Parallel()
	yes := true
	got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &yes}, nil, "", false, true)
	if got == model.PrClean {
		t.Fatal("unreadable check runs read as clean: a PR with failing CI would be auto-merged")
	}
	if got != model.PrUnknown {
		t.Fatalf("got %q, want unknown", got)
	}
}

// Both facts healthFrom reads straight off the PR stay authoritative
// without checks: neither needs the Checks API, and a deployment that
// cannot reach it must still close out merged PRs and notice conflicts.
func TestHealthFromClosedAndConflictedSurviveUnknownChecks(t *testing.T) {
	t.Parallel()
	if got := healthFrom(github.PullRequestDetail{State: "closed"}, nil, "", false, true); got != model.PrClosed {
		t.Errorf("closed PR with unreadable checks = %q, want closed", got)
	}
	no := false
	if got := healthFrom(github.PullRequestDetail{State: "open", Mergeable: &no}, nil, "", false, true); got != model.PrConflicted {
		t.Errorf("conflicted PR with unreadable checks = %q, want conflicted", got)
	}
}

func TestCheckRunsForReportsAForbiddenReadAsUnknownNotAnError(t *testing.T) {
	t.Parallel()
	// Every test in this group claims the log for itself: checkRunsFor
	// warns, once per process, about a credential that cannot read CI,
	// and which of these tests is the one to reach that warning first is
	// nobody's business to depend on. See log_test.go.
	logToTest(t)
	client := &checkRunsClient{err: &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)}, workflowErr: &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)}}
	checks, known, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef")
	if err != nil {
		t.Fatalf("a 403 must not fail the sync: %v", err)
	}
	if known {
		t.Error("checks reported known after a 403")
	}
	if checks != nil {
		t.Errorf("checks = %v, want nil", checks)
	}
}

// ChecksUnavailable is what lets pkg/ui warn an operator that Submit will
// never actually merge anything on this deployment (bwsalmon/agents#483)
// -- it has to flip alongside the log line checkRunsFor already prints on
// a 403, or that warning would never appear either.
func TestChecksUnavailableReflectsAForbiddenRead(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{err: &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)}, workflowErr: &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)}}
	if _, _, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef"); err != nil {
		t.Fatalf("a 403 must not fail the sync: %v", err)
	}
	if !ChecksUnavailable() {
		t.Error("ChecksUnavailable() = false after both CI reads 403'd, want true")
	}
}

// Only the one permission a deployment can be configured without is
// tolerated. Anything else -- a 404, a 500, a transport failure -- is
// still a real error, and swallowing it would hide a broken deployment
// behind the same silent "unknown".
//
// The 403s below are the reason IsPermissionDenied reads the body rather
// than the status. Each one clears -- on its own, or with a change the
// operator can make -- but checksUnavailable never clears within a
// process, so classifying one as a missing permission would switch
// auto-merge off until the next restart over a condition that had
// already resolved. A propagated error costs one retried cycle instead.
func TestCheckRunsForStillFailsOnEveryOtherError(t *testing.T) {
	t.Parallel()
	logToTest(t)
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"not found", &github.Error{Status: 404}},
		{"server error", &github.Error{Status: 500}},
		{"transport", errors.New("dial tcp: connection refused")},
		{
			"a rate-limited 403",
			&github.Error{Status: 403, Body: []byte(`{"message":"API rate limit exceeded for user ID 1."}`)},
		},
		{
			"a secondary rate limit's 403",
			&github.Error{Status: 403, Body: []byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`)},
		},
		{
			"a SAML-enforcement 403",
			&github.Error{Status: 403, Body: []byte(`{"message":"Resource protected by organization SAML enforcement. You must grant your Personal Access token access to this organization."}`)},
		},
		{
			"an IP-allow-list 403",
			&github.Error{Status: 403, Body: []byte(`{"message":"Although you appear to have the correct authorization credentials, the organization has an IP allow list enabled, and your IP address is not permitted to access this resource."}`)},
		},
		{"a 403 with a body that cannot be read", &github.Error{Status: 403, Body: []byte("<html>403 Forbidden</html>")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &checkRunsClient{err: tc.err, workflowErr: tc.err}
			if _, _, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef"); err == nil {
				t.Fatal("expected the error to propagate")
			}
		})
	}
}

// The whole point of the fallback: a fine-grained PAT cannot be granted
// the Checks permission at all (GitHub withdrew it and has said only
// GitHub Apps may use that API), so without this a deployment on one has
// no CI signal and auto-merge never fires. With "Actions" read it does.
func TestCheckRunsForFallsBackToWorkflowRunsWhenChecksAreForbidden(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{
		err:          &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)},
		workflowRuns: []github.CheckRun{{Name: "tests", Status: "completed"}},
	}
	checks, known, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef")
	if err != nil {
		t.Fatalf("the fallback must not fail the sync: %v", err)
	}
	if !known {
		t.Fatal("a successful workflow-run read must report the checks as known")
	}
	if len(checks) != 1 || checks[0].Name != "tests" {
		t.Fatalf("checks = %v, want the one workflow run", checks)
	}
}

// The fallback reads the commit the PR points at, not its branch. A
// branch-scoped read could return a run of an older commit, and reading
// that as this commit's pass is what would auto-merge untested code.
func TestCheckRunsForScopesTheFallbackToTheHeadSHA(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{err: &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)}}
	if _, _, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if client.workflowSHASaw != "deadbeef" {
		t.Errorf("the fallback read %q, want the head sha deadbeef", client.workflowSHASaw)
	}
}

// GitHub computes the head sha asynchronously the same way it computes
// mergeable, so a PR read can legitimately arrive without one. With no
// commit to scope to, the fallback is skipped rather than widened to a
// branch-scoped read -- health goes unknown for a cycle instead.
func TestCheckRunsForSkipsTheFallbackWithNoHeadSHA(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{
		err:          &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)},
		workflowRuns: []github.CheckRun{{Name: "tests", Status: "completed"}},
	}
	_, known, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "")
	if err != nil {
		t.Fatal(err)
	}
	if known {
		t.Error("checks reported known from a fallback that had no commit to scope to")
	}
	if client.workflowSHASaw != "" {
		t.Errorf("the fallback ran anyway, against %q", client.workflowSHASaw)
	}
}

// The fallback gets the same treatment as the read it stands in for: a
// permission error is a fact about the credential, anything else is a
// real error. A 500 from the Actions API must not read as "no CI".
func TestCheckRunsForPropagatesANonPermissionFallbackFailure(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{
		err:         &github.Error{Status: 403, Body: []byte(`{"message":"Resource not accessible by personal access token"}`)},
		workflowErr: &github.Error{Status: 500, Body: []byte("upstream is unwell")},
	}
	if _, _, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef"); err == nil {
		t.Fatal("a 500 from the fallback must propagate, not report unknown")
	}
}

// A credential that can read checks directly must never pay for the
// fallback -- the Checks API sees every provider's checks, the Actions
// API only sees Actions, so preferring it would narrow what CI grain can
// see on a deployment that had no problem to begin with.
func TestCheckRunsForDoesNotReachForTheFallbackWhenChecksWork(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{
		checks:       []github.CheckRun{{Name: "buildkite", Status: "completed"}},
		workflowRuns: []github.CheckRun{{Name: "tests", Status: "completed"}},
	}
	checks, known, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef")
	if err != nil || !known {
		t.Fatalf("known=%v err=%v", known, err)
	}
	if len(checks) != 1 || checks[0].Name != "buildkite" {
		t.Fatalf("checks = %v, want the Checks API's own answer", checks)
	}
	if client.workflowSHASaw != "" {
		t.Error("the fallback ran even though the Checks read succeeded")
	}
}

// The Checks read is scoped to the commit for the same reason the
// Actions fallback already is. The Checks API takes a branch name
// happily, and answers for whatever that branch points at now -- so a
// push landing between the caller's GetPullRequest and this read would
// hand back one commit's checks to a caller deciding another commit's
// health.
func TestCheckRunsForAsksForTheHeadSHANotTheBranch(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{checks: []github.CheckRun{{Name: "build", Status: "completed"}}}
	if _, _, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if client.checksRefSaw != "deadbeef" {
		t.Errorf("the Checks read asked for %q, want the head sha deadbeef", client.checksRefSaw)
	}
}

// The one case with no commit to name: GitHub fills the head sha in
// asynchronously, so a PR read can arrive without one. A branch-scoped
// read beats no CI signal at all there, and nothing downstream can
// conclude much from it anyway -- emptyChecksSettled refuses to settle an
// empty list with no head sha.
func TestCheckRunsForFallsBackToTheBranchWithNoHeadSHA(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{checks: []github.CheckRun{{Name: "build", Status: "completed"}}}
	if _, _, err := checkRunsFor(client, testPullRequestRef(), "head-branch", ""); err != nil {
		t.Fatal(err)
	}
	if client.checksRefSaw != "head-branch" {
		t.Errorf("the Checks read asked for %q, want the branch head-branch", client.checksRefSaw)
	}
}

func TestCheckRunsForPassesASuccessfulReadThrough(t *testing.T) {
	t.Parallel()
	logToTest(t)
	client := &checkRunsClient{checks: []github.CheckRun{{Name: "build", Status: "completed"}}}
	checks, known, err := checkRunsFor(client, testPullRequestRef(), "head-branch", "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if !known {
		t.Error("a successful read must report the checks as known")
	}
	if len(checks) != 1 || checks[0].Name != "build" {
		t.Errorf("checks = %v, want the one build run", checks)
	}
}

func testPullRequestRef() model.PullRequestRef {
	return model.PullRequestRef{
		Repo:   model.RepoRef{Owner: "owner", Name: "repo"},
		Number: 1,
	}
}

// checkRunsClient is a github.Client that only the two CI reads are ever
// called on -- every other method is embedded and would panic on a nil
// interface, which is the point: checkRunsFor must touch nothing else.
//
// workflowErr defaults to nothing, so a test that sets only err is
// asking for "the Checks read failed and the Actions read succeeded".
// The tests that mean "no CI signal at all" set both.
type checkRunsClient struct {
	github.Client
	checks       []github.CheckRun
	err          error
	checksRefSaw string

	workflowRuns   []github.CheckRun
	workflowErr    error
	workflowSHASaw string
}

func (c *checkRunsClient) ListCheckRuns(owner, repo, ref string) ([]github.CheckRun, error) {
	c.checksRefSaw = ref
	if c.err != nil {
		return nil, c.err
	}
	return c.checks, nil
}

func (c *checkRunsClient) ListWorkflowRuns(owner, repo, headSHA string) ([]github.CheckRun, error) {
	c.workflowSHASaw = headSHA
	if c.workflowErr != nil {
		return nil, c.workflowErr
	}
	return c.workflowRuns, nil
}

// --- what a no-action run reports --------------------------------------

func TestToolCallSummary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		calls []agent.ToolCall
		want  string
	}{
		// The most informative case: an agent that called nothing did not
		// fail to act so much as never start.
		{"no calls at all", nil, ""},
		{"one call", []agent.ToolCall{{Name: "run_command"}}, " [run_command x1]"},
		{"repeats are counted", []agent.ToolCall{
			{Name: "run_command"}, {Name: "run_command"}, {Name: "read_file"},
		}, " [read_file x1, run_command x2]"},
		// An erroring tool is a different signal from a working one --
		// four runs of read_file(error) says the sandbox is wrong, not the
		// model.
		{"errors are distinguished", []agent.ToolCall{
			{Name: "read_file"}, {Name: "read_file", IsError: true},
		}, " [read_file x1, read_file(error) x1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := toolCallSummary(&agent.Result{ToolCalls: tc.calls})
			if got != tc.want {
				t.Errorf("toolCallSummary() = %q, want %q", got, tc.want)
			}
		})
	}
}

// FinalText is model output and a sandbox can hold a .git-credentials, so
// the bound is not advisory.
func TestTruncateBoundsModelOutput(t *testing.T) {
	t.Parallel()
	if got := truncate("", 10); got != "<empty>" {
		t.Errorf("empty final text = %q, want <empty>", got)
	}
	if got := truncate("   \n ", 10); got != "<empty>" {
		t.Errorf("whitespace-only final text = %q, want <empty>", got)
	}
	if got := truncate("short", 10); got != `"short"` {
		t.Errorf("short text = %q, want it quoted whole", got)
	}
	long := strings.Repeat("x", 40)
	got := truncate(long, 10)
	if !strings.HasSuffix(got, "... (truncated)") {
		t.Errorf("long text = %q, want it marked as truncated", got)
	}
	if len(got) > 10+len(`""... (truncated)`) {
		t.Errorf("long text = %q, longer than the bound plus its marker", got)
	}
}

// --- re-checking a branch that is not there yet ------------------------

type branchClient struct {
	github.Client
	answers []bool
	err     error
	calls   int
	// repoErr, when set, is what the repository-visibility probe on the
	// negative path returns -- the "this client cannot see the repo at
	// all, so its 404s mean nothing" case.
	repoErr   error
	repoCalls int
}

func (b *branchClient) DefaultBranch(owner, repo string) (string, error) {
	b.repoCalls++
	if b.repoErr != nil {
		return "", b.repoErr
	}
	return "main", nil
}

func (b *branchClient) BranchExists(owner, repo, branch string) (bool, error) {
	b.calls++
	if b.err != nil {
		return false, b.err
	}
	if b.calls-1 < len(b.answers) {
		return b.answers[b.calls-1], nil
	}
	return false, nil
}

func withNoSleep(t *testing.T) {
	t.Helper()
	prev := branchExistsSleep
	branchExistsSleep = func(time.Duration) {}
	t.Cleanup(func() { branchExistsSleep = prev })
}

// The case this exists for: the push landed, GitHub had not caught up on
// the first read. Believing that first answer records no_action and
// abandons work already on the remote.
func TestBranchExistsSettledReChecksANegative(t *testing.T) {
	withNoSleep(t)
	c := &branchClient{answers: []bool{false, false, true}}
	exists, err := branchExistsSettled(c, "o", "r", "grain/task-1")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("gave up on a branch that appeared on the third read")
	}
	if c.calls != 3 {
		t.Errorf("calls = %d, want 3", c.calls)
	}
}

// A positive costs nothing extra: it is taken at once.
func TestBranchExistsSettledReturnsAPositiveImmediately(t *testing.T) {
	withNoSleep(t)
	c := &branchClient{answers: []bool{true}}
	if _, err := branchExistsSettled(c, "o", "r", "b"); err != nil {
		t.Fatal(err)
	}
	if c.calls != 1 {
		t.Errorf("calls = %d, want 1 -- a positive must not be re-checked", c.calls)
	}
}

// A branch that genuinely is not there still comes back false, bounded.
func TestBranchExistsSettledGivesUpBounded(t *testing.T) {
	withNoSleep(t)
	c := &branchClient{}
	exists, err := branchExistsSettled(c, "o", "r", "b")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("reported a branch that never appeared")
	}
	if c.calls != branchExistsRetries {
		t.Errorf("calls = %d, want %d", c.calls, branchExistsRetries)
	}
}

// An error is the caller's to report, not something to sit on: 404 is
// already handled inside BranchExists, so anything reaching here is a
// real failure.
func TestBranchExistsSettledDoesNotRetryAnError(t *testing.T) {
	withNoSleep(t)
	c := &branchClient{err: errors.New("500 from github")}
	if _, err := branchExistsSettled(c, "o", "r", "b"); err == nil {
		t.Fatal("expected the error to propagate")
	}
	if c.calls != 1 {
		t.Errorf("calls = %d, want 1 -- an error must not be retried", c.calls)
	}
}

// --- what the no_action detail records ---------------------------------

// outcomeOf already distinguishes "called tools and achieved nothing"
// from "made no tool calls at all", and the no_action overwrite used to
// replace both with one fixed sentence -- destroying the more useful of
// the two on its way past.
func TestNoActionDetailKeepsTheDiagnosis(t *testing.T) {
	t.Parallel()
	got := noActionDetail(&agent.Result{})
	if !strings.Contains(got, "no tool calls at all") {
		t.Errorf("detail = %q, want it to say the run made no tool calls", got)
	}

	got = noActionDetail(&agent.Result{ToolCalls: []agent.ToolCall{
		{Name: "run_command"}, {Name: "run_command"}, {Name: "read_file"},
	}})
	for _, want := range []string{"3 tool call", "run_command x2", "read_file x1"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail = %q, want it to contain %q", got, want)
		}
	}
}

// Both forms still say what did not happen -- that is the part a reader
// is looking for when a task shows no pull request.
func TestNoActionDetailStillNamesWhatIsMissing(t *testing.T) {
	t.Parallel()
	for _, r := range []*agent.Result{
		{},
		{ToolCalls: []agent.ToolCall{{Name: "run_command"}}},
	} {
		if got := noActionDetail(r); !strings.Contains(got, "without pushing a branch") {
			t.Errorf("detail = %q, want it to still name the missing ending", got)
		}
	}
}

// A negative is only believed once the repository itself reads back. A
// client that cannot see the repo 404s identically to one looking at a
// repo with no such branch, and the caller ends the task on that
// difference -- see branchExistsSettled's own comment for the live
// failure (a REST transport aimed at github.com rather than
// api.github.com) this catches.
func TestBranchExistsSettledRefusesToBelieveAnUnreadableRepo(t *testing.T) {
	withNoSleep(t)
	c := &branchClient{repoErr: &github.Error{Status: 404, Body: []byte("Not Found")}}
	exists, err := branchExistsSettled(c, "o", "r", "grain/task-1")
	if err == nil {
		t.Fatal("a branch absent from a repo the client cannot read must not come back as a plain negative")
	}
	if exists {
		t.Error("exists = true")
	}
	if !strings.Contains(err.Error(), "o/r") {
		t.Errorf("error = %v, want it to name the repo it could not read", err)
	}
}

// The probe costs one call, on the negative path only.
func TestBranchExistsSettledDoesNotProbeTheRepoOnAPositive(t *testing.T) {
	withNoSleep(t)
	c := &branchClient{answers: []bool{true}}
	if _, err := branchExistsSettled(c, "o", "r", "b"); err != nil {
		t.Fatal(err)
	}
	if c.repoCalls != 0 {
		t.Errorf("repo probes = %d, want 0 -- a branch that is there needs no confirmation the repo is", c.repoCalls)
	}
}

// --- grain CI as a merge gate (grain/task-302) ---------------------------
//
// healthFrom is judged above on GitHub's checks alone; these are the
// other half, and the whole of what grain's own CI adds to a pull
// request's health. withGrainCI's doc comment is the reasoning; each
// test below is one line of it.

func grantedTask() model.Task {
	return model.Task{ID: "t1", Grants: []model.Grant{{Capability: grainci.CapabilityName}}}
}

func ciObservation(commit, conclusion string) *model.Observation {
	return &model.Observation{CI: &model.CIStatus{RunID: "run1", Commit: commit, Conclusion: conclusion}}
}

func TestGrainCIVerdictReadsAFailureAgainstThisCommit(t *testing.T) {
	t.Parallel()
	obs := ciObservation("sha1", string(ci.ConclusionFailure))
	obs.CI.Detail = "failure: checks/test"
	got := grainCIVerdictOf(grantedTask(), obs, "sha1", false)
	if !got.Failed || got.Running {
		t.Fatalf("verdict = %+v, want a concluded failure", got)
	}
	if got.Detail != "failure: checks/test" {
		t.Errorf("detail = %q, want the verdict the task's record carries", got.Detail)
	}
}

func TestGrainCIVerdictReadsAnUnfinishedRunAsRunning(t *testing.T) {
	t.Parallel()
	got := grainCIVerdictOf(grantedTask(), ciObservation("sha1", ""), "sha1", false)
	if !got.Running || got.Failed {
		t.Fatalf("verdict = %+v, want a run still going", got)
	}
}

// A run grain could not do at all is read as its own thing, so that
// withGrainCI can hold the merge without accusing the branch of
// anything. This is the defect of grain/task-361: every job died at
// Acquire, and every one of them was reported as a failing check.
func TestGrainCIVerdictReadsARunItCouldNotDoAsErrored(t *testing.T) {
	t.Parallel()
	obs := ciObservation("sha1", string(ci.ConclusionErrored))
	obs.CI.Detail = "grain CI could not run 1 job(s) (tests/go): building a sandbox for this job: too long"
	got := grainCIVerdictOf(grantedTask(), obs, "sha1", false)
	if !got.Errored || got.Failed || got.Running {
		t.Fatalf("verdict = %+v, want an errored run and not a failed one", got)
	}
	if got.Detail != obs.CI.Detail {
		t.Errorf("detail = %q, want the verdict the task's record carries", got.Detail)
	}
}

func TestGrainCIVerdictRequiresSuccessAgainstThisCommit(t *testing.T) {
	t.Parallel()
	passed := grainCIVerdictOf(grantedTask(), ciObservation("sha1", string(ci.ConclusionSuccess)), "sha1", false)
	if !passed.Required || !passed.Passed {
		t.Fatalf("success verdict = %+v, want the required gate satisfied", passed)
	}
	for _, conclusion := range []ci.Conclusion{ci.ConclusionCancelled, ci.ConclusionSkipped} {
		got := grainCIVerdictOf(grantedTask(), ciObservation("sha1", string(conclusion)), "sha1", false)
		if !got.Required || got.Passed {
			t.Errorf("verdict for %q = %+v, want an unsatisfied required gate", conclusion, got)
		}
	}
}

func TestGrainCIVerdictRequiresAnAnswerForTheCurrentCommit(t *testing.T) {
	t.Parallel()
	failed := ciObservation("sha1", string(ci.ConclusionFailure))
	cases := []struct {
		name string
		task model.Task
		obs  *model.Observation
		head string
	}{
		{"no capability", model.Task{ID: "t1"}, failed, "sha1"},
		{"no observation", grantedTask(), nil, "sha1"},
		{"no status", grantedTask(), &model.Observation{}, "sha1"},
		{"another commit", grantedTask(), failed, "sha2"},
		{"no head sha", grantedTask(), failed, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := grainCIVerdictOf(c.task, c.obs, c.head, false)
			if c.name == "no capability" || c.name == "no head sha" {
				if got != (grainCIVerdict{}) {
					t.Fatalf("verdict = %+v, want no gate", got)
				}
			} else if !got.Required || got.Passed {
				t.Fatalf("verdict = %+v, want an unsatisfied required gate", got)
			}
		})
	}
}

func TestWithGrainCITurnsACleanPullRequestFailingOrPending(t *testing.T) {
	t.Parallel()
	if got := withGrainCI(model.PrClean, grainCIVerdict{Failed: true}); got != model.PrFailing {
		t.Errorf("got %q, want failing", got)
	}
	if got := withGrainCI(model.PrClean, grainCIVerdict{Running: true}); got != model.PrPending {
		t.Errorf("got %q, want pending", got)
	}
	if got := withGrainCI(model.PrClean, grainCIVerdict{Required: true}); got != model.PrPending {
		t.Errorf("got %q, want pending until the full suite passes", got)
	}
	if got := withGrainCI(model.PrClean, grainCIVerdict{Required: true, Passed: true}); got != model.PrClean {
		t.Errorf("got %q, want clean after the required full suite passes", got)
	}
	if got := withGrainCI(model.PrClean, grainCIVerdict{}); got != model.PrClean {
		t.Errorf("got %q, want clean: nothing was said about this commit", got)
	}
}

// A run grain could not do holds the merge -- the tree is as untested as
// it is under a run still going -- but as pending, not failing: PrFailing
// files a fix task, and there is nothing about the branch to fix. It then
// leaves by the stalled-checks door, which is the one that reaches a
// person about the deployment.
func TestWithGrainCIHoldsARunItCouldNotDoAsPendingRatherThanFailing(t *testing.T) {
	t.Parallel()
	if got := withGrainCI(model.PrClean, grainCIVerdict{Errored: true}); got != model.PrPending {
		t.Errorf("got %q, want pending", got)
	}
	phrase := stalledChecksPhrase(nil, grainCIVerdict{
		Errored: true,
		Detail:  "grain CI could not run 1 job(s) (tests/go): building a sandbox for this job: too long",
	})
	if !strings.Contains(phrase, grainCICheckName) || !strings.Contains(phrase, "tests/go") {
		t.Errorf("phrase = %q, want it to name grain CI and say what it could not run", phrase)
	}
}

// Pending outranks failing here as it does in healthFrom: a pull request
// whose GitHub checks are still running is not sent for repair on grain
// CI's answer alone, so the one repair it is entitled to is asked for
// against the whole verdict.
func TestWithGrainCILeavesEveryHealthButCleanAlone(t *testing.T) {
	t.Parallel()
	for _, health := range []model.PrHealth{
		model.PrPending, model.PrFailing, model.PrConflicted,
		model.PrUnknown, model.PrMerged, model.PrClosed,
	} {
		for _, grain := range []grainCIVerdict{{Failed: true}, {Running: true}, {Errored: true}} {
			if got := withGrainCI(health, grain); got != health {
				t.Errorf("withGrainCI(%q, %+v) = %q, want it unchanged", health, grain, got)
			}
		}
	}
}

// The repair's brief names grain CI among the failing checks, and its own
// verdict follows in a paragraph of its own -- an agent sent to fix "the
// checks" would otherwise find every GitHub check green.
func TestARepairNamesGrainCIAndItsVerdict(t *testing.T) {
	t.Parallel()
	grain := grainCIVerdict{Failed: true, Detail: "failure: checks/test"}
	reason := healthReason(model.PrFailing, github.PullRequestDetail{BaseRef: "main"}, nil, grain)
	if !strings.Contains(reason, grainCICheckName) {
		t.Errorf("reason = %q, want it to name grain CI", reason)
	}
	note := grainCINote(grain)
	if !strings.Contains(note, "checks/test") {
		t.Errorf("note = %q, want it to carry the verdict's own detail", note)
	}
	if grainCINote(grainCIVerdict{}) != "" {
		t.Error("a repair for something else carries a paragraph about grain CI")
	}
}

// And the stall escalation names it too: an unfinished grain CI run is
// one of the two things that can hold a pull request pending now, and the
// one whose usual causes are this deployment's own.
func TestAStalledGrainCIRunIsNamedInTheEscalation(t *testing.T) {
	t.Parallel()
	got := stalledChecksPhrase(nil, grainCIVerdict{Running: true})
	if !strings.Contains(got, grainCICheckName) {
		t.Errorf("phrase = %q, want it to name grain CI", got)
	}
	if got := stalledChecksPhrase(nil, grainCIVerdict{}); !strings.Contains(got, "no check") {
		t.Errorf("phrase = %q, want the no-checks-at-all sentence unchanged", got)
	}
}

// A workflow run waiting on approval reaches the stall escalation too,
// and the person it wakes is told to approve the run -- not to re-run it,
// and not to go looking for a stuck runner.
func TestTheStallEscalationNamesARunAwaitingApproval(t *testing.T) {
	t.Parallel()
	awaiting := []github.CheckRun{
		{Name: "workflows", Status: "completed", Conclusion: strPtr("success")},
		{Name: "go", Status: "completed", Conclusion: strPtr("action_required")},
	}
	phrase := stalledChecksPhrase(awaiting, grainCIVerdict{})
	if !strings.Contains(phrase, "approve") || !strings.Contains(phrase, "`go`") {
		t.Errorf("phrase = %q, want it to say `go` is waiting for approval", phrase)
	}
	if strings.Contains(phrase, "workflows") || strings.Contains(phrase, "cancelled") {
		t.Errorf("phrase = %q, want only the approval wait named", phrase)
	}
	cause := stalledChecksCause(awaiting, grainCIVerdict{})
	if !strings.Contains(cause, "approves") || strings.Contains(cause, "never finishes") ||
		strings.Contains(cause, "re-run") {
		t.Errorf("cause = %q, want the approval explanation alone", cause)
	}
	both := append(awaiting, github.CheckRun{Name: "frontend", Status: "queued"})
	if cause := stalledChecksCause(both, grainCIVerdict{}); !strings.Contains(cause, "approves") ||
		!strings.Contains(cause, "never finishes") {
		t.Errorf("cause = %q, want both explanations", cause)
	}
	// A stale check is a verdict that will never arrive, the same news as
	// a cancelled one.
	stale := []github.CheckRun{{Name: "go", Status: "completed", Conclusion: strPtr("stale")}}
	if phrase := stalledChecksPhrase(stale, grainCIVerdict{}); !strings.Contains(phrase, "`go`") {
		t.Errorf("phrase = %q, want it to name the stale check", phrase)
	}
}

// A cancelled check reaches the same escalation by the same clock, and
// the person it wakes gets told what actually happened rather than the
// stuck-runner story: the check did finish, it finished saying nothing,
// and a re-run is the only thing that will change that.
func TestTheStallEscalationSeparatesCancelledChecksFromUnfinishedOnes(t *testing.T) {
	t.Parallel()
	cancelled := []github.CheckRun{
		{Name: "workflows", Status: "completed", Conclusion: strPtr("success")},
		{Name: "go", Status: "completed", Conclusion: strPtr("cancelled")},
	}
	phrase := stalledChecksPhrase(cancelled, grainCIVerdict{})
	if !strings.Contains(phrase, "cancelled") || !strings.Contains(phrase, "go") {
		t.Errorf("phrase = %q, want it to say `go` was cancelled", phrase)
	}
	if strings.Contains(phrase, "workflows") {
		t.Errorf("phrase = %q, want it to leave the check that reported out", phrase)
	}
	if cause := stalledChecksCause(cancelled, grainCIVerdict{}); !strings.Contains(cause, "re-run") ||
		strings.Contains(cause, "never finishes") {
		t.Errorf("cause = %q, want the cancelled explanation and not the stuck-runner one", cause)
	}

	// Both at once keeps both sentences: the queue was waiting on two
	// different things and neither of them is the other's cause.
	both := append(append([]github.CheckRun{}, cancelled...),
		github.CheckRun{Name: "deploy approval", Status: "queued"})
	phrase = stalledChecksPhrase(both, grainCIVerdict{})
	for _, want := range []string{"go", "deploy approval"} {
		if !strings.Contains(phrase, want) {
			t.Errorf("phrase = %q, want it to name %q", phrase, want)
		}
	}
	if cause := stalledChecksCause(both, grainCIVerdict{}); !strings.Contains(cause, "re-run") ||
		!strings.Contains(cause, "never finishes") {
		t.Errorf("cause = %q, want both explanations", cause)
	}

	// And nothing cancelled leaves the sentence that was there before.
	unfinished := []github.CheckRun{{Name: "deploy approval", Status: "queued"}}
	if cause := stalledChecksCause(unfinished, grainCIVerdict{}); !strings.Contains(cause, "never finishes") ||
		strings.Contains(cause, "re-run") {
		t.Errorf("cause = %q, want the stuck-runner explanation alone", cause)
	}
}

// A cancelled grain CI run says why it was cancelled, where the person
// who cancelled it said so. The note reaches the pull request comment
// escalateStalledChecks writes as well as the merge queue row, since both
// are stalledChecksPhrase; a run stopped for a reason a person can read
// is one they need not go and investigate.
func TestACancelledGrainCIRunQuotesTheReasonItWasCancelledFor(t *testing.T) {
	t.Parallel()
	phrase := stalledChecksPhrase(nil, grainCIVerdict{
		Required: true, Cancelled: true,
		Detail: "cancelled: superseded: grain/task-9 moved to 1a2b3c4 while this run was judging 5d6e7f8",
	})
	if !strings.Contains(phrase, "superseded: grain/task-9 moved") {
		t.Errorf("phrase = %q, want it to quote why the run was cancelled", phrase)
	}
	// The conclusion word once, not twice: the sentence says "cancelled
	// without a verdict" and the detail ciDetail wrote starts with
	// "cancelled: " as well.
	if strings.Contains(phrase, "cancelled: cancelled") || strings.Contains(phrase, "verdict: cancelled") {
		t.Errorf("phrase = %q, want the detail's own `cancelled:` prefix dropped", phrase)
	}

	// A cancellation nobody left a reason for -- a status written before
	// there was one to leave, or a detail that was only ever the word --
	// keeps the sentence it had, with no empty bracket hanging off it.
	for _, detail := range []string{"", "cancelled:", "cancelled"} {
		phrase := stalledChecksPhrase(nil, grainCIVerdict{Required: true, Cancelled: true, Detail: detail})
		if !strings.Contains(phrase, "cancelled without a verdict, which nothing but another run") {
			t.Errorf("phrase for detail %q = %q, want the plain sentence", detail, phrase)
		}
	}
}

// "Which nothing but another run will replace" was true while nothing
// replaced one, and grain now does. The row has to say which of the
// things is next, because two of them end by themselves, one is the
// task's agent and one is a person -- and a row that sent somebody to push an empty commit at
// a branch grain was about to run again would be worse than the silence
// grain/task-170 ended.
func TestACancelledGrainCIRunSaysWhetherAnotherRunIsComing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		verdict grainCIVerdict
		want    []string
		unwant  []string
	}{{
		name: "a replacement is owed",
		verdict: grainCIVerdict{Required: true, Cancelled: true, Attempts: 1,
			Replacement: forge.GrainCIReplacementDue},
		want:   []string{"grain is replacing with another run", "attempt 2 of 4"},
		unwant: []string{"nothing but another run will replace", "needs a person"},
	}, {
		name: "grain has spent its attempts",
		verdict: grainCIVerdict{Required: true, Cancelled: true, Attempts: 4,
			Replacement: forge.GrainCIReplacementSpent},
		want:   []string{"after 4 attempts", "needs a person"},
		unwant: []string{"grain is replacing with another run"},
	}, {
		name: "an errored run grain is replacing",
		verdict: grainCIVerdict{Required: true, Errored: true, Attempts: 2,
			Replacement: forge.GrainCIReplacementDue, Detail: "grain CI could not run 1 job(s) (tests/smoke)"},
		want:   []string{"could not do at all", "tests/smoke", "grain is replacing with another run", "attempt 3 of 4"},
		unwant: []string{"needs a person"},
	}, {
		name: "an errored run grain has stopped retrying",
		verdict: grainCIVerdict{Required: true, Errored: true, Attempts: 4,
			Replacement: forge.GrainCIReplacementSpent, Detail: "grain CI could not run 1 job(s) (tests/smoke)"},
		want:   []string{"could not do at all", "after 4 attempts", "needs a person"},
		unwant: []string{"grain is replacing with another run"},
	}} {
		t.Run(c.name, func(t *testing.T) {
			phrase := stalledChecksPhrase(nil, c.verdict)
			for _, want := range c.want {
				if !strings.Contains(phrase, want) {
					t.Errorf("phrase = %q, want it to say %q", phrase, want)
				}
			}
			for _, unwant := range c.unwant {
				if strings.Contains(phrase, unwant) {
					t.Errorf("phrase = %q, want it not to say %q", phrase, unwant)
				}
			}
		})
	}
}

// gitBranches is a github.GitBranchSource that answers for one host.
type gitBranches struct {
	host  string
	asked []string
}

func (g *gitBranches) TokenFor(owner, repo string) *string { return nil }

func (g *gitBranches) GitBranchExists(owner, repo, branch string) (bool, bool, error) {
	if model.RepoFromStored(owner, repo).GitHost() != g.host {
		return false, false, nil
	}
	g.asked = append(g.asked, owner+"/"+repo+" "+branch)
	return branch == "grain/1e3bf7ca/task-3", true, nil
}

// The single-tenant walkthrough's B8: a branch on the plain git host
// 127.0.0.1:8088 was checked by asking the GitHub API for walk/demo, host
// dropped, and its 404 failed the check. It is read over git, with the
// host kept, and the API is not asked at all.
func TestTargetBranchExistsReadsAPlainGitHostOverGit(t *testing.T) {
	withNoSleep(t)
	transport := github.NewFakeTransport()
	src := &gitBranches{host: "127.0.0.1:8088"}
	client := github.NewClient(transport, src)
	target := model.RepoRef{Host: "127.0.0.1:8088", Owner: "walk", Name: "demo"}

	if exists, err := targetBranchExists(client, target, "grain/1e3bf7ca/task-3"); err != nil || !exists {
		t.Errorf("pushed branch = %v, %v; want true", exists, err)
	}
	if exists, err := targetBranchExists(client, target, "grain/1e3bf7ca/task-4"); err != nil || exists {
		t.Errorf("unpushed branch = %v, %v; want false", exists, err)
	}
	if len(src.asked) != 2 || src.asked[0] != "127.0.0.1:8088/walk/demo grain/1e3bf7ca/task-3" {
		t.Errorf("git was asked %q", src.asked)
	}
	for _, call := range transport.Calls {
		t.Errorf("the API was asked %s %s about a plain git host's repository", call.Method, call.Path)
	}

	// A repository on GitHub is still the API's.
	transport.Default = github.ApiResponse{Status: 200, Body: []byte(`{"name":"b"}`)}
	if exists, err := targetBranchExists(client, model.RepoRef{Owner: "o", Name: "r"}, "b"); err != nil || !exists {
		t.Errorf("GitHub branch = %v, %v; want true from the API", exists, err)
	}
	if len(transport.Calls) != 1 {
		t.Errorf("API calls = %d, want 1", len(transport.Calls))
	}
}

func TestQueueOrderPrefersHigherPriorityAndPreservesBandOrder(t *testing.T) {
	t.Parallel()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	approve := &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}}
	makeEntry := func(id string, priority int, orderKey float64, fixesBase bool) queueEntry {
		tk := model.Task{
			ID:        id,
			Priority:  priority,
			Target:    &repo,
			Approval:  approve,
			AutoMerge: true,
			OrderKey:  orderKey,
		}
		if fixesBase {
			model.SetFixesBaseBranch(&tk, true)
		}
		return queueEntry{
			task:   tk,
			detail: github.PullRequestDetail{State: "open"},
		}
	}

	entries := []queueEntry{
		makeEntry("prio-2", 2, 10, false),
		makeEntry("prio-5-back", 5, 40, false),
		makeEntry("prio-5-front", 0, 20, false), // Priority 0 defaults to 5
		makeEntry("prio-5-fixes-base", 5, 50, true),
		makeEntry("prio-8", 8, 100, false),
	}

	got := queueOrder(entries)
	want := []string{"prio-8", "prio-5-fixes-base", "prio-5-front", "prio-5-back", "prio-2"}
	if len(got) != len(want) {
		t.Fatalf("len(queueOrder) = %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].task.ID != w {
			t.Errorf("queueOrder[%d] = %q, want %q", i, got[i].task.ID, w)
		}
	}
}
