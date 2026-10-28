# Saved boards and task visualizations walkthrough

Date: 2026-10-09 (00:05–00:20 UTC)

Commit tested: `689a1954a9968d70b858a5a103cbf789e378e824` (candidate merged tip, `origin/candidate` @ `b5c8f2526`).

Prior walkthroughs: `2026-10-08-saved-boards-and-visualizations.md`, `2026-10-07-saved-boards-and-visualizations.md`.
All prior findings from earlier walkthroughs were re-verified and remain clean:

- `b26bcd20b`: `fix(ui): name a report's capacity section's handle after its heading` (`ui/src/report.js`). Confirmed: the runs/capacity section's keyboard reorder handle reads `Move section 3, “Capacity”` matching the rendered `h3` heading.
- `ed56a60ce`: `fix(ui): keep a report board's picked window in its URL` (`ui/src/paths.js`, `App.jsx`, `ReportBoard.jsx`). Confirmed: selecting `Last 30 days` updates the URL with `?window=30d` and fetches `window=30d`; on reload or Back/Forward navigation, the window pick persists.

No regressions or new defects were identified in this run.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6, Playwright 1.62.1 with Chromium headless shell (build 1234).
- The frontend assets were built with `npm ci && npm run build` into `pkg/ui/static`, and the server binary was built with `go build -o /tmp/grain ./cmd/grain`.
- The server was started with `/tmp/grain demo -addr 127.0.0.1:0 -open=false` with a dedicated throwaway directory `/tmp/grain-demo-wt-*`.
- Real Chromium was driven via Playwright automation scripts outside the repository (/tmp/wt, not committed).
  - Desktop viewport: 1440x900.
  - Mobile viewport pass: Playwright's `iPhone 13` device emulation (390x844, touch).
  - Listeners monitored console errors, page errors, uncaught exceptions, and HTTP status >= 400.
- Package installations behind proxy utilized `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`.
- No Gemini API key was present at `/home/debian/.gemini-api-key`, so no Gemini-backed tooling was invoked.
- Identity: seeded user `alice`, initialized through `localStorage["grain.username"]`.

## Stubs and boundaries

- **State-repository reconciler:**
  - Board creation, panel additions, and modifications are asynchronous actions in Grain: "New board" files a task against the team's config repository (`grain/config-default`) targeting `tables/board.json`.
  - The UI workflow for asynchronous board creation was exercised end-to-end through the browser (submitting a task titled `WT Add Custom Triage Board <stamp>`, verifying the draft values, repo routing, and the resulting task in `queued` state with `grain/config-default` and `autoMerge: true`).
  - Stand-in boards representing merged agent changes were seeded via `POST /api/boards`:
    - `WT List <stamp>` (`kind: list`, `query: f=state:any:running`, folder `Walkthrough`)
    - `WT Kanban <stamp>` (`kind: kanban`, folder `Walkthrough`, explicit columns: In Flight Only = `state:running`)
    - `WT Timeline <stamp>` (`kind: gantt`, `query: repo:any:acme/widgets`)
    - `WT Dashboard <stamp>` (`kind: panels`, 5 panels: states "States", inheriting list "Inheriting List", scoped list "Scoped Running", metrics "Metrics", gantt "Timeline")
    - `WT Report <stamp>` (`kind: report`, `query: f=repo:any:acme/widgets&f=state:any:running`, sections: throughput, latency)
  - No board deletion controls exist in `ui/src` (boards are deleted via Git PRs against the state repository), so deletion was verified against throwaway demo lifecycle teardown and API deletion of test fixtures.
- **External boundaries (GitHub):**
  - Opening task detail triggers `GET /api/repos/<repo>/github-branches`, which returns 404 on the embedded demo server lacking live GitHub credentials. This 404 is anticipated and handled gracefully.

## Steps and outcomes

1. **Boards index and rail:**
   - Nav rail showed `Boards 4` (3 seeded demo boards: Board, Timeline, Report + 1 default).
   - Visiting `/boards` displayed badge count `4`.
   - Seeded boards `Board`, `Timeline`, and `Report` were listed under their template names.
   - Verified browser history: `Back` returned to `/`, `Forward` restored `/boards`, and `Reload` preserved the board index.
   - Deep-linking to an invalid board ID (`/boards/nonexistent-board-id`) safely fell back to `/boards`.

2. **Asynchronous creation:**
   - From `/boards`, clicked "New board" action button, navigating to `/new`.
   - The task creation form pre-filled with:
     - Title: `Add a board`
     - Description: prefilled prompt referencing `tables/board.json`, available kinds, and query contracts.
     - Repo: defaulted to `grain/config-default`. No warning alerts about off-list repositories were triggered.
   - Submitted a task titled `WT Add Custom Triage Board <stamp>`. The browser redirected back to `/boards`.
   - Board count remained unchanged at 4, respecting the asynchronous creation contract where boards appear only after agent merge.
   - Navigated to task backlog `/`, confirmed the filed task row was visible in `queued` state with repo `grain/config-default`. Opened task detail and pressed `Escape` to close.

3. **List presentation (WT List):**
   - Opened `WT List` (`f=state:any:running`). Filtered rows rendered matching running tasks (`Bump the Go toolchain to 1.24`, `Add pagination to the tasks API`).
   - Clicked a task row to open the task detail view (`.detail-header`).
   - Pressed `Escape`: detail overlay closed and returned cleanly to the board list.

4. **Kanban presentation (seeded Board, WT Kanban):**
   - Opened `WT Kanban` carrying custom fixed column `In Flight Only` (rendered 2 running cards).
   - Clicked card: opened task detail overlay; `Escape` returned to the Kanban view.
   - Opened seeded `Board` with unfixed columns: opened column editor via `Columns` button.
   - Verified presence of "Board columns" heading and "Reset to default" button affordances. Closed editor with `Escape`.

5. **Timeline / Gantt presentation (WT Timeline):**
   - Opened `WT Timeline` (`kind: gantt`, `query: repo:any:acme/widgets`).
   - Axis ticks and the `.gantt-now` current-time marker rendered correctly.
   - Verified Range and Lanes selectors:
     - Initial values: Range = `Last 7 days`, Lanes = `No lanes`.
     - Switched Range to `Everything` and Lanes to `By state`.
     - Reloaded the page: confirmed persistent settings (`Everything` and `By state`) survived reload.
   - Clicked a task bar: opened task detail overlay; `Escape` closed overlay.

6. **Dashboard presentation (WT Dashboard):**
   - Rendered 5 panels: `States`, `Inheriting List`, `Scoped Running`, `Metrics`, and `Timeline`.
   - Verified panel keyboard reorder handle (`button[aria-label*='Move panel']`).
   - Applied whole-board search `toolchain`: URL updated with query; inheriting panels filtered down and exposed `Keep` buttons; scoped panels maintained independent queries.

7. **Report board presentation (WT Report):**
   - `WT Report` rendered metrics sections: `Throughput` and `Latency`.
   - Verified section move handles: handles rendered with descriptive names matching their headings (`Move section 1, “Throughput <stamp>”`, `Move section 2, “Latency <stamp>”`).
   - Window picker verification:
     - Default was `Last 7 days`.
     - Selected `Last 30 days`: URL updated to include `&window=30d`.
     - Reloaded the page: URL preserved `&window=30d` and window picker displayed `Last 30 days`.

8. **Phone viewport validation:**
   - Emulated iPhone 13 (390px width) across `/boards`, `/boards/<dash>`, `/boards/<report>`, `/boards/<timeline>`, and `/boards/<kanban>`.
   - Checked `document.documentElement.scrollWidth` against `window.innerWidth`: no horizontal layout overflow (`overflow=false` across all views).

9. **Cleanup:**
   - Deleted throwaway boards via `DELETE /api/boards/<id>` and cleaned up filed creation task.

## Findings

### Re-checked prior findings (both verified clean)

1. **Capacity section handle naming (fixed in `b26bcd20b`):**
   - Verified that capacity section handle names accurately match the rendered heading.

2. **Report window URL persistence (fixed in `ed56a60ce`):**
   - Verified that changing report window selection updates the URL parameter `&window=30d` and survives reload/history navigation.

### New findings

- None. All board views, panel behaviors, reordering affordances, empty states, error fallbacks, and URL interactions behaved as specified.

## Automated test suite verification

- Executed `ui/e2e/saved-boards-walkthrough.spec.js`, `ui/e2e/boards.spec.js`, `ui/e2e/board.spec.js`, and `ui/e2e/gantt.spec.js` using Playwright:
  - 44 test cases executed across suites.
  - 44 passed (0 failed, 0 skipped) in 41.8s.
