# Deployment Operations Walkthrough Report — 2026-10-26

- **Commit tested:** `6338ed34d5d895d83cf0f13df4e9cb41b1b44ab5` (`feat(ui): show which model and framework ran each attempt`)
- **Date:** 2026-10-26
- **Environment:**
  - OS: Debian GNU/Linux 12 (`linux/amd64`)
  - Server: `./bin/grain demo -observability` over a throwaway embedded SQLite store with embedded Vite production static bundle (`pkg/ui/static`)
  - Browser: Playwright Chromium (`chromium-1234`, Headless) at `1440×900` (desktop), `820×1180` (tablet), and `390×844` (mobile), light and dark themes

---

## 1. Bugs Found and Fixed

### Bug 1: Acknowledging or investigating an operator notification on `System → Health` (`/system`) did not refresh open notifications
- **Surface:** [System → Health](/system) (`ui/src/App.jsx`, `ui/src/components/SystemPage.jsx`, `ui/src/components/system/HealthTab.jsx`)
- **Reproduction:**
  1. Start `grain demo -observability` (which seeds an open operator notification, `"A merge has been stalled for 2 hours"`, or receive any open notification from `GET /api/notifications?open=1`).
  2. Navigate to [System → Health](/system), where `HealthTab` renders `<AlertsPanel alerts={alerts} onChanged={onChanged} />` containing both config-derived alerts and `notificationAlerts(notifications)`.
  3. Click **Acknowledge** on the operator notification row in `System → Health`.
  4. Observe that `POST /api/notifications/{id}/ack` succeeds (`200 OK`), but the notification row remains visible in `System → Health` and counted in the top-bar **Alerts** button badge for up to 30 seconds (`POLL_MS = 30000` in `ui/src/notifications.js`).
- **Root Cause:** Commit `bfc25e7d8` ("feat(ui): operator notifications in the alerts bell and on the Inbox") added `...notificationAlerts(notifications)` to `alerts` in `ui/src/App.jsx` and wired `<AlertsButton onChanged={() => Promise.all([refreshConfig(), reloadNotifications()])} />`, but left `<SystemPage onConfigChanged={refreshConfig} />` calling only `refreshConfig` without `reloadNotifications()`.
- **Fix:** Updated `<SystemPage>` in `ui/src/App.jsx` to pass `onConfigChanged={() => Promise.all([refreshConfig(), reloadNotifications()])}` so acknowledging or investigating an operator notification from `System → Health` immediately reloads open notifications alongside `/api/config`.
- **Regression Coverage:**
  - Unit test in `ui/src/App.overlays.test.jsx`: `"reloads open notifications immediately when acknowledging one on System → Health"`.
  - Playwright e2e assertion in `ui/e2e/deployment_operations_walkthrough.spec.js` (Test 8).

### Bug 2: Resuming the pipeline from `System → Pipeline` (`/system/pipeline`) during a scheduled upgrade hold did not warn that resuming cancels the upgrade
- **Surface:** [System → Pipeline](/system/pipeline) (`ui/src/components/PipelineControl.jsx`)
- **Reproduction:**
  1. Schedule an upgrade drain (or have `config.pipelinePause` active with `byKind: "automation", byId: "upgrade-schedule"`).
  2. Open the top-bar **Alerts** popover or [System → Health](/system) and click **Resume now**: `resumePipeline(pause)` in `ui/src/alerts.js` checks `scheduledUpgradeHold(pause)` and prompts with `resumeCancelsUpgrade(pause)` warning that resuming cancels the scheduled upgrade (`"This deployment is paused because it is waiting to upgrade...\n\nResuming cancels that upgrade: tasks start again and the upgrade will not be applied. Schedule it again from System → Upgrade."`).
  3. Navigate to [System → Pipeline](/system/pipeline) and click **Resume pipeline**.
  4. Observe that `PipelineControl.jsx` ignores `scheduledUpgradeHold(pause)` and displays the generic prompt `"Resume the pipeline? Grain will start dispatching queued agent and CI runs again."` without warning the operator that resuming cancels the scheduled upgrade.
- **Root Cause:** `PipelineControl.jsx` hardcoded the generic resume confirmation string instead of checking `scheduledUpgradeHold(pause)` and using `resumeCancelsUpgrade(pause)` from `ui/src/alerts.js`.
- **Fix:** Imported `scheduledUpgradeHold` and `resumeCancelsUpgrade` in `ui/src/components/PipelineControl.jsx` and used `resumeCancelsUpgrade(pause)` when confirming a `DELETE /api/pipeline-pause` while a scheduled upgrade hold is active.
- **Regression Coverage:**
  - Unit test in `ui/src/components/PipelineControl.test.jsx`: `"warns that resuming during a scheduled upgrade hold cancels the upgrade"`.
  - Playwright e2e assertion in `ui/e2e/deployment_operations_walkthrough.spec.js` (Test 8).

---

## 2. Coverage Walkthrough & Outcomes

### 2.1 Sandboxes (`/sandboxes` and `/system/sandboxes`)
- **Navigation & URL canonicalization:** Navigating to `[/sandboxes](/sandboxes)` redirects cleanly to `[/system/sandboxes](/system/sandboxes)` with the **Sandboxes** tab selected under **System**. Switching tabs and using browser `Back`/`Forward` preserves tab state in the URL.
- **Host commitment, overcommit & pressure:**
  - On [System → Health](/system) and [System → Sandboxes](/system/sandboxes), verified `SandboxBudgetPanel`, `SandboxClusterFree` (`Cluster free: 6.5 vCPU / 40.0 GiB of 286.5 vCPU / 1080.0 GiB allocatable on 3 nodes · roomiest node gke-pool-1-node-b`), `SandboxOvercommitStatus` (`Overcommit: auto`, `Effective factors: CPU 2.75× · memory 1.25×`, PSI readings, OOM kills count, and recent overcommit adjustment log `CPU 2.50× → 2.75×`), and `Reconciliation` (`0 accounted in ledger vs. 0 live on backend · Orphans reaped: 0`, `in sync` chip).
  - Exercised host load average (`0.45 / 0.55 / 0.60 of 4 vCPU · Utilization: 15% · Memory: 4096 / 16384 MB · Swap: 0 / 4096 MB`), CPU/memory trend sparklines across consecutive polls, and critical host pressure reasons (`available memory below 10%`, `CPU PSI some avg10 at 42.5%`, `3 OOM kills`).
- **Storage, CI artifacts, CI job cache & CI history:**
  - Verified the **Storage** breakdown for active sandboxes, **Proxy cache** (`holding 5.3 GiB`, linking to [System → Caches](/system/caches) and [Settings → Caches](/settings/caches)), **CI history** (`0 B of 64.0 MiB · no run is holding step output`), **CI job cache** (`0 B of 256.0 MiB · no job result is cached`, `Empty the job cache` button disabled when empty and enabled when populated), and **CI artifacts** (empty/disabled state and populated `32.0 MiB of 512.0 MiB` state).
- **Standing sandboxes, Persistent sandboxes & Connected executors:**
  - Verified empty-state guidance and `/session/director` links for **Standing sandboxes** (`tables/standing_sandbox.json`) and **Persistent sandboxes** (`tables/persistent_sandbox.json`).
  - Verified **Connected executors** (`runner-infra-worker-01` with `Antigravity (agy)`, `Claude`, `Active jobs: 2 / 4`, and `runner-frontend-worker-01` with `Antigravity (agy)`, `Codex`, `Active jobs: 0 / 2`), plus error handling when `GET /api/executors` fails (`Connected executors could not be read (...)` instead of a false empty message).
- **Active sandboxes, collapsible CI group & 60-row scrolling:**
  - Started a live held CI run via `POST /api/e2e/ci/start` (`branch: "ops-walkthrough/sandboxes"`), verified the `CI sandboxes (1)` collapsible group row is collapsed by default (`aria-expanded="false"`), expanded it to inspect the live `ci` row, and finished the run via `POST /api/e2e/ci/finish` to confirm the table returns to `"Nothing in flight, so no sandboxes exist."`
  - Stubbed 60 concurrent sandboxes (`5` agent + `55` CI), expanded `CI sandboxes (55)`, scrolled to the 60th row (`grain-task-159`), and confirmed `document.documentElement.scrollWidth <= document.documentElement.clientWidth` at both `1440×900` and `390×844`.
  - Stubbed `500` on `GET /api/sandboxes`, confirmed the error alert renders, and verified clicking **Refresh** recovers cleanly once the route is restored.

### 2.2 Caches (`/caches` and `/system/caches`)
- **Navigation:** Visiting `[/caches](/caches)` redirects to `[/system/caches](/system/caches)` with the **Caches** tab selected.
- **Proxy cache:**
  - Verified total space consumed (`5.3 GiB` across ecosystems) and per-kind rows: `apt packages: 412 packages`, `npm packages: 128 packages`, `Python packages: 85 packages`, `Go modules: 64 packages`, `Docker Hub layers: 12 layers`, and `Playwright browsers: 3 browsers`.
  - Exercised per-kind flush (`Flush apt packages` -> `DELETE /api/package-cache/apt`), verifying immediate update to `0 packages (0 B)`.
  - Exercised `Flush all` (`DELETE /api/package-cache`) with `window.confirm` dismiss (no mutation) and accept (all ecosystems drop to `0`).
- **Actions cache:**
  - Verified repository entries (`acme/widgets: 2.0 GiB`, `acme/gadgets: 1.0 GiB`) and clear/flush confirmation flow.

### 2.3 CI (`/ci` and `/ci/<id>`)
- **Active sandboxes & Job cache line:**
  - Verified the **Active sandboxes** section (`No CI job is on a machine right now` when idle, and collapsible table with `localStorage` persistence under `grain.ci.activeSandboxes.expanded` when jobs are running).
  - Verified the **Job cache** summary line (`entries · bytes of maxBytes · hit rate since this daemon started`) and `Clear cache` confirmation dialog.
- **Manual run, live output, cancel & finish:**
  - Opened **Start a run** on `[/ci](/ci)`, selected `acme/widgets`, entered branch `ops-walkthrough/cancel`, clicked **Load workflows**, selected `.grain/workflows/observe.yml`, and clicked **Start run**.
  - Verified navigation to `/ci/<id>` while running, dismissed the **Cancel run** `window.prompt` (confirming the run remains in flight), then accepted **Cancel run** and confirmed `DELETE /api/ci/runs/<id>` transitions the run to `cancelled` and hides the cancel button.
  - Started and finished a held run on `ops-walkthrough/logs`, opened the `observe` job and `Wait for the browser` step, clicked **Show output**, and confirmed the step log displays `"observed through the sandbox"`.
- **Task link, unknown run ID & multi-state run:**
  - Triggered a push run for [Task #2](/tasks/2) via `POST /api/ci/runs` (`{ taskId: "2" }`), navigated to `[/tasks/2](/tasks/2)`, clicked **Show the run**, and verified the errored run overlay opens with `no workflow of acme/widgets answers push`.
  - Navigated directly to a forgotten run ID (`[/ci/0000000000000000](/ci/0000000000000000)`) and verified the info alert: `no CI run "0000000000000000" -- a run is remembered for a while and then forgotten`.
  - Exercised a stubbed multi-state run (`/ci/deadbeef12345678`) covering `failure` with `retried: true` and `priorAttempts`, `errored` step (`exitCode: -1` with transport `error`), `success` with `cached: true` (`Cache hit` chip), `skipped`, stored downloadable artifact (`coverage-report`, `.tar.gz`) and oversized unstored artifact note (`280MB, over the 32MB limit`), and both **Retry** and **Retry failed jobs** buttons.

### 2.4 System Operations (`/system`, `/system/logs`, `/system/top`, `/system/pipeline`, `/system/power`)
- **System → Logs (`[/system/logs](/system/logs)`):**
  - Verified log output in `pre.logs-view`, filtered via **Search logs** (`daemon` matches; `nonexistent-pattern-xyz` shows `No matching log lines.`), cleared filter, and clicked **Refresh**.
- **System → Top (`[/system/top](/system/top)`):**
  - Verified `grain-daemon` appears in the process table, toggled **Auto-refresh (2s)** off, and triggered manual **Refresh**.
- **System → Pipeline (`[/system/pipeline](/system/pipeline)`):**
  - Tested **Pause pipeline** confirmation dismiss (remains running) and accept (`POST /api/pipeline-pause`), verified the deployment-wide pipeline pause alert appears in the top-bar **Alerts** button and [System → Health](/system), and tested **Resume pipeline** confirmation dismiss and accept (`DELETE /api/pipeline-pause`).
  - Verified the new scheduled-upgrade hold confirmation warning (`"Resuming cancels that upgrade..."`) when clicking **Resume pipeline** during an `upgrade-schedule` automation pause.
- **System → Power (`[/system/power](/system/power)`):**
  - Intercepted `POST /api/host/restart`, `POST /api/host/reboot`, and `POST /api/host/shutdown` so the sandbox host was never mutated.
  - Verified dismissing `window.confirm` sends `0` POST requests for all three actions.
  - Verified accepting **Restart grain** (`200 OK`), **Reboot host** (`connectionreset` abort treated as accepted reboot), and **Shut down host** (`500` error surfaced in the error banner).
- **Single-tenant access model note:**
  - Per commits `85dbd23aa` ("refactor: remove -tenancy and make the daemon single-tenant only") and `3912ea39b` ("refactor: remove admin mode (single-tenant reset 3c)"), the multi-tenant non-admin viewer role and the `Admin mode` browser toggle were intentionally removed from the codebase; all authenticated single-tenant operators have access to the **System** and **Settings** surfaces.

### 2.5 Deployment-Wide Banners & Alerts
- **Standing alerts architecture (`d98d9c853`):**
  - Per commit `d98d9c853` ("feat(ui): show pauses and deployment warnings in an alerts panel, not banners") and `e65a1d3da` ("refactor: remove the UI trust options the single-tenant daemon never reads"), deployment pauses and health warnings live in the top-bar **Alerts** button (`AlertsButton` / `AlertsPanel`) and on [System → Health](/system), while `DisconnectedBanner` is the sole standing banner above the top bar.
- **Exercised alerts & grouping:**
  - Drove `reconciler-down` (`Reconcile loop stopped`), `state-sync-failing` (`Settings are not being exported` with **Config repo** action), `host-sandboxes` (`critical` pressure), `pipeline-pause` (`Deployment paused` with scheduled-upgrade hold copy and **Resume now**), two `agent-pause` connection rows (`Team A Claude` and `Team B Codex` with `Until ...`, **Resume now**, and **Open connection**), `setup` (`1 setup step left` with **Finish setup**), and open operator notifications (`CI runner quota exhausted` / `ZFS scrub detected 2 checksum errors` with **Acknowledge** and **Investigate**).
  - Confirmed that with 5 or more alerts across multiple scopes (`GROUP_AT = 5`), `groupAlerts` renders scope subheaders (`Deployment`, `Connection Team A Claude`, `Connection Team B Codex`), and that acknowledging or resolving each cause clears its row and updates the badge severity (`critical` -> `warning` -> `info` -> `none`).
- **DisconnectedBanner:**
  - Aborted `/api/tasks*` and `/api/config*` to trigger `data-testid="disconnected-banner"` (`Unreachable since ... -- retrying automatically; this page will pick back up on its own once it answers again.`), then removed the abort routes and confirmed the banner clears automatically on the next poll tick.

### 2.6 Metrics (`/metrics` and `/system/metrics`)
- **Entry points:** Opened from `[/metrics](/metrics)` (redirects to `[/system/metrics](/system/metrics)`) and from the **System → Metrics** tab, and verified clicking `task 1` in the **Backlog** section opens `[/tasks/1](/tasks/1)` and browser `Back` returns to `[/system/metrics](/system/metrics)`.
- **ReleaseNotesPanel:** Verified both **Weekly** (`week` selector, hero chips `Pull requests merged`, `Tasks completed`, `Tasks filed`, `Agent runs`, `Agent hours`, and per-repo breakdown) and **Versions** (embedded release notes list with `running` badge on `v1.2.0`).
- **Report sections, windows, narrowing & error states:**
  - Verified `Throughput`, `Latency`, `Capacity`, `Backlog`, `Scheduling`, and `EmptyWindowNote`.
  - Exercised time windows (`Last 24 hours` -> `?window=24h`, `Last 30 days` -> `?window=30d`, `Last 90 days` -> `?window=90d`, `Last 7 days` -> bare `/system/metrics`, and invalid `?window=999d` falling back to `Last 7 days`).
  - Exercised URL narrowing (`/system/metrics?f=repo:any:acme/widgets&f=state:any:queued`), confirming both `"Measured over the tasks in acme/widgets."` and the unsupported-filter notice `"A report is measured over whole distributions, so the rest of this question is not in force: state."`
  - Exercised partial section failure (`report.failed` rendering `"Not available in this report: prompts (census table locked (ref: m-91))."`) and full `500` cold-load failure (`"The report could not be loaded (database busy). Refresh to try again."`) followed by recovery via **Refresh**.

---

## 3. Intercepted / Stubbed Routes

| Route | Method | Purpose of Stub |
| :--- | :--- | :--- |
| `/api/host/restart` | `POST` | Prevented restarting the daemon process; returned `200 OK` (`{ ok: true }`) to verify the confirmation and post-restart UI state. |
| `/api/host/reboot` | `POST` | Prevented rebooting the sandbox VM; aborted with `connectionreset` to verify the UI treats a dropped connection during reboot as an accepted command. |
| `/api/host/shutdown` | `POST` | Prevented shutting down the sandbox VM; returned `500` (`{ error: "intercepted demo shutdown failure" }`) to verify error surfacing. |
| `/api/sandboxes` | `GET` | Supplemented the live demo response to test 60 active sandboxes (`5` agent + `55` CI), pool readiness (`2/4 ready`), critical host pressure, and `500` failure/recovery. |
| `/api/ci/runs/deadbeef12345678` | `GET` | Exercised all CI job/step conclusion states (`failure` with `priorAttempts`, `errored` transport fault, `cached`, `skipped`, stored and oversized `artifacts`) in one run detail view. |
| `/api/config` | `GET` | Temporarily injected `reconcilerDown`, `stateRepoSyncFailure`, `sandboxHealth`, `pipelinePause` (`upgrade-schedule`), and `agentPause` entries to verify `AlertsPanel` scope grouping and confirmation flows. |
| `/api/notifications?open=1`, `/api/notifications/*/ack` | `GET`, `POST` | Verified immediate notification reload when acknowledging an operator notification on `System → Health`. |
| `/api/tasks*`, `/api/config*` | `GET` | Temporarily aborted to drive `DisconnectedBanner` and verify automatic recovery when restored. |
| `/api/metrics*` | `GET` | Temporarily injected `report.failed` and a `500` response to verify partial and full report failure states and `Refresh` recovery. |
