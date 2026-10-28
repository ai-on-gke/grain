package model

import (
	"fmt"
	"strings"
	"testing"
)

// The host grain/task-173 was measured on, so the arithmetic below is
// the arithmetic that deployment got wrong: 28 vCPU and 128 GiB, with CI
// jobs of 16 GiB and agent sandboxes of 8.
func hostBudget() Budget {
	return Budget{
		Host:             Commitment{CPUs: 28, MemoryMB: 128 * 1024},
		ReserveCPUs:      DefaultReserveCPUs(28),
		ReserveMemoryMB:  DefaultReserveMemoryMB(128 * 1024),
		CPUOvercommit:    DefaultCPUOvercommit,
		MemoryOvercommit: DefaultMemoryOvercommit,
	}
}

func TestBudgetKeepsItsReserveBackFromTheMachine(t *testing.T) {
	b := hostBudget()
	if got := DefaultReserveMemoryMB(128 * 1024); got != 16*1024 {
		t.Fatalf("an eighth of 128 GiB: got %d MiB", got)
	}
	memMB, ok := b.MemoryBudgetMB()
	if !ok {
		t.Fatal("a budget with a host reading bounds memory")
	}
	if memMB != 112*1024 {
		t.Errorf("128 GiB less a 16 GiB reserve at 1x: got %d MiB", memMB)
	}
	if cpus, ok := b.CPUBudget(); !ok || cpus != 52 {
		t.Errorf("28 vCPU less a 2 vCPU reserve at 2x: got %v (bounded %v)", cpus, ok)
	}
	// The reserve is not the last byte of RAM: a commitment that fits
	// the machine but not the machine-less-reserve is refused, which is
	// the whole point of holding anything back.
	live := Commitment{MemoryMB: 104 * 1024}
	if v := b.Admits(live, Commitment{MemoryMB: 16 * 1024}); v.OK {
		t.Error("104 GiB committed plus a 16 GiB job fits inside 128 GiB but not inside the budget")
	}
	if v := b.Admits(live, Commitment{MemoryMB: 8 * 1024}); !v.OK {
		t.Errorf("104 + 8 GiB is inside the 112 GiB budget: %q", v.Reason)
	}
}

// A small host keeps back the floor rather than an eighth of very
// little: 8 GiB of RAM and 2 cores are kept for the grain controller daemon.
// But never more than half the host, or a host under 16 GiB has a
// budget that admits nothing (task 1940).
func TestBudgetReserveHasAFloor(t *testing.T) {
	if got := DefaultReserveMemoryMB(16 * 1024); got != MinReserveMemoryMB {
		t.Errorf("an eighth of 16 GiB is below the floor: got %d MiB", got)
	}
	if got := DefaultReserveMemoryMB(8 * 1024); got != 4*1024 {
		t.Errorf("the 8 GiB floor would be the whole of an 8 GiB host: got %d MiB, want half of it", got)
	}
	b := NewBudget(Commitment{CPUs: 4, MemoryMB: 7987}, ReserveMemoryDefault, DefaultCPUOvercommit, DefaultMemoryOvercommit)
	if mem, _ := b.MemoryBudgetMB(); mem <= 0 {
		t.Errorf("the default reserve leaves a 7.8 GiB host no sandbox memory at all: %+v", b)
	}
	if got := DefaultReserveMemoryMB(32 * 1024); got != MinReserveMemoryMB {
		t.Errorf("an eighth of 32 GiB is below the floor: got %d MiB", got)
	}
	if got := DefaultReserveMemoryMB(0); got != 0 {
		t.Errorf("no host reading reserves nothing: got %d MiB", got)
	}
	if got := DefaultReserveCPUs(28); got != MinReserveCPUs {
		t.Errorf("28 host CPUs: got %d CPU reserve, want %d", got, MinReserveCPUs)
	}
	if got := DefaultReserveCPUs(0); got != 0 {
		t.Errorf("no host reading reserves nothing: got %d CPU reserve", got)
	}
}

func TestBudgetAppliesTheOvercommitRatioToWhatIsLeft(t *testing.T) {
	b := hostBudget()
	b.MemoryOvercommit = 2
	memMB, _ := b.MemoryBudgetMB()
	if memMB != 224*1024 {
		t.Errorf("2x of (128 - 16) GiB: got %d MiB", memMB)
	}
	cpus, ok := b.CPUBudget()
	if !ok || cpus != 52 {
		t.Errorf("2x of (28 - 2) vCPU: got %v (bounded %v)", cpus, ok)
	}
	// And the ratio multiplies what is left, not the machine: the
	// reserve survives an operator who doubles the ratio.
	if memMB <= 0 || memMB >= 2*128*1024 {
		t.Errorf("the reserve is still held back at 2x: %d MiB of a 128 GiB host", memMB)
	}
}

func TestBudgetWithNoHostReadingBoundsNothing(t *testing.T) {
	var b Budget
	if b.Known() {
		t.Fatal("the zero budget bounds nothing")
	}
	if v := b.Admits(Commitment{MemoryMB: 1 << 20}, Commitment{MemoryMB: 1 << 20}); !v.OK {
		t.Error("a budget with nothing to hold work against admits it rather than refusing everything")
	}
	// The same for a dimension nobody chose a ratio for.
	half := Budget{Host: Commitment{CPUs: 4, MemoryMB: 4096}, MemoryOvercommit: 1}
	if _, ok := half.CPUBudget(); ok {
		t.Error("an overcommit ratio of 0 leaves that dimension unenforced")
	}
	if v := half.Admits(Commitment{CPUs: 64}, Commitment{CPUs: 64}); !v.OK {
		t.Errorf("unenforced cpu admits any shape: %q", v.Reason)
	}
}

func TestBudgetRefusesOnCPUAsWellAsMemory(t *testing.T) {
	b := Budget{Host: Commitment{CPUs: 4, MemoryMB: 64 * 1024}, CPUOvercommit: 2, MemoryOvercommit: 1}
	v := b.Admits(Commitment{CPUs: 6, MemoryMB: 1024}, Commitment{CPUs: 4, MemoryMB: 1024})
	if v.OK {
		t.Fatal("6 + 4 vCPU is past a 4-core host at 2x")
	}
	if !strings.HasPrefix(v.Reason, "cpu:") {
		t.Errorf("the refusal says which dimension is short: %q", v.Reason)
	}
}

// A sandbox nothing can ever make room for runs rather than waiting for
// capacity that is not coming: a budget too small for one job costs a
// deployment its parallelism, where a refusal would cost it its CI.
func TestBudgetAdmitsAShapeBiggerThanItselfWhenNothingElseHoldsIt(t *testing.T) {
	b := Budget{Host: Commitment{CPUs: 2, MemoryMB: 8192}, CPUOvercommit: 1, MemoryOvercommit: 1, ReserveMemoryMB: 2048}
	v := b.Admits(Commitment{}, Commitment{CPUs: 4, MemoryMB: 16384})
	if !v.OK || !v.Sole {
		t.Fatalf("a job larger than the whole budget runs alone: %+v", v)
	}
	// But only alone: the second one waits.
	if v := b.Admits(Commitment{CPUs: 4, MemoryMB: 16384}, Commitment{CPUs: 4, MemoryMB: 16384}); v.OK {
		t.Error("the second such job waits for the first")
	}
}

// A refusal of a sandbox bigger than the whole budget says it will start
// only once nothing else is live, in whichever dimension is short; one
// that would fit in an emptier budget does not (2026-10-25 walkthrough:
// an OOM-raised task otherwise reads as merely waiting for a slot).
func TestBudgetRefusalSaysWhenOneSandboxIsBiggerThanTheWholeBudget(t *testing.T) {
	const clause = "more than the whole budget, so it starts only once no other sandbox is live"
	b := Budget{Host: Commitment{CPUs: 4, MemoryMB: 16384}, CPUOvercommit: 1, MemoryOvercommit: 1, ReserveMemoryMB: 2048}
	for _, tc := range []struct {
		name       string
		live, want Commitment
		prefix     string
		bigger     bool
	}{
		{"memory past the budget", Commitment{CPUs: 1, MemoryMB: 4096}, Commitment{CPUs: 1, MemoryMB: 32768}, "memory:", true},
		{"memory that would fit alone", Commitment{CPUs: 1, MemoryMB: 8192}, Commitment{CPUs: 1, MemoryMB: 8192}, "memory:", false},
		{"cpu past the budget", Commitment{CPUs: 1, MemoryMB: 1024}, Commitment{CPUs: 8, MemoryMB: 1024}, "cpu:", true},
		{"cpu that would fit alone", Commitment{CPUs: 2, MemoryMB: 1024}, Commitment{CPUs: 3, MemoryMB: 1024}, "cpu:", false},
	} {
		v := b.Admits(tc.live, tc.want)
		if v.OK {
			t.Fatalf("%s: admitted %+v, want a refusal", tc.name, v)
		}
		if !strings.HasPrefix(v.Reason, tc.prefix) || strings.Contains(v.Reason, clause) != tc.bigger {
			t.Errorf("%s: reason %q, want prefix %q and the whole-budget clause %v", tc.name, v.Reason, tc.prefix, tc.bigger)
		}
	}
}

// The budget is three settings rather than three flags now
// (grain/task-197), so it moves under a deployment that is already
// running: an operator relieving a throttled host tightens memory in the
// pane while six sandboxes are live on the old figure. What that has to
// mean is the next admission and only the next admission -- nothing is
// interrupted to make the smaller number true, and nothing goes on being
// admitted against the larger one.
func TestABudgetShrunkUnderLiveSandboxesBindsOnlyTheNextAdmission(t *testing.T) {
	agent := Commitment{CPUs: 4, MemoryMB: 8192}
	live := agent.Times(6)

	// 48 GiB live under a budget of 112: room for more.
	if v := hostBudget().Admits(live, agent); !v.OK {
		t.Fatalf("a seventh sandbox fits the budget it was sized against: %q", v.Reason)
	}

	// The same six sandboxes, and a memory overcommit an operator has
	// just cut to a third of the machine. Nothing here can kill one --
	// Admits is asked about a sandbox being built, not about the ones
	// that exist -- so what the new figure does is hold the next.
	tight := hostBudget()
	tight.ReserveMemoryMB = 96 * 1024
	v := tight.Admits(live, agent)
	if v.OK {
		t.Fatalf("the seventh sandbox is admitted against the new budget, not the one the live six were: %+v", v)
	}
	if !strings.Contains(v.Reason, "memory") {
		t.Errorf("the refusal names what is short: %q", v.Reason)
	}
	// And it is not a wedge: as the live six drain, the deployment goes
	// on admitting work against the smaller number rather than waiting
	// for a restart to believe it.
	if v := tight.Admits(agent.Times(3), agent); !v.OK {
		t.Errorf("with 24 GiB live of a 32 GiB budget, the next 8 GiB sandbox fits: %q", v.Reason)
	}
}

func TestBudgetCountsEveryLivePoolAtOnce(t *testing.T) {
	// One budget, both pools in it: 48 GiB of agent sandboxes live, and
	// the 112 GiB budget has 64 left, which is four 16 GiB CI jobs.
	live := Commitment{CPUs: 24, MemoryMB: 48 * 1024}
	b := hostBudget()
	for i := 0; i < 4; i++ {
		if v := b.Admits(live, Commitment{CPUs: 4, MemoryMB: 16 * 1024}); !v.OK {
			t.Fatalf("job %d of 4 fits in what is left: %q", i+1, v.Reason)
		}
		live = live.Plus(Commitment{CPUs: 4, MemoryMB: 16 * 1024})
	}
	v := b.Admits(live, Commitment{CPUs: 4, MemoryMB: 16 * 1024})
	if v.OK {
		t.Fatal("the fifth 16 GiB job would take the host past what it has")
	}
	if !strings.Contains(v.Reason, "memory") {
		t.Errorf("the refusal names memory: %q", v.Reason)
	}
}

func TestCommitmentArithmetic(t *testing.T) {
	c := Commitment{CPUs: 2, MemoryMB: 1024}
	if got := c.Times(3); got != (Commitment{CPUs: 6, MemoryMB: 3072}) {
		t.Errorf("three of them: %+v", got)
	}
	if got := c.Times(0); !got.IsZero() {
		t.Errorf("none of them commits nothing: %+v", got)
	}
	// A ledger cannot go negative: an extra release would otherwise
	// report capacity nobody gave back.
	if got := c.Minus(Commitment{CPUs: 9, MemoryMB: 9999}); !got.IsZero() {
		t.Errorf("giving back more than was taken floors at nothing: %+v", got)
	}
}

func TestLimitsAdmitAgentRunsAgainstTheCountAndTheBudgetSeparately(t *testing.T) {
	// The count is the team's, and it is the whole of what Admits asks:
	// the budget is a different question with a different answer
	// (SandboxVerdict), because the two bound different scopes now.
	team := Limits{Workers: 5, Mergers: 1}
	if !team.Admits(RunCounts{Workers: 2}, false) {
		t.Error("two live workers of five leaves room")
	}
	if team.Admits(RunCounts{Workers: 5, Mergers: 1}, true) {
		t.Error("nothing is admitted past Workers+Mergers")
	}
	if team.Admits(RunCounts{Workers: 5}, false) {
		t.Error("a sixth worker is past max_workers even with a merger slot free")
	}
	if !team.Admits(RunCounts{Workers: 5}, true) {
		t.Error("a merger may take the capacity a worker may not")
	}
	// A scope with no counts bounds no count: that is the deployment
	// scope, and a team that has chosen nothing.
	if !(Limits{}).Admits(RunCounts{Workers: 99}, false) {
		t.Error("no counts means no count bound, not admit nothing")
	}

	// And the budget half, which is deployment-wide and reads the live
	// ledger rather than a count. Room for three 8 GiB sandboxes.
	ledger := &Ledger{}
	deployment := Limits{
		Budget:     Budget{Host: Commitment{CPUs: 32, MemoryMB: 32 * 1024}, CPUOvercommit: 4, MemoryOvercommit: 1, ReserveMemoryMB: 8 * 1024},
		AgentShape: Commitment{CPUs: 4, MemoryMB: 8 * 1024},
		Ledger:     ledger,
	}
	for i := 0; i < 3; i++ {
		verdict, release := deployment.ChargeSandbox(fmt.Sprintf("run-%d", i))
		if !verdict.OK || release == nil {
			t.Fatalf("sandbox %d of 3 fits a 24 GiB budget: %q", i+1, verdict.Reason)
		}
	}
	if deployment.SandboxAdmits() {
		t.Error("a fourth 8 GiB sandbox is past a 24 GiB budget")
	}
	// Giving one back makes room again, which a standing reservation
	// never did.
	ledger.Release(PoolAgent, "run-0")
	if !deployment.SandboxAdmits() {
		t.Error("with one released the next sandbox fits again")
	}
	// A deployment with no host reading bounds nothing at all.
	if !(Limits{AgentShape: Commitment{CPUs: 4, MemoryMB: 8 * 1024}}).SandboxAdmits() {
		t.Error("no host reading leaves the budget bounding nothing")
	}
}

// The two pools share one budget rather than one reserving against the
// other: a CI job and an agent run are charged the same way to the same
// ledger, and what refuses either one is the sum of both.
func TestBothPoolsAreAdmittedAgainstOneLiveTotal(t *testing.T) {
	ledger := &Ledger{}
	limits := incidentLimits()
	limits.Ledger = ledger
	// 28 host vCPU at 2.0x is a 56 vCPU budget. Seven agent sandboxes at
	// 4 vCPU each fill half of it.
	for i := 0; i < 7; i++ {
		verdict, _ := limits.ChargeSandbox(fmt.Sprintf("run-%d", i))
		if !verdict.OK {
			t.Fatalf("agent sandbox %d of 7 fits: %q", i+1, verdict.Reason)
		}
	}
	if got := ledger.Live().CPUs; got != 28 {
		t.Fatalf("seven 4 vCPU sandboxes commit 28 vCPU, got %d", got)
	}
	// CI sees them, which is the whole point: 28 of 56 is spent, so four
	// more 8 vCPU jobs fit and a fifth does not.
	job := Commitment{CPUs: 8, MemoryMB: 8 * 1024}
	for i := 0; i < 3; i++ {
		verdict, _ := ledger.Admit(limits.Budget, PoolCI, fmt.Sprintf("job-%d", i), job)
		if !verdict.OK {
			t.Fatalf("CI job %d fits the 28 vCPU left: %q", i+1, verdict.Reason)
		}
	}
	verdict, release := ledger.Admit(limits.Budget, PoolCI, "job-4", job)
	if verdict.OK || release != nil {
		t.Fatal("52 of 56 vCPU is committed, so an 8 vCPU job waits")
	}
	// And the refusal reports the live total against the whole budget,
	// with no pool's share and no reservation in it -- which is what an
	// operator could not see on the night of grain/task-506.
	for _, want := range []string{
		"52 vCPU is committed by this deployment's live sandboxes",
		"its 56 vCPU sandbox budget (28 host vCPU at 2.0x)",
		"this one wants 8",
	} {
		if !strings.Contains(verdict.Reason, want) {
			t.Errorf("the refusal should say %q: %s", want, verdict.Reason)
		}
	}
	for _, unwanted := range []string{"left to this pool", "held back for", "agent pool"} {
		if strings.Contains(verdict.Reason, unwanted) {
			t.Errorf("no reservation is described any more: %s", verdict.Reason)
		}
	}
	// An idle agent slot costs nothing: releasing the seven agent
	// sandboxes lets the job straight in, where a standing reservation
	// for the configured pool would have gone on holding 28 vCPU.
	for i := 0; i < 7; i++ {
		ledger.Release(PoolAgent, fmt.Sprintf("run-%d", i))
	}
	if v, _ := ledger.Admit(limits.Budget, PoolCI, "job-4", job); !v.OK {
		t.Errorf("with the agent sandboxes gone the job fits: %q", v.Reason)
	}
}

// A configured maximum is never multiplied into the budget or subtracted
// from it: the legacy count fields may say anything at all and the
// admission arithmetic does not move.
func TestConfiguredCountsDoNotMoveTheBudget(t *testing.T) {
	ledger := &Ledger{}
	base := incidentLimits()
	base.Ledger = ledger
	huge := base
	huge.Workers, huge.Mergers, huge.CIJobs = 300, 300, 300
	live := Commitment{CPUs: 48}
	if base.Budget.Admits(live, Commitment{CPUs: 4}) != huge.Budget.Admits(live, Commitment{CPUs: 4}) {
		t.Error("the counts changed what the budget admits")
	}
	if a, b := base.SandboxVerdict(), huge.SandboxVerdict(); a.OK != b.OK || a.Reason != b.Reason {
		t.Errorf("the counts changed the deployment's own verdict: %+v vs %+v", a, b)
	}
}

// incidentLimits is the night of grain/task-506 in its own numbers: 28
// host vCPU at 2.0x is a 56 vCPU budget, and the deployment was
// configured for four workers, two mergers and seven CI jobs.
//
// What made it hard to read was not the numbers but the accounting: half
// of that 56 was a standing reservation for seven agent slots, six of
// them live and sitting at load 0.00, and the sentence a refused CI job
// got named the deployment-wide total and nothing about the reservation.
// The counts survive here only as the legacy fields they now are -- the
// tests above use them to show that they change nothing.
func incidentLimits() Limits {
	return Limits{
		Workers: 4, Mergers: 2, CIJobs: 7,
		Budget:     Budget{Host: Commitment{CPUs: 28, MemoryMB: 128 * 1024}, CPUOvercommit: 2, MemoryOvercommit: 1, ReserveMemoryMB: 16 * 1024},
		AgentShape: Commitment{CPUs: 4, MemoryMB: 8 * 1024},
	}
}

// Disk is promised against the cluster's allocatable ephemeral storage
// (Host.DiskGB) less the floor, times DiskOvercommit: the third 40 GiB
// sandbox that a request equal to its limit used to keep off a 100 GiB
// node fits once the ratio says a disk is mostly never written.
func TestBudgetHoldsPromisedDiskAgainstTheClusterTimesTheRatio(t *testing.T) {
	b := Budget{
		Host:           Commitment{DiskGB: 100},
		DiskFloor:      DiskFloor{Share: 0.1},
		DiskOvercommit: 2,
	}
	// (100 GiB - a tenth kept free) x 2 = 180 GiB.
	if gb, ok := b.DiskBudgetGB(); !ok || gb != 180 {
		t.Fatalf("DiskBudgetGB = %d (bounded %v), want 180", gb, ok)
	}
	if !b.Known() {
		t.Error("a budget bounding only disk is still a budget")
	}
	sandbox := Commitment{CPUs: 2, MemoryMB: 4096, DiskGB: 40}
	if v := b.Admits(sandbox.Times(3), sandbox); !v.OK {
		t.Errorf("a fourth 40 GiB sandbox (160 of 180 GiB) was refused: %s", v.Reason)
	}
	v := b.Admits(sandbox.Times(4), sandbox)
	if v.OK {
		t.Fatal("a fifth 40 GiB sandbox (200 of 180 GiB) was admitted")
	}
	if !strings.Contains(v.Reason, "disk: 160 GiB") || !strings.Contains(v.Reason, "180 GiB") {
		t.Errorf("refusal = %q, want it to say what is promised and of what", v.Reason)
	}
	// The Sole rule holds for disk as for memory: one sandbox bigger
	// than the whole budget still runs on an empty deployment.
	if v := b.Admits(Commitment{}, Commitment{DiskGB: 500}); !v.OK || !v.Sole {
		t.Errorf("an oversized disk on an empty budget = %+v, want admitted as Sole", v)
	}
	if b.WithinCap(sandbox.Times(5)) {
		t.Error("200 GiB promised of 180 reads as within cap")
	}
}

// On a local host there is no reported disk; the live reading of the
// sandbox volume is the figure instead. And a ratio of zero leaves the sum
// unbounded, held by the floor alone.
func TestBudgetDiskFallsBackToTheVolumeAndZeroIsUnbounded(t *testing.T) {
	b := Budget{
		Disk:           DiskSpace{Path: "/var/lib/grain", TotalMB: 200 * 1024, FreeMB: 150 * 1024},
		DiskFloor:      DiskFloor{Share: 0.1},
		DiskOvercommit: 1.5,
	}
	// (200 GiB - 20 GiB floor) x 1.5 = 270 GiB.
	if gb, ok := b.DiskBudgetGB(); !ok || gb != 270 {
		t.Fatalf("DiskBudgetGB = %d (bounded %v), want 270 off the volume reading", gb, ok)
	}
	if v := b.Admits(Commitment{DiskGB: 240}, Commitment{DiskGB: 40}); v.OK {
		t.Error("280 GiB promised of a 270 GiB volume budget was admitted")
	}
	// A shape nobody sized a disk for is charged none.
	if v := b.Admits(Commitment{DiskGB: 270}, Commitment{CPUs: 1, MemoryMB: 1024}); !v.OK {
		t.Errorf("a shape with no disk was refused on disk: %s", v.Reason)
	}
	b.DiskOvercommit = 0
	if _, ok := b.DiskBudgetGB(); ok {
		t.Error("a disk ratio of 0 still bounds the promised disk")
	}
	if v := b.Admits(Commitment{DiskGB: 10000}, Commitment{DiskGB: 40}); !v.OK {
		t.Errorf("an unbounded disk ratio refused: %s", v.Reason)
	}
}

func TestCommitmentCarriesDisk(t *testing.T) {
	a := Commitment{CPUs: 2, MemoryMB: 1024, DiskGB: 30}
	if got := a.Plus(a).Minus(Commitment{DiskGB: 100}); got != (Commitment{CPUs: 4, MemoryMB: 2048}) {
		t.Errorf("Plus/Minus = %+v, want the disk floored at zero", got)
	}
	if got := a.Times(3); got.DiskGB != 90 {
		t.Errorf("Times(3).DiskGB = %d, want 90", got.DiskGB)
	}
	if (Commitment{DiskGB: 1}).IsZero() {
		t.Error("a commitment of disk alone reads as zero")
	}
	if got := a.String(); got != "2 vCPU / 1.0 GiB / 30 GiB disk" {
		t.Errorf("String = %q", got)
	}
}
