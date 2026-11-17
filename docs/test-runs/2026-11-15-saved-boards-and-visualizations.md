# Saved Boards and Task Visualizations Walkthrough — 2026-11-15

This is the fourth saved-boards and visualizations walkthrough, following
[2026-11-13-saved-boards-and-visualizations.md](2026-11-13-saved-boards-and-visualizations.md)
(task 4727),
[2026-11-13-saved-boards-and-visualizations-task-5385.md](2026-11-13-saved-boards-and-visualizations-task-5385.md)
(task 5385) and
[2026-11-14-saved-boards-and-visualizations.md](2026-11-14-saved-boards-and-visualizations.md)
(task 5631). Every coverage item was exercised in headless Chromium through
Playwright against a real `grain demo` server over a throwaway embedded SQLite
store, walking `/next/` first wherever a `/next/` surface exists and falling
back to the classic UI at `/` only for pages that have not migrated yet.

Four defects were uncovered on `/next/` and fixed in this change (section 3):

1. **Failed `/api/boards` read on `/next/tasks` claimed `"No saved boards yet."`**
   instead of explaining why the boards could not be loaded.
2. **Choosing `New → New board` from the `/next/` rail while on a narrowed or
   Timeline `/next/tasks` view discarded `tab`, `group`, `q`, `repo` and
   `view=timeline`** before opening the save bar, saving an unnarrowed list
   instead of the view on screen.
3. **Every Backlog row's `⋮` (`More actions`) button on `/next/tasks` wrapped
   onto a third line at the far-left edge** inside the default 30/70 desktop
   split (`410px`) and on a `390px` phone, opening its right-aligned popup off
   the left edge (`x = -54` on desktop, underneath `<nav class="nav-rail">`) so
   `Move to top`, `Move up`, `Move down`, `Move to bottom` and `Defer` could
   not be clicked.
4. **Closing the `Save current view as board:` bar on `/next/tasks`** via
   `Escape`, `Cancel` or `Save board` dropped keyboard focus to `<body>` instead
   of returning it to the `Boards` button.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `5a9bce62c` (`main`, 2026-11-15) |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci` over a throwaway `TMPDIR` under `/var/tmp` |
| Frontends | `make frontend` (`ui/`, `ui-ng/`, `ui-hub/`) embedded into `./bin/grain` (`make grain`); rebuilt after fixes |
| Browser | Playwright Chromium (headless, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), Desktop `1440×900` and `iPhone 13` (`390×844`, touch), light and dark |
| Toolchain | Go from `go.mod`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent; `LOOK=1` is unavailable, so layout was checked directly via bounding boxes, `scrollWidth`/`clientWidth` and `screenshot.mjs` renders |

Sign-in posted `{code, name: "alice@example.com"}` to `POST /api/auth/access-code`.
Walkthrough scripts and `before`/`after` screenshots were written to `/var/tmp/walk`
outside the repository and are not committed.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| External GitHub and CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | Sandbox has no external GitHub or CI credentials |
| Agent landing a `grain/config` board PR | Verified up to the filed `POST /api/tasks` record in `grain/config` (2.7) | `grain demo` runs no model worker |
| Additional board kinds, folders and task states | Seeded via `POST /api/boards` and `POST /api/tasks` (listed below) | Stock `grain demo` seeds four boards (`Board`, `Timeline`, `Report`, `Tree`) and no `holdForApproval` or `keepRunning: true` task |
| Viewer and contributor roles | Scoped `page.route` on `GET /api/config` setting `access` to `grain/viewer` (read scopes only) or `grain/contributor` (read scopes plus `tasks:write`, `ci:write`) | No identity-aware proxy in sandbox |
| Store / metrics read failures | Scoped `page.route` returning `500 {"error":"store unavailable"}` on `GET /api/boards` and `500 {"error":"metrics broken"}` on `GET /api/metrics` | Embedded SQLite does not fail on demand |

Throwaway fixtures created against the demo server:

- **Same-named `Triage` boards (3):** loose (`kind: list`, `f=state:any:queued`),
  `WT Ops` (`kind: list`, `f=state:any:running`), and `WT Zeta` (`kind: kanban`)
- **Additional `WT Zeta` boards (3):** `WT Zeta Alpha`, `WT Zeta Beta`,
  `WT Zeta Gamma` (`kind: list`)
- **Custom-column kanban:** `WT Submit Only` (`Asked to submit` →
  `awaiting_submit`, `Queued` → `queued`)
- **Narrowed tree:** `WT Widgets Tree` (`kind: tree`, `f=repo:any:acme/widgets`)
- **Dashboards:** `WT Dash` (`State counts`, `Running only` with
  `f=state:any:running`, `Week timeline` 7d, `Throughput` 7d) and
  `WT Empty Dash` (0 panels)
- **Reports:** `WT Nine` (all 9 measurement sections) and `WT Widgets Report`
  (`f=repo:any:acme/widgets`)
- **Tasks:** `WT held proposal` (`#26`, `proposedBy: "8"`,
  `holdForApproval: true`) and `WT standing task` (`#27`, `keepRunning: true`,
  approved into `queued`). Note that `main` since `08f840116` also seeds task 21
  (`Watch the acme/widgets nightly build`, closed by rollover) and task 22 (its
  queued successor), shifting demo IDs after task 20 by +2 compared with the
  2026-11-14 run.

## 2. Steps and outcomes

`[next]` marks steps run against `/next/` (`ui-ng/`), and `[classic]` marks
steps run against `/` (`ui/`).

**Surface migration map (`ui-ng/src/app/shell/nav.ts`, `app.routes.ts`):**

- **Available on `/next/`:**
  - `/next/tasks` — `Boards` saved-views disclosure, inline `Save current view as
    board:` bar, rail `New → New board` entry, List view with Backlog drag/keyboard/menu
    reordering, and Timeline view (`?view=timeline`).
  - `/next/workstreams` and `/next/workstreams/:key` — goal/filer task trees and
    dependency graphs (`Split`, `List`, `Graph`) with inspector pane.
  - `/next/telemetry` (`/next/metrics` redirects here) — measurement report
    (`?window=`, `Refresh`, repository scoping, throughput, latency, outcomes,
    tools/CI queueing, templates, and active workstream task links).
- **Still classic-only (no `/next/` route yet; `/next/boards` redirects via `**`
  to `/next/inbox`):**
  - The `/boards` index and folder drag-reordering, standalone `/boards/:id`
    pages, Kanban (`TaskBoard`), Gantt with range/lane selectors (`GanttChart`),
    `TaskTree` boards, multi-panel Dashboards (`PanelBoard`), reorderable-section
    Report boards (`ReportBoard`), and the asynchronous `grain/config`
    board-request form (`/new`).

### 2.1 `/next/tasks` Saved Boards menu, load failure and rail `New board` `[next]`

- **Menu disclosure and keyboard focus:**
  - Clicking `saved-boards-btn` opened `saved-boards-menu` with all 16 boards
    and moved focus to the first board (`board-apply-board-1`, `"Board"`).
  - `Escape` closed the menu and restored focus to `saved-boards-btn`.
- **Folder disambiguation:**
  - The three `Triage` boards exposed distinct accessible names on both apply
    and delete buttons: `Triage, outside every folder`, `Triage, in WT Ops`,
    and `Triage, in WT Zeta`.
  - Uniquely named boards in folders (`WT Zeta Alpha`) kept `aria-label = null`
    and rendered the folder name inside `<span class="board-folder muted"
    aria-hidden="true">`.
- **Failed `GET /api/boards` fetch:**
  - **Before fix (section 3.1):** with `GET /api/boards` returning `500
    {"error":"store unavailable"}`, opening `saved-boards-btn` rendered
    `"No saved boards yet."`.
  - **After fix:** the menu renders `role="status"` (`boards-load-error`):
    `The boards could not be loaded (store unavailable).` and never says `"No
    saved boards yet."`.
- **Rail `New → New board` and save-bar focus:**
  - From `/next/inbox`, clicking `rail-create-menu` → `New board` navigated to
    `/next/tasks`, opened `board-save-form`, and focused `board-name-input`.
  - **Before fix (section 3.2 & 3.4):** from
    `/next/tasks?tab=asked&q=merge&repo=acme%2Fwidgets&view=timeline`, clicking
    `rail-create-menu` → `New board` navigated to bare `/next/tasks`, wiping out
    `tab=asked`, `q=merge`, `repo=acme/widgets` and `view=timeline`; closing the
    save bar via `Escape`, `Cancel` or `Save board` dropped `document.activeElement`
    to `<body>`.
  - **After fix:** `New → New board` on `/next/tasks` preserves the active
    `tab=asked&q=merge&repo=acme%2Fwidgets&view=timeline` URL and Timeline view,
    saves all four parameters on `POST /api/boards`, and returns focus to
    `saved-boards-btn` on `Escape`, `Cancel`, and `Save board`.
- **Save, duplicate-name tie-break, apply, Back/Forward and delete:**
  - Saving `"WT Next Saved"` on `?tab=asked&q=retry` posted
    `{"kind":"list","name":"WT Next Saved","query":"tab=asked&q=retry"}` and
    updated the trigger label to `Boards WT Next Saved`.
  - Saving a second `"WT Next Saved"` disambiguated the pair by ID:
    `WT Next Saved (board-57), outside every folder` and `(board-58)`.
  - Applying `board-57` from `/next/tasks` navigated to
    `/next/tasks?tab=asked&q=retry`; browser `Back` returned to `/next/tasks`
    and `Forward` restored `/next/tasks?tab=asked&q=retry`.
  - Clicking `×` (`board-delete-board-58`) closed the menu and opened
    `confirm-dialog` (`Delete board WT Next Saved (board-58), outside every
    folder?`) with `Cancel` focused. Pressing `Escape` sent zero `DELETE`
    requests and restored focus to `saved-boards-btn`. Confirming `Delete board`
    sent `DELETE /api/boards/board-57` and `/board-58` (both throwaway boards
    created in this run) and restored focus to `saved-boards-btn`.
- **Role gating on `/next/tasks`:**
  - `grain/viewer`: `New → New board` disabled; `Boards` menu hides `+ Save
    current view…` and all `×` buttons (`0`); Backlog renders `0` drag handles.
  - `grain/contributor` (`tasks:write`, no `boards:write`): `New → New board`
    disabled and `Boards` menu hides save/delete (since `/next/` writes directly
    to `POST /api/boards`), while Backlog renders all `6` task drag handles.

### 2.2 `/next/tasks` List, Backlog reorder and task chips `[next]`

- **Tabs and labels:** `Backlog 6`, `Needs approval 3`, `Running 2`, `Asked 2`,
  `Submit & land 10` (`tab-merging`), `Deferred 1`, `Closed`, `All 24`.
- **Row badges and markers:**
  - Blocked task `#11` carries `Waits on a dependency` and `Part of Add
    pagination to the tasks API` (linking to `/next/workstreams/director:director:8?node=11`).
  - Durable task `#20` carries `Skips worker limits`.
  - Closed rolled-over task `#21` carries `Rolled over to #22`.
  - Unlike classic Kanban/List cards (2.8), `/next/tasks` rows do not render
    separate `keeps running`, `held for approval`, or `imported` pills on the row
    itself.
- **Opening task detail:** clicking `open-2` opened the side pane at
  `/next/tasks?task=2`, which survived a reload.
- **Backlog reorder (keyboard and row menu):**
  - Focusing `handle-2` (`aria-label="Move #2, position 2"`), pressing `Space`
    announced `Picked up #2, position 2 of 6. Use the arrow keys to move, Space
    to drop, Escape to cancel.`, `ArrowUp` + `Space` posted
    `POST /api/tasks/reorder {"ids":["2"],"afterId":"","beforeId":"11"}`, and
    the new order `[2, 11, 20, 22, 25, 27]` survived a reload.
  - **Row `⋮` menu (`#menu-list-<id>`):**
    - **Before fix (section 3.3):** at `1440×900` (30% split pane = `410px`),
      `menu-11` wrapped onto line 3 at `x = 86`, and `#menu-list-11` opened at
      `x = -54..126` underneath `<nav class="nav-rail">` (`x = 0..72`), so
      clicking `Move down` timed out with `<nav class="nav-rail"> subtree
      intercepts pointer events`.
    - **After fix:** `menu-11` sits at the right edge of its row (`x = 427..467`
      on desktop, `x = 336..376` on phone), `#menu-list-11` opens inside the
      pane (`x = 287..467` on desktop, `x = 196..376` on phone), and clicking
      `Move down` posted `POST /api/tasks/reorder {"ids":["11"],"afterId":"2","beforeId":"20"}`.

### 2.3 `/next/tasks` Timeline view `[next]`

- On `/next/tasks?tab=proposed&view=timeline`, open task `#1`'s `.timeline-bar`
  ran all the way to the right-hand `"now"` edge of `.timeline-track`
  (`width = 381.4px` across a `381.4px` track).
- Clicking `open-2` on `/next/tasks?tab=backlog&view=timeline&group=repo`
  opened the side pane at `?tab=backlog&view=timeline&group=repo&task=2`;
  `Back` returned to `?tab=backlog&view=timeline&group=repo` (note `Group by`
  stays in the URL on Timeline without splitting the swimlane list into group
  headers).

### 2.4 `/next/workstreams` trees `[next]`

- `/next/workstreams?repo=acme%2Fwidgets&scope=all` survived a reload with `2`
  streams: `director:director:8` (`Add pagination to the tasks API`) and
  `task:24` (`Default new workspaces to the paid tier`).
- Opening `ws-row-director:director:8` rendered `ws-tree` and `ws-graph` with
  root `#8` (`Running`) and nested children `#11` (`Blocked`) and `#26` (`WT
  held proposal`, `Proposed`).
- Clicking `ws-tree-node-11` updated the URL to
  `/next/workstreams/director:director:8?node=11` (surviving reload) and opened
  `ws-inspector` showing `Filed by #8 Add pagination to the tasks API` and its
  blocking chain. `Back` returned to the stream.

### 2.5 `/next/telemetry` report `[next]`

- Clicking `telemetry-window-30d` updated the URL to
  `/next/telemetry?window=30d`; after `reload()`, `aria-pressed="true"` stayed
  on `30d`.
- Clicking `telemetry-refresh` re-requested `GET /api/metrics?window=30d`.
- Selecting `acme/widgets` in `telemetry-repo-trigger` requested
  `GET /api/metrics?window=7d&repo=acme%2Fwidgets`.
- Active Workstream Directions rendered real links (`/next/task/10`,
  `/next/task/9`, `/next/task/20`); clicking `#10` navigated to `/next/task/10`
  and `Back` returned to `/next/telemetry`.
- With `GET /api/metrics` stubbed to `500`, `telemetry-error` displayed the
  server error message cleanly.

### 2.6 Classic `/boards` index, folders, ordering and load failure `[classic]`

- Clicking `Boards 16` in the classic rail opened `/boards`. The index has no
  search or sort inputs (`0` inputs/selects).
- **Folders and accessible names:** loose boards render first, followed by
  `WT Ops` and `WT Zeta`. The three `Triage` links and their move handles have
  distinct accessible names (`Triage, outside every folder`, `Triage, in WT Ops`,
  `Triage, in WT Zeta`); uniquely named boards use bare names.
- **Ordering in `WT Zeta` (below the first section):**
  - HTML5 dragging `Move WT Zeta Gamma` onto `WT Zeta Alpha` reordered `WT Zeta`
    from `[Alpha, Beta, Gamma]` to `[Gamma, Alpha, Beta]` and persisted across
    reload.
  - Tapping `Move WT Zeta Gamma` offered slots naming folder and neighbour
    (`Move here, above Triage, in WT Zeta`, `Move here, above WT Zeta Beta`,
    `Move here, at the end of WT Zeta`) with no slot adjacent to `Gamma` itself;
    choosing `Move here, at the end of WT Zeta` put `[Alpha, Beta, Gamma]`,
    kept focus on `Move WT Zeta Gamma`, and survived reload.
- **History:** clicking `WT Zeta Alpha` → `Back` restored `/boards` with focus
  on `WT Zeta Alpha`; `Forward` returned to `/boards/board-48`.
- **Load failure:** with `GET /api/boards` returning `500`, `/boards` displayed
  `The boards could not be loaded (store unavailable).` with a `Retry` button
  (never `"No boards yet"`), and the rail omitted `0`.

### 2.7 Classic asynchronous board creation and viewer gating `[classic]`

- From `/?f=state:any:running`, `Create options → New board` opened `/new`
  prefilled with title `Add a board` targeting `grain/config` and carrying
  `f=state:any:running` in the task description. Submitting `create task`
  posted `POST /api/tasks` and queued task `#28` (`Add a board` in
  `grain/config`) while leaving `/api/boards` unchanged at `16`.
- From `/boards`, header `New board` opened `/new` with `query: <see below>`;
  pressing `Escape` returned to `/boards` with focus on `New board`.
- **Viewer (`grain/viewer`):** `New board` (index and rail menu), `Edit board`,
  and `Add panel` were disabled; board, kanban, list and panel move handles and
  `Keep` buttons were absent (`0`). `Edit columns` stayed enabled because
  `board-1`'s column layout is stored in `localStorage`.
- **Contributor (`grain/contributor`):** `New board`, `Edit board`, `Add panel`,
  and kanban/list task move handles (`25` and `8`) were enabled (`tasks:write`),
  while board and panel move handles remained `0` (`boards:write`).

### 2.8 Classic List and Kanban `[classic]`

- On `/boards/board-1`, columns were `Proposed 3`, `Waiting 8`, `Running 2`,
  `Asked 10`, `Failed 0`, `Merging 2`.
- Card markers verified:
  - Task `#11`: `.badge-blocked` oval (`aria-label="Blocked on: #8 (running)"`).
  - `WT standing task` (`#27`): `keeps running`.
  - `WT held proposal` (`#26`): `held for approval by 8`.
  - Task `#12`: `imported`.
- Clicking `Fix flaky retry test` opened `/tasks/2`; `Back` restored focus to
  that card's link.
- Tapping `Move task 22` offered slots identifying tasks by ID and title
  (`Move here, above task 2, “Fix flaky retry test”, in Waiting`), posted
  `POST /api/tasks/reorder {"ids":["22"],"afterId":"11","beforeId":"2"}`, kept
  focus on `Move task 22`, and persisted across reload. `Space` on `Move task 2`
  entered `Stop moving task 2`, and `Escape` cancelled.
- `Edit columns`: `Move Asked left` kept focus on `Move Asked left`; `Remove
  Merging` + `Save` persisted across reload and showed `5 tasks in no column
  (Merging, Deferred, Closed)`; `Reset to default` restored all 6 columns.
- Custom-column `WT Submit Only` hid `Edit columns` (`0`) and rendered
  `12 tasks in no column (Proposed, Running, Asked for a reply, Merging,
  Deferred, Closed)`, distinguishing `Asked for a reply` from the `Asked to
  submit` column it collects.

### 2.9 Classic Gantt, Tree, Dashboard and Report `[classic]`

- **Gantt (`/boards/board-2`):** `Range` (`Last 24 hours`, `Last 7 days`,
  `Last 30 days`, `Last 90 days`, `Everything`) and `Lanes` (`No lanes`,
  `By repo`, `By state`, `By author`) persisted in `localStorage` across reload
  at URL `/boards/board-2`. `Last 24 hours` + `By state` showed `4 tasks outside
  this range` and ordered lanes `Proposed, Waiting, Running, Asked, Merging,
  Deferred` (`Asked` after `Running`). `.gantt-now` rendered, bar labels named
  blocker/open duration (`Waiting, blocked: waiting for 1h 1m, never dispatched`),
  and `Back` from `/tasks/2` restored focus to `Fix flaky retry test`.
- **Tree (`WT Widgets Tree`):** opened at
  `/boards/board-52?f=repo:any:acme/widgets` (surviving reload), excluded
  `grain/config` task `#28`, nested `#25` under `#24` (and `#11`, `#26` under
  `#8`), and opened `/tasks/11` on row click.
- **Dashboard (`WT Dash` & `WT Empty Dash`):**
  - Typing `pagination` set `?q=pagination&f=state:none:deferred|closed`
    (surviving reload) and offered `Keep` on task panels (`State counts`,
    `Week timeline`) but **not** on `Throughput` (`metrics`), whose report text
    search does not alter.
  - Narrowing with `?f=repo:any:acme/widgets` offered `Keep` on `Throughput` too
    (`3` `Keep` buttons); clicking `Throughput`'s `Keep` opened `/new` listing
    every panel with its `order_key`.
  - Tap-moving `Running only` ahead of `State counts` persisted across reload.
  - `8 Waiting` (`/?f=state:none:deferred|closed&f=state:any:queued` → `8` rows)
    and `Running only` `Open` (`/?f=state:any:running` → `2` rows) matched their
    counts and restored focus on browser `Back`.
  - `Add panel` opened `/new` titled `Add a panel to the dashboard "WT Dash"`.
  - `WT Empty Dash` rendered `This dashboard has no panels yet. “Add panel”
    files the task that adds one.` With `GET /api/metrics` returning `500`,
    `Throughput` rendered `The metrics report could not be loaded.` while
    sibling panels rendered normally.
- **Report (`WT Nine` & `WT Widgets Report`):**
  - `WT Nine` drew active sections `Throughput`, `Latency`, `Backlog` and
    omitted the 6 silent sections (`Merge queue`, `Prompts`, `Tool use`,
    `Agents`, `Sandboxes`, `CI waits`) while offering all 9 `Move section`
    handles.
  - Selecting `30 days` set `?window=30d` (surviving reload), and `Refresh`
    requested `/api/metrics?window=30d&buckets=24`.
  - Moving `Backlog` to section 1 kept focus on `Move section 1, “Backlog”` and
    survived reload.
  - Backlog chips matched their destination lists and restored focus on `Back`:
    `Proposed 3` (`3`), `Waiting 8` (`8`), `Running 2` (`2`), `Asked 1` (`1`).
    `task 22` linked to `/tasks/22`. `WT Widgets Report` scoped metrics to
    `acme/widgets`.

### 2.10 Responsive widths, themes, console, network and destructive actions

- At `390×844` (`iPhone 13`, light and dark), `/next/tasks`,
  `/next/tasks?view=timeline`, `/next/workstreams`, `/next/workstreams/task:8`,
  `/next/telemetry`, `/boards`, `board-1`, `board-2`, `WT Widgets Tree`,
  `WT Dash` and `WT Nine` all had `scrollWidth == clientWidth` (`390 == 390`, no
  horizontal overflow).
- Zero uncaught `pageerror` events across all desktop and phone runs, and zero
  unexpected `>= 400` responses (only the intentionally stubbed `500`s).
- Classic UI exposes `0` board delete buttons; `/next/` confirms board deletion
  and was exercised strictly against throwaway boards created in this run.

## 3. Bugs found and fixed

### 3.1 `/next/tasks` Saved Boards menu reported a failed `/api/boards` read as `"No saved boards yet."`

- **Reproduction:** intercept `GET /api/boards` with `500 {"error":"store
  unavailable"}`, open `/next/tasks`, and click `Boards`. `TasksStore.loadBoards`
  swallowed the error, leaving `boards()` at `[]`, so `@empty` rendered `"No
  saved boards yet."`.
- **Fix:** `TasksStore` now records `boardsError` on a failed `GET /api/boards`
  (while keeping any previously loaded boards across transient refresh errors)
  and clears it on success. When `boards()` is empty and `boardsError()` is set,
  `TasksPageComponent` renders `The boards could not be loaded (<reason>).`
  (`data-testid="boards-load-error"`).

### 3.2 `/next/` rail `New → New board` discarded active `/next/tasks` filters and Timeline mode

- **Reproduction:** open
  `/next/tasks?tab=asked&q=merge&repo=acme%2Fwidgets&view=timeline` and choose
  `New → New board` from the left rail (or phone `More` sheet). `#createEntry`
  navigated to `/tasks?newBoard=1` with default query replacement, resetting the
  page to an unnarrowed Backlog list before opening the save bar.
- **Fix:** `CreateItem` supports `mergeQueryOnSameRoute`, enabled on `New board`.
  `AppComponent` tracks `currentPath` and passes `queryParamsHandling="merge"`
  when already on `/tasks`, while still replacing query parameters when arriving
  from another page such as `/inbox`.

### 3.3 `/next/tasks` Backlog row `⋮` menu wrapped to the left edge and opened off-screen beneath the rail

- **Reproduction:** open `/next/tasks` at `1440×900` (where the 30/70 split
  gives `.tasks-list-pane` `410px`, matching `@container (max-width: 620px)`) or
  at `390×844` phone width, and click `⋮` (`menu-11`). Because `.backlog-row`
  adds a `28px` drag handle before the checkbox and status pill (`~144px`),
  `.row-main`'s `flex-basis: calc(100% - 130px)` forced `.row-main` onto line 2
  and left `.row-menu-wrap` (`⋮`) alone on line 3 at `x = 86` (`x = 14` on
  phone). Its right-aligned `.pop-menu` (`right: 0; min-width: 180px`) opened at
  `x = -54..126` (`x = -126..54` on phone), off-screen and underneath
  `<nav class="nav-rail">`, which intercepted clicks on `Move down`.
- **Fix (`tasks-timeline.css`):** under `@container (max-width: 620px)`,
  `.backlog-row .row-main` uses `flex-basis: calc(100% - 165px)` so the title
  stays on line 1 beside the status pill, and `.backlog-row .row-menu-wrap` gets
  `margin-left: auto` so `⋮` always sits in the right-hand slot and its menu
  opens inside the pane (`x = 287..467` on desktop, `x = 196..376` on phone).

### 3.4 Closing the `Save current view as board:` bar dropped focus to `<body>`

- **Reproduction:** open the save bar on `/next/tasks` and press `Escape`, click
  `Cancel`, or submit a board name. Destroying `board-save-form` left
  `document.activeElement` on `<body>`.
- **Fix (`tasks-page.component.ts`):** `closeBoardSave(true)` clears the bar and
  returns focus to `saved-boards-btn`, matching the delete confirmation flow.

### Reviews (Design, Functional, Adversarial)

- **Design:** checked `before` and `after` renders produced by
  `ui/e2e/screenshot.mjs --pages '/next/tasks' --click '[data-testid="menu-11"]'`
  at `1440×900` and `390×844` in light and dark. Backlog rows keep title on line
  1, priority chip and `⋮` aligned on line 2 with `⋮` in the right-hand slot,
  and `.boards-load-error` uses `--md-sys-color-error` (contrast `6.4:1` light,
  `8.9:1` dark).
- **Functional:** verified all four fixes end-to-end against `grain demo`, and
  confirmed all 3 new Playwright scenarios in `ui-ng/e2e/next-saved-boards.spec.js`
  fail when the source fix is stashed (`REVERT_EXIT=1`) and pass with the fix.
- **Adversarial:** scoped `.boards-menu.pop-menu .boards-load-error` to avoid
  global selector collisions under `ViewEncapsulation.None`; verified `New board`
  from `/inbox?item=4` does not leak `item=4` onto `/tasks`; restored worker
  backlog order in `finally` in the e2e spec.

## 4. Follow-ups

None newly filed. The two-grammar gap between `/next/` (`tab`/`group`/`repo`/`view`)
and classic boards (`f=…&sort=…`) is already tracked by task 5385's follow-up
proposal, and standalone `/boards` presentations (`kanban`, `gantt`, `panels`,
`report`, `tree`) remain on the classic UI migration backlog.

## 5. Tests run

| Suite | Result |
| --- | --- |
| `cd ui-ng && npm run lint` | clean |
| `cd ui-ng && npm run build` | clean, within bundle budgets |
| `cd ui-ng && npx vitest run` | all specs passed |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js --project=desktop -g "failed /api/boards\|New board from the rail\|Backlog row More actions"` (fix stashed) | 3 failed, reproducing each bug |
| `cd ui-ng && npx playwright test` (full `/next/` e2e suite across `desktop`, `phone`, `dark`) | 83 passed |
| `cd ui && npx playwright test e2e/boards.spec.js --project=chromium` | 17 passed |
| `go test ./tests/hygiene/... ./pkg/ui/...` | passed |
