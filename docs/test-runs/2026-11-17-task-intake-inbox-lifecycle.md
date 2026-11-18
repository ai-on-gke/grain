# Task intake, Inbox and lifecycle walkthrough — 2026-11-17 (task 6588)

- **Date:** 2026-11-17, 22:55–00:30 UTC, one run.
- **Task:** task 6588, branch
  `grain/53549e42/task-6588-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `07e43408c` ("Merge pull request #1803"). Each fix
  below was checked again on a rebuild of that commit with it applied.
- **Focus:** the parts the 2026-11-16 report left out or listed as not
  covered: pause / Pause immediately / Resume, Stop and its Resume,
  dependencies, attachments, close/reopen at phone width, role-gated
  read-only rendering, the changelist (CL) rows' inline actions, the
  Tasks selection bar's Capability / Close / Reopen / Submit / Retry
  merge, the New task sheet's grant builder and auto-merge, and the
  Inbox's Defer all, parked-question reply and alert Acknowledge.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go from `/usr/local/bin/go`; Node.js
    v20.19.0; Playwright from `ui-ng/package-lock.json` with headless
    Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`); timezone
    UTC; viewport 1400×900 unless a step says 390×844.
  - Built with `make frontend` and `go build ./cmd/grain`; fixed builds
    re-ran `cd ui-ng && npm run build` and `go build`. As on 2026-11-16,
    `make frontend` drops `@playwright/test` from `ui-ng/node_modules`;
    `npm ci` in `ui-ng/` puts it back.
  - Servers: `grain demo -addr 127.0.0.1:8450 -open=false -github-sim
-fake-ci -scheduled-workflows -fork-pull-request -git-proxy
-base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
-sensitive-hold -review-questions -human-review -secrets-store
-notifications -qualification -mechanical-port -config-change
-access-code-file <file>` on the base build, and a second one on
    `:8451` (fewer flags) on the fixed build for the screenshots. Each had
    `TMPDIR` set to a fresh scratch directory, so each store was a
    throwaway embedded SQLite file.
  - There was no `/home/debian/.gemini-api-key`, so no model was called
    and `LOOK=1` was not available.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in through `POST /api/auth/access-code` (as
  `ui-shared/e2e/accesscode.js` `signIn` does) and then acts only through
  visible controls. Each page logged uncaught page errors, console errors
  and warnings, failed requests, `/api/` responses of 400 or more (with
  bodies), native dialogs, and every non-GET `/api/` request with its
  body. The REST API (bearer access code) read back what each gesture
  persisted.
- **Console baseline:** no uncaught page error on any `/next/` page. The
  only console errors were Bug 2's 404s. The only failed requests were
  `GET /api/config … net::ERR_ABORTED`, where a script navigated away
  while a read was in flight.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout. One boundary was stubbed:

1. **Read-only role:** an access-code session is the deployment's owner,
   whatever name it signs in with (adding `user:viewer@example.com` as
   `grain/viewer` under `/api/access/principals` and signing in as that
   name still resolved the owner's access). So, for steps 4.1–4.4 only,
   Playwright's `page.route` rewrote the `access` block of
   `GET /api/config` to `{role: "grain/viewer", roles: ["grain/viewer"],
full: false, scopes: ["tasks:read"]}`, as `next-inbox.spec.js`'s
   viewer test does. The server still treated the session as the owner,
   which is why step 4.3's comment was accepted.

## Which UI each area ran against

| Area                            | UI                                           |
| ------------------------------- | -------------------------------------------- |
| Inbox                           | `/next/inbox`                                |
| New task sheet                  | `/next/` sheet (rail **New**)                |
| Tasks list, tabs, selection bar | `/next/tasks`                                |
| Task detail and lifecycle       | `/next/task/<id>`                            |
| System → Pipeline manual mode   | no `/next/` page; not walked                 |
| `/new?field.<name>=` links      | no `/next/` route (`**` → Inbox); not walked |
| Boards                          | classic only; not walked                     |

No step was walked on the classic UI. Classic code was read only to
compare behaviour (`TaskChat.jsx`'s `AccessGate scope="tasks:write"`,
`state.js`'s state labels).

## Steps and outcomes

### 1. Inbox (`/next/inbox`, base build)

| #   | Step                                                | Outcome                                                                                                                                                                                                                                                                                  |
| --- | --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1.1 | Open `/next/tasks`, then the rail's **Inbox**.      | Lands on `/next/inbox`. Headline "28 decisions waiting on you"; rail "Inbox (28 waiting)". Groups: release 1, reply 4, review decision 1, submit 10, unblock a merge 1, approve on GitHub 1, stuck 5, proposals 3, alerts 3. No console error.                                           |
| 1.2 | Stuck #25 → **Run now**.                            | `POST /api/tasks/25/run-now`; row leaves the group; headline 26; API `queued`.                                                                                                                                                                                                           |
| 1.3 | Select CL rows #14, #16, #19.                       | **Submit** is shown disabled, its tooltip naming why ("Not submittable yet: review is not approved (changes requested); required presubmit has not passed on snapshot 3 (failed)."; #19 "behind head: sync it."). **Bug 2:** each selection logged a 404 console error (`diff-preview`). |
| 1.4 | CL #15 (submittable) → **Submit**.                  | `POST …/15/submit`; row leaves; API `closed`.                                                                                                                                                                                                                                            |
| 1.5 | Parked question #4 → type a reply → **Send to #4**. | `POST …/4/comments`; row leaves; API `queued`.                                                                                                                                                                                                                                           |
| 1.6 | Proposals → **Select all** → **Defer all**.         | A confirm: "Defer all: 3 selected items? … 2 owned by debian, 1 owned by operator". **Bug 3:** the tick it requires reads "I am **approving** work owned by someone else". Confirmed: all three `deferred`; headline 22 → 19.                                                            |
| 1.7 | Alert "A merge has been stalled" → **Acknowledge**. | `POST /api/notifications/1/ack`; the row stays, marked "acknowledged" (2026-11-16 O1). Alert actions: Acknowledge, Investigate, Clear.                                                                                                                                                   |
| 1.8 | Reload.                                             | Headline and rail both 19; nothing answered above came back.                                                                                                                                                                                                                             |
| 1.9 | Look for collapsible sections.                      | None: `/next/` group headers are plain headings, and Alerts is open. Still a gap (below).                                                                                                                                                                                                |

### 2. Task detail and lifecycle (`/next/task/<id>`, base build)

| #    | Step                                                                                              | Outcome                                                                                                                                                                                                                                                                                                                                                         |
| ---- | ------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 2.1  | #2 (queued) → **Pause**; reload; **Resume**.                                                      | `POST …/2/pause {"immediate":false}` → `paused`, still `queued`, same place in the queue (`20,25,22,2,11,…` before, during and after). After the reload the page shows **Resume**. `POST …/resume` lifts it.                                                                                                                                                    |
| 2.2  | #8 (running) → **Pause**.                                                                         | Graceful pause sent at once (`immediate:false`); the page offers **Resume**, and More no longer offers Pause immediately (it is only offered while not paused). **Resume** → `running`.                                                                                                                                                                         |
| 2.3  | #3 (running) → More → **Pause immediately**.                                                      | Asks first: "Pause #3 now? Its run is stopped at once, without the grace period to push its work and write its handover; whatever it has not pushed is lost." **Pause now** → `paused.immediate: true`, still `running`. **Resume** lifts it.                                                                                                                   |
| 2.4  | #26 (running) → **Stop**.                                                                         | `POST …/26/stop`, no confirm; `awaiting_reply` with `turnStopped`. The page offers **Resume** (the retry control).                                                                                                                                                                                                                                              |
| 2.5  | #26 → **Resume**.                                                                                 | Asks first: "Resume #26? alice@example.com (access code) stopped it." Confirm → `POST …/retry` → `queued`, stop lifted.                                                                                                                                                                                                                                         |
| 2.6  | #2 → Dependencies → type `8` → **Add dependency**; hover the chip; open `/next/tasks`; remove it. | `POST …/depends-on {"id":"8","attach":true}`; API `blocked: true`. The chip reads "#8" with title "#8 Add pagination to the tasks API — Running (blocking this task)". The Backlog row gains "Waits on a dependency", and the toolbar "· 2 waiting on a dependency". Remove → `attach:false`; `blocked: false`. The rail has no Blocked count on `/next/` (O3). |
| 2.7  | New task with `red.png` and `blue.png` attached and a Mermaid block → open its page.              | Both upload (`POST /api/tasks` carries them); two thumbnails load from `/api/tasks/41/attachments/{1,2}` (naturalWidth 8). The Mermaid fence renders as a diagram region. Thumbnails open in a new tab: there is no full-screen viewer on `/next/` (gap F1, below).                                                                                             |
| 2.8  | At 390×844, #41 → **Close**, then **Reopen**, reload.                                             | `POST …/close` (no confirm, no reason, as 2026-11-16 O3) → `closed`; `POST …/reopen` → `queued`. No sideways overflow (scrollWidth 390).                                                                                                                                                                                                                        |
| 2.9  | #20 at priority 12 (set by API) → **Edit** → change the title → **Save changes**.                 | The priority chip reads "Urgent 12"; the `PATCH` carries `priority: 12`; the title and priority persist.                                                                                                                                                                                                                                                        |
| 2.10 | CL #14 → **Changes and CI**.                                                                      | Offers Submit, Force merge…, Sync change and Publish replies inline. **Bug 2:** a 404 `diff-preview` console error, then "Diff preview: No branch changes against base yet."                                                                                                                                                                                    |

### 3. Tasks (`/next/tasks`, base build)

| #   | Step                                                                                   | Outcome                                                                                                                                                                                                                                                                                                                                       |
| --- | -------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 3.1 | Click every tab.                                                                       | Backlog 10, Needs approval 3, Running 2, Asked 4, Submit & land 14, Deferred 3, Closed, All 36; the URL follows (`?tab=…`). Badges: "Urgent 12" on #20, "Skips worker limits" on bypass-queue rows, "Merging" on `completed` rows, "Ready to submit" on `awaiting_submit`. A queued task reads "Queued" on Backlog but "Waiting" on All (O1). |
| 3.2 | Backlog → tick #31 and #33 → More → **Capability…** → API preset: memory → **Attach**. | `POST …/capabilities` ×2; both carry `api#memory`; the selection clears. No confirm on attach.                                                                                                                                                                                                                                                |
| 3.3 | Reselect → Capability… → memory → **Detach**.                                          | Asks first, and needs "I am detaching a capability owned by someone else" ticked. Both lose `api#memory`.                                                                                                                                                                                                                                     |
| 3.4 | Reselect → **Close**.                                                                  | Queued tasks close at once with Undo (only live work asks first, by design). Both `closed`.                                                                                                                                                                                                                                                   |
| 3.5 | Closed tab → tick both → **Reopen**.                                                   | Both `queued`.                                                                                                                                                                                                                                                                                                                                |
| 3.6 | Bulk edit… → **Cancel**.                                                               | Nothing sent; the selection stays.                                                                                                                                                                                                                                                                                                            |
| 3.7 | Submit & land → #28 + #30 → More.                                                      | Offers Submit, Retry merge, Bulk edit…, Capability…; with #14 (CL) instead, "Retry merge (1)". **Submit** asks first ("Submit 2 tasks? … I am submitting changes owned by someone else"); #28 → `completed` (merging); #30 stays `awaiting_submit`, held by CI and targeted review #31, and leaves the Inbox (O2).                            |

### 4. Read-only role (Stub 1, base build)

| #   | Step                                                | Outcome                                                                                                                                              |
| --- | --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| 4.1 | `/next/inbox` as `grain/viewer`, select a proposal. | "Your role grain/viewer does not hold tasks:admin, which this needs." The reply box is read-only.                                                    |
| 4.2 | Rail **New task**.                                  | Disabled.                                                                                                                                            |
| 4.3 | `/next/task/2`.                                     | Every lifecycle control is gone. **Bug 1:** the composer stays editable, and typing enables **Send to #2**, which sent `POST /api/tasks/2/comments`. |
| 4.4 | `/next/tasks`.                                      | No row controls beyond the list's own view switches.                                                                                                 |

### 5. New task sheet (`/next/`, base build)

| #   | Step                                                                                   | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                 |
| --- | -------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 5.1 | Rail **New**.                                                                          | Template picker, title, description, attachments, the demo's custom fields (Priority, Area, Requested by, Due date, Estimate, Customer-facing), repository, Create a new branch, base, "Merge on its own once CI and review pass" (ticked by default), model ("Deployment default (gemini-2.5-pro)"), Depends on, "Capabilities & repo context (3 grants)", More options, Approve and start now, Put at front of queue. |
| 5.2 | Capabilities → **+ Add API scope or method grant** → open its scope picker → `Escape`. | Closes only the picker; the sheet and builder stay. A second `Escape` (focus in the builder) closes the sheet; reopening it keeps the draft's title.                                                                                                                                                                                                                                                                    |
| 5.3 | Untick auto-merge, turn **Approve and start now** off → **Save as proposal**.          | `POST /api/tasks` with `autoMerge:false`; #42 `proposed`, `autoMerge:false`.                                                                                                                                                                                                                                                                                                                                            |
| 5.4 | #42 → **Defer** → **Bring back** → **Approve** → **Edit** → new title → Save.          | `autoMerge` stays `false` through each; Edit's box reads unticked; `PATCH` sends `autoMerge:false`.                                                                                                                                                                                                                                                                                                                     |

## Bugs found and fixed

### Bug 1 — a read-only role could type and send on a task's page

**Reproduction:** sign in with a role lacking `tasks:write` (Stub 1).
Open `/next/task/2` and type in the composer. **Send to #2** turns on, and
pressing it sends `POST /api/tasks/2/comments`, which a real read-only
role's server refuses. The `/next/` Inbox reply box and classic's
`TaskChat` (`AccessGate scope="tasks:write"`) are both read-only for that
role.

**Fix:** `ui-ng/src/app/task/task-detail.component.ts` disables the
composer (textarea, Attach file and Send) when the role cannot post to
`POST /api/tasks/{id}/comments`. It passes `AccessService`'s
`deniedReasonForTask` as the composer's note ("Your role grain/viewer
does not hold tasks:write, which this needs. …"), and `submitComment`
refuses as well.

**Coverage:** `ui-ng/e2e/next-task-viewer-composer.spec.js`. It fails on
the base code (the composer was enabled).

### Bug 2 — a changelist task asked for a diff preview the server cannot give

**Reproduction:** `/next/inbox` → select CL row #14 (no repository), or
open `/next/task/14?tab=changes`. Each makes a
`GET /api/tasks/14/diff-preview` request, which returns 404 ("task 14 has
no repository branch to preview"; `pkg/ui/diff_preview.go` refuses any
task with no target). Each one logs a console error. The tab then says
"No branch changes against base yet", as though a branch existed.

**Fix:** `InboxStore.ensureDiff` (`ui-ng/src/app/inbox/inbox.store.ts`)
records no preview for an entry with no repository, without asking. The
Changes tab (`task-changes-tab.component.ts`) skips the read and says
"This task's change is a changelist, with no repository branch to
preview."

**Coverage:** `ui-ng/e2e/next-changelist-diff-preview.spec.js` (no
`diff-preview` request from either surface, and the tab's wording). It
fails on the base code.

### Bug 3 — Inbox bulk confirms said "approving" for every action

**Reproduction:** `/next/inbox` → Proposals → **Select all** → **Defer
all**, with a proposal owned by someone else. The tick the dialog
requires reads "I am approving work owned by someone else".
`confirmBulk` never set the confirm dialog's `actionVerb`, so Defer all,
Close, Decline, Submit and Retry merge all fell back to its default.

**Fix:** `confirmBulk` (`ui-ng/src/app/inbox/inbox-page.component.ts`)
passes the batched action's verb ("deferring work", "closing work",
"submitting changes", "retrying merges", …), as the Tasks selection bar
already does.

**Coverage:** `ui-ng/e2e/next-inbox-bulk-verb.spec.js`. It fails on the
base code.

## Tests run

- `npx vitest run src/app/task src/app/inbox` in `ui-ng/`: 12 files, 188
  tests pass. `npx tsc -p tsconfig.app.json --noEmit` is clean, and
  Prettier is clean on every touched file.
- Each new spec was run with its source fix stashed (fails) and applied
  (passes).
- `npx playwright test` on `next-full-system`, the three new specs,
  `next-task-lifecycle-regressions`, `next-tasks-batch-actions` and
  `next-task-interactions`: 20 passed. `next-inbox`,
  `next-inbox-intake-regressions` and `next-inbox-bulk-verb`: 9 passed.
- The whole `ui-ng` e2e suite (`npx playwright test` in `ui-ng/`, all
  three fixes applied, desktop, phone and dark projects): 412 passed,
  2 skipped, in 9.4 minutes.

## Rendering

On the fixed build (`:8451`), these were shot at 1400×900 and 390×844 in
light and dark:

- the viewer's composer on `/next/task/2`;
- CL #14's Changes and CI tab;
- the Inbox's Defer all dialog.

The file reader here cannot show PNGs and there was no Gemini key, so
the layout was checked by bounding boxes:

- **Viewer note:** desktop 946×37 at x=93, above Send (y 523); phone
  364×77 at x=13, inside the 390 px width.
- **Changes tab's line:** desktop 914×20; phone 332×40 (two lines).
- **Defer all dialog:** desktop 570×342, centred; phone 358×382 at x=16.
  It ends "I am deferring work owned by someone else / Cancel / Defer all
  (3)".
- `scrollWidth − clientWidth` was 0 on every page.

None of the shots were committed.

## Observations (not treated as defects)

- **O1:** a queued task's state oval reads "Queued" on the Backlog tab
  and in Workstreams, but "Waiting" on the All tab, in dependency chips
  and on classic. That is `stateLabel`, matching classic's
  `ui/src/state.js`. The two names come from the first `/next/` commit.
  Picking one is a naming decision, so it was left alone.
- **O2:** after a person's Submit on #30, which is still held by CI and
  targeted review #31, the task keeps reason
  `awaiting_submit/not_auto_merge` ("awaiting a person", control Submit),
  and its page still offers Submit. The Inbox correctly drops it.
- **O3:** `/next/` has no Blocked rail count or Blocked tab. Blocked
  tasks are marked "Waits on a dependency" on Backlog rows, and counted
  in its toolbar.
- **O4:** **Stop** acts without asking (it is undone by Resume, which
  does ask). Capability **Attach** acts without asking, though Detach
  asks and needs the ownership tick.
- **O5:** with focus inside the New task grant builder (not on an open
  picker), `Escape` closes the whole sheet rather than the builder. The
  draft survives.

## Gaps on `/next/` (no page or control yet)

Still as listed on 2026-11-14 to 2026-11-16, rechecked where noted:

- **Inbox:** collapsible sections (rechecked: headers are plain
  headings; Alerts starts open), and local-script/host-script approval
  rows (none seeded).
- **Tasks:** the State/Admission/Human review/External ref/Origin
  attribute filters, `owner:any:me`, and server-side closed search.
  Rows have no `imported` chip, outlined external-PR chip or "held for
  approval by <task>" chip. Rechecked: #12, imported from
  `acme/widgets#42` and awaiting import approval, shows none, and
  nothing in `ui-ng/src/app/tasks` reads `imported` or
  `awaitingImportApproval`. Follow-up proposed (below).
- **Task detail:** the full-screen image viewer (F1). Edit still has no
  Owner, External ref, Reviewers, Keep running or free Task priority
  (F3, rechecked on #20).
- **New task:** no Task priority (a fixed 5), Owner, Keep running,
  Admission or Hosting control.
- System → Pipeline (classic only), `/new?field.<name>=` links and the
  `settings/new-task-form.yml` notice, and Boards.

## Not covered

- Host-script approvals, the pending-secret box, `request_access` cards,
  suspended `resume_later` sessions, cross-team approvals, the capability
  audit, the live transcript and the config-change card's Approve &
  Apply. Either no demo seed reaches them, or there was no time in this
  run.
- Force merge on a CI-stuck task, Re-run CI, and the goal card. Retry
  merge was offered, but not pressed.
- The cold `/api/tasks` failure and stale-list retry were walked on
  2026-11-16, and their specs still pass.
