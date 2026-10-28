package ui_test

// grain/task-197: the sandbox budget's three knobs -- the two overcommit
// ratios and the host memory reserve -- were daemon flags, and are
// settings now. These cover what that move has to be true for: they are
// stored and read back, a value that is not a ratio is refused rather
// than quietly unbounding a dimension, a reserve that would leave no
// sandbox memory at all is refused rather than leaving the deployment
// admitting one job at a time, and the pane is told the arithmetic they
// make -- what is committed, what is held back for the host itself, and
// what that leaves grain CI.

import (
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
)

// budgetHost is a machine of the size these settings are for: 32 vCPU
// and 128 GiB, which is the shape of the host grain/task-173's own
// overcommit was measured on.
var budgetHost = ui.HostCapacity{CPUs: 32, MemoryMB: 128 * 1024}

func TestSettingsStoresTheSandboxBudget(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	cpu, memory, reserve := 3.0, 1.5, 8192
	got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUOvercommit:    &cpu,
		SandboxMemoryOvercommit: &memory,
		SandboxMemoryReserveMB:  &reserve,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxCPUOvercommit != 3 || got.SandboxMemoryOvercommit != 1.5 || got.SandboxMemoryReserveMB != 8192 {
		t.Errorf("saved budget = %v/%v at %d MiB reserved, want 3/1.5 at 8192",
			got.SandboxCPUOvercommit, got.SandboxMemoryOvercommit, got.SandboxMemoryReserveMB)
	}
	// And read back out of the row rather than only echoed by the save:
	// a setting the next process does not see is a flag with extra steps.
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.SandboxCPUOvercommit != 3 || read.SandboxMemoryOvercommit != 1.5 || read.SandboxMemoryReserveMB != 8192 {
		t.Errorf("stored budget reads back as %v/%v at %d MiB, want what was saved",
			read.SandboxCPUOvercommit, read.SandboxMemoryOvercommit, read.SandboxMemoryReserveMB)
	}
	if read.SandboxCPUOvercommitDefault != model.DefaultCPUOvercommit ||
		read.SandboxMemoryOvercommitDefault != model.DefaultMemoryOvercommit {
		t.Errorf("reported defaults = %v/%v, want grain's own %v/%v -- the pane shows them beside the fields",
			read.SandboxCPUOvercommitDefault, read.SandboxMemoryOvercommitDefault,
			model.DefaultCPUOvercommit, model.DefaultMemoryOvercommit)
	}
}

// Zero is a ratio an operator may deliberately want -- it is what every
// deployment did before the budget existed, with the run counts as the
// whole limit -- and a negative one is not a ratio at all. The budget
// reads both the same way, so the refusal has to happen here or nobody
// is ever told.
func TestSettingsRefusesANegativeOvercommit(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	unbounded := 0.0
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxCPUOvercommit: &unbounded}); err != nil {
		t.Fatalf("saving a zero cpu overcommit: %v -- zero is 'bounded by the counts alone', not an error", err)
	}
	negative := -1.0
	for name, req := range map[string]ui.UpdateSettingsRequest{
		"cpu":    {SandboxCPUOvercommit: &negative},
		"memory": {SandboxMemoryOvercommit: &negative},
	} {
		if _, err := c.UpdateSettings(ctx, req); err == nil {
			t.Errorf("a negative %s overcommit was accepted, want it refused", name)
		}
	}
}

// The disk ratio is stored, read back and validated like the other two,
// with grain's own default reported beside it for the pane.
func TestSettingsStoresTheSandboxDiskOvercommit(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, ui.HostCapacity{CPUs: 32, MemoryMB: 128 * 1024, DiskGB: 300}, nil)
	before, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.SandboxDiskOvercommit != model.DefaultDiskOvercommit ||
		before.SandboxDiskOvercommitDefault != model.DefaultDiskOvercommit {
		t.Errorf("a fresh deployment's disk overcommit = %v (default %v), want grain's own %v",
			before.SandboxDiskOvercommit, before.SandboxDiskOvercommitDefault, model.DefaultDiskOvercommit)
	}
	disk := 10.0
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxDiskOvercommit: &disk}); err != nil {
		t.Fatal(err)
	}
	read, err := c.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.SandboxDiskOvercommit != 10 {
		t.Errorf("stored disk overcommit reads back as %v, want 10", read.SandboxDiskOvercommit)
	}
	if b := read.SandboxCommitment.Budget; b == nil || !b.DiskBounded || b.DiskOvercommit != 10 {
		t.Errorf("budget = %+v, want the disk bounded at 10x", b)
	} else if want := int(float64(300*1024-model.DiskFloor{Share: model.DefaultDiskFloorShare, MinMB: model.MinDiskFloorMB}.FloorMB(300*1024)) / 1024 * 10); b.DiskGB != want {
		t.Errorf("disk budget = %d GiB, want %d", b.DiskGB, want)
	}
	unbounded := 0.0
	if got, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{
		SandboxCPUOvercommit:    &unbounded,
		SandboxMemoryOvercommit: &unbounded,
	}); err != nil {
		t.Fatalf("unbounding cpu and memory overcommit: %v", err)
	} else if b := got.SandboxCommitment.Budget; b == nil || !b.DiskBounded || b.DiskOvercommit != 10 {
		t.Errorf("disk-only budget = %+v, want disk bounded at 10x even when cpu and memory are unbounded", b)
	}
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxDiskOvercommit: &unbounded}); err != nil {
		t.Errorf("saving a zero disk overcommit: %v -- zero is 'held by the floor alone', not an error", err)
	}
	negative := -1.0
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxDiskOvercommit: &negative}); err == nil {
		t.Error("a negative disk overcommit was accepted, want it refused")
	}
}

// A reserve at or above what the host has leaves a memory budget of
// nothing: the deployment goes on running, one sandbox at a time, with
// everything else queued behind a budget that can never have room
// (model.Budget.Admits' Sole rule). Refused where this process can speak
// for a host, which is the only place the number can be judged.
func TestSettingsRefusesAReserveLargerThanTheHost(t *testing.T) {
	t.Parallel()
	c, ctx := settingsClientOnHost(t, budgetHost, nil)
	whole := budgetHost.MemoryMB
	_, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxMemoryReserveMB: &whole})
	if err == nil {
		t.Fatal("a reserve of the whole host was accepted, want it refused")
	}
	if !strings.Contains(err.Error(), "131072") {
		t.Errorf("refusal = %q, want it naming the host's own MiB so an operator can pick a number", err)
	}
	// Grain's own reserve is a negative number, and has to stay
	// settable: it is how an operator gets back to "an eighth of this
	// machine" after choosing a figure.
	def := model.ReserveMemoryDefault
	if _, err := c.UpdateSettings(ctx, ui.UpdateSettingsRequest{SandboxMemoryReserveMB: &def}); err != nil {
		t.Fatalf("saving grain's own reserve: %v", err)
	}
}

// The arithmetic beside the fields: what this deployment promises and
// what it keeps back for the host itself -- the budget model.Limits hands
// the gate and dispatch alike, so the pane and the admission cannot
// disagree. There is no pool's share in it: both pools are admitted
// against the whole of this (grain/task-28).
func TestSandboxCommitmentReportsTheBudget(t *testing.T) {
	t.Parallel()
	got := ui.NewSandboxCommitment(model.Config{
		MaxWorkers: 3, MaxMergers: 1, MaxCIJobs: 15,
		SandboxCPUs: 2, SandboxMemoryMB: 8192,
		SandboxCPUOvercommit: 2, SandboxMemoryOvercommit: 1,
		SandboxMemoryReserveMB: 16 * 1024,
	}, budgetHost)
	if got == nil || got.Budget == nil {
		t.Fatalf("commitment = %+v, want one carrying this deployment's budget", got)
	}
	b := got.Budget
	if !b.CPUsBounded || b.CPUs != 60 {
		t.Errorf("cpu budget = %v (bounded %t), want 60: 32 host vCPU less 2 reserved, at 2x", b.CPUs, b.CPUsBounded)
	}
	if !b.MemoryBounded || b.MemoryMB != 112*1024 {
		t.Errorf("memory budget = %d MiB (bounded %t), want %d: 128 GiB less the 16 reserved",
			b.MemoryMB, b.MemoryBounded, 112*1024)
	}
	if b.ReserveMemoryMB != 16*1024 || b.ReserveIsDefault {
		t.Errorf("reserve = %d MiB (grain's own: %t), want the 16 GiB this deployment chose",
			b.ReserveMemoryMB, b.ReserveIsDefault)
	}
	// The whole budget is every pool's: nothing is held back for agents,
	// and a summary that named a reservation would be describing a rule
	// this deployment no longer has.
	// 3 workers + 1 merger + 15 CI slots, at 8 GiB each.
	if got.MemoryMBCommitted != 19*8192 {
		t.Errorf("committed memory = %d MiB, want the %d MiB its slots commit at this shape",
			got.MemoryMBCommitted, 19*8192)
	}
	summary := got.Summary()
	if !strings.Contains(summary, "sharing a 60 vCPU / 112.0 GiB sandbox budget") {
		t.Errorf("Summary = %q, want the one budget both pools share", summary)
	}
	for _, unwanted := range []string{"reserved for agent runs", "left of this deployment"} {
		if strings.Contains(summary, unwanted) {
			t.Errorf("Summary still describes a reservation: %q", summary)
		}
	}
	// A deployment that has chosen no reserve says so, and reports what
	// grain's own rule came to on this host: an eighth of 128 GiB.
	def := ui.NewSandboxCommitment(model.Config{
		MaxWorkers: 3, SandboxCPUs: 2, SandboxMemoryMB: 8192,
		SandboxCPUOvercommit: 2, SandboxMemoryOvercommit: 1,
		SandboxMemoryReserveMB: model.ReserveMemoryDefault,
	}, budgetHost)
	if def.Budget == nil || !def.Budget.ReserveIsDefault || def.Budget.ReserveMemoryMB != 16*1024 {
		t.Errorf("unset reserve = %+v, want grain's own eighth of this host resolved to 16 GiB", def.Budget)
	}
}

// A CI job names its own sandbox in its workflow file -- on this
// repository, 16 GiB against an 8 GiB agent sandbox -- so a count of CI
// jobs times the agent shape was never a bound on anything. What the pane
// reports instead is the budget both pools are admitted against, which is
// the figure the gate uses, and what is left for a CI job is whatever the
// live sandboxes have not taken.
func TestSandboxCommitmentReportsTheWholeBudgetCIIsAdmittedAgainst(t *testing.T) {
	t.Parallel()
	// The host grain/task-232 measured: 28 cores, 128 GiB, agent
	// sandboxes of 4 vCPU / 8 GiB.
	host := ui.HostCapacity{CPUs: 28, MemoryMB: 128 * 1024}
	cfg := model.Config{
		MaxWorkers: 3, MaxMergers: 1, MaxCIJobs: 15,
		SandboxCPUs: 4, SandboxMemoryMB: 8192,
		SandboxCPUOvercommit: model.DefaultCPUOvercommit, SandboxMemoryOvercommit: model.DefaultMemoryOvercommit,
		SandboxMemoryReserveMB: model.ReserveMemoryDefault,
	}
	got := ui.NewSandboxCommitment(cfg, host)
	if got == nil || got.Budget == nil {
		t.Fatalf("commitment = %+v, want one carrying this deployment's budget", got)
	}

	budget := model.NewBudget(model.Commitment{CPUs: host.CPUs, MemoryMB: host.MemoryMB},
		cfg.SandboxMemoryReserveMB, cfg.SandboxCPUOvercommit, cfg.SandboxMemoryOvercommit)
	wantMemoryMB, _ := budget.MemoryBudgetMB()
	if got.Budget.MemoryMB != wantMemoryMB {
		t.Errorf("the budget on the pane = %d MiB, want the %d MiB its gate admits against",
			got.Budget.MemoryMB, wantMemoryMB)
	}
	// And emphatically not the product it used to be: fifteen CI jobs at
	// the agent's own 8 GiB.
	if got.Budget.MemoryMB == cfg.MaxCIJobs*cfg.SandboxMemoryMB {
		t.Errorf("the budget = %d MiB, which is max_ci_jobs times the *agent* shape", got.Budget.MemoryMB)
	}

	// Read as jobs of the shape the workflows actually name, against an
	// empty host: this budget holds seven 16 GiB CI jobs, and holds fewer
	// as agent runs take their share of it -- rather than a fixed slice
	// reserved for one pool or the other.
	ciShape := model.Commitment{CPUs: 4, MemoryMB: 16 * 1024}
	count := func(live model.Commitment) int {
		n := 0
		for i := 0; i < 32; i++ {
			if v := budget.Admits(live, ciShape); !v.OK {
				break
			}
			n, live = n+1, live.Plus(ciShape)
		}
		return n
	}
	if got := count(model.Commitment{}); got != 7 {
		t.Errorf("an empty host runs %d jobs of %s, want 7", got, ciShape)
	}
	agents := model.Commitment{CPUs: 4, MemoryMB: 8192}.Times(5)
	if got := count(agents); got != 4 {
		t.Errorf("with five agent sandboxes live this host runs %d jobs of %s, want 4", got, ciShape)
	}
}

// With no budget bounding a dimension -- both ratios at zero, which is
// "the run counts are the whole limit" -- the commitment keeps the
// arithmetic it has always had, and says nothing about a budget that
// bounds nothing.
func TestSandboxCommitmentWithoutABudgetCountsCIJobsAsBefore(t *testing.T) {
	t.Parallel()
	got := ui.NewSandboxCommitment(model.Config{
		MaxWorkers: 1, MaxCIJobs: 4,
		SandboxCPUs: 2, SandboxMemoryMB: 8192,
	}, budgetHost)
	if got == nil || got.Budget != nil {
		t.Fatalf("commitment = %+v, want one with no budget to report", got)
	}
	// 1 worker + 4 CI jobs, each at the shape.
	if got.CPUsCommitted != 10 || got.MemoryMBCommitted != 5*8192 {
		t.Errorf("committed %d vCPU / %d MiB, want 10 / %d", got.CPUsCommitted, got.MemoryMBCommitted, 5*8192)
	}
}
