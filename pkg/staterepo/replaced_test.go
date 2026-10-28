package staterepo_test

// What the deployment says about the rows a settings import replaced
// (replaced.go).
//
// The neighbouring file, resets_test.go, is about what a merged file
// leaves out. These are about what it carries: the dump is whatever the
// last export wrote, so every row of it replaces whatever this database
// has done to that row since -- and until now the only visible trace of
// that on a tick was the setting somebody changed on purpose. The case
// that was driven end to end is in docs/hand-runs/2026-09-07-repo-case.md
// (finding 2): a reconciliation of every divergent repo-name spelling in
// the database, half undone by the next settings import, silently.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// replacedNotice is the phrase the line about replaced rows is found by,
// and is written by no other notice this package logs -- the reset notice
// next door is about a settings file that "did not mention" something,
// and a test that looked for a word as ordinary as "replaced" would find
// that one's prose as well.
const replacedNotice = "did not match this database on"

// The line this file is about: an import that puts a settings row back
// says which row, what this database held and what arrived instead.
//
// All three shapes are here at once, because one import is where an
// operator meets all three: a row both sides hold whose value moved
// under the merge, a row the database had that the file does not (which
// the import deletes), and a row the file has that the database had
// dropped (which the import brings back). The last is the one that makes
// this more than a partial revert on a real deployment -- a template
// coming back is a schedule firing it again, under settings somebody
// thought they had changed.
func TestApplyingSettingsSaysWhichRowsItReplaced(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.EnvironmentName = "Staging" })
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		DefaultCapabilities: []string{"grain-ci"},
	}); err != nil {
		t.Fatalf("putting repo config: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Nightly", Body: "Run it.",
		CreatedAt: time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	// The export every settings file is written from, and the commit this
	// host has loaded.
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// Somebody merges an ordinary settings change, written from that
	// export and touching one value.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"), nil,
		func(row map[string]any) { row["max_workers"] = 9 })
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Raise max_workers")
	git(t, work, "push", "--quiet", "origin", "main")

	// Meanwhile this deployment moves on, the way `grain repo-case
	// -apply` moves one on: a repo_config merged into a different value,
	// a template that was there deleted, and a new one written.
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		DefaultCapabilities: []string{"grain-ci", "kontur-ssh"},
	}); err != nil {
		t.Fatalf("putting repo config: %v", err)
	}
	if err := store.DeleteTemplate(ctx, "tpl-1"); err != nil {
		t.Fatalf("deleting a template: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-2", Name: "reconciled", Title: "Reconciled", Body: "Run it.",
		CreatedAt: time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}

	logs := journal(t)
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	got := logs.String()
	if !strings.Contains(got, replacedNotice) {
		t.Fatalf("an import that replaced a row said nothing about it:\n%s", got)
	}
	// The change the merge was actually about is named: a stored value
	// replaced by the file's.
	if !strings.Contains(got, "max_workers") {
		t.Errorf("the journal does not name the value the merge itself changed:\n%s", got)
	}
	// The rows this deployment moved since its last export, which the
	// merge did not touch, are kept rather than rewound to that export --
	// and said to be, by name (merge.go). grain-eu-host lost seventy
	// rows to the replacement this used to be.
	for _, want := range []string{
		"never been exported",
		filepath.Join(staterepo.TablesDir, "repo_config.json"),
		"'tpl-1'",
		"'tpl-2'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the journal does not mention %s among the rows the merge kept:\n%s", want, got)
		}
	}
	if strings.Contains(got, "is gone") || strings.Contains(got, "is here now") {
		t.Errorf("the journal reports a kept row as replaced:\n%s", got)
	}
	if cfg, err := store.GetConfig(ctx); err != nil || cfg.MaxWorkers != 9 {
		t.Fatalf("the merged change did not land: max_workers %v (err %v)", cfg.MaxWorkers, err)
	}
	if cfgs, err := store.ListRepoConfigs(ctx); err != nil {
		t.Fatalf("reading repo configs: %v", err)
	} else if len(cfgs) != 1 || strings.Join(cfgs[0].DefaultCapabilities, ",") != "grain-ci,kontur-ssh" {
		t.Fatalf("the deployment's own repo config change was rewound: %+v", cfgs)
	}
	if tpl, err := store.GetTemplate(ctx, "tpl-1"); err == nil && tpl != nil {
		t.Fatalf("the template this deployment deleted came back: %+v", tpl)
	}
	if tpl, err := store.GetTemplate(ctx, "tpl-2"); err != nil || tpl == nil {
		t.Fatalf("the template this deployment added was dropped: %+v (err %v)", tpl, err)
	}
}

// appendTo adds to a file in a clone, which is how a test moves the
// repository without touching a settings file.
func appendTo(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// An import that replaces nothing says nothing, on a database holding a
// row of every settings table there is.
//
// The failure mode this pins is not a wrong line but a line at all.
// Nearly every settings import arrives at a deployment whose settings
// tables are exactly what the dump says, and a notice that fired for one
// of those would be a paragraph in the journal on every merge -- and,
// worse, would train whoever reads the journal to skip the paragraph
// that matters. The comparison has to survive every kind of value grain
// stores in a settings table for that to hold: a timestamp comes back
// from the driver as a time.Time and out of the file as a string, an
// integer as an int64 and as a JSON number, and a comparison that read
// either pair as a difference would report the whole database as
// replaced every time.
func TestApplyingSettingsIsSilentWhenItReplacesNothing(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	settled := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	configWith(t, store, func(c *model.Config) { c.EnvironmentName = "Staging" })
	if err := store.PutCustomField(ctx, model.CustomField{
		Name: "customer", Label: "Customer", Type: model.CustomFieldEnum,
		Options: []string{"acme"}, Default: "acme", Position: 3,
	}); err != nil {
		t.Fatalf("declaring a custom field: %v", err)
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo:                model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		DefaultCapabilities: []string{"grain-ci"},
		PromptExtension:     "Read docs/design.md first.",
	}); err != nil {
		t.Fatalf("putting repo config: %v", err)
	}
	if err := store.PutTemplate(ctx, model.Template{
		ID: "tpl-1", Name: "nightly", Title: "Nightly", Body: "Run it.",
		Reads:        []model.RepoRef{{Owner: "bwsalmon", Name: "grain"}},
		Writes:       []model.WriteTarget{{Repo: model.RepoRef{Owner: "bwsalmon", Name: "grain"}, Base: "main"}},
		Grants:       []model.Grant{{Capability: "githubtoken", Via: model.GrantByLabel}},
		CustomFields: map[string][]string{"customer": {"acme"}},
		CreatedAt:    settled,
	}); err != nil {
		t.Fatalf("putting a template: %v", err)
	}
	last := settled.Add(-time.Hour)
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sch-1", Title: "nightly", Target: model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 6},
		Overlap:    model.OverlapAllow, Enabled: true,
		NextRunAt: settled, LastRunAt: &last, CreatedAt: settled,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}
	boardID, err := store.NewBoardID(ctx)
	if err != nil {
		t.Fatalf("allocating a board id: %v", err)
	}
	if err := store.PutBoard(ctx, model.Board{
		ID: boardID, Name: "Monday", Kind: model.BoardKanban, OrderKey: 1.5,
		Columns: []model.BoardColumn{{Name: "Doing", States: []model.State{model.StateRunning}}},
	}); err != nil {
		t.Fatalf("putting a board: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// A commit that moves the repository without touching a settings
	// file: the import runs, replaces every settings row with an
	// identical one, and has nothing to say about any of it.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	appendTo(t, filepath.Join(work, staterepo.ReadmeFile), "\nA note for whoever opens this next.\n")
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "A note in the README")
	git(t, work, "push", "--quiet", "origin", "main")

	logs := journal(t)
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	if got := logs.String(); strings.Contains(got, replacedNotice) {
		t.Errorf("an import that replaced nothing said it had replaced something:\n%s", got)
	}
}

// The restore says nothing, for the reason it says nothing about resets:
// there is no value on the other side of the import.
//
// Load builds the whole database out of the dump on a host that has
// never held one, so every row it writes is the first version of that
// row this deployment has ever had. Telling an operator restoring a
// backup that the restore replaced their settings would be false on the
// one path where nothing was there to replace.
func TestRestoringFromADumpSaysNothingAboutReplacedRows(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.EnvironmentName = "Staging" })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	_, fresh := openDB(t)
	other, err := staterepo.Open(ctx, staterepo.Config{
		Dir: filepath.Join(t.TempDir(), "restored"), Remote: remote})
	if err != nil {
		t.Fatalf("opening the second working tree: %v", err)
	}
	logs := journal(t)
	if err := staterepo.Load(ctx, other, fresh, model.SchemaVersion); err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if got := logs.String(); strings.Contains(got, replacedNotice) {
		t.Errorf("a restore reported replacing rows, where there was nothing to replace:\n%s", got)
	}
}

// The notice never gets ahead of the import, which is the same ordering
// the reset notice keeps and is worth pinning separately, because these
// are two statements three lines apart in applySettings and swapping
// either with the import would be an easy tidy-up to make.
//
// The values are read before ImportTables, because afterwards they are
// gone; they are reported after it, because importInto is one
// transaction and a file the schema refuses changes nothing at all. A
// journal that claimed an import had replaced a row it had not would
// send an operator looking for a change nobody made.
func TestApplyingSettingsThatCannotBeImportedSaysNothingAboutReplacedRows(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.EnvironmentName = "Staging" })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// max_workers is the value the notice would name, and
	// poll_interval_ms -- NOT NULL with no default -- is what stops the
	// import ever getting there.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"),
		[]string{"poll_interval_ms"},
		func(row map[string]any) { row["max_workers"] = 9 })
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Drop a column grain cannot")
	git(t, work, "push", "--quiet", "origin", "main")

	logs := journal(t)
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err == nil {
		t.Fatal("a settings file missing a NOT NULL column with no default applied cleanly")
	}
	if got := logs.String(); strings.Contains(got, replacedNotice) {
		t.Errorf("an import that was refused still said it had replaced a row:\n%s", got)
	}
	if after, err := store.GetConfig(ctx); err != nil || after == nil || after.EnvironmentName != "Staging" {
		t.Fatalf("the refused import moved the running config: %+v (err %v)", after, err)
	}
}
