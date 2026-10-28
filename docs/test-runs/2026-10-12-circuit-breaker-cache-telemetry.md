# Test Run: Shared-Fault Circuit Breaker, Base-Transition CI Cache Invalidation, Merge-Queue Telemetry

**Date**: 2026-10-12
**Origin**: task 2955 (nightly test plan), run as task 3142
**Plan**: `docs/test-plans/2026-10-11.md` §6
**Base**: `candidate` at `b2ce41d09`, which carries both commits under test:
`3ea13cc05` (circuit breaker, incident lock, `BaseCommit` cache invalidation) and
`faf82f23e` (merge-queue contention telemetry, `tuning_recommendation`, tuning allowlist).
**Walkthrough**: `tests/e2e/circuit_breaker_cache_telemetry_walkthrough_test.go` (durable; runs in the `tests/e2e` suite)

---

## 1. Summary

All four steps were run end to end on the real orchestrator. The walkthrough found two bugs; both are fixed on this branch, with regression tests.

| Step | Result |
|------|--------|
| 1. Base goes red: exactly one repair task, two queued tasks held with a branch wait (API) | **Pass** |
| 2. Base fixed: holds clear, queue resumes, the old-base cached failure is re-run rather than replayed | **Failed, then fixed.** Holds never cleared while the repair task stayed open (bug 1). Passes with the fix. |
| 3. Batched merges against `/api/metrics` merges contention | **Pass**, with the semantic findings in §4 |
| 4. Tuning runner: allowlisted parameter filed, unknown parameter refused, nothing filed for it | **Pass**. Ordinary tasks' "Tune …" proposals were silently dropped (bug 2); fixed. |

## 2. Environment

The `tests/e2e` harness (`triggerWorld`/`holdWorld` from `security_hold_walkthrough_test.go` / `path_triggers_walkthrough_test.go`), extended as `breakerWorld`.

What is real:
- `orchestrator.RunCycle` over an embedded SQLite `model.Store`, including the base-branch CI reconciler (seed, run, confirmation, repair filing), dispatch with `heldForBrokenBase`, the CI reconciler, the merge queue with real batches (`reconcileMergeBatches`), sync, and the finish path that relays `propose_task`.
- The daemon's own grain CI service, `daemon.BuildCIService`, with its stored run history and its on-disk job cache (`<data>/state/ci-cache`).
- The API, `ui.NewServer`: `GET /api/tasks/{id}` and `GET /api/metrics?section=merges`.

The repo `acme/widgets` declares one job, `checks/base-health`, on `[push, pull_request]`. It fails while `marker` exists on `refs/heads/main` and passes otherwise, whichever branch or batch it runs for. A task branch's verdict therefore depends on its base and not on its own tree, which is the case a cached failure must not outlive.

### Stubs (external boundaries only)
- **GitHub**: `githubsim` behind the e2e git host. This is the fake forge, as in every walkthrough here.
- **Agents**: scripted antigravity runs (`holdWorld.Framework`). Queued tasks push a one-file change. The repair task answers without pushing, which parks it in `awaiting_ack`. The tuning runner and the ordinary task call `propose_task`.
- **CI job machine**: `localCISandbox` runs each step with `sh` on the host. Each step gets `E2E_BARE`, pointing at the bare repository.
- **Workflow source**: `bareRepoCISource` reads `.grain/workflows` out of the bare repository.
- **Clock**: the cycle clock starts at wall-clock time and advances one minute per cycle. CI runs are stamped with the wall clock, and a merge batch's start is its run's start while its end is the cycle's moment. If the clock started at `baseTime` (August), every gate latency would come out negative.

### Not covered
The task pane via Playwright was not run, because this harness serves the API but not the built frontend. The pane's "waiting: …" line is the task's `activity` field (`ui.grantWaitActivity`). The walkthrough asserts that field through `GET /api/tasks/{id}`.

## 3. Steps and results

### Step 1: base goes red
1. `main` is seeded as watched (`WatchDefaultBranch`) and judged green.
2. `marker` is pushed to `main`. Base CI fails, the confirmation reproduces it, and `fileBaseBranchRepair` files **exactly one** repair task: task 3, "Fix grain CI on acme/widgets main", tagged `BaseCIRepairBranchTag`. Further cycles file no second one.
3. Two tasks are filed while `main` is red. Both stay `queued`, are never dispatched, and carry a `branch` grant wait. The API returns, for both:
   `activity: "waiting: base branch \"main\" on acme/widgets has failing CI; waiting for repair task 3 to clear incident lock"`.
4. A victim task with a pushed branch and an open PR, completed so that it skips dispatch, gets its required CI run from the CI reconciler. The run fails against the red base. The on-disk cache entry for that job key is a failure with `baseCommit` = the red `main` commit.

### Step 2: base fixed
1. `marker` is removed from `main`.
2. **Cycle N** (grain notices the move): `startBaseBranchCI` re-judges the new tip and `EvictBaseTransitionFailures` purges the victim's cached failure. Asserted: the cache entry is gone.
3. **Cycle N+1**: both holds are cleared and both tasks are dispatched. The one-cycle lag is ordering, not a bug: `dispatch` reconciles before `base-branch-ci` in `Reconcilers()`, so cycle N's dispatch still read the old row.
4. Without the fix, the holds never cleared (bug 1, below).
5. The victim's CI is asked again at the **same commit** through `ui.Client.StartTaskCIRunOpt`, the API's "run CI". The job runs fresh (`cached=false`, a second sandbox is built for that head) and passes. The cache now holds a pass recorded against the fixed base.

### Step 3: merges in batches against `/api/metrics`
Five auto-merge tasks were merged by the real queue in two batches, of 3 and 2 members, both with outcome `merged` and no splits.

`GET /api/metrics?section=merges&since=…&until=…` returned:

| Field | Value | Checked against |
|-------|-------|-----------------|
| `landed` | 5 | the 5 merged branches |
| `contention.batchSizes` | n=2, min=2, max=3, total=5 | `MergeBatchRecords` (3 and 2) |
| `contention.gateLatency.n` | 2 | one sample per batch with a positive duration |
| `contention.queueWaitTime.n` | the merged PRs with opened < merged | each task's `PrOpenedAt` / `PrMergedAt` |
| `rebaseRetest` | rebases 0, retests 0, total 0 | the sum of batch splits (0); nothing contended |

All values match what was merged. See §4 for what these fields actually measure.

### Step 4: tuning runner
An infra-review task (`TemplateID = InfraReviewTemplateID`) called `propose_task` twice:
- "Tune merge_batch_size to 4", with `template_fields.target_parameter=merge_batch_size`. **Filed.** One `tuning_recommendation` row was recorded: `merge_batch_size=4`.
- "Tune merge_window_secs to 9", with `target_parameter=merge_window_secs`, which is not in `RecognizedGrainConfigParameters`. **Refused.** No task was filed and no `tuning_recommendation` row was recorded. The only trace is the log line `refusing tuning proposal …`.

## 4. Findings

### Bug 1 (fixed): an open repair task held all dispatch on a base that was already green
`heldForBrokenBase` (in `pkg/orchestrator/cycle.go`, from `3ea13cc05`) treated `baseBroken != nil || existingRepair != nil` as an active incident for downstream tasks. A repair or release-triage task that is still open therefore held every queued task on the branch after `main` was judged green. An open task here means one parked in `awaiting_ack`, or one whose PR is still queued. The holds lasted until a person closed it.

**Reproduction (deterministic):** walkthrough step 2 without the fix. `main` is green at the fixed commit, the repair task is in `awaiting_ack`, and both queued tasks still show "waiting for repair task 3 to clear incident lock" across ten further cycles. Unit form: `TestSharedFaultCircuitBreaker_OpenRepairDoesNotHoldOnceBaseIsGreen`, which fails on `candidate` with t-feature dispatched 0 times and the wait still recorded.

**Fix:** downstream holds now follow the base verdict (`brokenBaseBranch`) alone. The single-flight lock between repair tasks is unchanged.

**Side effect, worth a reviewer's eye:** an open *release* triage task (`BaseReleaseTriageBranchTag`) no longer holds downstream dispatch through this path when CI is green. It still holds new release runs (`startBaseBranchRelease`). Holding all work on a publishing failure was the same deadlock in another form; this deployment's notes record task 3018 keeping such a hold in place.

### Bug 2 (fixed): ordinary tasks' "Tune …" proposals were silently dropped
`relayProposedTasks` (in `pkg/orchestrator/finish.go`, from `faf82f23e`) ran `ExtractTuningProposal`'s title regex on every task's proposals. "Tune the e2e retry loop" from an ordinary task yielded the parameter `the`, failed the allowlist, and was dropped unfiled with only a log line.

**Reproduction:** walkthrough step 4, the ordinary task's proposal. On `candidate` that task files nothing.

**Fix:** the title and body are read for a parameter only on the tuning runner's own proposals. Explicit `target_parameter`/`parameter` fields are still validated wherever they appear.

### Not fixed (filed as follow-ups)
1. **The contention telemetry measures something narrower or different from what its names say** (`faf82f23e`):
   - `rebaseRetest.rebases` counts merged tasks with `FixTaskFiled`, which is a merge-queue *repair*, not a rebase. Real rebases (`refreshStaleHead`) are not counted anywhere.
   - `retests` counts only batch splits.
   - `queueWaitTime` is PR opened → merged, which is the PR's lifetime rather than its time in the queue. Zero-length samples are dropped (`wait > 0`), which skews p50 upward on a fast queue.
   - The in-memory `mergequeue.Telemetry` (`RecordRebase`/`RecordRetest`/…) is never instantiated outside its own tests, so `MetricsInput.MergeQueueContention` is always nil.
   - The numbers are internally consistent with their sources (step 3), but a tuning run reading "rebases" would be misled.
2. **`BaseCommit` is only in the job cache, not in stored run history** (`3ea13cc05`):
   - `model.CIJob` has no column for it, so `ci.Service.Find`/`Get` return jobs with an empty `BaseCommit`. Nobody can see from run history which base a verdict was against.
   - `DirCache.PurgeNegativeEntries(base)` is documented as removing failures "whose BaseCommit matches baseCommit (or older)", but it removes every failure with any `BaseCommit`.
   - On green, `collectBaseBranchCI` also calls `ClearCache`, which empties passes too. That makes "preserving unaffected passing entries" moot on recovery.
   - Behaviour is safe (it over-invalidates), but the code and its documentation disagree.
3. **A refused tuning proposal is invisible to the runner**: it is logged, but not commented on the task or returned to the agent.

## 5. Fixes on this branch
- `pkg/orchestrator/cycle.go`: `heldForBrokenBase` holds downstream work only while the base is judged red.
- `pkg/orchestrator/finish.go`: title/body tuning heuristics apply only to the infra-review runner.
- `pkg/orchestrator/circuit_breaker_test.go`: `TestSharedFaultCircuitBreaker_OpenRepairDoesNotHoldOnceBaseIsGreen`.
- `tests/e2e/circuit_breaker_cache_telemetry_walkthrough_test.go`: `TestCircuitBreakerHoldsDispatchOnBrokenBaseAndCacheForgetsTheOldBase`, `TestMergeQueueContentionTelemetryMatchesWhatMerged` and `TestTuningRunnerRefusesAParameterOutsideTheAllowlist`. Locally they pass with `-count=3`, in about 9 s per iteration.

## 6. CI verdict
Grain CI, full suite, on `58f2286`, the code as it lands here (this report section was added afterwards): **success**.
- First run `2cb396c5197bccfa`: every job passed except `ui e2e`. Its one failure was `task_detail_workflows.spec.js:170` ("quotes an edited description as typed and links related tasks by address"), a browser-tab timeout. grain reported it as a shared fault, failing the same way on `grain/eu/task-3180` and `grain-merge-queue/candidate`. This branch touches no UI code.
- The uncached recheck of `ui e2e` (`eb7798ceff33e549`) passed.
- Run `f9905261fcf1367c` is the full-suite verdict, green on every job: frontend, go (18m, including the new walkthrough), installer, lint, package cache, postgres, relay image, terraform, ui e2e.

## 7. On `main`
This walkthrough ran against `candidate`, and the CI verdict above is for that branch. When the change was ported to `main`, the Bug 1 fix to `heldForBrokenBase` was dropped. `main` had already removed that function (acd860412, task 3024): work on a red branch now depends on the branch's repair task, and the repair is closed as superseded once the branch's grain CI is green. Steps 1-2 of the walkthrough assert that mechanism on `main`. Queued work is blocked by the repair (the API's `blockedBy`) rather than holding a branch grant wait. The holds clear when the repair closes, which happens the cycle after the green verdict is recorded.

## 8. Addendum (2026-10-13): step 3 on a contended queue
Once follow-up 1 above landed (`0c7590e63` on `candidate`, `de54ab390` on `main`: contention now comes from the queue's own `merge_queue_contention` events, and queue wait is measured from `Observation.MergeQueueEnteredAt`), `TestMergeQueueContentionTelemetryMatchesWhatMerged` was extended so that the queue actually contends:

- **A split.** The repository's merge rules say `defaults: flake_recheck: false`, so a red batch is bisected rather than run again. The first merge batch's run is made red once. In the run recorded here, that batch (2 members) was split; its first half (1 member) passed and merged, and the remaining changes merged in a batch of 3.
- **A rebase.** A sixth change touches `legacy/**`, which a rule gates on `ci.mode: both`. Its GitHub check `build` is still running when its pull request opens. Main then moves (an unrelated commit), and the check reports `failure`. At the head of the queue, with grain CI green, `refreshStaleHead` merges main into the branch rather than filing a repair. The check on the merged head passes and the change merges.

`GET /api/metrics?section=merges` returned the following:

| Field | Value | Checked against |
|-------|-------|-----------------|
| `landed` | 6 | the 6 merged branches |
| `contention.batchSizes` | n=3, min=1, max=3, total=6 | `MergeBatchRecords` |
| `contention.queueWaitTime` | n=6, min ≈ 179 s, max ≈ 299 s | one sample per merged change, `PrMergedAt − MergeQueueEnteredAt`, with min, max and mean matched |
| `rebaseRetest` | rebases 1, retests 2 | `MergeQueueContentionEvents`: `rebase/stale head` ×1 for the late change, `retest/after rebase` ×1, `retest/batch split` ×1 (one per split record), and nothing else |

These stubs were added on top of §2:
- **Forge clock**: `githubsim.Sim.Now` is set to the cycle clock, so pull requests are opened and merged at the cycle's moment. Before this, the forge stamped merges with the wall clock while the cycle clock ran ahead, so every merge read as earlier than its queue entry and the queue-wait series was empty.
- **The failing batch**: when armed, the job fails the first run it is asked to make for `grain-merge-queue/main`, and only that run (a flag file the step moves aside).
- **The GitHub check** on the late change is seeded in `githubsim` (`CheckRuns`), as in `mergequeue_refresh_test.go`.

A stale head that grain CI itself failed is never rebased under `ci_mode: grain` (`refreshWorthTrying`: grain judged that exact commit), unless the repository requires up-to-date branches. The red in this walkthrough therefore comes from a GitHub check that a path rule adds. A freshly pushed branch is also never stale: `ensurePullRequest` merges the base into a branch that is behind before it opens the pull request, which is why main moves *after* the late change's pull request is open.
