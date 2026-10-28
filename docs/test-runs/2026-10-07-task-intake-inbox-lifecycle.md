# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-07

## What was tested

- **Commit:** `61604b729` (tip of `candidate`; `grain/task-2412`
  branched from it). The one finding below was reproduced on this
  commit. The fix on this branch was then re-checked in the same
  browser, against a rebuilt frontend and server.
- **Since the last walkthrough (2026-10-06, `58ae037b2`):** these changes
  in this area were the focus, as well as the full walk:
  - A **hidden tab** keeps reading the task list every 30s while inbox
    notifications are on (`61c31fa31`).
  - A team's own **per-repo default capabilities** are ticked on the New
    task form (`661aba7fd`).
  - The inbox leaves out a **read-only team's** tasks, and keeps an
    open reply (`31394cc2c`, `c668a16cb`), which was the fix from 10-06.
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64 x86_64.
  - Go 1.26.6, Node.js 20.19.0.
  - Playwright 1.62.1 with headless Chromium (chromium-headless-shell
    1234).
  - Viewport 1400×900, browser time zone UTC and locale en-US.
- **Servers:** all three are the real `grain demo`, built from this tree
  (`npm ci && npm run build` into `pkg/ui/static`, then
  `go build ./cmd/grain`). Each ran over its own throwaway embedded
  SQLite store, under a scratch `HOME`/`TMPDIR`.
  - **Main walkthrough:** `grain demo -addr 127.0.0.1:18080 -open=false`
    over `/tmp/demo`.
  - **Outage check:** `127.0.0.1:18081` over `/tmp/demo2`. It was
    stopped and restarted under an open page.
  - **Re-check:** `127.0.0.1:18082` over `/tmp/demo3`, rebuilt with the
    fix.
- **Driver:**
  - Ad-hoc Playwright scripts kept outside the repository (`/tmp/wt`)
    and not committed. They drove visible controls by role, label and
    placeholder: clicks, typing, Space, Enter, Escape, Tab order,
    Back/Forward, reload and deep links.
  - Every script logged console errors and warnings, page errors, failed
    requests, HTTP ≥ 400 responses, browser dialogs, new tabs, and the
    body of every non-GET `/api/` request.
  - The REST API was read to confirm persisted state after a UI action.
    It was written to only for the stub listed below.
- **Account:** `operator`, created through the first-run "Create account"
  dialog on each server.
- **Gemini key:** `/home/debian/.gemini-api-key` is not present in this
  sandbox, so nothing model-backed ran. The demo needs no model.
  `gemini-2.5-pro` was picked on the New task form by typing "pro", and
  persisted.
- **Sandbox setup:** `npx playwright install --with-deps chromium`
  needed `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, as on
  earlier runs.

## Stubs

Nothing of grain's own was mocked. The stubs were:

- **GitHub** is not reachable from the demo.
  `GET /api/repos/<repo>/github-branches` answers 404 whenever a
  base-branch field is shown. The UI treats that as "no suggestions".
- **The OS notification, the permission prompt, and page visibility.**
  One script replaced `window.Notification` with a recorder:
  `permission` is `granted`, `requestPermission()` resolves `granted`,
  and constructed notifications are kept in an array. It also overrode
  `document.visibilityState`, `document.hidden` and `document.hasFocus()`
  so the tab could be marked hidden, and dispatched `visibilitychange`.
  grain's own poll, inbox diff and click handler ran unmodified.
- **An agent filing a proposal:** for the hidden-tab check, the new
  arrival (#14) was filed with `POST /api/tasks` from outside the
  browser, the way an agent's `propose_task` arrives.
- **Live agent sessions and CI:** none exist. The transcripts on #3 and
  #8 are the demo's seeded data. No CI runs are seeded (rail "CI 0"), so
  CI links on task detail could only be checked for absence.

## Steps and outcomes

### 1. Inbox

1. **Opening it:** a cold load of `/inbox`, and the rail's **Inbox**
   button, both open `/inbox`. It shows four groups with one task each,
   and the rail badge reads 4:
   - Answer a question (#4)
   - Submit for merge (#6)
   - Read an answer (#7)
   - Approve a proposal (#1)
2. **Row navigation:** clicking #1's title opened `/tasks/1` ("1 Add a
   dark mode toggle"), and Back returned to `/inbox`.
3. **Decline (dismissed):** **Decline** on #1 asks "Close 1 without
   running it? It can be reopened." Dismissing it left #1 `proposed`.
4. **Approve:** **Approve** on #1 sent `POST /api/tasks/1/approve`. #1
   became `queued` and its row left the group.
5. **Reply:**
   - **Reply** on #4 shows the parked question ("Two PRs are both stuck
     failing the same lint check…") and focuses the "Reply to 4" box.
   - Send is disabled while the box is empty.
   - A draft survived a 4-second poll.
   - Send stored the reply as a comment by `operator`, and #4 became
     `queued`. After a reload only Submit for merge and Read an answer
     were left.
6. **Hidden-tab notifications (new, `61c31fa31`):**
   - With permission granted, notifications are already on: the header
     reads **Turn off notifications**.
   - With the tab marked hidden, a proposal filed from outside (#14)
     raised exactly one notification after 28s: "14 was proposed and
     needs approval", body "Agent proposal while hidden 2", tag
     `task:14:proposed`. The hidden tab made one `GET /api/tasks` in
     that time.
   - Clicking the notification opened `/tasks/14`.
   - **Turn off notifications** survives a reload (**Turn on
     notifications**).
   - The driver's first attempt clicked the button assuming notifications
     were off, which turned them off. With them off, a proposal filed
     while hidden (#13) raised nothing in 35s, and the hidden tab read
     nothing. That is the intended quiet path.
   - With the browser's real `denied` permission the header reads
     "Notifications blocked".
7. **Emptying it:**
   - **Defer** on proposal #14 deferred it at once
     (`POST /api/tasks/14/defer`), with no dialog.
   - **Decline** on #16, with the confirm accepted, closed it.
   - **Acknowledge** on #7 closed it (`POST /api/tasks/7/close`).
   - **Keyboard:** from page start, Tab walks "+ New task", Create
     options, Customize pinned items, the rail, User settings, then the
     row title and its **Submit**. Enter on Submit submitted #6
     (`completed`).
   - The page then reads "Nothing is waiting on you. Every task is
     either running, queued or done.", with a count of 0. The rail badge
     is gone, also after a reload.

**Result:** pass.

### 2. New task form (`/new`, the rail's "+ New task", and a repo page)

1. **Team repo default capabilities (new, `661aba7fd`):**
   - On `/teams/default/repos/acme/widgets`, **Memory** was added under
     "Default capabilities for default"
     (`PUT …/config {"defaultCapabilities":["memory"]}`). It survived a
     reload, and `/api/config` reports
     `teamRepoDefaultCapabilities: {default: {acme/widgets: [memory]}}`.
   - **New task** from that page (`/teams/default/new`) ticks Self debug,
     GCP key, Gemini key and Memory.
   - The global form ticks only the first three until `acme/widgets` is
     picked, then adds Memory. The queued task #15 was filed with
     `memory`.
2. **Validation:**
   - Create is disabled with no title, with a whitespace-only title, and
     until a repo (or "No repo") is chosen.
   - A Disk of −5 GiB is refused by the browser's own validation
     ("Value must be greater than or equal to 0."). Nothing is sent, and
     the form stays open. With 30 the task was filed.
3. **Queued path (#15):** the `POST /api/tasks` body and the stored task
   carry:
   - **Text:** the title, and a Markdown description with a fenced
     block.
   - **Custom fields:** priority high, area frontend + docs,
     requested-by Contoso, due 2026-11-15, estimate 3, customer-facing
     true.
   - **Repo:** `acme/widgets`, base `release-2.0`, `autoMerge: false`.
   - **Model:** `gemini-2.5-pro` (typed "pro").
   - **Advanced options:** network `none`, nothing-needed `ack`, prompt
     extension "Be terse.", and 2 vCPU / 4096 MiB / 30 GiB.
   - The task was `queued`, `approved: true`. The form closed onto `/`.
4. **Proposed path (#16):** "No repo", "Queue immediately" unticked, and
   the same fields. It was stored `proposed` with no repo. As on earlier
   runs, Base branch and Auto-merge stay visible after "No repo", and the
   proposal was stored with `autoMerge: true`.
5. **Capabilities:** unticking **Self debug** in the Capabilities picker
   left `gcp-key, gemini-key, memory`. Escape inside the picker closed
   only the picker, not the form.
6. **Cancel and draft:**
   - **Close dialog** filed nothing (16 tasks before and after).
   - Reopening restored the title and the unticked capability.
   - **Discard draft** empties the form, which stays open. Escape closes
     the form.
7. **Depends on:** typing "walkthrough queued" in "Search tasks to depend
   on…" offered #15. #17 (a title with `—`, `ü` and `✓`) was filed
   with `dependsOn: ["15"]`, and is listed under **Blocked**. Its base
   was prefilled `release-5` from the repo's newest task
   (`lastBaseForRepo`), which is shown in the field before filing.
8. **Off-list repo (Bug 1, fixed):** Repos → Other… → `acme/gizmos` filed
   #18 with no warning. The server parked it `awaiting_reply`. See
   below.

**Result:** pass after Bug 1.

### 3. All tasks, state views, search, sort, narrowing

1. **Search:** "walkthrough" in "Search tasks…" writes `q=walkthrough`
   and leaves #15 and #16.
2. **Sort:** **Backlog order → Sort by… → Priority** writes
   `sort=custom.priority:asc`. The order became low (#8, #11), medium
   (#1, #3), high (#2, #4, #5, #15, #16), then unset.
3. **Compound narrowing:** **Filters → Add filter → Priority**, value
   "high", writes `f=custom.priority:any:high` (#2, #4, #5, #15, #16).
   With the search on top, only #15 and #16 are left.
4. **History:** as recorded on 10-05, a narrowing _replaces_ the history
   entry. Back from the narrowed list left the app, and Forward twice
   returned to the exact narrowed URL, with the search box still reading
   "walkthrough".
5. **Reload and deep link:** both restore the same two rows.
6. **State views from the rail:** each carries `q=`, the sort and the
   priority filter. **Proposed** shows #16, **Queued** #15, and
   **Deferred**, **Blocked** and **Closed** none. Back from Closed
   returns to the Blocked URL.
7. **Blocked view, unfiltered:** #11 and #17.
8. **Keyboard:** Tab from the search box walks Filters, Backlog order,
   the two filter chips, **Select all**, then each row's drag handle,
   checkbox and title link. Enter on a title opens `/tasks/11`. Escape
   returns to `/?state=blocked`, with focus back on that title.

**Result:** pass.

### 4. Task detail

1. **Entry points:** a list row (`/tasks/13`), an Inbox row
   (`/tasks/1`), a notification (`/tasks/14`), the keyboard (`/tasks/11`)
   and deep links. `/tasks/999` reads "no task 999".
2. **Edit:**
   - **Edit → Save** on #13 sent one PATCH with the new title and a
     Markdown description, and the heading followed.
   - **Edit → Cancel** discarded a changed title.
   - A whitespace-only title disables **Save**.
3. **Repo:**
   - **Other… → `acme/gizmos`** (Tab) sent `PATCH {"repo":"acme/gizmos"}`.
     It was refused with a 400 ("acme/gizmos is not on this deployment's
     target repo list, so this task cannot be moved onto it…"). The
     message was shown as an alert, the picker went back to the list,
     and the repo stayed `acme/widgets`.
   - Picking `acme/widgets` again left the repo unchanged.
4. **Pane fields:**
   - Base `release-9` was saved on Tab (`PATCH {"base":"release-9"}`).
   - The **Auto-merge** checkbox toggled `autoMerge` false → true.
   - **Edit fields** with priority high and requested-by Fabrikam
     (`PATCH customFields`).
   - After a reload: heading, base, auto-merge and fields all as saved.
5. **Defer and pick up (#13):**
   - **Defer** with no date → `deferred`. The pane offers Undefer, Run
     now and Close. #13 is listed under Deferred.
   - **Undefer** → back to `proposed`.
   - **Defer** until 2026-10-20 → `deferredUntil:
2026-10-20T00:00:00Z`. After a reload the pane reads "Picks itself
     back up on 10/20/2026, 12:00:00 AM, back into the proposals for
     someone to approve."
   - **Undefer**, then **Approve** → `queued`. The actions became
     Withdraw approval, Defer, Run now, Close.
   - A **past date** (2026-01-01) on #17 was refused with a 400 ("wake
     time … is not in the future…"), shown as an alert. #17 stayed
     `queued`.
6. **Comment:** a comment on queued #15 was stored by `operator`. The box
   cleared, and the state stayed `queued`.
7. **Attempts and links:**
   - #8 shows "Live attempt #2 transcript" and "Attempt #2 · Running",
     and links `https://github.com/acme/widgets/pull/104`.
   - #3 shows "attempt #1 transcript". #5 links `…/pull/101`.
   - #3 and #12 also show `acme/widgets#104`. That link sits in #8's row
     in the pane's work tree (`task-tree-row`), not in the task's own
     fields, which is correct.
   - No CI links, because no CI runs exist.

**Result:** pass.

### 5. Selection and bulk edit

1. **Selection:**
   - Ticking #2 and #15, plus Space on #13's focused checkbox, shows "3
     selected", with Approve, Defer, Close, Bulk edit… and Clear
     selection.
   - Escape does not clear the selection.
2. **Bulk edit** on #2, #13 and #15:
   - Fields the three disagree on are marked "(mixed across the
     selection)". Apply is disabled until a field is ticked.
   - Ticking Base branch, typing "cancelled-base", then **Cancel**,
     changed nothing (API snapshot identical) and kept the selection.
     Reopening starts unticked.
   - Base `release-5`, Auto-merge (left at "Off") and Requested by
     "Northwind", then **Apply to 3 tasks**, sent one PATCH per task.
   - Each task kept its other fields: #2 its area, #15 its due date,
     estimate and customer-facing.
   - The selection cleared. A reload shows the `release-5` chip on all
     three.
3. **Bar Approve on queued tasks:** with #2 and #15 (both already
   approved) selected, **Approve** posts `/approve` for each. Both are
   no-ops (`BatchActionsBar.jsx` relies on these endpoints being
   idempotent), and the selection clears.
4. **Batch close:**
   - **Select all** in Queued, then a search that shows one row, reads
     "7 selected".
   - **Close** asked "Close 7 task(s)? 6 of them are not shown in this
     view." Dismissing it closed nothing and kept the selection.

**Result:** pass.

### 6. Outage (server on 18081)

- With the server stopped, a banner reads "This deployment cannot be
  reached: the connection to it failed. Unreachable since 3:22:23 AM --
  retrying automatically…".
- **Approve** on #1 while it was down sent the request, and the row
  stayed.
- After a restart the banner cleared on its own.

**Result:** pass.

### Console and network

- No page errors.
- **Console errors:**
  - The GitHub branch-list 404 (stubbed boundary, above).
  - The deliberate 400s: the off-list repo PATCH and the past-date defer.
  - `/api/tasks/999` 404.
  - Connection refusals during the outage check.
- Chromium reports each poll answered `304 Not Modified` as
  `net::ERR_ABORTED`. The scripts filtered these out.

## Bug found and fixed on this branch

### 1. New task filed on an off-list repo without a word

**Reproduction:**

1. Start `grain demo`. Its target repo list is `acme/widgets`.
2. Open **New task** and type a title.
3. Under Repos pick **Other…** and type `acme/gizmos`.
4. Press **Create task**.

**Outcome:**

- **Before:**
  - The form shows no warning at any point.
  - `POST /api/tasks` answers 201, and the form closes onto `/` as for
    any task.
  - The server parks the task (`ui.Client.parkOffAllowlist`): #18 is
    `awaiting_reply`, and grain's comment says "`acme/gizmos` is not on
    this deployment's target repo list…".
  - The only clue is that comment, on a task nobody has opened, and an
    "Answer a question" row in the Inbox that reads "A run stopped to
    ask something".
  - The pane warns about the same repo (`Target.jsx`), and refuses a
    PATCH onto it, but the form does neither.
- **Fix:** `NewTaskOverlay` shows a warning caption under the repo field
  (`data-testid="repo-off-allowlist"`) whenever the picked or typed repo
  is not `repoTargetable(config, repo, team)`. That is the rule the
  pane's warning uses, and it matches the server's: a listed repo, the
  config repository and an unrestricted deployment get no warning.
  Nothing about what is filed changes.
- **After** (rebuilt frontend, server on 18082):
  - No warning for `acme/widgets` or `grain/config-default`.
  - For `acme/gizmos`: "acme/gizmos is not on this deployment's target
    repo list. The task will be filed but parked, and runs only once an
    operator adds the repo under Settings and replies on it."
  - Filing still parks the task, which waits under "Answer a question".
- **Coverage:** both tests below failed against the unfixed code.
  - `NewTaskOverlay.repos.test.jsx` "warns before filing on a repo off
    the target list".
  - The e2e test `new-task-off-list-repo.spec.js`, which files through
    the form on a real demo and finds the task in the Inbox.

## Observations, not changed

- **Parked off-list task in the Inbox:** it is grouped under "Answer a
  question", whose description reads "A run stopped to ask something",
  though no run happened. The row's Reply does show grain's comment.
- **No-repo tasks:** the New task form keeps Base branch and Auto-merge
  visible after "No repo", and sends `autoMerge: true` (as on earlier
  runs).
- **Failed Approve during an outage:** no toast names the failed action.
  The unreachable banner already explains it, and the row stays.

## Tests run

- `npx vitest run src/components/NewTaskOverlay.repos.test.jsx`: 19
  tests, all passed. The new case failed against the unfixed component
  (stashed).
- `npx vitest run` over `NewTaskOverlay.test.jsx`,
  `NewTaskOverlay.advanced.test.jsx` and
  `NewTaskOverlay.capabilities.test.jsx`: 3 files, 52 tests, all passed.
- `npx playwright test e2e/new-task-off-list-repo.spec.js
--project=chromium`: passed against the rebuilt frontend. It failed
  against a build without the fix, at the warning.
- `npx prettier --check` on every changed file: clean after `--write`.
