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

package staterepo_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func TestExportConfigLeavesOperationalRowsOnlyInSQLite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := model.DefaultConfig()
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	task := model.Task{ID: "task-1", Title: "private operational row"}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	ciRun := model.CIRun{ID: "ci-run-1", Key: "private-ci-run"}
	if err := store.PutCIRun(ctx, ciRun); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	tables := filepath.Join(dir, staterepo.TablesDir)
	if err := os.MkdirAll(tables, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"task", "ci_run", "ci_job", "ci_step"} {
		if err := os.WriteFile(filepath.Join(tables, table+".json"), []byte("legacy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := staterepo.ExportConfig(ctx, db, dir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(tables, "grain_config.json")); err != nil {
		t.Fatalf("configuration was not exported: %v", err)
	}
	for _, table := range []string{"task", "ci_run", "ci_job", "ci_step"} {
		if _, err := os.Stat(filepath.Join(tables, table+".json")); !os.IsNotExist(err) {
			t.Fatalf("operational %s file remains: %v", table, err)
		}
	}
	if got, err := store.GetTask(ctx, task.ID); err != nil || got == nil {
		t.Fatalf("task left SQLite: %v %v", got, err)
	}
	if got, err := store.GetCIRun(ctx, ciRun.ID); err != nil || got == nil {
		t.Fatalf("CI run left SQLite: %v %v", got, err)
	}
}
