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
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func populate20kTasksAndObservations(tb testing.TB, totalTasks int) (*model.Store, *sql.DB, context.Context) {
	tb.Helper()
	store, db, ctx := openStore(tb)

	now := time.Date(2026, 9, 28, 7, 0, 0, 0, time.UTC)
	batchSize := 2000

	for start := 0; start < totalTasks; start += batchSize {
		end := start + batchSize
		if end > totalTasks {
			end = totalTasks
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			tb.Fatalf("begin tx: %v", err)
		}

		taskStmt, err := tx.Prepare(`INSERT INTO task (
			id, intent, title, body, origin_actor_kind, origin_actor_id, origin_reason,
			binding, auto_merge, order_key, created_at, approval_actor_kind, approval_actor_id, approved_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			tx.Rollback()
			tb.Fatalf("prepare task stmt: %v", err)
		}

		obsStmt, err := tx.Prepare(`INSERT INTO task_observation (
			task_id, observed_at, completed_at, closed_at, streak_count
		) VALUES (?, ?, ?, ?, ?)`)
		if err != nil {
			taskStmt.Close()
			tx.Rollback()
			tb.Fatalf("prepare obs stmt: %v", err)
		}

		for i := start; i < end; i++ {
			taskID := fmt.Sprintf("task-%06d", i)
			title := fmt.Sprintf("Benchmark Task %d", i)
			orderKey := float64(i)
			createdAt := now.Add(time.Duration(i) * time.Second)

			// Assign varied states: queued, running, completed, closed, proposed
			var approvalKind, approvalID *string
			var approvedAt *time.Time
			var completedAt, closedAt *time.Time
			var streakCount int
			autoMerge := 0
			if i%2 == 0 {
				autoMerge = 1
			}

			switch i % 5 {
			case 0: // proposed
				approvalKind, approvalID, approvedAt = nil, nil, nil
			case 1: // queued
				ak, ai := "human", "operator"
				approvalKind, approvalID, approvedAt = &ak, &ai, &createdAt
			case 2: // completed
				ak, ai := "human", "operator"
				approvalKind, approvalID, approvedAt = &ak, &ai, &createdAt
				comp := createdAt.Add(10 * time.Minute)
				completedAt = &comp
			case 3: // closed
				ak, ai := "human", "operator"
				approvalKind, approvalID, approvedAt = &ak, &ai, &createdAt
				comp := createdAt.Add(10 * time.Minute)
				completedAt = &comp
				cls := createdAt.Add(20 * time.Minute)
				closedAt = &cls
			case 4: // failed / streak
				ak, ai := "human", "operator"
				approvalKind, approvalID, approvedAt = &ak, &ai, &createdAt
				streakCount = 3
			}

			if _, err := taskStmt.Exec(
				taskID, "implement", title, "benchmark body",
				"human", "operator", "direct",
				"directive", autoMerge, orderKey, createdAt.Format(time.RFC3339Nano),
				approvalKind, approvalID, approvedAt,
			); err != nil {
				taskStmt.Close()
				obsStmt.Close()
				tx.Rollback()
				tb.Fatalf("insert task %d: %v", i, err)
			}

			obsAt := createdAt.Add(5 * time.Minute)
			var compStr, clsStr *string
			if completedAt != nil {
				s := completedAt.Format(time.RFC3339Nano)
				compStr = &s
			}
			if closedAt != nil {
				s := closedAt.Format(time.RFC3339Nano)
				clsStr = &s
			}

			if _, err := obsStmt.Exec(
				taskID, obsAt.Format(time.RFC3339Nano), compStr, clsStr, streakCount,
			); err != nil {
				taskStmt.Close()
				obsStmt.Close()
				tx.Rollback()
				tb.Fatalf("insert obs %d: %v", i, err)
			}
		}

		taskStmt.Close()
		obsStmt.Close()

		if err := tx.Commit(); err != nil {
			tb.Fatalf("commit batch: %v", err)
		}
	}

	return store, db, ctx
}

func TestCursorVsOffsetPagination20k(t *testing.T) {
	const totalTasks = 20000
	const pageSize = 100
	store, db, ctx := populate20kTasksAndObservations(t, totalTasks)

	// 1. Fetch all via Cursor Pagination (StatesPage)
	var cursorResults []struct {
		id    string
		state model.State
	}
	var cursor string
	cursorPages := 0
	for {
		page, nextCursor, err := store.StatesPage(ctx, pageSize, cursor)
		if err != nil {
			t.Fatalf("StatesPage at cursor %q: %v", cursor, err)
		}
		cursorPages++
		for id, st := range page {
			cursorResults = append(cursorResults, struct {
				id    string
				state model.State
			}{id: id, state: st})
		}
		if nextCursor == "" || len(page) == 0 {
			break
		}
		cursor = nextCursor
	}

	if len(cursorResults) != totalTasks {
		t.Fatalf("cursor pagination got %d tasks, want %d", len(cursorResults), totalTasks)
	}

	// 2. Fetch all via Legacy Offset Pagination
	var offsetResults []struct {
		id    string
		state model.State
	}
	offsetPages := 0
	for offset := 0; offset < totalTasks; offset += pageSize {
		rows, err := db.QueryContext(ctx,
			"SELECT `task_id`, `state` FROM `task_state` ORDER BY `task_id` LIMIT ? OFFSET ?",
			pageSize, offset)
		if err != nil {
			t.Fatalf("offset query at offset %d: %v", offset, err)
		}
		offsetPages++
		count := 0
		for rows.Next() {
			var id, state string
			if err := rows.Scan(&id, &state); err != nil {
				rows.Close()
				t.Fatalf("scan offset row: %v", err)
			}
			count++
			offsetResults = append(offsetResults, struct {
				id    string
				state model.State
			}{id: id, state: model.State(state)})
		}
		rows.Close()
		if count == 0 {
			break
		}
	}

	if len(offsetResults) != totalTasks {
		t.Fatalf("offset pagination got %d tasks, want %d", len(offsetResults), totalTasks)
	}

	t.Logf("Successfully verified parity between Cursor (%d pages) and Offset (%d pages) over %d tasks",
		cursorPages, offsetPages, totalTasks)
}

func BenchmarkCursorVsOffset20kTasks(b *testing.B) {
	const totalTasks = 20000
	const pageSize = 100
	store, db, ctx := populate20kTasksAndObservations(b, totalTasks)

	// Targets for point page queries
	offsets := []int{0, 1000, 5000, 10000, 15000, 19900}

	for _, off := range offsets {
		cursorVal := ""
		if off > 0 {
			cursorVal = fmt.Sprintf("task-%06d", off-1)
		}

		b.Run(fmt.Sprintf("Cursor_Offset_%05d", off), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				page, _, err := store.StatesPage(ctx, pageSize, cursorVal)
				if err != nil {
					b.Fatalf("StatesPage: %v", err)
				}
				if len(page) != pageSize {
					b.Fatalf("expected %d items, got %d", pageSize, len(page))
				}
			}
		})

		b.Run(fmt.Sprintf("LegacyOffset_Offset_%05d", off), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := db.QueryContext(ctx,
					"SELECT `task_id`, `state` FROM `task_state` ORDER BY `task_id` LIMIT ? OFFSET ?",
					pageSize, off)
				if err != nil {
					b.Fatalf("offset query: %v", err)
				}
				count := 0
				for rows.Next() {
					var id, state string
					if err := rows.Scan(&id, &state); err != nil {
						rows.Close()
						b.Fatalf("scan row: %v", err)
					}
					count++
				}
				rows.Close()
				if count != pageSize {
					b.Fatalf("expected %d items, got %d", pageSize, count)
				}
			}
		})
	}

	b.Run("FullWalk_CursorPagination_20k", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var cursor string
			total := 0
			for {
				page, nextCursor, err := store.StatesPage(ctx, pageSize, cursor)
				if err != nil {
					b.Fatalf("StatesPage: %v", err)
				}
				total += len(page)
				if nextCursor == "" || len(page) == 0 {
					break
				}
				cursor = nextCursor
			}
			if total != totalTasks {
				b.Fatalf("total = %d, want %d", total, totalTasks)
			}
		}
	})

	b.Run("FullWalk_OffsetPagination_20k", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			total := 0
			for offset := 0; offset < totalTasks; offset += pageSize {
				rows, err := db.QueryContext(ctx,
					"SELECT `task_id`, `state` FROM `task_state` ORDER BY `task_id` LIMIT ? OFFSET ?",
					pageSize, offset)
				if err != nil {
					b.Fatalf("offset query: %v", err)
				}
				count := 0
				for rows.Next() {
					var id, state string
					if err := rows.Scan(&id, &state); err != nil {
						rows.Close()
						b.Fatalf("scan row: %v", err)
					}
					count++
					total++
				}
				rows.Close()
				if count == 0 {
					break
				}
			}
			if total != totalTasks {
				b.Fatalf("total = %d, want %d", total, totalTasks)
			}
		}
	})
}
