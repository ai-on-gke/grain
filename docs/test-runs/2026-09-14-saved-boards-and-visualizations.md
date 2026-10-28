# Saved boards and visualizations walkthrough

Date: 2026-09-14 (08:51 UTC)

Commit tested: `da20fbe72b9492ac6c3193511115173cbdcb174e`

## Environment

- Debian 12 sandbox; Go 1.26.6 and Node 20.19.0.
- Production frontend built with `make build`, then served by the resulting
  `bin/grain demo` against a new throwaway embedded SQLite store.
- Headless Chrome for Testing 151, driven by Playwright 1.62.1 at 1440x900.
- Gemini 2.5 Flash was used to pressure-test the exploratory checklist. Its API
  key was read only into the request process environment and was not logged or
  written.

## Walkthrough and results

1. Opened `/boards` and verified the seeded Board, Timeline, and Report rows,
   count 3, server order, rail entries, descriptions, and the absence of index
   search/sort controls. A nonexistent board deep link recovered to the
   populated Boards index rather than a blank or crash.
2. Clicked **New board**, inspected the prewritten title and
   `tables/board.json` instructions, chose **No repo**, disabled immediate
   queueing, and submitted it as `Add Task 40 exploratory board`. The resulting
   task was visibly Proposed and retained the board instructions; the board
   count remained 3. This confirms that the UI requests asynchronous
   state-repository work instead of creating a board directly.
3. Added four fixture boards to the same throwaway store (list, six-panel
   dashboard, empty dashboard, and scoped report) so presentations absent from
   the demo seed could be exercised. On the index, verified the `Task 40`
   folder and order, moved a board with keyboard pickup/drop controls, reloaded,
   and confirmed both index and rail order persisted.
4. Opened the saved list from the index and its direct URL. Verified its saved
   `q=toolchain` scope, task count, task-detail opening, Back/Forward, reload,
   live search to `q=pagination`, and Created/Newest sort encoded as
   `sort=created:desc`; the URL and controls survived reload.
5. Opened seeded Kanban from the rail and a card into task detail. Added an
   Archive column for Closed, moved the column with keyboard and mouse button
   activation, saved, and reloaded. Reordered the two Running cards once with
   mouse drag and once with keyboard pickup/drop, reloading after each; card and
   column order persisted. Cross-column card movement is deliberately absent
   because task state is derived, not directly writable.
6. Opened seeded Timeline, selected Everything by keyboard and By state by
   mouse, and inspected lane counts, ticks, ended/ongoing bars, wait segments,
   task tooltips, and the in-range now marker. Opened a task from a bar, went
   Back, and reloaded; the board URL plus range/lane settings persisted.
   Dependency arrows are deliberately not part of this historical timeline;
   dependencies affect task state instead of scheduling bar endpoints.
7. Loaded a six-panel dashboard under 800 ms of browser network latency and
   observed the metrics panel's Loading state followed by real data. Exercised
   dashboard-wide `q=toolchain` narrowing: inheriting states/list/timeline
   panels narrowed to one task, the `q=retry` panel remained independently
   scoped, and the metrics tooltip explicitly named search/state/kind clauses
   that distributions cannot apply. Verified a naturally empty list panel,
   underlying list links, task-detail opening, and keyboard panel reorder with
   reload persistence.
8. Went offline only after the app and rail were loaded, opened the dashboard,
   and observed the metrics panel's isolated “could not be loaded” state while
   the other panels stayed usable. Restored connectivity and reopened the board
   to verify recovery. Clicked **Add panel**, submitted `Add a Task 40 dashboard
   panel` as a Proposed task, and confirmed its detail named the dashboard id
   and `tables/board_panel.json`. The separate empty dashboard showed its empty
   explanation and Add panel recovery action.
9. Opened the scoped report under 800 ms latency and observed its real loading
   indicator. Verified the 30-day window, `acme/widgets` scope, visible notice
   that the saved State condition is not in force, throughput and backlog
   metrics, explanatory prose, tool/CI sections, and the underlying oldest-task
   link. Moved CI evidence to the first section with keyboard pickup/drop and
   confirmed order and URL after reload. This build's report section vocabulary
   is metric-oriented; it has no separate prose or task-list section kinds, so
   their equivalents exercised here were section explanation text and the
   underlying task link.
10. Watched console errors, page errors, failed requests, and HTTP responses for
    the whole run. The passing proof run had no unexpected console/page errors,
    failed requests, or HTTP 4xx/5xx responses. The only console failures were
    the deliberately induced offline icon and metrics requests; one task poll
    aborted during navigation, as expected.

## Automated verification

- Exploratory Playwright proof driver: all 12 workflow checkpoints passed.
- `npx vitest run src/panelOrder.test.js src/sectionOrder.test.js`: 16 passed.
- Corrected panel case repeated three times in Chromium: 3 passed.
- `npx playwright test e2e/board.spec.js e2e/boards.spec.js
  e2e/gantt.spec.js --project=chromium --workers=1`: 24 passed.

## Stubs, mutations, and findings

No response was stubbed. Four fixture board rows were inserted through the real
demo server solely to provide dashboard and scoped variants missing from the
seed. Loading and error states used browser-level latency/offline conditions
against real endpoints, not synthetic responses. The demo intentionally has no
configured model, Git credential, dispatcher, or state repository, so the two
UI-created requests remained Proposed; that unavailable asynchronous boundary
was not simulated.

All direct board, panel, section, task-order, and task mutations were confined
to the throwaway store or browser local storage. The UI exposes no board-delete
control, so no board deletion or confirmation was attempted. No screenshots or
browser artifacts were retained.

No product defect reproduced in the walkthrough. The focused browser regression
run did expose a deterministic stale assertion in
`e2e/boards.spec.js`: after moving an initially unnumbered dashboard's last
panel to the front and then between its siblings, the test expected every
stored key to be positive. Since `fb415fd16`, zero is deliberately preserved
when any sibling is numbered; the real stored order was the correct strictly
ascending `0, 0.5, 1`. The e2e assertion and its obsolete comments were
updated to the current store contract. Reproduction before the correction:
`npx playwright test e2e/boards.spec.js --project=chromium --workers=1 --grep
'drags a panel into place' --repeat-each=3` (3/3 failed).
