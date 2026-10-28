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

// A task's own stretch of the calendar (model.TaskSpan): when its first
// attempt began and when it stopped being work in progress. It is what
// the Gantt view draws each bar from (ui/src/components/GanttChart.jsx),
// and unlike everything else on a task row it cannot be derived from the
// state -- a state says where a task is now, not when it got there.
//
// These tests drive a real store, since the whole question is what two
// tables say together, and assert on all three shapes a task is read
// back through: the list (one query for everyone), the single task, and
// the detail. A span that disagreed between them would put two different
// bars on screen for one task.

import (
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ran starts an attempt at `at` and, when finished is non-nil, ends it
// there -- the two writes dispatch and the orchestrator make around a
// run, in the order they make them.
func ran(t *testing.T, store *model.Store, taskID, runID string, attempt int, at time.Time, finished *time.Time) {
	t.Helper()
	if err := store.StartRun(t.Context(), model.Run{
		ID: runID, TaskID: taskID, Sandbox: "sandbox-" + runID,
		Attempt: attempt, StartedAt: at,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting %s: %v", runID, err)
	}
	if finished == nil {
		return
	}
	if err := store.FinishRun(t.Context(), runID, *finished, "succeeded", ""); err != nil {
		t.Fatalf("finishing %s: %v", runID, err)
	}
}

// The ordinary shape: a task is filed, waits, is dispatched, and
// completes. All three timestamps come back, and they are the three the
// chart needs -- filed, picked up, done.
func TestTaskSpanCoversFirstAttemptToCompletion(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	started := baseTime.Add(30 * time.Minute)
	finished := baseTime.Add(90 * time.Minute)
	ran(t, store, task.ID, "run-1", 1, started, &finished)
	completed := baseTime.Add(2 * time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: task.ID, CompletedAt: &completed}); err != nil {
		t.Fatalf("observing completion: %v", err)
	}

	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("ListTasks = %d tasks, want 1", len(list))
	}
	got := list[0]
	if got.CreatedAt == nil || !got.CreatedAt.Equal(baseTime) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, baseTime)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("startedAt = %v, want the first attempt's start %v", got.StartedAt, started)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(completed) {
		t.Errorf("endedAt = %v, want completion %v", got.EndedAt, completed)
	}

	// The same span from the other two readers, which take it a task at a
	// time rather than a map of the whole store.
	one, err := client.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.StartedAt == nil || !one.StartedAt.Equal(started) || one.EndedAt == nil || !one.EndedAt.Equal(completed) {
		t.Errorf("Task = %v..%v, want %v..%v", one.StartedAt, one.EndedAt, started, completed)
	}
	detail, err := client.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.StartedAt == nil || !detail.StartedAt.Equal(started) || detail.EndedAt == nil || !detail.EndedAt.Equal(completed) {
		t.Errorf("GetTask = %v..%v, want %v..%v", detail.StartedAt, detail.EndedAt, started, completed)
	}
}

// A task that has never been dispatched has no start, and one still open
// has no end. Both read back as absent rather than as some stand-in,
// because a bar that started when the task did -- or that ended when its
// last attempt did -- would be a claim about work that is not what
// happened.
func TestTaskSpanIsAbsentUntilThereIsSomethingToSay(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)

	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].StartedAt != nil || list[0].EndedAt != nil {
		t.Errorf("queued task = %v..%v, want no span at all", list[0].StartedAt, list[0].EndedAt)
	}

	// Dispatched, and the attempt has since failed: the task is open, so
	// the bar has not stopped.
	started := baseTime.Add(time.Hour)
	finished := started.Add(20 * time.Minute)
	if err := store.StartRun(ctx, model.Run{
		ID: "run-1", TaskID: task.ID, Sandbox: "sandbox-1", Attempt: 1, StartedAt: started,
	}, model.Limits{}); err != nil {
		t.Fatalf("starting a run: %v", err)
	}
	if err := store.FinishRun(ctx, "run-1", finished, "failed", "the agent gave up"); err != nil {
		t.Fatalf("finishing a run: %v", err)
	}
	list, err = client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].StartedAt == nil || !list[0].StartedAt.Equal(started) {
		t.Errorf("startedAt = %v, want %v", list[0].StartedAt, started)
	}
	if list[0].EndedAt != nil {
		t.Errorf("endedAt = %v, want none while the task is still open", list[0].EndedAt)
	}
}

// Two attempts, one span: it starts at the first, not the last. A task
// retried a day later has been somebody's problem since the first
// dispatch, and a bar that began at the retry would hide exactly the
// stretch worth seeing.
//
// The later attempt is written first, so that a reader picking whichever
// row the join happened to hand it -- rather than the earliest -- fails
// here rather than in front of somebody.
func TestTaskSpanStartsAtTheFirstAttempt(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	second := baseTime.Add(25 * time.Hour)
	secondEnd := second.Add(10 * time.Minute)
	ran(t, store, task.ID, "run-2", 2, second, &secondEnd)
	first := baseTime.Add(time.Hour)
	firstEnd := first.Add(10 * time.Minute)
	ran(t, store, task.ID, "run-1", 1, first, &firstEnd)

	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].StartedAt == nil || !list[0].StartedAt.Equal(first) {
		t.Errorf("startedAt = %v, want the first attempt %v", list[0].StartedAt, first)
	}
}

// A task that completed and was then closed ended when it completed:
// that is when it stopped being work in progress, and the closure over
// it is bookkeeping (model.TaskSpan).
func TestTaskSpanEndsAtTheEarlierOfCompletedAndClosed(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	completed := baseTime.Add(time.Hour)
	closed := baseTime.Add(5 * time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID: task.ID, CompletedAt: &completed, ClosedAt: &closed,
	}); err != nil {
		t.Fatalf("observing: %v", err)
	}

	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].EndedAt == nil || !list[0].EndedAt.Equal(completed) {
		t.Errorf("endedAt = %v, want completion %v rather than the later closure", list[0].EndedAt, completed)
	}

	// And a task closed with nothing ever completed ends at the closure,
	// which is the only ending it has.
	other := create(t, client, ctx)
	if err := store.Observe(ctx, model.Observation{TaskID: other.ID, ClosedAt: &closed}); err != nil {
		t.Fatalf("observing a closure: %v", err)
	}
	one, err := client.Task(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.EndedAt == nil || !one.EndedAt.Equal(closed) {
		t.Errorf("endedAt = %v, want the closure %v", one.EndedAt, closed)
	}
}

// The map ListTasks reads holds a span for the tasks that have one and
// nothing at all for the rest, so a store full of proposals costs the
// same as one with none. Asserted through the store directly, since it
// is the map's own shape that is the promise.
func TestTaskSpansLeavesOutTasksWithNoSpan(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	quiet := create(t, client, ctx)
	busy := create(t, client, ctx)
	started := baseTime.Add(time.Hour)
	ran(t, store, busy.ID, "run-1", 1, started, nil)

	spans, err := store.TaskSpans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spans[quiet.ID]; ok {
		t.Errorf("spans = %v, want no entry for a task that has neither started nor ended", spans)
	}
	span, ok := spans[busy.ID]
	if !ok || span.StartedAt == nil || !span.StartedAt.Equal(started) {
		t.Errorf("spans[%s] = %+v, want a start of %v", busy.ID, span, started)
	}
}

func TestTaskSpanIncludesPrMergedAt(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	started := baseTime.Add(time.Hour)
	finished := baseTime.Add(2 * time.Hour)
	ran(t, store, task.ID, "run-1", 1, started, &finished)
	closed := baseTime.Add(3 * time.Hour)
	merged := baseTime.Add(3 * time.Hour)
	if err := store.Observe(ctx, model.Observation{
		TaskID:     task.ID,
		ClosedAt:   &closed,
		PrMergedAt: &merged,
	}); err != nil {
		t.Fatal(err)
	}

	list, err := client.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("ListTasks = %d tasks, want 1", len(list))
	}
	if list[0].PrMergedAt == nil || !list[0].PrMergedAt.Equal(merged) {
		t.Errorf("list[0].PrMergedAt = %v, want %v", list[0].PrMergedAt, merged)
	}
	one, err := client.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if one.PrMergedAt == nil || !one.PrMergedAt.Equal(merged) {
		t.Errorf("one.PrMergedAt = %v, want %v", one.PrMergedAt, merged)
	}
}

// TaskSpans reads one row per attempt and keeps the earliest start itself
// rather than asking the database for MIN() (which PostgreSQL refused in
// the form SQLite accepted). So the answer must not depend on the order
// the attempts come back in: a later attempt stored first still loses
// to the earlier one.
func TestTaskSpansKeepsTheEarliestAttemptWhateverOrderTheyWereStored(t *testing.T) {
	t.Parallel()
	client, store, ctx := testClient(t)
	task := create(t, client, ctx)
	early := baseTime.Add(time.Hour)
	late := baseTime.Add(4 * time.Hour)
	lateEnd := late.Add(time.Minute)
	ran(t, store, task.ID, "run-2", 2, late, &lateEnd)
	ran(t, store, task.ID, "run-1", 1, early, nil)

	spans, err := store.TaskSpans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	span, ok := spans[task.ID]
	if !ok || span.StartedAt == nil || !span.StartedAt.Equal(early) {
		t.Errorf("spans[%s] = %+v, want a start of %v", task.ID, span, early)
	}
}
