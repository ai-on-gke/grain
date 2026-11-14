# Task intake, Inbox and lifecycle walkthrough — 2026-11-13 (task 4726)

- **Date:** 2026-11-13, 02:39–03:30 UTC, one run.
- **Task:** task 4726, branch
  `grain/098073e4/task-4726-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `4ae6438f` ("Merge pull request #1435"). The fixes
  below were checked again on rebuilds of the same commit with them
  applied.
- **Focus:** this series has walked the classic UI (`/`) daily since
  2026-09-14. Since 2026-11-12 the Angular UI at `/next/` is the new
  experience (`PROMPT.md`, "Two UIs"), so this run walked the same operator
  path there first: the Inbox, the New task sheet, the Tasks list and the
  task page. It then re-walked the classic surfaces that `/next/` does not
  have yet: the `/new` form's Task priority, `/new?field.` links, list
  filters, manual mode and the selection bar. All four bugs found were in
  `/next/`.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0;
    Playwright 1.62.1 from `ui/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 unless a step says 390×844.
  - Built with `npm ci && npm run build` in `ui/` and `ui-ng/`, then
    `go build -o /var/tmp/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
-fake-ci -scheduled-workflows -fork-pull-request -git-proxy
-base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
-sensitive-hold -review-questions -human-review -secrets-store
-notifications -qualification -mechanical-port -config-change
-access-code-file <file>`. `TMPDIR` pointed at a fresh scratch directory
    on every start, so each store was a throwaway embedded SQLite file.
    The server was restarted on a fresh store twice, to load rebuilt
    frontends, so task ids from before and after a restart can name
    different tasks.
  - There was no `/home/debian/.gemini-api-key`, so no model was called
    and `LOOK=1` could not describe screenshots.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each one signs in through the Access code page (Your email
  `alice@example.com`, Access code) and then acts only through visible
  controls. Each page recorded uncaught page errors, console errors and
  warnings, failed requests, `/api/` responses of 400 or more (with
  bodies), every native dialog, and every non-GET `/api/` request with its
  body. The REST API (bearer access code) was used to read back what a
  gesture had persisted, and for the setup listed under Stubs.
- **Console baseline:** two kinds of console error appeared, and neither
  is a defect. One is the 401 for the signed-out page before sign-in. The
  other is a 404 from `GET /api/tasks/{id}/diff-preview` when the `/next/`
  Inbox inspector opens a task with no branch to compare (#5, #14, #15,
  #21, #26); see O5. The classic list's polls show up as `ERR_ABORTED`
  when a page navigates away mid-request. Nothing else was recorded.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout.

1. **Task-list read failures:** a Playwright route on `GET /api/tasks`
   answering `500 {"error":"store unavailable"}`. It was switched on for
   a cold load of `/next/inbox`, `/inbox` and `/next/tasks`, and,
   separately, mid-session on `/next/tasks`. Everything else passed
   through.
2. **Two PNG attachments** (40×30 and 30×40, made in the sandbox),
   attached through the New task sheet's file input.
3. **Three batch tasks** (`Walk 4726 batch A/B/C`, no repo) and the
   dependency e2e's two tasks were filed with `POST /api/tasks`, so the
   selection bar acted on tasks of this run's own.

Nothing else was stubbed. Role-gated rendering on `/next/` was covered by
`ui-ng/e2e/next-inbox.spec.js`'s viewer test (run below), not by a new
stub.

## Steps and outcomes

### 1. `/next/` Inbox

| #   | Step                    | Outcome                                                                                                                                                                                                                                                                                                                                                       | Verdict |
| --- | ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Sign in, groups, counts | Signed out, `/next/inbox` is the access-code page. Once signed in it reads "28 decisions waiting on you", which matches the rail's 28. Groups: Asked: approve a release 1, Asked: reply 4, Review needs your decision 1, Asked: submit for merge 10, Unblock a merge 1, Approve on GitHub 1, Look at a stuck task 4, Approve a proposal 3 and Alerts 3.       | PASS    |
| 2   | CL rows (before fix)    | All six changelist rows (#14–#19), #15 included, said "Submit is off: CI has not reported on this change yet." Their inspector's Submit was disabled, and so was the task page's (`/next/task/15`). #15 has presubmit passed and LGTM from alice, and the classic Inbox offers Submit on it (Bug 1).                                                          | FIXED   |
| 3   | CL rows (after fix)     | #15's Submit is enabled. #14 reads "Not submittable yet: review is not approved (changes requested); required presubmit has not passed on snapshot 3 (failed).", #17 gives its own reasons, and #19 reads "Not submittable yet: behind head: sync it." Submit on #15 → `POST /api/tasks/15/submit` → `closed`. The row leaves, and the headline goes 28 → 27. | PASS    |
| 4   | Stuck rows (before fix) | #22 (queued/ready), #23 (running/live run), #24 (deferred/wake time) and #5 (on the merge queue) offered no control at all, only Open full task and the composer. Their sub-cases name Run now, Cancel and Undefer, and the classic Inbox offers those (Bug 2).                                                                                               | FIXED   |
| 5   | Stuck rows (after fix)  | #22 Run now → `POST …/22/run-now` → the row leaves. #24 Undefer → `POST …/24/undefer` → `queued`, and the row leaves. #23 Cancel asks "Cancel #23? The run in flight is stopped and the task is closed; it can be reopened later." with Keep it running / Cancel. Accepting sends `POST …/23/close` → `closed`. #5 names no control and gets none.            | PASS    |
| 6   | Cancel dialog wording   | The first build of the fix gave that dialog two buttons both labelled "Cancel", one to dismiss and one to accept. Its dismiss button now reads "Keep it running".                                                                                                                                                                                             | FIXED   |
| 7   | Approve a proposal      | #12 → Approve → `POST …/12/approve` → `queued`, `approved: true`, and the row leaves.                                                                                                                                                                                                                                                                         | PASS    |
| 8   | Answer parked question  | #4's inspector shows the question and "Send to #4". A reply goes out as `POST …/4/comments`, #4 goes `awaiting_reply` → `queued`, and the row leaves. After a reload the headline still reads 22.                                                                                                                                                             | PASS    |
| 9   | Cold `/api/tasks` 500   | `/next/inbox` reads "4 decisions waiting on you" (the release and the alerts, which come from other endpoints) and "Could not read tasks; retrying." Classic `/inbox` reads "The task list could not be read, so this page cannot show what is waiting on you yet." Neither claims nothing is waiting.                                                        | PASS    |
| 10  | Layout                  | The new controls are the inspector's 40 px filled primary button, in light (`rgb(11,87,208)`) and dark (`rgb(168,199,250)`). They sit inside the viewport, with no horizontal overflow, at 1400 px and at 390 px, where the inspector is a full-screen sheet.                                                                                                 | PASS    |

### 2. `/next/` New task sheet

| #   | Step            | Outcome                                                                                                                                                                                                                                                                                                                          | Verdict |
| --- | --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Queued path     | Title, a description with a Mermaid block, two PNGs, Don't port, Fixes red base branch, reviewer `bob@example.com` and Merge on its own off → Create and start sends one `POST /api/tasks` carrying `noPort`, `fixesBaseBranch`, `reviewers` and `autoMerge:false`. Filed as #38 `queued`, `approved: true`, with 2 attachments. | PASS    |
| 2   | Proposed path   | With "Approve and start now" off, the button reads "Save as proposal" and files #39 `proposed`.                                                                                                                                                                                                                                  | PASS    |
| 3   | Cancel          | Cancel closes the sheet and files nothing.                                                                                                                                                                                                                                                                                       | PASS    |
| 4   | New-branch mode | With "Create a new branch" ticked, the base box becomes New branch name / Create it from, and Create says "Name the new branch, or untick the new-branch box."                                                                                                                                                                   | PASS    |
| 5   | Fork head label | Unticked, `someone:feature-x` gets no explanation and `a:b:c` is not refused. The classic form explains the first and refuses the second. The sheet has no Task priority, Owner, Keep running or External ref fields either. These are parity gaps rather than regressions, so they are filed as a follow-up (F1).               | GAP     |

### 3. `/next/` task page (#38)

| #   | Step                 | Outcome                                                                                                                                                                                                                                                                                                       | Verdict |
| --- | -------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Pause / Resume       | Pause → `POST …/pause {"immediate":false}`. The state stays `queued`, with `paused.fromState: "queued"`. After a reload Resume is offered, and it clears the pause. The timeline reads "Paused by alice@example.com (access code)." then "Resumed by … has now been resumed".                                 | PASS    |
| 2   | Close / Reopen       | Close → `POST …/close {"close_pull_request":false}` → `closed`, with no confirmation (it is reversible). Reopen → `queued`. The timeline reads "now Closed · alice@example.com (access code)" and "now reopened · alice@example.com (access code)".                                                           | PASS    |
| 3   | Defer / Bring back   | Defer → `POST …/defer {"deferredUntil":""}` → `deferred`. Bring back → `POST …/undefer` → `queued`.                                                                                                                                                                                                           | PASS    |
| 4   | Dependency           | Add `8` → `POST …/depends-on {"id":"8","attach":true}`. #38 becomes `blocked`, and the API's blocked count goes 1 → 2. × → `attach:false`, and it goes back. The chip read only "#8", with nothing on hover. The classic chip reads "Add pagination to the tasks API — Running (blocking this task)" (Bug 4). | FIXED   |
| 5   | Attachments, Mermaid | The two thumbnails are links that open the file in a new tab. There is no full-screen viewer, and the Mermaid block shows as highlighted source. The classic page has both (step 4.1). Filed as a follow-up (F2).                                                                                             | GAP     |

### 4. Classic surfaces

| #   | Step                     | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                      | Verdict |
| --- | ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Viewer and Mermaid (#38) | A thumbnail opens "1 / 2 · red.png · Download". `→` gives 2 / 2 and `←` gives 1 / 2. Download is `/api/tasks/38/attachments/1` with `download="red.png"`. `Escape`, × and the backdrop each close only the viewer, leaving the task pane. The Mermaid block renders, and Expand diagram → Zoom in reads 125%.                                                                                                                | PASS    |
| 2   | `/new` Task priority     | With Repos `none`: `0`, `-1` and `2.5` each show "a whole number, 1 or more; default 5, higher numbers run first", with Create task disabled. `1e3` enables it, and with Keep running ticked it files as `priority: 1000`, `keepRunning: true`.                                                                                                                                                                              | PASS    |
| 3   | Off-allowlist repo       | `someone/else` → "someone/else is not on this deployment's target repo list. the task will be filed but parked, and runs only once an operator adds the repo under settings and replies on it."                                                                                                                                                                                                                              | PASS    |
| 4   | `/new?field.` link       | `?field.priority=high&field.area=frontend&field.bogus=z&field.estimate=notanumber` fills priority high and area frontend. The notice reads "This link asked for values this form has left out: "bogus" is not a field on this deployment; Estimate (days): "notanumber" is not one of its values."                                                                                                                           | PASS    |
| 5   | List filters             | `admission:any:bogus` → "(unknown)", no rows. `admission:any:durable` → 1 row. `admission:any:bypass` → "bypass queue", 0 rows. `origin:any:imported` → 1 row. `origin:any:external` → "filed by an external agent", 0 rows. `origin:any:external-agent` → "(unknown)". `?repo=ACME/Widgets` → `f=repo:any:ACME/Widgets`, "acme/widgets", 30 rows. `owner:any:me` → "me (debian)". `?sort=admission` → `sort=admission:asc`. | PASS    |
| 6   | Back / Forward           | `?state=running` (2 rows) → `?state=blocked` (1 row), then Back and Forward each restore their URL and rows.                                                                                                                                                                                                                                                                                                                 | PASS    |
| 7   | Mid-session read failure | On `/next/tasks` the rows stay, and "The task list could not be read: store unavailable. This page tries again on its own." appears. It goes once reads recover.                                                                                                                                                                                                                                                             | PASS    |
| 8   | Manual mode              | System → Pipeline → Pause pipeline (with a reason) → "Paused — only tasks you Run now will start… Reason: walk 4726 manual mode". The queued rows #38, #2, #22, #28, #30 and #32 carry "waits for Run now". Run selected now on #2/#38 → two `POST …/run-now`. After a reload neither row carries the marker.                                                                                                                | PASS    |
| 9   | Bulk edit, Close         | With three tasks selected, Bulk edit → Task priority `0` is refused and Apply stays disabled. Cancel keeps "3 selected". `1e2` → three `PATCH {"priority":100}`. Batch Close on two asks "Close 2 task(s)?" (native confirm, 2026-11-11 O3 still open), and both close. The third stays `queued`.                                                                                                                            | PASS    |

### 5. Cold `/api/tasks` failure on `/next/tasks`

| #   | Step       | Outcome                                                                                                                                                                                   | Verdict |
| --- | ---------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Before fix | Every tab read 0, and the pane read "Nothing in this tab — switch tabs or create a task", with no error anywhere on the page. That says the list is empty when it was never read (Bug 3). | FIXED   |
| 2   | After fix  | `role="alert"`: "The task list could not be read: store unavailable. This page tries again on its own." The pane reads "No tasks to show until the list can be read".                     | PASS    |

## Bugs found and fixed

### Bug 1 — `/next/` never offered Submit on a changelist

**Reproduction:** on a fresh demo store, open `/next/inbox` and select
"Move the depot retry budget behind a flag" (#15, presubmit passed, LGTM
alice). The row and the inspector say "Submit is off: CI has not reported on
this change yet." and Submit is disabled. `/next/task/15` has it disabled
too. The classic Inbox offers Submit.

**Cause:** `ui-ng/src/app/shared/gates.ts`'s `submitBlock` decides only from
`mergeGates`. A changelist task has none. The change system answers in
`clSubmit` (`pkg/ui/change_submit.go`), which the classic Inbox reads.

**Fix:** for a task with `clSubmit` and no merge gates, `submitBlock` returns
"" when `canSubmit` is true, and "Not submittable yet: <reasons>." when it is
not. The `Task` model types `canSubmit`, `reasons` and `target`.

**Regression coverage:** `gates.spec.ts` "reads a changelist task's Submit
from clSubmit, not its absent gates"; `e2e/next-intake-walkthrough.spec.js`
"CL rows offer Submit as the change system says, with its reasons".

### Bug 2 — `/next/` stuck rows offered no control

**Reproduction:** open `/next/inbox` and select any row in Look at a stuck
task (#22, #23 or #24). The inspector offers Open full task and the composer
only. The classic Inbox offers Run now, Cancel and Undefer, the controls the
tasks' `reason.control` names.

**Cause:** `inbox-classifier.ts`'s stalled branch filled in the summary and
the pill, but pushed no action.

**Fix:** `stallControl` maps `reason.control` onto the request the task
page's button of the same name makes: Run now (`run-now`), Cancel (`close`,
asking first, destructive), Undefer, and Retry merge. A sub-case that names
none gets none. The confirmation can now carry a `dismiss` label, so
Cancel's dialog reads Keep it running / Cancel rather than Cancel / Cancel.

**Regression coverage:** `inbox-classifier.spec.ts` "offers a stuck task the
control its sub-case names, and none where it names none"; the e2e "a stuck
row offers its sub-case's control, and Cancel asks first".

### Bug 3 — a cold task-list failure read as an empty list on `/next/tasks`

**Reproduction:** with `GET /api/tasks` answering 500 (Stub 1), open
`/next/tasks`. Every tab reads 0, and the pane reads "Nothing in this tab —
switch tabs or create a task". No error is shown.

**Cause:** the shell's shared `TaskFeedStore` reads the list before the
Tasks page calls `TasksStore.start()`. The store's effect saw the error while
stopped and skipped it. Each later failure sets the same string, which does
not wake a signal effect, so the error was never reported. A failure that
starts mid-session was already reported.

**Fix:** `start()` reports a feed error that is already there. The empty
pane says the list cannot be read rather than that the tab is empty.

**Regression coverage:** `tasks.store.spec.ts` "says the task list could not
be read when it failed before the page started" fails on the base and passes
with the fix. Also the e2e "a cold task-list failure on /next/tasks says so,
not an empty list".

### Bug 4 — `/next/` dependency chips named only an id

**Reproduction:** on `/next/task/<id>`, add a dependency. The chip reads
"#8", with nothing on hover. The classic chip says the dependency's title,
its state and whether it is blocking.

**Fix:** the chip's `title` reads "#8 Add pagination to the tasks API —
Running (blocking this task)". The title comes from the open-task feed
where it is known, and the state from `dependsOnStates`.

**Regression coverage:** the e2e "a dependency chip names the task it waits
for and its state".

**Tests run:** `npx vitest run` over `src/app/inbox`, `src/app/shared`,
`src/app/task`, `src/app/tasks` and `src/app/services` (all pass), and
`npx tsc --noEmit -p tsconfig.app.json`. Also
`npx playwright test e2e/next-intake-walkthrough.spec.js
e2e/next-full-system.spec.js e2e/next-inbox.spec.js
e2e/next-task-interactions.spec.js` (18 passed, including the viewer and
axe tests).

## Follow-ups filed

- **F1 — New task sheet parity on `/next/`:** fork head labels
  (`<owner>:<branch>` explained, malformed ones refused), Task priority,
  Owner, Keep running and External ref.
- **F2 — task page media on `/next/`:** the full-screen attachment viewer
  (paging, `i / N`, Download, `Escape`/×/backdrop) and Mermaid rendering
  (zoom, fullscreen, invalid-syntax fallback).

## Observations (not treated as defects)

- **O1:** On `/next/` the stuck row with no named control (#5, on the merge
  queue) offers nothing but the conversation. That matches classic, but the
  row's long reason is its only guidance.
- **O2:** The batch Close confirmation still reads "Close 2 task(s)?"
  (2026-11-11 O3).
- **O3:** `/next/` task-page Close does not ask first, and the classic one
  does not either. Close is reversible.
- **O4:** `f=humanReview:any:required` is marked unknown; the attribute's
  values are not `required`. The marking is correct. I did not look up
  which values the attribute takes.
- **O5:** The `/next/` Inbox inspector requests `diff-preview` for every
  selected task, so tasks with no branch put a 404 in the console. The
  inspector handles it, but the request could be skipped for CL and
  branchless tasks.

## Not covered this run

Host-script approvals, pending secrets, suspended sessions, cross-team
approvals, `request_access` cards, config-repo change cards and the sync
group, the goal card, Force merge, Re-run CI, closed-task paging and
server-side closed search, templates and the template picker, the chat
pane's Pause now, Hosting and Admission on open tasks, and the
held-for-approval chip were not walked. Earlier reports in this series
cover them on the classic UI. Most need rows only a run or a reconciler
writes, and the `/next/` versions are the next place to look.

Screenshots of `/next/inbox`, `/next/tasks` and `/next/task/22` at desktop
and phone widths, in light and dark, were taken with `ui/e2e/screenshot.mjs`
on the fixed build. More were taken of the stuck-row inspector, the Cancel
dialog and the CL row. None were committed. With no Gemini key and a file
reader that shows PNGs as bytes, I could not look at them. The layout
evidence is the bounding-box check in step 1.10, plus
`next-full-system.spec.js` and the axe tests.
