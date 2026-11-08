# Deployment Operations Walkthrough (Task 3464)

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real Chromium browser against real `grain demo` servers
(task 3464). This follows `2026-11-08-deployment-operations.md` (task 3360).

- **Commit tested:** `ad6d8b914ad6266ceeb529545b862c7977c0a5ac` (`main`). The
  fixes, regression tests and this report are on
  `grain/098073e4/task-3464-walk-through-system-health`.
- **Date:** 2026-11-08, 10:15–11:25 UTC.
- **Result:** two scoped UI bugs uncovered, reproduced deterministically in
  Chromium, fixed in-branch, and covered by Vitest unit tests and Playwright
  end-to-end tests (§1). No unsafe or out-of-scope defects required follow-up
  tasks. All other observability and operator-control flows behaved as designed
  with zero uncaught `pageerror`s and zero unexpected `5xx` responses (§2).

## Environment

- Debian 12 sandbox, `linux/amd64`; Go `1.26.10`; Node `20.19.0`; Playwright
  Chromium `151.0.7922.34` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- **Build:** `cd ui && npm ci && npm run build`, followed by
  `go build -tags grain_e2e -ldflags "-X github.com/ai-on-gke/grain/pkg/version.Release=v3.2.0-dev.2430+ad6d8b9" -o /tmp/walk-bin/grain ./cmd/grain`.
  The rail build stamp read `v3.2.0-dev.2430+ad6d8b9 · ad6d8b9 · 2026-11-08 10:20Z`.
  After applying the fixes, the frontend and `grain` binary were rebuilt and
  verified against live `grain demo` servers and before/after screenshot sheets.
- **Gemini:** `/home/debian/.gemini-api-key` was not present in this sandbox, so
  `LOOK=1` was unavailable. Layout was verified directly via DOM geometry
  (`getBoundingClientRect()`, `documentElement.scrollWidth`,scrollers) and
  Playwright before/after screenshots (`ui/e2e/screenshot.mjs` across desktop
  `1440×900` and phone `390×844` in light and dark themes).
- **Servers:** each `grain demo` instance ran against its own throwaway SQLite
  directory (`-open=false -access-code-file <dir>/access-code`) and was signed
  into through the visible `"Enter access code"` page:
  - **Demo A (`-observability -staging`):** real CI engine, sandbox ledger,
    overcommit status, job cache, proxy & Actions caches, logs, top, pipeline
    pause/resume, metrics, and persistent staging sandboxes (`docs-preview`,
    `grain-staging`).
  - **Demo B (`-github-sim -fake-ci -base-branch-ci -scheduled-workflows -notifications -reconcile 1s -agent-paused -host-mode-install -host-executor hostd-down`):**
    connection usage-limit pause, unmigrated host-mode install notice banner,
    unreachable host agent readiness note, red-base-branch reconciler & repair
    alerts, and shared CI faults.
  - **Demo C (`-unconfigured`):** fresh-install setup alerts (missing git
    connection, model connection, and repository) and empty sandbox states.
  - **Demo D (`-upgrade-image 127.0.0.1:<port>/x/grain -upgrade-allowed-branches latest,candidate,rc,main,v3.2.0,v3.1.1`):**
    image-mode upgrade target listing over a loopback Node OCI `/v2/` stub.
  - **Demo E (`-disable-self-upgrade -management-url https://console.example.test/grain -external-config-repo https://github.com/acme/grain-config.git`):**
    externally managed upgrade notice and external config-repo read-only state.

---

## 1. Findings and Fixes

### 1.1 `UnbackedStateDot` collapsed to `0px` width in non-flex parents and duplicated accessible text on `System → Health`

- **Where:** `ui/src/components/UnbackedStateDot.jsx` and
  `ui/src/components/system/HealthTab.jsx` (`data-testid="health-config-repo"`,
  plus `SettingsPage.jsx`'s `"Config repo"` section nav label).
- **Reproduction:** With `config.stateRepoResetNotice = true` on `/system`
  (Health) and `/settings/config-repo`:
  1. `UnbackedStateDot` rendered `<Box component="span" role="img" aria-label="A merged change reset settings on this deployment" sx={{ width: 8, height: 8, borderRadius: "50%", flex: "none", bgcolor: "warning.main" }} />` without `display: "inline-block"`.
  2. Inside `Sidebar` and `PageHeader`, its parent was a flex container, so CSS
     blockified the `<span>` to `8px × 8px`. Inside `HealthTab`'s MUI `<Alert>`
     (`.MuiAlert-message` has `display: block`) and `SettingsPage`'s
     `<ListItemText>` (`<span>`), the dot remained `display: inline`, causing
     Chromium to ignore `width`/`height` and collapse the dot to **`0px × 16px`**
     (invisible).
  3. On `HealthTab`, the `0px` `<span role="img" aria-label="A merged change reset settings on this deployment">`
     sat immediately before the literal text `"A merged change reset settings on this deployment."`,
     duplicating the sentence in the accessibility tree.
- **Fix:** Added `display: "inline-block"` and `verticalAlign: "middle"` to
  `UnbackedStateDot` so it renders `8px × 8px` in both flex and inline/block
  contexts, and added an optional `decorative` prop (`aria-hidden="true"`, no
  `role="img"`) used by `HealthTab` (`sx={{ mr: 0.75 }}`) where the adjacent
  alert text already states the exact message.
- **Regression coverage:** Unit test in `ui/src/components/SystemPage.test.jsx`
  and real-browser bounding-box + accessibility assertions in
  `ui/e2e/deployment-operations-health-sandboxes.spec.js`.

### 1.2 `SandboxHealthPage` ignored the deployment `timeZone` in four timestamp call sites

- **Where:** `ui/src/components/SandboxHealthPage.jsx`:
  - `SandboxOvercommitStatus` (`oc.lastOomAt`, rendered on **both**
    `System → Health` and `System → Sandboxes`),
  - `StandingSandboxesPanel` (`s.nextAttempt` and `s.startedAt`),
  - `HostCredentialsAlert` (`warning.since`),
  - `SandboxHealthPage` live sandboxes table `Started …` cell `title` tooltip.
- **Reproduction:** Open `/system` or `/system/sandboxes` in a `UTC` browser
  context when `GET /api/config` advertises `timeZone: "Asia/Tokyo"` (`UTC+9`).
  `SandboxOvercommitStatus` already read `const zone = useTimeZone()` for its
  `Recent overcommit adjustments` list (printing `2026-10-24T09:20:00Z` as
  `10/24/2026, 6:20:00 PM` JST), yet printed `lastOomAt` (`2026-11-07T20:15:00Z`)
  via raw `new Date(oc.lastOomAt).toLocaleString()` (`11/7/2026, 8:15:00 PM` UTC
  instead of `11/8/2026, 5:15:00 AM` JST — even landing on the wrong calendar
  date). The same raw `toLocaleString()` mismatch occurred on standing sandbox
  `startedAt`/`nextAttempt`, expired host-credential alerts, and live-row
  `Started …` tooltips.
- **Fix:** Replaced all four `new Date(...).toLocaleString()` calls in
  `SandboxHealthPage.jsx` with `formatDateTime(..., zone)` backed by
  `useTimeZone()`.
- **Regression coverage:** Unit test in
  `ui/src/components/SandboxHealthPage.test.jsx` (`<TimeZoneProvider zone="Asia/Tokyo">`)
  and Playwright e2e test in
  `ui/e2e/deployment-operations-health-sandboxes.spec.js` verifying both
  `/system` and `/system/sandboxes` in a `UTC` browser context.

---

## 2. Walkthrough Coverage and Outcomes

### 2.1 System shell, entry points, responsive navigation & legacy redirects

- **Entry points:**
  - Desktop (`1440×900`): clicked `System` (`data-testid="top-bar-system"`) in
    the page header globals -> opened `/system` with `Health` selected
    (`aria-selected="true"`, controlling `tabpanel` `"Health"`).
  - Build stamp (`data-testid="build-stamp"` at the foot of the rail, showing
    `v3.2.0-dev.2430+ad6d8b9 · ad6d8b9 · 2026-11-08 10:20Z`) -> opened
    `/settings/upgrade`. Direct navigation to `/system/upgrade` (including hash
    anchors) redirected cleanly to `/settings/upgrade`.
  - Phone (`390×844`, touch context): opened `"Open navigation"` drawer, tapped
    `System` at the foot of the drawer -> opened System's 8-row section list;
    tapped into every section (`Health`, `Logs`, `Sandboxes`, `Caches`, `Top`,
    `Pipeline`, `Metrics`, `Power`), confirmed `documentElement.scrollWidth === 390`
    and a visible alerts bell on all 8, and returned to the section list via
    `"All of System"`. Deep-linking `/system/health` on a phone opened the
    `Health` view directly rather than stopping on the section list.
- **Tabs, URL state & keyboard:** Clicked and `ArrowRight` + `Enter` traversed
  `Health` (`/system`), `Logs` (`/system/logs`), `Sandboxes`
  (`/system/sandboxes`), `Caches` (`/system/caches`), `Top` (`/system/top`),
  `Pipeline` (`/system/pipeline`), `Metrics` (`/system/metrics`), and `Power`
  (`/system/power`). Reloaded on `/system/power` and exercised browser `Back`
  and `Forward`, confirming URL and active `tabpanel` stayed synchronized.
- **Narrow desktop tab strip (`640×800`):** `.MuiTabs-scroller` overflowed
  internally (`clientWidth: 280`, `scrollWidth: 730`) while the document had
  zero horizontal overflow (`documentElement.scrollWidth === 640`); focusing the
  `Power` tab scrolled `.MuiTabs-scroller` (`scrollLeft: 450`) to bring `Power`
  fully into view.
- **Legacy redirects:**
  - `/admin/logs` -> `/system/logs`
  - `/sandboxes` -> `/system/sandboxes`
  - `/caches` -> `/system/caches`
  - `/metrics?window=24h&repo=acme%2Fwidgets&q=auth` ->
    `/system/metrics?q=auth&f=repo:any:acme/widgets&window=24h` (query preserved
    and canonicalized).

### 2.2 System → Health

- **Alerts & `"See all"`:** Confirmed every active alert from `GET /api/config`
  renders in `AlertsPanel` with its action button, and that `"See all on System → Health"`
  in the bell popover closes the popover and navigates to `/system` (desktop) or
  `/system/health` (phone).
- **Config-repo state:** Verified default/unbacked state, `stateRepoResetNotice`
  warning alert (`"A merged change reset settings on this deployment."` with its
  `8×8` warning dot and `"Config repo"` button opening `/settings/config-repo`),
  and `-external-config-repo` backing state.
- **Pending-restart & unrunnable settings:** Verified `health-pending-restart`
  (`Saved, not applied until restart: …` + `"Power"` button) and
  `health-unrunnable` (`Stored, but not runnable: …`). Clicked setting links
  (`#environmentName`, `#sandboxOvercommitCpuMax`, `#sandboxOvercommitCpuMin`)
  and confirmed each navigated to the matching Settings section, scrolled the
  field into view, and placed `document.activeElement` on the exact `<input>`.
- **GitHub API budget & idle-poll backoff:** Verified `1,650 of 5,000 calls left`
  bar, run-out projection (`At that rate it runs out in 30m 0s`),
  `Idle-poll backoff: 2 of 3 tracked PRs backed off`, and the 6 per-caller rows.
- **Host agent readiness & host-mode install notice:**
  - Missing allowlist entries (`hostd-health-missing`): named missing CLI
    entries alongside the `systemctl` reload command and `/etc/grain/hostd.yaml`
    snippet.
  - Unreachable host agent (`-host-executor hostd-down`, `hostd-unreachable`):
    rendered the unreachable diagnostic note on `/system`.
  - Unmigrated host-mode install (`-host-mode-install`): rendered
    `health-install-notice` on `/system` and `upgrade-install-notice` on
    `/settings/upgrade`.

### 2.3 System → Sandboxes

- **Commitment, pressure, budget, pools & overcommit:** Inspected host CPU/RAM/disk
  commitment and PSI pressure, `sandbox-budget`, `sandbox-overcommit-status`
  (`Overcommit: auto`, effective CPU/memory factors, bounds, PSI targets, OOM
  kill count/timestamp, and recent adjustments), pool readiness, cluster-free
  headroom, sandbox lifetime, and storage usage.
- **Live sandboxes ordering & `"CI sandboxes (N)"` fold:** Started a held CI
  sandbox and added live `agent` and `standing` sandboxes; confirmed `agent` and
  `standing` rows render above the `"CI sandboxes (1)"` summary row, which starts
  collapsed (`aria-expanded="false"`), expands on click, and persists both its
  expanded (`"true"`) and re-collapsed (`"false"`) states across full page
  reloads (`localStorage`).
- **Persistent sandboxes (`-staging` vs empty):** On Demo A (`-staging`),
  verified `docs-preview` (`running`, sub-path link) and `grain-staging`
  (restarting/host-port proxy note, exit status), running commit SHA, last
  refresh timestamp/reason, and inline `Logs` toggle. On Demo C, verified the
  empty `Persistent sandboxes` guidance state.
- **Trends, job cache, error/empty states & scrolling:** Confirmed `Sparkline`
  SVG paths populate across polls, exercised `"Empty the job cache"` confirm
  dialog (dismiss leaves cache intact; accept flushes and disables button),
  verified `500` read failure alert (`Sandbox health could not be read` rather
  than an infinite spinner), and verified 20+ live rows scroll inside
  `TableContainer` without overflowing `documentElement.scrollWidth`.

### 2.4 System → Caches

- Inspected **Proxy cache** (6 ecosystem rows with entry and byte usage against
  ceiling) and **Actions cache** (`acme/widgets: 2.0 GiB`, `acme/gadgets: 1.0 GiB`,
  fullness bar).
- Exercised `Refresh`, per-ecosystem flush (`Playwright browsers`), per-repo
  Actions flush (`acme/gadgets`), whole Actions cache flush, and whole proxy
  cache flush — dismissing a confirmation first (no state change) and accepting
  each against throwaway demo entries, then reloading `/system/caches` to verify
  persisted zeroed usage.

### 2.5 CI (`/ci`, from the rail)

- **Rail reachability:** On a fresh browser profile, `CI` is in
  `DEFAULT_HIDDEN_SECTIONS`; enabled `"Show CI"` in `"Customize sidebar"`, saved,
  and clicked `CI` in the rail (`/` -> `/ci`), confirming persistence across
  reload.
- **Active sandboxes fold:** With a CI sandbox running, verified `#ci-active-sandboxes`
  starts collapsed (`aria-expanded="false"`, summary
  `1 CI sandbox running · longest 0s (observe, held-1)`), expands on click,
  persists across reload, and opens `CIRunOverlay` when clicking the row's repo
  button.
- **Filtering, direct-links, reload & forgotten run IDs:** Filtered runs via the
  narrowing bar (persisting in `/ci?...` across reload); opened runs from table
  rows, task links, and direct URLs; navigated to and reloaded an unknown run ID
  (`/ci/c999999-missing`) and verified the info alert explaining runs are
  remembered for a while and then forgotten.
- **Job/step states, shared CI faults, Cancel & Retry:**
  - Inspected `Cache hit` (`job.cached`), `queued`/`pending`, `in_progress`/`running`,
    `success`, `failure` (with exit code and step log output), `errored`
    (infrastructure error alert), and `skipped` job and step states.
  - Verified shared CI fault display (collapse/expand persisted across reload,
    `new` badge for unseen faults, and `"Assigned to task #2 (grain/task-2)"`
    linking to `/tasks/2`).
  - Exercised `"Cancel run"` on an in-flight run: dismissing `prompt()` kept the
    run active; accepting with a reason cancelled the run, recorded
    `Cancelled: <reason>`, and persisted across reload.
  - Exercised both `"Retry"` (all jobs) and `"Retry failed jobs"` (failed/errored
    jobs only) on concluded runs, confirming each dispatches `POST /api/ci/runs`
    and follows immediately to the newly created `/ci/<id>` pane.

### 2.6 Logs, Top, Pipeline & Power

- **Logs (`/system/logs`):** Confirmed initial view pins to the newest tail line,
  scrolling up unpins tail auto-scroll, `"Search logs"` filters lines (showing
  `(no matching log lines)` when unmatched) and clearing returns a tail-pinned
  reader to the bottom while preserving scroll position for a reader who scrolled
  up, plus `Refresh` and failed-read error reporting.
- **Top (`/system/top`):** Verified process table rendering, `Refresh`, and
  failed-read recovery.
- **Pipeline (`/system/pipeline`):** Exercised `"Pause pipeline"` confirmation
  (dismiss leaves unpaused; accept enters manual mode
  `Paused — only tasks you Run now will start` with operator attribution and
  reason), verified the bell alert appears immediately and `"See all"` navigates
  to `/system`, opened `"Contact running agents"` dialog, and confirmed
  `"Resume pipeline"` clears both the pause state and the bell alert across
  reload.
- **Power (`/system/power`):** Intercepted `POST /api/host/*` so the host was
  never restarted, rebooted or shut down. Verified native `confirm()` dialogs on
  `"Restart grain"`, `"Reboot host"` and `"Shut down host"`: dismissing sent `0`
  requests; `202 Accepted` displayed `"Restart requested…"` / `"Shutdown requested…"`;
  `500` displayed the error notification cleanly.

### 2.7 Metrics (`System → Metrics` and dashboard throughput panel)

- Inspected every section (`Release notes` with `Weekly`, `Versions` and rollup
  chips `Today`..`Last month`, `Throughput`, `Latency`, `Capacity`, `Backlog`,
  and `GitHub CI`).
- Verified `Window` selector (`?window=24h`, `?window=30d`, unknown window
  fallback to `7d`) and narrowing query (`?q=repo:acme/widgets&window=30d`)
  persist across reload, `Back` and `Forward`.
- Verified navigation from a dashboard `metrics` (`Throughput`) panel's `"Open"`
  link into narrowed `/system/metrics`, navigation from Metrics task links
  (`#10` -> `/tasks/10`, returning via `Back`), and empty, loading, `500`
  failure, and long-value states (`scrollWidth <= clientWidth`).

### 2.8 Settings → Upgrade

- Opened `/settings/upgrade` from both the rail build stamp and `/system/upgrade`.
- Verified `"Now running …"` version/commit display, image-mode description,
  `BranchPicker` populated from the loopback OCI registry (with unallowed tags
  marked `aria-disabled="true"`), `"Will pull …"` preview and custom image
  override in `ImageChooser`, `"Wait until this deployment is idle"` toggle
  (`"Upgrade"` vs `"Schedule upgrade"`), read-only upgrade-check pointer
  (`data-testid="upgrade-check-settings-link"`, navigating to `/settings/general`),
  `InFlightRuns` at-risk breakdown + `"Confirm upgrade"` dialog (Cancel sends `0`
  POSTs; confirm intercepted on `POST /api/upgrade` for both `500` error and
  in-flight step progress with animated `UpgradeMark`), externally managed mode
  (`-disable-self-upgrade -management-url`), and the read-only `Database Backend`
  panel (`SQLite` migration & `grain db check` commands, fitting both desktop and
  phone widths).

### 2.9 Alerts bell & standing banners

- **Alerts bell (desktop header & phone top bar):** Exercised and verified
  wording, action links, scope grouping (`Deployment` vs `Connection …` when 5+
  alerts are active), `"See all on System → Health"`, and automatic clearing when
  resolved for:
  1. `reconcilerDown` (`"Power"` -> `/system/power`),
  2. `stateRepoSyncFailure` — both export failure (`"Settings are not being exported"`)
     and external pull failure (`"Configuration is not being pulled"`, `"Config repo"`
     -> `/settings/config-repo`),
  3. Agent/connection usage-limit pause (`-agent-paused`, showing resume time and
     remaining duration, `"Open connection"` -> `/settings/models`, and
     `"Resume now"` clearing the pause across reload),
  4. Pipeline pause & manual mode,
  5. Fresh-install setup (`-unconfigured`: 2 and 3 steps left across missing git
     connection, model connection, and repository; `"Finish setup"` ->
     `/settings/git`, `/settings/models`, `/repos`),
  6. `hostSandboxes` (`"No isolation: runs execute on this host"`),
  7. Red base branch & repair task (`POST /api/e2e/base-branch/red` raising
     `acme/widgets main is red` + repair task link, and
     `POST /api/e2e/base-branch/green` clearing it).
- **Standing banners (only two remain by design):**
  - `DisconnectedBanner` (and `disconnected-banner-mirror` inside open modals):
    raised by aborting `/api/**`; verified on desktop (`1440×900`, `top: 0`,
    `bottom: 48px`, above bell `top: 68px` and `main` `top: 125px`) and phone
    (`390×844`) with zero overlap, clearing automatically once `/api/**` resumed.
  - `InstallNoticeBanner` (`-host-mode-install`): verified at `top: 0` above the
    rail, header bell, phone navigation button, and build stamp without covering
    any control, and clearing when `installNotice` cleared.

---

## 3. Stubbed and Intercepted Boundaries

Real `grain demo` servers, SQLite stores, built Vite assets, and Playwright
Chromium user gestures were kept in the loop throughout. Only external or
host-mutating boundaries unavailable or unsafe in the sandbox were stubbed:

| Boundary | Route / Mechanism | Reason & Verification |
| :--- | :--- | :--- |
| Host power actions | Playwright `page.route("**/api/host/**")` | Never restart grain, reboot, or shut down the sandbox VM; verified `confirm()` cancel (`0` POSTs), `202` acknowledgement, and `500` error handling. |
| Self-upgrade execution | Playwright `page.route("**/api/upgrade")` (`POST` only) + loopback Node OCI `/v2/` tag-list HTTP server | Never overwrite or restart the running binary; real `grain demo -upgrade-image` queried the loopback `/v2/<repo>/tags/list` stub, while `POST /api/upgrade` was intercepted to verify cancel, `500` error, and step-progress UI. |
| Unreachable daemon (`DisconnectedBanner`) | Playwright `page.route("**/api/**", route => route.abort("failed"))` | Simulated network drop between browser and daemon, then unrouted to confirm automatic reconnect and banner dismissal. |
| Failure states (`500`) on read endpoints | Playwright `page.route` on `/api/sandboxes`, `/api/metrics`, `/api/caches`, `/api/top`, `/api/logs` | Verified explicit error alerts and `Refresh` / `Try again` recovery in place of infinite spinners. |
| Daemon panic / sync / host-credential / multi-state edge cases | Playwright `page.route` enriching real `GET /api/config`, `GET /api/settings`, and `GET /api/sandboxes` responses | Exercised `reconcilerDown`, `stateRepoSyncFailure` (export & external pull), `stateRepoResetNotice`, `hostSandboxes`, `timeZone: "Asia/Tokyo"` OOM/credential/standing timestamps, and multi-job CI states without crashing the demo process. |
