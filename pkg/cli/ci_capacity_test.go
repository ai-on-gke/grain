package cli

// ci_capacity_test.go covers the deployment's half of grain/task-296:
// that CI and dispatch each honor their independently configured
// capacity, that CI's is re-read rather than pinned at
// startup, and that a CI job's sandbox is one the operator's own
// sandbox-health pane counts -- so the number that pane reports is the
// number of sandboxes that exist.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// The legacy grain_config counts reach nothing this deployment schedules
// on: a row saying 4 workers, 2 mergers and 2 CI jobs, or one saying 300
// of each, produces the same limits (grain/task-28).
func TestStoredCountsHaveNoSchedulingEffect(t *testing.T) {
	t.Parallel()
	modest := daemon.Config{MaxWorkers: 4, MaxMergers: 2, MaxCIJobs: 2, Ci: true, Ledger: &model.Ledger{}}
	legacy := modest
	legacy.MaxWorkers, legacy.MaxMergers, legacy.MaxCIJobs = 300, 300, 300

	for _, cfg := range []daemon.Config{modest, legacy} {
		limits := cfg.Limits()
		if limits.Workers != cfg.MaxWorkers || limits.Mergers != cfg.MaxMergers || limits.CIJobs != 0 {
			t.Errorf("limits = %+v, want Workers=%d Mergers=%d CIJobs=0", limits, cfg.MaxWorkers, cfg.MaxMergers)
		}
		if limits.Unlimited() {
			t.Error("with configured worker/merger counts, the deployment's limits bound agent runs")
		}
		if limits.AgentRuns() != cfg.MaxWorkers+cfg.MaxMergers || limits.CILimit() != 0 {
			t.Errorf("agent and CI ceilings: got %d, %d; want %d, 0", limits.AgentRuns(), limits.CILimit(), cfg.MaxWorkers+cfg.MaxMergers)
		}
		if limits.Budget.Known() {
			t.Errorf("a config with no host reading bounds nothing: %+v", limits.Budget)
		}
		// And the one thing the deployment does bound is untouched by the
		// counts: an empty ledger against an unknown budget admits.
		if !limits.SandboxAdmits() {
			t.Error("a deployment with nothing live admits a sandbox")
		}
	}

	// -ci=false changes nothing here either. It used to zero CI's count so
	// the agent pool would not reserve capacity for an engine that did not
	// exist; nothing is reserved for anything now.
	off := modest
	off.Ci = false
	if off.Limits() != modest.Limits() {
		t.Errorf("-ci=false changed the deployment's limits: %+v vs %+v", off.Limits(), modest.Limits())
	}
}

// The budget and the ledger are what the deployment does bound, and one
// value carries both to everything that rations a sandbox.
func TestConfigLimitsCarryOneBudgetAndOneLedger(t *testing.T) {
	t.Parallel()
	ledger := &model.Ledger{}
	cfg := daemon.Config{Ci: true, Ledger: ledger, HostCPUs: 16, HostMemoryMB: 32 * 1024,
		CpuOvercommit: 1, MemoryOvercommit: 1, SandboxCPUs: 4, SandboxMemoryMB: 8 * 1024}
	limits := cfg.Limits()
	if limits.Ledger != ledger {
		t.Fatal("the deployment's limits carry its own ledger")
	}
	if !limits.Budget.Known() {
		t.Fatal("a host reading gives the deployment a budget")
	}
	// What is live moves the verdict; what is configured does not.
	if _, release := limits.ChargeSandbox("run-1"); release == nil {
		t.Fatal("the first sandbox is admitted")
	}
	if got := cfg.Limits().LiveCommitment(); got.CPUs == 0 {
		t.Errorf("a live sandbox is committed against the budget: %s", got)
	}
}

// The gate the engine is given has to read the pool as it stands, not as
// it stood when the daemon started: -max-workers is live-editable in
// Settings (liveConfig), and a gate holding CI to a number this process
// was started with would be rationing a deployment that no longer
// exists.
func TestTheCIEngineIsGatedOnThePoolAsItStandsNow(t *testing.T) {
	t.Parallel()
	cfg := daemon.Config{MaxWorkers: 4, MaxMergers: 2, MaxCIJobs: 2, Ci: true, Ledger: &model.Ledger{}}
	live := cfg
	svc := daemon.BuildCIService(context.Background(), cfg, func() model.Limits { return live.Limits() }, nil, nil,
		nil, &fakeSandboxBackend{sandbox: &fakeSandbox{}}, nil, "http://127.0.0.1:9999", nil, nil, nil, nil, nil, nil, nil, nil, nil)

	gate, ok := svc.Engine.Jobs.(*ci.SharedGate)
	if !ok {
		t.Fatalf("the engine has no shared gate: %T", svc.Engine.Jobs)
	}
	// No count at all (grain/task-28), and no team has one of its own:
	// this gate has no Limit to read.
	if gate.Limit != nil {
		t.Errorf("the deployment's gate imposes a count of its own: %d", gate.Limit())
	}
	if svc.Engine.ConcurrencyLimit == nil {
		t.Fatal("one run's width is not read from anywhere")
	}
	if got := svc.Engine.ConcurrencyLimit(); got != 0 {
		t.Errorf("a deployment that sets no per-run limit leaves a run at pkg/ci's own default width: %d", got)
	}

	// And the legacy stored counts move none of it.
	live.MaxWorkers, live.MaxMergers, live.MaxCIJobs = 8, 2, 40
	if gate.Limit != nil {
		t.Error("a stored max_ci_jobs gave the deployment's gate a count again")
	}
	if got := gate.Budget(); got.Known() {
		t.Errorf("this configuration has no host reading, so no budget: %+v", got)
	}
	if gate.Ledger == nil || gate.Ledger() == nil {
		t.Error("the deployment's gate charges no ledger")
	}
}

// The same property for the other two live numbers the gate is handed:
// how fast a queueing job's rank rises, and how long the budget has to
// have been refusing one before capacity is held for it
// (model.Limits.CIAgeBoost and CIReserveAfter, grain/task-325). A job
// that has been queueing for half an hour is exactly the job an operator
// shortening the boost is trying to rescue, so the gate has to ask the
// configuration as it stands rather than as this process started.
func TestTheCIGateReadsItsOrderingWindowsAsTheyStandNow(t *testing.T) {
	t.Parallel()
	cfg := daemon.Config{MaxWorkers: 4, MaxMergers: 2, MaxCIJobs: 2, Ci: true, Ledger: &model.Ledger{}}
	live := cfg
	svc := daemon.BuildCIService(context.Background(), cfg, func() model.Limits { return live.Limits() }, nil, nil,
		nil, &fakeSandboxBackend{sandbox: &fakeSandbox{}}, nil, "http://127.0.0.1:9999", nil, nil, nil, nil, nil, nil, nil, nil, nil)

	gate, ok := svc.Engine.Jobs.(*ci.SharedGate)
	if !ok {
		t.Fatalf("the engine has no shared gate: %T", svc.Engine.Jobs)
	}
	// A deployment that has chosen neither passes zero rather than a
	// number of its own: what an unconfigured gate runs is pkg/ci's
	// constant, resolved there beside the rule it belongs to.
	if got := gate.AgeBoost(); got != 0 {
		t.Errorf("the age boost of a deployment that has chosen none = %v, want 0 (ci.DefaultAgeBoost)", got)
	}
	if got := gate.ReserveAfter(); got != 0 {
		t.Errorf("the reservation window of a deployment that has chosen none = %v, want 0 "+
			"(ci.DefaultReserveAfter)", got)
	}

	// A configuration row arriving under the running daemon -- an
	// operator saving the Settings pane, or a merged change to the state
	// repository -- which is what liveConfig.refresh does with it.
	live = live.WithModelConfig(model.Config{
		MaxCIJobs:      2,
		CIAgeBoost:     30 * time.Second,
		CIReserveAfter: 5 * time.Minute,
	})
	if got := gate.AgeBoost(); got != 30*time.Second {
		t.Errorf("the age boost after the stored one changed = %v, want 30s", got)
	}
	if got := gate.ReserveAfter(); got != 5*time.Minute {
		t.Errorf("the reservation window after the stored one changed = %v, want 5m", got)
	}
	// Including the reading that turns reservation off, which is a choice
	// an operator may make and not a value to fall back from.
	live.CiReserveAfter = -1
	if got := gate.ReserveAfter(); got != -1 {
		t.Errorf("a reservation window turned off = %v, want it passed through as -1", got)
	}
}

func TestTheCIEngineDefaultsToNoPerRunLimitAndHonorsConfiguredCIJobsPerRun(t *testing.T) {
	t.Parallel()
	cfg := daemon.Config{
		DataDir: t.TempDir(),
		Ci:      true, Ledger: &model.Ledger{},
		HostCPUs: 64, HostMemoryMB: 128 * 1024,
		CpuOvercommit: 2, MemoryOvercommit: 2,
		SandboxCPUs: 2, SandboxMemoryMB: 4 * 1024,
	}
	live := cfg
	svc := daemon.BuildCIService(context.Background(), cfg, func() model.Limits { return live.Limits() }, nil, nil,
		nil, &fakeSandboxBackend{sandbox: &fakeSandbox{}}, nil, "http://127.0.0.1:9999", nil, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.Engine.Cache = nil

	if got := svc.Engine.ConcurrencyLimit(); got != 0 {
		t.Fatalf("default per-run ConcurrencyLimit = %d, want 0 (unlimited)", got)
	}

	// Configuring CIJobsPerRun via model.Config updates ConcurrencyLimit live.
	live = live.WithModelConfig(model.Config{CIJobsPerRun: 6})
	if got := svc.Engine.ConcurrencyLimit(); got != 6 {
		t.Fatalf("ConcurrencyLimit after setting CIJobsPerRun=6 = %d, want 6", got)
	}

	// Clearing CIJobsPerRun back to 0 restores unlimited per-run concurrency.
	live = live.WithModelConfig(model.Config{CIJobsPerRun: 0})
	if got := svc.Engine.ConcurrencyLimit(); got != 0 {
		t.Fatalf("ConcurrencyLimit after clearing CIJobsPerRun = %d, want 0", got)
	}

	// Verify an actual 6-job run on svc.Engine runs all 6 jobs concurrently
	// without WaitForRun when CIJobsPerRun is 0, and caps at 3 with WaitForRun
	// when CIJobsPerRun is 3.
	wf, err := ci.Parse(".grain/workflows/six.yml", []byte("on: push\njobs:\n"+
		"  a:\n    steps:\n      - run: a\n"+
		"  b:\n    steps:\n      - run: b\n"+
		"  c:\n    steps:\n      - run: c\n"+
		"  d:\n    steps:\n      - run: d\n"+
		"  e:\n    steps:\n      - run: e\n"+
		"  f:\n    steps:\n      - run: f\n"))
	if err != nil {
		t.Fatalf("ParseWorkflow: %v", err)
	}
	plan, err := ci.PlanRun(ci.Request{Repo: "o/r", Ref: "main", Event: ci.EventPush}, []ci.Workflow{wf})
	if err != nil {
		t.Fatalf("PlanRun: %v", err)
	}

	sb := &blockingCISandboxes{block: make(chan struct{}), acquires: make(chan struct{}, 6)}
	svc.Engine.Sandboxes = sb
	var sawWaitForRun bool
	done := make(chan ci.Run, 1)
	go func() {
		done <- svc.Engine.Run(context.Background(), "run-unlimited", plan, func(job ci.JobRun) {
			if job.QueuedCause == ci.WaitForRun || (job.Activity != "" && job.QueuedCause == ci.WaitForRun) {
				sawWaitForRun = true
			}
		})
	}()
	for i := 0; i < 6; i++ {
		select {
		case <-sb.acquires:
		case <-time.After(5 * time.Second):
			t.Fatalf("with CIJobsPerRun=0, only %d/6 jobs started concurrently", i)
		}
	}
	close(sb.block)
	run := <-done
	if run.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("unlimited run conclusion = %q (%s)", run.Conclusion, run.Error)
	}
	if sb.peak != 6 {
		t.Errorf("unlimited run peak concurrent jobs = %d, want 6", sb.peak)
	}
	if sawWaitForRun {
		t.Error("unlimited run reported WaitForRun")
	}
	for _, j := range run.Jobs {
		if j.QueuedCause == ci.WaitForRun {
			t.Errorf("job %s had QueuedCause = %q, want no WaitForRun", j.JobID, j.QueuedCause)
		}
	}

	// Now set CIJobsPerRun = 3 and verify the engine caps the run at 3 and reports WaitForRun.
	live = live.WithModelConfig(model.Config{CIJobsPerRun: 3})
	sbLimited := &blockingCISandboxes{block: make(chan struct{}), acquires: make(chan struct{}, 6)}
	svc.Engine.Sandboxes = sbLimited
	waitReported := make(chan struct{}, 6)
	doneLimited := make(chan ci.Run, 1)
	go func() {
		doneLimited <- svc.Engine.Run(context.Background(), "run-limited", plan, func(job ci.JobRun) {
			if job.Activity != "" {
				select {
				case waitReported <- struct{}{}:
				default:
				}
			}
		})
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-sbLimited.acquires:
		case <-time.After(5 * time.Second):
			t.Fatalf("with CIJobsPerRun=3, only %d/3 jobs started concurrently", i)
		}
	}
	select {
	case <-waitReported:
	case <-time.After(5 * time.Second):
		t.Fatal("with CIJobsPerRun=3, waiting jobs never reported WaitForRun")
	}
	close(sbLimited.block)
	runLimited := <-doneLimited
	if runLimited.Conclusion != ci.ConclusionSuccess {
		t.Fatalf("limited run conclusion = %q (%s)", runLimited.Conclusion, runLimited.Error)
	}
	if sbLimited.peak > 3 {
		t.Errorf("with CIJobsPerRun=3, peak concurrent jobs = %d, want <= 3", sbLimited.peak)
	}
}

// CI's jobs are built by this deployment's own sandbox backend -- the
// same value runDaemon hands ui.Config.Sandboxes through
// sandboxHealthAdapter -- which is what makes a CI job's sandbox
// something the System pane counts rather than a machine nothing in the
// UI knows exists.
func TestACIJobsSandboxIsOneTheHealthPaneCounts(t *testing.T) {
	t.Parallel()
	backend := orchestrator.NewHostSandboxes(t.TempDir())
	cfg := daemon.Config{MaxWorkers: 2, MaxMergers: 1, MaxCIJobs: 1, Ci: true, Ledger: &model.Ledger{}}
	svc := daemon.BuildCIService(context.Background(), cfg, cfg.Limits, nil, nil, nil, backend, nil,
		"http://127.0.0.1:9999", nil, nil, nil, nil, nil, nil, nil, nil, nil)

	// Under StateCheckSandboxes, which answers only the configuration
	// repository's check itself and hands every other job on.
	builtins, ok := svc.Engine.Sandboxes.(daemon.StateCheckSandboxes)
	if !ok {
		t.Fatalf("the engine's sandboxes do not answer the config check in the daemon: %T", svc.Engine.Sandboxes)
	}
	adapter, ok := builtins.Sandboxes.(daemon.CiSandboxes)
	if !ok {
		t.Fatalf("the engine's sandboxes are not the deployment's: %T", builtins.Sandboxes)
	}
	if adapter.Sandboxes != orchestrator.Sandboxes(backend) {
		t.Fatal("a CI job would be built somewhere the health pane cannot see")
	}

	ctx := context.Background()
	run, err := backend.Acquire(ctx, "17-1", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("acquiring an agent run's sandbox: %v", err)
	}
	defer run.Release(ctx)
	job, err := adapter.Sandboxes.Acquire(ctx, "ci-abc123-grain-yml-test", orchestrator.Shape{})
	if err != nil {
		t.Fatalf("acquiring a CI job's sandbox: %v", err)
	}
	defer job.Release(ctx)

	got := daemon.SandboxHealthAdapter{Inner: backend}.Health(ctx)
	if len(got) != 2 {
		t.Fatalf("the pane reports %d sandbox(es) with two live: %+v", len(got), got)
	}
	var sawJob bool
	for _, s := range got {
		if s.Sandbox == "ci-abc123-grain-yml-test" {
			sawJob = true
		}
	}
	if !sawJob {
		t.Errorf("the CI job's sandbox is missing from the pane: %+v", got)
	}
}

type blockingCISandboxes struct {
	mu       sync.Mutex
	live     int
	peak     int
	block    chan struct{}
	acquires chan struct{}
}

func (b *blockingCISandboxes) Acquire(ctx context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	b.mu.Lock()
	b.live++
	if b.live > b.peak {
		b.peak = b.live
	}
	b.mu.Unlock()
	if b.acquires != nil {
		b.acquires <- struct{}{}
	}
	return &blockingCISandbox{parent: b, name: req.Name}, nil
}

type blockingCISandbox struct {
	parent *blockingCISandboxes
	name   string
	once   sync.Once
}

func (s *blockingCISandbox) Name() string { return s.name }

func (s *blockingCISandbox) Exec(ctx context.Context, cmd ci.Command) (ci.CommandResult, error) {
	if s.parent.block != nil {
		select {
		case <-s.parent.block:
		case <-ctx.Done():
			return ci.CommandResult{}, ctx.Err()
		}
	}
	return ci.CommandResult{ExitCode: 0}, nil
}

func (s *blockingCISandbox) Release(context.Context) error {
	s.once.Do(func() {
		s.parent.mu.Lock()
		s.parent.live--
		s.parent.mu.Unlock()
	})
	return nil
}

// End-to-end daemon/dispatch/CI integration for shared sandbox budget contention
// (d21320e92, daf546150):
//  1. A live agent run dispatched by dispatch.Cycle holds 12 vCPU / 24 GiB of a
//     16 vCPU / 32 GiB host budget.
//  2. A second ready agent task (12 vCPU / 24 GiB) cannot fit in the remaining
//     4 vCPU / 8 GiB, so dispatch.Cycle records a QuotaWaitCapability GrantWait
//     on the task and sets AgentWaiting + AgentShape on the shared Ledger.
//  3. An unrelated 2 vCPU / 4 GiB CI job fits the remaining 4 vCPU / 8 GiB, but
//     BuildCIService's SharedGate holds it back with WaitForPriority
//     ("waiting behind a queued agent sandbox") so it cannot starve the queued
//     agent sandbox.
//  4. When the live agent run is blocked inside wait_for_checks waiting on its
//     own CI run (recorded via Observation.CI.RunID), BuildCIService's Stuck
//     hook (ciJobStuck) admits that CI run's job despite AgentWaiting() == true,
//     preventing deadlock.
//  5. Once the first agent run releases its sandbox and dispatch.Cycle admits
//     the second agent task (clearing AgentWaiting), the held unrelated CI job
//     is admitted once budget permits and completes.
func TestSharedSandboxBudgetContentionHoldsCIJobsAndAdmitsStuckWaitForChecksJob(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("sqlite.Open: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		db.Close()
	})
	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("store.Init: %v", err)
	}

	ledger := &model.Ledger{}
	cfg := daemon.Config{
		DataDir:          t.TempDir(),
		Ci:               true,
		Ledger:           ledger,
		HostCPUs:         18, // 18 host vCPU less 2 reserved = 16 vCPU sandbox budget
		HostMemoryMB:     32 * 1024,
		CpuOvercommit:    1,
		MemoryOvercommit: 1,
		SandboxCPUs:      8,
		SandboxMemoryMB:  16 * 1024,
	}

	svc := daemon.BuildCIService(ctx, cfg, cfg.Limits, nil, nil, store,
		&fakeSandboxBackend{sandbox: &fakeSandbox{}}, nil, "http://127.0.0.1:9999",
		nil, nil, nil, nil, nil, nil, nil, nil, nil)
	svc.Engine.Cache = nil
	svc.Engine.Sandboxes = &blockingCISandboxes{}
	gate, ok := svc.Engine.Jobs.(*ci.SharedGate)
	if !ok {
		t.Fatalf("svc.Engine.Jobs = %T, want *ci.SharedGate", svc.Engine.Jobs)
	}
	gate.Poll = 5 * time.Millisecond

	who := model.Principal{Kind: model.PrincipalHuman, ID: "bsalmon"}
	for _, id := range []string{"task-1", "task-2"} {
		tk := model.Task{
			ID:       id,
			Intent:   model.IntentImplement,
			Title:    "Agent task " + id,
			Origin:   model.Origin{Attribution: model.Attribution{Actor: who}, Reason: model.ReasonDirect},
			Binding:  model.BindingDirective,
			Approval: &model.Attribution{Actor: who},
		}
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatalf("PutTask(%s): %v", id, err)
		}
	}

	// An existing CI job holds 8 vCPU / 16 GiB, so only 8 vCPU / 16 GiB is free.
	releaseCIHolder := ledger.Charge(model.PoolCI, "ci-holder-1", model.Commitment{CPUs: 8, MemoryMB: 16 * 1024})
	t.Cleanup(releaseCIHolder)

	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	dispatched, err := dispatch.Cycle(ctx, store, cfg.Limits(), now)
	if err != nil {
		t.Fatalf("dispatch.Cycle 1: %v", err)
	}
	if len(dispatched) != 1 || dispatched[0].TaskID != "task-1" {
		t.Fatalf("dispatch.Cycle 1 = %+v, want only task-1 dispatched", dispatched)
	}
	t.Cleanup(func() {
		ledger.Release(model.PoolAgent, dispatched[0].RunID)
	})

	// task-2 was held back by the shared sandbox budget: verify its QuotaWaitCapability
	// GrantWait and the ledger's AgentWaiting / AgentWaitShape state.
	wait, err := store.GrantWaitOf(ctx, "task-2")
	if err != nil {
		t.Fatalf("GrantWaitOf(task-2): %v", err)
	}
	if wait == nil || wait.Capability != model.QuotaWaitCapability {
		t.Fatalf("GrantWaitOf(task-2) = %+v, want QuotaWaitCapability", wait)
	}
	if !strings.Contains(wait.Resource, "32.0 GiB is committed") || !strings.Contains(wait.Resource, "32.0 GiB sandbox budget") {
		t.Errorf("GrantWaitOf(task-2).Resource = %q, want budget commitment details", wait.Resource)
	}
	if !ledger.AgentWaiting() {
		t.Fatal("ledger.AgentWaiting() = false, want true while task-2 waits for sandbox budget")
	}
	if got := ledger.AgentWaitShape(); got.CPUs != 8 || got.MemoryMB != 16*1024 {
		t.Fatalf("ledger.AgentWaitShape() = %+v, want 8 vCPU / 16 GiB", got)
	}

	// Release ci-holder-1 so 8 vCPU / 16 GiB is free again, while task-2 is still
	// queued and waiting for the next dispatch.Cycle.
	releaseCIHolder()

	wf, err := ci.Parse(".grain/workflows/check.yml", []byte("on: push\njobs:\n  test:\n    steps:\n      - run: echo ok\n"))
	if err != nil {
		t.Fatalf("ci.Parse: %v", err)
	}
	plan, err := ci.PlanRun(ci.Request{Repo: "acme/widgets", Ref: "main", Event: ci.EventPush}, []ci.Workflow{wf})
	if err != nil {
		t.Fatalf("ci.PlanRun: %v", err)
	}

	// Start an unrelated CI run: even though 8 vCPU / 16 GiB is free in the budget,
	// SharedGate holds it back with "waiting behind a queued agent sandbox" so it
	// cannot steal the freed capacity from task-2.
	heldBehindAgent := make(chan string, 1)
	unrelatedDone := make(chan ci.Run, 1)
	go func() {
		unrelatedDone <- svc.Engine.Run(ctx, "ci-run-unrelated", plan, func(job ci.JobRun) {
			if strings.Contains(job.Activity, "waiting behind a queued agent sandbox") && job.Sandbox == "" {
				select {
				case heldBehindAgent <- job.Activity:
				default:
				}
			}
		})
	}()

	select {
	case activity := <-heldBehindAgent:
		if !strings.Contains(activity, "waiting behind a queued agent sandbox") {
			t.Errorf("unrelated CI job activity = %q, want it to contain %q", activity, "waiting behind a queued agent sandbox")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for unrelated CI job to report 'waiting behind a queued agent sandbox'")
	}

	// Confirm the unrelated CI job has not completed while task-2 is still waiting.
	select {
	case r := <-unrelatedDone:
		t.Fatalf("unrelated CI run completed while agent sandbox was still waiting: %+v", r)
	case <-time.After(30 * time.Millisecond):
	}

	// Now dispatch.Cycle runs and dispatches task-2 into the freed 8 vCPU / 16 GiB slot,
	// clearing task-2's QuotaWaitCapability and clearing AgentWaiting on the ledger.
	dispatched2, err := dispatch.Cycle(ctx, store, cfg.Limits(), now.Add(time.Minute))
	if err != nil {
		t.Fatalf("dispatch.Cycle 2: %v", err)
	}
	if len(dispatched2) != 1 || dispatched2[0].TaskID != "task-2" {
		t.Fatalf("dispatch.Cycle 2 = %+v, want task-2 dispatched", dispatched2)
	}
	t.Cleanup(func() {
		ledger.Release(model.PoolAgent, dispatched2[0].RunID)
	})

	if waitAfter, err := store.GrantWaitOf(ctx, "task-2"); err != nil || waitAfter != nil {
		t.Fatalf("GrantWaitOf(task-2) after dispatch = (%+v, %v), want nil", waitAfter, err)
	}
	if ledger.AgentWaiting() {
		t.Fatal("ledger.AgentWaiting() = true after task-2 was dispatched, want false")
	}

	// Now task-1 and task-2 hold all 16 vCPU / 32 GiB of the budget, and both live
	// runs are blocked inside wait_for_checks on "ci-run-stuck". SharedGate.Stuck
	// (ciJobStuck) must break the deadlock and admit "ci-run-stuck" immediately!
	for _, id := range []string{"task-1", "task-2"} {
		if err := store.ObserveField(ctx, id, now.Add(time.Minute), func(o *model.Observation) {
			o.CI = &model.CIStatus{RunID: "ci-run-stuck"}
		}); err != nil {
			t.Fatalf("ObserveField(%s CI): %v", id, err)
		}
	}

	stuckDone := make(chan ci.Run, 1)
	go func() {
		stuckDone <- svc.Engine.Run(ctx, "ci-run-stuck", plan, nil)
	}()

	select {
	case stuckRun := <-stuckDone:
		if stuckRun.Conclusion != ci.ConclusionSuccess {
			t.Fatalf("stuck CI run conclusion = %q (error: %s), want success", stuckRun.Conclusion, stuckRun.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Stuck deadlock escape hatch to admit ci-run-stuck")
	}

	// Finally, finish task-1's run and release its 8 vCPU / 16 GiB sandbox so
	// "ci-run-unrelated" has room in the budget and completes.
	if err := store.FinishRun(ctx, dispatched[0].RunID, now.Add(2*time.Minute), "succeeded", ""); err != nil {
		t.Fatalf("FinishRun(task-1): %v", err)
	}
	ledger.Release(model.PoolAgent, dispatched[0].RunID)

	select {
	case unrelatedRun := <-unrelatedDone:
		if unrelatedRun.Conclusion != ci.ConclusionSuccess {
			t.Fatalf("unrelated CI run conclusion = %q (error: %s), want success", unrelatedRun.Conclusion, unrelatedRun.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for unrelated CI run to be admitted once agent sandbox released")
	}
}
