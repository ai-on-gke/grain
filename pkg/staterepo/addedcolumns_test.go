package staterepo_test

// Two builds that stamp their dump with the same schema number and
// disagree about which columns a row has.
//
// That is not a corner case, it is what an ordinary week looks like. A
// column added with a default is absorbed by an existing database in
// place (Store.Init's ensure*Column rungs), so model.SchemaVersion does
// not move -- rightly, since the number exists to say "these rows are
// shaped for a schema this binary does not have" and they are not. What
// does move is the shape of the dump: the build with the column writes
// it into every row, the build without it does not, and both files are
// stamped the same.
//
// The dump machinery is built to absorb exactly that. Import skips a
// column this build does not have and treats a table it has no file for
// as empty (dump.go); Check reports both as warnings rather than
// failures (check.go), because both are also what a typo in a
// hand-edited file looks like and a reviewer is owed the news. This file
// pins that for the columns added on 2026-09-05, in both directions:
// a dump written before them, read by a build that has them, and a dump
// written by a build that has them, read by one that does not -- which
// is what the state repository's own CI step does on every pull request,
// against whatever image DefaultCheckImage pins.
//
// The pkg/model/sqlite side of the same question is well covered
// already, one TestInitMigratesAnExistingDatabaseMissing* per column.
// This is the other half: the same columns, going through the files.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/staterepo"
)

// addedColumns are the columns added in place -- with a default, and
// with no SchemaVersion bump -- by the table they were added to.
//
// task.deferred_at is the Defer feature, and the only one here that
// feeds a view. schedule.overlap is what a schedule does when it comes
// due with its own last firing still running (model.OverlapPolicy), and
// the only one whose default is not simply "empty": a row written before
// it existed reads back as 'skip', which is what every schedule did when
// there was no column to say otherwise. The rest are the deployment's
// own settings row.
//
// Named here rather than inlined so that the two directions below are
// demonstrably about the same set, and so that adding the next column of
// this shape is one line and a re-read of what these tests assert.
//
// One table cannot be listed here, and it is worth knowing which before
// assuming this covers the column you just added: task_observation.
// preUpgradeDump ages the golden fixture by deleting these columns out
// of its rows, so a table whose fixture file is an empty list has no
// pre-upgrade shape to make -- and writeGoldenDump seeds no observation
// at all, so `tables/task_observation.json` is `[]`. Its columns are
// covered by TestADumpWithoutTheDiffSizeColumnsImportsAsUnmeasured at
// the foot of this file instead, which builds its own dump from a store
// and ages that. Give writeGoldenDump an observation row and this list
// can have the table back.
var addedColumns = map[string][]string{
	// work_ownership and work_overrides (grain/task-486) are not listed
	// here and do not need to be: the golden dump predates them, so the
	// fixture is already a dump without them and every test in this file
	// reads one. A column only belongs here once the fixture is
	// regenerated at a build that writes it.
	"repo_config": {"ci_capabilities", "clone_url"},
	"task":        {"deferred_at"},
	"schedule":    {"overlap"},
	"grain_config": {
		"upgrade_check_mode",
		"upgrade_check_pauses",
	},
}

// task.deferred_at is the only one of the five that is task data rather
// than settings, and the only one a view reads, so it is the only one
// where the round trip through the files has something to lose: a
// deployment restored from its state repository must not silently pick
// up a task somebody deliberately put aside.
func TestADeferredTaskComesBackDeferredFromTheDump(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	for _, id := range []string{"deferred-1", "queued-1"} {
		if err := store.PutTask(ctx, task(id)); err != nil {
			t.Fatalf("putting %s: %v", id, err)
		}
		if err := store.Approve(ctx, id, model.Attribution{
			Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"},
		}, now); err != nil {
			t.Fatalf("approving %s: %v", id, err)
		}
	}
	deferredAt := now.Add(2 * time.Hour)
	if err := store.SetDeferred(ctx, "deferred-1", &deferredAt, nil, deferredAt); err != nil {
		t.Fatalf("deferring: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// It travels as a timestamp rather than as a flag, so the far side
	// can say when it was put aside and not merely that it was.
	if body := readGolden(t, filepath.Join(dir, staterepo.TablesDir, "task.json")); !strings.Contains(
		body, deferredAt.Format(time.RFC3339)) {
		t.Errorf("the exported task does not carry the time it was deferred at:\n%s", body)
	}

	other, otherDB := openRealDB(t)
	if err := staterepo.Import(ctx, otherDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := other.GetTask(ctx, "deferred-1")
	if err != nil || got == nil {
		t.Fatalf("the deferred task did not arrive: %v", err)
	}
	if got.DeferredAt == nil || !got.DeferredAt.Equal(deferredAt) {
		t.Fatalf("the task came back deferred at %v, want %v", got.DeferredAt, deferredAt)
	}
	// The view on the far side, which is what dispatch reads: a restore
	// that carried the column but landed the task back in the queue would
	// be the same failure with an extra step.
	if state, err := other.State(ctx, "deferred-1"); err != nil || state != model.StateDeferred {
		t.Fatalf("the restored task reads as %q (err %v), want %q", state, err, model.StateDeferred)
	}
	ready, err := other.Ready(ctx)
	if err != nil {
		t.Fatalf("reading what is dispatchable after the restore: %v", err)
	}
	for _, id := range ready {
		if id == "deferred-1" {
			t.Errorf("a restore from the state repository picked a deferred task back up: %v", ready)
		}
	}
	// The task beside it, deferred by nobody, is still dispatchable --
	// otherwise this test would pass just as well against a restore that
	// dropped every task on the floor.
	if state, err := other.State(ctx, "queued-1"); err != nil || state != model.StateQueued {
		t.Fatalf("the undeferred task reads as %q (err %v), want %q", state, err, model.StateQueued)
	}
	var found bool
	for _, id := range ready {
		if id == "queued-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("the undeferred task is not dispatchable after the restore: %v", ready)
	}
}

// What a settings file that does not mention a column does to a running
// deployment, pinned because the answer is a loss and the loss is
// intended: SettingsTables is a replacement, row and all, so a column
// the file leaves out lands on its DDL default rather than on what the
// database was holding. SettingsTables' own doc comment says so in as
// many words; this is that sentence as a test.
//
// The window is narrow and real: a pull request against
// grain_config.json branched before a column was added and merged after
// it, or a settings file edited by hand that simply does not mention the
// new column. What it costs is not nothing -- time_zone reverting moves
// every schedule's firing time, agent_git_name reverting changes who
// sandboxes commit as -- which is exactly why it is written down here
// rather than left for the next person to find on a deployment.
func TestApplyingASettingsFileWithoutAColumnResetsItToItsDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	remote := bareRemote(t)
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: dir, Remote: remote})
	if err != nil {
		t.Fatalf("opening: %v", err)
	}
	cfg := model.Config{}
	if stored, err := store.GetConfig(ctx); err != nil {
		t.Fatalf("reading the config: %v", err)
	} else if stored != nil {
		cfg = *stored
	}
	cfg.UpgradeCheckMode = model.UpgradeCheckOff
	cfg.MaxWorkers = 7
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("writing the config: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("loading: %v", err)
	}

	// The merge: a settings file from before those columns existed, with
	// one edit to a column it does have.
	work := filepath.Join(t.TempDir(), "clone")
	git(t, "", "clone", "--quiet", remote, work)
	path := filepath.Join(work, staterepo.TablesDir, "grain_config.json")
	var rows []map[string]any
	dec := json.NewDecoder(strings.NewReader(read(t, path)))
	dec.UseNumber()
	if err := dec.Decode(&rows); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	for _, row := range rows {
		for _, col := range addedColumns["grain_config"] {
			delete(row, col)
		}
		row["max_workers"] = 9
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, work, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-am", "Raise max_workers")
	git(t, work, "push", "--quiet", "origin", "main")

	if applied, err := staterepo.Apply(ctx, repo, db, model.SchemaVersion); err != nil || !applied {
		t.Fatalf("applying: applied=%v err=%v", applied, err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("reading the config after applying: %v", err)
	}
	// The column the file carries is what the file says.
	if got.MaxWorkers != 9 {
		t.Errorf("MaxWorkers after applying = %d, want the 9 the merged file holds", got.MaxWorkers)
	}
	// The columns it does not carry are back at their defaults.
	if got.UpgradeCheckMode != model.UpgradeCheckAuto {
		t.Errorf("UpgradeCheckMode after applying = %q, want the DDL default %q -- see SettingsTables' doc comment",
			got.UpgradeCheckMode, model.UpgradeCheckAuto)
	}

}

// task_observation's three diff-size columns (grain/task-381) going
// through the files, in the direction that matters: a dump written
// before they existed, read by this build.
//
// Not in addedColumns above, and it cannot be. That list can only name
// columns the golden fixture's rows *carry*, since taking one back out
// is what makes a pre-upgrade shape, and the fixture's
// task_observation.json is an empty list -- writeGoldenDump seeds no
// observation at all. So this builds its own dump from a store instead,
// which is the same exercise one table wide.
//
// What it has to pin is the distinction the whole feature rests on: a
// row that arrived without these columns holds NULL, not zero. A merge
// nobody measured must not read back as a pull request that changed
// nothing, because a week's lines-changed total is summed over exactly
// the rows that are not NULL (model.ClusterStats.PullRequestsMeasured).
func TestADumpWithoutTheDiffSizeColumnsImportsAsUnmeasured(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	merged := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, task("sized-1")); err != nil {
		t.Fatalf("filing: %v", err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: "sized-1", ClosedAt: &merged, CompletedAt: &merged, PrMergedAt: &merged,
		PrAdditions: ptr(214), PrDeletions: ptr(37), PrChangedFiles: ptr(9),
	}); err != nil {
		t.Fatalf("observing: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "dump")
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}

	// The build with the columns writes them into every row, which is
	// the half of this that is about the export at all.
	path := filepath.Join(dir, staterepo.TablesDir, "task_observation.json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(readGolden(t, path)), &rows); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s holds %d rows, want the one observed above", path, len(rows))
	}
	for _, col := range []string{"pr_additions", "pr_deletions", "pr_changed_files"} {
		if _, ok := rows[0][col]; !ok {
			t.Fatalf("the dump this build wrote carries no %q, so there is no pre-upgrade shape to make here", col)
		}
		delete(rows[0], col)
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	// And the build without them hands the same rows back, minus the
	// sizes -- imported into a store of this build's own schema, since
	// that is the upgrade: yesterday's files, today's columns.
	fresh, freshDB := openRealDB(t)
	if err := staterepo.Import(ctx, freshDB, dir); err != nil {
		t.Fatalf("importing a dump written before the diff-size columns existed: %v", err)
	}
	got, err := fresh.GetObservation(ctx, "sized-1")
	if err != nil || got == nil {
		t.Fatalf("the observation in the aged dump did not arrive: (%+v, %v)", got, err)
	}
	if got.PrMergedAt == nil || !got.PrMergedAt.Equal(merged) {
		t.Errorf("PrMergedAt after importing = %v, want the %v the file holds", got.PrMergedAt, merged)
	}
	if got.PrAdditions != nil || got.PrDeletions != nil || got.PrChangedFiles != nil {
		t.Errorf("a row with no size columns in it arrived as +%v -%v over %v files, want nothing "+
			"recorded: a merge grain never measured is not one that changed nothing",
			got.PrAdditions, got.PrDeletions, got.PrChangedFiles)
	}
}

// task.undeferred_at (grain/task-442) going through the files, in both
// directions.
//
// Not in addedColumns above, for the reason the diff-size columns are
// not: that list can only name columns the golden fixture's rows carry,
// and the fixture was exported by a build long before this one. So this
// builds its own dump and ages that, one column wide.
//
// Both halves matter, and they are not the same assertion. A deployment
// restored from its state repository must remember that a task it holds
// came back from a deferral last week, or the restore re-runs the
// failure the column was added to stop: the task reads as queued since
// whenever it was approved, and is flagged for standing still. And a
// dump written before the column existed must arrive as a task nobody
// ever picked back up, since that is all such a file can honestly claim
// -- an undeferral made then left nothing behind.
func TestTheUndeferralStampSurvivesTheDumpAndIsAbsentFromAnOlderOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openRealDB(t)
	if err := store.PutTask(ctx, task("parked-1")); err != nil {
		t.Fatalf("filing: %v", err)
	}
	if err := store.Approve(ctx, "parked-1", model.Attribution{
		Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bwsalmon"},
	}, now); err != nil {
		t.Fatalf("approving: %v", err)
	}
	deferredAt := now.Add(time.Hour)
	pickedUp := now.Add(90 * 24 * time.Hour)
	if err := store.SetDeferred(ctx, "parked-1", &deferredAt, nil, deferredAt); err != nil {
		t.Fatalf("deferring: %v", err)
	}
	if err := store.SetDeferred(ctx, "parked-1", nil, nil, pickedUp); err != nil {
		t.Fatalf("picking it back up: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "dump")
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	path := filepath.Join(dir, staterepo.TablesDir, "task.json")
	if body := readGolden(t, path); !strings.Contains(body, pickedUp.Format(time.RFC3339)) {
		t.Errorf("the exported task does not carry the moment it was picked back up:\n%s", body)
	}

	restored, restoredDB := openRealDB(t)
	if err := staterepo.Import(ctx, restoredDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := restored.GetTask(ctx, "parked-1")
	if err != nil || got == nil {
		t.Fatalf("the task did not arrive: (%+v, %v)", got, err)
	}
	if got.UndeferredAt == nil || !got.UndeferredAt.Equal(pickedUp) {
		t.Fatalf("the restored task was last picked back up at %v, want %v", got.UndeferredAt, pickedUp)
	}
	// Which is the whole point of carrying it: the restored deployment
	// measures the wait from the return, not from an approval three
	// months older than it.
	times, err := restored.StateTimes(ctx)
	if err != nil {
		t.Fatalf("gathering state times after the restore: %v", err)
	}
	if since := times["parked-1"].Since(model.StateQueued); since == nil || !since.Equal(pickedUp) {
		t.Errorf("the restored task is queued since %v, want the undeferral %v", since, pickedUp)
	}

	// And the same files as the build before the column wrote them.
	var rows []map[string]any
	if err := json.Unmarshal([]byte(readGolden(t, path)), &rows); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if len(rows) != 1 {
		t.Fatalf("%s holds %d rows, want the one filed above", path, len(rows))
	}
	if _, ok := rows[0]["undeferred_at"]; !ok {
		t.Fatalf("the dump this build wrote carries no undeferred_at, so there is no pre-upgrade "+
			"shape to make here:\n%s", readGolden(t, path))
	}
	delete(rows[0], "undeferred_at")
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	aged, agedDB := openRealDB(t)
	if err := staterepo.Import(ctx, agedDB, dir); err != nil {
		t.Fatalf("importing a dump written before the undeferral was stamped: %v", err)
	}
	got, err = aged.GetTask(ctx, "parked-1")
	if err != nil || got == nil {
		t.Fatalf("the task in the aged dump did not arrive: (%+v, %v)", got, err)
	}
	if got.UndeferredAt != nil {
		t.Errorf("a task row with no undeferred_at in it arrived picked back up at %v, want nothing "+
			"recorded", got.UndeferredAt)
	}
	// The row the file did carry is still what the file said, which is
	// what makes the above an absence rather than a dropped task.
	if got.DeferredAt != nil {
		t.Errorf("the aged task arrived deferred at %v, want not deferred", got.DeferredAt)
	}
	if state, err := aged.State(ctx, "parked-1"); err != nil || state != model.StateQueued {
		t.Errorf("the aged task reads as %q (err %v), want %q", state, err, model.StateQueued)
	}
}

func ptr[T any](v T) *T { return &v }
