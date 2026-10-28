package ui_test

// Submit is a human's answer to every hold a change waits under (task
// 441): it drops them there and then rather than leaving them for a later
// cycle, puts a change the merge queue had given up on back on it, and
// marks the decision for the orchestrator to pin to the head it reads next
// (model.LinkHumanSubmitted, orchestrator's securityhold_submit.go).

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func heldAndBlocked(t *testing.T, ctx context.Context, store *model.Store, id string) {
	t.Helper()
	if err := store.UpdateTask(ctx, id, func(task *model.Task) error {
		task.AutoMerge = false
		task.Links = append(task.Links,
			model.Link{Kind: model.LinkSecurityHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkHumanOnlyHold, Target: "trust-boundary/v5"},
			model.Link{Kind: model.LinkSecurityReviewTask, Target: "551"},
			model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "trust-boundary/v5@stale"},
			model.Link{Kind: model.LinkBrokenRulesHold, Target: "stale"},
			model.Link{Kind: model.LinkReviewQuestions, Target: "551:3"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	blockedAt := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC)
	if err := store.ObserveField(ctx, id, blockedAt, func(o *model.Observation) {
		o.MergeQueueBlockedAt = &blockedAt
		o.MergeQueueBlockedBase, o.MergeQueueRearms = "0560ddf5", 2
		o.MergeQueueState, o.MergeQueueNote = "blocked", "merge failed"
	}); err != nil {
		t.Fatal(err)
	}
}

func requireSubmitted(t *testing.T, ctx context.Context, store *model.Store, id string) {
	t.Helper()
	got, err := store.GetTask(ctx, id)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v", id, err)
	}
	if !got.AutoMerge {
		t.Fatal("auto-merge is still off after Submit")
	}
	links := map[model.LinkKind]string{}
	for _, l := range got.Links {
		links[l.Kind] = l.Target
	}
	for _, kind := range []model.LinkKind{model.LinkSecurityHold, model.LinkHumanOnlyHold, model.LinkBrokenRulesHold,
		model.LinkReviewQuestions} {
		if target, ok := links[kind]; ok {
			t.Errorf("%s = %q survived Submit", kind, target)
		}
	}
	if links[model.LinkHumanSubmitted] == "" {
		t.Errorf("Submit left no %s marker for the orchestrator to pin: links=%+v", model.LinkHumanSubmitted, got.Links)
	}
	// What the review was and what it cleared stay for the orchestrator
	// to judge against the head it reads.
	if links[model.LinkSecurityReviewTask] != "551" {
		t.Errorf("the security review link went with the hold: links=%+v", got.Links)
	}
	obs, err := store.GetObservation(ctx, id)
	if err != nil || obs == nil {
		t.Fatalf("reading %s's observation: %v", id, err)
	}
	if obs.MergeQueueBlockedAt != nil || obs.MergeQueueBlockedBase != "" || obs.MergeQueueRearms != 0 ||
		obs.MergeQueueState != "" || obs.MergeQueueNote != "" {
		t.Errorf("the merge queue block survived Submit: %+v", obs)
	}
}

func TestSubmitClearsHoldsAndTheMergeQueueBlock(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)

	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	requireSubmitted(t, ctx, store, task.ID)
}

// Turning auto-merge on by editing the task is Submit by another name.
func TestTurningAutoMergeOnClearsHoldsAndTheMergeQueueBlock(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)

	on := true
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{AutoMerge: &on}); err != nil {
		t.Fatal(err)
	}
	requireSubmitted(t, ctx, store, task.ID)
}

// A Submit over a change already merging itself -- pressed to retry its
// CI -- is no decision about a head the human has looked at, so it marks
// nothing for the orchestrator to pin: a head that moved since its
// clearance is classified again rather than cleared by the press.
func TestSubmittingAnAutoMergingChangePinsNothing(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	if err := store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
		task.AutoMerge = true
		task.Links = append(task.Links,
			model.Link{Kind: model.LinkTrustBoundaryCleared, Target: "trust-boundary/v5@older"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v", task.ID, err)
	}
	cleared := ""
	for _, l := range got.Links {
		if l.Kind == model.LinkHumanSubmitted {
			t.Fatalf("a Submit over an auto-merging change was marked to pin: links=%+v", got.Links)
		}
		if l.Kind == model.LinkTrustBoundaryCleared {
			cleared = l.Target
		}
	}
	if cleared != "trust-boundary/v5@older" {
		t.Fatalf("the recorded clearance changed to %q; it is the orchestrator's to judge", cleared)
	}
}

// An edit that leaves auto-merge where it was says nothing about holds.
func TestAnEditLeavingAutoMergeOffKeepsTheHolds(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)

	off := false
	if _, err := c.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{AutoMerge: &off}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	held := false
	for _, l := range got.Links {
		if l.Kind == model.LinkHumanSubmitted {
			t.Fatalf("an edit leaving auto-merge off was read as a Submit: links=%+v", got.Links)
		}
		held = held || l.Kind == model.LinkSecurityHold
	}
	if !held {
		t.Fatalf("the security hold went with an edit that did not submit: links=%+v", got.Links)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil || obs == nil || obs.MergeQueueBlockedAt == nil {
		t.Fatalf("the merge queue block went with an edit that did not submit: %+v, %v", obs, err)
	}
}

// Submit records the head the human was shown -- here the commit grain CI
// last judged, this client having no GitHub to ask -- for the
// orchestrator to pin the decision to that head and no later one (task
// 979: a push between the Submit and the next cycle took the clearance).
func TestSubmitRecordsTheHeadTheHumanWasShown(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	task := create(t, c, ctx)
	heldAndBlocked(t, ctx, store, task.ID)
	if err := store.UpdateTask(ctx, task.ID, func(task *model.Task) error {
		task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#7"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, time.Now(), func(o *model.Observation) {
		o.CI = &model.CIStatus{RunID: "run-full", Commit: "abcdef0123"}
	}); err != nil {
		t.Fatal(err)
	}

	if err := c.Submit(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v", task.ID, err)
	}
	for _, l := range got.Links {
		if l.Kind == model.LinkHumanSubmitted {
			if head := model.HumanSubmittedHead(l.Target); head != "abcdef0123" {
				t.Fatalf("Submit recorded head %q (%q), want abcdef0123", head, l.Target)
			}
			return
		}
	}
	t.Fatalf("Submit left no %s marker: links=%+v", model.LinkHumanSubmitted, got.Links)
}
