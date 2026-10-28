package model

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ledgerBudget is a 16 vCPU / 32 GiB budget: four sandboxes of the shape
// below and no more, which is small enough to race over.
func ledgerBudget() Budget {
	return Budget{
		Host:             Commitment{CPUs: 16, MemoryMB: 32 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
}

func ledgerShape() Commitment { return Commitment{CPUs: 4, MemoryMB: 8 * 1024} }

// The property the whole of grain/task-28 turns on: an agent admission
// and a CI admission racing for the last of a budget cannot both be given
// it. Two pools, one ledger, one lock.
func TestLedgerNeverHandsTheSameCapacityToBothPools(t *testing.T) {
	for attempt := 0; attempt < 50; attempt++ {
		ledger := &Ledger{}
		budget := ledgerBudget()
		// Three of the four sandboxes are already live, so exactly one
		// admission of this shape can succeed.
		for i := 0; i < 3; i++ {
			if v, _ := ledger.Admit(budget, PoolAgent, fmt.Sprintf("live-%d", i), ledgerShape()); !v.OK {
				t.Fatalf("sandbox %d of 3 should fit: %q", i, v.Reason)
			}
		}

		var wg sync.WaitGroup
		var mu sync.Mutex
		var won []string
		start := make(chan struct{})
		for _, c := range []struct{ pool, id string }{
			{PoolAgent, "racing-run"},
			{PoolCI, "racing-job"},
		} {
			wg.Add(1)
			go func(pool, id string) {
				defer wg.Done()
				<-start
				if v, release := ledger.Admit(budget, pool, id, ledgerShape()); v.OK {
					mu.Lock()
					won = append(won, pool)
					mu.Unlock()
					_ = release
				}
			}(c.pool, c.id)
		}
		close(start)
		wg.Wait()

		if len(won) != 1 {
			t.Fatalf("attempt %d: %d of the two racing admissions were given the last sandbox (%v); "+
				"exactly one may have it", attempt, len(won), won)
		}
		if live := ledger.Live(); live.CPUs > 16 || live.MemoryMB > 32*1024 {
			t.Fatalf("attempt %d: the ledger is over the budget it admitted against: %s", attempt, live)
		}
	}
}

// And the same property with every goroutine racing from empty: however
// many ask at once, the ledger never admits more than the budget holds.
func TestLedgerHoldsTheBudgetUnderConcurrentAdmission(t *testing.T) {
	ledger := &Ledger{}
	budget := ledgerBudget()
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pool := PoolAgent
			if i%2 == 1 {
				pool = PoolCI
			}
			if v, _ := ledger.Admit(budget, pool, fmt.Sprintf("%s-%d", pool, i), ledgerShape()); v.OK {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if admitted != 4 {
		t.Errorf("a 16 vCPU / 32 GiB budget holds four sandboxes of 4 vCPU / 8 GiB, %d were admitted", admitted)
	}
	if got := ledger.Live(); got != (Commitment{CPUs: 16, MemoryMB: 32 * 1024}) {
		t.Errorf("live commitment after the race: %s", got)
	}
}

// A release gives back exactly what its own admission took, once, however
// many times it is called.
func TestLedgerReleaseIsIdempotent(t *testing.T) {
	ledger := &Ledger{}
	_, release := ledger.Admit(ledgerBudget(), PoolCI, "job", ledgerShape())
	if release == nil {
		t.Fatal("an admitted job has a release")
	}
	release()
	release()
	release()
	if got := ledger.Live(); !got.IsZero() {
		t.Errorf("three releases of one charge gave back more than it took: %s", got)
	}
	// And releasing something nobody charged is not an error either: a
	// run that never reached a sandbox ends up here.
	ledger.Release(PoolAgent, "never-charged")
	if got := ledger.Live(); !got.IsZero() {
		t.Errorf("releasing an uncharged id changed the ledger: %s", got)
	}
}

// Charging the same holder twice replaces its entry rather than adding to
// it, so a charge repeated by a reconcile cannot double-spend.
func TestLedgerChargeIsIdempotentPerHolder(t *testing.T) {
	ledger := &Ledger{}
	for i := 0; i < 3; i++ {
		ledger.Charge(PoolAgent, "run-1", ledgerShape())
	}
	if got := ledger.Live(); got != ledgerShape() {
		t.Errorf("one holder charged three times: %s", got)
	}
	if got := ledger.Count(PoolAgent); got != 1 {
		t.Errorf("agent sandboxes live: %d, want 1", got)
	}
}

// Sync is the restart path and the leak path: a live run nobody charged
// is charged, and a charge with no live run behind it is collected once it
// is old enough to be a leak rather than a dispatch in progress.
func TestLedgerSyncAdoptsLiveRunsAndCollectsLeaks(t *testing.T) {
	now := time.Now()
	ledger := &Ledger{Now: func() time.Time { return now }}

	// A process that came up with two runs already in flight.
	ledger.Sync(PoolAgent, map[string]Commitment{"run-1": ledgerShape(), "run-2": ledgerShape()})
	if got := ledger.Live(); got != ledgerShape().Times(2) {
		t.Fatalf("two recovered runs: %s", got)
	}

	// A charge younger than the grace window survives a sync that has
	// never heard of it: this is a run charged a moment before its row
	// was written.
	ledger.Charge(PoolAgent, "run-3", ledgerShape())
	ledger.Sync(PoolAgent, map[string]Commitment{"run-1": ledgerShape(), "run-2": ledgerShape()})
	if got := ledger.Count(PoolAgent); got != 3 {
		t.Errorf("a charge made a moment ago was collected as a leak: %d live", got)
	}

	// Past the grace window it is a leak, and is collected.
	now = now.Add(LedgerSyncGrace + time.Second)
	ledger.Sync(PoolAgent, map[string]Commitment{"run-1": ledgerShape()})
	if got := ledger.Count(PoolAgent); got != 1 {
		t.Errorf("stale charges were not collected: %d live", got)
	}

	// And the other pool is left entirely alone: CI's charges live and
	// die with the gate that made them.
	ledger.Charge(PoolCI, "job-1", ledgerShape())
	now = now.Add(LedgerSyncGrace + time.Second)
	ledger.Sync(PoolAgent, nil)
	if got := ledger.Count(PoolCI); got != 1 {
		t.Errorf("syncing the agent pool collected a CI charge: %d live", got)
	}
}

// An idle slot costs nothing. This is the behaviour the standing
// reservation did not have, stated directly.
func TestLedgerHoldsNothingForWorkThatIsNotRunning(t *testing.T) {
	ledger := &Ledger{}
	limits := Limits{
		// Counts as high as anybody could set them, and a team's counts
		// at that: none of it reaches the budget.
		Workers: 50, Mergers: 50, CIJobs: 50,
		Budget: ledgerBudget(), AgentShape: ledgerShape(), Ledger: ledger,
	}
	if got := limits.LiveCommitment(); !got.IsZero() {
		t.Errorf("a deployment with nothing running commits nothing: %s", got)
	}
	if !limits.SandboxAdmits() {
		t.Error("a deployment with nothing running admits a sandbox")
	}
	if got := ledger.Describe(); got != "nothing" {
		t.Errorf("an empty ledger describes itself as %q", got)
	}

	_, release := limits.ChargeSandbox("run-1")
	if got := limits.LiveCommitment(); got != ledgerShape() {
		t.Errorf("one live run commits its shape: %s", got)
	}
	if got := ledger.Describe(); !strings.Contains(got, "1 agent sandbox") {
		t.Errorf("the ledger says where the machine went: %q", got)
	}
	release()
	if got := limits.LiveCommitment(); !got.IsZero() {
		t.Errorf("the run ended, so its shape is given back: %s", got)
	}
}

// The Director's bypass is a bypass of the count and not of the
// accounting: its sandbox is charged while the turn lasts, so everything
// else admitted meanwhile sees it, and is given back when the turn ends.
func TestDirectorIsChargedButNotRefused(t *testing.T) {
	ledger := &Ledger{}
	limits := Limits{Budget: ledgerBudget(), AgentShape: ledgerShape(), Ledger: ledger}
	// Fill the budget with ordinary runs.
	for i := 0; i < 4; i++ {
		if v, _ := limits.ChargeSandbox(fmt.Sprintf("run-%d", i)); !v.OK {
			t.Fatalf("run %d of 4 should fit: %q", i, v.Reason)
		}
	}
	if limits.SandboxAdmits() {
		t.Fatal("the budget is full, so an ordinary run waits")
	}
	// The console still starts, because an operator who cannot reach a
	// full deployment cannot fix it.
	release := limits.ChargeDirectorSandbox("director-run")
	if got := ledger.Live().CPUs; got != 20 {
		t.Errorf("the Director's sandbox is counted while it exists: %d vCPU live, want 20", got)
	}
	release()
	if got := ledger.Live().CPUs; got != 16 {
		t.Errorf("the Director's shape is given back when its turn ends: %d vCPU live, want 16", got)
	}
}

// Holders is the per-id breakdown LiveIn and Describe only ever
// aggregate -- what a caller deciding "is everything in my way held by
// *these* holders" (ci.SharedGate's Stuck hook) needs instead.
func TestLedgerHoldersBreaksOutEveryEntryByItsOwnPoolAndID(t *testing.T) {
	ledger := &Ledger{}
	if got := ledger.Holders(); len(got) != 0 {
		t.Fatalf("an empty ledger holds nothing: %+v", got)
	}
	releaseRun := ledger.Charge(PoolAgent, "run-1", Commitment{CPUs: 2, MemoryMB: 4 * 1024})
	defer releaseRun()
	releaseJob := ledger.Charge(PoolCI, "job-1", Commitment{CPUs: 1, MemoryMB: 2 * 1024})
	defer releaseJob()

	got := ledger.Holders()
	if len(got) != 2 {
		t.Fatalf("want one holder per charge, got %+v", got)
	}
	by := map[string]LedgerHolder{}
	for _, h := range got {
		by[h.Pool+"/"+h.ID] = h
	}
	run, ok := by[PoolAgent+"/run-1"]
	if !ok || run.Shape != (Commitment{CPUs: 2, MemoryMB: 4 * 1024}) {
		t.Errorf("the agent run's own entry = %+v", run)
	}
	job, ok := by[PoolCI+"/job-1"]
	if !ok || job.Shape != (Commitment{CPUs: 1, MemoryMB: 2 * 1024}) {
		t.Errorf("the CI job's own entry = %+v", job)
	}

	releaseRun()
	if got := ledger.Holders(); len(got) != 1 || got[0].ID != "job-1" {
		t.Errorf("a released holder drops out: %+v", got)
	}
}

// Describe names holders when they are few (3 or fewer), and falls back
// to per-pool totals when there are more.
func TestLedgerDescribeNamesFewHoldersAndSummarizesMany(t *testing.T) {
	ledger := &Ledger{}
	if got := ledger.Describe(); got != "nothing" {
		t.Fatalf("empty ledger = %q, want nothing", got)
	}

	r1 := ledger.Charge(PoolAgent, "run-1", Commitment{CPUs: 2, MemoryMB: 4 * 1024})
	defer r1()
	r2 := ledger.Charge(PoolAgent, "run-2", Commitment{CPUs: 2, MemoryMB: 4 * 1024})
	defer r2()
	j1 := ledger.Charge(PoolCI, "job-1", Commitment{CPUs: 1, MemoryMB: 2 * 1024})
	defer j1()

	got := ledger.Describe()
	wantFew := "5 vCPU / 10.0 GiB (4 vCPU / 8.0 GiB in 2 agent sandboxes, 1 vCPU / 2.0 GiB in 1 ci sandbox: agent run run-1, agent run run-2, CI job job-1)"
	if got != wantFew {
		t.Errorf("Describe (3 holders) = %q, want %q", got, wantFew)
	}

	// 4 holders: exceeds 3, so holders are omitted and only per-pool totals remain.
	j2 := ledger.Charge(PoolCI, "job-2", Commitment{CPUs: 1, MemoryMB: 2 * 1024})
	defer j2()

	got4 := ledger.Describe()
	wantMany := "6 vCPU / 12.0 GiB (4 vCPU / 8.0 GiB in 2 agent sandboxes, 2 vCPU / 4.0 GiB in 2 ci sandboxes)"
	if got4 != wantMany {
		t.Errorf("Describe (4 holders) = %q, want %q", got4, wantMany)
	}
}

// A nil ledger is a caller with no deployment to charge, and behaves as
// admission did before one existed.
func TestNilLedgerAdmitsAgainstAnEmptyHost(t *testing.T) {
	var ledger *Ledger
	if got := ledger.Live(); !got.IsZero() {
		t.Errorf("a nil ledger holds nothing: %s", got)
	}
	if !ledger.Admits(ledgerBudget(), ledgerShape()) {
		t.Error("a nil ledger admits against an empty host")
	}
	v, release := ledger.Admit(ledgerBudget(), PoolAgent, "run", ledgerShape())
	if !v.OK || release == nil {
		t.Errorf("a nil ledger admits and returns a release: %+v", v)
	}
	release()
	ledger.Release(PoolAgent, "run")
	ledger.Sync(PoolAgent, map[string]Commitment{"run": ledgerShape()})
	if got := ledger.Describe(); got != "nothing" {
		t.Errorf("a nil ledger describes itself as %q", got)
	}
}

// Policy: a director/console session or deadlock-breaking exception may exceed
// the budget cap by at most one sandbox while existing commitments are within
// the cap. Once committed exceeds the cap, both ordinary work and any second
// exception sandbox wait until committed drops back under the cap.
func TestDirectorExceptionAdmitsAtMostOneSandboxOverCap(t *testing.T) {
	ledger := &Ledger{}
	limits := Limits{Budget: ledgerBudget(), AgentShape: ledgerShape(), Ledger: ledger}
	for i := 0; i < 4; i++ {
		if v, _ := limits.ChargeSandbox(fmt.Sprintf("run-%d", i)); !v.OK {
			t.Fatalf("run %d of 4 should fit: %q", i, v.Reason)
		}
	}
	if !limits.Budget.WithinCap(ledger.Live()) {
		t.Fatalf("4 runs at 4 vCPU / 8 GiB should be exactly at cap (16 vCPU / 32 GiB), got %s", ledger.Live())
	}

	// First director is admitted as the single allowed exception over cap.
	v1, releaseDir1 := limits.AdmitDirectorSandbox("director-1")
	if !v1.OK || !v1.Exception {
		t.Fatalf("first director should be admitted as an exception: %+v", v1)
	}
	if limits.Budget.WithinCap(ledger.Live()) {
		t.Fatalf("after exception admission, live (%s) should exceed cap", ledger.Live())
	}

	// While over cap, a second director and ordinary work are both refused.
	if limits.DirectorSandboxAdmits() {
		t.Fatal("second director must wait while committed already exceeds the budget cap")
	}
	v2, _ := limits.AdmitDirectorSandbox("director-2")
	if v2.OK {
		t.Fatal("AdmitDirectorSandbox for second director must be refused while over cap")
	}
	if limits.SandboxAdmits() {
		t.Fatal("ordinary sandbox must wait while over cap")
	}

	// Once one ordinary run and the first director finish, committed is back under cap.
	releaseDir1()
	if !limits.DirectorSandboxAdmits() {
		t.Fatal("once back within cap, a director can be admitted again")
	}
}

// Persistent sandboxes reserve their share up front via Reserve, and re-admitting
// an existing (pool, id) replaces its shape rather than double-counting it.
func TestLedgerReserveAndReplacement(t *testing.T) {
	ledger := &Ledger{}
	budget := ledgerBudget() // 16 vCPU / 32 GiB

	ledger.Reserve(PoolPersistent, map[string]Commitment{
		"staging": {CPUs: 4, MemoryMB: 8 * 1024},
		"preview": {CPUs: 4, MemoryMB: 8 * 1024},
	})
	if got := ledger.LiveIn(PoolPersistent); got != (Commitment{CPUs: 8, MemoryMB: 16 * 1024}) {
		t.Fatalf("LiveIn(PoolPersistent) = %s, want 8 vCPU / 16 GiB", got)
	}
	if got := ledger.Describe(); !strings.Contains(got, "persistent sandbox") {
		t.Errorf("Describe should mention persistent sandboxes: %q", got)
	}

	// Re-admitting "staging" with the same shape when 8 vCPU more are held by
	// agents (total 16 vCPU = full cap) must succeed because liveWithout excludes
	// "staging"'s existing 4 vCPU reservation.
	r1 := ledger.Charge(PoolAgent, "run-1", Commitment{CPUs: 8, MemoryMB: 16 * 1024})
	defer r1()
	v, _ := ledger.Admit(budget, PoolPersistent, "staging", Commitment{CPUs: 4, MemoryMB: 8 * 1024})
	if !v.OK {
		t.Fatalf("re-admitting already-reserved persistent sandbox should succeed: %+v", v)
	}
}
