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

import (
	"context"
	"database/sql"
	"fmt"
)

// daemonLeaseKey is the session-level advisory lock a running daemon
// holds on its store for as long as it runs (HoldDaemonLease), keyed
// with the schema the way writerLockKey is. It is how a copy, a move or
// a migration of a PostgreSQL store tells that a live writer is still
// attached: a copy taken while one is (2026-11-04, the move of a store
// into its container) loses every write made after the copy point.
const daemonLeaseKey = 0x67726164 // "grad"

const leaseLockSQL = "SELECT pg_try_advisory_lock(%d, hashtext(current_schema()))"

// HoldDaemonLease takes the daemon's lease on db's store on a connection
// of its own and keeps it until release is called or the connection
// drops. held is false, with no error, when another session already
// holds it.
func HoldDaemonLease(ctx context.Context, db *sql.DB) (release func(), held bool, err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: opening a connection for the daemon's lease: %w", err)
	}
	var got bool
	if err := conn.QueryRowContext(ctx, fmt.Sprintf(leaseLockSQL, daemonLeaseKey)).Scan(&got); err != nil {
		conn.Close()
		return nil, false, fmt.Errorf("postgres: taking the daemon's lease: %w", err)
	}
	if !got {
		conn.Close()
		return nil, false, nil
	}
	return func() {
		_, _ = conn.ExecContext(context.Background(), fmt.Sprintf("SELECT pg_advisory_unlock(%d, hashtext(current_schema()))", daemonLeaseKey))
		conn.Close()
	}, true, nil
}

// DaemonLeaseHeld reports whether a daemon holds its lease on db's
// store right now. It asks by trying to take the lease itself, and
// gives it straight back when it gets it.
func DaemonLeaseHeld(ctx context.Context, db *sql.DB) (bool, error) {
	release, got, err := HoldDaemonLease(ctx, db)
	if err != nil {
		return false, err
	}
	if got {
		release()
		return false, nil
	}
	return true, nil
}
