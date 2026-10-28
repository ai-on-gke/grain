# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-06

## What was tested

- **Commit:** `58ae037b2` (tip of `candidate`; `grain/task-2232`
  branched from it). The one finding below was reproduced on this
  commit. The fix on this branch was then re-checked in the same
  browser, against a rebuilt frontend and server.
- **Since the last walkthrough (2026-10-05, `98f2a031d`):** these changes
  in this area were the focus, as well as the full walk:
  - A **proposer** team role that files tasks only as proposals
    (`ea6453fd6`).
  - **Desktop notifications** for new inbox arrivals (`017527a85`).
  - A person's **comment on an awaiting-ack task** sends it back to the
    queue (`660c5577e`).
  - A **running task's repo and base** are shown read-only (`c210c4e98`).
  - **Model pickers** are searchable by typing (`ef877ca0b`,
    `2781a4e9a`).
  - Template pickers tell same-named templates apart (`a620f1e51`).
  - The task pane no longer flickers on open (`a216d12da`).
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
  - **Clean comparison:** `127.0.0.1:18081` over `/tmp/demo18081`. It
    replayed the New task submission on a fresh store, and showed the
    awaiting-ack hint on a #7 nobody had touched.
  - **Re-check:** `127.0.0.1:18082` over `/tmp/demo18082`, rebuilt with
    the fix, for the proposer inbox.
- **Driver:**
  - Ad-hoc Playwright scripts kept outside the repository (`/tmp/wt`)
    and not committed. They drove visible controls by role and
    accessible name: clicks, typing, Space and Enter, Tab order,
    Back/Forward, reload, deep links, and Ctrl-click into a new tab.
  - Every script logged console errors and warnings, page errors, failed
    requests, HTTP ≥ 400 responses, browser dialogs, new tabs, and the
    body of every non-GET `/api/` request.
  - The REST API was read to confirm persisted state after a UI action.
    It was written to only for the setup and stubs listed below.
- **Accounts:**
  - `operator` was created through the first-run "Create account"
    dialog.
  - `pat` was registered with `POST /api/auth/register` (setup). The
    operator then added `pat` as a **Proposer** through Team settings >
    Members. `pat` signed in through the dialog's "Log in" tab.
- **Gemini key:** `/home/debian/.gemini-api-key` is not present in this
  sandbox, so nothing model-backed ran. The demo needs no model.
  `gemini-2.5-pro` was picked on the New task form and
  `gemini-2.5-flash` in the pane's edit form, both by typing part of the
  name; both persisted.
- **Sandbox setup:** `npx playwright install --with-deps chromium`
  needed `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, as on
  earlier runs.

## Stubs

Nothing of grain's own was mocked. The stubs were:

- **GitHub** is not reachable from the demo.
  `GET /api/repos/<repo>/github-branches` answers 404 whenever a
  base-branch field is shown. The UI treats that as "no suggestions".
- **The OS notification and the browser's permission prompt.**
  Headless Chromium reports `Notification.permission` as `denied` even
  after `grantPermissions(["notifications"])`. One script therefore
  replaced `window.Notification` with a recorder: `requestPermission()`
  resolves `granted`, and constructed notifications are kept in an
  array. It also wrapped `document.hasFocus()`, so the page could be
  marked as "in the background". grain's own code (the switch, the
  inbox diff and the click handler) ran unmodified.
- **An agent filing a proposal:** for the notification check, the new
  inbox arrival (#13) was filed with `POST /api/tasks` from outside the
  browser, the way an agent's `propose_task` arrives.
- **Live agent sessions and CI:** none exist. The transcripts on #3 and
  #8 are the demo's seeded data. No CI runs are seeded (rail "CI 0"), so
  CI links on task detail could only be checked for absence.

## Steps and outcomes

### 1. Inbox

1. **Opening it:** a cold load of `/inbox`, and the rail's **Inbox**
   button from `/`, both open `/inbox`. It shows four groups with one
   task each, under a count of 4, and the rail badge reads 4:
   - Answer a question (#4)
   - Submit for merge (#6)
   - Read an answer (#7)
   - Approve a proposal (#1)
2. **Row navigation:** clicking #1's title opened `/tasks/1` ("1 Add a
   dark mode toggle"), and Back returned to `/inbox`.
3. **Desktop notifications (new):**
   - With permission granted, the header shows **Turn on
     notifications**. Clicking it turns it into **Turn off
     notifications**.
   - With the page marked as in the background, a proposal filed from
     outside (#13) raised exactly one notification: "13 was proposed and
     needs approval", body "Agent proposal arriving", tag
     `task:13:proposed`. The count went to 5.
   - Clicking the notification opened `/tasks/13`.
   - "On" survives a reload. Turning it off survives a reload too
     (`grain.inboxNotifications = off`).
   - With the browser's real `denied` permission the header reads
     "Notifications blocked", with a hint on how to allow them.
4. **Decline:** **Decline** on #13 asks "Close 13 without running it? It
   can be reopened." Dismissing it left #13 `proposed`.
5. **Approve:** **Approve** on #1 sent `POST /api/tasks/1/approve`. #1
   became `queued` and its row left the group.
6. **Reply:**
   - **Reply** on #4 shows the parked question ("Two PRs are both stuck
     failing the same lint check…") and focuses the reply box.
   - Send is disabled while the box is empty.
   - A draft survived a 3.5-second poll.
   - Send stored the reply as a comment by `operator`, and #4 became
     `queued`. After a reload the count read 3, with the groups Submit,
     Read an answer, and Approve a proposal.
7. **Defer from the Inbox:** **Defer** on proposal #23 deferred it at
   once (`POST /api/tasks/23/defer`), with no dialog.
8. **Emptying it:**
   - #7 was acknowledged by a comment (section 4), and #23 deferred.
   - **Keyboard:** from page start, Tab walks "+ New task", the rail
     entries, Admin, Metrics, User settings, then the row title and its
     **Submit**. Enter on Submit submitted #6 (`completed`).
   - The page then reads "Nothing is waiting on you. Every task is
     either running, queued or done.", with a count of 0. The rail badge
     is gone, also after a reload.

**Result:** pass for the operator. Bug 1 (fixed) is the proposer's view
of this page.

### 2. New task form (`/new` and the rail's "+ New task")

1. **Validation:**
   - Create is disabled with a whitespace-only title, and until a repo
     (or "No repo") is chosen.
   - A Disk of −5 GiB is refused by the browser's own validation
     ("Value must be greater than or equal to 0."). The field is
     focused, nothing is sent, and the form stays open. With 30 the task
     was filed.
2. **Model search (new):**
   - Typing "pro" or "fla" in Model narrows the list to
     `gemini-2.5-pro` or `gemini-2.5-flash`.
   - Escape inside the picker closes only the picker, not the form.
3. **Queued path:** this filed `queued` tasks #14–#22. Their
   `POST /api/tasks` body and stored task carry:
   - **Text:** the title, and a Markdown description with a fenced
     block.
   - **Custom fields:** priority high, area frontend + docs,
     requested-by Contoso, due 2026-11-15, estimate 3, customer-facing
     true.
   - **Repo:** `acme/widgets`, base `release-2.0`, `autoMerge: false`.
   - **Capabilities:** Self debug unticked, so `gcp-key, gemini-key`.
   - **Model:** `gemini-2.5-pro`.
   - **Advanced options:** network `none`, nothing-needed `ack`, prompt
     extension "Be terse.", and 2 vCPU / 4096 MiB / 30 GiB.
   - **Option lists:** Network offers Deployment default / Egress /
     None. Review offers No review, nightly-maintenance, security-audit
     and release-qualification. Specific connection offers "any" or
     "gemini 2.5 pro · antigravity".
   - **#14 differs,** with no area and Self debug kept. That was a
     restored draft, not a bug: the previous script run had left a draft
     holding area frontend + docs and Self debug unticked. The script's
     own clicks then toggled those back. Replaying the same steps from
     an empty draft, four times here and once on a fresh server, always
     sent `area: ["frontend","docs"]` and no `self-debug`.
4. **Cancel and draft:**
   - Typing a title, then **Close dialog**, kept a draft. Reopening
     restored it, and **Discard draft** emptied the title.
5. **Proposed path:**
   - Setup: "No repo", "Queue immediately" unticked, Review
     **security-audit**, and **Add to backlog → Front**.
   - This filed #23 `proposed` with `noRepo`,
     `reviewTemplateId: template-2` and `atFront`. It heads All tasks
     (backlog order).
   - The next form opened on Front, as its helper text says.
6. **Proposer (new):**
   - Signed in as `pat`, the form shows "You are a proposer on this
     team: what you file is a proposal…".
   - "Queue immediately" and "Add to backlog" are not offered.
   - Create filed #24 `proposed` (`approved: false`), with its
     capabilities kept.

**Result:** pass.

### 3. All tasks, state views, Blocked, search, sort, filters

1. **All tasks:** `/tasks` opens `/` with the default narrowing (`State
   is none of Deferred, Closed`, `Kind is none of Interactive`).
2. **Search:**
   - Under **Filters**, "Search tasks…" with "dark mode" leaves #1 and
     writes `?q=dark+mode`.
   - A reload keeps the query and the box's text.
   - Typing replaces the history entry rather than adding one.
3. **Sort:**
   - **Sort by… → Title** writes `?sort=title:asc` and reorders A–Z. A
     reload keeps it.
   - **Sort by… → Priority** puts low, medium, high in declaration
     order, with unset rows last. "Last to first" reverses the set
     values and still leaves unset last.
   - **Then by… → Created** writes
     `sort=custom.priority:desc,created:desc`. **Remove sort 1** leaves
     `sort=created:desc`.
4. **State views:** each rail state has its own URL and the expected
   rows:
   - Proposed → 23, 13
   - Queued → 1, 2, 4, 11, 12, 14–22
   - Running → 3, 8
   - Awaiting submit → 6
   - Awaiting ack → 7
   - Queued for merge → 5
   - Deferred → 9
   - Closed → 10
   - Blocked → 11
5. **Back/Forward:** stepping back from Blocked went to Closed, then
   Deferred. Forward returned to Closed, with its one row.
6. **Compound narrowing:**
   - Queued + Priority = high gave 11 rows
     (`f=custom.priority:any:high`). Adding the search "walkthrough"
     gave 9.
   - A reload kept all three conditions.
   - **Clear the search and every filter** returned to the 14 queued
     tasks.

**Result:** pass.

### 4. Task detail

1. **Entry points:**
   - Inbox row (#1), Proposed list row (#13), a cold deep link
     (`/tasks/3`, `/tasks/13`), and Deferred list row.
   - Ctrl-click on #3's "12" header link opened `/tasks/12` in a new tab
     and left the original on `/tasks/3`.
2. **Edit** (#13, proposed):
   - Title "Agent proposal, retitled", a bulleted description, and Model
     `gemini-2.5-flash` (typed "fla") were saved in one PATCH.
   - The timeline note quotes the description verbatim, with no
     trailing full stop (the 10-05 fix).
   - Saving again unchanged added no second note.
3. **Pane fields:**
   - Base `release-9` was saved on Tab (`PATCH {"base":"release-9"}`).
   - **Edit fields** with priority high and requested-by Fabrikam was
     saved (`PATCH customFields`).
   - The **Auto-merge** checkbox toggled `autoMerge`.
   - After a reload: heading, base, auto-merge and fields all as saved.
4. **Defer and pick up:**
   - **Defer** with no date → `deferred` (`deferredUntil: ""`). The
     pane offers **Undefer** only.
   - #13 is listed under Deferred. **Undefer** → back to `proposed`.
   - **Defer** with Until 2026-10-20 → `deferredUntil:
     2026-10-20T00:00:00Z`. After a reload the pane reads "Picks itself
     back up on 10/20/2026, 12:00:00 AM, back into the proposals for
     someone to approve."
   - **Undefer**, then **Approve** → `queued`. The actions became
     Withdraw approval, Defer, Run now, Close.
5. **Running task (new read-only repo/base):** #3's Repos group reads
   "Repo acme/widgets · Base branch the repo's default", with no
   editable Base branch field.
6. **Attempts and links:**
   - #8 shows the merge-queue repair note, "Live attempt #2 transcript",
     "Attempt #2 · Running" and "Still running -- nothing written yet.".
   - #8 links `https://github.com/acme/widgets/pull/104`, and #5 links
     `…/pull/101`.
   - No CI links, because no CI runs exist.
7. **Comment on awaiting-ack (new):**
   - On #7 the comment box says "Sending a comment reopens this task
     for another run." (checked on the clean server).
   - Commenting moved #7 from `awaiting_ack` to `queued`.
8. **Proposer's own proposal (new):**
   - `pat` sees Edit and Comment on #24. The Edit form has only Title
     and Description, and Capabilities is disabled.
   - Editing the title (`PATCH {title, description}`) and commenting
     both worked, and #24 stayed `proposed`.
   - On someone else's task (#2) `pat` gets no Edit, Approve or Comment.

**Result:** pass.

### 5. Selection and bulk edit

1. **Selection:**
   - Ticking #2 and #14, plus Space on #13's focused checkbox, shows "3
     selected", with Approve, Defer, Close, Bulk edit… and Clear
     selection.
   - Escape does not clear the selection.
2. **Bulk edit** on #2, #13 and #14:
   - Fields the three disagree on are marked "(mixed across the
     selection)". Apply is disabled until a field is ticked.
   - Ticking Base branch, typing "cancelled-base", then **Cancel**,
     changed nothing and kept the selection. Reopening starts unticked.
   - Base `release-5`, Auto-merge (left at "Off") and Requested by
     "Northwind", then **Apply to 3 tasks**, sent one PATCH per task.
   - Each task kept its other fields: #2 its area, #14 its due date,
     estimate and customer-facing.
   - The selection cleared. A reload shows the `release-5` chip on all
     three.
3. **Batch close:**
   - **Select all** in Queued, then a search that shows one row, reads
     "16 selected".
   - **Close** asked "Close 16 task(s)? 15 of them are not shown in this
     view." Dismissing it closed nothing and kept the selection.

**Result:** pass.

### Console and network

- No page errors and no failed requests.
- **Console errors:**
  - The GitHub branch-list 404 (stubbed boundary, above).
  - The deliberate 403 from pressing Approve as `pat` before the fix.
  - For the non-admin `pat` only: two `403 GET /api/state-repo` on every
    full page load, each followed by the warning "grain: state repo
    unavailable … does not have administrator privileges". See
    Observations.
- Chromium reports each poll answered `304 Not Modified` as
  `net::ERR_ABORTED`. The scripts filtered these out.

## Bug found and fixed on this branch

### 1. A proposer's (or viewer's) inbox listed tasks they cannot act on

**Reproduction:**

1. Start `grain demo`. Register `pat`, and add `pat` to the default team
   as **Proposer** (Team settings > Members).
2. Sign in as `pat` and file a task from New task. It is filed
   `proposed` (#24).
3. Open `/inbox`.

**Outcome:**

- **Before:**
  - `pat`'s own proposal is listed under "Approve a proposal" ("An agent
    proposed these…"), with **Approve**, **Defer** and **Decline**.
  - The rail badge reads 1.
  - With notifications on, it is announced as "24 was proposed and
    needs approval".
  - Pressing Approve sends `POST /api/tasks/24/approve`, which is
    refused with a 403 ("you hold the proposer role on team
    "default"…").
  - The task pane already hides these writes (`taskWriteRefusal`), but
    the inbox never asked. Every waiting task in a team its reader holds
    only the viewer or proposer role on was listed as waiting on them.
- **Fix:**
  - `state.js`'s `inboxTasks(tasks, config)` drops any task that
    `taskWriteRefusal` says the reader cannot change.
  - `InboxPage`, the rail (`Sidebar`) and `useInboxNotifications` all
    pass `config`, so the page, the count and the notifications still
    agree.
  - A config without `readOnlyTeams` changes nothing.
  - One exception: the row whose reply box is open stays while it is
    open. A member demoted mid-reply keeps what they typed and sees the
    403, as the task pane keeps its own draft (grain/task-1835).
    `ui-followups-walkthrough.spec.js` checks that path, and caught the
    first version of this fix in CI.
- **After** (rebuilt frontend, server on 18082):
  - `pat`'s inbox reads 0, "Nothing is waiting on you…", with no rail
    badge.
  - The operator's inbox still lists the proposal, with a working
    Approve, under a count of 5.
- **Coverage:** all four tests below failed against the unfixed code.
  - `state.test.js` "leaves out tasks in a team the reader may only
    read".
  - `InboxPage.test.jsx` "leaves out the tasks of a team the reader may
    only read", and "keeps an open reply's row when its team turns
    read-only" (the second fails without the carve-out).
  - `inboxNotifications.test.js` "does not announce a task its reader
    may not act on".
  - The e2e test `inbox-read-only-teams.spec.js`, which files the
    proposal as a proposer through the New task form and checks both
    inboxes. It runs on a demo of its own.

## Observations, not changed

- **Non-admin `/api/state-repo`:** for a non-admin, the start-up effect
  in `App.jsx` always asks for `/api/state-repo` and logs a 403 and a
  warning, even though `config.isAdmin` already says the answer will be
  a refusal. It is harmless: the prefill it feeds simply falls back.
  Filed as a follow-up.
- **Signed-out dialog:** once accounts exist, the dialog still opens on
  the "New user" tab, so a returning user has to pick "Log in" first.
- **Hand-typed sort keys:** a hand-typed `?sort=priority:desc` is
  ignored without comment. The key is `custom.priority`, which the Sort
  panel writes.
- **No-repo tasks:** the New task form keeps Base branch and Auto-merge
  visible after "No repo" is picked, and the proposal was stored with
  `autoMerge: true`, as noted on earlier runs.
- **Area picker:** the Area picker closes after each pick, so a second
  value needs a second click. Escape after a pick then closes the whole
  form (the draft is kept), as on earlier runs.

## Tests run

- `npx vitest run src/state.test.js src/inboxNotifications.test.js
  src/components/InboxPage.test.jsx src/components/Sidebar.test.jsx`: 4
  files, 338 tests, all passed. The three new cases failed against the
  unfixed sources (stashed).
- `npx vitest run src/components/InboxPage.test.jsx`: 43 tests, all
  passed after the carve-out.
- `npx playwright test e2e/inbox-read-only-teams.spec.js
  --project=chromium`: passed against the rebuilt frontend. It failed
  against a build without the fix, at "Nothing is waiting on you".
- `npx playwright test --project=chromium` over
  `ui-followups-walkthrough`, `inbox-read-only-teams`, `inbox` and
  `task_intake_inbox_lifecycle_walkthrough`: 17 tests, all passed.
- `npx prettier --check` on every changed file: clean.
