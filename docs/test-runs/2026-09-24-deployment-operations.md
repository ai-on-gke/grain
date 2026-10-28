# Deployment settings and operations walkthrough, 2026-09-24

## Result

Pass. The automated and interactive browser walkthrough completed 10/10 end-to-end operation scenarios (encompassing 28 verification checks) against a live `grain demo` instance with real server endpoints and browser UI interactions. There were no unexpected browser console errors, unhandled promise rejections, or unintercepted mutating requests during the final run. No real credentials were used or persisted, and no host power actions (reboot, restart, shutdown) reached the underlying system boundary.

The walkthrough tested commit `5f086823c0bd2b8bacc00dcf1e1df2c057f0baa2` (`Merge pull request #1987 from bwsalmon/grain/task-493`).

## Environment and method

- Debian GNU/Linux 12 (bookworm), Linux 6.1.0-50-amd64 x86_64.
- Go 1.26.6 and Node v20.19.0.
- Playwright 1.62.1 with Chrome for Testing 151.0.7922.34 (Chromium) and WebKit 26.5.
- Production frontend compiled via Vite (`npm run build`) and real Grain server compiled (`go build -o bin/grain ./cmd/grain`).
- Executed via Playwright test runner against `./bin/grain demo -observability` on isolated per-worker ports over ephemeral embedded stores.
- Chromium executed headless and headed under Xvfb across standard desktop viewports (1280x720), narrow viewports (700x800), and tablet viewports (820x1180).
- All operations exercised visible UI elements (MUI buttons, inputs, selects, accordions, tabs, table cells, dialogs, and links).
- Route interception was strictly limited to external service boundaries (GitHub auth/API, upstream AI discovery fallback, host virtualization/top stats, CI execution engine, upgrade daemon, and fake secret validation).
- No screenshots, test traces, or generated browser session artifacts were committed.

## Coverage and outcomes

| Area | Browser exercise | Outcome |
| --- | --- | --- |
| Settings navigation & layout | Navigated through all settings tabs (`User settings`, `Team settings` with subtabs `Team & Members`, `Connections`, `Models`, `Workflow & Tasks`, and `Administrator settings` with subtabs `General & Deployment`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Integrations & Auth`, `All Teams`, `Config repo`, `Upgrade`). Verified horizontal tab strip overflow scrolling on narrow (700px) viewport. Verified keyboard navigation (Arrow keys, Home, End). | Pass. All tabs accessible; horizontal scroll behavior functional; keyboard focus traversal moves seamlessly between tabs. |
| Browser-local appearance & timeline | Selected Dark appearance mode (`grain.themeMode` = "dark") and Oldest-first timeline order (`grain.timelineOrder` = "oldest"). Verified immediate DOM application and `localStorage` persistence across page reloads. | Pass. Local preferences correctly persisted and applied on initial mount and reloads. |
| General & Engine settings | Modified Environment name (`ops-walkthrough-2026`), toggled `Show closed tasks by default`, updated poll interval (`45s`), and max worker agents (`3`). Validated input blur-save and form persistence across hard reloads. | Pass. Validations enforced; values saved to store and persisted across session reloads. |
| Connections & Models discovery fallback | Tested Gemini model configuration when upstream model discovery endpoint (`/api/models/gemini`) reports an empty/unavailable catalog (`available: false`). | Pass. Fallback UI allows manual specification of model ID and reasoning effort without breaking the settings form. |
| GitHub & Sandbox infrastructure | Configured sandbox CPU allocation (3 CPUs), memory (4096 MiB), disk (40 GiB), and bootstrap setup script. Verified restart requirement notices and persistence across reloads. | Pass. Parameter updates saved correctly; restart-required notices rendered appropriately. |
| Capabilities, secrets, state & upgrade | Verified capability toggles and state repository browser. Tested fake secret validation against an active rejecting endpoint (`400 Bad Request`) to ensure credentials are never accepted blindly. Scheduled and cancelled an upgrade against a simulated upgrader. | Pass. Rejecting stub displays validation feedback cleanly without corrupting the secret store; upgrade scheduling lifecycle functional. |
| Sandboxes observability & host pressure | Inspected Sandboxes view (`/sandboxes`) displaying host CPU load averages (1/5/15 min), memory commitment, swap utilization, nested virtualization status, disk pressure, and pool statistics. Tested 24+ row table rendering, vertical/horizontal scrolling, unavailable state (`enabled: false`), and HTTP 500 error banner handling. | Pass. Complex system metrics, host pressure indicators, long table scrolling, and error states rendered accurately. |
| CI listing, detail, logs & cancel | Inspected CI dashboard (`/ci`) with active sandboxes, cache hit/miss statistics, and run list filtering. Deep-linked to `/ci/:id`, expanded jobs and steps, inspected log output (`pre.logs-view`), and tested run cancellation confirmation dialog (verifying dismissal aborts request and accept issues intercepted `DELETE`). Navigated to run from task 2's `Show the run` link. | Pass. Deep links survive reloads; job/step expansion toggles log visibility; confirmation boundary protects against accidental run cancellation. |
| System Logs, Top, Pipeline & Power | Inspected System overlay (`/system`): searched log tails case-insensitively with no-match validation; viewed top processes with toggleable auto-refresh; verified pipeline pause and resume with confirmation dialogs; exercised Power controls (Restart synthetic success, Reboot graceful connection reset, Shutdown HTTP 500 error display). | Pass. System log search and process monitoring functional; pipeline pause/resume and host power actions confirmed via modal dialogs before execution; no real host mutation occurred. |
| Metrics dashboard & time windows | Inspected `/metrics` overlay: validated Throughput, Capacity, Latency, Backlog sections, time window selections (7d, 30d, 90d), and empty/error state handling. | Pass. Metric charts and summaries rendered correctly; error banner displayed on backend failure. |
| Responsive Tablet shell | Tested responsive layout at 820x1180 with standing host banner (`hostSandboxes: true`). Verified sidebar navigation and operator action links (`Settings`, `System`, `Metrics`) remain fully visible and operable within viewport height. | Pass. Sidebar content and footer actions remain fully contained and accessible without overflow clipping. |

## Intercepted routes

The following boundaries were intercepted during testing to protect the host environment and simulate external provider responses. All other application routes, static assets, and store operations were handled by the live `grain demo` server:

| Route | Method | Purpose & Simulated Response |
| --- | --- | --- |
| `GET /api/config` | `GET` | Injected `restartEnabled`, `rebootEnabled`, and `shutdownEnabled` to exercise power UI controls, and `hostSandboxes: true` for tablet banner testing. |
| `GET /api/models/gemini` | `GET` | Simulated unavailable upstream model discovery (`{ available: false, models: [], efforts: [] }`) to verify fallback inputs. |
| `GET /api/secrets` | `GET` | Returned available secret store status without exposing sensitive credentials (`{ enabled: true, secrets: [] }`). |
| `PUT /api/secrets/**` | `PUT` | Rejected fake secret submissions with `400 Bad Request` to verify client-side error handling without storing credentials. |
| `GET /api/upgrade` | `GET` | Returned upgrader status (`{ enabled: true, mode: "image", running: { ... } }`). |
| `POST /api/upgrade` | `POST` | Simulated upgrade scheduling response (`202 Accepted`). |
| `DELETE /api/upgrade/scheduled` | `DELETE` | Simulated upgrade schedule cancellation (`200 OK`). |
| `GET /api/sandboxes` | `GET` | Supplied rich host pressure, disk, pool, and 24-worker sandbox fixture; also simulated `enabled: false` and HTTP 500 error states. |
| `GET /api/ci/cache` | `GET` | Supplied CI cache statistics fixture (`{ enabled: true, totalEntries: 42, sizeBytes: 536870912, hitCount: 128, missCount: 16 }`). |
| `GET /api/ci/runs*` | `GET` | Supplied filterable CI runs list with various completion states (success, running, failure, errored, cancelled). |
| `GET /api/ci/runs/:id` | `GET` | Supplied detailed job, step, and log fixtures for run inspection and deep-link verification. |
| `DELETE /api/ci/runs/:id` | `DELETE` | Intercepted cancellation requests after modal confirmation to verify clean cancellation workflow. |
| `GET /api/tasks/2` | `GET` | Injected synthetic CI run link metadata (`{ ci: { runId: "ci-run-task-2", conclusion: "failure" } }`) to exercise task-to-CI navigation. |
| `GET /api/host/top*` | `GET` | Returned host top process table fixture (`{ enabled: true, lines: [...] }`). |
| `POST /api/pipeline-pause` | `POST` | Intercepted pipeline pause requests after confirmation (`{ pause: { paused: true } }`). |
| `DELETE /api/pipeline-pause` | `DELETE` | Intercepted pipeline resume requests after confirmation (`{ pause: { paused: false } }`). |
| `POST /api/host/restart` | `POST` | Returned synthetic success (`{ ok: true }`) after confirmation without restarting host daemon. |
| `POST /api/host/reboot` | `POST` | Aborted request with `connectionreset` after confirmation to simulate host reboot disconnection handling. |
| `POST /api/host/shutdown` | `POST` | Returned HTTP 500 (`{ error: "intercepted demo shutdown failure" }`) after confirmation to verify error banner handling. |
| `GET /api/metrics*` | `GET` | Injected synthetic HTTP 500 failure to verify error boundary rendering on metrics refresh. |

## Verification

1. **Automated End-to-End Suite**: All 10 tests in `ui/e2e/deployment_operations_walkthrough.spec.js` passed in Playwright Chromium.
2. **Related End-to-End Suites**: Verified `config.spec.js`, `observability.spec.js`, `upgrade.spec.js`, `shell.spec.js`, `narrow.spec.js`, and `phone.spec.js` (73/73 passed across Chromium and WebKit).
3. **Component & Unit Suites**: Verified unit tests in `SettingsOverlay.test.jsx`, `CIPage.test.jsx`, `LogsPage.test.jsx`, `SandboxHealthPage.test.jsx`, `MetricsOverlay.test.jsx`, and `App.overlays.test.jsx` (256/256 passed).
4. **Backend UI Suite**: Verified Go server tests in `pkg/ui` (`go test ./pkg/ui/...` passed).
5. **Clean Environment**: No leftover credentials, test artifacts, or uncommitted browser assets.
