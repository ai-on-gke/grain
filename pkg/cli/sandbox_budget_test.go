package cli

// sandbox_budget_test.go covers the deployment's half of grain/task-173
// and grain/task-28: that the shapes this deployment builds are held
// against what this machine actually has, that both pools are held
// against the *same* budget by the same live ledger rather than one
// reserving against the other, and that a deployment with no host reading
// bounds nothing at all.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/granule"
	"github.com/ai-on-gke/grain/pkg/model"
)

// The host the overcommit was measured on: 28 vCPU, 128 GiB, agent
// sandboxes of 4 vCPU / 8 GiB, and CI jobs asking for 16 GiB each.
func budgetedConfig() daemon.Config {
	return daemon.Config{
		MaxWorkers: 3, MaxMergers: 2, MaxCIJobs: 15, Ci: true,
		SandboxCPUs: 4, SandboxMemoryMB: 8192,
		HostCPUs: 28, HostMemoryMB: 128 * 1024,
		CpuOvercommit: model.DefaultCPUOvercommit, MemoryOvercommit: model.DefaultMemoryOvercommit,
		MemoryReserveMB: -1,
		Ledger:          &model.Ledger{},
	}
}

func TestConfigBudgetHoldsBackAReserveAndHonoursTheOvercommitRatios(t *testing.T) {
	cfg := budgetedConfig()
	budget := cfg.Budget()
	memMB, ok := budget.MemoryBudgetMB()
	if !ok {
		t.Fatal("a host reading bounds memory")
	}
	if memMB != 112*1024 {
		t.Errorf("128 GiB less an eighth reserved, at 1x: got %d MiB", memMB)
	}
	cpus, ok := budget.CPUBudget()
	if !ok || cpus != 52 {
		t.Errorf("28 vCPU less 2 reserved, at 2x: got %v (bounded %v)", cpus, ok)
	}

	// An operator who wants more than the machine has says so, and the
	// reserve survives it: the ratio multiplies what is left over, not
	// the machine.
	cfg.MemoryOvercommit = 2
	doubled, _ := cfg.Budget().MemoryBudgetMB()
	if doubled != 224*1024 {
		t.Errorf("(128 - 16) GiB at 2x: got %d MiB", doubled)
	}

	// And one who wants none at all gets the counts back, rather than a
	// budget that refuses everything.
	cfg.MemoryOvercommit, cfg.CpuOvercommit = 0, 0
	if cfg.Budget().Known() {
		t.Error("ratios of 0 leave both dimensions unenforced")
	}
}

func TestConfigAgentShapeFallsBackToTheShapeTheBackendWouldApply(t *testing.T) {
	cfg := budgetedConfig()
	if got := cfg.AgentShape(); got != (model.Commitment{CPUs: 4, MemoryMB: 8192, DiskGB: granule.DefaultDiskGB}) {
		t.Errorf("the deployment-wide shape: %+v", got)
	}
	// A deployment that has chosen no shape still commits one: the
	// backend gives every run the default, and charging it nothing is
	// the arithmetic this budget exists to fix.
	cfg.SandboxCPUs, cfg.SandboxMemoryMB = 0, 0
	if got := cfg.AgentShape(); got != (model.Commitment{CPUs: granule.DefaultCPUs, MemoryMB: granule.DefaultMemoryMB, DiskGB: granule.DefaultDiskGB}) {
		t.Errorf("an unset shape resolves to the backend's own default: %+v", got)
	}
	// Except where the backend drops the shape on the floor, in which
	// case there is no per-run figure to add up at all (host sandboxing,
	// ui.NewUncappedSandboxCommitment).
	cfg.SandboxShapeIgnored = true
	if got := cfg.AgentShape(); !got.IsZero() {
		t.Errorf("a backend that applies no shape commits none: %+v", got)
	}
}

// One budget, both pools, and the ledger they share: an agent run and a
// CI job take the machine out of the same total, and an idle slot takes
// nothing.
func TestBothPoolsAreAdmittedAgainstOneBudget(t *testing.T) {
	cfg := budgetedConfig()
	cfg.Ledger = &model.Ledger{}
	limits := cfg.Limits()

	// 112 GiB of budget. Five agent sandboxes at 8 GiB take 40 of it, and
	// this repository's 16 GiB CI jobs get the other 72 -- four of them,
	// with the fifth refused, however high max_ci_jobs may be.
	for i := 0; i < 5; i++ {
		if v, _ := limits.ChargeSandbox(fmt.Sprintf("run-%d", i)); !v.OK {
			t.Fatalf("agent sandbox %d of 5: %q", i, v.Reason)
		}
	}
	job := model.Commitment{CPUs: 4, MemoryMB: 16 * 1024}
	admitted := 0
	for i := 0; i < 15; i++ {
		if v, _ := cfg.Ledger.Admit(limits.Budget, model.PoolCI, fmt.Sprintf("job-%d", i), job); !v.OK {
			break
		}
		admitted++
	}
	if admitted != 4 {
		t.Errorf("16 GiB jobs admitted beside 40 GiB of agent sandboxes: got %d, want 4", admitted)
	}
	if live := cfg.Ledger.Live(); live.MemoryMB > 112*1024 {
		t.Errorf("everything admitted at once fits the budget: %s committed", live)
	}

	// And the thing a standing reservation could not do: when the agent
	// sandboxes end, the capacity is CI's.
	for i := 0; i < 5; i++ {
		cfg.Ledger.Release(model.PoolAgent, fmt.Sprintf("run-%d", i))
	}
	if v, _ := cfg.Ledger.Admit(limits.Budget, model.PoolCI, "job-5", job); !v.OK {
		t.Errorf("with the agent sandboxes gone the fifth job fits: %q", v.Reason)
	}

	// Worker and merger counts are carried on Limits for dispatch.Cycle,
	// while MaxCIJobs has no effect and the budget itself is untouched by
	// any count.
	cfg.MaxWorkers, cfg.MaxMergers, cfg.MaxCIJobs = 300, 300, 300
	if got := cfg.Limits(); got.Workers != 300 || got.Mergers != 300 || got.CIJobs != 0 || got.Budget != limits.Budget {
		t.Errorf("updated limits = %+v", got)
	}
}

// And the gate the engine is actually given asks that same question of
// the same budget and the same ledger, re-read per attempt.
func TestTheCIGateIsBuiltWithTheDeploymentsBudgetAndLedger(t *testing.T) {
	cfg := budgetedConfig()
	live := cfg
	svc := daemon.BuildCIService(context.Background(), cfg, func() model.Limits { return live.Limits() }, nil, nil,
		nil, &fakeSandboxBackend{sandbox: &fakeSandbox{}}, nil, "http://127.0.0.1:9999",
		nil, nil, nil, nil, nil, nil, nil, nil, nil)
	gate, ok := svc.Engine.Jobs.(*ci.SharedGate)
	if !ok {
		t.Fatalf("the engine has no shared gate: %T", svc.Engine.Jobs)
	}
	if gate.Budget == nil || gate.Default == nil || gate.Ledger == nil {
		t.Fatal("the deployment's gate rations the machine as well as the count")
	}
	// The whole budget, with nothing held back for anybody: 128 GiB less
	// an eighth reserved for the host itself, at 1x.
	memMB, bounded := gate.Budget().MemoryBudgetMB()
	if !bounded || memMB != 112*1024 {
		t.Errorf("the gate is given this deployment's whole sandbox budget: %d MiB (bounded %v)", memMB, bounded)
	}
	if gate.Ledger() != cfg.Ledger {
		t.Error("the gate charges a different ledger from the one dispatch charges")
	}
	if got := gate.Default(); got != (model.Commitment{CPUs: 4, MemoryMB: 8192, DiskGB: granule.DefaultDiskGB}) {
		t.Errorf("a job with no shape of its own is charged the deployment's: %+v", got)
	}
	// Live, not pinned: a budget resized in Settings under a running
	// daemon is honoured by the next job that asks -- and the legacy
	// counts move nothing.
	live.MaxWorkers = 9
	if memMB, _ := gate.Budget().MemoryBudgetMB(); memMB != 112*1024 {
		t.Errorf("a legacy count moved the gate's budget: %d MiB", memMB)
	}
	live.MemoryOvercommit = 2
	if memMB, _ := gate.Budget().MemoryBudgetMB(); memMB != 224*1024 {
		t.Errorf("the gate re-reads the budget's own settings: %d MiB", memMB)
	}
}

// A deployment whose host cannot be counted is bounded by nothing
// deployment-wide -- not by a budget of nothing, which would refuse every
// sandbox. Its teams' own counts are then the whole rule.
func TestNoHostReadingLeavesTheTeamsAsTheWholeRule(t *testing.T) {
	cfg := budgetedConfig()
	cfg.Ledger = &model.Ledger{}
	cfg.HostCPUs, cfg.HostMemoryMB = 0, 0
	limits := cfg.Limits()
	if limits.Budget.Known() {
		t.Fatal("no host reading bounds nothing")
	}
	if !limits.SandboxAdmits() {
		t.Error("an unknown budget admits rather than refusing")
	}
	if v := limits.Budget.Admits(model.Commitment{MemoryMB: 1 << 20}, model.Commitment{MemoryMB: 1 << 20}); !v.OK {
		t.Errorf("CI is admitted against the same unknown budget: %q", v.Reason)
	}
	// And a team's counts still bound that team.
	team := model.Limits{Workers: 3, Mergers: 1}
	if team.Admits(model.RunCounts{Workers: 3, Mergers: 1}, true) {
		t.Error("a team at Workers+Mergers admits nothing more")
	}
}

// Kubernetes has no host reading either, for a reason that is not a
// failure -- sandboxHostCapacity declines on both its backends because a
// pod's node is not this daemon's own machine. The two startup messages
// that say so have to say that, specifically, rather than the wording a
// host backend uses when sysstat actually failed: that wording reads as
// a gap an operator should go close, and there is no setting here to
// close.
func TestBudgetSentenceNamesTheClusterSchedulerUnderKubernetes(t *testing.T) {
	cfg := budgetedConfig()
	cfg.HostCPUs, cfg.HostMemoryMB = 0, 0
	limits := cfg.Limits()
	if limits.Budget.Known() {
		t.Fatal("no host reading bounds nothing")
	}

	plain := daemon.BudgetSentence(limits, false)
	if !strings.Contains(plain, "nothing here knows how big this host is") {
		t.Errorf("a true unreadable host keeps its own wording: %q", plain)
	}
	if strings.Contains(plain, "scheduler") {
		t.Errorf("a non-Kubernetes deployment has no cluster scheduler to name: %q", plain)
	}

	k8s := daemon.BudgetSentence(limits, true)
	if !strings.Contains(k8s, "scheduler") {
		t.Errorf("Kubernetes mode should name the cluster scheduler as what actually bounds it: %q", k8s)
	}
	if strings.Contains(k8s, "could not be read") {
		t.Errorf("Kubernetes mode should not read as a failed reading: %q", k8s)
	}
}

func TestLogSandboxBudgetUnderKubernetesNamesTheSchedulerNotAFailedReading(t *testing.T) {
	cfg := budgetedConfig()
	cfg.HostCPUs, cfg.HostMemoryMB = 0, 0
	cfg.K8sSandboxes = true

	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	daemon.LogSandboxBudget(cfg)

	got := logs.String()
	if !strings.Contains(got, "cluster's own scheduler") {
		t.Errorf("logSandboxBudget under Kubernetes = %q, want it to name the cluster scheduler", got)
	}
	if strings.Contains(got, "could not be read") {
		t.Errorf("logSandboxBudget under Kubernetes = %q, should not read as a failed host reading -- "+
			"there is no host for this backend to read in the first place", got)
	}
}
