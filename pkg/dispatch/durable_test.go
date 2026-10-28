package dispatch_test

// Durable tasks (model.DurableCapability, docs/design/durable-tasks.md#DT-1):
// admitted with priority, outside max_workers, but held to their own
// ceiling (model.Limits.Durable); and a setup suspension holds its task
// for the 2-minute backoff with no strike to back off from.

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

func durableTask(id string) model.Task {
	tk := task(id, true)
	tk.Grants = []model.Grant{{Capability: model.DurableCapability, Via: model.GrantByLabel}}
	return tk
}

func TestDurableTasksStartOutsideTheWorkerPoolUpToTheirCeiling(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("worker-1", true))
	limits := model.Limits{Workers: 1, Durable: 2}
	if got, err := dispatch.Cycle(ctx, store, limits, now); err != nil || len(got) != 1 {
		t.Fatalf("filling the worker pool: %+v, %v", got, err)
	}

	putTasks(t, store, ctx, durableTask("d1"), durableTask("d2"), durableTask("d3"), priorityTask("steward"))
	got, err := dispatch.Cycle(ctx, store, limits, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range got {
		ids = append(ids, d.TaskID)
	}
	if strings.Join(ids, ",") != "d1,d2,steward" {
		t.Fatalf("dispatched %v, want d1,d2 (the durable ceiling) and the priority task, which it does not hold", ids)
	}
	wait, err := store.GrantWaitOf(ctx, "d3")
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || !strings.Contains(wait.Resource, "durable-run ceiling") {
		t.Fatalf("d3's wait = %+v, want it to name the durable-run ceiling", wait)
	}
}

func TestADurableSetupSuspensionWaitsOutTheSetupBackoff(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, durableTask("d"))
	got, err := dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("starting the durable task: %+v, %v", got, err)
	}
	resume := now.Add(dispatch.DurableSetupBackoff)
	if err := store.Suspend(ctx, model.Suspension{RunID: got[0].RunID, Kind: model.SuspendSetup,
		Detail: "sandbox could not be prepared", ResumeAt: &resume, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, err := dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, now.Add(time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("a minute after a setup suspension dispatched %+v, %v; want it still backing off", got, err)
	}
	got, err = dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, resume)
	if err != nil || len(got) != 1 || got[0].TaskID != "d" {
		t.Fatalf("once the backoff had passed dispatched %+v, %v; want the durable task", got, err)
	}
	if streak, err := store.FailureStreak(ctx, "d"); err != nil || (streak != nil && streak.Count != 0) {
		t.Fatalf("FailureStreak = %+v, %v; want a setup suspension to be no strike", streak, err)
	}
}

func TestADurableRunNowSetupSuspensionAlsoWaitsOutTheSetupBackoff(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, durableTask("d"))
	requestRunNow(t, ctx, store, "d", now.Add(-time.Minute))
	got, err := dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("starting the durable run-now task: %+v, %v", got, err)
	}
	resume := now.Add(dispatch.DurableSetupBackoff)
	if err := store.Suspend(ctx, model.Suspension{RunID: got[0].RunID, Kind: model.SuspendSetup,
		Detail: "sandbox could not be prepared", ResumeAt: &resume, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got, err := dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, now.Add(time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("a minute after a setup suspension dispatched %+v, %v; want its restored run-now still backing off", got, err)
	}
	got, err = dispatch.Cycle(ctx, store, model.Limits{Durable: 4}, resume)
	if err != nil || len(got) != 1 || got[0].TaskID != "d" {
		t.Fatalf("once the backoff had passed dispatched %+v, %v; want the durable task", got, err)
	}
}
