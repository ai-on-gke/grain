# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-04

## What was tested

- **Commit:** `f56fe0d45` (tip of `candidate`, v1.7.0-rc.1;
  `grain/task-1813` branched from it). The one finding below was
  reproduced on this commit. The fix on this branch was then re-checked
  in the same browser, against a rebuilt frontend and server over a
  freshly seeded store.
- **Since the last walkthrough (2026-10-03, `57632f61f`):** yesterday's
  three fixes (batch selection not on screen, the bulk-edit note's
  spacing, Inbox Send on an empty box) are on this base and were
  re-checked. Other changes inside this area:
  - The task pane now opens at once, loading, on a click (`97f315b6a`).
  - A failed save keeps the edit or reply that was typed (`7a531b8fb`).
  - The New task form has a "Don't port to the repo's paired branches"
    box (`noPort`), a Target team picker, and the Model picker sits
    outside Advanced options. Advanced options has a "Specific
    connection" picker. The separate Agent framework picker is gone
    from the form; the framework follows the model.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64 x86_64.
  - Go 1.26.6, Node.js 20.19.0.
  - Playwright 1.62.1 with headless Chromium (chromium-headless-shell
    1234).
  - Viewport 1400×900, browser time zone UTC and locale en-US, except
    where a step names another zone.
- **Server:**
  - The real `grain demo -addr 127.0.0.1:18080 -open=false`, built from
    this tree (`npm ci && npm run build` into `pkg/ui/static`, then
    `go build ./cmd/grain`).
  - It ran over a throwaway embedded SQLite store under a scratch
    `HOME`/`TMPDIR` (`/tmp/demo`). The demo's configured time zone is
    `UTC` (`GET /api/config`).
  - That directory was deleted and the store re-seeded when the binary
    was rebuilt with the fix, so ids filed after that restart at 13.
- **Driver:**
  - Ad-hoc Playwright scripts kept outside the repository (`/tmp/wt`)
    and not committed. They drove visible controls by role and
    accessible name: clicks, typing, keyboard, Back/Forward, reload and
    deep links.
  - The REST API was only read, to confirm persisted state after a UI
    action. It was never used to perform one.
  - Every script logged console errors and warnings, page errors, failed
    requests, HTTP ≥ 400 responses and browser dialogs. Some scripts
    also logged every non-GET request body, to see exactly what a
    gesture sent.
- **Account:** created through the first-run "Create account" dialog
  (`operator`). The session was reused through Playwright storage state.
- **Gemini key:** `/home/debian/.gemini-api-key` was not present in this
  sandbox, so nothing model-backed was exercised. The demo needs no
  model. `gemini-2.5-pro` was picked on the New task form and
  `gemini-2.5-flash` in the task pane's edit form; both persisted.
- **Sandbox setup:** `npx playwright install chromium` needed
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, because the
  package-cache proxy terminates TLS with its own CA.

## Stubs

Nothing of grain's own was stubbed or mocked. The only things missing
were outside it:

- **GitHub** is not reachable from the demo.
  `GET /api/repos/acme/widgets/github-branches` answers 404 whenever a
  base-branch field is shown. The UI treats that as "no suggestions".
  Apart from the deliberate errors listed below, it is the only console
  error on these screens.
- **CI** has no runs in the demo (rail "CI 0"), so CI links on task
  detail could only be checked for absence.
- **Live agent sessions:** none exist. The Director chat was exercised
  as far as posting a message, which is stored as a task comment. No
  agent answered.
- **Browser time zone** was set per context with Playwright's
  `timezoneId` (UTC, America/Los_Angeles, Asia/Tokyo). This stands in
  for operators sitting in other zones; nothing on the server changed.

## Steps and outcomes

### 1. Inbox

1. **Opening it:** a cold load of `/inbox`, and the rail's **Inbox**
   button from `/`, both open `/inbox`. It shows four groups with one
   task each, under a count of 4, and the rail badge reads 4:
   - Answer a question (#4)
   - Submit for merge (#6)
   - Read an answer (#7)
   - Approve a proposal (#1)
2. **Row navigation:** clicking #1's title opened the pane at `/tasks/1`.
   Back returned to `/inbox`.
3. **Decline confirmation:** **Decline** on #1 asked "Close 1 without
   running it? It can be reopened.". Dismissing it left #1 `proposed`.
4. **Approve:** **Approve** moved #1 to `queued`, and the proposal
   group disappeared.
5. **Reply:**
   - **Reply** on #4 showed the parked question ("Two PRs are both stuck
     failing the same lint check…") and focused the "Reply to 4" box.
   - Send stays disabled on an empty or whitespace-only box (yesterday's
     fix).
   - A typed draft survived a 3-second poll.
   - The sent reply was stored as a comment by `operator`
     (`authorKind: human`), and #4 became `queued`.
   - The question group left, and after a reload the count and badge
     read 2.
6. **Emptying it:** **Submit** (#6 → `completed`) and **Acknowledge**
   (#7 → `closed`) emptied the inbox.
   - The page then reads "Nothing is waiting on you. Every task is
     either running, queued or done.", with a count of 0.
   - The rail badge disappears.
7. **Keyboard:** Tab from page start reaches "+ New task", Create
   options, every rail destination, Admin, Metrics and User settings,
   then wraps through the document. Each inbox row's link and buttons
   are reachable while the inbox has rows.

**Result:** pass.

### 2. New task form (`/new` and the rail's "+ New task")

1. **Validation:**
   - Create is disabled with an empty or whitespace-only title, and
     until a repo (or "No repo") is chosen.
   - A vCPU of `-2` is blocked by native validation ("Value must be
     greater than or equal to 0."). `1.5` is blocked too ("…The two
     nearest valid values are 1 and 2.").
2. **Queued path:** task #13 was filed `queued`. Every field below
   persisted, as checked through `GET /api/tasks/13`:
   - **Text:** the title, and a Markdown description.
   - **Custom fields:** priority high, requested-by Contoso, due
     2026-11-15, estimate 3, customer-facing Yes.
   - **Repo:** `acme/widgets`, base `release-1.2`, and auto-merge (on by
     default).
   - **Don't port** ticked → `noPort: true`.
   - **Capabilities:** GCP key unticked, leaving `gemini-key,
     self-debug`.
   - **Model:** `gemini-2.5-pro` → `agentFramework: antigravity`.
   - **Advanced options:** Network "None (this deployment only)"
     (`networkAccess: none`), "When the agent finds nothing to do →
     Leave for acknowledgement" (`nothingNeededAction: ack`), prompt
     extension "Be terse.", and 2 vCPU / 4096 MiB / 30 GiB.
3. **Area (multi-select):** a separate proposal (#14) picked
   *frontend* and *backend*. Both persisted
   (`area: ["backend","frontend"]`). The first #13 attempt is noted
   under Observations.
4. **Draft, cancel and discard:**
   - **Close dialog** filed nothing and kept the draft, and reopening
     the form restored it.
   - **Discard draft** cleared the title and repo.
   - Escape after picking an Area option closes the whole form. The
     popup has already closed by then, so Escape reaches the dialog.
     The draft is kept. This is unchanged since earlier reports.
5. **Proposed path:** "No repo" with "Queue immediately" unticked filed
   #14 `proposed`, with no repo. It appeared in the Inbox's proposal
   group with Approve/Defer/Decline.
6. **Repo typed under "Other…":** a malformed name (`not a repo`) is
   refused with a 400. The message is announced as an alert ("repo must
   be owner/name, got "not a repo""), and the form stays open.

**Result:** pass.

### 3. All tasks, state views, Blocked, search, sort, filters

1. **State views:** every rail state has its own URL and the expected
   rows:
   - Proposed → #14
   - Queued → 1, 2, 4, 11, 12, 13
   - Running → 3, 8
   - Awaiting submit → 6, and Awaiting ack → 7
   - Queued for merge → 5
   - Deferred → 9, and Closed → 10
   - Blocked → 11

   Awaiting reply is left off the rail once its count is 0. A reload of
   Blocked kept it. Back went to Closed, and Forward returned to Blocked.
2. **Search:**
   - "pagination" in Queued narrowed the list to #11 and wrote `q=`.
     Reloading kept both the URL and the search box.
   - A search that matches nothing shows "No tasks match your search.".
   - Clearing the search removed `q=`.
3. **Sort:**
   - **Backlog order → Sort by… → Priority** wrote
     `sort=custom.priority:asc` and ordered the list low → high, with
     unset last.
   - Direction "Last to first" wrote `:desc` and ordered high → low,
     unset still last.
   - A deep link to the sorted URL gave the same order.
   - The sort carried into the Queued view.
4. **Compound narrowing:**
   - Queued, plus **Filters → Add filter → Priority → is any of →
     high**, narrowed the list to 2, 4, 13. Adding the search "flaky"
     narrowed it to #2 at
     `/?state=queued&q=flaky&sort=custom.priority:desc&f=kind:none:interactive&f=custom.priority:any:high`.
   - A trip to Closed (which keeps the search and filter, and so shows
     nothing) and Back returned to that exact URL, with the search box
     refilled. Reload kept it, chips included.
5. **Empty view:** an empty Proposed view reads "No tasks match these
   filters.".

**Result:** pass.

### 4. Task detail

1. **Entry points:**
   - an Inbox row title;
   - a list row title in the Proposed view;
   - a deep link (`/tasks/14`, and others);
   - Back from each returned to where it came from.
   - `/tasks/999` shows the alert "no task 999" and falls back to `/`.
2. **Editing #14 (no-repo proposal):**
   - **Edit → Cancel** discarded a typed title.
   - **Edit → Save:** title, description, Model `gemini-2.5-flash` and
     "When the agent finds nothing to do → Leave for acknowledgement"
     all persisted. The timeline gained the "This task was just edited…"
     note.
   - **Repo:** `—` → `acme/widgets` persisted.
   - **Base branch:** `release-9` persisted on Enter, and `release-10`
     on blur.
   - **Edit fields → Save fields:** requested-by Fabrikam,
     customer-facing No.
   - **Auto-merge:** the box sends `PATCH {"autoMerge": …}` at once and
     the result survives a reload. The same held right after Save fields,
     and after the whole edit sequence was replayed on a fresh task.
     (The box redraws only once the server answers, so a test that reads
     it in the same tick sees the old value.)
3. **Lifecycle on #14:**
   - **Defer** with no date → `deferred`; **Undefer** → `proposed`.
   - Defer until 2026-12-31 → "Picks itself back up on 12/31/2026,
     12:00:00 AM, back into the proposals for someone to approve."
     (`deferredUntil` 2026-12-31T00:00:00Z). The note survived a reload.
   - Picked it back up → `proposed`, with the date cleared.
   - A past date (2020-01-01) was refused with a visible alert ("wake
     time … is not in the future…"), and the task stayed `proposed`.
   - **Approve** → `queued`, and **Withdraw approval** replaced
     Approve.
   - **Bug 1:** the same date picked from a browser in another time zone
     was stored as a different day (see Bugs).
4. **Attempts, transcripts and links:**
   - #3 (running) shows "Live Attempt #1 Transcript" with its streamed
     transcript.
   - #8 (repairing) shows the merge-queue repair note, "Attempt #2 ·
     Running" and "Still running -- nothing written yet.".
   - #5 and #8 link `acme/widgets#101` and `#104` to
     `https://github.com/acme/widgets/pull/…`.
   - There are no CI links, because no CI runs are seeded.
5. **Chat (`/session/director`):** the composer is reachable as textbox
   "Message". A message sent with Enter became a comment by `operator`
   on #12, and it was still shown after a reload.

**Result:** fail on the commit tested (bug 1); pass after the fix.

### 5. Selection and bulk edit

1. **Batch actions** with #15 and #16 selected (Proposed view):
   - The bar shows "2 selected".
   - **Close** asks "Close 2 task(s)?". Dismissing it changed nothing
     and kept the selection.
   - **Defer** deferred both and cleared the selection. Under Deferred,
     **Undefer** brought both back to `proposed`. **Approve** on #16
     alone queued it.
2. **Bulk edit:**
   - Fields the two tasks disagree on are marked "(mixed across the
     selection)", spaced correctly in Chromium's accessible names
     (yesterday's fix).
   - **Apply** stays disabled until a field is ticked.
   - **Cancel** left both tasks untouched and kept the selection.
   - Ticking Base branch (`release-2`), Auto-merge (left at its shown
     "Off") and Priority (low), then **Apply to 2 tasks**, sent one
     PATCH per task with exactly those fields. Other custom fields were
     preserved (#15 kept requested-by and customer-facing). The selection
     cleared.
3. **Keyboard:** Space on a focused row checkbox selects it. Escape does
   not clear a selection; **Clear selection** does.

**Result:** pass.

### Console and network

- No page errors and no failed requests.
- The only console errors are the GitHub branch-list 404 above, and the
  deliberate errors:
  - the malformed repo, a 400 on `POST /api/tasks`;
  - the past defer date, a 400 on `POST /api/tasks/14/defer`;
  - the unknown task, a 404 on `GET /api/tasks/999`.
- Chromium reports each 3-second `/api/tasks` poll answered
  `304 Not Modified` as `net::ERR_ABORTED`. This is expected, and the
  scripts filtered it out.

## Bugs found and fixed on this branch

### 1. A defer date picked east of the deployment woke a day early

**Reproduction** (the demo runs in `UTC`):

1. Open a browser whose time zone is `Asia/Tokyo` and open a proposed
   task, e.g. `/tasks/16`.
2. Type `2026-12-31` into **Until (optional)** and press **Defer**.

**Outcome:**

- **Before:** the task is stored with `deferredUntil`
  2026-12-30T15:00:00Z, and the pane says "Picks itself back up on
  12/30/2026, 3:00:00 PM". The operator picked the 31st and is told the
  30th.
  - From `America/Los_Angeles` the same pick was stored as
    2026-12-31T08:00:00Z and shown as "12/31/2026, 8:00:00 AM".
  - Picking *today* from Los Angeles (3 October there, 4 October in
    UTC) was accepted, as midnight Los Angeles time. From UTC or Tokyo
    the same pick is refused as in the past.
  - The field's `min` was "tomorrow" in the browser's zone: 10-04 in
    Los Angeles, 10-05 elsewhere.
- **Cause:** `Actions.jsx` sent `new Date(`${day}T00:00`)`, which is
  midnight where the browser is. The note under it prints with
  `formatDateTime(…, zone)`, in the deployment's zone from
  `TimeZoneContext`. The two halves of the same field used different
  zones.
- **Fix:**
  - The day is converted with `taskFilters.dayStart(day, zone)`, the
    zone-aware midnight the date filters already use.
  - `earliestWakeDate` reckons "tomorrow" in the same zone.
  - The helper text names the zone ("…at the start of this day, UTC.").
  - With no zone configured nothing changes: the browser's own zone is
    used, as before.
- **After** (rebuilt server, fresh store): from UTC, Los Angeles and
  Tokyo alike, a pick of 2026-12-31 is stored as 2026-12-31T00:00:00Z
  and shown as "Picks itself back up on 12/31/2026, 12:00:00 AM". The
  `min` is 2026-10-05 in all three. Reload keeps it.
- **Coverage:**
  - `Actions.test.jsx`: "reads a wake date in the deployment's zone, the
    one its note prints it in" fakes the clock at 2026-10-04T03:00Z
    under an `Asia/Tokyo` provider. It checks the `min` and the instant
    sent, and fails on the old code. A second case checks the note
    prints that instant as the picked day.
  - The e2e test "from a browser in another time zone › defers to the
    start of the picked day in the deployment's zone" runs in an
    `Asia/Tokyo` browser against the demo. It failed against the
    unfixed build ("…on 12/30/2030, 3:00:00 PM…") and passes with the
    fix.

## Observations, not changed

- **Restored drafts and multi-selects:** a restored New task draft keeps
  its Area choices. Clicking an option that is already chosen unchooses
  it, so a script (or person) re-picking "frontend" on a restored draft
  files the task without it. This is ordinary toggle behaviour; it is
  noted because it looked like data loss at first.
- **"Other…" is not restored:** a restored draft does not keep an
  "Other…" repo choice. The repo picker comes back at "—".
- **New task → No repo:** Base branch and Auto-merge stay editable, and
  a no-repo proposal is stored with `autoMerge: true`. Bulk edit also
  sets a base branch on a no-repo task. Unchanged and harmless.
- **Bulk edit vs the form:** Bulk edit still offers "Agent framework",
  which the New task form no longer shows. It does not offer Network
  access, "When the agent finds nothing to do", Don't port, or the
  sandbox shape. The pane's edit form also has no Network access or
  sandbox shape.
- **Bulk edit's mixed Auto-merge** defaults to "Off" once ticked, so
  ticking it without looking turns auto-merge off everywhere. It is
  shown, so this is a choice, not a bug.
- **Empty state wording:** an empty state view says "No tasks match
  these filters." even when the only filter is the default "Kind is none
  of Interactive", and the state is simply empty.
- **Closed keeps the search:** moving from a narrowed Queued view to
  Closed carries `q=` and the filters with it. That is how Back can
  restore them, but a Closed view that shows nothing for that reason
  says only "No tasks match…".

## Tests run

- `npx vitest run src/components/detail src/taskFilters src/time`: 17
  files, 274 tests, all passed. The new zone test was also run against
  the unfixed `Actions.jsx` (with `TZ=America/Los_Angeles`), where it
  failed as expected.
- `npx playwright test --project=chromium` over
  `task_intake_inbox_lifecycle_walkthrough`, `inbox`,
  `batch_actions_workflows`, `tasks` and `task_detail_workflows`: 22
  tests, all passed against the rebuilt frontend. The new e2e case
  failed against the stale (unfixed) build first.
