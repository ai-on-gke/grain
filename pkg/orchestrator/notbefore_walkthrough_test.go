package orchestrator_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// The 2026-10-02 test plan's §4 not_before walkthrough, the half the
// demo cannot show because it runs no orchestrator: two tasks filed
// through ui.Client.CreateTask (what propose_task's live filer and
// POST /api/tasks both reach) with a wake time two minutes ahead, one
// approved and one waiting for a human. The cycle's own "wake"
// reconciler leaves both alone before that time and, after it, puts the
// approved one on the queue and the other back into proposed.
func TestANearFutureNotBeforeWakesIntoTheStateItWasFiledIn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	client := ui.NewClient(ui.Config{
		Actor:        ui.DefaultActor("alice"),
		Capabilities: ui.OfferedCapabilities(),
	}, store)
	client.Now = func() time.Time { return baseTime }
	wake := baseTime.Add(2 * time.Minute)

	approved, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "approved, not before two minutes", Repo: "acme/widgets", Approved: true, DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "unapproved, not before two minutes", Repo: "acme/widgets", DeferredUntil: &wake,
	})
	if err != nil {
		t.Fatal(err)
	}

	runWake := func(now time.Time) {
		t.Helper()
		deps := orchestrator.Deps{Store: store}
		for _, r := range orchestrator.Reconcilers() {
			if r.Name == "wake" {
				if err := r.Reconcile(ctx, deps, now); err != nil {
					t.Fatalf("wake reconciler: %v", err)
				}
				return
			}
		}
		t.Fatal(`no "wake" reconciler in the cycle`)
	}
	stateOf := func(id string) model.State {
		t.Helper()
		state, err := store.State(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}

	runWake(baseTime.Add(time.Minute))
	for _, id := range []string{approved.ID, waiting.ID} {
		if got := stateOf(id); got != model.StateDeferred {
			t.Fatalf("task %s is %q a minute before its wake time, want deferred", id, got)
		}
	}
	runWake(baseTime.Add(3 * time.Minute))
	if got := stateOf(approved.ID); got != model.StateQueued {
		t.Fatalf("the approved task woke into %q, want queued", got)
	}
	if got := stateOf(waiting.ID); got != model.StateProposed {
		t.Fatalf("the unapproved task woke into %q, want proposed", got)
	}
}
