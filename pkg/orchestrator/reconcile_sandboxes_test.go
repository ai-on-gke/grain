// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
)

type fakeSizedBackend struct {
	mu      sync.Mutex
	live    map[string]*fakeSizedSandbox
	reaped  []string
	swept   int
	sweepFn func(context.Context) (int, error)
}

func newFakeSizedBackend() *fakeSizedBackend {
	return &fakeSizedBackend{live: map[string]*fakeSizedSandbox{}}
}

func (b *fakeSizedBackend) Acquire(_ context.Context, name string, shape Shape) (Sandbox, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if shape.CPUs <= 0 {
		shape.CPUs = 2
	}
	if shape.MemoryMB <= 0 {
		shape.MemoryMB = 4096
	}
	sb := &fakeSizedSandbox{
		backend: b,
		name:    name,
		shape:   model.Commitment{CPUs: shape.CPUs, MemoryMB: shape.MemoryMB},
	}
	b.live[name] = sb
	return sb, nil
}

func (b *fakeSizedBackend) LiveSandboxes() []LiveSandbox {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]LiveSandbox, 0, len(b.live))
	for id, sb := range b.live {
		pool, holder := sandboxPoolAndHolder(id)
		out = append(out, LiveSandbox{
			ID:      id,
			Name:    "vm-" + id,
			Backend: "granule",
			Kind:    sandboxKind(id),
			Pool:    pool,
			Holder:  holder,
			Shape:   sb.shape,
			Release: sb.Release,
		})
	}
	return out
}

func (b *fakeSizedBackend) SweepOrphans(ctx context.Context) (int, error) {
	if b.sweepFn != nil {
		return b.sweepFn(ctx)
	}
	return b.swept, nil
}

type fakeSizedSandbox struct {
	backend *fakeSizedBackend
	name    string
	shape   model.Commitment
	once    sync.Once
}

func (s *fakeSizedSandbox) Name() string                              { return s.name }
func (s *fakeSizedSandbox) Tools(context.Context) ([]mcp.Tool, error) { return nil, nil }
func (s *fakeSizedSandbox) ConfigureGitCredentials(context.Context, string, string, mcp.GitIdentity) error {
	return nil
}
func (s *fakeSizedSandbox) RunShell(context.Context, ShellRequest) (ShellResult, error) {
	return ShellResult{ExitCode: 0}, nil
}
func (s *fakeSizedSandbox) Release(context.Context) error {
	s.once.Do(func() {
		s.backend.mu.Lock()
		delete(s.backend.live, s.name)
		s.backend.reaped = append(s.backend.reaped, s.name)
		s.backend.mu.Unlock()
	})
	return nil
}

func makeTestTask(id string, director bool) model.Task {
	human := model.Principal{Kind: model.PrincipalHuman, ID: "alice"}
	tk := model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin: model.Origin{
			Attribution: model.Attribution{Actor: human},
			Reason:      model.ReasonDirect,
		},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: human},
	}
	if director {
		tk.Origin.Reason = model.ReasonConsoleSession
		tk.Tags = []string{model.ConsoleSessionTag(id)}
		tk.Interactive = true
		tk.Grants = model.ConsoleSessionGrants(model.ConsoleSession{ID: id})
	}
	return tk
}

func TestPersistentSandboxSupervisorReservesUpFrontAndRespectsBudget(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)

	backend := newFakeSizedBackend()
	sup := NewPersistentSandboxSupervisor()
	ledger := &model.Ledger{}
	// Budget has 6 vCPU / 12 GiB: room for one 4 vCPU / 8 GiB persistent sandbox, not two.
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 6, MemoryMB: 12 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
	deps := Deps{
		Store:               st,
		Sandboxes:           backend,
		PersistentSandboxes: sup,
		Budget:              budget,
		AgentShape:          model.Commitment{CPUs: 2, MemoryMB: 4 * 1024},
		Ledger:              ledger,
	}

	desired := []model.PersistentSandbox{
		{Name: "staging", Repo: "acme/app", Command: "./serve.sh", CPUs: 4, MemoryMB: 8 * 1024, Enabled: true},
		{Name: "too-big", Repo: "acme/app", Command: "./serve.sh", CPUs: 4, MemoryMB: 8 * 1024, Enabled: true},
	}
	sup.Reconcile(ctx, deps, desired)

	// Only one of the two 4 vCPU / 8 GiB persistent sandboxes fits the 6 vCPU / 12 GiB budget.
	if got := ledger.LiveIn(model.PoolPersistent); got != (model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}) {
		t.Fatalf("LiveIn(PoolPersistent) = %s, want 4 vCPU / 8 GiB (second persistent sandbox refused by budget)", got)
	}

	// Disabling all persistent sandboxes and reconciling releases the reservation in PoolPersistent.
	sup.Reconcile(ctx, deps, nil)
	if got := ledger.LiveIn(model.PoolPersistent); !got.IsZero() {
		t.Fatalf("LiveIn(PoolPersistent) after disabling = %s, want 0", got)
	}
}

func TestStandingSandboxSupervisorAdmitsAgainstBudgetAndReleasesOnStop(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	now := time.Now().UTC()

	launcher := func(_ context.Context, spec ServeSpec) (ServingSandbox, error) {
		return &fakeServingSandbox{spec: spec}, nil
	}
	sup := NewStandingSandboxSupervisor(launcher)
	ledger := &model.Ledger{}
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 4, MemoryMB: 8 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
	deps := Deps{
		Store:             st,
		StandingSandboxes: sup,
		Budget:            budget,
		AgentShape:        model.Commitment{CPUs: 2, MemoryMB: 4 * 1024},
		Ledger:            ledger,
	}

	if err := st.PutStandingSandbox(ctx, model.StandingSandbox{
		Name: "fits", Image: "img:1", Command: "run", CPUs: 4, MemoryMB: 8 * 1024, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutStandingSandbox(ctx, model.StandingSandbox{
		Name: "over-budget", Image: "img:1", Command: "run", CPUs: 2, MemoryMB: 4 * 1024, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := reconcileStandingSandboxes(ctx, deps, now); err != nil {
		t.Fatal(err)
	}
	if got := ledger.LiveIn(model.PoolStanding); got != (model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}) {
		t.Fatalf("LiveIn(PoolStanding) = %s, want 4 vCPU / 8 GiB", got)
	}
	overStatus, ok := sup.GetActive("over-budget")
	if !ok || overStatus == nil || !overStatus.Failed || !strings.Contains(overStatus.LastError, "memory") {
		t.Fatalf("over-budget standing sandbox should fail admission on memory, got %+v (ok=%v)", overStatus, ok)
	}

	// Delete both standing sandboxes and verify PoolStanding is released.
	if err := st.DeleteStandingSandbox(ctx, "fits"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteStandingSandbox(ctx, "over-budget"); err != nil {
		t.Fatal(err)
	}
	if err := reconcileStandingSandboxes(ctx, deps, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := ledger.LiveIn(model.PoolStanding); !got.IsZero() {
		t.Fatalf("LiveIn(PoolStanding) after delete = %s, want 0", got)
	}
}

func TestReconcileSandboxesDetectsDiscrepanciesReapsOrphansAndReports(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	baseTime := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)

	// Create two tasks: one with a live run, one whose run has already finished.
	if err := st.PutTask(ctx, makeTestTask("10", false)); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTask(ctx, makeTestTask("11", false)); err != nil {
		t.Fatal(err)
	}
	if err := st.StartRun(ctx, model.Run{ID: "10-1", TaskID: "10", Attempt: 1, StartedAt: baseTime}, model.Limits{Workers: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.StartRun(ctx, model.Run{ID: "11-1", TaskID: "11", Attempt: 1, StartedAt: baseTime}, model.Limits{Workers: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "11-1", baseTime.Add(time.Second), "succeeded", "done"); err != nil {
		t.Fatal(err)
	}

	backend := newFakeSizedBackend()
	// 1. Live agent sandbox for active run 10-1.
	if _, err := backend.Acquire(ctx, "10-1", Shape{CPUs: 4, MemoryMB: 8 * 1024}); err != nil {
		t.Fatal(err)
	}
	// 2. Leaked agent sandbox for finished run 11-1.
	if _, err := backend.Acquire(ctx, "11-1", Shape{CPUs: 4, MemoryMB: 8 * 1024}); err != nil {
		t.Fatal(err)
	}
	// 3. Unaccounted live CI sandbox c-leak that was not charged in Ledger.
	unaccountedCI, err := backend.Acquire(ctx, "c-leak", Shape{CPUs: 4, MemoryMB: 16 * 1024})
	if err != nil {
		t.Fatal(err)
	}

	ledger := &model.Ledger{}
	ledger.Charge(model.PoolAgent, "10-1", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024})
	// 4. Stale agent ledger entry "99-1" with no live run or sandbox.
	ledger.Charge(model.PoolAgent, "99-1", model.Commitment{CPUs: 4, MemoryMB: 8 * 1024})

	deps := Deps{
		Store:      st,
		Sandboxes:  backend,
		Ledger:     ledger,
		AgentShape: model.Commitment{CPUs: 4, MemoryMB: 8 * 1024},
		Budget: model.Budget{
			Host:             model.Commitment{CPUs: 32, MemoryMB: 64 * 1024},
			CPUOvercommit:    1,
			MemoryOvercommit: 1,
		},
	}

	// Advance past LedgerSyncGrace so stale entry "99-1" is eligible for release.
	checkTime := time.Now().UTC().Add(model.LedgerSyncGrace + time.Minute)
	if err := ReconcileSandboxes(ctx, deps, checkTime); err != nil {
		t.Fatal(err)
	}

	// Leaked sandbox 11-1 must have been reaped.
	for _, sb := range backend.LiveSandboxes() {
		if sb.ID == "11-1" {
			t.Fatal("leaked sandbox 11-1 for finished run should have been reaped")
		}
	}

	// Stale ledger holder 99-1 must have been released, and unaccounted live
	// sandbox c-leak must have been charged into PoolCI.
	if got := ledger.LiveIn(model.PoolAgent); got != (model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}) {
		t.Fatalf("LiveIn(PoolAgent) = %s, want 4 vCPU / 8 GiB (only 10-1)", got)
	}
	if got := ledger.LiveIn(model.PoolCI); got != (model.Commitment{CPUs: 4, MemoryMB: 16 * 1024}) {
		t.Fatalf("LiveIn(PoolCI) = %s, want 4 vCPU / 16 GiB (c-leak accounted for)", got)
	}

	rec, ok := LatestSandboxReconciliation(ledger)
	if !ok {
		t.Fatal("expected LatestSandboxReconciliation to be recorded")
	}
	if rec.OrphansReaped < 1 || rec.TotalOrphansReaped < 1 {
		t.Fatalf("expected OrphansReaped >= 1, got %+v", rec)
	}
	if len(rec.Discrepancies) == 0 {
		t.Fatalf("expected discrepancies to be recorded on first reconcile, got %+v", rec)
	}

	// Once c-leak is released and another reconciliation cycle runs, the
	// synthetic charge is removed and the deployment reports InSync = true.
	_ = unaccountedCI.Release(ctx)
	if err := ReconcileSandboxes(ctx, deps, checkTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	rec2, ok := LatestSandboxReconciliation(ledger)
	if !ok || !rec2.InSync || rec2.AccountedCount != 1 || rec2.LiveCount != 1 {
		t.Fatalf("after cleanup, expected InSync with 1 accounted and 1 live, got %+v", rec2)
	}
}

// Committed never exceeds the budget, plus at most one deadlock-breaking
// exception, plus one sandbox per live console session (priority
// dispatch): sessions are never held by the budget (Budget.AdmitsPriority,
// docs/sandbox-budget.md), and everything else is.
func TestCommittedNeverExceedsBudgetPlusExceptionAndSessionsAcrossAllPaths(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	now := time.Date(2026, 10, 24, 2, 0, 0, 0, time.UTC)

	// Budget: 16 vCPU / 32 GiB. Each standard sandbox is 4 vCPU / 8 GiB (4 slots total).
	// The three sessions below may each exceed the cap by their own sandbox
	// (max 28 vCPU / 56 GiB); nothing else may push committed past it.
	shape := model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}
	maxAllowedWithException := model.Commitment{CPUs: 28, MemoryMB: 56 * 1024}
	budget := model.Budget{
		Host:             model.Commitment{CPUs: 16, MemoryMB: 32 * 1024},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
	}
	ledger := &model.Ledger{}

	assertBound := func(step string) {
		t.Helper()
		live := ledger.Live()
		if live.CPUs > maxAllowedWithException.CPUs || live.MemoryMB > maxAllowedWithException.MemoryMB {
			t.Fatalf("%s: committed %s exceeded budget + 1 exception allowance (%s)", step, live, maxAllowedWithException)
		}
	}

	// 1. Persistent sandbox reserves 1 slot (4 vCPU / 8 GiB).
	ledger.Reserve(model.PoolPersistent, map[string]model.Commitment{"staging": shape})
	assertBound("after persistent reserve")

	// 2. Standing sandbox admits 1 slot (4 vCPU / 8 GiB). Total = 8 vCPU / 16 GiB.
	vStand, releaseStanding := ledger.Admit(budget, model.PoolStanding, "s01234567", shape)
	if !vStand.OK {
		t.Fatalf("standing sandbox should fit: %+v", vStand)
	}
	defer releaseStanding()
	assertBound("after standing admit")

	// 3. Queue 3 worker tasks and dispatch: 2 fit in the remaining 8 vCPU / 16 GiB under cap, 1 waits.
	for i := 1; i <= 3; i++ {
		if err := st.PutTask(ctx, makeTestTask(fmt.Sprintf("w-%d", i), false)); err != nil {
			t.Fatal(err)
		}
	}

	limits := model.Limits{
		Workers:    10,
		Budget:     budget,
		AgentShape: shape,
		Ledger:     ledger,
	}

	dispatched, err := dispatch.Cycle(ctx, st, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 2 {
		t.Fatalf("expected 2 workers dispatched to fill budget cap, got %+v", dispatched)
	}
	assertBound("after worker dispatch cycle")

	// 4. Now queue 2 director tasks while budget is at 100% cap (16 vCPU / 32 GiB).
	// Both are admitted (bringing live to 24 vCPU / 48 GiB): a session is
	// never held by the budget; the waiting worker w-3 still is.
	for i := 1; i <= 2; i++ {
		if err := st.PutTask(ctx, makeTestTask(fmt.Sprintf("d-%d", i), true)); err != nil {
			t.Fatal(err)
		}
	}
	dispatchedDirs, err := dispatch.Cycle(ctx, st, limits, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatchedDirs) != 2 {
		t.Fatalf("expected both directors dispatched over a full budget, got %+v", dispatchedDirs)
	}
	assertBound("after director dispatch cycle")
	if got, want := ledger.Live(), (model.Commitment{CPUs: 24, MemoryMB: 48 * 1024}); got != want {
		t.Fatalf("after director dispatch, live = %s, want %s", got, want)
	}

	// While over the cap, a third director (d-3) is still admitted, and
	// nothing else is: neither the waiting worker nor a Stuck CI job.
	if err := st.PutTask(ctx, makeTestTask("d-3", true)); err != nil {
		t.Fatal(err)
	}
	dispatchedOver, err := dispatch.Cycle(ctx, st, limits, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(dispatchedOver) != 1 || dispatchedOver[0].TaskID != "d-3" {
		t.Fatalf("expected only d-3 dispatched while over cap, got %+v", dispatchedOver)
	}
	assertBound("after a third session over the cap")

	gate := &ci.SharedGate{
		Budget: func() model.Budget { return budget },
		Ledger: func() *model.Ledger { return ledger },
		Poll:   time.Millisecond,
		Stuck:  func(string) (bool, string) { return true, "waiting" },
	}
	ciCtx, cancelCI := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancelCI()
	if _, err := gate.Acquire(ciCtx, ci.GateRequest{Want: shape}); err == nil {
		t.Fatal("CI gate must not admit even a Stuck job while committed already exceeds the cap")
	}
	assertBound("after blocked CI acquire")

	// 5. Recreating a sandbox for an active run keeps its single ledger slot without double-charging.
	backend := newFakeSizedBackend()
	meter := MeterFor(st)
	sb1, releaseMeter1, err := meter.Acquire(ctx, backend, model.SandboxKindAgent, dispatched[0].RunID, Shape{CPUs: 4, MemoryMB: 8 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	releaseMeter1(ctx)
	_ = sb1.Release(ctx)
	sb2, releaseMeter2, err := meter.Acquire(ctx, backend, model.SandboxKindAgent, dispatched[0].RunID, Shape{CPUs: 4, MemoryMB: 8 * 1024})
	if err != nil {
		t.Fatal(err)
	}
	assertBound("during recreate_sandbox")
	releaseMeter2(ctx)
	_ = sb2.Release(ctx)
}

// TestReconcileSandboxesCountsAndSweepsK8sGranulePods: a live
// kubernetes-granule pod is what System -> Sandboxes counts, and a pod
// whose Release could not delete it -- the API server answering with a
// timeout -- is reaped by the next reconcile rather than left
// holding its node until the daemon restarts.
func TestReconcileSandboxesCountsAndSweepsK8sGranulePods(t *testing.T) {
	ctx := context.Background()
	kubectl := writeSweepKubectl(t)
	k := NewK8sGranuleSandboxes(K8sGranuleConfig{Kubectl: kubectl.bin, Image: "example/granule", ReadyPollInterval: 5 * time.Millisecond})
	sb, err := k.Acquire(ctx, "c0ffee-1", Shape{CPUs: 2, MemoryMB: 4096})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	kubectl.takeDeleted(t)
	kubectl.setPods(t, "grain-granule-c0ffee-1")

	ledger := &model.Ledger{}
	ledger.Charge(model.PoolCI, "c0ffee-1", model.Commitment{CPUs: 2, MemoryMB: 4096})
	deps := Deps{Sandboxes: k, Ledger: ledger}
	if err := ReconcileSandboxes(ctx, deps, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rec, ok := LatestSandboxReconciliation(ledger)
	if !ok || rec.LiveCount != 1 || rec.OrphansReaped != 0 {
		t.Fatalf("reconciliation with the pod live = %+v, want LiveCount 1 and nothing reaped", rec)
	}
	if got := kubectl.takeDeleted(t); len(got) != 0 {
		t.Fatalf("reconcile deleted the live pod: %v", got)
	}

	kubectl.failDeletes(t, 1)
	if err := sb.Release(ctx); err == nil {
		t.Fatal("Release succeeded although its delete failed")
	}
	ledger.Release(model.PoolCI, "c0ffee-1")
	if err := ReconcileSandboxes(ctx, deps, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := kubectl.takeDeleted(t); len(got) != 1 || got[0] != "grain-granule-c0ffee-1" {
		t.Fatalf("reconcile deleted %v, want the leaked grain-granule-c0ffee-1", got)
	}
	rec, _ = LatestSandboxReconciliation(ledger)
	if rec.LiveCount != 0 || rec.OrphansReaped != 1 {
		t.Fatalf("reconciliation after the leak = %+v, want LiveCount 0 and one orphan reaped", rec)
	}
}

func TestReconcileSandboxesPersistentSandboxMatchesPersistentPool(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	now := time.Date(2026, 10, 27, 18, 0, 0, 0, time.UTC)

	backend := newFakeSizedBackend()
	ledger := &model.Ledger{}
	shape := model.Commitment{CPUs: 4, MemoryMB: 8 * 1024}

	sbName := persistentSandboxName("staging")
	if _, err := backend.Acquire(ctx, sbName, Shape{CPUs: shape.CPUs, MemoryMB: shape.MemoryMB}); err != nil {
		t.Fatal(err)
	}
	ledger.Reserve(model.PoolPersistent, map[string]model.Commitment{sbName: shape})

	deps := Deps{
		Store:      st,
		Sandboxes:  backend,
		Ledger:     ledger,
		AgentShape: shape,
	}

	for cycle := 0; cycle < 2; cycle++ {
		if err := ReconcileSandboxes(ctx, deps, now.Add(time.Duration(cycle)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		rec, ok := LatestSandboxReconciliation(ledger)
		if !ok {
			t.Fatal("expected LatestSandboxReconciliation to be recorded")
		}
		if !rec.InSync || len(rec.Discrepancies) != 0 {
			t.Fatalf("cycle %d: expected InSync with no discrepancies, got %+v", cycle, rec)
		}
		if got := rec.Pools[model.PoolPersistent]; got != shape {
			t.Fatalf("cycle %d: Pools[PoolPersistent] = %s, want %s", cycle, got, shape)
		}
		if got := rec.Pools[model.PoolAgent]; !got.IsZero() {
			t.Fatalf("cycle %d: Pools[PoolAgent] = %s, want 0 (persistent sandbox must not double-count into agent pool)", cycle, got)
		}
		if rec.AccountedCount != 1 || rec.LiveCount != 1 || rec.Committed != shape {
			t.Fatalf("cycle %d: unexpected reconciliation counts/totals: %+v", cycle, rec)
		}
	}
}

// A session's sandbox keeps the name of the run that built it (2-1) while
// later runs of the session (2-4, ...) use it. The reaper must not read it
// as leaked by finished run 2-1 in any of the windows where neither the
// store's live runs nor InFlight name it: parked warm, claimed by a dispatch
// before SetRunSandbox records it, or used by a run whose row is finished
// before the sandbox is parked warm again. Once nothing holds it, it is
// reaped as before.
func TestReconcileSandboxesKeepsSessionSandboxThroughClaimAndPark(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	now := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	ttl := 30 * time.Minute

	if err := st.PutTask(ctx, makeTestTask("2", true)); err != nil {
		t.Fatal(err)
	}
	limits := model.Limits{Workers: 10}
	if err := st.StartRun(ctx, model.Run{ID: "2-1", TaskID: "2", Attempt: 1, StartedAt: now}, limits); err != nil {
		t.Fatal(err)
	}
	backend := newFakeSizedBackend()
	sb, err := backend.Acquire(ctx, "2-1", Shape{CPUs: 2, MemoryMB: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRun(ctx, "2-1", now.Add(time.Second), "succeeded", "done"); err != nil {
		t.Fatal(err)
	}

	runs := &InFlight{}
	deps := Deps{Store: st, Sandboxes: backend, Runs: runs}
	t.Cleanup(func() { discardWarmSessionSandbox(context.Background(), deps, "2") })

	reconcile := func(step string) {
		t.Helper()
		if err := ReconcileSandboxes(ctx, deps, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		backend.mu.Lock()
		_, live := backend.live["2-1"]
		backend.mu.Unlock()
		if !live {
			t.Fatalf("%s: session sandbox 2-1 was reaped while the session still held it", step)
		}
	}

	parkWarmSessionSandbox(deps, "2", "2-1", "key", "grants", sb, nil, model.Commitment{}, now, ttl)
	reconcile("parked warm after run 2-1 finished")

	// Run 2-4 is dispatched and claims the warm sandbox; its row is live
	// but does not yet name the sandbox.
	if err := st.StartRun(ctx, model.Run{ID: "2-4", TaskID: "2", Attempt: 1, StartedAt: now}, limits); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	runs.Go("2-4", "2", func() { <-release })
	claimed, _ := claimWarmSessionSandbox(deps, "2", "2-4", now)
	if claimed == nil || claimed.SandboxID != "2-1" {
		t.Fatalf("claimWarmSessionSandbox = %+v, want sandbox 2-1", claimed)
	}
	reconcile("claimed by run 2-4 before SetRunSandbox")

	if err := st.SetRunSandbox(ctx, "2-4", "2-1", nil); err != nil {
		t.Fatal(err)
	}
	reconcile("recorded as run 2-4's sandbox")

	// Run 2-4's row is finished while its goroutine is still in flight,
	// before the sandbox is parked warm again.
	if err := st.FinishRun(ctx, "2-4", now.Add(2*time.Second), "succeeded", "done"); err != nil {
		t.Fatal(err)
	}
	reconcile("run 2-4 finished, not yet parked")

	parkWarmSessionSandbox(deps, "2", "2-1", "key", "grants", sb, nil, model.Commitment{}, now, ttl)
	unholdSessionSandbox(st, "2-1", "2-4")
	close(release)
	if err := runs.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	reconcile("parked warm again after run 2-4")

	// A run that claims the sandbox and lets it go without parking it
	// leaves nothing holding it: the reaper takes it.
	if claimed, _ := claimWarmSessionSandbox(deps, "2", "2-5", now); claimed == nil {
		t.Fatal("claimWarmSessionSandbox(2-5) found no warm sandbox")
	}
	unholdSessionSandbox(st, "2-1", "2-5")
	if err := ReconcileSandboxes(ctx, deps, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	_, live := backend.live["2-1"]
	backend.mu.Unlock()
	if live {
		t.Fatal("sandbox 2-1 held by nothing should have been reaped")
	}
}

// A run reusing a session sandbox is charged under its own run ID while
// the sandbox keeps the name of the run that built it: the reconciler
// must count that sandbox as the run's charge, not charge it a second
// time as "missing from ledger" -- neither between the claim and
// SetRunSandbox nor once the run's row names it.
func TestReconcileSandboxesDoesNotDoubleChargeReusedSessionSandbox(t *testing.T) {
	ctx := context.Background()
	st := openTestStoreForOrchestrator(t)
	now := time.Now().UTC()
	shape := model.Commitment{CPUs: 2, MemoryMB: 3072}

	if err := st.PutTask(ctx, makeTestTask("2", true)); err != nil {
		t.Fatal(err)
	}
	limits := model.Limits{Workers: 10}
	backend := newFakeSizedBackend()
	if _, err := backend.Acquire(ctx, "2-1", Shape{CPUs: 2, MemoryMB: 3072}); err != nil {
		t.Fatal(err)
	}
	if err := st.StartRun(ctx, model.Run{ID: "2-50", TaskID: "2", Attempt: 50, StartedAt: now}, limits); err != nil {
		t.Fatal(err)
	}
	ledger := &model.Ledger{}
	ledger.Charge(model.PoolAgent, "2-50", shape)
	deps := Deps{Store: st, Sandboxes: backend, Ledger: ledger, AgentShape: shape}
	t.Cleanup(func() { unholdSessionSandbox(st, "2-1", "2-50") })

	check := func(step string) {
		t.Helper()
		for i := 0; i < 2; i++ {
			if err := ReconcileSandboxes(ctx, deps, now.Add(model.LedgerSyncGrace+time.Minute)); err != nil {
				t.Fatal(err)
			}
			if got := ledger.LiveIn(model.PoolAgent); got != shape {
				t.Fatalf("%s, pass %d: LiveIn(PoolAgent) = %s, want %s (one sandbox, charged once)", step, i, got, shape)
			}
			rec, _ := LatestSandboxReconciliation(ledger)
			for _, d := range rec.Discrepancies {
				if strings.Contains(d, "missing from ledger") {
					t.Fatalf("%s, pass %d: unexpected discrepancy %q", step, i, d)
				}
			}
		}
	}

	holdSessionSandbox(st, "2-1", "2-50")
	check("claimed by run 2-50 before SetRunSandbox")

	if err := st.SetRunSandbox(ctx, "2-50", "2-1", nil); err != nil {
		t.Fatal(err)
	}
	check("recorded as run 2-50's sandbox")
}
