# Saved boards and task visualizations walkthrough — 2026-10-30

## Commit and environment

- **Commit tested:** `30c78723` (`main`, "Merge pull request #210"), on
  branch `grain/049f1e09/task-1901`. The fix below was re-verified on
  the same tree plus the fix commit.
- **Date:** 2026-10-30, 11:30–12:25 UTC.
- **Server:** the real `grain demo`, built from `./cmd/grain` after
  `make frontend` (the production Vite bundle embedded from
  `pkg/ui/static`). It ran over its own throwaway embedded store with
  `-addr 127.0.0.1:18433 -open=false -access-code-file <file>`. A second
  demo built with the fix ran on `127.0.0.1:18434` for before/after
  comparison.
- **Browser:** Playwright Chromium (`/var/cache/playwright`,
  chromium_headless_shell-1234), driven by ad-hoc scripts (not
  committed) through visible controls. Sign-in went through the "Access
  code" page. Desktop was 1440×1000. Phone was 390×844 with `isMobile`
  and `hasTouch`.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so
  `LOOK=1` was not used and no Gemini-backed tooling ran.
- **Build note:** a `make frontend` run straight after a full `npm ci`
  failed once with `Rollup failed to resolve import
  "micromark-util-subtokenize"` (the `npm ci --omit=dev` it runs left an
  inconsistent tree). Running `npm run build` after a full `npm ci`
  built cleanly. Not a product fault.

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`.** The demo seeds no
   held proposal, and only an agent's `propose_task` call with
   `hold_for_approval` can file one. Task 20 was filed with the fields
   that call sends: `proposedBy: "8"`, `holdForApproval: true` and a
   `proposalKey`. It was closed from its overlay at the end.
2. **Stand-in boards, created over `POST /api/boards`.** The product's
   own creation path files a task against `grain/config` for an agent,
   and `grain demo` runs no agent. That contract was exercised first
   (§2). The stand-ins:
   - `WT List` (`list`, `f=state:any:running`, folder `WT1901`)
   - `WT Kanban` (`kanban`, own columns `Todo`/`Doing`/`Talking`,
     `f=repo:any:acme/widgets`, folder `WT1901`)
   - `WT Gantt` (`gantt`, `q=retry`, folder `Ops`)
   - `WT Dash` (`panels`, `f=repo:any:acme/widgets`, folder `Ops`; six
     panels: `states`, an inheriting `list` "Inherit", a `list` "Running
     only" with its own query at `third` width, `metrics` 30d, `gantt`
     24h at `full` width, and a `list` "Nothing" over `q=zzzznothing`)
   - `WT Report` (`report`, `f=repo:any:acme/widgets`, four sections:
     throughput, backlog titled "Waiting work", prompts, merges)
   - `WT Tree` (`tree`, `q=pagination`, no folder)
   - `WT Empty Dash` (`panels`, no panels, no query)

   A first `WT Kanban` attempt naming the state `asked` was refused with
   `400 column "Talking" names unknown state "asked"` (the stored name is
   `awaiting_reply`). Both stores were throwaway and were discarded.
3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*`: delayed 1–1.5 s, then `500
     {"error":"synthetic metrics 500"}`, for loading and error states.
   - `GET **/api/tasks`: `503`, for what a board shows when the task list
     cannot be read.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- Rail `Boards 4` (11 with the stand-ins) opened `/boards`. The header
  count matched the rail. Seeded boards in order with kind chips and
  descriptions: Board/Kanban, Timeline/Timeline, Report/Report,
  Tree/Tree. The index has no search or sort control (0 inputs).
- Unfiled boards come first, then folders `Ops` and `WT1901`
  alphabetically. A board with no description shows its query, and one
  with neither shows "every task".
- History: `WT Dash` → `← Boards` → `WT Tree` → Back → Back → Forward →
  Reload landed on `/boards/board-10?q=pagination`, `/boards`,
  `/boards/board-8?f=repo:any:acme/widgets`, `/boards`, `/boards`.
- `/boards/nope` fell back to `/boards` with the alert `no board "nope"`.
  A cold `/boards/board-12` restored `?f=repo:any:acme/widgets`.
- The rail's `Boards` entry opens the index. Customize sidebar offers
  pinning; the walkthrough did not pin a board.
- Index moves:
  - Keyboard: `Move WT Tree` → Enter offered 12 slots, including `at the
    end of Ops` and `at the end of WT1901`. Choosing `at the end of Ops`
    sent `PATCH /api/boards/board-10 {"folder":"Ops",…}`. Focus stayed on
    the handle and the move survived a reload.
  - Mouse: **bug 1** below. Dragging a board in the first section
    (Report onto Timeline) worked. Any board below a section boundary
    could not be dragged at all.

### 2. Asynchronous board creation

- From `/?q=pagination&f=repo:any:acme/widgets`, `Create options` →
  `New board` opened `/new` with title "Add a board" and a description
  carrying the query `q=pagination&f=repo:any:acme/widgets`, every kind,
  and `tables/board.json`. `Create task` returned `POST /api/tasks 201`
  for task 21: `queued`, on `grain/config`. `/api/boards` still held 11
  boards.
- `/boards` header `New board` gave `query: <see below>` and filed task
  22 (`queued`, `grain/config`).
- `New board` from inside a board carries that board's kind and query:
  gantt `q=retry`, kanban, tree `q=pagination`, panels and report each
  `f=repo:any:acme/widgets`. `Edit board` opens `Change the board "<name>"`,
  listing the board's columns, panels or sections in order. `Add panel`
  opens `Add a panel to the dashboard "WT Dash"`, listing the six panels
  with their order keys.

### 3. List

- `WT List` showed the chip `State is any of Running` and tasks 3 and 8.

### 4. Kanban

- Seeded `Board`: Proposed 3, Waiting 4, Running 2, Asked 9, Failed 0,
  **Merging** 1. Footer: "2 tasks in no column (Deferred, Closed)." The
  merge-queue column reads "Merging".
- Task 11 has the blocked state oval (`.badge-blocked`, `img "Blocked
  on: #8 (running)"`). Held task 20 has the chip **"held for approval by
  8"**. The Merging card's handle (task 5) is disabled.
- Clicking a card title opened `/tasks/2` as an overlay. Escape returned
  to `/boards/board-1`.
- `Edit columns` → `Board columns`: renamed column 4 to "Needs me", moved
  Merging left, and added "Later" with state Deferred. "Off the board"
  then read "Closed." Escape on the dialog with unsaved edits raised the
  native confirm "You have unsaved changes here. Leave and discard
  them?" (dismissed; the dialog stayed). After `Save` the columns read
  proposed 3, waiting 4, running 2, needs me 9, merging 1, failed 0,
  later 1, and the same after a reload
  (`localStorage['grain.board.columns']`).
- Card reorder within Waiting:
  - Keyboard: `Move task 22` → Enter offered 3 slots → `Move here, above
    Fix flaky retry test, in Waiting` sent `POST /api/tasks/reorder`.
    Focus stayed on `Move task 22`, and the order survived a reload.
  - Mouse: dragging `Move task 11` onto task 2's card swapped them and
    persisted.
  - Phone: tapping `Move task 21` showed "Moving 1 task — choose where it
    goes" with 3 slots. Tapping the first moved it, and it persisted.
  - Dragging a card into another column sends nothing, by design
    (`TaskBoard.jsx`, "Why a card cannot be dragged into another
    column").
- `WT Kanban` (own columns): Todo 5, Doing 2, Talking 2, "4 tasks in no
  column (Asked, Merging, Deferred, Closed)". No `Edit columns` on a
  board that carries its own columns.

### 5. Timeline / gantt

- Seeded `Timeline`: Range offers last 24 hours / 7 / 30 / 90 days /
  everything. Lanes offers no lanes / by repo / by state / by author.
  Last 24 hours + By repo drew lanes `acme/widgets` 11, `grain/config` 2
  and `no repo` 5, said "3 tasks outside this range.", and drew one today
  marker (`.gantt-now` at `left: 100%`).
- Both settings survived a reload and a fresh tab. They live in the
  browser, not the URL, as the board prompt says.
- Task 11's row reads "Blocked on: #8 (running)", with bar state
  "Waiting, blocked". Clicking a bar opened `/tasks/2`, and Enter on a
  focused title opened `/tasks/3`.
- Search `retry` wrote `?q=retry` (6 bars) and survived a reload.

### 6. Tree

- Seeded `Tree`: task 13 (Director Session) holds 1, 3 and 8. Task 8
  holds 11 and the held proposal 20, which task 8 filed. PR links sit
  beside 5, 6 and 8.
- `WT Tree` (`?q=pagination`) showed 8 → 11, and the same after a
  reload. Clicking `11` opened `Task 11: Extract pagination query helper`.
  Escape returned to `/boards/board-10?q=pagination`.
- `/boards/board-4?f=state:any:running` narrowed to 8 and 3.

### 7. Dashboard

- `WT Dash` counts: State counts 13 (tiles 3/2/2/3/0/1 and "2 in no
  column"); Inherit 13 ("Showing 8 of 13."); Running only 2; Throughput
  14 filed / 3 completed; Timeline 13 ("Showing 10 of 11 in this range. 2
  tasks outside this range."); Nothing 0.
- Search `pagination` wrote `?q=pagination&f=repo:any:acme/widgets`. Every
  inheriting list/states/gantt panel went to 2. The panels with their own
  query did not change. `Keep this narrowing on “…”` appeared only on the
  four inheriting panels, and the narrowing survived a reload.
- The Throughput panel kept 14 under the search. It cannot apply a text
  search, and its tooltip says so (`scopeNote`: "the rest of this
  question is not in force").
- `Clear` (the search and every filter) landed on
  `?f=state:none:deferred|closed`.
- Panel reorder:
  - Keyboard: `Move panel 6` → `Move here, above panel 1` sent
    `PATCH /api/boards/board-8`. Focus moved to `Move panel 1, “Nothing”`,
    and the order persisted.
  - Mouse: dragging panel 2's handle onto panel 1's swapped them,
    persisted.
- Links out: Running tile → `/?f=repo:any:acme/widgets&f=state:any:running`.
  Merging tile → `…&f=state:any:completed`. Metrics `Open` →
  `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`. A row
  opened `/tasks/2`, and Escape returned to the board's own URL.
- Metrics stubbed to 500: the panel read "Loading…", then "The metrics
  report could not be loaded." The other panels drew normally.
- `WT Empty Dash`: "This dashboard has no panels yet. “Add panel” files
  the task that adds one.", under the default chip `State is none of
  Deferred, Closed`.
- `GET /api/tasks` stubbed to 503: the app-wide banner said "This
  deployment cannot be reached … retrying automatically", so the failure
  is not silent.

### 8. Report

- `WT Report`: "Measured over the tasks in acme/widgets." Sections in
  order: Throughput, Waiting work (titled, with the Backlog body under
  its own heading), and "Prompts: nothing to report over this window." /
  "Merge queue: nothing to report over this window."
- Window offers 24 hours / 7 / 30 / 90 days. 30 days wrote `window=30d`
  and survived a reload ("· 30d 0h").
- Search `ignored` printed "A report is measured over whole
  distributions, so the rest of this question is not in force: the
  search for "ignored"."
- Section reorder:
  - Keyboard: `Move section 4` → above section 1 sent `PATCH`, and focus
    moved to `Move section 1, “Merge queue”`.
  - Mouse: dragged section 3 above section 1.
  Both persisted across a reload.
- `Oldest waiting task: task 2` opened `/tasks/2`.
- Metrics stubbed to 500: a progressbar, then the alert "The report could
  not be loaded (synthetic metrics 500). Refresh to try again."
- Seeded `Report`: 13 list items, 5 visible, 0 visible-and-empty, so
  2026-10-29's fix holds.
- The backlog reads Waiting 3 / Asked 1 where the dashboard reads 2 / 3.
  Both are right by design: the report counts the queued Director
  Session, and it leaves out awaiting-submit and ended awaiting-reply
  tasks (`pkg/metrics/metrics.go`, `Backlog.ByState`).

### 9. Destructive actions

- No board page and no part of `/boards` exposes a delete control.
  Boards are removed through `grain/config` (or `DELETE /api/boards/<id>`,
  which the e2e suite uses for cleanup).
- Throwaway tasks 20, 21 and 22 were closed from their pages' `Close`
  button. Each went to `closed` with no confirmation. That is consistent
  with Close being reversible through Reopen.

### 10. Phone width, keyboard, console and network

- At 390×844 touch, `/boards` and all 11 board pages had
  `scrollWidth == innerWidth`. Kanban columns and gantt ticks scroll
  inside their own containers.
- Tab order on a kanban board runs rail → toolbar (Edit board, New board,
  Columns, search, Filter, Backlog order) → `← Boards` → Edit columns →
  the first column's select-all → each card's handle, checkbox and link.
- No uncaught page errors. The only console and network errors were
  expected:
  - the initial `401` on `/` before sign-in;
  - `404 /api/repos/acme/widgets/github-branches` when a task overlay
    opens on a demo without `-github-sim`;
  - `net::ERR_ABORTED` on an in-flight `/api/tasks` poll cut off by
    navigation;
  - the synthetic 500s and 503s.

## Bugs found

### 1. Boards below the first section could not be dragged by mouse (fixed)

- **Reproduction:** on a fresh `grain demo`, create two boards in a
  folder (`POST /api/boards {"name":"A","folder":"F","kind":"list"}`,
  twice), open `/boards`, and drag either one's handle (or the row) onto
  the other or onto any folder heading. The page fires `dragstart` and
  then `dragend` at once: no `dragover`, no `drop`, no PATCH. Playwright's
  `dragTo` hangs. Dragging a board in the first (unfiled) section works.
  The keyboard and tap pick-up always worked.
- **Cause:** `BoardList` (`ui/src/components/BoardList.jsx`) set its drag
  state inside the `dragstart` handler. React rendered synchronously,
  and the `board-drop-end` zone it draws at the end of every section
  pushed every later row down by 2rem. Chromium re-hit-tests the drag
  origin as soon as `dragstart` returns, and cancels the drag when the
  source element is no longer under it. The kanban, dashboard and report
  drags were unaffected, because nothing is inserted above their
  source.
- **Fix:** the drag is marked a tick after `dragstart` (`setTimeout 0`),
  after Chromium's check. A drag that ends before then clears the
  pending start, so nothing stays marked.
- **Regression coverage:**
  - vitest `BoardsPage.test.jsx` "leaves the rows where they are until
    dragstart has returned" (fails without the fix) and "forgets a drag
    that ended before it was marked".
  - e2e `boards.spec.js` "files a board into another folder by dragging
    it": a real Chromium drag of a filed board onto another folder's
    heading, then a check of the stored folder. It times out without the
    fix and passes with it.
  - The three existing jsdom drag tests (`BoardsPage.test.jsx`, and
    `App.test.jsx` "moving a board") now wait for the tick.
- **Verified:**
  - `npx vitest run src/components/BoardsPage.test.jsx src/App.test.jsx
    src/components/TaskBoard.test.jsx`: 118 passed.
  - `npx playwright test e2e/boards.spec.js`: 17 passed.
  - On the fixed demo, `Move WT Kanban` dropped above `WT Dash` sent
    `PATCH … {"folder":"Ops"}`, and a drag from `Ops` onto the `WT1901`
    heading sent `PATCH … {"folder":"WT1901"}`. Both persisted across a
    reload, and no drop zone was left behind.
  - Screenshots with `ui/e2e/screenshot.mjs` of `/boards` and
    `/boards/board-1`, desktop and phone, light and dark, before and
    after. Desktop pixel diffs were 0. Phone diffs were confined to the
    header clock row and the kanban cards' relative times, so there is no
    static visual change.

## Follow-ups and observations

- **Rows jump when a board drag starts (follow-up filed).** With the drag
  now working, its start still draws a 2rem drop zone at the end of each
  section, so every later folder moves down by 2rem per section above it
  (64px for the `WT1901` heading here). A drop aimed at where a row *was*
  lands one row off. With the old binary, a `dragTo` aimed at `WT Dash`
  went nowhere. With the fix, the same `dragTo` filed `WT Kanban` above
  `WT Gantt`. Re-aiming after the jump lands correctly, which is what the
  new e2e does.
- The own-columns kanban's footer says "(Asked, …)" are in no column
  while its "Talking" column shows Asked tasks. `awaiting_reply` and
  `awaiting_submit` share the label "Asked", and only the first was
  given a column. The text is accurate but ambiguous.
- Tree rows link only the task number (`link "11"`); the title beside it
  is plain text.
