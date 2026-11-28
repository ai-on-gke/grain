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

// An offline command migrating the store in place (finding F3 of
// docs/test-runs/2026-11-18-migrated-secret-copies.md): on a data dir an
// older build left, `grain secrets ... -secrets-backend secretmanager list`
// opened the store through OpenStore for its catalogue, Init migrated it from
// N-1 to N, and the state repository stayed at N-1. Every daemon start of the
// same build then refused with "state repository was written by an older
// build of grain". OpenStore is what every such command calls (pkg/cli's
// secrets, connections, state, import, version and repocase), so it is what
// these tests call.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// offlineOpen opens dataDir's store the way an offline grain command does,
// and closes it again.
func offlineOpen(t *testing.T, dataDir string) {
	t.Helper()
	_, db, err := OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("OpenStore (the offline command's open): %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStartsAfterAnOfflineCommandMigratedTheStoreInPlace(t *testing.T) {
	// Not parallel: Run touches package-level globals, restored on cleanup
	// as TestDaemonRunStartsAcrossPlainBinarySwapFromSchema65 does.
	t.Cleanup(func() {
		model.SetDefaultGitHost("")
		ReconcilerDown.Store(false)
		LivePullRequests.Store(nil)
		liveGitHubCalls.Store(nil)
		liveRepoNames.Store(nil)
	})

	previous := model.SchemaVersion - 1
	dataDir := t.TempDir()
	buildStoreAtSchema(t, dataDir, previous, func(t *testing.T, db *sql.DB) {
		commitDumpAtSchema(t, dataDir, db, "", previous)
	})

	offlineOpen(t, dataDir)
	if got, err := staterepo.ReadSchemaVersion(StateRepoDir(dataDir)); err != nil || got != previous {
		t.Fatalf("state-repo schema after the offline open = %d (err %v), want %d untouched", got, err, previous)
	}
	if from, ok := readInPlaceMigration(dataDir); !ok || from != previous {
		t.Fatalf("offline open left no in-place migration note from %d (got %d, %v)", previous, from, ok)
	}
	// And the daemon may not start for a while after it: age the note past
	// schemaUpgradeMaxAge, which does not apply to it.
	backdateInPlaceMigration(t, dataDir, 30*24*time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	uiAddrCh := make(chan string, 1)
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, Config{
			DataDir:                dataDir,
			SandboxDir:             t.TempDir(),
			MaxWorkers:             1,
			PollInterval:           time.Hour,
			UiAddr:                 "127.0.0.1:0",
			UIAddrReady:            func(addr string) { uiAddrCh <- addr },
			SandboxPortBase:        0,
			DisableNativeSandboxes: true,
			Actor:                  "tester",
		})
	}()
	defer func() {
		cancel()
		if err := <-runErr; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("daemon Run returned unexpected error: %v", err)
		}
	}()

	var uiAddr string
	select {
	case uiAddr = <-uiAddrCh:
	case err := <-runErr:
		runErr <- err
		t.Fatalf("daemon exited after an offline command migrated its store from %d: %v", previous, err)
	case <-time.After(30 * time.Second):
		t.Fatal("daemon did not bind UI listener within 30s")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-runErr:
			runErr <- err
			t.Fatalf("daemon exited instead of starting: %v", err)
		default:
		}
		resp, err := http.Get("http://" + uiAddr + "/api/config")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusUnauthorized {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon UI at %s did not come up within 30s", uiAddr)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if got, err := staterepo.ReadSchemaVersion(StateRepoDir(dataDir)); err != nil || got != model.SchemaVersion {
		t.Errorf("state-repo schema after the daemon start = %d (err %v), want %d", got, err, model.SchemaVersion)
	}
	if msg := headCommitMessage(t, StateRepoDir(dataDir)); !strings.Contains(msg, "Migrate grain state to schema "+strconv.Itoa(model.SchemaVersion)) {
		t.Errorf("state-repo commit message = %q, want migration commit", msg)
	}
	if _, err := os.Stat(inPlaceMigrationFile(dataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("in-place migration marker %s still present after startup (%v)", inPlaceMigrationFile(dataDir), err)
	}
}

// The note an offline open leaves authorises the one dump it describes:
// a state repository written by a newer build is still ErrSchemaTooNew.
func TestOfflineInPlaceMigrationDoesNotAuthoriseANewerDump(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	previous := model.SchemaVersion - 1
	dataDir := t.TempDir()
	buildStoreAtSchema(t, dataDir, previous, func(t *testing.T, db *sql.DB) {
		commitDumpAtSchema(t, dataDir, db, "", model.SchemaVersion+1)
	})

	offlineOpen(t, dataDir)

	_, db, migrated, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if migrated.from != previous {
		t.Fatalf("migrated = %+v, want from %d recovered from the offline open's note", migrated, previous)
	}
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadStateRepo(ctx, dataDir, repo, db, migrated); !errors.Is(err, staterepo.ErrSchemaTooNew) {
		t.Fatalf("loadStateRepo err = %v, want ErrSchemaTooNew for a dump at %d", err, model.SchemaVersion+1)
	}
	if got, _ := staterepo.ReadSchemaVersion(repo.Dir()); got != model.SchemaVersion+1 {
		t.Errorf("newer dump rewritten to schema %d, want it left at %d", got, model.SchemaVersion+1)
	}
}

func backdateInPlaceMigration(t *testing.T, dataDir string, by time.Duration) {
	t.Helper()
	path := inPlaceMigrationFile(dataDir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var note schemaUpgrade
	if err := json.Unmarshal(data, &note); err != nil {
		t.Fatal(err)
	}
	note.WrittenAt = note.WrittenAt.Add(-by)
	if data, err = json.Marshal(note); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
