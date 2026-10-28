package dispatch_test

// Priority dispatch (model.PriorityDispatchCapability): a task a person
// granted it is admitted the way a console session is -- outside the
// worker and merger counts, ahead of mergers and ordinary work, past any
// pause but an upgrade's hold -- and is retried promptly after a run that
// never got past setup.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

func priorityTask(id string) model.Task {
	tk := task(id, true)
	tk.Grants = []model.Grant{{Capability: model.PriorityDispatchCapability, Via: model.GrantByLabel}}
	return tk
}

func TestAPriorityDispatchTaskStartsWithWorkersFullAndOrdinaryWorkDoesNot(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("worker-1", true))
	limits := model.Limits{Workers: 1}
	if got, err := dispatch.Cycle(ctx, store, limits, now); err != nil || len(got) != 1 {
		t.Fatalf("filling the worker pool: %+v, %v", got, err)
	}

	putTasks(t, store, ctx, task("worker-2", true), priorityTask("steward"))
	got, err := dispatch.Cycle(ctx, store, limits, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "steward" {
		t.Fatalf("with workers full dispatched %+v, want the priority task alone", got)
	}
	live, err := store.LiveRunCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if live.Workers != 1 || live.Directors != 1 {
		t.Fatalf("live = %+v, want one worker and the priority run counted apart from it", live)
	}
}

func TestAPriorityDispatchTaskPassesAManualPause(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), priorityTask("steward"))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now,
		dispatch.Paused(), dispatch.PriorityPassesPause())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "steward" {
		t.Fatalf("a manual pause dispatched %+v, want the priority task alone", got)
	}
}

func TestAnUpgradesHoldHoldsAPriorityDispatchTask(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, priorityTask("steward"))

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now, dispatch.Paused())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("an upgrade's hold dispatched %+v, want nothing", got)
	}
	wait, err := store.GrantWaitOf(ctx, "steward")
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || !strings.Contains(wait.Resource, "upgrade") {
		t.Fatalf("the held task's wait = %+v, want one saying an upgrade holds it", wait)
	}
}

func TestAPriorityDispatchTaskIsOrderedAheadOfAMerger(t *testing.T) {
	store, ctx := open(t)
	steward := priorityTask("steward")
	steward.OrderKey = 10 // behind the fix task (-1) and t0 (0) in the backlog
	putTasks(t, store, ctx, task("t0", true), fixTask("fix"), steward)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3, Mergers: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, d := range got {
		order = append(order, d.TaskID)
	}
	if len(order) != 3 || order[0] != "steward" || order[1] != "fix" || order[2] != "t0" {
		t.Fatalf("dispatch order = %v, want steward, then the merger, then ordinary work", order)
	}
}

// failSetup records n runs of id in a row that never got past setup,
// the last finishing at last.
func failSetup(t *testing.T, store *model.Store, id string, n int, last time.Time) {
	t.Helper()
	ctx := t.Context()
	for i := 1; i <= n; i++ {
		at := last.Add(time.Duration(i-n) * time.Minute)
		runID := dispatch.RunID(id, i)
		if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: id, Attempt: i, StartedAt: at.Add(-time.Second)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(ctx, runID, at, model.SetupFailedOutcome, "dial tcp: i/o timeout"); err != nil {
			t.Fatal(err)
		}
	}
}

// Four setup failures in a row would ordinarily back a task off four
// minutes; a priority task waits two at most, and an ordinary one with
// the same history still waits out its own.
func TestAPriorityDispatchTaskRetriesASetupFailureQuickly(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), priorityTask("steward"))
	failSetup(t, store, "t0", 4, now)
	failSetup(t, store, "steward", 4, now)

	got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 3}, now.Add(2*time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "steward" {
		t.Fatalf("two minutes after the fourth setup failure dispatched %+v, want the priority task alone", got)
	}
}
