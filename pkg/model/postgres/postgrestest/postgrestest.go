// Package postgrestest provides temporary isolated PostgreSQL schemas
// for integration tests of model.Store against PostgreSQL.
package postgrestest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/postgres"
)

const defaultTestURL = "postgres://postgres:postgres@localhost:5432/graintest?sslmode=disable"

// RequireEnv names the environment variable that makes a missing
// PostgreSQL a test failure rather than a skip. CI sets it on every job
// that installs PostgreSQL, so the store tests cannot silently stop
// running there because the server did not come up.
const RequireEnv = "GRAIN_REQUIRE_POSTGRES"

var (
	adminDBOnce  sync.Once
	adminDB      *sql.DB
	adminDBErr   error
	activeConfig postgres.Config

	// embeddedStop and embeddedDir are set when getAdminDB started a
	// server of its own, so Shutdown can stop it and remove its files.
	embeddedStop func() error
	embeddedDir  string
)

// Required reports whether RequireEnv is set.
func Required() bool {
	v := os.Getenv(RequireEnv)
	return v != "" && v != "0" && v != "false"
}

// skip skips t, or fails it when RequireEnv is set.
func skip(t testing.TB, format string, args ...any) {
	t.Helper()
	if Required() {
		t.Fatalf(format+" (%s is set, so postgres is required)", append(args, RequireEnv)...)
	}
	t.Skipf(format, args...)
}

func getAdminDB() (*sql.DB, postgres.Config, error) {
	adminDBOnce.Do(func() {
		testURL := os.Getenv("GRAIN_POSTGRES_TEST_URL")
		if testURL != "" {
			activeConfig = postgres.Config{
				URL:          testURL,
				MaxOpenConns: 50,
				MaxIdleConns: 50,
			}
			adminDB, adminDBErr = postgres.Open(activeConfig)
			return
		}

		if postgres.IsAvailable() {
			testDir, err := os.MkdirTemp("", "grain-postgres-test-*")
			if err != nil {
				adminDBErr = fmt.Errorf("creating temp dir for postgres test: %w", err)
				return
			}
			cfg, stop, err := postgres.EnsureRunning(context.Background(), testDir, postgres.WithoutCrashSafety())
			if err != nil {
				_ = os.RemoveAll(testDir)
				adminDBErr = fmt.Errorf("starting embedded postgres for tests: %w", err)
				return
			}
			embeddedStop, embeddedDir = stop, testDir
			cfg.MaxOpenConns = 50
			cfg.MaxIdleConns = 50
			activeConfig = cfg
			adminDB, adminDBErr = postgres.Open(activeConfig)
			return
		}

		activeConfig = postgres.Config{
			URL:          defaultTestURL,
			MaxOpenConns: 50,
			MaxIdleConns: 50,
		}
		adminDB, adminDBErr = postgres.Open(activeConfig)
	})
	return adminDB, activeConfig, adminDBErr
}

// Shutdown closes the shared admin connection and, when this package
// started an embedded server, stops it and removes its data directory.
// Without it the server outlives the test binary. Call it from TestMain
// after m.Run, or use Main, which does exactly that.
func Shutdown() {
	if adminDB != nil {
		_ = adminDB.Close()
	}
	if embeddedStop != nil {
		_ = embeddedStop()
		embeddedStop = nil
	}
	if embeddedDir != "" {
		_ = os.RemoveAll(embeddedDir)
		embeddedDir = ""
	}
}

// Main runs m and then Shutdown, and exits with m's status. A package
// whose tests call Open should use it as its TestMain:
//
//	func TestMain(m *testing.M) { postgrestest.Main(m) }
func Main(m *testing.M) {
	code := m.Run()
	Shutdown()
	os.Exit(code)
}

// SkipUnlessBinaries skips t when no PostgreSQL binaries are installed
// for postgres.EnsureRunning to start, or fails it under RequireEnv.
func SkipUnlessBinaries(t testing.TB) {
	t.Helper()
	if !postgres.IsAvailable() {
		skip(t, "postgres binaries not available")
	}
}

// SkipUnlessAvailable skips t when there is neither a configured test
// server (GRAIN_POSTGRES_TEST_URL) nor binaries to start one, or fails it
// under RequireEnv.
func SkipUnlessAvailable(t testing.TB) {
	t.Helper()
	if !postgres.IsAvailable() && os.Getenv("GRAIN_POSTGRES_TEST_URL") == "" {
		skip(t, "PostgreSQL is not available")
	}
}

// Config returns the connection settings of the test server Open uses,
// pointed at its default database rather than an isolated schema. It
// skips t (or fails it, under RequireEnv) when no server is reachable.
func Config(t testing.TB) postgres.Config {
	t.Helper()
	_, cfg, err := getAdminDB()
	if err != nil {
		skip(t, "skipping postgres test: connecting to admin database: %v", err)
	}
	return cfg
}

// Open connects to the test PostgreSQL database, creates an isolated schema
// for this test, applies the full model schema, and registers a cleanup
// handler to drop the schema when the test finishes.
func Open(t testing.TB) (*model.Store, *sql.DB) {
	t.Helper()
	db := OpenEmpty(t)
	if db == nil {
		return nil, nil
	}

	// Initialize tables and views in a single batch
	ctx := context.Background()
	combinedDDL := strings.Join(postgres.Statements(), ";\n")
	if _, err := db.ExecContext(ctx, combinedDDL); err != nil {
		t.Fatalf("applying postgres schema: %v", err)
	}

	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("initializing postgres model store: %v", err)
	}

	return store, db
}

// OpenEmpty is Open without the schema: an isolated, empty schema for a
// test that lays down a store of its own -- an older build's, say
// (pkg/model/v2baseline) -- before Init sees it.
func OpenEmpty(t testing.TB) *sql.DB {
	t.Helper()

	admin, baseCfg, err := getAdminDB()
	if err != nil {
		skip(t, "skipping postgres test: connecting to admin database: %v", err)
		return nil
	}

	// Generate a unique schema name
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("generating random schema name: %v", err)
	}
	schemaName := fmt.Sprintf("test_%d_%s", time.Now().UnixNano(), hex.EncodeToString(b))

	ctx := context.Background()
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA "%s"`, schemaName)); err != nil {
		skip(t, "skipping postgres test: creating test schema %s: %v", schemaName, err)
		return nil
	}

	// Open test connection with search_path set to the isolated schema
	cfg := baseCfg
	cfg.SearchPath = fmt.Sprintf(`"%s", public`, schemaName)
	cfg.MaxOpenConns = 10
	cfg.MaxIdleConns = 10

	db, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("opening test db with schema %s: %v", schemaName, err)
	}

	t.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.ExecContext(context.Background(), fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schemaName))
	})

	return db
}
