# Grain CI recheck joining, operator retry and the runs poll cache, 26 October 2026

grain/task-932 runs the walkthrough in `docs/test-plans/2026-10-25.md` §6. It
covers three things:

- Grain CI recheck joining and superseding (`5224748c7`, PR #4686).
- Operator retry of a cancelled or failed run, `Retry` and `Re-run CI`
  (`a5a25f9fa`, PR #4656; `a9b744fcb`).
- The `/api/ci/runs` poll cache and fault-cache isolation (`f1eb05049`,
  PR #4697; `6f82f323d`, PR #4665; `607094db1`; `c172311b7`).

The branch was cut from `origin/candidate` at `6d8f57485`. Every commit
the plan names is an ancestor of it.

## Summary

- **One bug found and fixed.** A full-suite `wait_for_checks` could join a
  `retrigger_ci` recheck. If a second `retrigger_ci` then superseded that
  recheck, the wait returned the first recheck's cancellation as its verdict.
  It also said the run was "cancelled by hand", though grain had cancelled it.
  The wait now follows the run that replaced it and reports that run's
  result. The fix is `7c874701e`. It is covered by unit tests and by a live
  test against a real `ci.Service` (`abffa832d`), which fails on the old code.
- **Operator retry works in the browser on `grain demo`.** Cancel, Retry,
  Retry failed jobs and the task pane's Re-run CI each started or stopped the
  run they should. `GET /api/ci/runs` showed every change on the very next
  read.
- **Part of the plan does not match the tree.** There is no
  `pkg/ci/store_cache.go`, `pkg/ci/recheck_test.go` or
  `POST /api/ci/runs/<id>/retry` on `candidate` or `main` (see
  "Plan corrections").

## Environment

- Debian sandbox; Go 1.26.10, Node 20.19.0.
- Playwright Chromium (`/var/cache/playwright/chromium-1234`), headless.
- `npm --prefix ui ci && npm --prefix ui run build`; `go build -o ~/grain ./cmd/grain`.
- `grain demo -addr 127.0.0.1:4391 -open=false -fake-ci -github-sim -scheduled-workflows -access-code-file ~/demo/code`.
  - `-fake-ci` accepts runs and never executes them.
  - `-scheduled-workflows` adds `POST /api/e2e/fake-ci/runs/{id}/finish`,
    which ends a fake run with a chosen conclusion.
- Setup: `acme/widgets` CI mode set to `grain` (`PUT /api/repos/acme/widgets/ci-mode`).
  Then task 13 was filed on it. It was granted `grain-ci` by default.
- The walkthrough scripts were ad hoc and are not committed. They signed in
  with `POST /api/auth/access-code` and used Playwright's `chromium` API.

## 1. CI runs UI: cancel, Retry, Retry failed jobs

| Step | What was done | Result |
| --- | --- | --- |
| 1.1 | `/ci` with one failed task run (`fake-ci-1`). | Rendered, with the run listed. |
| 1.2 | `POST /api/ci/runs {repo, ref: main, commit: abc1234}` started `fake-ci-2`; the next `GET /api/ci/runs` was read. | `fake-ci-2` listed with no conclusion. |
| 1.3 | Opened `/ci/fake-ci-2` and clicked **Cancel run**. The prompt was answered "walkthrough cancel". | **Retry** appeared at once. **Retry failed jobs** and **Cancel run** were not offered. `GET /api/ci/runs` listed `fake-ci-2` as `cancelled`, with `cancelReason` "walkthrough cancel". |
| 1.4 | Clicked **Retry**. | `POST /api/ci/runs` started `fake-ci-3` on the same repo, ref and commit. The pane moved to `/ci/fake-ci-3`, which showed **Cancel run**. The next `GET /api/ci/runs` listed `fake-ci-3` with no conclusion. |
| 1.5 | Task 13's run `fake-ci-4` (commit `def5678aaaa`) was failed on its `test` job. Then `/ci/fake-ci-4` was opened. | **Retry** and **Retry failed jobs** were both offered. |
| 1.6 | Clicked **Retry failed jobs**. | It posted `{taskId: "13", recheck: true, recheckFailed: true}` and started recheck `fake-ci-5` of `test` alone. The run list and task 13's verdict (`runId: fake-ci-5`, commit `def5678aaaa`) both showed it at once. |

**Demo-only gap.** Retry failed jobs on the first task run, `fake-ci-1`, did
nothing visible. The server refused it: "there is no grain CI run on
grain/task-13 to re-run the failed jobs of". This happens because `-fake-ci`
records a task's run with no commit (the demo's task branch does not exist
in the simulated repo). `failedTaskJobs` only reads runs with a commit.
`ci.Service` always resolves a commit for a real run, so this was not
treated as a product bug. A run started with an explicit commit (1.5–1.6)
behaved correctly.

`ui/e2e/ci-retry.spec.js` and `ui/e2e/ci_workflows.spec.js` passed on
Chromium (2/2).

## 2. Task detail: Re-run CI

The demo has no CI reconciler (`orchestrator.reconcileCI` is not among
`-scheduled-workflows`' reconcilers). Its tasks also have no open pull
request for the reconciler to act on. So a task's grain CI verdict there
keeps a run id and never gets a conclusion. The task pane only offers
**Re-run CI** on a verdict that has a conclusion.

To work around that, the walkthrough rewrote task 13's first reads in the
browser so its verdict read `failure`. The click's `POST` and every read
after it went to the server unchanged.

| Step | What was done | Result |
| --- | --- | --- |
| 2.1 | Opened `/tasks/13` (verdict `failure`, injected), at 1440×900 and 390×844. | **Re-run CI** and **Re-run failed jobs** rendered, and **Re-run CI** stayed on screen at 390 px. |
| 2.2 | First try: clicked **Re-run CI** while the recorded verdict was the live recheck `fake-ci-5`. | No new run started. The server handed back the live recheck (`liveRecheck`), as designed. This only showed up because of the injection: a live recheck has no conclusion, so a real pane shows no button. |
| 2.3 | Failed `fake-ci-5`, then clicked **Re-run CI** again. | It posted `{taskId: "13"}` and started full run `fake-ci-6`. The button disappeared, and the pane said "grain CI is running against this task's branch". The next `GET /api/ci/runs` listed `fake-ci-6` with no conclusion, and `GET /api/tasks/13` gave `ci.runId = fake-ci-6`. `/ci` listed it as the newest row, and `/ci/fake-ci-6` opened with **Cancel run**. |

After `fake-ci-6` was cancelled to clean up, `/tasks/13` still said "grain
CI is running". This is the same demo-only gap: nothing in the demo
collects a finished run onto the task. In a daemon, `reconcileCI` does.

## 3. `/api/ci/runs` freshness

In every step above, the first `GET /api/ci/runs` after a start, cancel,
retry or recheck already showed the new run or conclusion. No list was ever
served stale.

There is no list cache to invalidate. `handleListCIRuns` asks the run store
(`runner.Find`) on every request. The only cache on that route is the
shared-fault annotation (`pkg/ui/cifault.go`). It holds faults for 30 s and
is keyed by the IDs and conclusions of the recently failed runs, so a new
failure changes the key and is never hidden behind it.
`canShareFaultRuns` keeps a reader-scoped page (`filter.Visible`) out of
that shared cache. `pkg/ui/ci_faultshare_internal_test.go` and
`pkg/ui/cifault_test.go` cover it.

## 4. Recheck joining and superseding against `ci.Service`

The new test is
`pkg/daemon/grainci_recheck_walkthrough_test.go`
(`TestWalkthroughWaitForChecksFollowsASupersedingRecheckToTheCombinedVerdict`).
It drives the agent's `wait_for_checks` and `retrigger_ci` tools
(`mcp.NewGrainCIWaitTools`) over `DaemonGrainCI` and `ui.HTTPClient`, the
same REST hop a sandbox run makes. They talk to a real `pkg/ui` server whose
CI is a real `ci.Service`. That service reads a workflow from disk, runs it
on an engine, and caches results in a `DirCache`.

The workflow has two jobs. `go` always passes. `e2e` fails the first time
it runs. After that it waits until the test lets it through, then passes.

1. A full-suite `wait_for_checks` ran `run-1`: go passed and e2e failed.
2. `retrigger_ci` started `run-2`, an uncached recheck of e2e alone.
3. A second full-suite `wait_for_checks` joined `run-2` and started no run
   of its own.
4. Repeating the same `retrigger_ci` returned `run-2` again, with no
   supersede. `ci.Service.Start` hands back a live run with the same plan
   key. The plan's phrase "a second `retrigger_ci` supersedes" only holds
   for a recheck with a different job selection.
5. `retrigger_ci jobs: ["*"]` started `run-3`. That cancelled `run-2` with
   "superseded by run run-3, started for the same task".
6. **Before the fix**, the joined wait returned when it next read `run-2`
   (15 s):

   > Grain CI run run-2 for grain/task-7 at abcdef0 is cancelled … This run
   > was cancelled by hand. The reason given: superseded by run run-3,
   > started for the same task … Job e2e: cancelled

   **After the fix**, it followed to `run-3` and reported its success.
7. The next full-suite `wait_for_checks` ran `run-4`. Both jobs were cached
   passes, which is the combined verdict. The job counts were go ×2 (run-1,
   run-3) and e2e ×3 (run-1, run-2 superseded, run-3).

## Bug fixed

**`wait_for_checks` reported a superseded recheck's cancellation as its
verdict** (`pkg/mcp/grain_ci_wait_tool.go`).

`waitForGrainCI` polled the one run id it was first handed. When grain
cancelled that run to make way for a newer run for the same task, the
cancellation counted as "done". The agent was then told its branch was
cancelled, and that someone had done it by hand.

The fix:

- The supersede reason is now written and parsed in one place:
  `ci.SupersededByRunReason` and `ci.SupersedingRun`. Both
  `ui.Client.cancelSupersededTaskRuns` and
  `orchestrator.cancelSupersededBranchRuns` write it with
  `ci.SupersededByRunReason`.
- A full-suite wait follows a run grain itself superseded, but only to a
  replacement at the same commit (`followSupersede`, which guards against
  cycles).
- These are still reported as before, without following: a selective wait,
  a supersede onto another commit (the branch moved), and an operator's
  cancellation.
- A cancel reason grain wrote itself is now introduced with "Grain
  cancelled this run itself:" instead of "This run was cancelled by hand."
  Only an operator's cancellation (`CancelledBy == operator`) is said to be
  by hand.

Regression coverage:

- `pkg/ci/cancel_reason_test.go`: `TestSupersedingRunReadsOnlyGrainsOwnSupersede`.
- `pkg/mcp/grain_ci_wait_supersede_test.go`, all three fail on the old code:
  - `TestWaitForChecksFollowsTheRecheckThatSupersededTheOneItJoined`.
  - `TestWaitForChecksReportsACancellationItShouldNotFollow` (selective,
    other commit, operator).
  - `TestWaitForChecksSaysWhenGrainCancelledTheRunItself`.
- `TestWaitForChecksSaysWhyARunWasCancelled` now marks its run
  `CancelledBy: operator`, since that is the case it describes.
- The live test in §4.

## Plan corrections

- `pkg/ci/store_cache.go` and `pkg/ci/store_cache_test.go` exist on
  neither `candidate` nor `main`. Neither does an in-memory SQLite list
  cache for `/api/ci/runs`. The caching the cited commits added is the
  shared-fault cache in `pkg/ui/cifault.go` (§3).
- `pkg/ci/recheck_test.go` does not exist. The recheck coverage is in:
  - `pkg/ui/ci_test.go` (`TestTaskScopedRecheck*`).
  - `pkg/orchestrator/recheck_test.go` and
    `pkg/orchestrator/recheck_internal_test.go`.
  - `pkg/ui/ci_recheck_internal_test.go`.
- Recheck joining lives in `pkg/ui/ci.go` (`StartTaskCIRunOpt`,
  `liveRecheck`, `recordTaskRecheck`) and `pkg/mcp/grain_ci_wait_tool.go`.
  It is not in `pkg/ci/service.go` or `pkg/mcp/sandbox/tools_ci.go`.
- `POST /api/ci/runs/<id>/retry` and `POST /api/tasks/<id>/rerun-ci` do not
  exist. Retry and Re-run CI are `POST /api/ci/runs`:
  - with the run's repo, ref, commit and event, for a run started by hand;
  - or with `taskId`, for a task's run.

  The UI lives in `CIPage.jsx` (`RunDetail`), `CIRunOverlay.jsx` and
  `detail/GrainCIStatus.jsx`, not in `CIRunDetailView.jsx` or
  `TaskDetailPanel.jsx`.
