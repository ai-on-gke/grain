# Shared CI Fault Ownership, Queue-Order Attribution, and Blocked-Owner Promotion

Plan: `docs/test-plans/2026-10-22.md` §5. Task 216 (`/tasks/216`, filed by task 175 `/tasks/175`). Run 2026-10-23, 10:15–10:55 UTC.

## Base

Branch `grain/049f1e09/task-216`, cut from `candidate` at `78d2b0283`. Commits under test on `candidate`:
- `ff3c19557` (`fix(ci): assign shared CI fault ownership in queue order and stream Go test progress`)
- `7c5407ad6` (`fix(ci): promote shared CI fault ownership when owner is blocked and stream go test heartbeats`)
- `e387ad3ff` / `27ba1b893` (`fix(ci): stream go test progress and heartbeats during CI runs`)
- `0aa2a891d` (`fix(ci): handle race boundaries and repeated test runs in run-go-tests.sh`)

## Environment

- **End-to-end multi-task shared CI fault ownership & blocked-owner promotion (§5.1)**:
  - `tests/e2e/shared_ci_fault_ownership_walkthrough_test.go` (`TestSharedCIFaultOwnershipAndBlockedOwnerPromotionWalkthrough`).
  - Runs `orchestrator.RunCycle` over a real SQLite store (`pkg/model/sqlite`), a real bare Git repository served through `gitproxy` and `githubsim`, the daemon's real CI service (`daemon.BuildCIService` with `DirCache` job caching and `daemon.CiMergeQueuePriority` priority ranking), the real UI/API server (`ui.NewServer`), and real `grain mcpserver` subprocesses spoken to over stdio JSON-RPC for tasks `/tasks/1`, `/tasks/2`, and `/tasks/3`.
- **Live Go test progress & heartbeat streaming (§5.2)**:
  - `tests/deploy/run_go_tests_test.go` (`TestRunGoTests*`, 9 tests) plus a live execution against a temporary Go module fixture with `GRAIN_GOTEST_HEARTBEAT_SECONDS=1` and `GRAIN_GOTEST_LOG`.

### Stubs (external boundaries only)

| Boundary | Stand-in |
|---|---|
| GitHub | `githubsim` backed by a real bare Git repository (`w.bare`) behind `gitproxy`. |
| Agent execution | Scripted `antigravity` steps pushing real commits over `gitproxy` (`repairScript`), plus real `grain mcpserver` processes invoking `wait_for_checks` and `get_ci_log` against `ui.NewServer`. |
| CI job machine | `baseMarkerSandboxes` (`localCISandbox`, executing workflow steps via `sh` on the host against `$E2E_BARE` and `$E2E_COMMIT`). |
| Workflow source | `bareRepoCISource`, reading `.grain/workflows/checks.yml` out of the bare Git repository. |

---

## Walkthrough & Test Results

### 1. Multi-Task Shared CI Fault Ownership & Blocked-Owner Promotion (`tests/e2e/shared_ci_fault_ownership_walkthrough_test.go`)

#### Step 1.1 — Three Auto-Merge Tasks Hitting an Identical Shared CI Failure Digest
- **Set-up**:
  - `acme/widgets` `main` is seeded with `.grain/workflows/checks.yml` (`lint` job, step `golangci-lint`) and a broken `pkg/foo/bar.go` referencing `missingSharedSymbolAcrossBranches()`, which fails with:
    ```
    pkg/foo/bar.go:42:12: undefined: missingSharedSymbolAcrossBranches
    FAIL	github.com/acme/widgets/pkg/foo [build failed]
    ```
  - `WatchDefaultBranch` is disabled on `acme/widgets` so `BaseBranchCI` does not file a separate `repair-main` task, exercising pure queue-order shared-fault ownership across task branches.
  - Three auto-merge tasks `/tasks/1` (`grain/task-1`, PR `#9000`, `OrderKey=1048576`), `/tasks/2` (`grain/task-2`, PR `#9001`, `OrderKey=2097152`), and `/tasks/3` (`grain/task-3`, PR `#9002`, `OrderKey=3145728`) are queued in order on `(acme/widgets, main)` and run CI against their branch heads.

#### Step 1.2 — MCP `wait_for_checks` and `get_ci_log` Queue-Order Attribution
- **Execution**:
  - Real `grain mcpserver` subprocesses for `/tasks/1`, `/tasks/2`, and `/tasks/3` call `wait_for_checks` and `get_ci_log` (`job: "lint"`).
- **Observed Output**:
  - `/tasks/1` (`wait_for_checks`):
    ```
    Shared fault: lint is failing the same way on 2 other branches right now -- one fault, not this branch's.
      Also failing on: grain/task-3, grain/task-2
      Every one of them names: pkg/foo/bar.go
      Assigned to you (Task #1): [SHARED FAULT — ASSIGNED TO YOU] You are the first task in the queue hitting this failure (assigned to you to fix). Fix it on your branch so the merge queue can advance.

    [SHARED FAULT — ASSIGNED TO YOU]: You are the first task in the queue hitting this failure (assigned to you to fix). Fix it on your branch so the merge queue can advance.
    ```
  - `/tasks/1` (`get_ci_log`):
    ```
    grain CI run 27f0c02393801368, job lint (failure)
    Shared fault: lint is failing the same way on 2 other branches right now -- one fault, not this branch's. Also failing on: grain/task-3, grain/task-2. Assigned to you (Task #1) [SHARED FAULT — ASSIGNED TO YOU: Task #1 is first in queue order; fix it on your branch.]
    ```
  - `/tasks/2` (`wait_for_checks`, and `/tasks/3` identically):
    ```
    Shared fault: lint is failing the same way on 2 other branches right now -- one fault, not this branch's.
      Also failing on: grain/task-3, grain/task-1
      Every one of them names: pkg/foo/bar.go
      Assigned to Task #1 ([Task #1](/tasks/1)) ahead of you in the queue: do not attempt to fix this on your branch.

    At least one failure above is a shared fault already assigned to Task #1 ([Task #1](/tasks/1)) ahead of you in the queue — it is not this branch's own. Do not attempt to fix this on your branch; the merge queue will re-test your branch once Task #1 lands.
    ```
  - `/tasks/2` (`get_ci_log`):
    ```
    grain CI run 7fe84bed8dba3b21, job lint (failure)
    Shared fault: lint is failing the same way on 2 other branches right now -- one fault, not this branch's. Also failing on: grain/task-3, grain/task-1. Assigned to Task #1 ([Task #1](/tasks/1)) ahead of you in the queue; do not attempt to fix this on your branch.
    ```

#### Step 1.3 — Orchestrator Sync Cycle 1: Holding `/tasks/2` and `/tasks/3` Behind `/tasks/1` Without Spending Repairs
- **Execution**:
  - Ran one orchestrator cycle (`reconcileCI` + `SyncPullRequests`).
- **Outcome**:
  - `cifault.ResolveOwnerForBase(ctx, store, fault, "main")` resolved `("1", "grain/task-1")`.
  - `/tasks/1` was requeued for automatic repair (`StateQueued`, `RepairsSpent() == 1`) and ranked `ci.PriorityMergeHead` by `daemon.CiMergeQueuePriority`.
  - `/tasks/2` and `/tasks/3` were held in `StateCompleted` with `RepairsSpent() == 0`, tagged `base-ci-wait:1`, and linked via `LinkDependsOn` to `1`.

#### Step 1.4 — `/tasks/1` Repair Fails & Escalates to Merge-Blocked; `/tasks/2` Promoted to Fault Owner
- **Execution**:
  - `/tasks/1`'s repair dispatch pushed a commit to `grain/task-1` that left `missingSharedSymbolAcrossBranches()` in `pkg/foo/bar.go`.
  - On the next cycle after `/tasks/1`'s repair CI failed with the same `checks/lint` failure on the same base commit, `/tasks/1` escalated to `MergeQueueBlockedAt != nil` (`MergeQueueState: "merge_failed"`) while remaining open (`StateCompleted`).
- **Outcome**:
  - `cifault.ResolveOwnerForBase(ctx, store, fault, "main")` skipped merge-blocked `/tasks/1` and promoted `/tasks/2` (`("2", "grain/task-2")`).
  - Calling `wait_for_checks` and `get_ci_log` on `/tasks/2`'s `grain mcpserver` session immediately reported:
    - `Assigned to you (Task #2): [SHARED FAULT — ASSIGNED TO YOU] You are the first task in the queue hitting this failure (assigned to you to fix). Fix it on your branch so the merge queue can advance.`
    - `Assigned to you (Task #2) [SHARED FAULT — ASSIGNED TO YOU: Task #2 is first in queue order; fix it on your branch.]`
  - `daemon.CiMergeQueuePriority` immediately demoted blocked `grain/task-1` to `ci.PriorityNormal` and promoted `grain/task-2` to `ci.PriorityMergeHead`.
  - `waitOrRearm` removed stale `base-ci-wait:1` and `LinkDependsOn -> 1` from `/tasks/2` and `/tasks/3`, re-linked `/tasks/3` to `base-ci-wait:2` (`LinkDependsOn -> 2`, `RepairsSpent() == 0`), and requeued `/tasks/2` for automatic repair (`RepairsSpent() == 1`).

#### Step 1.5 — `/tasks/2` Fixes the Shared Fault and Merges; `/tasks/3` Unblocks, Re-arms, and Merges With Zero Repairs Spent
- **Execution**:
  - `/tasks/2`'s repair run replaced `missingSharedSymbolAcrossBranches()` in `pkg/foo/bar.go` with valid Go code and pushed `grain/task-2`.
  - `grain/task-2` passed `checks/lint` (`ConclusionSuccess`) and merged into `main`, closing `/tasks/2` (`StateClosed`).
  - When `/tasks/2` closed, `/tasks/3` unblocked, stripped `base-ci-wait:2` and `LinkDependsOn -> 2`, and executed `rearmAgainstBase`, merging updated `main` (carrying `/tasks/2`'s fix in `pkg/foo/bar.go`) into `grain/task-3` (advancing `grain/task-3` from `0063ed958826` to `1e3fc5e5ac7f`).
  - `grain/task-3` ran CI on the re-armed commit, passed `checks/lint`, and merged cleanly into `main` (`StateClosed`) with `dispatchCount("3") == 0` and `RepairsSpent() == 0`.
  - `/tasks/1` remained open and merge-blocked (`MergeQueueBlockedAt != nil`) because its earlier failed edit to `pkg/foo/bar.go` conflicted with `/tasks/2`'s fix on `main`.

---

### 2. Live Go Test Progress & Heartbeat Streaming (`scripts/run-go-tests.sh`)

- **Automated deploy suite**:
  - `go test -v ./tests/deploy -run TestRunGoTests` passed all 9 tests (`TestRunGoTestsScriptIsExecutableAndValidBash`, `TestRunGoTestsStreamsLiveProgressAndHeartbeatsIncrementally`, `TestRunGoTestsStaysCompactOnNoisyPassingRun`, `TestRunGoTestsSurfacesFailuresPanicsRacesAndPreservesExitStatus`, `TestRunGoTestsSupportsCompiledTestBinaryPlainOutput`, `TestRunGoTestsSurfacesSkipDiagnosticsInVerboseAndBinaryModes`, `TestRunGoTestsPreservesTailDiagnosticsAndIsolatesConcurrentFailingTests`, `TestRunGoTestsSurfacesIncompleteInFlightTestOnAbruptFailure`, `TestRunGoTestsRaceBoundaryAndRepeatedTestFailures`).
- **Live fixture walkthrough**:
  - Executed `GRAIN_GOTEST_HEARTBEAT_SECONDS=1 GRAIN_GOTEST_LOG="$TMPDIR/raw.log" ./scripts/run-go-tests.sh go test -json ./pkg/sample` against a two-test fixture (`TestSlowHeartbeatFixture` sleeping `2.2s` with noisy `t.Log` output, followed by `TestImmediateFailureExcerptFixture` failing with `t.Fatalf`).
  - **Streamed stdout** (exit code `1`):
    ```
    === running: go test ./pkg/sample ===
    [go test +1s] 0 pkg ok, 0 fail; in flight: example.com/walkthrough/pkg/sample:TestSlowHeartbeatFixture (1s)
    [go test +2s] 0 pkg ok, 0 fail; in flight: example.com/walkthrough/pkg/sample:TestSlowHeartbeatFixture (2s)
    [go test] --- FAIL: TestImmediateFailureExcerptFixture (0.00s) [example.com/walkthrough/pkg/sample]
    FAIL	example.com/walkthrough/pkg/sample	2.207s
    === failing tests ===
    --- FAIL: TestImmediateFailureExcerptFixture (0.00s) [example.com/walkthrough/pkg/sample]
        sample_test.go:14: diagnostic context before failure
        sample_test.go:15: expected promoted fault owner #2, got #1
    === panics ===
    === data races ===
    === per-package results ===
    FAIL	example.com/walkthrough/pkg/sample	2.207s
    ```
  - **Raw log capture (`GRAIN_GOTEST_LOG`)**:
    - Preserved the complete `=== RUN` / `--- PASS:` / `--- FAIL:` log, including `TestSlowHeartbeatFixture`'s passing `t.Log("noisy log line that stays in raw GRAIN_GOTEST_LOG only")` line that was kept out of the live progress stream.

---

## Bugs Found and Fixed

### Bug 1 — `pkg/ui/cifault.go`: `Client.CISharedFaults` Served Stale Fault List and Stale `OwnerTaskID` Across New Failures and Blocked-Owner Promotions Within the 30s Cache TTL
- **Symptom**:
  1. `Client.CISharedFaults` (`GET /api/ci/faults`, which `grain mcpserver`'s `wait_for_checks` and `get_ci_log` call via `ListGrainCIFaults`) cached `c.ciFaults` unconditionally for `ciFaultTTL` (`30s`). If `/tasks/1` finished its CI run first and called `wait_for_checks` when only one branch had failed so far, `c.ciFaults` cached an empty list `[]` for 30s; when `/tasks/2` failed a second later and called `wait_for_checks` or `get_ci_log`, `/api/ci/faults` returned the cached `[]` instead of detecting the shared fault.
  2. Once `c.ciFaults` cached a shared fault with `OwnerTaskID: "1"`, if `/tasks/1` became merge-blocked (`MergeQueueBlockedAt != nil`), failed, or closed within 30s, `CISharedFaults` returned the cached `OwnerTaskID: "1"` without re-evaluating `cifault.ResolveOwner(ctx, c.Store, fault)`. As a result, promoted owner `/tasks/2` calling `wait_for_checks` or `get_ci_log` within 30s was still told `"Assigned to Task #1 ahead of you in the queue: do not attempt to fix this on your branch"` instead of `"Assigned to you (Task #2): [SHARED FAULT — ASSIGNED TO YOU]"`.
- **Fix**:
  - Keyed `ciFaultCache` in `pkg/ui/cifault.go` by a lightweight fingerprint (`failedRunsKey`) of the recent failed run summaries from `runner.List()` so any new failed run or job conclusion immediately invalidates the grouping cache.
  - On cache hits within `ciFaultTTL`, re-resolved `OwnerTaskID, OwnerRef = cifault.ResolveOwner(ctx, c.Store, cached[i].SharedFault)` on a copy of the cached slice while preserving the cached `Fixes` slice (avoiding any extra GitHub PR file API calls).
- **Regression test**:
  - `TestCISharedFaultsRefreshesOnNewFailedRunAndBlockedOwnerPromotion` in `pkg/ui/cifault_test.go` and `TestSharedCIFaultOwnershipAndBlockedOwnerPromotionWalkthrough` in `tests/e2e/shared_ci_fault_ownership_walkthrough_test.go`.

### Bug 2 — `pkg/orchestrator/mergequeueheads.go` & `pkg/daemon/ci.go`: Blocked Fault Owners Retained `PriorityMergeHead` and Delayed Promoted Owner Priority
- **Symptom**:
  1. In `pkg/orchestrator/mergequeueheads.go`, the `base-ci-wait:<id>` scan checked `st == model.StateClosed` on `ownerID`, but did not skip `st == model.StateFailed` or `obs.MergeQueueBlockedAt != nil` (nor did the `MarkedBaseRepairTag` loop skip `obs.MergeQueueBlockedAt != nil`). Before the next sync stripped stale `base-ci-wait:<id>` tags, `MergeQueueHeads` continued granting `PriorityMergeHead` to a blocked or failed former owner.
  2. In `pkg/daemon/ci.go`, `ciQueueHeads.read()` cached `h.heads` unconditionally for `ciQueueHeadsTTL = 5 * time.Second`, delaying `PriorityMergeHead` promotion when `/tasks/1` became merge-blocked and `/tasks/2` was promoted within 5 seconds.
- **Fix**:
  - Updated `pkg/orchestrator/mergequeueheads.go` to skip tasks in `model.StateFailed` or with `obs.MergeQueueBlockedAt != nil` when scanning `MarkedBaseRepairTag` and `base-ci-wait:` tags.
  - Updated `ciQueueHeads.read()` in `pkg/daemon/ci.go` to hold `ciQueueHeadsTTL` only when `h.failed` is true (as a store-error backoff), so queue head and fault-owner promotions take effect immediately at the CI gate.
- **Regression tests**:
  - `TestMergeQueueHeadsIncludesBaseRepairsAndSharedFaultOwners` in `pkg/orchestrator/mergequeueheads_test.go` and `TestCIMergeQueuePriorityRanksTheMergeQueueHeadAboveEverythingElse` in `pkg/cli/ci_priority_test.go`.
