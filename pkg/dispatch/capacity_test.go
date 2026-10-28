package dispatch_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestAProviderOutageNeverRetiresATaskButStillSpacesItsRetries is
// model.CapacityOutcome's own rule, held against the two things dispatch
// decides with it. A run the provider had no capacity for says nothing
// about the task, so however many of them a task collects it must stay
// dispatchable -- an outage that outlasts model.MaxConsecutiveFailures
// polls must not retire every task that was running through it. But
// nothing pauses dispatch for an overload either, so the retries have to
// go on backing off: sending the task straight back at the same refusal
// on the next poll spends a sandbox build a minute for as long as the
// outage lasts.
func TestAProviderOutageNeverRetiresATaskButStillSpacesItsRetries(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))
	const maxWorkers = 1

	clock := now
	for i := 0; i < model.MaxConsecutiveFailures+2; i++ {
		// Well past whatever backoff the previous refusal earned, so
		// that what this loop measures is the cap and not a wait.
		clock = clock.Add(time.Hour)
		got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, clock)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].TaskID != "t0" {
			t.Fatalf("dispatch %d = %+v, want t0 offered a slot: a provider outage must not retire it", i+1, got)
		}
		if err := store.FinishRun(ctx, got[0].RunID, clock.Add(time.Minute), model.CapacityOutcome,
			"antigravity: the provider had no capacity for this run; 503 Service Unavailable"); err != nil {
			t.Fatal(err)
		}
	}
	if state, err := store.State(ctx, "t0"); err != nil {
		t.Fatal(err)
	} else if state != model.StateQueued {
		t.Fatalf("state after %d capacity refusals = %q, want %q",
			model.MaxConsecutiveFailures+2, state, model.StateQueued)
	}

	// The other half: the next attempt still waits. The refusals above
	// have grown the backoff well past baseRetryBackoff, so a poll a few
	// seconds after the last one gets nothing.
	tooSoon, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, clock.Add(time.Minute+2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(tooSoon) != 0 {
		t.Fatalf("dispatch = %+v, want nothing yet: a refused task must back off rather than retry every poll", tooSoon)
	}
	after, err := dispatch.Cycle(ctx, store, model.Limits{Workers: maxWorkers}, clock.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].TaskID != "t0" {
		t.Fatalf("dispatch = %+v, want t0 back once its backoff elapsed", after)
	}
}

// The distinction the two counts draw, from the side dispatch reads: a
// capacity refusal is not a failure of this task (Count), but it is a
// run worth waiting before repeating (BackoffCount).
func TestFailureStreakCountsACapacityRefusalForBackoffOnly(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))

	d, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now)
	if err != nil || len(d) != 1 {
		t.Fatalf("dispatch: %v, %+v", err, d)
	}
	if err := store.FinishRun(ctx, d[0].RunID, now.Add(time.Minute), model.CapacityOutcome, "overloaded"); err != nil {
		t.Fatal(err)
	}

	streak, err := store.FailureStreak(ctx, "t0")
	if err != nil {
		t.Fatal(err)
	}
	if streak == nil || streak.Count != 0 {
		t.Fatalf("FailureStreak = %+v, want Count 0: the task did not fail", streak)
	}
	if streak.BackoffCount != 1 {
		t.Fatalf("FailureStreak = %+v, want BackoffCount 1: the retry is still worth spacing", streak)
	}
}

// When a 16-vCPU agent task is ready and held back by the shared sandbox
// budget alongside queued 4-vCPU CI jobs, dispatch.Cycle registers that
// pending agent demand on the ledger so SharedGate holds freed 4-vCPU slivers
// back and wakes dispatch on each release until the 16-vCPU agent task fits.
func TestCycleRegistersAgentSandboxDemandAndHoldsBackCIJobsUntilAgentDispatches(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true))

	wakes := make(chan struct{}, 16)
	ledger := &model.Ledger{
		Wake: func() {
			select {
			case wakes <- struct{}{}:
			default:
			}
		},
	}
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 16, MemoryMB: 32 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
	agentShape := model.Commitment{CPUs: 16, MemoryMB: 32 * 1024}
	ciShape := model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}
	limits := model.Limits{
		Workers:    2,
		Budget:     budget,
		AgentShape: agentShape,
		Ledger:     ledger,
	}
	gate := &ci.SharedGate{
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Poll:   time.Millisecond,
	}

	// Four 4-vCPU CI jobs fill the 16-vCPU budget.
	var heldCI [4]func()
	for i := range heldCI {
		release, err := gate.Acquire(ctx, ci.GateRequest{Want: ciShape})
		if err != nil {
			t.Fatalf("CI job %d of 4: %v", i+1, err)
		}
		heldCI[i] = release
	}

	// First dispatch cycle: t0 is ready and admissible by worker count, but
	// held back by the sandbox budget. Cycle records the pending agent demand.
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("dispatched %+v while budget is full, want nothing", got)
	}
	if !ledger.AgentWaiting() {
		t.Fatal("ledger.AgentWaiting() = false after Cycle skipped ready task t0 for sandbox budget")
	}
	if gotShape := ledger.AgentWaitShape(); gotShape != agentShape {
		t.Fatalf("ledger.AgentWaitShape() = %+v, want %+v", gotShape, agentShape)
	}

	// Queue another 4-vCPU CI job behind the running four.
	var waitMu sync.Mutex
	var lastWait ci.Wait
	waitCh := make(chan ci.Wait, 16)
	admittedCI := make(chan func(), 1)
	go func() {
		release, err := gate.Acquire(ctx, ci.GateRequest{
			Want: ciShape,
			Wait: func(w ci.Wait) {
				waitMu.Lock()
				lastWait = w
				waitMu.Unlock()
				if w.Reason != "" {
					select {
					case waitCh <- w:
					default:
					}
				}
			},
		})
		if err == nil {
			admittedCI <- release
		}
	}()

	// Release three 4-vCPU CI jobs one by one. Each release wakes dispatch;
	// dispatch runs a cycle (still only 4, 8, 12 vCPUs free, so t0 keeps
	// waiting), and the queued 4-vCPU CI job is held back behind the waiting
	// agent sandbox rather than consuming the freed slivers.
	for i := 0; i < 3; i++ {
		heldCI[i]()
		select {
		case <-wakes:
		case <-time.After(2 * time.Second):
			t.Fatalf("release %d did not wake dispatch", i+1)
		}
		got, err = dispatch.Cycle(ctx, store, limits, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("after %d releases, dispatched %+v with only %d vCPUs free", i+1, got, (i+1)*4)
		}
		if !ledger.AgentWaiting() {
			t.Fatalf("after %d releases, ledger.AgentWaiting() = false, want true", i+1)
		}
	}

	// Wait until the queued CI job reports waiting behind the queued agent sandbox.
	deadline := time.After(2 * time.Second)
	for {
		waitMu.Lock()
		w := lastWait
		waitMu.Unlock()
		if w.Cause == ci.WaitForPriority && strings.Contains(w.Reason, "waiting behind a queued agent sandbox") {
			break
		}
		select {
		case <-waitCh:
		case <-deadline:
			t.Fatalf("queued CI job never reported waiting behind a queued agent sandbox; last wait = %+v", w)
		}
	}
	select {
	case <-admittedCI:
		t.Fatal("queued 4-vCPU CI job consumed freed slivers while 16-vCPU agent task was waiting")
	default:
	}

	// Releasing the fourth 4-vCPU CI job wakes dispatch; all 16 vCPUs are
	// now free, so dispatch.Cycle admits t0 and clears AgentWaiting.
	heldCI[3]()
	select {
	case <-wakes:
	case <-time.After(2 * time.Second):
		t.Fatal("releasing the fourth CI job did not wake dispatch")
	}
	got, err = dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "t0" {
		t.Fatalf("dispatched %+v after all four CI jobs released, want [t0]", got)
	}
	if ledger.AgentWaiting() {
		t.Fatal("ledger.AgentWaiting() = true after the only waiting agent task was dispatched, want false")
	}

	// Finishing t0's agent sandbox releases the 16 vCPUs, and the queued
	// 4-vCPU CI job is now admitted normally.
	ledger.Release(model.PoolAgent, got[0].RunID)
	select {
	case rel := <-admittedCI:
		rel()
	case <-time.After(2 * time.Second):
		t.Fatal("queued CI job was not admitted after waiting agent task dispatched and released")
	}
}

// Tasks skipped by dispatch.Skip (such as paused tasks) or held back by the
// worker count ceiling do not mark the ledger as waiting on sandbox budget,
// whereas an eligible RunNow request held back by the sandbox budget does.
func TestCycleMarksAgentWaitingOnlyForTasksHeldBySandboxBudget(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("t0", true), task("unapproved", false))

	ledger := &model.Ledger{}
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 16, MemoryMB: 32 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
	// Hold 8 vCPUs in CI so a 16-vCPU agent shape cannot fit.
	releaseCI := ledger.Charge(model.PoolCI, "ci-1", model.Commitment{CPUs: 8, MemoryMB: 16 * 1024})
	defer releaseCI()

	limits := model.Limits{
		Workers:    1,
		Budget:     budget,
		AgentShape: model.Commitment{CPUs: 16, MemoryMB: 32 * 1024},
		Ledger:     ledger,
	}

	// If t0 is held (e.g. paused), no task is waiting on sandbox budget.
	if _, err := dispatch.Cycle(ctx, store, limits, now, dispatch.Hold(func(id string) string {
		if id == "t0" {
			return "paused"
		}
		return ""
	})); err != nil {
		t.Fatal(err)
	}
	if ledger.AgentWaiting() {
		t.Fatal("ledger.AgentWaiting() = true when the only ready task was skipped")
	}

	// If Workers is 0 and Mergers is 1 (capped by worker count rather than
	// sandbox budget), no worker task is waiting on sandbox budget.
	capped := limits
	capped.Workers = 0
	capped.Mergers = 1
	if _, err := dispatch.Cycle(ctx, store, capped, now); err != nil {
		t.Fatal(err)
	}
	if ledger.AgentWaiting() {
		t.Fatal("ledger.AgentWaiting() = true when worker count limit is 0")
	}

	// An operator clicking RunNow on "unapproved" while t0 is held puts
	// a RunNow request in line that is held back solely by the sandbox budget.
	requestRunNow(t, ctx, store, "unapproved", now)
	if _, err := dispatch.Cycle(ctx, store, limits, now, dispatch.Hold(func(id string) string {
		if id == "t0" {
			return "paused"
		}
		return ""
	})); err != nil {
		t.Fatal(err)
	}
	if !ledger.AgentWaiting() {
		t.Fatal("ledger.AgentWaiting() = false when a RunNow request is held back by sandbox budget")
	}
}
