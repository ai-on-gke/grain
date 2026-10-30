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

package orchestrator_test

import (
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// Task 1493's shape: moved from bwsalmon/grain to ai-on-gke/grain after
// a run had opened bwsalmon/grain#5044, then given ai-on-gke/grain's own
// pull request by the next run. The reconciler watches only the current
// one -- the old one is neither read nor reconciled against the task's
// observation, files nothing, repairs nothing -- and takes the old link
// off the task on its first cycle.
func TestSyncPullRequestsDropsAPullRequestInTheRepoATaskWasMovedOff(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", repo)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	current := model.PullRequestRef{Repo: repo, Number: pr.Number}.String()
	// The simulated GitHub stands for acme/widgets alone and panics on
	// any other repository, so reading the stale pull request at all
	// fails this test.
	task.Links = append(task.Links,
		model.Link{Kind: model.LinkFixes, Target: "bwsalmon/grain#5044"},
		model.Link{Kind: model.LinkFixes, Target: current})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}

	links, err := store.OpenPullRequestLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []model.TaskPullRequestLink{{TaskID: task.ID, PullRequest: current}}; !slices.Equal(links, want) {
		t.Fatalf("OpenPullRequestLinks = %+v, want %+v", links, want)
	}
	if got := model.TaskPullRequest(task); got == nil || got.String() != current {
		t.Fatalf("TaskPullRequest = %v, want %s", got, current)
	}

	for range 2 {
		if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
			t.Fatalf("SyncPullRequests: %v", err)
		}
	}

	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	var fixes []string
	for _, l := range got.Links {
		if l.Kind == model.LinkFixes {
			fixes = append(fixes, l.Target)
		}
	}
	if want := []string{current}; !slices.Equal(fixes, want) {
		t.Fatalf("LinkFixes after sync = %v, want %v", fixes, want)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st == model.StateClosed {
		t.Fatalf("state = %q (%v), want the task still open on its current pull request", st, err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs == nil || obs.PrOpenedAt == nil || obs.PrClosedAt != nil || obs.MergeQueueRepairAt != nil {
		t.Fatalf("observation = %+v, want the current pull request's: open, no repair", obs)
	}
	tasks, err := store.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("tasks = %d, want only the one: nothing filed for a stale pull request", len(tasks))
	}
}

// Somebody closing the stale pull request on GitHub says nothing about a
// task that has since moved to another repository: it is not closed.
func TestSyncPullRequestsDoesNotCloseATaskWhenThePullRequestItWasMovedOffCloses(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	old := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", old)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	stale := model.PullRequestRef{Repo: old, Number: pr.Number}.String()
	moved := model.RepoRef{Owner: "acme", Name: "gizmos"}
	task.Target = &moved
	task.Links = append(task.Links, model.Link{Kind: model.LinkFixes, Target: stale})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}
	for i := range sim.PullRequests {
		sim.PullRequests[i].State = "closed"
	}

	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}

	if st, err := store.State(ctx, task.ID); err != nil || st == model.StateClosed {
		t.Fatalf("state = %q (%v), want the task left open", st, err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if obs != nil && (obs.ClosedAt != nil || obs.PrClosedAt != nil) {
		t.Fatalf("observation = %+v, want nothing recorded for the stale pull request", obs)
	}
	got, err := store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pr := model.TaskPullRequest(*got); pr != nil {
		t.Fatalf("TaskPullRequest = %v, want none", pr)
	}
	for _, l := range got.Links {
		if l.Kind == model.LinkFixes {
			t.Fatalf("links = %+v, want the stale LinkFixes dropped", got.Links)
		}
	}
}

// A pull request in one of the task's own write targets is the task's,
// however the task's main target moved: it is still watched, and its
// closing still closes the task.
func TestSyncPullRequestsStillWatchesAPullRequestInAWriteTarget(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	writes := model.RepoRef{Owner: "acme", Name: "widgets"}
	task := filedTask(t, ctx, store, "t1", writes)
	pushBranch(t, sim.BareRepo, model.BranchName(task.ID))
	pr, err := orchestrator.EnsurePullRequest(ctx, store, client, task, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	target := model.RepoRef{Owner: "acme", Name: "gizmos"}
	task.Target = &target
	task.Writes = []model.WriteTarget{{Repo: writes}}
	task.Links = append(task.Links, model.Link{
		Kind: model.LinkFixes, Target: model.PullRequestRef{Repo: writes, Number: pr.Number}.String()})
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &baseTime}); err != nil {
		t.Fatal(err)
	}
	for i := range sim.PullRequests {
		sim.PullRequests[i].State = "closed"
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	if st, err := store.State(ctx, task.ID); err != nil || st != model.StateClosed {
		t.Fatalf("state = %q (%v), want closed", st, err)
	}
}
