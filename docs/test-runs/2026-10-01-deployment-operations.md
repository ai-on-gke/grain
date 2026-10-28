# Deployment Settings and Operations Walkthrough

**Date:** 2026-10-01
**Branch:** `grain/task-1359`
**Commit tested:** `7ee1b2802` (`Merge pull request #2502 from bwsalmon/grain/task-1355`), then each fix on top of it, re-checked against a rebuilt server
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1, Chromium 151 (headless shell, `chromium-1234`)
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`, built with `go build ./cmd/grain` after `npm run build`. It ran over the throwaway embedded SQLite store the demo creates under `TMPDIR`, rebuilt and restarted after each fix.
**Model and tooling:** no `/home/debian/.gemini-api-key` was present in this sandbox, so nothing here called Gemini. The walkthrough uses no model.

## How it was driven

The browser walkthrough was a set of Node scripts using Playwright's `chromium` API (kept out of the tree). They drove the real frontend through visible controls: tabs, text fields with blur-to-save, native selects, MUI selects, radio groups, buttons, and native `confirm()` and `prompt()` dialogs, which were answered on purpose rather than left to Playwright's default.

Each script recorded console errors and warnings, page errors, failed requests, and every 4xx/5xx response. Each mutation was confirmed by reading `GET /api/settings` (or the endpoint concerned), then by reloading or reopening the screen.

The sign-in was a real one. The demo enables password auth, so the account `operator` (one of the demo's seeded system admins) was created through the "new user" tab of the sign-in dialog. The browser state from that sign-in was reused by later scripts.

Two notes on setup:

- **Playwright download:** `npx playwright install chromium` fails in this sandbox with "self-signed certificate in certificate chain" (the package-cache proxy). It succeeds with `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`.
- **Aborted task polls are expected:** each `/api/tasks` poll after the first shows up in Chromium as `requestfailed … net::ERR_ABORTED`. It is not a fault. The response is a deliberate `304 Not Modified` (the ETag path in `ui/src/api.js`), and Chromium reports the empty body as aborted.

## Bugs found and fixed

| # | Bug | Reproduction (before the fix) | Fix | Regression coverage |
|---|-----|------------------------------|-----|---------------------|
| 1 | Admin tabs were not in the URL. | Open `/admin/logs`: the Logs tab shows, but the address bar is immediately rewritten to `/admin`. Clicking Pipeline leaves it at `/admin`. Reload lands on General & Deployment, and Back leaves the pane. The same happens with `/system/<tab>`. | `buildPath` writes `/admin/<tab>` (General keeps `/admin`). `AdminOverlay` reports a tab the user picks through `onTabChange`, and the popstate handler restores the tab. Only explicit picks are reported. A first attempt that reported the overlay's own tab state made tab and URL oscillate, because before the config arrives nobody reads as an admin and the overlay falls back to Sandboxes. | `paths.test.js` (build/parse/round-trip, team-prefixed), `App.overlays.test.jsx` (deep link, click, Back), and e2e `deployment-operations-navigation.spec.js`. |
| 2 | A credential re-check could leave a stale "Credential works". | On Integrations & Auth, Test credential answers `ok`. Test again and get a 502: the error banner appears **beside** the earlier green "Credential works", which nothing has just confirmed. | `CredentialCheck` clears the verdict as each check starts. | `CapabilitiesPanel.test.jsx` and e2e `deployment-operations-navigation.spec.js`. |
| 3 | Back from a task opened in Metrics skipped Metrics. | `/metrics` → click "task 2" under Backlog → `/tasks/2`. `history.length` grows by 2 (`/`, then `/tasks/2`), so Back goes to `/` instead of `/metrics`. | `openTaskFromMetrics` fetches the task first, then closes the pane and opens the task in one update, so there is one history entry. A failed fetch leaves the report up beside the error. | `App.overlays.test.jsx` (asserts exactly one `pushState`, then Back) and e2e `deployment-operations-navigation.spec.js`. |

Each new unit test was run against the unfixed code and failed there:

- Admin tab: received `/admin`, expected `/admin/logs`.
- Metrics: received an extra `/` push.
- Credential check: "Credential works" was still present.

Two existing specs had relied on bug 1, each reloading on one Admin tab and then reading General's Environment name:

- `deployment_operations_walkthrough.spec.js` test 2 now reads Engine on the reloaded `/admin/engine` first.
- `cluster_setup_task_lifecycle_walkthrough.spec.js` test 1 now checks `/admin/sandboxes` and opens General by its tab. Grain CI's `ui e2e` caught this one.

## Coverage and outcomes

### Settings

- **Tab traversal:**
  - Admin has 16 tabs and every one rendered without console errors.
  - Team settings has 8 tabs: Team, Members, Capacity, Git identity, Connections, Models, Executors and Workflow & Tasks.
  - User settings has no tabs.
- **Horizontal scrolling and keyboard:** at 700 px the Admin tab strip scrolls (scrollWidth 1831 vs clientWidth 355). Clicking Upgrade scrolls it fully into view, and Home moves focus to the first tab.
- **Appearance and timeline order:** choosing Dark and Oldest first writes `grain.themeMode=dark` and `grain.timelineOrder=oldest` to localStorage, and the body background changes to `rgb(15,16,19)`. Both survive a hard reload. Arrow keys move between the radios. Both were reset to Auto and Newest first afterwards.
- **General settings:** Environment name `ops-walkthrough-2026-10-01` was saved on blur, persisted, and showed up in the document title (`ops-walkthrough-2026-10-01 — grain`) after a reload.
- **Engine and validation:**
  - Poll interval `not-a-duration` → `PUT` 400 `pollInterval: time: invalid duration`. The message shows in the error banner, the field reverts to the stored value, and the store is unchanged.
  - Poll interval `45s` → saved, and still there after a reload.
  - Merge batch size `-3` → 400, store unchanged.
- **Sandbox resources:**
  - vCPUs 3, memory 4096 MiB, disk 40 GiB and CPU overcommit 2.5 each saved and survived a reload.
  - Disk `-5` → 400 `sandboxDiskGb must be 0 (unset) or at least 1`, and the field reverted.
  - The browser refuses non-numeric input in the number fields.
  - Network `none` (a native select) saved, survived a reload, and was restored to `open`.
  - The setup script (multi-line) saved and survived a reload.
- **GitHub host:** saving `github.example.test` worked, and `restartRequired` lists `githubHost`. Clearing it returns 400 `githubHost cannot be empty`. That only matters in the demo, whose store starts with an empty host; a real daemon always seeds `github.com`.
- **Restart-required and unrunnable notices:** these were rendered from a stubbed settings response (see stubs below).
  - The banners "Stored, but not runnable: Poll interval (stored 0s, running 10s)…" and "Saved, but not applied yet: GitHub host…" appear.
  - The poll-interval field shows a "not runnable" chip with the stored/running explanation.
  - On Integrations, GitHub host shows a "restart to apply" chip with "Changed, but not applied…".
  - None of these banners appear on diagnostic tabs such as Logs.
- **Agent and model selection (Gemini):** Team settings → Connections → edit "Gemini 2.5 Pro" (Antigravity).
  - **Real path:** the demo cannot read agy's catalogue (`{"available":false}`), so the editor offers a free-text model field. `gemini-2.5-flash` with effort `high` autosaved through `PUT /api/settings` (`agentBackends`). Effort choices are model default, low, medium and high.
  - **Stubbed catalogue:** picking `gemini-3-pro` narrows effort to that family's own (model default, high, low).
  - **Persistence:** both values survived reopening and a hard reload. The editor autosaves each change, so "Save model" stays disabled and reads "No unsaved changes".
- **Capabilities and secrets:**
  - The Integrations sub-tabs read "22 of 22 configured".
  - With a secret row injected (see stubs), Set is disabled while the field is empty. Setting a dummy value reaches the real server, which answers 404 "secrets are not available: this UI has no local secrets directory". The error is shown and the typed value is kept for a retry. No credential was stored anywhere.
  - Test credential, stubbed: `ok` shows "Credential works" with "checked as … a point-in-time answer". Refused shows "Credential refused". A 502 goes to the banner, which is bug 2.
- **Memory and Config repo:**
  - The Memory capability sub-tab renders its description and readiness.
  - Config repo shows its own "not running inside a daemon that owns a config repository" notice, as the demo has none.
- **Upgrade planning:** stubbed (see below).
  - With 2 in-flight runs, Upgrade opens an in-app "confirm upgrade / … Upgrade anyway?" dialog. Cancel sends nothing and keeps the branch.
  - Confirming with an unknown branch shows the 400 message.
  - With "wait until this deployment is idle" ticked, the button becomes "schedule upgrade", and the scheduled view reads "Waiting to upgrade to grain/task-1359 … 2 runs and 1 CI run still to finish". Cancel clears it.
  - Without the stub, the real demo shows its own "Not available … neither -upgrade-image nor -upgrade-src-dir" notice. The after-upgrade check choices and the database-backend section render.
- **Settings preview:** there is no "Preview changes" control any more. Settings autosave since `77af21c94`, and `SettingsPlanPreview.jsx` is imported nowhere; the dirty and preview step is now the per-field save itself. This is noted, not changed: it is a cleanup question rather than a defect.

### Sandboxes (Admin → Sandboxes)

- **Real demo data:**
  - The host line, CPU and memory trends, and storage sections render.
  - "No sandbox has been built since…" shows when idle.
  - With a live CI run, the storage line reads "229.0 MiB written by 1 live sandbox".
- **Host pressure and the long table:** stubbed on top of the real response.
  - Load 31.5/24.2/18.9 of 16 vCPU, 97% utilisation, 31800/32768 MB memory, 3900/4096 MB swap, and "Nested virtualization: disabled (…)" all render.
  - A volume under its floor shows the "new CI jobs are held and agent dispatch deferred" alert.
  - 40 sandbox rows plus 2 table rows render. The last row scrolls into view, and the wide table scrolls inside its own `overflow-x: auto` container with no page-level horizontal overflow at 1024 px.
  - The failed row shows its error chip and "failed after 4.8s". Load and trend sparklines update across refreshes.
- **Failure and empty states:**
  - A 500 shows the error and keeps the last data visible.
  - `{enabled:false}` shows "Not available: this deployment has no sandbox pool or host stats configured."
- A first stub with `disks[].holds` as a string (the API's type is `[]string`) blanked the whole app with `TypeError: (a.holds || []).join is not a function`. That was a malformed stub, not a contract the server breaks, but it shows that one bad field unmounts the app because nothing catches a render error there.

### CI

These were real runs: the observability demo's `pkg/ci.Service`, on-disk job cache and host sandboxes.

- **Start a run:** Load workflows stays disabled while the branch is blank. Starting a run opens its overlay at `/ci/<id>`. The run appears in "active sandboxes (1)", the runs list (running, "1 jobs: 1 running"), and Admin → Sandboxes.
- **Finishing:** the demo's own `POST /api/e2e/ci/finish` releases the job, and the run turns to success, "1 jobs, all green". The job cache goes from 0 to 1 entry.
- **Cancel:** in the overlay, "cancel run" opens a native `prompt()`. Dismissing it leaves the run running; accepting it turns the run and job to cancelled.
- **Cache hit:** an identical second run of `main` completed in 0s as a "cache hit" ("1 jobs, all green · 1 cache hit").
- **Step output:** "show/hide output" shows "(waiting for output…)" while the step is held.
- **Direct link and reload:** `/ci/<id>` survives a reload. Escape closes the overlay back to `/ci`.
- **Controls:** no re-run or retry control is exposed. Cancel is the only run control.
- **Run filter:** the filter button opens the narrowing control.
- **Cache line:** the "Job cache" line refreshes on its own 30 s clock (`CACHE_REFRESH_MS`), so right after a cache hit it can still read "0% of 1 lookups" while the server says 1 of 3. That is by design.
- **Branch suggestions:** `GET /api/repos/acme/widgets/github-branches` returns 404 in the demo, which has no GitHub. It only feeds the branch suggestions and logs one console error.
- **Not covered:** the "from task links" path to a run was not exercised, because no demo task carries a CI run. Errored and skipped states were not reachable with the demo's single workflow.

### System

- **Logs:**
  - 300 lines load, pinned to the bottom.
  - Searching `MERGE-QUEUE` is case-insensitive (50 lines, all matching).
  - A search with no match shows "(no matching log lines)".
  - Refresh fetches once, and auto-refresh fetches every ~5 s.
  - Scrolling up unpins the view, which then stays put across refreshes.
  - A 500 (stub) shows "journal unavailable (stub)".
- **Top (stub):** process lines render. A 400-character command line scrolls inside its own `overflow-x: auto` box. Auto-refresh polls, and unticking it stops polling. Without the stub, the real notice reads "Not available: this deployment has no process snapshot configured."
- **Pipeline (real demo state, not the host):**
  - Pause with a reason opens a native `confirm()`. Dismissing it leaves the pipeline running.
  - Accepting it pauses with `byId: operator` and the reason. Both the global banner and the Pipeline tab say so, and both survive a reload.
  - Resume has its own confirmation, with the same dismiss and accept behaviour.
- **Power (stubbed POSTs):** Restart grain, Reboot host and Shut down host each open a native `confirm()` naming the consequence.
  - Dismiss sends nothing.
  - Accept sends exactly one POST each: restart → 200, reboot → connection reset, shutdown → 500.
  - The 500 shows its message in the banner. A connection reset is deliberately silent, since the machine is going down.
  - A successful restart shows no acknowledgement at all. This is a minor UX gap, noted rather than changed.
  - Without the stubbed config, the real demo shows "Not available: restarting grain, and rebooting or shutting down the host, are not enabled".
- **Root shell:** no Root shell entry exists in this build's Admin tabs or sidebar, so there was no availability message to check.

### Metrics

- **Sections:** Release notes (week 39), Throughput (with trend lines), Latency (stage table, with the n-too-small footnote), Capacity, and Backlog (oldest queued task link).
- **Windows:** last 24 h, 7, 30 and 90 days each re-query and re-span (1d 0h / 7d 0h / 30d 0h / 90d 0h; tasks filed 9/11/12/12).
- **Window persistence:** the window is not in the URL, so a reload returns to the 7-day default. Noted, not changed.
- **Loading and failure:** during a slow 503 (stub), Refresh is disabled. Afterwards the message shows and the previous report stays up.
- **Narrow layout:** at 390 px there is no element past the viewport and no document overflow.
- **Navigation:** the "task 2" link opens `/tasks/2`. Back returns to Metrics, which is bug 3.

### Console and network

Over the whole walkthrough, the only console errors were:

- the intended 4xx/5xx answers (validation 400s, the 404 from the secrets directory, and the stubbed 500/502/503 responses);
- the demo-only `github-branches` 404;
- the malformed-stub crash described under Sandboxes.

No other page errors were seen.

## Stubs (every intercepted route)

All stubs were Playwright `page.route` interceptions inside the browser. The server, frontend, store and gestures stayed real, and stubs that start with `route.fetch()` modify the real server response rather than replacing it.

| Route | Why | What it returned |
|---|---|---|
| `GET /api/config` | Power is not enabled in the demo. | Real response plus `restartEnabled`, `rebootEnabled` and `shutdownEnabled` set to true. |
| `POST /api/host/restart`, `/reboot`, `/shutdown` | These must never touch the host. | 200 `{ok:true}`; a connection reset; 500 `{error}`. |
| `GET /api/host/top` | The demo has no process snapshot. | `{enabled:true, lines:[…]}`, including a 400-character command line. |
| `GET/POST /api/upgrade`, `DELETE /api/upgrade/scheduled` | The demo has no upgrade daemon. | Image mode, 2 in-flight runs, a 400 for an unknown branch, and scheduling/cancellation state. |
| `GET /api/settings` | The demo has no running daemon and no secrets directory. | Real response plus `pendingRestart:["githubHost"]` and `unrunnable:[pollInterval 0s→10s]` for the notices. In a separate run, `checkable` and one secret row were added to `gemini-key`. |
| `POST /api/capabilities/*/check` | This calls the service that issued the credential. | ok, then refused, then 502. |
| `GET /api/agent-models` | agy's Gemini model catalogue is unavailable. | Four Gemini models across two families, with efforts low/medium/high. Only in the "stubbed catalogue" run; the fallback run used the real `{"available":false}`. |
| `GET /api/sandboxes` | The demo host cannot produce pressure or 40 sandboxes. | Real response with host pressure, two disks (one under its floor) and 40 cloned rows, one failed; also a 500, and `{enabled:false}`. |
| `GET /api/logs/*` | Failure state only. | 500. |
| `GET /api/metrics` | Loading and failure state only. | A 1.5 s delay, then 503. |

The CI execution boundary was not stubbed. The demo's own `/api/e2e/ci/finish` and `/api/e2e/sandboxes/other` controls only pace or add real work.

## Tests run

- **Vitest:** `src/paths.test.js`, `src/App.overlays.test.jsx`, `src/components/AdminOverlay.test.jsx` and `src/components/CapabilitiesPanel.test.jsx` all pass. The 3 new tests each fail without their fix.
- **Playwright (Chromium) on the new spec:** `e2e/deployment-operations-navigation.spec.js` — 3/3 passed.
- **Playwright (Chromium) on related specs:** `deployment_operations_walkthrough`, `access-accounts-iap-walkthrough`, `paths`, `system-overlay-tabs`, `system_workflows`, `system-settings-scoped-access-walkthrough`, `observability`, `upgrade`, `shell`, `team-admin-rbac-walkthrough`, `track6-navigation-merged-dissolve`, `responsive-navigation-walkthrough` and `config` all passed after the one spec update above. That run was 50 passed, plus the 1 test that was then updated and re-run green.
- **Full grain CI:** run `8b0c13d2465ecd00` at `891119b` was green: frontend, go, installer, lint, package cache, terraform and ui e2e all passed.

## Left as notes, not changed

- Team settings tabs are not in the URL either (`/team-settings` always opens on Team). This is the same shape as bug 1, but Team settings has no path segment for a tab today.
- The Metrics window is not in the URL.
- A successful Restart grain shows no acknowledgement.
- `SettingsPlanPreview.jsx` is unused since settings moved to autosave.
- A malformed `/api/sandboxes` field unmounts the whole app.
