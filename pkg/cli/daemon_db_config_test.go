package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"

	"github.com/bwsalmon/grain/pkg/model/postgres"
)

func TestParseDaemonArgsDatabaseFlags(t *testing.T) {
	// Default flags (sqlite, empty DSN)
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/var/lib/grain/data", "-sandbox-dir=/var/lib/grain/data/scratches"})
	if err != nil {
		t.Fatalf("parseDaemonArgs default: %v", err)
	}
	if cfg.DbDriver != "sqlite" {
		t.Errorf("default dbDriver = %q, want %q", cfg.DbDriver, "sqlite")
	}
	if cfg.DbDSN != "" {
		t.Errorf("default dbDSN = %q, want empty", cfg.DbDSN)
	}

	// Explicit flags for postgres
	dsn := "postgres://user:pass@127.0.0.1:5432/graindb?sslmode=disable"
	cfg, _, _, err = parseDaemonArgs([]string{
		"-data-dir=/var/lib/grain/data",
		"-sandbox-dir=/var/lib/grain/data/scratches",
		"-db-driver=postgres",
		"-db-dsn=" + dsn,
	})
	if err != nil {
		t.Fatalf("parseDaemonArgs with postgres flags: %v", err)
	}
	if cfg.DbDriver != "postgres" {
		t.Errorf("dbDriver = %q, want postgres", cfg.DbDriver)
	}
	if cfg.DbDSN != dsn {
		t.Errorf("dbDSN = %q, want %q", cfg.DbDSN, dsn)
	}
}

func TestParseDaemonArgsDatabaseEnvVars(t *testing.T) {
	t.Setenv("GRAIN_DB_DRIVER", "postgres")
	dsn := "postgres://user:pass@/graindb?host=/cloudsql/proj:reg:inst"
	t.Setenv("GRAIN_DB_DSN", dsn)

	cfg, _, _, err := parseDaemonArgs([]string{
		"-data-dir=/var/lib/grain/data",
		"-sandbox-dir=/var/lib/grain/data/scratches",
	})
	if err != nil {
		t.Fatalf("parseDaemonArgs with env vars: %v", err)
	}
	if cfg.DbDriver != "postgres" {
		t.Errorf("dbDriver from env = %q, want postgres", cfg.DbDriver)
	}
	if cfg.DbDSN != dsn {
		t.Errorf("dbDSN from env = %q, want %q", cfg.DbDSN, dsn)
	}

	// Explicit flags take precedence over env vars
	cfg, _, _, err = parseDaemonArgs([]string{
		"-data-dir=/var/lib/grain/data",
		"-sandbox-dir=/var/lib/grain/data/scratches",
		"-db-driver=sqlite",
		"-db-dsn=/custom/sqlite/store",
	})
	if err != nil {
		t.Fatalf("parseDaemonArgs with flag overrides: %v", err)
	}
	if cfg.DbDriver != "sqlite" {
		t.Errorf("dbDriver flag override = %q, want sqlite", cfg.DbDriver)
	}
	if cfg.DbDSN != "/custom/sqlite/store" {
		t.Errorf("dbDSN flag override = %q, want /custom/sqlite/store", cfg.DbDSN)
	}
}

func TestOpenStoreSqliteDefaultAndCustom(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()

	// Default sqlite DSN under dataDir
	store, db, err := daemon.OpenStore("sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStore default sqlite: %v", err)
	}
	defer db.Close()

	if _, err := os.Stat(filepath.Join(dataDir, "store")); err != nil {
		t.Errorf("expected sqlite store file to exist: %v", err)
	}

	// Store initialization check
	if err := store.Init(context.Background()); err != nil {
		t.Errorf("store.Init: %v", err)
	}
}

func TestOpenStorePostgresEmbeddedOrRequiresDSN(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, db, err := daemon.OpenStore("postgres", "", dataDir)
	if postgres.IsAvailable() {
		if err != nil {
			t.Fatalf("openStore(postgres, \"\") with embedded postgres available failed: %v", err)
		}
		defer db.Close()
		if err := store.Init(context.Background()); err != nil {
			t.Fatalf("store.Init: %v", err)
		}
	} else {
		if err == nil {
			t.Fatal("openStore(postgres, \"\") expected error when postgres is not available, got nil")
		}
	}
}

func TestOpenStoreUnsupportedDriver(t *testing.T) {
	t.Parallel()
	_, _, err := daemon.OpenStore("invalid-driver", "foo", t.TempDir())
	if err == nil {
		t.Fatal("openStore(invalid-driver) expected error, got nil")
	}
}
