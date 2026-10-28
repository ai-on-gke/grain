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

// A custom field declaration withdrawn from the state repository while
// tasks still carry values for it (grain/task-53).
//
// The two halves have tests of their own -- the declarations are a
// settings table here, the values are task data on the task -- and the
// migration case is where the two meet: `custom_field` is decided in this
// repository and nowhere else, so withdrawing a declaration is a merged
// pull request against tables/custom_field.json, applied to a deployment
// whose tasks are already holding values against it. What happens to
// those values is what these tests pin, and they do it the way it
// actually happens: a real remote, a real merge, staterepo.Apply, and the
// database read back afterwards.
//
// The answer, in one line: the values survive. Nothing walks the tasks to
// strip them (schema.go deliberately puts no foreign key between
// task_custom_field and custom_field), and
// model.NormalizeCustomFieldValues leaves exactly one thing sayable about
// such a value -- clear it. What changes is only who will describe one:
// the task pane shows it as an undeclared leftover (ui/src/components/
// CustomFields.jsx's fieldRows) and a run is still told it
// (orchestrator.customFieldsSection), while everything built out of the
// declarations -- the filters, the columns, the forms -- has nothing to
// offer for a field that is no longer declared.

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// declaringDeployment is a deployment with two fields declared and one
// task carrying a value for each, its state repository already seeded
// from that database -- the state every test here starts a merge from.
type declaringDeployment struct {
	store  *model.Store
	db     *sql.DB
	repo   *staterepo.Repo
	remote string
	ctx    context.Context
}

func deploymentDeclaringFields(t *testing.T) declaringDeployment {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	for _, f := range []model.CustomField{
		{Name: "priority", Label: "Priority", Type: model.CustomFieldEnum,
			Options: []string{"low", "high"}, Position: 1},
		{Name: "customer", Label: "Customer"},
	} {
		if err := store.PutCustomField(ctx, f); err != nil {
			t.Fatalf("declaring %s: %v", f.Name, err)
		}
	}
	tk := task("a1b2")
	tk.CustomFields = map[string][]string{"priority": {"high"}, "customer": {"acme"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatalf("putting a task: %v", err)
	}

	remote := bareRemote(t)
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("seeding the repository: %v", err)
	}
	return declaringDeployment{store: store, db: db, repo: repo, remote: remote, ctx: ctx}
}

// merge is the pull request somebody opened against the declarations and
// somebody else merged: clone the remote, rewrite
// tables/custom_field.json with edit applied to its rows, commit, push,
// and let the running deployment apply it. Rows in, rows out, because a
// declaration is a row and what a reviewer merges is a row appearing,
// disappearing or changing.
func (d declaringDeployment) merge(t *testing.T, message string, edit func([]map[string]any) []map[string]any) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", d.remote, work)
	path := filepath.Join(work, staterepo.TablesDir, "custom_field.json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(read(t, path)), &rows); err != nil {
		t.Fatalf("parsing the dump: %v", err)
	}
	out, err := json.MarshalIndent(edit(rows), "", "  ")
	if err != nil {
		t.Fatalf("writing the dump: %v", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("editing the dump: %v", err)
	}
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", message)
	git(t, work, "push", "--quiet", "origin", "main")

	applied, err := staterepo.Apply(t.Context(), d.repo, d.db, model.SchemaVersion)
	if err != nil {
		t.Fatalf("applying: %v", err)
	}
	if !applied {
		t.Fatal("a merged commit arrived and nothing was applied")
	}
}

// without drops the row declaring name -- a field retired in review.
func without(name string) func([]map[string]any) []map[string]any {
	return func(rows []map[string]any) []map[string]any {
		out := []map[string]any{}
		for _, r := range rows {
			if r["name"] != name {
				out = append(out, r)
			}
		}
		return out
	}
}

func declaredNames(t *testing.T, store *model.Store, ctx context.Context) []string {
	t.Helper()
	fields, err := store.ListCustomFields(ctx)
	if err != nil {
		t.Fatalf("listing declarations: %v", err)
	}
	names := make([]string, 0, len(fields))
	for _, f := range fields {
		names = append(names, f.Name)
	}
	return names
}

func valuesOf(t *testing.T, store *model.Store, ctx context.Context, taskID string) map[string][]string {
	t.Helper()
	got, err := store.GetTask(ctx, taskID)
	if err != nil || got == nil {
		t.Fatalf("reading %s: %v (nil=%v)", taskID, err, got == nil)
	}
	return got.CustomFields
}

// The migration case itself: a declaration deleted by a merge, with a
// task still holding a value for it.
func TestADeclarationAMergeWithdrewLeavesTheValuesTasksCarry(t *testing.T) {
	t.Parallel()
	d := deploymentDeclaringFields(t)
	d.merge(t, "Retire the priority field", without("priority"))

	// The declaration is gone, which is what the merge said.
	if got, err := d.store.GetCustomField(d.ctx, "priority"); err != nil || got != nil {
		t.Fatalf("priority is still declared (%+v, err %v) after a merge that withdrew it", got, err)
	}
	if got := declaredNames(t, d.store, d.ctx); !slices.Equal(got, []string{"customer"}) {
		t.Fatalf("declarations = %v, want only customer", got)
	}

	// The value is not: it is what somebody recorded about this task, and
	// a declaration going away does not make it untrue.
	values := valuesOf(t, d.store, d.ctx, "a1b2")
	if !slices.Equal(values["priority"], []string{"high"}) {
		t.Errorf("customFields = %v, want the withdrawn field's value still on the task", values)
	}
	if !slices.Equal(values["customer"], []string{"acme"}) {
		t.Errorf("customFields = %v, want the field that is still declared untouched", values)
	}

	// And there is exactly one thing left to say about it: an edit naming
	// it may clear it, and may not set it to anything, since a value
	// nothing declares can be shown nowhere and found by nobody.
	fields, err := d.store.ListCustomFields(d.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.NormalizeCustomFieldValues(fields, map[string][]string{"priority": {"low"}}); err == nil {
		t.Error("a withdrawn field was accepted as something to set")
	}
	cleared, err := model.NormalizeCustomFieldValues(fields, map[string][]string{"priority": {""}})
	if err != nil {
		t.Errorf("clearing a withdrawn field: %v", err)
	}
	if len(cleared) != 0 {
		t.Errorf("clearing a withdrawn field left %v", cleared)
	}
}

// The other half of "the values survive": they survive the repository
// too. A value whose declaration is gone is still exported with its task
// and still imported back -- so a dump restored onto a fresh host has the
// same orphaned value, rather than having quietly dropped what no file
// could describe.
func TestTheValuesOfAWithdrawnDeclarationStillRoundTripThroughTheRepository(t *testing.T) {
	t.Parallel()
	d := deploymentDeclaringFields(t)
	d.merge(t, "Retire the priority field", without("priority"))

	dir := t.TempDir()
	if err := staterepo.Export(d.ctx, d.db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// The value is in the dump under its own name, with no declaration
	// anywhere near it: that is what a reviewer of this repository sees.
	dump := read(t, filepath.Join(dir, staterepo.TablesDir, "task_custom_field.json"))
	if !strings.Contains(dump, `"priority"`) || !strings.Contains(dump, `"high"`) {
		t.Fatalf("task_custom_field.json lost the withdrawn field's value:\n%s", dump)
	}
	if fields := read(t, filepath.Join(dir, staterepo.TablesDir, "custom_field.json")); strings.Contains(fields, `"priority"`) {
		t.Fatalf("custom_field.json still declares the withdrawn field:\n%s", fields)
	}

	other, otherDB := openDB(t)
	if err := staterepo.Import(d.ctx, otherDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	if got := valuesOf(t, other, d.ctx, "a1b2"); !slices.Equal(got["priority"], []string{"high"}) {
		t.Errorf("customFields after a round trip = %v, want the orphaned value intact", got)
	}
	if got, err := other.GetCustomField(d.ctx, "priority"); err != nil || got != nil {
		t.Errorf("the round trip brought the withdrawn declaration back: %+v (err %v)", got, err)
	}
}

// Renaming one is deleting a field and declaring another
// (model.CustomField.Name): the values already on tasks are keyed by the
// name, and nothing rewrites them. So a merge that renames a declaration
// leaves the old values behind under the old name and the new field empty
// on every task -- which is the thing to know before writing that pull
// request, and the reason the name is not what a human reads (Label is).
func TestRenamingADeclarationDoesNotCarryTheValuesAcross(t *testing.T) {
	t.Parallel()
	d := deploymentDeclaringFields(t)
	d.merge(t, "Rename priority to urgency", func(rows []map[string]any) []map[string]any {
		for _, r := range rows {
			if r["name"] == "priority" {
				r["name"] = "urgency"
			}
		}
		return rows
	})

	renamed, err := d.store.GetCustomField(d.ctx, "urgency")
	if err != nil || renamed == nil {
		t.Fatalf("urgency is not declared after the rename: %v (nil=%v)", err, renamed == nil)
	}
	if !slices.Equal(renamed.Options, []string{"low", "high"}) {
		t.Errorf("options = %v, want the declaration's own, carried over by the merge", renamed.Options)
	}
	if got, err := d.store.GetCustomField(d.ctx, "priority"); err != nil || got != nil {
		t.Fatalf("priority is still declared (%+v, err %v) after being renamed away", got, err)
	}

	values := valuesOf(t, d.store, d.ctx, "a1b2")
	if !slices.Equal(values["priority"], []string{"high"}) {
		t.Errorf("customFields = %v, want the value still under the name it was written with", values)
	}
	if _, ok := values["urgency"]; ok {
		t.Errorf("customFields = %v, want the renamed field to start empty on every task", values)
	}
}
