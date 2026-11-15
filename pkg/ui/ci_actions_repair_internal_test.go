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

package ui

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

// A listing of main's runs names the open default-branch repair, which
// the release train repeats when it has nothing to cut; a listing of any
// other branch, or with no repair open, says nothing of it.
func TestAListingOfMainNamesItsOpenRepair(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := sqlitetest.Open(t)
	c := &Client{Store: store}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if got := c.openBaseRepairNote(ctx, repo, "main"); got != "" {
		t.Fatalf("no repair open, note %q", got)
	}
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	if err := store.PutTask(ctx, model.Task{ID: "4801", Title: "Fix the red tests.yml on main", Target: &repo,
		Tags: []string{model.GitHubBaseRepairTag(repo)}, CreatedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if got, want := c.openBaseRepairNote(ctx, repo, "refs/heads/main"), "repair task 4801 is open for the red main"; got != want {
		t.Errorf("note %q, want %q", got, want)
	}
	if got := c.openBaseRepairNote(ctx, repo, "feature-x"); got != "" {
		t.Errorf("a listing of feature-x says %q", got)
	}
}
