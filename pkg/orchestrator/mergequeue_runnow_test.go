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

// The merge queue's half of "Run now" (ui.Client.RunNow): what it does
// with an entry somebody has put an agent back on themselves.
//
// Most such tasks the queue never sees -- asking for a run clears the
// task's completion, which takes it out of Store.OpenPullRequestLinks
// until the run puts it back. The one that stays in the list is the one
// this button was most asked for: a task the queue repaired, gave up on,
// and left for a person, whose repair stamp keeps it a queue entry. The
// queue must not merge that branch, or push a base merge to it, while
// somebody's agent is writing to it.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/github"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// runNow is a person clicking "Run now", through ui.Client itself so
// that what these tests put in front of the queue is exactly what the
// button does -- followed by the run the request asks for, since it is
// the live run, not the request, that the queue reads (dispatch.Cycle is
// what starts one, and none of it runs here).
func runNow(t *testing.T, ctx context.Context, store *model.Store, taskID string, now time.Time) string {
	t.Helper()
	client := ui.NewClient(ui.Config{Actor: ui.DefaultActor("alice")}, store)
	client.Now = func() time.Time { return now }
	if err := client.RunNow(ctx, taskID); err != nil {
		t.Fatalf("RunNow(%s): %v", taskID, err)
	}
	runID := taskID + "-run-now"
	if err := store.StartRun(ctx, model.Run{
		ID: runID, TaskID: taskID, Attempt: 99, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting the requested run of %s: %v", taskID, err)
	}
	return runID
}

// repairedAndBlocked drives a task all the way through the queue's own
// one-repair policy: conflicted head, repair asked for, repair finished,
// still conflicted, queue gives up. That is the state a person finds
// when they come to fix something by hand -- and the one they reach for
// "Run now" from.
func repairedAndBlocked(t *testing.T, ctx context.Context, store *model.Store,
	client *github.RESTClient, taskID string) model.Task {

	t.Helper()
	obs := func() *model.Observation {
		t.Helper()
		o, err := store.GetObservation(ctx, taskID)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("asking for the repair: %v", err)
	}
	if obs().MergeQueueRepairAt == nil {
		t.Fatal("no repair was asked for, so this task never reached the state under test")
	}
	finished := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, taskID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{finished, finished.Add(time.Minute)} {
		if err := orchestrator.SyncPullRequests(ctx, store, client, at); err != nil {
			t.Fatalf("settling the repair round: %v", err)
		}
	}
	if obs().MergeQueueBlockedAt == nil {
		t.Fatal("the queue never gave up, so this task never reached the state under test")
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return *task
}

// A task the queue gave up on merges the moment it reads clean, whoever
// made it clean -- which is right, and wrong while an agent is in the
// middle of pushing to it. That agent was sent to do something to this
// change, and a merge lands what was there before it started.
func TestSyncPullRequestsDoesNotMergeWhileAnAgentIsOnTheBranch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, branch := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)
	repairedAndBlocked(t, ctx, store, client, task.ID)

	// Somebody has had a look, put the conflict right, and asked for an
	// agent to carry on with the rest of it.
	setMergeable(sim, true)
	sim.CheckRuns[branch] = []github.CheckRun{
		{Name: "tests", Status: "completed", Conclusion: strPtr("success")},
	}
	at := baseTime.Add(3 * time.Hour)
	runID := runNow(t, ctx, store, task.ID, at)

	if err := orchestrator.SyncPullRequests(ctx, store, client, at.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	for _, pr := range sim.PullRequests {
		if pr.Merged {
			t.Fatalf("%s#%d merged while a run was in flight on its branch", repo, pr.Number)
		}
	}

	// And it is a wait, not a refusal: the run ends, the task completes
	// again, and the next cycle merges it exactly as it would have.
	done := at.Add(time.Hour)
	if err := store.FinishRun(ctx, runID, done, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveField(ctx, task.ID, done, func(o *model.Observation) {
		o.CompletedAt = &done
	}); err != nil {
		t.Fatal(err)
	}
	if err := orchestrator.SyncPullRequests(ctx, store, client, done.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests after the run: %v", err)
	}
	merged := false
	for _, pr := range sim.PullRequests {
		merged = merged || pr.Merged
	}
	if !merged {
		t.Fatal("the pull request never merged once the run it was waiting for had finished")
	}
}

// The queue's other move on a head is to write to its branch itself --
// merge the base in, or ask an agent for a repair. Asking for a run
// returns both of those (it clears the failure streak, which is what
// advanceMergeQueueHead reads as "this head is worth another round"), so
// the guard has to hold there too: the person who asked for the run is
// the one deciding what happens to this branch now.
func TestSyncPullRequestsLeavesAFailingHeadAloneWhileItIsBeingWorkedOn(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sim, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	task, _ := queuedTaskWithPullRequest(t, ctx, store, sim, client, "t1", repo)
	setMergeable(sim, false)

	// One repair round, finished, with the pull request still conflicted
	// -- but read before the queue's own next cycle, so the task is a
	// live queue entry rather than one it has given up on.
	if err := orchestrator.SyncPullRequests(ctx, store, client, baseTime); err != nil {
		t.Fatalf("asking for the repair: %v", err)
	}
	repairAt := repairAskedAt(t, ctx, store, task.ID)
	finished := baseTime.Add(time.Hour)
	if err := store.ObserveField(ctx, task.ID, finished, func(o *model.Observation) {
		o.CompletedAt = &finished
	}); err != nil {
		t.Fatal(err)
	}

	at := finished.Add(time.Minute)
	runNow(t, ctx, store, task.ID, at)

	if err := orchestrator.SyncPullRequests(ctx, store, client, at.Add(time.Minute)); err != nil {
		t.Fatalf("SyncPullRequests: %v", err)
	}
	obs, err := store.GetObservation(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.MergeQueueRepairAt.Equal(repairAt) {
		t.Fatalf("MergeQueueRepairAt = %v, want the first round's %v: no second repair under a live run",
			obs.MergeQueueRepairAt, repairAt)
	}
	if obs.MergeQueueRefreshedAt != nil {
		t.Fatalf("MergeQueueRefreshedAt = %v, want no base merge pushed under a live run",
			obs.MergeQueueRefreshedAt)
	}
	if obs.MergeQueueBlockedAt != nil {
		t.Fatalf("MergeQueueBlockedAt = %v, want the queue to wait rather than give up on a task being worked on",
			obs.MergeQueueBlockedAt)
	}
}
