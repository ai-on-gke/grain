# Deployment Settings and Operations Walkthrough (2026-10-10)

**Date:** 2026-10-10
**Branch:** `grain/task-2830`
**Commit tested:** `003d11565` (`003d11565 Merge pull request #3345 from bwsalmon/grain/task-2826`).
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with bundled headless Chromium (`chromium-1234`).
**Server under test:** `grain demo -observability` on dynamic port / 127.0.0.1:0, built with `go build -tags grain_e2e ./cmd/grain` after `make frontend` into `pkg/ui/static`. Driven against the embedded SQLite store created under `TMPDIR`.
**Model and tooling:** This sandbox carried no `/home/debian/.gemini-api-key`, so no direct Gemini API calls were made. Gemini model selection and reasoning effort settings (`low`, `medium`, `high`) were exercised through the UI controls in Team Settings.

**Setup note:** `npx playwright install chromium` requires `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` due to the local package-cache proxy certificate.

## How it was driven

The walkthrough was executed via real browser automation in headless Chromium against a live, running `grain demo -observability` server. Automation scripts were placed in `ui/.walk/` (ignored via `.git/info/exclude`).

Every operational pane was thoroughly exercised through visible controls:
- Tab navigation by mouse click and keyboard traversal across narrow (375 px, 600 px), tablet (820 px), and wide (1280 px, 1400 px) viewport widths.
- MUI and native select elements, blur-to-save text and number inputs.
- Confirmation dialogs for destructive or mutating operations (`confirm()`, `prompt()`), verifying both dismissal (cancelling) and acceptance.
- Local storage persistence across full browser reloads.
- Destructive and host-altering POST boundaries were intercepted at the browser network layer (`page.route`) to verify client handling, error reporting, and confirmation flows without modifying the underlying VM.

Every mutation was verified by reading the corresponding backend endpoint, confirming persistence across full page reloads, and testing edge cases (invalid inputs, unrunnable states, partial service failures).

## Findings and Fixes

- **Existing Suites & Number Box Verification:**
  - Ran `ui/e2e/deployment_operations_walkthrough.spec.js` (10 tests passed in 31.6s).
  - Ran `ui/e2e/observability.spec.js` (passed in 17.5s).
  - Ran `ui/e2e/deployment-operations-*.spec.js` (all 25 unit/navigation specs passed).
  - Number box input handling across integer, fractional, exponent, and invalid states remained rock-solid without regressions.
- **Observability Data Robustness & Sparklines:**
  - Evaluated `SandboxHealthPage.jsx` and `Sparkline.jsx` behavior under empty, sparse, and heavily loaded synthetic sandbox collections (60 concurrent rows).
  - Confirmed all metrics and trend charts tolerate undefined/null metrics without uncaught client-side render exceptions.
  - No application code regressions were encountered; no code modifications or follow-up tasks were required.

## Coverage and Outcomes

### Settings

- **Tab Traversal & Horizontal Scrolling:** All 16 Admin tabs were visited in sequence (`General & Deployment`, `Access & Accounts`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Executors`, `Integrations & Auth`, `All Teams`, `All Users`, `Config repo`, `Logs`, `Sandboxes`, `Caches`, `Top`, `Pipeline`, `Power`, `Upgrade`). Each route mapped cleanly to its respective `/admin/<tab>` path without console errors. At 600 px and 700 px, the tabs container measured scroll width exceeding client width (e.g. 1831 px vs 355 px), with smooth keyboard focus traversal across narrow viewports down to 375 px. Confirmed complete absence of the deprecated "Root shell" tab.
- **Browser-Local Preferences (`/settings`):** Dark mode and Oldest-first timeline ordering persisted cleanly to `localStorage` (`grain.themeMode=dark`, `grain.timelineOrder=oldest`) and survived reloads. Preferences were toggled back to Light/Auto and Newest-first.
- **General & Deployment:** Environment name updates (`ops-validation-2026`) persisted on blur and survived page reloads. Checked "Show closed tasks by default" preference. Admin account management verified: duplicate additions were rejected, and administrator removal triggered a confirmation dialog before mutating the system administrator list.
- **Engine & Concurrency:** Poll interval (`30s`) and merge concurrency limits (`4`) persisted on blur and across full reload. Invalid duration inputs (e.g. `invalid-time`, `0s`) were rejected by the backend with clear 400 responses, rolling back inputs safely.
- **Sandbox & Infrastructure:** Configured sandbox vCPUs (`4`), memory (`8192 MiB`), and bootstrap setup script (`#!/bin/bash\necho 'running bootstrap ops'`). All values persisted on blur and remained identical after full reload.
- **Integrations, Secrets & Auth:** Tested secret entry with fake keys and tokens (`test-ops-secret` / `api-token` / `super-sensitive-token-9999`). Values were masked as write-only, displayed with key tags only, and never leaked in DOM text or attributes. Secret deletion required explicit confirmation dialog acceptance (dismissing preserved the secret; accepting deleted it cleanly).
- **Restart-Required & Unrunnable Notices:** Stubbed `pendingRestart` (`githubHost`, `sandboxBackend`) and `unrunnable` (`pollInterval: 0s` vs `30s`) configuration states. Verified clear visual alert banners ("Stored, but not runnable" and "Saved, but not applied yet") indicating operational action is needed. Diagnostic tabs cleanly omitted deployment-wide setting warnings.
- **Team Settings (Models & Reasoning Effort):** Traversed `/teams/default/settings/models`. Verified agent model selector, tested dropdown options and Gemini reasoning effort controls (`low`, `medium`, `high`), verified Claude models display "Claude takes no reasoning effort.", and confirmed model selections persisted via `PUT /api/teams/default`. Connection editor dirty state verified: navigating away or pressing Escape on unsaved connection drafts prompted for confirmation.
- **Upgrade Planning:** Tested `/admin/upgrade` lifecycle transitions via stubbed API endpoints. Verified idle state, in-flight image pull/schema migration progression, failure state handling, and scheduled upgrade cancellation (`DELETE /api/upgrade/scheduled`).

### Sandboxes

- **Host Pressure & Empty States:** Empty sandbox pool displayed informative notice ("Nothing in flight, so no sandboxes exist.") along with host load averages and core counts (load average 1/5/15m, utilization, memory and swap).
- **Live CI Sandboxes:** Initiated CI run against `acme/widgets` (`ops-walkthrough/sandboxes`). Verified immediate appearance in the sandboxes table as a live `ci` backend sandbox (with host statistics). Completed run via `POST /api/e2e/ci/finish` and verified graceful teardown and release back to empty state.
- **Host Commitment & High-Load Scrolling:** Synthetic load test of 60 active sandboxes with elevated host load averages (15.20 / 12.10 / 9.00 on 4 vCPUs) and high memory/swap commitment (15800 MiB / 16384 MiB RAM, 3900 MiB / 4096 MiB swap). Verified table scrolling performance, sparkline trend rendering, and load level threshold indicators.
- **Error & Disabled States:** Injected 500 server error ("failed to sample sandboxes") and disabled flag (`enabled: false`). Verified descriptive alert banners without UI crashing.

### CI Observability

- **Run Initiation & Log Streaming:** Dispatched run on `acme/widgets` (`ops-walkthrough/log-inspect`). Live execution steps streamed stdout/stderr output down to individual step output (`observed through the sandbox`).
- **Run Cancellation:** Verified "Cancel run" button with confirmation dialog. Dismissing dialog left the run running; accepting cancelled the run with `cancelled` status and destroyed the sandbox.
- **Deep-Links, Reload & Bogus Runs:** Direct navigation and reloads on running, completed, and cancelled runs displayed proper badges and status. Direct navigation to non-existent run (`/ci/nonexistent-run-id-9999`) displayed clean 404 alert ("no CI run ...").
- **Task Link & Errored Push Run:** Dispatched push run for task 2 (`POST /api/ci/runs` with `taskId: "2"`), verifying navigation from task view ("Show the run") to the errored run (`no workflow of acme/widgets answers push`).

### System & Power Controls

- **System Logs:** Tail view opened scrolled to newest entries. Verified log filtering and text search ("daemon" vs non-matching query showing "no matching log lines"). Polling and manual refresh functioned smoothly without scroll jumps.
- **Top & Auto-refresh:** Inspected top CPU and memory consumers (`grain-daemon`). Toggled auto-refresh checkbox, verifying polling suspension when unchecked.
- **Pipeline Controls:** Verified pause/resume pipeline functionality. Both operations required dialog confirmation and updated system state and global banners across the application.
- **Power Operations (Intercepted Boundary):** All power actions (Restart grain, Reboot host, Shut down host) were strictly intercepted at `POST /api/host/{action}`. Confirmed:
  - Dismissing confirmation modal aborted requests without hitting network.
  - Accepting Restart grain triggered 200 acknowledge and UI notification.
  - Accepting Reboot host simulated connection reset and surfaced clean error notification.
  - Accepting Shutdown host tested 500 error boundary handling.
  - Host remained intact and unmutated throughout.

### Metrics

- **Sections & Navigation:** Rendered Throughput, Latency, Capacity, and Backlog sections with standard semantic level 3 headings.
- **Time Windows:** Switched across 24h, 7d, 30d, and 90d windows. Verified URL synchronization (`/metrics?window=...`) and dataset updates.
- **Partial Failure & Degraded Modes:** Injected `failed` section array (e.g. `Throughput (database timeout)`). Verified per-section warning alerts ("Not available in this report: Throughput (database timeout)") without affecting sibling sections.
- **Error Handling & Large Values:** Simulated 500 error ("metrics database unavailable") and large numeric counters. Verified tabular number formatting and confirmed absence of horizontal layout overflow.

### Responsive Tablet Shell

- Tested 820x1180 viewport. Verified standing banner layout, sticky bottom controls, navigation, and modal containment without clipping or overflow.

## Browser Route Stubs Table

| Route | Method | Why Intercepted | Response / Behavior |
|-------|--------|-----------------|---------------------|
| `POST /api/host/{restart,reboot,shutdown}` | POST | Guard VM host from actual restart, reboot, or shutdown | Intercepted in browser. Restart: 200 `{ok:true}`. Reboot: aborted connection reset. Shutdown: 500 `{error:"intercepted host shutdown test failure"}`. |
| `GET /api/settings` | GET | Exercise pending restart and unrunnable alerts | Augmented demo response with `pendingRestart` and `unrunnable` configuration keys. |
| `POST /api/upgrade`, `GET /api/upgrade`, `DELETE /api/upgrade/scheduled` | GET/POST/DELETE | Exercise upgrade wizard stages without actual daemon upgrade | Simulated idle, scheduled, running, cancellation, and failure states. |
| `GET /api/sandboxes` | GET | Exercise high host load and 60-row sandbox table scrolling | Injected 60 synthetic sandboxes with high host load averages (15.20 / 4 vCPUs) and high memory usage; 500 error state; disabled state. |
| `GET /api/ci/runs` | GET | Exercise diverse CI conclusion states and job summary formatting | Augmented runs list with complex failure, YAML error, cache hit, and pending/running states. |
| `GET /api/metrics*` | GET | Exercise partial failure alerts, full 500 error state, and large numbers | Augmented response with `failed` array (`Throughput (database timeout)`); 500 error response; and very large numeric counters. |
