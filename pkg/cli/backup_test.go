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
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/backup"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

func TestScheduledBackupLoopRunsAndStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var calls atomic.Int32
	go func() {
		defer close(done)
		daemon.RunBackupLoop(ctx, time.Millisecond, func(context.Context) {
			if calls.Add(1) == 2 {
				cancel()
			}
		})
	}()
	select {
	case <-done:
	case <-time.After(daemonWaitLimit):
		t.Fatal("backup loop did not stop after cancellation")
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("scheduled backup calls = %d, want 2", got)
	}
}

func TestBackupLoopDoesNotRunBeforeItsFirstInterval(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		daemon.RunBackupLoop(ctx, time.Hour, func(context.Context) { calls.Add(1) })
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(daemonWaitLimit):
		t.Fatal("backup loop did not stop")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("scheduled backup calls = %d before first interval", got)
	}
}

// TestBackupHelpExitsZero pins that `grain backup -h` is a success: the
// usage it asked for is printed, and the flag package's ErrHelp is not
// reported as a failure with exit status 1.
func TestBackupHelpExitsZero(t *testing.T) {
	err := runBackup(context.Background(), []string{"-h"}, io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("runBackup(-h) = %v, want flag.ErrHelp", err)
	}
	if code := backupExitCode(err); code != 0 {
		t.Errorf("backupExitCode(ErrHelp) = %d, want 0", code)
	}
	if code := backupExitCode(errors.New("boom")); code != 1 {
		t.Errorf("backupExitCode(boom) = %d, want 1", code)
	}
	if code := backupExitCode(nil); code != 0 {
		t.Errorf("backupExitCode(nil) = %d, want 0", code)
	}
}

type captureBackupDestination struct {
	name    string
	content []byte
}

func (d *captureBackupDestination) Upload(_ context.Context, name, _ string, content io.Reader) (backup.Remote, error) {
	d.name = name
	var err error
	d.content, err = io.ReadAll(content)
	return backup.Remote{ID: "gs://test-bucket/" + name}, err
}

func TestRunBackupSnapshotsPostgresStoreFromRecordedConfig(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	if _, err := postgres.FindBinary("pg_dump"); err != nil {
		t.Skipf("pg_dump not available: %v", err)
	}
	for _, env := range []string{"GRAIN_DB_DRIVER", "GRAIN_DB_DSN", "GRAIN_POSTGRES_URL", "DATABASE_URL"} {
		t.Setenv(env, "")
	}

	dsn, _ := postgresTestSchemaDSN(t)
	dataDir := t.TempDir()
	if err := daemon.RecordDatabaseConfig(dataDir, "postgres", dsn); err != nil {
		t.Fatalf("RecordDatabaseConfig: %v", err)
	}

	ctx := context.Background()
	db, err := postgres.Open(postgres.Config{URL: dsn})
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	if _, err := db.ExecContext(ctx, strings.Join(postgres.Statements(), ";\n")); err != nil {
		_ = db.Close()
		t.Fatalf("applying postgres DDL: %v", err)
	}
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		_ = db.Close()
		t.Fatalf("store.Init: %v", err)
	}
	_ = db.Close()

	configRepo := daemon.StateRepoDir(dataDir)
	if err := os.MkdirAll(configRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, gitArgs := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = configRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", gitArgs, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(configRepo, "config.yaml"), []byte("defaultModel: gemini\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, gitArgs := range [][]string{{"add", "config.yaml"}, {"commit", "-m", "seed"}} {
		cmd := exec.Command("git", gitArgs...)
		cmd.Dir = configRepo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", gitArgs, err, out)
		}
	}

	captured := &captureBackupDestination{}
	prevFactory := newBackupService
	newBackupService = func(_ context.Context, _, _, _ string) (backup.Service, string, error) {
		return backup.Service{Destination: captured}, "GCS", nil
	}
	t.Cleanup(func() { newBackupService = prevFactory })

	var out bytes.Buffer
	if err := runBackup(ctx, []string{"-data-dir", dataDir, "-gcs-bucket", "test-bucket"}, &out); err != nil {
		t.Fatalf("runBackup against recorded Postgres store: %v", err)
	}
	if !strings.Contains(out.String(), "to GCS as grain-backup-") {
		t.Fatalf("unexpected CLI output: %q", out.String())
	}

	zr, err := zip.NewReader(bytes.NewReader(captured.content), int64(len(captured.content)))
	if err != nil {
		t.Fatalf("opening uploaded zip: %v", err)
	}
	entries := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[f.Name] = b
	}
	if len(entries["database/grain.dump"]) == 0 {
		t.Fatalf("archive missing non-empty database/grain.dump (entries: %d)", len(entries))
	}
	var manifest backup.Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatalf("unmarshaling manifest: %v", err)
	}
	if manifest.Database != "database/grain.dump" || manifest.DatabaseDriver != "postgres" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
}
