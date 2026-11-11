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
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// TestFenceWritesRefusesWritesOnceTheLeaseSessionIsGone is the
// 2026-11-07 walkthrough's split brain (docs/test-runs/
// 2026-11-07-instance-runtime-stores.md, item 2): daemon A's lock session
// is terminated, daemon B takes the lease at once, and A -- whose
// keep-alive has not ticked yet -- must not commit another write.
// Before FenceWrites, A kept committing for up to a keep-alive interval
// (43 writes in the walkthrough).
func TestFenceWritesRefusesWritesOnceTheLeaseSessionIsGone(t *testing.T) {
	t.Parallel()
	cfg, adminDB := newLockTestSchema(t)
	ctx := context.Background()

	lockA, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout: -1,
		// Long enough that the keep-alive never notices within the test:
		// the fence alone has to refuse.
		KeepAliveInterval: time.Hour,
		ReacquireWindow:   -1,
		ApplicationName:   "grain-test-fence-a",
		Logf:              t.Logf,
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
	if _, err := dbA.ExecContext(ctx, `CREATE TABLE fence_probe (n INTEGER)`); err != nil {
		t.Fatalf("creating probe table: %v", err)
	}
	if err := postgres.FenceWrites(dbA, lockA); err != nil {
		t.Fatalf("FenceWrites: %v", err)
	}

	insert := func(db *sql.DB, n int) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO fence_probe (n) VALUES ($1)`, n); err != nil {
			_ = tx.Rollback()
			return err
		}
		return tx.Commit()
	}

	if err := insert(dbA, 1); err != nil {
		t.Fatalf("write while holding the lease: %v", err)
	}
	// Read-only transactions are never fenced.
	if tx, err := dbA.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}); err != nil {
		t.Fatalf("read-only BeginTx: %v", err)
	} else {
		_ = tx.Rollback()
	}

	// The lease's session dies (a network partition, a DBA's
	// pg_terminate_backend) and daemon B takes the lease straight away.
	if _, err := adminDB.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, int64(lockA.BackendPID())); err != nil {
		t.Fatalf("terminating A's lock session: %v", err)
	}
	var lockB *postgres.WriterLock
	deadline := time.Now().Add(10 * time.Second)
	for {
		lockB, err = postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
			WaitTimeout: -1, KeepAliveInterval: -1, ApplicationName: "grain-test-fence-b", Logf: t.Logf,
		})
		if err == nil {
			break
		}
		if !errors.Is(err, postgres.ErrWriterLockHeld) || time.Now().After(deadline) {
			t.Fatalf("AcquireWriterLock(B): %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer lockB.Close()

	if err := insert(dbA, 2); !errors.Is(err, postgres.ErrWriteFenced) {
		t.Fatalf("A's write after B took the lease = %v, want ErrWriteFenced", err)
	}
	var count int
	if err := dbA.QueryRowContext(ctx, `SELECT count(*) FROM fence_probe`).Scan(&count); err != nil {
		t.Fatalf("counting probe rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("probe rows = %d, want 1: a write committed after the lease was lost", count)
	}

	// Unfenced, the same pool writes again: the fence is the only thing
	// refusing.
	if err := postgres.FenceWrites(dbA, nil); err != nil {
		t.Fatalf("FenceWrites(nil): %v", err)
	}
	if err := insert(dbA, 3); err != nil {
		t.Fatalf("write on an unfenced pool: %v", err)
	}
}

func TestFenceWritesNeedsAPool(t *testing.T) {
	t.Parallel()
	if err := postgres.FenceWrites(nil, nil); err == nil {
		t.Fatal("FenceWrites(nil, nil) = nil, want an error")
	}
}
