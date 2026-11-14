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
	"fmt"
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

func TestClusterStatsRollsUpWorkByRootDirection(t *testing.T) {
	store, ctx := open(t)
	since := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 7)
	grain := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	// Root direction #1: filed by sam before the window (a repo-less audit
	// direction), which spawned follow-up #2 and grandchild follow-up #3
	// inside the window, both landing pull requests on bwsalmon/grain.
	before := since.Add(-12 * time.Hour)
	if err := store.PutTask(ctx, model.Task{
		ID: "1", Title: "Audit fork workflow", CreatedAt: &before,
		Origin: model.Origin{Attribution: model.Attribution{
			Actor: model.Principal{Kind: model.PrincipalHuman, ID: "sam"}}},
		Binding: model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing root 1: %v", err)
	}
	for _, child := range []struct {
		id, parent     string
		added, deleted int
		hours          int
	}{
		{id: "2", parent: "1", added: 310, deleted: 40, hours: 2},
		{id: "3", parent: "2", added: 190, deleted: 10, hours: 1},
	} {
		filed := since.Add(time.Hour)
		merged := filed.Add(3 * time.Hour)
		if err := store.PutTask(ctx, model.Task{
			ID: child.id, Title: "follow-up " + child.id, Target: &grain, CreatedAt: &filed,
			Origin: model.Origin{Attribution: model.Attribution{
				Actor:      model.Principal{Kind: model.PrincipalAutomation, ID: "grain"},
				OnBehalfOf: &model.Principal{Kind: model.PrincipalAgent, ID: child.parent}}},
			Links:   []model.Link{{Kind: model.LinkProposedBy, Target: child.parent}},
			Binding: model.BindingDirective,
		}); err != nil {
			t.Fatalf("filing child %s: %v", child.id, err)
		}
		if err := store.Observe(ctx, model.Observation{
			TaskID: child.id, PrMergedAt: &merged, CompletedAt: &merged,
			PrAdditions: ptr(child.added), PrDeletions: ptr(child.deleted), PrChangedFiles: ptr(2),
		}); err != nil {
			t.Fatalf("observing child %s: %v", child.id, err)
		}
		run := model.Run{ID: "r" + child.id, TaskID: child.id, Sandbox: "s" + child.id, Attempt: 1, StartedAt: filed}
		if err := store.StartRun(ctx, run, model.Limits{Workers: 10}); err != nil {
			t.Fatalf("starting run for %s: %v", child.id, err)
		}
		if err := store.FinishRun(ctx, run.ID, filed.Add(time.Duration(child.hours)*time.Hour), "succeeded", ""); err != nil {
			t.Fatalf("finishing run for %s: %v", child.id, err)
		}
	}

	// Root direction #4: standalone task filed by sam inside the window,
	// landing one pull request with no follow-up tasks spawned.
	filed4 := since.Add(10 * time.Hour)
	merged4 := filed4.Add(time.Hour)
	if err := store.PutTask(ctx, model.Task{
		ID: "4", Title: "Standalone fix", Target: &grain, CreatedAt: &filed4,
		Origin: model.Origin{Attribution: model.Attribution{
			Actor: model.Principal{Kind: model.PrincipalHuman, ID: "sam"}}},
		Binding: model.BindingDirective,
	}); err != nil {
		t.Fatalf("filing root 4: %v", err)
	}
	if err := store.Observe(ctx, model.Observation{
		TaskID: "4", PrMergedAt: &merged4, CompletedAt: &merged4,
		PrAdditions: ptr(45), PrDeletions: ptr(5), PrChangedFiles: ptr(1),
	}); err != nil {
		t.Fatalf("observing root 4: %v", err)
	}

	stats, err := store.ClusterStats(ctx, since, until)
	if err != nil {
		t.Fatalf("ClusterStats: %v", err)
	}
	if len(stats.Directions) != 2 {
		t.Fatalf("directions = %+v, want the 2 root directions active in the window", stats.Directions)
	}
	first := stats.Directions[0]
	if first.TaskID != "1" || first.Title != "Audit fork workflow" {
		t.Errorf("first direction = %+v, want root #1 Audit fork workflow", first)
	}
	if first.Author.Kind != model.PrincipalHuman || first.Author.ID != "sam" {
		t.Errorf("first direction author = %+v, want human:sam rather than the automation that filed the proposals", first.Author)
	}
	if first.Repo == nil || *first.Repo != grain {
		t.Errorf("first direction repo = %v, want fallback to child repo %s", first.Repo, grain)
	}
	if first.PullRequestsMerged != 2 || first.PullRequestsMeasured != 2 {
		t.Errorf("first direction merged/measured = %d/%d, want 2/2", first.PullRequestsMerged, first.PullRequestsMeasured)
	}
	if first.LinesAdded != 500 || first.LinesDeleted != 50 {
		t.Errorf("first direction lines = +%d -%d, want +500 -50", first.LinesAdded, first.LinesDeleted)
	}
	if first.TasksCompleted != 2 || first.TasksFiled != 2 || first.TasksSpawned != 2 {
		t.Errorf("first direction completed/filed/spawned = %d/%d/%d, want 2/2/2",
			first.TasksCompleted, first.TasksFiled, first.TasksSpawned)
	}
	if first.Runs != 2 || first.RunsSucceeded != 2 || first.AgentTime != 3*time.Hour {
		t.Errorf("first direction runs = %d (%d succeeded, %s), want 2 (2 succeeded, 3h)",
			first.Runs, first.RunsSucceeded, first.AgentTime)
	}

	second := stats.Directions[1]
	if second.TaskID != "4" || second.PullRequestsMerged != 1 || second.TasksSpawned != 0 {
		t.Errorf("second direction = %+v, want root #4 with 1 merge and 0 spawned", second)
	}
}

func TestClusterStatsBatchesMoreThanOneChunkOfDirectionsAndTasks(t *testing.T) {
	store, ctx := open(t)
	since := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	until := since.AddDate(0, 0, 7)
	grain := model.RepoRef{Owner: "bwsalmon", Name: "grain"}

	// Create 260 tasks spanning > 200 (the SQL IN chunk size): 130 roots each
	// spawning one child via Origin.
	for i := 0; i < 130; i++ {
		rootID := fmt.Sprintf("r-%03d", i)
		childID := fmt.Sprintf("c-%03d", i)
		if err := store.PutTask(ctx, model.Task{
			ID: rootID, Title: "root " + rootID, Target: &grain, CreatedAt: &since,
			Origin:  model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "sam"}}},
			Binding: model.BindingDirective,
		}); err != nil {
			t.Fatalf("PutTask %s: %v", rootID, err)
		}
		if err := store.PutTask(ctx, model.Task{
			ID: childID, Title: "child " + childID, Target: &grain, CreatedAt: &since,
			Origin:  model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}},
			Links:   []model.Link{{Kind: model.LinkProposedBy, Target: rootID}},
			Binding: model.BindingDirective,
		}); err != nil {
			t.Fatalf("PutTask %s: %v", childID, err)
		}
		merged := since.Add(time.Duration(i+1) * time.Minute)
		if err := store.Observe(ctx, model.Observation{TaskID: childID, PrMergedAt: &merged, CompletedAt: &merged}); err != nil {
			t.Fatalf("Observe %s: %v", childID, err)
		}
	}

	stats, err := store.ClusterStats(ctx, since, until)
	if err != nil {
		t.Fatalf("ClusterStats: %v", err)
	}
	if len(stats.Directions) != 130 {
		t.Fatalf("directions = %d, want 130", len(stats.Directions))
	}
	for _, d := range stats.Directions {
		if d.TasksSpawned != 1 || d.PullRequestsMerged != 1 || d.Title != "root "+d.TaskID {
			t.Fatalf("unexpected chunked direction rollup %+v", d)
		}
	}
}

func ptr[T any](v T) *T { return &v }
