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

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// startedTask files one approved task and puts a live run on it, which is
// the only state in which a synopsis means anything.
func startedTask(t *testing.T, store *model.Store, ctx context.Context, id string) {
	t.Helper()
	if err := store.PutTask(ctx, task(id, true)); err != nil {
		t.Fatalf("creating %s: %v", id, err)
	}
	if err := store.StartRun(ctx, model.Run{
		ID: "run-" + id, TaskID: id, Sandbox: "sandbox-" + id,
		Attempt: 1, StartedAt: now,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run on %s: %v", id, err)
	}
}

// The whole round trip: a live run says what it is doing, and both the
// per-task read and the whole-store one hand it back with the time it was
// said.
func TestTaskActivityRoundTrips(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	startedTask(t, store, ctx, "a1b2")

	at := now.Add(3 * time.Minute)
	live, err := store.SetTaskActivity(ctx, "a1b2", "waiting for CI on the second push", at)
	if err != nil || !live {
		t.Fatalf("SetTaskActivity = (%v, %v), want (true, nil)", live, err)
	}

	got, err := store.TaskActivityOf(ctx, "a1b2")
	if err != nil {
		t.Fatalf("TaskActivityOf: %v", err)
	}
	if got == nil || got.Note != "waiting for CI on the second push" {
		t.Fatalf("TaskActivityOf = %+v, want the note just written", got)
	}
	if got.At == nil || !got.At.Equal(at) {
		t.Errorf("TaskActivityOf At = %v, want %v", got.At, at)
	}

	all, err := store.TaskActivity(ctx)
	if err != nil {
		t.Fatalf("TaskActivity: %v", err)
	}
	if a, ok := all["a1b2"]; !ok || a.Note != "waiting for CI on the second push" {
		t.Fatalf("TaskActivity = %+v, want a1b2's own note in it", all)
	}
}

// Each call replaces the last: the row carries what the run is doing now,
// never a log of what it has done.
func TestSetTaskActivityReplacesTheLastOne(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	startedTask(t, store, ctx, "a1b2")

	if _, err := store.SetTaskActivity(ctx, "a1b2", "reading the dispatch path", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskActivity(ctx, "a1b2", "running the test suite", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	got, err := store.TaskActivityOf(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("TaskActivityOf = (%+v, %v)", got, err)
	}
	if got.Note != "running the test suite" {
		t.Errorf("note = %q, want the most recent one", got.Note)
	}
}

// A call that arrives after the run is over records nothing and says so,
// rather than failing: the run has done nothing wrong, there is simply
// nobody left to show it to. The finished run keeps whatever it last
// said, which is what leaves a record of where a cancelled run got to --
// but nothing reads it back as current.
func TestSetTaskActivityOnAFinishedRunRecordsNothing(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	startedTask(t, store, ctx, "a1b2")
	if _, err := store.SetTaskActivity(ctx, "a1b2", "waiting for CI", now); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "run-a1b2", now.Add(time.Hour), "succeeded", ""); err != nil {
		t.Fatal(err)
	}

	live, err := store.SetTaskActivity(ctx, "a1b2", "too late", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("SetTaskActivity after the run finished: %v", err)
	}
	if live {
		t.Error("SetTaskActivity = true, want false: the run was over")
	}

	got, err := store.TaskActivityOf(ctx, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("TaskActivityOf on a finished task = %+v, want nil", got)
	}
	if all, err := store.TaskActivity(ctx); err != nil || len(all) != 0 {
		t.Errorf("TaskActivity = (%v, %v), want no live runs", all, err)
	}

	// The finished run itself still carries what it last said.
	runs, err := store.Runs(ctx, "a1b2")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = (%v, %v)", runs, err)
	}
	if runs[0].Activity != "waiting for CI" {
		t.Errorf("finished run's Activity = %q, want what it last said", runs[0].Activity)
	}
	if runs[0].ActivityAt == nil {
		t.Error("finished run's ActivityAt = nil, want when it said it")
	}
}

// Two hands write this column and the row says which: grain's own
// narration of a run's setup, before its agent exists to say anything
// (orchestrator.setupNotes), and the run's own once it does. BySetup is
// read off agent_started_at rather than off the words, so nothing an
// agent can write makes its own status read as grain's.
func TestTaskActivityBeforeTheAgentStartedIsGrainsOwn(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	startedTask(t, store, ctx, "a1b2")

	if _, err := store.SetTaskActivity(ctx, "a1b2", "building a sandbox", now); err != nil {
		t.Fatal(err)
	}
	got, err := store.TaskActivityOf(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("TaskActivityOf = (%+v, %v), want the setup's own phrase", got, err)
	}
	if !got.BySetup {
		t.Errorf("BySetup = false for %q, want true: no agent has started yet", got.Note)
	}
	if all, err := store.TaskActivity(ctx); err != nil || !all["a1b2"].BySetup {
		t.Errorf("TaskActivity = (%+v, %v), want the same attribution in the list read", all, err)
	}

	if err := store.SetRunAgentStarted(ctx, "run-a1b2", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetTaskActivity(ctx, "a1b2", "reading pkg/orchestrator", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err = store.TaskActivityOf(ctx, "a1b2")
	if err != nil || got == nil {
		t.Fatalf("TaskActivityOf = (%+v, %v), want the run's own phrase", got, err)
	}
	if got.BySetup {
		t.Errorf("BySetup = true for %q, want false: the agent is driving now", got.Note)
	}
	if all, err := store.TaskActivity(ctx); err != nil || all["a1b2"].BySetup {
		t.Errorf("TaskActivity = (%+v, %v), want it unmarked in the list read too", all, err)
	}
}

// The same attribution survives the run finishing: Runs carries
// agent_started_at alongside the note, so a caller reading a *finished*
// run -- which TaskActivity deliberately never returns -- can still tell
// grain's own account of the setup from the agent's own account of its
// work (ui.Attempt.SetupNote is exactly that reader).
func TestRunsCarryWhetherTheAgentEverStarted(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	startedTask(t, store, ctx, "a1b2")

	if _, err := store.SetTaskActivity(ctx, "a1b2", "cloning acme/widgets", now); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishRun(ctx, "run-a1b2", now.Add(time.Minute), "setup-failed",
		"this run's sandbox could not be prepared: no such repo"); err != nil {
		t.Fatal(err)
	}
	runs, err := store.Runs(ctx, "a1b2")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = (%v, %v)", runs, err)
	}
	if runs[0].AgentStartedAt != nil {
		t.Errorf("AgentStartedAt = %v, want nil: this run never reached its agent", runs[0].AgentStartedAt)
	}
	if runs[0].Activity != "cloning acme/widgets" {
		t.Errorf("Activity = %q, want the phrase grain broke on", runs[0].Activity)
	}

	// A second attempt, this one handed to its agent.
	if err := store.StartRun(ctx, model.Run{
		ID: "run2-a1b2", TaskID: "a1b2", Sandbox: "sandbox-a1b2",
		Attempt: 2, StartedAt: now.Add(time.Hour),
	}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	started := now.Add(time.Hour + time.Minute)
	if err := store.SetRunAgentStarted(ctx, "run2-a1b2", started); err != nil {
		t.Fatal(err)
	}
	runs, err = store.Runs(ctx, "a1b2")
	if err != nil || len(runs) != 2 {
		t.Fatalf("Runs = (%v, %v), want both attempts", runs, err)
	}
	if runs[1].AgentStartedAt == nil || !runs[1].AgentStartedAt.Equal(started) {
		t.Errorf("AgentStartedAt = %v, want %v", runs[1].AgentStartedAt, started)
	}
}

// An empty note clears the row rather than leaving an empty phrase on it
// -- how the dispatch path hands a run over to its agent without leaving
// "minting the task's credentials" standing over the agent's first turns
// (orchestrator.setupNotes.handOver). Both columns go, so nothing is left
// holding a timestamp for a phrase that is not there.
func TestSetTaskActivityToNothingClearsIt(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	startedTask(t, store, ctx, "a1b2")
	if _, err := store.SetTaskActivity(ctx, "a1b2", "building a sandbox", now); err != nil {
		t.Fatal(err)
	}

	live, err := store.SetTaskActivity(ctx, "a1b2", "", now.Add(time.Minute))
	if err != nil || !live {
		t.Fatalf("clearing the synopsis = (%v, %v), want (true, nil)", live, err)
	}
	if got, err := store.TaskActivityOf(ctx, "a1b2"); err != nil || got != nil {
		t.Errorf("TaskActivityOf after clearing = (%+v, %v), want (nil, nil)", got, err)
	}
	if all, err := store.TaskActivity(ctx); err != nil || len(all) != 0 {
		t.Errorf("TaskActivity after clearing = (%v, %v), want the task gone from it", all, err)
	}
	runs, err := store.Runs(ctx, "a1b2")
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs = (%v, %v)", runs, err)
	}
	if runs[0].Activity != "" || runs[0].ActivityAt != nil {
		t.Errorf("run = (%q, %v), want both columns cleared together",
			runs[0].Activity, runs[0].ActivityAt)
	}
}

// A task nobody has dispatched, and a live run that has never called the
// tool, both read as "nothing to show" -- absent from the map rather than
// present and empty, so a caller never has to tell an empty note from a
// missing one.
func TestTaskActivityOmitsTasksWithNothingToSay(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.PutTask(ctx, task("a1b2", true)); err != nil {
		t.Fatal(err)
	}
	startedTask(t, store, ctx, "c3d4")

	got, err := store.TaskActivityOf(ctx, "a1b2")
	if err != nil || got != nil {
		t.Errorf("TaskActivityOf a task with no run = (%+v, %v), want (nil, nil)", got, err)
	}
	got, err = store.TaskActivityOf(ctx, "c3d4")
	if err != nil || got != nil {
		t.Errorf("TaskActivityOf a silent run = (%+v, %v), want (nil, nil)", got, err)
	}
	all, err := store.TaskActivity(ctx)
	if err != nil || len(all) != 0 {
		t.Errorf("TaskActivity = (%v, %v), want nothing", all, err)
	}
}

// Naming a task that does not exist writes nothing and reports no live
// run, the same answer a task whose run is over gets. The daemon checks
// the task itself (ui.Client.SetTaskActivity answers 404), so this is
// only about the store never inventing a row.
func TestSetTaskActivityOnAnUnknownTaskRecordsNothing(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	live, err := store.SetTaskActivity(ctx, "nope", "hello", now)
	if err != nil || live {
		t.Fatalf("SetTaskActivity on an unknown task = (%v, %v), want (false, nil)", live, err)
	}
}
