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

package dispatch_test

// The admission guarantee a console session -- and any task holding
// priority-dispatch -- is given (docs/sandbox-budget.md, "Sessions are
// never held by the budget"; task 2189): with the worker pool full *and*
// the sandbox budget full of CI, however it is full, a ready session
// starts in the very cycle that sees it and an ordinary task does not.
//
// Session 2174 waited 16 minutes on 2026-10-31 while the budget was full
// of CI sandboxes, and its log line said only "admission refused". These
// pin both halves of what that should have been: no budget refusal for
// a session, and a refusal that names its gate when one does happen.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
)

// sessionBudget is 16 vCPU / 32 GiB / 100 GiB of sandbox disk with a
// 4 vCPU / 8 GiB repair reserve and no overcommit.
func sessionBudget() model.Budget {
	return model.Budget{
		Host:             model.Commitment{CPUs: 16, MemoryMB: 32 * 1024, DiskGB: 100},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
		DiskOvercommit:   1,
		RepairReserve:    model.Commitment{CPUs: 4, MemoryMB: 8 * 1024},
	}
}

func sessionAgentShape() model.Commitment {
	return model.Commitment{CPUs: 4, MemoryMB: 8 * 1024, DiskGB: 20}
}

// budgetFullOfCI is every way the budget can be full of CI sandboxes.
// Each refuses an ordinary agent sandbox of sessionAgentShape.
func budgetFullOfCI() map[string][]model.Commitment {
	return map[string][]model.Commitment{
		"cpu":    {{CPUs: 8, MemoryMB: 1024}, {CPUs: 8, MemoryMB: 1024}},
		"memory": {{CPUs: 1, MemoryMB: 16 * 1024}, {CPUs: 1, MemoryMB: 16 * 1024}},
		"disk":   {{CPUs: 1, MemoryMB: 1024, DiskGB: 50}, {CPUs: 1, MemoryMB: 1024, DiskGB: 50}},
		// Everything but the repair reserve: only a base-branch repair may
		// spend what is left.
		"repair-reserve": {{CPUs: 12, MemoryMB: 24 * 1024}},
		// A deadlock-breaking CI exception has already pushed committed
		// over the cap.
		"over-cap": {{CPUs: 16, MemoryMB: 32 * 1024}, {CPUs: 8, MemoryMB: 16 * 1024}},
	}
}

func consoleSession(id string) model.Task {
	tk := task(id, true)
	tk.Origin.Reason = model.ReasonConsoleSession
	tk.Interactive = true
	tk.Tags = []string{model.ConsoleSessionTag(id)}
	tk.Grants = model.ConsoleSessionGrants(model.ConsoleSession{ID: id})
	return tk
}

// assertOnlyPriorityStarts fills the worker pool, fills the budget with
// ciJobs, queues an ordinary task ahead of prio and checks that one cycle
// starts prio and nothing else.
func assertOnlyPriorityStarts(t *testing.T, prio model.Task, ciJobs []model.Commitment) {
	t.Helper()
	store, ctx := open(t)
	ledger := &model.Ledger{}
	limits := model.Limits{
		Workers:    1,
		Budget:     sessionBudget(),
		AgentShape: sessionAgentShape(),
		Ledger:     ledger,
	}
	// The one worker slot, taken with an unbounded budget so it is not
	// what the budget is full of.
	putTasks(t, store, ctx, task("worker-live", true))
	if got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now); err != nil || len(got) != 1 {
		t.Fatalf("filling the worker pool: %+v, %v", got, err)
	}
	for i, job := range ciJobs {
		ledger.Charge(model.PoolCI, fmt.Sprintf("ci-job-%d", i), job)
	}
	if limits.SandboxAdmitsShape(sessionAgentShape()) {
		t.Fatalf("the budget should be full of CI: %s", ledger.Describe())
	}

	ordinary := task("ordinary", true)
	ordinary.OrderKey = -10 // ahead of the session in the backlog
	prio.OrderKey = 10
	putTasks(t, store, ctx, ordinary, prio)

	got, err := dispatch.Cycle(ctx, store, limits, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != prio.ID {
		t.Fatalf("with workers and the budget full of CI dispatched %+v, want %s alone in this same cycle (budget: %s)",
			got, prio.ID, ledger.Describe())
	}
	if runs, err := store.Runs(ctx, "ordinary"); err != nil || len(runs) != 0 {
		t.Fatalf("an ordinary task started past a full budget: %+v, %v", runs, err)
	}
	if held := ledger.LiveIn(model.PoolAgent); held != sessionAgentShape() {
		t.Errorf("the session's sandbox is charged to the ledger: agent pool holds %s, want %s", held, sessionAgentShape())
	}
	if wait, err := store.GrantWaitOf(ctx, "ordinary"); err != nil || wait == nil || wait.Capability != model.QuotaWaitCapability {
		t.Errorf("the ordinary task should say it is waiting for capacity: %+v, %v", wait, err)
	}
}

func TestConsoleSessionAdmittedWhenSandboxBudgetFullOfCI(t *testing.T) {
	for name, jobs := range budgetFullOfCI() {
		t.Run(name, func(t *testing.T) {
			assertOnlyPriorityStarts(t, consoleSession("director"), jobs)
		})
	}
}

func TestPriorityDispatchTaskAdmittedWhenSandboxBudgetFullOfCI(t *testing.T) {
	for name, jobs := range budgetFullOfCI() {
		t.Run(name, func(t *testing.T) {
			assertOnlyPriorityStarts(t, priorityTask("steward"), jobs)
		})
	}
}

// A run-now request for a session (what posting a message to a parked
// session amounts to when it is not already ready) takes the same path.
func TestConsoleSessionRunNowAdmittedWhenSandboxBudgetFullOfCI(t *testing.T) {
	store, ctx := open(t)
	ledger := &model.Ledger{}
	ledger.Charge(model.PoolCI, "ci-full", model.Commitment{CPUs: 16, MemoryMB: 32 * 1024, DiskGB: 100})
	limits := model.Limits{Workers: 1, Budget: sessionBudget(), AgentShape: sessionAgentShape(), Ledger: ledger}
	putTasks(t, store, ctx, consoleSession("director"))
	requestRunNow(t, ctx, store, "director", now)
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "director" {
		t.Fatalf("run-now for a session over a full budget dispatched %+v, want it", got)
	}
}

// Every session is admitted, not only the first: the old "at most one
// sandbox over the cap" exception held a second session behind CI.
func TestEveryConsoleSessionAdmittedWhileTheBudgetIsOverItsCap(t *testing.T) {
	store, ctx := open(t)
	ledger := &model.Ledger{}
	ledger.Charge(model.PoolCI, "ci-full", model.Commitment{CPUs: 16, MemoryMB: 32 * 1024})
	limits := model.Limits{Workers: 1, Budget: sessionBudget(), AgentShape: sessionAgentShape(), Ledger: ledger}
	putTasks(t, store, ctx, consoleSession("director"), consoleSession("support"), consoleSession("ops"), task("ordinary", true))
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, d := range got {
		ids[d.TaskID] = true
	}
	if len(got) != 3 || !ids["director"] || !ids["support"] || !ids["ops"] {
		t.Fatalf("dispatched %+v, want all three sessions and not the ordinary task", got)
	}
}

// The CI gate's "hole" -- capacity it holds back so a large job queued a
// while can start -- is the gate's own: a session is not held behind it.
func TestConsoleSessionAdmittedWhileCIHoldsAHoleForABigJob(t *testing.T) {
	store, ctx := open(t)
	ledger := &model.Ledger{}
	budget := sessionBudget()
	shape := sessionAgentShape()
	limits := model.Limits{Workers: 1, Budget: budget, AgentShape: shape, Ledger: ledger}
	gate := &ci.SharedGate{
		Budget:       func() model.Budget { return budget },
		Ledger:       func() *model.Ledger { return ledger },
		Default:      func() model.Commitment { return model.Commitment{CPUs: 1, MemoryMB: 1024} },
		Poll:         5 * time.Millisecond,
		ReserveAfter: func() time.Duration { return time.Millisecond },
	}
	// 12 vCPU / 24 GiB held by CI: 4 vCPU / 8 GiB short of a big job.
	held, err := gate.Acquire(ctx, ci.GateRequest{Want: model.Commitment{CPUs: 12, MemoryMB: 24 * 1024}})
	if err != nil {
		t.Fatal(err)
	}
	defer held()
	holeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	holding := make(chan struct{})
	var signalled bool
	go func() {
		release, err := gate.Acquire(holeCtx, ci.GateRequest{
			Want: model.Commitment{CPUs: 16, MemoryMB: 32 * 1024},
			Wait: func(w ci.Wait) {
				if !signalled && strings.Contains(w.Reason, "a hole big enough") {
					signalled = true
					close(holding)
				}
			},
		})
		if err == nil {
			release()
		}
	}()
	select {
	case <-holding:
	case <-time.After(10 * time.Second):
		t.Fatal("the big CI job never started holding a hole")
	}

	putTasks(t, store, ctx, task("ordinary", true), consoleSession("director"))
	got, err := dispatch.Cycle(ctx, store, limits, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].TaskID != "director" {
		t.Fatalf("with CI holding a hole dispatched %+v, want the session alone", got)
	}
}

// captureLog redirects the standard logger for one test, restoring the
// writer it had (not os.Stderr) when the test ends.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(flags)
	})
	return &buf
}

// An admitter's refusal names its gate and its reason, and a session's
// is a warning: "admission refused" with nothing after it is what made
// session 2174's wait undiagnosable.
func TestAnAdmissionRefusalNamesItsGateAndASessionsIsAWarning(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("ordinary", true), consoleSession("director"))
	buf := captureLog(t)
	refuse := dispatch.Admit(func(context.Context, dispatch.Candidate) (dispatch.Admission, error) {
		return dispatch.Admission{Gate: "model availability",
			Reason: "all connections serving claude-opus-5-5 are busy (2 cooling off after a usage limit)"}, nil
	})
	if got, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 2}, now, refuse); err != nil || len(got) != 0 {
		t.Fatalf("dispatched %+v (%v), want every candidate refused", got, err)
	}
	out := buf.String()
	for _, want := range []string{
		"dispatch: candidate ordinary skipped: admission refused by model availability -- all connections serving claude-opus-5-5 are busy (2 cooling off after a usage limit)",
		"dispatch: WARNING: priority-dispatch task director (a console session or a priority-dispatch holder) refused admission by model availability -- all connections serving claude-opus-5-5",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

func TestAnAdmissionRefusalWithNoReasonSaysSo(t *testing.T) {
	store, ctx := open(t)
	putTasks(t, store, ctx, task("ordinary", true))
	buf := captureLog(t)
	refuse := dispatch.Admit(func(context.Context, dispatch.Candidate) (dispatch.Admission, error) {
		return dispatch.Admission{}, nil
	})
	if _, err := dispatch.Cycle(ctx, store, model.Limits{Workers: 1}, now, refuse); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "admission refused by no reason given") {
		t.Errorf("log = %q, want the refusal to say it gave no reason", buf.String())
	}
}

// If a session is ever refused by the budget -- only the disk floor or the
// cluster can do that now -- its row says so in words a person reads as a
// wait, and the log has a warning.
func TestASessionRefusedByTheDiskFloorSaysItIsWaitingForSandboxBudget(t *testing.T) {
	store, ctx := open(t)
	budget := sessionBudget()
	budget.DiskFloor = model.DiskFloor{MinMB: 10 * 1024}
	budget.Disk = model.DiskSpace{Path: "/var/lib/grain", TotalMB: 100 * 1024, FreeMB: 1024}
	limits := model.Limits{Workers: 1, Budget: budget, AgentShape: sessionAgentShape(), Ledger: &model.Ledger{}}
	putTasks(t, store, ctx, consoleSession("director"))
	buf := captureLog(t)
	if got, err := dispatch.Cycle(ctx, store, limits, now); err != nil || len(got) != 0 {
		t.Fatalf("dispatched %+v (%v) below the disk floor, want nothing", got, err)
	}
	wait, err := store.GrantWaitOf(ctx, "director")
	if err != nil || wait == nil || !strings.HasPrefix(wait.Resource, "waiting for sandbox budget: disk:") {
		t.Fatalf("the session's wait = %+v (%v), want \"waiting for sandbox budget: disk: ...\"", wait, err)
	}
	if !strings.Contains(buf.String(), "dispatch: WARNING: priority-dispatch task director") {
		t.Errorf("a session held by the budget should be logged as a warning:\n%s", buf.String())
	}
}
