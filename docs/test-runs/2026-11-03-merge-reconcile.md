# 2026-11-03 — Merge queue and pull-request reconciliation walkthrough (test plan §7)

Plan: `docs/test-plans/2026-11-03.md` §7. Task 2713 (filed by task 2697).
Scope: PRs #485 (external-check nudge), #444 (every member's stall), #535
(closed-task PRs), #534 (node-ID GraphQL routing), #490 (Actions
pagination), #496 (`comment_on_issue` relay).

Read first, and not repeated: `docs/test-runs/2026-11-01-review-merge-lifecycle.md`
(review threads, security hold with two PRs, errored re-check, close/reopen,
change-target, empty CLs). No 2026-11-02 merge-walk report had landed (the
plan's carried-over §4 is still open).

## Environment

- Task sandbox, Debian, Go toolchain from the image, checkout of `main` at
  `3d811a7d`.
- **Which runtime.** Shared step 3 offers `ui/e2e/consolerestserver` or a real
  daemon. `consolerestserver` has no GitHub client at all (it runs the
  console-session walkthrough with no forge), so it cannot hold a pull
  request. The walkthrough instead uses the `tests/e2e` harness, which is
  the real store, `orchestrator.RunCycle` (dispatch, finish, every
  reconciler), a scripted agent provider whose `run_command` really clones,
  commits and pushes to a bare upstream, and `githubsim` as GitHub. That is
  the same cycle a daemon with `-github-sim` runs, with a clock the test
  controls. Items 4 and 5 go through the daemon's `forgeRouter` and the UI
  server's `/api/ci/runs` handler, which is the endpoint `grain ci list` hits.
- **Lowered threshold.** `orchestrator.SetExternalCheckStallThreshold(2m)`,
  restored in `t.Cleanup`. There is no daemon flag for it. Because it is
  package state, the walkthrough tests are not `t.Parallel()`: Go runs every
  non-parallel test before it releases the parallel ones.
- No real agent backend was used, and none was needed.

Scripts: `docs/test-runs/2026-11-03-merge-reconcile/run.sh` runs every test
named below.

## Results

| # | Item under "What is NOT proved" | How it was driven | Result |
|---|---|---|---|
| 1a | Nudge, behind base | `TestMergeReconcileStalledExternalCheckBehindBaseNudgesOnceThenEscalates` (`tests/e2e/merge_reconcile_walkthrough_test.go`) | **Pass** |
| 1b | Nudge, up to date | `TestMergeReconcileStalledExternalCheckUpToDateRerequestsOnceThenEscalates` | **Pass** |
| 1c | No nudge on a PR held for a person | `TestMergeReconcileStalledExternalCheckOnAPullRequestHeldForAPersonIsNeverNudged` | **Pass** |
| 2 | Two-member lane | `TestMergeReconcileSecondLaneMembersStallEscalatesWhileTheFirstMerges` | **Pass** |
| 3 | Hand merge after close | `TestMergeReconcileHandMergeAfterCloseIsRecordedThroughTheCycle` | **Pass**, with a design gap for parked waiters (finding 2) |
| 4 | Node-ID routing to a second host | `TestNodeMutationsOnASecondRESTHostReachThatHostOnly` (new, `pkg/daemon`), plus the existing forge-CLI-host tests | **Pass** |
| 5 | More than 100 workflow runs, `--workflow nightly.yml --since 26h` | `TestCIRunsListOverGitHubSimFindsNightlyRunsPastTheFirstHundred` (new, `pkg/ui`) | **Bug in the sim, fixed** (finding 1) |
| 6 | Short `comment_on_issue` kept over longer final text | `TestMergeReconcileShortCommentOnIssueIsTheClosingNoteOverALongerFinalText` | **Pass** |

### 1. External-check nudge

Every scenario uses an auto-merge task. Its scripted run pushes one commit
and opens a PR. The PR's head carries a green `Tests / unit` (app
`github-actions`) and `cla/google` (app `google-cla`, a check-suite id)
stuck `queued`. The threshold is 2m.

**Behind base.** `main` is advanced after the PR opens.
- t=1m: the pending clock starts.
- t=2m: the row reads ``waiting_ci`` / `` `cla/google` pending 1m ``, and nothing is merged.
- t=4m: one `POST /merges`. The PR branch now contains `main`. One PR comment
  and one task note say `` `cla/google` was pending for 3m on `<sha>`; merged `main` to re-trigger it. ``
  The PR is not merged.
- `main` moves again. t=5m and t=6m: no second base merge. The row note ends
  `; nudged once`, and nothing has been escalated yet.
- t=8m: exactly one open `pipeline.merge-stalled:ext-behind`. Still one base
  merge, and the PR is still unmerged.
- `cla/google` turns green, and the next cycle merges the PR and clears the
  notification.

**Up to date.**
- t=4m: the nudge asks `/merges`, and the sim answers 204 as GitHub does
  for a branch already up to date. The branch head SHA is unchanged, so
  nothing was pushed. Check suite 601 is re-requested once, and one PR
  comment and one task note end `re-requested it.`. The row note ends
  `; nudged once`.
- t=7m: still one re-request, one `pipeline.merge-stalled:ext-current`, and
  the PR is unmerged.

**Held for a person.** The same PR with `auto_merge` off sits in
`awaiting_submit`. Cycles at 1, 4, 8, 15 and 40 minutes made no base merge,
no re-request and no PR comment, raised no notification, and did not merge.
The paused and in-flight variants are already pinned by
`TestInFlightPausedAndAwaitingSubmitPullRequestsAreNotNudged`
(`pkg/orchestrator/external_checks_test.go`), so they were not repeated.

### 2. Two-member lane

Two auto-merge tasks touch different files and both have `cla/google`
pending from t=1m. At t=4m the first one's check reports. **In that one
cycle** the first PR merges and the second member is nudged: one
`POST /merges` brings it up to the `main` the first just merged into, with a
task note. At t=8m the second member has one `pipeline.merge-stalled:lane-2`,
the merged first member has none, and the second PR is still unmerged. The
member behind the one that merged is read and acted on, not left unread.

### 3. Hand merge after close

The PR is green, but the sim refuses the merge (`RefuseMerges` plus a
`required_review_thread_resolution` rule). The queue hands it to a person:
`MergeQueueBlockedAt` is set and the row has a note. The task is closed at
t=3m. At t=4m the PR is still open, and a wait `until: merged` reads
*settled, unmet*. The PR is then merged by hand in the sim at t=10m. The
first cycle one idle-read cap later (`github.PollCap`, 8m) records:
- `PrMergedAt` = the forge's merge time;
- `MergeQueueBlockedAt` cleared, and the row's state and note empty;
- `WaitTargetOf(...).Settled(true)` = met, outcome `merged`.

All of this happens through `RunCycle`, which proves `syncClosedPullRequests`
is wired into the cycle and not only into `SyncPullRequests`.

### 4. Node-ID routing

The plan asks for a repository on a second simulated host.
`pkg/daemon/forgeroute_graphql_test.go` already drives `grain pr resolve`
(`PullRequestOpener.ResolveForTask`, the same code as MCP
`resolve_review_thread`) and the orchestrator's `ResolveReviewThread`
against a **forge-CLI** second host. Both pass here.

The case it did not cover is a second **REST** host. The new
`TestNodeMutationsOnASecondRESTHostReachThatHostOnly` puts one `githubsim`
behind `forgeRouter.def` (github.com) and another behind `transportFor`
(`ghes.example.com`), and logs each one's requests:
- `resolveReviewThread` and `enqueuePullRequest` for `ghes.example.com/acme/widgets`
  resolve the thread and enqueue the PR on the second sim;
- the default sim sees **no** request;
- the same mutation for bare `acme/widgets` goes to the default sim only.

### 5. Pagination — bug in the sim (fixed)

**Reproduction** (unmodified `pkg/github/githubsim`, 150 seeded runs):

```
GET /repos/acme/widgets/actions/workflows/nightly.yml/runs?per_page=30
  -> panic: githubsim: unhandled request GET /repos/acme/widgets/actions/workflows/nightly.yml/runs?per_page=30
GET /repos/acme/widgets/actions/runs?per_page=100&created=>=2026-11-02T00:00:00Z
  -> 200, every one of the 150 runs in one page, no Link header
```

PR #490's per-workflow listing (`ListActionsRunsPage` tries
`/actions/workflows/{file}/runs` first) could therefore not be driven on
`-github-sim`. `grain ci list --workflow nightly.yml` against a sim repo hit
the panic, and the repo-wide listing never paged, so ">100 runs" was not
something the sim could show. `WorkflowRun` also had no creation time, so
`--since` had nothing to filter on.

**Fix** (`pkg/github/githubsim/githubsim.go`):
- `WorkflowRun.CreatedAt` is served as `created_at` and `run_started_at`.
- `pageRuns` applies GitHub's `branch`, `created>=` and `per_page` (≤100) /
  `page` to the repo-wide listing, with `total_count` covering the whole
  match and a `Link: <…>; rel="next"` header.
- New `.../actions/workflows/{id}/runs` filters by workflow file or id, and
  404s for a workflow no seeded run comes from, as GitHub does.
- A request with no `per_page`/`page` is still served whole, so the earlier
  callers (`RecentRunStarts`, `CompletedWorkflowRuns`) read what they always
  did. `pkg/github/...`, `pkg/ui`, `pkg/daemon`, `pkg/demo`, `pkg/ci`,
  `pkg/sandboxcli`, `pkg/forge` and `pkg/orchestrator` all pass.

**Regression test.** `TestCIRunsListOverGitHubSimFindsNightlyRunsPastTheFirstHundred`
seeds, newest first, 140 PR runs four minutes apart, then three nightly runs
at 10h, 25h and 34h old. It then calls the UI server:
- `/api/ci/runs?…&provider=github-actions&workflow=nightly.yml&since=26h`
  lists exactly the 10h and 25h nightly runs;
- `limit=200` lists all 143, over at least two sim pages that follow the
  `Link` header.

Not walked: the release watchdog's §2b query (plan §5 item 6) belongs to the
§5 walkthrough. Its listing goes through the same `ListActionsRunsPage`.

### 6. Relay

A scripted run calls `comment_on_issue` with `Nothing needed: the flake is
already tracked.`, then emits about 900 characters of final text. The
task's relayed note is exactly the short report, and none of the final text
is relayed.

## Findings

1. **Bug (fixed): `githubsim` could not answer the Actions listings PR #490
   reads.** The per-workflow endpoint panicked, and the repo-wide one
   ignored `per_page`/`page`/`created`/`branch`. Reproduction and fix are
   above, and the regression test is in `pkg/ui`.
2. **Design gap (not changed): a park `until: merged` resumes at the close,
   before a later hand merge.** `wait_for_task`'s park adds the target to
   `depends_on`, and readiness is closure (`ResumeOn.UntilMerged` is never
   read by `Store.Ready`). The tool's own description says so: "A park
   resumes on closure either way". So when a task with an open PR is
   closed, the waiter resumes at once and reads "closed, not merged". When
   the PR is merged by hand afterwards, #535's pass correctly turns the
   target's outcome to `merged` (item 3), but the waiter has already been
   told otherwise and is not woken again. A blocking `wait_for_task` (no
   park) behaves the same way, because `Settled(true)` reports
   settled-unmet at the close. Changing this means deciding how long a
   waiter may sit on a closed task's open PR (the closed-PR pass watches
   for 30 days), so it is filed as a follow-up and not changed here.
   *Since resolved (task 2757):* an `until: merged` wait now keeps waiting
   while grain still reads the closed task's open PR, and the item 3 test
   pins that instead.
3. **Observation: `effectiveCommentOnIssue` prefers the final text whenever
   the comment ends in `...` or `…`.** A short report that ends in an
   ellipsis on purpose ("Waiting on #12...") is then replaced by whatever
   longer prose follows. This is within #496's stated heuristic and was not
   changed.
4. **Harness note:** `ui/e2e/consolerestserver` has no forge, so it is not
   an option for any of §7. The plan's shared step 3 could say so.

## Commands

```
go test ./tests/e2e/ -run TestMergeReconcile -count=1 -v
go test ./pkg/daemon/ -run 'TestNodeMutationsOnASecondRESTHost|TestGrainPRResolveOnAForgeCLIHost|TestResolveReviewThreadOnAForgeCLI' -count=1 -v
go test ./pkg/ui/ -run 'TestCIRunsList' -count=1
```
