# Multiple Teams: Dispatcher Concurrency Isolation, Scheduling, and Proposal Inheritance Test Run

## Overview
Comprehensive test run, automated verification, and live walkthrough for Multiple Teams Dispatcher Concurrency Isolation, Scheduling Reconciliation, and Proposal Inheritance per `docs/test-plans/2026-09-23.md §6`.

## Scope and Environment

- Repository: `bwsalmon/grain`
- Branch: `grain/task-416`
- Host: Debian bookworm, Linux amd64
- Go `1.26.6`
- Test Harness: Full Grain server simulation with SQLite store, REST API (`pkg/ui.Server` / `pkg/ui.Client`), multi-team candidate dispatcher (`pkg/dispatch.Cycle`), orchestrator result processing loop (`pkg/orchestrator.ProcessResult`), and agent tool execution.
- Walkthrough Suite: `tests/e2e/multiteam_walkthrough_test.go` (`TestMultiTeamWalkthroughSection6`) and `tests/e2e/multiteam_stress_test.go`.

---

## Detailed Walkthrough & Verification Steps

### Step 1: Initialize Multi-Team Server Environment & REST API
- Initialized an embedded SQLite store and `pkg/ui.Client` with actor `test-lead@acme.com`.
- Configured global capacity limits to 10 workers, 5 mergers, and 10 CI jobs to verify that per-team capacity limits govern dispatch decisions independently of global headrooms.

### Step 2: Configure Teams with Distinct Worker Limits
- Created Team **"platform"**:
  - `ID: "platform"`
  - `Name: "Platform Infrastructure Team"`
  - `MaxWorkers: 1`
  - `MaxMergers: 1`
  - `MaxCIJobs: 2`
  - `Users: ["test-lead@acme.com", "platform-lead@acme.com", "infra-dev@acme.com"]`
- Created Team **"apps"**:
  - `ID: "apps"`
  - `Name: "Application Engineering Team"`
  - `MaxWorkers: 3`
  - `MaxMergers: 2`
  - `MaxCIJobs: 6`
  - `Users: ["test-lead@acme.com", "app-lead@acme.com", "app-dev@acme.com"]`
- Verified via API `client.ListTeams` that both teams registered correctly alongside the default team.

### Step 3: Enqueue Candidate Tasks Across Teams
- Enqueued 5 approved candidate tasks for Team **"platform"** (`Platform Task 1` through `Platform Task 5`).
- Enqueued 5 approved candidate tasks for Team **"apps"** (`Apps Task 1` through `Apps Task 5`).
- Verified all 10 tasks entered their respective team backlogs with ordered backlog keys via `store.OrderKeyForNewTaskForTeam`.

### Step 4: Dispatcher Concurrency Isolation & Throttling
- Executed `dispatch.Cycle(ctx, store, limits, clock)`.
- **Observed Dispatches**: Total 4 tasks dispatched simultaneously.
  - Team "platform": Exactly **1 task** dispatched (`Platform Task 1`, Task ID `1`). Remaining 4 tasks remained queued.
  - Team "apps": Exactly **3 tasks** dispatched (`Apps Task 1`, `Apps Task 2`, `Apps Task 3`, Task IDs `6`, `7`, `8`). Remaining 2 tasks remained queued.
- **Starvation Resistance**: Despite Team "platform" having candidates with earlier timestamps / order keys, Team "apps" dispatches filled all 3 available slots up to its `MaxWorkers: 3` limit without stalling on the saturated "platform" queue.
- **Saturated Cycle Check**: Executed a subsequent dispatcher cycle while all 4 tasks were running. Confirmed **0 additional tasks** were dispatched.

### Step 5: Slot Reclaim and Continuous Dispatch Isolation
- Completed `Apps Task 1` (Run ID `6-1`) and marked task completed.
- Executed `dispatch.Cycle`.
- **Observed**: Exactly **1 task** was dispatched — `Apps Task 4` (Task ID `9`).
- Team "platform" remained strictly capped at 1 running worker and did not leak capacity into the vacated "apps" slot.

### Step 6: Live Agent Run & Proposal Team Inheritance
- Executed a live agent run on `Platform Task 1` (Task ID `1`).
- The agent executed `git` operations, updated infrastructure files, and invoked the `propose_task` MCP tool twice:
  - Proposal 1: `"Child Platform Proposal 1: VPC Subnet Topology"`
  - Proposal 2: `"Child Platform Proposal 2: Control Plane Hardening"`
- `orchestrator.ProcessResult` processed the agent run results.
- **Inheritance Verification**:
  - Both child proposals were created with `task.Team = "platform"`.
  - Approved child proposals landed into `store.ReadyForTeam("platform")`.
  - Confirmed child tasks were completely excluded from `store.ReadyForTeam("apps")` and `store.ReadyForTeam("default")`.

### Step 7: Team-Targeted Schedules & Overlap Reconciliation
- Created two recurring schedules targeted at distinct teams:
  - Platform Schedule: `sched-plat-reconcile`, `Team: "platform"`, `Recurrence: daily at 02:00`, `Overlap: OverlapSkip`.
  - Apps Schedule: `sched-apps-sync`, `Team: "apps"`, `Recurrence: daily at 03:00`, `Overlap: OverlapSkip`.
- Advanced clock by 24 hours and fired both schedules:
  - Platform schedule fired Task ID `13` with `Team: "platform"`.
  - Apps schedule fired Task ID `14` with `Team: "apps"`.
- Completed and closed `Apps Task 14` while keeping `Platform Task 13` open.
- Advanced clock by another 24 hours and fired schedules again:
  - Platform schedule evaluated `HasOpenTaskWithTag("schedule:sched-plat-reconcile")`, found active Task `13`, and **skipped firing** per `OverlapSkip`.
  - Apps schedule evaluated `HasOpenTaskWithTag("schedule:sched-apps-sync")`, found previous task closed, and **successfully fired Task ID `15`** with `Team: "apps"`.
- Verified schedules for different teams operate completely independently without cross-team tag interference or cadence suppression.

### Step 8: Dynamic Team Limit Scaling & Task Reassignment
- Dynamically updated Team "platform" limit from `MaxWorkers: 1` to `MaxWorkers: 2` via `client.UpdateTeam`.
- Executed `dispatch.Cycle`: Confirmed dispatcher immediately recognized the increased limit and dispatched 2 tasks (`Task 2` and `Task 3`) concurrently.
- Reassigned `Platform Task 5` (Task ID `5`) to Team "apps" via `client.UpdateTask(ctx, "5", UpdateTaskRequest{Team: "apps"})`.
- Confirmed Task `5` was removed from `ReadyForTeam("platform")` and placed into `ReadyForTeam("apps")`.

---

## Test Results Summary

| Suite / Test Case | Scope | Result | Duration |
| :--- | :--- | :--- | :--- |
| `tests/e2e/multiteam_walkthrough_test.go:TestMultiTeamWalkthroughSection6` | Full Section 6 End-to-End Walkthrough | **PASS** | 0.45s |
| `tests/e2e/multiteam_stress_test.go:TestMultiTeamSimulationSmoke` | Multi-team concurrency stress & starvation resistance | **PASS** | 0.48s |
| `tests/e2e/proposal_team_inheritance_test.go:TestProposalTeamInheritanceE2E` | Live agent `propose_task` inheritance & reassignments | **PASS** | 0.32s |
| `pkg/dispatch/...` | Dispatch candidate selection & team limit enforcement | **PASS** | 0.05s |
| `pkg/orchestrator/...` | Result processing, schedule reconciliation & overlap | **PASS** | 0.85s |
| `pkg/model/...` | SQLite / Postgres store queries & team order keys | **PASS** | 1.12s |
| Full Repository Test Suite (`./pkg/...`, `./tests/e2e/...`) | 62 Go packages | **PASS** | 100% Green |

---

## Findings and Observations

1. **Strict Concurrency Isolation**: Per-team worker limits (`MaxWorkers`) prevent high-volume teams from starving smaller or higher-priority teams. When a team reaches capacity, the dispatcher skips its candidates and evaluates the next eligible team without blocking the cycle.
2. **Deterministic Inheritance**: Tasks filed by an agent via `propose_task` strictly inherit their parent's `Team` attribute and are assigned backlog positions inside the parent team's queue.
3. **Independent Schedule Execution**: Team-targeted schedules tag their created tasks with idempotency tags and evaluate `OverlapSkip` policies on per-schedule tags, allowing each team's schedules to fire and skip independently.
4. **Immediate Dynamic Updates**: Changes to team worker limits and task team assignments via the REST API take effect on the next dispatcher cycle without requiring server or daemon restarts.
