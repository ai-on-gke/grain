package staterepo_test

// What the deployment says when a merged settings file resets a value it
// was running on (resets.go).
//
// The pull request already gets a warning from `grain state check`, and
// that warning is as far as a check can go: it imports into a throwaway
// database, so it can say which default a missing column would land on
// and not which value that would be instead of. These tests are the other
// half -- the deployment doing the reset, which knows both sides, saying
// what it actually took away and staying quiet where it took nothing.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// journal is what grain logged during this test, which is where this
// notice goes and therefore the only place to read it back.
//
// It reads the router's own journal (log_test.go) rather than pointing
// log.SetOutput at a buffer of its own: putting the log back to
// os.Stderr afterwards, as this used to, would switch the routing off
// for every test that ran after this one.
func journal(t *testing.T) fmt.Stringer {
	t.Helper()
	return staterepo.LogToTest(t)
}

// dropColumns rewrites one table file in a clone the way a branch taken
// before those columns existed would have it: the columns simply are not
// mentioned. edit gets the last word, so a test can leave a real change
// in the file as well -- a merge that resets something is almost never a
// merge that does nothing else.
func dropColumns(t *testing.T, path string, cols []string, edit func(row map[string]any)) {
	t.Helper()
	var rows []map[string]any
	dec := json.NewDecoder(strings.NewReader(read(t, path)))
	dec.UseNumber()
	if err := dec.Decode(&rows); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for _, row := range rows {
		for _, col := range cols {
			delete(row, col)
		}
		if edit != nil {
			edit(row)
		}
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The line this whole file is about: a merge that resets a setting says
// which setting, what it was, and what it is now.
//
// The value is the point. "time_zone would land on 'America/Los_Angeles'"
// is what the pull request was told and is all a check can know; what an
// operator needs, afterwards, when the UI's timestamps have moved and the
// schedules are firing at a different hour, is that this deployment was
// running 'Europe/London' and a merge took it. Nothing but the process
// doing the import is in a position to say so, and it can only say it in
// the moment before the row is replaced.
func TestApplyingSettingsSaysWhatItReset(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) {
		c.UpgradeCheckMode = model.UpgradeCheckOff
		c.PromptExtension = "high"
		c.EnvironmentName = ""
	})
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"),
		[]string{"upgrade_check_mode", "prompt_extension", "environment_name"},
		func(row map[string]any) { row["max_workers"] = 9 })
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Raise max_workers")
	git(t, work, "push", "--quiet", "origin", "main")

	logs := journal(t)
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	got := logs.String()
	// The column, the file to put it back in, the value that was lost and
	// the value that replaced it. All four, because any three of them
	// leave the reader with a question they cannot answer from the
	// journal.
	for _, want := range []string{
		"upgrade_check_mode",
		filepath.Join(staterepo.TablesDir, "grain_config.json"),
		"'off'",
		"'auto'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the journal does not mention %s after a merge that reset it:\n%s", want, got)
		}
	}
	// The second reset value is named too: a file branched before an
	// upgrade is missing every column the upgrade added, so one line has
	// to carry all of them.
	if !strings.Contains(got, "prompt_extension") || !strings.Contains(got, "'high'") {
		t.Errorf("the journal does not name prompt_extension and what it held:\n%s", got)
	}
	// And the one that cost nothing is not mentioned, which is the whole
	// difference between this notice and the check's warning. The check
	// has to warn about every omitted column because it cannot see the
	// running value; here the running value was already the default, so
	// the import changed nothing and there is nothing to report.
	if strings.Contains(got, "environment_name") {
		t.Errorf("the journal names environment_name, which was already at its default and lost "+
			"nothing:\n%s", got)
	}
	// The notice is about the row that was there, not about the merge as
	// a whole: the column the file does carry landed as the file says.
	after, err := store.GetConfig(ctx)
	if err != nil || after == nil {
		t.Fatalf("reading the config after applying: %v", err)
	}
	if after.MaxWorkers != 9 || after.UpgradeCheckMode != model.UpgradeCheckAuto {
		t.Fatalf("the merge did not land as expected: max_workers=%d upgrade_check_mode=%q",
			after.MaxWorkers, after.UpgradeCheckMode)
	}
}

// A merge that takes nothing away says nothing, on the tick that carries
// it and on every tick after it.
//
// Worth pinning separately because the failure mode is not a wrong line
// but a line at all: this runs on every tick that finds a commit, on
// every deployment, and a notice that fired for an ordinary settings
// change would be a paragraph of noise in the journal each time somebody
// edits a template.
func TestApplyingSettingsIsSilentWhenNothingIsReset(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.UpgradeCheckMode = model.UpgradeCheckOff })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// A whole settings file, one value changed: the ordinary merge.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"), nil,
		func(row map[string]any) { row["max_workers"] = 9 })
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Raise max_workers")
	git(t, work, "push", "--quiet", "origin", "main")

	logs := journal(t)
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	if got := logs.String(); strings.Contains(got, "did not mention") {
		t.Errorf("a merge that reset nothing wrote a reset notice:\n%s", got)
	}
	if after, err := store.GetConfig(ctx); err != nil || after == nil || after.UpgradeCheckMode != model.UpgradeCheckOff {
		t.Fatalf("the merge did not land as expected: %+v (err %v)", after, err)
	}
}

// The restore says nothing, and that is a decision rather than an
// oversight.
//
// The same ImportTables runs with the same file missing the same column,
// but there is no value on the other side of it: Load builds the database
// out of the dump on a host that has never held one, so a column the dump
// does not carry lands on its default because the default is the right
// answer. Calling that a reset would tell an operator restoring a backup
// that they had lost something, on the one path where nothing was there
// to lose.
func TestRestoringFromADumpSaysNothingAboutResets(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.UpgradeCheckMode = model.UpgradeCheckOff })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}
	// A dump from before the column existed, pushed to the remote.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"),
		[]string{"upgrade_check_mode"}, nil)
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "An older dump")
	git(t, work, "push", "--quiet", "origin", "main")

	// The restore: a clone onto a host that has never loaded this
	// repository, and an empty database beside it.
	_, fresh := openDB(t)
	otherDir := filepath.Join(t.TempDir(), "restored")
	other, err := staterepo.Open(ctx, staterepo.Config{Dir: otherDir, Remote: remote})
	if err != nil {
		t.Fatalf("opening the second working tree: %v", err)
	}
	logs := journal(t)
	if err := staterepo.Load(ctx, other, fresh, model.SchemaVersion); err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if got := logs.String(); strings.Contains(got, "did not mention") {
		t.Errorf("a restore reported a reset, where there was nothing to reset:\n%s", got)
	}
}

// The journal is not the only place this can go, so the values are
// handed out as well as logged: Config.AnnounceResets is what carries
// them to a surface that reaches somebody -- the pull request the merge
// came in on (pkg/cli/stateresets.go) -- and what it is handed has to
// be the whole fact and the commit it belongs to, because the surface
// has no way back to the database to ask.
//
// The two silences are pinned here too, and they are the same two the
// journal keeps: nothing for a restore, which had no value to lose, and
// nothing for a merge that took nothing away. A surface that posts to a
// pull request must not be woken for either -- one would be a comment on
// every ordinary settings change, the other a comment telling somebody
// restoring a backup that they had lost something.
func TestApplyingSettingsHandsTheResetsToTheDeploymentsOtherSurfaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	type announcement struct {
		commit string
		resets []staterepo.Reset
	}
	var announced []announcement
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote,
		AnnounceResets: func(_ context.Context, commit string, resets []staterepo.Reset) error {
			announced = append(announced, announcement{commit: commit, resets: resets})
			return nil
		}})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.UpgradeCheckMode = model.UpgradeCheckOff })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	// An ordinary settings change first: a whole file with one value
	// edited, which is what almost every merge is.
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"), nil,
		func(row map[string]any) { row["max_workers"] = 9 })
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Raise max_workers")
	git(t, work, "push", "--quiet", "origin", "main")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying an ordinary change: applied=%v err=%v", applied, err)
	}
	if len(announced) != 0 {
		t.Fatalf("a merge that reset nothing was announced: %+v", announced)
	}

	// And now the one that costs something.
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"),
		[]string{"upgrade_check_mode"}, nil)
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "A file branched before upgrade_check_mode")
	git(t, work, "push", "--quiet", "origin", "main")
	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying the reset: applied=%v err=%v", applied, err)
	}
	if len(announced) != 1 {
		t.Fatalf("a merge that reset a value was announced %d times, want once: %+v",
			len(announced), announced)
	}
	// The commit, because the surface's first job is to find where the
	// change was made, and a sha is the only thing that leads back to it.
	head, err := repo.Head(ctx)
	if err != nil {
		t.Fatalf("reading the head: %v", err)
	}
	if announced[0].commit != head {
		t.Errorf("announced commit %q, want the commit that was applied %q", announced[0].commit, head)
	}
	if len(announced[0].resets) != 1 {
		t.Fatalf("announced %d resets, want the one that was lost: %+v", len(announced[0].resets), announced[0].resets)
	}
	got := announced[0].resets[0]
	want := staterepo.Reset{
		Path:   filepath.Join(staterepo.TablesDir, "grain_config.json"),
		Column: "upgrade_check_mode",
		Row:    "id = 1",
		From:   "'off'",
		To:     "'auto'",
	}
	if got != want {
		t.Errorf("announced %+v, want %+v", got, want)
	}

	// The restore, on a host that has never held this repository: the
	// same file missing the same column, and nothing to announce.
	_, fresh := openDB(t)
	var restoreAnnounced []announcement
	other, err := staterepo.Open(ctx, staterepo.Config{Dir: filepath.Join(t.TempDir(), "restored"), Remote: remote,
		AnnounceResets: func(_ context.Context, commit string, resets []staterepo.Reset) error {
			restoreAnnounced = append(restoreAnnounced, announcement{commit: commit, resets: resets})
			return nil
		}})
	if err != nil {
		t.Fatalf("opening the second working tree: %v", err)
	}
	if err := staterepo.Load(ctx, other, fresh, model.SchemaVersion); err != nil {
		t.Fatalf("restoring: %v", err)
	}
	if len(restoreAnnounced) != 0 {
		t.Fatalf("a restore was announced as a reset: %+v", restoreAnnounced)
	}
}

// The notice never gets ahead of the import, which is what keeps it from
// announcing a loss that did not happen.
//
// resetsFromApplying is asked before ImportTables, and on its own it
// would happily report a column the file omits that the running database
// holds a value for -- including one declared NOT NULL with no default,
// where the "default" it would name is a NULL the schema refuses. What
// stops that reaching anybody is the order in applySettings: the report
// is written after the import, so an import the constraint refuses
// returns first and the journal says nothing. Worth pinning rather than
// reasoning about, because the two statements are three lines apart and
// swapping them would be an easy tidy-up to make.
//
// `grain state check` is where such a file is meant to be caught, one
// pull request earlier, and it now says which of the two things it is
// (TestCheckSaysAnUnomittableSettingsColumnIsRefusedRatherThanReset).
func TestApplyingSettingsThatCannotBeImportedSaysNothingAboutResets(t *testing.T) {
	ctx := context.Background()
	store, db := openDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	configWith(t, store, func(c *model.Config) { c.UpgradeCheckMode = model.UpgradeCheckOff })
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// upgrade_check_mode is the reset this deployment would be told about, and
	// poll_interval_ms is the column that stops the import ever getting
	// there.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	dropColumns(t, filepath.Join(work, staterepo.TablesDir, "grain_config.json"),
		[]string{"upgrade_check_mode", "poll_interval_ms"}, nil)
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Drop two columns")
	git(t, work, "push", "--quiet", "origin", "main")

	logs := journal(t)
	if _, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err == nil {
		t.Fatal("a settings file missing a NOT NULL column with no default applied cleanly")
	}
	if got := logs.String(); strings.Contains(got, "did not mention") {
		t.Errorf("an import that was refused still announced a reset:\n%s", got)
	}
	// And the value is still there, which is the fact the silence is
	// about: importInto is one transaction, so the refusal took nothing.
	if after, err := store.GetConfig(ctx); err != nil || after == nil || after.UpgradeCheckMode != model.UpgradeCheckOff {
		t.Fatalf("the refused import moved the running config: %+v (err %v)", after, err)
	}
}

// configWith reads this deployment's config row, changes it and writes it
// back, which is how a test gets a settings row that holds something
// worth losing.
func configWith(t *testing.T, store *model.Store, edit func(*model.Config)) model.Config {
	t.Helper()
	ctx := context.Background()
	cfg := model.Config{}
	if stored, err := store.GetConfig(ctx); err != nil {
		t.Fatalf("reading the config: %v", err)
	} else if stored != nil {
		cfg = *stored
	}
	edit(&cfg)
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	return cfg
}
