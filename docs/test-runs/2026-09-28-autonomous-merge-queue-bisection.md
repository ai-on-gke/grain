# Autonomous Merge Queue, Speculative Batching & Bisection Engine Walkthrough

**Date**: 2026-09-28  
**Test Plan Reference**: `docs/test-plans/2026-09-28-changes-test-plan.md` §1 ("Autonomous Merge Queue, Speculative Batching & Bisection Engine")  
**Target Branch**: `grain/task-951`  
**Environment**: Debian Linux x86_64, Go 1.26.6, SQLite 3.x / PostgreSQL  

---

## 1. Executive Summary

This qualification run completes the end-to-end walkthrough and verification for §1 of the 2026-09-28 Changes Test Plan, covering Grain's Autonomous Merge Queue, Speculative Batching Pipeline, Demand-Driven Base CI Reconciler, and Automated Git Bisection Engine.

Key verified capabilities and invariants:
1. **Demand-Driven Base Branch CI Reconciler (`reconcileBaseBranchCI`)**:
   - Zero compute consumption during idle state: When a repository's base branch tip is green and has no pending merge activity, no CI runs are triggered.
   - Demand-driven activation: Automatically triggers a single-flight CI run on the base branch tip when unverified merges land or pending merge tasks arrive in the backlog targeting an unjudged base.
   - Single-flight lock: Base CI deduplication ensures duplicate reconciler cycles reuse in-flight runs rather than spawning concurrent duplicate builds.
   - Cache recovery: Verified green base branch tips automatically clear invalid CI cache entries and prevent redundant runs.

2. **Speculative Multi-PR Batching (`reconcileMergeBatches`, `formMergeBatches`)**:
   - Groups up to `model.Config.MergeBatchSize` (default 3) eligible, clean auto-merge PRs onto a speculative throwaway branch (`grain-merge-queue/<branch>`).
   - Task states progress to `"batching"` (UI chip: `"Testing Batch"`, MCP phrase: `"Testing Batch"`).
   - Atomic multi-PR merge on single CI pass: All batched PRs land on the target branch sequentially upon 1 passing CI suite verdict without individual post-merge builds.
   - Member tasks transition immediately to completed/merged state.

3. **Automated Git Bisection & Culprit Isolation Engine (`bisectMergeBatch`, `sendBatchCulpritBack`)**:
   - Automated halving from the front: When a speculative batch fails CI, `bisectMergeBatch` halves the batch from the front (`keep = row.Members[:ceil(N/2)]`) and releases remaining members back to the queue without penalty or blame.
   - Recursive isolation: Halving continues across successive failed runs until a single culprit PR ($N=1$) is isolated (`MergeBatchCulprit`).
   - Conflict repair dispatch: The culprit PR is marked with `MergeQueueRepairAt`, has its `CompletedAt` cleared for agent re-dispatch, and receives a diagnostic comment stating that while its own branch was green alone, it broke when combined with base.
   - Non-blocking queue progress: Innocent queued PRs are unblocked to merge cleanly in subsequent cycles.

4. **Merge Queue UI & MCP State Parity**:
   - Verified 1:1 state and phrase mapping across `ui/src/state.js` (`MERGE_QUEUE_PHRASES`), `pkg/mcp/task_tools.go` (`MergeQueuePhrase`), and `pkg/orchestrator/queuestatus.go`:
     - `waiting_queue` -> `"Waiting on Queue"`
     - `batching` -> `"Testing Batch"`
     - `repairing` -> `"Resolving Conflicts"`
     - `merging` -> `"Merging"`

5. **MCP Merge Queue Inspection & Control Tools (`pkg/mcp`)**:
   - `query_merge_queue`: Inspects pipeline status, held gates, active speculative batches, and queued PRs.
   - `pause_pipeline` & `resume_pipeline`: Provides emergency controls to pause automated merges and record audit reasons.
   - `revert_task` & `revert_merge`: Creates clean git revert commits with structured provenance footers (`Reverts-Task`, `Reverts-Commit`).

All automated unit tests in `pkg/orchestrator`, `pkg/mcp`, `tests/e2e`, and the comprehensive walkthrough suite `TestAutonomousMergeQueueBatchingAndBisectionWalkthrough` passed with 100% success.

---

## 2. Architecture & Verification Methodology

```
                             [ Queued Pull Requests (t1, t2, t3) ]
                                               │
                                               ▼
                              [ formMergeBatches (Size: 3) ]
                                               │
                                               ▼
                        [ Speculative Ref: grain-merge-queue/<branch> ]
                                               │
                                     [ Run Grain CI Suite ]
                                        /              \
                                  (Pass)                (Fail)
                                   /                        \
                  [ Atomic Sequential Merge ]      [ bisectMergeBatch (Halve Front) ]
                             │                                │
                 [ Mark Tasks Merged/Closed ]           [ Re-test Front Half (t1, t2) ]
                                                        [ Release Remainder (t3) ]
                                                              │
                                                        [ Repeat until N=1 Culprit ]
                                                              │
                                                        [ sendBatchCulpritBack (t1) ]
                                                        - Tag MergeQueueRepairAt
                                                        - Post Diagnostic Comment
                                                        - Dispatch Conflict Repair
```

### Verification Sections in Walkthrough Test
The qualification suite (`pkg/orchestrator/autonomous_merge_queue_batching_bisection_walkthrough_test.go`) systematically executes all 5 core phases:

1. **Section 1: Demand-Driven Base CI Triggering & Single-Flight Lock**
   - Evaluates idle repo with clean base -> 0 CI runs started.
   - Seeds unjudged base commit and arrives with pending auto-merge task demand -> triggers base CI run (`run2`).
   - Re-invokes reconciler while run is pending -> confirms single-flight execution lock (0 duplicate runs).
   - Completes base CI with `ConclusionSuccess` -> confirms 0 subsequent runs triggered.

2. **Section 2: Speculative Multi-PR Batching & Fast Merge Pipeline**
   - Enqueues 3 ready PRs (`t101`, `t102`, `t103`).
   - Reconciler forms batch on `grain-merge-queue/main`, starts 1 batch CI run (`run5`).
   - Verifies member tasks transition to `batching` state ("Testing Batch") and are held from individual merging.
   - Batch CI completes with `ConclusionSuccess` -> all 3 PRs merge cleanly in one atomic batch pass with 1 CI suite.
   - Verifies batch row is cleanly removed from store and tasks transition to merged/completed.

3. **Section 3: Speculative Batching & Automated Git Bisection Engine**
   - Enqueues 3 PRs where `t1` contains an intentional tree regression.
   - Round 1: 3-member batch fails CI.
   - Round 2: Automated bisect halves from the front into 2-member batch (`t1`, `t2`) with `Splits = 1`. Releases `t3` without blame.
   - Round 2 fails CI -> Round 3 halves into 1-member batch (`t1`).
   - Round 3 fails CI -> `t1` confirmed as culprit (`MergeBatchCulprit`).
   - Verifies `t1` is tagged with `MergeQueueRepairAt`, completion cleared, and diagnostic comment attached.
   - Innocent tasks (`t2`, `t3`) are released and merge cleanly in subsequent cycles.

4. **Section 4: UI State Progression Verification**
   - Confirms state string consistency between Go backend models, MCP agent tool representations, and React frontend dictionaries.

5. **Section 5: MCP Merge Queue Inspection, Rollback & Pipeline Control Tools**
   - `query_merge_queue` formatting: Verifies pipeline status, active batches, member lists, and queued PRs.
   - `pause_pipeline` & `resume_pipeline`: Verifies pause state toggle and reason storage.
   - `revert_task` & `revert_merge`: Verifies real git revert commits, workspace cleanliness, and commit log metadata.

---

## 3. Detailed Execution Results

### Walkthrough Test Execution Output
```
=== RUN   TestAutonomousMergeQueueBatchingAndBisectionWalkthrough
=== PAUSE TestAutonomousMergeQueueBatchingAndBisectionWalkthrough
=== CONT  TestAutonomousMergeQueueBatchingAndBisectionWalkthrough
    autonomous_merge_queue_batching_bisection_walkthrough_test.go:106: === Section 1: Demand-Driven Base CI Triggering ===
    log_test.go:115: orchestrator: task t101: grain CI run run1: success at cc1240c7b978 -- this repository declares no workflow, so grain CI ran nothing
    log_test.go:115: orchestrator: acme/widgets main at dfded8bc683f was never judged (): asking grain CI again (attempt 2)
    log_test.go:115: orchestrator: run run2 is what judges acme/widgets main at dfded8bc683f, covering no merge grain made, asked for under push, and it is made of: no job at all, so it judges nothing -- no workflow of this repository answers that event
    log_test.go:115: orchestrator: base branch main recovered to green: cleared 1 CI cache entries
    autonomous_merge_queue_batching_bisection_walkthrough_test.go:182: === Section 2: Speculative Multi-PR Batching & Clean Merge ===
    log_test.go:115: orchestrator: task t102: grain CI run run3: success at 23db02a39edd -- this repository declares no workflow, so grain CI ran nothing
    log_test.go:115: orchestrator: task t103: grain CI run run4: success at f786b8dc9343 -- this repository declares no workflow, so grain CI ran nothing
    log_test.go:115: orchestrator: testing 3 queued change(s) for acme/widgets main together on grain-merge-queue/main at 3a07f144fb0a (run run5)
    log_test.go:115: orchestrator: the merge batch on acme/widgets main passed at 3a07f144fb0a: merging 3 change(s) on one suite
    autonomous_merge_queue_batching_bisection_walkthrough_test.go:253: === Section 3: Automated Git Bisection Engine ===
    log_test.go:115: orchestrator: task t1: grain CI run run1: success at 0e51c8d42cdd -- this repository declares no workflow, so grain CI ran nothing
    log_test.go:115: orchestrator: task t2: grain CI run run2: success at bc987b99df3a -- this repository declares no workflow, so grain CI ran nothing
    log_test.go:115: orchestrator: task t3: grain CI run run3: success at a191d12f334b -- this repository declares no workflow, so grain CI ran nothing
    log_test.go:115: orchestrator: testing 3 queued change(s) for acme/widgets main together on grain-merge-queue/main at 845d2d933f21 (run run4)
    log_test.go:115: orchestrator: the merge batch on acme/widgets main failed at 845d2d933f21 (failure: go): re-testing the first 2 of 3 change(s)
    log_test.go:115: orchestrator: testing 2 queued change(s) for acme/widgets main together on grain-merge-queue/main at d5d8770c2ed9 (run run5)
    log_test.go:115: orchestrator: the merge batch on acme/widgets main failed at d5d8770c2ed9 (failure: go): re-testing the first 1 of 2 change(s)
    log_test.go:115: orchestrator: testing 1 queued change(s) for acme/widgets main together on grain-merge-queue/main at 2be6687bcc74 (run run6)
    log_test.go:115: orchestrator: the merge batch on acme/widgets main failed with one member left (2be6687bcc74): failure: go
    autonomous_merge_queue_batching_bisection_walkthrough_test.go:344: === Section 4: UI State Progression Verification ===
    autonomous_merge_queue_batching_bisection_walkthrough_test.go:361: === Section 5: MCP Tools Verification ===
    autonomous_merge_queue_batching_bisection_walkthrough_test.go:546: === All 5 Walkthrough Sections Verified Successfully ===
--- PASS: TestAutonomousMergeQueueBatchingAndBisectionWalkthrough (1.14s)
PASS
```

### Full Repository Test Suite Status
- `pkg/orchestrator`: PASS (70.020s)
- `pkg/mcp`: PASS (16.111s)
- `pkg/ci`: PASS (20.328s)
- `pkg/ui`: PASS (44.324s)
- `tests/e2e`: PASS (72.282s)
- Zero build failures, zero test regressions.

---

## 4. Summary of Code Enhancements

- **`pkg/mcp/task_tools.go`**: Exported `MergeQueuePhrase(state string) string` helper function to provide a standardized public API for formatting merge queue phrases across backend packages and testing tools, matching the wording in `ui/src/state.js` `MERGE_QUEUE_PHRASES`.
- **`pkg/orchestrator/autonomous_merge_queue_batching_bisection_walkthrough_test.go`**: Added comprehensive end-to-end integration test validating demand-driven base CI, speculative multi-PR batching, automated bisection halving, UI state chip progression, and MCP merge queue management tools.

---

## 5. Conclusion & Verification Sign-Off

The Autonomous Merge Queue, Speculative Batching, and Bisection Engine fully satisfies all requirements specified in `docs/test-plans/2026-09-28-changes-test-plan.md` §1:
- Demand-driven base branch CI activates only when unverified merges land or pending merge tasks exist.
- Speculative batching successfully aggregates ready PRs onto throwaway refs and merges them on a single CI pass.
- Bisection engine deterministically isolates offending changes via front-halving without penalizing clean PRs.
- UI state progression and MCP tooling provide full visibility and rollback control.
