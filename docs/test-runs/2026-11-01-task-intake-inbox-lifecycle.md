# Task intake, Inbox and lifecycle UI walkthrough — 2026-11-01

- **Date:** 2026-11-01, 04:07–05:40 UTC
- **Task:** grain/049f1e09/task-2233, base `main`
- **Commit tested:** `ea9bceb0` (`main` at the start of the run, "Merge pull
  request #338"). The fix below was then checked on the same tree with it
  applied (branch `grain/049f1e09/task-2233`).
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go `/usr/local/bin/go`, Node.js `v20.19.0`
  - Playwright from `ui/package-lock.json`, headless Chromium `chromium-1234`
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, 1400×900
  - Frontend `cd ui && npm ci && npm run build`; server `go build ./cmd/grain`
    after it, so the embedded `pkg/ui/static` was current.
  - `:8420`, the unmodified commit: `grain demo -addr 127.0.0.1:8420 -open=false
    -github-sim -fake-ci -scheduled-workflows -merge-rules -merge-queue-states
    -secrets-store -stalled-tasks -sensitive-hold -review-questions
    -access-code-file <file>`, `TMPDIR=/var/tmp/walk`, so the store was a
    throwaway embedded SQLite file. The fixed build was run through
    `npx playwright test` (its own per-worker demo server).
  - There was no `/home/debian/.gemini-api-key`, so no model was called.
    `sqlite3` was installed with apt for the stubs.
- **How it was driven:** throwaway Playwright scripts (not committed). They sign
  in with `POST /api/auth/access-code`, then act only through visible controls.
  Every page logged `pageerror`, console errors and warnings, responses of 400
  or more, failed requests, every dialog (its text and whether it was
  accepted), and every non-GET `/api/` request with its body. The REST API was
  used only to read back what a gesture had persisted, except where a stub
  below says otherwise.

## Stubs

The server, store, built frontend, browser and gestures were real throughout.
Each stub stands in for something that only an agent run, a reconciler or an
external system writes:

1. **Held-for-approval proposal** (an agent's `propose_task
   hold_for_approval`): sqlite `origin_behalf_kind='task', origin_behalf_id='3'`
   on #12, and a `task_tag` row `held-for-approval`.
2. **A merged pull request** (nothing in the demo merges): `pr_merged_at` and
   `closed_at` on #19's `task_observation`.
3. **Pending secret** (an agent's `request_secret`):
   `task_observation.pending_secret='stripe-api-key'` on #21.
4. **Suspended session** (a run that suspends): an inserted `task_observation`
   row for #2 with `suspended_session_*` set.
5. **A grain CI run and a failed verdict for #6**: `POST /api/ci/runs
   {"taskId":"6"}` (`fake-ci-1`), then sqlite `ci_conclusion='failure'`,
   `ci_finished_at` and `ci_detail`.
6. **A deferral whose wake time has passed** (probing the Undefer stall
   sub-case): `deferred_until='2026-10-28'` on #9. This was reverted afterwards.

## Steps and outcomes

### Inbox

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 1 | The rail's Inbox from `/`, then `/inbox` directly | Lands on `/inbox`. Groups: Notifications 3, Asked: reply 2, Review needs your decision 1, Asked: submit for merge 9, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 3, Approve a proposal 2. They sum to 22, the rail's count. | PASS |
| 2 | Click proposal #1's title, then Back | `/tasks/1`; Back returns to `/inbox`. | PASS |
| 3 | Stuck group: sub-cases | #21 queued >72h (`reconcileDispatch`) offers **Run now**; #22 running >24h15m (`ProcessResult`) offers **Cancel**; #5 on the merge queue >12h15m (`syncEntry`) offers only Open task (its sub-case names no control). | PASS |
| 4 | Run now on #21 | `POST …/21/run-now`. The row leaves and the group goes 3→2; #21 is `queued`, with `runNow` set. | PASS |
| 5 | Cancel on #22: dismiss, then accept | Confirm "Cancel 22? The run in flight is stopped and the task is closed; it can be reopened later." Dismissing sends nothing (#22 is still `running`). Accepting sends `POST …/22/close`; the group goes to 1 and #22 is `closed`. | PASS |
| 6 | Retry merge on #24 (Unblock a merge) | `POST …/24/retry-merge`; the group is gone and #24 is `completed`, its row oval reading "Merging". | PASS |
| 7 | Undefer sub-case (stub 6) | Unreachable. #9's `reason` reads `deferred/wake_time`, driven by `reconcileWake`, control Undefer, but `stall` is `null`, because `liveness.go` gives `deferred` `noPatience`. `InboxPage.jsx`'s comment promises "Undefer on a deferral whose wake time has passed". Filed as a follow-up (O1). | FAIL (follow-up) |
| 8 | Approve #1 from its row | `POST …/1/approve`; the proposal group goes 2→1; #1 is `queued` and approved. | PASS |
| 9 | Reply on #4: Send while empty, then type and Send | Send is disabled while empty. Then `POST …/4/comments`, and #4 leaves Asked: reply and is `queued`. The rail reads `17` ("14 tasks waiting on you, 3 notifications"). A reload shows the same groups. | PASS |
| 10 | Keyboard | Tab walks the rail, then Alerts, System, Settings, then each Inbox group toggle and its rows in order. | PASS |

### New task

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 11 | The rail's "Create options" | Menu: new task / new session / new board. New task opens `/new`. | PASS |
| 12 | Validation | Create task is disabled with an empty title, a whitespace title, and while Repos is `—`. | PASS |
| 13 | Model picker | `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`. That is exactly `/api/config`'s single agent backend `gemini-pro`, models `gemini-2.5-pro` and `gemini-2.5-flash`. | PASS |
| 14 | A refused field inside Advanced options | Advanced closed, vCPUs `-3`, Create task: no POST is sent, and Advanced options opens (`aria-expanded` false→true). | PASS |
| 15 | Cancel: ✕, reopen, Discard draft | The title is kept as a draft; Discard draft empties it. | PASS |
| 16 | `/new?field.priority=high&field.requested-by=Link%20Sender` → queued task with everything | Prefilled Priority high and Requested by "Link Sender". Added a description, an attachment, acme/widgets, base `release/1.0` and auto-merge. Unticked Self debug; GCP key and Gemini key, both "not ready -- needs gcp-key-minter", stayed ticked. Added Grain API preset ci-read from Add grant (the menu lists task-admin, ci-read, dispatch-control, deployment-ops *dangerous*, …). Advanced: network "egress", 4 vCPUs, 8192 MiB, model gemini-2.5-flash, Task priority 7. Filed #29 `queued`. Read back: `api:ci-read, gcp-key, gemini-key, grain-ci`, model, `networkAccess: open`, cpus, memory, priority 7, custom fields and `attach.txt` all persisted. The row shows "p7", "api preset: ci-read" and `release/1.0`. | PASS |
| 17 | Proposed path, no repo, empty Task priority | Unticking "queue immediately" files #30 `proposed`, `noRepo`, priority 5 (an empty box falls back to the default). Base branch still shows with No repo (O5 of 10-29, unchanged). | PASS |
| 18 | `/new?field.priority=bogus&field.nosuch=1` | The 10-30 notice names both dropped values. | PASS |
| 19 | Task priority `-4`, `0`, `2.7` | The browser's own validity check (`min=1 step=1`) blocks the submit; nothing is sent. | PASS |
| 20 | Task priority `1e3` | **Bug 1:** the box accepts it as valid (it is 1000), and the task is filed with `priority: 1`. Fixed here. | FAIL → PASS |

### Task list

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 21 | Rail views | Tasks `/?f=state:none:deferred\|closed` (25 rows); Proposed 12, 30; Merging 5, 20, 24; Blocked 11; Deferred 9; Closed 22, 10; Merged (stub 2) `/?state=merged&sort=merged:desc` 19, and the same from a deep link. | PASS |
| 22 | Ovals and chips | #11 `Blocked on: #8 (running)`; #5 and #24 "Merging", #5 also stalled with its note as the title; #3 "Running", chip title "running/live_run Driven by ProcessResult … "Cancel" ends this wait"; #12 (stub 1) "held for approval by 3", hover "task 3 could have queued this on its own permission…". The chip is gone after Approve (step 31). | PASS |
| 23 | Search `retry`, sort by title, reload | `?q=retry` → 2, 14–18; `sort=title:asc` reorders them; a reload keeps the URL, the rows and the search text. | PASS |
| 24 | Open #2, Back, Forward | Back returns to the narrowed URL with the same rows; Forward returns to `/tasks/2`. | PASS |
| 25 | Clear, then deep link `/?state=running` | Clear drops the search back to the default state filter and keeps the sort. The deep link lists 3, 8. | PASS |

### Task detail

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 26 | Pause → reload → Resume → Pause now → Resume on running #3 | `…/pause`, `…/resume`, `…/pause {"immediate":true}`, `…/resume`. The pane follows without a reload; Resume is still offered after a reload; #3 stays `running` and the backlog order is unchanged. | PASS |
| 27 | Pause → Resume on queued #29 | Stays `queued` in the same place. | PASS |
| 28 | Force merge on #8 (attempt #2 running) | A `prompt` explains the override and that the run in flight is stopped. Dismissing sends nothing. Accepting sends `{"reason":"walkthrough: CI is stuck"}`; without a reload the pane says "…force the merge of acme/widgets#104 at 000000000000…", and #8 is `completed`. | PASS |
| 29 | Attempt capability audit, work tree on #8 | "capability audit records · the run made 14 tool call(s)"; Escape stays on `/tasks/8`. Provenance "work from task 13: 0 of 4 done", expanded: generated from 13, then 13, 1, 3, 8 (acme/widgets#104), 11, each with its state and a link. | PASS |
| 30 | Defer (no date) → Undefer → Defer until 2026-11-15 → reload → Approve → Undefer, on #30 | `defer {"deferredUntil":""}` → `deferred`; Undefer → `proposed`; the dated defer stores `2026-11-15T00:00:00Z`, and after a reload the pane says "Picks itself back up on 11/15/2026, 12:00:00 AM, back into the proposals…". Approve keeps it `deferred` and approved; Undefer → `queued`. | PASS |
| 31 | Approve held proposal #12 | `queued`; the held chip is gone from its row. | PASS |
| 32 | Dependency #29 → #3, then remove | Typing "bump" plus Enter sends `depends-on {"id":"3","attach":true}`. Chip "3 bump the go toolchain to 1.24 (open)"; hover "3 Bump the Go toolchain to 1.24 — Running (blocking this task)". The rail's Blocked goes 1→2, the Blocked view lists 29 and 11, and #29's oval reads "Blocked on: #3 (running)". × sends `attach:false`, and Blocked goes back to 1. | PASS |
| 33 | Merge policy on #23 | `ci.mode: grain -- from repository setting`; then `ci.mode: both`, `auto_merge: never`, `stall_timeout: 30m`, `ci.grain: full suite`, `ci.github: build`, `ci.github: cla/google`, each from rule `deploy`; then "GitHub CI is narrowed to build, cla/google…". | PASS |
| 34 | Grain CI verdict on #6 (stub 5), then Re-run CI | "grain CI: failure. 1 job failed: go test…", with show the run, Re-run CI and Re-run failed jobs. Re-run CI sends `POST /api/ci/runs` at once, and the pane goes back to "grain CI is running…" without a reload. | PASS |
| 35 | Pending secret on #21 (stub 3) | A password box "Value for stripe-api-key"; Set secret is disabled while empty; then `PUT …/21/secret`. The timeline says "Set stripe-api-key. Its value is in grain's secret store, not in this conversation…". The value is in neither the DOM (before or after a reload) nor the task JSON, and `grep -rlF` finds it in no file under the demo directory. | PASS |
| 36 | Suspended session on #2 (stub 4) | "Left by demo-run-2-1 on 11/1/2026, 2:00:00 AM (Antigravity). Any further run of this task resumes this conversation…" | PASS |
| 37 | Parked question #7 | `.pending-question` "Waiting for your reply", the question, a reply box and Send. | PASS |
| 38 | Full prompt and live transcript on #3 | "prompt for 3 · attempt #1 · 10 kb · ~2,615 tokens · 1% of a 200,000-token window"; Escape stays on `/tasks/3`. "live attempt #1 transcript" inline. | PASS |
| 39 | Edit #30: title and description (Save), Repo `—`→acme/widgets, base `release/1.0`, auto-merge off, Edit fields (Requested by); reload | Each sends its own PATCH and is persisted after the reload. Enter followed by blur in Base branch sends the same `{"base":"release/1.0"}` PATCH twice (harmless, O2). | PASS |
| 40 | Withdraw approval | Offered on queued #2 and #29 (the cross-team approval section the brief names does not exist in this tree; 10-30's step 47 still holds). | N/A |
| 41 | Chat pane Pause/Resume, host-script approvals, Internal PR link | Not re-run this time for lack of time. They passed on 10-30 (steps 30, 43, 46 there), and nothing under those components has changed since. | NOT RUN |

### Selection, bulk edit, manual mode

| # | Step | Outcome | Verdict |
|---|------|---------|---------|
| 42 | Queued view, tick #2, #21, #29 | "3 selected" with approve / run selected now / submit / defer / undefer / close / reopen. Close confirms "Close 3 task(s)?"; dismissing sends nothing and keeps the selection. | PASS |
| 43 | Bulk edit… → Cancel | Fields that differ say "(mixed across the selection)". Cancel sends nothing and keeps the selection. | PASS |
| 44 | Bulk edit: tick Requested by, type "Bulk walker", Apply to 3 tasks; reload | Three PATCHes with `customFields` only, merged into each task's own (#2 keeps its priority and area, #29 its priority). Base, auto-merge and capability counts are unchanged; the selection is cleared. | PASS |
| 45 | System → Pipeline → Pause pipeline | Confirm, then `POST /api/pipeline-pause`. Queued 29, 1, 2, 4, 12, 26, 28, 30 carry "waits for Run now"; blocked #11 and #21 (Run now pressed in step 4) do not. | PASS |
| 46 | Select #29 and #1 → Run selected now; reload after 5s | `…/run-now` for each; their markers are gone and stay gone after the reload (the others keep theirs). Resume pipeline confirms, then `DELETE /api/pipeline-pause`, and every marker goes. | PASS |

### Console and network

There were no uncaught page errors, no console errors or warnings, and no
failed requests. Nothing returned 400 or more.

## Bug 1: New task filed a number box's exponent entry as its leading digit (fixed here)

- **Reproduction:** on any `grain demo`, open `/new`, give it a title and a
  repo, and type `1e3` into Task priority. The box reports itself valid,
  because `1e3` is 1000. Create task sends `"priority":1`, and the task is
  filed at priority 1, the bottom of the backlog, instead of the top. Advanced
  options' Memory (MiB) `8e3` sends `8`, and so do vCPUs and disk.
- **Cause:** `NewTaskOverlay.jsx` and `EditTaskForm.jsx` read those boxes with
  `parseInt(value, 10)`, which stops at the `e`.
- **Fix:** `ui/src/wholeNumber.js` reads the whole string as the input does
  (`Number`), and answers NaN for anything that is not a whole number. Every
  caller already treats NaN as "not set", so empty and invalid entries behave
  as before.
- **Coverage:** `ui/src/wholeNumber.test.js`; `NewTaskOverlay.test.jsx` ›
  "files a Task priority typed in exponent notation as the number it is";
  `ui/e2e/new-task-number-boxes.spec.js`, which files `1e3` and `8e3` through
  the real demo, checks the request body and reads back priority 1000, then
  closes the task. That spec and `new-task-link-values.spec.js` pass on the
  fixed build. The four affected vitest files (47 tests) pass. Nothing about
  the layout changed, so no screenshots were taken.

## Observations

- **O1 (follow-up filed):** the Inbox's stuck group can never offer Undefer.
  A deferral whose wake time `reconcileWake` has missed (stub 6: four days
  past) has `reason.class=driven` but no stall note, because `deferred` has
  `noPatience`. The Inbox and `livenessreasons.go` both describe this
  sub-case, so the rule and the UI disagree.
- **O2:** Base branch on the task pane sends its PATCH on Enter and again on
  blur. The second one is identical and harmless.
- **O5 (10-29)**, unchanged: New task still shows Base branch with No repo
  selected.
- Notes 10-30 recorded and still true: Pause now asks nothing (O4 of 10-29).
  While a task pane is open the rail is `aria-hidden` (the pane is modal), so
  rail counts read from a task page have to be read through the DOM.
