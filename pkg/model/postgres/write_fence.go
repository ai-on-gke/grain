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

package postgres

// write_fence.go closes the window between a daemon losing its
// single-writer lease and noticing it.
//
// WriterLock notices a lost session on its keep-alive tick
// (DefaultWriterLockKeepAlive, five seconds). Until then the daemon kept
// committing: in the 2026-11-07 walkthrough
// (docs/test-runs/2026-11-07-instance-runtime-stores.md, item 2) daemon
// B took the lease within half a second of daemon A's lock session being
// terminated, and A still committed a comment after that, while B was
// already the writer. Up to a whole keep-alive interval of such writes
// is possible, which is exactly the split brain the lease exists to
// prevent.
//
// FenceWrites ties a pool's read-write transactions to the lease. Every
// read-write transaction already takes the store's transaction-scoped
// writer lock first (takeWriterLock); with a fence attached it then asks
// PostgreSQL whether the lock's session still holds the lease, and fails
// with ErrWriteFenced, rolling back, when it does not. Because each such
// transaction holds the writer lock from that check to its commit, and
// the new writer's own transactions queue on the same lock, every write
// the old daemon commits is ordered before the first the new one makes.

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
)

// ErrWriteFenced is returned by BeginTx for a read-write transaction on a
// fenced pool once the daemon's single-writer lease is no longer held by
// its lock session.
var ErrWriteFenced = errors.New("postgres: write refused: this daemon no longer holds the single-writer lease on the store")

// leaseHolder is what a fence asks: which backend holds the lease, and
// under which key. WriterLock is the implementation; tests may supply
// another.
type leaseHolder interface {
	BackendPID() uint32
	Key() int64
}

// writeFence is shared by a pool's connector, driver and connections.
type writeFence struct {
	holder atomic.Pointer[leaseHolder]
}

// FenceWrites makes every read-write transaction db begins from now on
// first confirm that lock's session still holds the single-writer lease,
// and fail with ErrWriteFenced when it does not. db must have been opened
// by Open. A nil lock removes the fence.
func FenceWrites(db *sql.DB, lock *WriterLock) error {
	if lock == nil {
		return fenceWrites(db, nil)
	}
	return fenceWrites(db, lock)
}

func fenceWrites(db *sql.DB, holder leaseHolder) error {
	if db == nil {
		return errors.New("postgres: no database to fence")
	}
	d, ok := db.Driver().(*adaptedDriver)
	if !ok || d.fence == nil {
		return fmt.Errorf("postgres: %T was not opened by postgres.Open, so its writes cannot be fenced", db.Driver())
	}
	if holder == nil {
		d.fence.holder.Store(nil)
		return nil
	}
	d.fence.holder.Store(&holder)
	return nil
}

// leaseHeldSQL is true when backend $1 holds the session-level advisory
// lock pg_try_advisory_lock($key) took: a single bigint key is listed in
// pg_locks as its high and low 32 bits, in classid and objid, with
// objsubid 1.
const leaseHeldSQL = `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND granted AND pid = $1 AND classid::bigint = $2 AND objid::bigint = $3 AND objsubid = 1)`

// check is called inside a read-write transaction, after the writer lock.
func (f *writeFence) check(ctx context.Context, conn driver.Conn) error {
	if f == nil {
		return nil
	}
	hp := f.holder.Load()
	if hp == nil {
		return nil
	}
	h := *hp
	pid := h.BackendPID()
	if pid == 0 {
		return fmt.Errorf("%w (its lock session is disconnected)", ErrWriteFenced)
	}
	q, ok := conn.(driver.QueryerContext)
	if !ok {
		return fmt.Errorf("postgres: %T cannot run queries, so it cannot check the single-writer lease", conn)
	}
	key := uint64(h.Key())
	rows, err := q.QueryContext(ctx, leaseHeldSQL, []driver.NamedValue{
		{Ordinal: 1, Value: int64(pid)},
		{Ordinal: 2, Value: int64(key >> 32)},
		{Ordinal: 3, Value: int64(key & 0xffffffff)},
	})
	if err != nil {
		return fmt.Errorf("postgres: checking the single-writer lease: %w", err)
	}
	defer rows.Close()
	dest := make([]driver.Value, 1)
	if err := rows.Next(dest); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("postgres: checking the single-writer lease: no answer")
		}
		return fmt.Errorf("postgres: checking the single-writer lease: %w", err)
	}
	if held, _ := dest[0].(bool); !held {
		return fmt.Errorf("%w (lock session pid %d no longer holds it)", ErrWriteFenced, pid)
	}
	return nil
}
