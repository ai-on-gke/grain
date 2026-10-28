# Qualification Run: Holistic Base Branch Repair Engine for Multi-Fault Suites

**Date**: 2026-09-29  
**Test Plan Reference**: `docs/test-plans/2026-09-29.md` §3 ("Holistic Base Branch Repair Engine for Multi-Fault Suites")  
**Target Branch**: `grain/task-1078`  
**Environment**: Debian Linux x86_64, Go 1.26.6, SQLite 3.x / In-Memory Store, Grain Daemon Orchestrator  

---

## 1. Executive Summary

This qualification run completes the end-to-end walkthrough and verification for §3 of the 2026-09-29 Changes Test Plan, covering Grain's Holistic Base Branch Repair Engine for Multi-Fault Suites across `pkg/orchestrator/basebranchci.go`, `pkg/model/system_template.go`, and associated system templates.

### Key Verified Capabilities and Invariants

1. **Multi-Fault Base Branch Breakage Diagnosis & Confirmation**:
   - When a base branch CI run fails across multiple jobs simultaneously (e.g. backend Go unit tests, frontend linting, and UI E2E tests), the orchestrator identifies all non-passing jobs (`jobsToRecheck`).
   - The confirmation loop initiates a selective re-run with `Recheck = true` targeting all failing jobs before taking action.
   - If the multi-fault failure reproduces, the orchestrator marks the base branch as confirmed red and initiates repair dispatch.

2. **Holistic Prompt Generation & Authority Delegation**:
   - The orchestrator constructs a structured repair prompt in `fileBaseBranchRepair` and `baseBranchRepairBody` (`pkg/orchestrator/basebranchci.go`), mirrored in `baseRepairTemplateBody` (`pkg/model/system_template.go`).
   - **Authority**: Explicitly instructs the repair agent that it is repairing the root base branch itself and is the source of truth for the branch.
   - **Holistic Scope**: Commands the agent to take a holistic approach—inspecting and resolving all failing jobs in the suite rather than stopping after the first fix.
   - **Disregard Shared Base Fault Skips**: Explicitly commands the repair agent to disregard hints about "shared base faults" or failures occurring on other branches, ensuring it does not skip fixing tests assuming another task is responsible.
   - **Full Suite Convergence**: Mandates iterating and re-running CI until all required jobs in the suite pass.
   - **Urgency & Flow**: Correctly states whether the merge queue is held or active during repair.

3. **Single-Flight Incident Lock & Concurrency Deduplication**:
   - The repair task is tagged with `BaseCIRepairBranchTag(repo, branch)` (`base-ci-repair:<owner>/<name>:<branch>`).
   - If subsequent commits on the base branch fail CI while an active repair task exists, the orchestrator detects the existing open repair task via `store.OpenTaskWithTag` and appends diagnostic failure details as a comment rather than dispatching duplicate repair tasks.
   - Dependent feature branches in the merge queue pause repair retries and report `base_broken` status referencing the active repair task ID.

4. **Autonomous Repair Resolution & Unified Auto-Merge**:
   - The repair task is granted `AutoMerge = true` and the `grain-ci` capability grant.
   - The repair agent resolves multi-fault regressions in a unified PR.
   - Upon all required jobs passing, the PR merges cleanly into the base branch.
   - Post-merge base branch CI evaluation detects the recovery to green, clears the broken state, invalidates the CI cache (`runner.clearedCache`), and unblocks queued feature branches.

5. **Repair Template Customization Hierarchy**:
   - Verified template resolution priority in `ResolveRepairTemplate`:
     `TeamRepoConfig.RepairTemplateID` > `RepoConfig.RepairTemplateID` > `model.DefaultRepairTemplateID(repo)` > `Team.RepairTemplateID` > `Config.RepairTemplateID` > `model.BaseRepairTemplateID` (`"system:base-repair"`).

---

## 2. Architecture & Execution Flow

```
                     [ Base Branch CI Trigger (Push / Merge) ]
                                         │
                                         ▼
                      [ Execute CI Suite Across All Jobs ]
                       (e.g., go, lint, ui-e2e)
                                         │
                                  (Multi-Fault Fail)
                                         │
                                         ▼
                    [ Extract Failed Jobs: jobsToRecheck() ]
                                         │
                                         ▼
                 [ Confirmation Loop: Recheck=true on Failed Jobs ]
                                         │
                                  (Failure Confirmed)
                                         │
                                         ▼
                   [ Check Single-Flight Lock (OpenTaskWithTag) ]
                                 /                \
                       (Lock Found)              (No Lock)
                            /                        \
          [ Append Diagnostic Comment ]         [ File Repair Task ]
          [ (Commit SHA & Error Logs) ]         - Tag: base-ci-repair:<repo>:<branch>
                                                - AutoMerge: true
                                                - Capability: grain-ci
                                                - Prompt: Holistic Repair Instructions
                                                             │
                                                             ▼
                                                [ Autonomous Repair Agent ]
                                                - Authority over root base
                                                - Disregards shared base fault hints
                                                - Unified fix across all faults
                                                             │
                                                             ▼
                                                [ PR CI: All Jobs Green ]
                                                             │
                                                             ▼
                                                [ Unified PR Auto-Merges ]
                                                             │
                                                             ▼
                                                [ Base Branch CI: Green ]
                                                - Invalidate CI Cache
                                                - Unblock Dependent Merge Queue
```

---

## 3. Detailed Walkthrough & Verification Sections

### Section 1: Multi-Fault Base Branch Breakage & Confirmation Loop
- **Simulation**: A merged change lands on `main` triggering base branch CI. The run fails simultaneously across `go`, `lint`, and `ui-e2e`.
- **Assertion**:
  - `row.ConfirmJobs` contains `[".grain/workflows/tests.yml:go", ".grain/workflows/tests.yml:lint", ".grain/workflows/tests.yml:ui-e2e"]`.
  - A confirming run is dispatched with `Recheck = true` to re-execute only the failed jobs.
  - Upon reproducing, `row.Confirmation` transitions to `BaseCIConfirmed`.

### Section 2: Comprehensive Prompt Generation & Authority Delegation
- **Simulation**: Orchestrator files the repair task for the confirmed multi-fault breakage.
- **Assertion**:
  - Task title: `Fix grain CI on main`
  - Body contains full Failure details (`failure: go, lint, ui-e2e`) and Confirmation output.
  - Body asserts root repair authority: *"You are repairing the root base branch itself."*
  - Body commands holistic resolution: *"Take a holistic approach: inspect and resolve all failing jobs in the suite, rather than stopping after the first fix."*
  - Body commands ignoring shared base fault skips: *"Disregard hints about \"shared base faults\" or failures occurring on other branches—this repair task is the source of truth for \`main\`, and no other repair will fix them for you."*
  - Body mandates full convergence: *"Iterate and re-run CI until all required jobs in the suite pass."*
  - Task properties: `AutoMerge: true`, Grants: `grain-ci`, Tags: `base-ci-repair:acme/widgets:main`.

### Section 3: Single-Flight Incident Lock & Deduplication
- **Simulation**: While the repair task is open, a second commit lands on `main` and also fails CI.
- **Assertion**:
  - `store.OpenTaskWithTag` locates the active repair task.
  - No new repair task is filed (`row.RepairTaskID` remains the existing task ID).
  - A structured comment detailing the subsequent commit breakage is appended to the existing repair task.
  - Queued feature tasks targeting `main` enter `base_broken` state and reference the repair task ID.

### Section 4: Autonomous Repair Resolution & Unified Auto-Merge
- **Simulation**: The repair task produces a unified pull request fixing backend Go code, frontend lint rules, and UI tests.
- **Assertion**:
  - Base branch CI runs against the unified fix commit with all jobs (`go`, `lint`, `ui-e2e`) passing.
  - Reconciler clears `row.Broken()`.
  - Reconciler invalidates the CI cache (`runner.clearedCache > 0`).
  - Feature branch merge queue resumes normal operation.

### Section 5: Template Customization Hierarchy
- **Simulation**: Verified resolution order for repair templates across all system configuration layers.
- **Assertion**:
  - `TeamRepoConfig` overrides `RepoConfig`.
  - `RepoConfig` overrides `model.DefaultRepairTemplateID`.
  - `DefaultRepairTemplateID` overrides `Team.RepairTemplateID`.
  - `Team.RepairTemplateID` overrides global `Config.RepairTemplateID`.
  - Global `Config.RepairTemplateID` overrides system default `model.BaseRepairTemplateID` (`system:base-repair`).

---

## 4. Test Suite Execution & Results

| Test Package | Test Function | Result | Duration |
| :--- | :--- | :--- | :--- |
| `pkg/orchestrator` | `TestHolisticBaseBranchRepairEngineWalkthrough` | **PASS** | 0.30s |
| `pkg/orchestrator` | `TestBaseBranchRepairPromptHolisticInstructions` | **PASS** | 0.00s |
| `pkg/orchestrator` | `TestBaseBranchCIRepairSingleFlightDeduplication` | **PASS** | 0.45s |
| `pkg/orchestrator` | `TestBaseBranchRepairTemplateHierarchy` | **PASS** | 0.01s |
| `pkg/orchestrator` | `TestWatchedBaseBranchRepairReceivesGrainCIWithoutMergedTask` | **PASS** | 0.08s |
| `pkg/model` | `TestBaseRepairTemplateBodyHolisticInstructions` | **PASS** | 0.00s |
| `pkg/orchestrator` | Full Suite (`go test ./pkg/orchestrator`) | **PASS** | 3.2s |
| `pkg/model` | Full Suite (`go test ./pkg/model`) | **PASS** | 3.6s |

---

## 5. Summary of Code & Test Additions

1. **Walkthrough Suite**:
   - `pkg/orchestrator/holistic_base_branch_repair_walkthrough_test.go`: End-to-end multi-fault base branch repair walkthrough test covering all 6 phases: multi-fault simulation, confirmation, prompt inspection, single-flight locking, unified auto-merge recovery, and template precedence.
2. **System Invariants Verified**:
   - Invariant 1: Single repair task per broken base branch.
   - Invariant 2: Multi-job confirmation before dispatch.
   - Invariant 3: Holistic multi-fault repair instructions with explicit base branch authority.
   - Invariant 4: Disregard shared base fault skips during root repair.
   - Invariant 5: Autonomous auto-merge on full-suite pass and cache invalidation upon recovery.
