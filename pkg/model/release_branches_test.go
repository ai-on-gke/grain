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

package model_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

func TestIsTaskBranch(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		template, name string
		want           bool
	}{
		{"", "grain/task-12", true},
		{"", "grain/task-12-fix-the-parser", true},
		{"", "grain/049f1e09/task-123", true},
		{"", "grain/other-deploy/task-7-slug", true},
		{model.DefaultTaskBranchTemplate, "grain/prod/task-56", true},
		{model.DefaultTaskBranchTemplate, "grain/prod/task-56-fix-header-parser", true},
		{"agents/{deployment}/{id}-{slug}", "agents/prod/56", true},
		{"agents/{deployment}/{id}-{slug}", "agents/prod/56-fix-it", true},
		{"agents/{deployment}/{id}-{slug}", "agents/other/56", false},
		{"", "main", false},
		{"", "candidate", false},
		{"", "2.4", false},
		{"", "2.4.latest", false},
		{"", "grain/task-abc", false},
		{"", "release/grain/task-1", false},
		{"", "", false},
	} {
		if got := model.IsTaskBranch(tc.template, "inst", "prod", tc.name); got != tc.want {
			t.Errorf("IsTaskBranch(%q, %q) = %v, want %v", tc.template, tc.name, got, tc.want)
		}
	}
}

// ReleaseBranches is the default branch, the paired branches and the open
// releases' branches -- and a task's branch never, even when a
// configuration names one.
func TestReleaseBranchesNeverIncludeATaskBranch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := sqlitetest.Open(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{
		Repo: repo, PortBranches: []string{"main", "candidate", "grain/task-9"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateRelease(ctx, repo, "2.4", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReleaseBranches(ctx, repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"main", "candidate", "2.4", "2.4.latest"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReleaseBranches = %v, want %v", got, want)
	}
}

func TestPurgeTaskBranchBaseRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _ := sqlitetest.Open(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	now := time.Now().UTC()
	for _, b := range []string{"main", "grain/049f1e09/task-123"} {
		if err := store.PutBaseBranchCI(ctx, model.BaseBranchCI{Repo: repo, Branch: b, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutBaseBranchRelease(ctx, model.BaseBranchRelease{Repo: repo, Branch: b, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutBaseBranchDispatch(ctx, model.BaseBranchDispatch{Repo: repo, Branch: b, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := store.PurgeTaskBranchBaseRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 3 {
		t.Fatalf("removed %v, want the task branch's three rows", removed)
	}
	rows, err := store.ListBaseBranchCI(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Branch != "main" {
		t.Fatalf("base branch CI rows left = %+v, want main's alone", rows)
	}
	releases, err := store.ListBaseBranchRelease(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].Branch != "main" {
		t.Fatalf("release rows left = %+v, want main's alone", releases)
	}
	dispatches, err := store.ListBaseBranchDispatch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatches) != 1 || dispatches[0].Branch != "main" {
		t.Fatalf("dispatch rows left = %+v, want main's alone", dispatches)
	}
}
