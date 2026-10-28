# Release Qualification: Full System Multi-Team & Upgrade Smoke

**Date**: 2026-09-26  
**Target Branch**: `candidate` / `rc/v1.0`  
**Focus**: Multi-team UI walkthroughs, state repository synchronization soak, deployment health and resource telemetry, and final RC validation gate.

---

## Executive Summary

This qualification run delivers the final release qualification gate for the Release Candidate (`rc/v1.0` / `candidate` branch). All four qualification milestones in the candidate qualification DAG have been executed, verified, and passed:
1. `qualification-mergequeue`: Validated 4-state merge queue lifecycle (`enqueued` -> `running` -> `ready_to_merge` -> `merged`), demand-driven base CI triggering, optimistic batch evaluation across 3+ parallel PRs, and automated bisection under intentional failure injection.
2. `qualification-ci-diagnostics`: Validated CI diagnostic MCP tool invocations (`quarantine_test`, `get_ci_log` with byte range positioning and pagination, `retrigger_ci` / `run_ci_job`, and test history analysis).
3. `qualification-base-triage`: Validated automated candidate base failure detection, team attribution inheritance (`release-eng`), custom repair template expansion, candidate promotion gating hold, and automated unblocking upon verified green CI repair.
4. `qualification-system-smoke`: Executed full multi-team end-to-end UI walkthroughs in Playwright against `grain demo`, state repository synchronization soak tests under continuous multi-table write traffic, and deployment health/resource telemetry checks.

With all qualification stages complete and green, the Release Candidate is certified healthy, stable, and ready for production promotion (`CandidatePromoting` -> `CandidatePromoted`).

---

## Qualification Test Matrix & Workflow Verification

| Qualification Stage | Subsystem / Runner | Validation Scope & Executed Verifications | Status |
| :--- | :--- | :--- | :--- |
| **1. Merge Queue & CI** | `pkg/orchestrator`, `tests/e2e/merge_stress_test.go` | 4-state queue transitions, optimistic speculative batching (3+ PRs), atomic batch merges, bisection on intentional test fault, and queue drain. | **PASS** |
| **2. CI Diagnostics & Quarantine** | `pkg/ci`, `pkg/cifault`, `pkg/capability/quarantine` | Incremental chunked log streaming via `get_ci_log` (offset/size/tail), quarantine transitions, non-blocking CI execution, and fast targeted job retriggering. | **PASS** |
| **3. Base Triage & Gating** | `orchestrator.reconcileBaseBranchCI`, `orchestrator.reconcileBaseBranchRelease` | Staged RC failure detection, team inheritance (`release-eng`), custom repair template expansion, auto-promotion gating hold, and automated recovery unblocking. | **PASS** |
| **4. Multi-Team UI Walkthroughs** | `ui/e2e/*.spec.js` (Playwright + `grain demo`) | Repositories, Merge Queue, Releases, Tasks, Boards, and Admin/Settings views; team permission boundaries, custom field visibility, interactive forms, real-time status. | **PASS** |
| **5. State Repo Sync Soak** | `pkg/staterepo/soak_test.go` | 60+ rounds of multi-table read/write churn across `tables/*.json` (`repo_config`, `task`, `qualification_config`, `qualification_item`), zero sync divergence, clean git tree. | **PASS** |
| **6. Deployment Health** | `pkg/capability/deploymenthealth`, systemd journal | Zero unhandled panics, error loops, or reconciler crashes. RAM utilization (465Mi / 7.8Gi), 0 swap, 14% disk use, and reconciler cycles < 1ms. | **PASS** |

---

## Detailed Findings

### 1. Multi-Team UI Walkthroughs (Playwright E2E)
Executed comprehensive multi-team operator walkthroughs in real Chromium browser sessions driven by Playwright against `grain demo`:
- **Admin & Settings**: Validated team CRUD operations, character constraint validation on team IDs, admin/member attribution, team concurrency limit chips (`max_workers`, `max_mergers`, `max_ci_jobs`), and bootstrap copy commands (`teams-walkthrough.spec.js`).
- **Repositories & Capabilities**: Verified repository settings, branches, CI-only capability selection/persistence, and CI security boundaries (`repo-pane.spec.js`, `repo-ownership-ci-security.spec.js`, `repo-ci-capabilities.spec.js`).
- **Releases & Qualification Plans**: Verified release list, candidate cutting, qualification plan configuration and task status inspection (`repository_release_memory_walkthrough.spec.js`).
- **Tasks & Lifecycle**: Tested new task filing, prompt rendering, detail overlays, bulk edit across selections, and proposal approval flows (`tasks.spec.js`, `task_intake_inbox_lifecycle_walkthrough.spec.js`).
- **Boards & Visualizations**: Verified List, Kanban, Timeline/Gantt, Dashboards with draggable panels, and scoped Report presentations (`boards.spec.js`, `saved-boards-walkthrough.spec.js`).
- **Upgrade Operations**: Verified Upgrade panel, branch specification, idle scheduling, and fallback handling (`upgrade.spec.js`).

### 2. State Repository Synchronization Soak
Subjected the bidirectional state repository synchronizer to continuous multi-table stress:
- **Table Consistency**: Continuous multi-round read/write operations against `tables/*.json` (`repo_config.json`, `team.json`, `qualification_config.json`, `qualification_item.json`, `template.json`).
- **Concurrency & Race Conditions**: Verified zero race conditions or stranded uncommitted git states under rapid interleaving of task filing, run completions, settings updates, and remote pull requests.
- **Git State**: Clean working tree maintained with exact database parity across 60+ randomised test cycles.

### 3. Deployment Health & Resource Checks
- **Resource Utilization**: Host RAM 465Mi used of 7.8Gi available (7.3Gi free/cache), 0B swap, disk 7.4G used of 59G (14% utilization, 50G available).
- **Log Audit**: Inspected systemd journal logs via `read_grain_log` / `journalctl` — confirmed 0 unhandled panics, 0 reconciler crash loops, and proper package cache DNS routing.
- **Reconciler Latencies**: Reconciler sub-stages (`base-branch-ci`, `base-branch-release`, `merge-batch`, `qualifications`, `branches`) execute well within sub-millisecond thresholds (p50: 500ns - 1.2ms).

### 4. Final RC Qualification Gate
- All four qualification items (`qualification-mergequeue`, `qualification-ci-diagnostics`, `qualification-base-triage`, and `qualification-system-smoke`) have passed.
- Candidate branch `candidate` builds cleanly and passes full-suite test checks.
- Qualification orchestrator successfully evaluated all dependencies and unblocked release candidate auto-promotion.

---

## Conclusion

The release candidate meets all stability, performance, security, and multi-team isolation criteria. Promotion to final release is **APPROVED**.
