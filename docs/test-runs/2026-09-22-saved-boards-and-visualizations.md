# Saved boards and task visualizations walkthrough

Date: 2026-09-22 (16:15 UTC)

Commit tested: `69e589cfe0e93ce1d199ffaa72c9b3af39462b9e`

## Environment

- Debian 12 sandbox; Go 1.26.6 and Node 20.19.0.
- Production frontend built with `make build`, then served by the resulting
  `bin/grain demo` against a throwaway embedded SQLite store.
- Headless Chrome for Testing 151, driven by Playwright 1.62.1 at 1440x900.

## Walkthrough and results

1. Opened `/boards` and verified the seeded Board (Kanban), Timeline, and Report
   rows, count 3, server order, rail entries, descriptions, and the absence of
   index search/sort controls. Deep-linking to a nonexistent board ID recovered
   gracefully to the populated `/boards` index. Tested legacy routes `/board` and
   `/gantt`, confirming proper redirection to `/`.
2. Clicked **New board**, inspected the pre-filled title and `tables/board.json`
   contract description, selected the target repository, and submitted the task
   as `Add Task 307 exploratory board`. The resulting task was filed and listed
   on `/` with its board instructions intact. The board count on `/boards`
   remained 3, confirming that board creation is asynchronous and handled via
   state repository tasks.
3. Created fixture boards (List, Kanban, Loose) in a custom folder to test
   organization and ordering. Verified folder headings in the main index, moved
   the Kanban board above the List board using keyboard pickup/drop controls,
   and filed the loose board into the folder. Confirmed order persisted across
   browser reloads and reflected accurately in the sidebar rail.
4. Opened a saved List board scoped to `q=toolchain`. Verified scoped task
   filtering, opened task detail directly from the list row, confirmed modal
   dismissal via Escape, updated search query to `q=pagination` with live URL
   query update, and confirmed filter persistence across page reloads.
5. Opened a saved Kanban board and inspected card distribution across columns.
   Opened task detail from a card, accessed the **Columns** editor, added an
   **Archive** column mapped to the Closed state, and saved the layout.
   Reordered cards within the column using keyboard pickup/drop handles.
   Verified column configurations and card positions persisted across reloads.
   Cross-column dragging is deliberately not used for state changes because
   task state is computed from task lifecycle and events.
6. Opened a Timeline (Gantt) board with ongoing and completed task bars. Verified
   ongoing bar indicators, tick markers, and out-of-range indicators for the
   default 7d range. Widened range to **Everything** and switched grouping lanes
   to **By state**, inspecting lane counts and task bars. Opened task detail
   from a timeline bar and verified that range and lane settings persisted across
   reloads and in the URL state.
7. Opened a multi-panel Dashboard containing States, List (`f=state:any:running`),
   Metrics (`f=repo:any:acme/widgets`), and Gantt panels. Tested whole-board
   narrowing via the search toolbar, verifying that inheriting panels updated
   appropriately while explicitly scoped panels retained their queries. Used
   panel **Open** links to navigate to filtered views, reordered panels using
   keyboard pickup/drop handles, and verified layout persistence across reloads.
8. Opened an empty Dashboard board, observing the empty state explanation and
   the **Add panel** button. Clicking Add panel opened the task submission form
   pre-filled with `tables/board_panel.json` instructions and board ID targeting.
9. Opened a scoped Report board configured with `throughput` and `latency`
   metric sections. Observed the scoping notice explaining active repo conditions
   and noting inapplicable state conditions. Verified metrics and summary data,
   reordered sections using keyboard pickup/drop handles, and confirmed order
   preservation across reloads.
10. Monitored console logs, network responses, and page errors throughout all
    interactive flows. No unexpected console errors, unhandled rejections, or
    4xx/5xx HTTP responses occurred.

## Automated verification

- Playwright End-to-End Suite: 33 passed across `board.spec.js`, `boards.spec.js`,
  `gantt.spec.js`, and `saved-boards-walkthrough.spec.js` (47.9s).
- Vitest Unit Suite: 2,360 passed across 131 test files (202.5s).
- All walkthrough assertions verified in Chromium against live `grain demo` server.

## Stubs, mutations, and findings

No endpoints were stubbed. All interactions ran against the live `grain demo`
server and embedded SQLite store. Direct board and task mutations were confined
to temporary fixture data created during the test run. The UI does not expose
board-deletion controls, which is consistent with the state-repository-backed
architecture. No screenshots or generated browser artifacts were retained.
