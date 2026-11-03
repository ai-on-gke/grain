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

package model

// The sandbox budget's guarantee to console sessions and priority-dispatch
// tasks (Budget.AdmitsPriority, docs/sandbox-budget.md): whatever CI and
// ordinary work hold, a person writing to a session is not made to wait
// for the budget. Only what is physically short -- the disk floor, a
// cluster with no node for the pod -- refuses one.

import (
	"fmt"
	"testing"
)

// priorityBudget is 16 vCPU / 32 GiB / 100 GiB of sandbox disk, a 4 vCPU
// / 8 GiB repair reserve, and no overcommit: small enough to fill with a
// handful of CI sandboxes.
func priorityBudget() Budget {
	return Budget{
		Host:             Commitment{CPUs: 16, MemoryMB: 32 * 1024, DiskGB: 100},
		CPUOvercommit:    1,
		MemoryOvercommit: 1,
		DiskOvercommit:   1,
		RepairReserve:    Commitment{CPUs: 4, MemoryMB: 8 * 1024},
	}
}

func sessionShape() Commitment { return Commitment{CPUs: 4, MemoryMB: 8 * 1024, DiskGB: 20} }

// fullOfCI is each way a budget fills with CI sandboxes: every one of
// them refuses an ordinary agent sandbox, and none may refuse a session.
func fullOfCI() map[string][]Commitment {
	return map[string][]Commitment{
		// 4 x 4 vCPU: the vCPU ceiling, with memory and disk to spare.
		"cpu": {{CPUs: 4, MemoryMB: 1024}, {CPUs: 4, MemoryMB: 1024}, {CPUs: 4, MemoryMB: 1024}, {CPUs: 4, MemoryMB: 1024}},
		// 2 x 16 GiB: the memory ceiling.
		"memory": {{CPUs: 1, MemoryMB: 16 * 1024}, {CPUs: 1, MemoryMB: 16 * 1024}},
		// 5 x 20 GiB of sandbox disk: the disk ceiling.
		"disk": {{CPUs: 1, MemoryMB: 1024, DiskGB: 20}, {CPUs: 1, MemoryMB: 1024, DiskGB: 20},
			{CPUs: 1, MemoryMB: 1024, DiskGB: 20}, {CPUs: 1, MemoryMB: 1024, DiskGB: 20}, {CPUs: 1, MemoryMB: 1024, DiskGB: 20}},
		// 12 vCPU / 24 GiB: everything but the repair reserve, which only
		// a base-branch repair may spend.
		"repair-reserve": {{CPUs: 12, MemoryMB: 24 * 1024}},
		// A deadlock-breaking CI exception already pushed committed over
		// the cap (Ledger.AdmitException's "at most one over").
		"over-cap": {{CPUs: 16, MemoryMB: 32 * 1024}, {CPUs: 4, MemoryMB: 8 * 1024}},
	}
}

func TestConsoleSessionAdmittedWhenSandboxBudgetFullOfCI(t *testing.T) {
	for name, jobs := range fullOfCI() {
		t.Run(name, func(t *testing.T) {
			ledger := &Ledger{}
			limits := Limits{Budget: priorityBudget(), AgentShape: sessionShape(), Ledger: ledger}
			for i, job := range jobs {
				ledger.Charge(PoolCI, fmt.Sprintf("ci-%d", i), job)
			}

			if v := limits.SandboxVerdictFor(sessionShape()); v.OK {
				t.Fatalf("an ordinary agent sandbox should be refused with the budget full of CI (%s)", ledger.Live())
			}
			if v := limits.Ledger.VerdictException(limits.Budget, sessionShape()); name == "over-cap" && v.OK {
				t.Fatal("a second CI exception should be refused while committed is over the cap")
			}
			if v := limits.Ledger.VerdictAs(limits.Budget, Commitment{CPUs: 4, MemoryMB: 1024}, false); v.OK && name != "memory" && name != "disk" {
				t.Fatalf("another ordinary CI job should be refused with the budget full of CI: %+v", v)
			}

			if v := limits.DirectorSandboxVerdictFor(sessionShape()); !v.OK {
				t.Fatalf("a console session's verdict = %q, want admitted whatever CI holds", v.Reason)
			}
			v, release := limits.AdmitDirectorSandboxShape("session-run", sessionShape())
			if !v.OK || release == nil {
				t.Fatalf("a console session's sandbox was refused: %q", v.Reason)
			}
			if got := ledger.LiveIn(PoolAgent); got != sessionShape() {
				t.Errorf("the session's sandbox is charged while it exists: agent pool holds %s, want %s", got, sessionShape())
			}
			if limits.SandboxAdmitsShape(sessionShape()) {
				t.Error("with the session charged, ordinary work still waits: the session defers CI and ordinary work, not the other way round")
			}
			release()
			if got := ledger.LiveIn(PoolAgent); !got.IsZero() {
				t.Errorf("the session's shape is given back when its run ends: %s", got)
			}
		})
	}
}

// A second session, and a third, are admitted too: the old "at most one
// sandbox over the cap" rule held every session after the first one
// behind whatever CI was running.
func TestEverySessionAdmittedWhileEarlierSessionsHoldTheBudgetOverCap(t *testing.T) {
	ledger := &Ledger{}
	limits := Limits{Budget: priorityBudget(), AgentShape: sessionShape(), Ledger: ledger}
	ledger.Charge(PoolCI, "ci-big", Commitment{CPUs: 16, MemoryMB: 32 * 1024, DiskGB: 100})
	for i := 0; i < 3; i++ {
		v, _ := limits.AdmitDirectorSandbox(fmt.Sprintf("session-%d", i))
		if !v.OK {
			t.Fatalf("session %d refused: %q", i, v.Reason)
		}
		if !v.Exception {
			t.Errorf("session %d admitted over the budget should be marked an exception", i)
		}
	}
	if limits.Budget.WithinCap(ledger.Live()) {
		t.Fatalf("three sessions over a full budget should leave committed over the cap: %s", ledger.Live())
	}
	if limits.SandboxAdmits() {
		t.Error("ordinary work waits while committed is over the cap")
	}
}

// The deadlock-breaking CI exception keeps its own, tighter rule: only a
// session's verdict changed.
func TestCIExceptionStillAdmitsAtMostOneSandboxOverCap(t *testing.T) {
	ledger := &Ledger{}
	budget := priorityBudget()
	ledger.Charge(PoolCI, "ci-full", Commitment{CPUs: 16, MemoryMB: 32 * 1024})
	if v, _ := ledger.AdmitException(budget, PoolCI, "ci-exception-1", Commitment{CPUs: 4, MemoryMB: 8 * 1024}); !v.OK || !v.Exception {
		t.Fatalf("first CI exception at cap = %+v, want admitted as the exception", v)
	}
	if v, _ := ledger.AdmitException(budget, PoolCI, "ci-exception-2", Commitment{CPUs: 4, MemoryMB: 8 * 1024}); v.OK {
		t.Fatal("a second CI exception must wait while committed exceeds the cap")
	}
	if v, _ := ledger.AdmitPriority(budget, PoolAgent, "session", sessionShape()); !v.OK {
		t.Fatalf("a session is admitted even over the cap the CI exception left: %q", v.Reason)
	}
}

// What is physically short still refuses a session: a volume below its
// disk floor would fail the sandbox, not run it.
func TestASessionIsStillHeldByTheDiskFloor(t *testing.T) {
	budget := priorityBudget()
	budget.DiskFloor = DiskFloor{MinMB: 10 * 1024}
	budget.Disk = DiskSpace{Path: "/var/lib/grain", TotalMB: 100 * 1024, FreeMB: 2 * 1024}
	v := (&Ledger{}).VerdictPriority(budget, sessionShape())
	if v.OK || v.Reason == "" {
		t.Fatalf("a session below the disk floor = %+v, want refused with the floor's words", v)
	}
}

type noNodeFits struct{}

func (noNodeFits) FitsNow(Commitment) (bool, string) { return false, "no node has 4 vCPU free" }

// And so does a cluster with no node that can schedule the pod -- but
// only once the budget is full: inside the budget the arithmetic admits
// it, exactly as for any other sandbox.
func TestASessionOverTheBudgetIsHeldByAClusterWithNoRoom(t *testing.T) {
	budget := priorityBudget()
	budget.ClusterFit = noNodeFits{}
	ledger := &Ledger{}
	ledger.Charge(PoolCI, "ci-full", Commitment{CPUs: 16, MemoryMB: 32 * 1024})
	v, release := ledger.AdmitPriority(budget, PoolAgent, "session", sessionShape())
	if v.OK || release != nil {
		t.Fatalf("a session the cluster cannot schedule = %+v, want refused", v)
	}
	if got := ledger.LiveIn(PoolAgent); !got.IsZero() {
		t.Errorf("a refused session charges nothing: %s", got)
	}
}
