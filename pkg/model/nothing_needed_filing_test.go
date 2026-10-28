package model_test

// Task.NothingNeededAction carried by the things that file tasks for
// somebody: a template and a schedule store it, and a firing copies it
// onto the task it files, so a recurring sweep that mostly finds nothing
// to do can choose "ack" (or an explicit "close") once for every run.

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

var nothingNeededActions = []model.NothingNeededAction{"", model.NothingNeededClose, model.NothingNeededAck}

func TestATemplatesNothingNeededActionRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	for _, action := range nothingNeededActions {
		id := "template-nn-" + string(action)
		if err := store.PutTemplate(ctx, model.Template{
			ID: id, Name: id, Title: "Sweep", CreatedAt: firstNight, NothingNeededAction: action,
		}); err != nil {
			t.Fatalf("writing %s: %v", id, err)
		}
		got, err := store.GetTemplate(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("reading %s: (%+v, %v)", id, got, err)
		}
		if got.NothingNeededAction != action {
			t.Errorf("%s: NothingNeededAction = %q, want %q", id, got.NothingNeededAction, action)
		}
	}
}

func TestASchedulesNothingNeededActionRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	for _, action := range nothingNeededActions {
		id := "sched-nn-" + string(action)
		if err := store.PutSchedule(ctx, model.Schedule{
			ID:                  id,
			Title:               "Sweep",
			Target:              nightlyRepo,
			Recurrence:          model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
			Enabled:             true,
			NextRunAt:           firstNight,
			CreatedAt:           firstNight,
			NothingNeededAction: action,
		}); err != nil {
			t.Fatalf("writing %s: %v", id, err)
		}
		got, err := store.GetSchedule(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("reading %s: (%+v, %v)", id, got, err)
		}
		if got.NothingNeededAction != action {
			t.Errorf("%s: NothingNeededAction = %q, want %q", id, got.NothingNeededAction, action)
		}
	}
}

func fireForTask(t *testing.T, store *model.Store, ctx context.Context, sched model.Schedule) *model.Task {
	t.Helper()
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("writing the schedule: %v", err)
	}
	firing, err := store.FireSchedule(ctx, sched, firstNight, time.UTC, model.RepoRef{})
	if err != nil || firing.TaskID == "" {
		t.Fatalf("firing %s: (%+v, %v), want a filed task", sched.ID, firing, err)
	}
	task, err := store.GetTask(ctx, firing.TaskID)
	if err != nil || task == nil {
		t.Fatalf("reading the filed task: (%+v, %v)", task, err)
	}
	return task
}

// An inline schedule's own setting lands on the task it files, and an
// unset one stays unset -- which the task reads as close.
func TestAnInlineScheduleFilesItsNothingNeededAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	for _, action := range nothingNeededActions {
		task := fireForTask(t, store, ctx, model.Schedule{
			ID:                  "sched-inline-" + string(action),
			Title:               "Weekly sweep",
			Target:              nightlyRepo,
			Recurrence:          model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
			Enabled:             true,
			NextRunAt:           firstNight,
			CreatedAt:           firstNight.Add(-24 * time.Hour),
			NothingNeededAction: action,
		})
		if task.NothingNeededAction != action {
			t.Errorf("schedule saying %q filed a task with NothingNeededAction = %q", action, task.NothingNeededAction)
		}
	}
}

// A template-backed schedule files the template's setting, not whatever
// its own display cache held, and the firing brings that cache up to
// date the way it does for the template's other content.
func TestATemplateBackedScheduleFilesTheTemplatesNothingNeededAction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	if err := store.PutTemplate(ctx, model.Template{
		ID: "template-sweep", Name: "Sweep", Title: "Weekly sweep", CreatedAt: firstNight,
		NothingNeededAction: model.NothingNeededAck,
	}); err != nil {
		t.Fatalf("writing the template: %v", err)
	}
	templateID := "template-sweep"
	task := fireForTask(t, store, ctx, model.Schedule{
		ID:                  "sched-from-template",
		Title:               "Weekly sweep",
		Target:              nightlyRepo,
		TemplateID:          &templateID,
		Recurrence:          model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Enabled:             true,
		NextRunAt:           firstNight,
		CreatedAt:           firstNight.Add(-24 * time.Hour),
		NothingNeededAction: model.NothingNeededClose,
	})
	if task.NothingNeededAction != model.NothingNeededAck {
		t.Errorf("filed task's NothingNeededAction = %q, want the template's %q",
			task.NothingNeededAction, model.NothingNeededAck)
	}
	sched, err := store.GetSchedule(ctx, "sched-from-template")
	if err != nil || sched == nil {
		t.Fatalf("re-reading the schedule: (%+v, %v)", sched, err)
	}
	if sched.NothingNeededAction != model.NothingNeededAck {
		t.Errorf("schedule's cached NothingNeededAction = %q, want the template's %q after firing",
			sched.NothingNeededAction, model.NothingNeededAck)
	}
}

// A suite carries no setting of its own: each of its tasks takes its
// template's, the same as it takes the template's network.
func TestASuiteTaskCarriesItsTemplatesNothingNeededAction(t *testing.T) {
	t.Parallel()
	for _, action := range nothingNeededActions {
		tmpl := model.Template{ID: "template-1", Title: "Sweep", NothingNeededAction: action}
		comp := model.ComposeSuiteTask(tmpl, nightlyRepo, "", false, false, false)
		if comp.Sources.NothingNeededAction != model.LayerTemplate {
			t.Errorf("source = %q, want the template", comp.Sources.NothingNeededAction)
		}
		task := comp.Task("t-1", model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}, "", firstNight)
		if task.NothingNeededAction != action {
			t.Errorf("template saying %q composed a suite task with NothingNeededAction = %q", action, task.NothingNeededAction)
		}
	}
}
