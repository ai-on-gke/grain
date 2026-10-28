package staterepo_test

// What this deployment's own history says about when a parameter changed
// (grain/task-495).
//
// The evening that asked for it: `go` jobs running 31-34 minutes where the
// same job had taken 15-26 minutes earlier, a host at 99% CPU, and an
// operator dropping max_ci_jobs from 10 to 7 on that evidence. Everything
// about the jobs was recorded; the change to max_ci_jobs was recorded
// nowhere an agent could read, so "did that help" had no left-hand side.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/staterepo"
)

func TestSettingsChangesSaysWhichParameterMovedAndWhen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, dir := historyRepo(t)

	// The settings as they stood, and beside them grain's own record of
	// what it did -- which moves constantly and is not a settings change.
	writeState(t, dir, "tables/grain_config.json", configDump(10, 7))
	writeState(t, dir, "tables/task.json", "[\n  {\n    \"id\": \"a1b2\"\n  }\n]\n")
	commit(t, ctx, repo, "the deployment as it was")

	// An operator drops max_ci_jobs from 10 to 7, in the same export that
	// carried a task moving.
	writeState(t, dir, "tables/grain_config.json", configDump(7, 7))
	writeState(t, dir, "tables/task.json", "[\n  {\n    \"id\": \"c3d4\"\n  }\n]\n")
	commit(t, ctx, repo, "max_ci_jobs 10 -> 7: the host was at 99% CPU")

	// And a commit that is only grain's record moving, which must not read
	// as a settings change at all.
	writeState(t, dir, "tables/task.json", "[\n  {\n    \"id\": \"e5f6\"\n  }\n]\n")
	commit(t, ctx, repo, "a task moved")

	changes, err := repo.SettingsChanges(ctx, time.Time{}, 0)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("got %d settings changes, want the two that touched settings: %+v", len(changes), changes)
	}
	newest := changes[0]
	if newest.Subject != "max_ci_jobs 10 -> 7: the host was at 99% CPU" {
		t.Errorf("newest change is %q, want the operator's own commit", newest.Subject)
	}
	if newest.At.IsZero() || newest.Commit == "" {
		t.Errorf("a change with no moment or no commit to look at: %+v", newest)
	}
	if len(newest.Paths) != 1 || newest.Paths[0] != "tables/grain_config.json" {
		t.Errorf("paths are %v, want only the settings file -- task.json moved in the same commit and is not a setting", newest.Paths)
	}
	if len(newest.Fields) != 1 {
		t.Fatalf("fields are %+v, want max_ci_jobs alone", newest.Fields)
	}
	got := newest.Fields[0]
	if got.Name != "max_ci_jobs" || got.From != "10" || got.To != "7" {
		t.Errorf("got %+v, want max_ci_jobs from 10 to 7", got)
	}
}

// A value that is not a dial is named and not quoted: this report is read
// by agents and by anyone with the UI open, and a settings row holds
// prompts, paths, credential names and capability lists as well as the
// numbers a tuning agent is after.
func TestSettingsChangesNamesAStringFieldWithoutQuotingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, dir := historyRepo(t)

	writeState(t, dir, "tables/grain_config.json",
		"[\n  {\n    \"max_ci_jobs\": 7,\n    \"github_posting_credential\": \"before\"\n  }\n]\n")
	commit(t, ctx, repo, "as it was")
	writeState(t, dir, "tables/grain_config.json",
		"[\n  {\n    \"max_ci_jobs\": 7,\n    \"github_posting_credential\": \"after\"\n  }\n]\n")
	commit(t, ctx, repo, "a credential renamed")

	changes, err := repo.SettingsChanges(ctx, time.Time{}, 0)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(changes) == 0 || len(changes[0].Fields) != 1 {
		t.Fatalf("got %+v, want one field changed", changes)
	}
	got := changes[0].Fields[0]
	if got.Name != "github_posting_credential" {
		t.Fatalf("got %+v, want the credential field named", got)
	}
	if got.From != "" || got.To != "" {
		t.Errorf("got %+v, want the value withheld -- only numbers and booleans are quoted", got)
	}
}

// A declaration is a setting too: schedules, suites and templates live in
// settings/ as YAML rather than in a table, and the whole point of the
// companion nightly job is that it is one of them.
func TestSettingsChangesReadsADeclarationsOwnKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, dir := historyRepo(t)

	writeState(t, dir, "settings/schedules.yml", "schedules:\n  - id: nightly\n    hour: 2\n")
	commit(t, ctx, repo, "a nightly schedule")
	writeState(t, dir, "settings/schedules.yml", "schedules:\n  - id: nightly\n    hour: 3\n")
	commit(t, ctx, repo, "move it an hour later")

	changes, err := repo.SettingsChanges(ctx, time.Time{}, 0)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(changes) == 0 {
		t.Fatalf("no changes read at all")
	}
	if len(changes[0].Fields) != 1 || changes[0].Fields[0].Name != "hour" ||
		changes[0].Fields[0].From != "2" || changes[0].Fields[0].To != "3" {
		t.Errorf("got %+v, want the schedule's hour from 2 to 3", changes[0].Fields)
	}
	if len(changes[0].Paths) != 1 || changes[0].Paths[0] != "settings/schedules.yml" {
		t.Errorf("paths are %v, want the declaration file", changes[0].Paths)
	}
}

// A repository nobody has committed to yet has no history, which is an
// empty answer rather than an error -- the same contract every other
// optional reading in this tree gives a precondition that is missing.
func TestSettingsChangesOnARepositoryWithNoCommitsIsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, _ := historyRepo(t)
	changes, err := repo.SettingsChanges(ctx, time.Time{}, 0)
	if err != nil {
		t.Fatalf("reading the history of an unseeded repository: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("got %+v, want nothing", changes)
	}
}

// Every table a merged pull request can change underneath a running
// daemon is one whose change this reports: the allowlist the reader uses
// and the allowlist Apply imports are the same list, so a table added to
// one cannot go missing from the other.
func TestSettingsPathsCoverEveryTableAppliedLive(t *testing.T) {
	t.Parallel()
	paths := map[string]bool{}
	for _, p := range staterepo.SettingsPaths() {
		paths[p] = true
	}
	for _, table := range staterepo.SettingsTables {
		if !paths[staterepo.DumpPath(table)] {
			t.Errorf("%s is applied to a running daemon and a change to it is not reported", table)
		}
	}
	for _, dir := range []string{staterepo.SettingsDir, staterepo.WorkflowsDir} {
		if !paths[dir] {
			t.Errorf("%s holds declarations and a change to one is not reported", dir)
		}
	}
}

func historyRepo(t *testing.T) (*staterepo.Repo, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	repo, err := staterepo.Open(context.Background(), staterepo.Config{Dir: dir})
	if err != nil {
		t.Fatalf("opening a state repository: %v", err)
	}
	return repo, dir
}

func writeState(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("making %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func commit(t *testing.T, ctx context.Context, repo *staterepo.Repo, message string) {
	t.Helper()
	committed, err := repo.Commit(ctx, message)
	if err != nil {
		t.Fatalf("committing %q: %v", message, err)
	}
	if !committed {
		t.Fatalf("committing %q wrote nothing", message)
	}
}

// configDump is the two numbers these tests move, in the shape the export
// writes a table: one key per line, which is what makes a -U0 diff a list
// of the settings that changed.
func configDump(maxCIJobs, maxWorkers int) string {
	return "[\n  {\n    \"id\": 1,\n" +
		"    \"max_ci_jobs\": " + itoa(maxCIJobs) + ",\n" +
		"    \"max_workers\": " + itoa(maxWorkers) + "\n  }\n]\n"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for ; n > 0; n /= 10 {
		digits = string(rune('0'+n%10)) + digits
	}
	return digits
}
