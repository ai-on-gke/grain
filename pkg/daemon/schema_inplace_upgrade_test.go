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

// Plain binary swap across a schema bump (gap G2 of
// docs/test-runs/2026-11-01-schema-upgrade-65-78.md): the store is at or past
// model.V2BaselineSchemaVersion (64 or 65), its internal grain/config dump is
// at that same schema, and the new build starts directly without
// `grain state prepare-upgrade` or `grain state reformat`.
//
// Init migrates the store in place, and openStoreForSchemaUpgrade hands the
// schema it migrated from to loadStateRepo so migrateStateRepo rewrites the
// matching dump -- while still refusing a dump stamped at any other schema or
// a dump met over an empty database (the restore case).

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/model/v2baseline"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// buildStoreAtSchema creates a real SQLite store under dataDir/store at
// targetVersion by laying down the frozen 2.0 baseline (schema 27) and running
// every model.Migration up to targetVersion.
func buildStoreAtSchema(t *testing.T, dataDir string, targetVersion int, seed func(t *testing.T, db *sql.DB)) {
	t.Helper()
	if targetVersion < model.V2BaselineSchemaVersion || targetVersion >= model.SchemaVersion {
		t.Fatalf("targetVersion %d outside [%d, %d)", targetVersion, model.V2BaselineSchemaVersion, model.SchemaVersion)
	}
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(dataDir, "store")))
	if err != nil {
		t.Fatalf("opening sqlite store: %v", err)
	}
	defer db.Close()

	for _, stmt := range v2baseline.Split(v2baseline.File(t, "schema.sqlite.sql")) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("applying v2 baseline DDL: %v\n%s", err, stmt)
		}
	}
	for _, stmt := range v2baseline.Split(v2baseline.File(t, "rows.sql")) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("applying v2 baseline rows: %v\n%s", err, stmt)
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, "SELECT `name` FROM `sqlite_master` WHERE `type` = 'view'")
	if err != nil {
		t.Fatal(err)
	}
	var views []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		views = append(views, name)
	}
	rows.Close()
	for _, v := range views {
		if _, err := tx.ExecContext(ctx, "DROP VIEW IF EXISTS `"+v+"`"); err != nil {
			t.Fatal(err)
		}
	}

	for _, m := range model.Migrations {
		if m.Version > targetVersion {
			continue
		}
		for _, stmt := range m.Statements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				if strings.Contains(strings.ToUpper(stmt), " ADD COLUMN ") &&
					strings.Contains(strings.ToLower(err.Error()), "duplicate column name:") {
					continue
				}
				t.Fatalf("migration %d statement: %v\n%s", m.Version, err, stmt)
			}
		}
		if m.Up != nil {
			if err := m.Up(ctx, tx); err != nil {
				t.Fatalf("migration %d Up: %v", m.Version, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE `grain_schema` SET `version` = ? WHERE `id` = 1", targetVersion); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(t, db)
	}
}

// commitDumpAtSchema initialises dataDir's internal grain/config repository
// with a dump stamped at schemaVersion. When fixtureDir is non-empty its files
// are copied in; otherwise db's config tables are exported.
func commitDumpAtSchema(t *testing.T, dataDir string, db *sql.DB, fixtureDir string, schemaVersion int) *staterepo.Repo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("OpenStateRepo: %v", err)
	}
	if fixtureDir != "" {
		if err := os.CopyFS(repo.Dir(), os.DirFS(fixtureDir)); err != nil {
			t.Fatalf("copying %s into %s: %v", fixtureDir, repo.Dir(), err)
		}
	} else {
		if err := staterepo.ExportConfig(ctx, db, repo.Dir()); err != nil {
			t.Fatalf("ExportConfig: %v", err)
		}
	}
	if err := staterepo.WriteSchemaVersion(repo.Dir(), schemaVersion); err != nil {
		t.Fatalf("WriteSchemaVersion: %v", err)
	}
	committed, err := repo.Commit(ctx, "Export grain state at schema "+strconv.Itoa(schemaVersion))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !committed {
		t.Fatalf("expected initial dump at schema %d to produce a commit", schemaVersion)
	}
	return repo
}

func headCommitMessage(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%B").CombinedOutput()
	if err != nil {
		t.Fatalf("git log in %s: %v\n%s", dir, err, out)
	}
	return string(out)
}

func seedSchema65Deployment(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		"UPDATE `grain_config` SET " +
			"`default_capabilities` = 'memory,dispatch-control,deployment-health,db-read', " +
			"`task_branch_template` = '" + model.UnsluggedTaskBranchTemplate + "' " +
			"WHERE `id` = 1",
		"INSERT OR REPLACE INTO `repo_config` (`owner`, `name`, `default_capabilities`, `issue_import_enabled`, `issue_import_interval_seconds`) " +
			"VALUES ('acme', 'widgets', 'grain-log', 1, 300)",
		"UPDATE `task` SET `title` = 'Task written at schema 65' WHERE `id` = 'v2-1'",
		"INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('v2-tpl', 'memory', ''), ('v2-tpl', 'db-read', '')",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seeding schema-65 store (%s): %v", stmt, err)
		}
	}
}

func TestPlainBinarySwapFromSchema64StoreAndDumpMigratesBoth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()

	fixture := filepath.Join("..", "staterepo", "testdata", "upgrade-schema-64")
	buildStoreAtSchema(t, dataDir, 64, func(t *testing.T, db *sql.DB) {
		for _, stmt := range []string{
			"UPDATE `grain_config` SET " +
				"`default_capabilities` = 'team-broadcast,dispatch-control,memory,deployment-health,db-read', " +
				"`task_branch_template` = '" + model.UnsluggedTaskBranchTemplate + "' " +
				"WHERE `id` = 1",
			"UPDATE `task` SET `title` = 'Task written at schema 64' WHERE `id` = 'v2-1'",
			"INSERT INTO `template_grant` (`template_id`, `capability`, `via`) VALUES ('v2-tpl', 'memory', ''), ('v2-tpl', 'db-read', '')",
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("seeding schema-64 store: %v", err)
			}
		}
	})
	commitDumpAtSchema(t, dataDir, nil, fixture, 64)

	store, db, migrated, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("openStoreForSchemaUpgrade: %v", err)
	}
	defer db.Close()
	if migrated.from != 64 || migrated.note {
		t.Fatalf("migrated = %+v, want {note:false from:64}", migrated)
	}

	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatalf("OpenStateRepo: %v", err)
	}
	if err := loadStateRepo(ctx, dataDir, repo, db, migrated); err != nil {
		t.Fatalf("loadStateRepo refused a schema-64 dump whose store was just migrated from 64: %v", err)
	}
	discardSchemaUpgrade(dataDir)

	gotDumpSchema, err := staterepo.ReadSchemaVersion(repo.Dir())
	if err != nil {
		t.Fatalf("ReadSchemaVersion: %v", err)
	}
	if gotDumpSchema != model.SchemaVersion {
		t.Errorf("state-repo schema-version = %d, want %d", gotDumpSchema, model.SchemaVersion)
	}
	if msg := headCommitMessage(t, repo.Dir()); !strings.Contains(msg, "Migrate grain state to schema "+strconv.Itoa(model.SchemaVersion)) {
		t.Errorf("state-repo commit message = %q, want migration commit", msg)
	}

	task, err := store.GetTask(ctx, "v2-1")
	if err != nil {
		t.Fatalf("task v2-1 lost across in-place migration: %v", err)
	}
	if task.Title != "Task written at schema 64" {
		t.Errorf("task title = %q", task.Title)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TaskBranchTemplate != model.DefaultTaskBranchTemplate {
		t.Errorf("task_branch_template = %q, want %q", cfg.TaskBranchTemplate, model.DefaultTaskBranchTemplate)
	}
	for _, want := range []string{"api:memory", "api:dispatch-control", model.GrainHealthGrant, model.GrainSQLReadGrant} {
		if !slices.Contains(cfg.DefaultCapabilities, want) {
			t.Errorf("default_capabilities = %v, missing migrated grant %q", cfg.DefaultCapabilities, want)
		}
	}
	if _, err := os.Stat(inPlaceMigrationFile(dataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("in-place migration marker %s still present after discard (%v)", inPlaceMigrationFile(dataDir), err)
	}
}

func TestDaemonRunStartsAcrossPlainBinarySwapFromSchema65(t *testing.T) {
	// Not parallel: Run touches package-level globals (SetDefaultGitHost,
	// ReconcilerDown, LivePullRequests), which we restore on cleanup.
	t.Cleanup(func() {
		model.SetDefaultGitHost("")
		ReconcilerDown.Store(false)
		LivePullRequests.Store(nil)
		liveGitHubCalls.Store(nil)
		liveRepoNames.Store(nil)
	})

	dataDir := t.TempDir()
	buildStoreAtSchema(t, dataDir, 65, func(t *testing.T, db *sql.DB) {
		seedSchema65Deployment(t, db)
		commitDumpAtSchema(t, dataDir, db, "", 65)
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	uiAddr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- Run(ctx, Config{
			DataDir:                dataDir,
			SandboxDir:             t.TempDir(),
			MaxWorkers:             1,
			PollInterval:           time.Hour,
			UiAddr:                 uiAddr,
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

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-runErr:
			// Put back for the deferred wait.
			runErr <- err
			t.Fatalf("daemon exited instead of starting on schema-65 store and dump: %v", err)
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

	gotDumpSchema, err := staterepo.ReadSchemaVersion(StateRepoDir(dataDir))
	if err != nil {
		t.Fatalf("ReadSchemaVersion: %v", err)
	}
	if gotDumpSchema != model.SchemaVersion {
		t.Errorf("state-repo schema-version = %d, want %d", gotDumpSchema, model.SchemaVersion)
	}
	if msg := headCommitMessage(t, StateRepoDir(dataDir)); !strings.Contains(msg, "Migrate grain state to schema "+strconv.Itoa(model.SchemaVersion)) {
		t.Errorf("state-repo commit message = %q, want migration commit", msg)
	}
	if _, err := os.Stat(inPlaceMigrationFile(dataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("in-place migration marker %s still present after startup (%v)", inPlaceMigrationFile(dataDir), err)
	}
}

func TestPlainBinarySwapInterruptedAfterStoreMigrationFinishesOnNextStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()

	buildStoreAtSchema(t, dataDir, 65, func(t *testing.T, db *sql.DB) {
		seedSchema65Deployment(t, db)
		commitDumpAtSchema(t, dataDir, db, "", 65)
	})

	// First start migrates the store in place (65 -> SchemaVersion) and stops
	// before loadStateRepo and discardSchemaUpgrade run.
	_, firstDB, firstMig, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("first openStoreForSchemaUpgrade: %v", err)
	}
	if firstMig.from != 65 {
		t.Fatalf("first migrated.from = %d, want 65", firstMig.from)
	}
	if err := firstDB.Close(); err != nil {
		t.Fatal(err)
	}

	// Second start sees a store already at SchemaVersion, recovers the pending
	// in-place migration from store-migrated.json, and brings the 65 dump across.
	_, secondDB, secondMig, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
	if err != nil {
		t.Fatalf("second openStoreForSchemaUpgrade: %v", err)
	}
	defer secondDB.Close()
	if secondMig.from != 65 {
		t.Fatalf("second migrated.from = %d, want 65 recovered from marker", secondMig.from)
	}
	repo, err := OpenStateRepo(ctx, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := loadStateRepo(ctx, dataDir, repo, secondDB, secondMig); err != nil {
		t.Fatalf("second start loadStateRepo: %v", err)
	}
	discardSchemaUpgrade(dataDir)

	if got, err := staterepo.ReadSchemaVersion(repo.Dir()); err != nil || got != model.SchemaVersion {
		t.Errorf("state-repo schema = %d (err %v), want %d", got, err, model.SchemaVersion)
	}
	if _, err := os.Stat(inPlaceMigrationFile(dataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("marker %s still present after completion: %v", inPlaceMigrationFile(dataDir), err)
	}
}

func TestInPlaceStoreMigrationDoesNotAuthoriseDumpFromDifferentSchemaOrRestore(t *testing.T) {
	t.Parallel()

	t.Run("dump at another schema is still refused", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		dataDir := t.TempDir()
		fixture64 := filepath.Join("..", "staterepo", "testdata", "upgrade-schema-64")
		buildStoreAtSchema(t, dataDir, 65, seedSchema65Deployment)
		commitDumpAtSchema(t, dataDir, nil, fixture64, 64)

		_, db, migrated, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		repo, err := OpenStateRepo(ctx, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		err = loadStateRepo(ctx, dataDir, repo, db, migrated)
		if !errors.Is(err, staterepo.ErrSchemaTooOld) {
			t.Fatalf("loadStateRepo err = %v, want ErrSchemaTooOld when store migrated from 65 but dump is at 64", err)
		}
		if got, _ := staterepo.ReadSchemaVersion(repo.Dir()); got != 64 {
			t.Errorf("dump schema-version changed to %d despite refusal, want 64", got)
		}
	})

	t.Run("empty store over old dump (restore) is still refused", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		dataDir := t.TempDir()
		fixture64 := filepath.Join("..", "staterepo", "testdata", "upgrade-schema-64")
		commitDumpAtSchema(t, dataDir, nil, fixture64, 64)

		_, db, migrated, err := openStoreForSchemaUpgrade(ctx, "sqlite", "", dataDir)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if migrated.any() {
			t.Fatalf("empty store reported migration %+v, want none", migrated)
		}
		repo, err := OpenStateRepo(ctx, dataDir)
		if err != nil {
			t.Fatal(err)
		}
		err = loadStateRepo(ctx, dataDir, repo, db, migrated)
		if !errors.Is(err, staterepo.ErrSchemaTooOld) {
			t.Fatalf("loadStateRepo err = %v, want ErrSchemaTooOld on an empty store meeting a schema-64 dump", err)
		}
		if got, _ := staterepo.ReadSchemaVersion(repo.Dir()); got != 64 {
			t.Errorf("dump schema-version changed to %d despite refusal, want 64", got)
		}
	})
}
