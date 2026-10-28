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

package ui_test

// The task lane's stall flag, from the reader's end: a task whose own
// state has stood longer than that state should take says so on the row
// beside the badge, on the pane the row opens, and nowhere else.
//
// What the sentence says is orchestrator's (liveness.go's taskLane, which
// owns the patience per state and the words that go with it, and whose
// own tests hold it to them). What is tested here is the wiring: that
// every task read asks, that it asks about the right moment, and that it
// says nothing at all when this deployment was handed no deadlines.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// stallClient is testClient with the seam filled: a stand-in for
// orchestrator.TaskStallNote that flags anything queued for an hour, so
// the test states its own deadline rather than reaching for the
// deployment's (three days, which a test that moved the clock by would be
// asserting the orchestrator's number a second time).
func stallClient(t *testing.T, now func() time.Time) (*ui.Client, *model.Store, context.Context) {
	t.Helper()
	ui.LogToTest(t)
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	client := ui.NewClient(ui.Config{
		Actor:         ui.DefaultActor("alice"),
		DefaultTarget: &repo,
		Capabilities:  ui.OfferedCapabilities(),
		Stall: func(state model.State, since, at time.Time) string {
			if state != model.StateQueued || at.Sub(since) < time.Hour {
				return ""
			}
			return "this has stood here for more than 1h"
		},
	}, store)
	client.Now = now
	return client, store, ctx
}

func TestAQueuedTaskNobodyDispatchesSaysSoOnItsRow(t *testing.T) {
	t.Parallel()
	clock := baseTime
	client, _, ctx := stallClient(t, func() time.Time { return clock })

	task := create(t, client, ctx)
	// Filed a moment ago: waiting, not stuck, and the row says nothing.
	// An empty note here is the answer for nearly every task there is, so
	// it has to be the answer for a healthy one.
	listed := listOne(t, client, ctx, task.ID)
	if listed.Stall != "" {
		t.Errorf("a freshly queued task is flagged: %q", listed.Stall)
	}
	if one, err := client.Task(ctx, task.ID); err != nil || one.Stall != "" {
		t.Errorf("Task(%s).Stall = %q (%v), want nothing yet", task.ID, one.Stall, err)
	}

	// And a day later, measured from the approval, which is when this
	// task became something dispatch could take.
	clock = baseTime.Add(24 * time.Hour)
	listed = listOne(t, client, ctx, task.ID)
	if listed.Stall == "" {
		t.Error("a task queued for a day says nothing on the list")
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reading the task: %v", err)
	}
	if detail.Stall != listed.Stall {
		t.Errorf("the pane and the row disagree:\n  pane: %q\n  row:  %q", detail.Stall, listed.Stall)
	}
}

func TestARunningTaskIsMeasuredFromItsOwnRunNotItsApproval(t *testing.T) {
	t.Parallel()
	clock := baseTime
	client, store, ctx := stallClient(t, func() time.Time { return clock })
	task := create(t, client, ctx)

	// The stand-in flags nothing but 'queued', so a task that is running
	// says nothing however long ago it was approved -- which is the point
	// being made: the row is measured against the state it is in now.
	clock = baseTime.Add(24 * time.Hour)
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: clock,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	listed := listOne(t, client, ctx, task.ID)
	if listed.State != model.StateRunning {
		t.Fatalf("state = %s, want running", listed.State)
	}
	if listed.Stall != "" {
		t.Errorf("a run that started this instant is flagged: %q", listed.Stall)
	}
}

func TestATaskSomebodyHasJustRetriedIsNotFlaggedForTheWaitBeforeIt(t *testing.T) {
	t.Parallel()
	clock := baseTime
	client, store, ctx := stallClient(t, func() time.Time { return clock })
	task := create(t, client, ctx)

	// A run that ended a day ago, and a retry a minute ago: the task is
	// queued, and the wait that is actually going on is a minute old.
	// Measured from the approval it would be flagged for the one thing
	// that is not standing still.
	if err := store.StartRun(ctx, model.Run{
		ID: "r1", TaskID: task.ID, Sandbox: "s1", Attempt: 1, StartedAt: baseTime,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if err := store.FinishRun(ctx, "r1", baseTime.Add(time.Hour), "failed", "no"); err != nil {
		t.Fatalf("finishing the run: %v", err)
	}
	clock = baseTime.Add(48 * time.Hour)
	retried := clock.Add(-time.Minute)
	if err := store.ObserveField(ctx, task.ID, clock, func(o *model.Observation) {
		o.RetryRequestedAt = &retried
	}); err != nil {
		t.Fatalf("recording the retry: %v", err)
	}
	if listed := listOne(t, client, ctx, task.ID); listed.Stall != "" {
		t.Errorf("a task retried a minute ago is flagged: %q", listed.Stall)
	}

	// Two hours later it is genuinely standing still again, and says so.
	clock = clock.Add(2 * time.Hour)
	if listed := listOne(t, client, ctx, task.ID); listed.Stall == "" {
		t.Error("a task queued for two hours since its retry says nothing")
	}
}

func TestATaskPickedBackUpIsNotFlaggedForTheDeferralBeforeIt(t *testing.T) {
	t.Parallel()
	clock := baseTime
	client, _, ctx := stallClient(t, func() time.Time { return clock })
	task := create(t, client, ctx)

	// Approved today, put aside this afternoon, and picked back up eight
	// months later. The wait that is actually going on is a minute old;
	// measured from the approval it is eight months, and the row would be
	// flagged on the first read after the task came back -- which is the
	// one thing that is not standing still.
	clock = baseTime.Add(4 * time.Hour)
	if err := client.Defer(ctx, task.ID, ui.DeferOptions{}); err != nil {
		t.Fatalf("deferring: %v", err)
	}
	clock = baseTime.Add(8 * 30 * 24 * time.Hour)
	if listed := listOne(t, client, ctx, task.ID); listed.State != model.StateDeferred || listed.Stall != "" {
		t.Fatalf("a deferred task reads %q and is flagged %q, want deferred and nothing: nobody is "+
			"waiting on a task somebody parked", listed.State, listed.Stall)
	}
	if err := client.Undefer(ctx, task.ID); err != nil {
		t.Fatalf("picking it back up: %v", err)
	}

	clock = clock.Add(time.Minute)
	listed := listOne(t, client, ctx, task.ID)
	if listed.State != model.StateQueued {
		t.Fatalf("state after picking it back up = %s, want queued", listed.State)
	}
	if listed.Stall != "" {
		t.Errorf("a task picked back up a minute ago is flagged: %q", listed.Stall)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reading the task: %v", err)
	}
	if detail.Stall != "" {
		t.Errorf("the pane flags a task picked back up a minute ago: %q", detail.Stall)
	}

	// Two hours on it really is standing still again, and says so -- the
	// stamp restarts the clock, it does not switch the flag off.
	clock = clock.Add(2 * time.Hour)
	if listed := listOne(t, client, ctx, task.ID); listed.Stall == "" {
		t.Error("a task queued for two hours since it came back says nothing")
	}
}

func TestATaskReadSaysNothingWhenThisDeploymentHasNoDeadlines(t *testing.T) {
	t.Parallel()
	// ui.Config.Stall's nil-means-unavailable contract: `grain demo`, and
	// any UI not colocated with an orchestrator whose deadlines it could
	// speak for. Nothing is flagged, and nothing errors.
	client, _, ctx := testClient(t)
	client.Now = func() time.Time { return baseTime.Add(30 * 24 * time.Hour) }
	task := create(t, client, ctx)

	if listed := listOne(t, client, ctx, task.ID); listed.Stall != "" {
		t.Errorf("a UI with no deadlines flagged a row: %q", listed.Stall)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("reading the task: %v", err)
	}
	if detail.Stall != "" {
		t.Errorf("a UI with no deadlines flagged a pane: %q", detail.Stall)
	}
}

func listOne(t *testing.T, c *ui.Client, ctx context.Context, id string) ui.Task {
	t.Helper()
	list, err := c.ListTasks(ctx)
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	for _, task := range list {
		if task.ID == id {
			return task
		}
	}
	t.Fatalf("task %s is not in the list", id)
	return ui.Task{}
}
