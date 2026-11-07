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

package daemon

// storelock.go is how a tool tells a live daemon is attached to a data
// directory's store. Copying, migrating or moving a store under a
// running daemon forks it: the daemon keeps writing to the store it
// opened, the copy stops at the moment it was taken, and the next start
// opens the copy (the bsalmon deployment lost a day of tasks this way,
// twice, on 2026-11-04). Migrations are offline only, so every tool that
// copies a store asks here first and refuses while the lock is held.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// StoreLockFile is the file under the data directory a running daemon
// holds an exclusive flock(2) on for as long as it runs. Its contents
// (pid and start time) are only for the message naming the holder; the
// lock itself is what counts, and the kernel drops it with the process.
const StoreLockFile = "daemon.lock"

// StopDaemonCommand is the command every refusal names: the offline
// procedure starts by stopping the daemon (docs/database.md).
const StopDaemonCommand = "sudo systemctl stop grain-daemon.service"

// StoreLock is a held StoreLockFile lock.
type StoreLock struct {
	f *os.File
}

// Release lets the lock go. Safe on nil.
func (l *StoreLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

func openStoreLockFile(dataDir string) (*os.File, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(dataDir, StoreLockFile), os.O_RDWR|os.O_CREATE, 0o644)
}

// AcquireStoreLock takes dataDir's daemon lock, waiting up to wait for a
// daemon that is still on its way out (systemctl restart stops the old
// process before it starts the new one, but the old one can take a
// moment to let go).
func AcquireStoreLock(dataDir string, wait time.Duration) (*StoreLock, error) {
	f, err := openStoreLockFile(dataDir)
	if err != nil {
		return nil, fmt.Errorf("opening the daemon lock in %s: %w", dataDir, err)
	}
	deadline := time.Now().Add(wait)
	logged := false
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("taking the daemon lock %s: %w", f.Name(), err)
		}
		if !time.Now().Before(deadline) {
			holder := readLockHolder(f)
			f.Close()
			return nil, fmt.Errorf("another grain daemon (%s) is running on %s and holds %s; "+
				"two daemons on one store fork it. Stop the other one first (%s)",
				holder, dataDir, filepath.Join(dataDir, StoreLockFile), StopDaemonCommand)
		}
		if !logged {
			log.Printf("grain daemon: waiting up to %s for the daemon holding %s (%s) to exit",
				wait, f.Name(), readLockHolder(f))
			logged = true
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(fmt.Sprintf("pid=%d started=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0)
	}
	return &StoreLock{f: f}, nil
}

func readLockHolder(f *os.File) string {
	buf := make([]byte, 256)
	n, _ := f.ReadAt(buf, 0)
	s := strings.TrimSpace(string(buf[:n]))
	if s == "" {
		return "pid unknown"
	}
	return s
}

// StoreLockHolder reports whether a daemon holds dataDir's lock right
// now, and what it wrote about itself. It never creates the data
// directory; a missing one has no daemon.
func StoreLockHolder(dataDir string) (held bool, holder string, err error) {
	f, err := os.OpenFile(filepath.Join(dataDir, StoreLockFile), os.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false, "", nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return true, readLockHolder(f), nil
	}
	return false, "", fmt.Errorf("checking the daemon lock %s: %w", f.Name(), err)
}

// ErrDaemonAttached is what RefuseWhileDaemonAttached answers when a
// daemon holds the store.
var ErrDaemonAttached = errors.New("a grain daemon is attached to this store")

// RefuseWhileDaemonAttached is the check every offline store tool --
// `grain upgrade-postgres`, `grain backup`, a store move -- makes before
// it copies anything: an error naming the exact command to stop the
// daemon when one holds dataDir's lock, or db's PostgreSQL lease when db
// is a PostgreSQL store (nil db, or a SQLite one, skips that half). It
// never stops the daemon itself: a hand-run command that did would be
// the online migration the offline procedure exists to rule out.
func RefuseWhileDaemonAttached(ctx context.Context, what, dataDir string, db *sql.DB, isPostgres bool) error {
	if dataDir != "" {
		held, holder, err := StoreLockHolder(dataDir)
		if err != nil {
			return err
		}
		if held {
			return fmt.Errorf("%w: %s refuses to run while the daemon on %s (%s) is running, "+
				"because a copy taken under a live daemon loses every write made after it. "+
				"Stop it first with `%s`, run this again, and start it with `sudo systemctl start grain-daemon.service` "+
				"when it is done (docs/database.md)", ErrDaemonAttached, what, dataDir, holder, StopDaemonCommand)
		}
	}
	if db != nil && isPostgres {
		held, err := postgres.DaemonLeaseHeld(ctx, db)
		if err != nil {
			return fmt.Errorf("checking for a daemon attached to the PostgreSQL store: %w", err)
		}
		if held {
			return fmt.Errorf("%w: %s refuses to run while a grain daemon holds its lease on this PostgreSQL store "+
				"(from this host or another). Stop it first with `%s` on the host it runs on, then run this again (docs/database.md)",
				ErrDaemonAttached, what, StopDaemonCommand)
		}
	}
	return nil
}

// storeLockWait is how long a starting daemon waits for the previous one
// to let go of the lock.
var storeLockWait = 90 * time.Second

// holdPostgresLease takes the daemon's lease on its PostgreSQL store,
// waiting storeLockWait for a previous daemon to let go of it.
func holdPostgresLease(ctx context.Context, db *sql.DB) (func(), error) {
	deadline := time.Now().Add(storeLockWait)
	for {
		release, held, err := postgres.HoldDaemonLease(ctx, db)
		if err != nil {
			return nil, err
		}
		if held {
			return release, nil
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("another grain daemon holds its lease on this PostgreSQL store; two daemons on one store fork it. "+
				"Stop the other one first (%s on the host it runs on)", StopDaemonCommand)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
