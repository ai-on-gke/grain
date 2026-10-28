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
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// The state repository's export and import, end to end on Postgres:
// every settings table the store can fill, exported, edited the way a
// pull request edits it -- a value changed, a column left out -- imported
// back, and exported again. grain-eu-host ran 36 hours with no export
// because two of the queries on this path were SQLite-only
// (`"id" IS $2`, `quote(...)` and `col IS ”`), and nothing ran them
// against Postgres. This does, on every settings table at once.
func TestStateRepoRoundTripOverEverySettingsTable(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	ctx := context.Background()
	store, db := postgrestest.Open(t)
	const oauthSecret = "GOCSPX-roundtrip-0123456789"
	seedSettings(t, ctx, store, oauthSecret)

	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	// Every settings table the seed filled has a file in the export.
	for _, table := range []string{"grain_config", "repo_config", "template", "schedule", "suite", "suite_item"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath(table)))); err != nil {
			t.Errorf("the export has no file for %s: %v", table, err)
		}
	}

	// The edit: a value changed and a column left out of grain_config,
	// which is what makes the import ask what the missing column held
	// (resets.go) -- the query that failed with `syntax error at or
	// near "''"`.
	path := filepath.Join(dir, staterepo.TablesDir, "grain_config.json")
	editJSONRows(t, path, func(row map[string]any) {
		row["max_workers"] = 7
		delete(row, "environment_name")
	})

	if err := staterepo.ImportTables(ctx, db, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing: %v", err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxWorkers != 7 {
		t.Errorf("max_workers = %d, want the edited 7", cfg.MaxWorkers)
	}
	if got := configColumn(t, ctx, store, "google_oauth_client_secret"); got != oauthSecret {
		t.Errorf("the withheld client secret = %q after the import, want the running one", got)
	}

	// Re-exported, every file is what was imported: nothing on this path
	// lost or reshaped a row on the way through Postgres.
	again := t.TempDir()
	if err := staterepo.ExportConfig(ctx, db, again); err != nil {
		t.Fatalf("re-exporting: %v", err)
	}
	for _, table := range staterepo.SettingsTables {
		rel := filepath.FromSlash(staterepo.DumpPath(table))
		want, werr := os.ReadFile(filepath.Join(dir, rel))
		got, gerr := os.ReadFile(filepath.Join(again, rel))
		if os.IsNotExist(werr) && os.IsNotExist(gerr) {
			continue
		}
		if table == "grain_config" {
			// The left-out column comes back at its default, which is the
			// one difference the edit asked for.
			continue
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s changed on the way through Postgres:\nimported:\n%s\nre-exported:\n%s", rel, want, got)
		}
	}
}

func seedSettings(t *testing.T, ctx context.Context, store *model.Store, oauthSecret string) {
	t.Helper()
	now := time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)
	cfg := model.DefaultConfig()
	cfg.EnvironmentName = "grain-eu-host"
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	setOAuthColumns(t, ctx, store, "client-id.apps.googleusercontent.com", oauthSecret)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, DefaultCapabilities: []string{"grain-ci"}}); err != nil {
		t.Fatal(err)
	}
	id := "tpl-1"
	for _, tid := range []string{"tpl-1", "tpl-2"} {
		if err := store.PutTemplate(ctx, model.Template{ID: tid, Name: tid, Title: "Title " + tid, Body: "Run it.", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, sid := range []string{"suite-1", "suite-2"} {
		if err := store.PutSuite(ctx, model.Suite{
			ID: sid, Name: sid, Mode: model.SuiteCount, Count: 1,
			Items: []model.SuiteItem{{TemplateID: "tpl-1"}}, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutSchedule(ctx, model.Schedule{
		ID: "sched-1", Title: "Nightly", TemplateID: &id, Target: repo,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 9 * 60},
		Overlap:    model.OverlapSkip, Enabled: true, NextRunAt: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func editJSONRows(t *testing.T, path string, edit func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		edit(row)
	}
	out, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
}
