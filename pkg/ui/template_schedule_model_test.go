package ui_test

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// A template chooses a model (model.Template.Model): stored, edited and
// cleared through the templates API, and a task filed from the template
// takes it -- unless the request chose its own, which wins.
func TestTemplateChoosesAModelForTheTasksItFiles(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	tmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "port", Title: "Port the parser", Description: "port it", Model: " gpt-5.6-sol ",
	})
	if err != nil {
		t.Fatalf("creating a template with a model: %v", err)
	}
	if tmpl.Model != "gpt-5.6-sol" {
		t.Fatalf("template.model on the wire = %q, want gpt-5.6-sol", tmpl.Model)
	}

	fromTemplate, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatalf("filing from the template: %v", err)
	}
	stored, err := store.GetTask(ctx, fromTemplate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Model != "gpt-5.6-sol" || stored.AgentFramework != model.AgentFrameworkCodex {
		t.Errorf("task filed from the template = model %q on %q, want gpt-5.6-sol on codex",
			stored.Model, stored.AgentFramework)
	}

	own, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Model: "claude-opus-5", Approved: true})
	if err != nil {
		t.Fatalf("filing from the template with a model of its own: %v", err)
	}
	if own.Model != "claude-opus-5" {
		t.Errorf("task's own model = %q, want claude-opus-5 to beat the template's", own.Model)
	}

	// A pin chooses what runs the task: the template's model is not
	// pressed onto a connection that does not serve it.
	pinned, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, AgentBackendID: "claude-deep", Approved: true})
	if err != nil {
		t.Fatalf("filing from the template pinned to another connection: %v", err)
	}
	if pinned.AgentBackendID != "claude-deep" || pinned.Model != "" {
		t.Errorf("pinned task = model %q pin %q, want no model and the pin", pinned.Model, pinned.AgentBackendID)
	}

	updated, err := client.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Model: ptr("claude-opus-5")})
	if err != nil {
		t.Fatalf("changing the template's model: %v", err)
	}
	if updated.Model != "claude-opus-5" {
		t.Errorf("after the edit model = %q, want claude-opus-5", updated.Model)
	}
	untouched, err := client.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Title: ptr("Port it again")})
	if err != nil {
		t.Fatal(err)
	}
	if untouched.Model != "claude-opus-5" {
		t.Errorf("an edit that said nothing about the model changed it to %q", untouched.Model)
	}
	cleared, err := client.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Model: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Model != "" {
		t.Errorf("after clearing model = %q, want empty", cleared.Model)
	}
	plain, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Model != "" {
		t.Errorf("task from a template that chose no model = %q, want the team's default (empty)", plain.Model)
	}
}

// A task records which declaration chose its model (model.Task.ModelSource)
// and says so on the wire: "template" for one its template carried,
// nothing for a model the request chose itself, and nothing once an edit
// has taken the choice over.
func TestATaskSaysWhichDeclarationChoseItsModel(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	tmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "port", Title: "Port the parser", Model: "gpt-5.6-sol",
	})
	if err != nil {
		t.Fatal(err)
	}
	fromTemplate, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if fromTemplate.Model != "gpt-5.6-sol" || fromTemplate.ModelSource != "template" {
		t.Errorf("task filed from the template = model %q from %q, want gpt-5.6-sol from the template",
			fromTemplate.Model, fromTemplate.ModelSource)
	}
	stored, err := store.GetTask(ctx, fromTemplate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ModelSource != model.LayerTemplate {
		t.Errorf("stored model source = %q, want template", stored.ModelSource)
	}

	own, err := client.CreateTask(ctx, ui.CreateTaskRequest{TemplateID: tmpl.ID, Model: "claude-opus-5", Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if own.ModelSource != "" {
		t.Errorf("a model the request chose itself records source %q, want none", own.ModelSource)
	}

	// Restating the template's model leaves its source alone; changing
	// it makes the model the task's own.
	same, err := client.UpdateTask(ctx, fromTemplate.ID, ui.UpdateTaskRequest{Model: ptr("gpt-5.6-sol")})
	if err != nil {
		t.Fatal(err)
	}
	if same.ModelSource != "template" {
		t.Errorf("after restating the same model source = %q, want template", same.ModelSource)
	}
	edited, err := client.UpdateTask(ctx, fromTemplate.ID, ui.UpdateTaskRequest{Model: ptr("claude-opus-5")})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Model != "claude-opus-5" || edited.ModelSource != "" {
		t.Errorf("after editing the model = %q from %q, want claude-opus-5 and no source",
			edited.Model, edited.ModelSource)
	}
}

// A template or a schedule names a model, never a connection: a "model"
// that is a configured connection's id is refused rather than quietly
// read as a pin.
func TestTemplateAndScheduleRefuseAConnectionAsTheirModel(t *testing.T) {
	client, _, ctx := testClient(t)
	modelPool(t, ctx, client)

	_, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", Model: "codex-day",
	})
	if err == nil || !strings.Contains(err.Error(), "names a connection") {
		t.Fatalf("template with a connection as its model = %v, want a refusal", err)
	}
	_, err = client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay, Model: "codex-day",
	})
	if err == nil || !strings.Contains(err.Error(), "names a connection") {
		t.Fatalf("schedule with a connection as its model = %v, want a refusal", err)
	}
}

// A schedule's own model is stored, beats its template's in what it
// composes, survives a firing's sync of the template's content, and can
// be edited and cleared while the template stays attached.
func TestScheduleChoosesAModelThatBeatsItsTemplates(t *testing.T) {
	client, store, ctx := testClient(t)
	modelPool(t, ctx, client)

	tmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "port", Title: "Port the parser", Model: "gpt-5.6-sol",
	})
	if err != nil {
		t.Fatal(err)
	}
	sched, err := client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		TemplateID: tmpl.ID, Repo: "acme/widgets", Recurrence: everyDay, Model: "claude-opus-5",
	})
	if err != nil {
		t.Fatalf("creating a schedule with a model alongside its template: %v", err)
	}
	if sched.Model != "claude-opus-5" {
		t.Fatalf("schedule.model on the wire = %q, want claude-opus-5", sched.Model)
	}
	stored, err := store.GetSchedule(ctx, sched.ID)
	if err != nil || stored == nil {
		t.Fatalf("get: (%+v, %v)", stored, err)
	}
	comps, err := store.ScheduleComposition(ctx, *stored, model.RepoRef{})
	if err != nil || len(comps) != 1 {
		t.Fatalf("composing: %d, %v", len(comps), err)
	}
	if comps[0].Model != "claude-opus-5" || comps[0].Sources.Model != model.LayerSchedule {
		t.Errorf("composed model = %q from %q, want claude-opus-5 from the schedule",
			comps[0].Model, comps[0].Sources.Model)
	}

	cleared, err := client.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Model: ptr("")})
	if err != nil {
		t.Fatalf("clearing the schedule's model: %v", err)
	}
	if cleared.Model != "" {
		t.Errorf("after clearing model = %q", cleared.Model)
	}
	stored, _ = store.GetSchedule(ctx, sched.ID)
	comps, err = store.ScheduleComposition(ctx, *stored, model.RepoRef{})
	if err != nil || len(comps) != 1 {
		t.Fatalf("composing: %d, %v", len(comps), err)
	}
	if comps[0].Model != "gpt-5.6-sol" || comps[0].Sources.Model != model.LayerTemplate {
		t.Errorf("composed model = %q from %q, want the template's gpt-5.6-sol",
			comps[0].Model, comps[0].Sources.Model)
	}
	set, err := client.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Model: ptr("gemini-3.1-pro")})
	if err != nil {
		t.Fatalf("setting a model on a template-backed schedule: %v", err)
	}
	if set.Model != "gemini-3.1-pro" {
		t.Errorf("after the edit model = %q", set.Model)
	}
}

// A suite run files each of its templates as it stands, so a schedule
// running a suite has no task for a model to apply to.
func TestASuiteScheduleRefusesAModel(t *testing.T) {
	client, _, ctx := testClient(t)
	tmpl, err := client.CreateTemplate(ctx, ui.CreateTemplateRequest{Name: "smoke", Title: "Smoke"})
	if err != nil {
		t.Fatal(err)
	}
	suite, err := client.CreateSuite(ctx, ui.CreateSuiteRequest{Name: "smoke", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 1})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}
	_, err = client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay, Model: "gpt-5.6-sol",
	})
	if err == nil || !strings.Contains(err.Error(), "cannot choose a model") {
		t.Fatalf("suite schedule with a model = %v, want a refusal", err)
	}
	sched, err := client.CreateSchedule(ctx, ui.CreateScheduleRequest{
		SuiteID: suite.ID, Repo: "acme/widgets", Base: "main", Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a suite schedule: %v", err)
	}
	_, err = client.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{Model: ptr("gpt-5.6-sol")})
	if err == nil || !strings.Contains(err.Error(), "cannot choose a model") {
		t.Fatalf("editing a model onto a suite schedule = %v, want a refusal", err)
	}
}
