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

// Check against real dumps and a real database: what a state
// repository's CI step would run, given the files a pull request
// proposes.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// dumpOf writes a real export of a database holding one task, which is
// the shape every test here starts from and then breaks.
func dumpOf(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutTask(ctx, task("a1b2")); err != nil {
		t.Fatalf("putting a task: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	return dir
}

// dumpWithConfig is dumpOf for the tests about settings columns: the
// export of a database that has actually written its config row, since
// grain_config.json is an empty file until something has.
func dumpWithConfig(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	store, db := openDB(t)
	cfg := model.Config{}
	if stored, err := store.GetConfig(ctx); err != nil {
		t.Fatalf("reading the config: %v", err)
	} else if stored != nil {
		cfg = *stored
	}
	cfg.UpgradeCheckMode = model.UpgradeCheckOff
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatalf("stamping: %v", err)
	}
	return dir
}

// readTable/writeTable are how these tests play the part of somebody
// hand-editing a file in a pull request.
func readTable(t *testing.T, dir, table string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, staterepo.TablesDir, table+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func writeTable(t *testing.T, dir, table string, rows any) {
	t.Helper()
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, staterepo.TablesDir, table+".json"), append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SettingsTables and the other table-name lists are checked against the
// schema in tables_internal_test.go, which needs this package's
// unexported ones too.

func TestCheckAcceptsADumpGrainWrote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	_, db := openDB(t)

	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking a dump grain itself wrote: %v", err)
	}
	if report.SchemaVersion != model.SchemaVersion {
		t.Errorf("report says schema %d, want %d", report.SchemaVersion, model.SchemaVersion)
	}
	if report.Rows["task"] != 1 {
		t.Errorf("report counted %d tasks, want 1 (rows: %v)", report.Rows["task"], report.Rows)
	}
	if report.Total() < 1 {
		t.Errorf("report totals %d rows", report.Total())
	}
	if len(report.Warnings) != 0 {
		t.Errorf("grain's own dump warned: %v", report.Warnings)
	}
}

// The case this whole command exists for: a row that lost a column the
// schema insists on. Before `grain state check`, this failed on the next
// daemon start, after the merge.
func TestCheckRejectsARowMissingARequiredColumn(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	rows := readTable(t, dir, "task")
	delete(rows[0], "title")
	writeTable(t, dir, "task", rows)
	_, db := openDB(t)

	_, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err == nil {
		t.Fatal("a task row with no title passed the check")
	}
	if !strings.Contains(err.Error(), "task") {
		t.Errorf("error does not name the table: %v", err)
	}
}

// A hand-edited file that duplicates a row -- the shape of a copy-paste
// that forgot to change the id -- is refused by the primary key, which
// is exactly the sort of thing nobody sees by reading the diff.
func TestCheckRejectsTwoRowsWithTheSameKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	rows := readTable(t, dir, "task")
	writeTable(t, dir, "task", append(rows, rows[0]))
	_, db := openDB(t)

	if _, err := staterepo.Check(ctx, db, dir, model.SchemaVersion); err == nil {
		t.Fatal("two task rows with the same id passed the check")
	}
}

func TestCheckRejectsAMalformedFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	path := filepath.Join(dir, staterepo.TablesDir, "task.json")
	if err := os.WriteFile(path, []byte("[{\"id\": \"a1b2\",,}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, db := openDB(t)

	_, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err == nil {
		t.Fatal("a file that is not JSON passed the check")
	}
	if !strings.Contains(err.Error(), "task.json") {
		t.Errorf("error does not name the file that broke: %v", err)
	}
}

// Import deliberately skips a file or a column this build does not have
// -- that is what a dump from a newer build looks like -- so the check
// cannot fail on either. It says so instead, because it is also what a
// typo looks like.
func TestCheckWarnsAboutWhatImportWouldIgnore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	writeTable(t, dir, "task_templates", []map[string]any{{"id": "x"}})
	rows := readTable(t, dir, "task")
	rows[0]["titel"] = "a typo"
	writeTable(t, dir, "task", rows)
	_, db := openDB(t)

	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	joined := strings.Join(report.Warnings, "\n")
	if !strings.Contains(joined, "task_templates.json") {
		t.Errorf("no warning about a file naming no table: %v", report.Warnings)
	}
	if !strings.Contains(joined, "titel") {
		t.Errorf("no warning about a column no table has: %v", report.Warnings)
	}
}

// A dump from before schema 26 still carries grain_config's
// admin_accounts. Nothing reads an administrator list any more --
// whoever holds the access code is the owner -- so check says the column
// would be ignored rather than refusing the dump.
func TestCheckSaysARetiredAdminListWouldBeIgnored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpWithConfig(t)
	rows := readTable(t, dir, "grain_config")
	if len(rows) != 1 {
		t.Fatalf("grain_config rows = %d, want 1", len(rows))
	}
	rows[0]["admin_accounts"] = "alice,bob"
	writeTable(t, dir, "grain_config", rows)
	_, db := openDB(t)

	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), `"admin_accounts"`) {
		t.Errorf("no warning that admin_accounts would be ignored: %v", report.Warnings)
	}
}

// A dump from before schema 27 still carries repo_config's admins. A
// repository's settings ask only whether the caller is a person, so the
// list granted nothing; check says the column would be ignored rather
// than refusing the dump.
func TestCheckSaysARetiredRepoAdminListWouldBeIgnored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpWithConfig(t)
	writeTable(t, dir, "repo_config", []map[string]any{
		{"owner": "acme", "name": "widgets", "default_capabilities": "", "admins": "alice,bob"},
	})
	_, db := openDB(t)

	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if !strings.Contains(strings.Join(report.Warnings, "\n"), `"admins"`) {
		t.Errorf("no warning that repo_config's admins would be ignored: %v", report.Warnings)
	}
}

// The other direction, for the tables where Import's tolerance is not
// tolerance at all: a column a *settings* file does not mention.
//
// Everywhere else a file missing a column is silence on purpose -- the
// database supplies the default and nothing is lost -- and that is
// exactly what is not true here. Apply replaces whole rows in
// SettingsTables, so the column the merged file does not mention is
// reset on the next tick rather than left alone, and the reset is what
// nobody reviewing the diff can see: it is in the lines the diff does
// not have. time_zone is the one with teeth, since its default is a zone
// rather than the empty string, so the warning has to name the value
// that would land.
func TestCheckWarnsAboutASettingsColumnAMergeWouldReset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	cfg := model.Config{}
	if stored, err := store.GetConfig(ctx); err != nil {
		t.Fatalf("reading the config: %v", err)
	} else if stored != nil {
		cfg = *stored
	}
	cfg.UpgradeCheckMode = model.UpgradeCheckOff
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	// The pull request: a grain_config.json from before the column
	// existed, which is what a branch taken before the upgrade and merged
	// after it produces.
	rows := readTable(t, dir, "grain_config")
	delete(rows[0], "upgrade_check_mode")
	writeTable(t, dir, "grain_config", rows)

	_, checkDB := openDB(t)
	report, err := staterepo.Check(ctx, checkDB, dir, model.SchemaVersion)
	// A warning and a pass. Failing would red-tick a branch that loses
	// nothing on a deployment already running the default, and would
	// block `grain state check -migrate` on the very upgrade that adds
	// the column -- see settingsResets on why the reviewer decides.
	if err != nil {
		t.Fatalf("a settings file missing a column failed the check: %v", err)
	}
	var said string
	for _, w := range report.Warnings {
		if strings.Contains(w, "grain_config.json") && strings.Contains(w, `"upgrade_check_mode"`) {
			said = w
		}
	}
	if said == "" {
		t.Fatalf("nothing warns that a merge of this would reset upgrade_check_mode: %v", report.Warnings)
	}
	// The value, not just the name: "a column is missing" is the thing a
	// reviewer can already see, and "it goes back to auto" is not.
	if !strings.Contains(said, "auto") {
		t.Errorf("the warning does not say which value would land: %q", said)
	}
	// And the row that is still there is not warned about, so a settings
	// file grain itself wrote goes through silent.
	for _, w := range report.Warnings {
		if strings.Contains(w, `"max_workers"`) {
			t.Errorf("a column the file does carry was warned about: %q", w)
		}
	}
}

// The other half of that warning, which used to be the same sentence and
// is not the same fact: a settings column that cannot be left out at all.
//
// grain_config declares poll_interval_ms NOT NULL with no default, along
// with most of the columns the first schema wrote. A file that omits one
// of those is not reset on the next tick -- the insert has nothing to
// write and SQLite refuses the row, so the dump does not load and no
// deployment ever gets as far as the question. Telling a reviewer that
// the column "lands on its default NULL rather than on whatever the
// running database holds" was a promise the schema refuses one line
// further down the same output, and it sends them looking for a running
// value that has nothing to do with why their branch is red.
func TestCheckSaysAnUnomittableSettingsColumnIsRefusedRatherThanReset(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpWithConfig(t)
	rows := readTable(t, dir, "grain_config")
	// Both kinds in one file, because the news is that they read
	// differently: upgrade_check_mode has a default to land on and poll_interval_ms
	// has not.
	for _, col := range []string{"poll_interval_ms", "upgrade_check_mode"} {
		if _, ok := rows[0][col]; !ok {
			t.Fatalf("the exported grain_config row has no %s to take out of it", col)
		}
		delete(rows[0], col)
	}
	writeTable(t, dir, "grain_config", rows)
	_, db := openDB(t)

	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	// A failure and not a warning, unlike every other omission in a
	// settings file: this one is not a judgement for the reviewer, it is a
	// file that does not load.
	if err == nil {
		t.Fatal("a grain_config row with no poll_interval_ms passed the check")
	}
	var refused, reset string
	for _, w := range report.Warnings {
		switch {
		case strings.Contains(w, `"poll_interval_ms"`):
			refused = w
		case strings.Contains(w, `"upgrade_check_mode"`):
			reset = w
		}
	}
	if refused == "" {
		t.Fatalf("nothing says anything about the omitted poll_interval_ms: %v", report.Warnings)
	}
	// What it must say: the column cannot be imported. What it must not
	// say: that anything is reset, or that a default lands, or that the
	// running database's value is what would be replaced -- none of which
	// happens, because nothing is imported.
	if !strings.Contains(refused, "NOT NULL") || !strings.Contains(refused, "cannot be imported") {
		t.Errorf("the warning does not say the column cannot be left out: %q", refused)
	}
	for _, wrong := range []string{"lands on its default", "whatever the running database holds"} {
		if strings.Contains(refused, wrong) {
			t.Errorf("the warning still promises a reset that cannot happen (%q): %q", wrong, refused)
		}
	}
	// And the column that does have a default still reads the way it did:
	// this splits one warning in two rather than replacing it.
	if reset == "" || !strings.Contains(reset, "auto") {
		t.Errorf("the omitted upgrade_check_mode no longer gets its reset warning: %v", report.Warnings)
	}
}

// The exception the rule above has to carve out, or it would be the same
// false promise pointing the other way.
//
// grain_config.id is INTEGER NOT NULL with no default too, and SQLite
// fills it in regardless: a single-column INTEGER primary key is an alias
// for the rowid, and an omitted rowid is generated rather than refused. A
// check that called that unimportable would red-flag a file that imports
// perfectly well.
func TestCheckDoesNotCallAnAutoAssignedPrimaryKeyUnomittable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpWithConfig(t)
	rows := readTable(t, dir, "grain_config")
	if _, ok := rows[0]["id"]; !ok {
		t.Fatal("the exported grain_config row has no id to take out of it")
	}
	delete(rows[0], "id")
	writeTable(t, dir, "grain_config", rows)
	_, db := openDB(t)

	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("a grain_config row with no id failed the check, but SQLite assigns one: %v", err)
	}
	for _, w := range report.Warnings {
		if strings.Contains(w, `"id"`) && strings.Contains(w, "cannot be imported") {
			t.Errorf("the check calls the rowid alias unimportable, and the import above disagrees: %q", w)
		}
	}
}

// The same question one level up: not a settings file missing a column
// but a settings file that is not there at all.
//
// It is the worse of the two and it used to be the quieter one. Import
// treats a table with no file as empty on purpose, which is right for a
// dump written before the table existed; Apply is a replacement within
// the tables it is given, so the same absence empties the running
// deployment's table instead -- every board, or every declared custom
// field, deleted by a pull request whose diff does not mention them.
func TestCheckWarnsAboutASettingsTableWithNoFileAtAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	// custom_field is a settings table, and board is a settings table
	// whose file this deployment's own board would be in: the two shapes
	// of loss are the same, so one of each is enough.
	for _, table := range []string{"custom_field", "board"} {
		if err := os.Remove(filepath.Join(dir, staterepo.TablesDir, table+".json")); err != nil {
			t.Fatal(err)
		}
	}
	_, db := openDB(t)
	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	// A warning and a pass, for settingsResets' own reasons: this
	// database is not the deployment's, so nothing here knows whether
	// there was anything to lose.
	if err != nil {
		t.Fatalf("a dump with a settings file missing failed the check: %v", err)
	}
	for _, table := range []string{"custom_field", "board"} {
		var said bool
		for _, w := range report.Warnings {
			if strings.Contains(w, table+".json") && strings.Contains(w, "empties") {
				said = true
			}
		}
		if !said {
			t.Errorf("nothing warns that applying this would empty %s: %v", table, report.Warnings)
		}
	}
	// And the tables whose files are there say nothing, so a dump grain
	// wrote goes through silent -- which TestCheckAcceptsADumpGrainWrote
	// is the whole-dump version of.
	for _, w := range report.Warnings {
		if !strings.Contains(w, "custom_field.json") && !strings.Contains(w, "board.json") {
			t.Errorf("a dump missing two files warned about something else: %q", w)
		}
	}
}

// The narrowness of that warning, which is the whole reason it is
// tolerable: a table grain writes for itself says nothing at all when
// its file is missing, because importing it empty is what a dump from
// before the table meant and nothing is lost.
func TestCheckIsSilentAboutAMissingFileOutsideTheSettingsTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	// task_run is grain's own record of what it did: a file that is not
	// there imports as no runs, which is what a deployment that had none
	// looks like, and Apply never touches the table at all.
	if err := os.Remove(filepath.Join(dir, staterepo.TablesDir, "task_run.json")); err != nil {
		t.Fatal(err)
	}
	_, db := openDB(t)
	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("a dump with no task_run.json warned about something: %v", report.Warnings)
	}
}

// The narrowness of the reset warning, which is the whole reason it is
// tolerable: the same omission in a table Apply does not touch says
// nothing at all.
//
// task is grain's own record rather than settings, so a file missing a
// column there is imported once at startup into a database that supplies
// the default, and warning about it would put a warning on every
// deployment's first pull request after an upgrade -- for a load where
// nothing is lost.
func TestCheckIsSilentAboutAMissingColumnOutsideTheSettingsTables(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	rows := readTable(t, dir, "task")
	if _, ok := rows[0]["deferred_at"]; !ok {
		t.Fatal("the exported task row has no deferred_at to take out of it")
	}
	delete(rows[0], "deferred_at")
	writeTable(t, dir, "task", rows)
	_, db := openDB(t)

	report, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err != nil {
		t.Fatalf("checking: %v", err)
	}
	if len(report.Warnings) != 0 {
		t.Errorf("a task row missing a column warned about something: %v", report.Warnings)
	}
}

// The one place a dump carries a value as something other than itself:
// a one-key {"base64": ...} object, which is how a column holding bytes
// that are not text survives the trip (encodeValue). Anything beside
// that key is a hand edit, and it used to be taken silently -- the
// value went in as whatever "base64" said and the rest of the object
// was never looked at, which is a diff a reviewer reads one way and a
// deployment reads another.
func TestCheckRejectsABase64ObjectWithAnythingElseInIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	rows := readTable(t, dir, "task")
	rows[0]["body"] = map[string]any{
		"base64": base64.StdEncoding.EncodeToString([]byte("a body")),
		"note":   "and this, which nothing reads",
	}
	writeTable(t, dir, "task", rows)
	_, db := openDB(t)

	_, err := staterepo.Check(ctx, db, dir, model.SchemaVersion)
	if err == nil {
		t.Fatal("a base64 object carrying a second key passed the check")
	}
	if !strings.Contains(err.Error(), "body") {
		t.Errorf("error does not name the column that broke: %v", err)
	}
}

func TestCheckRefusesADumpFromAnotherSchema(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := dumpOf(t)
	if err := staterepo.WriteSchemaVersion(dir, model.SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	_, db := openDB(t)

	if _, err := staterepo.Check(ctx, db, dir, model.SchemaVersion); !errors.Is(err, staterepo.ErrSchemaTooNew) {
		t.Fatalf("check of a newer dump = %v, want ErrSchemaTooNew", err)
	}
}

func TestCheckRefusesADirectoryThatIsNotAStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openDB(t)
	if _, err := staterepo.Check(ctx, db, t.TempDir(), model.SchemaVersion); err == nil {
		t.Fatal("an empty directory passed the check")
	}
}
