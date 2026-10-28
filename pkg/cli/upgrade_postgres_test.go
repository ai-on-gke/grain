package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	grainsqlite "github.com/ai-on-gke/grain/pkg/model/sqlite"
)

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
