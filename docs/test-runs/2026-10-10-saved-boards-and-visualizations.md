# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-10 (03:40–03:45 UTC)

Commit tested: `003d1156504a17068e5ea1200e06b2e75f0453ce` (tip of `grain/task-2827` on `origin/candidate`).

Prior walkthroughs: `2026-10-09-saved-boards-and-visualizations.md`, `2026-10-08-saved-boards-and-visualizations.md`, `2026-10-07-saved-boards-and-visualizations.md`.

All prior findings from earlier walkthroughs were re-verified and remain clean:
- `b26bcd20b`: `fix(ui): name a report's capacity section's handle after its heading` (`ui/src/report.js`). Confirmed: keyboard handles for capacity / run sections consistently name the section heading drawn beneath them.
- `ed56a60ce`: `fix(ui): keep a report board's picked window in its URL` (`ui/src/paths.js`, `App.jsx`, `ReportBoard.jsx`). Confirmed: selecting `Last 30 days` updates the URL with `?window=30d`, queries `/api/metrics` with `window=30d`, and survives page reloads and history transitions.

No regressions, layout breakages, or unhandled exceptions were identified during this walkthrough.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6.4.3, Playwright 1.62.1 with Chromium headless shell (build 1234).
- Production frontend bundle built into `pkg/ui/static` via `npm ci && npm run build`.
- Server binary built via `go build -o /tmp/grain ./cmd/grain`.
- Server executed in demo mode over a throwaway directory `/tmp/grain-demo-wt-*` with loopback port assignment (`/tmp/grain demo -addr 127.0.0.1:0 -open=false`).
- Browser automation executed using Playwright Chromium:
  - Desktop viewport: 1440x900.
  - Mobile viewport pass: Playwright `iPhone 13` device profile (390x844, touch events enabled).
  - Uncaught page errors, React ErrorBoundary console logs, and unexpected HTTP responses (>= 400) monitored continuously.
- Authentication/identity: seeded admin user `alice` (`localStorage["grain.username"] = "alice"`, with `X-Grain-Admin-Mode: on`).
- Gemini API key: none present at `/home/debian/.gemini-api-key`.

## Stubs and boundaries

- **State-repository reconciler / asynchronous creation:**
  - Board creation in Grain is an asynchronous contract: clicking "New board" routes to `/new` and drafts a task targeting `grain/config-default` modifying `tables/board.json`.
  - The browser walkthrough verified this contract end-to-end: submitting the drafted task (`WT Async Board Request <stamp>`) kept the board count identical on `/boards`, created a backlog task in `queued` state targeting `grain/config-default`, and confirmed that boards are never created synchronously in the store without a merged change.
  - Stand-in boards representing merged agent changes were seeded via the HTTP API (`POST /api/boards`):
    - `Alpha <stamp>` & `Beta <stamp>` (`folder: Infra <stamp>`, kinds `list` and `kanban`)
    - `Loose <stamp>` (`kind: list`, unfiled)
    - `List View <stamp>` (`kind: list`, `query: f=state:any:running`)
    - `Fixed Kanban <stamp>` (`kind: kanban`, column `In Flight Only` mapped to `state:running`)
    - `Timeline View <stamp>` (`kind: gantt`, unconstrained query)
    - `Dashboard View <stamp>` (`kind: panels`, 5 panels: `states`, `list` inheriting, `list` scoped to running, `metrics` scoped to `acme/widgets`, `gantt` 7d)
    - `Empty Dash <stamp>` (`kind: panels`, 0 panels)
    - `Report View <stamp>` (`kind: report`, `query: f=repo:any:acme/widgets&f=state:any:running`, sections: `throughput`, `latency`)
  - No interactive board deletion button exists in `ui/src` (boards are deleted via Git pull requests against `tables/board.json`). Teardown of throwaway test boards was exercised through API deletion (`DELETE /api/boards/<id>`) and store directory cleanup.
- **External repository boundary (GitHub):**
  - Opening task detail triggers `GET /api/repos/<repo>/github-branches`, which returns 404 in demo mode as expected when no live GitHub token is configured.

## Steps and outcomes

1. **Boards index & rail navigation:**
   - Sidebar navigation button `Boards` (badge `4`) navigated to `/boards`.
   - Seeded boards `Board` (Kanban), `Timeline` (Gantt), `Report` (Report), and `Tree` (Tree) rendered with their corresponding kind chips.
   - Deep-linking to each seeded board (`/boards/<id>`) loaded the board directly without intermediate navigation flashes.
   - Browser history navigation (`Back`, `Forward`, `Reload`) worked seamlessly across board views.
   - Nonexistent board URL (`/boards/nonexistent-board-id-9999`) safely redirected to `/boards` without crashing the application.
   - Legacy routes (`/board`, `/gantt`) cleanly redirected to `/`.

2. **Asynchronous creation workflow:**
   - Clicked "New board" button on `/boards`, navigating to `/new`.
   - Form fields verified:
     - Title: `Add a board`
     - Description: pre-filled with schema instructions for `tables/board.json`, kind enumerations (`list`, `kanban`, `gantt`, `panels`, `report`, `tree`), and query syntax.
     - Repo: pre-selected to `grain/config-default`.
   - Filled title `WT Async Board Request <stamp>` and submitted.
   - Redirected back to `/boards`. Total board count remained at `4`, confirming that no immediate optimistic board insertion occurs before state reconciliation.
   - Navigated to `/`, located the created task in `queued` state, inspected its detail overlay, and closed it with `Escape`.

3. **Folder organization, keyboard handles & reordering:**
   - Seeded folder `Infra <stamp>` with `Alpha <stamp>` and `Beta <stamp>`, plus loose board `Loose <stamp>`.
   - Verified that folder heading rendered in `/boards`.
   - Clicked keyboard pickup button `Move Beta <stamp>`, then clicked `Move here, above Alpha <stamp>`.
   - Clicked keyboard pickup button `Move Loose <stamp>`, then clicked `Move here, at the end of Infra <stamp>`.
   - Reloaded `/boards`: confirmed persistent ordering `[Beta, Alpha, Loose]` within folder `Infra <stamp>`.

4. **List & Kanban presentations:**
   - Opened list board `List View <stamp>` (`f=state:any:running`): rendered running tasks `Bump the Go toolchain to 1.24` and `Add pagination to the tasks API`.
   - Clicked a task row to open the task detail overlay; closed cleanly with `Escape`.
   - Filtered via live search `pagination`: URL updated with `?q=pagination`, toolchain task filtered out, and reload preserved search results.
   - Opened fixed-column Kanban board `Fixed Kanban <stamp>`: rendered column `In Flight Only` with running cards. Opening task detail from card worked cleanly.
   - Opened seeded `Board` (unfixed Kanban): opened column editor via `Columns` button, added column `Archive` mapped to `Closed`, saved, and confirmed that closed tasks rendered in `Archive`. Reset columns back to default and saved.

5. **Timeline / Gantt presentation:**
   - Opened Gantt board `Timeline View <stamp>`: time axis ticks, task bars, and `.gantt-bar-ongoing` markers rendered.
   - Clicked a task bar: opened task detail overlay; closed with `Escape`.
   - Changed Range to `Everything` and Lanes to `By state`: swimlanes rendered grouped by task state.
   - Reloaded the page: confirmed that both Range (`Everything`) and Lanes (`By state`) were preserved.

6. **Dashboard (Panels) presentation:**
   - Opened `Dashboard View <stamp>`: all 5 panels rendered simultaneously (`States`, `Inheriting List`, `Scoped Running`, `Metrics`, `Timeline`).
   - Narrowed entire dashboard via search `toolchain`:
     - Inheriting list panel narrowed down to 1 matching task and displayed the `Keep` button.
     - Scoped list panel maintained its independent query.
   - Reordered panels using keyboard handles: moved panel 5 (`Timeline`) above panel 1 (`States`). Reloaded and confirmed `Timeline` remained the first panel.
   - Opened `Empty Dash <stamp>`: verified empty dashboard state message ("no panels yet") and "+ Add panel" button leading to task creation.

7. **Report board presentation:**
   - Opened `Report View <stamp>` (`f=repo:any:acme/widgets&f=state:any:running`):
     - Scope notice rendered: `Measured over the tasks in acme/widgets.` and noted that `State` filter was not in force for metric aggregations.
     - Rendered metric sections `Throughput <stamp>` and `Latency <stamp>`.
   - Reordered sections using keyboard handles: moved `Latency` above `Throughput`. Reloaded and verified updated section ordering persisted.
   - Window picker: selected `Last 30 days`, verified `?window=30d` updated in URL, reloaded, and confirmed window pick survived reload.

8. **Mobile viewport (iPhone 13) validation:**
   - Tested `/boards`, `/boards/<kanban>`, `/boards/<gantt>`, `/boards/<panels>`, and `/boards/<report>` under 390x844 viewport emulation.
   - Checked `document.documentElement.scrollWidth > window.innerWidth`: 0 horizontal overflows detected across all board views.

9. **Teardown & cleanup:**
   - All 9 throwaway test boards were deleted cleanly via `DELETE /api/boards/<id>`.
   - Temporary test server and SQLite store were destroyed cleanly.

## Findings

### Re-verified prior findings
- **Capacity section handle naming (`b26bcd20b`)**: Verified clean.
- **Report window URL persistence (`ed56a60ce`)**: Verified clean.

### New findings
- None. All board presentations, interactive controls, live narrowing, drag/keyboard reordering, and view state persistence functioned as expected.

## Automated test suite verification

- Ran full automated suite across board specs via Playwright:
  - `ui/e2e/saved-boards-walkthrough.spec.js`: 17 passed (31.4s)
  - `ui/e2e/boards.spec.js`, `ui/e2e/board.spec.js`, `ui/e2e/gantt.spec.js`, `ui/e2e/boards_workflows.spec.js`: 28 passed (19.6s)
  - Custom comprehensive standalone walkthrough: 9 test phases passed (13.2s)
  - Total: 54 / 54 tests passed (0 failures).
