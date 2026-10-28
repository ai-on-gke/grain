package postgres_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

func TestEnsureRunningIncludedPostgres(t *testing.T) {
	postgrestest.SkipUnlessBinaries(t)

	dataDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, stop, err := postgres.EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	db, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("opening postgres with returned config: %v", err)
	}
	defer db.Close()

	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("pinging postgres: %v", err)
	}

	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("initializing model store on included postgres: %v", err)
	}

	// Verify idempotency
	cfg2, stop2, err := postgres.EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning second call: %v", err)
	}
	defer stop2()

	if cfg2.Host != cfg.Host {
		t.Errorf("host mismatch: got %s, want %s", cfg2.Host, cfg.Host)
	}

	// The second call found the server running, so its stop must not
	// take the server away from the first caller.
	if err := stop2(); err != nil {
		t.Fatalf("stop from second call: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("server gone after stopping a call that did not start it: %v", err)
	}
}

func showSetting(t *testing.T, ctx context.Context, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRowContext(ctx, "SHOW "+name).Scan(&v); err != nil {
		t.Fatalf("SHOW %s: %v", name, err)
	}
	return v
}

func wantSettings(t *testing.T, ctx context.Context, cfg postgres.Config, want map[string]string) {
	t.Helper()
	db, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer db.Close()
	for name, v := range want {
		if got := showSetting(t, ctx, db, name); got != v {
			t.Errorf("%s = %q, want %q", name, got, v)
		}
	}
}

// TestEnsureRunningIsCrashSafe pins the durability trade documented on
// embeddedTuning: synchronous_commit off, but fsync and full_page_writes
// -- the two settings that can corrupt a cluster -- on, unless a test
// asks otherwise, and then only for the server that call starts.
func TestEnsureRunningIsCrashSafe(t *testing.T) {
	if !postgres.IsAvailable() {
		t.Skip("postgres binaries not available")
	}
	dataDir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg, stop, err := postgres.EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	wantSettings(t, ctx, cfg, map[string]string{"fsync": "on", "full_page_writes": "on", "synchronous_commit": "off"})
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	cfg, stop, err = postgres.EnsureRunning(ctx, dataDir, postgres.WithoutCrashSafety())
	if err != nil {
		t.Fatalf("EnsureRunning WithoutCrashSafety: %v", err)
	}
	wantSettings(t, ctx, cfg, map[string]string{"fsync": "off", "full_page_writes": "off"})
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// The option lived on the command line only: the next plain start
	// of the same cluster is crash-safe again.
	cfg, stop, err = postgres.EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning after WithoutCrashSafety: %v", err)
	}
	defer stop()
	wantSettings(t, ctx, cfg, map[string]string{"fsync": "on", "full_page_writes": "on"})
}

// TestEnsureRunningRepairsLegacyTuning covers a cluster an older grain
// created with fsync=off and full_page_writes=off in postgresql.conf:
// EnsureRunning rewrites that block, whether or not the server is up.
func TestEnsureRunningRepairsLegacyTuning(t *testing.T) {
	if !postgres.IsAvailable() {
		t.Skip("postgres binaries not available")
	}
	const legacy = "\n# Grain embedded tuning\nfsync = off\nsynchronous_commit = off\nfull_page_writes = off\nmax_connections = 200\nshared_buffers = 128MB\n"
	const current = "\n# Grain embedded tuning\nsynchronous_commit = off\nmax_connections = 200\nshared_buffers = 128MB\n"

	dataDir := t.TempDir()
	confPath := filepath.Join(dataDir, "postgres", "postgresql.conf")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg, stop, err := postgres.EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	plantLegacy := func() {
		t.Helper()
		data, err := os.ReadFile(confPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), current) {
			t.Fatalf("new cluster's postgresql.conf lacks the tuning block:\n%s", data)
		}
		if err := os.WriteFile(confPath, []byte(strings.Replace(string(data), current, legacy, 1)), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Stopped cluster: repaired before it starts.
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	plantLegacy()
	cfg, stop, err = postgres.EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning on legacy cluster: %v", err)
	}
	wantSettings(t, ctx, cfg, map[string]string{"fsync": "on", "full_page_writes": "on", "synchronous_commit": "off"})

	// Running cluster with the legacy settings in effect: repaired and
	// reloaded.
	plantLegacy()
	db, err := postgres.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "SELECT pg_reload_conf()"); err != nil {
		t.Fatal(err)
	}
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for showSetting(t, ctx, db, "fsync") != want {
			if time.Now().After(deadline) {
				t.Fatalf("fsync never became %q", want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	waitFor("off")
	if _, _, err := postgres.EnsureRunning(ctx, dataDir); err != nil {
		t.Fatalf("EnsureRunning on running legacy cluster: %v", err)
	}
	waitFor("on")
	data, err := os.ReadFile(confPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "fsync = off") || strings.Contains(string(data), "full_page_writes = off") {
		t.Errorf("postgresql.conf still carries unsafe settings:\n%s", data)
	}
}
