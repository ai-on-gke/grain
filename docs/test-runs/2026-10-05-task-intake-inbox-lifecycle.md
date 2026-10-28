# Task intake, Inbox, and lifecycle UI walkthrough — 2026-10-05

## What was tested

- **Commit:** `98f2a031d` (tip of `candidate`, v1.8.0-rc.1;
  `grain/task-1982` branched from it). All three findings below were
  reproduced on this commit. The fixes on this branch were then
  re-checked in the same browser, against a rebuilt frontend and server.
- **Since the last walkthrough (2026-10-04, `f56fe0d45`):** yesterday's
  fix (a defer date picked east of the deployment) is on this base and
  was re-checked from UTC. New in this area since then:
  - An "Approve on GitHub" inbox group for merge-queue entries in
    `waiting_github_approval` (`ca3c79074`).
  - A "Work from task N" tree on a task's pane (`8fa57d578`).
  - An "origin" link in the pane header (`8cc94b852`).
  - A refused 403 keeps the typed edit (`bf116f79c`).
- **Environment:**
  - Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64 x86_64.
  - Go 1.26.6, Node.js 20.19.0.
  - Playwright 1.62.1 with headless Chromium (chromium-headless-shell
    1234).
  - Viewport 1400×900, browser time zone UTC and locale en-US.
- **Servers:**
  - **Main walkthrough:** the real
    `grain demo -addr 127.0.0.1:18080 -open=false`, built from this tree
    (`npm ci && npm run build` into `pkg/ui/static`, then
    `go build ./cmd/grain`). It ran over a throwaway embedded SQLite
    store under a scratch `HOME`/`TMPDIR` (`/tmp/demo`).
  - **Second server:**
    `grain demo -addr 127.0.0.1:18081 -open=false -merge-queue-states -fake-ci`,
    built after the first two fixes, over a fresh store (`/tmp/demo2`).
    It covered the new Approve on GitHub group and re-checked the fixes.
- **Driver:**
  - Ad-hoc Playwright scripts kept outside the repository (`/tmp/wt`)
    and not committed. They drove visible controls by role and
    accessible name: clicks, typing, keyboard, Back/Forward, reload,
    deep links, and Ctrl-click into a new tab.
  - The REST API was only read, to confirm persisted state after a UI
    action. It was never used to perform one.
  - Every script logged console errors and warnings, page errors, failed
    requests, HTTP ≥ 400 responses, browser dialogs, and the body of
    every non-GET `/api/` request.
- **Account:** `operator`, created through the first-run "Create
  account" dialog on each server. The session was reused through
  Playwright storage state.
- **Gemini key:** `/home/debian/.gemini-api-key` is not present in this
  sandbox, so nothing model-backed ran. The demo needs no model.
  `gemini-2.5-pro` was picked on the New task form and
  `gemini-2.5-flash` in the pane's edit form; both persisted.
- **Sandbox setup:** `npx playwright install chromium` needed
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, because the
  package-cache proxy terminates TLS with its own CA.

## Stubs

Nothing of grain's own was stubbed or mocked. The only things missing
were outside it:

- **GitHub** is not reachable from the demo.
  `GET /api/repos/<repo>/github-branches` answers 404 whenever a
  base-branch field is shown. The UI treats that as "no suggestions".
  Apart from the deliberate errors listed below, it is the only console
  error on these screens.
- **CI:** the second server ran with the demo's own `-fake-ci` stub,
  which accepts runs but executes nothing. No runs were seeded (rail
  "CI 0"), so CI links on task detail could only be checked for
  absence.
- **GitHub approvals:** the second server's `-merge-queue-states` seeds
  the `waiting_github_approval` entry through the store, exactly as the
  queue writes one. Nothing on GitHub was asked.
- **Live agent sessions:** none exist. The streamed transcript on #3 and
  the empty one on #8 are the demo's own seeded data.

## Steps and outcomes

### 1. Inbox

1. **Opening it:** a cold load of `/inbox`, and the rail's **Inbox**
   button from `/`, both open `/inbox`. It shows four groups with one
   task each, under a count of 4, and the rail badge reads 4:
   - Answer a question (#4)
   - Submit for merge (#6)
   - Read an answer (#7)
   - Approve a proposal (#1)
2. **Row navigation:** clicking #1's title opened the pane at `/tasks/1`,
   and Back returned to `/inbox`.
3. **Approve:** **Approve** on #1 sent `POST /api/tasks/1/approve`. #1
   became `queued` and the proposal group disappeared.
4. **Reply:**
   - **Reply** on #4 shows the parked question ("Two PRs are both stuck
     failing the same lint check…") and focuses the reply box.
   - Send is disabled while the box is empty.
   - A draft survived a 3-second poll.
   - Send stored the reply as a comment by `operator`, and #4 became
     `queued`. The group left, and after a reload the count and badge
     read 2.
5. **Emptying it:** **Submit** (#6 → `completed`, into the merge queue)
   and **Acknowledge** (#7 → `closed`) emptied it. The page reads
   "Nothing is waiting on you. Every task is either running, queued or
   done.", with a count of 0, and the rail badge is gone, also after a
   reload.
6. **Keyboard:** Tab from page start reaches "+ New task", Create
   options, Customize pinned items, every rail destination, Team
   settings, Admin, Metrics and User settings, then wraps.
7. **Approve on GitHub** (second server, new group):
   - The inbox count is 5. #14 ("Move the billing exporter onto the new
     schema") sits under "Approve on GitHub". It shows the queue's note
     and an "Open pull request" link to
     `https://github.com/acme/widgets/pull/121`.
   - The base-branch wait is not listed, as designed.
   - **Bug 3:** the note's review address was plain text (see Bugs).

**Result:** pass, except bug 3 (fixed).

### 2. New task form (`/new` and the rail's "+ New task")

1. **Validation:** Create is disabled with an empty or whitespace-only
   title, and until a repo (or "No repo") is chosen.
2. **Queued path:** task #13 was filed `queued`. Every field below
   persisted, as checked through `GET /api/tasks/13`:
   - **Text:** the title, and a Markdown description.
   - **Custom fields:** priority high, area frontend + docs,
     requested-by Contoso, due 2026-11-15, estimate 3, customer-facing
     Yes.
   - **Repo:** `acme/widgets`, base `release-1.2`, and auto-merge
     unticked (`autoMerge: false`). Read-only repo `acme/docs`.
   - **Model:** `gemini-2.5-pro` → `agentFramework: antigravity`.
   - **Advanced options:** Network "None" (`networkAccess: none`),
     nothing-needed "Leave for acknowledgement" (`ack`), prompt
     extension "Be terse.", and 2 vCPU / 4096 MiB / 30 GiB.
3. **Proposed path:** "No repo", "Queue immediately" unticked, GCP key
   unticked under Capabilities, Review **security-audit**, and **Add to
   backlog → Front**. This filed #14 `proposed`, with `noRepo`,
   `reviewTemplateId: template-2` and capabilities `gemini-key,
   self-debug`. It heads All tasks, which is backlog order.
4. **Front is remembered:** the next form opened with "Add to backlog"
   already on Front, as its helper text says it will be.
5. **Base carries over:** the next task on `acme/widgets` arrived with
   base `release-1.2`. This is by design: the form fills in the base
   last used for that repo (`lastBaseForRepo`).
6. **Writes repo:** adding `acme/docs` under "Another repo this task may
   change" gave it its own base row. Create was refused with a 400,
   announced as an alert ("acme/docs is not on this deployment's target
   repo list…"), and the form stayed open with everything kept.
7. **Draft:**
   - Escape after picking an option closes the whole form, because the
     popup has already closed.
   - A click on the backdrop closes it too.
   - Either way the draft is kept, and reopening `/new` restored the
     title and repo. Both behaviours are unchanged from earlier reports.

**Result:** pass.

### 3. All tasks, state views, Blocked, search, sort, filters

1. **State views:** every rail state has its own URL and the expected
   rows:
   - Proposed → #14
   - Queued → 1, 2, 4, 11, 12, 13
   - Running → 3, 8
   - Queued for merge → 5, 6
   - Deferred → 9, and Closed → 7, 10
   - Blocked → 11 (`f=state:none:deferred|closed`)
2. **Search:** "walkthrough" in Queued narrowed the list to #13 and wrote
   `q=`. Clearing the box removed it.
3. **Sort:**
   - **Backlog order → Sort by…** offers every built-in attribute and
     each declared field.
   - **Priority** wrote `sort=custom.priority:asc`, and flipping the
     direction wrote `:desc`. Tasks with no priority sort last both
     ways.
4. **Compound narrowing:** All tasks with sort Priority ↓, Priority is
   any of high, State is any of Queued, and search "stall" narrowed to
   #4. The URL was
   `/?q=stall&sort=custom.priority:desc&f=kind:none:interactive&f=custom.priority:any:high&f=state:any:queued`.
   - **Reload:** kept the rows, the chips and the search box.
   - **Deep link** in a new page: same rows and chips.
   - **Opening and closing a row:** opening #4 went to `/tasks/4`.
     Escape returned to the exact narrowed URL.
   - **Back/Forward:** a narrowing *replaces* the history entry, so Back
     from the narrowed list left the app rather than undoing one filter.
     This is deliberate and documented in `App.jsx` (narrowing is "a
     change of question rather than of page"). Back/Forward between
     pages and rail views work as expected.
   - **Clear** emptied the search and every added filter, and restored
     the default chips.

**Result:** pass.

### 4. Task detail

1. **Entry points:** an Inbox row, a list row, a deep link
   (`/tasks/14`), and the links in another task's pane header and work
   tree.
2. **Edits on #14:**
   - **Edit → Save** sent one PATCH with the title, description, model
     `gemini-2.5-flash` and nothing-needed `ack`.
   - **Repo** "—" → `acme/widgets`, and **Base branch** `release-3` on
     Enter.
   - **Edit fields → Save fields:** priority low, requested-by
     Fabrikam, customer-facing No.
   - **Auto-merge** unticked sends `PATCH {"autoMerge": false}`.
   - All of it survived a reload and matches `GET /api/tasks/14`.
   - **Bug 1:** the timeline note for the description edit read "Now
     with a *description*.." (see Bugs).
3. **Lifecycle on #14:**
   - **Defer** with no date → `deferred`, then **Undefer** → `proposed`.
   - Defer until 2026-12-31 → "Picks itself back up on 12/31/2026,
     12:00:00 AM, back into the proposals…" (`deferredUntil`
     2026-12-31T00:00:00Z). The note survived a reload, and #14 is
     listed under Deferred.
   - Picked it back up from the Deferred view → `proposed`, then
     **Approve** → `queued`, and **Withdraw approval** replaced Approve.
   - Today's date (typed past the field's `min` of 2026-10-06) is
     refused with a page alert ("wake time … is not in the future…"),
     and the task stays as it was.
4. **Attempts, transcripts and links:**
   - #3 (running) shows "Live attempt #1 transcript" with its streamed
     text, and the new "Work from task 12: 0 of 4 done" tree.
   - #8 shows the merge-queue repair note, "Attempt #2 · Running" and
     "Still running -- nothing written yet.".
   - #5, #8 and (second server) #14 link their pull requests on
     `github.com`. #14's approval note links its review page.
   - There are no CI links, because no CI runs exist.
   - **Bug 2:** the "generated from 12 · origin 12" links in the header
     had `href="#"` (see Bugs).

**Result:** pass, except bugs 1 and 2 (fixed).

### 5. Selection and bulk edit

1. **Selection:**
   - Ticking #13 and #14, then Space on #2's focused checkbox, showed
     "3 selected".
   - Escape does not clear a selection; **Clear selection** does.
   - **Select all** in Queued selected 6. A search that then showed 2
     rows reads "6 selected · 4 not shown here". **Close** asks "Close 6
     task(s)? 4 of them are not shown in this view." (the 10-03 fix);
     the dialog was dismissed.
2. **Batch close:** with #2 and #13 selected, **Close** asked "Close 2
   task(s)?". Dismissing it changed nothing and kept the selection.
3. **Bulk edit** on #2, #13 and #14:
   - Fields the three disagree on are marked "(mixed across the
     selection)", and Apply is disabled until a field is ticked.
   - Ticking Base branch and typing, then **Cancel**, changed nothing
     and kept the selection. Reopening starts unticked.
   - Base branch `release-5`, Auto-merge (left at "Off") and
     Requested by "Northwind", then **Apply to 3 tasks**, sent one
     PATCH per task. Other custom fields were kept (#13 kept its area,
     due date, estimate and customer-facing). The selection cleared.
     A reload shows the `release-5` chip on all three.

**Result:** pass.

### Console and network

- No page errors and no failed requests.
- The only console errors are the GitHub branch-list 404 above, and the
  deliberate errors:
  - the off-list writes repo, a 400 on `POST /api/tasks`;
  - today's defer date, a 400 on `POST /api/tasks/{14,1}/defer`.
- Chromium reports each 3-second poll answered `304 Not Modified` as
  `net::ERR_ABORTED`. The scripts filtered these out.

## Bugs found and fixed on this branch

### 1. An edited description reached the timeline (and the run) with a full stop after it

**Reproduction:**

1. Open `/tasks/14`, press **Edit**, and set the description to
   `Now with a *description*.`.
2. Press **Save**.

**Outcome:**

- **Before:** the timeline note reads "This task was just edited -- …
  the description changed to: Now with a *description*.." The stored
  comment body ends `*description*..`.
  - This comment is how a live run learns of the edit
    (`orchestrator.addendaPoller`), so the run is handed text the
    operator never wrote.
  - A description ending in a fenced code block gets a closing fence of
    "```.", which no longer closes it.
- **Cause:** `Client.noteEdit` (`pkg/ui/client.go`) formatted the whole
  note as `"This task was just edited -- %s."`, with the new description
  as its last part.
- **Fix:** the description is quoted last with nothing after it. A
  title-only edit keeps its full stop.
- **Coverage:**
  - `TestUpdateTaskQuotesAnEditedDescriptionVerbatim`
    (`pkg/ui/client_test.go`) checks both bodies exactly. It failed on
    the old code with "…make test\n```."".
  - The e2e test "quotes an edited description as typed and links
    related tasks by address" (`task_detail_workflows.spec.js`).

### 2. A task pane's links to other tasks pointed at "#"

**Reproduction:** open `/tasks/3`. Its header reads "generated from 12 ·
origin 12". Ctrl-click (or middle-click, "Open in new tab", "Copy link")
on either 12.

**Outcome:**

- **Before:** both anchors have `href="#"`. A plain click opens #12
  through an `onClick`, but a new tab, or a copied link, gets
  `/tasks/3#`, the pane already open.
  - The same pattern was on every header relation (generated from,
    origin, security review, supersedes/superseded by, reverts/reverted
    by, port of/ported by).
  - It was also on the Actions links to a security review and to a
    path-trigger review task.
  - The work tree just below already linked `/tasks/12` properly.
- **Fix:** a shared `detail/TaskLink.jsx` renders
  `href="/tasks/<id>"` and only takes over a plain left click, the way
  `ListPrimitives.RowLink` does.
- **After** (rebuilt frontend, second server): the header links carry
  `/tasks/12`. A plain click opens #12 in place. Ctrl-click opens
  `/tasks/12` in a new tab (heading "12 director session"), and the
  original page stays on `/tasks/3`.
- **Coverage:**
  - `DetailOverlay.test.jsx` "gives a link to another task that task's
    own address" checks the hrefs, that a Ctrl-click is left to the
    browser, and that a plain click opens. It failed on the old code.
  - The e2e test named under bug 1 checks the href, and that a
    Ctrl-click opens a new tab while a plain click opens in place.

### 3. The inbox's GitHub approval note printed its review address as text

**Reproduction:** start `grain demo -merge-queue-states` and open
`/inbox`. Under "Approve on GitHub", #14's note says "Approve it at
https://github.com/acme/widgets/pull/121/files and grain merges it on
its own…".

**Outcome:**

- **Before:** the address is plain text in the inbox, while the same
  note in the task pane links it (`LinkedNote`, from `de9811738`). The
  inbox is the page somebody uses to find what to do next.
- **Fix:** `InboxPage` renders every group's note (approval waits,
  stalled merges, stalls) through `LinkedNote`. The notes are grain's
  own strings, and `LinkedNote` links only http(s) and never renders
  markup.
- **After:** the inbox note's address is a link to
  `…/pull/121/files`.
- **Coverage:**
  - `InboxPage.test.jsx`'s approval-group case asks for the link. It
    failed without the fix.
  - New e2e test "the inbox's Approve on GitHub row links the review
    page its note names" (`merge-queue-waiting-states.spec.js`).

## Observations, not changed

- **No-repo in the pane:** a no-repo task's pane shows Repo "—" rather
  than "No repo". Base branch and Auto-merge stay editable, and the
  proposal was stored with `autoMerge: true`. This was noted before and
  is harmless.
- **Model label:** the pane reads "Model Rotation" for a task with no
  model, while the edit form calls the same choice "Team default".
- **Writes picker:** typing `not a repo` into "Another repo this task
  may change" and pressing Enter does nothing, with no message. The
  read-only picker and the main repo's "Other…" do explain.
- **Single-value storage:** seeded #2 stores `area` as the string
  `"backend"`. A bulk edit sends `["backend"]` and the stored value is
  unchanged. Both render alike.
- **Bulk edit vs the form:** as reported on 10-04, Bulk edit offers
  "Agent framework", which the New task form no longer shows. It does
  not offer Network access or the sandbox shape.

## Tests run

- `go test ./pkg/ui -run 'TestUpdateTaskQuotes|TestUpdateTaskNotes'`:
  passed. The new test failed against the unfixed `client.go`.
- `npx vitest run src/components/DetailOverlay src/components/detail`:
  14 files, 187 tests, all passed.
- `npx vitest run src/components/InboxPage`: 41 tests, all passed. The
  two new assertions each failed against the unfixed component.
- `npx playwright test --project=chromium` over
  `task_detail_workflows`, `merge-queue-waiting-states`, `inbox` and
  `task_intake_inbox_lifecycle_walkthrough`: 17 tests, all passed
  against the rebuilt frontend. The new inbox e2e case failed against
  the build without the inbox fix.
