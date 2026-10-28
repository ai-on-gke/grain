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
		Grants: []model.Grant{{Capability: "memory", Via: model.GrantByLabel}},
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
			entry["grants"] = []any{"memory", "self-debug"}
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
	if len(tmpl.Grants) != 2 || tmpl.Grants[0].Capability != "memory" || tmpl.Grants[1].Capability != "self-debug" {
		t.Errorf("the grants did not land: %+v", tmpl.Grants)
	}
	if len(tmpl.Fields) != 1 || tmpl.Fields[0].Name != "area" || tmpl.Fields[0].Default != "ui" {
		t.Errorf("the inputs did not land: %+v", tmpl.Fields)
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
