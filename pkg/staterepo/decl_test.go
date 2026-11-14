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

// The settings documents: what the export writes for a template, a suite
// and a schedule now that the three of them are nested rather than
// scattered across thirteen files of rows (decl.go).
//
// The round trip itself is not tested here -- fidelity_test.go already
// exports a database with a row in every table, imports it into an empty
// one and compares every column with typeof() alongside it, which is the
// strongest thing that can be said about these files and now says it
// about them. What is here is the half that test cannot see: that the
// file reads like the format a repository writes, that every settings
// field of every kind reaches it, and that a hand edit of one is applied
// the way a hand edit of a row was.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/declschema"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// settingsDocument reads one of the three documents out of a repository
// as the list it holds, with each entry a plain map.
func settingsDocument(t *testing.T, root, table string) (path string, list string, entries []map[string]any) {
	t.Helper()
	path = filepath.Join(root, filepath.FromSlash(staterepo.DumpPath(table)))
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(read(t, path)), &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for name, value := range doc {
		items, ok := value.([]any)
		if !ok {
			continue
		}
		list = name
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				t.Fatalf("%s holds an entry that is not a mapping: %#v", path, item)
			}
			entries = append(entries, entry)
		}
	}
	if list == "" {
		t.Fatalf("%s holds no list of settings:\n%s", path, read(t, path))
	}
	return path, list, entries
}

// editSettings is a pull request against one of the documents: read it,
// change an entry, write it back. The equivalent of rewriteRows one file
// shape along.
func editSettings(t *testing.T, root, table string, edit func(entries []map[string]any)) {
	t.Helper()
	path, list, entries := settingsDocument(t, root, table)
	edit(entries)
	rewritten := make([]any, 0, len(entries))
	for _, entry := range entries {
		rewritten = append(rewritten, entry)
	}
	data, err := yaml.Marshal(map[string]any{list: rewritten})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("editing %s: %v", path, err)
	}
}

// What the file actually looks like, which is the whole point of the
// change: hyphenated keys, the cadence as one block rather than five
// columns with four of them NULL, the relations as nested lists, and a
// body a reviewer can read as the prose it is.
func TestASettingsDocumentIsWrittenInTheAuthoringFormat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep",
		Body:      "Do the sweep.\nThen write it down.\n",
		Target:    &repo,
		Base:      "main",
		AutoMerge: true,
		Reads:     []model.RepoRef{{Owner: "bwsalmon", Name: "notes"}},
		Writes:    []model.WriteTarget{{Repo: model.RepoRef{Owner: "bwsalmon", Name: "docs"}}},
		Grants:    []model.Grant{{Capability: "memory", Via: model.GrantByLabel}},
		Fields: []model.TemplateField{
			{Name: "area", Label: "Which area", Options: []string{"ui", "api"}, Default: "ui", Required: true},
		},
		CustomFields: map[string][]string{"team": {"platform"}},
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	id := "tpl-1"
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Nightly", TemplateID: &id, Target: repo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	templates := read(t, filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("template"))))
	for _, want := range []string{
		"- id: tpl-1",
		"auto-merge: true",
		"target: bwsalmon/grain",
		"reads:\n      - bwsalmon/notes",
		"writes:\n      - bwsalmon/docs",
		"grants:\n      - memory",
		"custom-fields:\n      team: platform",
		"options: [ui, api]",
		"required: true",
		"body: |\n      Do the sweep.\n      Then write it down.",
	} {
		if !strings.Contains(templates, want) {
			t.Errorf("the templates document does not hold %q:\n%s", want, templates)
		}
	}
	// And the five recurrence columns, four of them NULL, are one block.
	schedules := read(t, filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("schedule"))))
	if !strings.Contains(schedules, "daily:\n      at: \"09:00\"") {
		t.Errorf("the schedules document does not carry the cadence as a block:\n%s", schedules)
	}
	for _, gone := range []string{"recurrence_kind", "every_n_hours", "time_of_day_minutes", "day_of_month"} {
		if strings.Contains(schedules, gone) {
			t.Errorf("the schedules document still writes the column %q:\n%s", gone, schedules)
		}
	}
	// The thirteen files it stands in for are not written, and are not
	// left behind by an earlier build either.
	for _, table := range []string{
		"template", "template_read", "template_write", "template_grant", "template_field",
		"template_custom_field", "suite", "suite_item", "schedule", "schedule_read",
		"schedule_write", "schedule_grant", "schedule_custom_field"} {
		if _, err := os.Stat(filepath.Join(dir, staterepo.TablesDir, table+".json")); err == nil {
			t.Errorf("the export still writes tables/%s.json beside the settings documents", table)
		}
	}
}

// Every key the settings context of pkg/declschema declares reaches the
// document, and nothing else does.
//
// This is the anti-drift half. pkg/declschema already pins its own
// entries against pkg/model's fields and against the columns of the
// three tables, in both directions; this closes the loop by pinning the
// file the exporter writes against the same list, so a column added to
// `schedule` cannot land as a key nobody decided the spelling of.
func TestEverySettingsKeyReachesTheDocument(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	fill(t, ctx, store, db)
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	for _, schema := range declschema.Declarations() {
		_, _, entries := settingsDocument(t, dir, schema.Table)
		if len(entries) == 0 {
			t.Fatalf("the %s document is empty, so nothing here is checking anything", schema.Table)
		}
		written := map[string]bool{}
		for _, entry := range entries {
			for key := range entry {
				written[key] = true
			}
		}
		allowed := map[string]bool{}
		for _, key := range schema.Keys(declschema.Settings) {
			allowed[key] = true
		}
		// The four cadence blocks are one field written under whichever
		// of them the schedule's kind calls for, plus the explicit form
		// for a row that fits none of them (decl.go's recurrenceNode).
		allowed["recurrence"] = true
		for key := range written {
			if !allowed[key] {
				t.Errorf("the %s document writes %q, which pkg/declschema does not carry in settings",
					schema.Table, key)
			}
		}
		// Every key with a column behind it is written on every entry:
		// a settings file that leaves one out is a row that lands on the
		// schema's default when it is imported, which is a loss rather
		// than a tidier file (bind.go's SettingsTables).
		for _, f := range schema.Fields {
			key, ok := f.KeyIn(declschema.Settings)
			if !ok || len(f.Columns) == 0 {
				continue
			}
			if f.Model == "Recurrence" {
				continue
			}
			for i, entry := range entries {
				if _, carried := entry[key]; !carried {
					t.Errorf("%s entry %d does not write %q, so importing it resets %v to the schema's default",
						schema.Table, i, key, f.Columns)
				}
			}
		}
	}
}

// A hand edit of a document is applied exactly as a hand edit of a row
// was: this is the format people are meant to write in, and the claim
// the whole repository rests on is that a merged change to it is live on
// the next tick.
func TestAHandEditOfASettingsDocumentIsApplied(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", CreatedAt: now,
		Grants: []model.Grant{{Capability: "api:memory", Via: model.GrantByLabel}},
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	editSettings(t, work, "template", func(entries []map[string]any) {
		for _, entry := range entries {
			if entry["id"] != "tpl-1" {
				continue
			}
			entry["title"] = "Run the nightly sweep, twice"
			entry["grants"] = []any{"api:memory", "self-debug"}
			entry["fields"] = []any{map[string]any{"name": "area", "label": "Which area", "default": "ui"}}
		}
	})
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Teach the nightly sweep")
	git(t, work, "push", "--quiet", "origin", "main")

	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	tmpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tmpl == nil {
		t.Fatalf("reading the template back: %v %v", tmpl, err)
	}
	if tmpl.Title != "Run the nightly sweep, twice" {
		t.Errorf("the title did not land: %q", tmpl.Title)
	}
	// self-debug lands as what it became (model.SelfDebugGrants).
	// api:memory lands as its reference too: an import is migration 132's
	// rewrite as well (model.MigrateCanonicalGrants).
	if len(tmpl.Grants) != 3 || tmpl.Grants[0].Capability != model.APIRef("memory") || tmpl.Grants[1].Capability != model.APIRef("self-debug") ||
		tmpl.Grants[2].Capability != model.GrainSourceGrant {
		t.Errorf("the grants did not land: %+v", tmpl.Grants)
	}
	if len(tmpl.Fields) != 1 || tmpl.Fields[0].Name != "area" || tmpl.Fields[0].Default != "ui" {
		t.Errorf("the inputs did not land: %+v", tmpl.Fields)
	}
}

// unified-grants §3: grain/config's `grants:` and `reads:`/`writes:` are
// one key on import. A task-level repository grant under grants: lands
// in template_read / template_write, an entry both lists name is held
// once, {capability, with} folds into the stored reference, a legacy api:
// spelling is stored as its reference, and the next export writes
// grants: beside reads: and writes:, which an older binary still reads.
func TestSettingsGrantsAndReadsWritesAreOneKey(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep", CreatedAt: now}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	editSettings(t, work, "template", func(entries []map[string]any) {
		for _, entry := range entries {
			if entry["id"] != "tpl-1" {
				continue
			}
			entry["reads"] = []any{"acme/docs"}
			entry["grants"] = []any{
				"api:ci:read@acme/widgets",
				map[string]any{"capability": model.RepoReadRef, "with": map[string]any{"repo": "acme/docs"}},
				map[string]any{"capability": model.RepoReadRef, "with": map[string]any{"repo": "acme/tools"}},
				model.RepoWriteRef + `={"base":"main","repo":"acme/site"}`,
				map[string]any{"capability": model.APIRef("tasks:read"), "with": map[string]any{}},
				"api:tasks:read",
				model.APIRef("tasks:read"),
			}
		}
	})
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Spell the nightly sweep's grants as one list")
	git(t, work, "push", "--quiet", "origin", "main")

	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	tmpl, err := store.GetTemplate(ctx, "tpl-1")
	if err != nil || tmpl == nil {
		t.Fatalf("reading the template back: %v %v", tmpl, err)
	}
	var reads []string
	for _, r := range tmpl.Reads {
		reads = append(reads, r.String())
	}
	if strings.Join(reads, " ") != "acme/docs acme/tools" {
		t.Errorf("Reads = %v, want acme/docs once and acme/tools", reads)
	}
	if len(tmpl.Writes) != 1 || tmpl.Writes[0].Repo.String() != "acme/site" || tmpl.Writes[0].Base != "main" {
		t.Errorf("Writes = %+v, want acme/site on main", tmpl.Writes)
	}
	var grants []string
	for _, g := range tmpl.Grants {
		grants = append(grants, g.Capability)
	}
	if want := []string{model.APIGrantString("ci:read", "acme/widgets"), model.APIRef("tasks:read")}; strings.Join(grants, " ") != strings.Join(want, " ") {
		t.Errorf("Grants = %v, want %v", grants, want)
	}

	out := t.TempDir()
	if err := staterepo.Export(ctx, db, out); err != nil {
		t.Fatalf("exporting again: %v", err)
	}
	_, _, entries := settingsDocument(t, out, "template")
	for _, entry := range entries {
		if entry["id"] != "tpl-1" {
			continue
		}
		for _, key := range []string{"reads", "writes", "grants"} {
			if _, ok := entry[key]; !ok {
				t.Errorf("the export carries no %s: for the template: %v", key, entry)
			}
		}
		want := []any{
			model.APIGrantString("ci:read", "acme/widgets"), model.APIRef("tasks:read"),
			model.TaskRepoReadGrant(model.RepoRef{Owner: "acme", Name: "docs"}).Key(),
			model.TaskRepoReadGrant(model.RepoRef{Owner: "acme", Name: "tools"}).Key(),
			model.TaskRepoWriteGrant(model.WriteTarget{Repo: model.RepoRef{Owner: "acme", Name: "site"}, Base: "main"}).Key(),
		}
		if got := entry["grants"]; !reflect.DeepEqual(got, want) {
			t.Errorf("exported grants: = %#v, want %#v (the grant rows, then the repository access as grants)", got, want)
		}
	}

	for _, bad := range []any{
		map[string]any{"capability": model.APIRef("tasks:read"), "with": "not-a-map"},
		map[string]any{"capability": model.APIRef("tasks:read"), "with": nil},
		map[string]any{"capability": model.APIRef("ci:read") + `={"repo":"acme/widgets"}`, "with": map[string]any{"repo": "acme/other"}},
		map[string]any{"capability": model.APIRef("ci:write"), "repo": "acme/widgets"},
		map[string]any{"capability": model.APIRef("ci:write"), "wtih": map[string]any{"repo": "acme/widgets"}},
		map[string]any{"capability": "api:ci:read", "with": map[string]any{"repo": "acme/widgets"}},
		map[string]any{"capability": "gcp-key", "with": map[string]any{"permission_set": "ci"}},
		map[string]any{"capability": "https://not-a-method", "with": map[string]any{"a": 1}},
		map[string]any{"capability": nil, "with": map[string]any{"repo": "acme/widgets"}},
	} {
		editSettings(t, work, "template", func(entries []map[string]any) {
			for _, entry := range entries {
				if entry["id"] == "tpl-1" {
					entry["grants"] = []any{bad}
				}
			}
		})
		if _, err := staterepo.Check(ctx, db, work, model.SchemaVersion); err == nil {
			t.Errorf("Check accepted malformed grant %#v", bad)
		}
	}
}

// unified-grants §3: the export writes reads:/writes: under grants: as well,
// as …/repo#read {repo} and …/repo#write {repo, base}, and an import of
// that export holds each repository once. Export → import → export is
// byte-for-byte stable, and the read and write tables are what they were.
func TestRepoAccessExportedAsGrantsRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	offHost, err := model.ParseRepo("git.example.com/acme/mirror")
	if err != nil {
		t.Fatalf("parsing an off-host repository: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Run the nightly sweep",
		Reads: []model.RepoRef{{Owner: "acme", Name: "docs"}, offHost},
		Writes: []model.WriteTarget{
			{Repo: model.RepoRef{Owner: "acme", Name: "site"}, Base: "main"},
			{Repo: model.RepoRef{Owner: "acme", Name: "wiki"}},
		},
		Grants:    []model.Grant{{Capability: model.APIRef("tasks:read"), Via: model.GrantByLabel}},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Nightly", Target: model.RepoRef{Owner: "acme", Name: "widgets"},
		Reads:      []model.RepoRef{{Owner: "acme", Name: "notes"}},
		Writes:     []model.WriteTarget{{Repo: model.RepoRef{Owner: "acme", Name: "site"}, Base: "release"}},
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}

	first := t.TempDir()
	if err := staterepo.Export(ctx, db, first); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	site := model.RepoRef{Owner: "acme", Name: "site"}
	for table, want := range map[string][]any{
		"template": {
			model.APIRef("tasks:read"),
			model.TaskRepoReadGrant(model.RepoRef{Owner: "acme", Name: "docs"}).Key(),
			model.TaskRepoWriteGrant(model.WriteTarget{Repo: site, Base: "main"}).Key(),
			model.TaskRepoWriteGrant(model.WriteTarget{Repo: model.RepoRef{Owner: "acme", Name: "wiki"}}).Key(),
		},
		"schedule": {
			model.TaskRepoReadGrant(model.RepoRef{Owner: "acme", Name: "notes"}).Key(),
			model.TaskRepoWriteGrant(model.WriteTarget{Repo: site, Base: "release"}).Key(),
		},
	} {
		_, _, entries := settingsDocument(t, first, table)
		if len(entries) != 1 {
			t.Fatalf("%s: %d entries, want 1", table, len(entries))
		}
		if got := entries[0]["grants"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: exported grants: = %#v, want %#v (an off-host repository stays under reads: alone)", table, got, want)
		}
		for _, key := range []string{"reads", "writes"} {
			if _, ok := entries[0][key]; !ok {
				t.Errorf("%s: the export no longer writes %s:: %v", table, key, entries[0])
			}
		}
	}

	freshStore, freshDB := openDB(t)
	if err := staterepo.Import(ctx, freshDB, first); err != nil {
		t.Fatalf("importing: %v", err)
	}
	for _, table := range []string{"template_read", "template_write", "template_grant", "schedule_read", "schedule_write", "schedule_grant"} {
		var was, is int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&was); err != nil {
			t.Fatalf("counting %s: %v", table, err)
		}
		if err := freshDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&is); err != nil {
			t.Fatalf("counting imported %s: %v", table, err)
		}
		if was != is {
			t.Errorf("%s holds %d rows after the round trip, want %d", table, is, was)
		}
	}
	tmplWas, _ := store.GetTemplate(ctx, "tpl-1")
	tmplIs, err := freshStore.GetTemplate(ctx, "tpl-1")
	if err != nil || tmplIs == nil {
		t.Fatalf("reading the imported template: %v %v", tmplIs, err)
	}
	if !reflect.DeepEqual(tmplIs.Reads, tmplWas.Reads) || !reflect.DeepEqual(tmplIs.Writes, tmplWas.Writes) || !reflect.DeepEqual(tmplIs.Grants, tmplWas.Grants) {
		t.Errorf("template access after the round trip: reads %v writes %v grants %v, want %v %v %v",
			tmplIs.Reads, tmplIs.Writes, tmplIs.Grants, tmplWas.Reads, tmplWas.Writes, tmplWas.Grants)
	}
	schedWas, _ := store.GetSchedule(ctx, "sched-1")
	schedIs, err := freshStore.GetSchedule(ctx, "sched-1")
	if err != nil || schedIs == nil {
		t.Fatalf("reading the imported schedule: %v %v", schedIs, err)
	}
	if !reflect.DeepEqual(schedIs.Reads, schedWas.Reads) || !reflect.DeepEqual(schedIs.Writes, schedWas.Writes) || !reflect.DeepEqual(schedIs.Grants, schedWas.Grants) {
		t.Errorf("schedule access after the round trip: reads %v writes %v grants %v, want %v %v %v",
			schedIs.Reads, schedIs.Writes, schedIs.Grants, schedWas.Reads, schedWas.Writes, schedWas.Grants)
	}

	second := t.TempDir()
	if err := staterepo.Export(ctx, freshDB, second); err != nil {
		t.Fatalf("exporting again: %v", err)
	}
	for _, table := range []string{"template", "schedule"} {
		path := filepath.FromSlash(staterepo.DumpPath(table))
		if a, b := read(t, filepath.Join(first, path)), read(t, filepath.Join(second, path)); a != b {
			t.Errorf("%s: the second export differs from the first:\n--- first\n%s\n--- second\n%s", table, a, b)
		}
	}
}

func TestScheduleGrainCIRoundTripsAsYAMLBooleanInSettingsSchedules(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "state"), Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	target := model.RepoRef{Owner: "acme", Name: "widgets"}
	wfTmpl := model.WorkflowReviewTemplateID("check")
	for _, sc := range []model.Schedule{
		{
			ID: "sched-off", Title: "Default off", Target: target, TemplateID: &wfTmpl,
			GrainCI:    false,
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
			Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
		},
		{
			ID: "sched-on", Title: "Opted in", Target: target, TemplateID: &wfTmpl,
			GrainCI:    true,
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
			Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
		},
	} {
		if err := store.PutSchedule(ctx, sc); err != nil {
			t.Fatalf("putting schedule %s: %v", sc.ID, err)
		}
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	raw := read(t, filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("schedule"))))
	for _, bad := range []string{"grain-ci: 0", "grain-ci: 1"} {
		if strings.Contains(raw, bad) {
			t.Errorf("settings/schedules.yml wrote integer %q instead of YAML boolean:\n%s", bad, raw)
		}
	}
	for _, want := range []string{"grain-ci: false", "grain-ci: true"} {
		if !strings.Contains(raw, want) {
			t.Errorf("settings/schedules.yml missing %q:\n%s", want, raw)
		}
	}

	// Flipping grain-ci in settings/schedules.yml applies cleanly, and legacy
	// integer 1/0 dumps also remain loadable.
	editSettings(t, work, "schedule", func(entries []map[string]any) {
		for _, entry := range entries {
			switch entry["id"] {
			case "sched-off":
				entry["grain-ci"] = true
			case "sched-on":
				entry["grain-ci"] = 0
			}
		}
	})
	if _, err := staterepo.Check(ctx, db, work, model.SchemaVersion); err != nil {
		t.Fatalf("Check failed on edited schedules.yml: %v", err)
	}
	git(t, work, "-c", "user.email=alice@example.com", "-c", "user.name=Alice", "commit", "-am", "Flip grain-ci")
	git(t, work, "push", "--quiet", "origin", "main")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("Apply: applied=%v err=%v", applied, err)
	}
	off, err := store.GetSchedule(ctx, "sched-off")
	if err != nil || off == nil || !off.GrainCI {
		t.Errorf("sched-off after Apply = %+v, %v, want GrainCI=true", off, err)
	}
	on, err := store.GetSchedule(ctx, "sched-on")
	if err != nil || on == nil || on.GrainCI {
		t.Errorf("sched-on after Apply = %+v, %v, want GrainCI=false", on, err)
	}
}

// Companion rows whose parent is gone survive the round trip.
//
// Under an older build Store.DeleteTemplate deleted the `template` row
// and left template_read, template_grant and the rest behind, so a
// deployment that deleted a template then holds rows no template claims
// -- and so does any state repository it wrote. The dump this replaces
// carried them because it dumped tables; a nested document has nowhere
// to nest them, and a dump that drops rows is not a restore -- so they
// go out verbatim under `orphaned-rows`.
//
// The orphan is written straight into the table rather than made by
// deleting a template, because deleting a template no longer makes one
// (grain/task-380): this is the database an older build hands over, and
// carrying it is the whole reason the key stays after nothing produces
// it.
func TestASettingsDocumentCarriesRowsWhoseOwnerIsGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openDB(t)
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `template_grant` (`template_id`,`capability`,`via`,`folder`) VALUES (?,?,?,?)",
		"tpl-gone", "memory", string(model.GrantByLabel), nil); err != nil {
		t.Fatalf("writing an orphaned grant: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `schedule_read` (`schedule_id`,`owner`,`name`) VALUES (?,?,?)",
		"sched-gone", "owner", "shared-lib"); err != nil {
		t.Fatalf("writing an orphaned read: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	for _, f := range []struct{ file, table string }{
		{"templates.yml", "template_grant"},
		{"schedules.yml", "schedule_read"},
	} {
		written, err := os.ReadFile(filepath.Join(dir, staterepo.SettingsDir, f.file))
		if err != nil {
			t.Fatalf("reading %s: %v", f.file, err)
		}
		if !strings.Contains(string(written), "orphaned-rows:") ||
			!strings.Contains(string(written), f.table+":") {
			t.Errorf("%s does not carry the orphaned %s row:\n%s", f.file, f.table, written)
		}
	}
	_, restored := openDB(t)
	if err := staterepo.Import(ctx, restored, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	sameDatabase(t, ctx, db, restored, "a database restored from a dump with orphaned settings rows")
}
