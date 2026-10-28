package dispatch_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A task whose OOM kills have raised its sandbox memory past the whole
// sandbox budget (orchestrator.raiseMemoryAfterOOM stops at four times
// the backend default, which can be more than a small host's budget)
// does not wait in silence: while anything else holds the budget its
// row carries a quota wait naming memory and what it wants, and once
// the budget is empty it is admitted on its own (Budget.Admits' Sole
// rule) rather than waiting for room that can never exist.
//
// 2026-10-25 test plan §4, coverage gap 3 (docs/test-runs/
// 2026-10-25-sandbox-budget-oom-retry.md).
func TestAnOOMRaisedTaskBiggerThanTheBudgetWaitsWithAReasonThenRunsAlone(t *testing.T) {
	store, ctx := open(t)

	// 8192 MiB default, raised twice by OOM kills to the 32768 MiB
	// ceiling; the budget is 16 GiB host less 2 GiB reserved at 1.0x.
	raised := task("t-oom", true)
	raised.SandboxMemoryMB = 4 * 8192
	putTasks(t, store, ctx, raised)

	ledger := &model.Ledger{}
	limits := model.Limits{
		Workers:    4,
		Budget:     model.NewBudget(model.Commitment{CPUs: 4, MemoryMB: 16 * 1024}, 2048, 0, 1),
		AgentShape: model.Commitment{CPUs: 2, MemoryMB: 8192},
		Ledger:     ledger,
	}
	if mem, ok := limits.Budget.MemoryBudgetMB(); !ok || mem >= raised.SandboxMemoryMB {
		t.Fatalf("MemoryBudgetMB = %d (%v), want a bounded budget smaller than the raised task", mem, ok)
	}

	// Another run already holds a default-shaped sandbox.
	release := ledger.Charge(model.PoolAgent, "other-run", limits.AgentShape)

	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dispatched %+v while another sandbox holds the budget, want none", got)
	}
	wait, err := store.GrantWaitOf(ctx, "t-oom")
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || wait.Capability != model.QuotaWaitCapability {
		t.Fatalf("GrantWaitOf(t-oom) = %+v, want a quota wait", wait)
	}
	for _, want := range []string{"memory:", "this one wants 32.0 GiB", "more than the whole budget, so it starts only once no other sandbox is live"} {
		if !strings.Contains(wait.Resource, want) {
			t.Errorf("wait reason %q does not say %q", wait.Resource, want)
		}
	}

	release()
	got, err = dispatch.Cycle(ctx, store, limits, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t-oom" {
		t.Fatalf("dispatched %+v once the budget is empty, want t-oom admitted alone", got)
	}
	if live := ledger.Live(); live.MemoryMB != raised.SandboxMemoryMB {
		t.Errorf("ledger.Live() = %s, want t-oom's raised %d MiB charged", live, raised.SandboxMemoryMB)
	}
}
