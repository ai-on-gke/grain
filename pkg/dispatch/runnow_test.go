package dispatch_test

// A run-now request (model.Observation.RunNowAt, ui.Client.RunNow) is a
// person naming the task they want worked on next, and these are the
// four things it has to be: dispatched ahead of the queue, dispatched
// whatever task_ready makes of the task, spent by the one run it asks
// for, and still bounded by everything that is not the state machine's
// opinion -- capacity, a pause, the caller's own Busy guard.

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// requestRunNow is a person clicking "Run now" -- ui.Client.RunNow's own
// stamp, written here directly so this package keeps its dependency on
// the store and nothing else.
func requestRunNow(t *testing.T, ctx context.Context, store *model.Store, taskID string, at time.Time) {
	t.Helper()
	if err := store.ObserveField(ctx, taskID, at, func(o *model.Observation) {
		o.RunNowAt = &at
	}); err != nil {
		t.Fatalf("requesting a run of %s: %v", taskID, err)
	}
}

func TestRunNowDispatchesAheadOfTheBacklogsOwnOrder(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("t1", true), task("t2", true))
	requestRunNow(t, ctx, store, "t2", now)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t2" {
		t.Fatalf("dispatched %+v, want t2 alone: the requested task takes the one free unit of capacity", got)
	}
}

// The whole of what makes this different from dragging a row to the top
// of the backlog: task_ready does not offer any of these, and each is
// dispatched all the same.
func TestRunNowDispatchesTasksTaskReadyWouldNotOffer(t *testing.T) {
	blocked := task("blocked", true, model.Link{Kind: model.LinkDependsOn, Target: "blocker"})
	unapproved := task("unapproved", false)
	deferred := task("deferred", true)
	deferredAt := now
	deferred.DeferredAt = &deferredAt

	for _, tk := range []model.Task{blocked, unapproved, deferred} {
		t.Run(tk.ID, func(t *testing.T) {
			store, ctx := open(t)
			putTasks(t, store, ctx, task("blocker", true), tk)
			// The blocker is dispatched by the first cycle and left
			// running, so the case under test is the only thing the
			// second cycle could be starting.
			if _, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now); err != nil {
				t.Fatal(err)
			}
			requestRunNow(t, ctx, store, tk.ID, now)

			got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].TaskID != tk.ID {
				t.Fatalf("dispatched %+v, want %s: a run-now request does not consult task_ready", got, tk.ID)
			}
		})
	}
}

// The second half of "regardless of the state machine", and the reason
// this exists at all beyond queue-jumping: a task whose work is over,
// with a pull request already open, has no other way back to an agent.
func TestRunNowSendsAFinishedTaskBackToAnAgent(t *testing.T) {
	store, ctx := open(t)
	// A task with a pull request open and auto-merge set: the shape
	// whose work is over and whose change is on the merge queue, which
	// is the one this button was asked for.
	done := task("done", true, model.Link{Kind: model.LinkFixes, Target: "acme/widgets#42"})
	done.AutoMerge = true
	putTasks(t, store, ctx, done)
	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(first) != 1 {
		t.Fatalf("first cycle dispatched %+v, err = %v; want the task's own first attempt", first, err)
	}
	completed := now.Add(30 * time.Second)
	if err := store.FinishRun(ctx, first[0].RunID, completed, "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "done", CompletedAt: &completed}); err != nil {
		t.Fatal(err)
	}
	if st, _ := store.State(ctx, "done"); st != model.StateCompleted {
		t.Fatalf("state = %q, want completed before the request", st)
	}

	// ui.Client.RunNow clears the completion in the same write that asks
	// for the run; the request alone is what this package acts on.
	at := now.Add(time.Minute)
	if err := store.ObserveField(ctx, "done", at, func(o *model.Observation) {
		o.RunNowAt = &at
		o.CompletedAt = nil
	}); err != nil {
		t.Fatal(err)
	}

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "done" {
		t.Fatalf("dispatched %+v, want the finished task started again", got)
	}
	if got[0].Attempt != 2 {
		t.Errorf("attempt = %d, want 2: the run is the task's next attempt, not its first", got[0].Attempt)
	}
}

// A request buys one run. Without that, a task would be re-dispatched on
// every cycle for as long as the stamp sat there.
func TestRunNowRequestIsSpentByTheRunItAsksFor(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))
	requestRunNow(t, ctx, store, "t0", now)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("first cycle dispatched %+v, err = %v; want the requested run", got, err)
	}
	obs, err := store.GetObservation(ctx, "t0")
	if err != nil {
		t.Fatal(err)
	}
	if obs.RunNowAt != nil {
		t.Errorf("run-now stamp survived the run it started: %v", obs.RunNowAt)
	}
	if err := store.FinishRun(ctx, got[0].RunID, now.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "t0", CompletedAt: timePtr(now.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}

	again, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("cycle after the requested run dispatched %+v, want nothing: the request was spent", again)
	}
}

// Capacity is not the state machine's opinion, so a request waits for it
// -- and waits without being lost, which is the part worth proving.
func TestRunNowWaitsForCapacityAndKeepsItsRequest(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("running", true), task("wanted", true))
	first, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(first) != 1 || first[0].TaskID != "running" {
		t.Fatalf("filling capacity: dispatch = %+v, err = %v", first, err)
	}
	requestRunNow(t, ctx, store, "wanted", now)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dispatched %+v with no capacity free, want nothing", got)
	}

	if err := store.FinishRun(ctx, first[0].RunID, now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.Observe(ctx, model.Observation{TaskID: "running", CompletedAt: timePtr(now.Add(2 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "wanted" {
		t.Fatalf("dispatched %+v once capacity freed, want the task the request named", got)
	}
}

// A pause the caller does not let requests through (an upgrade's hold,
// in orchestrator's wiring), and a provider with no budget left, are
// neither of them the queue having an opinion about this task, so
// neither is overridden. The request survives both.
func TestRunNowIsStillHeldByAPauseItMayNotPass(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("wanted", true))
	requestRunNow(t, ctx, store, "wanted", now)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now, dispatch.Paused())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a paused cycle dispatched %+v, want nothing", got)
	}

	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(30*time.Second),
		dispatch.Paused(), dispatch.RunNowPassesPause(),
		dispatch.PausedFor(func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a run-now request passed a usage-limit pause: dispatched %+v", got)
	}

	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(time.Minute),
		dispatch.PausedFor(func(string) bool { return true }))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a cycle whose framework is out of budget dispatched %+v, want nothing", got)
	}

	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "wanted" {
		t.Fatalf("dispatched %+v once the pauses lifted, want the requested run", got)
	}
}

// Manual mode: under an operator's pause, a run-now request is started
// and nothing else is -- not the backlog, not a merge-queue repair --
// still within capacity, and resuming gives the queue back as it was.
func TestRunNowPassesAnOperatorPauseAndNothingElseDoes(t *testing.T) {
	store, ctx := open(t)
	fix := task("fix", true)
	fix.Origin.Reason = model.ReasonFix
	putTasks(t, store, ctx, task("backlog", true), fix, task("wanted", true), task("second", true))
	requestRunNow(t, ctx, store, "wanted", now)
	requestRunNow(t, ctx, store, "second", now.Add(time.Second))

	manual := []dispatch.Option{dispatch.Paused(), dispatch.RunNowPassesPause()}
	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1, Mergers: 4}, now.Add(time.Minute), manual...)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "wanted" {
		t.Fatalf("dispatched %+v, want only the oldest request: one worker, and the pause holds the rest", got)
	}
	if obs, _ := store.GetObservation(ctx, "second"); obs == nil || obs.RunNowAt == nil {
		t.Fatalf("the request that found no capacity was dropped: %+v", obs)
	}

	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 2, Mergers: 4}, now.Add(2*time.Minute), manual...)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "second" {
		t.Fatalf("dispatched %+v, want the second request and still not the backlog or the repair", got)
	}

	got, err = dispatch.Cycle(ctx, store, model.Limits{Workers: 4, Mergers: 4}, now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range got {
		ids[d.TaskID] = true
	}
	if len(got) != 2 || !ids["backlog"] || !ids["fix"] {
		t.Fatalf("after resuming dispatched %+v, want the backlog task and the repair", got)
	}
}

// The task is started once, not twice, when it is both requested and
// perfectly ready -- the ordinary case, since a queued task is exactly
// what somebody is most likely to ask for.
func TestRunNowStartsAReadyTaskExactlyOnce(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))
	requestRunNow(t, ctx, store, "t0", now)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 4}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t0" {
		t.Fatalf("dispatched %+v, want exactly one run of t0", got)
	}
	runs, err := store.Runs(ctx, "t0")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("t0 has %d runs, want 1", len(runs))
	}
}

func timePtr(t time.Time) *time.Time { return &t }
