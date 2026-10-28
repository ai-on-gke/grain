# Live Sandbox Ledger and Multi-Team CI Capacity Stress Verification

**Date**: 2026-09-28  
**Topic**: Live Compute Ledger (`model.Ledger`), Host Resource Saturation, Director Singleton Bypass, and Multi-Team CI Capacity  
**Target Branch**: `grain/task-930`  
**Test Suites**:
- `tests/e2e/live_budget_ledger_stress_test.go`:
  - `TestLiveBudgetLedgerConcurrentSaturationAndQueueing`
  - `TestDirectorSingletonBypassUnderSaturation`
  - `TestLegacyDaemonStartupFlagsIgnoredWithNotice`
- `cmd/grain/sandbox_budget_test.go`
- `cmd/grain/ci_capacity_test.go`
- `pkg/model/ledger_test.go`
- `pkg/ci/gate_test.go`
- `pkg/dispatch/dispatch_test.go`

---

## Executive Summary

End-to-end stress and saturation testing was executed for Grain's live compute ledger (`model.Ledger`), host CPU/RAM budget enforcement, per-team `MaxCIJobs` limits, Director singleton bypass mechanics, and legacy daemon flag deprecation.

Key findings confirmed:
1. **Live Ledger Accounting & Host Budget Saturation**:
   Under sustained multi-team contention (18 agent tasks and 9 concurrent CI workflows across 3 teams), the live compute ledger (`model.Ledger`) accurately tracked physical host resource commitments (`CPUs` and `MemoryMB`). Total admitted sandboxes never exceeded the host budget of 16 vCPUs and 32 GiB RAM (100% capacity = 4 concurrent sandboxes at 4 vCPU / 8 GiB each). Sandboxes queued cleanly without race conditions, deadlocks, or memory exhaustion, returning exactly to zero upon workload completion.
2. **Per-Team CI Capacity Isolation**:
   Concurrent CI pipeline dispatches (`ci.SharedGate.Acquire`) respected individual team allocations (`team-alpha: MaxCIJobs=2`, `team-beta: MaxCIJobs=4`, `team-gamma: MaxCIJobs=2`) while simultaneously sharing host capacity with the agent runner pool without starvation or limit violations.
3. **Director Session Singleton Bypass**:
   When host resources and worker slots were 100% saturated (4/4 active worker sandboxes), an interactive Director session task (`ReasonConsoleSession`, tagged `session:director`) cleanly bypassed worker capacity limits, immediately admitted into execution, and accurately charged the live ledger (raising utilization to 20 vCPU / 40 GiB). The singleton constraint was strictly enforced: subsequent Director sessions queued until the active Director session completed.
4. **Legacy Daemon Startup Flags Deprecation**:
   Legacy daemon startup flags (`-max-workers`, `-max-mergers`, `-max-ci-jobs`) were parsed cleanly without causing daemon startup failures, logged deprecation notices, and imposed no deployment-wide count ceilings (`limits.Unlimited() == true`), allowing team-level limits and live host compute budgeting to govern capacity.

---

## Verification Matrix

| Validation Requirement | Harness / Test Function | Verified Behavior & Assertions | Status |
| :--- | :--- | :--- | :--- |
| **1. Concurrent Ledger Saturation & Queueing** | `TestLiveBudgetLedgerConcurrentSaturationAndQueueing` | 18 agent tasks + 9 multi-job CI workflows executed across 3 teams (`team-alpha`, `team-beta`, `team-gamma`). Host budget (16 vCPU, 32 GiB) never exceeded. Peak CPUs = 16, Peak RAM = 32768 MiB. Per-team CI job limits enforced. 0 data races (`-race`). Zero balance on completion. | **PASS** |
| **2. Director Singleton Bypass Under Saturation** | `TestDirectorSingletonBypassUnderSaturation` | Host fully saturated with 4 ordinary worker tasks (16 vCPU, 32 GiB). Ordinary 5th task rejected (`SandboxAdmits() == false`). Director session 1 dispatched immediately via `Limits.ChargeDirectorSandbox`, ledger charged to 20 vCPU / 40 GiB (5 agent sandboxes). Second Director session blocked until Director 1 finished. | **PASS** |
| **3. Legacy Daemon Startup Flags Deprecation** | `TestLegacyDaemonStartupFlagsIgnoredWithNotice` | FlagSet parsing with `-max-workers`, `-max-mergers`, `-max-ci-jobs` succeeded without error. `model.Limits` built from config verified count bounds remain 0 and `Unlimited()` holds true. | **PASS** |
| **4. Core Ledger & Gate Unit Verification** | `go test -race ./pkg/model ./pkg/ci ./pkg/dispatch ./cmd/grain` | Full suite execution of ledger admissions, shared CI gate priority queueing, dispatch scheduler cycles, and daemon budget tests passed cleanly with race detector enabled. | **PASS** |

---

## Detailed Test Walkthrough & Results

### 1. Concurrent Host Saturation & Multi-Team CI Workflows

**Setup**:
- Host Budget: 16 vCPUs, 32768 MiB RAM (`CPUOvercommit: 1.0`, `MemoryOvercommit: 1.0`).
- Sandbox Shape: 4 vCPUs, 8192 MiB RAM per sandbox.
- Maximum Concurrent Sandboxes: 4 total.
- Teams:
  - `team-alpha`: `MaxWorkers: 4, MaxMergers: 1, MaxCIJobs: 2`
  - `team-beta`: `MaxWorkers: 4, MaxMergers: 1, MaxCIJobs: 4`
  - `team-gamma`: `MaxWorkers: 4, MaxMergers: 1, MaxCIJobs: 2`
- Workload: 18 agent tasks (6 per team) and 9 CI jobs (3 per team) launched concurrently.

**Observations**:
- The dispatcher (`dispatch.Cycle`) and CI gate (`ci.SharedGate.Acquire`) dynamically coordinated through `model.Ledger`.
- Peak CPU utilization observed: 16 vCPUs (100.0% of host capacity).
- Peak Memory utilization observed: 32768 MiB (100.0% of host capacity).
- Maximum concurrent CI jobs observed per team:
  - `team-alpha`: 2 (limit: 2)
  - `team-beta`: 2 (limit: 4)
  - `team-gamma`: 2 (limit: 2)
- Upon completion of all 18 agent tasks and 9 CI jobs:
  - `ledger.Live()` returned `{CPUs: 0, MemoryMB: 0}`.
  - `ledger.Count(PoolAgent) == 0` and `ledger.Count(PoolCI) == 0`.

### 2. Director Session Singleton Bypass Under 100% Saturation

**Setup**:
- Host capacity saturated with 4 active background agent tasks (16 vCPUs, 32768 MiB).
- `limits.SandboxAdmits()` returned `false`.
- Ordinary 5th candidate task was skipped by the dispatcher due to capacity exhaustion.

**Bypass Execution**:
- Enqueued Director task (`ID: director-task-1`, `ReasonConsoleSession`, tagged `session:director`).
- `dispatch.Cycle` invoked `limits.ChargeDirectorSandbox("director-task-1", shape)`:
  - Bypassed host saturation and immediately dispatched `director-task-1`.
  - Ledger balance updated to 20 vCPUs, 40960 MiB RAM, and 5 active agent sandboxes.
- Enqueued second Director task (`ID: director-task-2`).
  - `dispatch.Cycle` verified `live.Directors == 1`, preventing `director-task-2` from dispatching.
- Completed `director-task-1` and released its run in the ledger:
  - Ledger balance reverted to 16 vCPUs, 32768 MiB RAM.
  - Next dispatch cycle admitted `director-task-2` into the freed Director singleton slot.
- Fully drained and released all tasks; ledger returned to zero.

### 3. Legacy Daemon Startup Flags Deprecation

**Validation**:
- Tested startup argument processing with `-max-workers=100`, `-max-mergers=50`, `-max-ci-jobs=200`, and custom `-data-dir`.
- Verified flags are recognized and discarded without breaking configuration parsing.
- Verified scheduler limits enforce live resource budgeting rather than legacy static process counters.

---

## Automated Test Output

```
=== RUN   TestLiveBudgetLedgerConcurrentSaturationAndQueueing
=== RUN   TestDirectorSingletonBypassUnderSaturation
=== RUN   TestLegacyDaemonStartupFlagsIgnoredWithNotice
--- PASS: TestLegacyDaemonStartupFlagsIgnoredWithNotice (0.00s)
    live_budget_ledger_stress_test.go:545: Legacy startup flags parsed cleanly and verified ignored by live scheduler
--- PASS: TestDirectorSingletonBypassUnderSaturation (1.29s)
    live_budget_ledger_stress_test.go:497: Director singleton bypass and live ledger accounting verified cleanly under saturation
--- PASS: TestLiveBudgetLedgerConcurrentSaturationAndQueueing (2.37s)
    live_budget_ledger_stress_test.go:267: Live budget stress completed successfully: max CPUs seen = 16 (budget 16), max Mem seen = 32768 MiB (budget 32768 MiB)
    live_budget_ledger_stress_test.go:276: Team team-alpha: MaxCIJobs limit = 2, peak concurrent CI jobs observed = 2
    live_budget_ledger_stress_test.go:276: Team team-beta: MaxCIJobs limit = 4, peak concurrent CI jobs observed = 2
    live_budget_ledger_stress_test.go:276: Team team-gamma: MaxCIJobs limit = 2, peak concurrent CI jobs observed = 2
PASS
ok  	github.com/ai-on-gke/grain/tests/e2e	3.421s
```

---

## Conclusion & Readiness

The live compute ledger (`model.Ledger`), shared CI gate admission, Director session singleton bypass, and legacy flag handling have been thoroughly validated under multi-team concurrent saturation. All test suites pass cleanly with race detection enabled (`-race`).
