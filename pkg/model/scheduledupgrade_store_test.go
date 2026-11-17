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

package model_test

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func openScheduledUpgradeSQLiteStore(t *testing.T) (*model.Store, *sql.DB, context.Context) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}
	return store, db, ctx
}

func startScheduledUpgradeTestRun(t *testing.T, store *model.Store, ctx context.Context, taskID string, interactive bool, at time.Time) string {
	t.Helper()
	actor := model.Principal{Kind: model.PrincipalHuman, ID: "operator"}
	if err := store.PutTask(ctx, model.Task{
		ID:          taskID,
		Intent:      model.IntentImplement,
		Title:       "task " + taskID,
		Body:        "work",
		Filing:      model.Filing{Attribution: model.Attribution{Actor: actor}, Reason: model.ReasonDirect},
		Approval:    &model.Attribution{Actor: actor},
		Binding:     model.BindingDirective,
		Interactive: interactive,
	}); err != nil {
		t.Fatalf("filing task %s: %v", taskID, err)
	}
	runID := taskID + "-1"
	if err := store.StartRun(ctx, model.Run{
		ID:        runID,
		TaskID:    taskID,
		Sandbox:   "sbx-" + runID,
		Attempt:   1,
		StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting run %s: %v", runID, err)
	}
	return runID
}

func TestBusyRunCount(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC)

	t.Run("zero runs", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openScheduledUpgradeSQLiteStore(t)
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 0 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (0, nil)", got, err)
		}
	})

	t.Run("live non-interactive run is counted", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openScheduledUpgradeSQLiteStore(t)
		startScheduledUpgradeTestRun(t, store, ctx, "worker", false, at)
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 1 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (1, nil)", got, err)
		}
	})

	t.Run("live interactive run is excluded", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openScheduledUpgradeSQLiteStore(t)
		startScheduledUpgradeTestRun(t, store, ctx, "director", true, at)
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 0 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (0, nil)", got, err)
		}
	})

	t.Run("finished non-interactive run is excluded", func(t *testing.T) {
		t.Parallel()
		store, _, ctx := openScheduledUpgradeSQLiteStore(t)
		runID := startScheduledUpgradeTestRun(t, store, ctx, "done", false, at)
		if err := store.FinishRun(ctx, runID, at.Add(time.Minute), "succeeded", ""); err != nil {
			t.Fatalf("finishing run %s: %v", runID, err)
		}
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 0 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (0, nil)", got, err)
		}
	})

	t.Run("orphan live task_run without task row counts as busy", func(t *testing.T) {
		t.Parallel()
		store, db, ctx := openScheduledUpgradeSQLiteStore(t)
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`) VALUES (?,?,?,?,?)",
			"orphan-1", "missing-task", "sbx-orphan-1", 1, at); err != nil {
			t.Fatalf("inserting orphan task_run: %v", err)
		}
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 1 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (1, nil)", got, err)
		}
	})

	t.Run("combined mix counts only live non-interactive and orphan runs", func(t *testing.T) {
		t.Parallel()
		store, db, ctx := openScheduledUpgradeSQLiteStore(t)
		startScheduledUpgradeTestRun(t, store, ctx, "live-worker", false, at)
		startScheduledUpgradeTestRun(t, store, ctx, "live-session", true, at)
		finishedID := startScheduledUpgradeTestRun(t, store, ctx, "finished-worker", false, at)
		if err := store.FinishRun(ctx, finishedID, at.Add(time.Minute), "succeeded", ""); err != nil {
			t.Fatalf("finishing run: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO `task_run` (`id`,`task_id`,`sandbox`,`attempt`,`started_at`) VALUES (?,?,?,?,?)",
			"orphan-1", "missing-task", "sbx-orphan-1", 1, at); err != nil {
			t.Fatalf("inserting orphan task_run: %v", err)
		}
		got, err := store.BusyRunCount(ctx)
		if err != nil || got != 2 {
			t.Fatalf("BusyRunCount() = (%d, %v), want (2, nil)", got, err)
		}
	})
}

func TestScheduledUpgradeRoundTripsAndClears(t *testing.T) {
	t.Parallel()
	store, _, ctx := openScheduledUpgradeSQLiteStore(t)

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
	store, _, ctx := openScheduledUpgradeSQLiteStore(t)
	at := time.Date(2026, 11, 16, 13, 0, 0, 0, time.UTC)

	if got, err := store.LiveCIRunCount(ctx); err != nil || got != 0 {
		t.Fatalf("LiveCIRunCount() on empty store = (%d, %v), want (0, nil)", got, err)
	}

	live := model.CIRun{
		ID: "ci-live", Key: "key-live", Repo: "bryk/grain", Ref: "main",
		Commit: "abcdef12", Event: "push", StartedAt: at,
	}
	concluded := model.CIRun{
		ID: "ci-done", Key: "key-done", Repo: "bryk/grain", Ref: "main",
		Commit: "12345678", Event: "push", StartedAt: at,
		FinishedAt: at.Add(time.Minute), Conclusion: "success",
	}
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("PutCIRun(live): %v", err)
	}
	if err := store.PutCIRun(ctx, concluded); err != nil {
		t.Fatalf("PutCIRun(concluded): %v", err)
	}

	if got, err := store.LiveCIRunCount(ctx); err != nil || got != 1 {
		t.Fatalf("LiveCIRunCount() with 1 live and 1 concluded = (%d, %v), want (1, nil)", got, err)
	}

	live.FinishedAt = at.Add(2 * time.Minute)
	live.Conclusion = "failure"
	if err := store.PutCIRun(ctx, live); err != nil {
		t.Fatalf("PutCIRun(concluding live): %v", err)
	}
	if got, err := store.LiveCIRunCount(ctx); err != nil || got != 0 {
		t.Fatalf("LiveCIRunCount() after concluding all runs = (%d, %v), want (0, nil)", got, err)
	}
}
