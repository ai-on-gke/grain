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
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

func newLockTestSchema(t *testing.T) (postgres.Config, *sql.DB) {
	t.Helper()
	baseCfg := postgrestest.Config(t)
	adminDB, err := postgres.Open(baseCfg)
	if err != nil {
		t.Fatalf("opening admin postgres connection: %v", err)
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("test_lock_%s", hex.EncodeToString(suffix[:]))
	if _, err := adminDB.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
		_ = adminDB.Close()
		t.Fatalf("creating test schema %q: %v", schema, err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
		_ = adminDB.Close()
	})
	cfg := baseCfg
	cfg.SearchPath = schema
	return cfg, adminDB
}

func TestWriterLockFencesConcurrentDaemonAndReleasesOnClose(t *testing.T) {
	t.Parallel()
	cfg, _ := newLockTestSchema(t)
	ctx := context.Background()

	lock1, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout:       -1,
		KeepAliveInterval: 200 * time.Millisecond,
		ApplicationName:   "grain-test-writer-1",
		Logf:              t.Logf,
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock(1): %v", err)
	}
	defer lock1.Close()

	if lock1.Schema() != cfg.SearchPath {
		t.Errorf("lock1.Schema() = %q, want %q", lock1.Schema(), cfg.SearchPath)
	}
	if lock1.Key() != postgres.AdvisoryLockKey(cfg.SearchPath) {
		t.Errorf("lock1.Key() = %d, want %d", lock1.Key(), postgres.AdvisoryLockKey(cfg.SearchPath))
	}
	if lock1.BackendPID() == 0 {
		t.Fatal("lock1.BackendPID() = 0, want active backend pid")
	}

	// Second writer with immediate fail-closed (-1) must fail with ErrWriterLockHeld
	// and name the holding backend.
	_, err = postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout:     -1,
		ApplicationName: "grain-test-writer-2",
		Logf:            t.Logf,
	})
	if !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("second AcquireWriterLock error = %v, want ErrWriterLockHeld", err)
	}
	if !strings.Contains(err.Error(), "grain-test-writer-1") {
		t.Errorf("second AcquireWriterLock error = %q, want holder application_name included", err.Error())
	}

	// Simulate VM replacement drain: start a second writer waiting up to 5s,
	// release lock1 after 150ms, and confirm the replacement acquires cleanly.
	acquiredCh := make(chan *postgres.WriterLock, 1)
	errCh := make(chan error, 1)
	go func() {
		l2, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
			WaitTimeout:       5 * time.Second,
			PollInterval:      50 * time.Millisecond,
			KeepAliveInterval: -1,
			ApplicationName:   "grain-test-writer-replacement",
			Logf:              t.Logf,
		})
		if err != nil {
			errCh <- err
			return
		}
		acquiredCh <- l2
	}()

	time.Sleep(150 * time.Millisecond)
	if err := lock1.Close(); err != nil {
		t.Fatalf("lock1.Close(): %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("replacement writer failed after drain: %v", err)
	case lock2 := <-acquiredCh:
		defer lock2.Close()
		if lock2.Key() != lock1.Key() {
			t.Errorf("lock2.Key() = %d, want %d", lock2.Key(), lock1.Key())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for replacement writer to acquire lock")
	}
}

func TestWriterLockIsolatesSchemas(t *testing.T) {
	t.Parallel()
	cfgA, _ := newLockTestSchema(t)
	cfgB, _ := newLockTestSchema(t)
	ctx := context.Background()

	lockA, err := postgres.AcquireWriterLock(ctx, cfgA, postgres.WriterLockOptions{
		WaitTimeout:       -1,
		KeepAliveInterval: -1,
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock(schema A): %v", err)
	}
	defer lockA.Close()

	lockB, err := postgres.AcquireWriterLock(ctx, cfgB, postgres.WriterLockOptions{
		WaitTimeout:       -1,
		KeepAliveInterval: -1,
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock(schema B) failed despite isolated schema: %v", err)
	}
	defer lockB.Close()

	if lockA.Key() == lockB.Key() {
		t.Errorf("distinct schemas %q and %q produced identical key %d", cfgA.SearchPath, cfgB.SearchPath, lockA.Key())
	}
}

func TestWriterLockDetectsSplitBrainTakeoverAndNotifiesOnLost(t *testing.T) {
	t.Parallel()
	cfg, adminDB := newLockTestSchema(t)
	ctx := context.Background()

	lostCh := make(chan error, 1)
	// Use a long KeepAliveInterval at first so we can deterministically kill the
	// session, let a replacement acquire the lock, and watch the next tick notice.
	lock1, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout:       -1,
		KeepAliveInterval: 150 * time.Millisecond,
		ReacquireWindow:   2 * time.Second,
		ApplicationName:   "grain-partitioned-old-vm",
		Logf:              t.Logf,
		OnLost: func(err error) {
			select {
			case lostCh <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock(old): %v", err)
	}
	defer lock1.Close()

	// Terminate the old VM's dedicated lock backend and immediately acquire on
	// the new VM before lock1's next keepalive tick re-acquires.
	pid := lock1.BackendPID()
	var terminated bool
	if err := adminDB.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil {
		t.Fatalf("pg_terminate_backend(%d): %v", pid, err)
	}

	lock2, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout:       2 * time.Second,
		PollInterval:      25 * time.Millisecond,
		KeepAliveInterval: -1,
		ApplicationName:   "grain-replacement-new-vm",
		Logf:              t.Logf,
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock(new): %v", err)
	}
	defer lock2.Close()

	select {
	case lostErr := <-lostCh:
		if !errors.Is(lostErr, postgres.ErrWriterLockHeld) {
			t.Errorf("OnLost error = %v, want ErrWriterLockHeld", lostErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for partitioned writer's OnLost callback")
	}
}

func TestWriterLockReacquiresAfterTransientSessionDropWhenUncontested(t *testing.T) {
	t.Parallel()
	cfg, adminDB := newLockTestSchema(t)
	ctx := context.Background()

	lostCh := make(chan error, 1)
	lock, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout:       -1,
		KeepAliveInterval: 100 * time.Millisecond,
		ReacquireWindow:   3 * time.Second,
		ApplicationName:   "grain-transient-drop",
		Logf:              t.Logf,
		OnLost: func(err error) {
			select {
			case lostCh <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock: %v", err)
	}
	defer lock.Close()

	oldPID := lock.BackendPID()
	var terminated bool
	if err := adminDB.QueryRowContext(ctx, "SELECT pg_terminate_backend($1)", oldPID).Scan(&terminated); err != nil {
		t.Fatalf("pg_terminate_backend(%d): %v", oldPID, err)
	}

	// Wait for watch() to reconnect onto a new backend PID and re-acquire the lock.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if pid := lock.BackendPID(); pid != 0 && pid != oldPID {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if pid := lock.BackendPID(); pid == 0 || pid == oldPID {
		t.Fatalf("lock.BackendPID() = %d (old=%d), want reconnected new backend pid", pid, oldPID)
	}

	select {
	case err := <-lostCh:
		t.Fatalf("unexpected OnLost during uncontested transient drop: %v", err)
	default:
	}

	// Confirm the re-acquired lock still fences a second writer.
	_, err = postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout: -1,
		Logf:        t.Logf,
	})
	if !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("contender after re-acquire got err = %v, want ErrWriterLockHeld", err)
	}
}
