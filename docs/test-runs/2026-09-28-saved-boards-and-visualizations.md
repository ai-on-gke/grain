# Saved boards and task visualizations walkthrough

Date: 2026-09-28 (03:15 UTC)

Commit tested: `29564f8714e17e363f63c8656949f3ebd3538ee9`

## Environment

- Debian 12 Linux sandbox (`x86_64`); Go 1.26.6 (`go1.26.6 linux/amd64`) and Node.js 20.19.0 (`v20.19.0`).
- Production frontend built with `make build` (Vite 6.4.3 production bundle embedded in `pkg/ui/static`), served by `bin/grain demo` against an isolated, throwaway embedded SQLite store.
- Headless Chromium (Playwright 1.62.1) at 1440x900 viewport, along with WebKit for responsive layout and pointer verification.

## Walkthrough and results

1. **Boards Index & Sidebar Rail Navigation**:
   - Navigated to the `/boards` index via the sidebar rail button and verified the seeded boards (`Board` [Kanban], `Timeline` [Gantt], and `Report` [Metrics]) with the board count badge (>= 3).
   - Confirmed folder grouping, server ordering, sidebar rail synchronized listing, and absence of inappropriate task search/sort controls on the boards index.
   - Verified cold deep-linking to each seeded board (`/boards/<id>`). Tested browser **Back**, **Forward**, and page **Reload**, confirming full state retention across navigation events.
   - Tested nonexistent board ID route recovery (`/boards/nonexistent-board-id-9999`), confirming graceful fallback to the populated `/boards` index. Tested legacy aliases `/board` and `/gantt`, confirming redirect to `/`.

2. **Asynchronous Board Creation Workflow**:
   - Clicked **New board** on `/boards`, opening the task creation form pre-filled with the `tables/board.json` specification contract and `- kind: list` default.
   - Submitted a new board creation task (`Add Task 395 Exploratory Board`).
   - Verified that the task was filed in the general task backlog with its specification markdown intact, while the board count on `/boards` remained unchanged, adhering to the asynchronous state-repository architecture where board records are added via state repo pull requests rather than synchronous ad-hoc DB inserts.

3. **Folder Organization and Reordering**:
   - Seeded test boards (`WT Alpha`, `WT Beta`, `WT Loose`) in a custom folder.
   - Exercised keyboard pickup and placement controls (`MoveHandle` / `MoveSlot`) to reorder `WT Beta` ahead of `WT Alpha` and file `WT Loose` into the folder.
   - Verified that new folder ordering persisted across browser reloads and was mirrored in the sidebar rail.
   - Verified API persistence and order key ordering via `GET /api/boards`.

4. **List and Kanban Presentations**:
   - Opened a scoped List board (`f=state:any:running`), verified task rows, opened task detail overlay by clicking a row, dismissed via Escape, updated search query to `pagination` via the live search field, and confirmed URL query update and reload persistence.
   - Opened a Kanban board with fixed column definitions (`columns: [{ name: "In Flight Only", states: ["running"] }]`) and verified that fixed-layout boards hide the ad-hoc Columns edit button.
   - Opened the default Kanban board (`Board`), accessed the **Columns** editor, added an **Archive** column mapped to `closed`, and verified that closed task cards appeared in the column and persisted across page reload in localStorage. Restored default column configuration.

5. **Timeline and Gantt Presentations**:
   - Opened a Timeline board, verifying ongoing task bar indicators (`.gantt-bar-ongoing`), axis tick markers, and out-of-range indicators for the default 7d window.
   - Adjusted range selector to **Everything** and grouping lanes to **By state**, confirming completed task bars and lane headers.
   - Opened task detail overlay directly from a task bar on the chart, verified dismissal, and confirmed range/lane persistence across page reload.

6. **Dashboard (Panels) Presentation**:
   - Opened a multi-panel dashboard containing States, Inheriting List, Scoped Running List, Scoped Metrics, and Timeline panels.
   - Tested whole-board narrowing via search toolbar (`Search this dashboard…`), confirming that inheriting panels filtered dynamically while scoped panels preserved their explicit queries.
   - Tested the **Keep** button on narrowed panels, verifying pre-filled task creation targeting `tables/board_panel.json` with the narrowed query and panel `order_key`.
   - Verified panel **Open** links navigating to full filtered views, reordered panels using keyboard pickup/drop handles, verified layout persistence across reloads, and inspected the empty dashboard state with its **Add panel** workflow.

7. **Report Board Presentation**:
   - Opened a scoped Report board configured with `throughput` and `latency` sections.
   - Verified the query scoping notice explaining active repo conditions and noting inapplicable state conditions.
   - Reordered report sections using keyboard pickup/drop handles, verified API order persistence, and confirmed layout preservation across page reload.

8. **Deletion Safety**:
   - Verified that the UI exposes no ad-hoc board deletion buttons, preserving repository-driven declarative configuration lifecycle integrity.

## Automated verification

- **Playwright End-to-End Suite**: 32 passed across `board.spec.js`, `boards.spec.js`, `gantt.spec.js`, and `saved-boards-walkthrough.spec.js` (31.7s). Full e2e suite: 190 passed, 1 skipped (3.7m).
- **Vitest Unit Suite**: 2,623 passed across 151 test files (175.33s).
- **Go Backend Test Suite**: `pkg/ui` tests passed (44.79s).
- Zero console errors or unhandled page errors logged during browser interactions.

## Stubs, mutations, and findings

- No mock servers or stubs were used. All tests executed against real `grain demo` backend server processes and Chromium / WebKit browser sessions.
- Direct mutations were strictly limited to throwaway fixture boards and tasks cleaned up after test runs.
- No screenshots or browser trace artifacts committed.
