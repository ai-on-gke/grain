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

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
)

// A store is migrated or copied only offline: `grain upgrade-postgres`
// and `grain backup` refuse while a daemon holds the data directory, say
// how to stop it, and leave it running -- and db-config.json as it was.
func TestStoreToolsRefuseWhileADaemonHoldsTheStore(t *testing.T) {
	dataDir := t.TempDir()
	if _, _, err := daemon.OpenStore("sqlite", "", dataDir); err != nil {
		t.Fatal(err)
	}
	lock, err := daemon.AcquireStoreLock(dataDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	for name, run := range map[string]func() error{
		"upgrade-postgres": func() error {
			return runUpgradePostgres(context.Background(), []string{"-data-dir", dataDir, "-dsn", "postgres://grain@127.0.0.1:1/grain?connect_timeout=1"}, io.Discard)
		},
		"backup": func() error {
			return runBackup(context.Background(), []string{"-data-dir", dataDir, "-gcs-bucket", "b"}, io.Discard)
		},
		"export": func() error {
			return runExport(context.Background(), []string{"-data-dir", dataDir, "-out", filepath.Join(t.TempDir(), "b.tar.gz")}, io.Discard)
		},
		"import": func() error {
			return runImport(context.Background(), []string{"-data-dir", dataDir, "-in", filepath.Join(t.TempDir(), "b.tar.gz"), "-force"}, io.Discard)
		},
	} {
		err := run()
		if !errors.Is(err, daemon.ErrDaemonAttached) || !strings.Contains(err.Error(), daemon.StopDaemonCommand) {
			t.Errorf("%s under a live daemon = %v, want a refusal naming %q", name, err, daemon.StopDaemonCommand)
		}
	}
	if _, err := os.Stat(filepath.Join(dataDir, postgres.DatabaseConfigFile)); !os.IsNotExist(err) {
		t.Errorf("db-config.json was written under a live daemon: %v", err)
	}
	if held, _, _ := daemon.StoreLockHolder(dataDir); !held {
		t.Error("the daemon's lock was taken away")
	}
}

func TestDBCheckSaysWhichStoreAndWhetherItIsBehind(t *testing.T) {
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}
	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.NewTaskID(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out strings.Builder
	if err := runDBCheck(context.Background(), []string{"-data-dir", dataDir}, &out); err != nil {
		t.Fatalf("db check with no mark: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "store: SQLite at "+filepath.Join(dataDir, "store")) || !strings.Contains(out.String(), "last task #1") {
		t.Errorf("db check said:\n%s", out.String())
	}
	if err := os.WriteFile(filepath.Join(dataDir, daemon.StoreHighWaterFile), []byte(`{"lastTaskId": 9}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err = runDBCheck(context.Background(), []string{"-data-dir", dataDir}, &out)
	if err == nil || !strings.Contains(err.Error(), "task ids up to #9 were handed out") {
		t.Errorf("db check behind the mark = %v\n%s", err, out.String())
	}
}
