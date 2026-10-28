# Deployment Operations UI Walkthrough — 2026-10-22

**Scope:** Sandboxes (`/sandboxes`, `/system/sandboxes`), Caches (`/caches`, `/system/caches`), CI (`/ci`, `/ci/<id>`), System operations (`/system`, `/system/logs`, `/system/top`, `/system/pipeline`, `/system/power`), Deployment-wide banners & alerts (`DisconnectedBanner`, top-bar `AlertsButton` / `AlertsPanel`, `System > Health`), and Metrics (`/metrics`, `/system/metrics`).

**Environment:**
- Real `grain demo -observability` server built from the repository over a throwaway embedded store (`GRAIN_E2E_ENDPOINTS=1`).
- Real built frontend (`make frontend`) driven in headless Chromium via Playwright (`@playwright/test`) at desktop (`1440×900`) and mobile (`390×844`) viewports in both dark and light modes.
- Existing 44 Playwright end-to-end tests across `caches.spec.js`, `ci-runs.spec.js`, `sandbox-health.spec.js`, `system-logs.spec.js`, `system-top.spec.js`, `system-power.spec.js`, `pipeline-pause-banner.spec.js`, `agent-pause-banner.spec.js`, `reconciler-down-banner.spec.js`, `disconnected-banner.spec.js`, and `metrics.spec.js` plus a 6-part custom Playwright walkthrough suite exercising every screen and boundary.

---

## 1. Intercepted Boundaries

Per the walkthrough protocol, the real Grain server and browser UI were retained throughout. Only host, CI-execution, cache, and live-connection boundaries absent or unsafe in the demo were stubbed via Playwright `page.route(...)` (or driven via the demo server's built-in `/api/e2e/*` hooks):

| Route / Boundary | Why Intercepted | Behaviors Exercised |
|---|---|---|
| `GET /api/sandbox-health` | Inject extreme pressure, 60-row tables, multi-point sparkline trends, standing sandboxes, guest/base-branch warnings, and disabled/error states | Default `grain demo -observability` reading inspected unstubbed first; then stubbed for host pressure (`9.60 / 5.20 / 2.00 of 4 vCPU`, disk floor warnings, `statfs` failure), 60-row live sandbox scrolling, 2-poll sparkline trend accumulation (`Trend, latest value ...`), standing sandboxes, guest/base-branch alerts, `enabled: false`, and HTTP 500 recovery. |
| `GET /api/executors` | Demo server has no remote executor nodes attached (`[]`) | Inspected default empty state (`No remote executors connected.`), stubbed 2 connected remote executors (`exec-west-1`, `exec-east-2`) to verify binary chips and active job counts, and stubbed HTTP 500 (`executor registry offline`) to verify error surfacing. |
| `POST /api/e2e/ci/finish` | Complete a manual CI run in demo mode without waiting on external VM guests | Completed manual `acme/widgets` CI run `test` job (`status: "success"`) so subsequent manual runs hit the real daemon's CI job cache (`Cache hit`, `1 jobs, all green · 1 cache hit`). |
| `GET /api/ci/runs` & `GET /api/ci/runs/:id` | Inject multi-job states (`failure` with `priorAttempts` & `sharedFailure`, `errored` with `fault`, `cached`, `skipped`, `pending`, `artifacts`) | Real daemon runs (`running`, `success`, `cached`, `cancelled`) and unknown run ID (`/ci/0000000000000000`) were exercised unstubbed against the real server; a synthetic run (`deadbeefcafe0001`) was stubbed to exercise `priorAttempts`, cross-branch `sharedFailure`, `fault: true`, `skipped`, `pending`, and downloadable artifacts in one run overlay. |
| `POST /api/host/restart`, `POST /api/host/reboot`, `POST /api/host/shutdown` | Never actually restart, reboot, or shut down the sandbox host | Intercepted `POST /api/host/{restart,reboot,shutdown}` to verify `window.confirm` cancellation (`0` POSTs), HTTP 500 error alert (`permission denied: systemctl`), HTTP 200 success banner, and network connection drop (`route.abort("connectionreset")` treated as positive completion since a real reboot/shutdown drops the connection before replying). |
| `GET /api/config` & `/api/events` | Drive deployment-wide banners and alerts caused by live daemon faults or multi-connection pauses | Disconnected banner driven by aborting `/api/config` & `/api/events` (`unreachable` and `restarting: true` variants, plus modal mirror inside an open dialog). Alert states (`reconcilerDown`, `stateRepoSyncFailure`, `hostSandboxes`, `agentPause` per-connection/per-model/escalated, `pipelinePause` manual & `upgrade-schedule`, `setup`, `stateRepoResetNotice`) driven and cleared via `/api/config` and `/api/settings` stubs alongside the real `POST`/`DELETE /api/pipeline-pause` endpoint. |
| `GET /api/metrics` | Inject partial collector failure (`report.failed`) and HTTP 500 error | Real 11-section demo metrics (`24h`, `7d`, `30d`, `90d`) inspected unstubbed first; then stubbed once with `failed: [{ section: "ciQueueing", error: "ci_runs_archive table locked" }]` and once with HTTP 500 (`metrics store unavailable`) followed by recovery on `Refresh`. |

---

## 2. Walkthrough Coverage & Verification

### 2.1 Sandboxes (`/sandboxes`, `/system/sandboxes`, `/settings/sandboxes`)

1. **Navigation & Pinned Sidebar Item:**
   - Navigated to `/sandboxes` (`[data-testid="system-page"]`), which redirects to `/system/sandboxes` with the `Sandboxes` tab selected (`aria-selected="true"`).
   - Opened `System` in the left rail (`/system`) and clicked the `Sandboxes` tab to reach `/system/sandboxes`.
   - Clicked `Customize pinned items` (`[data-testid="pin-customize-button"]`), checked `System → Sandboxes` (`[data-testid="pin-toggle-system/sandboxes"]`), verified `[data-testid="pinned-item-system/sandboxes"]` appeared in the rail and navigated directly to `/system/sandboxes`, then unpinned it.
2. **Host Commitment & Pool Configuration (`/settings/sandboxes`):**
   - Verified `Settings > Sandboxes` (`/settings/sandboxes`) displays the `Host commitment` card (`8 vCPU · 15.6 GiB RAM · 78.7 GiB disk on /var/lib/grain/sandboxes`, peak `24 vCPU · 24.0 GiB RAM · 120.0 GiB disk` across `4` agent runs and `2` CI jobs).
3. **Host Pressure, Pool Status, Lifetime, Storage, CI Panels & Connected Executors (`/system/sandboxes`):**
   - **Host & Pool:** Verified load average (`1.45 / 1.20 / 0.95 of 8 vCPU`), CPU utilization (`31%`), Memory (`6.0 / 16.0 GB (38%)`), Swap (`128 / 2048 MB (6%)`), Nested virtualization (`enabled (kvm_intel nested=Y)`), and disk table (`store, sandboxes, cache` on `/var/lib/grain` with `42.0 / 100.0 GB`, `20.0 GB kept free, 38.0 GB spare`).
   - **Storage panels:**
     - `SandboxStoragePanel` (`[data-testid="sandbox-storage-panel"]`): `14.0 GiB written by 2 live sandboxes of 40.0 GiB promised to them` + pointer to `Settings → Sandboxes`.
     - `CIArtifactsPanel` (`[data-testid="ci-artifacts-panel"]`): `312.0 MiB of 2.0 GiB · 9 artifacts from 4 runs · the oldest of them 3 days old`, bounds (`up to 128.0 MiB per run, 32.0 MiB for any one file, none older than 14 days`), explanation that artifacts have no clear button because they are the only copy of what a run produced, and pointer to `Settings → CI`.
     - `CIJobCachePanel` (`[data-testid="ci-job-cache-panel"]`): `148.0 MiB of 256.0 MiB · 384 of 4096 job results`, hit rate (`answered 91 of 110 job lookups (83%)`), pointer to `Settings → CI`, and `Empty the job cache` button (`window.confirm` dismiss leaves cache intact; accept calls `DELETE /api/ci/cache` and updates to `0 B of 256.0 MiB · no job result is cached`).
     - `CIHistoryPanel`: `18.5 MiB of 64.0 MiB · 14 runs still have step output · the oldest of it 4 days old`.
     - Proxy cache pointer: `holding 3.0 GiB of them right now` with pointer to `System → Caches`.
   - **Lifetime (`SandboxLifetimePanel`):** `142 sandboxes built, 64.5 h of machine time (27m 15s mean) · 2 failed to start` broken down by `Agent` (`38 built`) and `CI` (`104 built`).
   - **Live Sandboxes Table:** Verified columns (`Run or CI job`, `Kind`, `Backend`, `Sandbox`, `Status`, `Load (1/5/15m)`, `CPU trend`, `Memory`, `Memory trend`, `Swap`, `Disk`, `Disk trend`, `Resources`, `Network`, `Nested virt`).
   - **Connected Executors (`ConnectedExecutors`):** Verified empty state (`No remote executors connected.`), connected state (`exec-west-1`, `exec-east-2` with `Connected` chips, `Antigravity (agy)` / `Claude Code` / `Codex` framework chips, uptime, and `Active jobs: 2 / 4`), and HTTP 500 error state (`Connected executors could not be read (...)`).
   - **Resource Trends, Pressure, Warnings & 60-Row Scrolling:**
     - Polled twice via `Refresh` and verified `<svg>` sparklines transition from `Not enough data yet` to `Trend, latest value ...` across host CPU, host memory, host disk, and per-sandbox CPU/memory/disk columns.
     - Verified overloaded core badges (`2.40 per core: more than twice as much runnable work as this host has cores`), breached disk floor warning (`below the 20.0 GB floor: new CI jobs are held and agent dispatch deferred`), unreadable volume error (`statfs /var/lib/grain/scratch: input/output error`), stale sweep age (`As of 9s ago`), slow/breaker probe labels (`answered in 8.4s`, `failed after 20.0s, 14s ago`), standing sandboxes (`Standing sandboxes` with `3 failed launches in a row; next attempt ...`), guest publishing/drift warnings, base-branch gate/publishing alerts, and host credential expiry alert.
     - Verified a 60-row live sandbox table renders and scrolls cleanly to row 60 without horizontal page overflow (`scrollWidth <= innerWidth`).
     - Verified `enabled: false` (`Sandbox pool is not enabled on this server.`) and HTTP 500 error recovery via `Refresh`.

### 2.2 Caches (`/caches`, `/cache`, `/actions-cache`, `/system/caches`)

1. **Navigation:**
   - Verified `/caches`, `/cache`, and `/actions-cache` all redirect cleanly to `/system/caches` with `System > Caches` selected.
2. **Proxy Cache (`PackageCachePanel`):**
   - Inspected total summary (`3.4 GiB of 20.0 GiB · 462 cached artifacts · 85% of 228 fetches served from disk`), hit history breakdown (`194 hits (11.2 GiB served from disk) · 34 misses (3.4 GiB fetched upstream)`), and per-ecosystem table (`apt`, `pip`, `docker`, `npm`, `go`) with `Entries`, `On disk`, `Hits`, `Served from disk`, `Misses`, `Fetched upstream`, `Hit rate`, and per-row `Flush` buttons.
   - Clicked `Flush` on `npm`: verified `DELETE /api/package-cache/npm` zeroed `npm` (`0` entries, `0 B` on disk) while preserving `apt`, `pip`, `docker`, and `go` across page reload.
   - Clicked `Flush the whole cache`: verified `DELETE /api/package-cache` zeroed all 5 ecosystems (`0 B of 20.0 GiB · empty`) and persisted across reload.
3. **Actions Cache (`ActionsCachePanel`):**
   - Inspected summary (`1.5 GiB of 10.0 GiB · 15 entries across 2 repositories · 79% of 42 lookups hit · oldest unread 4 days ago · 2 evicted for space`) and per-repository breakdown (`acme/widgets` `1.1 GiB`, `acme/docs` `380.0 MiB`).
   - Clicked `Flush` on `acme/docs`: verified `DELETE /api/actions-cache/acme/docs` removed `acme/docs` and left `acme/widgets` (`1.1 GiB of 10.0 GiB · 4 entries in 1 repository`).
   - Clicked `Flush the whole cache`: verified `DELETE /api/actions-cache` emptied the Actions cache (`0 B of 10.0 GiB · empty`) and persisted across reload.

### 2.3 CI (`/ci`, `/ci/<id>`)

1. **Active Sandboxes, Filtering & Manual Run Creation:**
   - Opened `/ci` (`[data-testid="ci-runs-page"]`), verified the CI job cache chip (`148.0 MiB / 256.0 MiB (58%) · 83% hit`) and `Clear cache` button.
   - Tested `Filter by repo, branch, SHA, workflow, status, or task` textbox and status filter chips (`All`, `Failed`).
   - Clicked `Start a run`, entered `acme/widgets` on branch `main`, clicked `Load workflows` (`GET /api/ci/workflows`), and clicked `Start run` (`POST /api/ci/runs`).
2. **Run Overlay, Live Logs, Completion, Cache Hit & Clear Cache:**
   - Verified starting the run navigated to `/ci/<runId>` (`[data-testid="ci-run-overlay"]`) showing `Running`, job `test`, step `Run tests`, and live log output (`=== RUN   TestWidgets`).
   - Completed the run via `POST /api/e2e/ci/finish` (`status: "success"`), verified the overlay updated to `Success` (`1 jobs, all green`).
   - Started a second manual run on `acme/widgets` `main` and verified immediate cache reuse (`Cache hit` chip on the `test` job and `1 jobs, all green · 1 cache hit` summary).
   - Tested `Clear cache` on `/ci`: dismissing `window.confirm` issued `0` `DELETE /api/ci/cache` calls; accepting called `DELETE /api/ci/cache` and updated the chip to `0 entries · 0 B`.
3. **Cancel Run (`window.prompt`):**
   - Started a fresh run on `acme/widgets` `main`, opened `/ci/<runId>`, clicked `Cancel run`: dismissing `window.prompt` issued `0` cancel requests; accepting with `"operator walkthrough test cancel"` called `POST /api/ci/runs/<id>/cancel` and transitioned the run to `Cancelled` (`cancelled by user: operator walkthrough test cancel`).
4. **Direct-Link, Reload, Unknown Run ID & Multi-State Inspection:**
   - Directly loaded `/ci/0000000000000000` (a run ID the daemon does not have) and verified the error state (`CI run not found`) and clean closure back to `/ci`.
   - Verified task link navigation (`Task #...` chip in `/ci` rows navigates to `/tasks/<id>`).
   - Verified multi-state run rendering on `/ci/deadbeefcafe0001`:
     - `failure` job with `priorAttempts` (`Attempt 1` vs `Attempt 2` selector switching logs) and cross-branch `sharedFailure` warning (`Also failing on 2 other branches`).
     - `errored` job with `fault: true` (`Infrastructure fault — not a code failure`).
     - `success` + `cached: true` (`Cache hit`).
     - `skipped` and `pending` jobs.
     - Run `artifacts` section with downloadable file links (`/api/ci/runs/.../artifacts/...`).
   - Confirmed no manual `Retry` button is exposed in the `/ci` browser UI (reruns are triggered via the agent MCP tool `retrigger_ci` or starting a new run).

### 2.4 System Operations (`/system/logs`, `/system/top`, `/system/pipeline`, `/system/power`)

1. **Access Model Confirmation:**
   - Confirmed `#4880` (`single-tenant reset`) removed the multi-user non-admin / Admin-mode toggle gating; every visitor authenticated with the deployment access code is the deployment owner and sees `System` and `Settings` in full.
2. **System > Logs (`/system/logs`):**
   - Verified source selector (`grain daemon (in-memory)` plus any host units), `Search logs` filter input, tail scroll pinning to bottom when scrolled near the end, and `Refresh`.
3. **System > Top (`/system/top`):**
   - Verified live host CPU/memory/swap summary, process table (`PID`, `User`, `CPU%`, `Mem%`, `RSS`, `Time`, `Command`), `Auto-refresh` checkbox toggle, manual `Refresh` button, and recovery from a stubbed HTTP 500 on `GET /api/system/top`.
4. **System > Pipeline (`/system/pipeline`):**
   - Clicked `Pause pipeline`: dismissing `window.confirm` issued `0` `POST /api/pipeline-pause` requests; accepting with reason `"walkthrough maintenance window"` paused the pipeline (`POST /api/pipeline-pause`).
   - Verified the deployment-wide pipeline pause alert (`Deployment paused`, `No new tasks are being picked up... walkthrough maintenance window`) immediately appeared in the top-bar `AlertsButton` badge/popover (`[data-testid="alerts-button"]`, `[data-testid="alerts-panel"]`) and on `System > Health` (`/system`).
   - Clicked `Resume pipeline` (`DELETE /api/pipeline-pause`) and verified the status returned to `Running` and the alert cleared from both the top-bar `AlertsButton` and `System > Health`.
5. **System > Power (`/system/power`):**
   - Exercised all three power controls (`Restart grain`, `Reboot host`, `Shut down host`) with `POST /api/host/{restart,reboot,shutdown}` intercepted in Playwright:
     - **Cancel confirmation:** Dismissing `window.confirm` sent `0` POST requests.
     - **Failure (HTTP 500):** Returning `500 {"error":"permission denied: systemctl"}` displayed the error alert and re-enabled the buttons.
     - **Success (HTTP 200) & Connection Reset:** Returning `200 {"status":"restarting"}` or aborting with `connectionreset` (simulating immediate host/daemon teardown) both displayed the expected status feedback (`Grain is restarting…`, `Host reboot initiated…`, `Host shutdown initiated…`) without mutating the host.

### 2.5 Deployment-Wide Banners & Alerts

1. **Disconnected Banner (`DisconnectedBanner.jsx`):**
   - Intercepted `/api/config` and `/api/events` with `abort("failed")` and verified `[data-testid="disconnected-banner"]` appeared at the top of the page (`Cannot reach the grain server — changes and actions are unavailable until the connection is restored.`) without overlapping page content.
   - Tested the `restarting: true` state (`Grain is restarting — this page will reconnect automatically.`).
   - Opened a modal dialog while disconnected and verified `[data-testid="disconnected-banner-mirror"]` rendered at the top of the dialog so the warning is visible above the modal backdrop.
   - Restored `/api/config` and verified the banner cleared automatically on the next poll.
2. **Top-Bar `AlertsButton` / `AlertsPanel` & `System > Health` (`/system`):**
   - Since `#4008`, standing deployment warnings live in the top-bar `AlertsButton` (`[data-testid="alerts-button"]`) and `System > Health` (`[data-testid="system-health"]`) rather than pushing the page down with multiple full-width banners.
   - Exercised all alert sources simultaneously and individually:
     - `reconciler-down` (`critical`): `Reconcile loop stopped` — `The task reconciler has stopped; no tasks are being dispatched or checked until grain restarts.`
     - `state-sync-failing` (`critical`): `Config repo sync failing` — `Settings have not been exported to acme/grain-config since ...: push rejected` with `Open System` action (now wired on `System > Health` to open `Settings → Config repo`).
     - `host-sandboxes` (`warning`): `Sandboxes run on the host` with `Open Sandboxes` action.
     - `agent-pause` (`warning`): tested per-connection (`Connection Primary Claude`), single-model (`claude-opus-4 on Primary Claude paused until ... — usage limit; the connection's other models keep dispatching.`), and escalated account-wide (`nothing is being dispatched onto connection Primary Claude, on any model, until ...`) pauses with `Resume now` (`DELETE /api/pause?...`) and `Open connection` (`Settings → Models`).
     - `pipeline-pause` (`warning`): tested manual pause (`Resume pipeline`) and scheduled-upgrade hold (`byKind: "automation", byId: "upgrade-schedule"`, where clicking `Resume pipeline` prompts with `resumeCancelsUpgrade` pointing to `System ▸ Upgrade`).
     - `setup` (`info`): `N setup steps left` with `Finish setup` action.
     - `System > Health` settings banners: `[data-testid="health-config-repo"]` (`A merged change reset settings on this deployment.` with `Config repo` button), `[data-testid="health-pending-restart"]` (`Saved, not applied until restart: ...` with `Power` button), and `[data-testid="health-unrunnable"]` (`Stored, but not runnable: ...` with `Settings → Engine` link).
   - Verified flat list rendering when `< 5` alerts are active and collapsible severity groups (`Critical`, `Warning`, `Info`) when `>= 5` alerts are active, and confirmed all alerts clear when their underlying causes clear in `/api/config`.

### 2.6 Metrics (`/metrics`, `/system/metrics`, `/boards`)

1. **Navigation:**
   - Opened `/metrics` (redirects to `/system/metrics`), opened `System > Metrics` from the tab bar, and clicked `Open` on the throughput card in `/boards` (`BoardsView`) to reach `/system/metrics`.
2. **Release Notes Panel (`ReleaseNotesPanel`):**
   - Inspected `Release notes` (`[data-testid="release-notes-panel"]`), switched between `Weekly` (`[data-testid="release-notes-weekly"]`) and `Versions` (`[data-testid="release-notes-versions"]`), expanded accordion entries, and verified markdown rendering.
3. **All 11 Metrics Sections, Windows, URL Narrowing, Failures & Task Links:**
   - Inspected all 11 sections in order: `Throughput`, `Latency`, `Capacity`, `Backlog`, `Tool use`, `Prompts`, `CI waits`, `CI queueing`, `Merge queue`, `Scheduling`, and `Sandboxes`.
   - Switched time windows (`24h`, `7d`, `30d`, `90d`) and confirmed `?window=30d` updates in the URL, persists across reload, and invalid `?window=bogus` falls back to `7d`.
   - Verified narrowing query parameters carried in the URL (e.g. `?repo=acme%2Fwidgets`) display the `not in force` scope notice (`Narrowed to repo acme/widgets (some sections are deployment-wide and not narrowed)`).
   - Tested partial collector failure (`report.failed` for `ciQueueing`) and verified the per-section failure alert renders while the other 10 sections remain visible.
   - Tested full HTTP 500 failure on `GET /api/metrics` and verified recovery on clicking `Refresh`.
   - Clicked a task reference button (`task 2`) inside the metrics tables, confirmed navigation to `/tasks/2`, and returned via browser `Back`.

---

## 3. Bugs Found and Fixed

### Bug 1: `ConnectedExecutors` on `System > Sandboxes` had a stale pre-split token pointer and silently swallowed `GET /api/executors` errors as an empty state
- **Where:** `ui/src/components/ConnectedExecutors.jsx` (rendered at the bottom of `/system/sandboxes`).
- **What went wrong:**
  1. The subtitle read `"Remote executor nodes connected now, providing agent execution environments and binaries. The tokens they connect with are below."` That sentence was written when `ConnectedExecutors` sat directly above `ExecutorsPanel` on `Settings > Executors`. When `ConnectedExecutors` moved to `System > Sandboxes`, no join tokens existed below it.
  2. `api("/api/executors")` chained `.catch(() => {})`, so when `/api/executors` failed (e.g. HTTP 500), `executors` remained `[]` and the component rendered `<Alert severity="info">No remote executors connected.</Alert>` — a false empty state hiding the error.
- **Fix:** Updated the subtitle to `"Remote executor nodes connected now, providing agent execution environments and binaries. The tokens they connect with are on Settings → Executors."` and tracked `loadError` so a failed `/api/executors` fetch renders `<Alert severity="error">Connected executors could not be read ({loadError}).</Alert>` instead of the empty-state info alert. Added regression test in `ui/src/components/SystemPage.test.jsx`.

### Bug 2: `System > Health` showed stale copy for `stateRepoResetNotice` and had a no-op `Open System` button for `state-sync-failing`
- **Where:** `ui/src/components/system/HealthTab.jsx`.
- **What went wrong:**
  1. When `stateDot(config)` was truthy (which triggers only on `config.stateRepoResetNotice`, whose tooltip in `UnbackedStateDot.jsx` is `"A merged change reset settings on this deployment"`), `HealthTab.jsx` rendered `"The config repo is not backing this deployment's settings."` — leftover wording from the removed `stateRepoUnbacked` flag.
  2. `HealthTab` passed `onOpenSystem={() => {}}` to `<AlertsPanel />`. When `state-sync-failing` (`Config repo sync failing`) was active, clicking its `"Open System"` action button inside `System > Health` called the no-op function and did nothing.
- **Fix:** Updated the `health-config-repo` alert copy in `HealthTab.jsx` to `"A merged change reset settings on this deployment."` and wired `onOpenSystem={() => onOpenSettings?.("config-repo")}` so clicking the action button navigates to `Settings → Config repo`. Added regression tests in `ui/src/components/SystemPage.test.jsx`.

### Bug 3: Stale pre-split navigation pointers in `CachesSection.jsx` and `alerts.js`
- **Where:** `ui/src/components/settings/CachesSection.jsx` (line 80) and `ui/src/alerts.js` (`resumeCancelsUpgrade`, line 187).
- **What went wrong:**
  1. In `CachesSection.jsx`, the Proxy cache card said `"are on the Caches page in the sidebar."` whereas the Actions cache card immediately below it said `"are on System → Caches."` (`Caches` is a tab on `System`, not a default top-level sidebar item).
  2. In `alerts.js`, `resumeCancelsUpgrade` told operators `"Schedule it again from Settings ▸ Upgrade."` whereas the `Upgrade` tab lives on `System ▸ Upgrade` (`/system/upgrade`).
- **Fix:** Updated `CachesSection.jsx` to say `"System → Caches."` and `alerts.js` `resumeCancelsUpgrade` to say `"System → Upgrade."`. Added regression assertions in `ui/src/components/settingsReferences.test.jsx` and `ui/src/alerts.test.js`.
