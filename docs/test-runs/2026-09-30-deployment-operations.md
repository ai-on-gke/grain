# Deployment Settings and Operations Walkthrough

**Test Date:** 2026-09-30  
**Target Branch:** `grain/task-1228`  
**Execution Environment:** Debian GNU/Linux 12 (bookworm) amd64, Linux 6.1.0-53-amd64, Node.js v20.19.0, Go 1.26.6, Playwright 1.62.1 / Chromium & WebKit (headless)  
**Server Under Test:** Live `grain demo -observability` backend server over throwaway embedded SQLite store and compiled Vite frontend  
**Commit Tested:** `f6c8b54f1cdef62d70331053f301d55f4becc69f` (`Merge pull request #2355 from bwsalmon/grain/task-1227`)  
**Automated Spec:** `ui/e2e/deployment_operations_walkthrough.spec.js`  
**Related Specs:** `ui/e2e/config.spec.js`, `ui/e2e/observability.spec.js`, `ui/e2e/upgrade.spec.js`, `ui/e2e/shell.spec.js`, `ui/e2e/system-overlay-tabs.spec.js`, `ui/e2e/narrow.spec.js`, `ui/e2e/phone.spec.js`, `ui/e2e/ci_workflows.spec.js`, `ui/e2e/system_workflows.spec.js`  

---

## 1. Executive Summary

This walkthrough thoroughly exercised the Grain deployment configuration, observability, and operator-control user interfaces against a live `grain demo -observability` backend and compiled frontend using Playwright Chromium and WebKit browsers.

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
   - Set sandbox CPU allocation to `3`, memory to `4096 MB`, disk to `40 GB`, and added a demo setup bootstrap script.
   - Verified changes persisted across reloads and reflected proper resource commitments.
6. **Capabilities & Fake Secret Validation**:
   - Navigated to `Workflow & Tasks` under `Team Settings`.
   - Submitted a test dummy credential to `/api/secrets/github`.
   - Verified backend validation rejection (`400 Bad Request`) cleanly displayed the inline error message without saving credentials.
7. **State Repository Browser & Upgrade Planning**:
   - Navigated to `System settings -> Config repo`; verified commit history and file tree render.
   - Navigated to `System settings -> Upgrade`.
   - Configured target branch `grain/task-310-upgrade` and checked `Wait until this deployment is idle`.
   - Scheduled upgrade; verified UI rendered `.upgrade-scheduled` banner with busy run counter.
   - Cancelled scheduled upgrade; verified scheduled state cleanly cleared.

### 2.2 Sandboxes Observability

1. **Host Pressure and Pool Status**:
   - Loaded `/sandboxes` with rich mock dataset (16 CPUs, load 14.1/12.5/10.2, 27.8 GB RAM used, 980 MB swap, nested virt enabled, 500 GB disk with 10 GB floor).
   - Inspected pool counters (3 active, 2 idle, 1 warmup, max 8).
   - Verified 24-worker table rendering, status chips (`running`, `error` with crash reason `qemu crash`), and table vertical scrolling.
2. **Error & Disabled States**:
   - Injected simulated backend failure (`HTTP 500`); verified error banner rendered without unhandled JavaScript exceptions.
   - Injected `{ enabled: false }`; verified graceful "Sandboxes not available" empty state display.

### 2.3 CI Observability & Execution

1. **Run Filtering & Cache Statistics**:
   - Loaded `/ci`; verified cache statistics panel (42 entries, 512 MB, 128 hits, 16 misses).
   - Verified run list status badges: `success`, `running`, `failure`, `errored`, `cancelled`.
2. **Job and Step Log Inspection**:
   - Expanded `ci-run-1` (`acme/widgets` @ `main`); inspected `unit-tests` job and `npm test` step output.
   - Verified log viewer renders test summary (`2336 tests passed in 128 suites`).
3. **Deep Links and Cancellation Flow**:
   - Direct-linked to `/ci/ci-run-1` and verified direct hydration.
   - Direct-linked to active run `/ci/ci-run-2`; triggered "Cancel run".
   - Dismissed confirmation dialog (no cancel dispatched); re-triggered and accepted dialog, verifying `DELETE /api/ci/runs/ci-run-2` dispatch and UI transition to `cancelled`.
4. **Task-to-CI Link**:
   - Navigated to `/tasks/2` with associated CI failure; followed "Show the run" link directly to the failed CI job details.

### 2.4 System Controls, Logs, Process Monitor & Power Actions

1. **Logs Viewer & Search**:
   - Loaded `/admin/logs`; verified monospace log stream rendering.
   - Filtered logs with substring search (`daemon`); tested non-matching query (`nonexistent-pattern-xyz`) and verified "No matching log lines" empty state.
   - Triggered manual log stream refresh.
2. **Process Monitor (Top)**:
   - Switched to `Top` tab; verified process table parsing (`grain-daemon`, `node`, `systemd`).
   - Toggled auto-refresh checkbox off and performed manual refresh.
3. **Pipeline Pause / Resume Lifecycle**:
   - Switched to `Pipeline` tab.
   - Clicked "Pause pipeline"; verified browser confirmation dialog. Confirmed dialog and verified `POST /api/pipeline-pause` transition to paused state with maintenance message.
   - Clicked "Resume pipeline"; confirmed dialog and verified `DELETE /api/pipeline-pause` restoration.
4. **Dangerous Power Confirmations (Intercepted)**:
   - Navigated to `Power` tab under `Admin`.
   - Verified all dangerous action buttons require explicit confirmation dialog acceptance.
   - Intercepted `POST /api/host/restart`: verified dispatch count without mutating host.
   - Intercepted `POST /api/host/reboot` (simulated connection reset): verified graceful failure handling.
   - Intercepted `POST /api/host/shutdown` (simulated `HTTP 500` error): verified inline error presentation (`intercepted demo shutdown failure`).

### 2.5 Metrics Observability

1. **Section Coverage**:
   - Loaded `/metrics`; verified sections: Throughput, Capacity, Latency, and Backlog.
   - Verified time window selection (7d, 30d, 90d) and sparkline trend rendering.
2. **Failure Handling**:
   - Intercepted `/api/metrics` with `HTTP 500`; clicked Refresh and verified error banner display.

### 2.6 Responsive Tablet Shell

1. **Viewport Verification**:
   - Configured viewport to 820x1180 with `hostSandboxes: true` active.
   - Verified standing host banner renders cleanly above workspace.
   - Scrolled left navigation rail and verified settings button (`[data-testid="user-settings-button"]`) is fully reachable within viewport bounds without clipping.

---

## 3. Intercepted Routes & Boundaries

To ensure safe testing against a real browser and demo server without modifying the physical host or exposing production credentials, the following boundaries were intercepted:

| Intercepted Route | Method | Purpose / Behavior |
| :--- | :--- | :--- |
| `**/api/models/gemini` | `GET` | Simulates model catalog discovery failure to exercise manual model entry fallback. |
| `**/api/secrets/**` | `PUT`, `POST` | Rejects fake credential submissions with `400 Bad Request` to test inline form validation. |
| `**/api/upgrade` | `GET`, `POST` | Mocks image upgrade daemon and schedule state transitions. |
| `**/api/upgrade/scheduled` | `DELETE` | Mocks cancellation of pending deployment upgrades. |
| `**/api/sandboxes` | `GET` | Injects rich host metrics (16 CPUs, 24 workers) and simulates host error/disabled states. |
| `**/api/ci/cache` | `GET` | Supplies synthetic cache metrics (size, hit/miss counters). |
| `**/api/ci/runs*` | `GET` | Supplies multi-state CI run list (success, running, failed, errored, cancelled). |
| `**/api/ci/runs/:id` | `GET`, `DELETE` | Provides job step logs and simulates run cancellation. |
| `**/api/host/top*` | `GET` | Mocks process table output. |
| `**/api/pipeline-pause` | `GET`, `POST`, `DELETE` | Tracks pause/resume lifecycle without halting background task runner. |
| `**/api/host/restart` | `POST` | Intercepts daemon restart request. |
| `**/api/host/reboot` | `POST` | Aborts network request to simulate host reboot disconnection. |
| `**/api/host/shutdown` | `POST` | Returns `HTTP 500` to verify error presentation on host power failure. |
| `**/api/metrics*` | `GET` | Simulates metric endpoint failure during error state test. |

---

## 4. Test Run Results

- **Playwright E2E Suite (`ui/e2e/deployment_operations_walkthrough.spec.js`):** 10 / 10 passed (19.3s)
- **Related E2E Suites (`config`, `observability`, `upgrade`, `shell`, `system-overlay-tabs`, `narrow`, `phone`, `boards`, `ci_workflows`, `system_workflows`):** 91 / 91 passed (1 skipped)
- **Frontend Unit Tests (Vitest):** 156 test files, 2,668 / 2,668 tests passed
- **Backend Test Suite (`go test ./...`):** All packages passed
- **Defects Found:** None. All UI surfaces, validation flows, error states, and confirmation barriers performed strictly within specification.
