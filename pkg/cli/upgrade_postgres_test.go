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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	grainsqlite "github.com/ai-on-gke/grain/pkg/model/sqlite"
)

func runHostGrain(t *testing.T, argv ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	rawArgv, err := json.Marshal(argv)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testBinary(t), "-test.run=^TestHostCLISpecHelper$")
	cmd.Dir = dir
	cmd.Env = []string{
		"GRAIN_CLISPEC_ARGV=" + string(rawArgv),
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GRAIN_SANDBOX_CLI=0",
		"GRAIN_CLI_ENV=",
		"GRAIN_DATA_DIR=" + dir,
	}
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	t.Fatalf("running host grain %q: %v", argv, err)
	return "", 0
}

func TestDBCmdRefusesUnknownSubcommand(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "sandbox db query on host",
			argv: []string{"db", "query", "select", "1"},
			want: `grain db: unknown command "query" (grain db upgrade-postgres [flags], grain db check, grain db downgrade -to N; db query is the in-sandbox CLI's)`,
		},
		{
			name: "unknown subcommand",
			argv: []string{"db", "migrate"},
			want: `grain db: unknown command "migrate"`,
		},
		{
			name: "bare db",
			argv: []string{"db"},
			want: "grain db: a command is required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, code := runHostGrain(t, tc.argv...)
			if code != 1 {
				t.Fatalf("grain %v exited %d, want 1; output:\n%s", tc.argv, code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("grain %v output = %q, want substring %q", tc.argv, out, tc.want)
			}
			if strings.Contains(out, "grain upgrade-postgres:") {
				t.Errorf("grain %v reported as upgrade-postgres: %q", tc.argv, out)
			}
		})
	}

	var errBuf bytes.Buffer
	if err := runDB([]string{"-h"}, &errBuf); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("runDB(-h) = %v, want flag.ErrHelp", err)
	}
}

func TestUpgradePostgresRefusesStrayArguments(t *testing.T) {
	t.Parallel()
	// Bind a local TCP listener so we can prove runUpgradePostgres refuses the
	// stray argument before dialing -dsn or touching -data-dir.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var dialed atomic.Bool
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			dialed.Store(true)
			conn.Close()
		}
	}()

	dataDir := t.TempDir()
	dsn := "postgres://" + ln.Addr().String() + "/grain?sslmode=disable"
	for _, prefix := range [][]string{
		{"upgrade-postgres"},
		{"db", "upgrade-postgres"},
		{"db", "upgrade-sqlite-to-postgres"},
	} {
		argv := append(append([]string{}, prefix...), "-dsn", dsn, "-data-dir", dataDir, "stray")
		out, code := runHostGrain(t, argv...)
		if code != 1 {
			t.Fatalf("grain %v exited %d, want 1; output:\n%s", argv, code, out)
		}
		if !strings.Contains(out, `grain upgrade-postgres: unexpected argument "stray"`) {
			t.Errorf("grain %v output = %q, want unexpected argument \"stray\"", argv, out)
		}
	}
	if dialed.Load() {
		t.Fatal("upgrade-postgres dialed -dsn despite a stray positional argument")
	}
}

func TestUpgradePostgresCLIValidation(t *testing.T) {
	var buf bytes.Buffer
	// Missing DSN and no embedded postgres with missing flags
	err := runUpgradePostgres(context.Background(), []string{"-sqlite-path=/nonexistent"}, &buf)
	if err == nil {
		t.Fatalf("expected error when no dsn or postgres is provided, got nil")
	}
}

func TestUpgradePostgresCLIHelp(t *testing.T) {
	var buf bytes.Buffer
	err := runUpgradePostgres(context.Background(), []string{"-h"}, &buf)
	if err == nil {
		t.Fatalf("expected ErrHelp, got nil")
	}
}

func TestUpgradePostgresWithEmbeddedPostgres(t *testing.T) {
	if !postgres.IsAvailable() {
		t.Skip("postgres binaries not available")
	}

	dataDir := t.TempDir()
	sqlitePath := filepath.Join(dataDir, "store")
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}
	// Add some data
	task := model.Task{
		ID:    "task-1",
		Title: "CLI Migration Task",
		Body:  "Testing CLI migration flow",
	}
	if err := store.PutTask(context.Background(), task); err != nil {
		t.Fatalf("putting task: %v", err)
	}
	db.Close()

	var buf bytes.Buffer
	err = runUpgradePostgres(context.Background(), []string{
		"-data-dir=" + dataDir,
		"-sqlite-path=" + sqlitePath,
	}, &buf)
	if err != nil {
		t.Fatalf("runUpgradePostgres failed: %v", err)
	}

	if !strings.Contains(buf.String(), "Successfully upgraded database to PostgreSQL") {
		t.Errorf("output missing success string: %s", buf.String())
	}

	cfg, err := daemon.LoadDatabaseConfig(dataDir)
	if err != nil {
		t.Fatalf("LoadDatabaseConfig: %v", err)
	}
	if cfg == nil || cfg.Driver != "postgres" {
		t.Fatalf("expected saved postgres config, got %+v", cfg)
	}
}

func TestUpgradePostgresCLINoDataAndNoSaveConfig(t *testing.T) {
	if !postgres.IsAvailable() {
		t.Skip("postgres binaries not available")
	}

	dataDir := t.TempDir()
	sqlitePath := filepath.Join(dataDir, "store")
	db, err := grainsqlite.Open(grainsqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}
	task := model.Task{
		ID:    "task-no-data",
		Title: "Should Not Migrate",
	}
	if err := store.PutTask(context.Background(), task); err != nil {
		t.Fatalf("putting task: %v", err)
	}
	db.Close()

	var buf bytes.Buffer
	err = runUpgradePostgres(context.Background(), []string{
		"-data-dir=" + dataDir,
		"-sqlite-path=" + sqlitePath,
		"-no-data",
		"-no-save-config",
	}, &buf)
	if err != nil {
		t.Fatalf("runUpgradePostgres failed: %v", err)
	}

	if !strings.Contains(buf.String(), "Successfully upgraded database to PostgreSQL") {
		t.Errorf("output missing success string: %s", buf.String())
	}

	// db-config.json should NOT exist because -no-save-config was set
	cfg, err := postgres.ReadDatabaseConfig(dataDir)
	if err != nil {
		t.Fatalf("ReadDatabaseConfig: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected no saved config with -no-save-config, got %+v", cfg)
	}
}
