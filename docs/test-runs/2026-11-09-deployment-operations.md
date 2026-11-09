# Deployment Operations Walkthrough — 2026-11-09

## Environment & Build Tested

- **Repository:** `ai-on-gke/grain`
- **Commit tested:** `8ea343b0` (`v3.2.0-dev+8ea343b0`, build stamp `v3.2.0-dev+8ea343b0 · 8ea343b · 2026-11-09 04:38Z`)
- **Date:** `2026-11-09`
- **Browser & viewports:** Real headless Playwright Chromium against a production frontend bundle (`ui/dist` built with `npm run build` and embedded into `cmd/grain`) over throwaway SQLite stores (`grain demo -open=false`):
  - Desktop (`1440×900`, light mode)
  - Narrow desktop (`640×800`, light mode — horizontal tab bar scrolling)
  - Phone (`390×844`, dark & light modes — drawer navigation, section list, standing banners, and bell popover)
- **Demo server topologies exercised:**
  1. **Demo A (`-observability -staging`):** Seeded tasks, daemon logs, sandbox pool & PSI pressure, auto overcommit controller, persistent sandboxes (`docs-preview`, `grain-staging`), proxy & Actions caches, metrics & embedded release notes, pipeline controls, and power controls.
  2. **Demo B (`-github-sim -fake-ci -base-branch-ci -scheduled-workflows -notifications -reconcile=1s -agent-paused -host-mode-install -e2e`):** Host-mode install notice banner, unreachable `grain hostd` note, GitHub API budget & idle-poll backoff panel, usage-limit connection pause alert, red-base-branch repair alert (`/api/e2e/base-branch/{red,green}`), real fake-CI runs, shared CI faults, multi-job retries, and seeded operator notifications.
  3. **Demo B2 (`-observability` + loopback `grain hostd` stub):** Missing `grain hostd` CLI allowlist readiness note, config-repo reset notice, `pendingRestart` and `unrunnable` settings `#<field-id>` focus links.
  4. **Demo C (`-unconfigured`):** Empty sandbox state, 3/2/1-step setup alerts, all standing alerts in the bell and Health, `disconnected-banner` under both network failure (`ModalStatusMirror` inside an open `Dialog`) and a real `SIGSTOP`-frozen daemon (`10s` poll timeout), plus recovery on `SIGCONT`.
  5. **Demo D (`-upgrade-image=127.0.0.1:4355/x/grain -upgrade-allowed-branches=main,candidate,latest,v3.2.0` + loopback OCI v2 registry):** `Settings → Upgrade` version display, `DatabaseBackend`, read-only upgrade check link to `Settings → General`, branch picker, image chooser, disallowed-branch refusal, in-flight runs confirmation dialog, and safely intercepted `POST /api/upgrade`.
  6. **Demo E (`-disable-self-upgrade -management-url=https://console.example.com/deployments/grain-prod -external-config-repo=https://github.com/acme/grain-config.git -observability`):** Externally managed upgrade notice and external config-repo read-only backing state.

---

## Summary of Findings

- **Bugs found:** **0 product defects** across all observability, CI, upgrade, alert-bell, and standing-banner workflows.
- **Browser console & network audit:** **0 uncaught `pageerror` exceptions** across every desktop, narrow, and phone session. The only `4xx`/`5xx` responses recorded were deliberate walkthrough boundary probes (`400` on disallowed upgrade branch `v3.1.0`, `403` capability check on unlisted repo `acme/unlisted` when starting a CI run, `404` on forgotten CI run `/ci/deadbeefdeadbeef`, and intentional `500` error-recovery injections on `/api/logs`, `/api/top`, `/api/caches`, `/api/sandboxes`, `/api/metrics`, `/api/host/*`, and `/api/upgrade`).

---

## Walkthrough Steps & Outcomes

### 1. System Navigation, Tabs, Responsive Layout & Legacy Redirects

1. **Build stamp & `/system/upgrade` redirect:**
   - Clicked `[data-testid="sidebar-build"]` (`v3.2.0-dev+8ea343b0 · 8ea343b · 2026-11-09 04:38Z`) at the foot of the rail; landed on `/settings/upgrade` with `[data-testid="upgrade-section"]` visible.
   - Navigated to `/system/upgrade`; confirmed client redirect to `/settings/upgrade`.
2. **Header System button & all 8 tabs:**
   - Clicked `[data-testid="header-system-button"]` in the page header; landed on `/system` (`[data-testid="system-view"][data-tab="health"]`).
   - Traversed every tab via `#system-tab-<id>`:
     - `Health` (`/system`)
     - `Logs` (`/system/logs`)
     - `Sandboxes` (`/system/sandboxes`)
     - `Caches` (`/system/caches`)
     - `Top` (`/system/top`)
     - `Pipeline` (`/system/pipeline`)
     - `Metrics` (`/system/metrics`)
     - `Power` (`/system/power`)
   - Reloaded on `/system/power` and exercised browser `Back` (`/system/metrics`) and `Forward` (`/system/power`); URL and selected tab stayed synchronized throughout.
3. **Legacy URL redirects:**
   - `/admin/logs` → `/system/logs`
   - `/sandboxes` → `/system/sandboxes`
   - `/caches` → `/system/caches`
   - `/metrics?window=24h&repo=acme%2Fwidgets&q=flaky` → `/system/metrics?q=flaky&f=repo:any:acme/widgets&window=24h` (query preserved and migrated to `f=repo:any:...`).
4. **Narrow desktop (`640×800`) & phone (`390×844` dark mode):**
   - At `640×800`, `.MuiTabs-scroller` scrolled horizontally inside the tab strip (`scrollWidth: 730`, `clientWidth: 280`, `scrollLeft: 450` when focusing `#system-tab-power`) with zero document overflow (`documentElement.scrollWidth === 640`).
   - At `390×844` dark mode, opened the navigation drawer (`Open navigation`), clicked `[data-testid="drawer-system-item"]` at the foot of the drawer, landed on `[data-testid="system-section-list"]`, opened all 8 `[data-testid="system-section-<id>"]` rows, verified `documentElement.scrollWidth === 390` on every section, and returned to `/system` via `All of System`.

### 2. System → Health

1. **Standing alerts & bell parity:** Verified `[data-testid="system-health"]` lists the exact alerts counted on `[data-testid="alerts-button"]`, each with working action buttons (`Power` → `/system/power`, `Config repo` → `/settings/config-repo`, `Open connection` → `/settings/models`, `Finish setup` → `/settings/git` or `/repos`, `Open #1` → `/tasks/1`, `Resume now`, and `Acknowledge` on seeded operator notifications).
2. **Config-repo backing state (`[data-testid="health-config-repo"]`):**
   - Verified reset/backing warning (`stateRepoResetNotice`) with its `Config repo` action navigating to `/settings/config-repo`.
   - Verified external read-only config-repo state (`https://github.com/acme/grain-config.git`) in Demo E on `/settings/config-repo`, and both internal export failure (`Settings are not being exported`) and external pull failure (`Configuration is not being pulled`) alerts.
3. **Settings pending restart (`[data-testid="health-pending-restart"]`) & unrunnable (`[data-testid="health-unrunnable"]`):**
   - Clicked `Power` on `health-pending-restart` → `/system/power`.
   - Clicked `Max worker agents` (`/settings/engine#maxWorkers`) → navigated to `/settings/engine#maxWorkers` and focused `<input name="maxWorkers">` inside `[data-setting-id="maxWorkers"]`.
   - Clicked `Sandbox vCPUs` (`/settings/sandboxes#sandboxCpus`) → navigated to `/settings/sandboxes#sandboxCpus` and focused `<input name="sandboxCpus">` inside `[data-setting-id="sandboxCpus"]`.
4. **GitHub API budget & idle-poll backoff (`GitHubBudgetPanel`):** Confirmed core & GraphQL rate-limit remaining/ceiling figures, reset time, and per-subsystem call/backoff breakdown under Demo B (`-github-sim`).
5. **`grain hostd` readiness notes:**
   - Unreachable `hostd` (`Demo B`): rendered `[data-testid="hostd-health-down"]` (`The host agent is not reachable, so no run: host server can start...`).
   - Reachable `hostd` missing allowlisted CLIs (`Demo B2` loopback `hostd` offering `["git"]` while settings referenced `missing-cli-tool`): rendered `[data-testid="hostd-health-missing"]` naming the missing entry and `hostd.yaml`.
6. **Host-mode install notice:** Verified `[data-testid="health-install-notice"]` on `/system` and `[data-testid="upgrade-install-notice"]` on `/settings/upgrade` alongside the top-of-window banner under `-host-mode-install`.

### 3. System → Sandboxes

1. **Host commitment, PSI pressure, overcommit & pool:** Under Demo A (`-observability -staging`), verified host CPU/memory/disk commitment bars, Linux PSI 10s/60s stalls, `Overcommit: auto` live ceiling and PSI-driven step-down adjustments, warm pool fill/target, sandbox lifetime/storage metrics, and `23` resource trend sparklines (`svg`).
2. **Agent & standing sandboxes above folded CI sandboxes:**
   - Started a live CI run (`c1-*`) and seeded standing/agent sandboxes via `POST /api/e2e/sandboxes/other`.
   - Confirmed `1-identity-check` (agent sandbox) appears directly in the live table above `[data-testid="ci-sandboxes-fold"]` (`CI sandboxes (1)`), with `aria-expanded="false"` by default (`0` `.ci-sandbox-row` rows visible).
   - Clicked `CI sandboxes (1)` → `aria-expanded="true"` (`1` `.ci-sandbox-row` visible); reloaded `/system/sandboxes` → remained expanded (`grain.sandboxes.ciOpen = "1"` in `localStorage`); collapsed and reloaded → remained collapsed.
3. **Persistent sandboxes & empty/error states:**
   - Verified `[data-testid="persistent-sandboxes-table"]` listing `docs-preview` and `grain-staging` with running commit, last refresh timestamp, health status, and inline `Logs` disclosure (`[data-testid="persistent-sandbox-logs-grain-staging"]`).
   - Verified empty state under Demo C (`-unconfigured`): `Not available: this deployment has no sandbox pool or host stats configured.` and zero `[data-testid="persistent-sandboxes-table"]`.
   - Verified long-table containment with `28` live rows (`documentElement.scrollWidth === 1440`, table container `scrollWidth: 1168` inside `clientWidth: 1160`), and `500` error banner + `Refresh` recovery.

### 4. System → Caches & CI Job Cache

1. **Proxy & Actions caches (`/system/caches`):**
   - Inspected proxy subsystem entries/bytes against ceiling (`npm`, `gomod`, `pip`, `playwright`, etc.) and Actions cache usage (`acme/widgets`, `acme/gadgets`) against its `20.0 GiB` ceiling.
   - Clicked `Refresh` to re-read live stats.
   - Exercised `Flush Playwright browsers` (zeroed subsystem and disabled its button), `Flush the whole proxy cache` (emptied proxy cache and disabled button), `Flush the Actions cache of acme/gadgets` (removed `acme/gadgets` row while preserving `acme/widgets`), and `Flush the whole Actions cache` (`empty (0 B of 20.0 GiB)`), confirming persistence across reload. *(Note: proxy and Actions cache flushes are pure throwaway cache evictions and execute without a modal confirmation by design, whereas CI job-result cache clearing prompts `window.confirm` because cached pass verdicts gate merges.)*
   - Verified `500` error alert on `/api/caches` and clean recovery on `Refresh`.
2. **CI job cache confirmations:**
   - On `/ci`, clicked `Clear cache`, dismissed `window.confirm` (cache preserved), then accepted `window.confirm` → `Job cache: 0 entries · 0 B of 256.0 MiB`.
   - On `/system/sandboxes`, after repopulating a cached job entry, clicked `Empty the job cache`, dismissed `window.confirm` (entry preserved), then accepted `window.confirm` → `no job result is cached` and button disabled.

### 5. CI (`/ci` from the Rail)

1. **Rail entry & active sandboxes fold:** Enabled `Show CI` in `Customize sidebar`, opened `/ci` from the rail, and verified `Active sandboxes (1)` (`[data-testid="ci-sandboxes-toggle"]`) defaults to expanded (`aria-expanded="true"`), collapses to summary on click, and persists its collapsed/expanded state across reload (`grain.ci.sandboxesCollapsed`).
2. **Direct links, reload, task links & forgotten run ID:**
   - Opened `/ci/<run-id>` from a table row, reloaded into the open run overlay (`aria-label="CI run <id>"`), expanded job step log output, and closed back to `/ci`.
   - Clicked the task link (`#1`) on a `/ci` run row → navigated to `/tasks/1`.
   - Navigated directly to `/ci/deadbeefdeadbeef` and reloaded → displayed `no CI run "deadbeefdeadbeef" -- a run is remembered for a while and then forgotten` cleanly without crashing.
   - Filtered runs on `/ci` (`widgets` → matching runs; `no-such-repo-999` → `No runs match.`).
3. **Run/job/step states & shared CI faults:**
   - Verified `cache` (`Cache hit` chip on repeated identical commit), `pending`/`queued`, `running` (live step output), `success`, `failure`, `errored` (`sandbox guest image pull timed out`), and `skipped` states.
   - Verified `[data-testid="ci-shared-faults-section"]` showing `[data-testid="ci-fault-owner"]` (`Assigned to task #1 (main)` → `/tasks/1`), collapse persistence across reload, and `[data-testid="ci-shared-faults-new"]` (`1 new`) chip appearing without auto-expanding when a second fault arrived while collapsed.
   - Verified capability guard when manually starting a run on an unlisted repo (`acme/unlisted`): clear refusal explaining `Use Grain CI` on `Settings, Repos`.
4. **Cancel & Retry:**
   - On an open running CI run, clicked `Cancel run`, dismissed the dialog (run stayed active), then accepted with reason `"walkthrough cancel check"` → run concluded `cancelled`.
   - On a concluded multi-job failed task run, clicked `Retry failed jobs` (`POST /api/ci/runs` with `{"taskId":"1","recheck":true,"recheckFailed":true}`) → navigated automatically to `/ci/run-retried-failed-only`; clicked `Retry` (`{"taskId":"1"}`) → navigated to `/ci/run-retried-all-jobs`; and on a manual multi-job failed run, clicked `Retry failed jobs` (`{"repo":"acme/widgets","ref":"main","commit":"...","event":"workflow_dispatch","jobs":["lint"],"recheck":true}`) → navigated to `/ci/run-retried-manual-jobs`.

### 6. System → Logs, Top, Pipeline & Power

1. **Logs (`/system/logs`):** Verified `daemon` log tail (`300` lines pinned to bottom with `slackFromBottom: 0`, keyboard focusable `pre.logs-view[tabindex="0"]`), filtered `"merge-queue"` (`50` matching lines), filtered `"zzznomatch999"` (`(no matching log lines)`), cleared filter (re-pinned to bottom), clicked `Refresh`, and verified both `/api/logs` list load error (reopen tab recovery) and `/api/logs/daemon` read error (`Refresh` recovery).
2. **Top (`/system/top`):** Verified process snapshot (`PID`, `COMMAND`), `pre.top-output[tabindex="0"]`, `Auto-refresh` switch toggle, manual `Refresh`, and `500` error warning + `Refresh` recovery.
3. **Pipeline (`/system/pipeline`):**
   - Opened `Contact running agents (2)` dialog and dismissed with `Escape`.
   - Clicked `Pause pipeline` → dismissed confirmation dialog (not paused), then reopened, entered reason `"walkthrough maintenance check"`, enabled manual mode (`Allow manual Run now`), and confirmed.
   - Verified paused alert with manual-mode notice and operator attribution in both `[data-testid="system-pipeline"]` and the header alerts bell (`Alerts: 3 active`, `"See all"` → `/system`).
   - Clicked `Resume pipeline` → dismissed confirmation, then confirmed → pipeline resumed and pause alert cleared (`Alerts: 2 active`).
4. **Power (`/system/power`, `POST /api/host/*` safely intercepted):**
   - Exercised `Restart grain`, `Reboot host`, and `Shut down host`: cancelling each confirmation dialog sent `0` `POST` requests; confirming with `202 Accepted` rendered the exact status banners (`Restart requested...`, `Host reboot requested...`, `Host shutdown requested...`); confirming with `500` JSON rendered the server error alert and cleared the optimistic status banner.

### 7. System → Metrics & Dashboard Throughput Panel

1. **Dashboard throughput panel entry:** Created a saved `panels` board (`POST /api/boards`) containing a `metrics` panel (`Widget throughput`, `f=repo:any:acme/widgets`, `7d`), verified rendered throughput sparklines on the board, and clicked `.panel-open` → landed on `/system/metrics?f=repo:any:acme/widgets&f=created:within:7` showing `Measured over the tasks in acme/widgets.`.
2. **Sections, windows, release notes & URL narrowing:**
   - Inspected all 5 sections (`Throughput`, `Latency`, `Capacity`, `Backlog`, `GitHub CI`) and switched `Window` between `24h` and `30d`, confirming `?window=24h` / `?window=30d` persisted across reload.
   - Switched `Release notes` between `Weekly` and `Versions` (`16` embedded release tags) and expanded a version chip to verify `#release-version-notes` markdown (`1,718` characters).
   - Navigated to `/system/metrics?q=flaky&f=state:any:open&f=repo:any:acme/widgets` and verified `[data-testid="metrics-narrowing-ignored"]` naming the ignored `q` and `state` clauses.
   - Verified empty window (`f=repo:any:nobody/nonexistent`), delayed loading spinner, partial section failure (`report.failed` → `Not available in this report: Prompts (ref-walkthrough-partial).`), `500` error + `Refresh` recovery, long unbroken repository/model names (`documentElement.scrollWidth === 1440`), and clicking a task link into `/tasks/14`.

### 8. Settings → Upgrade (`/settings/upgrade`)

1. **Version display, `DatabaseBackend` & Updates link (Demo D):**
   - Opened via rail build stamp and `/system/upgrade` redirect.
   - Verified `Now running` version display, `.database-backend-current` (`This deployment runs on SQLite.`), offline daemon-stopped migration warning, and `setup.sh` / `grain db check` migration commands.
   - Clicked `[data-testid="upgrade-check-settings-link"]` → landed on `/settings/general` with focus inside `#updates` (`[data-setting-id="upgradeCheckMode"]`).
2. **Branch picker, image chooser & confirmation:**
   - Opened `Branch` autocomplete against the loopback OCI v2 registry (`latest`, `candidate`, `main`, `v3.2.0` enabled; disallowed tags marked `not allowed on this deployment`).
   - Verified `.upgrade-will-ref` updates dynamically for default repo (`127.0.0.1:4355/x/grain:v3.2.0`), custom repo (`127.0.0.1:4355/mirror/grain:v3.2.0`), explicit tag (`127.0.0.1:4355/mirror/grain:custom-build-42`), explicit digest (`@sha256:aaa...`), and clickable `imageRepos` chip (`127.0.0.1:4355/canary/grain`).
   - Toggled `Wait until this deployment is idle` → disabled `Image (optional)` input and switched submit button to `Schedule upgrade`.
   - Submitted disallowed `v3.1.0` against the real server → received `400` naming `-upgrade-allowed-branches` with zero image pull.
   - With `inFlightRuns: 2` and intercepted `POST /api/upgrade`, verified `Confirm upgrade` dialog (`2 runs are in flight. Upgrading restarts this deployment. Upgrade anyway?`): `Cancel` sent `0` requests; `Upgrade` sent `{"branch":"v3.2.0","image":"127.0.0.1:4355/canary/grain","afterTasksFinish":false}` to the safe stub.
3. **Externally managed mode (Demo E):** Verified `[data-testid="upgrade-externally-managed"]`, `[data-testid="upgrade-management-link"]` (`href="https://console.example.com/deployments/grain-prod"`, `target="_blank"`), and hidden upgrade form / check link.

### 9. Alerts Bell & Standing Banners

1. **Alerts bell (`1440×900` & `390×844`):**
   - Verified `reconciler-down` (`Reconcile loop stopped` → `Power`), `store-switched`, `state-sync-failing` (internal export & external pull variants → `Config repo`), `agent-pause` (with resume wall-clock time, `Open connection` → `/settings/models`, and `Resume now`), `pipeline-pause` (manual mode & scheduled-upgrade pause), `setup` (`3`, `2`, and `1` setup steps left → `/settings/git` and `/repos`), `host-sandboxes` (`No isolation: runs execute on this host`), `red-base-branch` repair (`POST /api/e2e/base-branch/red` → `main is red — repair #1`, `Open #1` → `/tasks/1`, `Submit`, cleared via `POST /api/e2e/base-branch/green`), and seeded operator notifications (`Acknowledge`).
   - Confirmed `"See all"` opens `/system` (`Health`), and clearing/acknowledging all causes returns the bell to `aria-label="Alerts: none"`.
2. **Standing banners (the only two top-of-window banners):**
   - **Host-mode install notice (`[data-testid="install-notice-banner"]`):** Verified above rail and header without overlap (`bannerBottom: 68 === railTop: 68 === headerTop: 68` on `1440×900`; `bannerBottom: 128 === headerTop: 128`, `scrollWidth === 390` on `390×844`), and confirmed it disappears when `installNotice` clears.
   - **Disconnected banner (`[data-testid="disconnected-banner"]`):**
     - *Network failure + open Dialog:* Aborted `/api/**` while `Customize sidebar` dialog was open; confirmed `[data-testid="disconnected-banner-mirror"]` appeared inside the modal alongside `[data-testid="disconnected-banner"]` above rail and header (`bannerBottom: 48 === railTop: 48 === headerTop: 48` on `1440×900`; `bannerBottom: 108 === headerTop: 108`, `scrollWidth === 390` on `390×844`), and cleared automatically once `/api/**` resumed.
     - *Silent/frozen daemon (`SIGSTOP`):* Sent `SIGSTOP` to the live `grain demo` process; after two `10s`-timed-out polls, `[data-testid="disconnected-banner"]` appeared with `"This deployment cannot be reached: it accepted the connection but sent no answer within 10 seconds..."`, and cleared automatically within seconds of `SIGCONT`.

---

## Stubbed & Intercepted Boundaries

| Boundary | Mechanism | Reason |
| --- | --- | --- |
| `POST /api/host/{restart,reboot,shutdown}` | Playwright `page.route` (`202` & `500` JSON) | Never restart, reboot, or shut down the sandbox/host VM while exercising Power confirmations and status/error states. |
| `POST /api/upgrade` (and `GET /api/upgrade` `inFlightRuns` / `imageRepos` augmentation) | Playwright `page.route` (`500` JSON on `POST`) | Never self-upgrade or replace the running container/binary; exercise the in-flight confirmation dialog and payload safely. |
| OCI v2 image registry (`GET /v2/`, `GET /v2/x/grain/tags/list`) | Loopback Node HTTP server on `127.0.0.1:4355` | Sandbox has no external container registry credentials; lets the real Go daemon resolve `-upgrade-image` and `-upgrade-allowed-branches`. |
| `grain hostd` (`GET /v1/health`) | Loopback Node HTTP server (`{"ok":true,"offered":["git"]}`) | Demo VM runs no host agent; exercises the real Go daemon's missing-allowlist readiness check alongside the unreachable-`hostd` state. |
| Demo E2E state drivers (`/api/e2e/sandboxes/other`, `/api/e2e/ci/finish`, `/api/e2e/base-branch/{red,green}`) | Built-in `grain demo -e2e` endpoints | Deterministically drives standing/agent sandboxes, fake-CI run completion, and red/green base-branch transitions inside the real Go server. |
| Transient failure & edge-state probes (`GET /api/{logs,top,caches,sandboxes,metrics,config,ci/runs}`) | Scoped Playwright `page.route` (immediately unrouted after each check) | Exercises `500` error recovery, partial `report.failed` metrics, long sandbox tables, multi-job CI `errored`/`skipped` states, and non-destructive config alert permutations (`reconcilerDown`, `storeSwitch`, `stateRepoSyncFailure`, `hostSandboxes`, `pendingRestart`, `unrunnable`). |
