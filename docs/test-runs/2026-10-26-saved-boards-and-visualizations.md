# Saved Boards and Visualizations Walkthrough — 2026-10-26

## Commit & Environment

- **Commit tested**: `80f042b219c82c6a4777c9b506c5992d28d01d35` (`branch grain/049f1e09/task-1145`)
- **Date**: `2026-10-26` (UTC)
- **Server**: Real `grain demo` binary built from `./cmd/grain` with the production Vite frontend bundle (`ui/dist` copied into `pkg/ui/static`), running over a fresh throwaway embedded store and git-backed `grain/config` repository (`-open=false -access-code-file <path> -addr 127.0.0.1:18433`).
- **Browser**: Real headless Chromium (`Playwright chromium_headless_shell-1234`) driven via visible UI controls at desktop (`1440×1000`) and mobile (`390×844`, `isMobile: true`, `hasTouch: true`) viewports.
- **Gemini API key**: Not present in this sandbox (`/home/debian/.gemini-api-key` absent).

---

## Stubs & Test Stand-Ins Documented

1. **Stand-in boards via `POST /api/boards` (cleaned up at end of run)**:
   - Because `grain demo` does not run a live agent worker to execute queued tasks against `grain/config`, filing a board-creation task through the UI leaves the board count unchanged until an agent merges the `tables/board.json` change. After verifying that asynchronous contract end-to-end in the browser (creating [Task #13](/tasks/13) on `grain/config` and confirming `/api/boards` stayed at `4`), we seeded 7 temporary stand-in boards (`WT List`, `WT Kanban`, `WT Timeline`, `WT Dashboard`, `WT Report`, `WT Empty Dash`, and `WT Tree`) via `POST /api/boards` to exercise every board presentation (`list`, custom-column `kanban`, narrowed `gantt`, `panels`, custom-section `report`, empty `panels`, and narrowed `tree`) and folder/order combination, then deleted all 7 stand-in boards via `DELETE /api/boards/<id>` at the end of the run and confirmed the board list returned to the 4 seeded boards (`board-1`..`board-4`).
2. **Playwright network route stub on `**/api/metrics*` (unrouted immediately after verification)**:
   - To deterministically verify the loading spinner and error banner on the `report` board and the error state on a `panels` dashboard `metrics` panel against a fast local embedded store, we temporarily intercepted `**/api/metrics*` with a 600ms delay followed by HTTP `500` (`{"error":"synthetic metrics 500"}`), verified both UI states, removed the route intercept (`page.unroute("**/api/metrics*")`), and clicked `Refresh` on the report board to confirm in-place recovery against the live server.

---

## Walkthrough Steps & Outcomes

### 1. Authentication, Legacy/Team/Unknown Routes, Boards Index, Seeded Boards & Rail Pinning

1. **Sign-in**:
   - Opened [`/`](/) in Chromium, entered the demo access code into the `Access code` field, clicked `Continue`, and confirmed the main backlog loaded at [`/`](/).
2. **Team-prefixed, legacy, and unknown board routes**:
   - Navigated to [`/t/default/boards`](/t/default/boards) and confirmed it canonicalized in-place to [`/boards`](/boards) with heading `Boards`.
   - Navigated to [`/t/default/boards/board-1`](/t/default/boards/board-1) and confirmed it canonicalized to [`/boards/board-1`](/boards/board-1).
   - Navigated to legacy routes [`/board`](/board) and [`/gantt`](/gantt) and confirmed both redirected cleanly to [`/`](/).
   - Navigated cold to [`/boards/nope`](/boards/nope) and confirmed it fell back to [`/boards`](/boards) and rendered an alert banner reading `no board "nope"`.
3. **Boards index ([`/boards`](/boards)) & rail**:
   - Clicked `Boards 4` in the left navigation rail (`<aside>`) and confirmed [`/boards`](/boards) rendered with header count `4`, explanatory prose on how boards are stored and requested, and **no** search/filter/sort toolbar (0 search inputs, by design).
   - Verified the 4 seeded unfiled rows in order:
     - `Board` (`Kanban` — `"Every task, in columns of states"`)
     - `Timeline` (`Timeline` — `"Every task as a bar against a clock"`)
     - `Report` (`Report` — `"How this deployment is going, measured"`)
     - `Tree` (`Tree` — `"Every task, under the task that filed it"`)
4. **Cold direct URL loads & browser history**:
   - Loaded [`/boards/board-1`](/boards/board-1) (`Board`, chip `Kanban`), [`/boards/board-2`](/boards/board-2) (`Timeline`, chip `Timeline`), [`/boards/board-3`](/boards/board-3) (`Report`, chip `Report`), and [`/boards/board-4`](/boards/board-4) (`Tree`, chip `Tree`) cold from their URLs.
   - Exercised `Back` (returned to [`/boards/board-3`](/boards/board-3) `Report`), `Forward` (advanced to [`/boards/board-4`](/boards/board-4) `Tree`), and `Reload` (remained on [`/boards/board-4`](/boards/board-4) `Tree`).
5. **Folders, keyboard reordering, equal-`orderKey` neighbor respace & rail pinning**:
   - With the 7 stand-in boards present (total count `11` on [`/boards`](/boards)), verified folder headings `Other` and `Walkthrough` rendered below unfiled boards.
   - Used the keyboard pickup handle on `WT Empty Dash` (`Move WT Empty Dash` → `Enter` → `Move here, at the end of Other` → `Enter`) to move `WT Empty Dash` from `Walkthrough` into `Other`; verified focus stayed on the moved board's handle (`document.activeElement === moveEmptyBtn`) and `PATCH /api/boards/board-10` updated `folder: "Other"`.
   - Used the keyboard handle on `WT Report` (`Move WT Report` → `Enter` → `Move here, above Board` → `Enter`) to move `WT Report` out of `Walkthrough` to the top of the unfiled list.
   - Used the keyboard handle on `Tree` (`Move Tree` → `Enter` → `Move here, above Timeline` → `Enter`) to reorder two seeded boards that shared `orderKey: 0`; verified `boardOrder.js` respaced both neighbors via `PATCH /api/boards/board-4` and `PATCH /api/boards/board-2`.
   - Reloaded [`/boards`](/boards) and confirmed the persisted order: `["WT Report", "Board", "Tree", "Timeline", "Report", "WT Tree", "WT Empty Dash", "WT List", "WT Kanban", "WT Timeline", "WT Dashboard"]`.
   - Opened `Customize pinned items` (`Customize pinned navigation` dialog) from the rail, selected `Type: Board` and `Item: WT Dashboard`, clicked `Pin item` then `Save`, clicked the new `WT Dashboard` pin in the rail to open [`/boards/board-8?f=repo:any:acme/widgets`](/boards/board-8?f=repo:any:acme/widgets), and verified `aria-current="page"` remained set on the pinned rail button both before and after reload.

### 2. Asynchronous Board Creation & Edit Workflows

1. **From narrowed backlog (`Create options` → `New board`)**:
   - Navigated to [`/?f=repo:any:acme/widgets&q=pagination`](/?f=repo:any:acme/widgets&q=pagination), opened `Create options` in the rail header, and clicked `New board`.
   - Verified the `New task` overlay opened with target repository `grain/config`, pre-filled title `"Add a board"`, and description instructing the agent to add a row to `tables/board.json` carrying `q=pagination&f=repo:any:acme/widgets` and listing valid `kind` values (`list`, `kanban`, `gantt`, `panels`, `report`, `tree`).
   - Updated the title to `"WT1145 board for pagination search"` and clicked `Create task`.
   - Verified `POST /api/tasks` returned `201 Created` for [Task #13](/tasks/13) (`state: "queued"`, `repo: "grain/config"`) while `GET /api/boards` still returned `4` boards, confirming the asynchronous creation contract.
2. **From [`/boards`](/boards) header (`New board`)**:
   - Clicked `New board` in the page header on [`/boards`](/boards) and confirmed it opened [`/new`](/new) with `New task` pre-filled with `"Add a board"` targeting `grain/config`.
3. **From a board page (`Edit board`)**:
   - Navigated to [`/boards/board-4?q=retry`](/boards/board-4?q=retry) and clicked `Edit board` in the page header.
   - Confirmed `New task` opened pre-filled with title `"Change the board \"Tree\""` and description referencing `board-4` in `tables/board.json` along with the active narrowing `q=retry`.

### 3. List Presentation (`WT List`, [`/boards/board-5`](/boards/board-5))

1. **Saved query & initial rows**:
   - Opened [`/boards/board-5`](/boards/board-5) (`query: "f=state:any:running"`).
   - Verified the URL hydrated to [`/boards/board-5?f=state:any:running`](/boards/board-5?f=state:any:running), the `State: Running` filter chip rendered, and only the 2 running tasks (`#3` and `#8`) were listed.
2. **Opening task detail & keyboard focus restoration**:
   - Focused task `#3`'s title link (`a.task-title`), pressed `Enter` to open the task detail overlay at [`/tasks/3`](/tasks/3), pressed `Escape` to close it, and verified focus returned to task `#3`'s row link (`focusReturnedToRow: true`).
3. **Removing saved filter (`?f=none`), search & sort**:
   - Clicked the delete icon on the `State: Running` chip; confirmed the URL updated to [`/boards/board-5?f=none`](/boards/board-5?f=none) so the board's saved filter was not re-applied on reload, and all `12` non-session tasks rendered both before and after reload (the 11 seeded non-session tasks plus [Task #13](/tasks/13); interactive session task `#12` `Director Session` is excluded from task list views).
   - Entered `pagination` in `Search tasks…` (`?q=pagination` → tasks `["8", "11", "13"]`), then opened `Backlog order` → `Sort by…` → `Created` (`?q=pagination&sort=created:desc`) and confirmed the list reordered to `["13", "11", "8"]`.

### 4. Kanban Presentation (`board-1` [`/boards/board-1`](/boards/board-1) & `WT Kanban` [`/boards/board-6`](/boards/board-6))

1. **Default columns & hidden-state footer note**:
   - Opened [`/boards/board-1`](/boards/board-1) (`Board`).
   - Verified the 5 default columns and their task cards:
     - `Proposed`: `["1"]`
     - `Queued`: `["2", "11"]`
     - `Running`: `["3", "8"]`
     - `Needs you`: `["4", "6", "7"]`
     - `Merging`: `["5"]`
   - Verified the footer note `.board-hidden-note` read `"3 tasks in no column (Deferred, Closed). Edit columns"` (accounting for `#10` in `deferred`, `#9` in `closed`, and `#13` in `closed`; `#12` `Director Session` is excluded).
2. **Card reordering via keyboard and mouse drag**:
   - Focused `Move task 11`, pressed `Enter`, and activated `Move here, above Fix flaky retry test, in Queued`. Verified focus remained on `Move task 11`, `PUT /api/tasks/11/order` persisted the move, and after reload `Queued` was ordered `["11", "2"]`.
   - Dragged card `#2` above card `#11` with the mouse (`card2.dragTo(card11)`), reloaded [`/boards/board-1`](/boards/board-1), and confirmed `Queued` persisted as `["2", "11"]`.
   - Clicked card `#2` (`Fix flaky retry test`) to open [`/tasks/2`](/tasks/2) in the detail overlay and closed it with `Escape`.
3. **Column editor, unsaved-changes confirmation, persistence & reset**:
   - Clicked `Columns` to open the `Board columns` overlay, edited `Column 1 title` from `Proposed` to `Ideas`, and pressed `Escape`.
   - Verified the browser `confirm` dialog fired with `"You have unsaved changes here. Leave and discard them?"`; dismissed it and confirmed the editor remained open (`editorStillOpenAfterDismiss: true`).
   - Clicked `+ Add column`, named the new column `Parked` with state `Deferred`, clicked `Move Parked left` to place it before `Merging`, and clicked `Save`.
   - Reloaded [`/boards/board-1`](/boards/board-1) and verified the columns persisted in `localStorage` as `["Ideas", "Queued", "Running", "Needs you", "Parked", "Merging"]` and `.board-hidden-note` updated to `"2 tasks in no column (Closed). Edit columns"`.
   - Re-opened `Columns`, clicked `Reset to default`, then `Save`, reloaded, and confirmed the columns returned to `["Proposed", "Queued", "Running", "Needs you", "Merging"]`.
4. **Board-defined custom columns (`WT Kanban`, [`/boards/board-6`](/boards/board-6))**:
   - Opened [`/boards/board-6`](/boards/board-6) (`columns: [{ name: "Triage", states: ["proposed", "queued"] }, { name: "Active", states: ["running", "awaiting_reply"] }]`).
   - Verified only `["Triage", "Active"]` rendered and the `Columns` customization button was absent (`wtKanbanHasColumnsBtn: 0`), since the board's own column definition governs the view.

### 5. Timeline / Gantt Presentation (`board-2` [`/boards/board-2`](/boards/board-2) & `WT Timeline` [`/boards/board-7`](/boards/board-7))

1. **Today marker, dependency badge & bar aria-label**:
   - Opened [`/boards/board-2`](/boards/board-2) (`Timeline`).
   - Confirmed axis ticks (`.gantt-tick`) and the today/now marker line (`.gantt-now`, `hasTodayMarker: true`) rendered.
   - Inspected task `#11` (`Extract pagination query helper`, which depends on `#8`) and confirmed its state badge tooltip read `"Blocked on: #8 (running)"` and its `.gantt-bar` `aria-label` read `"Queued, blocked: waiting for 40m, never dispatched"`.
2. **Range, lanes, out-of-range note & reload persistence**:
   - Changed `Range` from `Last 7 days` to `Last 24 hours` and `Lanes` from `No lanes` to `By author`.
   - Confirmed the out-of-range note updated to `"2 tasks outside this range."` and lane headers rendered for `["debian", "director"]`.
   - Reloaded [`/boards/board-2`](/boards/board-2) and confirmed `Range` (`Last 24 hours`) and `Lanes` (`By author`) persisted via `localStorage` (`grain.gantt.range`, `grain.gantt.lanes`).
   - Clicked the bar for task `#2` (`Fix flaky retry test`), confirmed the task detail overlay opened at [`/tasks/2`](/tasks/2), closed it with `Escape`, and restored `Range` to `Last 7 days` and `Lanes` to `No lanes`.
3. **Saved narrowing on `WT Timeline` ([`/boards/board-7`](/boards/board-7))**:
   - Opened [`/boards/board-7`](/boards/board-7) (`query: "f=state:any:running|queued"`) and confirmed only the 4 non-session running/queued tasks (`["2", "11", "3", "8"]`) rendered on the timeline.

### 6. Tree Presentation (`board-4` [`/boards/board-4`](/boards/board-4) & `WT Tree` [`/boards/board-11`](/boards/board-11))

1. **Parent-child nesting (`generatedFrom`)**:
   - Opened [`/boards/board-4`](/boards/board-4) (`Tree`).
   - Verified task `#12` (`Director Session`) rendered as a parent node with direct children `["1", "3", "8"]`, and task `#8` (`Add pagination to the tasks API`) rendered with nested child `["11"]` (`Extract pagination query helper`).
2. **Search, saved narrowing, sorting & reload persistence**:
   - Typed `pagination` into `Search tasks…` on [`/boards/board-4`](/boards/board-4), waited for `?q=pagination`, reloaded, and confirmed the tree retained matching tasks `["13", "8", "11"]` with `#11` nested under `#8`.
   - Opened [`/boards/board-11`](/boards/board-11) (`WT Tree`, `query: "f=state:any:queued|running"`), reloaded, and confirmed the tree preserved both its narrowing and hierarchy (`["12", "3", "8", "11", "2"]`).
   - On [`/boards/board-4`](/boards/board-4), clicked `Newest first` → `Sort by…` → `Title` (`?sort=title:asc`) and verified roots and sibling subtrees reordered alphabetically while keeping `#1`, `#8` → `#11`, and `#3` nested under `#12` (`["6", "12", "1", "8", "11", "3", "2", "4", "9", "5", "10", "7", "13"]`).
3. **Opening task detail vs. console session from tree rows**:
   - Clicked task `#3` (`Bump the Go toolchain to 1.24`) on the tree and confirmed the task detail overlay opened at [`/tasks/3`](/tasks/3).
   - Clicked the root session node `#12` (`Director Session`) on [`/boards/board-4`](/boards/board-4) and confirmed it navigated to the live session view at [`/session/director`](/session/director).

### 7. Dashboard Presentation (`WT Dashboard` [`/boards/board-8`](/boards/board-8) & `WT Empty Dash` [`/boards/board-10`](/boards/board-10))

1. **Multi-panel layout, counts, truncation & empty panel state**:
   - Opened [`/boards/board-8`](/boards/board-8) (`WT Dashboard`, `query: "f=repo:any:acme/widgets"`).
   - Verified all 6 panels rendered with expected scoping and counts:
     - `States` (`count: "11"`, per-state tiles for `acme/widgets` non-session tasks)
     - `Inheriting List` (`count: "11"`, footer `"Showing 8 of 11."`)
     - `Scoped Running` (`query: "f=state:any:running"`, `count: "2"`)
     - `Metrics` (`range: "30d"`, sparklines and totals over `acme/widgets`)
     - `Timeline` (`range: "24h"`, `count: "11"`)
     - `Nothing` (`query: "q=zzzznomatch"`, `count: "0"`, empty state `"Nothing here right now."`)
2. **Whole-board narrowing & `Keep` button contract**:
   - Typed `pagination` into `Search this dashboard…` (`?f=repo:any:acme/widgets&q=pagination`).
   - Verified the `Keep` button (`.panel-keep`) appeared **only** on the 4 panels that inherit the board's narrowing (`States`, `Inheriting List`, `Metrics`, `Timeline`) and **not** on `Scoped Running` or `Nothing` (which define their own `query`).
   - Clicked `Keep this narrowing on “Inheriting List”` and confirmed `New task` opened with title `"Narrow the panel \"Inheriting List\" on the dashboard \"WT Dashboard\""` and description referencing `tables/board_panel.json` and `` `order_key` 200 ``.
3. **Keyboard panel reordering & reload persistence**:
   - Focused `Move panel 6, “Nothing”`, pressed `Enter`, and activated `Move here, above panel 1, “States”`.
   - Verified focus remained on the moved panel's handle (`Move panel 1, “Nothing”`, `panelFocusKept: true`), `PUT /api/boards/board-8` persisted the new panel order, and after reload the panels rendered in order `["Nothing", "States", "Inheriting List", "Scoped Running", "Metrics", "Timeline"]`.
4. **Links out of panels**:
   - Clicked `Open` on `Scoped Running` → navigated to [`/?f=state:any:running`](/?f=state:any:running) showing `2` task rows.
   - Clicked the `Running` tile on `States` → navigated to [`/?f=repo:any:acme/widgets&f=state:any:running`](/?f=repo:any:acme/widgets&f=state:any:running).
   - Clicked `Open` on `Metrics` (`30d`) → navigated to [`/system/metrics?f=repo:any:acme/widgets&f=created:within:30`](/system/metrics?f=repo:any:acme/widgets&f=created:within:30).
5. **Unrecognized filter conditions & empty dashboard state**:
   - Navigated to [`/boards/board-8?f=nosuchattr:any:val&f=state:bogus:running`](/boards/board-8?f=nosuchattr:any:val&f=state:bogus:running) and verified the narrowing bar rendered warning chips `["Unknown condition: nosuchattr", "Unknown condition: state:bogus"]`.
   - Opened [`/boards/board-10`](/boards/board-10) (`WT Empty Dash`), verified the empty state message `"This dashboard has no panels yet. “Add panel” files the task that adds one."`, and clicked `Add panel` to confirm `New task` opened with title `"Add a panel to the dashboard \"WT Empty Dash\""`.

### 8. Report Presentation (`board-3` [`/boards/board-3`](/boards/board-3) & `WT Report` [`/boards/board-9`](/boards/board-9))

1. **Repo scoping, ignored non-repo filter notice & section rendering**:
   - Opened [`/boards/board-9`](/boards/board-9) (`WT Report`, `query: "f=repo:any:acme/widgets&q=ignored"`, sections `Throughput`, `Custom Latency`, `Backlog`, `Prompts`, `Merge queue`).
   - Verified the scope subtitle read `"Measured over the tasks in acme/widgets."` and the partial-scope notice read `"A report is measured over whole distributions, so the rest of this question is not in force: the search for \"ignored\"."`
   - Confirmed the 5 sections rendered in order (`Throughput`, `Custom Latency`, `Backlog`, `Prompts`, `Merge queue`), with empty sections named on the board (`Prompts`, `Merge queue`) explicitly rendering `<Section>: nothing to report over this window.` rather than blank headings.
2. **Window selector, section reordering & underlying task link**:
   - Changed `Window` from `Last 7 days` to `Last 24 hours`, confirmed the URL updated with `window=24h`, reloaded, and confirmed `Window` stayed on `Last 24 hours`.
   - Used the keyboard handle `Move section 5, “Merge queue”` → `Enter` → `Move here, above section 1, “Throughput”` → `Enter`. Verified focus remained on `Move section 1, “Merge queue”` (`sectionFocusKept: true`), `PUT /api/boards/board-9` persisted the section order, and after reload the section handles remained `["Move section 1, “Merge queue”", "Move section 2, “Throughput”", "Move section 3, “Custom Latency”", "Move section 4, “Backlog”", "Move section 5, “Prompts”"]`.
   - In the `Backlog` section, clicked the `Oldest queued: task 2` button and confirmed it opened the task detail overlay at [`/tasks/2`](/tasks/2).
3. **Loading, error & `Refresh` recovery**:
   - With `**/api/metrics*` stubbed (600ms delay + HTTP `500`), navigated to [`/boards/board-9`](/boards/board-9) and confirmed the `Loading the report` spinner appeared (`sawLoadingSpinner: true`) followed by the error alert `"The report could not be loaded (synthetic metrics 500). Refresh to try again."`
   - Navigated to [`/boards/board-8`](/boards/board-8) (`WT Dashboard`) and confirmed the `Metrics` panel rendered `"The metrics report could not be loaded."` while sibling task panels continued to render normally.
   - Removed the route stub (`page.unroute("**/api/metrics*")`), returned to [`/boards/board-9`](/boards/board-9), and clicked `Refresh` to verify the report reloaded cleanly in place.

### 9. Destructive & Confirmation Controls

- Verified no delete button is exposed on [`/boards`](/boards) or on any individual board page (board removal happens through `grain/config`).
- On the throwaway task created in Step 2 ([Task #13](/tasks/13)), clicked `Close` (state transitioned to `closed`), clicked `Reopen` (state transitioned back to `queued`), and clicked `Close` again (`closed`).
- Exercised the unsaved-changes confirmation dialog on the Kanban `Board columns` overlay (`"You have unsaved changes here. Leave and discard them?"`).
- Deleted all 7 temporary stand-in boards via `DELETE /api/boards/board-5`..`board-11` (`204 No Content`) and verified `GET /api/boards` returned only `["board-1", "board-4", "board-2", "board-3"]`.

### 10. Mobile Viewport (`390×844` Touch) & Console/Network Hygiene

- Opened a mobile context (`390×844`, `isMobile: true`, `hasTouch: true`), signed in, and visited all 12 board routes ([`/boards`](/boards), `board-1`..`board-4`, and `board-5`..`board-11`).
- Confirmed `document.documentElement.scrollWidth <= window.innerWidth` on all 12 routes (`phoneOverflows: []`).
- Tapped `Move panel 1` on [`/boards/board-8`](/boards/board-8) in the mobile viewport, verified the `"Moving 1 panel"` banner appeared with drop slots, and tapped `Cancel`.
- Checked console errors, uncaught page errors, and HTTP `>= 400` responses across both desktop and mobile contexts:
  - **Uncaught page errors**: `0`.
  - **Console / network errors**: Only the expected initial `401 Unauthorized` on `/api/auth/status` prior to sign-in, the expected `404 Not Found` on `/api/repos/<repo>/github-branches` when opening task detail in a demo server started without `-github-sim`, and the intentional synthetic `500 Internal Server Error` on `/api/metrics` during the error-state test. Zero unexpected console or network errors occurred.

---

## Bugs Found

None. Every saved board presentation (`list`, `kanban`, `gantt`, `tree`, `panels`, `report`), creation/editing flow, narrowing/search/sort control, drag/keyboard/touch reorder interaction, and persistence contract functioned as designed.
