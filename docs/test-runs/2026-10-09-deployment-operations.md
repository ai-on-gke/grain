# Deployment Settings and Operations Walkthrough (2026-10-09)

**Date:** 2026-10-09
**Branch:** `grain/task-2710` (incorporating `grain/task-2522`)
**Commit tested:** `5123d46d7705e7cfd61df454a4bf1d60df620640` and `b5c8f2526` (the tips of `candidate` during runs on 2026-10-09).
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with bundled headless Chromium (`chromium-1234`).
**Server under test:** `grain demo -observability` on dynamic port / 127.0.0.1:8420, built with `go build ./cmd/grain` after `npm ci && npm run build` into `pkg/ui/static`. Driven against the embedded SQLite store created under `TMPDIR`.
**Model and tooling:** This sandbox carried no `/home/debian/.gemini-api-key`, so no direct Gemini API calls were made. Gemini model selection and reasoning effort settings were exercised through the UI controls in Team Settings.

**Setup note:** `npx playwright install chromium` requires `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` due to the local package-cache proxy certificate.

## How it was driven

The walkthrough was executed via end-to-end automation scripts using Playwright Chromium in headless mode against the real running `grain demo -observability` server. Automation scripts were placed in `ui/.walk/` (ignored via `.git/info/exclude`).

Every operational pane was thoroughly exercised through visible controls:
- Tab navigation by mouse click and keyboard traversal across narrow and wide viewport widths.
- MUI and native select elements, blur-to-save text and number inputs.
- Confirmation dialogs for destructive or mutating operations (`confirm()`, `prompt()`), verifying both dismissal (cancelling) and acceptance.
- Local storage persistence across page reloads.
- Destructive and host-altering POST boundaries were intercepted at the network layer (`page.route`) to verify client handling, error reporting, and confirmation flows without modifying the underlying VM.

Every mutation was verified by reading the corresponding backend endpoint, confirming persistence across full page reloads, and testing edge cases (invalid inputs, unrunnable states, partial service failures).

## Findings and Fixes

- **Existing Suites & Number Box Verification:**
  - Verified `ui/e2e/deployment_operations_walkthrough.spec.js` (10 passed) and `ui/e2e/deployment-operations*.spec.js` (24 passed).
  - Number box input handling across integer, fractional, exponent, and invalid states remained rock-solid without regressions.
- **Observability Data Robustness & Sparklines:**
  - Evaluated `SandboxHealthPage.jsx` and `Sparkline.jsx` behavior under empty, sparse, and heavily loaded synthetic sandbox collections (60 concurrent rows).
  - Confirmed all metrics and trend charts tolerate undefined/null metrics without uncaught client-side render exceptions.
  - No new application code bugs were encountered; no code changes or follow-up tasks were required.

## Coverage and Outcomes

### Settings

- **Tab Traversal & Horizontal Scrolling:** All 16 Admin tabs were visited in sequence (`General & Deployment`, `Access & Accounts`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Executors`, `Integrations & Auth`, `All Teams`, `All Users`, `Config repo`, `Logs`, `Sandboxes`, `Caches`, `Top`, `Pipeline`, `Power`, `Upgrade`). Each route mapped cleanly to its respective `/admin/<tab>` path without console errors. At 700 px and 1400 px, the tabs container measured scroll width exceeding client width (e.g. 1831 px vs 355 px), with smooth keyboard focus traversal across narrow viewports down to 375 px. No deprecated "Root shell" tab exists.
- **Browser-Local Preferences (`/settings`):** Dark mode and Oldest-first timeline ordering persisted cleanly to `localStorage` (`grain.themeMode=dark`, `grain.timelineOrder=oldest`) and survived reloads. Preferences were reset cleanly to Auto and Newest-first.
- **General & Deployment:** Environment name updates (e.g. `walk-staging-2522`, `staging-walkthrough-2710`) persisted on blur and survived page reloads. Admin account management verified: duplicate additions were rejected, and administrator removal triggered a confirmation dialog before mutating the system administrator list.
- **Engine & Concurrency:** Poll interval and merge concurrency limits persisted on blur. Invalid duration inputs (e.g. `invalid-time`, `0s`) were rejected by the backend with clear 400 responses, rolling back inputs safely.
- **Sandbox & Infrastructure:** Configured sandbox vCPUs (2/4), memory (4096 MiB), disk size (40 GiB), and bootstrap setup scripts (`#!/bin/sh\necho ops-walkthrough-2710`). All values persisted on blur and remained identical after full reload.
- **Integrations, Secrets & Auth:** Tested secret entry with fake keys and tokens (`walkthrough-secret` / `token`, `demo-fake-secret` / `auth-token`). Values were masked as write-only, displayed with key tags only, and never leaked in DOM text or attributes. Secret deletion required explicit confirmation dialog acceptance.
- **Restart-Required & Unrunnable Notices:** Stubbed `pendingRestart` (`githubHost`, `sandboxBackend`) and `unrunnable` configuration states. Verified clear visual alert banners ("Stored, but not runnable" and "Saved, but not applied yet") indicating operational action is needed. Diagnostic tabs cleanly omitted deployment-wide setting warnings.
- **Team Settings (Models & Reasoning Effort):** Traversed `/teams/default/settings/models`. Verified agent model selector, tested dropdown options and Gemini reasoning effort controls, and confirmed model selections persisted via `PUT /api/teams/default`. Connection editor dirty state verified: navigating away or pressing Escape on unsaved connection drafts prompted for confirmation.
- **Upgrade Planning:** Tested `/admin/upgrade` lifecycle transitions via stubbed API endpoints. Verified idle state, in-flight image pull/schema migration progression, failure state handling ("manifest unknown"), and scheduled upgrade cancellation (`DELETE /api/upgrade/scheduled`).

### Sandboxes

- **Host Pressure & Empty States:** Empty sandbox pool displayed informative notice ("Nothing in flight, so no sandboxes exist.") along with host load averages and core counts (load average 1/5/15m, utilization, memory and swap).
- **Live CI Sandboxes:** Initiated CI run against `acme/widgets`. Verified immediate appearance in the sandboxes table as a live `ci` backend sandbox (with storage overlay figures, e.g. 213.0 MiB written). Completed run via `POST /api/e2e/ci/finish` and verified graceful teardown and release back to empty state.
- **Host Commitment & High-Load Scrolling:** Synthetic load test of 60 active sandboxes with elevated host load averages (15.20 / 12.10 / 9.00 on 4 vCPUs) and high memory/swap commitment. Verified table scrolling performance, sparkline trend rendering, and load level threshold indicators.
- **Error & Disabled States:** Injected 500 server error ("sandbox host unreachable") and disabled flag (`enabled: false`). Verified descriptive alert banners without UI crashing.

### CI Observability

- **Run Initiation & Log Streaming:** Dispatched run on `acme/widgets` (`ops-walkthrough/run-stream`). Redirected seamlessly to `/ci/<run-id>`, streaming live execution steps and stdout/stderr output (`observed through the sandbox`).
- **Run Cancellation:** Verified "Cancel run" button with confirmation dialog. Cancelling with reason persisted `cancelled` status and destroyed the sandbox.
- **Deep-Links, Reload & Bogus Runs:** Direct navigation and reloads on running, completed, and cancelled runs displayed proper badges and status. Direct navigation to non-existent run (`/ci/bogusrun00000000`) displayed clean 404 alert ("no CI run ...").
- **Run Filtering & Conclusions:** Tested runs showcasing full suite of conclusions (failure, errored, skipped, success, cache hit, running, pending). Verified composite job summaries ("3 jobs: 1 failure, 1 skipped, 1 success · 1 cache hit") and narrowing filters.

### System & Power Controls

- **System Logs:** Tail view opened scrolled to newest entries. Verified log filtering and text search ("daemon" vs non-matching query showing "no matching log lines"). Polling and manual refresh functioned smoothly without scroll jumps.
- **Top & Auto-refresh:** Inspected top CPU and memory consumers (`grain-daemon`). Toggled auto-refresh checkbox, verifying polling suspension when unchecked.
- **Pipeline Controls:** Verified pause/resume pipeline functionality with reason strings. Both operations required dialog confirmation and updated system state and global banners across the application.
- **Power Operations (Intercepted Boundary):** All power actions (Restart grain, Reboot host, Shut down host) were strictly intercepted at `POST /api/host/{action}`. Confirmed:
  - Dismissing confirmation modal aborted requests without hitting network.
  - Accepting Restart grain triggered 200 acknowledge and UI notification.
  - Accepting Reboot host simulated connection reset or 500 refusal ("reboot refused (stub)") and surfaced clean notification.
  - Accepting Shutdown host tested 500 error boundary handling.
  - Host remained intact and unmutated throughout.

### Metrics

- **Sections & Navigation:** Rendered Throughput, Latency, Capacity, and Backlog sections with standard semantic level 3 headings.
- **Time Windows:** Switched across 24h, 7d, 30d, and 90d windows. Verified URL synchronization (`/metrics?window=...`) and dataset updates.
- **Partial Failure & Degraded Modes:** Injected `failed` section array (e.g. database table lock on throughput). Verified per-section warning alerts ("Not available in this report") without affecting sibling sections.
- **Error Handling & Large Values:** Simulated 500 error ("metrics database unavailable") and large numeric counters (billions of tasks filed). Verified tabular number formatting and confirmed absence of horizontal layout overflow.

## Browser Route Stubs Table

| Route | Method | Why Intercepted | Response / Behavior |
|-------|--------|-----------------|---------------------|
| `POST /api/host/{restart,reboot,shutdown}` | POST | Guard VM host from actual restart, reboot, or shutdown | Intercepted in browser. Restart: 200 `{ok:true}`. Reboot: 500 / reset. Shutdown: 500 `{error:"intercepted demo shutdown failure"}`. |
| `GET /api/settings` | GET | Exercise pending restart and unrunnable alerts | Augmented demo response with `pendingRestart` and `unrunnable` configuration keys. |
| `POST /api/upgrade`, `GET /api/upgrade`, `DELETE /api/upgrade/scheduled` | GET/POST/DELETE | Exercise upgrade wizard stages without actual daemon upgrade | Simulated idle, scheduled, running (pull image -> migrate -> restart), cancellation, and failure states. |
| `GET /api/sandboxes` | GET | Exercise high host load and 60-row sandbox table scrolling | Injected 60 synthetic sandboxes with high host load averages (15.20 / 4 vCPUs) and high memory usage; 500 error state; disabled state. |
| `GET /api/ci/runs` | GET | Exercise diverse CI conclusion states and job summary formatting | Augmented runs list with complex failure, YAML error, cache hit, and pending/running states. |
| `GET /api/metrics` | GET | Exercise partial failure alerts, full 500 error state, and large numbers | Augmented response with `failed` array; 500 error response; and very large numeric counters. |
