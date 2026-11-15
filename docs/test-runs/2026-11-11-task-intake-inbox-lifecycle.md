# Task intake, Inbox and lifecycle walkthrough — 2026-11-11 (task 4298)

- **Date:** 2026-11-11, 07:00–07:50 UTC, one run.
- **Task:** task 4298, branch
  `grain/098073e4/task-4298-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `84f79bfd` ("Merge pull request #1180"), binary
  `v0.0.0-20261111064749-84f79bfda11b`. The fixes below were checked again on
  a rebuild of the same commit with them applied.
- **Focus:** the earlier reports in this series (most recently
  `2026-11-10-task-intake-inbox-lifecycle.md`) cover this area in depth. This
  run walked the operator path again on the current `main`. It spent its
  extra time on what changed since `61668e58`: comments left on a closed task
  and delivered on reopen (`f15e32e6`), the debounced stale task-list notice
  (`af7858e7`), and lazily loaded task threads (`bec18394`). It also covered
  role-gated rendering, which the 2026-11-10 run skipped.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0; Playwright
    1.62.1 from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 unless a step says 390×844.
  - Built with `cd ui && npm ci && npm run build`, then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -scheduled-workflows -fork-pull-request -git-proxy
-base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
-sensitive-hold -review-questions -human-review -secrets-store
-notifications -qualification -mechanical-port -config-change
-access-code-file <file>`. `TMPDIR` was set to a fresh scratch directory
    on every start, so the store was a throwaway embedded SQLite file. The
    server was restarted on a fresh store once, to load the rebuilt frontend.
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    `LOOK=1` could not describe screenshots.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each one signs in through the Access code page (Your email
  `alice@example.com`, the demo's allowlisted person) and then acts only
  through visible controls. Each page recorded uncaught page errors, console
  errors and warnings, failed requests, `/api/` responses of 400 or more
  (with bodies), every native dialog (its text, and whether it was
  accepted), and every non-GET `/api/` request with its body. The REST API
  (bearer access code) was used to read back what a gesture had persisted,
  and for the setup listed under Stubs.
- **Console baseline:** apart from the stubbed 500s in step 3.5, no
  uncaught page error, console error or failed request was recorded. Signing
  in as `operator@example.com` is refused with a 403 naming the allowlist,
  which is correct: that address is not on the demo's allowlist.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout.

1. **A template with a priority** (`Urgent fix` at 8):
   `POST /api/templates`. The demo seeds no template with a priority.
2. **Task-list read failures:** a Playwright route on `GET /api/tasks`
   answering `500 {"error":"store unavailable"}`, switched on for a cold
   load and, separately, mid-session. Everything else passed through.
3. **A grain/viewer role:** a Playwright route rewriting `GET /api/config`'s
   `access` to `grain/viewer` scopes (the same stub
   `ui/e2e/role-gated-actions.spec.js` uses). An access-code session always
   has the code's power, so a real read-only principal needs an identity
   verifier this sandbox does not have. This stub tests only what the UI
   offers. The server's refusals are covered by `pkg/ui`'s route-scope
   tests.
4. **Two PNG attachments** (40×30 and 30×40, generated in the sandbox),
   attached through the comment box's file input.

Nothing else was stubbed. As on 2026-11-10, the following were not
re-walked: host-script approvals, pending secrets, suspended sessions,
cross-team approvals, `request_access` cards, a grain CI verdict with Re-run
CI, the config-repo sync group and Force merge. They need rows that only a
run or a reconciler writes (see Not covered).

## Steps and outcomes

### 1. Inbox

| #   | Step                         | Outcome                                                                                                                                                                                                                                                                          | Verdict |
| --- | ---------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Open from the rail           | The rail's Inbox item (`role=button`) goes to `/inbox`.                                                                                                                                                                                                                          | PASS    |
| 2   | Groups and counts            | Notifications 3, Asked: approve a release 1, Asked: reply 4, Review needs your decision 1, Asked: submit for merge 10, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4 and Approve a proposal 3. These add up to the rail's 28.                                   | PASS    |
| 3   | Collapse by keyboard, reload | Notifications starts collapsed with its 3 shown. `Enter` on its toggle expands it, and `Space` on Approve on GitHub collapses that one. Both stay as left after a reload.                                                                                                        | PASS    |
| 4   | Defer all, dismissed         | "Defer all 3 proposals? They stop waiting to be approved and drop out of the lists until you ask for them." Dismissing it sends nothing.                                                                                                                                         | PASS    |
| 5   | Stalled Run now              | #22 "ready" → Run now → `POST …/22/run-now`. The group goes 4 → 3.                                                                                                                                                                                                               | PASS    |
| 6   | Approve a proposal           | Approve on #1 → `queued`, `approved: true`. The group goes 3 → 2, and the rail goes 28 → 26.                                                                                                                                                                                     | PASS    |
| 7   | Answer the parked question   | Show question on #4 expands the question and the run's reasoning. Reply → text → Send → `POST /api/tasks/4/comments`, and #4 goes `awaiting_reply` → `queued`. `Ctrl+Enter` still does not send (2026-11-10 O4).                                                                 | PASS    |
| 8   | Review decision row (#29)    | It offers Reply and "Merge without the code review", and no Ship as is: reviews are their own gate now. The seeded comment still said "ship it as it stands with **Ship as is** (Submit)" (Bug 1).                                                                               | FIXED   |
| 9   | CL rows                      | #15 (presubmit passed, lgtm) offers Submit inline. #14/#16/#17/#18/#19 offer none, each saying why ("Not submittable yet: …", "behind head: sync it"). Submit on #15 → `POST …/15/submit` → `closed`, and it leaves the group.                                                   | PASS    |
| 10  | Mail / Sync on the task page | #17 Mail → `POST …/17/change-mail`. The review then reads `canMail: false, mailReason: "already mailed"`. #19 Sync → `POST …/19/change-sync`. Publish replies is disabled with "no replies are waiting". Mail and Publish replies are on the task page, not inline in the Inbox. | PASS    |
| 11  | Cold `/api/tasks` failure    | The Inbox says "The task list could not be read, so this page cannot show what is waiting on you yet. It tries again on its own." It never claims nothing is waiting, and its notification and release groups still render.                                                      | PASS    |

### 2. New task

| #   | Step                    | Outcome                                                                                                                                                                                                                                                                                                                                                                                                | Verdict |
| --- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------- |
| 1   | Task priority           | `0`, `-2` and `2.5` are marked invalid with "a whole number, 1 or more; default 5, higher numbers run first". `1e3` and `7` are accepted. Create task files `1e3` as `priority: 1000`.                                                                                                                                                                                                                 | PASS    |
| 2   | Fork head labels        | With Create a new branch unticked, `alice:feature-x` → "GitHub fork head: branch feature-x on alice's fork. …". `alice:` and `:x` → "… needs both an owner and a branch, as <owner>:<branch>". `alice/x:y` → "… "alice/x" is not a GitHub login". Each malformed label holds Create task disabled. `bad owner:x` is not caught in the browser (2026-11-10 O2).                                         | PASS    |
| 3   | Create disabled         | Create task stays disabled until a repo is picked (or No repo chosen), and nothing on the form says so (O1).                                                                                                                                                                                                                                                                                           | OBS     |
| 4   | Queued path             | Title, repo acme/widgets, priority `1e3` → #38 `queued`, `approved: true`, priority 1000.                                                                                                                                                                                                                                                                                                              | PASS    |
| 5   | Proposed path and boxes | Queue immediately unticked, auto-merge unticked, Don't port, Keep running and This fixes a red main ticked → #39 `proposed` with `autoMerge: false, noPort: true, keepRunning: true, fixesBaseBranch: true`.                                                                                                                                                                                           | PASS    |
| 6   | Off-allowlist repo      | `someone/else` → "someone/else is not on this deployment's target repo list. the task will be filed but parked, and runs only once an operator adds the repo under settings and replies on it." Filed as #40 `awaiting_reply`.                                                                                                                                                                         | PASS    |
| 7   | `/new?field.` link      | `?field.priority=high&field.area=frontend&field.area=docs&field.bogus=z&field.estimate=notanumber&field.priority2=x` fills Priority high and the Area chips frontend and docs. The notice reads "This link asked for values this form has left out: "bogus" is not a field on this deployment; Estimate (days): "notanumber" is not one of its values; "priority2" is not a field on this deployment." | PASS    |

### 3. Tasks list

| #   | Step                      | Outcome                                                                                                                                                                                                                                                                                                                      | Verdict |
| --- | ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Closed-attribute unknowns | `?f=admission:any:bogus` → "admission is any of "bogus" (unknown)" with no rows. `?f=origin:any:external-agent` is marked unknown the same way (the value is `external`). `?state=bogus` → "No tasks in this state."                                                                                                         | PASS    |
| 2   | Repo case                 | `?repo=ACME/Widgets` rewrites to `?f=repo:any:ACME/Widgets`, reads "repo is any of acme/widgets" and matches every acme/widgets row.                                                                                                                                                                                         | PASS    |
| 3   | Admission / Origin        | `f=admission:any:durable` → 1 row. `f=origin:any:imported` → #12 only.                                                                                                                                                                                                                                                       | PASS    |
| 4   | Back / Forward            | Running → Blocked from the rail, then Back → `/?state=running&f=none` (3 rows), and Forward → `/?state=blocked&f=state:none:deferred\|closed` (1 row).                                                                                                                                                                       | PASS    |
| 5   | Stale list mid-session    | With `GET /api/tasks` failing, "not refreshing" appears after 3 failed polls (about 7 s), and the rows stay. When the reads recover the notice goes. There is no Retry button any more: the list retries on its own.                                                                                                         | PASS    |
| 6   | `owner:any:me`            | Reads "owner is any of me (debian)". An access-code session acts with the deployment's power, so "me" and a browser-filed task's owner are both the deployment owner (`debian`), not the email typed at sign-in, though the task's author is `access-code:alice@example.com`. This matches `request_actor.go`'s design (O2). | OBS     |

### 4. Task page

| #   | Step                                | Outcome                                                                                                                                                                                                                                                                                                                                                                                              | Verdict |
| --- | ----------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Pause / Resume                      | On #38 and #2, Pause → `paused.fromState: "queued"` with the state unchanged. After a reload the page offers Resume, and Resume clears the pause.                                                                                                                                                                                                                                                    | PASS    |
| 2   | Admission                           | Durable → `PATCH {"admission":"durable"}`, then back to Standard. The timeline reads "from Standard to Durable" / "from Durable to Standard", by alice@example.com (access code).                                                                                                                                                                                                                    | PASS    |
| 3   | Close, comment while closed, reopen | Close → `closed`. The box says "This task is closed; a comment won't reach an agent." and offers Reopen and send / Comment only. Comment only → `POST …/comments`, and the task stays closed. Reopen → `queued`. The timeline shows Closed and Reopened, each with its principal (an API reopen reads "by operator (access code)"), with the comment between them. No horizontal overflow at 390 px. | PASS    |
| 4   | Edit                                | Title, External ref `JIRA-4298` and Task priority 12 → one PATCH. The `JIRA-4298` link opens `/?f=externalRef:any:JIRA-4298`, which lists #38 only.                                                                                                                                                                                                                                                  | PASS    |
| 5   | Dependencies                        | Add a dependency… → `8` → "Add pagination to the tasks API" → `{"id":"8","attach":true}`. The chip's label reads "8 Add pagination to the tasks API — Running (blocking this task)", and the rail's Blocked goes 1 → 2. `Delete` on the chip detaches it, and Blocked goes back to 1.                                                                                                                | PASS    |
| 6   | Attachments viewer                  | Two PNGs on a comment show two thumbnails. The viewer reads "1 / 2 · red.png · Download", with Close / Previous / Next buttons. `→` gives 2 / 2 and `←` gives 1 / 2. Download is `/api/tasks/2/attachments/1` with `download="red.png"`. `Escape` closes the viewer only, and the task page stays.                                                                                                   | PASS    |
| 7   | Mermaid                             | A valid `graph TD` renders an SVG with Diagram / Source toggles. Expand diagram opens a dialog with Zoom out / Reset zoom / Zoom in, and Zoom in reads 125%. `Escape` closes it, leaving the task page. An invalid block shows "Diagram could not be rendered (Parse error on line 1 …)".                                                                                                            | PASS    |

### 5. Selection bar and manual mode

| #   | Step                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                  | Verdict |
| --- | ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Bulk edit priority       | With #38/#39/#40 selected, Bulk edit… → tick Task priority: `0` and `2.5` are refused in the shared wording, with Apply disabled. Cancel keeps the selection. `1e2` → Apply to 3 tasks → three `PATCH {"priority":100}`. The selection is cleared after applying (2026-11-10 O3).                                                                                                        | PASS    |
| 2   | Defer / Undefer          | Defer on #38/#39 → both `deferred`. Undefer from the Deferred view → `queued` / `proposed`.                                                                                                                                                                                                                                                                                              | PASS    |
| 3   | Approve / Close / Reopen | Approve #39 → `queued`. Close #38/#39 asks "Close 2 task(s)?" (O3), then both are `closed`. Reopen from the Closed view → both `queued`.                                                                                                                                                                                                                                                 | PASS    |
| 4   | Manual mode (Pause)      | System → Pipeline → Pause pipeline (with a reason) asks first, then reads "Paused — only tasks you Run now will start." Queued rows #38, #39, #1, #4, #28, #30 and #32 carry "paused — waits for Run now". #22, which was already started in step 1.5, does not. Run selected now on #38/#39 → two `POST …/run-now`, and after a reload the marker is gone from both. Resume asks first. | PASS    |

### 6. Role-gated rendering (grain/viewer stub)

| #   | Step                                  | Outcome                                                                                                                                                                                                                                                                 | Verdict |
| --- | ------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Inbox                                 | Every answer is disabled with its reason: Approve, Reject, Reply, Acknowledge, Submit, Retry merge, Run now, Cancel, Undefer, the bulk actions and New task. Only the section and show-question toggles stay live.                                                      | PASS    |
| 2   | Task list, task page (before the fix) | The stalled row's Cancel on the list, the task page's Comment, and Edit fields were all enabled for the viewer. Each is a write the server refuses with a 403 (`tasks:write` for close/comments/PATCH, `dispatch:write` for run-now) (Bug 2).                           | FIXED   |
| 3   | After the fix                         | All three are disabled with the reason, as in the Inbox. For the operator they still work: Run now on the list sends `POST …/22/run-now`, Comment posts, and Edit fields opens the editor. At 1400 px the boxes are the same for operator and viewer in light and dark. | PASS    |
| 4   | Phone row layout                      | At 390 px the stalled row's control sat on the title's line, 112–124 px above the sub-case chip it belongs to, for operator and viewer alike (Bug 3).                                                                                                                   | FIXED   |

## Bugs found and fixed

### Bug 1 — the demo's review decision named a button that no longer exists

**Reproduction:** on a fresh demo store, open `/inbox` and go to Review
needs your decision. #29's text ends "Or ship it as it stands with **Ship as
is** (Submit), leaving these open." The row offers Reply and "Merge without
the code review", and no Ship as is.

**Cause:** reviews became a separate merge gate (`63cdbc93`). The comment
that `pkg/orchestrator/reviewfeedback.go` posts changed then, but the demo's
seeded copy (`pkg/demo/demo_review_questions.go`) did not.

**Fix:** the seeded comment now uses the production wording: Submit records
approval only, and merging over open findings needs a force-merge override
with a reason.

**Regression coverage:** none beyond `go test ./pkg/demo/` (passes). This is
a seeded string, and no test pins it.

### Bug 2 — a viewer was offered three task writes

**Reproduction:** sign in, and stub `/api/config`'s `access` to
`grain/viewer` (Stub 3). On `/`, #23's stalled row offers an enabled
Cancel. On `/tasks/2`, Comment and Edit fields are enabled. Pressing any of
them ends in a 403 (`POST …/close`, `POST …/comments`, `PATCH /api/tasks/2`
all need `tasks:write`).

**Cause:** the Inbox's stalled group gates the same controls
(`inboxActionScope`), but `TaskRow` rendered its own copy bare. The
timeline's composer gated only Reopen and send. Its comment said a role that
may not reopen still gets "the plain post", but the server needs the same
scope for both. `CustomFieldsSection`'s Edit fields was never gated.

**Fix:** all three sit in `AccessGate`, disabled with the reason. The
row-action scope table moves to `state.js` as `actionScope`, so the list and
the Inbox read the same one. `InboxPage` still exports `inboxActionScope`.

**Regression coverage:** `TaskList.test.jsx` "disables a stalled row's
control for a role without its scope", `CustomFieldsSection.test.jsx`
"disables Edit fields for a role without tasks:write", and the existing
`Timeline.test.jsx` viewer case, which now also asserts Comment only. All
three fail on the base and pass with the fix. The full `vitest` suite
passes: 265 files, 4159 tests.

### Bug 3 — on a phone the stalled control left its chip behind

**Reproduction:** at 390×844, open `/`. On #23 ("live run") the Cancel
button sits at the end of the title's line, and the "live run" chip it
belongs to is at the bottom of the row. Their vertical centres are 124 px
apart for the operator, and 98 px apart in the viewer's shorter row.

**Cause:** a phone task row wraps and sorts its items with `order`. The
loose chips get `order: 4` (`.task-row > .MuiChip-root`), but the control is
a button, so it kept order 0 and sorted ahead of the badge, onto the title's
line.

**Fix:** `.task-row > .task-reason-control` and `.task-row > .access-gate`
take `order: 4` on a phone, so the control follows its chip whether bare or
gated.

**Regression coverage:** `e2e/task-reason.spec.js` "a stuck row's control
stays beside its chip on a phone, and is gated for a viewer". It checks the
bounding boxes for operator and viewer and fails on the base CSS (112 px
off). The whole of `task-reason.spec.js` and `role-gated-actions.spec.js`
passes (18 tests).

## Observations (not treated as defects)

- **O1:** Create task stays disabled with no repo picked, and nothing on the
  form says why. The repo field is the first thing a new user is likely to
  miss.
- **O2:** For an access-code session, `owner:any:me` and a browser-filed
  task's owner both resolve to the deployment owner, not the email given at
  sign-in. This is how `request_actor.go` is written ("still the code's
  power"), but the New task form's Owner hint says "defaults to the caller".
- **O3:** The batch Close confirmation reads "Close 2 task(s)?", and its
  error toast "… task(s) failed". The Inbox's confirmations pluralize
  properly.
- **O4:** A `/new?field.` link with a non-number for a number field is
  reported as `"notanumber" is not one of its values`. That wording fits an
  enum, and a unit test pins it.
- **O5:** `pkg/orchestrator/reviewquestions.go`'s doc comment still calls
  Submit "ship as is". That is a code comment only, and this run left it.

## Not covered this run

Host-script approval (stdin preview, approve and deny), the pending-secret
box, a suspended session, cross-team approvals, `request_access` cards, a
grain CI verdict with Re-run CI, Force merge, the held-for-approval chip, the
config-repo sync-failure group, the goal card's `on_limit`, closed-task
scroll paging and the chat pane. The first group needs rows only an agent
run or a reconciler writes. The 2026-11-08 and 2026-11-09 reports covered
them with sqlite stubs on builds a few days older.

Screenshots of `/`, `/tasks/23` and `/tasks/2` at desktop and phone widths,
in light and dark, were taken with `ui/e2e/screenshot.mjs` on the fixed
build. I could not look at them: there was no Gemini key, and the file
reader does not render PNGs. None were committed. The layout evidence is the
bounding-box comparison in step 6.3 and the new e2e assertion.
