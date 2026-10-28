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

package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// template.agent_model and schedule.agent_model (model.Template.Model,
// model.Schedule.Model) go out to the state repository and come
// back through the settings import -- the path a merged pull request
// against grain/config takes -- with the model a template or a schedule
// chose intact.
func TestTemplateAndScheduleModelsRoundTripThroughTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	tmpl := model.Template{
		ID: "template-1", Name: "Weekly sweep", Title: "Sweep", Body: "sweep it",
		Model: "claude-opus-5-5", CreatedAt: now,
	}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("putting the template: %v", err)
	}
	sched := model.Schedule{
		ID: "sched-1", Title: "Nightly", Body: "nightly work",
		Target:     model.RepoRef{Owner: "owner", Name: "payments-api"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 60},
		Model:      "gemini-3.1-pro", Enabled: true, NextRunAt: now, CreatedAt: now,
	}
	if err := store.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("putting the schedule: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// Written in the authoring format (decl.go), under the same `model`
	// key a repository's own .grain files use.
	for table, want := range map[string]string{"template": tmpl.Model, "schedule": sched.Model} {
		path := staterepo.DumpPath(table)
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(raw), "model: "+want) {
			t.Errorf("%s does not carry model %q:\n%s", path, want, raw)
		}
	}

	other, otherDB := openDB(t)
	if err := staterepo.ImportTables(ctx, otherDB, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing the settings tables: %v", err)
	}
	gotTmpl, err := other.GetTemplate(ctx, tmpl.ID)
	if err != nil || gotTmpl == nil {
		t.Fatalf("reading the imported template: %v, %v", gotTmpl, err)
	}
	if gotTmpl.Model != tmpl.Model {
		t.Errorf("template model after the round trip = %q, want %q", gotTmpl.Model, tmpl.Model)
	}
	gotSched, err := other.GetSchedule(ctx, sched.ID)
	if err != nil || gotSched == nil {
		t.Fatalf("reading the imported schedule: %v, %v", gotSched, err)
	}
	if gotSched.Model != sched.Model {
		t.Errorf("schedule model after the round trip = %q, want %q", gotSched.Model, sched.Model)
	}
}

// A settings file written before the column -- one whose templates and
// schedules say nothing about a model -- is warned about by `grain state
// check` like any settings column it leaves out, and the default it
// would land on is the empty string: nobody chose, so the team's
// default model runs the work. A revert of a preference rather than of
// anything a task may touch.
func TestASettingsFileWithoutModelsWarnsTheyGoBackToTheTeamDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	tmpl := model.Template{ID: "template-1", Name: "Sweep", Title: "Sweep", Body: "b", Model: "claude-opus-5-5", CreatedAt: now}
	if err := store.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("putting the template: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	path := filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("template")))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var kept []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) != "model: "+tmpl.Model {
			kept = append(kept, line)
		}
	}
	if len(kept) == len(strings.Split(string(raw), "\n")) {
		t.Fatalf("the export carries no model line to take out:\n%s", raw)
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}

	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	_, checkDB := openDB(t)
	report, err := staterepo.Check(ctx, checkDB, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	var warned string
	for _, w := range report.Warnings {
		if strings.Contains(w, "agent_model") {
			warned = w
		}
	}
	if warned == "" || !strings.Contains(warned, "default ''") {
		t.Errorf("want a warning that template.agent_model lands on its default '', got %q (all: %v)",
			warned, report.Warnings)
	}

	if err := staterepo.ImportTables(ctx, db, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("applying: %v", err)
	}
	got, err := store.GetTemplate(ctx, tmpl.ID)
	if err != nil || got == nil {
		t.Fatalf("reading the template back: %v, %v", got, err)
	}
	if got.Model != "" {
		t.Errorf("model after applying a file without it = %q, want the empty default", got.Model)
	}
}
