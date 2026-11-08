# Saved boards and task visualizations walkthrough — 2026-11-08

## Commit and environment

- **Commit tested:** `6d15dc3a` (`main`, "Merge pull request #912"), on
  branch `grain/098073e4/task-3459-walk-through-saved-boards-and`. The
  fix below was built on top of it and checked again in the browser.
- **Date:** 2026-11-08, 08:11–08:40 UTC.
- **Server:** the real `grain demo`, built with `go build ./cmd/grain`
  (go1.26.10) after `npm ci && npm run build` in `ui/` (Node v20.19.0;
  the production Vite bundle embedded from `pkg/ui/static`). Each server
  ran over its own throwaway embedded store (`TMPDIR` pointed at a
  scratch directory) with `-open=false -access-code-file <file>
  -human-review -merge-queue-states -github-sim`:
  - `127.0.0.1:18801`: `6d15dc3a` unchanged, for §1–§10 and the before
    screenshots;
  - `127.0.0.1:18802`: rebuilt with the fix, for the after screenshots
    and the layout checks under "Bugs found".
- **Browser:** Playwright 1.62.1 Chromium (`/var/cache/playwright`),
  driven by ad-hoc Node scripts through visible controls (not
  committed). Sign-in went through the "Access code" page. Desktop was
  1440×1000 (1280×900 for the screenshots and layout checks); phone was
  390×844 (`iPhone 13` descriptor, `isMobile`, `hasTouch`). Every script
  recorded page errors, console errors, HTTP ≥ 400 responses, failed
  requests and every non-GET `/api` call.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so no
  Gemini-backed tooling ran and `LOOK=1` was not used. The file reader
  shows PNGs as bytes, so the screenshots were checked by bounding boxes
  and computed colours (see "Bugs found").

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`** (task 26, "WT held
   proposal: tidy the retry docs", `proposedBy: "8"`,
   `holdForApproval: true`, a `proposalKey`). The demo seeds no held
   proposal: only an agent's `propose_task` with `hold_for_approval`
   files one, and `grain demo` runs no agent.
2. **Stand-in boards, created over `POST /api/boards`.** The product's
   own creation path files a task against `grain/config` for an agent to
   turn into a pull request; that contract was exercised first (§2). The
   stand-ins, all with `orderKey` 0:
   - `WT List` (board-5, `list`, `f=state:any:running`, folder `WT3459`)
   - `WT Kanban Reply` (board-6, `kanban`; columns Todo
     (`proposed`,`queued`) / Doing (`running`) / Talking
     (`awaiting_reply`), folder `WT3459`)
   - `WT Kanban Submit` (board-7, `kanban`; columns Sign off
     (`awaiting_submit`) / Doing (`running`), folder `WT3459`)
   - `WT Gantt` (board-8, `gantt`, `q=retry`, folder `Ops`)
   - `WT Tree` (board-9, `tree`, `q=pagination`, folder `Ops`)
   - `WT Tree Running` (board-10, `tree`, `f=state:any:running|queued`,
     folder `Ops`)
   - `WT Dash` (board-11, `panels`, `f=repo:any:acme/widgets`, folder
     `Ops`; panels `states` "State counts", an inheriting `list`
     "Inherit", a `list` "Running only" over `f=state:any:running` at
     `third` width, `metrics` "Throughput", a `gantt` "Timeline" 24h at
     `full` width, and a `list` "Nothing" over `q=zzzznothing`)
   - `WT Empty Dash` (board-12, `panels`, no panels)
   - `WT Report Widgets` (board-13, `report`, `f=repo:any:acme/widgets`;
     sections backlog titled "Waiting work", throughput, tools, agents)
   - `WT Report All` (board-14, `report`, all 13 `model.ReportSections`)
   - Folder `WT Reports`: `WT Report Two Repos` (board-15,
     `f=repo:any:acme/widgets|grain/config`), `WT Report 30d` (board-16,
     `f=created:within:30`), `WT Report Running` (board-17,
     `f=state:any:running&f=repo:any:acme/widgets`), `WT Report Not
     Repo` (board-18, `f=repo:none:acme/widgets`).
3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*` delayed 1.5 s then `500 {"error":"synthetic
     metrics 500"}`, for the metrics panel's loading and error states,
     the report's failed Refresh and its failed cold load.
   - `**/api/tasks` (GET) answered `500 {"error":"synthetic tasks
     500"}`, for a dashboard whose task load fails.
   - `**/api/boards` answered `500 {"error":"synthetic boards 500"}`,
     for the index and a board URL whose boards load fails (bug 1).

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- The rail's `Boards 4` opened `/boards`: Board / kanban, Timeline /
  timeline, Report / report, Tree / tree, each with its description. The
  index has no search or sort control (0 inputs, 0 comboboxes); its
  header carries `New board`.
- With the stand-ins the rail read `Boards 14`, and the index showed the
  unfiled boards first, then folders `Ops` and `WT3459`. Every row has a
  named handle (`Move <board>`), titled "Drag this to reorder, or tap it
  to pick this up and choose where it goes".
- **Dragging in a section below the first.** `WT Dash`'s handle dropped
  on the top edge of `WT Gantt` (both in `Ops`) sent `PATCH
  /api/boards/board-11 {"folder":"Ops","orderKey":-1048576}` and made it
  first in `Ops`. The order survived a reload.
- **Into and out of a folder.** `WT Empty Dash`'s handle dropped on the
  `WT3459` folder heading sent `{"folder":"WT3459","orderKey":-1048576}`
  and filed it first there, the same after a reload. From the keyboard,
  Enter on its handle offered 15 slots, among them "Move here, outside
  every folder"; Enter there sent `{"folder":"","orderKey":1048576}` and
  kept focus on `Move WT Empty Dash`. Escape on a picked-up handle
  withdrew every slot and left focus on the handle.
- All four seeded boards opened from a direct URL with no console error,
  no failed request and `scrollWidth == innerWidth` at 1440 px.
- Typing in a board's search box replaces the history entry rather than
  adding one: from `/boards` → `Board` → `q=retry` → `q=retry budget`,
  Back went to `/boards`, and Forward came back to the board.

### 2. Requesting a board (the asynchronous contract)

- **Rail:** `Create options` → menu `New task | New session | New
  board`. `New board` opened `/new` with the title "Add a board" and the
  description that asks for name, folder, kind, description and query,
  lists the six kinds, explains the query syntax and says the board is a
  row in `tables/board.json` of this deployment's state repository, with
  repo `grain/config`. Filed as "Add a board (WT3459 via rail)" it became
  task 23, `queued` against `grain/config`. The board list did not
  change: no board appears until an agent's pull request merges.
- **Boards index header:** `New board` opened the same draft. With
  "queue immediately" unticked (`approved: false` in the `POST
  /api/tasks`) it was filed as task 24, `proposed` against `grain/config`
  (a second run of the script filed 25 the same way).
- **From a narrowed list:** the rail's `New board` on
  `/?f=state:any:running&sort=created:desc` drafted `query:
  sort=created:desc&f=state:any:running`; on `/?q=retry`, `query:
  q=retry`; on `WT List`, `query: f=state:any:running` with "That query
  is the view the board was created from". The task list itself has no
  New board button of its own, although the index's text said to "click
  New board there" (bug 2).
- `Edit board` drafted "Change the board …" for every kind: the tree
  with "being read right now narrowed to `q=pagination&f=repo:any:acme/
  widgets`"; the gantt saying range and lanes are the reader's own; the
  report listing its four sections and, at `?window=30d`, saying the
  board would carry `f=created:within:30`; the Kanban listing its two
  columns' states. `Add panel` on `WT Dash` drafted the six panels with
  their widths, own queries and `order_key`s. `Discard draft` cleared
  each without filing anything.

### 3. List and Kanban

- **Seeded `Board`:** columns Proposed / Waiting / Running / Asked /
  Failed / Merging, and "2 tasks in no column (Deferred, Closed)."
  - The merge-queue column reads **merging** (tasks 5 and 20).
  - Task 11's card wears the **blocked oval** (`badge-blocked`, titled
    "Blocked on: #8 (running)").
  - The held proposal 26 carries **"held for approval by 8"**.
  - The imported issue 12 carries **imported**.
- **No-column notes on custom columns:** `WT Kanban Reply` (collects
  `awaiting_reply` only) said "12 tasks in no column (Asked to submit,
  Merging, Deferred, Closed)."; `WT Kanban Submit` (collects
  `awaiting_submit` only) said "15 tasks in no column (Proposed,
  Waiting, Asked for a reply, Merging, Deferred, Closed)." Narrowed to
  `f=repo:any:acme/widgets`, the latter counted 12. Narrowed to the
  state no column collects (`board-6?f=state:any:awaiting_submit`), it
  said "8 tasks in no column (Asked to submit)." and "None of the tasks
  this search and these filters match is in one of these columns."
- `WT List` showed 3 and 8 under the chip "State is any of Running".
- **Opening a card:** the title of card 11 opened `/tasks/11`; Escape
  returned to `/boards/board-1` with focus on that card's title link.
- **Moving cards:**
  - Keyboard: Enter on `Move task 23` offered three slots ("Move here,
    above Fix flaky retry test, in Waiting", …); Enter on the first sent
    `POST /api/tasks/reorder {"ids":["23"],"beforeId":"2"}` and kept
    focus on `Move task 23`.
  - Mouse (HTML5 drag): task 23 dropped near the end of Waiting sent
    `{"ids":["23"],"afterId":"11","beforeId":"22"}`.
  - A drag from Proposed into Waiting changed nothing and sent nothing:
    a column is a state, which a drag does not set.
  - Phone tap: tapping `Move task 22` then the first slot sent
    `{"ids":["22"],"beforeId":"2"}`.
  - Each order was the same after a reload.
- **Selection:** a column's "Select every task in Waiting" checkbox
  raised the batch bar ("4 selected | approve | run selected now |
  submit | retry merge | defer | undefer | close | reopen | bulk edit… |
  attach | detach | clear selection"). Close with 24 and 25 selected
  asked "Close 2 task(s)?"; dismissing it sent nothing.
- **Column editor (`Edit columns`):** every icon button is named; `Move
  Proposed left` and `Move Merging right` are disabled. Renaming column 2
  to "Up next" and `Move Failed left` (focus stayed on it), then Save:
  Proposed / Up next / Running / Failed / Asked / Merging, the same
  after a reload. An unsaved edit then Escape raised "You have unsaved
  changes here. Leave and discard them?"; dismissing kept the dialog
  open. `Reset to default` and Save put the six default columns back. A
  board with its own columns (`WT Kanban Reply`) has no `Edit columns`
  and drew Todo / Doing / Talking.

### 4. Timeline / gantt

- `Range` offers last 24 hours / 7 / 30 / 90 days / everything; `Lanes`
  offers no lanes / by repo / by state / by author.
- The default (7 days) drew 24 bars, "1 task outside this range." and
  one `.gantt-now`; ticks Nov 2 … Nov 8.
- **24 hours + by repo:** acme/widgets 14, grain/config 3, no repo 5,
  "3 tasks outside this range.", ticks every three hours. It survived a
  reload (the pickers read Last 24 hours / By repo) and the URL stays
  bare.
- **Everything + by state:** proposed 5 … closed 1; **by author:**
  debian 22, director, grain, issue-import.
- **Dependencies:** task 11's bar reads "Waiting, blocked: waiting for
  46m, never dispatched"; no arrows are drawn.
- A bar click opened `/tasks/1`; Escape returned with focus on its
  `a.gantt-row-title`. Every row title is in the Tab order.
- `WT Gantt` opened on `?q=retry` with 8 bars. Narrowed by URL to
  `?q=retry&f=state:any:awaiting_submit` under Everything it drew 6 bars
  and no today marker: "Everything" fits the window to the bars, which
  here all ended the previous afternoon (`gantt.js` `windowOf`, pinned by
  "fits everything to the spans it is given").

### 5. Tree

- **Seeded `Tree`:** the director session 13 nests 1, 3 and 8; 8 nests
  11 and the held proposal 26 (filed by 8); the review 22 nests under
  21.
- `WT Tree` (`?q=pagination`) showed 8 → 11. `WT Tree Running`
  (`f=state:any:running|queued`, chip "State is any of Running,
  Waiting") showed 23, 22, 13 → 3, 8 → 11, and 2; the same after a
  reload.
- The toolbar search `retry` on `Tree` wrote `/boards/board-4?q=retry`
  (26 at top level, since its parent 8 does not match; 14–19; 2) and
  survived a reload with the box still reading "retry".
- Enter on a focused row link opened `/tasks/26`; Back returned to
  `/boards/board-4?q=retry`.

### 6. Dashboard (`WT Dash`)

- Panels: State counts (16; 3 proposed, 3 waiting, 2 running, 4 asked, 0
  failed, 2 merging, "2 in no column"), Inherit ("Showing 8 of 16."),
  Running only (2), Throughput (16 filed, 5 completed), Timeline
  ("Showing 10 of 14 in this range. 2 tasks outside this range."),
  Nothing ("Nothing here right now.").
- **Each link goes to its own question, and the lists are the same
  size:** `/?f=repo:any:acme/widgets` 16 rows; `…&f=state:any:proposed`
  3; `…queued` 3; `…awaiting_reply|awaiting_submit` 4; `…completed` 2;
  `/?f=state:any:running` 2; `/?q=zzzznothing` "No tasks match your
  search."; Throughput → `/system/metrics?f=repo:any:acme/widgets`.
- **Whole-board narrowing:** the search `pagination` wrote
  `?q=pagination&f=repo:any:acme/widgets`. The inheriting panels' links
  and the metrics link took it; Running only and Nothing kept their
  own. It survived a reload; clearing it went back to
  `?f=repo:any:acme/widgets`.
- **Reorder:** keyboard `Move panel 6, "Nothing"` → the first slot sent
  `PATCH /api/boards/board-11 {"panels":[…]}` and moved focus to `Move
  panel 1, "Nothing"`; a mouse drag of panel 3 onto panel 1 made Inherit
  first. After a reload: Inherit / Nothing / State counts / Running only
  / Throughput / Timeline.
- **Loading and error:** with metrics delayed then 500, Throughput read
  "Loading…", then "The metrics report could not be loaded."
- **Failed task load:** every task panel said "The tasks could not be
  loaded (synthetic tasks 500)." with a Retry button. (The 11-06 run
  recorded these panels drawn as empty; that is fixed on `main`.)
- **Empty:** `WT Empty Dash`: "This dashboard has no panels yet. “Add
  panel” files the task that adds one."

### 7. Report

- **Seeded `Report`** (no sections named) drew Throughput, Latency,
  Capacity, Backlog and GitHub CI, leaving out every silent section.
- **`WT Report All`** names all 13 and accounts for each silent one:
  "Tool use / Prompts / CI waits / CI queueing / Scheduling / Sandboxes
  / Merge queue / Agents: nothing to report over this window."
- **Window:** the picker offers last 24 hours / 7 / 30 / 90 days. 90
  days wrote `?window=90d`; a reload kept it ("Last 90 days"); Refresh
  asked `/api/metrics?window=90d&buckets=24` once more.
  `?window=bogus` fell back to "Last 7 days" and dropped the parameter;
  `?window=24h` asked `window=24h`.
- **A window in the query:** `WT Report 30d` (`f=created:within:30`)
  opened on "Last 30 days" and asked `window=30d`; picking 7 days wrote
  `?f=created:within:30&window=7d`, which a reload kept;
  `?f=created:within:3` read "Last 3 days" and asked `window=3d`.
- **Links:** the backlog chips Proposed 5 / Waiting 4 / Running 2 /
  Asked 1 link to `/?f=state:any:…` (asked via
  `f=state:any:awaiting_reply&f=completed:empty:`), and "task 2" links
  to `/tasks/2`. The backlog leaves asked-to-submit tasks out on purpose
  (`pkg/metrics/metrics.go`, beside `StateAwaitingSubmit`).
- **Scoping:** `WT Report Widgets` said "Measured over the tasks in
  acme/widgets." and asked `/api/metrics?repo=acme%2Fwidgets…`;
  `WT Report Two Repos` said "… in acme/widgets or grain/config.", asked
  both `repo=` parameters, and its chips carried
  `f=repo:any:acme/widgets|grain/config`. `WT Report Running` and
  `WT Report Not Repo` said the rest of the question was not in force:
  "State (running)" and "Repo (acme/widgets)". `?q=retry` named "the
  search for "retry"".
- **Reorder:** keyboard `Move section 2, "Throughput"` → the first slot
  sent `PATCH /api/boards/board-13 {"sections":[{"kind":"throughput",…
  "orderKey":-1048575},…]}` and moved focus to `Move section 1,
  "Throughput"`; after a reload: Throughput, Waiting work, Tool use,
  Agents.
- **Failed Refresh** (metrics 500): the alert "synthetic metrics 500";
  the six sections already drawn stayed. A failed cold load said "The
  report could not be loaded (synthetic metrics 500). Refresh to try
  again."

### 8. Deep links that carry a query

`/boards/board-11?q=pagination` drew the dashboard narrowed by the
search alone, without the board's own `f=repo:any:acme/widgets`; the
same holds on a list, tree or report board. This is deliberate
(`paths.js` above `narrowingFromQuery`: "a query that asks anything at
all is taken as the whole of what the list is narrowed by"), and the
board's own search box writes both parts into the URL.

### 9. Destructive actions

- No board page and no part of `/boards` has a delete or remove control.
  Boards are removed through `grain/config`.
- The column editor's `Remove <col>` acts only on this browser's draft,
  behind the unsaved-changes confirm.
- The throwaway board requests 23, 24 and 25 were closed from `Board`'s
  batch bar: "Close 3 task(s)?" accepted sent `POST
  /api/tasks/{23,24,25}/close`, and the no-column note went from 2 to 5
  tasks. Both stores were discarded afterwards.

### 10. Phone, keyboard, console and network

- At 390×844, `/boards` and all 14 board pages had `scrollWidth ==
  innerWidth`, with no element past the right edge outside a scroller.
- Tab through Timeline, `WT Dash`, `WT Report Widgets` and `Tree`
  reached `← Boards`, the narrowing chips, every handle, every panel's
  `Open`, every state tile and every row link, in order. Focused report
  chips change background (`rgba(0,0,0,0.12)`), the others draw the 2 px
  `:focus-visible` outline.
- No uncaught page errors. The only errors were the synthetic 500s.

## Bugs found

1. **A boards load that failed read as "No boards yet."** (Fixed here.)
   - Reproduction: route `GET /api/boards` to a 500 and open `/boards` or
     `/boards/board-11`. For five seconds an error banner says
     "synthetic boards 500". After that the page says "No boards yet."
     with a `New board` button, the header reads "Boards 0", and a
     board's URL draws that same page. A reader who believes it files a
     request for a board that already exists. The task views stopped
     doing this with `TasksLoadError`; boards had no equivalent.
   - Fix: `App.jsx`'s `refreshBoards` records why `/api/boards` failed
     (`boardsError`, cleared on the next answer) and passes it to
     `BoardsPage` with a retry. With no boards to show, `BoardsPage`
     draws "The boards could not be loaded (…)." and a Retry button
     (the `.tasks-load-error` alert the task views use) in place of the
     empty state, and drops the header's count. A later poll that fails
     while boards are already listed leaves them up. The board's URL is
     kept, and Retry opens the board it names.
   - Coverage: `BoardsPage.test.jsx` "says the boards could not be
     loaded rather than that there are none" (fails without the fix)
     and "keeps the boards it has when a later load fails"; e2e
     `saved-boards-walkthrough.spec.js` 1b (the alert outlives the
     banner, the URL is kept, Retry draws the `Tree` board).
2. **The Boards index pointed at a New board button the task list does
   not have.** (Fixed here.)
   - Reproduction: `/boards` said "narrow the task list first and click
     New board there", but `/` has no New board button. `9be44f8f` moved
     the create menu out of the page header into the rail. The rail's
     `Create options` → `New board` on a narrowed list does carry the
     narrowing (§2).
   - Fix (`BoardsPage.jsx`): "narrow the task list first and pick New
     board from the menu beside New task".

**Layout checks** for both fixes. `e2e/screenshot.mjs` was run before
(18801) and after (18802) on `/boards` and `/boards/board-4`, at desktop
and phone widths in light and dark. For the failure state, an ad-hoc
script shot `/boards/board-4` with `/api/boards` stubbed to 500, 6.5 s
after load, at 1280×900 and 390×844 in both themes. The PNGs could not
be looked at here, so on 18802:

- the alert is 1048×79 px at desktop (text 343×21 px at x 260, Retry
  64×31 px at x 615, on one line) and 390×117 px on the phone (Retry
  wrapped under the text at x 16); the text and the button do not
  overlap, and both sit inside the alert and the content column;
- the text colour is `rgb(82,78,70)` on `rgb(247,246,243)` light and
  `rgb(184,181,173)` on `rgb(15,16,19)` dark;
- the header shows no count while the load is failing;
- the reworded paragraph wraps inside its 46 rem (736 px) measure at
  desktop and the 390 px column on the phone;
- no page scrolls horizontally.

Runs:
- `vitest`: 4023 tests passed in 257 files; `BoardsPage.test.jsx`
  17 passed.
- Playwright (chromium) `saved-boards-walkthrough`, `boards`, `board`
  and `boards_workflows`: 51 passed against a bundle built with both
  fixes.

## Observations (not fixed here)

- The rail still reads "Boards 0" while `/api/boards` is failing. It is
  a count in the rail beside every other list's count, and the page it
  opens now says what went wrong.
- The report window picker replaces the history entry, and a board's
  search box does the same, so Back leaves the board rather than undoing
  a search. This is consistent across task views.
- Bulk actions in a board's batch bar are all enabled whatever is
  selected (approve and submit with four waiting tasks selected); the
  ones that do not apply skip and count. That is the task list's shared
  bar, not anything boards add.
- Still true from earlier runs: the timeline's Lanes offer "by author"
  but not "by owner"; chips are lowercased by the theme.
