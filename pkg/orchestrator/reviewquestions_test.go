package orchestrator_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The policy (task 5008): the reviewer fixes everything it can, the change
// then goes through, and only what needs the owner holds it -- as a
// question, which the owner's reply turns into another run.

// say adds a comment to taskID's conversation as who.
func say(t *testing.T, ctx context.Context, store *model.Store, taskID string, who model.Principal, body string, at time.Time) {
	t.Helper()
	if _, err := store.AddComment(ctx, model.Comment{
		TaskID: taskID, Author: model.Attribution{Actor: who}, Body: body, CreatedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
}

var owner = model.Principal{Kind: model.PrincipalHuman, ID: "bsalmon"}

// A review that pushed its fixes and asked nothing: the fixes merge back
// into the reviewed branch and the change itself merges after them, with
// nobody asked anything.
func TestAReviewThatFixesEverythingAndAsksNothingLetsTheChangeMerge(t *testing.T) {
	t.Parallel()
	store, ctx, sim, client, reviewed, review := reviewedAndReviewing(t)

	pushBranch(t, sim.BareRepo, model.BranchName(review.ID))
	result := toolResult(agent.ToolCall{Name: "run_command", Text: "fixed the retry loop and pushed"})
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	for i := 0; i < 3; i++ {
		markMergeable(sim)
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime.Add(time.Duration(i+1)*time.Minute)); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}
	merged := map[string]bool{}
	for _, pr := range sim.PullRequests {
		merged[pr.Head] = pr.Merged
	}
	if !merged[model.BranchName(review.ID)] {
		t.Fatal("the review's fixes did not merge back into the branch it reviewed")
	}
	if !merged[model.BranchName(reviewed.ID)] {
		t.Fatal("a change whose review fixed everything and asked nothing must go through")
	}
	if _, held := model.ReviewQuestionsOf(reloaded(t, ctx, store, reviewed.ID)); held {
		t.Fatal("a review with no findings put questions to the owner")
	}
}

// A gating review on an `on-findings` trigger that fixed what it found:
// once its fixes have merged back, the hold is released.
func TestAGatingReviewWhoseFixesMergedBackReleasesTheHold(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml")
	gateID := gatingIDs(reloaded(t, ctx, store, task.ID))[0]
	pushedReview(t, ctx, store, gateID)

	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))
	if reloaded(t, ctx, store, task.ID).AutoMerge {
		t.Fatal("released while the review's fixes were still open")
	}
	merged := baseTime.Add(2 * time.Hour)
	if err := store.ObserveField(ctx, gateID, merged, func(o *model.Observation) { o.PrMergedAt = &merged }); err != nil {
		t.Fatal(err)
	}
	syncTriggerHolds(t, ctx, store, client, merged)
	got := reloaded(t, ctx, store, task.ID)
	if !got.AutoMerge || linkOf(got, model.LinkPathTriggerHold) != "" {
		t.Fatalf("a review whose fixes merged back did not release the hold: automerge=%v links=%+v", got.AutoMerge, got.Links)
	}
}

// The exception: a change caught by a `human: required` trigger -- this
// repository's `.grain/**` declaration -- waits for its owner's Submit
// however clean its review was, fixes and all.
func TestAHumanRequiredChangeStillHoldsAfterACleanReview(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("declarations", model.TriggerHumanRequired, ".grain")},
		".grain/workflows/ci.yml")
	gateID := gatingIDs(reloaded(t, ctx, store, task.ID))[0]
	pushedReview(t, ctx, store, gateID)
	merged := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, gateID, merged, func(o *model.Observation) { o.PrMergedAt = &merged }); err != nil {
		t.Fatal(err)
	}
	syncTriggerHolds(t, ctx, store, client, merged)
	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("human: required was released by a review")
	}
	if s := stateOf(t, ctx, store, task.ID); s != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want %q", s, model.StateAwaitingSubmit)
	}
}

// heldOnQuestions is a reviewed change whose review left one question.
func heldOnQuestions(t *testing.T) (*model.Store, context.Context, *github.RESTClient, model.Task, model.Task) {
	t.Helper()
	store, ctx, _, client, reviewed, review := reviewedAndReviewing(t)
	result := toolResult(reviewFinding(
		"Should the core build also publish the google image? I recommend not: keep it core-only.",
		"Makefile", 3))
	if err := orchestrator.ProcessResult(ctx, store, client, review, result, review.ID+"-1", baseTime); err != nil {
		t.Fatalf("ProcessResult: %v", err)
	}
	return store, ctx, client, reviewed, review
}

// Findings hold the change as questions for its owner, not as a bare
// Submit: the hold is recorded, and the task says what it is waiting on.
func TestAReviewsFindingsHoldTheChangeAsQuestionsForItsOwner(t *testing.T) {
	t.Parallel()
	store, ctx, _, reviewed, review := heldOnQuestions(t)

	got := reloaded(t, ctx, store, reviewed.ID)
	if got.AutoMerge {
		t.Fatal("a change with open questions kept merging itself")
	}
	if s := stateOf(t, ctx, store, reviewed.ID); s != model.StateAwaitingSubmit {
		t.Fatalf("state = %q, want %q", s, model.StateAwaitingSubmit)
	}
	q, ok := model.ReviewQuestionsOf(got)
	if !ok || q.ReviewTaskID != review.ID || !q.RestoreAutoMerge {
		t.Fatalf("hold = %+v (%v), want review %s restoring auto-merge; links %+v", q, ok, review.ID, got.Links)
	}
	if linkOf(got, model.LinkDraftReview) == "" {
		t.Errorf("the draft review was not recorded: %+v", got.Links)
	}
	bodies := strings.Join(commentBodies(t, ctx, store, reviewed.ID), "\n")
	for _, want := range []string{"Review needs your decision", "1 open question", "I recommend not", "Reply here", "Ship as is"} {
		if !strings.Contains(bodies, want) {
			t.Errorf("the task's conversation lacks %q:\n%s", want, bodies)
		}
	}
}

// The owner's reply is the answer: the held task goes back to the queue,
// with auto-merge restored so the run that carries out the decision goes
// through. Grain's own notes and agents' comments answer nothing.
func TestAnOwnersReplyRedispatchesTheHeldChange(t *testing.T) {
	t.Parallel()
	store, ctx, _, reviewed, _ := heldOnQuestions(t)

	say(t, ctx, store, reviewed.ID, model.Principal{Kind: model.PrincipalAgent, ID: "x"}, "noted", baseTime.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, reviewed.ID); s != model.StateAwaitingSubmit {
		t.Fatalf("a comment that is not the owner's moved the task to %q", s)
	}

	say(t, ctx, store, reviewed.ID, owner, "Agreed, keep it core-only.", baseTime.Add(3*time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, reviewed.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q -- the reply must send another run", s, model.StateQueued)
	}
	got := reloaded(t, ctx, store, reviewed.ID)
	if !got.AutoMerge {
		t.Error("the run carrying out the decision should go through: auto-merge was not restored")
	}
	if _, held := model.ReviewQuestionsOf(got); held {
		t.Error("the answered hold is still recorded")
	}
	bodies := commentBodies(t, ctx, store, reviewed.ID)
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "carries out that decision") {
		t.Errorf("the run is not told what to do with the answer: %q", last)
	}

	// Once is enough: another cycle sends nothing more.
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := len(commentBodies(t, ctx, store, reviewed.ID)); n != len(bodies) {
		t.Errorf("a second cycle said more: %d comments, want %d", n, len(bodies))
	}
}

// A reply on the review itself counts too, and is carried onto the held
// task where the run reads it.
func TestAReplyOnTheReviewRedispatchesTheHeldChange(t *testing.T) {
	t.Parallel()
	store, ctx, _, reviewed, review := heldOnQuestions(t)

	say(t, ctx, store, review.ID, owner, "Keep it core-only.", baseTime.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, reviewed.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q", s, model.StateQueued)
	}
	if bodies := strings.Join(commentBodies(t, ctx, store, reviewed.ID), "\n"); !strings.Contains(bodies, "> Keep it core-only.") {
		t.Errorf("the reply was not carried onto the held task:\n%s", bodies)
	}
}

// Submit is "ship as is": the change merges, nothing is redispatched, and
// the hold is dropped.
func TestSubmittingAChangeHeldOnQuestionsShipsItAsIs(t *testing.T) {
	t.Parallel()
	store, ctx, _, reviewed, _ := heldOnQuestions(t)
	if err := store.UpdateTask(ctx, reviewed.ID, func(tk *model.Task) error {
		tk.AutoMerge = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	say(t, ctx, store, reviewed.ID, owner, "shipping it", baseTime.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, reviewed.ID); s != model.StateCompleted {
		t.Fatalf("state = %q, want %q -- a submitted change is not run again", s, model.StateCompleted)
	}
	if _, held := model.ReviewQuestionsOf(reloaded(t, ctx, store, reviewed.ID)); held {
		t.Error("a submitted change still carries its questions hold")
	}
}
