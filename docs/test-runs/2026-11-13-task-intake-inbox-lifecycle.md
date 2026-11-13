# Task intake, Inbox and lifecycle walkthrough — 2026-11-13 (task 5384)

- **Date:** 2026-11-13, 14:03–15:00 UTC, one run.
- **Task:** task 5384, branch
  `grain/098073e4/task-5384-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `e69d34223` ("Merge pull request #1478"), binary
  `v0.0.0-20261113135704-e69d34223665`. Each fix below was checked again on a
  rebuild of that commit with the fix applied (`/var/tmp/grain2`, rebuilt
  three times during the run).
- **UI walked:** the new Angular UI at `/next/` first, as `PROMPT.md` ("Two
  UIs") asks. No classic page was changed, and the classic walk was limited
  to reading `ui/src` for the behaviour a `/next/` page should match. The
  previous report in this series (`2026-11-11-task-intake-inbox-lifecycle.md`)
  walked the classic pages in depth.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0;
    Playwright 1.62.1 (from `ui-ng/package-lock.json`), headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 unless a step says 390×844.
  - Built with `make frontend` (classic `ui/`, `ui-ng/`, `ui-hub/`) and
    `go build -o /var/tmp/grain ./cmd/grain`. After each fix:
    `cd ui-ng && npm run build`, then `go build` again.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -github-sim
    -fake-ci -scheduled-workflows -fork-pull-request -git-proxy
    -base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
    -sensitive-hold -review-questions -human-review -secrets-store
    -notifications -qualification -mechanical-port -config-change
    -access-code-file <file>`. `TMPDIR` pointed at a new scratch directory
    on every start, so the store was always a throwaway embedded SQLite
    file. The server was restarted on a fresh store five times: after each
    rebuild, and to reset seeded state between scripts.
  - There was no `/home/debian/.gemini-api-key`. No model was called, and
    `LOOK=1` could not describe screenshots.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout (`/var/tmp/walk`). Each one signs in through the Access code page
  (Your email `alice@example.com`) and then acts only through visible
  controls. Each page recorded:
  - uncaught page errors, and console errors and warnings;
  - failed requests, and `/api/` responses of 400 or more, with bodies;
  - every native dialog;
  - every non-GET `/api/` request, with its body.

  The REST API (bearer access code) was used only to read back what a
  gesture had persisted, and for the setup listed under Stubs.
- **Console baseline:** the only console error on a clean page was the 401
  for the signed-out first load of `/next/inbox`, which is how the
  access-code page is served. Other errors are listed under the step that
  caused them.

## Stubs and setup

The server, store, built frontend, browser and gestures were real
throughout.

1. **A stored template with a priority** (`Urgent fix`, priority 8):
   `POST /api/templates` after every server start. The demo seeds no
   template with a priority.
2. **Task-list read failures:** a Playwright route on `GET /api/tasks`
   (with or without a query string) answering
   `500 {"error":"store unavailable"}`. It was switched on once for a cold
   load and once mid-session. Every other request passed through.

## Results by area

| Area | UI | Outcome |
|---|---|---|
| Inbox groups, counts, rail | `/next/inbox` | Pass |
| Inbox approve / reply / ack | `/next/inbox` | Pass |
| Inbox stuck-task rows | `/next/inbox` | **Bug 4, fixed** |
| Inbox cold `/api/tasks` failure | `/next/inbox` | Pass |
| New task: queued, proposed, no-repo | New task sheet | Pass |
| New task from stored template | New task sheet | **Bug 1, fixed** |
| Tasks tabs, rows, search | `/next/tasks` | **Bug 2, fixed** |
| Tasks cold `/api/tasks` failure | `/next/tasks` | **Bug 3, fixed** |
| Tasks mid-session failure + recovery | `/next/tasks` | Pass |
| Task page lifecycle | `/next/task/:id` | Pass, **bug 5 fixed** |
| Task page sections | `/next/task/:id` | Pass, with gaps |
| Tasks batch bar | `/next/tasks` | Pass |

### 1. Inbox (`/next/inbox`)

1.1. Opened `/next/inbox` signed out: the daemon's access-code page (401).
After signing in, it landed on `/next/inbox` with "28 decisions waiting on
you". The rail's Inbox badge also said 28. The filter strip read All 28,
Asked 4, Releases & scripts 1, Merges 13, Proposals 3, Blocked & alerts 7,
Paused 0, Unread 28.

1.2. Groups shown, in order:
- Asked: approve a release (v2.4.0, soak 25h10m of 24h, Approve/Reject);
- Asked: reply (#4, #7, #36, #37);
- Review needs your decision (#29);
- Asked: submit for merge (10 rows). CL tasks #14–#19 say "Submit is off:
  CI has not reported on this change yet.";
- Unblock a merge (#26, Retry merge);
- Approve on GitHub (#21);
- Look at a stuck task (#5, #22, #23, #24), each with its stall sentence;
- Approve a proposal (#1, #12, #35, with Approve all 3);
- Alerts (3).

No base-branch repair, local-script or config-repo sync group appeared,
because this demo seeds none of them. The `/next/` Inbox is a queue with an
inspector, and has no collapsible sections. The classic-only parts of the
scripted coverage (Notifications collapsed by default, section state across
reloads, Defer all and Acknowledge all bulk actions) have no `/next/`
counterpart to walk.

1.3. **Approve a proposal:** clicked row #35 "Qualify the candidate". The
inspector offered Approve, Defer and Decline. Approve called
`POST /api/tasks/35/approve`. The row left the queue, and the API read
`queued`, approved.

1.4. **Answer the parked question:** clicked #4. The inspector showed the
question with a composer ("Send to #4"). I typed "Only unblock the merge
queue for now." and sent it (`POST /api/tasks/4/comments`). The row left,
the task read `queued`, and the newest comment was alice's.

1.5. **Approve all proposals:** pressed "Approve all 2" in the proposal
group. A confirmation asked "Approve 2 proposals in “Approve a proposal”?"
with "2 owned by debian". Its Approve 2 button stays disabled until "I am
approving work owned by someone else" is ticked, which is the intended
guard. I cancelled.

1.6. **Alerts:** `pipeline.merge-stalled:1` offered Acknowledge,
Investigate and Clear. Acknowledge called `POST /api/notifications/1/ack`.
The row stays, marked "acknowledged", as the seeded acknowledged
`agent.1:flaky-e2e` row already does, until it is cleared. I read this as
intended.

1.7. **Stuck tasks — bug 4:** each row in "Look at a stuck task" showed its
sub-case sentence. The inspector offered only the composer ("Send & wake
agent" or "Comment only"), and no control. `/api/tasks` names the control on
each task's `reason.control`:
- #22 `queued/ready` → Run now;
- #23 `running/live_run` → Cancel;
- #24 `deferred/wake_time` → Undefer.

The classic Inbox offers that control (`ui/src/state.js` `reasonControl`).
After the fix, #22 offers Run now, #23 Cancel (with a confirmation) and #24
Undefer. Pressing Undefer on #24 took the row out of the group, and the
task read `queued`.

1.8. **Cold `/api/tasks` failure:** the headline fell to "4 decisions
waiting on you": the release and the alerts, which come from other
endpoints. The banner read "Could not read tasks; retrying." It never
claimed nothing is waiting. The rail badge also fell to 4.

1.9. Keyboard: `j` moved the selection to the next row. Row clicks open the
inspector, and the inspector's "open task" link reaches `/next/task/:id`.

### 2. New task (the `/next/` New task sheet, from the rail's New)

2.1. The sheet opens as a modal with: Start from template, Title,
Description, Attachments, the demo's custom fields (Priority, Area,
Requested by, Due date, Estimate, Customer-facing), Repository, Create a new
branch, Base branch, Merge on its own, Model ("Deployment default
(gemini-2.5-pro)"), Depends on, Capabilities & repo context (3 grants),
More options, Approve and start now, Put at front of queue, Cancel, Shape
with Director, Ask the Director, and Create and start. More options adds:
- Do not port (`noPort`), Fixes red base branch, and Found in release;
- Connection, Agent framework, Network access, and the nothing-to-do
  action;
- Review template, Extra instructions, and Sandbox shape (CPUs, memory,
  disk);
- Reviewers, and Auto-approve child proposals.

2.2. **Proposed path:** turned off Approve and start now, and the submit
button became "Save as proposal". The new task #38 read `proposed`.

2.3. **Queued, no repository:** chose "No repository (answer in a
comment)". Base branch and auto-merge disappeared, and the sheet posted
`noRepo: true`. The new task #39 read `queued`, with no repository.

2.4. **Escape and Cancel:** Escape closed the sheet and kept the draft.
Reopening restored the title. Cancel closed it.

2.5. **From a stored template — bug 1:** picked "Urgent fix". The sheet
switched to a preview ("title: Urgent: (template)"). Create and start
posted `POST /api/tasks/from-template {"template":"template-3"}`, and the
server answered `404 {"error":"no template
repo:template:acme:widgets:template-3"}`. Nothing was filed. After the fix,
the sheet posts `POST /api/tasks {"templateId":"template-3"}`. It got a
201, and task #38 read priority 8 and `templateId: template-3`.

2.6. **Fork head label:** with Create a new branch unticked, Base branch
accepted `octo-fork:feature/x`, `bad:label:here` and `  :nope` with no
explanation, and Create stayed enabled. The server refuses the malformed
label (`base: "bad:label:here" has more than one ':'`), and the sheet shows
that error on submit. The client-side explanation that the classic form
gives (`ui/src/forkHead.js`) is not ported. See the gaps below.

### 3. Tasks (`/next/tasks`)

3.1. Every tab (`?tab=backlog|proposed|running|asked|submit|deferred|closed|all`)
loaded, and the URL kept the tab. Back and Forward walked between tabs.
Rows showed:
- a state oval ("Merging" for a change in the merge queue: #5, #21, #26,
  #34);
- the priority badge, the repo, the owner, and the CI chip;
- "Part of …" for workstream children, and "Waits on a dependency" for
  blocked #11 in the Backlog.

3.2. **Search — bug 2:** `/next/tasks?tab=all&q=retry` narrowed the list
to 8 rows, but the top bar's "Filter this view" box was empty. Typing
"charge" in it changed nothing, because `?q=` won. Clearing it changed
nothing either. Typed filters never reached the URL, so a reload dropped
them. After the fix, `?q=` fills the box, and typing writes back into
`?q=`:
- typing "charge" gave 2 rows and `?q=charge`;
- a reload kept it;
- clearing the box gave 35 rows and removed `q`.

3.3. **Cold failure — bug 3:** with every `GET /api/tasks` failing from the
first load, `/next/tasks` showed zero counts on every tab, no error, and
"Nothing in this tab — switch tabs or create a task". The cause: the rail
had already read the shared task feed and failed. The feed's error signal
does not change while it keeps failing, and the Tasks store only reported
the error when it changed after the page started. After the fix, the page
shows "The task list could not be read: … This page tries again on its
own." The pane says "Tasks could not be read yet." instead of claiming the
tab is empty.

3.4. **Mid-session failure:** failing the list after a good load showed the
same banner with the last rows kept in place. Lifting the stub cleared it
within one poll.

3.5. **Batch bar (Backlog):** ticked #2 and #11. The bar offered Move to
top, Move to bottom, Set priority, Defer, Close and Clear.
- Set priority → High 8 sent `PATCH {"priority":8}` to both, offered an
  Undo snackbar, and the API read 8 for both.
- Close on two unstarted tasks closed them at once with an Undo snackbar.
  Per `close()`, only live tasks ask first. I read this as intended.

### 4. Task page (`/next/task/:id`)

4.1. Opened from Tasks rows, from the Inbox inspector, and by deep link.

4.2. **Lifecycle on proposal #1:**
- Defer called `POST /defer {"deferredUntil":""}`, and a reload showed
  "Bring back". Bring back called `/undefer`.
- Approve made it `queued` and offered Withdraw approval.
- Close took effect with no dialog, and Reopen restored it to `queued`.

History, newest first: "reopened · alice@example.com (access code)",
"Closed · alice@example.com (access code)", Waiting, Proposed. So the
timeline names the principal that closed and reopened the task. At 390 px
the page had no horizontal overflow.

4.3. **Sections seen on seeded tasks:**
- #31: human-review hold callout with Release hold, the review task's
  reason and "Raised by review task 32".
- #36: inline config-change card. It shows the
  `grain/config/36/poll-interval @ 8c6350ed` diff of
  `tables/grain_config.json`, with Approve & merge config and Reject….
- #4: the parked question in the conversation, with a Reply action.
- #23: a live run with Stop.
- #14 (CL task, no repository): Submit is offered on the task page while
  the Inbox says Submit is off until CI reports.

4.4. **Bug 5:** Details read "Filed now ago by operator (local)" for any
task filed in the last minute. Fixed to "just now by …".

## Bugs fixed in this run

| # | Bug | Fix | Coverage |
|---|---|---|---|
| 1 | Stored template filed via from-template → 404 | `fromTemplateStem` stems only repo-declared templates (`repo:template:` ids or `declaredIn`) | unit + e2e |
| 2 | `?q=` and Filter box out of step | box seeded from `?q=`, writes back to it | unit + e2e |
| 3 | Cold feed failure → empty tab, no error | store reports the feed's existing error on start; pane stops claiming an empty tab | unit + e2e |
| 4 | Stuck-task rows offer no control | classifier adds the `reason.control` action | unit + e2e |
| 5 | "Filed now ago" | `filedAgo()` | e2e |

The new e2e spec is `ui-ng/e2e/next-intake-lifecycle.spec.js`. It runs over
a demo started with `-stalled-tasks`, through `make test-e2e-ng`. Each new
unit test failed with its fix reverted.

**Deterministic reproductions** (on `e69d34223`, over `grain demo` with the
flags above):
1. `POST /api/templates {"name":"Urgent fix","title":"Urgent: ","priority":8}`.
   Then New → Start from template → Urgent fix → Create and start. The
   console shows a 404 from `/api/tasks/from-template`.
2. Open `/next/tasks?tab=all&q=retry` and type in "Filter this view". The
   list does not change.
3. Route every `GET /api/tasks` to a 500, then open `/next/tasks`. The page
   shows zeros and "Nothing in this tab", with no banner.
4. Open `/next/inbox` and select #24 "Rotate the staging signing keys".
   There is no Undefer button.
5. File any task and open `/next/task/<id>` within a minute. It reads
   "Filed now ago".

## Gaps on `/next/` (not fixed here; proposed as follow-ups)

These exist on the classic pages and are missing from their `/next/`
counterparts:

- **New task sheet:**
  - no Task priority (the number), Owner, Keep running or Admission field;
  - no client-side fork-head (`<owner>:<branch>`) explanation or refusal;
  - no `/new?field.<name>=` link handling on `/next/`. The rail's New opens
    the sheet, and `CLASSIC_NEW_TASK = "/new"` stays the classic page.
- **Task page:**
  - Defer has no date (defer until) option;
  - Close does not ask for a reason;
  - #27 ("Charge in cents") carries `pathTriggerHold: "required"`, with
    its Code review gate running in #28. The `/next/` page renders no
    path-trigger hold callout or review-task link for it (nothing in
    `ui-ng/src` reads `pathTriggerHold`), while #31's human-review hold
    does get a callout.
- **Tasks list:**
  - no State, Admission, Human review, External ref or Origin filter;
  - no `owner:` or `externalRef` query syntax.

  The `/next/` list is tabs plus a free-text filter.
- **Inbox:**
  - no collapsible sections;
  - no Defer all or Acknowledge all bulk actions.

  These are design differences in the queue-and-inspector layout rather
  than breakage.

## Minor observations

- Selecting a change whose branch has no diff yet makes the Inbox inspector
  call `GET /api/tasks/{id}/diff-preview`, which answers 404 ("has nothing
  to compare yet" for #6 and #26, "no repository branch to preview" for
  CL task #14). That logs a console error each time. The UI handles it
  quietly.
- Picking a stored template whose `autoMerge` is false still posts
  `autoMerge: true` from the sheet's default. A stored template cannot say
  "unset" versus "off" (`POST /api/templates` stores `false` either way), so
  this needs a decision on the model before the UI can honour it.
- Seeded tasks show retired capability names (`self-debug`, `read-issues`,
  `ci`) in their grants. This is demo seed data.

## Steps not walked (no seed, or no time)

These could not be walked in this run's budget, or the demo seeds nothing
for them:
- pending-secret and `request_access` cards;
- host-script approvals;
- goal cards;
- the work tree and Committed context;
- image attachments in the full-screen viewer, and Mermaid diagrams;
- Force merge and Re-run CI;
- Hosting and Admission toggles on open tasks;
- pipeline manual mode and Run selected now;
- role-gated rendering on `/next/` (covered by `next-inbox.spec.js`'s
  viewer case).

## Tests run

- `cd ui-ng && npx vitest run`: 43 files, 379 tests passed.
- `npx eslint src` and `npx prettier --check .`: clean.
- `cd ui-ng && npx playwright test` (the whole `make test-e2e-ng-suite`):
  42 passed, including the new spec. That run used the build with fixes 1–4.
  The new spec and `next-task-interactions.spec.js` passed again after
  fix 5.
- No classic `ui/` file changed, so the classic suites were not re-run.

## Screenshots

`ui/e2e/screenshot.mjs` against the fixed build: `/next/inbox`,
`/next/tasks` and `/next/task/31`, desktop and phone, light and dark. Plus
the stuck-task row with its Undefer control at 1400×900 light and 390×844
dark. Undefer was visible and inside the viewport at both sizes, and the
phone page had no horizontal overflow. None of these are committed. No
Gemini key was present, so the PNGs could not be described by a model, and
this sandbox's file reader returns PNGs as bytes. Layout was checked with
bounding boxes instead.
