package orchestrator_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// A change touching both a path of grain's own list that only a human may
// release and a repository's gating trigger that a clean review releases
// is held for the human: the trigger settling clean is not the Submit the
// trust-boundary hold waits for.
func TestPathTriggerDoesNotReleaseAHumanOnlyHold(t *testing.T) {
	t.Parallel()
	ctx, store, _, client, task := finishGated(t,
		[]model.PathTrigger{gateTrigger("secrets", model.TriggerHumanOnFindings, "config/secrets")},
		"config/secrets/prod.yml", "pkg/trustboundary/x.go")

	got := reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Fatal("a change touching a human-only trust-boundary path kept auto-merge")
	}
	if linkOf(got, model.LinkHumanOnlyHold) == "" {
		t.Fatalf("a human-only hold carries no marker of its own: links %+v", got.Links)
	}
	if linkOf(got, model.LinkSecurityHold) != "" {
		t.Fatalf("a human-only hold carries a releasable hold link: links %+v", got.Links)
	}
	ids := gatingIDs(got)
	if len(ids) == 0 {
		t.Fatal("the gating trigger filed nothing")
	}
	for _, id := range ids {
		completeReview(t, ctx, store, id)
	}
	syncTriggerHolds(t, ctx, store, client, baseTime.Add(time.Hour))

	got = reloaded(t, ctx, store, task.ID)
	if got.AutoMerge {
		t.Error("a clean path-trigger review released a change only a human may release")
	}
	if linkOf(got, model.LinkPathTriggerHold) != "" {
		t.Errorf("the path-trigger hold was not settled: links %+v", got.Links)
	}
	if linkOf(got, model.LinkHumanOnlyHold) == "" {
		t.Errorf("settling the path trigger dropped the human-only hold: links %+v", got.Links)
	}
}

// The owner answering a review's questions sends the run that carries out
// the answer, but that answer is not the Submit a human-only
// trust-boundary hold waits for: auto-merge stays off.
func TestAnsweringQuestionsDoesNotReleaseAHumanOnlyHold(t *testing.T) {
	t.Parallel()
	store, ctx, _, reviewed, _ := heldOnQuestions(t)
	if err := store.UpdateTask(ctx, reviewed.ID, func(t *model.Task) error {
		t.Links = append(t.Links, model.Link{Kind: model.LinkHumanOnlyHold, Target: "trust-boundary/v7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	say(t, ctx, store, reviewed.ID, owner, "Agreed, keep it core-only.", baseTime.Add(time.Minute))
	if err := orchestrator.SyncReviewQuestions(ctx, store, baseTime.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if s := stateOf(t, ctx, store, reviewed.ID); s != model.StateQueued {
		t.Fatalf("state = %q, want %q -- the reply must still send another run", s, model.StateQueued)
	}
	got := reloaded(t, ctx, store, reviewed.ID)
	if got.AutoMerge {
		t.Error("answering a review's questions released a change only a human may release")
	}
	if linkOf(got, model.LinkHumanOnlyHold) == "" {
		t.Errorf("answering the questions dropped the human-only hold: links %+v", got.Links)
	}
	bodies := commentBodies(t, ctx, store, reviewed.ID)
	if last := bodies[len(bodies)-1]; !strings.Contains(last, "only a human may release") {
		t.Errorf("the run is not told the change still waits for a Submit: %q", last)
	}
}
