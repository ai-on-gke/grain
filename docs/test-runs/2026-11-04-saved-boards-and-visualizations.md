# Saved boards and task visualizations walkthrough — 2026-11-04

## Commit and environment

- **Commit tested:** `5eb6b1c0` (`main`, "Merge pull request #688"), on
  branch `grain/049f1e09/task-2960-walk-through-saved-boards-and`. The
  two fixes below (`fix(ui): keep both asked states together when a list
  is sorted by state`, `fix(ui): name a dashboard's running count once`)
  were built on top of it and checked again in the browser.
- **Date:** 2026-11-04, 09:06–09:50 UTC.
- **Server:** the real `grain demo`, built with `go build ./cmd/grain`
  (go1.26.10) after `npm ci && npm run build` in `ui/` (the production
  Vite bundle embedded from `pkg/ui/static`). Each server ran over its
  own throwaway embedded store with `-open=false -access-code-file
  <file>`:
  - `127.0.0.1:18701`: `5eb6b1c0` unchanged, for §1–§10 and the before
    screenshots;
  - `127.0.0.1:18702`: rebuilt with both fixes, for the after
    screenshots and the checks in "Bugs found".
- **Browser:** Playwright 1.62.1 Chromium (`/var/cache/playwright`),
  driven by ad-hoc Node scripts through visible controls (not
  committed). Sign-in went through the "Access code" page. Desktop was
  1440×1000. Phone was 390×844 (`iPhone 13` descriptor, `isMobile`,
  `hasTouch`). Every script recorded page errors, console errors, HTTP
  ≥ 400 responses, failed requests and every non-GET `/api` call.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so no
  Gemini-backed tooling ran and `LOOK=1` was not used. The file reader
  shows PNGs as bytes, so the screenshots were checked by bounding boxes
  (see "Bugs found").

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`.** The demo seeds no held
   proposal; only an agent's `propose_task` with `hold_for_approval`
   files one. Task 22 was filed with what that call sends
   (`proposedBy: "8"`, `holdForApproval: true`, a `proposalKey`).
2. **Stand-in boards, created over `POST /api/boards`.** The product's own
   creation path files a task against `grain/config` for an agent, and
   `grain demo` runs no agent. That contract was exercised first (§2).
   The stand-ins:
   - `WT List` (board-5, `list`, `f=state:any:running`, folder `WT2960`)
   - `WT Kanban` (board-6, `kanban`, `f=repo:any:acme/widgets`; columns
     Todo (`proposed`,`queued`) / Doing (`running`) / Talking
     (`awaiting_reply`), folder `WT2960`)
   - `WT Kanban Submit` (board-7, `kanban`; columns Sign off
     (`awaiting_submit`) / Doing (`running`), folder `WT2960`)
   - `WT Gantt` (board-8, `gantt`, `q=retry`, folder `Ops`)
   - `WT Dash` (board-9, `panels`, `f=repo:any:acme/widgets`, folder
     `Ops`; panels `states`, an inheriting `list` "Inherit", a `list`
     "Running only" with its own query at `third` width, `metrics` 30d,
     `gantt` 24h at `full` width, and a `list` "Nothing" over
     `q=zzzznothing`)
   - `WT Tree` (board-10, `tree`, `q=pagination`, folder `Ops`)
   - `WT Report All` (board-11, `report`, all 13 sections in
     `model.ReportSections`)
   - `WT Report Widgets` (board-12, `report`, `f=repo:any:acme/widgets`;
     sections backlog titled "Waiting work", throughput, tools, agents)
   - `WT Empty Dash` (board-13, `panels`, no panels)
   - `WT List Sorted` (board-14, `list`,
     `sort=state:asc&f=state:any:proposed|queued|running|awaiting_reply|awaiting_submit|failed|completed`)

   Every stand-in was created with `orderKey` 0, so the boards in a
   folder start out tied, which §1's drags then had to separate.
3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*` passed through to the real server, then had
     `tools`, `sandboxes` and `agents` filled with the shapes
     `ReportBoard.sections.test.jsx` uses. The demo's runs record no tool
     census, sandbox lifetimes or agent telemetry, so this is the only
     way those sections draw in a browser.
   - `**/api/metrics*` delayed 1.5 s, then `500 {"error":"synthetic
     metrics 500"}`, for the report's and the metrics panel's loading
     and error states.
   - `/api/tasks` (GET only) passed through, with one task's `state`
     rewritten to `failed` (task 21 on 18701, task 2 on 18702). The demo
     seeds no failed task: `failed` is what an agent run ends in, and
     the demo runs none. This was only for bug 1.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- Rail `boards 4` opened `/boards`. It listed the four seeded boards in
  order, with kind chips and descriptions: Board/kanban,
  Timeline/timeline, Report/report, Tree/tree. The index has no search
  or sort control (0 inputs, 0 selects). The header carries `new board`.
- With the stand-ins (count 14, and the rail read `boards 14`):
  - unfiled first: the four seeded boards, then WT Report All, WT Report
    Widgets, WT Empty Dash, WT List Sorted;
  - then `Ops`: WT Gantt, WT Dash, WT Tree;
  - then `WT2960`: WT List, WT Kanban, WT Kanban Submit.
- **Dragging in a section below the first:**
  - In `WT2960` (the third section), `Move WT Kanban Submit` dropped on
    `WT List` sent `PATCH /api/boards/board-7
    {"folder":"WT2960","orderKey":-1048576}`. Order became Kanban
    Submit, List, Kanban, and it stayed so after a reload.
  - In `Ops` (all tied at 0), `Move WT Tree` dropped on `WT Dash` sent
    `PATCH board-10 {"folder":"Ops","orderKey":1048576}` and `PATCH
    board-9 {"orderKey":2097152}`. It renumbered the tie rather than
    writing a midpoint equal to both. Order became Gantt, Tree, Dash
    after a reload.
- **Keyboard move.** `Move WT Gantt` → Enter offered 15 slots, including
  `outside every folder`, `at the end of Ops` and `at the end of
  WT2960`. The slot directly below the board itself is left out. Escape
  cleared them, with focus kept on `Move WT Gantt`.
- **Tab order** after the header: `Alerts` → `System` → `Settings` → `+
  New task` → `Create options` → `Move Board` → `Board` → `Move
  Timeline` → `Timeline`, one handle and one link per board.
- **History.** `WT List` → row → Escape → Back → Back → Forward → reload
  gave `/tasks/3`, `/boards/board-5?f=state:any:running`, `/tasks/3`,
  `/boards/board-5?…`, `/tasks/3`, `/tasks/3`.
- Direct URLs `/boards/board-1` … `/boards/board-14` all opened cold.

### 2. Asynchronous board creation

- From `/?q=retry&f=repo:any:acme/widgets`, `Create options` offered `new
  task / new session / new board`. `new board` opened `/new` titled "Add
  a board", with a description carrying ``- query:
  `q=retry&f=repo:any:acme/widgets` `` and every kind.
  - `Create task` sent `POST /api/tasks` (`repo: "grain/config"`) and
    filed task 20 (`queued`).
  - It returned to the list it came from, with the narrowing intact.
- The `/boards` header's `new board` gave `- query: <see below>` and filed
  task 21 (`queued`, `grain/config`), then returned to `/boards`.
- `/api/boards` held 4 boards before and after both requests. No board
  was created directly, so the asynchronous contract holds.
- `Add panel` on `WT Dash` opened `/new` with "Add a panel to grain's
  saved board `board-9` ("WT Dash")…". It listed the panels in drawn
  order with widths, queries and `order_key`s, already reflecting §7's
  reorder. Back returned to `/boards/board-9?f=repo:any:acme/widgets`.

### 3. List

- `WT List` showed tasks 3 and 8. A row click opened `/tasks/3`, and
  Escape returned to the board with its query.
- Typing `pagination` in the search wrote
  `?q=pagination&f=state:any:running` and left task 8.
- The list's sort control is `Backlog order`. `WT List Sorted` read
  proposed (1, 12, 22), waiting (11 blocked, 20, 2, 21), running (3, 8),
  awaiting_reply (4, 7), awaiting_submit (6, 14–19), and merging (5).
  With a failed task stubbed in, that is bug 1.

### 4. Kanban

- **Seeded `Board`:**
  - Proposed 3 (1, imported 12, held 22), Waiting 4 (2, 11, 20, 21),
    Running 2 (3, 8), Asked 9 (4, 6, 7, 14–19), Failed 0, and **Merging**
    1 (task 5).
  - Note: "2 tasks in no column (Deferred, Closed)."
- **Card markers:**
  - task 11: `badge badge-icon badge-blocked`, labelled `Blocked on: #8
    (running)`;
  - task 22: chip **"held for approval by 8"** (`chip-held`, titled
    "task 8 could have queued this on its own permission and asked for a
    person to approve it first");
  - task 12: chip **"imported"** (`chip-imported`, titled "imported from
    GitHub issue acme/widgets#42");
  - asked cards: `badge-awaiting_reply` on 4 and 7 ("Asked a question",
    "Answered in a comment") and `badge-awaiting_submit` on the rest.
- **Custom columns:**
  - `WT Kanban` (collects `awaiting_reply` only): Todo 5 / Doing 2 /
    Talking 2, and "4 tasks in no column (Asked to submit, Merging,
    Deferred, Closed)." Those are tasks 6, 5, 9 and 10, all in
    acme/widgets.
  - `WT Kanban Submit` (collects `awaiting_submit` only): Sign off 7 /
    Doing 2, and "12 tasks in no column (Proposed, Waiting, Asked for a
    reply, Merging, Deferred, Closed)."
  - Neither offers `edit columns`.
- **Card moves within Waiting:**
  - Keyboard: `Move task 21` → Enter → the first slot sent `POST
    /api/tasks/reorder {"ids":["21"],"afterId":"2","beforeId":"11"}`.
    Focus stayed on `Move task 21`.
  - Escape on a pending `Move task 1` cleared every slot and kept focus.
  - Mouse: dragging 20 onto 2 sent `{"ids":["20"],"beforeId":"2"}`.
    Dragging task 2 onto the Running column changed nothing (a card's
    state is not a drop target).
  - After a reload the order was 20, 2, 21, 11.
  - Phone: tapping `Move task 11` showed "Moving 1 task — choose where it
    goes" with 3 slots. Tapping the first sent
    `{"ids":["11"],"beforeId":"20"}`. After a reload: 11, 20, 2, 21.
- **Opening a card.** The title link opened `/tasks/1`. Escape returned
  to `/boards/board-1`, with focus on that card's title link.
- **`edit columns`:**
  - Every icon button is named (`Move <col> left/right`, `Remove
    <col>`). `Move Proposed left` and `Move Merging right` are disabled.
  - Enter on `Move Merging left` kept focus on it.
  - Renaming column 2 to "Up next" and pressing Escape raised the native
    confirm "You have unsaved changes here. Leave and discard them?".
    Dismissing it kept the dialog open.
  - Escape inside an open States listbox closed only the listbox.
  - Saved, the board drew Proposed / Up next / Running / Asked / Merging
    / Failed, the same after a reload (`localStorage
    ['grain.board.columns']`).

### 5. Timeline / gantt

- Range (`combobox "Range"`) offers last 24 hours / 7 / 30 / 90 days /
  everything. Lanes (`combobox "Lanes"`) offers no lanes / by repo / by
  state / by author.
- The default (7 days) drew 20 bars and "1 task outside this range.",
  with one `.gantt-now` at `left: 100%`.
- **24 hours + by repo:** acme/widgets 11, grain/config 2, No repo 5,
  with "3 tasks outside this range.". This survived a reload
  (`grain.gantt.view = {"range":"24h","lanes":"repo"}`), and the URL
  stays bare.
- **Everything + by state:** Proposed 3, Waiting 4, Running 2, Asked 9,
  Merging 1, Deferred 1, Closed 1. 2026-11-03's fix holds.
- **By author:** debian 18, director 1, grain 1, issue-import 1.
- **Dependencies.** Task 11's bar reads "Waiting, blocked: waiting for
  45m, never dispatched". The dependency is drawn as that blocked state;
  no arrows are drawn.
- **Opening a bar.** A bar click opened `/tasks/1`. Escape returned with
  focus on `a.gantt-row-title`.
- `WT Gantt` cold-opened on `?q=retry` with 8 bars and kept them through
  a reload.

### 6. Tree

- **Seeded `Tree`:** 13 (Director Session) → 1, 3, 8; 8 → 11 and the
  held 22, which task 8 filed. Everything else is a root, newest first.
- `WT Tree` (`?q=pagination`) showed 8 → 11, the same after a reload.
- Clicking row 11 opened `/tasks/11`. Escape returned to
  `/boards/board-10?q=pagination`.
- `/boards/board-4?f=state:any:running|queued` showed 21, 20, 13 → 3,
  8 → 11, and 2, under the chip "state is any of running, waiting".

### 7. Dashboard (`WT Dash`)

- **Panels:**
  - State counts;
  - Inherit ("Showing 8 of 13.");
  - Running only;
  - Throughput;
  - Timeline ("Showing 10 of 11 in this range. 2 tasks outside this
    range.");
  - Nothing ("Nothing here right now.").
- **Each `Open` goes to the panel's own question:**
  - `/?f=repo:any:acme/widgets` for the inheriting panels;
  - `/?f=state:any:running`;
  - `/?q=zzzznothing` (0 rows);
  - `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`.
- **State tiles link to lists of the same size:** proposed 3, waiting 2,
  running 2, asked 3 (`awaiting_reply|awaiting_submit`), failed 0,
  merging 1 (`completed`). The running tile is bug 2.
- **Search.** `pagination` wrote
  `?q=pagination&f=repo:any:acme/widgets`. The inheriting panels' links
  and the metrics link took the search; the own-query panels did not
  change.
  - The narrowing survived a reload.
  - Clearing the search went back to `?f=repo:any:acme/widgets`.
- **Reorder:**
  - Keyboard: `Move panel 6, “Timeline”` → the first slot sent `PATCH
    /api/boards/board-9 {"panels":[…]}`. Focus moved to `Move panel 1,
    “Timeline”`.
  - Mouse: panel 3's handle (State counts) dropped on panel 1 put it
    first.
  - After a reload: State counts / Timeline / Nothing / Inherit /
    Running only / Throughput.
- **Loading and error.** With metrics delayed then 500, Throughput read
  "Loading…", then "The metrics report could not be loaded."
- **Empty.** `WT Empty Dash`: "This dashboard has no panels yet. “Add
  panel” files the task that adds one."

### 8. Report

- **Seeded `Report`** (no sections named) drew Throughput, Latency,
  Capacity, Backlog and GitHub CI. It **omitted every silent section**.
- **`WT Report All`** names all 13. Unstubbed, it accounts for each silent
  one in a line: "Tool use: nothing to report over this window.", and
  the same for Prompts, CI waits, CI queueing, Scheduling, Sandboxes,
  Merge queue and Agents.
- **Window** (`combobox "Window"`):
  - 90 days wrote `?window=90d` and drew `· 90d 0h`. It survived a
    reload (the picker read "Last 90 days").
  - `?window=bogus` fell back to "Last 7 days" and dropped the parameter.
  - Refresh issued one more `/api/metrics` request.
- **`WT Report Widgets`** ("Measured over the tasks in acme/widgets."),
  metrics stubbed (§Stubs 3):
  - It drew Waiting work, Throughput, Tool use ("…over the 2 attempts
    that recorded it", with the run_command/edit_file table) and Agents.
  - Keyboard: `Move section 4` → the first slot made Agents first. Focus
    moved to `Move section 1, “Agents”`, and the `PATCH` sent `orderKey
    -1048575`.
  - Mouse: section 3 dropped on section 1 made Throughput first.
  - After a reload: Throughput, Agents, Waiting work, Tool use.
- `?q=retry` on it printed "A report is measured over whole
  distributions, so the rest of this question is not in force: the
  search for "retry"."
- **Backlog chips link to lists of the same size:** proposed 3 → 3,
  waiting 4 → 4, running 2 → 2, asked 1 → 1 (via
  `f=state:any:awaiting_reply&f=completed:empty:`). "Oldest waiting
  task: task 2" links to `/tasks/2`.
- **Failed Refresh.** With metrics stubbed to 500, Refresh raised the
  alert "synthetic metrics 500".

### 9. Destructive actions

- No board page and no part of `/boards` has a delete or remove control
  (checked by role on `/boards` and boards 1–14). Boards are removed
  through `grain/config`.
- The column editor's `Remove <col>` acts only on this browser's draft,
  behind the unsaved-changes confirm.
- The throwaway "Add a board" task 21 filed in §2 was closed with its
  page's `Close` button (`POST /api/tasks/21/close
  {"close_pull_request":false}`). It went to `closed` with no
  confirmation, which fits Close being reversible. Both stores were
  discarded afterwards.

### 10. Phone, console and network

- At 390×844, `/boards` and all 14 board pages had `scrollWidth ==
  innerWidth`.
- There were no uncaught page errors. The only errors were expected:
  - the pre-sign-in `401` on `/`;
  - `404 /api/repos/acme/widgets/github-branches` when a task overlay
    opens without `-github-sim`;
  - `ERR_ABORTED` `/api/tasks` polls cut off by navigation;
  - the synthetic 500s.

## Bugs found

1. **A list sorted by state split the asked tasks around the failed
   ones.** (Fixed here.)
   - Reproduction: `/?sort=state:asc&f=state:any:awaiting_reply|awaiting_submit|failed`
     (or `WT List Sorted`) with one failed task. Rows read
     awaiting_reply 4, 7 / **failed 21** / awaiting_submit 6, 14–19. The
     sidebar, the Kanban board (Asked 9, then Failed 1) and the
     timeline's state lanes all show the two asked states as one Asked,
     ahead of Failed.
   - Cause: `stateRank` (`ui/src/taskAttributes.js`) ranked by
     `STATE_ORDER`. That is `model.StateOf`'s precedence (`…,
     awaiting_reply, failed, awaiting_submit, …`), the same mistake
     2026-11-03 fixed in the timeline's lane order.
   - Fix: rank by `DISPLAY_STATE_ORDER`, with Asked opened back out into
     its two states side by side. The State attribute's `order` (which
     `pkg/cli`'s vocabulary test reads) is unchanged.
   - After, on 18702: proposed…, running, awaiting_reply ×2,
     awaiting_submit ×7, **failed**, merging (completed).
   - Coverage:
     - `taskFilters.test.js` "keeps the two asked states together, ahead
       of failed" (asc and desc);
     - e2e `saved-boards-walkthrough.spec.js` 5e, with the `/api/tasks`
       failed stub.

     Both fail without the fix: the e2e failed against a bundle built
     without it ("Expected: < 2, Received: 9").
   - Not fixed here: `grain list --sort state` has the same split
     (`pkg/cli/taskattributes.go` `taskStates`). That was filed as a
     follow-up.
2. **A dashboard's running count was named "2 Running Running".** (Fixed
   here.)
   - Reproduction: `/boards/board-9`. The state tile `a.panel-state` for
     Running has the accessible name "2 Running Running", where every
     other tile reads e.g. "3 Proposed".
   - Cause: the tile draws `StateDot` (the grain mark, `role="img"
     aria-label="Running"`) beside the column's own title text.
   - Fix (`PanelBoard.jsx`): the mark is wrapped in an `aria-hidden`
     span, keeping its tooltip. The wrapper is rendered only when the
     mark draws, so the other tiles gain no flex gap.
   - After, at 1440×1000 and 390×844 in light and dark, on 18702:
     - the tile reads "2 Running";
     - every other tile's words start 11 px from its tile's left edge,
       the same as before the fix;
     - Running's words start at 36 px, as before;
     - the mark's vertical centre is within 1 px of the text's;
     - no tile overflows, and the page does not scroll horizontally.

     The before/after screenshots (`make ui-screenshots`-style, via
     `e2e/screenshot.mjs` against each server, pages `/boards/board-9`
     and `/boards/board-14`) were taken but could not be looked at here
     (no Gemini key); the bounding boxes above are what was checked.
   - Coverage:
     - `PanelBoard.test.jsx` "names a running count by its column title
       alone";
     - e2e `saved-boards-walkthrough.spec.js` 6e (accessible name, and
       the equal title offsets).

     Both fail without the fix.

Runs:
- `vitest`: 3857 tests passed in 246 files.
- Playwright `saved-boards-walkthrough`, `board`, `boards`, `gantt`,
  `report-backlog-links` and `boards_workflows`: 60 passed.

## Observations (not bugs)

- "Attempts per completion" reads 0.00 beside 3 completed tasks. That is
  right for the demo: its completed tasks carry no runs
  (`pkg/metrics/metrics.go` counts the runs of the tasks completed in
  the window).
- A failed report Refresh shows the bare server message ("synthetic
  metrics 500"). The dashboard's metrics panel shows "The metrics report
  could not be loaded." with neither the reason nor a retry. Both are
  readable; they are only inconsistent.
- The phone move picker for task 11 offered two slots both named "Move
  here, above Add a board, in Waiting", because tasks 20 and 21 share a
  title. The slot names carry no task number to tell them apart.
- `grain demo -access-code-file` reads the file at sign-in, not only at
  start. Overwriting the file with another demo's code changed the live
  code of the first server. This was found by accident and is harmless
  for a throwaway demo.
- Still true from earlier runs:
  - a board created from `/boards` says `query: <see below>`;
  - picking a report window replaces the history entry.
