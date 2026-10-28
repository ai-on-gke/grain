package demo

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestDemoSchedulesFiresADueScheduleOnItsOwn is -schedules: a schedule
// that has come due is fired by the loop with nobody pressing Run now --
// into a repository whose standing work is switched off, which records a
// skip and moves the cadence on, and with the schedule's own override
// turned on, which files its task.
func TestDemoSchedulesFiresADueScheduleOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo,
		Work: model.WorkOwnership{Default: model.OwnershipOff}.With(model.ScheduleItem("kept"), model.OwnershipOn)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"skipped", "kept"} {
		if err := store.PutSchedule(ctx, model.Schedule{
			ID: id, Title: "Nightly " + id, Body: "walk", Target: repo,
			Recurrence: model.Recurrence{Kind: model.RecurrenceEveryNHours, EveryNHours: 24},
			Enabled:    true, NextRunAt: now.Add(-time.Minute), CreatedAt: now.Add(-time.Hour),
		}); err != nil {
			t.Fatalf("filing schedule %s: %v", id, err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runDemoSchedules(ctx, store, 10*time.Millisecond)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		skips, err := store.ScheduleSkips(ctx, "skipped")
		if err != nil {
			t.Fatalf("ScheduleSkips: %v", err)
		}
		kept, err := store.GetSchedule(ctx, "kept")
		if err != nil {
			t.Fatalf("GetSchedule: %v", err)
		}
		if len(skips) > 0 && kept.LastRunAt != nil {
			if skips[0].Reason != model.SuppressedByStandingWork {
				t.Errorf("skip reason = %q, want %q", skips[0].Reason, model.SuppressedByStandingWork)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 10s of -schedules: %d skip(s) for the off schedule, kept last ran %v", len(skips), kept.LastRunAt)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runDemoSchedules did not return once its context was done")
	}
}
