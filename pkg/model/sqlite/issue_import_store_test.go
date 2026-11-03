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

package sqlite_test

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRepoConfigIssueImportRoundTrips(t *testing.T) {
	store, _, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	want := model.IssueImport{Enabled: true, Repo: "kubernetes/autoscaler", Label: "bug", Approved: true, IntervalSeconds: 600}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo, IssueImport: want}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetRepoConfig(ctx, repo)
	if err != nil || got == nil {
		t.Fatalf("GetRepoConfig: %v, %v", got, err)
	}
	if got.IssueImport != want {
		t.Errorf("GetRepoConfig's IssueImport = %+v, want %+v", got.IssueImport, want)
	}
	list, err := store.ListRepoConfigs(ctx)
	if err != nil || len(list) != 1 || list[0].IssueImport != want {
		t.Errorf("ListRepoConfigs = %+v, %v; want one row with %+v", list, err, want)
	}

	// The importer's settings alone are something to say: a row holding
	// nothing else is kept, and one that drops them goes.
	if (model.RepoConfig{Repo: repo, IssueImport: model.IssueImport{Label: "x"}}).Empty() {
		t.Error("a config naming only an import label reads as empty")
	}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: repo}); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetRepoConfig(ctx, repo); err != nil || got != nil {
		t.Errorf("after clearing it, GetRepoConfig = %+v, %v; want no row", got, err)
	}
}

func TestIssueImportIntervalDefaultsAndFloors(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		want    time.Duration
	}{
		{0, model.DefaultIssueImportInterval},
		{-5, model.DefaultIssueImportInterval},
		{5, model.MinIssueImportInterval},
		{900, 15 * time.Minute},
	} {
		if got := (model.IssueImport{IntervalSeconds: tc.seconds}).Interval(); got != tc.want {
			t.Errorf("Interval() for %d seconds = %v, want %v", tc.seconds, got, tc.want)
		}
	}
}

func TestTaskForIssueFindsTasksInAnyStateByOriginOrNumber(t *testing.T) {
	store, _, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	upstream := model.RepoRef{Owner: "kubernetes", Name: "autoscaler"}
	put := func(task model.Task) {
		t.Helper()
		task.Intent = model.IntentImplement
		task.Binding = model.BindingDirective
		task.Origin = model.Origin{Attribution: model.Attribution{Actor: model.TriggerPrincipal}, Reason: model.ReasonDirect}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	put(model.Task{ID: "1", Title: "by origin", Target: &repo, WorkOrigin: model.GitHubOrigin(upstream, 7)})
	put(model.Task{ID: "2", Title: "by number", Target: &repo, Issue: 3})
	closedAt := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	if err := store.ObserveField(ctx, "1", closedAt, func(o *model.Observation) { o.ClosedAt = &closedAt }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		source model.RepoRef
		number int
		want   string
	}{
		{upstream, 7, "1"},
		// Spelled differently, still the same issue.
		{model.RepoRef{Owner: "Kubernetes", Name: "Autoscaler"}, 7, "1"},
		{repo, 3, "2"},
		// Issue 3 of another repository is not the target's issue 3.
		{upstream, 3, ""},
		{repo, 8, ""},
	} {
		got, err := store.TaskForIssue(ctx, tc.source, tc.number, repo)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("TaskForIssue(%s#%d) = %q, want %q", tc.source, tc.number, got, tc.want)
		}
	}
}
