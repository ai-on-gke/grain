# 2026-10-04: Merge queue and CI verdict correctness (test plan §4)

Task 1830 runs `docs/test-plans/2026-10-04.md` §4 ("Merge queue and CI verdict correctness").

## Environment

- Sandbox: Debian, Linux 6.1.0-53-amd64, Go 1.26.6, Node 20.19.0, Playwright 1.62.1 (chromium,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Go e2e harness: the real orchestrator and merge queue (`orchestrator.SyncPullRequestsGuarding`,
  the `ci` and `merge-batch` reconcilers) on an embedded SQLite store, `githubsim` for GitHub, and
  the package's fake grain CI runner (`fakeCI`) for runs.
- MCP: the real `grain mcpserver` binary over stdio, talking to a real `ui.Server`, whose
  `PullRequests` opener calls `orchestrator.OpenPullRequestForTask` against githubsim. This is the
  arrangement `grain daemon` builds.
- Browser: the real `grain demo` server (embedded SQLite) serving the production bundle built
  with `cd ui && npm ci && npm run build`, driven by Playwright through `ui/e2e/fixtures.js`.
- CLI: `grain force-merge` against a live `grain demo -addr 127.0.0.1:18420` daemon.

## Tested commit

- Base: `candidate` at `5ab9aa1ae` (Merge pull request #2850).
- `git branch -r --contains` lists `origin/candidate` and `origin/main` for every §4 commit:
  `a224f66aa`, `64b5383ab`, `bb42b0d5a`, `dbfcbc009`, `783807a3e`, `635b62474`, `78c6f3bb2`,
  `fe3dcedbe`, `d824c3aac`, and `fbd20123c` as well.
- The branch is `grain/task-1830`. Its commits are listed under Fixes.

## Commands

```sh
go test ./pkg/orchestrator/ -run 'TestAWaitingBaseEntryIsRetested|TestAFullyInheritedBatch' -count=1
go test ./pkg/orchestrator/ -count=1 -v -run 'TestAMergeWaitingOnAGitHubApproval|TestAnInheritedOnly|TestAWaitingEntry|TestAPartlyInherited|TestSecurityReviewIsClosed|TestOpenPullRequestRefuses|TestTheQueueNeverMergesAnEmptyDiff|TestSyncPullRequestsDoesNotForce'
go test ./pkg/ui/ -run 'TestForceMerge|TestOpenPullRequest|TestHTTPClientOpenPullRequest|SecurityHold' -count=1
go test ./tests/e2e/ -run 'TestMCPServerOpensAPullRequestOverStdio|SecurityHold' -count=1 -v
cd ui && npx vitest run src/linkedNote.test.jsx src/components/DetailOverlay.test.jsx
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/merge-queue-waiting-states.spec.js
/tmp/grain demo -addr 127.0.0.1:18420 -open=false -merge-queue-states &
grain -server http://127.0.0.1:18420 force-merge -head 0123456 13 "walkthrough: short head"
grain -server http://127.0.0.1:18420 force-merge -head 0123456789ABCDEF0123456789ABCDEF01234567 13 "walkthrough: upper"
grain -server http://127.0.0.1:18420 force-merge -head ffffffffffffffffffffffffffffffffffffffff 13 "walkthrough: wrong"
gofmt -l . ; cd ui && npm run format
make security
```

## Outcomes, step by step

### 1. `waiting_base` (Go harness): pass

- `TestAWaitingBaseEntryIsRetestedAndMergedOnceTheBaseIsGreen` (new) walks the whole round trip:
  1. `main` is red on `checks/go`, and the PR's run is red on `checks/go` only.
  2. The PR goes to `waiting_base`. The note reads "waiting for `main` to go green: `checks/go` is
     red there too (at …), so this is not this change's to fix -- no repair is spent and nothing
     is blocked…". `MergeQueueRepairAt` and `MergeQueueBlockedAt` stay nil.
  3. `main` gets a fix and goes green. The next cycle merges `main` into the branch, and the PR is
     not merged on its stale red.
  4. A fresh grain CI run starts against the merged head. When it passes, the PR merges, still with
     no repair spent.
- The existing `TestAWaitingEntryIsReArmedWhenTheBaseGoesGreen` stopped once `main` had been merged
  in. The new test covers the retest and the merge as well.
- Partly inherited: `TestAPartlyInheritedFailureIsRepairedAndNamesTheInheritedJobs` passes. A
  repair is charged, and its brief names `checks/test` as failing on `main`.
- Fully inherited batch: `TestAFullyInheritedBatchFailureIsReleasedNotBisected` (new) runs a batch
  of three that fails only on `checks/go` while `main` is red on `checks/go`. No further run starts
  (nothing is bisected), the batch row says "inherited and nothing was bisected", no member is
  left `batching`, and none is charged a repair or blocked.
  - No test covered this before.
  - Observation: a batch is not formed at all while the base's latest run is already red, so the
    test turns `main` red after the batch forms. That race is the realistic one.

### 2. `waiting_github_approval` (Go harness): pass

`TestAMergeWaitingOnAGitHubApprovalWaitsOutOfLineAndMergesWhenApproved` passes against githubsim's
405 with `RequireApprovals = 1` and a CODEOWNERS (`require_code_owner_review`) ruleset. It asserts:

- the state, and a note naming `@acme/platform`, `@bob`, "code owners", the ruleset and "1 approval
  still needed";
- one comment;
- no merge call over three further cycles (an hour of simulated time);
- that the PR behind it merges;
- a merge on the next cycle once `bob` approves.

No gap remained, so nothing was added.

### 3. Label, note and actions as a person sees them: pass after one fix

Seeded with the new opt-in `grain demo -merge-queue-states`, which writes one row in each state
through the store, the way the queue writes them.

|               | `waiting_base`                                                        | `waiting_github_approval`                                        |
| ------------- | --------------------------------------------------------------------- | ---------------------------------------------------------------- |
| Task list row | "grain · Waiting for the base branch to go green · 20m ago"           | "grain · Waiting for an approval GitHub requires · 20m ago"      |
| Note          | in the row's tooltip, and in full under the activity line in the pane | same                                                             |
| Review link   | none needed                                                           | **was plain text** (finding F1); now a link to `/pull/121/files` |
| Retry merge   | not offered (not blocked)                                             | not offered (not blocked)                                        |
| Force merge   | offered                                                               | offered                                                          |
| Inbox         | not listed (correct: nobody needs to act)                             | **not listed** (finding F4, filed)                               |

- Merge-queue view: the web UI has none. The only one is the MCP `merge_queue` tool, which flags
  the two states `[WAITING FOR BASE]` and `[WAITING FOR GITHUB APPROVAL]` and prints the note
  (`pkg/mcp/merge_queue_tool.go`). See F5.
- `ui/e2e/merge-queue-waiting-states.spec.js` (new) asserts everything in the table except the
  inbox row.

### 4. Force merge: server and CLI pass after one fix; not walked in a browser

- Server: `TestForceMergeIsForTheHeadTheOperatorReviewed` passes. It covers a moved head refused
  with "has moved since you looked … its head is now <sha>" and nothing stamped, an abbreviation
  stamped as the full sha, and no head named taking GitHub's head.
- CLI against a live daemon (`grain demo`, which has no GitHub and no CI commit for the task):
  - `-head 0123456` gives "0123456 is not a commit grain can confirm is the head of
    acme/widgets#120: name it in full". This is correct: with no GitHub to ask, only a full sha is
    taken as given.
  - `-head` with a full sha in upper case gave the same "name it in full" refusal, even though it
    is in full. This is finding F2, now fixed.
  - `-head ffff…ffff` (40 hex characters) is accepted, as documented: "Taken as said: the queue
    compares it against the real head before it merges anything." The queue's withdrawal of a
    moved head is pinned by `TestSyncPullRequestsDoesNotForceACommitPushedAfterTheOverride`.
- Browser race: not walked. `grain demo` has no GitHub behind it, so a head cannot be advanced
  under an open page (F6). The UI sends `t.ci?.commit`, the commit the page shows. The server
  refusal it would hit is the tested path above.

### 5. Landed work: pass after one fix

The new subtest of `TestMCPServerOpensAPullRequestOverStdio` runs these steps:

1. `open_pull_request` opens a PR.
2. The PR is squash-merged in githubsim's bare repo and marked merged.
3. `open_pull_request` is called again from the same task through the real `grain mcpserver`.

Result: no second PR, and nothing left open. The reply reads "This branch's changes already landed
on main as b74edc88ec11 (acme/widgets#9000); nothing to open. Every file grain/task-77 changes
already has the same content on main…". It used to end "(500 Internal Server Error)"; that was
finding F3 and is now fixed, so it ends "(409 Conflict)".

"A repair on an empty diff does not merge" is covered by `TestARepairThatResolvesToAnEmptyDiffClosesThePullRequest`
and `TestTheQueueNeverMergesAnEmptyDiff`, both of which pass.

### 6. Security review closure: pass in Go; not walked in a browser

- `TestSecurityReviewIsClosedWhenTheChangeItReviewsMerges` and `…IsClosed` pass, as do
  `TestSecurityReviewThatFinishedIsLeftAloneByTheMerge`, the `pkg/ui` security-hold tests and the
  `tests/e2e` `TestSecurityHoldWalkthrough` and `TestSecurityHoldV2Walkthrough`.
- `grain demo` seeds no held change, so the task pane's hold and review link were not seen in a
  browser in this run.

### 7. `retrigger_ci` and the cached verdict: not run, because the deployed build lacks it

- The descriptions this deployment serves its agents don't carry `fbd20123c`'s wording:
  - `retrigger_ci` says only "Bypasses cached job results."
  - `wait_for_checks` doesn't say that a cached red a recheck has cleared is no longer replayed.
- Source on `candidate` says both (`pkg/mcp/grain_ci_wait_tool.go`), so the running daemon predates
  `fbd20123c` and probably `78c6f3bb2`. As the plan says, the flaky-job experiment was not run.
- **Repository note on cached failures: still accurate.** Keep the fingerprint step (merge the base,
  or make a small real edit) until those descriptions appear. Retire the note on the first run that
  sees "each job's fresh success or failure is then saved as the cached verdict" in
  `retrigger_ci`'s description.

## Findings

| #   | Finding                                                                                                                                             | Reproduction                                                                                                                                          | Disposition                                                                                                 |
| --- | --------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------- |
| F1  | The task pane renders the merge queue's note as plain text, so the GitHub review page a `waiting_github_approval` note names cannot be clicked.     | `grain demo -merge-queue-states`, open task "Move the billing exporter…": `document.querySelectorAll('a[href*="pull/121/files"]').length === 0`.      | Fixed (`LinkedNote`).                                                                                       |
| F2  | `grain force-merge -head <SHA in upper case>` is refused ("name it in full" for a full sha), and an upper-case abbreviation never matches.          | `grain force-merge -head 0123456789ABCDEF0123456789ABCDEF01234567 13 "x"` against `grain demo -merge-queue-states`.                                   | Fixed (lower-cased in `ui.Client.ForceMerge`).                                                              |
| F3  | `POST /api/tasks/{id}/pull-request` answers 500 when the branch's work already landed, so an agent's `open_pull_request` reads it as grain failing. | `go test ./tests/e2e -run TestMCPServerOpensAPullRequestOverStdio -v` on the base with the new subtest: the reply ends "(500 Internal Server Error)". | Fixed (409 Conflict).                                                                                       |
| F4  | The inbox has no section for `waiting_github_approval`, though only a person can end it.                                                            | `grain demo -merge-queue-states`, open `/inbox`: the task is not listed.                                                                              | Filed with `propose_task` ("Inbox: surface pull requests waiting on a GitHub approval"), depending on 1830. |
| F5  | The plan's "merge-queue view" doesn't exist in the web UI. Only the MCP `merge_queue` tool lists the queue.                                         | `grep -rl "Merge queue" ui/src/components` finds only RepoPage's settings.                                                                            | Recorded only; the plan's assumption was wrong.                                                             |
| F6  | `grain demo` has no GitHub, so the browser force-merge race and the security-hold pane cannot be walked in it.                                      | —                                                                                                                                                     | Recorded; covered by the Go tests named above.                                                              |

## Stubs

- **GitHub:** `githubsim` (Go harness and MCP e2e), with its `RequireApprovals`, `Approvals`,
  `RequestedReviewers`, `BranchRules` and `RulesetNames` for the 405; a bare git repo for branches
  and the squash merge.
- **grain CI runner:** `pkg/orchestrator`'s `fakeCI`, whose verdicts are set by the test. Base
  branch verdicts are written with `store.PutBaseBranchCI`, as `reconcileBaseBranchCI` does.
- **Browser seeding:** `grain demo -merge-queue-states` (new, opt-in) writes the two rows through
  the store. Note texts follow `waitingBaseNote` and `approvalNote`. No API or React is mocked, and
  no `page.route` is used.
- **Live model sessions:** none used. The MCP server is driven directly by the test.
- `grain demo`'s own stand-ins (no GitHub, no orchestrator) apply to the CLI run.

## Fixes (branch `grain/task-1830`)

- `test(orchestrator)`: `pkg/orchestrator/inherited_walkthrough_test.go`. These are the full
  `waiting_base` re-arm → retest → merge, and the fully inherited batch released without a bisect.
- `fix(ui)` F1: `ui/src/linkedNote.jsx` (with `linkedNote.test.jsx`) is used for the pane's
  queue note. `grain demo -merge-queue-states` (`cmd/grain/demo_merge_queue.go`) and
  `ui/e2e/merge-queue-waiting-states.spec.js` add the e2e assertions for both states.
- `fix(ui)` F2: `pkg/ui/client.go` `ForceMerge` lower-cases the asked head. There are two new
  subtests in `pkg/ui/force_merge_test.go`, and the full-sha one fails without the fix.
- `fix(ui)` F3: `pkg/orchestrator/landed.go` gives `alreadyLandedError` a `NothingToPropose()`
  method, and `pkg/ui/pull_requests.go` maps it to 409.
  - Tests: `TestOpenPullRequestForWorkThatAlreadyLandedIsAConflictNotAServerError`, and the
    squash-merge subtest in `tests/e2e/mcpserver_open_pull_request_test.go`.
  - This touches `pkg/orchestrator`, so the trust-boundary hold is expected.

`gofmt -l` is clean, `npm run format` has been applied, and `make security` passed
(`tests/security` in 145s, with `go vet ./...` clean).

## CI verdict

- Run `970c299211927041` on `067a73d` (first full run): red on two jobs, neither caused by this
  branch.
  - `go`: `TestDesignDocMergeFilesOneDesignPlannerTask` ("filed 0 tasks, want one design
    planner"). Grain flagged it as a shared fault with `grain/task-1803`, and it passes 3/3 locally
    with `go test ./pkg/orchestrator -run DesignDoc -count=3`.
  - `ui e2e`: webkit `responsive-navigation-walkthrough.spec.js:415` timed out in `page.goto("/")`
    waiting for `load`. Its history shows 97 passes and no failures, and the new
    `merge-queue-waiting-states.spec.js` passed in the same run.
- `retrigger_ci` run `601f513d1e8759fb` (uncached) on the same commit: every job green, so both reds
  were flakes.
- `wait_for_checks` with no new push, run `4bb37a9d58f05b22`: green.
  - Every job ran in full (`go` 16m16s, `ui e2e` 11m24s), so the gate neither replayed the cached
    red nor reused the recheck's green.
  - That is what a deployed build without `78c6f3bb2` should do. `git merge-base --is-ancestor`
    confirms the build these runs report, `11606c9`, contains neither `78c6f3bb2` nor `fbd20123c`.
  - This is step 7's experiment, done on this branch. The repository note stays as it is.
- `candidate` itself went red after this branch was cut: `go vet` fails in
  `tests/e2e/port_walkthrough_test.go` at `6772d7c`. That is not merged here.
- The final commit's full-suite verdict is in the task's closing note.
