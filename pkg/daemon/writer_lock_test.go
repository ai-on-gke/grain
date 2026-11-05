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

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

func daemonPostgresTestSchemaDSN(t *testing.T) (dsn, schema string, admin *sql.DB, baseCfg postgres.Config) {
	t.Helper()
	postgrestest.SkipUnlessAvailable(t)
	baseCfg = postgrestest.Config(t)
	var err error
	admin, err = postgres.Open(baseCfg)
	if err != nil {
		t.Fatalf("connecting to test postgres: %v", err)
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		_ = admin.Close()
		t.Fatal(err)
	}
	schema = "daemon_lock_" + hex.EncodeToString(b[:])
	if _, err := admin.Exec(fmt.Sprintf(`CREATE SCHEMA "%s"`, schema)); err != nil {
		_ = admin.Close()
		t.Fatalf("creating schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema))
		_ = admin.Close()
	})

	var u *url.URL
	if baseCfg.URL != "" {
		u, err = url.Parse(baseCfg.URL)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		u = &url.URL{Scheme: "postgres", User: url.User(baseCfg.User), Path: "/" + baseCfg.Database}
		q := u.Query()
		q.Set("host", baseCfg.Host)
		q.Set("port", strconv.Itoa(baseCfg.Port))
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String(), schema, admin, baseCfg
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForDaemonUI(t *testing.T, uiAddr string, runErr chan error) {
	t.Helper()
	// Under -race, the first Store.Init in a binary adapts ~200 DDL statements
	// before caching them, which can take ~25s on a 2-core test VM.
	deadline := time.Now().Add(90 * time.Second)
	for {
		select {
		case err := <-runErr:
			runErr <- err
			t.Fatalf("daemon exited before UI %s became ready: %v", uiAddr, err)
		default:
		}
		resp, err := http.Get("http://" + uiAddr + "/api/config")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon UI at %s did not become ready within 90s", uiAddr)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func TestDaemonWriterLockFencesSecondDaemonAndTransfersOnShutdown(t *testing.T) {
	dsn, _, _, _ := daemonPostgresTestSchemaDSN(t)

	uiAddr1 := freeLocalAddr(t)
	ctx1, cancel1 := context.WithCancel(context.Background())
	runErr1 := make(chan error, 1)
	go func() {
		runErr1 <- Run(ctx1, Config{
			DataDir:                t.TempDir(),
			SandboxDir:             t.TempDir(),
			DbDriver:               "postgres",
			DbDSN:                  dsn,
			MaxWorkers:             1,
			PollInterval:           time.Hour,
			UiAddr:                 uiAddr1,
			DisableNativeSandboxes: true,
			Actor:                  "tester",
			WriterLockWaitTimeout:  2 * time.Second,
		})
	}()
	defer func() {
		cancel1()
		if err := <-runErr1; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("first daemon Run returned unexpected error: %v", err)
		}
	}()

	waitForDaemonUI(t, uiAddr1, runErr1)

	// A second daemon against the same Postgres schema while the first is live
	// fails closed with ErrWriterLockHeld before opening or mutating the store.
	err := Run(context.Background(), Config{
		DataDir:                t.TempDir(),
		SandboxDir:             t.TempDir(),
		DbDriver:               "postgres",
		DbDSN:                  dsn,
		MaxWorkers:             1,
		PollInterval:           time.Hour,
		UiAddr:                 freeLocalAddr(t),
		DisableNativeSandboxes: true,
		Actor:                  "tester",
		WriterLockWaitTimeout:  120 * time.Millisecond,
	})
	if !errors.Is(err, postgres.ErrWriterLockHeld) {
		t.Fatalf("second daemon error = %v, want errors.Is(_, postgres.ErrWriterLockHeld)", err)
	}

	// A replacement daemon started while the first drains waits for the lock
	// release and takes over cleanly once the first daemon closes its pool.
	uiAddr2 := freeLocalAddr(t)
	ctx2, cancel2 := context.WithCancel(context.Background())
	runErr2 := make(chan error, 1)
	go func() {
		runErr2 <- Run(ctx2, Config{
			DataDir:                t.TempDir(),
			SandboxDir:             t.TempDir(),
			DbDriver:               "postgres",
			DbDSN:                  dsn,
			MaxWorkers:             1,
			PollInterval:           time.Hour,
			UiAddr:                 uiAddr2,
			DisableNativeSandboxes: true,
			Actor:                  "tester",
			WriterLockWaitTimeout:  15 * time.Second,
		})
	}()
	defer func() {
		cancel2()
		if err := <-runErr2; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("replacement daemon Run returned unexpected error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	cancel1()
	if err := <-runErr1; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("first daemon drain error = %v", err)
	}
	// Put nil back so the deferred cleanup does not block.
	runErr1 <- nil

	waitForDaemonUI(t, uiAddr2, runErr2)
}

func TestDaemonWriterLockStopsOnSplitBrainTakeover(t *testing.T) {
	dsn, schema, admin, _ := daemonPostgresTestSchemaDSN(t)

	uiAddr := freeLocalAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, Config{
			DataDir:                   t.TempDir(),
			SandboxDir:                t.TempDir(),
			DbDriver:                  "postgres",
			DbDSN:                     dsn,
			MaxWorkers:                1,
			PollInterval:              time.Hour,
			UiAddr:                    uiAddr,
			DisableNativeSandboxes:    true,
			Actor:                     "tester",
			WriterLockWaitTimeout:     2 * time.Second,
			WriterLockKeepAlive:       150 * time.Millisecond,
			WriterLockReacquireWindow: 250 * time.Millisecond,
		})
	}()
	defer func() {
		cancel()
		<-runErr
	}()

	waitForDaemonUI(t, uiAddr, runErr)

	key := postgres.AdvisoryLockKey(schema)
	objid := int64(uint32(key >> 32))
	objsubid := int64(uint32(key))

	var lockPID int
	err := admin.QueryRow(
		`SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted AND classid = ?::bigint::oid AND objid = ?::bigint::oid AND objsubid = 1 LIMIT 1`,
		objid, objsubid,
	).Scan(&lockPID)
	if err != nil {
		t.Fatalf("locating daemon advisory lock PID: %v", err)
	}

	// Queue a rival session on pg_advisory_lock in PostgreSQL's lock manager
	// before terminating the daemon's lock session so ownership transfers
	// atomically inside PostgreSQL with no race against reconnect.
	rivalConn, err := admin.Conn(context.Background())
	if err != nil {
		t.Fatalf("opening rival connection: %v", err)
	}
	defer func() {
		_, _ = rivalConn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(?)`, key)
		_ = rivalConn.Close()
	}()

	rivalAcquired := make(chan error, 1)
	go func() {
		_, acquireErr := rivalConn.ExecContext(context.Background(), `SELECT pg_advisory_lock(?)`, key)
		rivalAcquired <- acquireErr
	}()

	// Wait until rivalConn appears as waiting on the advisory lock in pg_locks.
	waitDeadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		_ = admin.QueryRow(
			`SELECT COUNT(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted AND classid = ?::bigint::oid AND objid = ?::bigint::oid AND objsubid = 1`,
			objid, objsubid,
		).Scan(&waiting)
		if waiting > 0 || time.Now().After(waitDeadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := admin.Exec(`SELECT pg_terminate_backend(?)`, lockPID); err != nil {
		t.Fatalf("terminating daemon writer lock backend %d: %v", lockPID, err)
	}
	if err := <-rivalAcquired; err != nil {
		t.Fatalf("rival pg_advisory_lock failed: %v", err)
	}

	select {
	case daemonErr := <-runErr:
		// Put nil back so the deferred cleanup does not block.
		runErr <- nil
		if daemonErr == nil || !strings.Contains(daemonErr.Error(), "PostgreSQL single-writer advisory lock lost") {
			t.Fatalf("daemon exit error = %v, want PostgreSQL single-writer advisory lock lost", daemonErr)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not exit within 15s after losing PostgreSQL single-writer advisory lock")
	}
}
