# Test Run: Merge-Queue Overlap Re-Check, Abandon/Base-Hold Metrics, Base-CI Red Streak & Release Gates

- **Date:** 2026-10-26
- **Plan Reference:** `docs/test-plans/2026-10-26.md` §3
- **Task:** [Task #1201](/tasks/1201) (origin [Task #1140](/tasks/1140))
- **Branch:** `grain/049f1e09/task-1201`
- **Target Commits Under Test:**
  - Overlap-gated merge re-check (`merge_recheck: off | observe | enforce`): `9babc06a6`, `d0eda8d93`, `c082d6559`, `8c3189dbf`, `086db08cb` (PR #4810)
  - Merge batch abandon reasons & `base_hold_history` metrics: `c5aa70074`, `e376d22fe` (PR #4792)
  - Base-branch red streak confirmation: `8c9183d0b` (PR #4829)
  - `PriorityReleaseGate` & `infraVerdict` auto-rerun: `10b31f7d6` (PR #4877)
  - Red-CI procedure & `FixesBaseBranch` repair prompts: `bae0509ca`, `7d049ee38` (PR #4849)

---

## 1. Executive Summary

We executed the end-to-end walkthrough and verification plan defined in `docs/test-plans/2026-10-26.md` §3 against a live `grain demo -github-sim -fake-ci -merge-rules -base-branch-ci` daemon, the `grain metrics` CLI, SQLite store inspection, and the orchestrator/UI/metrics end-to-end test harnesses (`githubsim` + `fakeCI` + `orchestrator.Reconcilers()`).

During the walkthrough, we identified and fixed **two bugs** in the tree:

1. **Bug 1 — Missing `m.Rechecks` rendering in `pkg/daemon/metrics.go` (`grain metrics` CLI and `read_grain_metrics` MCP tool):**
   Commit `c082d6559` (`feat(metrics): overlap re-check counters in the merges section`) wired `ui.MetricsMergeRechecks` into `pkg/metrics/merges.go`, `pkg/ui/metrics.go` (`GET /api/metrics` JSON), and `pkg/mcp/metrics_tools.go` (`metricsSectionSummary[MetricsSectionMerges]`), and its commit message stated that `read_grain_metrics`'s `merges` section gained `rechecks`. However, `pkg/daemon/metrics.go` (`printMerges`) was never updated to render `m.Rechecks` or include `m.Rechecks.Merges == 0` in its early-return guard, so `grain metrics` and `read_grain_metrics` silently omitted overlap re-check statistics.
   - **Fix:** Added `m.Rechecks.Merges == 0` to `printMerges`'s early-return check and added `printMergeRechecks` in `pkg/daemon/metrics.go`, with regression coverage in `pkg/cli/metrics_test.go` (`TestRenderMetricsRendersTheMergesSection`).

2. **Bug 2 — `FixesBaseBranch` repair tasks without `grain-ci` granted told to skip inherited base failures in `pkg/orchestrator/run.go`:**
   Commit `7d049ee38` added the carve-out `"unless repairing that branch is this run's own work"` to step 2 of `redCIProcedureSection()` for tasks holding `grain-ci`. However, for repositories using GitHub check runs (without `grain-ci` granted), `BuildPrompt` and `BuildSuspendedPrompt` checked `history.Repair` while `mergeRepairRun(task, obs)` only checked `task.Origin.Reason.Merger() || obs.RepairInFlight()` and ignored `model.FixesBaseBranch(task)`. As a result, a `FixesBaseBranch` task (whether grain-filed or operator/agent-marked via `fixes_base_branch: true`) without `grain-ci` got `RunConfig.Repair = false`, `History.Repair = false`, and the `BuildPrompt` `else` branch instructing it: *"A failure that is red there too is not yours to fix -- leave it alone"*.
   - **Fix:** Updated `mergeRepairRun`, `BuildPrompt`, and `BuildSuspendedPrompt` in `pkg/orchestrator/run.go` to check `model.FixesBaseBranch(task)`, and added regression coverage in `pkg/orchestrator/redci_procedure_internal_test.go` (`TestFixesBaseBranchRepairPromptsDoNotSkipInheritedBaseFailures`).

All existing and newly added unit, integration, and end-to-end walkthrough tests pass.

---

## 2. Environment & Test Plan Notes

### Sandbox Environment
- **OS:** Linux (Debian bookworm sandbox)
- **Build:** Built frontend (`npm --prefix ui ci && npm --prefix ui run build` synced to `pkg/ui/static/`) and `cmd/grain` (`go build -o /home/debian/grain ./cmd/grain`).
- **Packages installed during run:** `sqlite3` (`sudo apt-get update && sudo apt-get install -y sqlite3`) for direct inspection of the demo SQLite database (`grain.db`).

### Plan vs. Tree Notes
- **Test file location:** `docs/test-plans/2026-10-26.md` §3 references `tests/e2e/merge_recheck_test.go` (`8c3189dbf`, `086db08cb`); in the repository tree, commits `8c3189dbf` and `086db08cb` placed those tests in `pkg/orchestrator/mergerecheck_test.go` and `pkg/orchestrator/mergerecheck_replay_test.go`.
- **Overlap clause names:** The test plan prose mentions `same_go_package`; the canonical clause constants in `pkg/model/merge_recheck_history.go` are hyphenated:
  - `model.MergeRecheckClauseSameFile = "same-file"`
  - `model.MergeRecheckClauseSamePackage = "same-package"`
  - `model.MergeRecheckClauseGlobalInput = "global-input"`
  - `model.MergeRecheckClauseUnknown = "unknown"`
- **`grain demo` vs. Orchestrator Reconcilers:** `grain demo` serves the live UI/API server (`pkg/ui.Server`), `githubsim`, `fakeCI`, `/api/repos/{owner}/{name}/settings`, and `/api/e2e/base-branch/{red,green,clear}`, but does not run a background `orchestrator.RunCycle` loop. We verified the HTTP API and CLI against a live `grain demo` server and verified the full reconciler state machines (`SyncPullRequests`, `reconcileMergeBatches`, `reconcileBaseBranchCI`) against `githubsim` + `fakeCI` in `pkg/orchestrator`.

---

## 3. Walkthrough & Verification Results

### §3.1 Overlap-Gated Merge Re-Check (`merge_recheck: off | observe | enforce`, PR #4810)

#### 1. Repository Settings API (`GET/PUT /api/repos/{owner}/{name}/settings`)
Against a live `grain demo -github-sim -fake-ci -merge-rules -base-branch-ci` instance, we exercised `GET` and `PUT` on `/api/repos/acme/widgets/settings`:

| Step | Request | HTTP Status | Response `mergeRecheck` | `repo_config.merge_recheck` in SQLite | Outcome |
|---|---|---|---|---|---|
| Default read | `GET /api/repos/acme/widgets/settings` | `200 OK` | `"observe"` | `""` (empty string resolves to `observe` via `MergeRecheckOr()`) | **PASS** |
| Set `enforce` | `PUT {"mergeRecheck": "enforce"}` | `200 OK` | `"enforce"` | `"enforce"` | **PASS** |
| Set `off` | `PUT {"mergeRecheck": "off"}` | `200 OK` | `"off"` | `"off"` | **PASS** |
| Invalid value | `PUT {"mergeRecheck": "invalid-mode"}` | `400 Bad Request` | `{"error":"mergeRecheck: unknown merge re-check mode \"invalid-mode\" (want off, observe or enforce)"}` | `"off"` (unchanged) | **PASS** |
| Reset with `""` | `PUT {"mergeRecheck": ""}` | `200 OK` | `"observe"` | `""` | **PASS** |

#### 2. Overlap Rule Clauses & Mode Behavior (`off`, `observe`, `enforce`)
Against `githubsim` and `fakeCI` (`pkg/orchestrator/mergerecheck_test.go`), we advanced `main` between a PR's fork point `F` and `main`'s current tip `T` across all overlap and non-overlap cases:

| Scenario | PR Changed Path (`F..H`) | `main` Landed Path (`F..T`) | Mode | Triggered? | Clauses Recorded | Recheck Batch Started? | Landed? | Outcome |
|---|---|---|---|---|---|---|---|---|
| Non-overlapping | `change.go` | `NOTES.md` | `observe` | `false` (`0`) | `""` | No (`rechecked=0`) | Yes (direct) | **PASS** |
| Non-overlapping | `change.go` | `NOTES.md` | `enforce` | `false` (`0`) | `""` | No (`rechecked=0`, 0 extra CI runs) | Yes (direct) | **PASS** |
| Clause 1: Same file | `pkg/alpha/a.go` | `pkg/alpha/a.go` (non-conflicting lines) | `observe` | `true` (`1`) | `"same-file,same-package"` | No (`rechecked=0`) | Yes (direct) | **PASS** |
| Clause 1: Same file | `pkg/alpha/a.go` | `pkg/alpha/a.go` (non-conflicting lines) | `enforce` | `true` (`1`) | `"same-file,same-package"` | Yes (`batch` of 1 on `grain-merge-queue/<id>/main`, `rechecked=1`) | Yes (after batch green) | **PASS** |
| Clause 2: Same Go package | `pkg/alpha/a.go` | `pkg/alpha/b.go` | `observe` | `true` (`1`) | `"same-package"` (`pkg/alpha`) | No (`rechecked=0`) | Yes (direct) | **PASS** |
| Clause 2: Same Go package | `pkg/alpha/a.go` | `pkg/alpha/b.go` | `enforce` | `true` (`1`) | `"same-package"` (`pkg/alpha`) | Yes (`batch` of 1, `rechecked=1`) | Yes (after batch green) | **PASS** |
| Clause 3: Global input (`go.mod`) | `pkg/alpha/a.go` | `go.mod` | `observe` / `enforce` | `true` (`1`) | `"global-input"` | `observe`: No; `enforce`: Yes (`batch` of 1) | Yes | **PASS** |
| Clause 3: Global input (`.grain/workflows/ci.yml`) | `pkg/alpha/a.go` | `.grain/workflows/ci.yml` | `observe` / `enforce` | `true` (`1`) | `"global-input"` | `observe`: No; `enforce`: Yes (`batch` of 1) | Yes | **PASS** |
| Clause 3: Global input (`Makefile`) | `pkg/alpha/a.go` | `Makefile` | `observe` / `enforce` | `true` (`1`) | `"global-input"` | `observe`: No; `enforce`: Yes (`batch` of 1) | Yes | **PASS** |
| Enforce re-check catches breakage | `change.go` | `landed.go` | `enforce` | `true` (`1`) | `"same-package"` | Yes (`batch` fails + flake recheck fails -> `caught=1`) | No (sent back for repair: `MergeQueueRepairAt != nil`) | **PASS** |
| Mode `off` | `change.go` | `landed.go` | `off` | — | No row written to `merge_recheck_history` | No | Yes (direct) | **PASS** |

#### 3. Post-Merge Base CI Verdict Recording (`JudgeMergeRechecks`) & Metrics
- Verified (`TestPostMergeBaseBranchCIJudgesMergeRecheckRecords` in `pkg/orchestrator/mergerecheck_test.go`) that when a change merges and `collectBaseBranchCI` subsequently collects the post-merge base-branch CI run on `main`, `deps.Store.JudgeMergeRechecks(ctx, row.Repo, row.Branch, row.StartedAt, v)` updates `merge_recheck_history.base_verdict` from `""` to `"green"` (when `main` passes) or `"red"` (when `main` fails).
- When a skipped merge (`triggered = 0`) is followed by a red post-merge base CI verdict (`base_verdict = 'red'`), `SummarizeMergeRechecks` increments `SkippedBaseRed` (`skippedBaseWentRedAfter` in `GET /api/metrics` and `skipped, base went red` in `grain metrics`).

---

### §3.2 Merge Batch Abandon Reasons & `base_hold_history` Metrics (PR #4792)

#### 1. Merge Batch Abandon Reasons (`merge_batch_history.reason`)
We exercised all distinct merge-batch abandonment paths through `reconcileMergeBatches` (`TestAnAbandonedBatchRecordsWhyItWasAbandoned` in `pkg/orchestrator/mergebatch_test.go`, augmented with the `disabled` case) and verified both SQLite persistence (`merge_batch_history.reason` + `detail`) and metrics aggregation:

| Abandon Scenario | Trigger | `merge_batch_history.outcome` | `merge_batch_history.reason` | Recorded Detail | Outcome |
|---|---|---|---|---|---|
| Manual / external cancellation | `runner.cancelByHand(runID, "not now")` | `abandoned` | `cancelled` | `the batch's run ended as cancelled: "not now"` | **PASS** |
| Base branch moved mid-batch | Direct push to `main` while batch CI is running | `abandoned` | `base_moved` | `main moved to <sha> while this batch was running` | **PASS** |
| Batch run timed out | `now - batch.StartedAt >= 90m` (`MaxMergeBatchAge`) | `abandoned` | `timed_out` | `grain CI had not reported on this batch after 1h30m0s` | **PASS** |
| Batching disabled mid-run | `cfg.MergeBatchSize = 1` while multi-member batch is running | `abandoned` | `disabled` | `batching was turned off while this batch was running` | **PASS** |
| Daemon interrupted | `runner.interrupt(runID)` | `abandoned` | `interrupted` | `the batch's run was interrupted by the daemon that was running it stopping` | **PASS** |
| Errored CI run | `runner.finish(runID, ci.ConclusionErrored)` | `abandoned` | `no_answer` | `the batch's run ended as errored` | **PASS** |

#### 2. Base-Branch Hold Passes (`base_hold_history`)
We verified (`TestARedBaseBranchRecordsWhatItHeld` in `pkg/orchestrator/basebranchci_test.go` and live `grain demo` + `grain metrics` verification) that:
- While `main` is confirmed red (`Conclusion = "failure"`, `Confirmation = "confirmed"`), queued PRs waiting in `base_broken` open a row in `base_hold_history` (`finished_at IS NULL`) tracking `repair_task_id`, `held`, `peak`, `total`, `held_tasks`, and `pr_seconds` across cycles.
- When `main` returns to green (`Conclusion = "success"`), the next sync cycle closes the pass (`finished_at = now`), preserving `peak`, `total` (distinct PR count), `held_tasks`, and accumulated `pr_seconds`.
- Both `GET /api/metrics?section=merges` (`merges.baseHolds`) and `grain metrics` (`base-branch holds`) report p50/p90/max for `peak PRs held`, `total PRs held`, `PR-hours held`, and `pass length`:

```text
base-branch holds (1 repair pass(es) finished, 0 still open)
  peak PRs held            p50 3, p90 3, max 3
  total PRs held           p50 4, p90 4, max 4 (4 across all passes)
  PR-hours held            p50 2.0, p90 2.0, max 2.0
  pass length              p50 1h0m0s, p90 1h0m0s, max 1h0m0s
  (the hold history is bounded at 200 passes and reaches back to 2026-10-26T14:32:26Z.)
  merge gate latency            5  p50 1h0m0s, p90 1h0m0s, max 1h0m0s

  overlap re-checks (4 merge(s) evaluated)
    triggered                   3  (3 triggered, 75%; 1 skipped)
    clauses                same-package 2, global-input 1, same-file 1
    hot Go packages        pkg/widgets 2
    re-checked                  2  (caught 1 triggered, 0 skipped)
    skipped, base went red      1
    modes                  enforce 2, observe 2
    (the merge recheck history is bounded at 1000 rows and reaches back to 2026-10-26T15:32:26Z.)
  batches                       5  (2 change(s) landed on one of their verdicts)
  passed first try              1 of 5 fresh  (20%; 20% of all 5 passed)
  had to be bisected            0  (0% of them; 0 named a culprit, 4 answered nothing)
  abandoned                     4  (by reason: base_moved 1, cancelled 1, disabled 1, timed_out 1)
```

---

### §3.3 Base-Branch Red Streak Confirmation (PR #4829), `PriorityReleaseGate` & `infraVerdict` Auto-Rerun (PR #4877), and Red-CI Procedure (PR #4849)

#### 1. `PriorityReleaseGate` & `infraVerdict` Auto-Rerun on Default Branch (`10b31f7d6`, PR #4877)
Verified via `pkg/orchestrator/basebranchci_infra_test.go`, `pkg/orchestrator/basebranchrelease_infra_test.go`, `pkg/orchestrator/scheduledworkflow_infra_test.go`, and `pkg/ci/gate_releasegate_test.go`:
- Default-branch post-merge runs, release runs, and nightly/release-train scheduled workflows request `ci.PriorityReleaseGate = 6` (above `PriorityMergeHead = 5`, below `PriorityRepair = 7`).
- When a default-branch base CI run ends in `cancelled`, `errored`, or a failure whose failing jobs all match infrastructure signatures (`infraVerdict`: step timeout exit 124, OOM kill exit 137, quota exhaustion `RESOURCE_EXHAUSTED: Quota exceeded`, Kubernetes scheduling/API timeout, or sandbox setup error):
  - On attempt 1 (`Attempts < BaseCIMaximumAttempts`, where `BaseCIMaximumAttempts = 2`), `collectBaseBranchCI` rewrites `row.Conclusion = "errored"` so `row.Broken()` is `false` (holding no merges and filing no repair task), and after the retry delay re-runs **only the unpassed jobs uncached** (`Recheck: true`, `Priority: ci.PriorityReleaseGate`, `Attempts: 2`).
  - If the retry also fails with the same failure once `Attempts == BaseCIMaximumAttempts`, the failure stands (`row.Broken() == true`, `row.Confirmation == "confirming"`).
  - Ordinary unit test failures (`--- FAIL: TestSomething`) are not classified as `infraVerdict` and immediately enter confirmation (`row.Broken() == true`, `row.Confirmation == "confirming"`).

#### 2. Base-Branch Red Streak Confirmation (`8c9183d0b`, PR #4829)
Verified via `pkg/orchestrator/basecistreak_test.go` and live `/api/e2e/base-branch/red` / `/green` transitions:
- When `main` moves faster than a single tip's confirmation re-run can complete, `noteBaseCIStreak` tracks `RedStreak`, `RedStreakJobs`, `RedStreakSince`, `RedStreakTip`, `LastGreenCommit`, and `RedStreakWoken` across consecutive judged tips on the `base_branch_ci` row.
- When the same job (e.g. `tests/frontend`) fails on `N` (`grain_config.base_ci_red_streak`, default `2`, clamped to `>= 2`) consecutive judged tips `A` and `B`:
  - Tip `A` records `RedStreak = 1`, `RedStreakSince = A`, `Confirmed() == false`, and does not wake the steward.
  - Tip `B` reaches `RedStreak = 2` and confirms `main` red immediately (`Confirmation = "confirmed"`) **without** starting a confirmation re-run (`rechecks == 0`).
  - `ConfirmDetail` and held changes' `baseBrokenStatus` surface `"tests/frontend red on 2 consecutive tips since <short-sha-A>"`.
  - The pipeline steward (`system:pipeline-steward`) is woken **once** with the streak summary and bisect range (`"last green <sha>, first red <sha>"`).
  - A third consecutive failure on the same job at tip `C` advances `RedStreak = 3` and stays confirmed without waking the steward a second time (`wakes == 1`).
  - A green tip resets `RedStreak = 0` and updates `LastGreenCommit`; a tip failing a different job (`tests/go` while `tests/frontend` passes) resets the streak to `1` on `tests/go` at the new tip without confirming.

#### 3. Red-CI Procedure & `FixesBaseBranch` Repair Prompts (`bae0509ca`, `7d049ee38`, PR #4849)
Verified via `pkg/orchestrator/redci_procedure_internal_test.go`:
- Every repo-targeted task holding `grain-ci` receives `redCIProcedureSection()` exactly once in `BuildPrompt`, containing the 7-step narrow reproduction and verification procedure (`wait_for_checks` + `get_ci_log` -> `query_test_history` -> classify infra exits `124`/`137` -> reproduce locally with `go test -race -run '<TestName>' ./<pkg>` -> fix and re-run locally -> selective `wait_for_checks` with `jobs` -> full `wait_for_checks` without `jobs`).
- Tasks without a target repository are not given `redCIProcedureSection()`.
- Built-in base-repair task bodies, the `system:base-repair` template, and release triage bodies reference `model.RedCIProcedureReminder` rather than duplicating the 7 steps.
- With our fix in `pkg/orchestrator/run.go`, `FixesBaseBranch` repair tasks (both grain-filed repairs carrying `BaseCIRepairBranchTag` and operator/agent-marked repairs carrying `MarkedBaseRepairTag`):
  - Are recognized by `mergeRepairRun(task, obs) == true` even on their first dispatch (`obs == nil`).
  - Receive the `"unless repairing that branch is this run's own work"` carve-out in `redCIProcedureSection()` when `grain-ci` is granted (and suppress `baseIncidentNote`).
  - Receive `repairCISection()` (and never the `"not yours to fix -- leave it alone"` paragraph) in both `BuildPrompt` and `BuildSuspendedPrompt` when `grain-ci` is not granted.

---

## 4. Summary of Code Changes in This Branch

| File | Change |
|---|---|
| `pkg/daemon/metrics.go` | Include `m.Rechecks.Merges == 0` in `printMerges` early-return guard; add `printMergeRechecks` and `formatCountMap` so `grain metrics` and `read_grain_metrics` render overlap re-check counters in the `merges` section. |
| `pkg/cli/metrics_test.go` | Extend `TestRenderMetricsRendersTheMergesSection` to assert `overlap re-checks` rendering and verify that a window whose only merges activity is an overlap re-check still renders the `merges` section. |
| `pkg/orchestrator/run.go` | Check `model.FixesBaseBranch(task)` in `mergeRepairRun`, `BuildPrompt`, and `BuildSuspendedPrompt` so base-branch repair tasks without `grain-ci` receive `repairCISection()` and `RunConfig.Repair = true` instead of being told to leave inherited base failures alone. |
| `pkg/orchestrator/redci_procedure_internal_test.go` | Add `TestFixesBaseBranchRepairPromptsDoNotSkipInheritedBaseFailures` covering both grain-filed and `FixesBaseBranch`-marked tasks with and without `grain-ci`. |
| `pkg/orchestrator/mergequeue_refresh_test.go` | Create parent directories in `pushFileToBranch` so test helpers can push nested package and `.grain/workflows/` paths. |
| `pkg/orchestrator/mergerecheck_test.go` | Add `TestAllOverlapClausesUnderObserveAndEnforce` (`same-file`, `same-package`, `global-input` for `go.mod`, `.grain/workflows/ci.yml`, `Makefile`) and `TestPostMergeBaseBranchCIJudgesMergeRecheckRecords`. |
| `pkg/orchestrator/mergebatch_test.go` | Add the `disabled` (`model.AbandonDisabled`) subtest to `TestAnAbandonedBatchRecordsWhyItWasAbandoned`. |
| `docs/test-runs/2026-10-26-merge-recheck-base-streak-release-gates.md` | Record full walkthrough execution, verification tables, and bug fixes. |
