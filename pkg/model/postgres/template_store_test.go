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

package postgres_test

// The template store (bwsalmon/agents#516), against a real embedded
// SQLite database -- store_test.go's own doc comment on why gives the
// reasoning again here, and schedule_store_test.go's own shape is the
// pattern this file follows almost field for field.

import (
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func template(id string) model.Template {
	return model.Template{
		ID:        id,
		Name:      "Dependency bump",
		Title:     "Bump dependencies",
		Body:      "Bump every dependency to its latest patch release.",
		CreatedAt: now,
	}
}

func TestTemplateRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := template("template-1")
	want.AutoMerge = true
	want.Reads = []model.RepoRef{{Owner: "owner", Name: "shared-lib"}}
	want.Writes = []model.WriteTarget{
		{Repo: model.RepoRef{Owner: "owner", Name: "deploy-config"}, Base: "deploy"},
	}
	want.Grants = []model.Grant{{Capability: "web-search", Via: model.GrantByLabel}}
	want.CustomFields = map[string][]string{"team": {"infra"}}

	if err := store.PutTemplate(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetTemplate(ctx, "template-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.Name != want.Name || got.Title != want.Title || got.Body != want.Body {
		t.Errorf("text did not survive: %+v", got)
	}
	if !got.AutoMerge {
		t.Errorf("declared fields did not survive: %+v", got)
	}
	if len(got.Reads) != 1 || got.Reads[0] != want.Reads[0] {
		t.Errorf("reads = %+v, want %+v", got.Reads, want.Reads)
	}
	// The branch as well as the repo (grain/task-86): a write target
	// without its base is half a declaration, and the half that decides
	// which branch a firing's proposal is filed against.
	if len(got.Writes) != 1 || got.Writes[0] != want.Writes[0] {
		t.Errorf("writes = %+v, want %+v", got.Writes, want.Writes)
	}
	if len(got.Grants) != 1 || got.Grants[0].Capability != "web-search" {
		t.Errorf("grants = %+v, want web-search", got.Grants)
	}
	// The values every task filed from this template carries for this
	// deployment's declared fields (grain/task-105), on their own table
	// beside the reads and the grants.
	if !slices.Equal(got.CustomFields["team"], []string{"infra"}) {
		t.Errorf("customFields = %v, want the template's own", got.CustomFields)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, now)
	}
	if got.Target != nil || got.Base != "" {
		t.Errorf("binding = (%+v, %q), want a template bound to nothing", got.Target, got.Base)
	}
}

// A binding is optional, so both halves of the choice need to survive a
// round trip: the unbound template above, and this one, bound to a repo
// and a branch (grain/task-285).
func TestBoundTemplateRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := template("template-1")
	want.Target = &model.RepoRef{Owner: "owner", Name: "payments-api"}
	want.Base = "release"

	if err := store.PutTemplate(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetTemplate(ctx, "template-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if got.Target == nil || *got.Target != *want.Target || got.Base != "release" {
		t.Fatalf("binding did not survive: (%+v, %q)", got.Target, got.Base)
	}

	// Unbinding is a write like any other, and has to clear the repo
	// rather than leave the old one readable underneath.
	want.Target, want.Base = nil, ""
	if err := store.PutTemplate(ctx, want); err != nil {
		t.Fatalf("put unbound: %v", err)
	}
	got, err = store.GetTemplate(ctx, "template-1")
	if err != nil || got == nil {
		t.Fatalf("get after unbinding: %v (nil=%v)", err, got == nil)
	}
	if got.Target != nil || got.Base != "" {
		t.Fatalf("binding = (%+v, %q), want it cleared", got.Target, got.Base)
	}
}

// The inputs a template asks for when somebody instantiates it
// (model.TemplateField, grain/task-58), on their own table beside the
// reads and the grants -- including the order they were declared in,
// which is the order a form asks for them and which a row order sorted
// by anything else would quietly lose.
func TestTemplateFieldsRoundTrip(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	want := template("template-1")
	want.Title = "Bump {{dependency}}"
	want.Fields = []model.TemplateField{
		{Name: "dependency", Label: "Dependency", Required: true},
		{Name: "environment", Options: []string{"staging", "production"}, Default: "staging"},
	}

	if err := store.PutTemplate(ctx, want); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.GetTemplate(ctx, "template-1")
	if err != nil || got == nil {
		t.Fatalf("get: %v (nil=%v)", err, got == nil)
	}
	if len(got.Fields) != 2 {
		t.Fatalf("fields = %+v, want both of them", got.Fields)
	}
	if got.Fields[0].Name != "dependency" || got.Fields[1].Name != "environment" {
		t.Errorf("fields = %+v, want the declared order", got.Fields)
	}
	if got.Fields[0].Label != "Dependency" || !got.Fields[0].Required {
		t.Errorf("text field = %+v, want its label and its required flag", got.Fields[0])
	}
	if !slices.Equal(got.Fields[1].Options, []string{"staging", "production"}) ||
		got.Fields[1].Default != "staging" {
		t.Errorf("selector = %+v, want its options in order and its default", got.Fields[1])
	}

	// Dropping a field is a write like any other: the row has to go,
	// not sit under the new declaration where a firing would still fill
	// it in.
	want.Fields = want.Fields[:1]
	if err := store.PutTemplate(ctx, want); err != nil {
		t.Fatalf("put again: %v", err)
	}
	got, err = store.GetTemplate(ctx, "template-1")
	if err != nil || got == nil {
		t.Fatalf("get after dropping one: %v (nil=%v)", err, got == nil)
	}
	if len(got.Fields) != 1 || got.Fields[0].Name != "dependency" {
		t.Errorf("fields = %+v, want only the one still declared", got.Fields)
	}
}

// ListTemplates reads through its own SELECT rather than getTemplate's,
// so the binding it scans is worth checking separately -- a column list
// that drifts between the two would otherwise only show up as a
// template that reads bound one way and unbound the other.
func TestListTemplatesReadsBindings(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	bound := template("template-1")
	bound.Target = &model.RepoRef{Owner: "owner", Name: "payments-api"}
	bound.Base = "release"
	if err := store.PutTemplate(ctx, bound); err != nil {
		t.Fatalf("put bound: %v", err)
	}
	unbound := template("template-2")
	if err := store.PutTemplate(ctx, unbound); err != nil {
		t.Fatalf("put unbound: %v", err)
	}

	list, err := store.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]model.Template{}
	for _, tmpl := range list {
		byID[tmpl.ID] = tmpl
	}
	if got := byID["template-1"]; got.Target == nil || *got.Target != *bound.Target || got.Base != "release" {
		t.Errorf("template-1 = (%+v, %q), want its binding", got.Target, got.Base)
	}
	if got := byID["template-2"]; got.Target != nil || got.Base != "" {
		t.Errorf("template-2 = (%+v, %q), want no binding", got.Target, got.Base)
	}
}

func TestGetTemplateReturnsNilWhenAbsent(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	got, err := store.GetTemplate(ctx, "nope")
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil), got (%v, %v)", got, err)
	}
}

func TestNewTemplateIDsAreDistinctFromTaskAndScheduleIDs(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	taskID, err := store.NewTaskID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	schedID, err := store.NewScheduleID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	templateID, err := store.NewTemplateID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if templateID == taskID || templateID == schedID {
		t.Fatalf("template id %q collided with task id %q or schedule id %q", templateID, taskID, schedID)
	}
	if templateID[:9] != "template-" {
		t.Errorf("template id = %q, want a template- prefix", templateID)
	}
}

func TestListTemplatesReturnsNewestFirst(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	earlier := template("template-1")
	earlier.CreatedAt = now.Add(-time.Hour)
	later := template("template-2")
	later.CreatedAt = now
	for _, tmpl := range []model.Template{earlier, later} {
		if err := store.PutTemplate(ctx, tmpl); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.ListTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "template-2" || got[1].ID != "template-1" {
		t.Fatalf("list = %+v, want [template-2, template-1]", got)
	}
}

func TestUpdateTemplateAppliesAndPersists(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTemplate(ctx, template("template-1")); err != nil {
		t.Fatal(err)
	}
	err := store.UpdateTemplate(ctx, "template-1", func(tmpl *model.Template) error {
		tmpl.Title = "Bump dependencies (patch only)"
		tmpl.AutoMerge = true
		p := 8
		tmpl.Priority = &p
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := store.GetTemplate(ctx, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Bump dependencies (patch only)" || !got.AutoMerge || got.Priority == nil || *got.Priority != 8 {
		t.Errorf("update did not persist: %+v", got)
	}
}

func TestUpdateTemplateOnAnUnknownIDErrors(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	err := store.UpdateTemplate(ctx, "nope", func(*model.Template) error { return nil })
	if err == nil {
		t.Fatal("want an error updating an unknown template, got nil")
	}
}

func TestDeleteTemplateRemovesIt(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTemplate(ctx, template("template-1")); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTemplate(ctx, "template-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, err := store.GetTemplate(ctx, "template-1")
	if err != nil || got != nil {
		t.Fatalf("want (nil, nil) after delete, got (%v, %v)", got, err)
	}
}

func TestSchedulesUsingTemplate(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTemplate(ctx, template("template-1")); err != nil {
		t.Fatal(err)
	}
	if err := store.PutTemplate(ctx, template("template-2")); err != nil {
		t.Fatal(err)
	}
	id := "template-1"
	pointsAtOne := schedule("sched-1", now)
	pointsAtOne.TemplateID = &id
	pointsAtNone := schedule("sched-2", now)
	for _, s := range []model.Schedule{pointsAtOne, pointsAtNone} {
		if err := store.PutSchedule(ctx, s); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.SchedulesUsingTemplate(ctx, "template-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "sched-1" {
		t.Fatalf("schedules using template-1 = %+v, want only sched-1", got)
	}

	got, err = store.SchedulesUsingTemplate(ctx, "template-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("schedules using template-2 = %+v, want none", got)
	}
}
