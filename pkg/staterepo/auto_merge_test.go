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

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// Tasks 2387, 2395 and 2396 read auto_merge false before the 2026-11-02
// 03:34 restart and true after it, without being dispatched. Neither
// import a restart can run -- the schema-upgrade handover's whole-database
// one, nor the config repository's settings one -- moves a task's
// auto-merge or the explicit "no" that keeps it off.
func TestAutoMergeSurvivesTheStateImports(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db := openDB(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	declined := model.Task{ID: "2387", Title: "review me", Body: "wait for the operator", Target: &repo, CreatedAt: &now}
	model.DeclineAutoMerge(&declined, "agent:2167")
	merging := model.Task{ID: "2388", Title: "routine", Body: "lands itself", Target: &repo, CreatedAt: &now, AutoMerge: true}
	for _, task := range []model.Task{declined, merging} {
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("putting %s: %v", task.ID, err)
		}
	}
	check := func(what string, s *model.Store) {
		t.Helper()
		for id, want := range map[string]bool{"2387": false, "2388": true} {
			got, err := s.GetTask(ctx, id)
			if err != nil || got == nil {
				t.Fatalf("%s: reading %s: %v", what, id, err)
			}
			if got.AutoMerge != want || model.AutoMergeDeclined(*got) == want {
				t.Errorf("%s: %s autoMerge=%v declined=%v, want autoMerge=%v", what, id,
					got.AutoMerge, model.AutoMergeDeclined(*got), want)
			}
		}
	}

	dir := t.TempDir()
	if err := staterepo.Export(ctx, db, dir); err != nil {
		t.Fatalf("exporting: %v", err)
	}
	if err := staterepo.ImportTables(ctx, db, dir, staterepo.SettingsTables); err != nil {
		t.Fatalf("importing the settings tables: %v", err)
	}
	check("after the settings import", store)

	other, otherDB := openDB(t)
	if err := staterepo.Import(ctx, otherDB, dir); err != nil {
		t.Fatalf("importing the whole dump: %v", err)
	}
	check("after the schema-upgrade import", other)
}
