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

// A deferred task (model.StateDeferred, grain/task-21) and the three
// things that would otherwise start it: a cycle with capacity, a
// dependency getting out of its way, and a schedule coming due.
//
// The state itself is covered where it is computed -- model's StateOf and
// the task_state view, and sqlite's own
// TestDeferringTakesATaskOutOfReadyAndGivesItBackApproved for the store's
// half. What none of that says is what happens when something *else*
// moves: "not in Ready" is a fact about a view, and the question a
// deployment actually has is whether a task somebody put aside can be
// picked up again by the machinery rather than by them. So every test
// here runs real cycles against a real store, with an agent standing by
// to take anything that is dispatched, and asserts on the run rows a
// dispatch would leave behind.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// deferTask puts a task aside the way ui.Client.Defer does -- the
// timestamp is the whole of the state (model.Task.DeferredAt).
func deferTask(t *testing.T, store *model.Store, id string, at time.Time) {
	t.Helper()
	if err := store.SetDeferred(t.Context(), id, &at, nil, at); err != nil {
		t.Fatalf("deferring %s: %v", id, err)
	}
}

// deferUntil is the same with a wake time on it (model.Task.
// DeferredUntil) -- ui.Client.Defer's own DeferOptions.Until.
func deferUntil(t *testing.T, store *model.Store, id string, at, until time.Time) {
	t.Helper()
	if err := store.SetDeferred(t.Context(), id, &at, &until, at); err != nil {
		t.Fatalf("deferring %s until %s: %v", id, until, err)
	}
}

func undeferTask(t *testing.T, store *model.Store, id string, at time.Time) {
	t.Helper()
	if err := store.SetDeferred(t.Context(), id, nil, nil, at); err != nil {
		t.Fatalf("picking %s back up: %v", id, err)
	}
}

// notDispatched asserts nothing has been started for taskID. A run row is
// written by dispatch.Cycle itself, inside the cycle, so this is a
// decision the cycle has already finished making by the time RunCycle
// returns -- there is nothing to wait for.
func notDispatched(t *testing.T, store *model.Store, taskID, why string) {
	t.Helper()
	runs, err := store.Runs(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("%s was dispatched (%d run(s)) %s", taskID, len(runs), why)
	}
}

func stateIs(t *testing.T, store *model.Store, taskID string, want model.State) {
	t.Helper()
	got, err := store.State(t.Context(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s reads %q, want %q", taskID, got, want)
	}
}

// The plain case: an approved task with nothing in front of it and a free
// worker is not dispatched while it is deferred, and is the moment it is
// picked back up -- with no second approval, which is what makes deferring
// a way to say "not now" rather than a way to withdraw the go-ahead.
func TestACycleDoesNotDispatchADeferredTask(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	deferTask(t, store, "t1", baseTime)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)
	cycle(t, ctx, deps)

	stateIs(t, store, "t1", model.StateDeferred)
	notDispatched(t, store, "t1", "while it was deferred, with a free worker and nothing blocking it")

	undeferTask(t, store, "t1", baseTime.Add(time.Hour))
	stateIs(t, store, "t1", model.StateQueued)
	cycle(t, ctx, deps)
	enteredRun(t, fw, "t1-1")
	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// drainRuns waits out the runs a test started, so the store outlives the
// goroutines still writing to it: a run in flight when openStore's own
// cleanup closes the database logs failures about a closed database that
// have nothing to do with what was being tested.
func drainRuns(t *testing.T, ctx context.Context, runs *orchestrator.InFlight) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := runs.Wait(waitCtx); err != nil {
		t.Fatalf("waiting for the dispatched runs: %v", err)
	}
}

// The fourth thing that starts a deferred task, and the only one that is
// not somebody pressing a button: its own wake time arriving
// (orchestrator.reconcileWake, grain/task-386). A task deferred until a
// date is held exactly as any other deferred task is held until then,
// and on the first cycle at or after it, it is picked back up and
// dispatched with no second approval -- which is what makes "do not
// start this before D" something a person can set and then forget.
//
// The deferral with no date on it is here as the control: the same
// cycle, at the same later moment, leaves it exactly where it was. A
// wake pass that woke every deferred task would pass the first half of
// this test and empty a deployment's "not this quarter" pile.
func TestATaskWakesItselfWhenItsWakeTimeArrives(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "dated", repo)
	filedTask(t, ctx, store, "indefinite", repo)
	wake := baseTime.Add(time.Hour)
	deferUntil(t, store, "dated", baseTime, wake)
	deferTask(t, store, "indefinite", baseTime)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)

	// A cycle before the wake time changes nothing: the date is a floor,
	// so the cycles in between are the ones that must not start it.
	cycleAt(t, ctx, deps, wake.Add(-time.Minute))
	stateIs(t, store, "dated", model.StateDeferred)
	notDispatched(t, store, "dated", "before its wake time had arrived")

	// The cycle at the wake time itself wakes it and dispatches it, in
	// that order and on the same tick -- which is why "wake" runs ahead
	// of "dispatch" (Reconcilers).
	cycleAt(t, ctx, deps, wake)
	enteredRun(t, fw, "dated-1")
	stateIs(t, store, "indefinite", model.StateDeferred)
	notDispatched(t, store, "indefinite", "though nobody ever gave it a wake time")

	// And nothing of the deferral is left behind: the woken task carries
	// no date it could wake on twice.
	woken, err := store.GetTask(ctx, "dated")
	if err != nil {
		t.Fatal(err)
	}
	if woken.DeferredAt != nil || woken.DeferredUntil != nil {
		t.Fatalf("the woken task is deferred at %v until %v, want neither",
			woken.DeferredAt, woken.DeferredUntil)
	}
	if woken.Approval == nil {
		t.Fatal("waking a task cleared its approval: it must come back queued, exactly as it was left")
	}
	// The return is stamped with the cycle that made it
	// (model.Task.UndeferredAt), so a task that wakes itself is measured
	// from the moment it came back rather than from the approval it was
	// deferred with -- a wake pass is exactly the case nobody is there to
	// see, and a task flagged as stalled the instant it woke would be
	// flagged for the one thing that is not standing still.
	if woken.UndeferredAt == nil || !woken.UndeferredAt.Equal(wake) {
		t.Fatalf("the woken task was last picked back up at %v, want the cycle that woke it %v",
			woken.UndeferredAt, wake)
	}
	times, err := store.StateTimes(ctx)
	if err != nil {
		t.Fatalf("gathering state times: %v", err)
	}
	if since := times["dated"].Since(model.StateQueued); since == nil || !since.Equal(wake) {
		t.Fatalf("the woken task is queued since %v, want the wake %v", since, wake)
	}
	// And the one nobody woke carries no stamp at all: it has not come
	// back from anything.
	held, err := store.GetTask(ctx, "indefinite")
	if err != nil {
		t.Fatal(err)
	}
	if held.UndeferredAt != nil {
		t.Fatalf("a task still deferred was stamped as picked back up at %v", held.UndeferredAt)
	}

	fw.releaseAll()
	drainRuns(t, ctx, runs)
}

// The interaction the deferred state's own tests could not reach: a task
// held back by *two* things at once. A dependency closing takes one of
// them away and must not take the other with it -- deferring is a
// declaration a person made about this task, and nothing that happens to
// another task revokes it.
//
// The blocker is run for real here rather than closed outright, which
// also says where the line is: a dependency that has *completed* is not a
// dependency that is out of the way (the task_blocked view reads
// closed_at, so a pull request still open goes on blocking), and neither
// half of that changes anything for the task waiting behind it while it
// is deferred.
func TestADeferredTaskIsNotDispatchedWhenItsDependencyFinishes(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "blocker", repo)
	blocked := filedTask(t, ctx, store, "blocked", repo)
	blocked.Links = []model.Link{{Kind: model.LinkDependsOn, Target: "blocker"}}
	if err := store.PutTask(ctx, blocked); err != nil {
		t.Fatal(err)
	}
	deferTask(t, store, blocked.ID, baseTime)

	fw := newBlockingFramework()
	deps, runs := asyncDeps(t, store, fw, 2)

	// The blocker runs and finishes: a comment and nothing else, which
	// completes its task without closing it.
	cycle(t, ctx, deps)
	enteredRun(t, fw, "blocker-1")
	notDispatched(t, store, blocked.ID, "while it was deferred behind a running dependency")
	fw.releaseAll()
	drainRuns(t, ctx, runs)
	// A comment and nothing else is a completion with no pull request
	// behind it, which reads 'awaiting_ack' (model.AwaitsAck) -- and,
	// like 'completed', is not closed, which is the whole point here.
	stateIs(t, store, "blocker", model.StateAwaitingReply)

	cycle(t, ctx, deps)
	notDispatched(t, store, blocked.ID, "once its dependency had completed")

	// And closed, which is what actually clears the block.
	closedAt := baseTime.Add(time.Hour)
	if err := store.Observe(ctx, model.Observation{TaskID: "blocker", ClosedAt: &closedAt}); err != nil {
		t.Fatal(err)
	}
	cycle(t, ctx, deps)
	stateIs(t, store, blocked.ID, model.StateDeferred)
	notDispatched(t, store, blocked.ID, "once its dependency had closed: it is still deferred")

	// Picked back up, with the blocker gone, it dispatches on the next
	// cycle -- so what the defer did was hold it, not drop it.
	undeferTask(t, store, blocked.ID, closedAt)
	cycle(t, ctx, deps)
	enteredRun(t, fw, "blocked-1")
	drainRuns(t, ctx, runs)
}

// The other way a task starts without anybody clicking anything: a
// schedule coming due. A schedule does not dispatch the firing it already
// made -- it files a new task each time -- so what a deferred firing
// decides is whether the *next* one is filed, and under the default
// overlap policy it is not: a deferred task is not a closed task, and
// Store.HasOpenTaskWithTag reads every state but closed as an unfinished
// firing.
//
// That is worth writing down because it is the one way a schedule can be
// stopped without being paused: defer its firing and the cadence quietly
// stops until somebody closes that task. Setting the schedule to
// model.OverlapAllow is the way out, and
// TestOverlapPolicyDecidesASecondFiringWhileTheFirstIsStillRunning covers
// what that policy does with a firing still in flight.
func TestADeferredFiringHoldsItsScheduleBackUntilItIsClosed(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	sched := filedSchedule(t, store, "sched-1", baseTime.Add(-time.Minute))
	tag := "schedule:" + sched.ID

	if err := runScheduleOnly(t, store, baseTime); err != nil {
		t.Fatalf("first RunCycle: %v", err)
	}
	firings := scheduledTasksTagged(t, store, tag)
	if len(firings) != 1 {
		t.Fatalf("filed %d tasks on the first cycle, want 1", len(firings))
	}
	first := firings[0]

	deferTask(t, store, first.ID, baseTime)
	stateIs(t, store, first.ID, model.StateDeferred)

	// Due again, with that firing set aside rather than running.
	later := baseTime.Add(time.Hour)
	forceDue := func() {
		t.Helper()
		if err := store.UpdateSchedule(ctx, sched.ID, func(s *model.Schedule) error {
			s.NextRunAt = baseTime
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	forceDue()
	if err := runScheduleOnly(t, store, later); err != nil {
		t.Fatalf("second RunCycle: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, tag); len(filed) != 1 {
		t.Fatalf("filed %d tasks while the previous firing was deferred, want still 1", len(filed))
	}

	// Picking it back up does not release the schedule either -- the
	// firing is queued again, and a queued firing is just as unfinished.
	undeferTask(t, store, first.ID, later)
	forceDue()
	if err := runScheduleOnly(t, store, later); err != nil {
		t.Fatalf("third RunCycle: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, tag); len(filed) != 1 {
		t.Fatalf("filed %d tasks while the previous firing was queued again, want still 1", len(filed))
	}

	// Closing it is what does.
	if err := store.Observe(ctx, model.Observation{TaskID: first.ID, ClosedAt: &later}); err != nil {
		t.Fatal(err)
	}
	forceDue()
	if err := runScheduleOnly(t, store, later); err != nil {
		t.Fatalf("fourth RunCycle: %v", err)
	}
	if filed := scheduledTasksTagged(t, store, tag); len(filed) != 2 {
		t.Fatalf("filed %d tasks once the deferred firing was closed, want 2", len(filed))
	}
}
