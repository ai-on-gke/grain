# Deployment Settings and Operations UI Walkthrough Report

**Test Date:** 2026-09-28  
**Target Branch:** `grain/task-946`  
**Execution Environment:** Debian GNU/Linux 12 (bookworm) amd64, Linux 6.1.0-53-amd64, Node.js v20.19.0, Go 1.26.6, Playwright 1.62.1 / Chromium & WebKit (headless)  
**Server Under Test:** Live `grain demo -observability` backend server over throwaway embedded SQLite store and compiled Vite frontend  
**Commit Tested:** `9c59b3d70b6c07df9e1e7096a9aa93dc838deb1c` (`Merge pull request #2217 from bwsalmon/grain/task-942`)  
**Automated Spec:** `ui/e2e/deployment_operations_walkthrough.spec.js`  
**Related Specs:** `ui/e2e/config.spec.js`, `ui/e2e/observability.spec.js`, `ui/e2e/upgrade.spec.js`, `ui/e2e/shell.spec.js`, `ui/e2e/system-overlay-tabs.spec.js`, `ui/e2e/narrow.spec.js`, `ui/e2e/phone.spec.js`  

---

## 1. Executive Summary

This walkthrough exercised the Grain deployment configuration, observability, and operator-control user interfaces against a live `grain demo -observability` backend and compiled frontend using Playwright Chromium and WebKit browsers.

All operator screens, modal dialogues, preference controls, system monitors, log search tools, and dangerous action confirmation boundaries were validated. Dangerous host boundaries (restart, reboot, shutdown) and external provider boundaries (Gemini model discovery, secrets validation, upgrade daemon, synthetic CI runs) were intercepted to ensure the underlying sandbox host and credentials remain untouched while exercising the full UI and error-handling workflows.

### Summary Matrix

| Section / Screen | Target Capabilities & Interactions | Result | Notes |
| :--- | :--- | :--- | :--- |
| **Settings Navigation & Layout** | User, Team, and System tabs; subtabs navigation; horizontal tab strip scrolling; keyboard reachability. | **PASS** | Tab containers handle horizontal overflow smoothly; focus navigation across subtabs functions as expected. |
| **Local Preferences** | Dark/Light theme mode; oldest/newest timeline order; persistence across hard page reloads. | **PASS** | Preferences stored in `localStorage` (`grain.themeMode`, `grain.timelineOrder`) and applied immediately to DOM. |
| **General & Engine Settings** | Environment name, closed tasks visibility default, poll interval, max worker agents, blur-to-save persistence. | **PASS** | Form inputs validate, preview, save to backend store, and persist across reloads. |
| **Connections & Models** | Team connections, modular models selection, Gemini fallback when upstream catalog is unavailable. | **PASS** | Fallback UI gracefully handles unlisted models and reasoning effort selection. |
| **Sandbox & Infrastructure** | CPU allocation, memory limit, disk space, bootstrap setup script, restart-required notices. | **PASS** | Hardware budget updates save cleanly and present restart notices where necessary. |
| **Capabilities, Secrets & Upgrade** | Capability toggles, fake secret rejection display, state repo browser, upgrade schedule & cancellation. | **PASS** | Secret submission failure cleanly displays validation error; upgrade scheduling lifecycle functional. |
| **Sandboxes Observability** | Host load averages (1/5/15 min), memory commitment, swap, nested virt, disk floors, 24-worker table, error states. | **PASS** | Rich host pressure metrics and large table scrolling verified; simulated HTTP 500 and disabled states handled gracefully. |
| **CI Observability & Detail** | Active sandboxes, cache stats, run status filters, job/step logs, run cancellation confirmation, task link. | **PASS** | Deep links (`/ci/:id`) render step logs; cancellation dialog prevents accidental deletion; task-to-run navigation verified. |
| **System Logs, Top & Power** | Case-insensitive log search, top process auto-refresh, pipeline pause/resume modals, power confirmation modals. | **PASS** | Search and process monitor operable; dangerous power actions confirmed via modal dialogs before dispatch; zero host mutation. |
| **Metrics Dashboard** | Throughput, Capacity, Latency, Backlog sections, 7d/30d/90d time windows, refresh and error handling. | **PASS** | Metrics panels render trends accurately; network error banners render on backend failure. |
| **Responsive Tablet Shell** | 820x1180 viewport, standing host banner (`hostSandboxes: true`), sidebar and operator footer containment. | **PASS** | Sidebar links remain fully visible and operable without vertical clipping. |

---

## 2. Detailed Verification by Area

### 2.1 Settings: Navigation, Preferences, Configuration & Upgrade

1. **Tab Traversal and Horizontal Overflow**:
   - Loaded `/settings` on a 700px viewport.
   - Traversed category tabs: `User settings`, `Team settings`, and `System settings`.
   - Verified subtabs:
     - `Team settings`: `Team`, `Members`, `Capacity`, `Git identity`, `Connections`, `Models`, `Workflow & Tasks`.
     - `System settings`: `General & Deployment`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Integrations & Auth`, `All Teams`, `Config repo`, `Upgrade`.
   - Confirmed horizontal scrolling on tab container without layout distortion.
2. **Local Appearance & Timeline Preferences**:
   - Selected `Dark` theme mode and `Oldest first` timeline ordering in `User settings`.
   - Verified immediate theme style change and persistence in `localStorage`.
   - Hard-reloaded `/settings`; verified settings remained `dark` and `oldest`.
3. **General and Engine Settings Persistence**:
   - Set Environment name to `ops-walkthrough-2026`.
   - Checked `Show closed tasks by default`.
   - Navigated to `Engine & Concurrency`; updated poll interval to `45s` and max worker agents to `3`.
   - Reloaded page; verified persisted values in backend store.
4. **Model Discovery Fallback**:
   - Intercepted `/api/models/gemini` with `{ available: false, models: [], efforts: [] }`.
   - Verified form allows manual input for `geminiModel` (`gemini-2.5-pro`) and reasoning effort without breaking.
5. **Sandbox Hardware Configuration**:
   - Configured sandbox CPUs (`3`), memory (`4096 MiB`), disk (`40 GiB`), and bootstrap script (`# demo ops bootstrap`).
   - Verified restart requirement notices and verified saved values upon page reload.
6. **Fake Secret Validation & Upgrade Scheduling**:
   - Intercepted `PUT /api/secrets/**` with `400 Bad Request`.
   - Attempted to save dummy token; verified client UI cleanly displays error message without storing fake credentials.
   - Navigated to `Upgrade` tab, configured branch `grain/task-310-upgrade` with `Wait until this deployment is idle` checked.
   - Clicked `Schedule upgrade`; confirmed scheduled status banner rendered.
   - Clicked `Cancel`; confirmed schedule cancellation.

---

### 2.2 Sandboxes: Observability, Pressure & Table Views

1. **Host Pressure & Resource Allocation**:
   - Loaded `/sandboxes` backed by fixture containing 16 CPUs, load averages `14.1 / 12.5 / 10.2`, 27.8 GB memory usage, 980 MB swap, nested virt enabled, and disk storage stats.
   - Verified all indicators rendered accurately in the dashboard header.
2. **24-Worker Sandbox Table**:
   - Verified table rendering of 24 worker rows with mixed statuses (agent/ci, running, error).
   - Inspected row `sb-4` displaying error state and error reason (`qemu crash`).
   - Verified vertical table scrolling and responsive column layout.
3. **Error & Disabled States**:
   - Intercepted `/api/sandboxes` returning `500 Internal Server Error`; verified error banner display.
   - Intercepted `/api/sandboxes` returning `{ enabled: false }`; verified friendly "not available" state banner.

---

### 2.3 CI: Observability, Log Inspection & Controls

1. **CI Overview & Filtering**:
   - Loaded `/ci` with cache fixture and multi-state run list (`success`, `running`, `failure`, `errored`, `cancelled`).
   - Inspected cache summary showing 42 entries, 512 MB, 128 hits, 16 misses.
2. **Job, Step & Output Expansion**:
   - Clicked run row `acme/widgets` (run `ci-run-1`).
   - Expanded job `unit-tests` and step `npm test`.
   - Clicked `Show output` and inspected preformatted log viewer containing execution output.
3. **Deep-Linking & Run Cancellation**:
   - Deep-linked directly to `/ci/ci-run-1` and `/ci/ci-run-2`.
   - On running run `ci-run-2`, clicked `Cancel run`.
   - Dismissed browser confirmation dialog: confirmed cancellation was aborted.
   - Accepted browser confirmation dialog: confirmed cancellation request dispatched and status transitioned to `cancelled`.
4. **Task-to-CI Navigation**:
   - Navigated to `/tasks/2` with associated CI metadata (`ci-run-task-2`).
   - Clicked `Show the run` link; verified direct navigation to the corresponding CI run view.

---

### 2.4 System: Logs, Top, Pipeline & Power Controls

1. **System Logs & Filtering**:
   - Loaded `/system`.
   - Inspected log viewer (`pre.logs-view`).
   - Filtered logs with search query `nonexistent-pattern-xyz`; verified "No matching log lines" indicator.
   - Tested refresh button.
2. **Top Processes & Auto-Refresh**:
   - Navigated to `Top` tab.
   - Verified process listing containing `systemd`, `grain-daemon`, and `node`.
   - Toggled auto-refresh checkbox and manually refreshed process list.
3. **Pipeline Pause/Resume Confirmation**:
   - Navigated to `Pipeline` tab.
   - Clicked `Pause pipeline`; dismissed dialog (no pause issued); accepted dialog (paused successfully).
   - Clicked `Resume pipeline`; dismissed dialog (no resume issued); accepted dialog (resumed successfully).
4. **Power Controls (Danger Zone)**:
   - Navigated to `Power` tab.
   - **Restart grain**: Confirmed modal dialog; verified synthetic success without restarting the daemon.
   - **Reboot host**: Confirmed modal dialog; verified handling of connection reset without host reboot.
   - **Shut down host**: Confirmed modal dialog; verified display of error notification on simulated 500 failure without host shutdown.

---

### 2.5 Metrics & Responsive Shell

1. **Metrics Dashboard**:
   - Opened `/metrics` overlay from navigation rail.
   - Inspected Throughput, Capacity, Latency, and Backlog panels.
   - Simulated network failure on `/api/metrics*`; verified error banner rendering.
2. **Responsive Tablet Shell (820x1180)**:
   - Configured viewport to 820x1180 with standing `Host mode` banner enabled.
   - Verified sidebar navigation items (`Settings`, `System`, `Metrics`) remain fully visible, clickable, and contained within the viewport height.

---

## 3. Intercepted Routes & Boundaries

| Endpoint | Method | Simulated Behavior & Purpose |
| :--- | :--- | :--- |
| `GET /api/config` | `GET` | Injected power action enablement flags (`restartEnabled`, `rebootEnabled`, `shutdownEnabled`) and `hostSandboxes: true`. |
| `GET /api/models/gemini` | `GET` | Simulated unavailable upstream discovery catalog (`{ available: false, models: [], efforts: [] }`) to verify fallback inputs. |
| `GET /api/secrets` | `GET` | Returned secret store status without exposing real credentials (`{ enabled: true, secrets: [] }`). |
| `PUT /api/secrets/**` | `PUT` | Rejected fake credentials with `400 Bad Request` to verify client-side validation error handling. |
| `GET /api/upgrade` | `GET` | Returned upgrade daemon status and scheduled state. |
| `POST /api/upgrade` | `POST` | Simulated upgrade scheduling response (`202 Accepted`). |
| `DELETE /api/upgrade/scheduled` | `DELETE` | Simulated upgrade schedule cancellation (`200 OK`). |
| `GET /api/sandboxes` | `GET` | Returned rich host pressure, disk, pool, and 24-worker fixture; also simulated HTTP 500 and disabled states. |
| `GET /api/ci/cache` | `GET` | Returned CI cache metrics fixture. |
| `GET /api/ci/runs*` | `GET` | Returned filterable CI runs list with various completion states. |
| `GET /api/ci/runs/:id` | `GET` | Returned detailed job, step, and log fixtures for run inspection and deep-linking. |
| `DELETE /api/ci/runs/:id` | `DELETE` | Intercepted cancellation requests after confirmation modal. |
| `GET /api/tasks/2` | `GET` | Injected synthetic CI run link metadata (`{ ci: { runId: "ci-run-task-2", conclusion: "failure" } }`). |
| `GET /api/host/top*` | `GET` | Returned host top process table fixture. |
| `POST /api/pipeline-pause` | `POST` | Intercepted pipeline pause requests after modal confirmation. |
| `DELETE /api/pipeline-pause` | `DELETE` | Intercepted pipeline resume requests after modal confirmation. |
| `POST /api/host/restart` | `POST` | Returned synthetic success (`{ ok: true }`) after modal confirmation without mutating host. |
| `POST /api/host/reboot` | `POST` | Aborted request with `connectionreset` after modal confirmation without mutating host. |
| `POST /api/host/shutdown` | `POST` | Returned HTTP 500 error after modal confirmation without mutating host. |
| `GET /api/metrics*` | `GET` | Injected synthetic HTTP 500 failure to verify error handling on metrics refresh. |

---

## 4. Test Suite Execution Results

1. **Automated E2E Operations Walkthrough**:
   - `ui/e2e/deployment_operations_walkthrough.spec.js`: **10/10 passed** (Chromium).
2. **Related E2E Suites**:
   - `ui/e2e/config.spec.js`, `ui/e2e/observability.spec.js`, `ui/e2e/upgrade.spec.js`, `ui/e2e/shell.spec.js`, `ui/e2e/system-overlay-tabs.spec.js`: **10/10 passed** (Chromium).
   - `ui/e2e/narrow.spec.js`, `ui/e2e/phone.spec.js`: **54/55 passed** across Chromium & WebKit (1 touch-CDP test deliberately skipped on WebKit).
3. **Frontend Vitest Unit Suite**:
   - `ui`: **151 test files passed**, **2,623 unit tests passed** (including `SettingsOverlay.test.jsx`, `CIPage.test.jsx`, `SandboxHealthPage.test.jsx`, `LogsPage.test.jsx`, `MetricsOverlay.test.jsx`, and `App.overlays.test.jsx`).
4. **Backend Go Package Suite**:
   - `pkg/ui`: `go test ./pkg/ui/...` **passed** (47.7s).
