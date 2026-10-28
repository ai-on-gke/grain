package ui_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// WaitOn refuses every wait nothing could end, with the picker's own
// refusals: the task itself, an unknown id, a standing chat session, and
// a task that already waits on the one asking.
func TestWaitOnRefusesWaitsThatCannotEnd(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	self := create(t, c, ctx)
	session := sessionTask("451")
	if err := store.PutTask(ctx, session); err != nil {
		t.Fatal(err)
	}
	waiter := create(t, c, ctx)
	if err := c.SetDependency(ctx, waiter.ID, self.ID, true); err != nil {
		t.Fatal(err)
	}

	for name, ids := range map[string][]string{
		"itself":       {self.ID},
		"unknown":      {"99999"},
		"chat session": {session.ID},
		"cycle":        {waiter.ID},
		"nothing":      {" "},
	} {
		for _, park := range []bool{false, true} {
			var ve *ui.ValidationError
			if _, err := c.WaitOn(ctx, self.ID, ids, park); !errors.As(err, &ve) {
				t.Errorf("%s (park %v): error = %v, want a ValidationError", name, park, err)
			}
		}
	}
	got, err := c.Task(ctx, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DependsOn) != 0 {
		t.Fatalf("a refused wait left dependencies %v", got.DependsOn)
	}
}

// Looking changes nothing; parking adds the depends-on links and says so
// in the task's conversation, once however often it is asked.
func TestWaitOnParksAsDependsOnLinks(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	self := create(t, c, ctx)
	target := create(t, c, ctx)

	targets, err := c.WaitOn(ctx, self.ID, []string{"#" + target.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].ID != target.ID || targets[0].Closed || targets[0].Title != target.Title {
		t.Fatalf("targets = %+v, want %s open", targets, target.ID)
	}
	if got, _ := c.Task(ctx, self.ID); len(got.DependsOn) != 0 {
		t.Fatalf("looking added dependencies %v", got.DependsOn)
	}

	for range 2 {
		if _, err := c.WaitOn(ctx, self.ID, []string{target.ID}, true); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.Task(ctx, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Blocked || len(got.DependsOn) != 1 || got.DependsOn[0] != target.ID {
		t.Fatalf("parked task = %+v, want blocked on %s alone", got, target.ID)
	}
	// Parked only once a run has ended on the park.
	if got.Parked {
		t.Fatal("task reads parked with no parked run behind it")
	}
	if err := store.StartRun(ctx, model.Run{ID: self.ID + "-1", TaskID: self.ID, Sandbox: "s", Attempt: 1,
		StartedAt: time.Now().UTC()}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, self.ID+"-1", time.Now().UTC(), model.ParkedOutcome,
		model.ParkedDetail([]string{target.ID})); err != nil {
		t.Fatal(err)
	}
	if detail, err := c.GetTask(ctx, self.ID); err != nil || !detail.Parked {
		t.Fatalf("after a parked run: parked = %v (%v), want true", detail.Parked, err)
	}
	comments, err := store.Comments(ctx, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "#"+target.ID) {
		t.Fatalf("comments = %+v, want one naming #%s", comments, target.ID)
	}
}

// A closed target is described with how it ended and its runs' closing
// note -- never a person's comment, which is not the task's to report.
func TestWaitOnDescribesHowATargetEnded(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	self := create(t, c, ctx)
	target := create(t, c, ctx)
	now := time.Now().UTC()
	agent := model.Principal{Kind: model.PrincipalAgent, ID: target.ID}
	for _, cm := range []model.Comment{
		{TaskID: target.ID, Body: "image published", CreatedAt: now,
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, OnBehalfOf: &agent}},
		{TaskID: target.ID, Body: "thanks!", CreatedAt: now.Add(time.Second),
			Author: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "someone"}}},
	} {
		if _, err := store.AddComment(ctx, cm); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ObserveField(ctx, target.ID, now, func(o *model.Observation) {
		o.CompletedAt = &now
		o.PrMergedAt = &now
		o.ClosedAt = &now
	}); err != nil {
		t.Fatal(err)
	}

	targets, err := c.WaitOn(ctx, self.ID, []string{target.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := targets[0]
	if !got.Closed || !got.Merged || got.Outcome != model.WaitOutcomeMerged || got.ClosingNote != "image published" {
		t.Fatalf("target = %+v, want closed, merged, with the agent's closing note", got)
	}
	if settled, met := got.Settled(true); !settled || !met {
		t.Fatalf("Settled(merged) = %v, %v, want met", settled, met)
	}
}

// ParkUntil defers the task with the wake time asked for and says so in
// its conversation; a time already past, or one further off than a park
// may last, is refused and leaves the task as it was.
func TestParkUntilDefersTheTaskWithItsWakeTime(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	self := create(t, c, ctx)

	for name, until := range map[string]time.Time{
		"past":     c.Now().Add(-time.Minute),
		"too long": c.Now().Add(ui.MaxParkUntil + time.Hour),
	} {
		var ve *ui.ValidationError
		if _, err := c.ParkUntil(ctx, self.ID, until); !errors.As(err, &ve) {
			t.Errorf("%s: error = %v, want a ValidationError", name, err)
		}
	}
	if task, _ := store.GetTask(ctx, self.ID); task.DeferredAt != nil {
		t.Fatalf("a refused park deferred the task at %v", task.DeferredAt)
	}

	want := c.Now().Add(time.Hour).Truncate(time.Second).UTC()
	got, err := c.ParkUntil(ctx, self.ID, want)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(want) {
		t.Fatalf("parked until %v, want %v", got, want)
	}
	task, err := store.GetTask(ctx, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.DeferredAt == nil || task.DeferredUntil == nil || !task.DeferredUntil.Equal(want) {
		t.Fatalf("deferred at %v until %v, want deferred until %v", task.DeferredAt, task.DeferredUntil, want)
	}
	comments, err := store.Comments(ctx, self.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) == 0 || !strings.Contains(comments[len(comments)-1].Body, "parked it until "+want.Format(time.RFC3339)) {
		t.Fatalf("comments = %+v, want one saying the task is parked until %s", comments, want.Format(time.RFC3339))
	}
}
