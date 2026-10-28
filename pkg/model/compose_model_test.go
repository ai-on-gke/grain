package model_test

import (
	"context"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// A template's model (Template.Model) is carried onto every task it
// files, from every firing point, and the preview credits the template
// with it.
func TestComposeCarriesTheTemplatesModel(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Model = "claude-opus-5-5"
	repo := composeRepo("bwsalmon", "grain")

	for _, tc := range []struct {
		name string
		comp model.Composition
	}{
		{"schedule fires it", model.ComposeScheduleTask(composeSchedule(), &tmpl)},
		{"suite runs it", model.ComposeSuiteTask(tmpl, repo, "main", false, false, false)},
		{"a plan qualifies with it", model.ComposeQualificationTask(tmpl, repo, "cand", false)},
		{"somebody files it", model.ComposeTemplateTask(tmpl, nil, repo, "main", true)},
	} {
		if tc.comp.Model != tmpl.Model {
			t.Errorf("%s: Model = %q, want the template's %q", tc.name, tc.comp.Model, tmpl.Model)
		}
		if tc.comp.Sources.Model != model.LayerTemplate {
			t.Errorf("%s: model decided by %q, want the template", tc.name, tc.comp.Sources.Model)
		}
		task := tc.comp.Task("task-1", model.SchedulePrincipal, model.ReasonSchedule, time.Now())
		if task.Model != tmpl.Model {
			t.Errorf("%s: filed task's Model = %q, want %q", tc.name, task.Model, tmpl.Model)
		}
	}
}

// Nobody chose a model: the team's default decides at dispatch, and the
// preview says so rather than crediting the template.
func TestComposeModelNobodyChoseIsTheTeams(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Model = ""
	for _, comp := range []model.Composition{
		model.ComposeScheduleTask(composeSchedule(), &tmpl),
		model.ComposeScheduleTask(composeSchedule(), nil),
		model.ComposeTemplateTask(tmpl, nil, composeRepo("bwsalmon", "grain"), "main", true),
	} {
		if comp.Model != "" || comp.Sources.Model != model.LayerTeam {
			t.Errorf("Model = %q from %q, want empty from the team", comp.Model, comp.Sources.Model)
		}
	}
}

// A schedule's own model beats its template's (Schedule.Model), and an
// inline schedule's is simply its own.
func TestComposeScheduleModelBeatsTheTemplates(t *testing.T) {
	tmpl := composeTemplate()
	tmpl.Model = "claude-opus-5-5"
	sched := composeSchedule()
	sched.Model = "gemini-3.1-pro"

	for name, comp := range map[string]model.Composition{
		"fires a template": model.ComposeScheduleTask(sched, &tmpl),
		"inline content":   model.ComposeScheduleTask(sched, nil),
	} {
		if comp.Model != sched.Model || comp.Sources.Model != model.LayerSchedule {
			t.Errorf("%s: Model = %q from %q, want %q from the schedule",
				name, comp.Model, comp.Sources.Model, sched.Model)
		}
	}
}

// A firing files the template's model, and the schedule's own choice when
// it has one -- and keeps that choice on the schedule row: the firing's
// display-cache sync copies the template's content over the schedule,
// and the model is not content.
func TestFiringAScheduleFilesTheChosenModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	tmpl := model.Template{
		ID: "template-m", Name: "Sweep", Title: "Sweep", Body: "sweep it",
		Model: "claude-opus-5-5", CreatedAt: now,
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("putting the template: %v", err)
	}
	fire := func(id, chosen string) (model.Task, model.Schedule) {
		t.Helper()
		tid := tmpl.ID
		sched := model.Schedule{
			ID: id, Title: "Sweep", TemplateID: &tid, Model: chosen,
			Target:     model.RepoRef{Owner: "bwsalmon", Name: "grain"},
			Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 60},
			Overlap:    model.OverlapAllow, Enabled: true, NextRunAt: now, CreatedAt: now,
		}
		if err := store.PutSchedule(ctx, sched); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
		firing, err := store.FireSchedule(ctx, sched, now, time.UTC, model.RepoRef{})
		if err != nil || firing.TaskID == "" {
			t.Fatalf("firing %s: %+v, %v", id, firing, err)
		}
		task, err := store.GetTask(ctx, firing.TaskID)
		if err != nil || task == nil {
			t.Fatalf("reading the filed task: %v, %v", task, err)
		}
		after, err := store.GetSchedule(ctx, id)
		if err != nil || after == nil {
			t.Fatalf("re-reading %s: %v, %v", id, after, err)
		}
		return *task, *after
	}

	task, after := fire("sched-template-model", "")
	if task.Model != tmpl.Model {
		t.Errorf("task filed from the template's model = %q, want %q", task.Model, tmpl.Model)
	}
	if task.ModelSource != model.LayerTemplate {
		t.Errorf("task filed from the template's model records source %q, want template", task.ModelSource)
	}
	if after.Model != "" {
		t.Errorf("schedule's own model after firing = %q, want it left empty", after.Model)
	}

	task, after = fire("sched-own-model", "gemini-3.1-pro")
	if task.Model != "gemini-3.1-pro" {
		t.Errorf("task filed under the schedule's own model = %q, want gemini-3.1-pro", task.Model)
	}
	if task.ModelSource != model.LayerSchedule {
		t.Errorf("task filed under the schedule's own model records source %q, want schedule", task.ModelSource)
	}
	if after.Model != "gemini-3.1-pro" {
		t.Errorf("schedule's own model after firing = %q, want it kept", after.Model)
	}
}
