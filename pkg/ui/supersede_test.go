package ui_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The whole of the pattern this exists for, through the live filing path
// a chat session's propose_task call takes (grain/task-473): a design is
// refined by re-filing it, and the revision it replaces stops being
// dispatchable the moment the replacement exists rather than when an
// operator gets to the close button.
func TestProposalSupersedesTheTaskItReplaces(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	director, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "director", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "merge queue design, revision four", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Written "#468"-style, which is how agents still spell a task
	// reference, and must resolve the same as the bare id.
	replacement, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "merge queue design, revision five", ProposedBy: director.ID,
		Supersedes: "#" + stale.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Supersedes != stale.ID {
		t.Errorf("supersedes = %q on the replacement, want %s", replacement.Supersedes, stale.ID)
	}

	closed, err := client.Task(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != model.StateClosed {
		t.Errorf("state of the replaced task = %s, want closed", closed.State)
	}
	if closed.SupersededBy != replacement.ID {
		t.Errorf("supersededBy = %q, want the replacement (%s)", closed.SupersededBy, replacement.ID)
	}
	if strings.Contains(replacement.Description, "still open") {
		t.Errorf("description = %q, want no refusal note on a supersede that happened",
			replacement.Description)
	}
	comments, err := store.Comments(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, replacement.ID) {
		t.Errorf("conversation on the closed task = %+v, want one comment naming %s",
			comments, replacement.ID)
	}
}

// And the bound, from the side a run meets it: a task with a run behind
// it is left exactly as it was, the proposal is filed anyway, and the
// reason is on the new task where whoever approves it will read it. The
// close is what gets handed back to a human, not the work.
func TestProposalSupersedingADispatchedTaskLeavesItOpenAndSaysSo(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	director, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "director", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	running, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "work an agent is already doing", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: running.ID + "-1", TaskID: running.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}

	replacement, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "the same work, restated", Description: "the agent's own words",
		ProposedBy: director.ID, Supersedes: running.ID,
	})
	if err != nil {
		t.Fatalf("filing the proposal failed over a supersede it could not do: %v", err)
	}
	if replacement.Supersedes != "" {
		t.Errorf("supersedes = %q, want no link recorded for a supersede that did not happen",
			replacement.Supersedes)
	}
	if !strings.Contains(replacement.Description, "still open") ||
		!strings.Contains(replacement.Description, running.ID) {
		t.Errorf("description = %q, want a note naming the task left open and why",
			replacement.Description)
	}

	left, err := client.Task(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if left.State == model.StateClosed {
		t.Error("a task with a run behind it was closed by a propose_task call")
	}
	comments, err := store.Comments(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 0 {
		t.Errorf("conversation on the untouched task = %+v, want nothing written to it", comments)
	}
}

// A supersede reaches no further than a proposal itself does: it closes
// a task in the repository the replacement lands in, and nothing in
// another one.
func TestProposalCannotSupersedeATaskInAnotherRepository(t *testing.T) {
	t.Parallel()
	client, _, ctx := testClient(t)
	elsewhere, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "work in the other repo", Repo: "acme/gadgets", Approved: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "a replacement filed here", Repo: "acme/widgets", Supersedes: elsewhere.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Supersedes != "" {
		t.Errorf("supersedes = %q across repositories, want none", replacement.Supersedes)
	}
	left, err := client.Task(ctx, elsewhere.ID)
	if err != nil {
		t.Fatal(err)
	}
	if left.State == model.StateClosed {
		t.Error("a task in another repository was closed by a proposal filed here")
	}
}

func TestProposalSupersedesNeverDispatchedDeferredTask(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	director, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "director", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	wake := baseTime.Add(time.Hour)
	stale, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "scheduled coordinator, revision one", Approved: true, DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stale.State != model.StateDeferred {
		t.Fatalf("stale state = %s, want deferred", stale.State)
	}

	replacement, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "scheduled coordinator, revision two", ProposedBy: director.ID,
		Supersedes: stale.ID, DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Supersedes != stale.ID {
		t.Errorf("supersedes = %q on the replacement, want %s", replacement.Supersedes, stale.ID)
	}
	if strings.Contains(replacement.Description, "still open") {
		t.Errorf("description = %q, want no refusal note on a supersede that happened",
			replacement.Description)
	}
	closed, err := client.Task(ctx, stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.State != model.StateClosed {
		t.Errorf("state of the replaced deferred task = %s, want closed", closed.State)
	}
	if closed.SupersededBy != replacement.ID {
		t.Errorf("supersededBy = %q, want %s", closed.SupersededBy, replacement.ID)
	}

	// A deferred task with attempts > 0 is still refused.
	dispatchedDeferred, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "deferred after an attempt", Approved: true, DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: dispatchedDeferred.ID + "-1", TaskID: dispatchedDeferred.ID,
		Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, dispatchedDeferred.ID+"-1", baseTime, "failed", "err"); err != nil {
		t.Fatal(err)
	}
	refusedAttempt, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "restated after attempt", ProposedBy: director.ID, Supersedes: dispatchedDeferred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refusedAttempt.Supersedes != "" || !strings.Contains(refusedAttempt.Description, "still open") {
		t.Errorf("superseding deferred task with attempt: supersedes=%q desc=%q, want refused",
			refusedAttempt.Supersedes, refusedAttempt.Description)
	}
	if left, err := client.Task(ctx, dispatchedDeferred.ID); err != nil || left.State != model.StateDeferred {
		t.Errorf("deferred task with attempt state = (%v, %v), want deferred", left, err)
	}

	// A deferred task with a LinkFixes PR is still refused.
	withPRDeferred, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "deferred with open PR", Approved: true, DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateTask(ctx, withPRDeferred.ID, func(tk *model.Task) error {
		tk.Links = append(tk.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#99"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	refusedPR, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "restated with PR", ProposedBy: director.ID, Supersedes: withPRDeferred.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if refusedPR.Supersedes != "" || !strings.Contains(refusedPR.Description, "acme/widgets#99") {
		t.Errorf("superseding deferred task with PR: supersedes=%q desc=%q, want refused naming PR",
			refusedPR.Supersedes, refusedPR.Description)
	}
	if left, err := client.Task(ctx, withPRDeferred.ID); err != nil || left.State != model.StateDeferred {
		t.Errorf("deferred task with PR state = (%v, %v), want deferred", left, err)
	}
}

// A proposal that supersedes a task with tasks waiting on it takes them
// over: they wait on the replacement now, rather than being released by
// the close, and the answer to the filing call says which moved and
// which stayed -- the replacement's own wait on the task it replaces
// among them -- so a propose_task call can tell its run.
func TestProposalSupersedingATaskMovesItsDependentsOntoTheReplacement(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	director, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "director", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "revision four", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "follow-up to revision four", Approved: true, DependsOn: []string{stale.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	replacement, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "revision five", ProposedBy: director.ID, Supersedes: stale.ID,
		DependsOn: []string{stale.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Supersedes != stale.ID {
		t.Fatalf("supersedes = %q, want %s", replacement.Supersedes, stale.ID)
	}
	if len(replacement.SupersedeMoved) != 1 || replacement.SupersedeMoved[0] != waiting.ID {
		t.Errorf("supersedeMoved = %v, want [%s]", replacement.SupersedeMoved, waiting.ID)
	}
	if len(replacement.SupersedeKept) != 1 || replacement.SupersedeKept[0].ID != replacement.ID {
		t.Errorf("supersedeKept = %+v, want the replacement itself", replacement.SupersedeKept)
	}

	n, err := store.OpenBlockers(ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("open blockers on the dependent = %d, want 1: it waits on the replacement now", n)
	}
	moved, err := store.GetTask(ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range moved.Links {
		if l.Kind == model.LinkDependsOn && l.Target == stale.ID {
			t.Errorf("dependent links = %+v, want its link to %s moved", moved.Links, stale.ID)
		}
	}

	if err := client.Close(ctx, replacement.ID, ui.CloseOptions{}); err != nil {
		t.Fatal(err)
	}
	if n, err := store.OpenBlockers(ctx, waiting.ID); err != nil || n != 0 {
		t.Errorf("open blockers once the replacement closed = %d (%v), want 0", n, err)
	}
}
