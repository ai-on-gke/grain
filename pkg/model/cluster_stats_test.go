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
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// What the week's hero numbers are counted from, and the two mistakes
// they must not make: counting work outside the window, and losing work
// that belongs to no repository.

func TestClusterStatsCountsTheWeeksWorkAcrossEveryRepo(t *testing.T) {
	store, ctx := open(t)
	since := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 7)
	grain := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	kontur := model.RepoRef{Owner: "bwsalmon", Name: "kontur"}

	// Two merged in one repo and one in another, inside the week; one
	// merged the week before, which is the boundary this is really
	// about; and a task with no repository at all, which is what a
	// question or a memory agent run looks like.
	filed := since.Add(time.Hour)
	for _, c := range []struct {
		id     string
		repo   *model.RepoRef
		merged *time.Time
	}{
		{"1", &grain, ptr(since.Add(24 * time.Hour))},
		{"2", &grain, ptr(since.Add(48 * time.Hour))},
		{"3", &kontur, ptr(since.Add(72 * time.Hour))},
		{"4", &grain, ptr(since.Add(-24 * time.Hour))},
		{"5", nil, nil},
	} {
		task := model.Task{
			ID: c.id, Title: "task " + c.id, Target: c.repo, CreatedAt: &filed,
			Origin: model.Origin{Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalHuman, ID: "sam"}}},
			Binding: model.BindingDirective,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("filing %s: %v", c.id, err)
		}
		obs := model.Observation{TaskID: c.id, CompletedAt: ptr(filed.Add(time.Hour))}
		if c.merged != nil {
			obs.PrMergedAt = c.merged
			obs.CompletedAt = c.merged
		}
		if err := store.Observe(ctx, obs); err != nil {
			t.Fatalf("observing %s: %v", c.id, err)
		}
	}

	stats, err := store.ClusterStats(ctx, since, until)
	if err != nil {
		t.Fatalf("ClusterStats: %v", err)
	}
	if stats.PullRequestsMerged != 3 {
		t.Errorf("merged = %d, want the 3 inside the window and not the one before it",
			stats.PullRequestsMerged)
	}
	if stats.TasksFiled != 5 {
		t.Errorf("filed = %d, want every task filed in the window including the one with no repo",
			stats.TasksFiled)
	}
	if len(stats.Repos) != 2 {
		t.Fatalf("repos = %+v, want the two that had work in them", stats.Repos)
	}
	// Most merged first, so the top of the list is where the week went.
	if stats.Repos[0].Repo != grain || stats.Repos[0].PullRequestsMerged != 2 {
		t.Errorf("first repo = %+v, want bwsalmon/grain with 2 merged", stats.Repos[0])
	}
	if stats.Repos[1].Repo != kontur || stats.Repos[1].PullRequestsMerged != 1 {
		t.Errorf("second repo = %+v, want bwsalmon/kontur with 1 merged", stats.Repos[1])
	}
	// The per-repo rows add up to less than the totals, on purpose: a
	// task with no target is the deployment's work and nobody's
	// repository.
	var perRepo int
	for _, w := range stats.Repos {
		perRepo += w.TasksFiled
	}
	if perRepo != 4 {
		t.Errorf("per-repo filed = %d, want the 4 that named a repository", perRepo)
	}
}

func TestClusterStatsCountsRunsAndTheTimeTheyTook(t *testing.T) {
	store, ctx := open(t)
	since := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 7)
	repo := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	task := model.Task{
		ID: "1", Title: "a task", Target: &repo, CreatedAt: &since,
		Origin: model.Origin{Attribution: model.Attribution{
			Actor: model.Principal{Kind: model.PrincipalHuman, ID: "sam"}}},
		Binding: model.BindingDirective,
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing: %v", err)
	}
	for i, r := range []struct {
		id      string
		started time.Time
		hours   int
		outcome string
	}{
		{"r1", since.Add(time.Hour), 2, "succeeded"},
		{"r2", since.Add(10 * time.Hour), 1, "failed"},
		// The week before: outside the window, and so outside every
		// number here.
		{"r3", since.Add(-20 * time.Hour), 3, "succeeded"},
	} {
		run := model.Run{ID: r.id, TaskID: "1", Sandbox: "s" + r.id, Attempt: i + 1, StartedAt: r.started}
		if err := store.StartRun(ctx, run, model.Limits{Workers: 10}); err != nil {
			t.Fatalf("starting %s: %v", r.id, err)
		}
		finished := r.started.Add(time.Duration(r.hours) * time.Hour)
		if err := store.FinishRun(ctx, r.id, finished, r.outcome, ""); err != nil {
			t.Fatalf("finishing %s: %v", r.id, err)
		}
	}

	stats, err := store.ClusterStats(ctx, since, until)
	if err != nil {
		t.Fatalf("ClusterStats: %v", err)
	}
	if stats.Runs != 2 || stats.RunsSucceeded != 1 {
		t.Errorf("runs = %d (%d succeeded), want 2 and 1", stats.Runs, stats.RunsSucceeded)
	}
	if stats.AgentTime != 3*time.Hour {
		t.Errorf("agent time = %s, want the 3 hours inside the window", stats.AgentTime)
	}
	if len(stats.Repos) != 1 || stats.Repos[0].Runs != 2 {
		t.Errorf("repos = %+v, want the one repo with both runs", stats.Repos)
	}
}

// The lines the week's merges changed, and the thing that figure must
// never do quietly: report a total over rows that carry no size as
// though those pull requests had changed nothing. A merge recorded
// before grain had the columns, and a merge GitHub reported no size
// for, both read back as "not measured" -- so the sums are short and
// PullRequestsMeasured is what says by how much.
func TestClusterStatsSumsTheLinesMergedPullRequestsChanged(t *testing.T) {
	store, ctx := open(t)
	since := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 7)
	grain := model.RepoRef{Owner: "bwsalmon", Name: "grain"}
	kontur := model.RepoRef{Owner: "bwsalmon", Name: "kontur"}

	for _, c := range []struct {
		id             string
		repo           *model.RepoRef
		merged         time.Time
		added, deleted int
		measured       bool
	}{
		{id: "1", repo: &grain, merged: since.Add(24 * time.Hour), added: 200, deleted: 30, measured: true},
		{id: "2", repo: &grain, merged: since.Add(48 * time.Hour), added: 14, deleted: 9, measured: true},
		// Merged in the window, but from before grain recorded a size:
		// the row carries NULL, not a zero.
		{id: "3", repo: &grain, merged: since.Add(72 * time.Hour)},
		// Measured, but in another repository -- so the per-repo rows
		// and the totals have to differ in the right way.
		{id: "4", repo: &kontur, merged: since.Add(96 * time.Hour), added: 51, deleted: 51, measured: true},
		// Measured, and merged the week before: outside every number
		// here, lines included.
		{id: "5", repo: &grain, merged: since.Add(-24 * time.Hour), added: 9000, deleted: 9000, measured: true},
	} {
		task := model.Task{
			ID: c.id, Title: "task " + c.id, Target: c.repo, CreatedAt: &since,
			Origin: model.Origin{Attribution: model.Attribution{
				Actor: model.Principal{Kind: model.PrincipalHuman, ID: "sam"}}},
			Binding: model.BindingDirective,
		}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatalf("filing %s: %v", c.id, err)
		}
		obs := model.Observation{TaskID: c.id, PrMergedAt: ptr(c.merged), CompletedAt: ptr(c.merged)}
		if c.measured {
			obs.PrAdditions, obs.PrDeletions = ptr(c.added), ptr(c.deleted)
			obs.PrChangedFiles = ptr(1)
		}
		if err := store.Observe(ctx, obs); err != nil {
			t.Fatalf("observing %s: %v", c.id, err)
		}
	}

	stats, err := store.ClusterStats(ctx, since, until)
	if err != nil {
		t.Fatalf("ClusterStats: %v", err)
	}
	if stats.PullRequestsMerged != 4 {
		t.Fatalf("merged = %d, want the 4 inside the window", stats.PullRequestsMerged)
	}
	if stats.PullRequestsMeasured != 3 {
		t.Errorf("measured = %d, want 3: the fourth merge carries no size at all",
			stats.PullRequestsMeasured)
	}
	if stats.LinesAdded != 265 || stats.LinesDeleted != 90 {
		t.Errorf("lines = +%d -%d, want +265 -90 over the three measured merges",
			stats.LinesAdded, stats.LinesDeleted)
	}
	if len(stats.Repos) != 2 {
		t.Fatalf("repos = %+v, want the two the week touched", stats.Repos)
	}
	first := stats.Repos[0]
	if first.Repo != grain || first.PullRequestsMerged != 3 || first.PullRequestsMeasured != 2 {
		t.Errorf("first repo = %+v, want bwsalmon/grain with 3 merged and 2 of them measured", first)
	}
	if first.LinesAdded != 214 || first.LinesDeleted != 39 {
		t.Errorf("first repo lines = +%d -%d, want +214 -39", first.LinesAdded, first.LinesDeleted)
	}
	second := stats.Repos[1]
	if second.Repo != kontur || second.LinesAdded != 51 || second.LinesDeleted != 51 {
		t.Errorf("second repo = %+v, want bwsalmon/kontur with +51 -51", second)
	}
}

func TestClusterStatsRefusesAWindowThatEndsBeforeItStarts(t *testing.T) {
	store, ctx := open(t)
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	if _, err := store.ClusterStats(ctx, now, now.Add(-time.Hour)); err == nil {
		t.Error("a backwards window was counted; every number over it would be zero and read as a quiet week")
	}
}

func ptr[T any](v T) *T { return &v }
