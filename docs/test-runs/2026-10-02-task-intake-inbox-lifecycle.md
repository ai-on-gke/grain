# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-02

## What was tested

- **Commit:** `cf9d6156c` (tip of `candidate`; `grain/task-1530` branched
  from here). Every finding below was reproduced on this commit. The
  fixes on this branch were then re-checked in the same browser, against
  a rebuilt server and frontend.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64 x86_64.
  - Go 1.26.6, Node.js 20.19.0.
  - Playwright 1.62.1 with headless Chromium (chromium-headless-shell
    1234).
  - Viewport 1400×900, browser time zone UTC.
- **Server:**
  - The real `grain demo -addr 127.0.0.1:18080 -open=false`, built from
    this tree (`go build ./cmd/grain`).
  - It ran over a throwaway embedded SQLite store under a scratch
    `TMPDIR`/`HOME`. That store was deleted and re-seeded each time the
    binary was rebuilt, so task ids for tasks filed during the
    walkthrough restart at 13 after each rebuild.
  - The frontend was the production build (`npm run build` into
    `pkg/ui/static`, embedded in the binary).
- **Driver:**
  - Ad-hoc Playwright scripts, kept outside the repository and not
    committed. They drove visible controls: clicks, typing, keyboard,
    Back/Forward, reload and deep links. They found controls by role and
    accessible name wherever the page offered one.
  - The REST API was only read, to *confirm* persisted state after a UI
    action. It was never used to perform one.
  - Every script logged browser console errors and warnings, page
    errors, failed requests and HTTP ≥ 400 responses.
- **Account:** created through the first-run "Create account" dialog
  (`operator`). The session was reused through Playwright storage state.
- **Gemini key:** `/home/debian/.gemini-api-key` was not present in this
  sandbox, so nothing model-backed was exercised. The demo needs no
  model. The Model picker does list the Gemini 2.5 Pro backend.
- **Sandbox setup:** `npx playwright install chromium` failed with
  `SELF_SIGNED_CERT_IN_CHAIN`, because the package-cache proxy terminates
  TLS with its own CA. Re-running it with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` worked.

## Stubs

None of grain's own components were stubbed or mocked. The only things
missing were outside it:

- **GitHub** is not reachable from the demo.
  `GET /api/repos/acme/widgets/github-branches` answers 404 whenever a
  base-branch field is shown. The UI treats that as "no suggestions".
  It is the only console error seen on these screens, apart from the
  deliberate 400s listed below.
- **CI** has no runs in the demo (rail "CI 0"), so the CI links on task
  detail could only be checked for absence.
- **Live agent sessions:** none exist. The Director chat was exercised
  as far as posting a message, which is stored as a task comment. No
  agent answered.

## Steps and outcomes

### 1. Inbox

1. **Opening it:** cold-loaded `/inbox`, and also opened it from the
   rail's **Inbox** button on `/`. Both open `/inbox` with four groups,
   one task each. The rail badge reads 4.
   - Answer a question (#4)
   - Submit for merge (#6)
   - Read an answer (#7)
   - Approve a proposal (#1)
2. **Row navigation:** clicking #1's title opened its pane at `/tasks/1`.
   Back returned to `/inbox`.
3. **Decline confirmation:** **Decline** on #1 asked "Close 1 without
   running it? It can be reopened.". Dismissing it left #1 `proposed`.
4. **Approve:** **Approve** moved #1 to `queued`, and it left the
   proposal group.
5. **Reply:** **Reply** on #4 showed the parked question ("Two PRs are
   both stuck…") and focused the "Reply to 4" box. After a reply was sent,
   #4 left the group and became `queued`. The comment is stored with
   author `operator`, kind `human`.
6. **Emptying it:**
   - After a reload the rail badge read 2.
   - **Submit** (#6 → `completed`) and **Acknowledge** (#7 → `closed`)
     emptied the inbox.
   - The page then reads "Nothing is waiting on you. Every task is either
     running, queued or done.", and the rail badge disappears.
7. **Keyboard:** Tab from page start reaches "+ New task", every rail
   destination, then each inbox row's number, title and action buttons
   in order.

**Result: pass.**

### 2. New task form (`/new` and the rail's "+ New task")

1. **Validation:**
   - Create is disabled with an empty or whitespace-only title, and
     until a repo (or "No repo") is chosen.
   - A vCPU of `-2` is blocked by native constraint validation ("Value
     must be greater than or equal to 0."), which focuses the field.
2. **Queued path:** task #13 was filed `queued`, and every field below
   persisted.
   - Title, and a Markdown description.
   - Priority high, Area frontend, requested-by Contoso, due
     2026-11-15, estimate 3, customer-facing Yes.
   - Repo `acme/widgets`, base `release-1.2`, auto-merge (on by default).
   - Capabilities with GCP key unticked.
   - **Advanced options:** agent framework `claude`, network `none`, a
     prompt extension, and 2 vCPU / 4096 MiB / 30 GiB.
3. **Draft, cancel and discard:**
   - **Close dialog** filed nothing and kept the draft, and reopening
     the form restored it.
   - **Discard draft** cleared it, including after a reload.
4. **Proposed path:** "No repo" with "Queue immediately" unticked. Task
   #14 was `proposed` with no repo, and appeared in the Inbox's proposal
   group.
5. **Repo typed under "Other…":**
   - A malformed name ("not a repo") enables Create. The server answers
     `400 repo must be owner/name, got "not a repo"`, and the form stays
     open.
   - **Bug 1:** that error was drawn on screen, but the accessibility tree
     did not contain it (see Bugs).
   - The config repo `grain/config-default`, which the picker offers, is
     accepted.

**Result: fail on the commit tested (bug 1); pass after the fix.**

### 3. All tasks, state views, Blocked, search, sort, filters

1. **State views:** clicked each rail state (Proposed, Queued, Running,
   Awaiting reply/submit/ack, Queued for merge, Deferred, Closed,
   Blocked).
   - Each view has its own URL (`/?state=…`), and the expected rows: for
     example Running → #3 and #8, and Blocked → #11.
   - Reloading Blocked kept it.
   - Back went Closed → Deferred, and Forward returned to Closed.
2. **Search:** "pagination" in Queued narrowed to #11 and wrote `q=`. A
   reload kept both the URL and the search box. Clearing it removed
   `q=`. A search with no match shows "No tasks match your search.".
3. **Sort:** **Sort by… → Priority** wrote `sort=custom.priority:asc`
   and ordered low → medium → high, with unset last. Last-to-first
   wrote `:desc` and ordered high → low, with unset still last. A deep
   link to the sorted URL reproduced the order. The sort carries over
   into a state view.
4. **Compound narrowing:**
   - Set: Queued, plus **Add filter → Priority is any of high**, plus
     search "flaky". The list narrowed to #2, with
     `/?state=queued&q=flaky&f=kind:none:interactive&f=custom.priority:any:high`.
   - A trip to Closed and Back returned to that exact URL, as did a
     reload and Forward/Back.
   - Opening #2 and pressing Escape returned to the same narrowed URL.
   - A hand-written deep link with the same conditions gave the same
     list.
5. **Keyboard:** focusing a row and pressing Enter opened it
   (`/tasks/1`).

**Result: pass.**

### 4. Task detail

1. **Entry points:** an Inbox row, a list row (click and Enter), and deep
   links to `/tasks/1`, `/tasks/3`, `/tasks/5`, `/tasks/8` and
   `/tasks/12`. `/tasks/999` falls back to `/` with "no task 999".
2. **Edits on #1**, each checked through the API:
   - **Edit → Save:** title and Markdown description persisted, and
     survived a reload.
   - **Escape while editing:** closes the pane and discards the edit
     (unchanged since the last report).
   - **Base branch:** persisted on both Enter (`release-9`) and blur
     (`release-10`).
   - **Repo → Other… → `acme/gadgets`:** refused with a 400 explaining
     that the repo is not on the target repo list. The message is shown
     (and, after fix 1, announced). The demo's only target repo is
     `acme/widgets`, so the repo could not actually be changed here.
   - **Edit fields → Save fields:** requested-by Fabrikam, customer-facing
     No. The read-only rows show "No".
   - **Auto-merge:** toggled `false → true`, and survived a reload.
3. **Lifecycle on #1:**
   - **Defer** with no date → `deferred`; **Undefer** → `proposed`.
   - Defer until 2026-12-31 → "Picks itself back up on 12/31/2026,
     12:00:00 AM.", with `deferredUntil` 2026-12-31T00:00:00Z. This
     survived a reload.
   - Picked it back up → `proposed`, with the date cleared.
   - A past date (2020-01-01) was refused with a visible error ("wake
     time … is not in the future"), and the task stayed `proposed`.
   - **Approve** → `queued`. Approve is then replaced by **Withdraw
     approval**.
   - The transition history keeps no entry for a deferral that has ended.
     This is documented in `model.Transitions`, so it was not filed as a
     bug.
4. **Attempts, transcripts and links:**
   - #3 (running) shows its live attempt #1 transcript inline.
   - #8 (repairing) shows the merge-queue repair note and attempt #2
     ("Still running -- nothing written yet.").
   - #5 and #8 link `acme/widgets#101` and `#104` to
     `https://github.com/acme/widgets/pull/…`.
   - There are no CI links, because no CI runs are seeded.
5. **Chat (`/sessions` → Director, `/session/director`):**
   - The transcript renders.
   - A message typed into the composer and sent became a comment by
     `operator` on #12. The composer cleared, and the message was still
     shown after a reload.
   - **Bug 3:** the composer had no role and no name (see Bugs).
6. **Bug 2:** the task pane is a modal dialog with no accessible name
   (see Bugs).

**Result: fail on the commit tested (bugs 2 and 3); pass after the fixes.**

### 5. Selection and bulk edit

1. **Selecting:** in Queued, ticked #2 and #11 → "2 selected" bar.
   - The selection survives switching to Closed and Back, which is
     deliberate.
   - It does not survive a reload.
2. **Cancel:**
   - Opened **Bulk edit…**, ticked Requested by and typed a value, then
     pressed **Cancel**. The dialog closed, both rows stayed ticked, and
     nothing changed (checked through the API).
   - Escape behaves the same way.
3. **Labels:**
   - Fields that differ are labelled "(mixed across the selection)".
   - Apply is disabled until a field is ticked.
   - **Bug 2** also applies here: the Bulk edit dialog was unnamed, and so
     was the bar's capability picker.
4. **Apply:** set Priority = high, Base branch = `release-2.0` and
   Requested by = `Bulk Co`, then **Apply to 2 tasks**.
   - The dialog closed and the selection cleared.
   - Both tasks carry the new values.
   - Fields that were not ticked kept their values (area; #11's estimate
     3).
   - After a reload, the rows show the new base and priority chips.

**Result: fail on the commit tested (bug 2); pass after the fix.**

### Console and network

- **On every screen:** no page errors and no console errors, apart from:
  - the GitHub branch-list 404 above;
  - the deliberate 400s: the malformed repo, the non-target repo, and
    the past defer date.
- **Polls:** Chromium reports each 3-second `/api/tasks` poll answered
  `304 Not Modified` as `net::ERR_ABORTED`. This is expected, and the
  scripts filtered it out.

## Bugs found and fixed on this branch

### 1. An error raised inside a modal was not announced

**Reproduction:**

1. Open `/new`.
2. Give it a title.
3. Under Repos, pick **Other…** and type `not a repo`.
4. Press **Create task**.

**Outcome:**

- **Before:** the red "repo must be owner/name…" snackbar is drawn on
  screen, but `getByRole("alert")` finds nothing. The snackbar sits
  inside `#root`, which the open MUI modal has marked
  `aria-hidden="true"`, so a screen reader never hears the error. The same
  happens to any error raised from a task pane (for example, the
  non-target repo and the past defer date above).
- **Fix:** `ErrorBanner.jsx` portals the snackbar to `document.body`.
  A modal that was already open does not hide that node.
- **Coverage:**
  - `ErrorBanner.test.jsx`, "stays in the accessibility tree while a
    modal hides the app". It fails on the old component.
  - The e2e test "names the intake modals and announces an error raised
    in one".

### 2. Task pane, Bulk edit dialog and batch capability picker had no name

**Reproduction:** open `/tasks/1`, or select a row and press **Bulk
edit…**. The accessibility tree shows a bare `dialog` with no name. The
batch bar's capability `<select>` is named only by its "Capability…"
placeholder option.

- **Cause:** `Overlay.jsx` wraps MUI's Dialog, which takes no name from
  the heading drawn inside it.
- **Fix:**
  - `Overlay` takes a `label` and sets it on the paper, which is the
    element carrying `role="dialog"`.
  - `DetailOverlay` passes "Task <id>: <title>". Unlike a heading id,
    this still names the dialog while the edit form has replaced the
    title heading.
  - `BulkEditOverlay` passes "Bulk edit".
  - The capability picker gets `aria-label="Capability to attach or
    detach"`.
  - Other `Overlay` callers (Settings, schedules, templates, suites…) are
    outside this walkthrough's area. They can opt in with the same prop.
- **Coverage:** new cases in `Overlay.test.jsx`, `DetailOverlay.test.jsx`
  and `BatchActionsBar.test.jsx`, all of which fail without the change,
  plus the e2e test above.

### 3. The chat composer had no role or name

**Reproduction:** open `/session/director`. `getByRole("textbox")` finds
nothing. The composer is chatscope's bare `contenteditable` div, and
`MessageInput` forwards no props to it.

- **Fix:** `TaskChat.jsx` sets `role="textbox"`, `aria-multiline` and
  `aria-label="Message"` on the editor it renders, once it is mounted.
- **Coverage:** `TaskChat.test.jsx`'s composer test now reaches the box
  by role and name, and fails without the change. Also covered by the
  e2e test above.

## Observations, not changed

- **Task rows:** each list row is a `role="link"` div (the keyboard-open
  row from `c1d899b68`) that contains a button, a checkbox and a real
  link. This is the "nested interactive" ARIA pattern: screen readers
  read the whole row as one link, with its controls inside. Similarly, the
  task timeline's attempt entry is a `role="button"` placed directly in
  a `list`. A fix means rethinking how a row opens from the keyboard,
  which is beyond a scoped change here, so it is proposed as a
  follow-up.
- **New task → Area:** after an Area option is clicked its popup has
  closed, so a following Escape closes the whole New task form. The
  draft is kept.
- **Inbox → Reply:** Send is enabled with an empty box and does nothing.
  This is unchanged since the last report.
- **New task → No repo:** Base branch and Auto-merge stay editable.
  This is unchanged and harmless.
- **Filter values:** the Priority filter's values list only the values
  present among the tasks in view (low / high / not set in Queued).

## Tests run

- `npx vitest run`: 158 files, 2746 tests, all passed.
- `npx playwright test --project=chromium` on
  `task_intake_inbox_lifecycle_walkthrough`, `inbox`, `director`,
  `chat-theme`, `batch_actions_workflows`, `task_detail_workflows`,
  `tasks` and `error-boundaries`: 25 tests. 24 passed on the first run.
  The new case failed on an ambiguous placeholder selector in the test
  itself; once that was corrected, the whole walkthrough spec (4 tests)
  passed.
- **Grain CI:**
  - The first full run (`d4fffaeb792297cd`) failed `ui e2e` on the new
    case. In CI, that worker's store already held tasks other specs had
    filed, so "Select 2" also matched "Select 20" onward.
  - The case now names the task pane by number and selects the first
    listed row.
  - The full suite then passed on `6d483fc` (run `60556ee9161f7972`):
    frontend, go, installer, lint, package cache, terraform and ui e2e.
