# Saved boards and task visualizations walkthrough — 2026-11-01

## Commit and environment

- **Commit tested:** `8688c0cf` (`main`, "Merge pull request #282"), on
  branch `grain/049f1e09/task-2086` with no product change on top.
- **Date:** 2026-11-01, 03:20–03:35 UTC.
- **Server:** the real `grain demo`, built with `go build ./cmd/grain`
  (go1.26.10) after `npm ci && npm run build` in `ui/` (the production
  Vite bundle, embedded from `pkg/ui/static`). It ran over its own
  throwaway embedded store with `-addr 127.0.0.1:18433 -open=false
  -access-code-file <file>`.
- **Browser:** Playwright 1.62.1 Chromium (`/var/cache/playwright`,
  chromium_headless_shell-1234), driven by ad-hoc Node scripts (not
  committed) through visible controls. Sign-in went through the "Access
  code" page. Desktop was 1440×1000. Phone was 390×844 with `isMobile`
  and `hasTouch`. Every script recorded page errors, console errors,
  HTTP ≥ 400 responses, failed requests and every non-GET `/api` call.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so
  no Gemini-backed tooling ran and `LOOK=1` was not used.

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`.** The demo seeds no
   held proposal, and only an agent's `propose_task` call with
   `hold_for_approval` files one. Task 22 was filed with the fields that
   call sends: `proposedBy: "8"`, `holdForApproval: true` and a
   `proposalKey`. It was closed from its page at the end.
2. **Stand-in boards, created over `POST /api/boards`.** The product's
   own creation path files a task against `grain/config` for an agent,
   and `grain demo` runs no agent. That contract was exercised first
   (§2). The stand-ins:
   - `WT List` (board-5, `list`, `f=state:any:running`, folder `WT2086`)
   - `WT Gantt` (board-7, `gantt`, `q=retry`, folder `Ops`)
   - `WT Dash` (board-8, `panels`, `f=repo:any:acme/widgets`, folder
     `Ops`; six panels: `states`, an inheriting `list` "Inherit", a `list`
     "Running only" with its own query at `third` width, `metrics` 30d,
     `gantt` 24h at `full` width, and a `list` "Nothing" over
     `q=zzzznothing`)
   - `WT Tree` (board-10, `tree`, `q=pagination`, no folder)
   - `WT Empty Dash` (board-11, `panels`, no panels, no query)
   - `WT Kanban` (board-12, `kanban`, own columns `Todo`/`Doing`/`Talking`,
     `f=repo:any:acme/widgets`, folder `WT2086`)
   - `WT Report` (board-13, `report`, `f=repo:any:acme/widgets`, four
     sections: throughput, backlog titled "Waiting work", prompts, merges)

   Two first attempts were refused, correctly: a column written with
   `title` instead of `name` (`400 a board column needs a name`), and a
   report section of kind `text` (`400 section 5: kind must be …`; there
   is no prose section kind, only a title on a section). A metrics panel
   posted with `window` instead of `range` was stored without a range
   (unknown JSON fields are dropped); a `PATCH` with `range: "30d"`
   stored it. The store was throwaway and was discarded.
3. **Playwright route stubs, removed after each check:** `**/api/metrics*`
   delayed 1.2 s, then `500 {"error":"synthetic metrics 500"}`, for the
   dashboard's and the report's loading and error states.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- Rail `Boards 4` opened `/boards`. The header count read 4 and the
  seeded boards were in order with kind chips and descriptions:
  Board/kanban, Timeline/timeline, Report/report, Tree/tree. The index
  has no search or sort control (0 inputs).
- With the stand-ins the rail and header both read 9 (then 11). Unfiled
  boards come first, then the folders `Ops` and `WT2086`. A board with no
  description shows its query, and one with neither shows "every task".
- History: `WT Dash` → `← Boards` → `WT Tree` → Back → Back → Forward
  landed on `/boards/board-8?f=repo:any:acme/widgets`, `/boards`,
  `/boards/board-10?q=pagination`, `/boards`, `/boards/board-8?…`,
  `/boards`. Reload kept `/boards`.
- `/boards/nope` fell back to `/boards` with the alert `no board "nope"`.
  A cold `/boards/board-12` restored `?f=repo:any:acme/widgets`.
- Index moves:
  - Keyboard: `Move WT Tree` → Enter offered 12 slots, including `at the
    end of Ops` and `at the end of WT2086`. Choosing `at the end of Ops`
    sent `PATCH /api/boards/board-10 {"folder":"Ops","orderKey":1048576}`.
    Focus stayed on `Move WT Tree`, and the move survived a reload.
  - Mouse: dragging `Move WT Kanban` (in `WT2086`) onto the `Ops` heading
    sent `PATCH /api/boards/board-12 {"folder":"Ops",…}` and survived a
    reload, with no drop zone left behind. 2026-10-30's fix holds.

### 2. Asynchronous board creation

- From `/?q=pagination&f=repo:any:acme/widgets`, `Create options` offered
  `new task / new session / new board`. `New board` opened `/new` with
  the title "Add a board", repo `grain/config`, and a description that
  carries the query `q=pagination&f=repo:any:acme/widgets`, every kind,
  the query encoding, and the `tables/board.json` /
  `board_column.json` / `board_section.json` instructions. `Create task`
  sent `POST /api/tasks` and filed task 20 (`queued`, `grain/config`).
  It returned to the list it came from.
- The `/boards` header `New board` gave `query: <see below>` and filed
  task 21 (`queued`, `grain/config`).
- `/api/boards` still held 11 boards after both. No board was created
  directly, so the asynchronous contract holds.
- `Edit board` on the seeded kanban opened `Change the board "Board"`,
  which lists its fields and says it has no columns of its own. On
  `WT Dash`, `Add panel` opened `Add a panel to the dashboard "WT Dash"`,
  listing the six panels with their kinds, widths, ranges, queries and
  `order_key`s. `Keep this narrowing` on "Inherit" opened `Narrow the
  panel "Inherit" on the dashboard "WT Dash"`, with that panel marked
  `<-- this one`.

### 3. List

- `WT List` showed the chip `State is any of Running` and tasks 3 and 8.
  A row click opened `/tasks/3`, and Escape returned to
  `/boards/board-5?f=state:any:running`. Enter on a focused row link
  opened it too.

### 4. Kanban

- Seeded `Board`: Proposed 3 (with held task 22), Waiting 4, Running 2,
  Asked 9, Failed 0, **Merging** 1. Footer: "2 tasks in no column
  (Deferred, Closed)." The merge-queue column reads "Merging".
- Task 11 has the blocked state oval: `<span class="badge badge-icon
  badge-blocked" role="img" aria-label="Blocked on: #8 (running)">`.
- Held task 22's card carries the chip **"held for approval by 8"**.
- The Merging card's move handle (task 5) is disabled.
- Clicking a card title opened `/tasks/1`. Escape returned to
  `/boards/board-1`.
- `Columns` → `Board columns`: every icon button is named (`Move
  Proposed left`, `Remove Proposed`, …). Renaming column 4 to "Needs me"
  and pressing Escape raised the native confirm "You have unsaved
  changes here. Leave and discard them?". Dismissing it kept the
  dialog. Adding "Later" with state Deferred and saving gave proposed 3,
  waiting 4, running 2, needs me 9, failed 0, merging 1, later 1, footer
  "1 task in no column (Closed).", and the same after a reload.
- Card reorder within Waiting (2, 11, 20, 21):
  - Keyboard: `Move task 21` → Enter offered 3 slots → `Move here, above
    Fix flaky retry test, in Waiting` sent `POST /api/tasks/reorder
    {"ids":["21"],"beforeId":"2"}`. Focus stayed on `Move task 21`, and
    the order survived a reload.
  - Mouse: dragging `Move task 20` onto the first card sent `reorder
    {"ids":["20"],"beforeId":"21"}`. The new order persisted.
  - Phone (touch): tapping `Move task 11` showed "Moving 1 task — choose
    where it goes" with 3 slots. Tapping the first sent `reorder
    {"ids":["11"],"beforeId":"20"}`, and the order persisted.
  - Dragging a card into another column sent nothing, by design.
- `WT Kanban` (own columns): Todo 5, Doing 2, Talking 2, "4 tasks in no
  column (Asked, Merging, Deferred, Closed).". It has no `Columns`
  control.

### 5. Timeline / gantt

- Seeded `Timeline`: Range offers last 24 hours / 7 / 30 / 90 days /
  everything. Lanes offers no lanes / by repo / by state / by author.
- Last 24 hours + By repo drew lanes `acme/widgets` 11, `grain/config` 2
  and `no repo` 5, and said "3 tasks outside this range.". It drew one
  today marker (`.gantt-now`, `left: 100%`).
- Both settings survived a reload (`localStorage['grain.gantt.view'] =
  {"range":"24h","lanes":"state"}` after switching to By state). That
  gave proposed 3, waiting 4, running 2, deferred 1, asked 8. They live
  in the browser rather than the URL, as the board prompt says. A fresh
  browser context starts on the default.
- Task 11's row carries `Blocked on: #8 (running)`, and its bar is
  labelled "Waiting, blocked: waiting for 45m, never dispatched". That
  is the timeline's dependency rendering; it draws no dependency arrows.
- Clicking a bar opened `/tasks/1`. Escape returned. Enter on a focused
  row link opened `/tasks/2`.
- Search `retry` wrote `?q=retry` (6 bars) and survived a reload.
  `WT Gantt` cold-opened on `?q=retry` with the same 6 bars.

### 6. Tree

- Seeded `Tree`: Director Session task 13 holds 1, 3 and 8. Task 8
  holds 11 and the held proposal 22, which task 8 filed. The PR links
  `acme/widgets#101/#102/#104` sit beside 5, 6 and 8. The other tasks
  (including 20 and 21, filed from the UI) are roots.
- `WT Tree` (`?q=pagination`) showed 8 → 11, and the same after a
  reload. Clicking `11` opened `Task 11: Extract pagination query
  helper`, and Escape returned to `/boards/board-10?q=pagination`.
- `/boards/board-4?f=state:any:running` narrowed to 8 and 3 under the
  chip `state is any of running`.

### 7. Dashboard

- `WT Dash` (`?f=repo:any:acme/widgets`):
  - State counts 13: tiles 3/2/2/3/0/1, and "2 in no column".
  - Inherit 13 ("Showing 8 of 13.").
  - Running only 2.
  - Throughput: 14 filed, 3 completed.
  - Timeline 13: "Showing 10 of 11 in this range. 2 tasks outside this
    range."
  - Nothing 0: "Nothing here right now."
- Search `pagination` wrote `?q=pagination&f=repo:any:acme/widgets`.
  - The inheriting states, list and gantt panels went to 2. The panels
    with their own query (Running only, Nothing) did not change.
  - `Keep` appeared on exactly the 4 inheriting panels.
  - The narrowing survived a reload.
  - Throughput kept 14, because it cannot apply a text search.
  - Emptying the box went back to `?f=repo:any:acme/widgets`.
- Links out:
  - State tiles → `/?f=repo:any:acme/widgets&f=state:any:<state>`. Asked
    → `awaiting_reply|awaiting_submit`. Merging → `completed`.
  - Each panel's `Open` → its own question: `/?f=state:any:running`,
    `/?q=zzzznothing`, and `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`
    for the metrics panel.
  - A row opened `/tasks/1`, and Escape returned to the board's own URL.
- Panel reorder:
  - Keyboard: `Move panel 6, “Nothing”` → `Move here, above panel 1`
    sent `PATCH /api/boards/board-8 {"panels":[…]}`. Focus moved to `Move
    panel 1, “Nothing”`, and the order persisted.
  - Mouse: dragging panel 3's handle onto panel 1's put "Inherit" first,
    and the order persisted. The stored order matched the screen.
- Metrics stubbed to 500: the Throughput panel read "Loading…", then
  "The metrics report could not be loaded." The other panels drew
  normally.
- `WT Empty Dash`: "This dashboard has no panels yet. “Add panel” files
  the task that adds one."

### 8. Report

- `WT Report`: "Measured over the tasks in acme/widgets." It shows the
  window span `… · 7d 0h`. Sections in order:
  - Throughput: 13 filed, 3 completed, sparklines.
  - Waiting work: titled, with the Backlog body under its own heading;
    proposed 3, waiting 3, running 2, asked 1.
  - "Prompts: nothing to report over this window."
  - "Merge queue: nothing to report over this window."
- Window offers 24 hours / 7 / 30 / 90 days. 30 days wrote `window=30d`
  and survived a reload.
- Search `ignored` printed "A report is measured over whole
  distributions, so the rest of this question is not in force: the
  search for "ignored"."
- Section reorder:
  - Keyboard: `Move section 4` → above section 1 sent `PATCH
    /api/boards/board-13 {"sections":[…]}`. Focus moved to `Move section
    1, “Merge queue”`.
  - Mouse: dragging section 3 onto section 1 put "Waiting work" first.
  - Both persisted across a reload.
- `Oldest waiting task: task 2` opened `/tasks/2`, and Escape returned to
  the board.
- Metrics stubbed to 500: a progressbar, then the alert "The report could
  not be loaded (synthetic metrics 500). Refresh to try again."
- The seeded `Report` (no query) drew the whole report: throughput,
  latency and the rest.

### 9. Destructive actions

- No board page and no part of `/boards` exposes a delete or remove
  control (checked on the index and on boards 1, 2, 3, 4, 5, 8 and 13).
  Boards are removed through `grain/config`.
- Throwaway tasks 20, 21 and 22 were closed from their pages' `Close`
  button (`POST /api/tasks/<id>/close`). Each went to `closed` with no
  confirmation. That is consistent with Close being reversible through
  Reopen.
- The stand-in boards were left in the throwaway store, which was
  discarded.

### 10. Phone width, keyboard, console and network

- At 390×844 touch, `/boards` and all 11 board pages had
  `scrollWidth == innerWidth` (390).
- Tab order on the kanban board from the header runs `← Boards` → `Edit
  columns` → the first column's select-all → each card's handle,
  checkbox, title link and grant chips → the next column's select-all.
- No uncaught page errors. The only console and network errors were
  expected:
  - the initial `401` on `/` before sign-in;
  - `404 /api/repos/acme/widgets/github-branches` when a task overlay
    opens on a demo without `-github-sim`;
  - `net::ERR_ABORTED` on in-flight `/api/tasks` polls cut off by
    navigation;
  - the synthetic 500s, and the walkthrough's own refused `POST`s and
    `GET /api/boards/<id>` probes (`405`; there is no single-board GET).

## Bugs found

None. Every check above behaved as the board prompts and the previous
walkthroughs describe, including 2026-10-30's fix for dragging a board
below the first section.

## Observations (not bugs)

- **The report's links into its data are thin.** The dashboard's state
  tiles link to the filtered task list, but the report's backlog chips
  ("waiting 3", …) are plain chips. Its task references ("Oldest waiting
  task: task 2", the prompts section's task ids) are `<button>`s
  (`MetricsSections.jsx`, `component="button"`) rather than links. They
  open the overlay but have no `href`, so they cannot be opened in a new
  tab or copied. This is filed as a follow-up rather than changed here,
  because the same sections render on `/system/metrics`.
- Still true from 2026-10-30:
  - The own-columns kanban's footer lists "Asked" among the states in no
    column while its "Talking" column holds Asked (`awaiting_reply`)
    tasks, because `awaiting_submit` shares the label.
  - Tree rows link only the task number.
- `Edit board` on a kanban with no columns of its own does not mention
  the columns the reader's browser is set to. The reader may have
  customized them ("Needs me", "Later") and want them kept.

---

# Second walkthrough, same day — 2026-11-01, 04:20–04:45 UTC

A second run of the same walkthrough (task 2234) against a newer `main`.
It writes its record here because the dated file name is the one the
walkthrough asks for.

## Commit and environment

- **Commit tested:** `96f09c68` (`main`, "Merge pull request #337"), on
  branch `grain/049f1e09/task-2234`. The fix below was built on top and
  checked again in the browser.
- **Server:** the real `grain demo`, built with `go build ./cmd/grain`
  (go1.26.10) after `npm ci && npm run build` in `ui/`. It ran over its
  own throwaway embedded store with `-addr 127.0.0.1:18434 -open=false
  -access-code-file <file>`. It was restarted twice with a fresh store,
  for the rebuilt bundle.
- **Browser:** Playwright 1.62.1 Chromium (`/var/cache/playwright`),
  driven by ad-hoc Node scripts (not committed) through visible
  controls. Sign-in went through the "Access code" page. Desktop was
  1440×1000. Phone was 390×844 (`iPhone 13` device, touch). Every script
  logged page errors, console errors, HTTP ≥ 400, failed requests and
  every non-GET `/api` call.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so no
  Gemini-backed tooling ran and `--look` was not used.

## Stubs and stand-ins

1. **Held proposal** (task 24, later task 20 on the restarted store),
   filed over `POST /api/tasks` with the fields `propose_task`'s
   `hold_for_approval` sends: `proposedBy: "8"`, `holdForApproval: true`
   and `proposalKey`. The demo seeds none, and no agent runs.
2. **Stand-in boards over `POST /api/boards`**, because the product's
   own creation path files a `grain/config` task for an agent, and the
   demo runs no agent. That path was exercised first (§2).
   - `WT List` (board-5, `list`, `f=state:any:running`, folder `WT2234`)
   - `WT Gantt` (board-6, `gantt`, `q=retry`, `Ops`)
   - `WT Dash` (board-7, `panels`, `f=repo:any:acme/widgets`, `Ops`), with
     these panels:
     - `states`
     - list "Inherit"
     - list "Running only" (`f=state:any:running`, third width)
     - `metrics` (30d)
     - `gantt` (24h, full width)
     - list "Nothing" (`q=zzzznothing`)
   - `WT Tree` (board-8, `tree`, `q=pagination`)
   - `WT Empty Dash` (board-9, `panels`, no panels)
   - `WT Kanban` (board-10, `kanban`, own columns Todo
     `proposed|queued` / Doing `running` / Talking `awaiting_reply`,
     `f=repo:any:acme/widgets`, `WT2234`)
   - `WT Report` (board-11, `report`, `f=repo:any:acme/widgets`), with
     these sections: throughput, backlog titled "Waiting work",
     prompts, merges
3. **Playwright route stub** `**/api/metrics*`: delayed 1.2 s, then
   `500 {"error":"synthetic metrics 500"}`. It was used for the
   dashboard's and the report's loading and error states, then removed.

## Steps and outcomes

1. **Boards index, rail, URLs, history.**
   - Rail `boards 4` (a button, not a link) opened `/boards`. It shows
     Board/kanban, Timeline/timeline, Report/report and Tree/tree with
     their descriptions, and no search or sort control.
   - With the stand-ins: rail 11. Unfiled boards come first, then
     `Ops` and `WT2234`. A board without a description shows its query
     (`f=state:any:running`) or "every task".
   - Each seeded board opened from its direct URL. `/boards/nope` fell
     back to `/boards` with the alert `no board "nope"`.
   - Keyboard: `Move WT Tree` → Enter offered 12 slots, among them `at
     the end of Ops`. Choosing it sent `PATCH /api/boards/board-8
     {"folder":"Ops","orderKey":1048576}`. Focus stayed on the handle, and
     the move persisted across a reload.
   - Mouse: dragging `Move WT Kanban` onto the `Ops` heading filed it in
     Ops, and that persisted.
   - History, `WT Dash` → `← Boards` → `WT Tree` → Back, Back, Forward,
     reload: `/boards/board-7?f=repo:any:acme/widgets`, `/boards`,
     `/boards/board-8?q=pagination`, `/boards`, `/boards/board-7?…`,
     `/boards`, then `/boards` again after the reload.
2. **Asynchronous creation.**
   - From `/?q=pagination&f=repo:any:acme/widgets`, `Create options`
     offered `new task / new session / new board`. New board opened
     `/new` with the title "Add a board", repo `grain/config`, and a
     description carrying `query: q=pagination&f=repo:any:acme/widgets`
     and every kind. `Create task` filed a queued `grain/config` task
     and returned to the list it came from.
   - The `/boards` header `New board`, submitted with Enter on a focused
     `Create task`, filed another one (`query: <see below>`) and
     returned to `/boards`.
   - `/api/boards` held 4 boards before and after, so neither path made
     a board directly.
   - `Edit board` on the seeded kanban opened `Change the board "Board"`.
     `Add panel` on `WT Dash` opened `Add a panel to the dashboard "WT
     Dash"`, listing all six panels with kinds, widths, ranges, queries
     and `order_key`s.
3. **List.** `WT List` showed the chip `state is any of running` and
   rows 3 and 8. Enter on a focused row link opened `/tasks/3`, and
   Escape returned to `/boards/board-5?f=state:any:running`.
4. **Kanban.**
   - Seeded `Board` columns: proposed 3 (1, 12 and held 24), waiting 6,
     running 2, asked 9, failed 0, **merging** 1 (task 5, whose handle
     is disabled). Note: "2 tasks in no column (Deferred, Closed)."
   - Task 11 carries `badge badge-icon badge-blocked`, labelled
     `Blocked on: #8 (running)`. The held proposal's card carries the
     chip **"held for approval by 8"**.
   - `Edit columns`: every control is named. `Move Merging left` by
     click and `Move Proposed right` by keyboard (focus stayed on it)
     gave Waiting, Proposed, Running, Asked, Merging, Failed. Saved, the
     board redrew in that order, and the same after a reload.
   - Keyboard card move: `Move task 11` → Enter offered 5 slots; the
     first sent `POST /api/tasks/reorder {"ids":["11"],"beforeId":"2"}`.
     Focus stayed on the handle, and the order survived a reload.
   - Mouse: dragging task 23 onto task 11 sent `{"ids":["23"],
     "beforeId":"11"}`, and 22 onto 21 sent `{"ids":["22"],"afterId":"11",
     "beforeId":"21"}`. Both persisted.
   - Phone tap: `Move task 21` showed "Moving 1 task — choose where it
     goes". Tapping the first slot sent `{"ids":["21"],"afterId":"11",
     "beforeId":"2"}`, and that persisted.
   - A drag into another column sent nothing, by design.
   - A card title opened `/tasks/2`, and Escape returned to
     `/boards/board-1`.
   - `WT Kanban`: Todo 5, Doing 2, Talking 2 (tasks 4 and 7), and no
     `Columns` control. Its note read "4 tasks in no column (**Asked**,
     Merging, Deferred, Closed)." — **bug 1** below.
5. **Timeline.**
   - Range offers last 24 hours / 7 / 30 / 90 days / everything. Lanes
     offers no lanes / by repo / by state / by author.
   - 24 hours + by repo drew lanes acme/widgets 11, grain/config 4 and
     no repo 5, with "3 tasks outside this range." It drew one
     `.gantt-now` marker at `left: 100%`.
   - Task 11's bar reads "Waiting, blocked: waiting for 47m, never
     dispatched". There are no dependency arrows; the blocked state is
     its dependency rendering.
   - Range and lanes survived a reload (`localStorage['grain.gantt.view']
     = {"range":"24h","lanes":"repo"}`). The URL stayed bare.
   - A bar click opened `/tasks/1`.
   - Search `retry` wrote `?q=retry` (6 bars), and it survived a reload.
     `WT Gantt` cold-opened on `?q=retry` with 6 bars.
6. **Tree.**
   - Seeded `Tree`: 13 → 1, 3, 8; and 8 → 11 and the held 24, which
     task 8 filed. The UI-filed tasks are roots.
   - `WT Tree` (`?q=pagination`) showed 8 → 11, the same after a reload.
     Row 11 opened `Task 11`, and Escape returned to
     `/boards/board-8?q=pagination`.
   - `/boards/board-4?f=state:any:running` showed 8 and 3.
7. **Dashboard** (`WT Dash`).
   - Panels: state counts 13 (3/2/2/3/0/1, "2 in no column"), Inherit 13
     ("Showing 8 of 13."), Running only 2, Throughput 14 filed / 3
     completed, Timeline 13 ("Showing 10 of 11 in this range. 2 tasks
     outside this range."), and Nothing 0 ("Nothing here right now.").
   - State tiles link to `/?f=repo:any:acme/widgets&f=state:any:<state>`.
     Asked links to `awaiting_reply|awaiting_submit`, Merging to
     `completed`. Each `Open` goes to the panel's own question, and the
     metrics panel goes to `/system/metrics?…&f=created:within:30`.
   - Search `pagination`: the inheriting panels went to 2, and the
     own-query panels did not change. `Keep` showed on 4 panels. The
     narrowing survived a reload, and clearing the search returned to
     the board's own query.
   - Keyboard: `Move panel 6, “Nothing”` → above panel 1 sent the
     `PATCH` with `orderKey -1048575`. Focus followed to `Move panel 1,
     “Nothing”`.
   - Mouse: dragging panel 3 onto panel 1 put Inherit first.
   - After a reload: Inherit / Nothing / State counts / Running only /
     Throughput / Timeline, matching the stored `orderKey`s.
   - A row opened `/tasks/1`, and Escape returned with the board's query.
   - Metrics stubbed: Throughput read "Loading…", then "The metrics
     report could not be loaded."
   - `WT Empty Dash`: "This dashboard has no panels yet. “Add panel”
     files the task that adds one."
8. **Report** (`WT Report`).
   - "Measured over the tasks in acme/widgets.", with the span `… · 7d
     0h` and sections Throughput, Waiting work, Prompts and Merge queue.
   - Keyboard: `Move section 4` → first slot stored merges at `orderKey
     -1048575`. After a reload the handles read `Move section 1, “Merge
     queue”`, ….
   - Window offers 24 hours / 7 / 30 / 90 days. 30 days wrote
     `window=30d`, and it survived a reload.
   - Metrics stubbed: a progressbar, then the alert "The report could not
     be loaded (synthetic metrics 500). Refresh to try again."
   - The report's only way into the data is the `task 2` button (no
     `<a>`), as the earlier run filed.
9. **Destructive actions.**
   - No delete or remove control on `/boards` or any board page.
   - The throwaway tasks 20–24 were closed with each page's `Close`
     button (`POST /api/tasks/<id>/close {"close_pull_request":false}`).
     Each went to `closed`, with no confirmation dialog (Close is
     reversible by Reopen).
   - The stores were discarded.
10. **Phone, console and network.**
    - At 390×844 `/boards` and all 11 board pages had `scrollWidth ==
      innerWidth`.
    - No uncaught page errors. The only errors were expected: the
      pre-sign-in `401` on `/`; `404 /api/repos/<repo>/github-branches`
      when a task overlay opens without `-github-sim`; `ERR_ABORTED`
      `/api/tasks` polls cut off by navigation; and the synthetic 500s.

## Bugs found

1. **An own-columns kanban's "in no column" note said "Asked" while a
   column of Asked cards sat above it.**
   - Reproduction: a kanban board with columns `[{name:"Talking",
     states:["awaiting_reply"]}]` over the demo data. The demo has both
     `awaiting_reply` and `awaiting_submit` tasks. The note read "… in no
     column (Asked, …)", because `stateLabels` labels both states
     "Asked". The 2026-10-29, 10-30 and the earlier 11-01 run all
     recorded it as an observation.
   - Fix: `partStateLabels` (`ui/src/state.js`) names the half that is
     present alone as "Asked to submit" or "Asked for a reply". With both
     halves present it still reads "Asked". `TaskBoard.jsx`'s note uses
     it. The column editor's own "Off the board" line is unchanged,
     because its picker only offers both asked states together.
   - After: "4 tasks in no column (Asked to submit, Merging, Deferred,
     Closed)." The default board's note is unchanged ("(Deferred,
     Closed)"). The note is one line at 1440 and at 390 px and does not
     overflow (bounding boxes 1208×20 and 358×20).
   - Coverage:
     - unit test `TaskBoard.test.jsx` "names the asked state it hides
       when a column collects the other one", which fails without the
       fix;
     - e2e `saved-boards-walkthrough.spec.js` 2c.
   - Runs: `vitest` on `TaskBoard.test.jsx` and `state.test.js` passed
     293. Playwright `saved-boards-walkthrough`, `board` and `boards`
     specs passed 45.

## Not bugs

- A Playwright `dragTo` from a card below the fold once reordered the
  wrong card (`ids:["21"]` and `["20"]` when dragging 23). The column
  scrolls inside `.board-columns`. `dragTo` scrolls the target into view
  between mouse-down and the first move, and the target's bounding box
  then lay under the clipped region (`elementFromPoint` returned
  `.board-columns`). With no scroll in between (a 2000-px viewport, or
  the source scrolled into view first) every drag sent the right id.
  This is a harness artifact, not something a person's drag does.
- Still open from earlier runs: the report's task references are buttons
  rather than links, and tree rows link only the task number.
