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

func TestClearAndReplaceGoalClearsNoProgressNotificationAndDeferral(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	self := create(t, c, ctx)
	now := c.Now().UTC()

	decl := model.GoalDecl{
		Kind:    model.GoalBaseGreen,
		Spec:    model.GoalSpec{Repo: "acme/widgets", Branch: "main"},
		OnMet:   model.GoalCloseOnMet,
		OnLimit: model.GoalHoldOnLimit,
	}
	if err := c.SetGoal(ctx, self.ID, decl); err != nil {
		t.Fatalf("SetGoal: %v", err)
	}

	// Simulate reaching the goal progress limit: raise task.no-progress:<id>,
	// record LimitNotifiedAt on the goal, and defer the task.
	subject := model.NotificationSubject(model.FamilyNoProgress, self.ID)
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  subject,
		Family:   model.FamilyNoProgress,
		Severity: model.NotificationWarning,
		Title:    "Task is not making progress",
		Body:     "Held at limit",
		TaskID:   self.ID,
		RaisedBy: "automation:grain",
	}); err != nil {
		t.Fatalf("RaiseNotification: %v", err)
	}
	goal, err := store.TaskGoal(ctx, self.ID)
	if err != nil || goal == nil {
		t.Fatalf("TaskGoal = %v, %v", goal, err)
	}
	goal.LimitNotifiedAt = &now
	if err := store.SaveTaskGoalProgress(ctx, *goal); err != nil {
		t.Fatalf("SaveTaskGoalProgress: %v", err)
	}
	if err := store.SetDeferred(ctx, self.ID, &now, nil, now); err != nil {
		t.Fatalf("SetDeferred: %v", err)
	}

	// Replacing the goal via SetGoal clears the open no-progress notification
	// and un-defers the held task.
	if err := c.SetGoal(ctx, self.ID, model.GoalDecl{Kind: model.GoalPersonCloses}); err != nil {
		t.Fatalf("SetGoal (replace): %v", err)
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open != nil {
		t.Fatalf("OpenNotification after SetGoal = %+v, %v; want nil", open, err)
	}
	if task, err := store.GetTask(ctx, self.ID); err != nil || task.DeferredAt != nil {
		t.Fatalf("DeferredAt after SetGoal = %v (%v), want nil", task.DeferredAt, err)
	}

	// Now simulate the task resting on its goal after a run finishes and
	// raising no-progress again, then ClearGoal (DELETE /api/tasks/{id}/goal).
	if _, err := store.RaiseNotification(ctx, model.Notification{
		Subject:  subject,
		Family:   model.FamilyNoProgress,
		Severity: model.NotificationWarning,
		Title:    "Task is not making progress",
		Body:     "Held at limit",
		TaskID:   self.ID,
		RaisedBy: "automation:grain",
	}); err != nil {
		t.Fatalf("RaiseNotification 2: %v", err)
	}
	if err := store.StartRun(ctx, model.Run{ID: self.ID + "-1", TaskID: self.ID, Sandbox: "s1", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, self.ID+"-1", now, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Suspend(ctx, model.Suspension{
		RunID:     self.ID + "-1",
		TaskID:    self.ID,
		Kind:      model.SuspendRequested,
		CreatedAt: now,
		Detail:    "the agent ended its run; the task rests on its person-closes goal",
		ResumeOn:  (model.ResumeOn{Message: true}).JSON(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeferred(ctx, self.ID, &now, nil, now); err != nil {
		t.Fatal(err)
	}

	if err := c.ClearGoal(ctx, self.ID); err != nil {
		t.Fatalf("ClearGoal: %v", err)
	}
	if open, err := store.OpenNotification(ctx, subject); err != nil || open != nil {
		t.Fatalf("OpenNotification after ClearGoal = %+v, %v; want nil", open, err)
	}
	if task, err := store.GetTask(ctx, self.ID); err != nil || task.DeferredAt != nil {
		t.Fatalf("DeferredAt after ClearGoal = %v (%v), want nil", task.DeferredAt, err)
	}
}

// TestATasksOwnCommentDoesNotWakeItFromAMessageHold pins that a run
// parked on resume_later(when: {message: true}) waits for somebody
// else's post: its own comment through the agent API, which now carries
// the source the gate established it by, still counts as its own.
func TestATasksOwnCommentDoesNotWakeItFromAMessageHold(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	self := create(t, c, ctx)
	now := c.Now().UTC()
	if err := store.StartRun(ctx, model.Run{ID: self.ID + "-1", TaskID: self.ID, Sandbox: "s1", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, self.ID+"-1", now, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Suspend(ctx, model.Suspension{
		RunID: self.ID + "-1", TaskID: self.ID, Kind: model.SuspendRequested, CreatedAt: now,
		Detail: "resume_later(when: {message: true})", ResumeOn: (model.ResumeOn{Message: true}).JSON(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeferred(ctx, self.ID, &now, nil, now); err != nil {
		t.Fatal(err)
	}

	for _, src := range []model.AuthSource{model.SourceSandbox, model.SourceSession, ""} {
		own := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: self.ID, Source: src})
		if err := c.AddComment(own, self.ID, "a note to myself", nil); err != nil {
			t.Fatalf("AddComment as its own agent (source %q): %v", src, err)
		}
		if task, err := store.GetTask(ctx, self.ID); err != nil || task.DeferredAt == nil {
			t.Fatalf("after its own comment (source %q): DeferredAt = %v (%v), want still deferred", src, task.DeferredAt, err)
		}
	}

	other := ui.WithPrincipal(ctx, model.Principal{Kind: model.PrincipalAgent, ID: "someone-else", Source: model.SourceSandbox})
	if err := c.AddComment(other, self.ID, "a message from another task", nil); err != nil {
		t.Fatalf("AddComment as another agent: %v", err)
	}
	if task, err := store.GetTask(ctx, self.ID); err != nil || task.DeferredAt != nil {
		t.Fatalf("after another task's comment: DeferredAt = %v (%v), want woken", task.DeferredAt, err)
	}
}
