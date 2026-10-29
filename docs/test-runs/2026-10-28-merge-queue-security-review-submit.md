# Merge queue without batching, in-place security reviews and Submit head pinning

- **Plan:** `docs/test-plans/2026-10-28.md` §8 (in the retired `bwsalmon/grain`; restated in task 1721)
- **Date:** 2026-10-29, 00:28–01:30 UTC
- **Task:** 1721 (origin: task 1549), branch `grain/049f1e09/task-1721`, base `main` at `0dc554f`
- **Environment:** Debian sandbox on `kubernetes-granule`, `linux/amd64`, Go `go1.26.10`, Node from the image, Playwright Chromium from `/var/cache/playwright`, the `grain` binary built from this tree (`go build ./cmd/grain` after `make frontend`). githubsim is the only GitHub; grain CI is the in-process fake (`-fake-ci` for the demo, `fakeCI` in `pkg/orchestrator` tests).

## How it was driven

`grain demo` runs no merge queue: it serves the UI over a seeded store, and nothing in it advances a pull request (`pkg/demo/demo.go`: "The demo runs no queue"). It has no route to push a commit to the simulated repo, either. So the plan's procedure was split between what each harness can really do:

| Step | Driven through | Test |
|---|---|---|
| 1. Two overlapping PRs, no batch, re-check after the first merges; no re-check of a head pushed after clearance | The real merge queue (`orchestrator.SyncPullRequestsGuarding`) over githubsim and a fake grain CI | `pkg/orchestrator/mergequeue_walkthrough_20261028_test.go`: `TestWalkthroughOverlappingChangesMergeOneAtATimeWithNoBatch`, `TestWalkthroughPushDuringRecheckIsNotMergedOnThatRecheck` |
| 2. Submit, then a push before the merge cycle | `ui.Client.Submit` (what the button posts to) with the daemon's GitHub head reader wired, then the real queue; and the button itself in Chromium over `grain demo -github-sim -fake-ci -merge-rules` | `TestWalkthroughSubmitThenPushDoesNotMergeTheUnseenHead`; `ui/e2e/merge-queue-submit-retry-walkthrough.spec.js` "Submit on a held change…" |
| 3. Trust-boundary hold, review fixes on the held branch, hold until a human submits | `RunCycle` end to end (dispatch, classification, `SyncSecurityHolds`, the queue) over a real git server, githubsim and a scripted agent | `tests/e2e/security_review_inplace_walkthrough_test.go`: `TestHumanOnlyHoldKeepsAnInPlaceReviewFixUntilSubmit` |
| 4. Block a merge, Inbox "Retry merge" | Chromium over `grain demo -github-sim -fake-ci -merge-rules`, the click going to the real server (the existing `inbox-retry-merge.spec.js` stubs it) | `merge-queue-submit-retry-walkthrough.spec.js` "Retry merge in the inbox…" |

## Observations

### Step 1 — no batch, one re-check at a time

- t1 (`pkg/alpha/a.go`) and t2 (`pkg/alpha/b.go`), both green on their own heads and both mergeable, in one cycle: **t1 merged; t2 did not.** The one `merge_batches` row is t2 alone (`Members == [t2]`), and exactly one grain CI run was started for it. No row ever holds two changes.
- The re-check passed and the next cycle merged t2.
- `merge_recheck` history: t1 untriggered and not re-checked; t2 `Triggered`, `Rechecked`, not `Caught`, clause `same-package`.
- A commit pushed to the branch while its re-check ran: the re-check's pass did not merge it, and the queue did not start a re-check of the pushed head on the strength of the earlier clearance.
- With `-merge-rules`, `/merge-queue` renders the seeded changes. It also shows a **two-change "Testing Batch" row**, which is finding F1.

### Step 2 — Submit pinned to the head seen

- Change held for Submit (auto-merge off, green at head `150d706`). Submit went through `ui.Client.Submit` with `GitHubPullRequests` wired, as `pkg/daemon/ui_server.go` wires it. It recorded `human-submitted …@150d706…`, the head the human was shown.
- A non-trust-boundary commit (`a7a1c07`) was then pushed before the merge cycle. The cycle logged `submitted by a human at 150d70689ec8, and its head is now a7a1c0713f65 -- classifying that head rather than pinning the Submit to it`. Nothing merged; the row reads `waiting_ci` "waiting on a full-suite grain CI pass for this commit". The pushed head did not merge on the seen head's verdict.
- In the browser, Submit on "Roll the ingress controller to v2" (held by `auto_merge: never`) posted `POST /api/tasks/18/submit` → 200. The button went away and the task read back `autoMerge: true`.

### Step 3 — in-place review, human-only hold

- The change rewrote `.grain/baselines/lint.json` (ci-suppressions, human-only). It was held with a `human-only-hold` and no releasable hold link. Its security review was filed on the held change's own branch `grain/task-1`.
- The review pushed `{"x":2}` to `grain/task-1`, and the branch head moved. The review opened no branch or pull request of its own.
- Over six more cycles, with the PR clean: no merge, and auto-merge stayed off after the review finished.
- After a human Submit, the next cycles merged the change, and `main:.grain/baselines/lint.json` holds the reviewer's fix.

### Step 4 — Retry merge

- "Raise the API pod memory limit" was listed under "Unblock a merge" with reason `completed/merge_failed`.
- Clicking Retry merge sent `POST /api/tasks/19/retry-merge` → 200. The row left the group, and the task reads `completed/on_merge_queue`.

The existing specs for these features passed unchanged on this tree: `inbox-retry-merge`, `merge-on-green`, `merge-rules-walkthrough`, `merge-queue-waiting-states` (12/12 on Chromium).

## Findings

**F1 — `grain demo` still seeds the retired speculative batch (filed as a follow-up).** `pkg/demo/demo.go` writes a two-member `merge_batches` row. Both members carry `batching` / `batching/batch_run` and the old note "testing this change together with 1 other(s) … one suite for the batch, and all of them merge if it passes … a failure is bisected towards it". The queue can no longer write any of that. `orchestrator.queuestatus.go` calls `mergeStateBatching` retired, and the only note the queue writes now is `setRecheckState`'s "re-checking this change on top of main". As a result:

- The demo's `/merge-queue`, task pages and repo page "Merge batches" panel ("2 changes on main", "first -- a failure is halved towards this end") show a state the product cannot reach.
- `task-reason.spec.js`, `dependency_automerge_revert_walkthrough.spec.js` and `pkg/demo/demo_test.go` assert on that state.

Fixing it means re-seeding a single-change overlap re-check and renaming the panel's wording, across Go, the UI and three specs, with screenshots. That is beyond a scoped fix here, so it was proposed as its own task.

Reproduction: `grain demo -open=false`, then open `/merge-queue`. Or `curl /api/merge-batches`: a row with two `members`.

**F2 — a push after Submit merges once it is green on its own (question for the owner).** The pin works the way it was written: the pushed head is classified again, and it does not merge on the seen head's verdict. A push that touches no trust-boundary path is cleared by that classification, though. Once its own grain CI passes it merges, and no person has looked at it. The plan's wording ("the merged head is the one seen, or the merge is refused with a reason") is stricter than that. Also:

- The Submit button sends no head. `submittedHead` reads it at the press, and the remaining page-load→click window is documented as accepted in `pkg/ui/client.go`.
- Force merge does send the head it showed.

Whether Submit should mean "this head" rather than "auto-merge on from here" is a product decision. It is raised on the pull request rather than changed here.

**F3 — a Submit nothing could read a head for costs a second review (observation; fails safe).** The case: `ui.Client` has no GitHub head reader, or the read fails, and there is no grain CI commit to fall back to. Then the marker names no head, and `pinHumanSubmit` takes it to mean the last classified head. Over a branch an in-place review has pushed to, that is the pre-fix head. The fixed head is classified again, re-held, and a second security review is filed, so the human has to Submit again.

Reproduction: `TestHumanOnlyHoldKeepsAnInPlaceReviewFixUntilSubmit` without its `w.ui.Config.GitHubPullRequests` line. This is the deliberate rule `TestHumanSubmitNamingNoHeadIsNotPinnedToAPush` covers, so it was left as is. The daemon always wires the reader, so this needs a failed GitHub read at the moment of the press.

## Fixes

None to product code: no step showed a defect in the features under test. The tests above are new regression coverage for the plan's paths.

## Outcome

§8 passes. No batch forms. The overlapping change is re-checked alone after the first one lands. Neither a head pushed after clearance nor a head pushed after Submit merges on an earlier verdict. Security reviews push their fixes to the held branch, and a human-only hold stays until a person submits. Inbox Retry merge returns a given-up change to the queue. F1 is filed as follow-up work, F2 is open as a question to the owner, and F3 is recorded.
