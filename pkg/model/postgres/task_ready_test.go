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

import (
	"database/sql"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// TestTaskOpenTriggerTransitionsAndMigration154 verifies task_open trigger
// maintenance across every PostgreSQL write shape (INSERT, ON CONFLICT DO
// UPDATE via REPLACE INTO, raw UPDATE, DELETE, state-repo import, and in-place
// migration 154 -> 154).
func TestTaskOpenTriggerTransitionsAndMigration154(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	isOpen := func(id string) bool {
		t.Helper()
		var one int
		err := db.QueryRowContext(ctx, "SELECT 1 FROM `task_open` WHERE `task_id` = ?", id).Scan(&one)
		if err == sql.ErrNoRows {
			return false
		}
		if err != nil {
			t.Fatalf("checking task_open(%s): %v", id, err)
		}
		return true
	}

	tk := task("t1", true)
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if !isOpen("t1") {
		t.Fatal("new task t1 missing from task_open")
	}
	if ok, err := store.IsReady(ctx, "t1"); err != nil || !ok {
		t.Fatalf("IsReady(t1) = %v, %v; want true", ok, err)
	}

	done := now.Add(time.Minute)
	closed := now.Add(2 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: "t1", CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if !isOpen("t1") {
		t.Fatal("completed (unclosed) task t1 dropped from task_open")
	}

	if err := store.Observe(ctx, model.Observation{TaskID: "t1", CompletedAt: &done, ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("closed task t1 still in task_open")
	}

	// Updating task via PutTask (ON CONFLICT DO UPDATE) while closed keeps it closed.
	tk.Title = "Edited while closed"
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("PutTask on closed task t1 resurrected it into task_open")
	}

	// Reopening via Observe (ON CONFLICT DO UPDATE clearing closed_at) restores task_open.
	if err := store.Observe(ctx, model.Observation{TaskID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if !isOpen("t1") {
		t.Fatal("reopening t1 via Observe did not restore task_open")
	}
	if ok, err := store.IsReady(ctx, "t1"); err != nil || !ok {
		t.Fatalf("IsReady(reopened t1) = %v, %v; want true", ok, err)
	}

	// Raw UPDATE closing and raw DELETE of closed observation.
	if _, err := db.ExecContext(ctx, "UPDATE `task_observation` SET `closed_at` = ? WHERE `task_id` = 't1'", closed); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("raw UPDATE setting closed_at left t1 in task_open")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM `task_observation` WHERE `task_id` = 't1'"); err != nil {
		t.Fatal(err)
	}
	if !isOpen("t1") {
		t.Fatal("deleting closed task_observation did not restore t1 to task_open")
	}

	// Raw DELETE of task removes it from task_open.
	if _, err := db.ExecContext(ctx, "DELETE FROM `task` WHERE `id` = 't1'"); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("deleting task t1 left orphan in task_open")
	}

	// Simulate pre-154 schema on Postgres and run in-place migration 154 -> 154.
	open1 := task("open1", true)
	closed1 := task("closed1", true)
	blocked1 := task("blocked1", true)
	blocked1.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "open1"}}
	dangling1 := task("dangling1", true)
	dangling1.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "missing-target"}}
	for _, item := range []model.Task{open1, closed1, blocked1, dangling1} {
		if err := store.PutTask(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "closed1", ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		`DROP VIEW IF EXISTS "task_ready" CASCADE`,
		`DROP TRIGGER IF EXISTS "task_open_after_task_change" ON "task"`,
		`DROP TRIGGER IF EXISTS "task_open_after_task_id_update" ON "task"`,
		`DROP TRIGGER IF EXISTS "task_open_after_obs_insert" ON "task_observation"`,
		`DROP TRIGGER IF EXISTS "task_open_after_obs_update" ON "task_observation"`,
		`DROP TRIGGER IF EXISTS "task_open_after_obs_delete" ON "task_observation"`,
		`DROP TABLE IF EXISTS "task_open"`,
		`DROP INDEX IF EXISTS "task_link_task_blocks"`,
		`DROP INDEX IF EXISTS "task_pr_feedback_pending"`,
		"UPDATE `grain_schema` SET `version` = 153 WHERE `id` = 1",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("simulating schema 153 (%s): %v", stmt, err)
		}
	}
	if err := store.Init(ctx); err != nil {
		t.Fatalf("migrating 153 -> 154 on Postgres: %v", err)
	}
	if !isOpen("open1") || !isOpen("blocked1") || !isOpen("dangling1") || isOpen("closed1") {
		t.Fatalf("task_open after migration 154: open1=%v blocked1=%v dangling1=%v closed1=%v",
			isOpen("open1"), isOpen("blocked1"), isOpen("dangling1"), isOpen("closed1"))
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ready, []string{"open1"}) {
		t.Fatalf("Ready after migration 154 = %v, want [open1]", ready)
	}

	// State-repo Export + Import into a fresh Postgres store rebuilds task_open accurately.
	dumpDir := t.TempDir()
	if err := staterepo.Export(ctx, db, dumpDir); err != nil {
		t.Fatalf("staterepo.Export: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dumpDir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	dstStore, dstDB, _ := openStore(t)
	if err := staterepo.Import(ctx, dstDB, dumpDir); err != nil {
		t.Fatalf("staterepo.Import: %v", err)
	}
	dstReady, err := dstStore.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dstReady, []string{"open1"}) {
		t.Fatalf("Ready after staterepo.Import = %v, want [open1]", dstReady)
	}
}
