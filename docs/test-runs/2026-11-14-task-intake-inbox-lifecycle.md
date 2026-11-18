# Task intake, Inbox and lifecycle walkthrough — 2026-11-14 (task 5630)

- **Date:** 2026-11-14, 11:40–12:20 UTC, one run.
- **Task:** task 5630, branch
  `grain/53549e42/task-5630-walk-through-task-intake-inbox`, base `main`.
- **Commit tested:** `3266698e` ("Merge pull request #1455"), binary
  `v0.0.0-20261114110730-3266698eaa13`. The fixes below were checked again
  on rebuilds of the same commit with them applied.
- **Focus:** the earlier reports in this series (most recently
  `2026-11-11-task-intake-inbox-lifecycle.md`) walked the classic UI. This
  run walked the new UI at `/next/` first, as "Two UIs" in `PROMPT.md`
  asks, and notes for each step which UI it ran against and which steps
  have no `/next/` page yet.
- **Environment:**
  - Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0; Playwright
    1.62.1 from `ui-ng/package-lock.json`, headless Chromium
    (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC,
    viewport 1400×900 unless a step says 390×844.
  - Built with `make frontend`, then `go build -o /home/debian/grain
    ./cmd/grain`. The fixed builds re-ran `cd ui-ng && npm run build` and
    `go build`.
  - Server: `grain demo -addr 127.0.0.1:<port> -open=false -github-sim
    -fake-ci -scheduled-workflows -fork-pull-request -git-proxy
    -base-branch-ci -merge-rules -merge-queue-states -stalled-tasks
    -sensitive-hold -review-questions -human-review -secrets-store
    -notifications -qualification -mechanical-port -config-change
    -access-code-file <file>`, with `TMPDIR` pointed at a fresh scratch
    directory each start, so each store was a throwaway embedded SQLite
    file. Three servers ran: `:8420` (base build), `:8421` (first fix),
    `:8422` (all fixes).
  - There was no `/home/debian/.gemini-api-key`, so no model was called and
    screenshots could not be described by `LOOK=1`.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout. Each signs in through the access-code page (Your email
  `alice@example.com`) and then acts only through visible controls. Each
  page logged uncaught page errors, console errors and warnings, failed
  requests, `/api/` responses of 400 or more (with bodies), native dialogs,
  and every non-GET `/api/` request with its body. The REST API (bearer
  access code) read back what a gesture persisted.
- **Console baseline:** apart from the stubbed 500s, the only console
  error was the expected 401 on the signed-out first load of
  `/next/inbox` (the access-code page). No uncaught page error and no
  failed request was recorded.

## Stubs

The server, store, built frontend, browser and gestures were real
throughout.

1. **Task-list read failures:** a Playwright route on `GET /api/tasks`
   answering `500 {"error":"store unavailable"}` from the first paint, and
   then removed mid-session to watch recovery.
2. **No open alerts:** for one cold-failure case, a Playwright route on
   `GET /api/notifications` answering `[]`, so nothing but tasks could
   reach the Inbox. The demo seeds three alerts and a staged release.
3. **A grain/viewer role:** a Playwright route rewriting `GET /api/config`'s
   `access` to `grain/viewer` with `tasks:read` only (the stub
   `ui-ng/e2e/next-inbox.spec.js` uses). This tests what the UI offers,
   not the server's refusals.

## Which UI each area ran against

| Area                                   | UI                               |
| -------------------------------------- | -------------------------------- |
| Inbox                                  | `/next/inbox`                    |
| New task (rail **New**)                | `/next/` sheet                   |
| `/new?field.<name>=` links             | no `/next/` page (redirects to Inbox) |
| Tasks, tabs, batch bar                 | `/next/tasks`                    |
| Tasks State/Admission/Origin/… filters | no `/next/` equivalent           |
| Task detail and lifecycle              | `/next/task/<id>`                |
| Boards                                 | classic only (not walked)        |

## Steps and outcomes

### 1. Inbox (`/next/inbox`)

| #    | Step | Outcome |
| ---- | ---- | ------- |
| 1.1  | Sign in at `/next/inbox`; open it again from the rail's **Inbox** link on `/next/tasks`. | Lands on `/next/inbox` both ways. Rail badge 28, headline "28 decisions waiting on you". |
| 1.2  | Read the groups. | release approval (v2.4.0, soaked 25h10m of 24h, Approve/Reject), reply (4: #4, #7, #36, #37), review decision (#29), submit for merge (10), unblock a merge (#26), approve on GitHub (#21), stuck (4: #5, #22, #23, #24), proposals (3: #1, #12, #35), alerts (3). No base-branch repair, host-script or config-repo sync rows were seeded. |
| 1.3  | Select #1 (proposal) → **Approve**. | `POST /api/tasks/1/approve`; row leaves at once; API reads `queued`. |
| 1.4  | Select #4 (parked question), read the question, type a reply, **Send**. | `POST /api/tasks/4/comments`; row leaves; API reads `queued` with the comment. |
| 1.5  | Select each stuck row. | **Bug 1:** no control at all, though #24's own text says "Undefer picks it back up by hand". Fixed: Run now (#22), Cancel task (#23, asks first), Undefer (#24). Each pressed row left the group, API reads `queued`, `queued`, `closed`, and stays gone after reload. #5 (merge-queue sub-case with no control) correctly offers none. |
| 1.6  | Cold `GET /api/tasks` failure (Stub 1), alerts present. | Before: "4 decisions waiting on you", counting only alerts. After: "At least 4 decisions waiting on you", with "Could not read tasks; retrying." |
| 1.7  | Cold failure with no alerts (Stubs 1+2). | **Bug 2:** "Nothing waiting on you" and "Nothing is waiting on you. Every task is running, waiting on grain, or done." under the error banner. Fixed: headline "Inbox" and "Tasks could not be read, so this queue cannot say yet what is waiting on you." |
| 1.8  | **Approve all 3** on the proposals group. | Confirm dialog counts per repo and owner, and needs the "approving work owned by someone else" tick before **Approve 3**. Three `POST …/approve`; group gone; all three `queued`. |
| 1.9  | Alert "A merge has been stalled for 2 hours" → **Acknowledge**. | `POST /api/notifications/1/ack`; the row stays, marked acknowledged, as the seeded acknowledged alert does; rail and headline drop to 25. |
| 1.10 | `x` on the keyboard. | Checks the focused row and raises the batch bar ("1 selected · Acknowledge all (1) · Clear"). |
| 1.11 | Viewer role (Stub 3). | Stuck row's Undefer, like Approve, is rendered disabled (now in the e2e spec). |
| 1.12 | #26 (unblock a merge). | Offers **Retry merge**. |

Not on `/next/inbox` by design: collapsible sections and their
`Enter`/`Space` toggles, the collapsed-by-default Notifications section, and
the classic "Ship as is" wording (the review group offers the conversation
and Submit instead).

### 2. Creating tasks (`/next/` New task sheet)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 2.1 | Rail **New** opens the sheet directly (no menu). | Fields: template, title, description, attachments, the demo's custom fields (Priority, Area, Requested by, Due date, Estimate, Customer-facing), repository, Create a new branch, base branch, auto-merge, model ("Deployment default (gemini-2.5-pro)"), depends on, capabilities (3 grants); **More options** adds Don't port, Fixes red base branch, Found in release, connection, agent framework, network, nothing-to-do action, review template, extra instructions, sandbox shape, reviewers, auto-approve child proposals. |
| 2.2 | Title + description, **Create and start**. | `POST /api/tasks` with `approved` true; #38 `queued`, priority 5, `acme/widgets`. |
| 2.3 | Untick **Approve and start now**; button becomes **Save as proposal**. | #39 `proposed`. |
| 2.4 | Type a title, **Cancel**, reopen. | Sheet closes; the draft title is restored on reopen. **Escape** also closes. |
| 2.5 | `/next/new?field.title=…`. | No such route: redirects to `/next/inbox` with the query kept and no sheet. Field links are classic-only. |

The `/next/` sheet has no numeric **Task priority**, **Owner**, **Keep
running**, or fork head label field, and no "no repository" choice was
found in the picker. Those steps, the template-priority draft rule and the
`settings/new-task-form.yml` notice remain classic-only, and were not
re-walked this run (the 2026-11-11 report covers them on the classic form).

### 3. Tasks (`/next/tasks`)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 3.1 | Rail **Tasks**. | Tabs Backlog 10, Needs approval 3, Running 3, Asked 3, Submit & land 15, Deferred 2, Closed, All 36. Rows show the "Normal 5" priority badge, "Waits on a dependency" and "Skips worker limits" chips. |
| 3.2 | Tick #2 and #20 → batch bar **Defer**. | Two `POST …/defer`; both `deferred`; rows leave the Backlog and the selection clears. |
| 3.3 | Deferred tab, tick both. | **Bug 3:** the bar offered only Set priority, Close and Clear. Fixed: **Bring back** undefers both; rows leave the tab; API reads `queued`. |
| 3.4 | Back / Forward between `/next/tasks` and `?tab=deferred`. | URL and tab follow. |
| 3.5 | Cold `GET /api/tasks` failure (Stub 1). | **Bug 4:** zero counts and no notice at all. Fixed: "The task list could not be read: store unavailable. This page tries again on its own." Removing the stub clears it and the rows arrive without a reload. |
| 3.6 | At 390×844 with two rows ticked on Deferred. | Bar wraps to two lines (Set priority, Bring back / Close, Clear), no horizontal overflow. |

No `/next/` equivalent exists for the classic State, Admission, Human
review, External ref and Origin filters, the server-side closed search, or
`owner:any:me`. Those were not re-walked.

### 4. Task detail and lifecycle (`/next/task/<id>`)

| #   | Step | Outcome |
| --- | ---- | ------- |
| 4.1 | `/next/task/2`. | Header Pause, Run now, Edit, Defer, Withdraw approval, Follow up, Close, More ▾ (Compact context…). Details, capabilities with remove controls, dependencies, history. |
| 4.2 | **Pause**, then **Resume** (queued task). | `POST …/pause {"immediate":false}`; "Paused" chip and `paused.by` = alice; resume clears it; state stays `queued`. |
| 4.3 | **Defer** (no date offered), **Bring back**. | `deferredUntil: ""`; `deferred` then `queued`. Defer-with-a-date has no `/next/` control. |
| 4.4 | Pause while deferred, Resume. | Keeps `deferred` throughout. |
| 4.5 | **Close**, **Reopen**. | `closed` then `queued`; history shows "Closed · alice@example.com (access code)" and "reopened · alice@example.com (access code)". No horizontal overflow at 390 px. |
| 4.6 | #31 → **Release hold**. | `POST …/human-review/release`; the "Held for human review" callout goes. |
| 4.7 | #36 → config diff → **Approve & merge config**. | `POST …/config-change/approve` with the head SHA; task `queued`; timeline records "Approved and applied config change". |
| 4.8 | #37 → **Reject…** → reason → **Confirm reject**. | `POST …/config-change/reject` with the reason; task `queued`; card gone after reload. |
| 4.9 | #26. | Merge-rule lines with the rule behind each ("from rule deploy"). |

## Bugs found and fixed

### Bug 1 — a stuck Inbox row offered no control

**Reproduction:** `grain demo -stalled-tasks`; open `/next/inbox`; select
"Rotate the staging signing keys" (#24, deferred past its wake time). Its
text says "Undefer picks it back up by hand", but the inspector offers only
the conversation.

**Cause:** `inbox-classifier.ts` filed `t.stall` tasks with no actions. The
classic Inbox reads the sub-case's `control` (`reasonControl` in
`ui/src/state.js`).

**Fix:** `stallControl` maps the task's `reason.control` (or the merge
queue's) to Run now, Cancel (confirms, labelled "Cancel task" so it is not a
second "Cancel" beside the dialog's own), Undefer or Retry merge.
`Task.mergeQueueReason` is now typed.

**Coverage:** `inbox-classifier.spec.ts` "offers a stuck task the control
its sub-case names…"; `ui-ng/e2e/next-inbox-intake-regressions.spec.js`
"a stuck row offers the control its sub-case names…" (also checks the
viewer sees it disabled). Fails on the base build.

### Bug 2 — a cold task-list failure read as an empty Inbox

**Reproduction:** Stubs 1 and 2, open `/next/inbox`.

**Cause:** the store sets `loaded` after its first read whether the task
feed answered or not, and the headline and empty state read only
`loaded` and the entry count.

**Fix:** `InboxStore.tasksRead` (the task feed's own `loaded`) gates the
"Nothing waiting" headline and empty state; until it is true the headline
is "Inbox" or "At least N …".

**Coverage:** e2e "a cold task-list failure never claims nothing is
waiting…". Fails on the base build.

### Bug 3 — no Bring back in the Deferred tab's selection bar

**Reproduction:** `/next/tasks`, defer two tasks, open the Deferred tab and
tick both.

**Fix:** a **Bring back** button (`batch-undefer`), gated on `POST
/api/tasks/{id}/undefer`, calling the existing `undefer` handler.

**Coverage:** e2e "the Deferred tab's selection bar brings several tasks
back at once".

### Bug 4 — the Tasks page hid a cold task-list failure

**Reproduction:** Stub 1, load `/next/tasks` directly.

**Cause:** the rail starts the shared `TaskFeedStore` before the page, so
the failure was already set when `TasksStore`'s effect first ran (with
`started` false, so it was ignored); later retries failing the same way
set an identical string and never re-ran the effect.

**Fix:** `TasksStore.start()` surfaces a standing failure.

**Coverage:** the same cold-failure e2e test, which also checks the notice
clears on recovery.

## Tests run

- `npx vitest run src/app/inbox src/app/tasks` — 6 files, 94 tests pass.
- `npx playwright test e2e/next-inbox-intake-regressions.spec.js
  e2e/next-inbox.spec.js e2e/next-full-system.spec.js
  e2e/next-task-interactions.spec.js` — all pass. The two original
  regression tests were run against the unfixed frontend and failed there.
- `npm run format:check` in `ui-ng/` passes.

## Observations (not treated as defects)

- **O1:** `/next/` task detail's **Close** acts with no confirmation, while
  the Inbox's Close on a failed task asks first. It is reversible (Reopen).
- **O2:** Seeded #2 lists `self-debug` and `read-issues` capabilities,
  names retired onto `api:self-debug` + `grain#source` and
  `github#read-issues`. The demo seed, not the UI.
- **O3:** Acknowledging an alert leaves its row in the Alerts group marked
  "acknowledged"; it only leaves on Clear. Consistent with the seeded
  acknowledged alert.

## Not covered this run

Host-script approvals, the pending-secret box, suspended sessions,
cross-team approvals, `request_access` cards, a grain CI verdict with
Re-run CI, Force merge, the chat pane, Mermaid and image viewers,
attachments, the pipeline's manual mode with Run selected now, the goal
card, committed context, and the classic-only steps listed above. Earlier
reports in this series covered most of them on the classic UI.

Screenshots of `/next/inbox` (stuck row selected), `/next/tasks?tab=deferred`
and `/next/task/24` at 1400×900 and 390×844 in light and dark, and of the
cold-failure Inbox and Tasks before and after the fix, were taken with a
throwaway Playwright script. The file reader here cannot render PNGs and
there was no Gemini key, so layout was checked by bounding boxes instead:
the new controls sit inside the viewport at both widths and no page
overflows sideways. None were committed.
