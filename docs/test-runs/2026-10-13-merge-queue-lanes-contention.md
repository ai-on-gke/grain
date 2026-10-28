# Walkthrough: merge queue lanes, contention telemetry and exact-base cache eviction

Plan: `docs/test-plans/2026-10-13.md` §2. Task 3548 (filed by task 3492). Run 2026-10-13, 23:40–00:30 UTC.

## Base

Branch `grain/eu/task-3548`, cut from `candidate` at `a4674d57b`. All four commits under test are on
`origin/candidate` (`git branch -r --contains`): `5c2776f62` (lanes), `0c7590e63` (contention
telemetry), `397e91eb0` (contention walkthrough test), `cbad2978a` (CI job `base_commit`, exact-base
eviction).

## Environment

- **The queue (steps 2–4)**: `tests/e2e/mergequeue_lanes_walkthrough_test.go`,
  `TestMergeQueueLanesContentionWalkthrough`. It runs `orchestrator.RunCycle` over a real SQLite store,
  with the merge queue, its batches and flake recheck, and the base-branch reconciler. It uses the CI
  service the daemon builds (`daemon.BuildCIService`, with the `DirCache` job cache under a data
  directory) and the API (`ui.NewServer`) that the task pane and Metrics read. It reuses the
  `breakerWorld` rig from `circuit_breaker_cache_telemetry_walkthrough_test.go`. The repository is
  `acme/widgets` with `main` and `candidate`, and `max_merges_per_repo` is 1 (the default; the test
  asserts it).
  - **Why not the demo:** `grain demo -merge-queue-states` seeds the queue's rows through the store but
    runs no merge queue. `-reconcile` runs only the suite and release steps, and `-github-sim` has
    `main` and `release/1.0` but no `candidate`. A real two-lane queue therefore had to be the real
    orchestrator over githubsim.
- **The cache (step 5)**: `TestCIFailureEvictedOnlyByGreenAtItsOwnBase`, in the same file and on the
  same rig. The `pkg/ci` engine and `DirCache` are real. Grain itself drives the base-branch verdicts,
  and the cache is read off disk.
- **The browser**: `ui/e2e/merge-queue-lanes-walkthrough.spec.js`, run with Playwright (Chromium)
  against the built UI (`cd ui && npm ci && npm run build`). It runs on a real
  `grain demo -github-sim -fake-ci -merge-queue-states` started by `e2e/fixtures.js`.

### Stubs (external boundaries only)

| Boundary | Stand-in |
|---|---|
| GitHub | `githubsim` behind the e2e git host. GitHub check runs are seeded on a branch where a test needs one. |
| GitHub failing to answer for one pull request (step 3) | `unreadablePullSim`, which wraps githubsim and answers 502 to `GET /repos/acme/widgets/pulls/<n>` while armed. |
| A CI job's machine | `localCISandbox` (sh on this host). The one workflow job fails while an armed file exists, or once for the first merge batch of a chosen branch (`failFirstBatch`, `failBranch`). |
| Where workflows come from | `bareRepoCISource` |
| The demo's CI | `-fake-ci`, as the plan asks. Only the browser part used it. |

Grain's own APIs, store, queue, CI engine and React were not mocked.

## Steps and outcomes

### Step 2: `main` held in CI while `candidate` batches and merges

- **Set-up**
  - A1 on `main` touches `held/a1.txt`. A merge rule (`held/**`, `ci: mode: both`) gates it on GitHub's
    checks as well as grain CI's. Its GitHub check `build` stays `in_progress`.
  - A2 on `main` is queued behind it.
  - B1 and B2 on `candidate` are green, and the first `candidate` batch is armed to fail once.
- **Outcome**
  - B1 and B2 batch on `grain-merge-queue/<id>/candidate`. The batch fails, its uncached recheck passes,
    and both merge onto `candidate`. A1 and A2 stay unmerged throughout.
  - Row notes, read through `GET /api/tasks/{id}`:
    - A1: "waiting on checks that have not finished (`build`)".
    - A2: "behind task t-a1 in the acme/widgets main queue".
    - B1/B2 (after the fix below): "grain CI is testing this change together with 1 other(s) at the
      front of the acme/widgets candidate queue, on `grain-merge-queue/…/candidate` …".
  - The test asserts that every "behind task" note names its own lane, and a head on that lane.
- **Bugs found** (both fixed below)
  - **F1.** On the first run, A1 merged inside a `main` batch while its required GitHub check was still
    `in_progress`.
  - **F2.** The batch notes said "at the front of acme/widgets's queue", naming the repository and not
    the lane.
- **Label**: System → Engine shows "Max merges per repo branch", with the helper text "maximum ready
  pull requests merged into each branch of a repository per queue cycle". A value of 1 persists
  (`GET /api/settings` returns `maxMergesPerRepo: 1`) and survives a reload. This is the Playwright
  spec's first test.

### Step 3: a task with no declared base

- **Set-up**: N0 is filed with no base, and its PR opens against `main`. GitHub then answers 502 for
  N0's pull request. B3 on `candidate` is queued behind it in queue order.
- **Outcome**
  - For six cycles B3, green, does not merge. This is the documented conservative behaviour: an entry
    whose lane is unknown closes and heads its whole repository.
  - N0's note reads "GitHub is not answering for this pull request (6 failed reads over 5m; …)". B3's
    note reads "behind task t-n0 in the acme/widgets candidate queue".
  - Once the reads come back, B3 merges onto `candidate` and N0 joins `main`'s lane (its note:
    "behind task t-a1 in the acme/widgets main queue").
  - A1's check then passes and `main`'s lane drains: A1, A2 and N0 merge.
- **Observation (not fixed)**: B3's note names the `candidate` queue for a head (N0) whose lane is
  unknown. The note is accurate about what B3 waits for, but it does not say that the whole repository
  is stopped. This is cosmetic, and the report leaves it as an observation.

### Step 4: contention counts

- **This walkthrough's queue**
  - It recorded exactly one contention event: `retest (flake recheck)` for the `candidate` batch that
    failed once.
  - `/api/metrics?section=merges` reports `landed 6`, `rebases 0`, `retests 1`, `totalContention 1`.
    Those counts equal the queue's own `merge_queue_contention` rows.
  - Queue wait has 6 samples (one per landed change), from `MergeQueueEnteredAt` to the merge: min
    179.3s, max 2099.3s. Min and max match the observations.
- **Stale head and halved batch**: these are covered on the same rig by the existing
  `TestMergeQueueContentionTelemetryMatchesWhatMerged`, re-run here and passing. It checks
  `rebase/stale head` = 1, `retest/after rebase` = 1, `retest/batch split` = the number of splits, and
  one queue-wait sample per change.
- **Zero waits**: no change in either rig merged in the same cycle it entered the queue. The cycle clock
  moves a minute per tick, and the CI service's wall-clock stamps add a fraction of a second. So a
  literal zero-wait sample was not produced end to end. That zero waits are kept is covered by
  `pkg/metrics/merges_test.go`.
- **Browser**: Metrics renders no contention figure. `MergesSection` shows landings, lead time and
  batches but not `contention`, and it hides entirely for a window with nothing landed. The Playwright
  spec asserts the report's zeros through the API, and that the page loads. Rendering contention is
  filed as a follow-up.

### Step 5: exact-base eviction, real CI service and DirCache

1. **P passes.** P's pass is cached.
2. **`main` goes red at X**, through an armed failure, and grain confirms it red.
3. **V and U fail.** V's job fails with `BaseCommit = X`, and U's job fails against Y, `candidate`'s tip.
   Both are cached with those bases.
4. **`base_commit` reads back.** `GET /api/ci/runs/{V}` returns `jobs[0].baseCommit = X`, and the
   stored run (`Store.GetCIRun`, `ci_job.base_commit`) agrees.
5. **`main` is turned green at X.** The armed failure is removed and an operator asks for a run
   (`RequestBaseBranchRun`).
   - **Bug F3**: the first attempt replayed main's own cached failure ("failure from cache"), so `main`
     could not go green at X from the button. Fixed below.
   - After the fix, the run at X passes and the log reads "base branch main recovered to green: evicted
     1 negative CI cache entries".
6. **Only V's failure is gone.** U's failure at Y is still cached, and P's pass is still cached: P's
   re-run is `success from cache`.
7. **No replay onto another base.** U's fingerprint is asked again at base X. The engine reports "cached
   failure tied to base commit Y invalidated (base advanced to X)", runs the job and passes. Y's failure
   is not replayed.

## Findings and fixes

### F1: a change its merge rules gate on GitHub's checks was batched

- **Reproduction**:
  - **Unit test**: `go test ./pkg/orchestrator -run TestAChangeGatedOnGitHubChecksIsNotBatched`. Three
    green queued changes are filed, and one touches `legacy/**` under a `ci: mode: both` rule. On the
    old code the batch is `t1,t2,t3` in all three positions.
  - **End to end**: `TestMergeQueueLanesContentionWalkthrough` without the fix. A1 merges while its
    `build` check is `in_progress`.
- **Cause**: `collectBatchCandidates` and `batchable` consult only the repository-wide gate
  (`repoIgnoresGitHubCI`). A batch is judged by grain CI alone, so a per-rule GitHub gate was skipped.
- **Fix** (`pkg/orchestrator/mergebatch.go`): a member whose recorded policy has `Mode.GitHub()` is not
  batched, and it stops collection for its branch. This matches how a repository-wide GitHub gate is
  already handled, and the change merges on the ordinary path once its checks pass.

### F2: batch row note named the repository, not the lane

- **Reproduction**: `go test ./pkg/orchestrator -run TestACandidateBatchFormsBehindAMainHeadStillInCI`,
  which now asserts each member's note.
  - Old note: "at the front of acme/widgets's queue".
  - New note: "at the front of the acme/widgets candidate queue".
- **Fix**: `setBatchState` names `batch.Repo` and `batch.Branch`.

### F3: an operator's by-hand base-branch run replayed the cached verdict

- **Reproduction**: `go test ./pkg/orchestrator -run TestARequestedBaseBranchRunWaitsForTheOneInFlight`.
  It now asserts that the requested run at the same commit is a `Recheck`. End to end,
  `TestCIFailureEvictedOnlyByGreenAtItsOwnBase` without the fix fails with "main never went green at
  X" and logs "failure from cache".
- **Cause**: `startBaseBranchCI` builds the same content-addressed request for a person's ask as for an
  automatic run. At a commit already judged red, it replays the failure in milliseconds.
- **Fix**: `req.Recheck = retry && requested`. A person asking again about the same commit gets every
  job run uncached, and the verdict is written back. Automatic runs, and runs on a moved base, are
  unchanged.
- **Test-helper change**: `fullRunsAt` now tells a confirmation apart by its `base-confirm:` requester,
  not by `Recheck`. A whole suite asked for again by hand is a recheck but still a full run.

## Tests added or changed

| Test | What it covers |
|---|---|
| `pkg/orchestrator` `TestAChangeGatedOnGitHubChecksIsNotBatched` | F1 (fails before the fix) |
| `pkg/orchestrator` `TestACandidateBatchFormsBehindAMainHeadStillInCI` | F2: lane named in batch notes (fails before the fix) |
| `pkg/orchestrator` `TestARequestedBaseBranchRunWaitsForTheOneInFlight` | F3: requested run is a recheck (fails before the fix) |
| `tests/e2e` `TestMergeQueueLanesContentionWalkthrough` | Steps 2–4 on the real queue |
| `tests/e2e` `TestCIFailureEvictedOnlyByGreenAtItsOwnBase` | Step 5 on the real CI service and DirCache |
| `ui/e2e/merge-queue-lanes-walkthrough.spec.js` | System label and value; the merges report's contention zeros; Metrics loads |

The `breakerWorld` rig gained `failBranch` (which branch's first batch fails) and `red` (an armed
failure for every run). `baseMarkerWorkflow` gained the matching `E2E_RED` check, which is inert unless
armed.

## Follow-ups

- Proposed: render `contention` (rebases, retests, queue wait) in Metrics → Merges, which shows none of
  it today.

## Local verification and CI verdict

- Ran locally and passing:
  - `go test ./pkg/orchestrator ./pkg/ci ./pkg/metrics ./pkg/ui`;
  - the two new `tests/e2e` tests and `TestMergeQueueContentionTelemetryMatchesWhatMerged`;
  - the Playwright spec (Chromium).
- grain CI: see the pull request. The full-suite result of `wait_for_checks` is recorded in the task's
  closing note.
- Sandbox note: `npx playwright install chromium` needs `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  behind the package-cache proxy, which otherwise fails with `SELF_SIGNED_CERT_IN_CHAIN`.
