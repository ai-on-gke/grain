# Saved boards and task visualizations walkthrough

Date: 2026-10-08 (22:30–23:00 UTC)

Commit tested: `651319b11` (`candidate` tip, `v1.8.0-rc.1-586-g651319b11`).

Prior walkthrough: `2026-10-07-saved-boards-and-visualizations.md`.
Both prior findings from earlier walkthroughs were re-checked and are confirmed fixed on this commit:

- `b26bcd20b`: `fix(ui): name a report's capacity section's handle after its heading` (`ui/src/report.js`). Confirmed: the runs/capacity section's reorder handle accurately reads `Move section 3, “Capacity”` matching the rendered `h3` heading.
- `ed56a60ce`: `fix(ui): keep a report board's picked window in its URL` (`ui/src/paths.js`, `App.jsx`, `ReportBoard.jsx`). Confirmed: selecting `Last 30 days` updates the URL to `?window=30d` and makes a request with `window=30d`; on reload or Back/Forward navigation, the pick and URL survive without resetting to default.

No regressions or new defects were identified in this run.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6, Playwright 1.62.1 with Chromium headless shell (build 1234).
- The frontend was built with `npm ci && npm run build` into `pkg/ui/static`, and the server binary was built with `go build -o /tmp/grain ./cmd/grain`.
- The server was started with `/tmp/grain demo -addr 127.0.0.1:0 -open=false` with a dedicated throwaway directory `/tmp/demo_wt`.
- Real Chromium was driven via Playwright automation scripts outside the repository (`/tmp/wt`, not committed).
  - Desktop viewport: 1440x900.
  - Mobile viewport pass: Playwright's `iPhone 13` device emulation (390x844, touch).
  - Listeners monitored console errors, page errors, uncaught exceptions, and HTTP status >= 400.
- Package installations behind proxy utilized `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`.
- No Gemini API key was present at `/home/debian/.gemini-api-key`, so no Gemini-backed tooling was invoked.
- Identity: seeded user `alice`, initialized through `localStorage["grain.username"]`.

## Stubs and boundaries

- **State-repository reconciler:**
  - Board creation, panel additions, and modifications are asynchronous actions in Grain: "New board" files a task against `grain/config-default` targeting `tables/board.json`.
  - The UI workflow for asynchronous board creation was exercised end-to-end through the browser (submitting a task titled `WT2519 add a board`, verifying the draft values, repo routing, and the resulting task #13 in `queued` state with `grain/config-default` and `autoMerge: true`).
  - Stand-in boards representing merged agent changes were seeded via `POST /api/boards`:
    - `WT List` (`board-5`, `q=pagination`, folder `WT Folder`)
    - `WT Tree` (`board-6`, `f=repo:any:acme/widgets`, folder `WT Folder`)
    - `WT Kanban` (`board-7`, explicit columns: Waiting = `proposed|queued`, Moving = `running`)
    - `WT Dash` (`board-8`, 6 panels: states "Counts", list "Running only", gantt "Week", metrics "Widgets", list "Nothing", inheriting list "Inherits")
    - `WT Report` (`board-9`, `f=repo:any:acme/widgets`, sections: throughput, latency, runs, backlog, merges, sandboxes titled "Sandbox use")
    - `WT Gantt` (`board-10`, `f=repo:any:acme/widgets`)
  - No board deletion controls exist in `ui/src` (boards are deleted via Git PRs against the state repository), so deletion was verified against throwaway demo lifecycle teardown.
- **Injected network faults:**
  - Delayed `/api/metrics` by 1.2s via `page.route` to verify loading states in the dashboard and report.
  - Aborted `/api/metrics` with `connectionfailed` to verify panel error isolation (the metrics panel displayed an error fallback while sibling panels remained responsive).
- **External boundaries (GitHub):**
  - Opening task detail triggers `GET /api/repos/<repo>/github-branches`, which returns 404 on the embedded demo server lacking live GitHub credentials. This 404 is anticipated and handled gracefully.

## Steps and outcomes

1. **Boards index and rail:**
   - Nav rail showed `Boards 10` after seeding boards (4 seeded demo boards + 6 walkthrough boards).
   - Visiting `/boards` displayed badge count `10`.
   - Folder grouping rendered loose boards first, followed by the `WT Folder` section containing `WT List` and `WT Tree`.
   - Navigated to `WT Tree` via direct click, verifying URL `/boards/board-6?f=repo:any:acme/widgets`.
   - Verified browser history: `Back` returned to `/boards`, `Forward` restored the board, and `Reload` preserved the board view.
   - Deep-linking to an invalid board ID (`/boards/nonexistent-board-9999`) safely fell back to `/boards`.

2. **Asynchronous creation:**
   - From `/boards`, clicked "New board" action button, navigating to `/new`.
   - The task creation form pre-filled with:
     - Title: `Add a board`
     - Description: prompt prefilled with instructions referencing `tables/board.json`, available kinds, and query contracts.
     - Repo: defaulted to `grain/config-default`. No warning alerts about off-list repositories were triggered.
   - Submitted a task titled `WT2519 add a board`. The browser redirected back to `/boards`.
   - Task list inspection confirmed task #13 was queued for `grain/config-default` with `autoMerge: true`.
   - Visited task detail page at `/tasks/13`: verified no target repo warning banner appeared.
   - Tested "Edit board" on `WT Report`: navigated to `/new` with description pre-populated with current board configuration (kind, name, query, and section list). Back returned cleanly to the board.

3. **List presentation (WT List):**
   - Opened `WT List` (`board-5`, `q=pagination`). Filtered rows rendered matching tasks (`#8`).
   - Clicked a task row to open the task detail view (`.detail-header`).
   - Pressed `Escape`: detail overlay closed and returned cleanly to the board list.

4. **Kanban presentation (seeded Board, WT Kanban):**
   - Seeded `Board` rendered default columns: `Proposed`, `Queued`, `Running`, `Needs you`, `Queued for merge`.
   - Clicked a `.board-card` to open task detail; `Escape` returned to the Kanban view.
   - Verified keyboard move handle (`Move task 12`) and drop affordances.
   - Opened column editor via `Columns` button: added a new column "Archive" mapped to state `Closed`, saved, and confirmed the column rendered in DOM.
   - Reopened column editor and used "Reset to default" to restore initial column structure.
   - Opened `WT Kanban` (`board-7`) carrying custom columns: rendered `Waiting` (5 cards) and `Moving` (2 cards).

5. **Timeline / Gantt presentation (seeded Timeline, WT Gantt):**
   - Seeded `Timeline` rendered tasks across the time track with axis ticks and the `.gantt-now` current-time marker.
   - Verified Range and Lanes selectors:
     - Initial values: Range = `Last 7 days`, Lanes = `No lanes`.
     - Switched Range to `Everything` and Lanes to `By state`.
     - Reloaded the page: confirmed persistent settings (`Everything` and `By state`) survived reload.
   - Clicked a task bar: opened task detail overlay; `Escape` closed overlay.
   - Opened scoped `WT Gantt` (`board-10`, `f=repo:any:acme/widgets`): loaded smoothly.

6. **Dashboard presentation (WT Dash):**
   - Rendered 6 heterogeneous panels: `Counts` (states), `Running only` (list), `Week` (gantt), `Widgets` (metrics), `Nothing` (list), and `Inherits` (list).
   - Applied whole-board search `pagination`: URL updated with query; inheriting panels updated and exposed `Keep` buttons; scoped panels maintained independent queries.
   - Keyboard reordering handle on panels (`Move panel 1, “Counts”`) operated properly.
   - Empty state check on panel "Nothing" displayed empty placeholder "Nothing here right now."
   - Injected network faults on `/api/metrics`:
     - Under latency (1.2s delay), loading indicator appeared before metrics rendered.
     - Under connection abort, error state displayed inside the panel while sibling panels operated normally.

7. **Report board presentation (WT Report, seeded Report):**
   - `WT Report` rendered metrics sections: `Throughput`, `Latency`, `Capacity`, `Backlog`, and `Sandbox use`.
   - Verified section move handles: all 6 section handles rendered with descriptive names matching their headings (notably `Move section 3, “Capacity”`).
   - Window picker verification:
     - Default was `Last 7 days`.
     - Selected `Last 30 days`: URL updated to include `&window=30d`.
     - Reloaded the page: URL preserved `&window=30d` and window picker displayed `Last 30 days`.
   - Seeded `Report` rendered 11 sections; empty metrics sections displayed clear empty state notes without errors.

8. **Phone viewport validation:**
   - Emulated iPhone 13 (390px width) across `/boards`, `/boards/board-8` (Dashboard), `/boards/board-9` (Report), and `/boards/board-2` (Timeline).
   - Checked `document.documentElement.scrollWidth` against `window.innerWidth`: no horizontal layout overflow (`overflow=false` across all views).

## Findings

### Re-checked prior findings (both verified clean)

1. **Capacity section handle naming (fixed in `b26bcd20b`):**
   - Verified that the capacity/runs section handle is named `Move section 3, “Capacity”` rather than `Attempts`, keeping the handle text aligned with the `h3` heading.

2. **Report window URL persistence (fixed in `ed56a60ce`):**
   - Verified that changing the report window selection from `7d` to `30d` immediately updates the URL with `&window=30d` and that reloading or navigating back/forward retains the selected time range.

### New findings

- None. All board views, panel behaviors, reordering affordances, empty states, error fallbacks, and URL interactions behaved as specified.

## Automated test suite verification

- Executed `ui/e2e/saved-boards-walkthrough.spec.js` using Playwright:
  - 17 test cases executed.
  - 17 passed (0 failed, 0 skipped) in 42.8s.
