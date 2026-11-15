# Deployment Operations Walkthrough — 2026-11-15

This walkthrough exercised Grain's observability and operator-control surfaces —
System / Fleet (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics /
Telemetry, Power), Settings → Upgrade, Settings → Caches, the rail's CI
workspace, the alerts bell, the global dispatch banner, and the standing
banners — in a real Playwright Chromium browser against real `grain demo`
servers (task 5892).

Following `PROMPT.md` ("Two UIs"), every step ran against the Angular UI at
`/next/` first wherever a `/next/` surface exists (`ui-ng/src/app/shell/nav.ts`
and `ui-ng/src/app/app.routes.ts`), and against the classic UI at `/` only
where `/next/` does not yet have that surface or control. Five bugs uncovered
during the `/next/` walkthrough were fixed in this branch with Vitest unit and
Playwright E2E regression coverage.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-5892-walk-through-system-health`.
- **Base commit tested:** `5a9bce62c` (`main` at the start of the run; build
  stamp `grain v3.9.0-dev.104+5a9bce6`). All five fixes (`F1`–`F5`) were
  re-verified against a freshly built `bin/grain` embedding the updated
  `pkg/ui/static-next` bundle.
- **Build:** `ui`, `ui-ng`, and `ui-hub` production bundles built into
  `pkg/ui/static`, `pkg/ui/static-next`, and `pkg/hub/server/static`, followed
  by `make grain` (`CGO_ENABLED=0 go build`).
- **Servers:** real `grain demo -addr 127.0.0.1:0 -open=false -access-code-file
  <file>` instances over isolated throwaway SQLite stores (`HOME` / `TMPDIR`),
  signed in through `POST /api/auth/access-code` as `alice@example.com`.
- **Browser:** headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`,
  `timezoneId: UTC`). Exercised at `1440×900`, `2048×1200`, `1024×860`,
  `640×800` (narrow), and `390×844` (phone, touch), in light and dark themes.
  No screenshots or browser artifacts are committed.
- **Model/tooling:** `/home/debian/.gemini-api-key` is absent in this sandbox,
  so `LOOK=1` was unavailable. Before and after screenshots were captured via
  `ui/e2e/screenshot.mjs` (`ui/screenshots/{before,after}/contact-sheet.png`),
  verified programmatically for PNG integrity and light/dark distinctness, and
  paired with DOM bounding-box and `axe-core` assertions across all four
  design-book breakpoints.

| Demo | Flags | Used for |
| --- | --- | --- |
| A | `-observability -staging` | `/next/fleet` (live agent + CI + persistent sandboxes, Top, Logs, Pauses & Controls, Caches, Restart), `/next/settings/caches`, `/next/telemetry`, `/next/ci` live observability run; classic System 8-tab sweep, Health counters/budget/overcommit, Sandboxes `"CI sandboxes (1)"` fold persistence, 10 named Caches flushes, `/ci` active sandboxes, Pipeline manual mode, Power 3 actions |
| B | `-github-sim -fake-ci -base-branch-ci -notifications -agent-paused -host-mode-install -reconcile=1s -scheduled-workflows` | `/next/` usage-limit dispatch banner & lift, `/next/ci` real `fake-ci` runs (pending/running, passed, failed, errored, cancelled), shared fault banner, retry & cancel, forgotten run ID; classic red-base-branch alert |
| B2 | as B plus `-host-executor=hostd-down -external-config-repo=https://github.com/acme/grain-config.git` | classic Health external config-repo read-only backing, `hostd` readiness note, host-mode install banner, bell alerts, disconnected banner (aborted polls and real `SIGSTOP`/`SIGCONT`) |
| C | `-unconfigured` | `/next/settings/caches` disabled caches, `/next/inbox`, `/next/telemetry` empty scope; classic phone bell setup steps (`2 setup steps left`) |
| D | `-upgrade-image=docker.io/grainai/grain -upgrade-allowed-branches=main,latest,v3.2.0` | classic `/settings/upgrade` loading spinner, `Now running 5a9bce6` fallback, `Will pull` image chooser, retracted/discarded banners, SQLite & PostgreSQL migration panels |
| E / E2 | `-disable-self-upgrade` with and without `-management-url=https://console.example.com/deployments/grain-prod` | `/next/settings/upgrade` externally managed notice (with and without `Manage instance` link) and controllable upgrade confirmation flows |

## Which UI each step ran against

| Area | `/next/` surface (`nav.ts` / `app.routes.ts`) | Ran against | Classic-only items (no `/next/` surface yet) |
| --- | --- | --- | --- |
| System navigation, tabs, history, legacy redirects | `/next/fleet` (`Sandboxes`, `Hosts`, `Pauses & Controls`, `Daemon Logs`), build stamp → `/next/settings/upgrade`, legacy `/next/system/*`, `/next/admin/logs`, `/next/sandboxes`, `/next/caches`, `/next/metrics` | `/next/` first, then classic `/system/<tab>` | Classic 8-tab strip (`Health`, `Power` host reboot/shutdown) and phone System section list |
| System → Health | Global `<g-dispatch-banner>`, `/next/fleet` `dispatchBlocked` banner, `/next/inbox` blocked/alert items | `/next/` where present; classic `/system/health` in full | Dedicated Health tab (GitHub API budget & idle-poll backoff, `pendingRestart`/`unrunnable` `#<field-id>` links, overcommit history, daemon health counters, `hostd` readiness note, host-mode install alert) |
| Sandboxes | `/next/fleet` (default `Sandboxes` tab: host CPU/vCPU/memory/disk strip, live sandboxes table, diagnostic probe, persistent sandboxes) | `/next/` first; classic `/system/sandboxes` for unmigrated widgets | Auto-overcommit controller adjustments card, `"CI sandboxes (N)"` folded summary row in the live table, per-sandbox CPU/memory sparklines |
| Caches | `/next/fleet?tab=controls` (`Proxy & Actions Caches` card) and `/next/settings/caches` | `/next/` first; classic `/system/caches` for per-entry flushes | Per-ecosystem (`apt`, `npm`, `pypi`, `go`, `docker`, `playwright`) and per-repository (`acme/widgets`, `acme/gadgets`) individual Flush buttons |
| Logs & Top | `/next/fleet?tab=logs` (`Daemon Logs`) and `/next/fleet` expandable `Host Process Snapshot (top)` | `/next/` | None |
| Pipeline & Power | `/next/fleet?tab=controls` (`Pipeline Dispatch Gate`, `Model Usage-Limit Pauses`, `Daemon Lifecycle` `Restart daemon`) | `/next/` first; classic `/system/pipeline` and `/system/power` | `Reboot host` and `Shut down host` buttons (Fleet exposes `Restart daemon` only) |
| Metrics / Telemetry | `/next/telemetry` (`24h`/`7d`/`30d` windows, repo & author narrowing, 4 SVG charts, Token Usage & Budgets editor, alphabetical SPACE rollup & workstream task links) | `/next/` first; classic `/` → `/system/metrics` | `90d` window in the picker (`/next/telemetry` clamps `90d` to `30d`) and dashboard throughput link |
| CI | `/next/ci` and `/next/ci/runs/:id` | `/next/` first; classic `/ci` for active sandboxes | Collapsible `"Active sandboxes (N)"` table at the top of `/ci` |
| Settings → Upgrade | `/next/settings/upgrade` | `/next/` first; classic `/settings/upgrade` for unmigrated cards | Searchable target autocomplete, rendered Markdown breaking-change callouts, retracted-release & discarded-RC warning banners, `Image (optional)` chooser (`release-assets` image source was removed in `#3850`), read-only check-mode link to General, offline SQLite/PostgreSQL migration instructions |
| Alerts bell & standing banners | `<g-dispatch-banner>` + `live-pill` in `/next/` shell, `/next/inbox` | `/next/` for dispatch/quota banners; classic for header bell & outage/install banners | Header alerts bell popover (`See all` → Health), disconnected banner (failed polls and silent/hung daemon), and `install-notice-banner` (`-host-mode-install`) |

## Findings & fixes landed in this branch

### F1 (fixed): Pausing/resuming the pipeline or lifting a quota pause in `/next/fleet` left `<g-dispatch-banner>` and `live-pill` stale

- **Where:** `ui-ng/src/app/fleet/fleet-page.component.ts`,
  `ui-ng/src/app/app.component.ts`, `ui-ng/src/app/services/access.service.ts`.
- **Reproduction:**
  1. Open `/next/fleet?tab=controls`, enter `"walkthrough 5892 maintenance"`,
     and click `Pause pipeline`. `FleetPageComponent` switches to `Paused`, but
     `global-dispatch-banner` does not appear and `live-pill` does not change.
  2. Navigate to `/next/inbox` (where `global-dispatch-banner` appears), click
     `Controls` back to `/next/fleet?tab=controls`, and click `Resume pipeline`.
     The card switches back to `Dispatching`, while `global-dispatch-banner`
     remains stuck at the top of the screen claiming
     `"Pipeline paused (manual Run now allowed)"`.
  3. On Demo B (`-agent-paused`), open `/next/fleet?tab=controls` and click
     `Lift pause` on `Gemini 2.5 Pro`. The pause row disappears from the card,
     while `global-dispatch-banner` stays visible claiming
     `"Model quota exceeded — Gemini 2.5 Pro development paused"`.
- **Cause:** `FleetPageComponent` updated only its local `pipelinePause` and
  `agentPause` signals without patching `AccessService.rawConfig()` or clearing
  cached `pausedUntil` entries in `AccessService.usage()`, and `AppComponent`
  read a private `usageStatus` signal instead of `AccessService.usage()`.
- **Fix:** `AppComponent.dispatchBanner` now reads `AccessService.usage()`
  directly; `AccessService.patchConfig` updates `rawConfig` in place; and
  `pausePipeline`, `resumePipeline`, and `liftAgentPause` immediately patch
  `AccessService` with the authoritative pause response from the server,
  invalidate conditional caches, and refresh `/api/usage` when lifting quota
  pauses.
- **Regression coverage:** unit test in `fleet-page.component.spec.ts` and
  Playwright E2E test in `ui-ng/e2e/next-deployment-operations.spec.js`.

### F2 (fixed): Deep-linking `/next/fleet?tab=logs` on a phone left the selected `Daemon Logs` tab scrolled off-screen

- **Where:** `ui-ng/src/app/fleet/fleet-page.component.ts`.
- **Reproduction:** at `390×844`, open `/next/fleet?tab=logs` (or
  `/next/system/logs` / `/next/admin/logs`). The tab strip is `358px` wide
  (`x = 16..374`, `scrollWidth = 521`, `scrollLeft = 0`), leaving the selected
  `Daemon Logs` tab completely off-screen at `x = 417..537`.
- **Fix:** an `afterRenderEffect` scrolls `#fleet-tab-btn-<active>` into view
  (`{ block: "nearest", inline: "nearest" }`) whenever `tab()` changes, matching
  `RepoDetailPageComponent` (`#1522` / `e9bd99879`).
- **Regression coverage:** `@phone` bounding-box Playwright E2E test in
  `ui-ng/e2e/next-deployment-operations.spec.js`.

### F3 (fixed): `/next/ci` dropped run selection from the URL when opened from `/next/ci`, re-ran every job on non-task `Re-run failed`, lacked per-run row accessible names, and polled 404 runs repeatedly

- **Where:** `ui-ng/src/app/app.routes.ts`, `ui-ng/src/app/ci/ci-page.component.ts`,
  `ui-ng/src/app/ci/ci.models.ts`, `ui-ng/src/app/ci/ci.store.ts`.
- **Reproduction:**
  1. Open `/next/ci` from the rail (`id()` is `undefined`) and click or press
     `Enter` on a run row. `openRun` gated navigation behind
     `if (this.id() && this.id() !== runId)`, so the URL stayed `/next/ci` and
     reloading snapped back to the newest run.
  2. Open a failed non-task Grain CI run (e.g. a push run on `main` where `lint`
     passed and `unit` failed) and click `Re-run failed`. `POST /api/ci/runs`
     sent `recheckFailed: true` with no `jobs` array and no `event`; because
     `pkg/ui/ci.go` (`handleStartCIRun`) reads `recheckFailed` only on
     `taskId`-bound runs and reads `req.Jobs` on repo/ref runs, the retry started
     every job in the workflow (`jobs: []`).
  3. Two runs on the same branch and commit (`main @ d663e09`) had identical
     accessible names on their `li[role="button"]` rows.
  4. Opening `/next/ci/runs/0000forgotten0000` requested
     `GET /api/ci/runs/0000forgotten0000` twice on load (and on every 5s poll)
     because `detail()` stayed `null` after the 404.
- **Fix:**
  - `/ci` and `/ci/runs/:id` now share one route matcher in `app.routes.ts` so
    navigating `/next/ci` ↔ `/next/ci/runs/:id` reuses the same
    `CIPageComponent` instance (preserving active filter chips and list scroll
    position), and `openRun` always navigates to `/ci/runs/<id>` (closing mobile
    detail when browser Back returns to `/ci`).
  - `rerunSelected` extracts `failedJobIds(run)` for `jobs` and preserves
    `run.event` on non-task runs (using `runRetryTaskId` so manual runs on a
    `grain/task-N` branch are retried by repo/ref rather than as task runs).
  - Each `.ci-run-row` carries a unique `aria-label` naming its verdict, title,
    repo, branch, short SHA, and run ID.
  - `CIStore` records `detailFailedId` on a failed detail read so `refresh()`
    does not re-request a forgotten run ID until a different run is selected or
    the ID appears in `runs()`.
- **Regression coverage:** unit test in `ci-page.component.spec.ts` and
  Playwright E2E test in `ui-ng/e2e/next-deployment-operations.spec.js`.

### F4 (fixed): `/next/settings/caches` left Flush buttons enabled when caches were disabled and flushed without confirmation

- **Where:** `ui-ng/src/app/settings/settings-instance.component.ts`.
- **Reproduction:** open `/next/settings/caches` on Demo C (`-unconfigured`) or
  a bare `grain demo`. `GET /api/package-cache` and `GET /api/actions-cache`
  return `{"enabled":false}`, yet `loadForSection` checked only
  `p?.available === false` (leaving the placeholder `"Shared package proxy
  cache"`), kept both `Flush proxy cache` and `Flush Actions cache` enabled, and
  sent `POST /api/package-cache/flush` / `POST /api/actions-cache/flush` on
  click with no `<g-confirm-dialog>` (unlike `/next/fleet` and `/next/ci`).
- **Fix:** `SettingsInstanceComponent` now checks `enabled !== false` (alongside
  `available !== false`) on both cache status endpoints, displays
  `"Proxy cache not enabled on daemon"` and
  `"Actions cache not enabled on daemon"`, disables each Flush button with an
  explanatory `gTip` when its cache is not running, and asks in
  `<g-confirm-dialog>` before flushing.
- **Regression coverage:** Playwright E2E test in
  `ui-ng/e2e/next-deployment-operations.spec.js` plus Escape-dismiss adversarial
  check.

### F5 (fixed): Classic Metrics `?f=repo:any:<owner/name>` links lost their repository scope on `/next/telemetry`

- **Where:** `ui-ng/src/app/telemetry/telemetry-page.component.ts`.
- **Reproduction:** classic `/metrics?repo=acme/widgets&window=30d` canonicalizes
  to `/system/metrics?f=repo:any:acme/widgets&window=30d`. Visiting
  `/next/system/metrics?f=repo:any:acme/widgets&window=30d` redirected to
  `/next/telemetry?f=repo:any:acme/widgets&window=30d` with `selectedRepo`
  empty (`telemetry-clear-scope` hidden), and `?window=90d` fell back to `7d`
  instead of clamping to `/next/telemetry`'s widest `30d` window.
- **Fix:** `TelemetryPageComponent.ngOnInit` parses `repo:(?:any:|is:)?<repo>`
  out of `f` when `repo` is absent and clamps `window=90d` to `30d`.
- **Regression coverage:** unit test in `telemetry-page.component.spec.ts` and
  Playwright E2E test in `ui-ng/e2e/next-deployment-operations.spec.js`.

## Walkthrough steps & outcomes

### 1. System / Fleet navigation, tabs, URLs, narrow & phone

- **`/next/` (`Demo A`):**
  - Clicking the footer build stamp (`grain v3.9.0-dev.104+5a9bce6`) from
    `/next/inbox` opened `/next/settings/upgrade`.
  - Clicking `Fleet` in the rail opened `/next/fleet`. All 4 tabs (`Sandboxes` →
    `/next/fleet`, `Hosts` → `/next/fleet?tab=hostds`, `Pauses & Controls` →
    `/next/fleet?tab=controls`, `Daemon Logs` → `/next/fleet?tab=logs`) linked
    `aria-controls` to `role="tabpanel"` and `aria-labelledby` back to the tab
    button, survived reload, stepped cleanly through browser Back/Forward, and
    responded to `ArrowRight`, `Home`, and `End`.
  - All 14 legacy paths (`/next/system`, `/next/system/{health,sandboxes,top,logs,pipeline,power,caches,upgrade,metrics}`,
    `/next/admin/logs`, `/next/sandboxes`, `/next/caches`, `/next/metrics`)
    landed on their target `/next/` route with query parameters preserved, and
    `?f=repo:any:acme/widgets&window=30d` now activates the `acme/widgets` scope
    on `/next/telemetry` (`F5`).
  - At `390×844` (phone, light and dark), the compact bar showed `Inbox`,
    `Tasks`, `Sessions`, and `More`, and deep-linking `/next/fleet?tab=logs` or
    `/next/system/logs` scrolled `Daemon Logs` into view (`scrollWidth = 390`,
    `F2`).
- **Classic `/` (`Demo A`):**
  - Header `System` button and `390×844` phone navigation drawer `System` row
    opened `/system`; footer build stamp and `/system/upgrade` opened
    `/settings/upgrade`.
  - All 8 tabs (`Health`, `Logs`, `Sandboxes`, `Caches`, `Top`, `Pipeline`,
    `Metrics`, `Power`) rode in `/system/<tab>` (`aria-controls="system-tabpanel"`),
    kept their tab across reload and Back/Forward, scrolled horizontally inside a
    `280px` tablist container at `640×800` (`scrollWidth = 730`), and redirected
    `/admin/logs` → `/system/logs`, `/sandboxes` → `/system/sandboxes`,
    `/caches` → `/system/caches`, and `/metrics?repo=acme/widgets&window=30d` →
    `/system/metrics?f=repo:any:acme/widgets&window=30d`.

### 2. Health (classic `/system/health` + `/next/` banners)

- **Classic `/system/health`:**
  - `Demo A`: rendered live `Overcommit: auto` (`CPU 2.75× · memory 1.25×`, PSI
    `CPU 8.4%, memory 1.9%`, dated adjustment `CPU 2.50× → 2.75×` at
    `10/24/2026, 9:20:00 AM` UTC), `GitHub API: 1,650 of 5,000 calls left this
    hour · resets in 38m 0s · spending 3,300/h. At that rate it runs out in
    30m 0s, before it resets`, `Idle-poll backoff`, and registered daemon health
    counters (`proxy:admitted`, `proxy:admitted_cached`, etc.).
  - With `pendingRestart: ["pollInterval", "githubHost"]` and `unrunnable:
    [{key: "sandboxBackend", stored: "bogus", running: "docker"}]` merged onto
    `GET /api/settings`, Health rendered both alerts with links; clicking `Poll
    interval` navigated to `/settings/engine#pollInterval` and scrolled
    `[data-setting-id="pollInterval"]` into view.
  - `Demo B2` (`-external-config-repo`, `-host-executor=hostd-down`,
    `-host-mode-install`): rendered the external config-repo read-only state
    (`PUT /api/repos/acme/widgets/ci-mode` refused with `409`), the unreachable
    host readiness note, and the `Unmigrated host-mode install` alert naming
    `sudo ./scripts/setup.sh`, `claude`, and `Migration docs`.

### 3. Sandboxes (`/next/fleet` + classic `/system/sandboxes`)

- **`/next/fleet` (`Demo A` `-observability -staging`):**
  - Host & Capacity strip rendered `Host CPU & Load` (`4 cores`, `15%`, `0.45 /
    0.55 / 0.60`), `vCPU Allocation` (`Within cap`, `4 / 4 vCPU`, `2 committed
    sandboxes`), `Host Memory` (`4.0 GB / 16.0 GB`), and `Primary Disk`.
  - With a live CI run (`POST /api/ci/runs`) and a live agent sandbox
    (`POST /api/e2e/sandboxes/other`) active simultaneously, the live table
    listed both `1-identity-check` (`Ready`, `agent`, `804 MB`, load `9.04 4.50
    2.25`) and `cbcf56c-0` (`Ready`, `ci`, `104 MB`, load `1.04 0.50 0.25`).
  - `Persistent & Staging Sandboxes` rendered `docs-preview` (`running`,
    `acme/docs`, `main`, `/staging/docs-preview`) and `grain-staging`
    (`restarting`, `bwsalmon/grain`, `candidate`, `restarts: 4`,
    `exited with status 1`) with `Start`, `Restart`, `Rebuild`, `Stop`, and
    `Logs` controls; without `-staging`, the empty state rendered cleanly.
- **Classic `/system/sandboxes` (`Demo A`):**
  - Verified `1-identity-check` (agent sandbox) sat above the folded
    `CI sandboxes (1)` summary row (`aria-expanded="false"` by default).
    Expanding it and reloading kept `aria-expanded="true"`; collapsing it and
    reloading kept `aria-expanded="false"`. CPU and memory trend sparklines,
    pool status, storage summary, and empty states on `Demo C` rendered cleanly.

### 4. Caches (`/next/fleet?tab=controls`, `/next/settings/caches`, classic `/system/caches`)

- **`/next/fleet?tab=controls` (`Demo A`):** showed all 6 proxy ecosystems (`apt:
  412 (3.00 GB)`, `npm: 128 (450.0 MB)`, `pypi: 85 (220.0 MB)`, `go: 64
  (180.0 MB)`, `docker: 12 (1.17 GB)`, `playwright: 3 (350.0 MB)`) and Actions
  cache (`20 entries`, `3.00 GB / 20.00 GB`). Dismissing `Flush package proxy
  cache?` left all `704` entries intact; confirming emptied it to `0` across
  reload, and the same held for `Flush GitHub Actions cache?` (`20` → `0`).
- **`/next/settings/caches` (`Demo A` & `Demo C`):** after `F4`, disabled caches
  disable both Flush buttons with `"Proxy cache not enabled on daemon"` and
  `"Actions cache not enabled on daemon"`, and enabled caches confirm via
  `<g-confirm-dialog>` (including Escape dismissal) before flushing.
- **Classic `/system/caches` (`Demo A`):** verified all 10 named Flush buttons
  (`Flush apt packages`, `Flush npm packages`, `Flush Python packages`,
  `Flush Go modules`, `Flush Docker Hub layers`, `Flush Playwright browsers`,
  `Flush the whole proxy cache`, `Flush the Actions cache of acme/widgets`,
  `Flush the Actions cache of acme/gadgets`, `Flush the whole Actions cache`)
  and `Refresh`. Flushing `npm` disabled `Flush npm packages` across reload, and
  flushing `acme/widgets` removed its per-repo row across reload.

### 5. Logs, Top, Pipeline, Power (`/next/fleet` + classic `/system/{pipeline,power}`)

- **Logs & Top (`/next/fleet` on `Demo A`):**
  - `Host top` toggle opened `Host Process Snapshot (top)` showing `systemd`,
    `grain-daemon`, and `node`, with `Refresh top`.
  - `Daemon Logs` (`?tab=logs`) tailed `300 / 300 lines`, narrowed to `200 / 200
    lines` on `200`, showed `(no matching log lines)` on a non-matching query,
    and refreshed cleanly on `Refresh`.
- **Pipeline & Power:**
  - `/next/fleet?tab=controls`: pausing with a reason switched the gate to
    `Paused`, immediately raised `<g-dispatch-banner>` (`Pipeline paused (manual
    Run now allowed) — ...`) and updated `live-pill` (`F1`), hid the `Controls`
    link while already on `?tab=controls` while showing `Telemetry` + `Controls`
    on `/next/inbox`, survived reload, and cleared `<g-dispatch-banner>`
    immediately on `Resume pipeline`.
  - `/next/fleet?tab=controls` `Restart daemon`: disabled when `restartEnabled:
    false`; on `Demo A` (`restartEnabled: true`), intercepting
    `POST /api/host/restart` verified `Cancel` sent 0 requests, `500` surfaced
    `Restart failed: walkthrough: restart refused` without leaving a `Restarting`
    pill, and `202` reported `Restart requested. Waiting for grain to come back…`.
  - Classic `/system/pipeline` & `/system/power` (`Demo A`): verified
    `window.confirm` dismiss/accept on `Pause pipeline` and `Resume pipeline`,
    manual mode notice (`Paused — only tasks you Run now will start.`), bell
    count (`Alerts: 2 active` → `3 active` → `2 active`), and intercepted
    `POST /api/host/{restart,reboot,shutdown}` across dismiss, `500`, and `202`
    for `Restart grain`, `Reboot host`, and `Shut down host`.

### 6. CI (`/next/ci` + classic `/ci`)

- **`/next/ci` (`Demo A` & `Demo B` with `acme/widgets` switched to `ciMode: grain`):**
  - Real observability run (`Observability` on `acme/widgets · main @ 0123456`)
    and real `fake-ci` runs exercised `Running` (`pending`), `Passed` (including
    cached jobs), `Failed · Code` (passing cached + failing + skipped jobs and
    step log output), `Errored · Infra` (with errored callout), and `Cancelled`.
  - Shared infrastructure fault banner rendered `1 active fault cluster across
    branches` (`acme/widgets · CI / e2e`), expanding to cluster cards and
    assigned repair task links (`Assigned fix #<id>`).
  - After `F3`, every row has a unique `aria-label` naming its own run,
    selecting a row from `/next/ci` navigates to `/next/ci/runs/<id>` while
    keeping filter chips intact across `/next/ci` ↔ `/next/ci/runs/:id` and
    browser Back/Forward, `Re-run all` and `Re-run failed` follow to the newly
    started run (with non-task `Re-run failed` sending `jobs` for only the
    failed jobs and preserving `event`), `Cancel run` confirms (`Cancel` keeps
    the run active; `Cancel run` marks it `Cancelled` across reload), and
    `/next/ci/runs/0000forgotten0000` shows
    `no CI run "0000forgotten0000" -- a run is remembered for a while and then forgotten`
    with a single `404` request.
- **Classic `/ci` (`Demo A`):** verified `Active sandboxes (1)` starts collapsed
  (`aria-expanded="false"`) and expands (`aria-expanded="true"`) to show the
  in-flight CI sandbox row.

### 7. Telemetry / Metrics (`/next/telemetry` + classic `/system/metrics`)

- **`/next/telemetry` (`Demo A` & `Demo C`):**
  - Rendered all 5 KPI cards, `Throughput & Merges` SVG chart (keyboard
    `ArrowRight`/`ArrowLeft` slice focus), `Run Outcomes`, `Cycle-Time Stages`,
    `Agent & Template Breakdown`, `Top Tools & CI Queue Causes`, `Token Usage,
    Cost & Concurrency Budgets` (plus `Configure budgets` editor), and
    `Alphabetical · Non-ranked` SPACE delivery rollups (`Last week`, `This week`,
    `This month`), with workstream direction link `#14` navigating into
    `/next/task/14`.
  - Time window (`?window=24h`) and repository narrowing
    (`?window=7d&repo=acme%2Fwidgets`, plus classic
    `?f=repo:any:acme/widgets&window=30d` after `F5`) persisted across reload and
    cleared via `Clear scope`. Non-JSON `502` and JSON `500` responses rendered
    clean error banners (`The server answered 502...`).
- **Classic `/system/metrics` (`Demo D`):** verified `90 days` window, release
  notes panel, and `500` error banner (`metrics unreadable`).

### 8. Settings → Upgrade (`/next/settings/upgrade` + classic `/settings/upgrade`)

- **`/next/settings/upgrade`:**
  - `Demo A` (no upgrader): `current-daemon-version` showed
    `v3.9.0-dev.104+5a9bce6`, rendered the `Not available` banner, and showed
    `Database: sqlite` with zero 404 requests.
  - `Demo E` / `Demo E2` (`-disable-self-upgrade`): rendered
    `In-VM self-upgrade is disabled on this deployment; upgrades and instance lifecycle are managed externally`,
    with `Manage instance` (`https://console.example.com/deployments/grain-prod`)
    present on `Demo E` and absent on `Demo E2`.
  - Controllable image-mode stub (`running: v3.8.0`, targets `v3.9.0`,
    `v3.9.0-rc.1`, `latest`, release notes with `**Breaking:**` item,
    `POST /api/upgrade` intercepted): verified target selection, release-notes
    preview, `Start upgrade` confirmation (`Cancel` sent 0 requests; drain-on
    `Schedule upgrade` raised `scheduled-upgrade-banner` + `Cancel scheduled
    upgrade`; drain-off destructive `Upgrade now` raised `active-upgrade-banner`
    + `Cancel upgrade`).
- **Classic `/settings/upgrade` (`Demo D`):**
  - Verified loading `progressbar` while `GET /api/upgrade` was held, `Now
    running 5a9bce6.` fallback from `/api/config`, `Will pull
    docker.io/grainai/grain:main` and `Image (optional)` alignment (note:
    `pkg/upgrade`'s `release-assets` image source was removed in `#3850`, so
    image deployments use container registries exclusively), retracted-release
    warning (`Release v3.5.0 was retracted... Upgrade to v3.6.0` populating
    `Branch`), discarded-RC warning (`Release candidate v3.5.0-rc.1 was
    discarded...`), and read-only `Database Backend` offline migration panels for
    both `SQLite` and `PostgreSQL`.

### 9. Alerts bell & standing banners (`/next/` dispatch banner + classic bell & banners)

- **`/next/` (`Demo A` & `Demo B`):** exercised `<g-dispatch-banner>` and
  `live-pill` for pipeline pause/resume (`F1`) and model usage-limit pauses
  (`Gemini 2.5 Pro`, timezone-formatted reset time + countdown, clearing
  immediately on `Lift pause`).
- **Classic header & phone-bar alerts bell (`Demo B`, `Demo B2`, `Demo C`, `Demo R`):**
  - Exercised `Reconcile loop stopped` (`Power` → `/system/power`), `Settings
    are not being exported` (`Config repo` → `/settings/config-repo`), `No
    isolation: runs execute on this host` (`hostSandboxes: true`), `Usage limit
    reached (claude-opus-4-6)` with future non-today date (`until 11/18/2026,
    2:30:00 PM`, `Resume now`, `Open connection`), `Paused — only tasks you Run
    now will start` (`Resume now`), `2 setup steps left` on `-unconfigured`
    (`390×844` phone bar bell), `acme/widgets main is red` (clearing when
    `POST /api/e2e/base-branch/green` turned `main` green), and `acme/widgets
    main is red — repair #1` (`Open #1` → `/tasks/1`). `See all` opened
    `/system` (Health).
- **Classic standing banners (`Demo B2` & `final-checks`):**
  - `disconnected-banner` raised both on failed polls (`This deployment cannot
    be reached: the connection to it failed...`) and when `demoB` was frozen
    with `SIGSTOP` (`...it accepted the connection but sent no answer within 10
    seconds...`), sat at `y = 0` above the rail and `System` heading without
    covering them, and cleared automatically without reload on unroute /
    `SIGCONT`.
  - `install-notice-banner` (`-host-mode-install`) sat at `y = 0` (`1440×68`)
    above the rail and `System` heading without covering them, named `claude` and
    `Migration docs`, and cleared without reload once `installNotice` cleared.

## Stubbed & intercepted boundaries

Every boundary below was intercepted via Playwright `page.route` (or OS signal on
the throwaway demo process) while keeping the real `grain demo` server, built
frontend, and browser gestures in the loop:

| Boundary | Why | Stub / action |
| --- | --- | --- |
| `POST /api/host/{restart,reboot,shutdown}` | Never restart, reboot, or shut down the host | Fulfilled with `202 {"ok":true}` or `500 {"error":"walkthrough: <action> refused"}` |
| `GET/POST /api/upgrade`, `/api/upgrade/targets`, `/api/upgrade/release-notes`, `DELETE /api/upgrade/scheduled` | Never trigger a real self-upgrade or hit Docker Hub from sandbox; exercise retracted/discarded banners & spinner | Held briefly for spinner; fulfilled with target/notes/retracted/discarded fixtures; `POST` fulfilled with `202` or `409` |
| `GET /api/database` (classic PostgreSQL check) | Default demo uses embedded SQLite | Fulfilled with `{"driver":"postgres","configuredBy":"config","dsnRedacted":"postgres://grain:***@127.0.0.1:5432/grain"}` |
| `GET /api/settings` (classic Health check) | Exercise `pendingRestart` and `unrunnable` deep-link focus | Real response overlaid with `pendingRestart: ["pollInterval", "githubHost"]` and `unrunnable` |
| `GET /api/metrics` (error state check) | Exercise `500` JSON and `502` HTML error banners | Fulfilled with `500` JSON / `502` HTML |
| `GET /api/config` & `GET /api/tasks` (classic bell & banner edge cases) | Exercise `reconcilerDown`, `stateRepoSyncFailure`, `hostSandboxes`, multi-day `agentPause`, `red-base-repair` task link, and `connectionrefused` outage | Overlaid on real responses or aborted; silent-daemon outage also exercised with real `SIGSTOP` / `SIGCONT` on the throwaway `grain demo` child process |
| E2E spec stubs (`next-deployment-operations.spec.js`) | Worker-scoped `grain demo` runs without `-observability` or `-upgrade-image` | Per-test `page.route` stubs for `restartEnabled`, quota pause, cache flush, and CI runs |

## Review & verification summary

- **Design review (`390×844`, `1024×860`, `1440×900`, `2048×1200`, light & dark, `reducedMotion: "reduce"`):**
  40/40 page×viewport×theme checks passed with `0` horizontal overflow, `0`
  serious/critical `axe-core` violations, `0` page errors, `0px` vertical/height
  delta between the paired `Flush proxy cache` and `Flush Actions cache` buttons,
  and verified light/dark distinctness across all 20 screenshots plus before/after
  contact sheets.
- **Functional & adversarial review:** verified `Escape` cancels the Settings →
  Caches flush confirmation with `0` POSTs, navigating `/next/ci` →
  `/next/ci/runs/:id` preserves active filter chips (`aria-pressed="true"`) and
  browser Back returns to `/next/ci`, malformed `?f=;;;repo:any:&window=90d`
  queries clamp cleanly without errors, and pipeline pause cleanup in E2E runs
  inside `finally`.
- **Automated suites run:**
  - `cd ui-ng && npm run format:check && npm run lint && npm test` (49 files,
    460 tests passed).
  - `npx playwright test e2e/next-deployment-operations.spec.js e2e/next-full-system.spec.js`
    (17 passed across `desktop` and `phone` projects).
  - Classic `ui` Vitest & Playwright deployment-operations regression checks and
    Go hygiene / Angular route sync tests (`TestCoreSaysNoGoogleTokens`,
    `TestAngularRouteTableInSync`).
