package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

var repairRepo = model.RepoRef{Owner: "acme", Name: "widgets"}

// repairTask is a task marked as fixing base on repairRepo.
func repairTask(id, base string) model.Task {
	tk := task(id, true)
	tk.Target = &repairRepo
	tk.Base = base
	if !model.SetFixesBaseBranch(&tk, true) {
		panic("SetFixesBaseBranch refused " + id)
	}
	return tk
}

// repairLimits is a 16 GiB budget of 4 GiB agent sandboxes with reserve
// of it kept for repairs, and full as far as ordinary work is concerned:
// everything but the reserve is already charged.
func repairLimits(reserveMB int) model.Limits {
	limits := model.Limits{
		Budget: model.Budget{
			Host: model.Commitment{CPUs: 64, MemoryMB: 16 * 1024}, CPUOvercommit: 1, MemoryOvercommit: 1,
			RepairReserve: model.Commitment{MemoryMB: reserveMB},
		},
		AgentShape: model.Commitment{CPUs: 1, MemoryMB: 4096},
		Ledger:     &model.Ledger{},
	}
	limits.Ledger.Charge(model.PoolCI, "busy", model.Commitment{CPUs: 1, MemoryMB: 16*1024 - reserveMB})
	return limits
}

func dispatched(got []dispatch.Dispatch) map[string]bool {
	out := map[string]bool{}
	for _, d := range got {
		out[d.TaskID] = true
	}
	return out
}

func TestCycleAdmitsOneRepairPerBaseFromTheReserve(t *testing.T) {
	store, ctx := open(t)
	ordinary := task("o1", true)
	ordinary.Target = &repairRepo
	putTasks(t, store, ctx, ordinary, repairTask("r1", "main"), repairTask("r2", "main"), repairTask("r3", "candidate"))

	// Two sandboxes of reserve: room for r1 and r3, which repair different
	// bases, and for r2 too if the reserve were not rationed per base.
	limits := repairLimits(2 * 4096)
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	ids := dispatched(got)
	if !ids["r1"] || !ids["r3"] {
		t.Errorf("dispatched %v, want r1 and r3 admitted from the repair reserve", ids)
	}
	if ids["r2"] {
		t.Errorf("dispatched r2 beside r1: two repairs of one base used the reserve at once")
	}
	if ids["o1"] {
		t.Errorf("dispatched o1, an ordinary task, into the repair reserve")
	}
	wait, err := store.GrantWaitOf(ctx, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || !strings.Contains(wait.Resource, "repair reserve") {
		t.Errorf("o1's wait = %+v, want one that names the repair reserve", wait)
	}

	// And on the next cycle r2 still waits: r1 is running against main.
	got, err = dispatch.Cycle(ctx, store, limits, now.Add(1))
	if err != nil {
		t.Fatal(err)
	}
	if dispatched(got)["r2"] {
		t.Errorf("r2 dispatched while r1 is live against the same base")
	}
}

func TestCyclePreemptsOnlyForRepairs(t *testing.T) {
	store, ctx := open(t)
	ordinary := task("o1", true)
	ordinary.Target = &repairRepo
	putTasks(t, store, ctx, ordinary)

	type call struct{ repair, base string }
	var calls []call
	preempt := dispatch.Preempt(func(_ context.Context, repair model.Task, base string) (string, bool) {
		calls = append(calls, call{repair.ID, base})
		return "pre-empted run x-1 of task x", true
	})

	// No reserve: the budget is simply full.
	limits := repairLimits(0)
	if _, err := dispatch.Cycle(ctx, store, limits, now, preempt); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("pre-empted %v for ordinary work", calls)
	}

	putTasks(t, store, ctx, repairTask("r1", "main"))
	got, err := dispatch.Cycle(ctx, store, limits, now.Add(1), preempt)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dispatched %+v on a full budget; the pre-empted sandbox is not free yet", got)
	}
	wantBase := model.BaseRepairBranchTag(repairRepo, "main")
	if len(calls) != 1 || calls[0] != (call{"r1", wantBase}) {
		t.Fatalf("pre-emption calls = %v, want one for r1 on %s", calls, wantBase)
	}
	wait, err := store.GrantWaitOf(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if wait == nil || !strings.Contains(wait.Resource, "pre-empted run x-1") {
		t.Errorf("r1's wait = %+v, want one that names what was pre-empted for it", wait)
	}
}
