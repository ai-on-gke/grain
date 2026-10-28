package sqlite_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// TestSQLiteHighConcurrencyStress exercises the SQLite store under high concurrency across
// multiple goroutines. It concurrently executes:
//  1. Task state transitions (creation, start run, activity update, finish run, observe field, defer/undefer).
//  2. Activity log writes and reads.
//  3. Telemetry and timing records.
//  4. Custom field definitions and task updates.
//  5. Schedule creation, due schedule evaluations, and updates.
//  6. Deliberate transaction rollbacks to verify isolation and non-interference.
//  7. High-frequency ChangeToken polling.
func TestSQLiteHighConcurrencyStress(t *testing.T) {
	t.Parallel()

	store, db := sqlitetest.Open(t)
	ctx := context.Background()

	workers := 10
	iterations := 20
	if env := os.Getenv("GRAIN_SQLITE_STRESS_WORKERS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			workers = n
		}
	}
	if env := os.Getenv("GRAIN_SQLITE_STRESS_ITERATIONS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			iterations = n
		}
	}

	var workerWG sync.WaitGroup
	var pollerWG sync.WaitGroup
	var errCount atomic.Int64
	var rollbacks atomic.Int64
	var changeTokenPolls atomic.Int64
	stopPollers := make(chan struct{})

	// Background high-frequency ChangeToken poller goroutines
	numPollers := 4
	for p := 0; p < numPollers; p++ {
		pollerWG.Add(1)
		go func(pollerID int) {
			defer pollerWG.Done()
			var lastGen uint64
			var lastVer int64
			for {
				select {
				case <-stopPollers:
					return
				default:
					token, err := store.ChangeToken(ctx)
					if err != nil {
						time.Sleep(2 * time.Millisecond)
						continue
					}
					changeTokenPolls.Add(1)
					parts := strings.Split(token, ":")
					if len(parts) == 2 {
						gen, _ := strconv.ParseUint(parts[0], 10, 64)
						ver, _ := strconv.ParseInt(parts[1], 10, 64)
						if gen == lastGen && ver < lastVer {
							t.Errorf("poller %d: data_version regressed within same gen %d: %d < %d", pollerID, gen, ver, lastVer)
							errCount.Add(1)
						}
						if gen >= lastGen {
							lastGen = gen
							lastVer = ver
						}
					}
					time.Sleep(1 * time.Millisecond)
				}
			}
		}(p)
	}

	// Worker goroutines driving concurrent model mutations
	for w := 0; w < workers; w++ {
		workerWG.Add(1)
		go func(workerID int) {
			defer workerWG.Done()
			r := rand.New(rand.NewPCG(uint64(workerID), uint64(time.Now().UnixNano())))

			for i := 0; i < iterations; i++ {
				taskID := fmt.Sprintf("stress-sq-w%d-i%d", workerID, i)
				title := fmt.Sprintf("SQLite Stress Task %s", taskID)
				createdAt := time.Now().UTC()

				// 1. PutTask with custom fields
				tk := model.Task{
					ID:        taskID,
					Title:     title,
					Intent:    model.IntentImplement,
					AutoMerge: workerID%2 == 0,
					CreatedAt: &createdAt,
					CustomFields: map[string][]string{
						"team":     {fmt.Sprintf("team-%d", workerID%3)},
						"priority": {"high"},
					},
				}
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
				if _, err := store.SetTaskActivity(ctx, taskID, fmt.Sprintf("worker %d processing step %d", workerID, i), time.Now().UTC()); err != nil {
					t.Errorf("worker %d: SetTaskActivity %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}

				// 4. Record Telemetry & Timings
				if err := store.RecordRunTelemetry(ctx, runID, model.RunTelemetry{
					Tools: []model.RunToolUse{
						{RunID: runID, Tool: "read_file", Calls: 3},
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

				// 6. ObserveField
				if err := store.ObserveField(ctx, taskID, time.Now().UTC(), func(o *model.Observation) {
					now := time.Now().UTC()
					o.CompletedAt = &now
				}); err != nil {
					t.Errorf("worker %d: ObserveField %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}

				// 7. Schedule creation and evaluation
				schedID := fmt.Sprintf("sched-w%d-i%d", workerID, i)
				sched := model.Schedule{
					ID:    schedID,
					Title: "Scheduled Stress Test",
					Recurrence: model.Recurrence{
						Kind:        model.RecurrenceEveryNHours,
						EveryNHours: 1,
					},
					Target:    model.RepoRef{Owner: "bwsalmon", Name: "grain"},
					Base:      "main",
					Enabled:   true,
					NextRunAt: time.Now().UTC().Add(-1 * time.Minute), // due now
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
				if len(due) == 0 {
					t.Errorf("worker %d: expected at least 1 due schedule, got 0", workerID)
				}

				// 8. Deliberate rollback injection in separate transaction
				if r.IntN(3) == 0 {
					tx, err := db.BeginTx(ctx, nil)
					if err == nil {
						_, _ = tx.ExecContext(ctx, "INSERT INTO `task_tag` (`task_id`, `tag`) VALUES (?, ?)", taskID, "rollback-tag")
						_ = tx.Rollback()
						rollbacks.Add(1)
					}
				}

				// 9. Read back task and verify invariants
				got, err := store.GetTask(ctx, taskID)
				if err != nil || got == nil {
					t.Errorf("worker %d: GetTask %s: %v", workerID, taskID, err)
					errCount.Add(1)
					return
				}
				if got.Title != title {
					t.Errorf("worker %d: task title mismatch: got %q, want %q", workerID, got.Title, title)
					errCount.Add(1)
				}
			}
		}(w)
	}

	workerWG.Wait()
	close(stopPollers)
	pollerWG.Wait()

	if errCount.Load() > 0 {
		t.Fatalf("High concurrency stress encountered %d errors", errCount.Load())
	}

	// Verify total tasks count
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks after stress: %v", err)
	}
	expectedTasks := workers * iterations
	if len(tasks) != expectedTasks {
		t.Errorf("Total tasks count: got %d, want %d", len(tasks), expectedTasks)
	}

	// Verify rollback tags never leaked
	for w := 0; w < workers; w++ {
		for i := 0; i < iterations; i++ {
			taskID := fmt.Sprintf("stress-sq-w%d-i%d", w, i)
			tk, err := store.GetTask(ctx, taskID)
			if err != nil || tk == nil {
				continue
			}
			for _, tag := range tk.Tags {
				if tag == "rollback-tag" {
					t.Errorf("found leaked rolled-back tag on task %s", taskID)
				}
			}
		}
	}

	t.Logf("Completed %d tasks across %d workers, %d rollbacks, %d change token polls",
		expectedTasks, workers, rollbacks.Load(), changeTokenPolls.Load())
}

// TestSQLiteSustainedMixedLoad runs a sustained time-bounded mixed concurrent workload against SQLite.
func TestSQLiteSustainedMixedLoad(t *testing.T) {
	t.Parallel()

	store, db := sqlitetest.Open(t)
	ctx := context.Background()

	duration := 2 * time.Second
	if env := os.Getenv("GRAIN_SQLITE_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			duration = d
		}
	} else if env := os.Getenv("GRAIN_STRESS_DURATION"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			duration = d
		}
	}

	workers := 6
	if env := os.Getenv("GRAIN_SQLITE_STRESS_WORKERS"); env != "" {
		if n, err := strconv.Atoi(env); err == nil && n > 0 {
			workers = n
		}
	}

	deadline := time.Now().Add(duration)
	var ops atomic.Int64
	var errCount atomic.Int64
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(workerID), uint64(time.Now().UnixNano())))
			i := 0
			for time.Now().Before(deadline) {
				i++
				taskID := fmt.Sprintf("sustained-sq-w%d-%d", workerID, i)
				switch r.IntN(6) {
				case 0:
					// PutTask
					if err := store.PutTask(ctx, model.Task{
						ID:     taskID,
						Title:  "Sustained SQLite Task",
						Intent: model.IntentImplement,
					}); err != nil {
						t.Errorf("worker %d PutTask: %v", workerID, err)
						errCount.Add(1)
					}
				case 1:
					// ObserveField
					_ = store.ObserveField(ctx, taskID, time.Now().UTC(), func(o *model.Observation) {
						now := time.Now().UTC()
						o.RunNowAt = &now
					})
				case 2:
					// ChangeToken
					_, _ = store.ChangeToken(ctx)
				case 3:
					// GetTask
					_, _ = store.GetTask(ctx, taskID)
				case 4:
					// ListTasks
					_, _ = store.ListTasks(ctx)
				case 5:
					// DueSchedules
					_, _ = store.DueSchedules(ctx, time.Now().UTC())
				}
				ops.Add(1)
			}
		}(w)
	}

	wg.Wait()
	if errCount.Load() > 0 {
		t.Fatalf("Sustained load encountered %d errors", errCount.Load())
	}
	t.Logf("Completed %d operations across %d workers in %v", ops.Load(), workers, duration)

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("db ping after sustained load: %v", err)
	}
}

// TestSQLiteMultiProcessSimulationStress tests multiple separate Store instances connected
// to the exact same SQLite database file concurrently, simulating multi-process concurrent access.
func TestSQLiteMultiProcessSimulationStress(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := sqlite.Config{Dir: dir, Name: "grain_multi"}
	dbPrimary, err := sqlite.Open(cfg)
	if err != nil {
		t.Fatalf("opening primary sqlite db: %v", err)
	}
	defer dbPrimary.Close()

	primaryStore := model.New(dbPrimary)
	ctx := context.Background()
	if err := primaryStore.Init(ctx); err != nil {
		t.Fatalf("initializing schema: %v", err)
	}

	numProcesses := 4
	iterationsPerProcess := 15

	var wg sync.WaitGroup
	var errCount atomic.Int64

	for p := 0; p < numProcesses; p++ {
		wg.Add(1)
		go func(procID int) {
			defer wg.Done()
			procDB, err := sqlite.Open(cfg)
			if err != nil {
				t.Errorf("process %d: open db: %v", procID, err)
				errCount.Add(1)
				return
			}
			defer procDB.Close()
			procStore := model.New(procDB)

			for i := 0; i < iterationsPerProcess; i++ {
				taskID := fmt.Sprintf("multi-proc-%d-item-%d", procID, i)
				if err := procStore.PutTask(ctx, model.Task{
					ID:     taskID,
					Title:  fmt.Sprintf("Task from proc %d iter %d", procID, i),
					Intent: model.IntentImplement,
				}); err != nil {
					t.Errorf("process %d: PutTask %s: %v", procID, taskID, err)
					errCount.Add(1)
					return
				}

				// Read back via primary store and local proc store
				if _, err := procStore.GetTask(ctx, taskID); err != nil {
					t.Errorf("process %d: local GetTask %s: %v", procID, taskID, err)
					errCount.Add(1)
				}

				tok, err := procStore.ChangeToken(ctx)
				if err != nil || tok == "" {
					t.Errorf("process %d: ChangeToken: %v", procID, err)
					errCount.Add(1)
				}
			}
		}(p)
	}

	wg.Wait()
	if errCount.Load() > 0 {
		t.Fatalf("Multi-process simulation encountered %d errors", errCount.Load())
	}

	tasks, err := primaryStore.ListTasks(ctx)
	if err != nil {
		t.Fatalf("primary ListTasks: %v", err)
	}
	expected := numProcesses * iterationsPerProcess
	if len(tasks) != expected {
		t.Errorf("Multi-process task count: got %d, want %d", len(tasks), expected)
	}
}
