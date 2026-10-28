# System Overlay Sandboxes and Caches Tabs Walkthrough (2026-09-25)

## Result

Pass. The automated end-to-end browser walkthrough completed verification for the System Overlay Sandboxes and Caches tabs (`docs/test-plans/2026-09-25.md §3`) against a live `grain demo` instance with real server endpoints, live background CI sandboxes, real package cache metrics, and browser UI interactions.

There were zero unhandled errors, zero page crashes, zero React unmounts, and no state loss during tab transitions. All package cache ecosystems (Go, NPM, PyPI, Debian / apt, Playwright browsers, Docker / OCI layers), Actions cache repository breakdowns, host pressure statistics, sparkline trends, active sandboxes table, and cache pruning / flushing operations functioned deterministically.

The walkthrough tested commit branch `grain/task-666`.

## Environment and Method

- Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64 x86_64.
- Go 1.26.6 and Node v20.19.0.
- Playwright 1.62.1 with Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Production frontend compiled via Vite (`npm run build`) and grain server compiled via `go build -o bin/grain ./cmd/grain`.
- Executed via Playwright test runner against `grain demo -observability` on isolated per-worker ports over embedded throwaway stores.
- Tested across standard desktop viewports (1280x720) and overlay pane layouts.
- All operations exercised actual UI components (`SystemOverlay.jsx`, `SandboxHealthPage.jsx`, `CachesPage.jsx`, `PackageCachePanel.jsx`, `PipelineControl.jsx`, `TopPage.jsx`, `LogsPage.jsx`, `Overlay.jsx`).

## Coverage and Outcomes

| Step / Area | Browser Exercise | Outcome |
| --- | --- | --- |
| 1. System Overlay Invocation | Started `grain demo -observability` with active background CI tasks. Opened the System overlay from the navigation sidebar. | Pass. Overlay opened as a full-height pane beside the sidebar with "System" heading and full tab navigation strip (`Logs`, `Sandboxes`, `Caches`, `Top`, `Pipeline`, `Power`). |
| 2. Sandboxes Tab Metrics & Active VMs | Switched to "Sandboxes" tab. Inspected host load averages (`1/5/15 min`), CPU utilization, memory & swap gauges, host storage allocations, CPU and Memory trend sparklines, and the active sandboxes table. Verified real-time CPU, memory, load average, status chips, and VM configurations for active CI / agent sandboxes. | Pass. Host pressure statistics rendered cleanly. Active sandbox rows listed live VM instances with dynamic load averages, memory readings, CPU/memory sparklines, and status badges. Tested "Refresh" action without layout disruption. |
| 3. Caches Tab & Storage Breakdown | Switched to "Caches" tab. Verified "Proxy cache" and "Actions cache" panels. Inspected storage breakdown and item counts across package ecosystems: Debian (`apt packages`), Python (`Python packages`), Docker / OCI (`Docker Hub layers`), Node (`npm packages`), Go (`Go modules`), and Playwright (`Playwright browsers`). Inspected total space consumed and Actions cache per-repository breakdown (`acme/widgets`, `acme/gadgets`). | Pass. Storage panels rendered exact entry counts and formatted byte sizes across all ecosystems, including Playwright browser binaries and OCI layers. |
| 4. Cache Pruning & Garbage Collection | Triggered per-ecosystem flush (`apt packages`) and whole-cache flush (`Flush the whole cache`). Verified immediate UI state update to 0 B and 0 packages. Tested Actions cache flush for individual repositories. Verified loading indicators and immediate metric updates without overlay dismissal. | Pass. Flush requests successfully invoked `POST /api/package-cache/flush` and `POST /api/actions-cache/flush`. Metrics updated in-place immediately while maintaining overlay focus and scroll position. |
| 5. Seamless Tab Switching & State Retention | Navigated sequentially through all System overlay tabs: `Top` (process snapshot), `Pipeline` (dispatch pause/resume controls), `Power` (danger zone power actions), `Logs` (live daemon log tail), `Sandboxes`, and `Caches`. | Pass. Zero DOM errors, zero React component exceptions, zero console warnings. Cached states and flushed values remained consistent across tab switches. |
| 6. Overlay Dismissal | Closed the System overlay via Escape key and verified full dismiss. Re-opened and closed via the "Back" button in the top-left pane header. | Pass. Escape key and Back button dismissed overlay cleanly, restoring view to the underlying tasks dashboard. |

## Endpoints and API Verification

| Route | Method | Purpose & Verification |
| --- | --- | --- |
| `GET /api/sandboxes` | `GET` | Returns live host pressure readings (CPUs, load averages, memory, swap), sandbox storage, and active sandbox health snapshots. |
| `GET /api/package-cache` | `GET` | Returns per-ecosystem package cache breakdown across apt, pypi, docker, npm, go, playwright, and github. |
| `POST /api/package-cache/flush` | `POST` | Flushes specified ecosystem (`kind: "apt"`, etc.) or entire package cache and returns updated statistics. |
| `GET /api/actions-cache` | `GET` | Returns Actions cache state, total byte usage, maximum capacity, and per-repository breakdown. |
| `POST /api/actions-cache/flush` | `POST` | Flushes specified repository cache or entire Actions cache. |
| `GET /api/host/top` | `GET` | Returns host top process output for the Top tab. |
| `GET /api/logs` | `GET` | Returns tail of daemon journal for the Logs tab. |
| `POST /api/pipeline-pause` | `POST` | Sets deployment-wide pipeline pause state. |

## Test Verification Commands

1. **E2E Playwright Suite**:
   ```bash
   cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/system-overlay-tabs.spec.js e2e/observability.spec.js
   ```
   *Result*: 2 passed (25.9s).

2. **Frontend Vitest Unit Test Suite**:
   ```bash
   cd ui && npm test
   ```
   *Result*: 143 test files passed, 2,542 unit tests passed (178.1s).

3. **Backend Go Package Test Suite**:
   ```bash
   go test ./cmd/grain ./pkg/...
   ```
   *Result*: All Go packages passed.
