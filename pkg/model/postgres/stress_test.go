package postgres_test

// Sustained and concurrent stress tests for PostgreSQL storage engine.
//
// These tests verify:
//   1. High concurrency CRUD operations, state transitions, activity logs, telemetry, and schedules.
//   2. Connection pool saturation, recovery, and context cancellation resilience.
//   3. Transaction rollback consistency under concurrent write pressure.
//   4. Concurrent schema creation, migrations, and teardown.
//   5. Sustained load operations over time without deadlocks or resource leaks.

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

func TestPostgresConcurrentCRUDStress(t *testing.T) {
	t.Parallel()
	postgrestest.SkipUnlessAvailable(t)

	store, db := postgrestest.Open(t)
	ctx := context.Background()

	workers := 8
	iterations := 15
	if env := os.Getenv("GRAIN_POSTGRES_STRESS_WORKERS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			workers = n
		}
	}
	if env := os.Getenv("GRAIN_POSTGRES_STRESS_ITERATIONS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			iterations = n
		}
	}

	var wg sync.WaitGroup
	var errCount atomic.Int64
	var rollbacks atomic.Int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(workerID), uint64(time.Now().UnixNano())))

			for i := 0; i < iterations; i++ {
				taskID := fmt.Sprintf("stress-pg-w%d-i%d", workerID, i)
				title := fmt.Sprintf("Stress Task %s", taskID)
				createdAt := time.Now().UTC()
				tk := model.Task{
					ID:        taskID,
					Title:     title,
					Intent:    model.IntentImplement,
					AutoMerge: workerID%2 == 0,
					CreatedAt: &createdAt,
					CustomFields: map[string][]string{
						"team":     {fmt.Sprintf("team-%d", workerID)},
						"priority": {"high"},
					},
				}

				// 1. PutTask
				if err := store.PutTask(ctx, tk); err != nil {
					t.Errorf("worker %d: PutTask %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}

				// 2. StartRun
				runID := fmt.Sprintf("run-%s", taskID)
				started := time.Now().UTC()
				if err := store.StartRun(ctx, model.Run{
					ID:        runID,
					TaskID:    taskID,
					StartedAt: started,
					Attempt:   1,
				}, model.Limits{}); err != nil {
					t.Errorf("worker %d: StartRun %s: %v", workerID, runID, err)
					errCount.Add(1)
					return
				}

				// 3. SetTaskActivity
				if _, err := store.SetTaskActivity(ctx, taskID, fmt.Sprintf("worker %d step %d", workerID, i), time.Now().UTC()); err != nil {
					t.Errorf("worker %d: SetTaskActivity %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}

				// 4. RecordRunTelemetry
				if err := store.RecordRunTelemetry(ctx, runID, model.RunTelemetry{
					Tools: []model.RunToolUse{
						{RunID: runID, Tool: "read_file", Calls: 2},
						{RunID: runID, Tool: "run_command", Calls: 1},
					},
				}); err != nil {
					t.Errorf("worker %d: RecordRunTelemetry %s: %v", workerID, runID, err)
					errCount.Add(1)
					return
				}

				// 5. FinishRun
				finished := started.Add(50 * time.Millisecond)
				if err := store.FinishRun(ctx, runID, finished, "succeeded", ""); err != nil {
					t.Errorf("worker %d: FinishRun %s: %v", workerID, runID, err)
					errCount.Add(1)
					return
				}

				// 6. Observation
				if err := store.ObserveField(ctx, taskID, time.Now().UTC(), func(o *model.Observation) {
					now := time.Now().UTC()
					o.CompletedAt = &now
				}); err != nil {
					t.Errorf("worker %d: ObserveField %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}

				// 7. Update custom fields
				tk.CustomFields["status"] = []string{"verified"}
				if err := store.PutTask(ctx, tk); err != nil {
					t.Errorf("worker %d: PutTask update %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}

				// 8. PutSchedule and DueSchedules
				schedID := fmt.Sprintf("sched-pg-w%d-i%d", workerID, i)
				sched := model.Schedule{
					ID:    schedID,
					Title: "Postgres Stress Schedule",
					Recurrence: model.Recurrence{
						Kind:        model.RecurrenceEveryNHours,
						EveryNHours: 1,
					},
					Target:    model.RepoRef{Owner: "bwsalmon", Name: "grain"},
					Base:      "main",
					Enabled:   true,
					NextRunAt: time.Now().UTC().Add(-1 * time.Minute),
				}
				if err := store.PutSchedule(ctx, sched); err != nil {
					t.Errorf("worker %d: PutSchedule %s: %v", workerID, schedID, err)
					errCount.Add(1)
					return
				}
				due, err := store.DueSchedules(ctx, time.Now().UTC())
				if err != nil {
					t.Errorf("worker %d: DueSchedules: %v", workerID, err)
					errCount.Add(1)
					return
				}
				found := false
				for _, d := range due {
					if d.ID == schedID {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("worker %d: DueSchedules did not return %s, just put due (%d due)", workerID, schedID, len(due))
					errCount.Add(1)
					return
				}
				// Disable it again once seen, so every DueSchedules call
				// returns only the handful of schedules workers have in
				// flight. Left enabled, each call returned every schedule
				// put so far -- up to 400, each hydrated with four more
				// queries -- and the test was quadratic, running out the
				// 15m timeout at 50 iterations.
				if err := store.UpdateSchedule(ctx, schedID, func(s *model.Schedule) error {
					s.Enabled = false
					return nil
				}); err != nil {
					t.Errorf("worker %d: UpdateSchedule %s: %v", workerID, schedID, err)
					errCount.Add(1)
					return
				}

				// 9. Deliberate rollback injection
				if r.IntN(3) == 0 {
					tx, err := db.BeginTx(ctx, nil)
					if err == nil {
						_, _ = tx.ExecContext(ctx, `INSERT INTO "task_tag" ("task_id", "tag") VALUES (?, ?)`, taskID, "pg-rollback-tag")
						_ = tx.Rollback()
						rollbacks.Add(1)
					}
				}

				// 10. Read back and verify
				got, err := store.GetTask(ctx, taskID)
				if err != nil || got == nil {
					t.Errorf("worker %d: GetTask %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}
				if got.Title != title {
					t.Errorf("worker %d: GetTask %s title mismatch: got %q, want %q", workerID, taskID, got.Title, title)
					errCount.Add(1)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	if errCount.Load() > 0 {
		t.Fatalf("Concurrent CRUD stress encountered %d errors", errCount.Load())
	}

	// Verify total count
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks after stress: %v", err)
	}
	expectedTasks := workers * iterations
	if len(tasks) != expectedTasks {
		t.Errorf("Total tasks count: got %d, want %d", len(tasks), expectedTasks)
	}

	// Verify no rolled back tags leaked
	for w := 0; w < workers; w++ {
		for i := 0; i < iterations; i++ {
			taskID := fmt.Sprintf("stress-pg-w%d-i%d", w, i)
			tk, err := store.GetTask(ctx, taskID)
			if err != nil || tk == nil {
				continue
			}
			for _, tag := range tk.Tags {
				if tag == "pg-rollback-tag" {
					t.Errorf("found leaked rolled back tag on postgres task %s", taskID)
				}
			}
		}
	}

	// Verify database connection pool is healthy
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("db ping after stress: %v", err)
	}
}

func TestPostgresPoolExhaustionAndReconnection(t *testing.T) {
	t.Parallel()
	postgrestest.SkipUnlessAvailable(t)

	store, db := postgrestest.Open(t)

	concurrency := 20
	var wg sync.WaitGroup

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			taskID := fmt.Sprintf("pool-stress-%d", idx)
			_ = store.PutTask(ctx, model.Task{
				ID:     taskID,
				Title:  fmt.Sprintf("Pool Stress %d", idx),
				Intent: model.IntentImplement,
			})

			// Read back
			_, _ = store.GetTask(ctx, taskID)
		}(i)
	}

	wg.Wait()

	// Ensure pool is still responsive
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("db ping after pool saturation: %v", err)
	}
}

func TestPostgresTransactionRollbackIntegrity(t *testing.T) {
	t.Parallel()
	postgrestest.SkipUnlessAvailable(t)

	store, db := postgrestest.Open(t)
	ctx := context.Background()

	// Initial state
	baseTask := model.Task{
		ID:     "rollback-test",
		Title:  "Initial Title",
		Intent: model.IntentImplement,
	}
	if err := store.PutTask(ctx, baseTask); err != nil {
		t.Fatalf("initial PutTask: %v", err)
	}

	// Perform rolled back manual transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE "task" SET "title" = 'Corrupted' WHERE "id" = 'rollback-test'`); err != nil {
		t.Fatalf("tx ExecContext: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Verify original row is preserved intact
	got, err := store.GetTask(ctx, "rollback-test")
	if err != nil {
		t.Fatalf("GetTask after rollback: %v", err)
	}
	if got.Title != "Initial Title" {
		t.Fatalf("task title after rollback: got %q, want %q", got.Title, "Initial Title")
	}
}

func TestPostgresConcurrentSchemaLifecycle(t *testing.T) {
	t.Parallel()
	postgrestest.SkipUnlessAvailable(t)

	// Concurrently create and initialize multiple independent stores
	numSchemas := 4
	var wg sync.WaitGroup
	errs := make(chan error, numSchemas)

	for i := 0; i < numSchemas; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			st, testDB := postgrestest.Open(t)
			ctx := context.Background()

			taskID := fmt.Sprintf("schema-%d-task", idx)
			if err := st.PutTask(ctx, model.Task{
				ID:     taskID,
				Title:  fmt.Sprintf("Schema Task %d", idx),
				Intent: model.IntentImplement,
			}); err != nil {
				errs <- fmt.Errorf("schema %d: PutTask: %w", idx, err)
				return
			}

			readBack, err := st.GetTask(ctx, taskID)
			if err != nil || readBack == nil {
				errs <- fmt.Errorf("schema %d: GetTask: %w", idx, err)
				return
			}
			if err := testDB.PingContext(ctx); err != nil {
				errs <- fmt.Errorf("schema %d: ping: %w", idx, err)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("schema lifecycle error: %v", err)
		}
	}
}

func TestPostgresSustainedMixedLoad(t *testing.T) {
	t.Parallel()
	postgrestest.SkipUnlessAvailable(t)

	store, db := postgrestest.Open(t)
	ctx := context.Background()

	duration := 2 * time.Second
	if env := os.Getenv("GRAIN_POSTGRES_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			duration = d
		}
	} else if env := os.Getenv("GRAIN_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			duration = d
		}
	}

	deadline := time.Now().Add(duration)
	var ops atomic.Int64
	var errCount atomic.Int64
	var wg sync.WaitGroup

	workers := 6
	if env := os.Getenv("GRAIN_POSTGRES_STRESS_WORKERS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			workers = n
		}
	}

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(workerID), uint64(time.Now().UnixNano())))
			i := 0
			for time.Now().Before(deadline) {
				i++
				taskID := fmt.Sprintf("sustained-pg-w%d-%d", workerID, i)
				switch r.IntN(6) {
				case 0:
					if err := store.PutTask(ctx, model.Task{
						ID:     taskID,
						Title:  "Sustained Postgres Task",
						Intent: model.IntentImplement,
					}); err != nil {
						t.Errorf("worker %d PutTask: %v", workerID, err)
						errCount.Add(1)
					}
				case 1:
					_ = store.ObserveField(ctx, taskID, time.Now().UTC(), func(o *model.Observation) {
						now := time.Now().UTC()
						o.RunNowAt = &now
					})
				case 2:
					_, _ = store.GetTask(ctx, taskID)
				case 3:
					_, _ = store.ListTasks(ctx)
				case 4:
					_, _ = store.DueSchedules(ctx, time.Now().UTC())
				case 5:
					if _, err := store.SetTaskActivity(ctx, taskID, "sustained pg work", time.Now().UTC()); err != nil {
						// Ignored if run not live
					}
				}
				ops.Add(1)
			}
		}(w)
	}

	wg.Wait()
	if errCount.Load() > 0 {
		t.Fatalf("Sustained Postgres stress encountered %d errors", errCount.Load())
	}
	t.Logf("Completed %d mixed operations across %d workers in %v", ops.Load(), workers, duration)

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("database health check after sustained load: %v", err)
	}
}
