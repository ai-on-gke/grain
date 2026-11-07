# 2026-11-07 Deployment Operations Walkthrough

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real Chromium browser against real `grain demo` servers
(task 3164). This follows `2026-11-04-deployment-operations.md` and re-walks
every observability and operator-control surface on today's `main`.

- **Commit tested:** `5dd24214` (`main`, merge of #939). Fixes and regression
  tests are on `grain/098073e4/task-3164-walk-through-system-health`.
- **Date:** 2026-11-07, 18:00–19:08 UTC.
- **Result:** three scoped bugs were found, reproduced, fixed, and covered by
  unit and Playwright regression tests (§1):
  1. `grain demo -observability`'s Actions cache left phantom `0 B`
     repository rows after per-repo or whole-cache flushes instead of
     dropping empty repositories and sorting largest-first like the real
     `pkg/actionscache` store, and `CachesPage` kept per-repository `Flush`
     buttons enabled on `0 B` rows.
  2. System → Top's `<pre class="logs-view top-view">` lacked `tabIndex={0}`,
     `role="region"`, and `aria-label`, leaving horizontally and vertically
     overflowing process snapshots unreachable by keyboard (unlike
     `LogsPage`'s `<pre class="logs-view">`).
  3. `Upgrader.validateReleaseRepositoryLocked` formatted a broken
     `"allowed are  (set another with the daemon's -upgrade-release-repo flag)"`
     error when an `-upgrade-image` repository had no derived release
     repository (`len(allowed) == 0`).
  Everything else behaved as designed (§2).

## Environment

- Debian 12, `linux/amd64`; Go 1.26.10; Node 20.19.0.
- **Build:**
  - `cd ui && npm ci && npm run build` (embedded into `pkg/ui/static/`).
  - `go build -tags grain_e2e -o ~/bin/grain ./cmd/grain`.
  - Build stamp at the foot of the rail:
    `v3.2.0-dev.1968+5dd2421 · 5dd2421 · 2026-11-07 16:41Z`.
- **Servers**, each over its own throwaway embedded SQLite store (`TMPDIR` in
  `/tmp/walk-3164/`) and signed in through the visible `"Enter access code"`
  page (`-access-code-file`):
  - **Demo A (`127.0.0.1:4201`):**
    `grain demo -open=false -observability -staging -addr 127.0.0.1:4201`
    (real CI engine, real on-disk job cache, real host-directory sandboxes,
    proxy and Actions caches, `ps`/`top` snapshot, logs, pipeline pause, and
    persistent staging sandboxes).
  - **Demo B (`127.0.0.1:4202`):**
    `grain demo -open=false -github-sim -fake-ci -base-branch-ci -reconcile 2s -agent-paused -host-mode-install -notifications -host-executor hostd-down -upgrade-image 127.0.0.1:4255/x/grain -upgrade-allowed-branches latest,candidate,rc,main,v3.2.0,v3.1.1,v3.1.0 -addr 127.0.0.1:4202`
    (GitHub API budget & idle-poll backoff, unreachable `grain hostd`,
    unmigrated host-mode install notice, paused agent connection, standing
    notifications, and container-image upgrade picker).
  - **Demo C (`127.0.0.1:4203`):**
    `grain demo -open=false -unconfigured -addr 127.0.0.1:4203`
    (unconfigured deployment with missing git host and model connection, and
    upgrade unconfigured).
  - **Demo D (`127.0.0.1:4204`):**
    `grain demo -open=false -host-executor hostd -addr 127.0.0.1:4204`
    (real `grain hostd` over a throwaway allowlist missing grain's own CLI
    entries).
  - **Demo E (`127.0.0.1:4205`):**
    `grain demo -open=false -disable-self-upgrade -management-url https://console.example.com/grain -external-config-repo https://github.com/acme/grain-config.git -addr 127.0.0.1:4205`
    (externally managed deployment with self-upgrade disabled and external
    config repository).
- **Loopback OCI v2 registry (`127.0.0.1:4255`):** small Node HTTP server
  answering `/v2/` and `/v2/x/grain/tags/list` (`latest`, `candidate`, `rc`,
  `main`, `v3.2.0`, `v3.1.1`, `v3.1.0`, `v3.0.0`) plus manifest digest HEAD/GET
  so `GET /api/upgrade/targets` exercises the real registry tag-listing client.
- **Browser:** Playwright headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`)
  at `1440×900` (desktop), `700×700` (narrow desktop), and `390×844` (phone),
  driving visible controls via role/label locators, keyboard navigation, and
  native/MUI confirmations while recording `console` errors, `pageerror`, and
  failed requests.
- **Gemini:** `/home/debian/.gemini-api-key` was absent in this sandbox, so
  `screenshot.mjs --look` was skipped and layout was verified via DOM bounding
  boxes and `documentElement.scrollWidth` checks alongside `before`/`after`
  renders from `ui/e2e/screenshot.mjs`.

## 1. Bugs found and fixed

### 1.1 `demoActionsCache` left phantom `0 B` repository rows after flushing, and `CachesPage` kept `Flush` enabled on `0 B` rows

- **Surface:** System → Caches (`/system/caches`), `pkg/demo/demo_observability.go`
  and `ui/src/components/CachesPage.jsx`.
- **Reproduction (Demo A):**
  1. Open `/system/caches`. Actions cache shows `20 entries, 3.0 GiB of 20.0 GiB`
     with `acme/widgets: 2.0 GiB` and `acme/gadgets: 1.0 GiB`.
  2. Click `Flush` on `acme/gadgets` (`Flush the Actions cache of acme/gadgets`),
     then reload `/system/caches`; or click `Flush the whole cache`
     (`Flush the whole Actions cache`).
- **Observed vs expected:**
  - Real `pkg/actionscache/cache.go` (`Stats()`) and `pkg/daemon/actionscache.go`
    build `repos` only from live entries (`bytes > 0`) sorted largest-first, so
    flushing one repository removes its row and flushing the whole cache yields
    `repos: []` (rendering `"Nothing cached yet..."`).
  - `demoActionsCache.FlushActionsCache` zeroed `d.repos[i].Bytes = 0` in place
    and `ActionsCacheStats` copied every slice element unconditionally without
    sorting. Flushing `acme/gadgets` left `acme/gadgets: 0 B` on screen across
    reloads, and flushing the whole cache rendered `empty (0 B of 20.0 GiB)`
    above two `0 B` repository rows instead of `"Nothing cached yet..."`.
  - In addition, `CachesPage.jsx` only checked `disabled={flushingActions !== ""}`
    on per-repository `Flush` buttons, leaving `Flush` clickable on `0 B` rows.
- **Fix & regression coverage:**
  - Updated `demoActionsCache` (`pkg/demo/demo_observability.go`) to drop flushed
    repositories in `FlushActionsCache` and filter `Bytes > 0` sorted
    largest-first in `ActionsCacheStats`.
  - Updated `CachesPage.jsx` to disable per-repository `Flush` when
    `!(repo.bytes || 0)`.
  - Added unit tests in `pkg/demo/demo_observability_test.go` and
    `ui/src/components/CachesPage.test.jsx`, and extended
    `ui/e2e/system_workflows.spec.js` to verify single-repository and
    whole-Actions-cache flushes end-to-end.
  - Follow-up (merge-queue repair): `ui/e2e/system-tabs.spec.js` still
    asserted the old phantom `acme/widgets: 0 B` row after a per-repository
    flush, which turned `ui e2e` red on this branch. It now asserts the
    flushed row leaves the list while `acme/gadgets: 1.0 GiB` remains. The
    full chromium suite (983 tests) and the ui-registry suite pass locally
    after merging `origin/main` at `4d99f591`.

### 1.2 System → Top process snapshot `<pre>` was not keyboard-focusable

- **Surface:** System → Top (`/system/top`), `ui/src/components/TopPage.jsx`.
- **Reproduction (Demo A at `700×700`):**
  1. Open `/system/top` at `700×700`.
  2. `<pre class="logs-view top-view">` overflows horizontally
     (`scrollWidth: 640 > clientWidth: 418`, `overflow: auto`), and overflows
     vertically with large process lists.
  3. Pressing `Tab` skips past the `<pre>` (`tabIndex: null`, `role: null`), so
     keyboard users cannot scroll wide `ps`/`top` columns or process rows
     (whereas `LogsPage.jsx` added `tabIndex={0} role="region" aria-label="Log lines"`
     to `<pre class="logs-view">`).
- **Fix & regression coverage:**
  - Added `tabIndex={0} role="region" aria-label="Process snapshot"` to
    `<pre className="logs-view top-view">` in `ui/src/components/TopPage.jsx`.
  - Added unit coverage in `ui/src/components/TopPage.test.jsx` and Playwright
    verification in `ui/e2e/system_workflows.spec.js`.

### 1.3 Empty `"allowed are  (...)"` error when an `-upgrade-image` repository maps to no release repository

- **Surface:** Settings → Upgrade (`/settings/upgrade` → Image source →
  `Release repository`), `pkg/upgrade/release_assets.go`.
- **Reproduction (Demo B):**
  1. Start `grain` with `-upgrade-image 127.0.0.1:4255/x/grain` (or any
     private/custom registry repository with no `-upgrade-release-repo` flag
     and no release track).
  2. Open `/settings/upgrade`, switch `Source` to `Release assets only`, type
     `github.com/acme/grain-fork` into `Release repository`, and blur
     (`PUT /api/upgrade/source`).
- **Observed vs expected:**
  - `validateReleaseRepositoryLocked` (`pkg/upgrade/release_assets.go`) had an
    empty `allowed` slice and formatted:
    `release repository "github.com/acme/grain-fork" is not one this deployment may upgrade from; allowed are  (set another with the daemon's -upgrade-release-repo flag)`
    with nothing after `allowed are` and a double space before `(`.
- **Fix & regression coverage:**
  - Compacted `allowed` after sorting in `validateReleaseRepositoryLocked`; when
    `len(allowed) == 0`, it now reports:
    `release repository "<repo>" is not one this deployment may upgrade from: none is configured for this image repository (set one with the daemon's -upgrade-release-repo flag)`.
  - Added unit coverage in `pkg/upgrade/release_assets_test.go`
    (`TestSetImageSourceHoldsTheReleaseRepositoryToTheAllowedOnes`).

## 2. Walkthrough steps and outcomes

### 2.1 System entry points, tabs, URLs, responsive nav, and legacy redirects

- **Header rail & build stamp (Demo A, `1440×900`):**
  - Clicking `System` in `GlobalButtons` opens `/system` with `Health` active
    (`aria-selected="true"`).
  - Clicking the build stamp (`v3.2.0-dev.1968+5dd2421 · 5dd2421 · 2026-11-07 16:41Z`)
    at the foot of the sidebar navigates to `/settings/upgrade` and renders
    `Settings → Upgrade`.
- **All 8 tabs, keyboard navigation, reload, and Back/Forward:**
  - Clicked every tab in order: `Health` (`/system`), `Logs` (`/system/logs`),
    `Sandboxes` (`/system/sandboxes`), `Caches` (`/system/caches`), `Top`
    (`/system/top`), `Pipeline` (`/system/pipeline`), `Metrics`
    (`/system/metrics`), and `Power` (`/system/power`).
  - Reloaded `/system/caches` (stayed on `Caches`), navigated to `Pipeline` and
    `Power`, verified browser `Back` (`/system/pipeline`, `Pipeline` selected)
    and `Forward` (`/system/power`, `Power` selected), and verified `ArrowRight`
    keyboard navigation between tabs (`Health` → `Logs`, `/system/logs`).
- **Legacy URL redirects:**
  - `/system/upgrade` → `/settings/upgrade`.
  - `/admin/logs` → `/system/logs`.
  - `/sandboxes` → `/system/sandboxes`.
  - `/caches` → `/system/caches`.
  - `/metrics?window=30d&repo=acme/widgets` →
    `/system/metrics?f=repo%3Aany%3Aacme%2Fwidgets&window=30d` (query migrated
    and kept).
  - `/metrics?f=state:any:running&window=24h` →
    `/system/metrics?f=state%3Aany%3Arunning&window=24h`.
  - Unknown `/system/bogus` → `/system` (`Health`).
- **Narrow window (`700×700`) & phone (`390×844`):**
  - At `700×700`, the MUI scrollable tab bar scrolls horizontally within its
    container without widening the page (`docScrollWidth: 700`).
  - At `390×844`, opened `Open navigation` drawer → tapped `System` at the foot
    of the drawer → landed on `/system` showing the phone section list (`Health`,
    `Logs`, `Sandboxes`, `Caches`, `Top`, `Pipeline`, `Metrics`, `Power`);
    tapped `Caches` → `/system/caches` with `← All of System` back button and
    zero horizontal overflow (`docScrollWidth: 390`); tapped `All of System` →
    returned to `/system`.

### 2.2 System → Health

- **Alerts mirror & links (Demo B):**
  - Confirmed `System → Health` renders the same active alerts as the header
    bell (`v2.4.0 is ready to promote`, `A merge has been stalled for 2 hours`,
    `Usage limit reached (antigravity)`) with working action buttons.
- **Config-repo backing state:**
  - Internal backing (Demo A/B): shows `grain/config` backing state with
    `"Config repo"` button opening `/settings/config-repo`.
  - External backing (Demo E): shows external config repository
    `https://github.com/acme/grain-config.git` state cleanly.
  - Stubbed `stateRepoResetNotice` and verified `"Dismiss"` sends
    `POST /api/state-repo/reset-notice` and clears the notice.
- **Pending-restart & unrunnable settings links (`#<field-id>` focus):**
  - With `pendingRestart` (`sandboxOvercommitCpuMax`, `environmentName`) and
    `unrunnable` (`sandboxOvercommitCpuMin`) populated on `/api/config`, each
    row rendered its label, reason, and Settings link:
    - Clicking `Sandbox CPU overcommit ceiling` opened
      `/settings/sandboxes#sandboxOvercommitCpuMax` and focused the `INPUT`
      inside `#sandboxOvercommitCpuMax`.
    - Clicking `Environment name` opened `/settings/general#environmentName`
      and focused `#environmentName`.
    - Clicking `Sandbox CPU overcommit floor` opened
      `/settings/sandboxes#sandboxOvercommitCpuMin` and focused the `INPUT`
      inside `#sandboxOvercommitCpuMin`.
- **GitHub API budget & idle-poll backoff (Demo B & Demo E):**
  - Verified `"GitHub API: 1,650 of 5,000 calls left this hour · resets in 38m 0s · spending 3,300/h. At that rate it runs out in 30m 0s, before it resets."`,
    `"Idle-poll backoff: 2 of 3 tracked PRs backed off · poll interval median 2m 0s, max 8m 0s (30s base, 8m 0s cap)."`,
    and the per-caller breakdown table (`sync`, `base-branch-ci`,
    `agent tools`, `review-threads`, `path-triggers`, `ui`).
- **`grain hostd` readiness & host-mode install notice:**
  - Demo B (`-host-executor hostd-down -host-mode-install`): rendered both the
    unreachable `grain hostd` readiness warning and the unmigrated host-mode
    install notice (`claude: no host binary...`, `Migration docs`).
  - Demo D (`-host-executor hostd`): connected to the real `grain hostd` and
    listed missing allowlist entries (`git`, `claude`, `codex`, `gemini`) with
    remediation guidance.

### 2.3 System → Sandboxes

- **Host commitment/pressure, budget, overcommit controller, pool, lifetime, and storage (Demo A):**
  - Verified host CPU/memory/disk commitment and PSI pressure panels, sandbox
    budget and auto overcommit controller status, pool status, sandbox lifetime
    counters, and storage usage.
  - Exercised `Empty the job cache`: dismissing `window.confirm` kept cached
    entries intact; accepting cleared the job cache to
    `"No job result is cached"`.
- **Live sandboxes ordering & `"CI sandboxes (N)"` fold:**
  - Started two held CI runs (`walk/held-1`, `walk/held-2`) and a live agent
    sandbox (`1-identity-check`), plus standing session sandbox `s-director`
    and persistent sandbox `docs-preview`.
  - Confirmed `docs-preview` (persistent), `s-director` (standing session), and
    `1-identity-check` (agent) appear **above** the collapsed
    `"CI sandboxes (2)"` summary row (`aria-expanded="false"`).
  - Expanded `"CI sandboxes (2)"` by click (`aria-expanded="true"`), reloaded
    `/system/sandboxes`, and confirmed `localStorage["grain.ciSandboxesOpen"] === "1"`
    kept the CI rows expanded across reload; toggled closed with `Enter` on the
    keyboard and confirmed reload kept them collapsed.
- **Resource trends, persistent sandboxes, empty/error states, and narrow scrolling:**
  - Confirmed `<svg>` sparklines render after consecutive `/api/sandboxes`
    polls.
  - Inspected persistent sandboxes (`docs-preview` running, `grain-staging`
    failed with `Logs` error toast on `409 Conflict`), running commit SHA,
    `after_green` refresh policy, `lastRefresh` details, a 70-character name
    (`docScrollWidth === 1440`, zero horizontal overflow), empty live/persistent
    states, `500` error state, and `700×700` horizontal table container
    scrolling (`docScrollWidth === 700`).

### 2.4 System → Caches

- **Proxy & Actions caches (Demo A):**
  - Verified proxy cache ecosystems (`apt`, `npm`, `pip`, `go`, `cargo`,
    `maven` + `Playwright browsers`) and Actions cache usage (`20 entries, 3.0 GiB of 20.0 GiB`,
    `LinearProgress` `aria-valuenow="15"`) with per-repository rows
    (`acme/widgets: 2.0 GiB`, `acme/gadgets: 1.0 GiB`) and all 10 named `Flush`
    buttons (`Flush apt packages`, …, `Flush the whole proxy cache`,
    `Flush the Actions cache of acme/widgets`,
    `Flush the Actions cache of acme/gadgets`, `Flush the whole Actions cache`).
  - Exercised `Refresh`, flushed throwaway `Playwright browsers` and
    `acme/gadgets` (uncovering and fixing §1.1), verified the `usedByJobs: false`
    warning (`"No CI job is being handed this cache"`), and verified the `500`
    load-error state.

### 2.5 Rail CI page (`/ci`)

- **Sidebar customization & rail navigation (Demo A):**
  - Opened `Customize sidebar` → enabled `Show CI` (`role="switch"`) → clicked
    `Save` → reloaded and confirmed `CI` stays pinned in the rail and opens
    `/ci`.
- **Active sandboxes fold, filtering, Cancel, Retry, logs, task link, and forgotten run:**
  - Verified `Active sandboxes (2)` starts collapsed (`aria-expanded="false"`,
    summary `"2 CI sandboxes running · longest 1s (observe, held-1)"`), expands
    on click, and clicking a sandbox row opens its `CIRunOverlay` (`/ci/<id>`).
  - Filtered runs by branch/status and confirmed filter persistence across
    reload.
  - Exercised `Cancel run` on an in-flight run: dismissing the prompt left the
    run running; accepting with reason
    `"Stopped during deployment-operations walkthrough"` cancelled the run and
    persisted across reload.
  - Completed runs via `/api/e2e/ci/finish`, inspected job/step expansion and
    step log output (`"observed through the sandbox"`), triggered a second run
    on the same commit to verify real `Cache hit` state, and clicked `Retry`
    (every job) confirming navigation to the newly started run.
  - Triggered a CI run for `taskId: "6"` (`POST /api/ci/runs`), opened
    `/tasks/6`, and clicked `"Show the run"` in `GrainCIStatus` to open
    `CIRunOverlay` directly over the task detail.
  - Navigated directly to `/ci/00000000deadbeef` (a run ID the daemon does not
    know), reloaded it, verified the in-place not-found dialog state, and
    clicked `Back` to return to `/ci`.
  - Exercised `errored`, `failure`, `success` (`Cache hit`), `skipped`, and
    `pending` states, a shared CI fault row reading
    `"Assigned to task #4 (grain/task-4)"` linking to `/tasks/4`, and
    `Retry failed jobs` (`jobs: [".grain/workflows/observability.yml:observe"], recheck: true`),
    confirming it starts and follows to a new real CI run.

### 2.6 System → Logs, Top, Pipeline, and Power

- **Logs (`/system/logs`, Demo A):**
  - Opened pinned to the bottom (`scrollTop + clientHeight === scrollHeight`,
    `tabIndex="0"`, `role="region"`, `aria-label="Log lines"`).
  - Searched `"grain"` (filtered matching lines) and `"zzz-no-such-log-line"`
    (`"(no matching log lines)"`); clearing search restored bottom pin;
    scrolling up unpinned and `Refresh` preserved scroll position; verified
    `390×844` phone toolbar/output fit (`docScrollWidth: 390`) plus disabled and
    `500` error states.
- **Top (`/system/top`, Demo A):**
  - Inspected live `ps`/`top` process snapshot, toggled `Auto-refresh` off and
    on, clicked `Refresh`, verified disabled and `500` states, and fixed
    keyboard region focusability (§1.2).
- **Pipeline (`/system/pipeline`, Demo A):**
  - Opened `Contact running agents (2)` dialog, inspected controls, and
    cancelled without mutating tasks.
  - Filled `Reason (optional)` and clicked `Pause pipeline`: dismissed
    confirmation first (pipeline stayed running), then accepted. Confirmed the
    manual-mode pause alert appeared in `Pipeline`, in the header `Alerts` bell,
    and on `System → Health`, and persisted across reload.
  - Clicked `Resume pipeline`: dismissed confirmation first (stayed paused),
    then accepted (cleared alert everywhere). Paused once more and resumed
    directly from the `Alerts` bell's `Resume now` button.
- **Power (`/system/power`, Demo A, `POST` boundaries strictly intercepted):**
  - Verified dismissing `window.confirm` on `Restart grain`, `Reboot host`, and
    `Shut down host` sent zero `POST` requests.
  - Verified `200` JSON responses on `POST /api/host/restart`,
    `POST /api/host/reboot`, and `POST /api/host/shutdown` show their status
    banners, `net::ERR_CONNECTION_RESET` on `POST /api/host/restart` is treated
    as an accepted restart, `500` clears the requested banner and surfaces the
    error toast, and disabled host-power flags render the unavailable state.

### 2.7 System → Metrics

- **Entry points, windows, narrowing, sections, and links (Demo A):**
  - Created a temporary `panels` board with a `Throughput` panel, clicked its
    `Open` button, and confirmed navigation to
    `/system/metrics?f=repo:any:acme/widgets&f=created:within:30` (then deleted
    the temporary board).
  - Switched `Window` (`Last 24 hours` → `?window=24h`, `Last 30 days` →
    `?window=30d`, `Last 90 days` → `?window=90d`, `Last 7 days` →
    `/system/metrics`) and confirmed `?window=30d` survives reload.
  - Inspected all populated report sections (`Throughput`, `Latency`,
    `Capacity`, `Backlog`, `Tool use`, `Prompts`, `CI waits`,
    `Mid-run pull requests`, `CI queueing`, `GitHub CI`,
    `Landings & lead time`, `Sandboxes`) and the `Release notes` panel
    (`Weekly`, `By direction`, `Versions`).
  - Clicked task `#10` in `Release notes` → opened `/tasks/10`, then `Back`
    returned to `/system/metrics?f=repo%3Aany%3Aacme%2Fwidgets&window=30d` with
    narrowing and window preserved; clicked `Proposed` in `Backlog` → opened
    `/?f=repo:any:acme/widgets&f=state:any:proposed` and returned cleanly via
    `Back`.
  - Verified empty-window rendering, `500` load failure + `Refresh` recovery,
    and long repository/branch identifiers at `1440×900` and `390×844` with zero
    page overflow.

### 2.8 Settings → Upgrade

- **Version display, Updates link, image source, branch/tag picker, and confirmations:**
  - Opened `/settings/upgrade` from the build stamp, from `/system/upgrade`
    redirect, and from Settings' sidebar nav. Confirmed `Loading upgrade status`
    resolves cleanly into the form even when `grain hostd` is down (Demo B).
  - Clicked `Settings → General → Updates` (`upgrade-check-settings-link`) →
    landed on `/settings/general`, scrolled `#updates` into view, and focused
    its input.
  - Exercised `ImageSourceSettings` (`PUT /api/upgrade/source`): switched
    between `default`, `Release assets only`, and
    `Container registry only (docker pull)`, verified persistence across reload,
    and fixed the empty allowed-list validation message (§1.3).
  - Opened `BranchPicker` against the loopback OCI registry (`127.0.0.1:4255`):
    verified channels (`latest`, `candidate`, `main`), semantic version tags
    (`v3.2.0`, `v3.1.1`, `v3.1.0`), and disallowed `v3.0.0 — not allowed on this deployment`
    (`aria-disabled="true"`). Selecting `candidate` updated preview to
    `Will pull 127.0.0.1:4255/x/grain:candidate`.
  - Typed `127.0.0.1:4255/x/grain-custom:canary-1` into `Image (optional)`
    (`Will pull 127.0.0.1:4255/x/grain-custom:canary-1`), and confirmed checking
    `Wait until this deployment is idle` disables `Image (optional)`.
  - Exercised `Confirm upgrade` dialog with intercepted `POST /api/upgrade`:
    verified in-flight run loss warning (`"2 runs are in flight... 2 would be lost (antigravity 2)"`),
    `Cancel` sending zero `POST`s, `500` error toast, and `200` scheduled
    upgrade (`afterTasksFinish: true`).
  - Verified multi-repository `imageRepos` chips, `Read-only (set by flag/env)`
    locked `ImageSource` chip, `Last upgrade:` fallback note, SQLite and
    PostgreSQL `DatabaseBackend` panels, Demo C's unconfigured upgrade notice,
    and Demo E's `-disable-self-upgrade` view (`"In-VM self-upgrade is disabled on this deployment..."`
    + `Manage instance` → `https://console.example.com/grain`).

### 2.9 Alerts bell and standing banners

- **Alerts bell on desktop (`1440×900`) and phone (`390×844`):**
  - On `390×844`, opened `AlertsButton` from `PhoneNav`'s top bar, verified the
    popover fits inside the `390px` viewport (`left: 82`, `right: 351`,
    `docScrollWidth: 390`), and tapped `See all on System → Health` →
    `/system/health`.
  - Verified every alert family, wording, link, and clear transition:
    - **Agent/connection pause** (real `-agent-paused` on Demo B + model-scoped
      `Team Gemini / gemini-3-pro`): verified resume time & reason, clicked
      `Open connection` → `/settings/models`, and clicked `Resume now`
      (`DELETE /api/pause`) confirming immediate removal.
    - **Pipeline pause & manual mode** (real on Demo A): verified alert appears
      when paused and clears via `Resume pipeline` or bell `Resume now`.
    - **Reconciler down** (`reconciler-down` with `dispatchBlocked` remedy):
      clicked `Power` → `/system/power`.
    - **Failing config-repo sync** (`state-sync-failing`, both internal export
      and external pull wording): clicked `Config repo` → `/settings/config-repo`.
    - **Store switched while running** (`store-switched`) and **host sandboxes
      no-isolation** (`host-sandboxes`).
    - **Red base branch repair** (`red-base-repair/99`): verified critical row
      with `Open #99` → `/tasks/99`, `Submit`, and `Force merge`.
    - **Setup steps left** (`setup`): on real Demo C (`-unconfigured`, 2 steps
      left: git host + model connection), clicked `Finish setup` →
      `/settings/git`; with git + model configured and 0 repositories (1 step
      left), clicked `Finish setup` → `/repos`.
    - **Scope grouping (`GROUP_AT = 5`):** with 8 active alerts across
      deployment and connection scopes, verified `Deployment` and
      `Connection Team Gemini` group headers render in severity order, and
      removing the causes clears every transient row.
- **Standing banners (`InstallNoticeBanner` and `DisconnectedBanner`):**
  - Confirmed these are the only two standing banners above the shell.
  - **`InstallNoticeBanner` (real `-host-mode-install` on Demo B):** rendered as
    a static block above `.app-body` (`bannerBottom: 68` on `1440×900`, `128` on
    `390×844`, never covering the rail or page), and cleared as soon as
    `installNotice` cleared.
  - **`DisconnectedBanner` + `ModalStatusMirror`:** opened `Customize sidebar`
    dialog and aborted `/api/config` + `/api/tasks` polls; after two failed
    ticks, verified both `data-testid="disconnected-banner"` (`role="status"`,
    static above `.app-body` at `48px` desktop / `108px` phone without covering
    the rail or page) and `ModalStatusMirror` inside the open dialog announced
    the outage; restoring poll responses cleared the banner automatically on the
    next tick.

## 3. Intercepted / stubbed boundaries

Every flow kept the real `grain demo` server, SQLite store, HTTP router, and
built React bundle in the loop. Only host-destructive mutations and external
states absent from `grain demo` were intercepted via Playwright `page.route` or
a loopback socket:

| Boundary | Route / mechanism | Purpose |
| --- | --- | --- |
| OCI container registry | Loopback HTTP server on `127.0.0.1:4255` (`/v2/`, `/v2/x/grain/tags/list`, manifest HEAD/GET) | Let `grain demo -upgrade-image` run its real registry tag-discovery client without external network access. |
| Host power mutations | `page.route` on `POST /api/host/{restart,reboot,shutdown}` | Prevented mutating/restarting the sandbox while testing `200`, `500`, and `ERR_CONNECTION_RESET` UI handling. |
| Self-upgrade execution | `page.route` on `POST /api/upgrade` (and `GET /api/upgrade` enrichment for multi-repo chips, locked source, and PostgreSQL panel) | Prevented executing an upgrade while exercising `Confirm upgrade` Cancel, `500`, and `200` scheduled flows. |
| Config / health edge states | `page.route` merging fields onto real `GET /api/config` (`pendingRestart`, `unrunnable`, `stateRepoResetNotice`, `reconcilerDown`, `dispatchBlocked`, `storeSwitch`, `stateRepoSyncFailure`, `hostSandboxes`, 1-step `deployment`) | Exercised Health field-focus links and alert rows whose live causes require daemon restarts or broken remotes. |
| Red-base repair task row | `page.route` on `GET /api/tasks` & `GET /api/tasks/99` (`state: "awaiting_submit"`, `redBaseBranch: "main"`) | Exercised `red-base-repair/99` alert actions (`Open #99`, `Submit`, `Force merge`). |
| CI concluded edge states | `page.route` appending synthetic runs onto real `GET /api/ci/runs` (`errored`, `skipped`, `pending`, `assignedTaskId: "4"`, failed job for `Retry failed jobs`) | Supplemented Demo A's real running/cancelled/succeeded/cached CI runs; `Retry failed jobs` posted to the real `POST /api/ci/runs/{id}/retry` endpoint. |
| Sandboxes / Caches / Logs / Top / Metrics failure & edge states | `page.route` on `/api/sandboxes`, `/api/package-cache`, `/api/actions-cache`, `/api/logs`, `/api/host/top`, `/api/metrics` | Tested `500` error recovery, `usedByJobs: false`, standing session sandbox ordering, persistent `lastRefresh` metadata, and full 12-section metrics layout. |
| Network outage | `page.route` aborting `/api/config` and `/api/tasks` | Raised `DisconnectedBanner` and `ModalStatusMirror`, then lifted to verify automatic recovery. |

## 4. Console, pageerror, and network audit

- **`pageerror` (uncaught exceptions):** `0` across all five servers and all
  four Playwright walkthrough runs.
- **Console & network:** Zero unexpected console errors or failed API requests;
  all recorded `>= 400` responses corresponded to initial pre-login `401`
  probes, the intentional `404` forgotten CI run check (`/ci/00000000deadbeef`),
  `grain-staging`'s expected `409 Conflict` when requesting `Logs` on a stopped
  sandbox, Demo B's `400` validation toast on a disallowed release repository,
  and the intentional `500` / network-abort error-state checks.
