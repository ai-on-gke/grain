# Test Run: Liveness Sub-Cases & Row Controls, Force-Merge Hung Run, Red-Base Repair Alerts, Stacked Base Retargeting

- **Date:** 2026-10-26
- **Plan Reference:** `docs/test-plans/2026-10-26.md` §2
- **Task:** [Task #1200](/tasks/1200) (origin [Task #1140](/tasks/1140))
- **Branch:** `grain/049f1e09/task-1200`
- **Commits under test:** liveness sub-cases `bdb163791`, `c83ffcc66`, `8e21a09fa` (PR #4634), `b6bd5a174` (PR #4840); force merge of a running / `awaiting_reply` task `e0fdcc3f4`, `7e3c3d85c` (PR #4888); red-base repair alert `35bb3400d` (PR #4880); stacked base retargeting `60c591f48`, `0443e0274` (PR #4742) and `nothing_needed` PR preservation `53c74b9b6` (PR #4822).

---

## 1. Summary

Every item in §2 was walked against a live `grain demo -github-sim -fake-ci -merge-rules` daemon. The UI parts were driven in Chromium with Playwright at 1440×900 and 390×844, and the server parts over the HTTP API and the simulated GitHub's bare repo. Almost everything behaved as the commits describe.

The walkthrough found **two UI bugs**, both fixed on this branch with regression tests:

1. **`/system/health` opened on the wrong page on a phone.** A direct load, reload or shared link to `/system/health` at 390px showed System's list of tabs, not Health. That hid the `red-base-repair/<id>` alert and its buttons until the person tapped Health a second time. `systemTabFor` folded `health` into the bare `""`, which means Health on a desktop but the tab list on a phone.
   - **Fix:** `ui/src/settingsSections.js` now keeps `health`.
   - `ui/src/App.jsx` folds it back to `""` on a wider screen, the same way Settings fills in General, so desktop still canonicalizes to `/system`. `openSystem("health")` now keeps the tab on a phone too.
   - **Tests:** `ui/e2e/system-health-phone-deep-link.spec.js` (phone with and without reload, plus desktop canonicalization); `ui/src/paths.test.js` updated.
2. **The inbox never offered Retry merge.**
   - `bdb163791` gives the stalled group "Retry merge on a change the queue gave up on". But every such change (completed, `merge_failed`) is claimed first by `merge_blocked` ("Unblock a merge"), whose only action was "Open pull request".
   - So in the seeded demo, #14 ("Raise the API pod memory limit") sat in `/inbox` with no way to retry. Its row in `/` did offer the button.
   - **Fix:** `merge_blocked` now offers the task's sub-case control when it is Retry merge (`POST /api/tasks/{id}/retry-merge`), next to Open pull request. A stop that names no control gets no button.
   - **Tests:** `ui/src/components/InboxPage.test.jsx` (fails without the change); `ui/e2e/inbox-retry-merge.spec.js` asserts with bounding boxes that the button sits in the action slot, stays on screen and clears the title at both widths.

The branch also carries the demo wiring from an earlier attempt at this task (`8e2a39dda`), which is what makes the force-merge walkthrough possible under `grain demo`:
- **PR reader:** `demoPullRequests` stands in for `ui.Config.GitHubPullRequests`. It reads the simulated GitHub when `-github-sim` is on, and otherwise synthesizes a head for pull requests seeded straight into the store.
- **Grants:** `-merge-rules` gives already-seeded tasks the default `grain-ci` grant.
- **Test:** `pkg/demo/demo_test.go`'s `TestDemoForceMergeOnRunningTaskAndStackedBaseRetargeting` drives the force-merge and retarget flows end to end through that wiring.

## 2. Environment

- Sandbox: Debian, kernel 6.1, Go 1.26.10, Node v20.19.0, Playwright 1.62.1 (Chromium from `/var/cache/playwright`).
- Built `ui/` (`npm run build`) and `cmd/grain` (`go build -o /home/debian/grain ./cmd/grain`) from `640e902ab` (the branch tip at the start of the run).
- Daemon: `grain demo -github-sim -fake-ci -merge-rules -open=false -addr 127.0.0.1:4290 -access-code-file …`. The Playwright contexts signed in through `POST /api/auth/access-code`, the same way `ui/e2e/accesscode.js` does; curl used the code as a bearer token.
- Installed during the run: `sqlite3`, to stub store rows the demo has no API for.
- Stubs, each standing in for something the demo lacks:
  - The red base branch is one `base_branch_ci` row (`conclusion = failure`, `repair_task_id = 6`). The demo has no base-branch CI.
  - Task #4's pull request is one `task_link` row (`fixes`, `acme/widgets#105`).
  - The upstream merge is a `task_observation` row with `pr_merged_at` for task #15, plus the stacked task's `origin_reason = review`. No public API sets either.
  - A stall note was added to `GET /api/tasks` answers with `page.route`. `grain demo` wires no `ui.Config.Stall`, so no task is ever stalled there; `ui/e2e/task-reason.spec.js` uses the same stub.

## 3. Walkthrough and results

### 3.1 Liveness sub-case chips and stalled-row controls

| Step | Result |
| --- | --- |
| `/` at 1440: each open row's outlined `.task-reason` chip and its hover title | ✅ The chip text and the title's first line agree on every row, e.g. `ready` / `queued/ready`, `live run` / `running/live_run`, `question` / `awaiting_reply/question`, `blocked on task` / `queued/blocked_on_task`, `merge failed` / `completed/merge_failed`, `on merge queue` / `completed/on_merge_queue`, `not auto merge` / `awaiting_submit/not_auto_merge`, `unapproved` / `proposed/unapproved`. |
| `MergeQueueReason` on the merge-queue activity line | ✅ `batch run` on #5 and #6 (`batching/batch_run`), `github checks pending` on #13, `given up` on #14. After Retry merge moved #14 to `completed/on_merge_queue` by a direct `MergeQueueState` write, both the merge-queue reason and the chip updated (`b6bd5a174`). |
| `/` row control **Run now** on stalled #2 | ✅ `POST /api/tasks/2/run-now` → 200. The task stays `queued/ready`, since nothing dispatches in the demo. |
| `/` row control **Undefer** on stalled #9 (`/?state=deferred`; the default list hides deferred rows) | ✅ `POST /api/tasks/9/undefer` → #9 goes from `deferred/wake_time` to `queued/ready`. |
| `/` row control **Retry merge** on stalled #14 (`/?state=completed`) | ✅ `POST /api/tasks/14/retry-merge` → 200; `merge_failed/given_up` → `completed/on_merge_queue`. |
| `/inbox` "Look at a stuck task": **Run now** (#2), **Undefer** (#9), **Cancel** (#3) | ✅ Each offered once. Cancel asked "Cancel 3? The run in flight is stopped and the task is closed; …" and closed #3 (`closed/closed`). |
| `/inbox` **Retry merge** on #14 | ❌ → fixed. #14 is listed under "Unblock a merge" with only Open pull request (bug 2 above). After the fix it offers Retry merge at 1440 and 390. |

### 3.2 Force-merge a running or `awaiting_reply` task

| Step | Result |
| --- | --- |
| `/tasks/8` (running, `acme/widgets#104`) at 1440 and 390: Force merge in the action column | ✅ At 1440 the button sits at x=1152, w=260, in the right-hand column. At 390 it spans the column (x=17, w=357). |
| Confirmation text | ✅ "…This also stops the agent run in flight and cancels its CI jobs still running, and merges the reviewed commit. …" |
| Confirm on #8 (running) | ✅ `POST /api/tasks/8/force-merge` → 200. The task went from `running/live_run` to `completed/on_merge_queue`. The thread comment records the reason and what the queue was saying, and that "The task's own run was stopped … any grain CI still running on it was cancelled." `TestDemoForceMergeOnRunningTaskAndStackedBaseRetargeting` additionally checks the in-flight fake-CI run ends `cancelled` with reason "the merge of acme/widgets#104 was forced" and an empty `CancelledBy` (`ci.CancelBySystem`), and that `ForceMergeHeadSHA` is the reviewed head. |
| Confirm on #4 (stopped `awaiting_reply`, PR stubbed) | ✅ 200. The dialog leaves out the run-stop paragraph, correctly, since no run is live. The pending question is cleared and `CompletedAt` set. #4 has `autoMerge: false`, so its derived state reads `awaiting_submit/not_auto_merge`. The queue merges it anyway: `sync.go`'s `forcing` arm ignores `AutoMerge`. See the observations in §4. |
| Branch head moved since review | ✅ Refused, but with **HTTP 400**, not the 409 the plan names: "acme/widgets#131 has moved since you looked: you reviewed 0123456789ab and its head is now a1b2c3d4e5f6 -- review that and force again". `ForceMerge` reports every refusal as a validation error, and the demo test pins 400 (it pushes a commit to `grain/task-8` in the sim between review and force). The plan's 409 is wrong, not the server. |

### 3.3 Red base branch waiting repair in the Alerts bell and System → Health

#6 (`awaiting_submit`, `acme/widgets#102`, `autoMerge: false`) was marked with `PATCH /api/tasks/6 {"fixesBaseBranch": true}`, and `main` was made red with the `base_branch_ci` row; the task then reported `redBaseBranch: "main"`.

| Step | Result |
| --- | --- |
| No `RedBaseRepairBanner` strip above `.app-body` (light and dark, 1440 and 390) | ✅ `.app-body` starts at y=0 and no red-base element exists outside the panel. |
| Alerts bell lists the critical `red-base-repair/6` row | ✅ At both widths and in both themes. The count badge reads 2. The row reads "`main` is red — repair #6 / … is awaiting submit / Other work targeting main waits until the repair merges." |
| `Open #6`, `Submit`, `Force merge` buttons | ✅ All three are inside the viewport and the row box at both widths, e.g. phone `x=175/226/277`, row `x=88…350`. |
| `/system/health` at 1440 | ✅ Same row and buttons. |
| `/system/health` at 390 | ❌ → fixed. Loading the URL showed the System tab list (bug 1 above). Tapping Health did open it with all three buttons (`x=137/205/273`, inside 390). After the fix, a direct load opens Health. |

### 3.4 Stacked task `Base` retargeting and `nothing_needed`

Live, over HTTP and the sim's bare repo (`/tmp/grain-demo-*/githubsim/acme/widgets.git`):

1. Upstream task #15 (`base: main`); branch `grain/task-15` pushed with one commit.
2. Stacked task #16 (`base: grain/task-15`, `dependsOn: [15]`, `autoMerge: true`), made a review task.
3. `main` fast-forwarded to `grain/task-15`'s head, `grain/task-15` deleted, #15's merge recorded.
4. `POST /api/ci/runs {"taskId":"16",…}` → 202. `GET /api/tasks/16` then returned **`base: "main"`, `autoMerge: false`** ✅ (`60c591f48`, `0443e0274`).

`nothing_needed` leaving an open pull request alone (`53c74b9b6`) needs an agent run, which the demo cannot start. It is covered by `TestProcessResultNothingNeededLeavesAPullRequestInReviewOpen`, run here and passing, along with `TestGrainCIRetargetsStackedTaskBaseWhenUpstreamBranchMergesAndIsDeleted`, `TestGrainCIRetargetedReviewLosesAutoMerge`, `TestStackedTaskRetargetsToMergedUpstreamBaseBranch` and the demo test above.

## 4. Observations not changed here

- The `red-base-repair/<id>` alert title is written with markdown backticks ("`` `main` is red — repair #6 ``"). `AlertTitle` renders plain text, so the backticks show literally. The removed banner did the same, and `35bb3400d` specifies that title, so it was left alone.
- A force merge on a stopped `awaiting_reply` task that does not auto-merge shows `awaiting_submit / not auto merge` until the queue's next cycle merges it, which reads as if a Submit were still owed. Cosmetic: the queue does not wait for one.
- With no `-github-sim` branch for a seeded pull request, the demo's PR reader synthesizes a head (`%040x` of the PR number). That is why the force-merge comments on #8 and #4 say "at 000000000000".

## 5. Screenshots

`make ui-screenshots`-style shots (`ui/e2e/screenshot.mjs`, `-merge-rules` demo) of `/inbox` and `/system/health`, at desktop and phone in light and dark, were taken from `640e902ab` (`screenshots/before`) and from this branch (`screenshots/after`). Element crops of #14's inbox row and of phone `/system/health` are attached to the task's closing note. The layout checks are the bounding-box assertions in `ui/e2e/inbox-retry-merge.spec.js` and the measurements above.
