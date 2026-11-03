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
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// ResolveDatabase says which database a tool handed only dataDir should
// open, the way the daemon on it chose one: $GRAIN_DB_DRIVER and
// $GRAIN_DB_DSN, then $GRAIN_POSTGRES_URL or $DATABASE_URL, then the
// db-config.json the daemon recorded in dataDir (RecordDatabaseConfig).
// An empty driver means none of them said anything: sqlite under
// dataDir/store.
func ResolveDatabase(dataDir string) (driver, dsn string, err error) {
	driver = os.Getenv("GRAIN_DB_DRIVER")
	dsn = os.Getenv("GRAIN_DB_DSN")
	if dsn == "" {
		if u := os.Getenv("GRAIN_POSTGRES_URL"); u != "" {
			dsn = u
			driver = "postgres"
		} else if u := os.Getenv("DATABASE_URL"); u != "" {
			dsn = u
			driver = "postgres"
		}
	}
	if driver == "" {
		saved, err := LoadDatabaseConfig(dataDir)
		if err != nil {
			return "", "", err
		}
		if saved != nil && saved.Driver != "" {
			driver = saved.Driver
			if dsn == "" {
				dsn = saved.DSN
			}
		}
	}
	return driver, dsn, nil
}

// HasSQLiteStore reports whether dataDir already holds the SQLite store
// OpenStore would open for it with no driver and no DSN. A tool that
// only writes into an existing store checks this first, so that a data
// dir whose daemon runs on another database is not left holding an
// empty one.
func HasSQLiteStore(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, "store", "grain.db"))
	return err == nil
}

// RecordDatabaseConfig writes the Postgres database a daemon was started
// on into dataDir's db-config.json -- the password into the secret store
// beside it -- so that the tools that take only -data-dir (`grain
// connections import`, `grain state sync`, `grain export`, `grain
// backup`) open the same store rather than falling back to SQLite. A
// config already recording the same database is left as it is, and
// SQLite, the default with nothing recorded, is not written at all.
func RecordDatabaseConfig(dataDir, driver, dsn string) error {
	if dataDir == "" || driver != "postgres" {
		return nil
	}
	if saved, err := LoadDatabaseConfig(dataDir); err == nil && saved != nil && saved.Driver == driver && saved.DSN == dsn {
		return nil
	}
	return postgres.SaveDatabaseConfig(dataDir, driver, dsn, OpenSecrets(dataDir))
}

func OpenRawDB(dataDir string) (*sql.DB, error) {
	driver, dsn, err := ResolveDatabase(dataDir)
	if err != nil {
		return nil, err
	}
	return openRawDB(driver, dsn, dataDir)
}

// openRawDB opens the database driver and dsn name the way OpenStore
// does, without running Init on it -- for asking a store this build may
// not be able to use what it is stamped with, and exporting it.
func openRawDB(driver, dsn, dataDir string) (*sql.DB, error) {
	if driver == "postgres" {
		if dsn != "" {
			return postgres.Open(postgres.Config{URL: dsn})
		}
		if postgres.IsAvailable() {
			cfg, _, err := postgres.EnsureRunning(context.Background(), dataDir)
			if err != nil {
				return nil, fmt.Errorf("starting included postgres database: %w", err)
			}
			return postgres.Open(cfg)
		}
	}
	sqlitePath := dsn
	if sqlitePath == "" {
		sqlitePath = filepath.Join(dataDir, "store")
	}
	return sqlite.Open(sqlite.DefaultConfig(sqlitePath))
}

// openStore returns both the Store and the *sql.DB behind it, so run can
// close the connection on the way out -- model.Store itself has no
// Close, deliberately: it imports no driver, so closing is the caller's job.
//
// When driver is sqlite (or empty), -data-dir/store (or custom DSN) is used.
// When driver is postgres, postgres.Open is called with the provided DSN (or included postgres if available).
// Startup connection readiness retries are performed for database backends
// (such as Cloud SQL Auth Proxy sidecars) that may start up asynchronously.
func OpenStore(driver, dsn, dataDir string) (*model.Store, *sql.DB, error) {
	store, db, _, err := openStore(driver, dsn, dataDir)
	return store, db, err
}

// openStore is OpenStore plus the schema version Init migrated the store in
// place from (0 when Init opened a store already at model.SchemaVersion or
// initialised an empty one).
func openStore(driver, dsn, dataDir string) (*model.Store, *sql.DB, int, error) {
	if driver == "" {
		driver = "sqlite"
	}
	var (
		db  *sql.DB
		err error
	)
	maxRetries := 1
	if driver == "postgres" {
		maxRetries = 15
	}
	const retryInterval = 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		switch driver {
		case "postgres":
			if dsn != "" {
				cfg := postgres.DefaultConfig(dsn)
				db, err = postgres.Open(cfg)
			} else if postgres.IsAvailable() {
				var cfg postgres.Config
				cfg, _, err = postgres.EnsureRunning(context.Background(), dataDir)
				if err == nil {
					db, err = postgres.Open(cfg)
				}
			} else {
				return nil, nil, 0, errors.New("opening the task store: postgres DSN is required")
			}
		case "sqlite":
			sqliteDir := dsn
			if sqliteDir == "" {
				sqliteDir = filepath.Join(dataDir, "store")
			}
			db, err = sqlite.Open(sqlite.DefaultConfig(sqliteDir))
		default:
			return nil, nil, 0, fmt.Errorf("opening the task store: unsupported driver %q", driver)
		}

		if err == nil {
			break
		}

		if attempt < maxRetries {
			log.Printf("grain daemon: database (%s) not ready yet (%v); retrying in %v (attempt %d/%d)...",
				driver, err, retryInterval, attempt, maxRetries)
			time.Sleep(retryInterval)
		}
	}
	if err != nil {
		return nil, nil, 0, fmt.Errorf("opening the task store: %w", err)
	}

	ctx := context.Background()
	store := model.New(db)
	// Read before Init runs: a store stamped in [V2BaselineSchemaVersion,
	// SchemaVersion) is migrated in place by Init and returns stamped at
	// SchemaVersion, indistinguishable afterwards from one that started there
	// or was created fresh (where before is 0).
	before, verr := store.SchemaVersion(ctx)
	if err := store.Init(ctx); err != nil {
		db.Close()
		return nil, nil, 0, fmt.Errorf("applying schema: %w", err)
	}
	migratedFrom := 0
	if verr == nil && before >= model.V2BaselineSchemaVersion && before < model.SchemaVersion {
		migratedFrom = before
		log.Printf("grain: migrated the store in place from schema %d to %d", before, model.SchemaVersion)
	}
	sweepOrphanedSettingsRows(ctx, store)
	liftMisdirectedQuestions(ctx, store)
	return store, db, migratedFrom, nil
}

// liftMisdirectedQuestions un-parks the tasks another task's run parked
// by calling POST /api/tasks/{id}/messages on them, before the agent API
// refused that route on anything but the caller's own task
// (model.Store.ClearMisdirectedQuestions has the history). Here beside
// sweepOrphanedSettingsRows, for its reason: it changes tasks' state, and
// the operator is owed a line naming them. A failure is logged and not
// fatal -- the tasks it would have lifted are only as parked as they were.
func liftMisdirectedQuestions(ctx context.Context, store *model.Store) {
	lifted, err := store.ClearMisdirectedQuestions(ctx, time.Now())
	if err != nil {
		log.Printf("grain: could not lift the questions other tasks' runs posted as these tasks' own "+
			"through POST /api/tasks/{id}/messages: %v -- carrying on", err)
	}
	if len(lifted) > 0 {
		log.Printf("grain: lifted %d question(s) another task's run had posted as the task's own "+
			"through POST /api/tasks/{id}/messages: tasks %s", len(lifted), strings.Join(lifted, ", "))
	}
}

// sweepOrphanedSettingsRows cleans up after the builds that deleted a
// template or a schedule without deleting its companion rows
// (model.Store.DeleteOrphanedSettingsRows has the history), once per
// store open.
//
// Here rather than inside model.Store.Init, which every test and every
// throwaway store also runs, for the one thing this needs that Init
// cannot do: it is a delete, of rows nothing reads but rows all the
// same, and an operator is owed a line saying it happened and how much
// of it there was. pkg/model logs nothing on purpose, so the count comes
// back to the caller and the caller -- this one, the only place grain
// opens the store it actually runs on -- writes the line.
//
// A sweep that fails is logged and not fatal: it deletes nothing on the
// way out (one transaction, rolled back), and refusing to start over a
// tidy-up of rows no read path has ever joined to would be a worse
// failure than the leak it is tidying.
func sweepOrphanedSettingsRows(ctx context.Context, store *model.Store) {
	swept, err := store.DeleteOrphanedSettingsRows(ctx)
	if err != nil {
		log.Printf("grain: could not delete the settings rows left behind by templates and schedules "+
			"deleted before grain deleted their companion rows with them: %v -- carrying on, "+
			"nothing reads those rows", err)
		return
	}
	if n := swept.Total(); n > 0 {
		log.Printf("grain: deleted %d settings rows whose template, suite or schedule was deleted by "+
			"an older build (%s)", n, swept)
	}
}
