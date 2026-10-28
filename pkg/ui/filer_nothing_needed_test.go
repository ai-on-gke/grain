package ui_test

// grain/task-1599: NothingNeededAction on the things that file tasks --
// templates and schedules -- from the API's side: saved, edited, carried
// onto what they file, and refused when it names neither choice.

import (
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func TestATemplatesNothingNeededActionIsSavedEditedAndFiled(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Sweep", Title: "Sweep for stale flags", NothingNeededAction: "ack",
	})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if tmpl.NothingNeededAction != "ack" {
		t.Errorf("the response says %q, want ack", tmpl.NothingNeededAction)
	}
	stored, err := store.GetTemplate(ctx, tmpl.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NothingNeededAction != model.NothingNeededAck {
		t.Errorf("the stored template says %q, want ack", stored.NothingNeededAction)
	}

	// Filed straight from the template, the task takes its setting.
	task, err := c.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Repo: "acme/widgets"})
	if err != nil {
		t.Fatalf("CreateTask from the template: %v", err)
	}
	if task.NothingNeededAction != "ack" {
		t.Errorf("the task filed from the template says %q, want ack", task.NothingNeededAction)
	}

	// An empty edit is meaningful: it puts the template back on close.
	empty := ""
	updated, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{NothingNeededAction: &empty})
	if err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	if updated.NothingNeededAction != "" {
		t.Errorf("after clearing, the response says %q, want empty (close)", updated.NothingNeededAction)
	}

	bad := "delete"
	_, err = c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{NothingNeededAction: &bad})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("UpdateTemplate with %q: error = %v, want a ValidationError", bad, err)
	}
	_, err = c.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "x", Title: "x", NothingNeededAction: bad})
	if !errors.As(err, &ve) {
		t.Errorf("CreateTemplate with %q: error = %v, want a ValidationError", bad, err)
	}
}

func TestAnInlineSchedulesNothingNeededActionIsSavedEditedAndPreviewed(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Sweep", Repo: "acme/widgets", Recurrence: everyDay, NothingNeededAction: "ack",
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if sched.NothingNeededAction != "ack" {
		t.Errorf("the response says %q, want ack", sched.NothingNeededAction)
	}
	stored, err := store.GetSchedule(ctx, sched.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	if stored.NothingNeededAction != model.NothingNeededAck {
		t.Errorf("the stored schedule says %q, want ack", stored.NothingNeededAction)
	}

	comp, err := c.ScheduleComposition(ctx, sched.ID)
	if err != nil || len(comp.Tasks) != 1 {
		t.Fatalf("composing: (%+v, %v)", comp, err)
	}
	wantField(t, comp.Tasks[0], "nothingNeeded", "schedule", "ack")

	close := "close"
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{NothingNeededAction: &close})
	if err != nil {
		t.Fatalf("UpdateSchedule: %v", err)
	}
	if updated.NothingNeededAction != "close" {
		t.Errorf("after the edit the response says %q, want close", updated.NothingNeededAction)
	}

	bad := "delete"
	_, err = c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{NothingNeededAction: &bad})
	var ve *ui.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("UpdateSchedule with %q: error = %v, want a ValidationError", bad, err)
	}
}

// A template-backed schedule shows its template's setting, the way it
// shows the rest of the template's content.
func TestATemplateBackedScheduleShowsItsTemplatesNothingNeededAction(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Sweep", Title: "Sweep for stale flags", NothingNeededAction: "ack",
	})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}
	if sched.NothingNeededAction != "ack" {
		t.Errorf("the schedule says %q, want its template's ack", sched.NothingNeededAction)
	}
	comp, err := c.ScheduleComposition(ctx, sched.ID)
	if err != nil || len(comp.Tasks) != 1 {
		t.Fatalf("composing: (%+v, %v)", comp, err)
	}
	wantField(t, comp.Tasks[0], "nothingNeeded", "template", "ack")
}
