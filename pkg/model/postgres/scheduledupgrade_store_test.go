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

package postgres_test

// The scheduled upgrade store against a real PostgreSQL database.
// task.interactive is BOOLEAN on PostgreSQL and INTEGER on SQLite, so
// BusyRunCount's LEFT JOIN over task_run and task is where an unadapted
// integer fallback (COALESCE(t.interactive, 0) = 0 -> SQLSTATE 42804)
// previously wedged reconcileScheduledUpgrade on every tick.

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func startScheduledUpgradeRun(t *testing.T, store *model.Store, ctx context.Context, taskID string, interactive bool, at time.Time) string {
	t.Helper()
	t1 := task(taskID, true)
	t1.Interactive = interactive
	if err := store.PutTask(ctx, t1); err != nil {
		t.Fatalf("PutTask(%s): %v", taskID, err)
	}
	runID := taskID + "-1"
	if err := store.StartRun(ctx, model.Run{
		ID:        runID,
		TaskID:    taskID,
		Sandbox:   "sbx-" + runID,
		Attempt:   1,
		StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatalf("StartRun(%s): %v", runID, err)
	}
	return runID
}

func TestBusyRunCount(t *testing.T) {
	t.Parallel()

	t.Run("zero runs", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openStore(t)
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 0 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (0, nil)", got, err)
		}
	})

	t.Run("live non-interactive run is counted", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openStore(t)
		startScheduledUpgradeRun(t, store, ctx, "worker", false, now)
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 1 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (1, nil)", got, err)
		}
	})

	t.Run("live interactive run is excluded", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openStore(t)
		startScheduledUpgradeRun(t, store, ctx, "director", true, now)
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 0 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (0, nil)", got, err)
		}
	})

	t.Run("finished non-interactive run is excluded", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openStore(t)
		runID := startScheduledUpgradeRun(t, store, ctx, "done", false, now)
		if err := store.FinishRun(ctx, runID, now.Add(time.Minute), "succeeded", ""); err != nil {
			t.Fatalf("FinishRun(%s): %v", runID, err)
		}
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 0 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (0, nil)", got, err)
		}
	})

	t.Run("orphan live task_run without task row counts as busy", func(t *testing.T) {
		t.Parallel()
		store, db, ctx := openStore(t)
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`) VALUES (?,?,?,?,?)",
			"orphan-1", "missing-task", "sbx-orphan-1", 1, now); err != nil {
			t.Fatalf("inserting orphan task_run: %v", err)
		}
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 1 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (1, nil)", got, err)
		}
	})

	t.Run("combined mix counts only live non-interactive and orphan runs", func(t *testing.T) {
		t.Parallel()
		store, db, ctx := openStore(t)
		startScheduledUpgradeRun(t, store, ctx, "live-worker", false, now)
		startScheduledUpgradeRun(t, store, ctx, "live-session", true, now)
		finishedID := startScheduledUpgradeRun(t, store, ctx, "finished-worker", false, now)
		if err := store.FinishRun(ctx, finishedID, now.Add(time.Minute), "succeeded", ""); err != nil {
			t.Fatalf("FinishRun(%s): %v", finishedID, err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`) VALUES (?,?,?,?,?)",
			"orphan-1", "missing-task", "sbx-orphan-1", 1, now); err != nil {
			t.Fatalf("inserting orphan task_run: %v", err)
		}
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 2 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (2, nil)", got, err)
		}
	})
}

// TestBusyRunCountCoalesceBoolAdapterExecutesOnPostgres exercises the adapter's
// COALESCE boolean rewrite directly against PostgreSQL so that even if a caller
// writes COALESCE(`t`.`interactive`, 0) = 0 (or != 0), PostgreSQL evaluates it
// cleanly without SQLSTATE 42804.
func TestBusyRunCountCoalesceBoolAdapterExecutesOnPostgres(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	startScheduledUpgradeRun(t, store, ctx, "live-worker", false, now)
	startScheduledUpgradeRun(t, store, ctx, "live-session", true, now)
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`) VALUES (?,?,?,?,?)",
		"orphan-1", "missing-task", "sbx-orphan-1", 1, now); err != nil {
		t.Fatalf("inserting orphan task_run: %v", err)
	}

	var nonInteractiveOrOrphan int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` AS `r` LEFT JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"WHERE `r`.`finished_at` IS NULL AND COALESCE(`t`.`interactive`, 0) = 0").Scan(&nonInteractiveOrOrphan); err != nil {
		t.Fatalf("COALESCE(..., 0) = 0 on postgres failed: %v", err)
	}
	if nonInteractiveOrOrphan != 2 {
		t.Fatalf("COALESCE(..., 0) = 0 count = %d, want 2", nonInteractiveOrOrphan)
	}

	var interactiveOnly int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task_run` AS `r` LEFT JOIN `task` AS `t` ON `t`.`id` = `r`.`task_id` "+
			"WHERE `r`.`finished_at` IS NULL AND COALESCE(`t`.`interactive`, 0) != 0").Scan(&interactiveOnly); err != nil {
		t.Fatalf("COALESCE(..., 0) != 0 on postgres failed: %v", err)
	}
	if interactiveOnly != 1 {
		t.Fatalf("COALESCE(..., 0) != 0 count = %d, want 1", interactiveOnly)
	}
}

func TestScheduledUpgradeRoundTripsAndClears(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	got, err := store.GetScheduledUpgrade(ctx)
	if err != nil || got != nil {
		t.Fatalf("GetScheduledUpgrade() on empty store = (%+v, %v), want (nil, nil)", got, err)
	}
	if cleared, err := store.ClearScheduledUpgrade(ctx); err != nil || cleared {
		t.Fatalf("ClearScheduledUpgrade() on empty store = (%v, %v), want (false, nil)", cleared, err)
	}

	first := model.ScheduledUpgrade{
		Branch:      "latest",
		RequestedAt: time.Date(2026, 11, 16, 12, 59, 53, 0, time.UTC),
		By:          model.Principal{Kind: model.PrincipalHuman, ID: "bryk@google.com", Source: model.SourceAccessCode},
	}
	if err := store.SetScheduledUpgrade(ctx, first); err != nil {
		t.Fatalf("SetScheduledUpgrade(first): %v", err)
	}
	got, err = store.GetScheduledUpgrade(ctx)
	if err != nil || got == nil {
		t.Fatalf("GetScheduledUpgrade() = (%+v, %v), want non-nil", got, err)
	}
	if !reflect.DeepEqual(*got, first) {
		t.Fatalf("GetScheduledUpgrade() = %+v, want %+v", *got, first)
	}

	second := model.ScheduledUpgrade{
		Branch:      "release/v3.7.0",
		RequestedAt: first.RequestedAt.Add(5 * time.Minute),
		By:          model.Principal{Kind: model.PrincipalAgent, ID: "24"},
	}
	if err := store.SetScheduledUpgrade(ctx, second); err != nil {
		t.Fatalf("SetScheduledUpgrade(second): %v", err)
	}
	got, err = store.GetScheduledUpgrade(ctx)
	if err != nil || got == nil {
		t.Fatalf("GetScheduledUpgrade() after replace = (%+v, %v), want non-nil", got, err)
	}
	if !reflect.DeepEqual(*got, second) {
		t.Fatalf("GetScheduledUpgrade() after replace = %+v, want %+v", *got, second)
	}

	cleared, err := store.ClearScheduledUpgrade(ctx)
	if err != nil || !cleared {
		t.Fatalf("ClearScheduledUpgrade() = (%v, %v), want (true, nil)", cleared, err)
	}
	got, err = store.GetScheduledUpgrade(ctx)
	if err != nil || got != nil {
		t.Fatalf("GetScheduledUpgrade() after clear = (%+v, %v), want (nil, nil)", got, err)
	}
}

func TestScheduledUpgradeLiveCIRunCount(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	if got, err := store.LiveCIRunCount(ctx); err != nil || got != 0 {
		t.Fatalf("LiveCIRunCount() on empty store = (%d, %v), want (0, nil)", got, err)
	}

	live := ciRun("live1", now)
	live.Conclusion = ""
	live.FinishedAt = time.Time{}
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("PutCIRun(live): %v", err)
	}

	concluded := ciRun("done1", now)
	concluded.Conclusion = "success"
	concluded.FinishedAt = now.Add(2 * time.Minute)
	if err := store.PutCIRun(ctx, concluded); err != nil {
		t.Fatalf("PutCIRun(concluded): %v", err)
	}

	if got, err := store.LiveCIRunCount(ctx); err != nil || got != 1 {
		t.Fatalf("LiveCIRunCount() with 1 live and 1 concluded = (%d, %v), want (1, nil)", got, err)
	}

	live.Conclusion = "failure"
	live.FinishedAt = now.Add(3 * time.Minute)
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("PutCIRun(concluding live): %v", err)
	}
	if got, err := store.LiveCIRunCount(ctx); err != nil || got != 0 {
		t.Fatalf("LiveCIRunCount() after concluding all runs = (%d, %v), want (0, nil)", got, err)
	}
}
