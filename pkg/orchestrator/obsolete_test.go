package orchestrator_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// heldTaskWithSim is heldTask, with the simulated forge kept so a test can
// push the held branch on.
func heldTaskWithSim(t *testing.T, ctx context.Context, store *model.Store) (*githubsim.Sim, *github.RESTClient, model.Task, string) {
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
	return sim, client, task, reviewID
}

// rerunHeld is a second run of the held task finishing on a new head that
// still touches the trust boundary -- the requeue that filed five reviews
// of PR #2733.
func rerunHeld(t *testing.T, ctx context.Context, store *model.Store, sim *githubsim.Sim, client github.Client,
	task model.Task) string {

	t.Helper()
	pushFile(t, sim.BareRepo, model.BranchName(task.ID), "pkg/ui/server.go", "changed again\n")
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, reloaded(t, ctx, store, task.ID), result, "t1-2",
		baseTime.Add(time.Hour)); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	return linkOf(reloaded(t, ctx, store, task.ID), model.LinkSecurityReviewTask)
}

func TestSecondSecurityReviewSupersedesTheUnstartedFirst(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, first := heldTaskWithSim(t, ctx, store)

	second := rerunHeld(t, ctx, store, sim, client, task)
	if second == "" || second == first {
		t.Fatalf("no new review was filed for the new head: first=%q second=%q", first, second)
	}
	if st, err := store.State(ctx, first); err != nil || st != model.StateClosed {
		t.Fatalf("first review state = %q (%v), want closed", st, err)
	}
	if got := linkOf(reloaded(t, ctx, store, first), model.LinkSupersededBy); got != second {
		t.Errorf("first review superseded-by = %q, want %q", got, second)
	}
	if got := linkOf(reloaded(t, ctx, store, second), model.LinkSupersedes); got != first {
		t.Errorf("second review supersedes = %q, want %q", got, first)
	}
	if notes := strings.Join(commentBodies(t, ctx, store, first), "\n"); !strings.Contains(notes, "superseded by task "+second) {
		t.Errorf("the first review does not say what replaced it: %s", notes)
	}
	if st, err := store.State(ctx, second); err != nil || st != model.StateQueued {
		t.Errorf("second review state = %q (%v), want queued", st, err)
	}
}

// A review already dispatched once and failed is no use either: it reviews
// a head the change has moved past. It is closed with the same links.
func TestSecondSecurityReviewSupersedesAFailedFirst(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, first := heldTaskWithSim(t, ctx, store)
	if err := store.StartRun(ctx, model.Run{ID: first + "-1", TaskID: first, Sandbox: "s1", Attempt: 1,
		StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, first+"-1", baseTime.Add(2*time.Minute), "failed", "clone failed"); err != nil {
		t.Fatal(err)
	}

	second := rerunHeld(t, ctx, store, sim, client, task)
	if st, err := store.State(ctx, first); err != nil || st != model.StateClosed {
		t.Fatalf("first review state = %q (%v), want closed", st, err)
	}
	if got := linkOf(reloaded(t, ctx, store, first), model.LinkSupersededBy); got != second {
		t.Errorf("first review superseded-by = %q, want %q", got, second)
	}
	if notes := strings.Join(commentBodies(t, ctx, store, first), "\n"); !strings.Contains(notes, "superseded by review "+second) {
		t.Errorf("the first review does not say what replaced it: %s", notes)
	}
}

// A review already running is closed, which is what stops its run, rather
// than left holding a sandbox beside its replacement (tasks 492 and 518).
// And a verdict that arrives from it anyway is about the head it was
// filed for: a clean one does not lift the hold the newer head is under.
func TestCleanReviewOfAnOlderHeadDoesNotReleaseTheNewerHold(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, first := heldTaskWithSim(t, ctx, store)
	if err := store.StartRun(ctx, model.Run{ID: first + "-1", TaskID: first, Sandbox: "s1", Attempt: 1,
		StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	second := rerunHeld(t, ctx, store, sim, client, task)
	if second == first {
		t.Fatal("no new review was filed for the new head")
	}
	if st, err := store.State(ctx, first); err != nil || st != model.StateClosed {
		t.Fatalf("running review state = %q (%v), want it closed so its run is stopped", st, err)
	}
	if got := linkOf(reloaded(t, ctx, store, first), model.LinkSupersededBy); got != second {
		t.Errorf("running review superseded-by = %q, want %q", got, second)
	}
	if notes := strings.Join(commentBodies(t, ctx, store, first), "\n"); !strings.Contains(notes, "run in flight has been stopped") {
		t.Errorf("the running review does not say its run was stopped: %s", notes)
	}

	if err := store.FinishRun(ctx, first+"-1", baseTime.Add(2*time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	completeReview(t, ctx, store, first)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge || linkOf(got, model.LinkSecurityHold) == "" {
		t.Fatalf("a clean review of the older head released the newer one: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}

	completeReview(t, ctx, store, second)
	if err := orchestrator.SyncSecurityHolds(ctx, store, client, baseTime.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("the current head's own clean review did not release it")
	}
}

// A review that finished with its fixes on a stacked pull request reads
// 'completed', which State.Settled counts as settled -- and so was left on
// the merge queue beside its replacement, to merge findings about a head
// the change had moved past (task 475, PR #4573). It is closed, and its
// pull request leaves the queue with it.
func TestSecondSecurityReviewSupersedesACompletedFirstWithAStackedPullRequest(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client, task, first := heldTaskWithSim(t, ctx, store)
	asStackedReview(t, ctx, store, first)
	if err := store.StartRun(ctx, model.Run{ID: first + "-1", TaskID: first, Sandbox: "s1", Attempt: 1,
		StartedAt: baseTime.Add(time.Minute)}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	review := reloaded(t, ctx, store, first)
	reviewBranch := model.BranchName(review.ID)
	// The review's own branch, cut from the one it reviews, with the
	// hardening commit it pushed.
	wd := filepath.Join(t.TempDir(), "work")
	run(t, filepath.Dir(wd), "git", "clone", "-q", "--branch", model.BranchName(task.ID), sim.BareRepo, wd)
	run(t, wd, "git", "checkout", "-q", "-b", reviewBranch)
	run(t, wd, "git", "push", "-q", "origin", reviewBranch)
	pushFile(t, sim.BareRepo, reviewBranch, "pkg/ui/hardening.go", "hardened\n")
	if err := store.FinishRun(ctx, first+"-1", baseTime.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, first+"-1",
		baseTime.Add(2*time.Minute)); err != nil {
		t.Fatalf("ProcessResult (review): %v", err)
	}
	if st, err := store.State(ctx, first); err != nil || st != model.StateCompleted {
		t.Fatalf("review with a stacked pull request state = %q (%v), want completed", st, err)
	}
	if !onMergeQueue(t, ctx, store, first) {
		t.Fatal("the review's stacked pull request is not on the merge queue to begin with")
	}

	second := rerunHeld(t, ctx, store, sim, client, task)
	if second == "" || second == first {
		t.Fatalf("no new review was filed for the new head: first=%q second=%q", first, second)
	}
	if st, err := store.State(ctx, first); err != nil || st != model.StateClosed {
		t.Fatalf("completed review state = %q (%v), want closed", st, err)
	}
	if got := linkOf(reloaded(t, ctx, store, first), model.LinkSupersededBy); got != second {
		t.Errorf("completed review superseded-by = %q, want %q", got, second)
	}
	if onMergeQueue(t, ctx, store, first) {
		t.Error("the superseded review's stacked pull request is still on the merge queue")
	}
}

// onMergeQueue reports whether the merge queue still watches id's pull
// request (Store.OpenPullRequestLinks).
func onMergeQueue(t *testing.T, ctx context.Context, store *model.Store, id string) bool {
	t.Helper()
	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if l.TaskID == id {
			return true
		}
	}
	return false
}

// fileStrayReview files a review of reviewed by hand, the way the older
// reviews of PR #2733 were left behind before supersede existed.
func fileStrayReview(t *testing.T, ctx context.Context, store *model.Store, id string, reviewed model.Task) {
	t.Helper()
	bot := model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}
	if err := store.PutTask(ctx, model.Task{
		ID: id, Title: "Security review: stray", Target: reviewed.Target, Base: model.BranchName(reviewed.ID),
		Intent:   model.IntentImplement,
		Origin:   model.Origin{Attribution: bot, Reason: model.ReasonReview},
		Approval: &bot, Binding: model.BindingDirective,
		Links: []model.Link{{Kind: model.LinkProposedBy, Target: reviewed.ID}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedReviewsAreClosedWhenTheReviewedChangeMerges(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client, task, reviewID := heldTaskWithSim(t, ctx, store)
	fileStrayReview(t, ctx, store, "stray", task)

	settleHeldPullRequest(t, ctx, store, client, task, true, baseTime.Add(time.Hour))
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{reviewID, "stray"} {
		if st, err := store.State(ctx, id); err != nil || st != model.StateClosed {
			t.Fatalf("review %s state = %q (%v), want closed", id, st, err)
		}
	}
	notes := strings.Join(commentBodies(t, ctx, store, "stray"), "\n")
	if !strings.Contains(notes, "task "+task.ID+" merged") || !strings.Contains(notes, "nothing left to review") {
		t.Errorf("the stray review does not say why it closed: %s", notes)
	}
}

// Closed by hand, with no pull request event at all: still found, on the
// next cycle, which is what cleans up reviews already orphaned.
func TestQueuedReviewsAreClosedWhenTheReviewedTaskIsClosed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, _, task, reviewID := heldTaskWithSim(t, ctx, store)
	closed := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, task.ID, closed, func(o *model.Observation) { o.ClosedAt = &closed }); err != nil {
		t.Fatal(err)
	}

	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, reviewID); err != nil || st != model.StateClosed {
		t.Fatalf("review state = %q (%v), want closed", st, err)
	}
	notes := strings.Join(commentBodies(t, ctx, store, reviewID), "\n")
	if !strings.Contains(notes, "task "+task.ID+" closed") || !strings.Contains(notes, "nothing left to review") {
		t.Errorf("the review does not say why it closed: %s", notes)
	}
	// Once: the next cycle has nothing more to say.
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := len(commentBodies(t, ctx, store, reviewID)); n != 1 {
		t.Errorf("review has %d comments after a second cycle, want 1", n)
	}
}

// A review of a task still open is nobody's business here.
func TestReviewsOfAnOpenTaskAreLeftAlone(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, _, _, reviewID := heldTaskWithSim(t, ctx, store)
	if err := orchestrator.SyncReviews(ctx, store, baseTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st, err := store.State(ctx, reviewID); err != nil || st != model.StateQueued {
		t.Fatalf("review state = %q (%v), want queued", st, err)
	}
}
