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

package model

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteCoordinatorDistinctKeysRunConcurrently(t *testing.T) {
	t.Parallel()
	var coord writeCoordinator
	ctx := context.Background()

	const n = 4
	entered := make(chan struct{}, n)
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		key := string(rune('a' + i))
		go func() {
			defer wg.Done()
			rel, err := coord.lockTask(ctx, key)
			if err != nil {
				t.Errorf("lockTask(%s): %v", key, err)
				return
			}
			defer rel()
			entered <- struct{}{}
			<-proceed
		}()
	}

	// All n distinct keys must enter simultaneously before proceed is closed.
	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-entered:
		case <-deadline:
			t.Fatalf("only %d/%d distinct keys entered concurrently", i, n)
		}
	}
	close(proceed)
	wg.Wait()

	coord.mu.Lock()
	remaining := len(coord.keys)
	coord.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("leaked %d key lock slots after release", remaining)
	}
}

func TestWriteCoordinatorSerialisesSameKeyAndHonorsExclusive(t *testing.T) {
	t.Parallel()
	var coord writeCoordinator
	ctx := context.Background()

	// Same key serialises without overlap.
	var active atomic.Int32
	var maxActive atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := coord.lockTask(ctx, "same-task")
			if err != nil {
				t.Errorf("lockTask: %v", err)
				return
			}
			cur := active.Add(1)
			for {
				prev := maxActive.Load()
				if cur <= prev || maxActive.CompareAndSwap(prev, cur) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			active.Add(-1)
			rel()
		}()
	}
	wg.Wait()
	if maxActive.Load() != 1 {
		t.Fatalf("same-key max concurrency = %d, want 1", maxActive.Load())
	}

	// Exclusive lock blocks task locks and honors context cancellation.
	relExcl, err := coord.lockExclusive(ctx)
	if err != nil {
		t.Fatalf("lockExclusive: %v", err)
	}
	cancelCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := coord.lockTask(cancelCtx, "t1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lockTask while exclusive held = %v, want DeadlineExceeded", err)
	}
	relExcl()

	// Task lock blocks exclusive lock and exclusive honors context cancellation.
	relTask, err := coord.lockTask(ctx, "t1")
	if err != nil {
		t.Fatalf("lockTask after exclusive release: %v", err)
	}
	cancelCtx2, cancel2 := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel2()
	if _, err := coord.lockExclusive(cancelCtx2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lockExclusive while task held = %v, want DeadlineExceeded", err)
	}
	relTask()

	// Clean map after cancelled waits too.
	coord.mu.Lock()
	remaining := len(coord.keys)
	waiting := coord.waitingExcl
	coord.mu.Unlock()
	if remaining != 0 || waiting != 0 {
		t.Fatalf("after cleanup: keys=%d waitingExcl=%d, want 0/0", remaining, waiting)
	}
}

type retryTestConnector struct {
	failFirst int
	failErr   error
	attempts  atomic.Int32
}

func (c *retryTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &retryTestConn{c: c}, nil
}
func (c *retryTestConnector) Driver() driver.Driver { return nil }

type retryTestConn struct{ c *retryTestConnector }

func (rc *retryTestConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (rc *retryTestConn) Close() error                        { return nil }
func (rc *retryTestConn) Begin() (driver.Tx, error) {
	return rc.BeginTx(context.Background(), driver.TxOptions{})
}
func (rc *retryTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if int(rc.c.attempts.Add(1)) <= rc.c.failFirst {
		return nil, rc.c.failErr
	}
	return retryTestTx{}, nil
}

type retryTestTx struct{}

func (retryTestTx) Commit() error   { return nil }
func (retryTestTx) Rollback() error { return nil }

func TestStoreWriteRetriesAndConflicts(t *testing.T) {
	t.Parallel()
	var nilStore *Store
	if nilStore.WriteRetries() != 0 || nilStore.WriteConflicts() != 0 {
		t.Fatalf("nil Store counters must be 0")
	}

	// Transient serialization failures increment WriteRetries and succeed on retry.
	dbOK := sql.OpenDB(&retryTestConnector{
		failFirst: 2,
		failErr:   errors.New("ERROR: canceling statement due to lock timeout (SQLSTATE 55P03)"),
	})
	defer dbOK.Close()
	sOK := New(dbOK)
	if err := sOK.write(context.Background(), "retry test", func(*sql.Tx) error { return nil }); err != nil {
		t.Fatalf("write with transient 55P03 failed: %v", err)
	}
	if gotR, gotC := sOK.WriteRetries(), sOK.WriteConflicts(); gotR != 2 || gotC != 0 {
		t.Fatalf("after 2 transient retries: retries=%d conflicts=%d, want 2/0", gotR, gotC)
	}

	// Exhausting maxWriteAttempts increments WriteConflicts and returns ErrConflict.
	dbFail := sql.OpenDB(&retryTestConnector{
		failFirst: maxWriteAttempts + 1,
		failErr:   errors.New("ERROR: could not serialize access (SQLSTATE 40001)"),
	})
	defer dbFail.Close()
	sFail := New(dbFail)
	err := sFail.write(context.Background(), "conflict test", func(*sql.Tx) error { return nil })
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("exhausted retries err = %v, want ErrConflict", err)
	}
	if gotR, gotC := sFail.WriteRetries(), sFail.WriteConflicts(); gotR != maxWriteAttempts-1 || gotC != 1 {
		t.Fatalf("after exhausted retries: retries=%d conflicts=%d, want %d/1", gotR, gotC, maxWriteAttempts-1)
	}
}
