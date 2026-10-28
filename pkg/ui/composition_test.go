package ui_test

// composition.go's own tests: what a preview says a schedule or a suite
// will file. The point of each of these is the *source* as much as the
// value -- a preview that says "auto-merge: on" and cannot say which
// declaration turned it on is the state this feature exists to end.

import (
	"context"
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// field is one named field of one composed task, or a fatal failure.
func field(t *testing.T, task ui.ComposedTask, name string) ui.ComposedField {
	t.Helper()
	for _, f := range task.Fields {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no %q field in %+v", name, task.Fields)
	return ui.ComposedField{}
}

func wantField(t *testing.T, task ui.ComposedTask, name, source string, values ...string) {
	t.Helper()
	f := field(t, task, name)
	if f.Source != source {
		t.Errorf("%s source = %q, want %q", name, f.Source, source)
	}
	if len(f.Values) != len(values) {
		t.Fatalf("%s = %v, want %v", name, f.Values, values)
	}
	for i, v := range values {
		if f.Values[i] != v {
			t.Errorf("%s[%d] = %q, want %q", name, i, f.Values[i], v)
		}
	}
}

func templateForComposition(t *testing.T, c *ui.Client, ctx context.Context, req ui.CreateTemplateRequest) ui.Template {
	t.Helper()
	tmpl, err := c.CreateTemplate(ctx, req)
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	return tmpl
}

func TestScheduleCompositionShowsInlineContentAsTheSchedules(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Sweep", Description: "Sweep the repo.", Repo: "acme/widgets", Base: "main",
		AutoMerge: true, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}

	got, err := c.ScheduleComposition(ctx, sched.ID)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if got.Kind != "task" || got.ScheduleID != sched.ID {
		t.Errorf("composition = %+v, want a task-backed schedule's", got)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("tasks = %+v, want exactly one", got.Tasks)
	}
	task := got.Tasks[0]
	if task.TemplateID != "" {
		t.Errorf("templateId = %q, want none: this schedule fires its own content", task.TemplateID)
	}
	wantField(t, task, "title", "schedule", "Sweep")
	wantField(t, task, "repo", "schedule", "acme/widgets")
	wantField(t, task, "base", "schedule", "main")
	wantField(t, task, "autoMerge", "schedule", "on")
	wantField(t, task, "approval", "schedule", "approved when filed")
	// Nothing says anything about the network, so the deployment does.
	wantField(t, task, "network", "deployment")
}

func TestScheduleCompositionShowsWhereEachFieldCameFrom(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{
		Name: "Bump", Title: "Bump the dependencies", Description: "Bump them.",
		AutoMerge: true, NetworkAccess: "none",
	})
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}

	got, err := c.ScheduleComposition(ctx, sched.ID)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("tasks = %+v, want exactly one", got.Tasks)
	}
	task := got.Tasks[0]
	if task.TemplateID != tmpl.ID || task.TemplateName != "Bump" {
		t.Errorf("template = %s/%s, want %s/Bump", task.TemplateID, task.TemplateName, tmpl.ID)
	}
	// Content from the template, placement from the schedule: the split
	// this whole preview exists to make visible.
	wantField(t, task, "title", "template", "Bump the dependencies")
	wantField(t, task, "body", "template", "Bump them.")
	wantField(t, task, "network", "template", "none")
	wantField(t, task, "autoMerge", "template", "on")
	wantField(t, task, "repo", "schedule", "acme/widgets")
	wantField(t, task, "base", "schedule", "main")
	wantField(t, task, "approval", "schedule", "approved when filed")
}

func TestScheduleCompositionFollowsABoundTemplatesRepo(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{
		Name: "Bound", Title: "Only here", Repo: "acme/widgets", Base: "release",
	})
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}

	got, err := c.ScheduleComposition(ctx, sched.ID)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	wantField(t, got.Tasks[0], "repo", "template", "acme/widgets")
	wantField(t, got.Tasks[0], "base", "template", "release")
}

func TestSuiteCompositionShowsTheSuitesOwnDisposition(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	off := false
	// A template that auto-merges on its own, in a suite that says not
	// to: the suite wins, and the preview says which one answered.
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{
		Name: "Sweep", Title: "Sweep", AutoMerge: true,
	})
	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{tmpl.ID}, Mode: "until_clean", MaxPasses: 4,
		RequireApproval: true, AutoMerge: &off,
	})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}

	got, err := c.SuiteComposition(ctx, suite.ID, "acme/widgets", "main")
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if got.Kind != "suite" || got.SuiteName != "Nightly" {
		t.Errorf("composition = %+v, want the suite's own", got)
	}
	if got.Mode != "until_clean" || got.MaxPasses != 4 {
		t.Errorf("stopping rule = %s/%d, want until_clean/4", got.Mode, got.MaxPasses)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("tasks = %+v, want one per item of a pass", got.Tasks)
	}
	task := got.Tasks[0]
	wantField(t, task, "title", "template", "Sweep")
	wantField(t, task, "autoMerge", "suite", "off")
	wantField(t, task, "approval", "suite", "waits for approval")
	wantField(t, task, "repo", "suite", "acme/widgets")
	wantField(t, task, "base", "suite", "main")
}

// A suite previewed before anybody has chosen a repo still answers for
// everything that is not placement -- which is what the "Run…" form has
// to show while it is being filled in.
func TestSuiteCompositionWithNoRepoYet(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{Name: "Sweep", Title: "Sweep"})
	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 2,
	})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}

	got, err := c.SuiteComposition(ctx, suite.ID, "", "")
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if got.Repo != "" {
		t.Errorf("repo = %q, want empty until the run names one", got.Repo)
	}
	if got.Count != 2 {
		t.Errorf("count = %d, want 2", got.Count)
	}
	wantField(t, got.Tasks[0], "repo", "suite")
	wantField(t, got.Tasks[0], "title", "template", "Sweep")
}

// A schedule that runs a suite composes that suite's pass, against the
// schedule's own repo and branch.
func TestScheduleCompositionOfASuiteBackedSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	first := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{Name: "One", Title: "First"})
	second := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{Name: "Two", Title: "Second"})
	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{first.ID, second.ID}, Mode: "count", Count: 3,
	})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", SuiteID: suite.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}

	got, err := c.ScheduleComposition(ctx, sched.ID)
	if err != nil {
		t.Fatalf("composing: %v", err)
	}
	if got.Kind != "suite" || got.SuiteID != suite.ID || got.SuiteName != "Nightly" {
		t.Errorf("composition = %+v, want the suite it runs", got)
	}
	if got.Mode != "count" || got.Count != 3 {
		t.Errorf("stopping rule = %s/%d, want count/3", got.Mode, got.Count)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("tasks = %+v, want one per item of the suite", got.Tasks)
	}
	wantField(t, got.Tasks[0], "title", "template", "First")
	wantField(t, got.Tasks[1], "title", "template", "Second")
	wantField(t, got.Tasks[0], "repo", "suite", "acme/widgets")
	// The suite's own defaults: queued and auto-merging
	// (bwsalmon/agents#642's "auto queue and auto merge").
	wantField(t, got.Tasks[0], "autoMerge", "suite", "on")
	wantField(t, got.Tasks[0], "approval", "suite", "approved when filed")
}

// --- drafts (grain/task-426) --------------------------------------------
//
// The same answers again, for a schedule or a suite that has not been
// saved: what the form in front of somebody would file if they pressed
// the button, rather than what the row behind it files.

func TestDraftScheduleCompositionOfAnUnsavedSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	got, err := c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{
		Title: "Sweep", Description: "Sweep the repo.", Repo: "acme/widgets", Base: "main",
		AutoMerge: true,
	})
	if err != nil {
		t.Fatalf("composing a draft: %v", err)
	}
	if got.ScheduleID != "" {
		t.Errorf("scheduleId = %q, want none: this schedule does not exist", got.ScheduleID)
	}
	if len(got.Tasks) != 1 {
		t.Fatalf("tasks = %+v, want exactly one", got.Tasks)
	}
	wantField(t, got.Tasks[0], "title", "schedule", "Sweep")
	wantField(t, got.Tasks[0], "repo", "schedule", "acme/widgets")
	wantField(t, got.Tasks[0], "autoMerge", "schedule", "on")
}

// The point of composing the draft rather than the row: the preview
// follows the edit. Nothing here is ever saved.
func TestDraftScheduleCompositionFollowsTheEdits(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	req := ui.CreateScheduleRequest{Title: "Sweep", Repo: "acme/widgets", Base: "main"}
	got, err := c.DraftScheduleComposition(ctx, req)
	if err != nil {
		t.Fatalf("composing a draft: %v", err)
	}
	wantField(t, got.Tasks[0], "base", "schedule", "main")

	req.Base = "release"
	got, err = c.DraftScheduleComposition(ctx, req)
	if err != nil {
		t.Fatalf("composing the edited draft: %v", err)
	}
	wantField(t, got.Tasks[0], "base", "schedule", "release")

	list, err := c.ListSchedules(ctx)
	if err != nil {
		t.Fatalf("listing schedules: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("schedules = %+v, want none: composing a draft writes nothing", list)
	}
}

// A draft that names a template resolves it out of the store on every
// call, exactly as a firing does -- so a preview shows today's template
// and not a copy taken when the form was opened.
func TestDraftCompositionResolvesTemplatesFresh(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{
		Name: "Bump", Title: "Bump the dependencies",
	})
	draft := ui.CreateScheduleRequest{Repo: "acme/widgets", Base: "main", TemplateID: tmpl.ID}
	got, err := c.DraftScheduleComposition(ctx, draft)
	if err != nil {
		t.Fatalf("composing a draft: %v", err)
	}
	wantField(t, got.Tasks[0], "title", "template", "Bump the dependencies")

	retitled := "Bump them harder"
	if _, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{Title: &retitled}); err != nil {
		t.Fatalf("editing the template: %v", err)
	}
	got, err = c.DraftScheduleComposition(ctx, draft)
	if err != nil {
		t.Fatalf("recomposing the draft: %v", err)
	}
	wantField(t, got.Tasks[0], "title", "template", retitled)

	suiteDraft := ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 2,
	}
	suiteGot, err := c.DraftSuiteComposition(ctx, suiteDraft, "acme/widgets", "main")
	if err != nil {
		t.Fatalf("composing a draft suite: %v", err)
	}
	wantField(t, suiteGot.Tasks[0], "title", "template", retitled)
}

// A draft schedule that runs a suite is still the suite's answer, layers
// and stopping rule included.
func TestDraftScheduleCompositionOfASuiteBackedSchedule(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{
		Name: "Sweep", Title: "Sweep", AutoMerge: true,
	})
	off := false
	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 3,
		RequireApproval: true, AutoMerge: &off,
	})
	if err != nil {
		t.Fatalf("creating a suite: %v", err)
	}

	got, err := c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", SuiteID: suite.ID,
	})
	if err != nil {
		t.Fatalf("composing a draft: %v", err)
	}
	if got.Kind != "suite" || got.SuiteName != "Nightly" || got.Count != 3 {
		t.Fatalf("composition = %+v, want the suite this draft would run", got)
	}
	wantField(t, got.Tasks[0], "autoMerge", "suite", "off")
	wantField(t, got.Tasks[0], "approval", "suite", "waits for approval")
}

func TestDraftSuiteCompositionOfAnUnsavedSuite(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	first := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{Name: "One", Title: "First"})
	second := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{Name: "Two", Title: "Second"})

	got, err := c.DraftSuiteComposition(ctx, ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{first.ID, second.ID},
		Mode: "until_clean", MaxPasses: 4, RequireApproval: true,
	}, "", "")
	if err != nil {
		t.Fatalf("composing a draft suite: %v", err)
	}
	if got.SuiteID != "" || got.SuiteName != "Nightly" {
		t.Errorf("composition = %+v, want an unsaved suite's own name and no id", got)
	}
	if got.Mode != "until_clean" || got.MaxPasses != 4 {
		t.Errorf("stopping rule = %s/%d, want until_clean/4", got.Mode, got.MaxPasses)
	}
	if len(got.Tasks) != 2 {
		t.Fatalf("tasks = %+v, want one per template picked so far", got.Tasks)
	}
	wantField(t, got.Tasks[0], "title", "template", "First")
	wantField(t, got.Tasks[1], "title", "template", "Second")
	// The two switches on the form, before it has ever been saved.
	wantField(t, got.Tasks[0], "approval", "suite", "waits for approval")
	wantField(t, got.Tasks[0], "autoMerge", "suite", "on")
}

// A form halfway through being filled in is composed as far as it goes:
// what it does not say yet is missing from the answer, not an error.
func TestDraftCompositionOfAHalfFilledForm(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{Name: "Sweep", Title: "Sweep"})

	// No repo, no branch, no cadence: every one of those would refuse
	// CreateSchedule, and none of them changes what the task carries.
	got, err := c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{TemplateID: tmpl.ID})
	if err != nil {
		t.Fatalf("composing a half-filled draft: %v", err)
	}
	wantField(t, got.Tasks[0], "title", "template", "Sweep")
	wantField(t, got.Tasks[0], "repo", "schedule")

	// A suite with nothing picked yet composes to no tasks rather than
	// to "a suite needs at least one template".
	empty, err := c.DraftSuiteComposition(ctx, ui.CreateSuiteRequest{}, "", "")
	if err != nil {
		t.Fatalf("composing an empty draft suite: %v", err)
	}
	if len(empty.Tasks) != 0 {
		t.Errorf("tasks = %+v, want none until a template is picked", empty.Tasks)
	}
	if empty.Mode != "" {
		t.Errorf("mode = %q, want none until one is chosen", empty.Mode)
	}
}

// What a draft cannot do is invent: an id behind which there is nothing
// is the answer, not something to compose around.
func TestDraftCompositionRefusesWhatItCannotResolve(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", TemplateID: "template-404",
	}); err == nil {
		t.Error("composing a draft naming an unknown template succeeded, want an error")
	}
	if _, err := c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", SuiteID: "suite-404",
	}); err == nil {
		t.Error("composing a draft naming an unknown suite succeeded, want an error")
	}
	if _, err := c.DraftSuiteComposition(ctx, ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{"template-404"}, Mode: "count", Count: 1,
	}, "acme/widgets", "main"); err == nil {
		t.Error("composing a draft suite naming an unknown template succeeded, want an error")
	}
}

func TestCompositionOfAnUnknownScheduleOrSuiteIsNotFound(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	if _, err := c.ScheduleComposition(ctx, "sched-404"); err == nil {
		t.Error("composing an unknown schedule succeeded, want an error")
	}
	if _, err := c.SuiteComposition(ctx, "suite-404", "acme/widgets", "main"); err == nil {
		t.Error("composing an unknown suite succeeded, want an error")
	}
}

// A repository whose CI Mode is "Use Grain CI" adds grain-ci to every task
// composed against it (model.adjustGrainCIGrant). That grant is the repo's
// doing, not the schedule's or the template's, so the composition response
// credits grain-ci to "repo" alongside whatever capabilities the schedule or
// template itself asked for.
func TestCompositionCreditsGrainCIToTheRepo(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, CIMode: model.CIModeGrain}); err != nil {
		t.Fatal(err)
	}

	// Inline schedule with its own capability (the sched-2 case from task 293).
	draft, err := c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{
		Title:        "Weekly Dependency & Vulnerability Audit",
		Repo:         "acme/widgets",
		Capabilities: []string{"self-debug"},
	})
	if err != nil {
		t.Fatalf("composing draft schedule: %v", err)
	}
	caps := field(t, draft.Tasks[0], "capabilities")
	if !slices.Equal(caps.Values, []string{"self-debug", "grain-ci"}) {
		t.Errorf("values = %v, want [self-debug grain-ci]", caps.Values)
	}
	if !slices.Equal(caps.Sources, []string{"schedule", "repo"}) {
		t.Errorf("sources = %v, want [schedule repo]", caps.Sources)
	}

	// Inline schedule with no capabilities of its own: grain-ci comes from
	// the repo alone, not the schedule.
	plainDraft, err := c.DraftScheduleComposition(ctx, ui.CreateScheduleRequest{
		Title: "Sweep",
		Repo:  "acme/widgets",
	})
	if err != nil {
		t.Fatalf("composing plain draft schedule: %v", err)
	}
	wantField(t, plainDraft.Tasks[0], "capabilities", "repo", "grain-ci")

	// Template with its own capability, previewed directly, through a
	// schedule, and through a suite.
	tmpl := templateForComposition(t, c, ctx, ui.CreateTemplateRequest{
		Name:         "Audit",
		Title:        "Audit",
		Capabilities: []string{"gcp-key"},
	})
	tmplComp, err := c.TemplateComposition(ctx, tmpl.ID, "acme/widgets", "main", nil, true)
	if err != nil {
		t.Fatalf("composing template: %v", err)
	}
	tmplCaps := field(t, tmplComp.Tasks[0], "capabilities")
	if !slices.Equal(tmplCaps.Values, []string{"gcp-key", "grain-ci"}) {
		t.Errorf("template values = %v, want [gcp-key grain-ci]", tmplCaps.Values)
	}
	if !slices.Equal(tmplCaps.Sources, []string{"template", "repo"}) {
		t.Errorf("template sources = %v, want [template repo]", tmplCaps.Sources)
	}

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", Base: "main", TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating schedule: %v", err)
	}
	schedComp, err := c.ScheduleComposition(ctx, sched.ID)
	if err != nil {
		t.Fatalf("composing schedule: %v", err)
	}
	schedCaps := field(t, schedComp.Tasks[0], "capabilities")
	if !slices.Equal(schedCaps.Values, []string{"gcp-key", "grain-ci"}) {
		t.Errorf("schedule values = %v, want [gcp-key grain-ci]", schedCaps.Values)
	}
	if !slices.Equal(schedCaps.Sources, []string{"template", "repo"}) {
		t.Errorf("schedule sources = %v, want [template repo]", schedCaps.Sources)
	}

	suite, err := c.CreateSuite(ctx, ui.CreateSuiteRequest{
		Name: "Nightly", TemplateIDs: []string{tmpl.ID}, Mode: "count", Count: 1,
	})
	if err != nil {
		t.Fatalf("creating suite: %v", err)
	}
	suiteComp, err := c.SuiteComposition(ctx, suite.ID, "acme/widgets", "main")
	if err != nil {
		t.Fatalf("composing suite: %v", err)
	}
	suiteCaps := field(t, suiteComp.Tasks[0], "capabilities")
	if !slices.Equal(suiteCaps.Values, []string{"gcp-key", "grain-ci"}) {
		t.Errorf("suite values = %v, want [gcp-key grain-ci]", suiteCaps.Values)
	}
	if !slices.Equal(suiteCaps.Sources, []string{"template", "repo"}) {
		t.Errorf("suite sources = %v, want [template repo]", suiteCaps.Sources)
	}
}
