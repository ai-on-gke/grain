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

// Filing and editing a task's custom field values through the API, and
// the declarations GET /api/config hands the forms that offer them
// (grain/task-53).

import (
	"context"
	"net/http"
	"reflect"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
)

func declareFields(t *testing.T, store *model.Store, ctx context.Context, fields ...model.CustomField) {
	t.Helper()
	for _, f := range fields {
		if err := store.PutCustomField(ctx, f); err != nil {
			t.Fatalf("declaring %s: %v", f.Name, err)
		}
	}
}

func TestCreateTaskWithCustomFields(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "customer"},
		model.CustomField{Name: "priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}},
		model.CustomField{Name: "escalated", Type: model.CustomFieldBool},
	)

	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "Fix the thing",
		CustomFields: map[string]ui.CustomFieldValue{
			"customer":  {" acme "},
			"priority":  {"high"},
			"escalated": {""},
		},
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	// Canonicalized on the way in, and the empty one dropped: what comes
	// back is what the task actually says.
	if !slices.Equal(task.CustomFields["customer"], []string{"acme"}) ||
		!slices.Equal(task.CustomFields["priority"], []string{"high"}) {
		t.Errorf("customFields = %v, want the two that were filled in", task.CustomFields)
	}
	if _, ok := task.CustomFields["escalated"]; ok {
		t.Errorf("an unfilled field was stored: %v", task.CustomFields)
	}

	// A field nothing declares is refused rather than written and shown
	// nowhere -- a typo'd key would otherwise sit on the task for ever.
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "Another",
		CustomFields: map[string]ui.CustomFieldValue{"custmer": {"acme"}},
	}); err == nil {
		t.Error("a value for an undeclared field was accepted")
	}
	// And so is a value the declared type cannot hold.
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "Another",
		CustomFields: map[string]ui.CustomFieldValue{"priority": {"urgent"}},
	}); err == nil {
		t.Error("a value outside the declared options was accepted")
	}
}

// An edit replaces the values for every declared field -- the pane
// sends the whole set -- but leaves alone what the task carries for a
// field that has since been deleted from the state repository, which
// nothing on that pane can render or re-state.
func TestUpdateTaskCustomFields(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "customer"},
		model.CustomField{Name: "retired"},
	)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "Fix the thing",
		CustomFields: map[string]ui.CustomFieldValue{"customer": {"acme"}, "retired": {"kept"}},
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	if err := store.DeleteCustomField(ctx, "retired"); err != nil {
		t.Fatalf("deleting a declaration: %v", err)
	}

	values := map[string]ui.CustomFieldValue{"customer": {"globex"}}
	got, err := client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{CustomFields: &values})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if !slices.Equal(got.CustomFields["customer"], []string{"globex"}) {
		t.Errorf("customFields = %v, want the edited value", got.CustomFields)
	}
	if !slices.Equal(got.CustomFields["retired"], []string{"kept"}) {
		t.Errorf("customFields = %v, want the value whose field was deleted left alone", got.CustomFields)
	}

	// Naming it with an empty value is how it goes, which is the only
	// way a value nothing declares can ever be taken off a task.
	values = map[string]ui.CustomFieldValue{"retired": {""}}
	got, err = client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{CustomFields: &values})
	if err != nil {
		t.Fatalf("clearing: %v", err)
	}
	if len(got.CustomFields) != 0 {
		t.Errorf("customFields = %v, want them all gone", got.CustomFields)
	}
}

// An enum option retired after somebody chose it leaves a value the
// declaration would no longer produce. The pane that edits this task
// renders every field and so restates every field, including that one,
// and the edit has to land: refusing it would mean a task's title could
// not be changed because of a declaration nobody touched today
// (model.NormalizeCustomFieldValuesFrom).
func TestUpdateTaskKeepsALegacyEnumValue(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "customer"},
		model.CustomField{Name: "priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high", "critical"}},
	)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "Fix the thing",
		CustomFields: map[string]ui.CustomFieldValue{"customer": {"acme"}, "priority": {"critical"}},
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	// The declaration narrows, the way a merged pull request against
	// tables/custom_field.json narrows it.
	declareFields(t, store, ctx, model.CustomField{Name: "priority", Type: model.CustomFieldEnum,
		Options: []string{"low", "high"}})

	// What the pane sends when somebody edits the customer: the legacy
	// value restated beside the one they changed.
	values := map[string]ui.CustomFieldValue{"customer": {"globex"}, "priority": {"critical"}}
	got, err := client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{CustomFields: &values})
	if err != nil {
		t.Fatalf("editing a task carrying a retired option: %v", err)
	}
	if !slices.Equal(got.CustomFields["priority"], []string{"critical"}) {
		t.Errorf("priority = %v, want the retired option still on the task", got.CustomFields)
	}
	if !slices.Equal(got.CustomFields["customer"], []string{"globex"}) {
		t.Errorf("customer = %v, want the edit that was actually made", got.CustomFields)
	}

	// It is grandfathered onto this task, not put back on offer: a task
	// that never had it still cannot be given it.
	other, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "Another"})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	values = map[string]ui.CustomFieldValue{"priority": {"critical"}}
	if _, err := client.UpdateTask(ctx, other.ID, ui.UpdateTaskRequest{CustomFields: &values}); err == nil {
		t.Error("a retired option was accepted onto a task that never carried it")
	}

	// And the way out of carrying one is the way out of carrying any
	// value: pick another, or clear it.
	values = map[string]ui.CustomFieldValue{"priority": {"high"}}
	if got, err = client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{CustomFields: &values}); err != nil {
		t.Fatalf("replacing a legacy value: %v", err)
	}
	if !slices.Equal(got.CustomFields["priority"], []string{"high"}) {
		t.Errorf("priority = %v, want the value that was picked", got.CustomFields)
	}
}

// The other way a declaration strands values: a field that held a set
// stops holding one, and the tasks that used it are left carrying
// several values a single-valued declaration cannot describe. They stay
// whole across an edit that is not about them, and can be pared down
// one at a time -- what must not happen is the edit silently keeping
// one of them.
func TestUpdateTaskKeepsALegacySetOnASingleValuedField(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "customer"},
		model.CustomField{Name: "teams", Type: model.CustomFieldEnum,
			Options: []string{"infra", "ui"}, Multiple: true},
	)
	task, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "Fix the thing",
		CustomFields: map[string]ui.CustomFieldValue{"customer": {"acme"}, "teams": {"infra", "ui"}},
	})
	if err != nil {
		t.Fatalf("creating: %v", err)
	}
	declareFields(t, store, ctx, model.CustomField{Name: "teams", Type: model.CustomFieldEnum,
		Options: []string{"infra", "ui"}})

	values := map[string]ui.CustomFieldValue{"customer": {"globex"}, "teams": {"infra", "ui"}}
	got, err := client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{CustomFields: &values})
	if err != nil {
		t.Fatalf("editing a task carrying a legacy set: %v", err)
	}
	if !slices.Equal(got.CustomFields["teams"], []string{"infra", "ui"}) {
		t.Errorf("teams = %v, want both values still there", got.CustomFields)
	}

	// Paring it down is a subset of what is already there, so the pane
	// can walk it back to one value.
	values = map[string]ui.CustomFieldValue{"customer": {"globex"}, "teams": {"ui"}}
	if got, err = client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{CustomFields: &values}); err != nil {
		t.Fatalf("paring a legacy set down: %v", err)
	}
	if !slices.Equal(got.CustomFields["teams"], []string{"ui"}) {
		t.Errorf("teams = %v, want the one value that was kept", got.CustomFields)
	}
	// Growing it is not: the field takes one value now, and the only
	// reason it was ever holding two is that it once did not.
	values = map[string]ui.CustomFieldValue{"teams": {"infra", "ui"}}
	if _, err := client.UpdateTask(ctx, task.ID, ui.UpdateTaskRequest{CustomFields: &values}); err == nil {
		t.Error("a single-valued field was given back the second value it had been pared down to one from")
	}
}

// A field this deployment declared as holding a set carries several
// values, through the API a form actually speaks: an array in, an array
// back, and the single-valued fields beside it still plain strings --
// which is the whole of what keeps every reader written before this
// right (ui.CustomFieldValue).
func TestMultiValuedCustomFieldsOnTheWire(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "components", Multiple: true},
		model.CustomField{Name: "areas", Type: model.CustomFieldEnum,
			Options: []string{"ui", "daemon"}, Multiple: true},
		model.CustomField{Name: "priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}},
	)
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodPost, "/api/tasks", `{"title":"fix it","customFields":{
		"components": ["store", "prompt", "store"],
		"areas": ["daemon"],
		"priority": "high"
	}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201: %s", rec.Code, rec.Body)
	}
	body := decode[struct {
		ID           string         `json:"id"`
		CustomFields map[string]any `json:"customFields"`
	}](t, rec)
	// Several values come back as an array, deduplicated: what the field
	// holds is a set (model.CustomField.Multiple).
	if got, want := body.CustomFields["components"], []any{"prompt", "store"}; !reflect.DeepEqual(got, want) {
		t.Errorf("components = %#v, want %#v", got, want)
	}
	// One value comes back as the bare string it was before any of this,
	// whether or not the field could hold more.
	if got := body.CustomFields["areas"]; got != "daemon" {
		t.Errorf("areas = %#v, want the one value as a string", got)
	}
	if got := body.CustomFields["priority"]; got != "high" {
		t.Errorf("priority = %#v, want the one value as a string", got)
	}

	// A single-valued field handed a set is a validation error rather
	// than a value picked from among them.
	rec = do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"fix it","customFields":{"priority":["low","high"]}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("two values for a single-valued field = %d, want 400: %s", rec.Code, rec.Body)
	}
	// And so is a value outside a multi-valued enum's own options.
	rec = do(t, srv, http.MethodPost, "/api/tasks",
		`{"title":"fix it","customFields":{"areas":["ui","nope"]}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an undeclared option inside a set = %d, want 400: %s", rec.Code, rec.Body)
	}

	// An edit replaces the whole set, the way it replaces a single value.
	rec = do(t, srv, http.MethodPatch, "/api/tasks/"+body.ID,
		`{"customFields":{"components":["prompt"],"areas":["ui","daemon"],"priority":"low"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200: %s", rec.Code, rec.Body)
	}
	edited := decode[ui.Task](t, rec)
	if !slices.Equal(edited.CustomFields["components"], []string{"prompt"}) ||
		!slices.Equal(edited.CustomFields["areas"], []string{"daemon", "ui"}) {
		t.Errorf("customFields = %v, want the sets as the edit left them", edited.CustomFields)
	}
}

// The declarations ride on GET /api/config, which is the one call the
// frontend makes before its first paint -- the forms that offer the
// fields need them the moment they render.
func TestConfigCarriesCustomFieldDeclarations(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "priority", Label: "Priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}, Position: 1, ShowOnRow: true},
		model.CustomField{Name: "customer", Position: 0},
	)
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/config", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d", rec.Code)
	}
	resp := decode[struct {
		CustomFields []ui.CustomFieldDeclaration `json:"customFields"`
	}](t, rec)
	if len(resp.CustomFields) != 2 {
		t.Fatalf("customFields = %+v, want both declarations", resp.CustomFields)
	}
	if resp.CustomFields[0].Name != "customer" || resp.CustomFields[1].Name != "priority" {
		t.Errorf("order = %+v, want position first", resp.CustomFields)
	}
	// The empty-means-text default is resolved here rather than left for
	// every reader to know about.
	if resp.CustomFields[0].Type != string(model.CustomFieldText) {
		t.Errorf("type = %q, want it resolved to text", resp.CustomFields[0].Type)
	}
	if len(resp.CustomFields[1].Options) != 2 {
		t.Errorf("options = %v, want the enum's own choices", resp.CustomFields[1].Options)
	}
	// Which fields belong on the rows that list a task is the
	// declaration's to say (model.CustomField.ShowOnRow), and this is the
	// only place a frontend can read it: a task's own values say what it
	// holds, never where to show it.
	if !resp.CustomFields[1].ShowOnRow || resp.CustomFields[0].ShowOnRow {
		t.Errorf("showOnRow = %v/%v, want only the flagged field on the rows",
			resp.CustomFields[0].ShowOnRow, resp.CustomFields[1].ShowOnRow)
	}
}

// The *values* ride on every task in the list, not only on the one a
// detail pane fetched -- which is what lets the task list narrow and
// order on a declared field the same way it does on a repo or a state
// (ui/src/taskAttributes.js, grain/task-64). A field the list could not
// read for every row would be offered in the filter menu and match
// nothing.
func TestTaskListCarriesCustomFieldValues(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "customer"},
		model.CustomField{Name: "priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}},
	)
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{
		Title:        "fix it",
		CustomFields: map[string]ui.CustomFieldValue{"customer": {"acme"}, "priority": {"high"}},
	}); err != nil {
		t.Fatalf("creating: %v", err)
	}
	if _, err := client.CreateTask(ctx, ui.CreateTaskRequest{Title: "and this"}); err != nil {
		t.Fatalf("creating: %v", err)
	}
	srv := ui.NewServerWithClient(client)

	rec := do(t, srv, http.MethodGet, "/api/tasks", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/tasks = %d", rec.Code)
	}
	listed := decode[[]ui.Task](t, rec)
	if len(listed) != 2 {
		t.Fatalf("listed %d tasks, want 2", len(listed))
	}
	byTitle := map[string]ui.Task{}
	for _, task := range listed {
		byTitle[task.Title] = task
	}
	if got := byTitle["fix it"].CustomFields; !slices.Equal(got["customer"], []string{"acme"}) ||
		!slices.Equal(got["priority"], []string{"high"}) {
		t.Errorf("customFields = %v, want both values on the listed task", got)
	}
	// A task that says nothing about any field carries no map at all,
	// which is what a reader joining values to declarations by name
	// already treats as "nobody said".
	if got := byTitle["and this"].CustomFields; len(got) != 0 {
		t.Errorf("customFields = %v, want none", got)
	}
}

// grain/task-105: a schedule carries values of its own for the declared
// fields, checked against those declarations when it is saved rather
// than at three in the morning when it fires, and returned on the wire
// so a form can render what it will file.
func TestScheduleCarriesCustomFieldValues(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "team"},
		model.CustomField{Name: "priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}},
	)

	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "Nightly dependency bump", Repo: "acme/widgets",
		Recurrence:   ui.Recurrence{Kind: "everyNHours", EveryNHours: 24},
		CustomFields: map[string]ui.CustomFieldValue{"team": {" infra "}, "priority": {"low"}},
	})
	if err != nil {
		t.Fatalf("creating a schedule: %v", err)
	}
	if !slices.Equal(sched.CustomFields["team"], ui.CustomFieldValue{"infra"}) ||
		!slices.Equal(sched.CustomFields["priority"], ui.CustomFieldValue{"low"}) {
		t.Errorf("customFields = %v, want both, canonicalized", sched.CustomFields)
	}

	// Refused to whoever is writing it down, who can fix it, rather than
	// to the firing nobody is watching.
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		CustomFields: map[string]ui.CustomFieldValue{"priority": {"urgent"}},
	}); err == nil {
		t.Error("a value outside the declared options was accepted onto a schedule")
	}
	if _, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Title: "x", Repo: "acme/widgets", Recurrence: everyDay,
		CustomFields: map[string]ui.CustomFieldValue{"custmer": {"acme"}},
	}); err == nil {
		t.Error("a value for a field nobody declares was accepted onto a schedule")
	}

	// An edit replaces the declared fields wholesale, the pane having
	// rendered them all -- and leaves alone a value whose declaration has
	// since been deleted, exactly as an edit to a task does.
	if err := store.UpdateSchedule(ctx, sched.ID, func(s *model.Schedule) error {
		s.CustomFields["retired"] = []string{"kept"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	values := map[string]ui.CustomFieldValue{"team": {"platform"}, "priority": {""}}
	updated, err := c.UpdateSchedule(ctx, sched.ID, ui.UpdateScheduleRequest{CustomFields: &values})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if !slices.Equal(updated.CustomFields["team"], ui.CustomFieldValue{"platform"}) {
		t.Errorf("customFields = %v, want the edited value", updated.CustomFields)
	}
	if _, ok := updated.CustomFields["priority"]; ok {
		t.Errorf("customFields = %v, want the blanked one cleared", updated.CustomFields)
	}
	if !slices.Equal(updated.CustomFields["retired"], ui.CustomFieldValue{"kept"}) {
		t.Errorf("customFields = %v, want the value whose field was deleted left alone",
			updated.CustomFields)
	}
}

// The same for a template, which is where a schedule's values come from
// while it fires from one.
func TestTemplateCarriesCustomFieldValues(t *testing.T) {
	t.Parallel()
	c, store, ctx := testClient(t)
	declareFields(t, store, ctx,
		model.CustomField{Name: "team"},
		model.CustomField{Name: "components", Multiple: true},
	)

	tmpl, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "Dependency bump", Title: "Bump dependencies",
		CustomFields: map[string]ui.CustomFieldValue{
			"team": {"infra"},
			// A field declared to hold a set carries one here as
			// anywhere else (grain/task-106): a template is not a
			// second, narrower place to say what a task can say.
			"components": {"store", "prompt"},
		},
	})
	if err != nil {
		t.Fatalf("creating a template: %v", err)
	}
	if !slices.Equal(tmpl.CustomFields["team"], ui.CustomFieldValue{"infra"}) {
		t.Errorf("customFields = %v, want the template's own", tmpl.CustomFields)
	}
	if !slices.Equal(tmpl.CustomFields["components"], ui.CustomFieldValue{"prompt", "store"}) {
		t.Errorf("customFields = %v, want the whole set, sorted", tmpl.CustomFields)
	}
	// And a single-valued field refuses a set here for the reason it
	// refuses one on a task: the declaration says how many it holds.
	if _, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "y", Title: "y", CustomFields: map[string]ui.CustomFieldValue{"team": {"infra", "platform"}},
	}); err == nil {
		t.Error("two values for a single-valued field were accepted onto a template")
	}
	if _, err := c.CreateTemplate(ctx, ui.CreateTemplateRequest{
		Name: "x", Title: "x", CustomFields: map[string]ui.CustomFieldValue{"nope": {"x"}},
	}); err == nil {
		t.Error("a value for a field nobody declares was accepted onto a template")
	}

	values := map[string]ui.CustomFieldValue{"team": {"platform"}}
	updated, err := c.UpdateTemplate(ctx, tmpl.ID, ui.UpdateTemplateRequest{CustomFields: &values})
	if err != nil {
		t.Fatalf("updating: %v", err)
	}
	if !slices.Equal(updated.CustomFields["team"], ui.CustomFieldValue{"platform"}) {
		t.Errorf("customFields = %v, want the edited value", updated.CustomFields)
	}

	// A schedule attached to a template takes the template's values with
	// the rest of its content, so what the schedule says it will file and
	// what it files are the same thing.
	sched, err := c.CreateSchedule(ctx, ui.CreateScheduleRequest{
		Repo: "acme/widgets", TemplateID: tmpl.ID, Recurrence: everyDay,
	})
	if err != nil {
		t.Fatalf("creating a template-backed schedule: %v", err)
	}
	if !slices.Equal(sched.CustomFields["team"], ui.CustomFieldValue{"platform"}) {
		t.Errorf("customFields = %v, want the template's own", sched.CustomFields)
	}
}
