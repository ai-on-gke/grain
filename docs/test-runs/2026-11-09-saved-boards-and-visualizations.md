# Saved boards and task visualizations walkthrough -- 2026-11-09 (task 3715)

A hand walkthrough of saved boards and every board presentation (list,
kanban, timeline/gantt, tree, dashboard of panels, report) in a real
Chromium driven by Playwright, against a real `grain demo` server over a
throwaway embedded store. The three 2026-11-08 reports (tasks 3459, 3355
and 3612) were read first, so this run spent its time on what they did
not reach: focus after Back from every kind of link a board offers,
stale pins, failure and slow-load states per presentation, and drags
inside a scrolled kanban column.

One bug was found, reproduced deterministically, fixed in this change
and covered by unit and Playwright regression tests (section 3).

## 1. What was tested

| | |
| --- | --- |
| Commit | `8b0d6bdf` (`main`, 2026-11-09 04:22 UTC), then the same tree with the fix |
| Server | `grain demo -addr 127.0.0.1:18900 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, `TMPDIR` a throwaway directory, so the embedded store was new; restarted once on the fixed build, over a new store |
| Frontend | `cd ui && npm ci && npm run build` (the production bundle `grain` embeds), then `go build ./cmd/grain` |
| Browser | Playwright `@playwright/test` ^1.62.1, Chromium 1234 headless; desktop 1440x900 and `iPhone 13` (390x844, touch) |
| Toolchain | Go 1.26.10, Node 20.19.0, Debian Linux |
| Gemini | `/home/debian/.gemini-api-key` is absent in this sandbox, so `LOOK=1` was not used; screenshots were compared by pixel diff and layout checked by bounding boxes |

Sign-in was the real access-code gate (typed into the field, then
`Continue`). After that, every step went through visible controls:
clicks, `focus()` + Enter, Tab, Escape, mouse drags, taps. The scripts
and their output lived under `/var/tmp` and are not committed, and
neither is any screenshot.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub | `-github-sim` | no GitHub access from the sandbox |
| CI | `-fake-ci`, `-base-branch-ci` | no CI runner |
| Agents / model | nothing was run by a model; board creation was verified up to the filed task | no model key |
| Held proposal | task 27 filed with `POST /api/tasks` (`proposedBy: "8"`, `holdForApproval: true`, a `proposalKey`) | only an agent's `propose_task` files one, and the demo seeds none (the same stand-in the 11-08 runs used) |
| Throwaway boards | `WT A1-A3` (folder `WT Ops`), `WT B1-B2` (`WT Zeta`), `WT Kanban` (own columns: Submit = `awaiting_submit`, Doing, Proposed), `WT Kanban Reply` (Replies = `awaiting_reply`), `WT Tree`/`WT Gantt` (`f=repo:any:acme/widgets`), `WT Dash` (five panels), `WT Report` (nine sections) through `POST /api/boards` | the UI creates boards by filing a task, which an agent would turn into a state-repo pull request; that path was exercised up to the filed task (2.2) |
| Failure and slow states | `page.route` answering 500 for `GET /api/tasks` / `/api/metrics`, `[]` for `/api/tasks`, and a 4 s delay on `/api/metrics`, on one page each, removed before the next step | to see the error, empty and loading rendering |

The `WT` boards and tasks lived only in the throwaway store, which was
deleted with the server; `WT A1` was also deleted through the API
mid-run to see a stale pin (2.1).

## 2. Steps and outcomes

No uncaught page errors at either width. The only failed requests were
the conditional task polls Chromium reports as `net::ERR_ABORTED` (`304`
answers to `If-None-Match`), the access gate's own `401` before sign-in,
and the known `404` for `/api/repos/grain/config/github-branches` when a
`grain/config` task is opened (O1 in `2026-11-07-rbac-roles-ui.md`).

### 2.1 Boards index, rail and history

- Rail `Boards` (count 4, then 14 once seeded) → `/boards`: seeded
  Board, Timeline, Report, Tree outside every folder, then `WT Ops` and
  `WT Zeta` with their boards in `order_key` order. The index offers no
  search or sort of its own (its text points at the task list's), so
  none was exercised.
- Open Timeline from its link → `/boards/board-2`; Back → `/boards` with
  focus on Timeline's link; Forward → the board; reload keeps it.
- Direct URLs `/boards/board-1`, `-3`, `-4` draw their boards cold;
  `/boards/board-99` falls back to the index with the "no longer in the
  list" banner, as recorded on 11-08.
- Second section: mouse drag of `WT B2`'s handle onto the top of `WT B1`
  → `WT B2, WT B1`, kept across a reload and in `GET /api/boards`
  (`orderKey`). `WT A3` dropped on the `WT Zeta` heading moved into that
  folder, at its top. A whole-row drag of `WT B1` onto `WT A3` put it
  above `WT A3`. Keyboard pick-up of `WT A2` (Enter on "Move WT A2"):
  focus moves to "Stop moving WT A2", the slots skip the two no-op
  places, Escape puts it down with focus back on "Move WT A2". Tab on
  the index runs handle, link, handle, link.
- Pinned `WT A1` through Customize sidebar (Type Board, Item, Pin item,
  Save): the rail shows it. Deleting the board and reloading drops it
  from the rail without an error (observation O2).

### 2.2 Requesting a new board

- Rail `Create options` → `new task | new session | new board`; New
  board opens `/new` titled "Add a board" with the board template
  (name, folder, kind, description, query; the six kinds; the
  `tables/board*.json` notes).
- Boards index header `New board` → the same draft. Filed as "WT board
  from header 3715": task 28, `grain/config`, `queued`, `approved: true`
  (the operator's own filing; with "queue immediately" unticked the
  11-08 run got `proposed`). The board list did not change -- a board
  appears only when an agent's pull request lands, which is the
  asynchronous contract.
- `Edit board` on `WT Dash` after two reorders drafts its panels in the
  order drawn, with the stored `order_key`s (-1048575, 3, 5, 1048581,
  2097157). On `WT Report` at `?window=30d` it lists the reordered
  sections and says the board would carry `f=created:within:30`.

### 2.3 List and kanban

- Seeded `Board`: Proposed / Waiting / Running / Asked / Failed /
  **Merging** (tasks 5 and 20); "2 tasks in no column (Deferred,
  Closed)". Task 11 wears the blocked oval (`.badge-blocked`); the held
  proposal 27 carries "held for approval by 8"; the imported issue 12
  carries `imported`.
- `WT Kanban` (Submit collects only `awaiting_submit`): "7 tasks in no
  column (Asked for a reply, Merging, Deferred, Closed)". `WT Kanban
  Reply` (only `awaiting_reply`): "22 tasks in no column (Proposed,
  Waiting, Asked to submit, Merging, Deferred, Closed)", and under
  `?q=retry` "7 tasks in no column (Waiting, Asked to submit)".
- A card's title opens `/tasks/6`; Back returns with focus on that
  card's link; Escape from a card opened by keyboard does the same.
- Columns editor on `Board`: renamed Proposed "Ideas WT", "Move Proposed
  right", Save → Waiting, Ideas WT, ...; same after a reload. Adding a
  column with an empty title saves it as "New column". Removing every
  column says "No columns. Saving now puts the default board back", and
  saving does that. Reset to default restores the layout.
- Keyboard: "Move task 6" → slots only within its own column (a column
  is a state, and moving a card does not change its state), "Move here,
  to the end of Submit" → `POST /api/tasks/reorder {"ids":["6"],
  "afterId":"21"}`, focus back on "Move task 6", kept after a reload.
- Mouse drag within a column: 16 dropped above 14 →
  `{"ids":["16"],"afterId":"6","beforeId":"14"}`, kept after a reload.
- On `WT Kanban?q=retry`, moving 19 above 14 sent `afterId 16, beforeId
  14`; unnarrowed, the column reads 16, 19, 14, ... -- the move lands
  between the neighbours the reader saw.

### 2.4 Timeline / gantt

- Range offers last 24 hours / 7 / 30 / 90 days / everything; Lanes
  offers no lanes / by repo / by state / by author. 30 days + by repo
  drew `.gantt-lane` groups, the `.gantt-now` marker and one bar
  labelled blocked; both settings survived a reload (they are this
  browser's, not the URL's, by design: `gantt.js`).
- 24 hours: "3 tasks outside this range."; a search matching nothing:
  "No tasks match this search and these filters in this range."
- A bar opens `/tasks/1`; Back returns with focus on that row's link.

### 2.5 Tree

- `WT Tree` (`f=repo:any:acme/widgets`): 22 under 21, 11 and the held
  proposal 27 under 8, 1/3/8 under the director session 13. Searching
  `retry` writes `?q=retry&f=repo:any:acme/widgets`; after a reload
  only task 2 is shown and the chip still reads "repo is any of
  acme/widgets". A row opens `/tasks/2`; Back and Escape both return
  focus to the row's link. No match: "No tasks match this search and
  these filters."

### 2.6 Dashboard (`WT Dash`)

- Five panels: inherit list ("Showing 8 of 19"), states (6/3/2/4/0/2,
  "2 in no column"), own-query running list (2), throughput (19 filed /
  5 completed), gantt ("Showing 10 of 18 in this range").
- "Search this dashboard…" `pagination` narrows the inheriting panels
  (2 each, with Keep), leaves the own-query panel alone and keeps the
  throughput panel without Keep (its report does not change); the URL
  carries it and survives a reload. Open links: the list panels to
  `/?q=pagination&f=repo:any:acme/widgets` (2 rows there),
  `/?f=state:any:running`, and throughput to `/system/metrics?...
  &f=created:within:7`, whose report agrees with the panel (19 / 5) and
  says the search is not in force.
- Reordered panels with the handle (desktop click and phone tap) and
  kept the order across a reload.
- Add panel opens the pre-written task; Discard draft and Escape return
  to the board (O3).
- Failure: with `/api/tasks` failing each task panel shows "The tasks
  could not be loaded (500 …)" with Retry while throughput stays up;
  Retry after the stub is lifted draws all five. With no tasks: "Nothing
  here right now." on a list panel, zeros on the states panel.
- **Bug** found here: Back from a panel's Open or a state count left
  focus on `<body>` (section 3).

### 2.7 Report

- `WT Report` names nine sections; drawn: throughput, latency, backlog.
  Merges, prompts, tools, agents, sandboxes and checks are all zero in
  `GET /api/metrics` and are each reduced to "…: nothing to report over
  this window." The seeded whole-report `Report` prints throughput,
  latency, capacity, backlog and GitHub CI, omitting the silent ones.
- Window → Last 30 days: `?window=30d` in the URL, one
  `/metrics?window=30d&buckets=24`; Refresh fetches it again; reload
  keeps Last 30 days. `?window=garbage` falls back to Last 7 days and
  drops the parameter. The window menu offers no "everything".
- Sections reordered by handle (Backlog to the top) and by mouse drag
  (Latency above Backlog); each `PATCH /api/boards/board-14` kept across
  a reload.
- Scope: `?f=repo:any:acme/widgets` fetches `repo=acme%2Fwidgets` and
  says "Measured over the tasks in acme/widgets."; `?f=state:any:running
  &q=retry` names both as not in force.
- Backlog counts are links whose lists hold the same number of rows
  (Proposed 6, Waiting 3/4, Running 2, Asked 1/2) unscoped, under
  `acme/widgets`, under `acme/gadgets` and at 24h; "Asked" leaves out
  the finished investigation 7 and its link says `completed:empty`, as
  `backlogOf` documents. The oldest waiting task opens `/tasks/2`.
- Failure: "The report could not be loaded (500 …). Refresh to try
  again."; Refresh after the stub recovers. A slow `/api/metrics` shows
  the "Loading the report" spinner.
- **Bug** found here as well: Back from a backlog count or the oldest
  waiting task left focus on `<body>` (section 3).

### 2.8 Phone, keyboard and destructive actions

- At 390 px every board page (`/boards`, kanban x2, timeline, tree,
  dashboard, report x2) has `scrollWidth` 390 and no element past the
  edge outside the scrolling board and chart bodies.
- Keyboard: every row, card, bar and tree row opens by Enter, and Back
  or Escape returns focus to it -- except the links fixed below.
- The boards UI exposes no delete for boards, panels, sections or
  columns (removing one is a task like any other change), so there was
  no destructive confirmation to exercise. The columns editor's Remove
  is an unsaved edit that Cancel undoes.

## 3. Bug found and fixed

### Back from a dashboard or report link dropped focus on `<body>`

- **Reproduction** (deterministic): open a dashboard (any `panels`
  board), Tab to a panel's `Open` (or a state count on a states panel)
  and press Enter; the task list opens. Press the browser's Back. The
  dashboard is drawn again with `document.activeElement === body`, so a
  keyboard user starts from the top of the document. The same happens
  on a report board for a backlog count and for the "Oldest waiting
  task: task N" link (Back from the task pane: the report remounts and
  fetches itself again, so the link that was followed no longer exists).
  On the same pages a list row, a timeline row, a tree row and a board
  on `/boards` all got focus back -- they are `RowLink`s, whose
  remembered href takes focus when the list mounts again
  (grain/task-1580).
- **Fix:** `ListPrimitives.jsx` lifts `RowLink`'s memory into
  `useReturnFocus(href, tag)` and a `FollowLink` anchor built on it. The
  tag tells apart links with one href -- two panels following the
  dashboard's own question both have an `Open` to the same list -- so
  Back lands on the one that was followed. `RowLink` uses the hook with
  the empty tag and behaves as before. `PanelBoard.jsx`'s `Open` and
  state counts are `FollowLink`s (tagged by panel key and column);
  `MetricsSections.jsx`'s backlog counts are a `BacklogCount` chip using
  the hook, and its task references pass `TaskLink` a `returnFocus` tag.
  `TaskLink` without one (a task pane's own links) never takes focus on
  mounting, as before.
- **After, on the fixed build:** Back from the states panel's Open, the
  running panel's Open and the throughput panel's Open each focus that
  link; Back from the report's "task 2" focuses it (the report having
  fetched itself again). Escape from the task pane was already right and
  still is.
- **Layout:** only `ref`s and click handlers changed; the elements,
  classes and attributes are the ones drawn before. `e2e/screenshot.mjs`
  shot `/boards` and `/boards/board-3` from a binary built at the base
  and from the fixed one, at desktop (1440x900) and phone (390x844), in
  light and dark. A pixel diff of each pair is empty on `/boards` at
  desktop, and otherwise confined to the report's timestamp line
  (319-470 x 237-245) and the phone top bar's live count (69-90 x
  17-38). `WT Dash` and `WT Report` were shot after the fix as well.
- **Coverage:**
  - Vitest `ListPrimitives.returnFocus.test.jsx`: the followed one of
    two same-href links takes focus on remounting; a plain click follows
    in-app; a `TaskLink` that did not opt in never takes focus. All
    three fail on the unfixed source.
  - Playwright `e2e/board-return-focus.spec.js` against `grain demo`: a
    dashboard whose two inheriting panels share an `Open` href (Back
    focuses the second panel's), a state count, a row (unchanged); a
    report's last backlog count and its oldest waiting task. Both tests
    fail on the unfixed bundle ("Expected: focused, Received: inactive")
    and pass with the fix.
- **Runs:** full Vitest suite 4054 passed in 258 files. Playwright
  (chromium) `board-return-focus`, `saved-boards-walkthrough`, `boards`,
  `board`, `boards_workflows`, `gantt`, `report-backlog-links`,
  `work-tree`, `keyboard-open-rows` and `role-gated-actions`: 90 passed.

## 4. Observations (not changed)

- **O1. "← Boards" lands on `<body>`.** The board page's own back button
  opens `/boards` with nothing focused, while the browser's Back
  focuses the board's link. It is a forward navigation to the index, so
  this was left as it is.
- **O2. A stale board pin is named by its id.** After a pinned board is
  deleted the rail drops it quietly, but Customize sidebar still lists
  "board-5" (and counts it in "pinned items (2)") with no word that the
  board is gone; Unpin removes it. `pinnedNav.js`'s `getPinnedItemInfo`
  does the same for a session. Marking it "gone" needs to know the
  boards have loaded, which the overlay is not told, so it was left
  alone.
- **O3. Escape straight after Discard draft is lost.** Discard draft
  unmounts the focused button, so for one tick focus is on `<body>` and
  an Escape pressed then does not close the drawer; MUI's focus trap
  brings focus back within about 50 ms, after which Escape works. This
  is the shared new-task drawer, not boards.
- **O4. Playwright's `dragTo` inside a kanban column that scrolls**
  (`.board-column-cards`, 1182/585 px) picks up a different card: it
  scrolls the source into view, then the target, so the mouse goes down
  over whatever is under it by then. A real drag does not scroll between
  press and move; specs should drag between cards in view.
- Still true from earlier runs: the report window picker and a board's
  search replace the history entry; dependencies are drawn as the
  blocked state rather than connector lines; Lanes offer "by author" but
  not "by owner".
