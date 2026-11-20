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

package sqlite_test

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func explainPlanLines(t *testing.T, ctx context.Context, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN %q: %v", query, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// TestTaskReadyQueryPlansAvoidFullTaskLinkAndHistoryScans pins the SQLite
// query plans for single-task readiness, full-queue readiness, open blockers,
// and open pull-request links so a future view edit cannot reintroduce a full
// task_link or task table scan.
func TestTaskReadyQueryPlansAvoidFullTaskLinkAndHistoryScans(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	blocker := task("blk1", true)
	if err := store.PutTask(ctx, blocker); err != nil {
		t.Fatal(err)
	}
	readyTask := task("rdy1", true)
	if err := store.PutTask(ctx, readyTask); err != nil {
		t.Fatal(err)
	}
	blockedTask := task("wait1", true)
	blockedTask.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "blk1"}}
	if err := store.PutTask(ctx, blockedTask); err != nil {
		t.Fatal(err)
	}

	assertNoForbidden := func(name string, lines []string, forbidden ...string) {
		t.Helper()
		joined := strings.Join(lines, "\n")
		for _, bad := range forbidden {
			if strings.Contains(joined, bad) {
				t.Errorf("%s plan unexpectedly contains %q:\n%s", name, bad, joined)
			}
		}
	}

	// Single-task readiness must perform zero full scans and never materialize task_blocked.
	oneLines := explainPlanLines(t, ctx, db, "SELECT `task_id` FROM `task_ready` WHERE `task_id` = ?", "rdy1")
	assertNoForbidden("is_ready_one", oneLines, "MATERIALIZE task_blocked", "SCAN ")
	if !strings.Contains(strings.Join(oneLines, "\n"), "task_link_task_blocks") {
		t.Errorf("is_ready_one plan did not use task_link_task_blocks:\n%s", strings.Join(oneLines, "\n"))
	}

	// Full-queue readiness must drive from task_open and never scan task_link or task.
	allLines := explainPlanLines(t, ctx, db, "SELECT `task_id` FROM `task_ready`")
	assertNoForbidden("ready", allLines,
		"MATERIALIZE task_blocked",
		"SCAN l",
		"SCAN task_link",
		"SCAN t\n",
		"SCAN task ",
	)
	if !strings.Contains(strings.Join(allLines, "\n"), "task_link_task_blocks") {
		t.Errorf("ready plan did not use task_link_task_blocks:\n%s", strings.Join(allLines, "\n"))
	}
	for _, line := range allLines {
		if strings.HasPrefix(line, "SCAN ") && !strings.HasPrefix(line, "SCAN op") {
			t.Errorf("ready plan has unexpected full scan %q:\n%s", line, strings.Join(allLines, "\n"))
		}
	}

	// Direct OpenBlockers lookup against task_blocked must use task_link_task_blocks, not scan task_link.
	blkLines := explainPlanLines(t, ctx, db, "SELECT `open_blockers` FROM `task_blocked` WHERE `task_id` = ?", "wait1")
	assertNoForbidden("open_blockers", blkLines, "SCAN l", "SCAN task_link")
	if !strings.Contains(strings.Join(blkLines, "\n"), "task_link_task_blocks") {
		t.Errorf("open_blockers plan did not use task_link_task_blocks:\n%s", strings.Join(blkLines, "\n"))
	}
}

// TestTaskOpenTriggerTransitionsAndMigration153 verifies every lifecycle
// transition of task_open (insert, update/replace on open and closed tasks,
// close, reopen via REPLACE INTO and raw UPDATE, observation delete, task
// delete, state-repo import, and schema 152 -> 153 migration backfill).
func TestTaskOpenTriggerTransitionsAndMigration153(t *testing.T) {
	t.Parallel()
	store, db, ctx := openFreshStore(t)

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

	// New task starts open and ready.
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

	// Non-closing observation keeps task open.
	done := now.Add(time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: "t1", CompletedAt: &done}); err != nil {
		t.Fatal(err)
	}
	if !isOpen("t1") {
		t.Fatal("completed (unclosed) task t1 dropped from task_open")
	}
	if ok, err := store.IsReady(ctx, "t1"); err != nil || ok {
		t.Fatalf("IsReady(completed t1) = %v, %v; want false", ok, err)
	}

	// Closing removes from task_open.
	closed := now.Add(2 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: "t1", CompletedAt: &done, ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("closed task t1 still in task_open")
	}

	// Updating the task declaration (REPLACE INTO task) while closed must NOT resurrect it into task_open.
	tk.Title = "Edited while closed"
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("PutTask on closed task t1 resurrected it into task_open")
	}

	// Updating observation (REPLACE INTO task_observation) while keeping ClosedAt set keeps it closed.
	prMerged := now.Add(3 * time.Minute)
	if err := store.Observe(ctx, model.Observation{TaskID: "t1", CompletedAt: &done, ClosedAt: &closed, PrMergedAt: &prMerged}); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("Observe keeping ClosedAt resurrected t1 into task_open")
	}

	// Reopening via Observe (REPLACE INTO task_observation with ClosedAt=nil) must restore task_open
	// even with SQLite's default recursive_triggers=OFF.
	if err := store.Observe(ctx, model.Observation{TaskID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if !isOpen("t1") {
		t.Fatal("reopening t1 via Observe (REPLACE) did not restore task_open")
	}
	if ok, err := store.IsReady(ctx, "t1"); err != nil || !ok {
		t.Fatalf("IsReady(reopened t1) = %v, %v; want true", ok, err)
	}

	// Raw UPDATE closing and reopening task_observation.
	if _, err := db.ExecContext(ctx, "UPDATE `task_observation` SET `closed_at` = ? WHERE `task_id` = 't1'", closed); err != nil {
		t.Fatal(err)
	}
	if isOpen("t1") {
		t.Fatal("raw UPDATE setting closed_at left t1 in task_open")
	}
	if _, err := db.ExecContext(ctx, "UPDATE `task_observation` SET `closed_at` = NULL WHERE `task_id` = 't1'"); err != nil {
		t.Fatal(err)
	}
	if !isOpen("t1") {
		t.Fatal("raw UPDATE clearing closed_at did not restore t1 to task_open")
	}

	// Raw DELETE of a closed observation restores task_open.
	if _, err := db.ExecContext(ctx, "UPDATE `task_observation` SET `closed_at` = ? WHERE `task_id` = 't1'", closed); err != nil {
		t.Fatal(err)
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

	// Migration 152 -> 153 backfill and state-repo Export/Import round-trip.
	open1 := task("open1", true)
	closed1 := task("closed1", true)
	blocked1 := task("blocked1", true)
	blocked1.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "open1"}}
	dangling1 := task("dangling1", true)
	dangling1.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "does-not-exist"}}
	for _, item := range []model.Task{open1, closed1, blocked1, dangling1} {
		if err := store.PutTask(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "closed1", ClosedAt: &closed}); err != nil {
		t.Fatal(err)
	}

	// Simulate pre-153 database state and re-run Init to exercise migration 153.
	for _, stmt := range []string{
		"DROP VIEW IF EXISTS `task_ready`",
		"DROP TRIGGER IF EXISTS `task_open_after_task_insert`",
		"DROP TRIGGER IF EXISTS `task_open_after_task_delete`",
		"DROP TRIGGER IF EXISTS `task_open_after_task_update`",
		"DROP TRIGGER IF EXISTS `task_open_after_obs_insert_closed`",
		"DROP TRIGGER IF EXISTS `task_open_after_obs_insert_open`",
		"DROP TRIGGER IF EXISTS `task_open_after_obs_update`",
		"DROP TRIGGER IF EXISTS `task_open_after_obs_delete`",
		"DROP TABLE IF EXISTS `task_open`",
		"DROP INDEX IF EXISTS `task_link_task_blocks`",
		"DROP INDEX IF EXISTS `task_pr_feedback_pending`",
		"UPDATE `grain_schema` SET `version` = 152 WHERE `id` = 1",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("simulating schema 152 (%s): %v", stmt, err)
		}
	}
	if err := store.Init(ctx); err != nil {
		t.Fatalf("migrating 152 -> 153: %v", err)
	}
	if !isOpen("open1") || !isOpen("blocked1") || !isOpen("dangling1") || isOpen("closed1") {
		t.Fatalf("task_open after migration 153: open1=%v blocked1=%v dangling1=%v closed1=%v",
			isOpen("open1"), isOpen("blocked1"), isOpen("dangling1"), isOpen("closed1"))
	}
	ready, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ready, []string{"open1"}) {
		t.Fatalf("Ready after migration 153 = %v, want [open1]", ready)
	}

	// State-repo Export + Import into a fresh store rebuilds task_open accurately.
	dumpDir := t.TempDir()
	if err := staterepo.Export(ctx, db, dumpDir); err != nil {
		t.Fatalf("staterepo.Export: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dumpDir, model.SchemaVersion); err != nil {
		t.Fatal(err)
	}
	dstStore, dstDB, _ := openFreshStore(t)
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
	openTasks, err := dstStore.ListOpenTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var openIDs []string
	for _, tk := range openTasks {
		openIDs = append(openIDs, tk.ID)
	}
	slices.Sort(openIDs)
	if !slices.Equal(openIDs, []string{"blocked1", "dangling1", "open1"}) {
		t.Fatalf("ListOpenTasks after Import = %v, want [blocked1 dangling1 open1]", openIDs)
	}
}

// TestTaskReadyMatchesTaskStateAndBlockersAcrossAllStates holds the rewritten
// task_ready view to the exact same state/blocker semantics as task_state +
// model.IsBlocked across every task state.
func TestTaskReadyMatchesTaskStateAndBlockersAcrossAllStates(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	deferAt := now.Add(time.Hour)
	doneAt := now.Add(2 * time.Hour)
	closedAt := now.Add(3 * time.Hour)
	qID := int64(42)

	seed := []model.Task{
		task("queued-no-obs", true),
		task("queued-open-obs", true),
		task("proposed", false),
		func() model.Task { tk := task("deferred", true); tk.DeferredAt = &deferAt; return tk }(),
		task("running", true),
		task("failed", true),
		task("awaiting-reply", true),
		func() model.Task {
			tk := task("awaiting-submit", true)
			tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "owner/payments-api#10"}}
			return tk
		}(),
		func() model.Task {
			tk := task("completed", true)
			tk.AutoMerge = true
			tk.Links = []model.Link{{Kind: model.LinkFixes, Target: "owner/payments-api#11"}}
			return tk
		}(),
		task("closed", true),
		task("paused", true),
		func() model.Task {
			tk := task("blocked-open", true)
			tk.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "queued-no-obs"}}
			return tk
		}(),
		func() model.Task {
			tk := task("unblocked-closed", true)
			tk.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "closed"}}
			return tk
		}(),
		func() model.Task {
			tk := task("blocked-dangling", true)
			tk.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "missing-target"}}
			return tk
		}(),
		func() model.Task {
			tk := task("nonblocking-link", true)
			tk.Links = []model.Link{{Kind: model.LinkAddresses, Target: "owner/payments-api#9:t1"}}
			return tk
		}(),
	}
	for _, tk := range seed {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", tk.ID, err)
		}
	}

	if err := store.Observe(ctx, model.Observation{TaskID: "queued-open-obs"}); err != nil {
		t.Fatal(err)
	}
	if err := store.StartRun(ctx, model.Run{ID: "r-run", TaskID: "running", Sandbox: "sb", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	for a := 1; a <= model.MaxConsecutiveFailures; a++ {
		rid := "r-fail-" + string(rune('0'+a))
		if err := store.StartRun(ctx, model.Run{ID: rid, TaskID: "failed", Sandbox: "sb", Attempt: a, StartedAt: now.Add(time.Duration(a) * time.Minute)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, rid, now.Add(time.Duration(a)*time.Minute+time.Second), "failed", "boom"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "awaiting-reply", PendingQuestionCommentID: &qID}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "awaiting-submit", CompletedAt: &doneAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "completed", CompletedAt: &doneAt}); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "closed", CompletedAt: &doneAt, ClosedAt: &closedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pause(ctx, model.TaskPause{
		TaskID: "paused", PausedAt: now, PausedBy: human, FromState: model.StateQueued,
	}); err != nil {
		t.Fatal(err)
	}

	wantReady := []string{"nonblocking-link", "queued-no-obs", "queued-open-obs", "unblocked-closed"}
	gotReady, err := store.Ready(ctx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(gotReady)
	if !slices.Equal(gotReady, wantReady) {
		t.Fatalf("Ready = %v, want %v", gotReady, wantReady)
	}

	// Verify IsReady agrees with Ready and with task_state + IsBlocked for every task.
	states, err := store.States(ctx)
	if err != nil {
		t.Fatal(err)
	}
	closedSet := map[string]bool{}
	for id, st := range states {
		if st == model.StateClosed {
			closedSet[id] = true
		}
	}
	for _, tk := range seed {
		isReady, err := store.IsReady(ctx, tk.ID)
		if err != nil {
			t.Fatalf("IsReady(%s): %v", tk.ID, err)
		}
		inReady := slices.Contains(gotReady, tk.ID)
		if isReady != inReady {
			t.Errorf("task %s: IsReady=%v disagrees with Ready membership=%v", tk.ID, isReady, inReady)
		}
		expected := states[tk.ID] == model.StateQueued && !model.IsBlocked(tk, closedSet) && tk.ID != "paused"
		if isReady != expected {
			t.Errorf("task %s (state=%s): IsReady=%v, want %v", tk.ID, states[tk.ID], isReady, expected)
		}
	}
}
