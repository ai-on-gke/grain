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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// A learned weekly capacity row (model key "<model>#weekly" or "#weekly")
// survives a full Export/Import dump round trip intact alongside an interval
// row, and a grain/config Sync + Load cycle succeeds without disturbing it.
func TestWeeklyUsageCapacitySurvivesExportImportAndConfigSync(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)

	obs := time.Date(2026, 11, 17, 1, 20, 0, 0, time.UTC)
	intervalReset := obs.Add(2 * time.Hour)
	weeklyReset := obs.Add(72 * time.Hour)
	want := []model.UsageCapacity{
		{
			Connection: "claude-primary",
			Model:      "",
			Tokens:     400000,
			Window:     5 * time.Hour,
			ObservedAt: obs,
			ResetAt:    intervalReset,
		},
		{
			Connection: "claude-primary",
			Model:      "#weekly",
			Tokens:     1930000,
			Window:     7 * 24 * time.Hour,
			ObservedAt: obs,
			ResetAt:    weeklyReset,
		},
		{
			Connection: "claude-primary",
			Model:      "claude-opus-4-6#weekly",
			Tokens:     1500000,
			Window:     7 * 24 * time.Hour,
			ObservedAt: obs,
			ResetAt:    weeklyReset,
		},
	}
	for _, c := range want {
		if err := store.RecordUsageCapacity(ctx, c); err != nil {
			t.Fatalf("RecordUsageCapacity(%q): %v", c.Model, err)
		}
	}

	// 1. Full database Export/Import preserves every #weekly row faithfully.
	dumpDir := t.TempDir()
	if err := staterepo.Export(ctx, db, dumpDir); err != nil {
		t.Fatalf("Export: %v", err)
	}
	restoredStore, restoredDB := openDB(t)
	if err := staterepo.Import(ctx, restoredDB, dumpDir); err != nil {
		t.Fatalf("Import: %v", err)
	}
	got, err := restoredStore.ListUsageCapacities(ctx)
	if err != nil {
		t.Fatalf("ListUsageCapacities after Import: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("restored %d usage_capacity rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Connection != want[i].Connection || got[i].Model != want[i].Model ||
			got[i].Tokens != want[i].Tokens || got[i].Window != want[i].Window ||
			!got[i].ResetAt.Equal(want[i].ResetAt) {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// 2. grain/config Sync + Load succeeds while #weekly rows exist and leaves them untouched.
	repoDir := t.TempDir()
	repo, err := staterepo.Open(ctx, staterepo.Config{Dir: repoDir})
	if err != nil {
		t.Fatalf("staterepo.Open: %v", err)
	}
	if _, err := staterepo.Sync(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Sync with #weekly rows: %v", err)
	}
	if err := staterepo.Load(ctx, repo, db, model.SchemaVersion); err != nil {
		t.Fatalf("staterepo.Load with #weekly rows: %v", err)
	}
	afterSync, err := store.ListUsageCapacities(ctx)
	if err != nil {
		t.Fatalf("ListUsageCapacities after config Sync+Load: %v", err)
	}
	if len(afterSync) != len(want) {
		t.Fatalf("after config Sync+Load got %d rows, want %d: %+v", len(afterSync), len(want), afterSync)
	}
}
