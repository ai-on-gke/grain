# Saved Boards and Task Visualizations Walkthrough — 2026-10-22

Date: 2026-10-22 (09:31–10:16 UTC)

Commit tested: `747f8245502f719f8fca60f125e9bc545128819e` (`Merge pull request #4758 from bwsalmon/grain/049f1e09/task-166`, tip of `origin/candidate`, checked out as `grain/049f1e09/task-177`). Both fixes below were rebuilt into the production frontend bundle (`pkg/ui/static`) and `/tmp/grain` binary and re-verified in a fresh Playwright browser run against the live `grain demo` server.

Prior walkthroughs: `2026-10-15-saved-boards-and-visualizations.md` and the earlier reports it lists. Note on route structure since the 2026-10-15 run: the `/t/<team>` prefix was retired in `grain/task-156`, so `/t/default/boards` now normalizes to `/` and the boards index lives at `/boards` (and individual boards at `/boards/<id>`).

## Environment

- Debian Linux sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Playwright (`@playwright/test` 1.59.1 from `ui/node_modules`) driving Chromium headless shell 151.0.7859.0 (`1223`).
- Frontend build: `cd /home/debian/work/ui && npm run build` into `/home/debian/work/pkg/ui/static`.
- Server build: `cd /home/debian/work && go build -o /tmp/grain ./cmd/grain` (embedding the built frontend assets in `pkg/ui/static`).
- Server invocation: `/tmp/grain demo -addr 127.0.0.1:8787 -open=false -access-code-file /tmp/wt-demo/access-code` running in a fresh throwaway directory `/tmp/wt-demo` over `grain demo`'s embedded SQLite store and demo seed.
- Browser: real headless Chromium via Playwright (`1440×1000` desktop viewport and `390×844` touch/mobile viewport), authenticated via `/__grain/access?code=<hex>` with `localStorage["grain.username"] = "debian"`. Every script monitored `pageerror`, `console` errors, and HTTP responses `>= 400`.
- Gemini key check: `/home/debian/.gemini-api-key` is not present in this sandbox; no Gemini-backed model or external LLM call was needed for these UI workflows.
- Sandbox package note: Chromium was installed once via `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt npx playwright install chromium` (`~110 MB`, ~5 seconds). Baking the Playwright Chromium browser cache into the sandbox image would save that first-run download across UI walkthrough tasks.

## Stubs and stand-ins

- **Merged board changes (stand-in, not a stub).** In production, requesting a new or edited board queues an agent task against the deployment's `grain/config` repository (`tables/board.json`, `tables/board_column.json`, `tables/board_panel.json`, `tables/board_report_section.json`). Because an isolated demo sandbox does not run or merge an external agent PR against a state repository, after exercising and verifying the real UI task-creation workflows (Step 2), we populated the additional board shapes (`list`, custom-column `kanban`, repo-scoped `gantt`, multi-panel `panels` dashboard, empty `panels` dashboard, repo-scoped `report`, and narrowed `tree`) through the server's real `POST /api/boards` endpoint:
  - `WT Running List` (`board-5`): `kind: "list"`, `folder: "Walkthrough"`, `query: "f=state:any:running"`, `orderKey: 100`.
  - `WT Custom Kanban` (`board-6`): `kind: "kanban"`, `folder: "Walkthrough"`, `orderKey: 200`, custom columns `Triage` (`proposed`, `queued`) and `Active` (`running`, `awaiting_reply`).
  - `WT Widgets Timeline` (`board-7`): `kind: "gantt"`, `folder: "Walkthrough"`, `query: "f=repo:any:acme/widgets"`, `orderKey: 300`.
  - `WT Dashboard` (`board-8`): `kind: "panels"`, `folder: "Walkthrough"`, `orderKey: 400`, 5 panels (`States` (`states`), `Inheriting List` (`list`, `query: ""`), `Scoped Running` (`list`, `query: "f=state:any:running"`), `Metrics` (`metrics`, `query: "f=repo:any:acme/widgets"`, `range: "30d"`), and `Timeline` (`gantt`, `range: "24h"`, `width: "full"`)).
  - `WT Scoped Report` (`board-9`): `kind: "report"`, `folder: "Walkthrough"`, `query: "f=repo:any:acme/widgets&q=ignored"`, `orderKey: 500`, sections `throughput`, `latency` (titled `Custom Latency`), `backlog`, and `prompts`.
  - `WT Empty Dash` (`board-10`): `kind: "panels"`, `folder: "Walkthrough"`, `orderKey: 600`, `panels: []`.
  - `WT Narrowed Tree` (`board-11`): `kind: "tree"`, `folder: ""`, `query: "q=pagination"`.
- **Synthetic `/api/metrics` latency and error stub (`page.route`).** Used only in Step 8 to observe the loading state (`400 ms` delay) and HTTP `500` error rendering (`{"error":"synthetic metrics 500"}`) on both a Report board and a Dashboard `MetricsPanel` without breaking the real server.
- **GitHub branches endpoint in demo mode (`GET /api/repos/<repo>/github-branches`).** Returns `404` when opening a task detail overlay because demo mode has no upstream GitHub credentials; the UI handles this gracefully. Zero uncaught `pageerror`s and zero unexpected `>= 400` responses occurred across the entire walkthrough.

## Walkthrough steps and outcomes

### 1. Boards index (`/boards`), rail navigation, direct URLs, legacy routes, and Back/Forward/reload

- **Rail and index:** Clicking `Boards 4` in the left navigation rail opened `/boards` with heading `Boards`, total count `4`, and the four seeded unfiled boards in order:
  1. `Board` (`board-1`, `Board` chip, `How your tasks are grouped into columns`)
  2. `Timeline` (`board-2`, `Timeline` chip, `When tasks ran and what overlapped`)
  3. `Report` (`board-3`, `Report` chip, `Throughput, cycle time and where work waited`)
  4. `Tree` (`board-4`, `Tree` chip, `Tasks nested under the task or chat that filed them`)
  - The `/boards` index intentionally presents no search box or sort dropdown of its own; boards are organized by folder and manual `Move <board>` ordering.
- **Direct URLs and legacy redirects:**
  - Cold-loading `/boards/board-1`, `/boards/board-2`, `/boards/board-3`, and `/boards/board-4` opened each presentation directly and updated the document title (`Board — grain`, `Timeline — grain`, `Report — grain`, `Tree — grain`).
  - Visiting legacy `/t/default/boards` redirected cleanly to `/` (`grain/task-156` retired `/t/<team>` prefixes).
  - Visiting legacy `/board?q=retry` redirected to `/?q=retry`, preserving the query string on the task list.
  - Visiting legacy `/gantt` redirected to `/`.
  - Visiting an unknown board `/boards/no-such-board` recovered cleanly by redirecting to `/boards` and displaying the alert banner `Board “no-such-board” is no longer in the list.`.
- **History and reload:** Navigating `/boards` → `Board` (`/boards/board-1`) → `Timeline` (`/boards/board-2`), then pressing browser `Back` twice and `Forward` once restored `/boards/board-1`, `/boards`, and `/boards/board-1` in order, and `page.reload()` preserved the active board and its narrowing.

### 2. Asynchronous board creation and modification workflows

- **New board from narrowed task list (`/?f=state:any:awaiting_reply`):**
  - Opened `Create options` (`button.split-caret`) next to `New task` in the top bar and clicked `New board`.
  - Verified the `New task` modal opened pre-populated with:
    - `Repository`: `grain/config`
    - `Title`: `Add a board for state:any:awaiting_reply`
    - `Description`: instructions referencing `tables/board.json` (`id`, `name`, `folder`, `kind`, `description`, `query`, `order_key`), `tables/board_column.json`, `tables/board_panel.json`, `tables/board_report_section.json`, and `Query on screen: f=state:any:awaiting_reply`.
  - Retitled the draft to `Add a board for awaiting_reply walkthrough` and clicked `Create Task`.
  - Confirmed `POST /api/tasks` succeeded (`201`) and queued task `13` (`state: "queued"`, `repo: "grain/config"`), verifying the asynchronous task-proposal creation contract end-to-end.
- **New board from `/boards` index:** Clicking `New board` in the `/boards` header opened the `New task` modal targeting `grain/config` with title `Add a board`.
- **Edit board from Kanban (`Board`, `/boards/board-1`):** Clicking `Edit board` opened the `New task` modal with title `Change the board "Board"` and description carrying the board's current column definitions (`Proposed`, `Queued`, `Running`, `Waiting`, `Done`, `Stopped`) and `tables/board_column.json` schema.
- **Edit board / New board from Report (`Report`, `/boards/board-3?window=7d`):** Switching the report window to `Last 7 days` (`?window=7d`) and clicking `Edit board` pre-populated the prompt with `Window on screen: 7d` alongside the report's sections (`throughput`, `latency`, `scheduling`, `outcomes`, `backlog`, `utilization`, `prompts`).

### 3. Folder organization, board ordering, and keyboard/touch handles on `/boards`

- With the stand-in boards created in the `Walkthrough` folder, `/boards` displayed count `11`, grouped with unfiled boards (`Board`, `Timeline`, `Report`, `Tree`, `WT Narrowed Tree`) above the `Walkthrough` folder (`6` boards).
- Exercised keyboard/click pickup reordering on `/boards`:
  - Clicked `Move WT Custom Kanban` (`aria-pressed="true"`), exposing drop targets `Move here, above WT Running List` and `Move here, below WT Empty Dash`.
  - Clicked `Move here, above WT Running List`, which issued `POST /api/boards/board-6/move` (`200`) and placed `WT Custom Kanban` first in the `Walkthrough` folder.
  - Reloaded `/boards` and confirmed `WT Custom Kanban` remained above `WT Running List` and remained focused after the keyboard move.
- Verified on a mobile/touch viewport (`390×844`, `hasTouch: true`) that the `Move Board` handle is immediately visible (`opacity: 1`).

### 4. List and Kanban presentations

- **Saved List board (`WT Running List`, `/boards/board-5`):**
  - Opened with `f=state:any:running` active (`3` matching tasks: `1`, `3`, `8`) and no `Columns` button (list boards hide the Kanban column editor).
  - Typed `toolchain` in `Search tasks…`, narrowing the list to `1` row (`Bump the Go toolchain`) and updating the URL to `/boards/board-5?f=state%3Aany%3Arunning&q=toolchain`.
  - Clicked `Reset` to return to the board's saved query (`3` rows).
- **Saved Kanban board with custom columns (`WT Custom Kanban`, `/boards/board-6`):**
  - Rendered only the board's two configured columns (`Triage` with 4 cards and `Active` with 5 cards), hiding the browser-local `Columns` button because the board defines its own columns in `tables/board_column.json`.
- **Default Kanban board (`Board`, `/boards/board-1`):**
  - Rendered 6 default columns (`Proposed (1)`, `Queued (3)`, `Running (3)`, `Waiting (2)`, `Done (2)`, `Stopped (1)`).
  - Clicked a card both on its title link and off its title (on the card body/chips) to open the task detail overlay (`DetailOverlay`), and pressed `Escape` to close the overlay and verify focus returned to the card's `.row-link`.
  - **Card reordering:** In the `Queued` column (`Director Session`, `Fix flaky retry test`, `Extract pagination query helper`), clicked `Move task 11, “Extract pagination query helper”` and then `Move here, above task 2, “Fix flaky retry test”`. Confirmed `POST /api/tasks/11/move` succeeded and the new order (`12`, `11`, `2`) survived `page.reload()`.
  - **Column editor (`BoardColumnsOverlay`) and dirty-close confirmation:**
    - Clicked `Columns` to open `Board columns`.
    - Clicked `Add column`, typed `Review`, toggled the `PR open` state pill (moving it from `Done` to `Review`), and reordered `Review` with its `Move column 7, “Review”` handle.
    - Clicked `Save` and reloaded `/boards/board-1`, confirming the `Review (1)` column persisted in `localStorage["grain.board.columns.v1"]`.
    - Re-opened `Columns`, typed an unsaved edit into a column title, and pressed `Escape`: verified `window.confirm("You have unsaved changes here. Leave and discard them?")` fired. Dismissing the confirm kept the dialog open; accepting it discarded the unsaved edits.
    - Re-opened `Columns`, clicked `Reset to defaults` → `Save`, restoring the 6 default columns.

### 5. Timeline and Gantt presentations (`Timeline`, `/boards/board-2`, and `WT Widgets Timeline`, `/boards/board-7`)

- **Markers and bars:** Verified `.gantt-now` (today/now vertical marker), `.gantt-row-blocked` (`blocked` chip on task `11`), `.gantt-bar-ongoing` (pulsing right edge on running tasks `1`, `3`, `8`), and `.gantt-bar-wait` (waiting segments).
- **Range and lane controls:**
  - Switched `Range` across `Last 24 hours` (`24h`), `Last 7 days` (`7d`), `Last 30 days` (`30d`), `Last 90 days` (`90d`), and `All shown` (`all`).
  - Switched `Lanes` across `None` (`none`), `Repository` (`repo` → `acme/widgets`, `acme/gadgets`, `acme/docs`), `State` (`state` → `Proposed`, `Queued`, `Running`, `Waiting`, `Merged`, `Closed`, `Cancelled`, `Failed`), `Author` (`author` → `debian`), and `Director` (`director`).
  - Reloaded `/boards/board-2` with `Range: Last 30 days` and `Lanes: Repository` and confirmed both settings persisted across reload via `localStorage["grain.gantt.v1"]`.
- **Repo-scoped timeline (`WT Widgets Timeline`, `/boards/board-7`):** Loaded cold with `f=repo:any:acme/widgets`; switching `Lanes` to `Repository` rendered only the single `acme/widgets` lane.
- **Task bar interaction:** Clicked `.gantt-bar` on row `1` (` Fix OAuth redirect loop on mobile Safari`), verified task `1`'s detail overlay opened, and pressed `Escape` to close and return focus to row `1`'s `.row-link`.

### 6. Tree presentation (`Tree`, `/boards/board-4`, and `WT Narrowed Tree`, `/boards/board-11`)

- **Parent-child hierarchy:** On `/boards/board-4`, verified `Director Session` (`12`) expanded with children `1`, `3`, and `8` (`li[data-task-id="12"] ul.task-tree-children > li`), and `8` (`Add pagination to the tasks API`) nested `11` (`Extract pagination query helper`) one level deeper (`12 → 8 → 11`).
- **Narrowing, reload persistence, and empty state:**
  - Typed `pagination` into `Search tasks…`, narrowing the tree to tasks `8` and `11` (`8 → 11`) and updating the URL to `/boards/board-4?q=pagination`.
  - Reloaded `/boards/board-4?q=pagination` and confirmed the search box (`pagination`) and narrowed tree (`8`, `11`) survived reload.
  - Typed `zzzz-no-match`, verifying the empty state `No tasks match this search and these filters.` and `Clear` button, then clicked `Clear` to restore all 12 tasks.
- **Row and link clicks:** Verified modified clicks (`Ctrl`/`Meta` click) on `a.task-tree-id` left `defaultPrevented === false` so the browser opens a new tab, while clicking anywhere on a `.task-tree-row` (including off the ID link) opened the task detail overlay and restored focus to `a.task-tree-id` on `Escape`.

### 7. Dashboard (`panels`) presentation (`WT Dashboard`, `/boards/board-8`, and `WT Empty Dash`, `/boards/board-10`)

- **Panel rendering and scoping:**
  - `States` rendered state count tiles (`Proposed 1`, `Queued 4`, `Running 3`, `Waiting 2`, `Done 2`, `Stopped 1`).
  - `Inheriting List` rendered all 13 tasks (`Open` → `/`), while `Scoped Running` rendered only the 3 running tasks (`Open` → `/?f=state:any:running`).
  - `Metrics` rendered `Filed 8`, `Completed 1`, `Scoped to acme/widgets` (`Open` → `/metrics?f=repo:any:acme/widgets`).
  - `Timeline` rendered the embedded Gantt chart (`width: "full"`) without a duplicate toolbar (`Open` → `/`).
- **Whole-dashboard narrowing and `Keep`:**
  - Typed `mobile` in `Search this dashboard…` (`/boards/board-8?q=mobile`): `States` updated to `Running 1`, `Inheriting List` narrowed to 1 row (`Fix OAuth redirect loop on mobile Safari`), `Scoped Running` kept its own query (`3` rows), and `Metrics` displayed the notice `the dashboard's search for "mobile" does not apply to this report`.
  - Clicked `Keep` on `Inheriting List`, verifying the `New task` modal opened with title `Narrow the panel "Inheriting List" on the dashboard "WT Dashboard"` and description referencing `tables/board_panel.json`, `order_key 200`, and `q=mobile`.
- **Adding and reordering panels:**
  - Clicked `Add panel` in the dashboard header, verifying the `New task` modal opened with title `Add a panel to the dashboard "WT Dashboard"`.
  - Clicked `Move panel 5, “Timeline”` and dropped it onto `Move here, above panel 1, “States”`. Confirmed `POST /api/boards/board-8/panels/move` succeeded, `Timeline` moved to slot 1, focus stayed on the moved panel's handle, and the new order survived `page.reload()`.
- **Empty dashboard (`WT Empty Dash`, `/boards/board-10`):** Rendered `This dashboard has no panels yet.` alongside the `Add panel` button.

### 8. Report (`report`) presentation (`Report`, `/boards/board-3`, and `WT Scoped Report`, `/boards/board-9`)

- **Scoped report (`WT Scoped Report`, `/boards/board-9`):**
  - Rendered the 4 configured sections (`Throughput`, `Custom Latency`, `Backlog`, `Prompts`) and two scope notices:
    - `Measured over the tasks in acme/widgets.`
    - `This board's query also sets the search for "ignored", which does not apply to a metrics report and was ignored.`
  - Switched the window selector from `Last 14 days` to `Last 7 days`, confirming the URL updated to `/boards/board-9?window=7d` and persisted across `page.reload()`.
  - Reordered `Prompts` above `Throughput` via `Move section 4, “Prompts”` → `Move here, above section 1, “Throughput”`, confirming `POST /api/boards/board-9/sections/move` persisted the new section order across reload.
  - Clicked the `task 2` data link inside the `Backlog` section ("Oldest open task: 2 days 2h (task 2)"), confirming task `2`'s detail overlay opened in place.
- **Loading and error states (`page.route` on `/api/metrics`):**
  - With a `400 ms` delay and HTTP `500` (`{"error":"synthetic metrics 500"}`) on `/api/metrics`:
    - `/boards/board-9` showed `Loading report…` during the delay and then rendered `The report could not be loaded (synthetic metrics 500).` inside `.metrics-error`.
    - `/boards/board-8` (`WT Dashboard`) rendered `The metrics report could not be loaded.` inside the `Metrics` panel while all other panels on the dashboard continued rendering normally.

### 9. Destructive actions and confirmation checks

- Confirmed that the UI exposes no destructive delete buttons for boards, panels, or report sections (structural board changes go through `Edit board` / `grain/config` tasks).
- Confirmed that the Kanban column editor (`BoardColumnsOverlay`) guards unsaved edits with `window.confirm("You have unsaved changes here. Leave and discard them?")` on `Escape`, backdrop click, or `Close`.

---

## Bugs found and fixed

### Bug 1 (Fixed): Returning to `/boards` dropped focus to `<body>` after opening a board off its title link or after opening a task inside a board

- **Symptom:**
  - **1A (Off-title click on `/boards`):** Clicking a board row on `/boards` anywhere outside its `a.row-link` title (for example, on its `.chips` presentation badge or `.template-desc` description) opened the board, but neither focused the row's `.row-link` nor recorded `returnFocusTo`. Returning to `/boards` via `← Boards` or browser `Back` left `document.activeElement` on `<body>`. (The same off-title click gap existed on `RepoList` and `ProcessesList`.)
  - **1B (Opening a task inside a board overwrote the board's return-focus target):** Even when a board was opened from `/boards` via `Enter` or by clicking its title link directly, opening any task row or card inside that board called `rememberReturnFocus("/tasks/<id>")`, overwriting the single module-level `let returnFocusTo = null` in `ui/src/components/ListPrimitives.jsx`. Because opening a task opens `DetailOverlay` without unmounting the board's task rows, `"/tasks/<id>"` was never consumed on mount, and returning to `/boards` via `← Boards` or browser `Back` failed to match any `/boards/<id>` link and dropped focus to `<body>`.
- **Deterministic reproduction:**
  1. Navigate to `/boards`.
  2. Click on the description text (`How your tasks are grouped into columns`) of the `Board` row to open `/boards/board-1`, then click `← Boards` (or press `Alt+Left`): `document.activeElement` is `<body>` instead of `<a href="/boards/board-1">Board</a>`.
  3. Alternatively, on `/boards`, focus `<a href="/boards/board-1">Board</a>` and press `Enter` to open `/boards/board-1`. Click any task card (`Fix flaky retry test`) to open its detail overlay, press `Escape` to close the overlay, and click `← Boards`: `document.activeElement` is `<body>` instead of `<a href="/boards/board-1">Board</a>`.
- **Fix:**
  - In `ui/src/components/ListPrimitives.jsx`:
    - Keyed `returnFocusTo` by route namespace (`href.split("/")[1] || ""`, separating `"boards"`, `"repos"`, `"tasks"`, and `"processes"`) in a `Map` instead of a single scalar variable, so opening a `/tasks/<id>` link inside a board or repo never clobbers the `/boards/<id>` or `/repos/<name>` link that opened the parent view.
    - Updated `focusRowLink(e)` so clicking a row container off its `.row-link` anchor also calls `rememberReturnFocus(link.getAttribute("href"))`.
  - In `ui/src/components/BoardsPage.jsx`, `ui/src/components/RepoList.jsx`, and `ui/src/components/ProcessesList.jsx`:
    - Called `focusRowLink(e)` in the row `onClick` handlers so off-title clicks focus the row's `.row-link` and record its `href` for return focus.
- **Regression coverage:**
  - Unit test in `ui/src/components/BoardsPage.test.jsx`: `remembers the opened board across off-title clicks and task opens inside the board`.
  - E2E test in `ui/e2e/saved-boards-walkthrough.spec.js` (`5c`): verifies both opening a board via an off-title click on `.template-desc` and opening a board via `Enter` → opening a task card → closing the task → clicking `← Boards` return focus to `a.row-link` (`Board`).

### Bug 2 (Fixed): Modified clicks (`Ctrl`/`Cmd`/`Shift`/`Alt` or middle-click) on Dashboard `Open` links and state count tiles hijacked the current tab

- **Symptom:** On a Dashboard (`panels`) board (`ui/src/components/PanelBoard.jsx`), both the panel header `Open` link (`a.panel-open`) and the state count tiles (`a.panel-state` in `StateCountsPanel`) had `onClick` handlers that unconditionally called `e.preventDefault()` when `onOpen` was provided, ignoring modifier keys (`e.metaKey`, `e.ctrlKey`, `e.shiftKey`, `e.altKey`) and non-primary mouse buttons (`e.button !== 0`). As a result, `Ctrl+Click`, `Cmd+Click`, `Shift+Click`, or middle-click on `Open` or a state tile navigated the current dashboard tab in-app instead of letting the browser open the target view in a new tab or window.
- **Deterministic reproduction:**
  1. Open any Dashboard board (e.g. `/boards/board-8`).
  2. `Ctrl+Click` (or `Shift+Click` / `Cmd+Click`) on a panel's `Open` link (`a.panel-open`) or a state count tile (`a.panel-state`): `e.defaultPrevented` is `true` and the current tab navigates away from `/boards/board-8` to `/?...` or `/metrics?...`.
- **Fix:**
  - In `ui/src/components/PanelBoard.jsx` (`StateCountsPanel`'s `a.panel-state` and `Panel`'s `a.panel-open`), added an early return when `e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey` so modified clicks and middle-clicks are left to the browser while plain primary clicks continue to navigate in-app via `onOpen`.
- **Regression coverage:**
  - Unit test in `ui/src/components/PanelBoard.test.jsx`: `leaves modified clicks on Open and state counts to the browser`.
  - E2E assertions in `ui/e2e/saved-boards-walkthrough.spec.js` (`6. Dashboard (Panels) presentation`): verifies `Shift`-clicking `a.panel-open` and `a.panel-state` leaves `e.defaultPrevented === false` and keeps the dashboard page at `/boards/<id>`.
