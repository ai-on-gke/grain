# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-03

## What was tested

- **Commit:** `57632f61f` (tip of `candidate`; `grain/task-1625` branched
  from it). Every finding below was reproduced on this commit. The fixes
  on this branch were then re-checked in the same browser, against a
  rebuilt frontend and server over a freshly seeded store.
- **Since the last walkthrough (2026-10-02, `cf9d6156c`):** the one
  change inside this area is the new per-task "When the agent finds
  nothing to do" choice (`71babe8c5`), on the New task form and on the
  task pane's edit form. It was exercised below.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64 x86_64.
  - Go 1.26.6, Node.js 20.19.0.
  - Playwright 1.62.1 with headless Chromium (chromium-headless-shell
    1234).
  - Viewport 1400×900, browser time zone UTC.
- **Server:**
  - The real `grain demo -addr 127.0.0.1:18080 -open=false`, built from
    this tree (`npm run build` into `pkg/ui/static`, then
    `go build ./cmd/grain`).
  - It ran over a throwaway embedded SQLite store under a scratch
    `HOME`/`TMPDIR` (`/tmp/demo`). That directory was deleted and the
    store re-seeded when the binary was rebuilt with the fixes, so ids
    filed during the walkthrough restart at 13.
- **Driver:**
  - Ad-hoc Playwright scripts kept outside the repository (`/tmp/wt`)
    and not committed. They drove visible controls by role and
    accessible name: clicks, typing, keyboard, Back/Forward, reload and
    deep links.
  - The REST API was only read, to confirm persisted state after a UI
    action. It was never used to perform one.
  - Every script logged console errors and warnings, page errors, failed
    requests and HTTP ≥ 400 responses.
- **Account:** created through the first-run "Create account" dialog
  (`operator`). The session was reused through Playwright storage state.
- **Gemini key:** `/home/debian/.gemini-api-key` was not present in this
  sandbox, so nothing model-backed was exercised. The demo needs no
  model. The Model picker lists the Gemini 2.5 Pro backend, and it was
  picked on the New task form and in the task pane's edit form (both
  persisted `agentBackendId: gemini-pro`).
- **Sandbox setup:** `npx playwright install chromium` needed
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, because the
  package-cache proxy terminates TLS with its own CA.

## Stubs

Nothing of grain's own was stubbed or mocked. The only things missing
were outside it:

- **GitHub** is not reachable from the demo.
  `GET /api/repos/acme/widgets/github-branches` answers 404 whenever a
  base-branch field is shown. The UI treats that as "no suggestions".
  Apart from the deliberate 400s listed below, it is the only console
  error on these screens.
- **CI** has no runs in the demo (rail "CI 0"), so CI links on task
  detail could only be checked for absence.
- **Live agent sessions:** none exist. The Director chat was exercised
  as far as posting a message, which is stored as a task comment. No
  agent answered.

## Steps and outcomes

### 1. Inbox

1. **Opening it:** a cold load of `/inbox`, and the rail's **Inbox**
   button from `/`, both open `/inbox`. It shows four groups with one
   task each, and the rail badge reads 4:
   - Answer a question (#4)
   - Submit for merge (#6)
   - Read an answer (#7)
   - Approve a proposal (#1)
2. **Row navigation:** clicking #1's title opened the pane at `/tasks/1`,
   named "Task 1: Add a dark mode toggle". Back returned to `/inbox`.
3. **Decline confirmation:** **Decline** on #1 asked "Close 1 without
   running it? It can be reopened.". Dismissing it left #1 `proposed`.
4. **Approve:** **Approve** moved #1 to `queued`, and it left the
   proposal group.
5. **Reply:** **Reply** on #4 showed the parked question ("Two PRs are
   both stuck…") and focused the "Reply to 4" box.
   - **Bug 3:** Send was enabled on the empty box (see Bugs).
   - The sent reply was stored as a comment by `operator`, kind
     `human`. #4 left the group and became `queued`.
   - After a reload the badge read 2.
6. **Inbox Defer:** a proposal filed through the form (#15) was deferred
   with the proposal row's **Defer**. It became `deferred` with no date,
   and the row left.
7. **Emptying it:** **Submit** (#6 → `completed`) and **Acknowledge**
   (#7 → `closed`) emptied the inbox.
   - The page then reads "Nothing is waiting on you. Every task is
     either running, queued or done.", with a count of 0.
   - The rail badge disappears.
8. **Keyboard:** Tab from page start reaches "+ New task", every rail
   destination, then each inbox row and its action buttons.

**Result:** pass, apart from bug 3.

### 2. New task form (`/new` and the rail's "+ New task")

1. **Validation:**
   - Create is disabled with an empty or whitespace-only title, and
     until a repo (or "No repo") is chosen.
   - A vCPU of `-2` is blocked by native validation ("Value must be
     greater than or equal to 0."), which focuses the field. `1.5` is
     blocked too ("…nearest valid values are 1 and 2").
2. **Queued path:** task #13 was filed `queued`. Every field below
   persisted, as checked through `GET /api/tasks/13`:
   - **Text:** the title, and a Markdown description.
   - **Custom fields:** priority high, area frontend, requested-by
     Contoso, due 2026-11-15, estimate 3, customer-facing Yes.
   - **Repo:** `acme/widgets`, base `release-1.2`, and auto-merge (on by
     default).
   - **Capabilities:** GCP key unticked, leaving `gemini-key,
     self-debug`.
   - **Advanced options:**
     - Model Gemini 2.5 Pro. This disables the Agent framework picker,
       which then shows the model's framework (Antigravity).
     - Network `none`.
     - **When the agent finds nothing to do → Leave for
       acknowledgement**, persisted as `nothingNeededAction: ack`.
     - Prompt extension "Be terse.".
     - 2 vCPU / 4096 MiB / 30 GiB.
3. **Draft, cancel and discard:**
   - **Close dialog** filed nothing and kept the draft, and reopening
     the form restored it.
   - **Discard draft** cleared the title.
   - Escape after picking an Area option closes the whole form, because
     the popup has already closed. The draft is kept, and the task
     filed afterwards still carried `area: frontend`. This is unchanged
     since the last report.
4. **Proposed path:** "No repo" with "Queue immediately" unticked filed
   #14 `proposed`, with no repo. It appeared in the Inbox's proposal
   group.
5. **Repo typed under "Other…":** a malformed name (`not a repo`) is
   refused with a 400. The message is announced as an alert
   ("repo must be owner/name…"), and the form stays open.

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
   - Direction "Last to first" wrote `:desc` and ordered high → low.
   - A deep link to the sorted URL gave the same order.
   - The sort carried into the Queued view.
4. **Compound narrowing:**
   - Queued, plus **Filter → Priority is any of high**, plus search
     "flaky", narrowed the list to #2 at
     `/?state=queued&q=flaky&sort=custom.priority:desc&f=kind:none:interactive&f=custom.priority:any:high`.
   - A trip to Closed and Back returned to that exact URL, with the
     search box refilled. Reload kept it.
   - Opening #2 and pressing Escape returned to it. Back reopened
     `/tasks/2`, and Forward came back to the list.
5. **Keyboard:** focusing a row and pressing Enter opened it.

**Result:** pass.

### 4. Task detail

1. **Entry points:** an Inbox row, a list row (click and Enter), and
   deep links to `/tasks/3`, `/tasks/5`, `/tasks/7`, `/tasks/8` and
   `/tasks/14`. `/tasks/999` falls back to `/` with "no task 999".
2. **Edits on #14** (the no-repo proposal), each checked through the
   API:
   - **Edit → Save:** title, Markdown description, Model → Gemini 2.5
     Pro, and **When the agent finds nothing to do → Leave for
     acknowledgement**. All persisted, and the read-only rows show the
     new choice.
   - **Repo:** `—` → `acme/widgets` persisted.
   - **Base branch:** `release-9` persisted on Enter, and `release-10`
     on blur.
   - **Edit fields → Save fields:** requested-by Fabrikam,
     customer-facing No.
   - **Auto-merge:** toggled `true → false`. It survived a reload, as
     did the heading and the base branch.
3. **Lifecycle on #14:**
   - **Defer** with no date → `deferred`; **Undefer** → `proposed`.
   - Defer until 2026-12-31 → "Picks itself back up on 12/31/2026,
     12:00:00 AM." (`deferredUntil` 2026-12-31T00:00:00Z). The date
     survived a reload.
   - Picked it back up → `proposed`, with the date cleared.
   - A past date (2020-01-01) was refused with a visible alert ("wake
     time … is not in the future…"), and the task stayed `proposed`.
   - **Approve** → `queued`, and **Withdraw approval** replaced
     Approve.
4. **Attempts, transcripts and links:**
   - #3 (running) shows "Live Attempt #1 Transcript".
   - #8 (repairing) shows the merge-queue repair note, attempt #2, and
     "Still running -- nothing written yet.".
   - #5 and #8 link `acme/widgets#101` and `#104` to
     `https://github.com/acme/widgets/pull/…`.
   - There are no CI links, because no CI runs are seeded.
5. **Chat (`/session/director`):** the composer is reachable as textbox
   "Message". A typed message sent with Enter became a comment by
   `operator` on #12, and it was still shown after a reload.

**Result:** pass.

### 5. Selection and bulk edit

1. **Batch actions:** with #13 and #14 selected (Queued view), the bar
   shows "2 selected".
   - **Close** asks "Close 2 task(s)?". Dismissing it changed nothing
     and kept the selection.
   - **Attach** Memory added `memory` to both tasks, and **Detach**
     removed it. Each action clears the selection.
   - **Approve** on already-queued tasks is a no-op with no error.
2. **Bulk edit:**
   - Fields the two tasks disagree on are marked "(mixed across the
     selection)".
   - **Bug 2:** the accessible name had no space before that note (see
     Bugs).
   - **Cancel** left both tasks untouched and kept the selection.
   - Ticking Base branch (`release-2`), Auto-merge (On) and Priority
     (low), then **Apply to 2 tasks**, updated both tasks. Their other
     custom fields were preserved, and the selection cleared. Reload
     confirmed it.
3. **Selection across views:**
   - **Select all** under Queued (7 rows), then opening Running: the bar
     still read "7 selected", with none of Running's rows ticked.
   - **Bug 1:** Close offered to close all seven, which are not on
     screen (see Bugs).
   - Escape does not clear a selection. **Clear selection** does.

**Result:** fail on the commit tested (bugs 1 and 2); pass after the
fixes.

### Console and network

- No page errors.
- The only console errors are the GitHub branch-list 404 above, and the
  deliberate 400s:
  - the malformed repo, on `POST /api/tasks`;
  - the past defer date, on `POST /api/tasks/14/defer`;
  - the unknown task, a 404 on `GET /api/tasks/999`.
- Chromium reports each 3-second `/api/tasks` poll answered
  `304 Not Modified` as `net::ERR_ABORTED`. This is expected, and the
  scripts filtered it out.

## Bugs found and fixed on this branch

### 1. A batch selection made in another view closed tasks that were not on screen

**Reproduction:**

1. Open `/?state=queued` and tick **Select all**.
2. Click **Running** on the rail.
3. Press **Close** in the batch bar.

**Outcome:**

- **Before:** the bar reads "7 selected" over a list in which nothing
  is ticked. Close asks "Close 7 task(s)?", and accepting closes seven
  queued tasks the operator cannot see.
- **Cause:** the selection is App state, and is deliberately kept
  across views (`selectedTasks` is resolved against the whole list).
  Nothing told the bar which of the selection the list was drawing.
- **Fix:**
  - `TaskList` reports the ids it is drawing (`onShownChange`), and App
    hands them to `BatchActionsBar` as `shownIds`.
  - The bar adds "N not shown here", or "none of them shown here".
  - Close's confirmation adds "N of them are not shown in this view.".
  - Keeping a selection across views is unchanged.
  - A board passes no `shownIds`, so nothing changes there.
- **After:** Running shows "3 selected · none of them shown here", and
  Close asks "Close 3 task(s)? 3 of them are not shown in this view.".
  Ticking #3 there shows "4 selected · 3 not shown here". A search that
  hides selected rows is counted the same way.
- **Coverage:**
  - `BatchActionsBar.test.jsx`: "says how much of the selection the list
    is not showing" and "warns before closing tasks that are not on
    screen".
  - `TaskList.test.jsx`: "reports the rows it is showing, and again when
    they change".
  - The e2e test "says when a batch selection is not on screen".

### 2. Bulk edit's "(mixed across the selection)" ran into the field name

**Reproduction:** select two tasks that differ in base branch, then
press **Bulk edit…**. In Chromium the checkbox's accessible name is
"Base branch(mixed across the selection)". Visually the note is spaced
only by a CSS margin.

- jsdom inserts a space here, so the unit test's `/^Base branch \(mixed/`
  never saw the problem.
- **Fix:** `BulkEditOverlay`'s `Row` puts a real space before the note.
- **Coverage:** the e2e test above asks for "Requested by (mixed across
  the selection)" by exact accessible name in Chromium.

### 3. Inbox Reply's Send was enabled on an empty box and did nothing

**Reproduction:** on `/inbox`, press **Reply** on #4, then press
**Send** without typing. Nothing happens: no request, no message. This
was noted, and left alone, in the last three reports.

- **Fix:** Send is disabled until the box holds more than whitespace.
  The box stays uncontrolled, so a draft still survives the page's
  polling re-render. That was checked in the browser across a 3-second
  poll.
- **Coverage:** `InboxPage.test.jsx`, "offers no Send until the box
  holds more than whitespace". It replaces "sends nothing when the box
  is empty", which clicked the enabled button. An e2e test was not
  added: the walkthrough spec's first test answers the only seeded
  question, and nothing in the UI can park another task.

## Observations, not changed

- **New task → No repo:** Base branch and Auto-merge stay editable, and
  a no-repo proposal is stored with `autoMerge: true`. This is
  unchanged and harmless.
- **Repo pickers differ:** the New task form offers the config repo
  `grain/config-default`, but the task pane's Repo picker offers only
  `acme/widgets` and Other…. The previous report found the config repo
  accepted on create.
- **Unknown fields are dropped:** #5's "File a task with these fields"
  link carries `field.sprint=2026-Q2`. `sprint` is not a field this demo
  declares, so the New task form drops it without saying so. The other
  fields prefill.
- **Bulk edit is missing advanced options:** Bulk edit offers Model and
  Agent framework, but not Network access, "When the agent finds nothing
  to do", or the sandbox shape.
- **Task rows:** each row is a `role="link"` div with a button,
  checkbox and link inside it. This "nested interactive" pattern was
  noted, and proposed as a follow-up, on 2026-10-02.

## Tests run

- `npx vitest run`, the four touched suites (TaskList, BatchActionsBar,
  InboxPage, BulkEditOverlay): 178 tests, all passed.
- The full `npx vitest run`: 2814 of 2815 passed.
  - The one failure was `App.narrowing.test.jsx`, "shows closed tasks
    when the rail asks for them": a `findByText` that ran out of time
    while 167 files shared four cores.
  - Run alone, that file passed three times out of three (12/12 each
    time).
- `npx playwright test --project=chromium`:
  - `task_intake_inbox_lifecycle_walkthrough`, `inbox`,
    `batch_actions_workflows` and `tasks`: 17 tests. 16 passed on the
    first run.
  - The new case failed in the test itself. Tasks filed through
    `POST /api/tasks` are proposals, not queued, so it looked in the
    wrong view.
  - After that was corrected, the whole walkthrough spec passed (5
    tests).
