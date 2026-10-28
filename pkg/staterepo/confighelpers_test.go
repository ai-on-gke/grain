package staterepo_test

// What the tests in this package reach for now that the repository
// carries editable configuration and nothing else (dump.go's
// ExportConfig).
//
// Nearly every test here needs the same two moves. It needs something to
// happen that the repository has an opinion about -- which used to be
// "file a task" and is now "change a setting" -- and it needs to read
// what reached the working tree or the remote, which used to be
// tables/task.json and is now a settings document. Doing both in one
// place is worth more than the three lines it saves each time: the
// question "is this a thing the export carries" has exactly one answer,
// and a later change to where settings are written moves it here rather
// than through forty call sites.
//
// The third move is the one the narrowing bought, and it is the reason
// this file exists at all rather than a `putTemplate` at the top of each
// file: a test that arranges a merge, a divergence, a recovery or a
// restore is a test that can put an operational table back into the
// repository by accident, and noOperationalTables is what says so. It is
// cheap enough to call on every one of them.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// configTables is every table the repository carries and a restore
// brings back: the settings Apply may make live underneath a running
// daemon, plus the memory files, which take the same path (bind.go's
// SettingsTables and memoryTables).
//
// Not release_note, which the export writes and no import reads back --
// the markdown under release-notes/ is the artifact, and the row behind
// it is not claimed to round-trip.
func configTables() []string {
	return append(append([]string{}, staterepo.SettingsTables...), "memory", "memory_document")
}

// operationalTables is grain's own record of what it did: the tables
// that live in SQLite and must never appear in the repository again.
//
// A short list rather than "every table that is not on configTables",
// deliberately. What this is for is a failure message an operator or a
// reviewer can act on, and these are the names somebody would actually
// recognise in a diff -- a task, a run, a CI step, a lease. A table a
// later build adds and forgets is caught one layer down, by
// TestExportConfigLeavesOperationalRowsOnlyInSQLite and by ExportConfig
// itself, which writes an allowlist and removes whatever else it finds.
var operationalTables = []string{
	"task", "task_comment", "task_read", "task_grant", "task_run",
	"task_observation", "ci_run", "ci_job", "ci_step", "lease",
}

// putTemplate writes one template into the store: the settings change
// these tests use to mean "the deployment did something the repository
// has an opinion about".
//
// A template rather than a repo_config or a schedule because it is the
// cheapest of the three to read back -- one id and one title -- and
// because it is the object a pull request against a real state
// repository is most often about.
func putTemplate(t *testing.T, ctx context.Context, store *model.Store, id, title string) {
	t.Helper()
	if err := store.PutTemplate(ctx, model.Template{
		ID: id, Name: id, Title: title, Body: "as configured", CreatedAt: now,
	}); err != nil {
		t.Fatalf("writing template %s: %v", id, err)
	}
}

// templatesIn reads the titles the settings document in dir holds, by id.
func templatesIn(t *testing.T, dir string) map[string]string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("template")))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return templateTitles(t, path, string(data))
}

// templatesOn is the same question asked of a bare remote: what the
// branch's own copy of the settings document says, read out of the
// repository that was pushed to rather than out of the tree that pushed.
func templatesOn(t *testing.T, remote, branch string) map[string]string {
	t.Helper()
	path := branch + ":" + staterepo.DumpPath("template")
	return templateTitles(t, path, git(t, remote, "show", path))
}

func templateTitles(t *testing.T, where, data string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range parseTemplateRows(t, where, data) {
		id, _ := row["id"].(string)
		title, _ := row["title"].(string)
		out[id] = title
	}
	return out
}

// hasTemplate fails unless the settings document in dir gives id that
// title, and says what it holds instead when it does not. The assertion
// almost every restored test here ends on.
func hasTemplate(t *testing.T, dir, id, title string) {
	t.Helper()
	got := templatesIn(t, dir)
	if got[id] != title {
		t.Fatalf("%s holds %s as %q, want %q (it holds %v)",
			staterepo.DumpPath("template"), id, got[id], title, got)
	}
}

// hasTemplateOn is hasTemplate against the remote's own branch, which is
// the claim whenever what is being tested is that a push landed.
func hasTemplateOn(t *testing.T, remote, branch, id, title string) {
	t.Helper()
	got := templatesOn(t, remote, branch)
	if got[id] != title {
		t.Fatalf("%s on the remote holds %s as %q, want %q (it holds %v)",
			staterepo.DumpPath("template"), id, got[id], title, got)
	}
}

// noOperationalTables fails if the repository in dir carries any of
// grain's own record of what it did.
//
// Worth asserting in any test that imports, merges, recovers or restores
// even when that is not what the test is about: the ways an operational
// file gets back in are sequences rather than a line of code somebody
// would notice writing -- a path that imports an old dump, a recovery
// that resurrects a file a later export removed, a reformat that reaches
// for the whole-database Export.
func noOperationalTables(t *testing.T, dir string) {
	t.Helper()
	for _, table := range operationalTables {
		if _, err := os.Stat(filepath.Join(dir, staterepo.TablesDir, table+".json")); err == nil {
			t.Fatalf("%s/%s.json is in the repository; the export is configuration only",
				staterepo.TablesDir, table)
		}
	}
}

// noOperationalTablesOn is the same question asked of a bare remote,
// which is where it matters most: a file that only ever reached the
// working tree is one host's mistake, and one that was pushed is the
// deployment's history published somewhere a sandbox can clone it.
func noOperationalTablesOn(t *testing.T, remote, branch string) {
	t.Helper()
	for _, table := range operationalTables {
		path := branch + ":" + staterepo.TablesDir + "/" + table + ".json"
		if _, err := gitOutput(remote, "show", path); err == nil {
			t.Fatalf("%s is on the remote; the export is configuration only", path)
		}
	}
}

// mergeATemplateTitle is a pull request against the repository, merged:
// one template retitled, pushed to the remote for the deployment to pull
// down on its next cycle.
func mergeATemplateTitle(t *testing.T, remote, id, title string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "pull-request")
	git(t, "", "clone", "--quiet", remote, work)
	path := filepath.Join(work, filepath.FromSlash(staterepo.DumpPath("template")))
	rows := readTemplateRows(t, path)
	found := false
	for _, row := range rows {
		if row["id"] == id {
			row["title"] = title
			found = true
		}
	}
	if !found {
		t.Fatalf("the repository has no template %q to retitle:\n%s", id, read(t, path))
	}
	writeTemplateRows(t, path, rows)
	git(t, work, "add", "--all", ".")
	git(t, work, "-c", "user.email=someone@example.com", "-c", "user.name=someone",
		"commit", "-m", "A merged pull request: a template retitled")
	git(t, work, "push", "--quiet", "origin", "main")
}

// readTemplateRows and writeTemplateRows are a hand edit of the
// templates document: the entries as they are, changed however the
// caller likes and written back. That is what a pull request against
// this repository is now -- one nested document per kind rather than
// seven files of rows (decl.go).
//
// What is written back is the entries alone, so the header comments the
// export puts above them are lost the way they would be under any editor
// that round-trips YAML through a map. That is deliberate: grain has to
// re-export a merged document in its own shape, comments and key order
// included, and a merge that came back byte-identical would never
// exercise the commit that does it.
func readTemplateRows(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return parseTemplateRows(t, path, string(data))
}

func parseTemplateRows(t *testing.T, where, data string) []map[string]any {
	t.Helper()
	var doc struct {
		Templates []map[string]any `yaml:"templates"`
	}
	if err := yaml.Unmarshal([]byte(data), &doc); err != nil {
		t.Fatalf("parsing %s: %v", where, err)
	}
	return doc.Templates
}

func writeTemplateRows(t *testing.T, path string, rows []map[string]any) {
	t.Helper()
	if rows == nil {
		rows = []map[string]any{}
	}
	data, err := yaml.Marshal(map[string]any{"templates": rows})
	if err != nil {
		t.Fatalf("encoding %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// sameExport fails unless two directories hold the same exported
// configuration: the settings documents, the table files behind them,
// and the trees the external-text columns are written into (dump.go's
// externalTexts).
//
// Everything ExportConfig writes and nothing else, which is what makes
// it usable against a clone: a clone also holds a README, a schema
// stamp, a .gitignore and a CI workflow, none of which an export into a
// bare directory produces.
func sameExport(t *testing.T, a, b string) {
	t.Helper()
	names := func(dir string) map[string]bool {
		out := map[string]bool{}
		for _, root := range []string{staterepo.TablesDir, staterepo.SettingsDir,
			staterepo.ScriptsDir, staterepo.WorkflowsDir, staterepo.MemoryDir, staterepo.ReleaseNotesDir} {
			base := filepath.Join(dir, root)
			err := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(dir, path)
				if err != nil {
					return err
				}
				out[filepath.ToSlash(rel)] = true
				return nil
			})
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("reading %s: %v", base, err)
			}
		}
		return out
	}
	left, right := names(a), names(b)
	for name := range left {
		if !right[name] {
			t.Fatalf("%s is in one export and not the other", name)
		}
	}
	for name := range right {
		if !left[name] {
			t.Fatalf("%s is in one export and not the other", name)
		}
	}
	for name := range left {
		x := read(t, filepath.Join(a, filepath.FromSlash(name)))
		y := read(t, filepath.Join(b, filepath.FromSlash(name)))
		if x != y {
			t.Fatalf("%s differs between the two exports:\n%s\n---\n%s", name, x, y)
		}
	}
}

// differingTemplates describes the first way got differs from want, or
// "" when they are the same set of ids with the same titles.
func differingTemplates(got, want map[string]string) string {
	for _, id := range sortedTitleKeys(want) {
		title, ok := got[id]
		if !ok {
			return fmt.Sprintf("no template %s, which should read %q", id, want[id])
		}
		if title != want[id] {
			return fmt.Sprintf("template %s as %q, which should read %q", id, title, want[id])
		}
	}
	for _, id := range sortedTitleKeys(got) {
		if _, ok := want[id]; !ok {
			return fmt.Sprintf("template %s (%q), which should not be there", id, got[id])
		}
	}
	return ""
}

func sortedTitleKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
