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

package postgres

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// memPasswords is a PasswordStore held in memory.
type memPasswords map[string]string

func (m memPasswords) Set(secret, key string, value []byte) error {
	m[secret] = string(value)
	return nil
}

func (m memPasswords) Resolve(_ context.Context, name string) (string, error) {
	v, ok := m[name]
	if !ok {
		return "", fmt.Errorf("no secret %q", name)
	}
	return v, nil
}

func TestSaveAndLoadDatabaseConfig(t *testing.T) {
	dataDir := t.TempDir()
	store := memPasswords{}

	// Initially empty
	cfg, err := LoadDatabaseConfig(context.Background(), dataDir, store)
	if err != nil {
		t.Fatalf("LoadDatabaseConfig initial: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config, got %+v", cfg)
	}

	// Save postgres config
	if err := SaveDatabaseConfig(dataDir, "postgres", "postgres://user:pass@localhost:5432/grain", store); err != nil {
		t.Fatalf("SaveDatabaseConfig: %v", err)
	}

	// The file holds no password; the store does.
	raw, err := os.ReadFile(filepath.Join(dataDir, DatabaseConfigFile))
	if err != nil {
		t.Fatalf("reading %s: %v", DatabaseConfigFile, err)
	}
	if strings.Contains(string(raw), "pass@") {
		t.Errorf("%s holds the password in clear text: %s", DatabaseConfigFile, raw)
	}
	if store[DatabasePasswordSecret] != "pass" {
		t.Errorf("secret store holds %q, want the password", store[DatabasePasswordSecret])
	}
	onDisk, err := ReadDatabaseConfig(dataDir)
	if err != nil {
		t.Fatalf("ReadDatabaseConfig: %v", err)
	}
	if onDisk.DSN != "postgres://user@localhost:5432/grain" || onDisk.PasswordSecret != DatabasePasswordSecret {
		t.Errorf("on disk = %+v", onDisk)
	}

	// Load back
	cfg, err = LoadDatabaseConfig(context.Background(), dataDir, store)
	if err != nil {
		t.Fatalf("LoadDatabaseConfig: %v", err)
	}
	if cfg == nil {
		t.Fatalf("expected non-nil config")
	}
	if cfg.Driver != "postgres" {
		t.Errorf("expected driver postgres, got %q", cfg.Driver)
	}
	if cfg.DSN != "postgres://user:pass@localhost:5432/grain" {
		t.Errorf("unexpected DSN: %q", cfg.DSN)
	}

	// Without the store the password cannot be put back, and that is
	// an error rather than a DSN quietly missing it.
	if _, err := LoadDatabaseConfig(context.Background(), dataDir, nil); err == nil {
		t.Errorf("LoadDatabaseConfig with no store succeeded")
	}
}

func TestSaveDatabaseConfigRefusesAPasswordWithNoStore(t *testing.T) {
	dataDir := t.TempDir()
	if err := SaveDatabaseConfig(dataDir, "postgres", "host=db password=hunter2", nil); err == nil {
		t.Fatal("SaveDatabaseConfig wrote a password with no secret store to put it in")
	}
	if _, err := os.Stat(filepath.Join(dataDir, DatabaseConfigFile)); !os.IsNotExist(err) {
		t.Errorf("%s was written: %v", DatabaseConfigFile, err)
	}
	// A DSN with no password needs no store.
	if err := SaveDatabaseConfig(dataDir, "postgres", "postgres://user@db/grain", nil); err != nil {
		t.Fatalf("SaveDatabaseConfig without a password: %v", err)
	}
	cfg, err := LoadDatabaseConfig(context.Background(), dataDir, nil)
	if err != nil || cfg.DSN != "postgres://user@db/grain" || cfg.PasswordSecret != "" {
		t.Errorf("LoadDatabaseConfig = %+v, %v", cfg, err)
	}
}

func TestMigrateDatabaseConfigPassword(t *testing.T) {
	dataDir := t.TempDir()
	legacy := `{"driver":"postgres","dsn":"postgres://user:old@db:5432/grain?sslmode=disable"}`
	if err := os.WriteFile(filepath.Join(dataDir, DatabaseConfigFile), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := memPasswords{}

	// Read as it stands before the move.
	if cfg, err := LoadDatabaseConfig(context.Background(), dataDir, store); err != nil || !cfg.HasPlaintextPassword() {
		t.Fatalf("legacy config = %+v, %v", cfg, err)
	}
	moved, err := MigrateDatabaseConfigPassword(dataDir, store)
	if err != nil || !moved {
		t.Fatalf("MigrateDatabaseConfigPassword = %v, %v", moved, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dataDir, DatabaseConfigFile))
	if strings.Contains(string(raw), "old") {
		t.Errorf("%s still holds the password: %s", DatabaseConfigFile, raw)
	}
	cfg, err := LoadDatabaseConfig(context.Background(), dataDir, store)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DSN != "postgres://user:old@db:5432/grain?sslmode=disable" {
		t.Errorf("DSN after migration = %q", cfg.DSN)
	}
	if moved, err := MigrateDatabaseConfigPassword(dataDir, store); err != nil || moved {
		t.Errorf("second migration = %v, %v; want nothing to do", moved, err)
	}
}

func TestSplitAndJoinDSNPassword(t *testing.T) {
	for _, tc := range []struct {
		dsn, stripped, password, joined string
	}{
		{"postgres://u:p%40ss@h:5432/db", "postgres://u@h:5432/db", "p@ss", "postgres://u:p%40ss@h:5432/db"},
		{"postgresql://u@h/db?password=s3cret&sslmode=require", "postgresql://u@h/db?sslmode=require", "s3cret", "postgresql://u:s3cret@h/db?sslmode=require"},
		{"host=db user=u password=plain dbname=grain", "host=db user=u dbname=grain", "plain", "host=db user=u dbname=grain password='plain'"},
		{`host=db password='a\'b\\c' user=u`, "host=db user=u", `a'b\c`, `host=db user=u password='a\'b\\c'`},
		{"postgres://u@h/db", "postgres://u@h/db", "", "postgres://u@h/db"},
		{"host=db user=u", "host=db user=u", "", "host=db user=u"},
	} {
		stripped, password, ok := SplitDSNPassword(tc.dsn)
		if stripped != tc.stripped || password != tc.password || ok != (tc.password != "") {
			t.Errorf("SplitDSNPassword(%q) = %q, %q, %v; want %q, %q", tc.dsn, stripped, password, ok, tc.stripped, tc.password)
		}
		if joined := JoinDSNPassword(stripped, password); joined != tc.joined {
			t.Errorf("JoinDSNPassword(%q) = %q, want %q", stripped, joined, tc.joined)
		}
	}
}

func TestUpgradeFromSQLite(t *testing.T) {
	skipUnlessBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	testNow := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	// 1. Create and populate SQLite database
	sqlitePath := filepath.Join(dataDir, "store")
	sqliteDB, err := sqlite.Open(sqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer sqliteDB.Close()

	sqliteStore := model.New(sqliteDB)
	if err := sqliteStore.Init(ctx); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}

	// Create test task
	t1 := model.Task{
		ID:     "task-upgrade-1",
		Intent: model.IntentImplement,
		Title:  "Test Task for SQLite to Postgres Upgrade",
		Body:   "Verify full round-trip migration from sqlite to postgres",
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
			},
			Reason: model.ReasonDirect,
		},
		Target:    &model.RepoRef{Owner: "owner", Name: "my-repo"},
		Binding:   model.BindingDirective,
		CreatedAt: &testNow,
	}
	if err := sqliteStore.PutTask(ctx, t1); err != nil {
		t.Fatalf("putting task 1: %v", err)
	}

	// Add comment
	comment := model.Comment{
		TaskID:    t1.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}},
		Body:      "Starting migration test comment",
		CreatedAt: testNow,
	}
	if _, err := sqliteStore.AddComment(ctx, comment); err != nil {
		t.Fatalf("adding comment: %v", err)
	}

	// 2. Start embedded Postgres
	pgCfg, stop, err := EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	// 3. Run UpgradeFromSQLite
	res, err := UpgradeFromSQLite(ctx, sqliteDB, pgCfg, true)
	if err != nil {
		t.Fatalf("UpgradeFromSQLite: %v", err)
	}

	if res.TablesMigrated == 0 {
		t.Errorf("expected tables to be migrated, got 0")
	}
	if res.Driver != "postgres" {
		t.Errorf("expected driver postgres, got %s", res.Driver)
	}

	// 4. Verify data in Postgres
	pgDB, err := Open(pgCfg)
	if err != nil {
		t.Fatalf("opening migrated postgres: %v", err)
	}
	defer pgDB.Close()

	pgStore := model.New(pgDB)
	readTask, err := pgStore.GetTask(ctx, t1.ID)
	if err != nil {
		t.Fatalf("getting task from postgres: %v", err)
	}
	if readTask == nil {
		t.Fatalf("task is nil in postgres")
	}
	if readTask.Title != t1.Title {
		t.Errorf("task title mismatch: got %q, want %q", readTask.Title, t1.Title)
	}
	if readTask.Intent != t1.Intent {
		t.Errorf("task intent mismatch: got %q, want %q", readTask.Intent, t1.Intent)
	}

	comments, err := pgStore.Comments(ctx, t1.ID)
	if err != nil {
		t.Fatalf("listing comments from postgres: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if comments[0].Body != "Starting migration test comment" {
		t.Errorf("comment body mismatch: got %q", comments[0].Body)
	}
}

func TestUpgradeFromSQLiteLargeSandboxUsageDuration(t *testing.T) {
	skipUnlessBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	testNow := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

	// 1. Create SQLite database with large sandbox usage duration (> 2^31-1 ms)
	sqlitePath := filepath.Join(dataDir, "store")
	sqliteDB, err := sqlite.Open(sqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer sqliteDB.Close()

	sqliteStore := model.New(sqliteDB)
	if err := sqliteStore.Init(ctx); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}

	// 2,485,095,604 ms overflows 32-bit signed integer (int4)
	largeDuration := 2485095604 * time.Millisecond
	if err := sqliteStore.RecordSandboxStart(ctx, model.SandboxKindAgent, true, testNow); err != nil {
		t.Fatalf("recording sandbox start: %v", err)
	}
	if err := sqliteStore.RecordSandboxFinish(ctx, model.SandboxKindAgent, largeDuration, testNow.Add(largeDuration)); err != nil {
		t.Fatalf("recording sandbox finish: %v", err)
	}

	// 2. Start embedded Postgres
	pgCfg, stop, err := EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	// 3. Run UpgradeFromSQLite
	res, err := UpgradeFromSQLite(ctx, sqliteDB, pgCfg, true)
	if err != nil {
		t.Fatalf("UpgradeFromSQLite: %v", err)
	}
	if res.TablesMigrated == 0 {
		t.Errorf("expected tables to be migrated, got 0")
	}

	// 4. Verify sandbox usage in Postgres
	pgDB, err := Open(pgCfg)
	if err != nil {
		t.Fatalf("opening migrated postgres: %v", err)
	}
	defer pgDB.Close()

	pgStore := model.New(pgDB)
	usages, err := pgStore.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("getting sandbox usages from postgres: %v", err)
	}
	if len(usages) != 1 {
		t.Fatalf("expected 1 usage row, got %d", len(usages))
	}
	if usages[0].Duration != largeDuration {
		t.Errorf("usage duration mismatch: got %v (%d ms), want %v (%d ms)",
			usages[0].Duration, int64(usages[0].Duration/time.Millisecond),
			largeDuration, int64(largeDuration/time.Millisecond))
	}
}

func TestPostgresComprehensiveWalkthrough(t *testing.T) {
	skipUnlessBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	testNow := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

	// 1. Create and populate rich SQLite database
	sqlitePath := filepath.Join(dataDir, "store")
	sqliteDB, err := sqlite.Open(sqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer sqliteDB.Close()

	sqliteStore := model.New(sqliteDB)
	if err := sqliteStore.Init(ctx); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}

	// Pre-allocate task IDs in SQLite to advance task_sequence to 3
	for i := 0; i < 3; i++ {
		if _, err := sqliteStore.NewTaskID(ctx); err != nil {
			t.Fatalf("allocating sqlite task id: %v", err)
		}
	}
	cf1 := model.CustomField{
		Name:    "environment",
		Label:   "Environment",
		Type:    model.CustomFieldEnum,
		Options: []string{"staging", "production"},
	}
	if err := sqliteStore.PutCustomField(ctx, cf1); err != nil {
		t.Fatalf("putting custom field 1: %v", err)
	}

	cf2 := model.CustomField{
		Name:    "cluster",
		Label:   "Cluster",
		Type:    model.CustomFieldEnum,
		Options: []string{"us-central1", "europe-west1"},
	}
	if err := sqliteStore.PutCustomField(ctx, cf2); err != nil {
		t.Fatalf("putting custom field 2: %v", err)
	}

	// 1b. Templates
	tmpl := model.Template{
		ID:        "tmpl-nightly-build",
		Name:      "Nightly Build",
		Title:     "Nightly Build Run",
		Body:      "Execute full suite and publish results",
		CreatedAt: testNow,
	}
	if err := sqliteStore.PutTemplate(ctx, tmpl); err != nil {
		t.Fatalf("putting template: %v", err)
	}

	// 1c. Schedules
	sched := model.Schedule{
		ID:         "sched-daily",
		Title:      "Daily Smoke Test",
		Body:       "Runs nightly smoke tests",
		Target:     model.RepoRef{Owner: "acme", Name: "infra"},
		TemplateID: &tmpl.ID,
		Recurrence: model.Recurrence{Kind: model.RecurrenceDaily, TimeOfDay: 120},
		Enabled:    true,
		CreatedAt:  testNow,
	}
	if err := sqliteStore.PutSchedule(ctx, sched); err != nil {
		t.Fatalf("putting schedule: %v", err)
	}

	// 1d. Tasks in diverse states
	tQueued := model.Task{
		ID:     "task-queued-1",
		Intent: model.IntentImplement,
		Title:  "Queued Task Ready for Dispatch",
		Body:   "Pending worker assignment",
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"},
			},
			Reason: model.ReasonDirect,
		},
		Target:    &model.RepoRef{Owner: "acme", Name: "webapp"},
		Binding:   model.BindingDirective,
		CreatedAt: &testNow,
		CustomFields: map[string][]string{
			"environment": {"staging"},
		},
	}
	if err := sqliteStore.PutTask(ctx, tQueued); err != nil {
		t.Fatalf("putting queued task: %v", err)
	}

	tCompleted := model.Task{
		ID:     "task-completed-2",
		Intent: model.IntentImplement,
		Title:  "Completed Task",
		Body:   "Task completed successfully",
		Tags:   []string{"infra", "resolved"},
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bob"},
			},
			Reason: model.ReasonDirect,
		},
		Target:    &model.RepoRef{Owner: "acme", Name: "infra"},
		Binding:   model.BindingDirective,
		CreatedAt: &testNow,
	}
	if err := sqliteStore.PutTask(ctx, tCompleted); err != nil {
		t.Fatalf("putting completed task: %v", err)
	}

	// Add comments to completed task
	c1 := model.Comment{
		TaskID:    tCompleted.ID,
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "bob"}},
		Body:      "Root cause identified in DB connector pooling",
		CreatedAt: testNow.Add(5 * time.Minute),
	}
	if _, err := sqliteStore.AddComment(ctx, c1); err != nil {
		t.Fatalf("adding comment: %v", err)
	}

	// Record task run
	runNow := testNow.Add(10 * time.Minute)
	finNow := testNow.Add(15 * time.Minute)
	run1 := model.Run{
		ID:        "run-comp-1",
		TaskID:    tCompleted.ID,
		Attempt:   1,
		StartedAt: runNow,
	}
	if err := sqliteStore.StartRun(ctx, run1, model.Limits{}); err != nil {
		t.Fatalf("starting run: %v", err)
	}
	if err := sqliteStore.FinishRun(ctx, run1.ID, finNow, "succeeded", "completed successfully"); err != nil {
		t.Fatalf("finishing run: %v", err)
	}

	// 2. Start PostgreSQL Instance
	pgCfg, stop, err := EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	// 3. Perform Full SQLite to PostgreSQL Upgrade
	res, err := UpgradeFromSQLite(ctx, sqliteDB, pgCfg, true)
	if err != nil {
		t.Fatalf("UpgradeFromSQLite failed: %v", err)
	}
	if res.TablesMigrated == 0 {
		t.Fatalf("expected tables migrated > 0, got 0")
	}

	// 4. Verify Full Data Fidelity in PostgreSQL
	pgDB, err := Open(pgCfg)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer pgDB.Close()

	pgStore := model.New(pgDB)

	// Verify Custom Fields
	gotCF1, err := pgStore.GetCustomField(ctx, "environment")
	if err != nil || gotCF1 == nil {
		t.Fatalf("get custom field 1 from postgres: %v", err)
	}
	if gotCF1.Label != "Environment" {
		t.Errorf("gotCF1 label = %q, want Environment", gotCF1.Label)
	}

	gotCF2, err := pgStore.GetCustomField(ctx, "cluster")
	if err != nil || gotCF2 == nil {
		t.Fatalf("get custom field 2 from postgres: %v", err)
	}
	if gotCF2.Label != "Cluster" {
		t.Errorf("gotCF2 label = %q, want Cluster", gotCF2.Label)
	}

	// Verify Template & Schedule
	gotTmpl, err := pgStore.GetTemplate(ctx, tmpl.ID)
	if err != nil || gotTmpl == nil {
		t.Fatalf("get template from postgres: %v", err)
	}
	if gotTmpl.Name != tmpl.Name {
		t.Errorf("template mismatch: %+v", gotTmpl)
	}

	gotSched, err := pgStore.GetSchedule(ctx, sched.ID)
	if err != nil || gotSched == nil {
		t.Fatalf("get schedule from postgres: %v", err)
	}
	if gotSched.Title != sched.Title {
		t.Errorf("schedule mismatch: %+v", gotSched)
	}

	// Verify Tasks
	gotTask1, err := pgStore.GetTask(ctx, tQueued.ID)
	if err != nil || gotTask1 == nil {
		t.Fatalf("get queued task from postgres: %v", err)
	}
	if gotTask1.Title != tQueued.Title {
		t.Errorf("queued task mismatch: %+v", gotTask1)
	}
	if len(gotTask1.CustomFields["environment"]) != 1 || gotTask1.CustomFields["environment"][0] != "staging" {
		t.Errorf("custom field value mismatch: %+v", gotTask1.CustomFields)
	}

	gotTask2, err := pgStore.GetTask(ctx, tCompleted.ID)
	if err != nil || gotTask2 == nil {
		t.Fatalf("get completed task from postgres: %v", err)
	}
	if gotTask2.Title != tCompleted.Title {
		t.Errorf("completed task mismatch: %+v", gotTask2)
	}
	if len(gotTask2.Tags) != 2 || gotTask2.Tags[0] != "infra" || gotTask2.Tags[1] != "resolved" {
		t.Errorf("tags mismatch: %v", gotTask2.Tags)
	}

	comments, err := pgStore.Comments(ctx, tCompleted.ID)
	if err != nil || len(comments) != 1 {
		t.Fatalf("comments mismatch: len=%d, err=%v", len(comments), err)
	}
	if comments[0].Body != c1.Body {
		t.Errorf("comment body mismatch: %q", comments[0].Body)
	}

	runs, err := pgStore.Runs(ctx, tCompleted.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs mismatch: len=%d, err=%v", len(runs), err)
	}
	if runs[0].Outcome != "succeeded" {
		t.Errorf("run outcome = %q, want %q", runs[0].Outcome, "succeeded")
	}

	// 5. Test Live Mutations on PostgreSQL Store (New Task Creation, Sequences, Runs)
	tLive := model.Task{
		ID:     "task-live-new-3",
		Intent: model.IntentImplement,
		Title:  "Live Task Created on Migrated Postgres",
		Body:   "Verify that post-migration writes succeed seamlessly",
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"},
			},
			Reason: model.ReasonDirect,
		},
		Target:    &model.RepoRef{Owner: "acme", Name: "infra"},
		Binding:   model.BindingDirective,
		CreatedAt: &testNow,
	}
	if err := pgStore.PutTask(ctx, tLive); err != nil {
		t.Fatalf("putting live task to postgres store: %v", err)
	}

	gotLive, err := pgStore.GetTask(ctx, tLive.ID)
	if err != nil || gotLive == nil {
		t.Fatalf("get live task from postgres: %v", err)
	}
	if gotLive.Title != tLive.Title {
		t.Errorf("live task title mismatch: %q", gotLive.Title)
	}

	// Verify sequence synchronization on Postgres
	nextID, err := pgStore.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("pgStore.NewTaskID failed: %v", err)
	}
	if nextID != "4" {
		t.Errorf("expected allocated task ID 4, got %q", nextID)
	}

	// 6. Test Aborted Migration / Connection Failure Rollback
	badCfg := DefaultConfig("postgres://invalid:pass@127.0.0.1:54321/invalid?sslmode=disable&connect_timeout=1")
	badCfg.ConnectTimeout = 500 * time.Millisecond
	_, err = UpgradeFromSQLite(ctx, sqliteDB, badCfg, true)
	if err == nil {
		t.Errorf("expected upgrade to invalid postgres to fail, got nil")
	}

	// Verify original SQLite database remains intact and operational
	checkTask, err := sqliteStore.GetTask(ctx, tQueued.ID)
	if err != nil || checkTask == nil {
		t.Fatalf("original SQLite store was corrupted during failed migration attempt: %v", err)
	}
}

func TestUpgradeConcurrentLiveSystem(t *testing.T) {
	skipUnlessBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	testNow := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	// 1. Initialize SQLite Database
	sqlitePath := filepath.Join(dataDir, "store")
	sqliteDB, err := sqlite.Open(sqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer sqliteDB.Close()

	sqliteStore := model.New(sqliteDB)
	if err := sqliteStore.Init(ctx); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}

	// Seed initial tasks
	initialCount := 10
	for i := 1; i <= initialCount; i++ {
		task := model.Task{
			ID:        fmt.Sprintf("task-live-%d", i),
			Intent:    model.IntentImplement,
			Title:     fmt.Sprintf("Initial Live Task %d", i),
			Body:      "Running in active daemon",
			Binding:   model.BindingDirective,
			Target:    &model.RepoRef{Owner: "grain", Name: "live"},
			CreatedAt: &testNow,
		}
		if err := sqliteStore.PutTask(ctx, task); err != nil {
			t.Fatalf("seeding task %d: %v", i, err)
		}
	}

	// 2. Start PostgreSQL Instance
	pgCfg, stop, err := EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	// 3. Start Concurrent Workers Writing to SQLite
	var stopWorkers int32
	var wg sync.WaitGroup
	errCh := make(chan error, 100)

	// Worker A: Continuous Task Creator
	wg.Add(1)
	go func() {
		defer wg.Done()
		idx := 100
		for atomic.LoadInt32(&stopWorkers) == 0 {
			idx++
			tNew := model.Task{
				ID:        fmt.Sprintf("task-concurrent-%d", idx),
				Intent:    model.IntentImplement,
				Title:     fmt.Sprintf("Concurrent Task %d", idx),
				Body:      "Active worker creation during upgrade",
				Binding:   model.BindingDirective,
				Target:    &model.RepoRef{Owner: "grain", Name: "live"},
				CreatedAt: &testNow,
			}
			if err := sqliteStore.PutTask(ctx, tNew); err != nil {
				select {
				case errCh <- fmt.Errorf("worker A: %w", err):
				default:
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Worker B: Continuous Comment Adder
	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stopWorkers) == 0 {
			c := model.Comment{
				TaskID:    "task-live-1",
				Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "worker-b"}},
				Body:      fmt.Sprintf("Live log entry at %s", time.Now().Format(time.RFC3339Nano)),
				CreatedAt: time.Now().UTC(),
			}
			if _, err := sqliteStore.AddComment(ctx, c); err != nil {
				select {
				case errCh <- fmt.Errorf("worker B: %w", err):
				default:
				}
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Worker C: Task Reader & State Mutator
	wg.Add(1)
	go func() {
		defer wg.Done()
		for atomic.LoadInt32(&stopWorkers) == 0 {
			tasks, err := sqliteStore.ListTasks(ctx)
			if err != nil {
				select {
				case errCh <- fmt.Errorf("worker C read: %w", err):
				default:
				}
				return
			}
			if len(tasks) > 0 {
				_ = sqliteStore.PutTask(ctx, tasks[0])
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Allow workers to generate load for 50ms before triggering upgrade
	time.Sleep(50 * time.Millisecond)

	// 4. Trigger UpgradeFromSQLite Concurrently While Workers Run
	res, err := UpgradeFromSQLite(ctx, sqliteDB, pgCfg, true)
	if err != nil {
		t.Fatalf("concurrent UpgradeFromSQLite failed: %v", err)
	}

	// Stop background workers
	atomic.StoreInt32(&stopWorkers, 1)
	wg.Wait()
	close(errCh)

	for wErr := range errCh {
		t.Fatalf("worker error during concurrent upgrade: %v", wErr)
	}

	if res.TablesMigrated == 0 {
		t.Fatalf("expected migrated tables > 0, got 0")
	}

	// 5. Verify PostgreSQL Data Snapshot and Sequence Integrity
	pgDB, err := Open(pgCfg)
	if err != nil {
		t.Fatalf("opening postgres db: %v", err)
	}
	defer pgDB.Close()

	pgStore := model.New(pgDB)

	// Verify that initial seeded tasks exist in PostgreSQL
	for i := 1; i <= initialCount; i++ {
		tID := fmt.Sprintf("task-live-%d", i)
		got, err := pgStore.GetTask(ctx, tID)
		if err != nil || got == nil {
			t.Fatalf("expected seeded task %s in postgres: %v", tID, err)
		}
	}

	// Verify that postgres sequences work and allow allocations
	nextID, err := pgStore.NewTaskID(ctx)
	if err != nil {
		t.Fatalf("pgStore.NewTaskID failed after concurrent migration: %v", err)
	}
	if nextID == "" {
		t.Errorf("empty task ID allocated on postgres")
	}

	// Verify that SQLite database remained uncorrupted and continues to operate
	postMigrateTask := model.Task{
		ID:        "task-after-upgrade-999",
		Intent:    model.IntentImplement,
		Title:     "Task After Upgrade",
		Body:      "SQLite remains active and intact",
		Binding:   model.BindingDirective,
		Target:    &model.RepoRef{Owner: "grain", Name: "live"},
		CreatedAt: &testNow,
	}
	if err := sqliteStore.PutTask(ctx, postMigrateTask); err != nil {
		t.Fatalf("sqlite store failed write after upgrade: %v", err)
	}
}

func TestUpgradeNoData(t *testing.T) {
	skipUnlessBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	testNow := time.Date(2026, 9, 28, 14, 0, 0, 0, time.UTC)

	// 1. Create SQLite DB with data
	sqlitePath := filepath.Join(dataDir, "store")
	sqliteDB, err := sqlite.Open(sqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer sqliteDB.Close()

	sqliteStore := model.New(sqliteDB)
	if err := sqliteStore.Init(ctx); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}

	t1 := model.Task{
		ID:        "task-no-data-1",
		Intent:    model.IntentImplement,
		Title:     "SQLite Task",
		Body:      "Should not be copied to Postgres",
		Binding:   model.BindingDirective,
		Target:    &model.RepoRef{Owner: "grain", Name: "core"},
		CreatedAt: &testNow,
	}
	if err := sqliteStore.PutTask(ctx, t1); err != nil {
		t.Fatalf("putting task: %v", err)
	}

	// 2. Start PostgreSQL and Upgrade with migrateData = false
	pgCfg, stop, err := EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	res, err := UpgradeFromSQLite(ctx, sqliteDB, pgCfg, false)
	if err != nil {
		t.Fatalf("UpgradeFromSQLite with migrateData=false failed: %v", err)
	}
	if res.TablesMigrated == 0 {
		t.Errorf("expected tables initialized, got 0")
	}

	// 3. Verify PostgreSQL has empty tables
	pgDB, err := Open(pgCfg)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer pgDB.Close()

	pgStore := model.New(pgDB)
	gotTask, err := pgStore.GetTask(ctx, t1.ID)
	if err != nil {
		t.Fatalf("GetTask on postgres: %v", err)
	}
	if gotTask != nil {
		t.Errorf("expected task to not be migrated when migrateData=false, got %+v", gotTask)
	}

	// Verify PostgreSQL can accept new records
	tPG := model.Task{
		ID:        "task-postgres-new-1",
		Intent:    model.IntentImplement,
		Title:     "Direct Postgres Task",
		Body:      "Direct insert",
		Binding:   model.BindingDirective,
		Target:    &model.RepoRef{Owner: "grain", Name: "core"},
		CreatedAt: &testNow,
	}
	if err := pgStore.PutTask(ctx, tPG); err != nil {
		t.Fatalf("inserting new task to postgres: %v", err)
	}
}

func TestUpgradeFromSQLiteWithNonUTF8Data(t *testing.T) {
	skipUnlessBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	testNow := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	// 1. Create SQLite DB with invalid UTF-8 bytes (e.g. 0x81) in ci_step output and task_run transcript
	sqlitePath := filepath.Join(dataDir, "store")
	sqliteDB, err := sqlite.Open(sqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer sqliteDB.Close()

	sqliteStore := model.New(sqliteDB)
	if err := sqliteStore.Init(ctx); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}

	task := model.Task{
		ID:        "task-utf8-test",
		Intent:    model.IntentImplement,
		Title:     "Task with non-utf8 data",
		Body:      "Testing upgrade with non-utf8 byte 0x81",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "operator"}}, Reason: model.ReasonDirect},
		Binding:   model.BindingDirective,
		CreatedAt: &testNow,
	}
	if err := sqliteStore.PutTask(ctx, task); err != nil {
		t.Fatalf("putting task: %v", err)
	}

	// Insert ci_run and ci_step with 0x81 invalid byte sequence
	ciRun := model.CIRun{
		ID:         "run-utf8-1",
		Repo:       "owner/repo",
		Commit:     "abcdef0123456789abcdef0123456789abcdef01",
		Ref:        "refs/heads/main",
		Conclusion: "success",
		StartedAt:  testNow,
		Jobs: []model.CIJob{
			{
				JobID:      "test-job",
				Name:       "test-job",
				Conclusion: "success",
				StartedAt:  testNow,
				FinishedAt: testNow.Add(time.Minute),
				Steps: []model.CIStep{
					{
						Name:       "test-step",
						Conclusion: "success",
					},
				},
			},
		},
	}
	if err := sqliteStore.PutCIRun(ctx, ciRun); err != nil {
		t.Fatalf("putting ci run: %v", err)
	}
	if err := sqliteStore.PutCIStepOutput(ctx, ciRun.ID, 0, 0, "output containing invalid byte \x81 and continuation \xff\xfe in logs"); err != nil {
		t.Fatalf("putting ci step output: %v", err)
	}

	// 2. Start PostgreSQL
	pgCfg, stop, err := EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	// 3. Run UpgradeFromSQLite - should succeed without 0x81 UTF8 encoding error
	res, err := UpgradeFromSQLite(ctx, sqliteDB, pgCfg, true)
	if err != nil {
		t.Fatalf("UpgradeFromSQLite failed with non-UTF8 data: %v", err)
	}
	if res.TablesMigrated == 0 {
		t.Errorf("expected tables migrated, got 0")
	}

	// 4. Verify PostgreSQL data
	pgDB, err := Open(pgCfg)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer pgDB.Close()

	pgStore := model.New(pgDB)
	readCIRun, err := pgStore.GetCIRun(ctx, ciRun.ID)
	if err != nil {
		t.Fatalf("getting ci run from postgres: %v", err)
	}
	if readCIRun == nil || len(readCIRun.Jobs) == 0 || len(readCIRun.Jobs[0].Steps) == 0 {
		t.Fatalf("ci run not properly migrated: %+v", readCIRun)
	}
	stepOutput := readCIRun.Jobs[0].Steps[0].Output
	if !utf8.ValidString(stepOutput) {
		t.Errorf("step output in postgres is not valid UTF8: %q", stepOutput)
	}
	if !strings.Contains(stepOutput, "output containing invalid byte") {
		t.Errorf("step output unexpected: %q", stepOutput)
	}
}

func TestUpgradeFromSQLiteBigIntDurationAndSanitizationWalkthrough(t *testing.T) {
	skipUnlessBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dataDir := t.TempDir()
	testNow := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	// 1. Initialize SQLite database
	sqlitePath := filepath.Join(dataDir, "store")
	sqliteDB, err := sqlite.Open(sqlite.DefaultConfig(sqlitePath))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	defer sqliteDB.Close()

	sqliteStore := model.New(sqliteDB)
	if err := sqliteStore.Init(ctx); err != nil {
		t.Fatalf("init sqlite: %v", err)
	}

	// 2. Seed large duration_ms (> 2.147B ms) for agent and CI
	agentDuration := 3500000000 * time.Millisecond
	ciDuration := 2500000000 * time.Millisecond
	if err := sqliteStore.RecordSandboxStart(ctx, model.SandboxKindAgent, true, testNow); err != nil {
		t.Fatalf("recording agent sandbox start: %v", err)
	}
	if err := sqliteStore.RecordSandboxFinish(ctx, model.SandboxKindAgent, agentDuration, testNow.Add(agentDuration)); err != nil {
		t.Fatalf("recording agent sandbox finish: %v", err)
	}
	if err := sqliteStore.RecordSandboxStart(ctx, model.SandboxKindCI, true, testNow); err != nil {
		t.Fatalf("recording ci sandbox start: %v", err)
	}
	if err := sqliteStore.RecordSandboxFinish(ctx, model.SandboxKindCI, ciDuration, testNow.Add(ciDuration)); err != nil {
		t.Fatalf("recording ci sandbox finish: %v", err)
	}

	// 3. Seed task, comments, and CI logs with invalid UTF-8 and null bytes
	task := model.Task{
		ID:        "task-walkthrough-1",
		Intent:    model.IntentImplement,
		Title:     "Title with invalid byte \x81\x82 and null \x00",
		Body:      "Body with non-utf8 \xff\xfe\xfd and null \x00 characters",
		Origin:    model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}, Reason: model.ReasonDirect},
		Binding:   model.BindingDirective,
		CreatedAt: &testNow,
	}
	if err := sqliteStore.PutTask(ctx, task); err != nil {
		t.Fatalf("putting task: %v", err)
	}

	comment := model.Comment{
		TaskID:    "task-walkthrough-1",
		Author:    model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}},
		Body:      "Comment with invalid sequence \x80\x81 and null \x00 byte",
		CreatedAt: testNow.Add(time.Hour),
	}
	if _, err := sqliteStore.AddComment(ctx, comment); err != nil {
		t.Fatalf("adding comment: %v", err)
	}

	ciRun := model.CIRun{
		ID:         "run-walkthrough-ci",
		Repo:       "bwsalmon/grain",
		Commit:     "1234567890abcdef1234567890abcdef12345678",
		Ref:        "refs/heads/main",
		Conclusion: "success",
		StartedAt:  testNow,
		Jobs: []model.CIJob{
			{
				JobID:      "job-1",
				Name:       "job-1",
				Conclusion: "success",
				StartedAt:  testNow,
				FinishedAt: testNow.Add(5 * time.Minute),
				Steps: []model.CIStep{
					{
						Name:       "step-1",
						Conclusion: "success",
					},
				},
			},
		},
	}
	if err := sqliteStore.PutCIRun(ctx, ciRun); err != nil {
		t.Fatalf("putting ci run: %v", err)
	}
	if err := sqliteStore.PutCIStepOutput(ctx, ciRun.ID, 0, 0, "Build logs with non-utf8 \x81\x92 and null \x00 byte"); err != nil {
		t.Fatalf("putting ci step output: %v", err)
	}

	// 4. Start PostgreSQL and migrate
	pgCfg, stop, err := EnsureRunning(ctx, dataDir)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	defer stop()

	res, err := UpgradeFromSQLite(ctx, sqliteDB, pgCfg, true)
	if err != nil {
		t.Fatalf("UpgradeFromSQLite: %v", err)
	}
	if res.TablesMigrated == 0 {
		t.Fatalf("expected tables migrated > 0, got 0")
	}

	// 5. Open PostgreSQL and verify schema & data
	pgDB, err := Open(pgCfg)
	if err != nil {
		t.Fatalf("opening postgres: %v", err)
	}
	defer pgDB.Close()

	// Verify duration_ms column is bigint in PostgreSQL
	var colType string
	err = pgDB.QueryRowContext(ctx, "SELECT data_type FROM information_schema.columns WHERE table_name = 'sandbox_usage' AND column_name = 'duration_ms'").Scan(&colType)
	if err != nil {
		t.Fatalf("checking column type: %v", err)
	}
	if colType != "bigint" {
		t.Errorf("expected column duration_ms to be bigint, got %q", colType)
	}

	pgStore := model.New(pgDB)

	// Verify SandboxUsages metrics
	usages, err := pgStore.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("getting sandbox usages: %v", err)
	}
	if len(usages) != 2 {
		t.Fatalf("expected 2 usage entries, got %d", len(usages))
	}

	var agentFound, ciFound bool
	for _, u := range usages {
		switch u.Kind {
		case model.SandboxKindAgent:
			agentFound = true
			if u.Duration != agentDuration {
				t.Errorf("agent duration = %v, want %v", u.Duration, agentDuration)
			}
			if u.Hours() < 970 {
				t.Errorf("agent hours = %v, want > 970", u.Hours())
			}
		case model.SandboxKindCI:
			ciFound = true
			if u.Duration != ciDuration {
				t.Errorf("ci duration = %v, want %v", u.Duration, ciDuration)
			}
			if u.Hours() < 690 {
				t.Errorf("ci hours = %v, want > 690", u.Hours())
			}
		}
	}
	if !agentFound || !ciFound {
		t.Fatalf("missing usage kinds: agentFound=%v, ciFound=%v", agentFound, ciFound)
	}

	// Verify task UTF-8 sanitization
	tk, err := pgStore.GetTask(ctx, "task-walkthrough-1")
	if err != nil {
		t.Fatalf("getting task: %v", err)
	}
	if tk == nil {
		t.Fatalf("task-walkthrough-1 not found")
	}
	if !utf8.ValidString(tk.Title) || strings.Contains(tk.Title, "\x00") {
		t.Errorf("task title not sanitized: %q", tk.Title)
	}
	if !utf8.ValidString(tk.Body) || strings.Contains(tk.Body, "\x00") {
		t.Errorf("task body not sanitized: %q", tk.Body)
	}

	// Verify comment UTF-8 sanitization
	comments, err := pgStore.Comments(ctx, "task-walkthrough-1")
	if err != nil {
		t.Fatalf("getting comments: %v", err)
	}
	if len(comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(comments))
	}
	if !utf8.ValidString(comments[0].Body) || strings.Contains(comments[0].Body, "\x00") {
		t.Errorf("comment body not sanitized: %q", comments[0].Body)
	}

	// Verify CI step output UTF-8 sanitization
	readCI, err := pgStore.GetCIRun(ctx, ciRun.ID)
	if err != nil {
		t.Fatalf("getting ci run: %v", err)
	}
	if readCI == nil || len(readCI.Jobs) == 0 || len(readCI.Jobs[0].Steps) == 0 {
		t.Fatalf("ci run not properly read")
	}
	stepOut := readCI.Jobs[0].Steps[0].Output
	if !utf8.ValidString(stepOut) || strings.Contains(stepOut, "\x00") {
		t.Errorf("step output not sanitized: %q", stepOut)
	}

	// 6. Mutate records on PostgreSQL
	if err := pgStore.RecordSandboxFinish(ctx, model.SandboxKindAgent, 1000*time.Millisecond, testNow.Add(agentDuration+time.Second)); err != nil {
		t.Fatalf("recording additional sandbox finish on pg: %v", err)
	}
	usagesAfter, err := pgStore.SandboxUsages(ctx)
	if err != nil {
		t.Fatalf("getting sandbox usages after mutation: %v", err)
	}
	for _, u := range usagesAfter {
		if u.Kind == model.SandboxKindAgent {
			if u.Duration != agentDuration+1000*time.Millisecond {
				t.Errorf("expected updated agent duration = %v, got %v", agentDuration+1000*time.Millisecond, u.Duration)
			}
		}
	}
}

// skipUnlessBinaries is postgrestest.SkipUnlessBinaries for this internal
// test package, which cannot import postgrestest (postgrestest imports
// this package): it skips t when no PostgreSQL binaries are installed,
// and fails it instead when GRAIN_REQUIRE_POSTGRES (postgrestest.RequireEnv)
// is set.
func skipUnlessBinaries(t *testing.T) {
	t.Helper()
	if IsAvailable() {
		return
	}
	if v := os.Getenv("GRAIN_REQUIRE_POSTGRES"); v != "" && v != "0" && v != "false" {
		t.Fatal("postgres binaries not available (GRAIN_REQUIRE_POSTGRES is set, so postgres is required)")
	}
	t.Skip("postgres binaries not available")
}
