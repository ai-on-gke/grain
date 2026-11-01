# 2026-11-01 — Review, merge and failure lifecycle walkthrough (test plan §10)

Plan: `docs/test-plans/2026-11-01.md` §10. Task 2225 (filed by task 2084).
Scope: PRs #211, #218, #297, #241, #260, #221, #253, #295.

## Environment

- Task sandbox, Debian, Go 1.26.10, Node 20.19, Chromium from
  `/var/cache/playwright` (chromium-1234).
- `grain` built from this branch (`ba8f8141`, with `make frontend` first, so
  the UI is embedded) and run as
  `grain demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:4290`.
  Requests were sent with the demo's access code as a bearer token.
- Pages were captured in Chromium at a 1440×900 viewport with a short
  Playwright script (`@playwright/test` from `ui/node_modules`).
- The demo seeds fake tasks and dispatches no agent runs. Its CI is
  `-fake-ci` and nothing executes. It therefore cannot produce a review
  task's `add_review_comment` relay, an owner's decision run, or a
  merge-queue re-check verdict. Those scenarios are scripted against the same
  GitHub sim (`pkg/githubsim`) as orchestrator and e2e tests, where a fake
  agent stands in for the agent. Those tests are deterministic, and each one
  is listed below as the reproduction.

## Scenarios

| # | Scenario | How it was driven | Result |
|---|----------|-------------------|--------|
| 1 | Review threads with "require conversation resolution" | `TestTwoRelayedThreadsOneRepliedToByAPersonMergeOnceThePersonResolvesIt`, `TestReviewTaskThreadsAreResolvedAfterOwnerRunHandlesThemAndPRMerges`, `TestGrainRelayedThreadAPersonRepliedInIsNeverAutoResolved`, `TestHumanReviewThreadIsLeftAloneAndReportedClearly` (`pkg/orchestrator/reviewthread_resolution_test.go`, over githubsim) | **Pass** |
| 2 | One task with two on-target PRs reaching the security hold | `TestTwoOnTargetPullRequestsHoldOnceAndStayStableAcrossTenCycles` (`pkg/orchestrator/securityhold_two_prs_test.go`) | **Pass** |
| 3 | Errored overlap re-check (no workflow answers the merge-queue ref) | `TestMergeRecheckErroredDoesNotSpinInAnInfiniteLoop` (`pkg/orchestrator/mergerecheck_test.go`), extended to count the runs a retry starts | **Pass** |
| 4 | Close and reopen a task with a live run | Live in the demo (below), plus `tests/e2e/close_reopen_streak_walkthrough_test.go` | **Pass** |
| 5 | Change-target proposal from an auto-merge parent | `TestChangeTargetProposalDoesNotInheritAutoMerge`, `TestChangeTargetHoldHoldsARefusedTaskUntilItIsFixed` (`pkg/orchestrator/change_target_hold_internal_test.go`), `TestAChangeTargetTaskWithNoCLIsNeverTurnedToAutoMerge` (`pkg/ui/change_target_test.go`) | **Pass** |
| 6 | Empty or deleted CL outside a run | `TestPollSendsATaskWhoseOnlyCLIsEmptyOrDeletedToAwaitingReply` (`pkg/orchestrator/change_submit_internal_test.go`), with core's stub `landingCLs` registered through `change.Register` | **Pass** |

### 1. Review threads

The sim's branch protection has `required_conversation_resolution` set. One
review relays two inline findings, a person replies in one of the threads,
and the owner answers. In the decision run:

- grain replies once in the thread nobody else spoke in, and resolves it;
- the thread the person replied in is left open;
- the merge is refused while that thread is open.

Once the person resolves their thread, the next cycle that polls the refusal
merges the pull request without anyone pressing Retry merge. The neighbouring
tests pin the rest of the fix:

- a thread another app replied in is never auto-resolved;
- a kept-as-is decision with no push still resolves its threads;
- the merge queue's belt-and-braces pass resolves decided threads and retries.

### 2. Security hold, two PRs

Both pull requests on the task touch the trust boundary. The task is held
once and files exactly one review. Its hold and clearance links stay the same
across ten `SyncSecurityHolds` and `SyncPullRequests` cycles. After the
review finishes clean:

- only the task's own pull request merges on that clearance;
- no second review is filed in ten more cycles;
- the hold note and the release note are each posted once.

With `holdIsAbout` forced back to pre-#218 behaviour, a second review is
filed on cycle 11.

### 3. Errored overlap re-check

The overlap re-check errors. One grain CI run is recorded, and then the task
is blocked with the error carried in its blocked reason. A person's Retry
merge starts exactly one more re-check beside the retried head run, and none
after that while it merges.

### 4. Close and reopen, live

Against the demo, with task 3 ("Bump the Go toolchain to 1.24") in
`running/live_run`:

```
POST /api/tasks/3/close  {"reason":"walkthrough §10.4"}  -> 200, state closed, reason closed/closed
POST /api/tasks/3/reopen {}                              -> 200, state running, reason running/live_run
GET  /api/tasks/3        -> no consecutive-failure count, no not-before/backoff
```

At 1440 px, the `/tasks/3` page shows "live attempt #1 transcript" and no
"Consecutive failed attempts" line. The e2e walkthrough runs the real
dispatch path. A task is closed while its scripted agent's `run_command` is
still running, and is cancelled by RunDispatch's store poll. After it is
reopened, it is dispatched on the very next cycle with no retry backoff, and
`grain get` reports no failed attempts. With `model.StoppedByPerson` forced
to false, the test fails with the `Consecutive failed attempts: 1` streak
that #241 removed.

### 5. Change-target proposals

A change-target task proposed by an auto-merge parent is filed with
`autoMerge` false. A setup refusal holds the task once, instead of
re-dispatching it every cycle, until the task is fixed.

### 6. Empty CL

Core does have a stub provider: tests register one through
`change.Register`. The empty-CL path is therefore in scope here, and it is
driven there rather than called out of scope:

- a task whose only CL is empty or deleted moves to `awaiting_reply`, with
  "holds no files" or "was abandoned or deleted" and "no live CL left" noted
  once;
- a task with a run in flight, or a queued task, is left alone.

The Google layer's real provider is not exercised.

### Pages

At 1440 px, `/inbox` lists task 4 ("Investigate the merge queue stall")
under "asked: reply" with Reply and Acknowledge. The notifications tab shows
3, and `scrollWidth` is 1440 (no horizontal overflow). The `/tasks/4` and
`/tasks/3` pages render with no overflow.

## Not covered live

- #221 (end-of-run holds not marked as `hold_for_approval`) and #295 (gate
  log dedup) need real runs and gate waits, which the demo does not produce.
  They rest on the unit tests that came with those PRs.
- The demo has no endpoint for posting a review comment as a review task's
  agent. A live version of scenarios 1–3 needs a `grain daemon` with
  sandboxed runs, which this sandbox's single-port setup cannot host
  alongside the git proxy.

## Bugs

- **`go` check red on main (fixed here, then on main too).**
  `TestSettingsLockedRefusesExactlyTheFieldsTheUIGreysOut` (fc9c9dcb) and the
  GCP project ID check (374a0736) landed independently. The test's sample
  `{"gcpProject":"proj"}` is a malformed ID. It is now refused with a 400
  before the lock gate can return 409, so `pkg/ui` failed on main and on
  every branch. The test now sends `my-project-123`. Refusing a malformed ID
  with a 400 is correct, so the server is unchanged. Main made the same fix
  independently (it also moved the service-account email onto
  `my-project-123`). The merge takes main's version, which leaves this
  branch no net change to the test.
- Nothing found in the §10 fixes themselves.
