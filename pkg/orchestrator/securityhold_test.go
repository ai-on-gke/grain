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
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/trustboundary"
)

// finishTouching runs an auto-merging task to the end of a run that pushed
// path, the way a real run reaches the security hold: through
// ProcessResult, which opens the pull request and classifies it.
func finishTouching(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	id, path string, autoMerge bool) model.Task {

	t.Helper()
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, id, repo)
	task.AutoMerge = autoMerge
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	branch := model.BranchName(task.ID)
	pushBranch(t, sim.BareRepo, branch)
	pushFile(t, sim.BareRepo, branch, path, "changed\n")
	return task
}

func reloaded(t *testing.T, ctx context.Context, store *model.Store, id string) model.Task {
	t.Helper()
	got, err := store.GetTask(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	return *got
}

func linkOf(task model.Task, kind model.LinkKind) string {
	for _, l := range task.Links {
		if l.Kind == kind {
			return l.Target
		}
	}
	return ""
}

func TestSecurityHoldWithdrawsAutoMergeAndFilesAScopedReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/gitproxy/proxy.go", true)

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}

	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("a change touching pkg/gitproxy/ must not keep auto-merge")
	}
	if v := linkOf(got, model.LinkSecurityHold); v != trustboundary.Version {
		t.Fatalf("hold link = %q, want %q (links %+v)", v, trustboundary.Version, got.Links)
	}
	reviewID := linkOf(got, model.LinkSecurityReviewTask)
	if reviewID == "" {
		t.Fatalf("no security review linked: %+v", got.Links)
	}
	review := reloaded(t, ctx, store, reviewID)
	if review.Origin.Reason != model.ReasonReview || review.Base != model.BranchName(task.ID) {
		t.Fatalf("review is not stacked on the branch: %+v", review)
	}
	if !strings.HasPrefix(review.Title, "Security review: ") {
		t.Errorf("review title = %q", review.Title)
	}
	if !strings.Contains(review.Body, "`pkg/gitproxy/proxy.go`") || !strings.Contains(review.Body, "credential-proxy") {
		t.Errorf("review body is not scoped to the diff:\n%s", review.Body)
	}
	if !strings.Contains(review.Body, "Fix what you find; ask only what you cannot settle") {
		t.Errorf("review body lacks the security template's instructions:\n%s", review.Body)
	}
	st, err := store.State(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st != model.StateAwaitingSubmit {
		t.Errorf("state = %q, want awaiting_submit", st)
	}
	bodies := strings.Join(commentBodies(t, ctx, store, task.ID), "\n")
	if !strings.Contains(bodies, "trust boundary") || !strings.Contains(bodies, reviewID) {
		t.Errorf("no comment explaining the hold: %s", bodies)
	}
	pr := strings.Join(prCommentBodies(t, client, got), "\n")
	if !strings.Contains(pr, "`pkg/gitproxy/proxy.go`") || !strings.Contains(pr, "credential-proxy") ||
		!strings.Contains(pr, reviewID) {
		t.Errorf("the pull request does not say why the change is held: %s", pr)
	}
}

// prCommentBodies is every top-level comment on task's pull request.
func prCommentBodies(t *testing.T, client github.Client, task model.Task) []string {
	t.Helper()
	ref, err := model.ParsePullRequestRef(linkOf(task, model.LinkFixes))
	if err != nil {
		t.Fatalf("no pull request on %s: %v", task.ID, err)
	}
	comments, err := client.ListComments(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, c := range comments {
		bodies = append(bodies, c.Body)
	}
	return bodies
}

func TestSecurityHoldLeavesAnOrdinaryChangeAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/client.go", true)

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" || linkOf(got, model.LinkSecurityReviewTask) != "" {
		t.Fatalf("an ordinary change was held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

func TestSecurityHoldIgnoresAChangeThatWasNeverGoingToMergeItself(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/gitproxy/proxy.go", false)

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if linkOf(got, model.LinkSecurityReviewTask) != "" {
		t.Fatalf("a change already waiting for a human got a review filed: %+v", got.Links)
	}
}

// Baselines and suppressions can never auto-merge: the review is filed,
// but nothing it does gives auto-merge back.
func TestSecurityHoldOnSuppressionsIsNeverReleasedByAReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", ".grain/quarantine.yml", true)

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	reviewID := linkOf(got, model.LinkSecurityReviewTask)
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" || reviewID == "" {
		t.Fatalf("automerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("a clean review released a change to .grain/quarantine.yml")
	}
}

func completeReview(t *testing.T, ctx context.Context, store *model.Store, reviewID string) {
	t.Helper()
	done := baseTime.Add(30 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: reviewID, CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
}

func heldTask(t *testing.T, ctx context.Context, store *model.Store) (*github.RESTClient, model.Task, string) {
	t.Helper()
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	if reviewID == "" {
		t.Fatal("expected a security review")
	}
	return client, task, reviewID
}

func TestSecurityHoldIsReleasedByACleanReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)

	// Still running: nothing changes.
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("released before the review finished")
	}

	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("a clean review did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	// One note on the pull request for the hold and one for its release,
	// however many cycles go by.
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	pr := prCommentBodies(t, client, got)
	if len(pr) != 2 || !strings.Contains(pr[1], "finished without findings") {
		t.Fatalf("pull request comments = %q, want the hold and its release once each", pr)
	}
}

func TestSecurityHoldIsNotReleasedByAReviewWithFindings(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)

	review := reloaded(t, ctx, store, reviewID)
	result := toolResult(reviewFinding("this route skips auth", "pkg/ui/server.go", 1))
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, reviewID+"-1", baseTime.Add(20*time.Minute)); err != nil {
		t.Fatalf("ProcessResult for the review: %v", err)
	}
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("findings did not leave the change for a human: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "left findings on this change") {
		t.Errorf("the pull request does not say the hold now waits for a human: %s", pr)
	}
}

// The reviewer fixes what it finds and the change goes through: a
// security review whose fixes merged back into the held branch, with no
// findings, releases the hold just as a review that read it clean does.
func TestSecurityHoldIsReleasedByAReviewWhoseFixesMergedBack(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)
	pushedReview(t, ctx, store, reviewID)

	// Still open: its fixes have not reached the branch yet.
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := reloaded(t, ctx, store, task.ID); got.AutoMerge || linkOf(got, model.LinkSecurityHold) == "" {
		t.Fatalf("a review whose fixes are still open settled the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	merged := baseTime.Add(2 * time.Hour)
	if err := store.ObserveField(ctx, reviewID, merged, func(o *model.Observation) { o.PrMergedAt = &merged }); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, merged); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("a clean review whose fixes merged did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if linkOf(got, model.LinkTrustBoundaryCleared) == "" {
		t.Errorf("the release dropped the clearance the new head is compared against: %+v", got.Links)
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "fixes have merged") {
		t.Errorf("the pull request does not say the review's fixes landed: %s", pr)
	}
}

// Fixes that were closed without merging are not a clean review: the
// hold ends and the change waits for a human.
func TestSecurityHoldIsNotReleasedByAReviewWhoseFixesWereClosed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)
	pushedReview(t, ctx, store, reviewID)
	closed := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, reviewID, closed, func(o *model.Observation) { o.PrClosedAt = &closed }); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, closed); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("a review whose fixes were closed released the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "closed without merging") {
		t.Errorf("the pull request does not say the review's fixes never landed: %s", pr)
	}
}

// asStackedReview turns reviewID back into the kind of security review
// filed before reviews worked on the held branch itself: one with a branch
// of its own, whose fixes reach the held change through a stacked pull
// request. Such reviews already open keep that flow until they settle.
func asStackedReview(t *testing.T, ctx context.Context, store *model.Store, reviewID string) {
	t.Helper()
	if err := store.UpdateTask(ctx, reviewID, func(r *model.Task) error {
		r.Branch = model.BranchName(r.ID)
		var links []model.Link
		for _, l := range r.Links {
			if l.Kind != model.LinkPushesReviewedBranch {
				links = append(links, l)
			}
		}
		r.Links = links
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// pushedReview gives the review a pull request of its own and finishes
// its run.
func pushedReview(t *testing.T, ctx context.Context, store *model.Store, reviewID string) {
	t.Helper()
	asStackedReview(t, ctx, store, reviewID)
	if err := store.UpdateTask(ctx, reviewID, func(r *model.Task) error {
		r.Links = append(r.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#99"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	completeReview(t, ctx, store, reviewID)
}

// unlistableFiles is a GitHub whose pull-request file listing fails, which
// is how the hold meets a change it cannot classify.
type unlistableFiles struct{ github.Client }

func (unlistableFiles) ListPullRequestFiles(string, string, int) ([]string, error) {
	return nil, errors.New("listing files: 502 Bad Gateway")
}

// A change whose files cannot be read is held as if it touched everything
// -- the paths only a human may let through included -- so a clean
// review must not release it. It did, because releasable was worked out
// from the (empty) classification alone.
func TestSecurityHoldOnAnUnreadableFileListIsNeverReleasedByAReview(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "README.md", true)

	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, unlistableFiles{client}, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	reviewID := linkOf(got, model.LinkSecurityReviewTask)
	if got.AutoMerge || reviewID == "" {
		t.Fatalf("an unclassifiable change was not held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if linkOf(got, model.LinkSecurityHold) != "" {
		t.Errorf("the hold on an unclassifiable change is marked releasable: links=%+v", got.Links)
	}

	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("a clean review released a change whose file list was never read")
	}
}

// clearedOrdinaryChange is an auto-merging change that touched nothing on
// the list when its run finished, green and mergeable, so the next
// SyncPullRequests merges it unless something stops it.
func clearedOrdinaryChange(t *testing.T, ctx context.Context, store *model.Store) (*githubsim.Sim, *github.RESTClient, model.Task, string) {
	t.Helper()
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "README.md", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	cleared := linkOf(got, model.LinkTrustBoundaryCleared)
	if !got.AutoMerge || !strings.HasPrefix(cleared, trustboundary.Version+"@") || len(cleared) <= len(trustboundary.Version)+1 {
		t.Fatalf("an ordinary change was not cleared for its head: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	branch := model.BranchName(task.ID)
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	return sim, client, got, branch
}

func TestSecurityHoldLetsTheHeadItClassifiedMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := clearedOrdinaryChange(t, ctx, store)

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("a cleared, green change did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}

// A commit reaching the branch without a run of its own task finishing --
// a stacked fix merging back, a push through the git proxy, which has no
// ref policy (#2387) -- used to merge on the strength of a classification
// of the commit before it. It is classified again first.
func TestSecurityHoldClassifiesAHeadThatMovedOutsideARun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := clearedOrdinaryChange(t, ctx, store)

	pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "pushed past the run\n")
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a trust-boundary commit pushed after the classification merged itself")
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) == "" {
		t.Fatalf("the moved head was not held for review: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// A head that moved while the task's own run is still in flight is that
// run's to classify when it finishes: the queue classifying it mid-run
// filed a review of every intermediate push (task 447, a base merge
// classified nine seconds before the run pushed its fixes on top). Once
// the run is over, a head it left unclassified is classified as before.
func TestSecurityHoldWaitsOutAPushMadeMidRun(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := clearedOrdinaryChange(t, ctx, store)

	// Sent back by the queue to repair its own branch, which is the one
	// way a task with a run in flight is still a queue entry
	// (Store.OpenPullRequestLinks).
	repairAt := baseTime.Add(time.Minute)
	if err := store.ObserveField(ctx, task.ID, repairAt, func(o *model.Observation) {
		o.MergeQueueRepairAt = &repairAt
		o.CompletedAt = nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: task.ID + "-2", TaskID: task.ID, Sandbox: "s1", Attempt: 2,
		StartedAt: repairAt}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "an intermediate push\n")
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 0 {
		t.Fatalf("filed %d security reviews of a head the run was still pushing to, want 0", len(reviews))
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a branch with a run in flight merged")
	}

	if err := store.FinishRun(ctx, task.ID+"-2", baseTime.Add(3*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a trust-boundary head merged unclassified once the run was over")
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews once the run was over, want 1", len(reviews))
	}
}

func TestSecurityHoldClearsAMovedHeadThatStillTouchesNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, branch := clearedOrdinaryChange(t, ctx, store)
	before := linkOf(task, model.LinkTrustBoundaryCleared)

	pushFile(t, sim.BareRepo, branch, "docs/notes.md", "a docs fix merged back\n")
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if after := linkOf(got, model.LinkTrustBoundaryCleared); after == before || !got.AutoMerge {
		t.Fatalf("the new head was not cleared: before=%q automerge=%v links=%+v", before, got.AutoMerge, got.Links)
	}
	if !mergedBranch(sim, branch) {
		t.Fatal("a moved head that still touches nothing waited a cycle it did not need to")
	}
}

// settleHeldPullRequest merges (or closes) a held task's pull request on
// GitHub, the way a human's Submit -- or a click on GitHub -- ends it, and
// runs the cycle that reads it.
func settleHeldPullRequest(t *testing.T, ctx context.Context, store *model.Store, client github.Client,
	task model.Task, merge bool, now time.Time) {

	t.Helper()
	ref, err := model.ParsePullRequestRef(linkOf(reloaded(t, ctx, store, task.ID), model.LinkFixes))
	if err != nil {
		t.Fatal(err)
	}
	if merge {
		err = client.MergePullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number, "")
	} else {
		err = client.ClosePullRequest(ref.Repo.Owner, ref.Repo.Name, ref.Number)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, now); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
}

func TestSecurityReviewIsClosedWhenTheChangeItReviewsMerges(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)

	settleHeldPullRequest(t, ctx, store, client, task, true, baseTime.Add(time.Hour))

	if st, err := store.State(ctx, reviewID); err != nil || st != model.StateClosed {
		t.Fatalf("review state = %q (%v), want closed", st, err)
	}
	if notes := strings.Join(commentBodies(t, ctx, store, reviewID), "\n"); !strings.Contains(notes, "merged") ||
		!strings.Contains(notes, task.ID) {
		t.Errorf("the review does not say why it closed: %s", notes)
	}
	// Its silence must not read as a pass: the merged change says the
	// review never finished, in grain and on the pull request.
	got := reloaded(t, ctx, store, task.ID)
	if notes := strings.Join(commentBodies(t, ctx, store, task.ID), "\n"); !strings.Contains(notes, "closed unfinished") ||
		!strings.Contains(notes, reviewID) {
		t.Errorf("the merged change does not say its review was cut short: %s", notes)
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "closed unfinished") {
		t.Errorf("the pull request does not say its review was cut short: %s", pr)
	}
}

func TestSecurityReviewIsClosedWhenTheChangeItReviewsIsClosed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)

	settleHeldPullRequest(t, ctx, store, client, task, false, baseTime.Add(time.Hour))

	if st, err := store.State(ctx, reviewID); err != nil || st != model.StateClosed {
		t.Fatalf("review state = %q (%v), want closed", st, err)
	}
	if notes := strings.Join(commentBodies(t, ctx, store, reviewID), "\n"); !strings.Contains(notes, "closed without merging") {
		t.Errorf("the review does not say why it closed: %s", notes)
	}
	if notes := strings.Join(commentBodies(t, ctx, store, task.ID), "\n"); strings.Contains(notes, "closed unfinished") {
		t.Errorf("a change thrown away should not be told its review was cut short: %s", notes)
	}
}

// A review that already finished has said what it had to say; the merge
// that follows its release must not close it a second time or tell the
// change its review was cut short.
func TestSecurityReviewThatFinishedIsLeftAloneByTheMerge(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client, task, reviewID := heldTask(t, ctx, store)

	completeReview(t, ctx, store, reviewID)
	settleHeldPullRequest(t, ctx, store, client, task, true, baseTime.Add(time.Hour))

	if notes := strings.Join(commentBodies(t, ctx, store, reviewID), "\n"); strings.Contains(notes, "unfinished") {
		t.Errorf("a finished review was closed as unfinished: %s", notes)
	}
	if notes := strings.Join(commentBodies(t, ctx, store, task.ID), "\n"); strings.Contains(notes, "closed unfinished") {
		t.Errorf("the change was told a finished review was cut short: %s", notes)
	}
}

// A port that inherited its source's clearance keeps it across the
// commits CI repair pushes onto it -- a rerun's own, and one that reached
// the branch outside a run -- so long as none of them touches a
// trust-boundary patch: no second review, no second note (task 2935,
// three reviews in a row). One that does is held.
func TestPortCIAdjustmentDoesNotFileDuplicateReviews(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	source, branch, reviewID := reviewedSource(t, ctx, store, sim, client, repo)
	mergeQueued(t, ctx, store, sim, client, source, branch)
	port := agentPort(t, ctx, store, sim, client, source, "pkg/ui/server.go", "reviewed\n")
	requireInherited(t, ctx, store, client, sim, port, source.ID, reviewID)
	portBranch := port.BranchName()

	// A rerun repairing CI.
	pushFile(t, sim.BareRepo, portBranch, "pkg/ui/server_test.go", "a flake fixed\n")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, port, result, port.ID+"-2", baseTime.Add(3*time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	requireInherited(t, ctx, store, client, sim, port, source.ID, reviewID)

	// A formatting fix merged back outside any run, then the merge. The
	// port's gate is grain CI (newPortTask grants it); this test's is the
	// sim's checks.
	if err := store.UpdateTask(ctx, port.ID, func(t *model.Task) error {
		t.Grants = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	pushFile(t, sim.BareRepo, portBranch, "docs/notes.md", "formatted\n")
	setMergeable(sim, true)
	sim.CheckRuns[portBranch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(4*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if reviews := securityReviewsOf(t, ctx, store, port.ID); len(reviews) != 0 {
		t.Fatalf("filed %d security reviews for repair commits, want 0", len(reviews))
	}
	if !mergedBranch(sim, portBranch) {
		t.Fatalf("the repaired port did not merge: links=%+v", reloaded(t, ctx, store, port.ID).Links)
	}
}

func TestPortCommitChangingATrustBoundaryPatchIsHeld(t *testing.T) {
	store, ctx := openStore(t)
	sim, client, repo := pairedSim(t, ctx, store, "main", "candidate")
	source, branch, reviewID := reviewedSource(t, ctx, store, sim, client, repo)
	mergeQueued(t, ctx, store, sim, client, source, branch)
	port := agentPort(t, ctx, store, sim, client, source, "pkg/ui/server.go", "reviewed\n")
	requireInherited(t, ctx, store, client, sim, port, source.ID, reviewID)

	pushFile(t, sim.BareRepo, port.BranchName(), "pkg/ui/server.go", "reviewed, then changed\n")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, port, result, port.ID+"-2", baseTime.Add(3*time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, port.ID)
	if got.AutoMerge || len(securityReviewsOf(t, ctx, store, port.ID)) != 1 {
		t.Fatalf("a port commit changing a trust-boundary patch was not held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// A change released by a clean review, whose head then moves on in files
// off the list, keeps its clearance and merges without another review.
func TestSecurityHoldCarriesAClearanceToAHeadThatChangedNoTrustBoundaryPatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	branch := model.BranchName(task.ID)
	pushFile(t, sim.BareRepo, branch, "docs/notes.md", "a docs fix merged back\n")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want only the first", len(reviews))
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("a reviewed change whose head moved off the list did not merge: links=%+v",
			reloaded(t, ctx, store, task.ID).Links)
	}
}

// A held change whose rerun pushes commits off the list goes on waiting
// on the review already filed rather than superseding it, and that
// review's clean finish releases the new head.
func TestSecurityHoldKeepsItsReviewForAHeadThatChangedNoTrustBoundaryPatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)

	branch := model.BranchName(task.ID)
	pushFile(t, sim.BareRepo, branch, "README.md", "a CI repair\n")
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-2", baseTime.Add(time.Minute)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) != reviewID {
		t.Fatalf("the hold moved off its review: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want 1", len(reviews))
	}

	completeReview(t, ctx, store, reviewID)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("the released change did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}

func TestSecurityHoldDoesNotDuplicateReviewOnUnchangedHead(t *testing.T) {
	t.Parallel()
	t.Run("releasable hold and cleared head", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
		result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}
		reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
		if err := store.StartRun(ctx, model.Run{ID: reviewID + "-1", TaskID: reviewID, Sandbox: "s1", Attempt: 1,
			StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}

		// Re-run at the exact same headSHA while the security review is running.
		if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), result, "t1-2",
			baseTime.Add(2*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (rerun while held): %v", err)
		}
		got := reloaded(t, ctx, store, task.ID)
		if got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) != reviewID {
			t.Fatalf("rerun on unchanged held head replaced the review: automerge=%v links=%+v", got.AutoMerge, got.Links)
		}
		if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
			t.Fatalf("filed %d security reviews, want 1", len(reviews))
		}
		if st, err := store.State(ctx, reviewID); err != nil || st != model.StateRunning {
			t.Fatalf("review state = %q (%v), want running (not superseded)", st, err)
		}

		// Finish the review cleanly and release the hold.
		if err := store.FinishRun(ctx, reviewID+"-1", baseTime.Add(3*time.Minute), "succeeded", ""); err != nil {
			t.Fatal(err)
		}
		completeReview(t, ctx, store, reviewID)
		if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(4*time.Minute)); err != nil {
			t.Fatal(err)
		}
		clearedTask := reloaded(t, ctx, store, task.ID)
		if !clearedTask.AutoMerge || linkOf(clearedTask, model.LinkSecurityHold) != "" {
			t.Fatalf("expected hold released: automerge=%v links=%+v", clearedTask.AutoMerge, clearedTask.Links)
		}

		// Re-run at the exact same headSHA after the head was already cleared.
		if err := orchestrator.ProcessResult(ctx, store, client, clearedTask, result, "t1-3",
			baseTime.Add(5*time.Minute)); err != nil {
			t.Fatalf("ProcessResult (rerun after clearance): %v", err)
		}
		after := reloaded(t, ctx, store, task.ID)
		if !after.AutoMerge || linkOf(after, model.LinkSecurityHold) != "" {
			t.Fatalf("rerun on already-cleared head re-held the change: automerge=%v links=%+v", after.AutoMerge, after.Links)
		}
		if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
			t.Fatalf("filed %d security reviews after rerun on cleared head, want 1", len(reviews))
		}
		if got := linkOf(reloaded(t, ctx, store, reviewID), model.LinkSupersededBy); got != "" {
			t.Fatalf("completed review was superseded by %q on unchanged head", got)
		}
	})

	t.Run("requires-human hold", func(t *testing.T) {
		t.Parallel()
		for _, path := range []string{".grain/quarantine.yml", ".grain/workflows/tests.yml"} {
			store, ctx := openStore(t)
			sim, client := newSim(t, "acme", "widgets", "main")
			task := finishTouching(t, ctx, store, sim, "t1", path, true)
			result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
			if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
				t.Fatalf("ProcessResult (%s): %v", path, err)
			}
			got := reloaded(t, ctx, store, task.ID)
			reviewID := linkOf(got, model.LinkSecurityReviewTask)
			head := branchSHA(t, sim.BareRepo, model.BranchName(task.ID))
			if got.AutoMerge || reviewID == "" || linkOf(got, model.LinkTrustBoundaryCleared) != trustboundary.Version+"@"+head {
				t.Fatalf("%s hold did not record classified head: automerge=%v links=%+v", path, got.AutoMerge, got.Links)
			}

			// Re-run at the same headSHA (including when AutoMerge was re-enabled).
			if err := store.UpdateTask(ctx, task.ID, func(tk *model.Task) error {
				tk.AutoMerge = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), result, "t1-2",
				baseTime.Add(time.Minute)); err != nil {
				t.Fatalf("ProcessResult (%s rerun): %v", path, err)
			}
			if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
				t.Fatalf("%s: filed %d security reviews on unchanged head, want 1", path, len(reviews))
			}
			if got := linkOf(reloaded(t, ctx, store, reviewID), model.LinkSupersededBy); got != "" {
				t.Fatalf("%s: review was superseded by %q on unchanged head", path, got)
			}
		}
	})
}

func mergeStackedReviewFix(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client *github.RESTClient, task model.Task, reviewID, path, content string, now time.Time) {

	t.Helper()
	asStackedReview(t, ctx, store, reviewID)
	if err := store.StartRun(ctx, model.Run{ID: reviewID + "-1", TaskID: reviewID, Sandbox: "s1", Attempt: 1,
		StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	review := reloaded(t, ctx, store, reviewID)
	reviewBranch := model.BranchName(review.ID)
	wd := filepath.Join(t.TempDir(), "work")
	run(t, filepath.Dir(wd), "git", "clone", "-q", "--branch", model.BranchName(task.ID), sim.BareRepo, wd)
	run(t, wd, "git", "checkout", "-q", "-b", reviewBranch)
	run(t, wd, "git", "push", "-q", "origin", reviewBranch)
	pushFile(t, sim.BareRepo, reviewBranch, path, content)
	if err := store.FinishRun(ctx, reviewID+"-1", now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, reviewID+"-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("ProcessResult (review): %v", err)
	}
	completeReview(t, ctx, store, reviewID)
	reviewPR, err := model.ParsePullRequestRef(linkOf(reloaded(t, ctx, store, reviewID), model.LinkFixes))
	if err != nil {
		t.Fatalf("reading review PR: %v", err)
	}
	if err := client.MergePullRequest(reviewPR.Repo.Owner, reviewPR.Repo.Name, reviewPR.Number, ""); err != nil {
		t.Fatalf("merging review PR: %v", err)
	}
	mergedAt := now.Add(2 * time.Minute)
	if err := store.ObserveField(ctx, reviewID, mergedAt, func(o *model.Observation) {
		o.PrMergedAt = &mergedAt
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSecurityHoldAdvancesClearanceWhenCleanReviewMergesTrustBoundaryFix(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
	mergeStackedReviewFix(t, ctx, store, sim, client, task, reviewID, "pkg/ui/server.go", "changed and hardened by reviewer\n",
		baseTime.Add(time.Minute))

	branch := model.BranchName(task.ID)
	mergedHead := branchSHA(t, sim.BareRepo, branch)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("SyncSecurityHolds: %v", err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("clean review fix did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if want := trustboundary.Version + "@" + mergedHead; linkOf(got, model.LinkTrustBoundaryCleared) != want {
		t.Fatalf("clearance = %q, want %q", linkOf(got, model.LinkTrustBoundaryCleared), want)
	}

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(15*time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want 1 (no daisy-chained review of the reviewer's own fix)", len(reviews))
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("the branch with the merged review fix did not merge: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}

func TestSecurityHoldReholdsWhenReviewFixIntroducesRequiresHumanOrNonReviewCommitLands(t *testing.T) {
	t.Parallel()
	t.Run("requires-human introduced by review fix", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
		result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}
		reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
		mergeStackedReviewFix(t, ctx, store, sim, client, task, reviewID, ".grain/quarantine.yml", "quarantined: []\n",
			baseTime.Add(time.Minute))

		if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(10*time.Minute)); err != nil {
			t.Fatalf("SyncSecurityHolds: %v", err)
		}
		got := reloaded(t, ctx, store, task.ID)
		if got.AutoMerge {
			t.Fatalf("review fix touching RequiresHuman path released auto-merge: links=%+v", got.Links)
		}
		if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 2 {
			t.Fatalf("filed %d security reviews, want 2", len(reviews))
		}
	})

	t.Run("non-review trust-boundary commit landed alongside review fix", func(t *testing.T) {
		t.Parallel()
		store, ctx := openStore(t)
		sim, client := newSim(t, "acme", "widgets", "main")
		task := finishTouching(t, ctx, store, sim, "t1", "pkg/ui/server.go", true)
		result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
		if err := orchestrator.ProcessResult(ctx, store, client, task, result, "t1-1", baseTime); err != nil {
			t.Fatalf("ProcessResult: %v", err)
		}
		reviewID := linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
		mergeStackedReviewFix(t, ctx, store, sim, client, task, reviewID, "pkg/ui/server.go", "hardened\n",
			baseTime.Add(time.Minute))
		pushFile(t, sim.BareRepo, model.BranchName(task.ID), "pkg/gitproxy/proxy.go", "unreviewed change\n")

		if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(10*time.Minute)); err != nil {
			t.Fatalf("SyncSecurityHolds: %v", err)
		}
		got := reloaded(t, ctx, store, task.ID)
		if got.AutoMerge || linkOf(got, model.LinkSecurityHold) == "" {
			t.Fatalf("non-review trust-boundary commit was not held: automerge=%v links=%+v", got.AutoMerge, got.Links)
		}
		if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 2 {
			t.Fatalf("filed %d security reviews, want 2", len(reviews))
		}
	})
}
