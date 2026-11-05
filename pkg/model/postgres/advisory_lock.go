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
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// DefaultWriterLockWaitTimeout is how long AcquireWriterLock waits for a
	// draining writer to release the session-level advisory lock before
	// failing closed.
	DefaultWriterLockWaitTimeout = 30 * time.Second
	// DefaultWriterLockPollInterval is how often AcquireWriterLock retries
	// pg_try_advisory_lock while waiting for a draining writer.
	DefaultWriterLockPollInterval = 500 * time.Millisecond
	// DefaultWriterLockKeepAlive is how often the background session monitor
	// verifies that the dedicated lock connection is still alive.
	DefaultWriterLockKeepAlive = 5 * time.Second
	// DefaultWriterLockReacquireWindow is how long the background monitor tries
	// to reconnect and re-acquire the advisory lock after a transient network
	// blip or database restart before declaring the writer fence lost. If
	// another session has already acquired the lock, it fails immediately.
	DefaultWriterLockReacquireWindow = 15 * time.Second

	advisoryLockNamespace = "grain.dev/postgres/single-writer:"
)

// ErrWriterLockHeld is returned when another PostgreSQL session already holds
// the single-writer advisory lock for the target database/schema.
var ErrWriterLockHeld = errors.New("postgres: another grain daemon holds the single-writer advisory lock")

// AdvisoryLockKey derives the deterministic 64-bit PostgreSQL advisory lock key
// for the given schema name (empty defaults to "public").
func AdvisoryLockKey(schema string) int64 {
	schema = strings.TrimSpace(schema)
	if schema == "" {
		schema = "public"
	}
	sum := sha256.Sum256([]byte(advisoryLockNamespace + schema))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}

// WriterLockOptions configures single-writer advisory lock acquisition and
// session monitoring.
type WriterLockOptions struct {
	// WaitTimeout bounds how long AcquireWriterLock polls pg_try_advisory_lock
	// when the lock is already held. Zero uses DefaultWriterLockWaitTimeout;
	// a negative duration tries once without waiting.
	WaitTimeout time.Duration
	// PollInterval is how often AcquireWriterLock retries while waiting.
	// Zero uses DefaultWriterLockPollInterval.
	PollInterval time.Duration
	// KeepAliveInterval is how often the dedicated lock session is pinged in
	// the background. Zero uses DefaultWriterLockKeepAlive; negative disables
	// the background monitor.
	KeepAliveInterval time.Duration
	// ReacquireWindow bounds how long the background monitor attempts to
	// reconnect and re-acquire the lock after a dropped connection. Zero uses
	// DefaultWriterLockReacquireWindow; negative disables re-acquisition and
	// reports loss immediately on disconnect.
	ReacquireWindow time.Duration
	// ApplicationName tags the dedicated lock connection in pg_stat_activity.
	// Empty defaults to "grain-daemon-writer-lock".
	ApplicationName string
	// Logf logs diagnostic transitions (waiting for drain, re-acquisition).
	// Nil uses log.Printf.
	Logf func(format string, args ...any)
	// OnLost is called at most once if the dedicated session drops and the
	// advisory lock cannot be re-acquired (or was taken by another writer).
	OnLost func(err error)
}

// WriterLock holds a session-level PostgreSQL advisory lock on a dedicated,
// non-pooled pgx connection for the lifetime of a grain daemon process.
type WriterLock struct {
	cfg     Config
	appName string
	key     int64
	schema  string
	logf    func(string, ...any)
	onLost  func(error)

	mu         sync.Mutex
	conn       *pgx.Conn
	backendPID uint32
	closed     bool

	closeOnce sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

// AcquireWriterLock opens a dedicated PostgreSQL session outside sql.DB's pool
// and acquires the session-level pg_try_advisory_lock before any schema or data
// writes run.
func AcquireWriterLock(ctx context.Context, cfg Config, opts WriterLockOptions) (*WriterLock, error) {
	waitTimeout := opts.WaitTimeout
	if waitTimeout == 0 {
		waitTimeout = DefaultWriterLockWaitTimeout
	}
	pollInterval := opts.PollInterval
	if pollInterval <= 0 {
		pollInterval = DefaultWriterLockPollInterval
	}
	keepAlive := opts.KeepAliveInterval
	if keepAlive == 0 {
		keepAlive = DefaultWriterLockKeepAlive
	}
	reacquireWindow := opts.ReacquireWindow
	if reacquireWindow == 0 {
		reacquireWindow = DefaultWriterLockReacquireWindow
	}
	appName := strings.TrimSpace(opts.ApplicationName)
	if appName == "" {
		appName = "grain-daemon-writer-lock"
	}
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}

	conn, err := connectWriterLockSession(ctx, cfg, appName)
	if err != nil {
		return nil, err
	}

	schema, err := resolveLockSchema(ctx, conn, cfg)
	if err != nil {
		_ = conn.Close(ctx)
		return nil, fmt.Errorf("postgres: resolving schema for writer lock: %w", err)
	}
	key := AdvisoryLockKey(schema)

	var deadline time.Time
	if waitTimeout > 0 {
		deadline = time.Now().Add(waitTimeout)
	}
	loggedWait := false

	for {
		var acquired bool
		if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired); err != nil {
			_ = conn.Close(ctx)
			return nil, fmt.Errorf("postgres: acquiring writer advisory lock: %w", err)
		}
		if acquired {
			w := &WriterLock{
				cfg:        cfg,
				appName:    appName,
				key:        key,
				schema:     schema,
				logf:       logf,
				onLost:     opts.OnLost,
				conn:       conn,
				backendPID: conn.PgConn().PID(),
				stopCh:     make(chan struct{}),
				doneCh:     make(chan struct{}),
			}
			if keepAlive > 0 {
				go w.watch(keepAlive, reacquireWindow)
			} else {
				close(w.doneCh)
			}
			return w, nil
		}

		if waitTimeout <= 0 || time.Now().After(deadline) {
			holder := describeAdvisoryLockHolder(ctx, conn, key)
			_ = conn.Close(ctx)
			if holder != "" {
				return nil, fmt.Errorf("%w (schema=%s, key=%d, held by %s)", ErrWriterLockHeld, schema, key, holder)
			}
			return nil, fmt.Errorf("%w (schema=%s, key=%d)", ErrWriterLockHeld, schema, key)
		}

		if !loggedWait {
			loggedWait = true
			holder := describeAdvisoryLockHolder(ctx, conn, key)
			if holder != "" {
				logf("grain daemon: PostgreSQL single-writer advisory lock (schema=%s) is held by %s; waiting up to %s for drain",
					schema, holder, waitTimeout)
			} else {
				logf("grain daemon: PostgreSQL single-writer advisory lock (schema=%s) is held by another session; waiting up to %s for drain",
					schema, waitTimeout)
			}
		}

		wait := pollInterval
		if rem := time.Until(deadline); rem < wait {
			wait = rem
		}
		if wait <= 0 {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = conn.Close(context.Background())
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// Key returns the 64-bit PostgreSQL advisory lock key held by w.
func (w *WriterLock) Key() int64 {
	if w == nil {
		return 0
	}
	return w.key
}

// Schema returns the PostgreSQL schema name this lock fences.
func (w *WriterLock) Schema() string {
	if w == nil {
		return ""
	}
	return w.schema
}

// BackendPID returns the PostgreSQL backend process ID currently holding the
// session lock.
func (w *WriterLock) BackendPID() uint32 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.backendPID
}

// Close stops session monitoring, explicitly releases the advisory lock, and
// closes the dedicated PostgreSQL connection. Safe to call more than once.
func (w *WriterLock) Close() error {
	if w == nil {
		return nil
	}
	var closeErr error
	w.closeOnce.Do(func() {
		close(w.stopCh)
		<-w.doneCh

		w.mu.Lock()
		w.closed = true
		conn := w.conn
		w.conn = nil
		w.backendPID = 0
		w.mu.Unlock()

		if conn == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		_ = conn.QueryRow(ctx, "SELECT pg_advisory_unlock($1)", w.key).Scan(&unlocked)
		closeErr = conn.Close(ctx)
	})
	return closeErr
}

func (w *WriterLock) watch(keepAlive, reacquireWindow time.Duration) {
	defer close(w.doneCh)
	ticker := time.NewTicker(keepAlive)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
		}

		if err := w.pingSession(keepAlive); err == nil {
			continue
		} else {
			if w.isStopping() {
				return
			}
			w.logf("grain daemon: PostgreSQL writer lock session interrupted (%v); attempting to re-acquire", err)
			if reacquireErr := w.reacquire(reacquireWindow); reacquireErr != nil {
				if w.isStopping() {
					return
				}
				if w.onLost != nil {
					w.onLost(reacquireErr)
				}
				return
			}
			w.logf("grain daemon: re-acquired PostgreSQL single-writer advisory lock (schema=%s)", w.schema)
		}
	}
}

func (w *WriterLock) isStopping() bool {
	select {
	case <-w.stopCh:
		return true
	default:
		return false
	}
}

func (w *WriterLock) pingSession(timeout time.Duration) error {
	w.mu.Lock()
	conn := w.conn
	expectedPID := w.backendPID
	closed := w.closed
	w.mu.Unlock()

	if closed || conn == nil {
		return errors.New("writer lock session closed")
	}
	if timeout < 2*time.Second {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var pid uint32
	if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
		return err
	}
	if expectedPID != 0 && pid != expectedPID {
		return fmt.Errorf("unexpected backend pid %d (want %d)", pid, expectedPID)
	}
	return nil
}

func (w *WriterLock) reacquire(window time.Duration) error {
	w.mu.Lock()
	oldConn := w.conn
	w.conn = nil
	w.backendPID = 0
	w.mu.Unlock()

	if oldConn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = oldConn.Close(ctx)
		cancel()
	}

	if window < 0 {
		return errors.New("postgres: single-writer advisory lock session disconnected")
	}

	deadline := time.Now().Add(window)
	retryStep := 250 * time.Millisecond
	if half := window / 4; half > 0 && half < retryStep {
		retryStep = half
	}
	var lastErr error

	for {
		if w.isStopping() {
			return nil
		}
		attemptTimeout := 5 * time.Second
		if rem := time.Until(deadline); rem > 0 && rem < attemptTimeout {
			attemptTimeout = rem
		}
		if attemptTimeout < 250*time.Millisecond {
			attemptTimeout = 250 * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		newConn, err := connectWriterLockSession(ctx, w.cfg, w.appName)
		if err == nil {
			var acquired bool
			err = newConn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", w.key).Scan(&acquired)
			cancel()
			if err == nil {
				if !acquired {
					closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
					holder := describeAdvisoryLockHolder(closeCtx, newConn, w.key)
					_ = newConn.Close(closeCtx)
					closeCancel()
					if holder != "" {
						return fmt.Errorf("%w: taken by %s while session was disconnected", ErrWriterLockHeld, holder)
					}
					return fmt.Errorf("%w: taken by another session while disconnected", ErrWriterLockHeld)
				}
				w.mu.Lock()
				if w.closed {
					w.mu.Unlock()
					closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = newConn.Close(closeCtx)
					closeCancel()
					return nil
				}
				w.conn = newConn
				w.backendPID = newConn.PgConn().PID()
				w.mu.Unlock()
				return nil
			}
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = newConn.Close(closeCtx)
			closeCancel()
			lastErr = err
		} else {
			cancel()
			lastErr = err
		}

		if time.Now().After(deadline) {
			if lastErr == nil {
				lastErr = errors.New("timed out")
			}
			return fmt.Errorf("postgres: single-writer advisory lock lost and could not be re-acquired within %s: %w", window, lastErr)
		}

		timer := time.NewTimer(retryStep)
		select {
		case <-w.stopCh:
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func connectWriterLockSession(ctx context.Context, cfg Config, appName string) (*pgx.Conn, error) {
	connConfig, err := buildConnConfig(cfg)
	if err != nil {
		return nil, err
	}
	if connConfig.RuntimeParams == nil {
		connConfig.RuntimeParams = make(map[string]string)
	}
	if _, exists := connConfig.RuntimeParams["application_name"]; !exists && appName != "" {
		connConfig.RuntimeParams["application_name"] = appName
	}
	conn, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return nil, fmt.Errorf("postgres: connecting writer lock session: %w", err)
	}
	return conn, nil
}

func resolveLockSchema(ctx context.Context, conn *pgx.Conn, cfg Config) (string, error) {
	var current sql.NullString
	if err := conn.QueryRow(ctx, "SELECT current_schema()").Scan(&current); err != nil {
		return "", err
	}
	if current.Valid && strings.TrimSpace(current.String) != "" {
		return strings.TrimSpace(current.String), nil
	}
	if sp := firstSearchPathEntry(cfg.SearchPath); sp != "" {
		return sp, nil
	}
	if sp := firstSearchPathEntry(conn.Config().RuntimeParams["search_path"]); sp != "" {
		return sp, nil
	}
	return "public", nil
}

func firstSearchPathEntry(searchPath string) string {
	for _, part := range strings.Split(searchPath, ",") {
		part = strings.Trim(strings.TrimSpace(part), `"`)
		if part != "" && part != "$user" {
			return part
		}
	}
	return ""
}

func describeAdvisoryLockHolder(ctx context.Context, conn *pgx.Conn, key int64) string {
	qCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	classID := int64(uint32(uint64(key) >> 32))
	objID := int64(uint32(uint64(key)))
	var pid int
	var appName, clientAddr, state sql.NullString
	err := conn.QueryRow(qCtx, `
		SELECT l.pid, a.application_name, a.client_addr::text, a.state
		FROM pg_locks l
		LEFT JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.locktype = 'advisory'
		  AND l.granted
		  AND l.objsubid = 1
		  AND l.classid = $1::bigint::oid
		  AND l.objid = $2::bigint::oid
		LIMIT 1
	`, classID, objID).Scan(&pid, &appName, &clientAddr, &state)
	if err != nil {
		return ""
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("pid=%d", pid))
	if appName.Valid && appName.String != "" {
		parts = append(parts, fmt.Sprintf("app=%q", appName.String))
	}
	if clientAddr.Valid && clientAddr.String != "" {
		parts = append(parts, fmt.Sprintf("addr=%s", clientAddr.String))
	}
	if state.Valid && state.String != "" {
		parts = append(parts, fmt.Sprintf("state=%s", state.String))
	}
	return strings.Join(parts, " ")
}
