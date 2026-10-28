package orchestrator_test

import (
	"context"
	"strconv"
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

// finishInPlaceReview runs the security review reviewID to the end of a run
// that, when fix is not "", pushed fix to the held branch itself -- the
// only branch its run may push -- and closed with a note, the way a real
// review that fixed what it found does.
func finishInPlaceReview(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim,
	client github.Client, task model.Task, reviewID, fix string, extra ...agent.ToolCall) {

	t.Helper()
	if err := store.StartRun(ctx, model.Run{ID: reviewID + "-1", TaskID: reviewID, Sandbox: "s-review",
		Attempt: 1, StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if fix != "" {
		pushFile(t, sim.BareRepo, model.BranchName(task.ID), fix, "hardened by the reviewer\n")
	}
	if err := store.FinishRun(ctx, reviewID+"-1", baseTime.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	calls := append([]agent.ToolCall{{Name: "comment_on_issue",
		Arguments: map[string]any{"comment": "Fixed: the route now checks auth."}}}, extra...)
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, reviewID),
		toolResult(calls...), reviewID+"-1", baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (review): %v", err)
	}
}

// The security review works on the held change's own branch: its run may
// push that branch and no other, and what it pushes lands in the change's
// own pull request rather than one of the review's.
func TestSecurityReviewPushesToTheReviewedBranchAndOpensNoPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)

	review := reloaded(t, ctx, store, reviewID)
	if review.BranchName() != branch || linkOf(review, model.LinkPushesReviewedBranch) != task.ID {
		t.Fatalf("review branch = %q links %+v, want %q pushing %s's own branch",
			review.BranchName(), review.Links, branch, task.ID)
	}
	if !strings.Contains(review.Body, "push any fix to it") {
		t.Errorf("review body does not say to push to the reviewed branch:\n%s", review.Body)
	}
	prompt := orchestrator.BuildPrompt(review, "work", "", true, 0, orchestrator.History{}, nil)
	if !strings.Contains(prompt, "Push any fixes to \""+branch+"\"") || !strings.Contains(prompt, "open no pull request") ||
		!strings.Contains(prompt, "rebase") {
		t.Errorf("prompt does not send the review's fixes to the reviewed branch:\n%s", prompt)
	}

	if err := store.StartRun(ctx, model.Run{ID: reviewID + "-1", TaskID: reviewID, Sandbox: "s-review",
		Attempt: 1, StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	pushBranch, live, err := store.GitPushBranch(ctx, "s-review")
	if err != nil || !live || pushBranch != branch {
		t.Fatalf("GitPushBranch = %q, %v, %v; want the reviewed branch %q", pushBranch, live, err, branch)
	}
	if bound, err := store.GitPushFastForwardOnly(ctx, "s-review"); err != nil || !bound {
		t.Fatalf("GitPushFastForwardOnly = %v, %v; a review on the reviewed branch may delete or force-push it", bound, err)
	}
	if _, err := orchestrator.OpenPullRequestForTask(ctx, store, client, review); err == nil {
		t.Error("open_pull_request opened a second pull request for a review working on the reviewed branch")
	}

	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "hardened by the reviewer\n")
	head := branchSHA(t, sim.BareRepo, branch)
	if err := store.FinishRun(ctx, reviewID+"-1", baseTime.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "comment_on_issue",
		Arguments: map[string]any{"comment": "Fixed: the route now checks auth."}})
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, reviewID+"-1",
		baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (review): %v", err)
	}
	got := reloaded(t, ctx, store, reviewID)
	if linkOf(got, model.LinkFixes) != "" {
		t.Fatalf("the review opened a pull request of its own: %+v", got.Links)
	}
	if pr, err := client.FindOpenPullRequestForBranch("acme", "widgets", model.BranchName(reviewID)); err != nil || pr != nil {
		t.Fatalf("a pull request is open off a branch of the review's own: %+v (%v)", pr, err)
	}
	if pr, err := client.FindOpenPullRequestForBranch("acme", "widgets", branch); err != nil || pr == nil ||
		linkOf(reloaded(t, ctx, store, task.ID), model.LinkFixes) != "acme/widgets#"+itoa(pr.Number) {
		t.Fatalf("the reviewed branch's pull request = %+v (%v), want still the reviewed change's own", pr, err)
	}
	if v := linkOf(got, model.LinkReviewerHead); v != head {
		t.Fatalf("reviewer head = %q, want %q", v, head)
	}
	if st, err := store.State(ctx, reviewID); err != nil || st != model.StateCompleted {
		t.Fatalf("review state = %q (%v), want completed", st, err)
	}
	if onMergeQueue(t, ctx, store, reviewID) {
		t.Error("the review is on the merge queue as a member of its own")
	}
	// A later attempt (an owner's reply, a retry) reattaches the pull
	// request open off the task's branch -- which, for this review, is the
	// reviewed change's, and must never become the auto-merging review's.
	if ref, err := orchestrator.ReattachPullRequest(ctx, store, client, got, true, baseTime.Add(3*time.Minute)); err != nil || ref != nil {
		t.Fatalf("ReattachPullRequest = %v (%v), want the reviewed change's pull request left alone", ref, err)
	}
	if linkOf(reloaded(t, ctx, store, reviewID), model.LinkFixes) != "" {
		t.Fatal("reattaching linked the reviewed change's pull request onto its review")
	}
}

// Fixes alone need no human: a review that fixed what it found on the
// held branch, off the trust boundary, and left nothing open releases the
// change, cleared at the head it left.
func TestSecurityHoldIsReleasedByAReviewThatPushedFixesToTheBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, "pkg/widgets/validate.go")
	head := branchSHA(t, sim.BareRepo, branch)

	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("a review that fixed what it found did not release the hold: automerge=%v links=%+v",
			got.AutoMerge, got.Links)
	}
	if want := trustboundary.Version + "@" + head; linkOf(got, model.LinkTrustBoundaryCleared) != want {
		t.Fatalf("clearance = %q, want %q, the head the reviewer left", linkOf(got, model.LinkTrustBoundaryCleared), want)
	}
	pr := strings.Join(prCommentBodies(t, client, got), "\n")
	if !strings.Contains(pr, "fixed what it found by pushing") || !strings.Contains(pr, head[:12]) {
		t.Errorf("the pull request does not say what the review pushed: %s", pr)
	}

	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 1 {
		t.Fatalf("filed %d security reviews, want 1 (no review of the reviewer's own fix)", len(reviews))
	}
	if !mergedBranch(sim, branch) {
		t.Fatalf("the change did not merge at the reviewer's head: links=%+v", reloaded(t, ctx, store, task.ID).Links)
	}
}

// A commit somebody else pushes after the reviewer's head is not covered
// by the review: touching the trust boundary, it is held and reviewed.
func TestSecurityHoldReholdsACommitPushedAfterTheReviewersHead(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, "pkg/widgets/validate.go")
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("the review did not release the hold to begin with")
	}

	pushFile(t, sim.BareRepo, branch, "pkg/gitproxy/proxy.go", "unreviewed change\n")
	setMergeable(sim, true)
	sim.CheckRuns[branch] = greenCheck
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if mergedBranch(sim, branch) {
		t.Fatal("a trust-boundary commit pushed after the reviewer's head merged unreviewed")
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) == "" {
		t.Fatalf("the later commit was not held: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 2 {
		t.Fatalf("filed %d security reviews, want 2", len(reviews))
	}
}

// What a review could not fix still waits for a human, fixes or no fixes.
func TestSecurityHoldWaitsForSubmitWhenAReviewThatPushedFixesLeftAFinding(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, "pkg/ui/server.go",
		reviewFinding("should this route be public at all?", "pkg/ui/server.go", 1))

	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("an open finding did not leave the change for a human: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "left findings on this change") {
		t.Errorf("the pull request does not say the review left findings: %s", pr)
	}
}

// A review that read the branch and pushed nothing releases the hold as it
// always has, at the head the hold was filed for.
func TestSecurityHoldIsReleasedByAnInPlaceReviewThatPushedNothing(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	cleared := linkOf(reloaded(t, ctx, store, task.ID), model.LinkTrustBoundaryCleared)
	finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, "")
	if v := linkOf(reloaded(t, ctx, store, reviewID), model.LinkReviewerHead); v != "" {
		t.Fatalf("a review that pushed nothing recorded a head: %q", v)
	}

	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("a clean review did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	if v := linkOf(got, model.LinkTrustBoundaryCleared); v != cleared {
		t.Errorf("clearance = %q, want the one the hold was filed at, %q", v, cleared)
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "finished without findings") {
		t.Errorf("the pull request does not say the review came back clean: %s", pr)
	}
}

// A review superseded by one of a newer head may no longer push the
// branch, records no head, and releases nothing.
func TestSupersededInPlaceReviewCannotPushOrRelease(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, first := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	if err := store.StartRun(ctx, model.Run{ID: first + "-1", TaskID: first, Sandbox: "s-review",
		Attempt: 1, StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if b, live, err := store.GitPushBranch(ctx, "s-review"); err != nil || !live || b != branch {
		t.Fatalf("before the supersede, GitPushBranch = %q, %v, %v; want %q", b, live, err, branch)
	}

	second := rerunHeld(t, ctx, store, sim, client, task)
	if second == "" || second == first {
		t.Fatalf("no new review was filed for the new head: first=%q second=%q", first, second)
	}
	if b, live, err := store.GitPushBranch(ctx, "s-review"); err != nil || live || b != "" {
		t.Fatalf("a superseded review's run may still push %q (live=%v, %v)", b, live, err)
	}

	// Its run ends anyway, having pushed before the stop reached it.
	pushFile(t, sim.BareRepo, branch, "pkg/ui/server.go", "late push\n")
	if err := store.FinishRun(ctx, first+"-1", baseTime.Add(90*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "comment_on_issue", Arguments: map[string]any{"comment": "fixed"}})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, first), result, first+"-1",
		baseTime.Add(90*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (superseded review): %v", err)
	}
	if v := linkOf(reloaded(t, ctx, store, first), model.LinkReviewerHead); v != "" {
		t.Errorf("a superseded review recorded a head: %q", v)
	}
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityReviewTask) != second {
		t.Fatalf("a superseded review released the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// A review's own fixes cannot widen what it may release: one that touches
// a path only a human may let through is held afresh, not cleared.
func TestSecurityHoldReholdsAnInPlaceFixTouchingAHumanOnlyPath(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, ".grain/quarantine.yml")

	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatalf("a review's fix touching a human-only path released auto-merge: links=%+v", got.Links)
	}
	if reviews := securityReviewsOf(t, ctx, store, task.ID); len(reviews) != 2 {
		t.Fatalf("filed %d security reviews, want 2", len(reviews))
	}
}

// No agent clears its own trust-boundary edits: a review whose fixes touch
// the boundary leaves a new head that is held again and reviewed by
// another review -- whose clean finish then releases it, still with no
// human needed.
func TestSecurityHoldReviewsAgainAnInPlaceFixOnTheTrustBoundary(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	branch := model.BranchName(task.ID)
	finishInPlaceReview(t, ctx, store, sim, client, task, reviewID, "pkg/ui/server.go")
	head := branchSHA(t, sim.BareRepo, branch)

	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) == "" {
		t.Fatalf("a reviewer's own trust-boundary fix cleared itself: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
	second := linkOf(got, model.LinkSecurityReviewTask)
	if second == "" || second == reviewID {
		t.Fatalf("no second review was filed for the reviewer's fix: links=%+v", got.Links)
	}
	if want := trustboundary.Version + "@" + head; linkOf(got, model.LinkTrustBoundaryCleared) != want {
		t.Fatalf("the new hold is at %q, want the reviewer's head %q", linkOf(got, model.LinkTrustBoundaryCleared), want)
	}
	if pr := strings.Join(prCommentBodies(t, client, got), "\n"); !strings.Contains(pr, "touch grain's trust boundary themselves") {
		t.Errorf("the pull request does not say why the reviewer's fix is reviewed again: %s", pr)
	}

	completeReview(t, ctx, store, second)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := reloaded(t, ctx, store, task.ID); !got.AutoMerge || linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("a clean second review did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}
