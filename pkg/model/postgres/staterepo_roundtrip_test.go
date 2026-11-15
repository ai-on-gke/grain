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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/postgres"
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

// TestStateRepoImportResyncsPostgresSequences verifies that staterepo.Import
// and staterepo.ImportTables advance PostgreSQL serial sequences past imported
// explicit IDs directly inside importInto, without relying on a later daemon
// restart.
func TestStateRepoImportResyncsPostgresSequences(t *testing.T) {
	postgrestest.SkipUnlessAvailable(t)
	ctx := context.Background()
	srcStore, srcDB := postgrestest.Open(t)
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	by := model.Principal{Kind: model.PrincipalHuman, ID: "operator@example.com"}

	var lastTaskID string
	for range 3 {
		id, err := srcStore.NewTaskID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		lastTaskID = id
		if err := srcStore.PutTask(ctx, model.Task{ID: id, Title: "Task " + id}); err != nil {
			t.Fatal(err)
		}
	}
	maxCommentID, err := srcStore.AddComment(ctx, model.Comment{
		TaskID: lastTaskID, Author: model.Attribution{Actor: by}, Body: "comment", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	maxEventID, err := srcStore.RecordTaskEvent(ctx, model.TaskEvent{
		TaskID: lastTaskID, Kind: model.TaskEventSubmitted, At: now, By: by,
	})
	if err != nil {
		t.Fatal(err)
	}
	maxAuditID, err := srcStore.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: now, By: by, Surface: "settings", Summary: "audit",
	})
	if err != nil {
		t.Fatal(err)
	}
	tplID, err := srcStore.NewTemplateID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := srcStore.PutTemplate(ctx, model.Template{
		ID: tplID, Name: "tpl-seq", Title: "Tpl", Body: "Run.", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	suiteID, err := srcStore.NewSuiteID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := srcStore.PutSuite(ctx, model.Suite{
		ID: suiteID, Name: "suite-seq", Mode: model.SuiteCount, Count: 1,
		Items: []model.SuiteItem{{TemplateID: tplID}, {TemplateID: tplID}}, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	dumpDir := t.TempDir()
	if err := staterepo.Export(ctx, srcDB, dumpDir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(dumpDir, model.SchemaVersion); err != nil {
		t.Fatalf("WriteSchemaVersion: %v", err)
	}

	// 1. Full staterepo.Import into a fresh Postgres store.
	dstStore, dstDB := postgrestest.Open(t)
	if err := staterepo.Import(ctx, dstDB, dumpDir); err != nil {
		t.Fatalf("staterepo.Import: %v", err)
	}
	newTaskID, err := dstStore.NewTaskID(ctx)
	if err != nil || newTaskID <= lastTaskID {
		t.Fatalf("NewTaskID after Import = (%q, %v), want > %q", newTaskID, err, lastTaskID)
	}
	if cid, err := dstStore.AddComment(ctx, model.Comment{
		TaskID: lastTaskID, Author: model.Attribution{Actor: by}, Body: "next", CreatedAt: now,
	}); err != nil || cid <= maxCommentID {
		t.Fatalf("AddComment after Import = (%d, %v), want > %d", cid, err, maxCommentID)
	}
	if eid, err := dstStore.RecordTaskEvent(ctx, model.TaskEvent{
		TaskID: lastTaskID, Kind: model.TaskEventRetried, At: now, By: by,
	}); err != nil || eid <= maxEventID {
		t.Fatalf("RecordTaskEvent after Import = (%d, %v), want > %d", eid, err, maxEventID)
	}
	if aid, err := dstStore.RecordConfigAudit(ctx, model.ConfigAuditEntry{
		At: now, By: by, Surface: "settings", Summary: "next",
	}); err != nil || aid <= maxAuditID {
		t.Fatalf("RecordConfigAudit after Import = (%d, %v), want > %d", aid, err, maxAuditID)
	}

	// 2. Partial staterepo.ImportTables(SettingsTables) into another fresh Postgres store.
	cfgDir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, srcDB, cfgDir); err != nil {
		t.Fatalf("ExportConfig: %v", err)
	}
	if err := staterepo.WriteSchemaVersion(cfgDir, model.SchemaVersion); err != nil {
		t.Fatalf("WriteSchemaVersion cfgDir: %v", err)
	}
	settingsStore, settingsDB := postgrestest.Open(t)
	if err := staterepo.ImportTables(ctx, settingsDB, cfgDir, staterepo.SettingsTables); err != nil {
		t.Fatalf("staterepo.ImportTables: %v", err)
	}
	if nextTplID, err := settingsStore.NewTemplateID(ctx); err != nil || nextTplID == tplID {
		t.Fatalf("NewTemplateID after ImportTables = (%q, %v), collided with %q", nextTplID, err, tplID)
	}
	nextSuiteID, err := settingsStore.NewSuiteID(ctx)
	if err != nil || nextSuiteID == suiteID {
		t.Fatalf("NewSuiteID after ImportTables = (%q, %v), collided with %q", nextSuiteID, err, suiteID)
	}
	if err := settingsStore.PutSuite(ctx, model.Suite{
		ID: nextSuiteID, Name: "suite-seq-2", Mode: model.SuiteCount, Count: 1,
		Items: []model.SuiteItem{{TemplateID: tplID}}, CreatedAt: now,
	}); err != nil {
		t.Fatalf("PutSuite after ImportTables failed (suite_item sequence not synced): %v", err)
	}
}

// TestSyncSequencesRespectsWriteFence verifies that SyncSequences called on a
// *sql.DB runs its sequence advancement inside a read-write transaction so
// FenceWrites refuses stale writers with ErrWriteFenced instead of mutating
// sequences after single-writer lease loss.
func TestSyncSequencesRespectsWriteFence(t *testing.T) {
	cfg, adminDB := newLockTestSchema(t)
	ctx := context.Background()

	lockA, err := postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
		WaitTimeout:       -1,
		KeepAliveInterval: time.Hour,
		ReacquireWindow:   -1,
		ApplicationName:   "grain-test-seq-fence-a",
		Logf:              t.Logf,
	})
	if err != nil {
		t.Fatalf("AcquireWriterLock(A): %v", err)
	}
	defer lockA.Close()

	dbA, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("Open(A): %v", err)
	}
	defer dbA.Close()
	if _, err := dbA.ExecContext(ctx, "CREATE TABLE `seq_fence_probe` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `label` TEXT)"); err != nil {
		t.Fatalf("creating probe table: %v", err)
	}
	if _, err := dbA.ExecContext(ctx, "CREATE VIEW `seq_fence_view` AS SELECT `id`, `label` FROM `seq_fence_probe`"); err != nil {
		t.Fatalf("creating probe view: %v", err)
	}
	if _, err := dbA.ExecContext(ctx, "INSERT INTO `seq_fence_probe` (`id`, `label`) VALUES (25, 'explicit')"); err != nil {
		t.Fatalf("inserting explicit id row: %v", err)
	}
	if err := postgres.FenceWrites(dbA, lockA); err != nil {
		t.Fatalf("FenceWrites(A): %v", err)
	}

	// Terminate A's lease session and acquire B's lease before A calls SyncSequences.
	if _, err := adminDB.ExecContext(ctx, `SELECT pg_terminate_backend($1)`, int64(lockA.BackendPID())); err != nil {
		t.Fatalf("terminating A's lock session: %v", err)
	}
	var lockB *postgres.WriterLock
	deadline := time.Now().Add(10 * time.Second)
	for {
		lockB, err = postgres.AcquireWriterLock(ctx, cfg, postgres.WriterLockOptions{
			WaitTimeout: -1, KeepAliveInterval: -1, ApplicationName: "grain-test-seq-fence-b", Logf: t.Logf,
		})
		if err == nil {
			break
		}
		if !errors.Is(err, postgres.ErrWriterLockHeld) || time.Now().After(deadline) {
			t.Fatalf("AcquireWriterLock(B): %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer lockB.Close()

	if _, err := postgres.SyncSequences(ctx, dbA); !errors.Is(err, postgres.ErrWriteFenced) {
		t.Fatalf("SyncSequences on fenced stale pool = %v, want ErrWriteFenced", err)
	}

	dbB, err := postgres.Open(cfg)
	if err != nil {
		t.Fatalf("Open(B): %v", err)
	}
	defer dbB.Close()
	if err := postgres.FenceWrites(dbB, lockB); err != nil {
		t.Fatalf("FenceWrites(B): %v", err)
	}
	var isCalled bool
	var lastVal int64
	if err := dbB.QueryRowContext(ctx, `SELECT last_value::bigint, is_called FROM seq_fence_probe_id_seq`).Scan(&lastVal, &isCalled); err != nil {
		t.Fatalf("reading sequence state after refused sync: %v", err)
	}
	if isCalled || lastVal != 1 {
		t.Fatalf("stale fenced pool mutated sequence: last_value=%d is_called=%v, want 1/false", lastVal, isCalled)
	}
	synced, err := postgres.SyncSequences(ctx, dbB)
	if err != nil {
		t.Fatalf("SyncSequences(B): %v", err)
	}
	if len(synced) != 1 || synced[0].Table != "seq_fence_probe" || synced[0].MaxID != 25 {
		t.Fatalf("SyncSequences(B) = %+v, want seq_fence_probe synced to 25", synced)
	}
}
