# Saved boards and task visualizations walkthrough — 2026-10-29

## Commit and environment

- **Commit tested:** `425375a5` (`main`, "Merge pull request #132"), on
  branch `grain/049f1e09/task-1755`. The fix below was then re-verified on
  `6bb95646`.
- **Date:** 2026-10-29, 09:30–10:05 UTC.
- **Server:** the real `grain demo`, built from `./cmd/grain` after
  `make frontend` (the production Vite bundle embedded from
  `pkg/ui/static`). It ran over its own throwaway embedded store with
  `-addr 127.0.0.1:18433 -open=false -access-code-file <file>`.
  `-sandbox-port-base` is not a `grain demo` flag; the demo refuses it.
- **Browser:** Playwright Chromium (`/var/cache/playwright`,
  chromium_headless_shell-1234), driven by ad-hoc scripts through visible
  controls. Sign-in went through the "Access code" page. Desktop was
  1440×1000. Phone was 390×844 with `isMobile` and `hasTouch`.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so
  `LOOK=1` was not used and no Gemini-backed tooling ran.

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`.** The demo seeds no
   proposal held for approval, and the UI cannot file one: only an
   agent's `propose_task` call with `hold_for_approval` can. Task 20 was
   filed with the fields that call sends: `proposedBy: "8"`,
   `holdForApproval: true` and a `proposalKey`. Task 8 is the demo's
   running auto-merge task. A first attempt proposed from task 3 (task 19)
   correctly did *not* get the tag, because task 3 is not an auto-merge
   job and its permission would not have queued the proposal anyway.
   Both tasks were closed through the UI at the end of the run.
2. **Stand-in boards, created over `POST /api/boards` and deleted at the
   end.** The product's own creation path files a task against
   `grain/config` for an agent to carry out, and `grain demo` runs no
   agent. That contract was exercised end to end first (§2). Seven
   boards were then created directly to cover every presentation:
   - `WT List` (`list`, `f=state:any:running`)
   - `WT Kanban` (`kanban`, own columns `Todo`/`Doing`/`Talking`,
     `f=repo:any:acme/widgets`)
   - `WT Gantt` (`gantt`, `q=retry`)
   - `WT Dash` (`panels`; six panels: `states`, an inheriting `list`, a
     `list` with its own query and `third` width, `metrics` 30d, `gantt`
     24h full width, and an empty `list`)
   - `WT Report` (`report`, four named sections, one titled, query
     `f=repo:any:acme/widgets&q=ignored`)
   - `WT Tree` (`tree`, `q=pagination`)
   - `WT Empty Dash` (`panels`, no panels)

   They were filed in the folders `WT1755` and `Ops`. All seven were
   deleted with `DELETE /api/boards/board-5..11` (each `204`), leaving
   `board-1`..`board-4`.
3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*`: an 800 ms delay, then `500
     {"error":"synthetic metrics 500"}`, for the loading and error
     states.
   - `**/api/metrics*`: unscoped requests delayed 2 s, to look for a
     stale-response overwrite.
   - `GET **/api/tasks`: `503`, for what boards show when the task list
     cannot be read.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- Rail `Boards 4` (later 11) opened `/boards`. The header count matched
  the rail. The four seeded boards were listed in order with kind chips
  and descriptions: Board/Kanban, Timeline/Timeline, Report/Report,
  Tree/Tree. The index offers no search or sort box (0 inputs), which is
  by design.
- With the stand-ins present: unfiled boards came first, then folders
  `Ops` and `WT1755` alphabetically, each ordered by `orderKey`. A board
  with no query shows "every task" as its description.
- History: link `WT Dash` → `← Boards` → link `WT Tree` → Back → Back →
  Forward → Reload all landed on the expected URLs and headings. Typing
  in a board's search box *replaces* the history entry rather than
  pushing one, so Back afterwards leaves the board.
- `/boards/nope` fell back to `/boards` with the alert `no board "nope"`.
  A cold deep link to `/boards/board-8` restored the board's own
  narrowing in the URL (`?f=repo:any:acme/widgets`).
- Phone, tap: picked up `WT Empty Dash` with `Move WT Empty Dash`; 12
  slots were offered, including `outside every folder` and `at the end of
  WT1755`. Tapping `Move here, at the end of WT1755` moved it, and it
  stayed there after a reload.

### 2. Asynchronous board creation

- From `/?q=pagination&f=repo:any:acme/widgets`, `Create options` →
  `New board` opened `/new`, targeted at `grain/config` and prefilled
  "Add a board". The description carried the query
  `q=pagination&f=repo:any:acme/widgets`, every kind, and where boards
  live (`tables/board.json`). `Create task` returned `POST /api/tasks 201`
  for task 21: `queued`, on `grain/config`, transitions proposed → queued
  at the same instant. `/api/boards` still held 11 boards; the board
  appears only when an agent's pull request lands.
- `/boards` header `New board` gave the same form with `query: <see
  below>`, since nothing was narrowed. It filed task 22, and its detail
  overlay rendered the full prompt.
- `Edit board` on a report board opened `Change the board "WT Report"`.
  `Add panel` on a dashboard opened `Add a panel to the dashboard "WT
  Dash"`.
- The generic New board prompt does not mention
  `tables/board_panel.json`. That is deliberate: the panel note is added
  only when the requested kind is `panels`, and
  `boards.test.js` ("says nothing about panels in a prompt for a board
  that has none") pins it.

### 3. List

- `WT List` (`/boards/board-5?f=state:any:running`) showed chip `State is
  any of Running` and exactly tasks 3 and 8.

### 4. Kanban

- Seeded `Board`: columns Proposed (1), Waiting (2), Running (2),
  Asked (9), Failed (0, "Nothing here."), and **Merging** (1). The
  merge-queue column reads "Merging". "2 tasks in no column (Deferred,
  Closed)."
- Task 11 shows the blocked state oval (`badge badge-blocked`, `Blocked
  on: #8 (running)`). Held task 20 shows the chip **"held for approval by
  8"** (`.chip-held`). Proposed and Merging cards have disabled drag
  handles.
- Clicking a card's title opened `/tasks/2` as an overlay, and Escape
  returned to `/boards/board-1`.
- `Columns` → `Board columns`: renamed column 4 to "Needs me", moved
  Merging left, and added "Later" with state Deferred. "Off the board"
  updated to "Closed." only. Escape with unsaved edits raised the native
  confirm "You have unsaved changes here. Leave and discard them?"
  (dismissed). `Cancel` discards without asking, as the component
  documents. After `Save`, the columns read proposed, waiting, running,
  needs me, merging, failed, later, and stayed that way after a reload
  (`localStorage['grain.board.columns']`). Task 9 appeared in Later.
- Card reorder within Waiting:
  - Keyboard: `Move task 11` → Enter → `Move here, above Fix flaky retry
    test, in Waiting` → Enter sent `POST /api/tasks/reorder 200`. Focus
    stayed on `Move task 11`. The order flipped in place within ~300 ms
    and survived a reload. Ten alternating keyboard moves all persisted.
  - Mouse: dragging the `Move task 2` handle onto the other card swapped
    them, and the result persisted.
  - Phone: tapping a handle showed "Moving 1 task — choose where it goes"
    with 3 slots, and Cancel put it down.
- `WT Kanban` with its own columns: Todo 5, Doing 2, Talking 3, "3 tasks
  in no column (Merging, Deferred, Closed)". The counts match the
  `acme/widgets` tasks. The browser `Columns` editor is not offered on a
  board that carries its own columns.

### 5. Timeline / gantt

- Seeded `Timeline`. Range options: last 24 hours, 7 days, 30 days, 90
  days, everything. Lanes: no lanes, by repo, by state, by author.
  Setting Last 24 hours / By repo drew lanes `acme/widgets`,
  `grain/config` and `No repo`, said "3 tasks outside this range.", and
  drew one today marker (`.gantt-now` at `left: 100%`).
- Both settings survived a reload and a fresh tab on the same URL. They
  are kept in the browser, not the URL, as the board prompt says ("its
  range and its grouping are the reader's own browser setting").
- Task 11's row is labelled "Blocked on: #8 (running)" with bar state
  "Waiting, blocked". Clicking a title link, clicking a bar, and Enter on
  a focused title each opened the task overlay (`/tasks/3`, `/tasks/2`).
- `WT Gantt` (`?q=retry`) showed only the six matching tasks in range.

### 6. Tree

- Seeded `Tree`: task 12 (Director Session) holds 1, 3 and 8, and 8
  holds 11. PR links render beside their tasks.
- `WT Tree` (`?q=pagination`) narrowed to 8 → 11, and the narrowing
  survived a reload. Changing the search to `retry` rewrote the URL and
  survived a reload. Reopening the board without a query restored its
  own `q=pagination`. Clicking `11` opened `Task 11: Extract pagination
  query helper`. `/boards/board-4?f=state:any:running` narrowed to 8 and
  3.

### 7. Dashboard

- `WT Dash` scoping:
  - States 13: tiles 3/2/2/3/0/1, plus "2 in no column".
  - Inherit 13: "Showing 8 of 13."
  - Running only 2: its own query.
  - Metrics: 14 filed / 3 completed.
  - Gantt 13: "Showing 10 of 11 in this range. 2 tasks outside this
    range."
  - Nothing 0: "Nothing here right now."
- Whole-board search `pagination` changed every inheriting panel to 2
  and left the panels with their own query alone. `Keep` appeared only
  on the four inheriting panels. The narrowing survived a reload.
  `Clear` ("Clear the search and every filter") dropped the board's own
  condition too, landing on the list default
  `f=state:none:deferred|closed`.
- Panel reorder:
  - Keyboard: `Move panel 6` → `Move here, above panel 1` sent
    `PATCH /api/boards/board-8 200`. Focus moved to `Move panel 1,
    “Metrics”` and the order persisted across a reload.
  - Mouse: dragging a panel header onto another panel's header reordered
    it and persisted (four consecutive drags, each with its own PATCH).
    Synthetic drags onto a panel below the fold do nothing, because
    HTML5 drag cannot auto-scroll under Playwright. That is a harness
    limit, not a product fault.
- Links out:
  - `Open` on Running only → `/?f=state:any:running`.
  - Merging tile → `/?f=repo:any:acme/widgets&f=state:any:completed`.
    The rail's Merging entry also reads state `completed`.
  - Metrics `Open` → `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`.
  - A panel row → `/tasks/2`.
- `WT Empty Dash` reads "This dashboard has no panels yet. “Add panel”
  files the task that adds one."
- With metrics stubbed to 500, the Metrics panel read "The metrics report
  could not be loaded." while the other panels rendered normally.

### 8. Report

- `WT Report`: "Measured over the tasks in acme/widgets." It also says,
  of the search for "ignored", that "the rest of this question is not in
  force". Sections in order: Throughput, Waiting work (titled, over
  Backlog), Prompts, and Merge queue. The last two read "<Section>:
  nothing to report over this window."
- Window: last 24 hours / 7 / 30 / 90 days. 30 days set `window=30d` and
  survived a reload.
- Section reorder:
  - Keyboard: `Move section 4` → above section 1. Focus moved to `Move
    section 1, “Merge queue”`.
  - Mouse: dragged section 2 above section 1.
  - Phone: tap pickup, 3 slots.
  All three persisted across a reload.
- `Oldest waiting task: task 2` opened `/tasks/2`.
- With metrics stubbed to 500: the progressbar showed, then the alert
  "The report could not be loaded (synthetic metrics 500). Refresh to try
  again." After removing the stub, the report loaded cleanly.
- Seeded `Report` (no sections named, so the whole report): **bug 1**
  below.

### 9. Destructive actions

- Neither `/boards` nor any board page exposes a delete control; boards
  are removed through `grain/config`.
- Throwaway tasks 19, 20, 21 and 22 were closed from their overlay's
  `Close` button. Each went straight to `closed` with no confirmation.
  Close is reversible with Reopen.

### 10. Phone width, console and network

- At 390×844 touch, `/boards` and all 11 board pages had
  `scrollWidth <= innerWidth`.
- No uncaught page errors. The console and network errors seen were all
  expected:
  - the initial `401` on `/` before sign-in;
  - `404 /api/repos/<repo>/github-branches` when a task overlay opens on
    a demo started without `-github-sim`;
  - the synthetic 500/503s.
- With `GET /api/tasks` stubbed to 503, every board drew zero counts.
  After a few seconds the app-wide status banner said "This deployment
  cannot be reached … retrying automatically" on `/`, kanban and
  dashboard alike, so the failure is not silent.

## Bugs found

### 1. Silent report sections stayed in the report's list (fixed)

- **Reproduction:** on a fresh `grain demo`, open `/boards/board-3`, the
  seeded Report with no sections named. Count
  `ul.report-sections > li`: 13 items, 8 of them with no text and 0 px
  tall. A screen reader announces "list, 13 items" and steps through
  eight blank entries. The Metrics pane draws the same component the
  same way.
- **Cause:** `ReportSection` (`ui/src/components/ReportBoard.jsx`) reads
  whether its body drew anything off the DOM. It used that only to print
  the "nothing to report" note on boards that *name* the section, and
  left the empty `<li>` in place everywhere else.
- **Fix:** the `<li>` is `hidden` while its body is empty and the board
  did not name the section. Named sections are unchanged.
- **Regression coverage:**
  - vitest `ReportBoard.sections.test.jsx` "leaves a silent section out
    of the list's items". It fails without the fix.
  - e2e `boards.spec.js` "scopes a report board to the repo its query
    names" now asserts that every list item has text and that the hidden
    ones exist.
- **Verified:** `npx vitest run ReportBoard.sections.test.jsx
  ReportBoard.test.jsx` (76 passed) and `npx playwright test
  e2e/boards.spec.js -g "report board"` (2 passed). `make
  ui-screenshots PAGES=/boards/board-3,/boards` was run before and after
  the fix at desktop and phone, light and dark. Pixel diffs were limited
  to the timestamp and count rows, so there is no visual change.

## Observations, not bugs

- A cold load of a repo-scoped report or dashboard first requests the
  unscoped `/api/metrics?window=…`, then the scoped one. Navigating
  within the app sends only the scoped request. Delaying the unscoped
  response by 2 s did not overwrite the scoped numbers (13 filed, against
  21 deployment-wide), so stale responses are discarded. The cost is one
  wasted whole-deployment metrics computation per cold load.
- A saved kanban board's page repeats an inner `h2` "Board" under the
  board's own `h2`. A list board shows the list's `h2` "All tasks" even
  when the board is narrowed to running tasks.
