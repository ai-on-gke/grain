package staterepo_test

// The everyNMinutes cadence (model.RecurrenceEveryNMinutes) through both
// shapes a state repository has held a schedule in: the settings
// document the export writes now, as `every: {minutes: N}`, and the
// tables/schedule.json of a repository written before the documents
// existed, as the bare every_n_minutes column.

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

func TestAnEveryNMinutesScheduleRoundTripsThroughTheSettingsDocument(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	want := model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Release manager", Target: repo, Recurrence: want,
		Overlap: model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	schedules := read(t, filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("schedule"))))
	if !strings.Contains(schedules, "every:\n      minutes: 30") {
		t.Errorf("the schedules document does not carry the cadence as `every: {minutes: 30}`:\n%s", schedules)
	}
	for _, gone := range []string{"recurrence:", "every_n_minutes", "every-n-minutes", "hours:"} {
		if strings.Contains(schedules, gone) {
			t.Errorf("the schedules document writes %q for a minutes cadence:\n%s", gone, schedules)
		}
	}

	restored, freshDB := openDB(t)
	if err := staterepo.Import(ctx, freshDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := restored.GetSchedule(ctx, "sched-1")
	if err != nil || got == nil {
		t.Fatalf("reading the schedule back: %v %v", got, err)
	}
	if got.Recurrence.Kind != want.Kind || got.Recurrence.EveryNMinutes != want.EveryNMinutes ||
		got.Recurrence.EveryNHours != 0 {
		t.Errorf("recurrence = %+v, want %+v", got.Recurrence, want)
	}

	// And the export of what was imported is the same file, byte for byte.
	again := t.TempDir()
	if err := staterepo.Export(ctx, freshDB, again); err != nil {
		t.Fatalf("exporting the restore: %v", err)
	}
	if got := read(t, filepath.Join(again, filepath.FromSlash(staterepo.DumpPath("schedule")))); got != schedules {
		t.Errorf("the restore exports a different schedules document:\n%s\nwant:\n%s", got, schedules)
	}
}

// A hand-written `every` names exactly one unit: both is a cadence
// nobody can read one way.
func TestAnEveryBlockNamingBothUnitsIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "x", Target: model.RepoRef{Owner: "bwsalmon", Name: "grain"},
		Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNMinutes, EveryNMinutes: 30},
		Enabled:    true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatalf("putting a schedule: %v", err)
	}
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	editSettings(t, dir, "schedule", func(entries []map[string]any) {
		entries[0]["every"] = map[string]any{"minutes": 30, "hours": 1}
	})
	_, freshDB := openDB(t)
	if err := staterepo.Import(ctx, freshDB, dir); err == nil {
		t.Fatal("imported an `every` with both hours and minutes, want it refused")
	}
}

// A repository written before the settings documents carries a schedule
// as its row in tables/schedule.json; the column is all a minutes
// cadence needs there.
func TestAnEveryNMinutesScheduleImportsFromTheLegacyTableFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, db := openDB(t)
	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// No document: the table file a pre-document build wrote stands in
	// its place.
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("schedule")))); err != nil &&
		!os.IsNotExist(err) {
		t.Fatal(err)
	}
	legacy := `[
  {
    "id": "sched-1",
    "title": "Release manager",
    "body": "",
    "target_owner": "bwsalmon",
    "target_name": "grain",
    "base": null,
    "auto_merge": 0,
    "auto_approve_proposals": 0,
    "template_id": null,
    "suite_id": null,
    "recurrence_kind": "everyNMinutes",
    "every_n_hours": null,
    "every_n_minutes": 15,
    "time_of_day_minutes": null,
    "weekday": null,
    "day_of_month": null,
    "overlap": "skip",
    "enabled": 1,
    "next_run_at": "2026-08-27T12:00:00Z",
    "last_run_at": null,
    "created_at": "2026-08-27T12:00:00Z"
  }
]
`
	if err := os.MkdirAll(filepath.Join(dir, staterepo.TablesDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, staterepo.TablesDir, "schedule.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	restored, freshDB := openDB(t)
	if err := staterepo.Import(ctx, freshDB, dir); err != nil {
		t.Fatalf("importing: %v", err)
	}
	got, err := restored.GetSchedule(ctx, "sched-1")
	if err != nil || got == nil {
		t.Fatalf("reading the schedule back: %v %v", got, err)
	}
	if got.Recurrence.Kind != model.RecurrenceEveryNMinutes || got.Recurrence.EveryNMinutes != 15 {
		t.Errorf("recurrence = %+v, want every 15 minutes", got.Recurrence)
	}
	if next, want := got.Recurrence.Next(got.NextRunAt, nil), now.Add(15*time.Minute); !next.Equal(want) {
		t.Errorf("Next = %v, want %v", next, want)
	}
}
