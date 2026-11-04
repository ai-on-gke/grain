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

// The guards around the store (storelock.go, storeswitch.go,
// highwater.go), each against the way the bsalmon deployment lost tasks
// on 2026-11-04: a migration rewriting db-config.json under a live
// daemon, a copy taken while it was writing, and a start on the older
// copy that handed task ids out again.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

func TestACopyUnderALiveDaemonIsRefused(t *testing.T) {
	dataDir := t.TempDir()
	ctx := context.Background()
	if err := RefuseWhileDaemonAttached(ctx, "grain upgrade-postgres", dataDir, nil, false); err != nil {
		t.Fatalf("with no daemon: %v", err)
	}
	lock, err := AcquireStoreLock(dataDir, 0)
	if err != nil {
		t.Fatalf("AcquireStoreLock: %v", err)
	}
	err = RefuseWhileDaemonAttached(ctx, "grain upgrade-postgres", dataDir, nil, false)
	if !errors.Is(err, ErrDaemonAttached) {
		t.Fatalf("with the daemon's lock held: %v, want ErrDaemonAttached", err)
	}
	if !strings.Contains(err.Error(), StopDaemonCommand) || !strings.Contains(err.Error(), "pid="+strconv.Itoa(os.Getpid())) {
		t.Errorf("refusal %q does not name %q and the holder", err, StopDaemonCommand)
	}
	// A second daemon on the same data directory is refused too.
	if second, err := AcquireStoreLock(dataDir, 300*time.Millisecond); err == nil {
		second.Release()
		t.Fatal("a second daemon took the lock the first holds")
	}
	lock.Release()
	if err := RefuseWhileDaemonAttached(ctx, "grain upgrade-postgres", dataDir, nil, false); err != nil {
		t.Fatalf("after the daemon stopped: %v", err)
	}
}

func resetStoreSwitch(t *testing.T) {
	t.Helper()
	prev := storeSwitched.Load()
	storeSwitched.Store(nil)
	t.Cleanup(func() { storeSwitched.Store(prev) })
}

func TestADBConfigChangeUnderARunningDaemonRaisesTheAlert(t *testing.T) {
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	resetStoreSwitch(t)
	dataDir := t.TempDir()
	opened, err := storeSelection(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 4, 3, 32, 0, 0, time.UTC)
	if checkStoreSelection(dataDir, opened, now) || storeDispatchHalted() != "" {
		t.Fatal("alert raised with nothing changed")
	}
	// What `grain upgrade-postgres` did at 03:32: db-config.json now
	// selects a PostgreSQL copy while this daemon writes to SQLite.
	if err := os.WriteFile(filepath.Join(dataDir, postgres.DatabaseConfigFile),
		[]byte(`{"driver":"postgres","dsn":"postgres://grain@127.0.0.1:5432/grain"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !checkStoreSelection(dataDir, opened, now) {
		t.Fatal("no alert after db-config.json switched the store")
	}
	sw := CurrentStoreSwitch()
	if sw == nil || sw.Opened != opened || !strings.Contains(sw.Selected, "PostgreSQL at") || !sw.Since.Equal(now) {
		t.Fatalf("alert = %+v", sw)
	}
	if why := storeDispatchHalted(); why == "" || !strings.Contains(why, "No more tasks are dispatched") {
		t.Errorf("dispatch not halted: %q", why)
	}
	// It stands until a restart, even when the file is put back.
	if err := os.Remove(filepath.Join(dataDir, postgres.DatabaseConfigFile)); err != nil {
		t.Fatal(err)
	}
	if !checkStoreSelection(dataDir, opened, now.Add(time.Minute)) || CurrentStoreSwitch() != sw {
		t.Error("the alert lifted without a restart")
	}
}

// openGuardedStore opens a SQLite store in dataDir the way the daemon does,
// with n task ids allocated and one run started.
func openGuardedStore(t *testing.T, dataDir string, n int, runAt time.Time) *model.Store {
	t.Helper()
	store, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	var last string
	for i := 0; i < n; i++ {
		if last, err = store.NewTaskID(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if !runAt.IsZero() {
		human := model.Principal{ID: "op@example.com", Kind: model.PrincipalHuman}
		if err := store.PutTask(ctx, model.Task{ID: last, Intent: model.IntentImplement, Title: "t",
			Origin:  model.Origin{Attribution: model.Attribution{Actor: human}, Reason: model.ReasonDirect},
			Binding: model.BindingDirective, Approval: &model.Attribution{Actor: human}}); err != nil {
			t.Fatal(err)
		}
		if err := store.StartRun(ctx, model.Run{ID: "run-" + last, TaskID: last, Attempt: 1, StartedAt: runAt}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

func TestAStartBehindTheHighWaterMarkIsRefused(t *testing.T) {
	t.Setenv(AcceptStoreBehindEnv, "")
	ctx := context.Background()
	runAt := time.Date(2026, 11, 4, 15, 0, 0, 0, time.UTC)

	// The store the daemon ran on: 3114 tasks' worth of ids is a lot of
	// rows for a test, so 14 stands in for it.
	live := t.TempDir()
	liveStore := openGuardedStore(t, live, 14, runAt)
	if err := CheckStoreHighWater(ctx, liveStore.DB(), "sqlite", "", live); err != nil {
		t.Fatalf("first start: %v", err)
	}
	mark, err := ReadStoreHighWater(live)
	if err != nil || mark == nil || mark.LastTaskID != 14 || mark.LastRunID != "run-14" {
		t.Fatalf("mark after the first start = %+v, %v", mark, err)
	}

	// The copy taken earlier: only 5 tasks, an older run, in the data
	// directory the mark lives in.
	stale := t.TempDir()
	staleStore := openGuardedStore(t, stale, 5, runAt.Add(-12*time.Hour))
	if err := writeStoreHighWater(stale, *mark); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stale, "postgres.pg16"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stale, "postgres.pg16", "PG_VERSION"), []byte("16\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = CheckStoreHighWater(ctx, staleStore.DB(), "sqlite", "", stale)
	if err == nil {
		t.Fatal("started on a store behind the mark")
	}
	for _, want := range []string{
		"refusing to start", "SQLite at " + filepath.Join(stale, "store"),
		"its last task is #5, but task ids up to #14 were handed out (9 task(s) missing)",
		"run run-14", "a PostgreSQL data directory at " + filepath.Join(stale, "postgres.pg16"),
		AcceptStoreBehindEnv + "=1",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q:\n%v", want, err)
		}
	}
	if got, _ := ReadStoreHighWater(stale); got == nil || got.LastTaskID != 14 {
		t.Errorf("a refused start moved the mark: %+v", got)
	}
	// grain db check gives the same verdict, without writing anything.
	check, err := CheckStore(ctx, staleStore.DB(), "sqlite", "", stale)
	if err != nil || len(check.Behind) == 0 || check.Message == "" {
		t.Errorf("CheckStore = %+v, %v; want it behind", check, err)
	}
}

func TestTheTaskIDSequenceNeverGoesBackwards(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	store := openGuardedStore(t, dataDir, 5, time.Time{})
	mark := StoreHighWater{LastTaskID: 14, LastWriteAt: time.Now().UTC(), Store: "SQLite at elsewhere"}
	if err := writeStoreHighWater(dataDir, mark); err != nil {
		t.Fatal(err)
	}
	// An operator who knows the newer store is gone lets the start
	// through; ids then continue past the mark, never from the store's 5.
	t.Setenv(AcceptStoreBehindEnv, "1")
	if err := CheckStoreHighWater(ctx, store.DB(), "sqlite", "", dataDir); err != nil {
		t.Fatalf("accepted start: %v", err)
	}
	id, err := store.NewTaskID(ctx)
	if err != nil || id != "15" {
		t.Fatalf("next task id = %q, %v; want 15", id, err)
	}
	// And the next start has nothing to refuse.
	t.Setenv(AcceptStoreBehindEnv, "")
	if err := CheckStoreHighWater(ctx, store.DB(), "sqlite", "", dataDir); err != nil {
		t.Fatalf("start after the accepted one: %v", err)
	}
	// The mark itself never moves back, whatever a store says.
	got, _ := ReadStoreHighWater(dataDir)
	if err := advanceStoreHighWater(dataDir, *got, StoreHighWater{LastTaskID: 2}, got.Store); err != nil {
		t.Fatal(err)
	}
	if after, _ := ReadStoreHighWater(dataDir); after.LastTaskID != 15 {
		t.Errorf("mark after a lower store = #%d, want #15", after.LastTaskID)
	}
}
