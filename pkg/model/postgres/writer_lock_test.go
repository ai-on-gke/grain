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
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// ObserveField reads a task's observation and writes it back whole. Two of
// them at once must both land: on Postgres' READ COMMITTED, without the
// adapter's writer lock, both read the same row and the later commit
// drops the earlier one's change -- which is how a task's close was lost
// to the orchestrator's next observation in the installer e2e.
func TestConcurrentObserveFieldsLoseNoWrite(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatalf("put: %v", err)
	}
	const writers = 24
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.ObserveField(ctx, "a1b2", time.Now(), func(o *model.Observation) {
				o.MergeQueueRearms++
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ObserveField: %v", err)
		}
	}
	obs, err := store.GetObservation(ctx, "a1b2")
	if err != nil || obs == nil {
		t.Fatalf("reading the observation back: %v (nil=%v)", err, obs == nil)
	}
	if obs.MergeQueueRearms != writers {
		t.Errorf("%d concurrent increments left the counter at %d: the rest were lost", writers, obs.MergeQueueRearms)
	}
	if store.WriteRetries() != 0 || store.WriteConflicts() != 0 {
		t.Errorf("in-process per-task serialisation should need zero retries/conflicts, got %d/%d",
			store.WriteRetries(), store.WriteConflicts())
	}
	if stats := db.Stats(); stats.WaitCount != 0 {
		t.Errorf("24 same-task writers pinned pool connections: WaitCount=%d WaitDuration=%v",
			stats.WaitCount, stats.WaitDuration)
	}
}

// TestTaskScopedWritesBlockBehindExclusiveSchemaLock verifies the two-level
// lock hierarchy across connections: a direct db.BeginTx(ctx, nil) holds the
// exclusive schema lock, which must block a task-scoped Store write until it
// releases.
func TestTaskScopedWritesBlockBehindExclusiveSchemaLock(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)

	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx blocker: %v", err)
	}
	defer blocker.Rollback()

	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = store.PutTask(shortCtx, task("blocked-task", true))
	if err == nil {
		t.Fatal("PutTask succeeded while exclusive schema writer lock was held")
	}
	if err := blocker.Rollback(); err != nil {
		t.Fatalf("rollback blocker: %v", err)
	}
	if err := store.PutTask(ctx, task("blocked-task", true)); err != nil {
		t.Fatalf("PutTask after blocker release: %v", err)
	}
}

// TestConcurrentTaskWritesPreserveIntegrationEventOrder runs concurrent writes
// across distinct tasks while a poller tails ListIntegrationEvents by cursor,
// verifying that out-of-order commits never cause an event ID to be skipped.
func TestConcurrentTaskWritesPreserveIntegrationEventOrder(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	const workers = 6
	const perWorker = 15
	pollCtx, stopPoll := context.WithCancel(ctx)
	var seenMu sync.Mutex
	seen := map[int64]bool{}
	var pollErr error
	pollDone := make(chan struct{})
	go func() {
		defer close(pollDone)
		var cursor int64
		for {
			events, err := store.ListIntegrationEvents(ctx, model.IntegrationEventFilter{
				AfterID: cursor,
				Limit:   500,
			})
			if err != nil {
				pollErr = err
				return
			}
			seenMu.Lock()
			for _, ev := range events {
				seen[ev.ID] = true
				if ev.ID > cursor {
					cursor = ev.ID
				}
			}
			seenMu.Unlock()
			if pollCtx.Err() != nil && len(events) == 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	var wg sync.WaitGroup
	var writeErr atomic.Pointer[error]
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				id := fmt.Sprintf("ev-w%d-i%d", w, i)
				if err := store.PutTask(ctx, task(id, true)); err != nil {
					writeErr.CompareAndSwap(nil, &err)
					return
				}
				if _, err := store.AddComment(ctx, model.Comment{
					TaskID: id, Author: model.Attribution{Actor: human}, Body: "note", CreatedAt: now,
				}); err != nil {
					writeErr.CompareAndSwap(nil, &err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	stopPoll()
	<-pollDone

	if ep := writeErr.Load(); ep != nil {
		t.Fatalf("concurrent write error: %v", *ep)
	}
	if pollErr != nil {
		t.Fatalf("poll error: %v", pollErr)
	}
	all, err := store.ListIntegrationEvents(ctx, model.IntegrationEventFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("ListIntegrationEvents(all): %v", err)
	}
	if len(all) != workers*perWorker*2 {
		t.Fatalf("total events = %d, want %d", len(all), workers*perWorker*2)
	}
	for _, ev := range all {
		if !seen[ev.ID] {
			t.Fatalf("cursor poller missed integration_event id=%d (out-of-order commit visibility)", ev.ID)
		}
	}
}

// TestFencedTaskScopedWriteDrainsBeforeTakeover verifies that when daemon A
// has an in-flight task-scoped transaction (holding the shared schema lock)
// and loses its lease, daemon B's AcquireWriterLock waits for A's in-flight
// write to finish before returning, and A's next task-scoped write is fenced.
func TestFencedTaskScopedWriteDrainsBeforeTakeover(t *testing.T) {
	t.Parallel()
	cfg, adminDB := newLockTestSchema(t)
	ctx := context.Background()

	lockA, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout: -1, KeepAliveInterval: time.Hour, ReacquireWindow: -1,
		ApplicationName: "grain-test-task-fence-a", Logf: t.Logf,
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock(A): %v", err)
	}
	defer lockA.Close()

	dbA, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("Open(A): %v", err)
	}
	defer dbA.Close()
	if _, err := dbA.ExecContext(ctx, `CREATE TABLE fence_task_probe (n INTEGER)`); err != nil {
		t.Fatalf("creating probe table: %v", err)
	}
	if err := postgres.FenceWrites(dbA, lockA); err != nil {
		t.Fatalf("FenceWrites(A): %v", err)
	}

	// Start an in-flight task-scoped transaction on A.
	taskCtx := model.WithTaskWriteScope(ctx, "t-in-flight")
	txInFlight, err := dbA.BeginTx(taskCtx, nil)
	if err != nil {
		t.Fatalf("BeginTx(task-scoped): %v", err)
	}
	if _, err := txInFlight.ExecContext(taskCtx, `INSERT INTO fence_task_probe (n) VALUES ($1)`, 1); err != nil {
		_ = txInFlight.Rollback()
		t.Fatalf("insert in-flight: %v", err)
	}

	// Terminate A's lease session and start B acquiring the lease concurrently.
	if _, err := adminDB.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, int64(lockA.BackendPID())); err != nil {
		_ = txInFlight.Rollback()
		t.Fatalf("terminating A's lock session: %v", err)
	}
	bAcquired := make(chan *postgres.WriterLock, 1)
	bErr := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			lb, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
				WaitTimeout: -1, KeepAliveInterval: -1, ApplicationName: "grain-test-task-fence-b", Logf: t.Logf,
			})
			if err == nil {
				bAcquired <- lb
				return
			}
			if !errors.Is(err, postgres.ErrWriterLockHeld) || time.Now().After(deadline) {
				bErr <- err
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
	}()

	// B must NOT finish acquiring while txInFlight still holds the shared schema lock.
	select {
	case lb := <-bAcquired:
		_ = lb.Close()
		_ = txInFlight.Rollback()
		t.Fatal("AcquireWriterLock(B) returned while A's task-scoped transaction was still in flight")
	case err := <-bErr:
		_ = txInFlight.Rollback()
		t.Fatalf("AcquireWriterLock(B): %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	// Once A's in-flight write commits, B completes acquisition and sees A's write.
	if err := txInFlight.Commit(); err != nil {
		t.Fatalf("commit in-flight: %v", err)
	}
	var lockB *postgres.WriterLock
	select {
	case lockB = <-bAcquired:
		defer lockB.Close()
	case err := <-bErr:
		t.Fatalf("AcquireWriterLock(B) after commit: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("AcquireWriterLock(B) timed out after A's in-flight tx committed")
	}

	// Any subsequent task-scoped write on A must be refused with ErrWriteFenced.
	if _, err := dbA.BeginTx(taskCtx, nil); !errors.Is(err, postgres.ErrWriteFenced) {
		t.Fatalf("A's task-scoped BeginTx after takeover = %v, want ErrWriteFenced", err)
	}
}
