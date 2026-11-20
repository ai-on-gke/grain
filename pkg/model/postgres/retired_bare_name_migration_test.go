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

package postgres_test

import (
	"context"
	"database/sql"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// Migration 153 on PostgreSQL: the bare retired names written by hand
// into the default and CI capability lists, which no page shows
// (docs/test-runs/2026-11-14-retired-capability-aliases.md, F4), are
// gone afterwards, a registered alias's rewritten to what it stands for.
// Not parallel: it registers an alias for the whole process.
func TestMigration153SettlesBareRetiredNamesOnPostgres(t *testing.T) {
	store, db, ctx := openStore(t)
	migrate153(t, ctx, store, db)
}

// Migration 153 on SQLite, the same rows.
func TestMigration153SettlesBareRetiredNamesOnSQLite(t *testing.T) {
	store, ctx := openStoreIn(t, t.TempDir())
	migrate153(t, ctx, store, store.DB())
}

const (
	widgetAlias      = "acme-widget-key"
	widgetAliasGrant = "https://grain.dev/capabilities/acme-widget#key={}"
)

func migrate153(t *testing.T, ctx context.Context, store *model.Store, db *sql.DB) {
	t.Helper()
	t.Cleanup(model.RegisterLegacyGrantAlias(widgetAlias, widgetAliasGrant))
	if err := store.PutConfig(ctx, model.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE `grain_config` SET `default_capabilities` = ? WHERE `id` = 1",
		"read-issues,grain-ci,local-scripts:deploy,"+model.GrainCIGrant); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO `repo_config` (`owner`, `name`, `default_capabilities`, `ci_capabilities`) VALUES (?, ?, ?, ?)",
		"acme", "widgets", widgetAlias+",self-debug", "grain-ci,self-debug,"+model.GrainCIGrant+",grain-log"); err != nil {
		t.Fatal(err)
	}
	rewindSchemaTo(t, ctx, db, 152)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-152 store: %v", err)
	}

	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// local-scripts:<name> is the daemon's to settle once it has read
	// the declarations (migrateLocalScripts), so it stays.
	if want := []string{"local-scripts:deploy", model.GrainCIGrant}; !slices.Equal(cfg.DefaultCapabilities, want) {
		t.Errorf("deployment default capabilities = %v, want %v", cfg.DefaultCapabilities, want)
	}
	rc, err := store.GetRepoConfig(ctx, model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || rc == nil {
		t.Fatalf("reading acme/widgets: (%v, %v)", rc, err)
	}
	if want := []string{widgetAliasGrant}; !slices.Equal(rc.DefaultCapabilities, want) {
		t.Errorf("acme/widgets default capabilities = %v, want %v", rc.DefaultCapabilities, want)
	}
	if want := []string{model.GrainCIGrant}; !slices.Equal(rc.CICapabilities, want) {
		t.Errorf("acme/widgets CI capabilities = %v, want %v", rc.CICapabilities, want)
	}

	// Running it again, as an import of an older dump into this store
	// would, changes nothing.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := model.MigrateRetiredBareCapabilityNames(ctx, tx); err != nil {
		t.Fatalf("migrating again: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	again, err := store.GetRepoConfig(ctx, model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || !slices.Equal(again.CICapabilities, rc.CICapabilities) || !slices.Equal(again.DefaultCapabilities, rc.DefaultCapabilities) {
		t.Errorf("acme/widgets after a second run = (%+v, %v), want it unchanged", again, err)
	}
}

// On a running daemon -- a grain/config dump stamped before 153 applied
// after migrateLocalScripts has set the runtime expansions -- a
// local-scripts:<name> the daemon has worked out is rewritten to the
// host-script grant it became, not removed with the names that grant
// nothing.
func TestMigration153RewritesARuntimeRetirementItKnows(t *testing.T) {
	store, ctx := openStoreIn(t, t.TempDir())
	db := store.DB()
	const deploy = `https://grain.dev/capabilities/host-script#deploy={}`
	t.Cleanup(model.SetRuntimeGrantExpansions(map[string][]string{"local-scripts:deploy": {deploy}}))
	if _, err := db.ExecContext(ctx, "INSERT INTO `repo_config` (`owner`, `name`, `default_capabilities`, `ci_capabilities`) VALUES (?, ?, ?, ?)",
		"acme", "widgets", "local-scripts:deploy,local-scripts:other", "local-scripts:deploy,self-debug,"+deploy); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := model.ApplyDataMigrations(ctx, tx, 152); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rc, err := store.GetRepoConfig(ctx, model.RepoRef{Owner: "acme", Name: "widgets"})
	if err != nil || rc == nil {
		t.Fatalf("reading acme/widgets: (%v, %v)", rc, err)
	}
	// local-scripts:other has no expansion: the daemon's to settle.
	if want := []string{deploy, "local-scripts:other"}; !slices.Equal(rc.DefaultCapabilities, want) {
		t.Errorf("acme/widgets default capabilities = %v, want %v", rc.DefaultCapabilities, want)
	}
	if want := []string{deploy}; !slices.Equal(rc.CICapabilities, want) {
		t.Errorf("acme/widgets CI capabilities = %v, want %v", rc.CICapabilities, want)
	}
}
