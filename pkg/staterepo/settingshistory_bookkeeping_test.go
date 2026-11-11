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

// The settings history is not swamped by grain's own bookkeeping (task
// 4405). On a deployment whose daemon rewrites a schedule's next-run-at
// every time it fires and re-exports agent_backends every half hour, the
// reader used to spend its whole 200-commit budget in about a day, and
// read a schedule's `body: |` prose as keys.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// A real change five days back is found behind more bookkeeping commits
// than one page of `git log` holds, and the reader says it reached the
// window's start rather than stopping at the newest few hundred commits.
func TestSettingsHistoryLooksPastBookkeepingToTheWindowStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, dir := historyRepo(t)
	now := time.Now().UTC().Truncate(time.Second)

	writeState(t, dir, "tables/grain_config.json", configWithBackends(10, "a"))
	writeState(t, dir, "settings/schedules.yml", scheduleFile("2026-01-01T00:00:00Z", "a nightly review"))
	commitAt(t, dir, now.Add(-20*24*time.Hour), "the deployment as it was")

	changedAt := now.Add(-5 * 24 * time.Hour)
	writeState(t, dir, "tables/grain_config.json", configWithBackends(12, "a"))
	commitAt(t, dir, changedAt, "max_workers 10 -> 12")

	// More than one page of the daemon's own bookkeeping, newer than the
	// change: schedules firing and model discovery re-exporting.
	const bookkeeping = 600
	step := (5*24*time.Hour - time.Hour) / bookkeeping
	for i := 0; i < bookkeeping; i++ {
		at := changedAt.Add(time.Duration(i+1) * step)
		if i%3 == 0 {
			writeState(t, dir, "tables/grain_config.json", configWithBackends(12, fmt.Sprintf("b%d", i)))
		} else {
			writeState(t, dir, "settings/schedules.yml", scheduleFile(at.Format(time.RFC3339), "a nightly review"))
		}
		commitAt(t, dir, at, "export")
	}

	since := now.Add(-14 * 24 * time.Hour)
	history, err := repo.SettingsHistory(ctx, since, 0)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(history.Changes) != 1 {
		t.Fatalf("got %d changes, want only max_workers -- bookkeeping is not a settings change: %+v",
			len(history.Changes), history.Changes[:min(3, len(history.Changes))])
	}
	got := history.Changes[0]
	if len(got.Fields) != 1 || got.Fields[0].Name != "max_workers" || got.Fields[0].From != "10" || got.Fields[0].To != "12" {
		t.Errorf("fields are %+v, want max_workers from 10 to 12", got.Fields)
	}
	if !got.At.Equal(changedAt) {
		t.Errorf("change at %v, want %v", got.At, changedAt)
	}
	if history.Reaches.IsZero() || history.Reaches.After(got.At) || history.Reaches.After(since) {
		t.Errorf("Reaches = %v, want no later than the window start %v -- the whole window was read", history.Reaches, since)
	}

	// The limit counts changes kept, not commits: one change asked for is
	// the max_workers one, however many bookkeeping commits are newer.
	one, err := repo.SettingsHistory(ctx, time.Time{}, 1)
	if err != nil {
		t.Fatalf("reading one settings change: %v", err)
	}
	if len(one.Changes) != 1 || one.Changes[0].Subject != "max_workers 10 -> 12" {
		t.Fatalf("got %+v, want the max_workers change", one.Changes)
	}
	if !one.Reaches.Equal(changedAt) {
		t.Errorf("Reaches = %v, want the oldest change kept (%v) when the limit stopped the reader", one.Reaches, changedAt)
	}
}

// An edit to a schedule's multi-line body is one field, body, and none of
// the prose inside it is read as a key -- even lines that look like one.
func TestSettingsChangesReadsABlockScalarAsOneField(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, dir := historyRepo(t)

	before := "schedules:\n" +
		"  - id: nightly\n" +
		"    body: |\n" +
		"      Review the deployment.\n" +
		"      via: the metrics report\n" +
		"      `state=current`: is what we have)\n" +
		"    daily:\n" +
		"      at: \"03:00\"\n" +
		"    grants:\n" +
		"      - capability: db-read\n" +
		"        via: schedule\n" +
		"    next-run-at: \"2026-11-01T03:00:00Z\"\n"
	after := strings.Replace(before, "      via: the metrics report\n",
		"      via: the settings history\n      (`api: and whatever else it needs\n", 1)
	after = strings.Replace(after, "2026-11-01T03:00:00Z", "2026-11-02T03:00:00Z", 1)

	writeState(t, dir, "settings/schedules.yml", before)
	commit(t, ctx, repo, "a nightly schedule")
	writeState(t, dir, "settings/schedules.yml", after)
	commit(t, ctx, repo, "say more in the body")

	changes, err := repo.SettingsChanges(ctx, time.Time{}, 0)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(changes) == 0 {
		t.Fatal("no changes read at all")
	}
	fields := changes[0].Fields
	if len(fields) != 1 || fields[0].Name != "body" || fields[0].Path != "settings/schedules.yml" {
		t.Fatalf("fields are %+v, want the body alone", fields)
	}
	if fields[0].From != "" || fields[0].To != "" {
		t.Errorf("got %+v, want prose named and not quoted", fields[0])
	}

	// And a change to a nested key after the body has ended is that key,
	// not more of the body.
	writeState(t, dir, "settings/schedules.yml", strings.Replace(after, `at: "03:00"`, `at: "04:00"`, 1))
	commit(t, ctx, repo, "an hour later")
	changes, err = repo.SettingsChanges(ctx, time.Time{}, 1)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(changes) != 1 || len(changes[0].Fields) != 1 || changes[0].Fields[0].Name != "at" {
		t.Errorf("got %+v, want the daily time alone", changes)
	}
}

// A commit that moves nothing but grain's own bookkeeping is not a
// settings change; one that moves bookkeeping beside a real setting is
// that setting alone.
func TestSettingsChangesSkipsBookkeepingOnlyCommits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo, dir := historyRepo(t)

	writeState(t, dir, "settings/schedules.yml", "schedules:\n  - id: nightly\n    hour: 2\n    next-run-at: \"2026-11-01T02:00:00Z\"\n    last-run-at: null\n")
	writeState(t, dir, "tables/grain_config.json", configWithBackends(10, "a"))
	commit(t, ctx, repo, "as it was")

	writeState(t, dir, "settings/schedules.yml", "schedules:\n  - id: nightly\n    hour: 2\n    next-run-at: \"2026-11-02T02:00:00Z\"\n    last-run-at: \"2026-11-01T02:00:03Z\"\n")
	commit(t, ctx, repo, "the schedule fired")
	writeState(t, dir, "tables/grain_config.json", configWithBackends(10, "b"))
	commit(t, ctx, repo, "models rediscovered")

	changes, err := repo.SettingsChanges(ctx, time.Time{}, 0)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(changes) != 1 || changes[0].Subject != "as it was" {
		t.Fatalf("got %+v, want only the first commit -- the other two are bookkeeping", changes)
	}

	writeState(t, dir, "settings/schedules.yml", "schedules:\n  - id: nightly\n    hour: 3\n    next-run-at: \"2026-11-03T03:00:00Z\"\n    last-run-at: \"2026-11-02T02:00:03Z\"\n")
	commit(t, ctx, repo, "an hour later, and it fired")
	changes, err = repo.SettingsChanges(ctx, time.Time{}, 1)
	if err != nil {
		t.Fatalf("reading settings history: %v", err)
	}
	if len(changes) != 1 || len(changes[0].Fields) != 1 || changes[0].Fields[0].Name != "hour" {
		t.Errorf("got %+v, want hour alone", changes)
	}
}

// configWithBackends is a grain_config dump with a max_workers dial and an
// agent_backends value standing for whatever discovery last wrote.
func configWithBackends(maxWorkers int, backends string) string {
	return "[\n  {\n    \"id\": 1,\n" +
		"    \"agent_backends\": \"[{\\\"name\\\": \\\"" + backends + "\\\"}]\",\n" +
		"    \"max_workers\": " + itoa(maxWorkers) + "\n  }\n]\n"
}

func scheduleFile(nextRunAt, body string) string {
	return "schedules:\n  - id: nightly\n    body: |\n      " + body + "\n      via: the report\n" +
		"    hour: 2\n    next-run-at: \"" + nextRunAt + "\"\n"
}

// commitAt commits everything in dir with both of git's dates set, which
// Repo.Commit cannot do: --since filters on the committer date, and the
// point of these tests is history spread over days.
func commitAt(t *testing.T, dir string, at time.Time, message string) {
	t.Helper()
	stamp := at.UTC().Format(time.RFC3339)
	for _, args := range [][]string{
		{"add", "-A"},
		{"-c", "user.name=history test", "-c", "user.email=history@example.com", "commit", "-q", "-m", message},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+stamp, "GIT_COMMITTER_DATE="+stamp)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
