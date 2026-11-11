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
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

func openUsageStore(t *testing.T) *model.Store {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return store
}

// The operator's budgets and usage mode are settings (grain/task-3994):
// a deployment rebuilt from its state repository -- ExportConfig on the
// old one, the settings import on the new -- comes back on the mode and
// budgets it had, not on advisory with none. The per-run usage and the
// learned capacity beside them are runtime state and stay behind.
func TestUsageSettingsSurviveARebuildFromTheStateRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	old := openUsageStore(t)
	want := model.UsageSettings{
		Mode: model.UsageModeAuto, MinWorkers: 2, RetentionDays: 14,
		Budgets: []model.TokenBudget{{Connection: "agy-1", Model: "gemini-3-pro", Tokens: 1000000, Window: "5h"}},
	}
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	if err := old.PutUsageSettings(ctx, want, at); err != nil {
		t.Fatal(err)
	}
	if err := old.RecordRunUsage(ctx, model.RunUsage{
		RunID: "run-1", Connection: "agy-1", InputTokens: 10, StartedAt: at, FinishedAt: at.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := staterepo.ExportConfig(ctx, old.DB(), dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(staterepo.DumpPath("usage_setting")))); err != nil {
		t.Fatalf("the state repository holds no usage settings: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, staterepo.TablesDir, "task_run_usage.json")); !os.IsNotExist(err) {
		t.Fatalf("per-run usage was exported to the state repository: %v", err)
	}

	rebuilt := openUsageStore(t)
	if err := staterepo.ImportTables(ctx, rebuilt.DB(), dir, staterepo.SettingsTables); err != nil {
		t.Fatal(err)
	}
	got, err := rebuilt.UsageSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("usage settings after a rebuild = %+v, want %+v", got, want)
	}

	// And the fixed point: the rebuilt deployment exports the same file.
	again := t.TempDir()
	if err := staterepo.ExportConfig(ctx, rebuilt.DB(), again); err != nil {
		t.Fatal(err)
	}
	path := filepath.FromSlash(staterepo.DumpPath("usage_setting"))
	if a, b := read(t, filepath.Join(dir, path)), read(t, filepath.Join(again, path)); a != b {
		t.Fatalf("%s differs after a rebuild:\n%s\n---\n%s", path, a, b)
	}
}
