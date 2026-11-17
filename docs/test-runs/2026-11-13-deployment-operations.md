# Deployment Operations Walkthrough — 2026-11-13

This run covered System (Health, Logs, Sandboxes, Caches, Top, Pipeline,
Metrics, Power), Settings → Upgrade, the rail's CI page, the alerts bell and
the standing banners. Each was walked in a real browser against real
`grain demo` servers (task 4732). The classic UI at `/` was walked in full.
The new UI at `/next/` was spot-checked for the same areas (`/next/fleet`,
`/next/telemetry`, `/next/ci`, `/next/settings/upgrade`), and that is where
this run's one fix landed.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/098073e4/task-4732-walk-through-system-health`.
- **Commit tested:** `4ae6438fd` (`main` at the start of the run; build
  stamp `v0.0.0-20261113022244-4ae6438fdcd4 · 4ae6438 · 2026-11-13 02:22Z`).
  The fix (F1) was re-verified on a binary built from the fix commit.
- **Build:** `make frontend` (the classic and `/next/` bundles embedded in
  `pkg/ui/static` and `pkg/ui/static-next`), then
  `go build -o /var/tmp/grain ./cmd/grain`. Go 1.26.10, Node 20.19.0.
- **Servers:** every server is `grain demo -open=false -access-code-file
  <file>` over its own throwaway embedded SQLite store (its own `HOME`).
  Each session signs in through `POST /api/auth/access-code` as
  `alice@example.com`.
- **Browser:** Playwright 1.62.1 headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), `timezoneId: UTC`.
  Viewports: 1440×900 light, 640×800 (narrow), 390×844 dark and light.
  The browser was driven by throwaway scripts outside the checkout, through
  visible controls (role, label and test-id locators). No screenshots or
  browser artefacts are committed.
- **Model/tooling:** the sandbox carries no `/home/debian/.gemini-api-key`.
  No Gemini-backed tooling was used, and `LOOK=1` was unavailable. Layout
  was checked by DOM geometry (bounding boxes, `scrollWidth`) rather than
  by eye.

| Demo | Port | Flags | Used for |
| --- | --- | --- | --- |
| A | 7801 | `-observability -staging` | System tabs, Health, Logs, Top, Pipeline, Power, Caches, Metrics, config-driven alerts, `/next/` spot checks |
| B | 7802 | `-github-sim -fake-ci -base-branch-ci -notifications -agent-paused -host-mode-install -reconcile=1s -scheduled-workflows` | CI runs, retry and cancel, shared fault, agent pause, host-mode install banner |
| C | 7803 | `-unconfigured` | setup alerts, empty states, disconnected banner (failed polls and `SIGSTOP`) |
| D | 7804 | `-upgrade-image=docker.io/grainai/grain -upgrade-allowed-branches=main,latest,v3.2.0` | classic Settings → Upgrade in image mode |
| E | 7805 | `-disable-self-upgrade -management-url=https://console.example.com/deployments/grain-prod -observability` | externally managed upgrade |
| A2 / D2 / E2 | 7811 / 7814 / 7815 | as A / D / E, on the fixed binary | re-verifying F1 |

## Findings

### F1 (fixed): `/next/` Settings → Upgrade ignored what the daemon says about upgrading

- **Where:** `/next/settings/upgrade`
  (`ui-ng/src/app/settings/settings-instance.component.ts`, the `upgrade`
  card).
- **Reproduction (deterministic):** start `grain demo` with no upgrade
  flags (demo A) and open `/next/settings/upgrade`.
  `GET /api/upgrade` answers `{"enabled":false,…}`.
- **Observed:**
  - The version pill read `dev`.
  - The card drew a `Target channel / branch` picker defaulting to
    `latest`, the check-mode picker, the drain switch and an enabled
    `Start upgrade`.
  - The console logged two `404 GET /api/upgrade/targets`.
  - Demo E (`-disable-self-upgrade`) drew the same controls. It said
    nothing about being managed externally and offered no management link.
  - The classic pane on the same servers says "Not available: this
    deployment was started with neither -upgrade-image nor -upgrade-src-dir
    configured…" (demo A), and "Now running 4ae6438. In-VM self-upgrade is
    disabled on this deployment; upgrades and instance lifecycle are managed
    externally. Manage instance" (demo E).
- **Cause:**
  - `loadUpgrade` read `GET /api/config`'s `version` only when it was a
    string. On current servers it is the build-stamp object, so the
    signal kept its `"dev"` initial value.
  - `applyUpgradeStatus` never looked at `enabled`, `selfUpgradeDisabled`
    or `managementUrl`.
  - Targets were fetched unconditionally.
- **Fix:**
  - The version comes from `running.release`/`running.ref`. Failing that,
    it is the `/api/config` build stamp, as classic "Now running" reads it
    (the short commit for an `unversioned` build). A bare running commit is
    shortened to 7 characters.
  - `enabled: false` shows the classic "Not available" note.
  - `selfUpgradeDisabled` shows the externally-managed notice, with a
    `Manage instance` link when `managementUrl` is an absolute http(s) URL.
  - Either way the upgrade controls are replaced by the notice, only
    `Database: sqlite` stays, and `GET /api/upgrade/targets` is not read.
  - Image-mode deployments (demo D2) are unchanged: same picker, `Start
    upgrade`, and version `4ae6438` instead of `dev`.
- **Verified:** on the rebuilt binary:
  - Demo A2: "4ae6438 · Not available: …", no Start upgrade, no console
    errors.
  - Demo E2: "4ae6438 · In-VM self-upgrade is disabled … Manage instance".
  - Demo D2: the picker and Start upgrade as before.
  - Each was shot at 1440×900 light and 390×844 dark.
- **Regression coverage:**
  - Two new `SettingsPageComponent` vitest cases (no upgrader;
    externally managed), plus a `safeManagementUrl` case. The suite passes
    12/12.
  - The Playwright spec `ui-ng/e2e/next-upgrade-unavailable.spec.js`, run
    against the worker's real demo. It stubs `GET /api/upgrade` only for
    the externally managed case.
  - That spec and `next-full-system.spec.js` pass, 7/7.

### Observations (no change made)

- **The host-mode install notice tells OSS users to run
  `./google/scripts/cloudtop-install.sh on a Cloudtop`**
  (`pkg/daemon/installnotice.go` `hostModeMigrateCommand`). That path does
  not exist in this repository. It is shown on System → Health and in the
  banner. I proposed a follow-up rather than changing it here, because the
  Google layer may depend on the wording.
- **A malformed `dispatchBlocked` entry with no `name` crashes the classic
  app** (`TypeError: Cannot read properties of undefined (reading
  'replaceAll')`, `alerts.js` `collectAlerts`). It was hit only through
  this walkthrough's own malformed stub. The server's `DispatchCondition`
  always sends `name`, so I'm noting it, not counting it as a defect. With
  a well-formed stub, the alert renders and clears.
- **The narrow (640px) tab strip:** the `tablist` itself reports
  `scrollLeft 0`, because its parent is the scroll container
  (`overflow-x: auto`). The selected Power tab is in view
  (x 486–576 of 640) and the page does not overflow. The earlier run's
  `scrollLeft 370` reading was taken on a different element, so this is
  not a regression.
- **Unchanged since 2026-11-11 and still deliberate:**
  - Cache flushes have no confirmation (`PackageCachePanel.jsx`).
  - Pipeline, Power and Cancel run use the browser's native
    `confirm()`/`prompt()`.
  - A successful CI run offers no Retry.
  - A cancelled run offers Retry but not "Retry failed jobs".
  - `400 GET /api/upgrade/release-notes?branch=v3` fires while a partial
    branch is typed.
  - `404 GET /api/ci/runs/<forgotten>` backs the "no CI run … forgotten"
    message.
  - The `Image source` card is still gone (`#3850`), so that part of the
    brief is out of date.
- **`POST /api/e2e/base-branch/{red,green}` answered `409`** to the payload
  this walkthrough sent, so the red-base-branch alert was not raised live
  this run. It was covered on 2026-11-11, and
  `deployment-operations-base-red-notification.spec.js` covers it.

## Walkthrough steps & outcomes

Every step passed unless noted. A few first attempts failed on a locator of
the walkthrough's own (no `<main>` landmark in the classic shell, an MUI
select, a native dialog). Each was re-run with a corrected locator, and
the outcome below is from the re-run.

### 1. System navigation, tabs, URLs, layouts

1. Header rail `System` (`top-bar-system`) → `/system`, with Health
   selected.
2. Clicking each tab led to `/system`, `/system/logs`,
   `/system/sandboxes`, `/system/caches`, `/system/top`,
   `/system/pipeline`, `/system/metrics` and `/system/power`. Each had
   `aria-selected="true"`, and the panel's `aria-labelledby` equals the
   tab's id via `aria-controls`.
3. Reloading on `/system/power` keeps Power. Back → `/system/metrics`;
   Forward → `/system/power`.
4. Keyboard: focus Health, then ArrowRight + Enter → `/system/logs`, with
   focus on Logs.
5. Old links:
   - `/admin/logs` → `/system/logs`
   - `/sandboxes` → `/system/sandboxes`
   - `/caches` → `/system/caches`
   - `/metrics?window=24h&repo=acme%2Fwidgets&q=flaky` →
     `/system/metrics?q=flaky&f=repo:any:acme/widgets&window=24h`
   - `/system/upgrade` → `/settings/upgrade`
   - `/system/bogus` → `/system`
6. Build stamp at the rail's foot (`build-stamp`, demo D) →
   `/settings/upgrade`.
7. 640×800: the tab strip scrolls inside its container
   (`scrollWidth 730`) and the document stays at 640.
8. 390×844: `/system` shows the section list (health, logs, sandboxes,
   caches, top, pipeline, metrics, power). Every section rendered at
   `scrollWidth 390`. The phone bar's bell is visible, and its panel stays
   inside the viewport (x 16, width 358).
9. `/next/system` → `/next/fleet`. `/next/fleet`, `/next/telemetry` and
   `/next/ci` rendered with no console errors.

### 2. Health

1. The bell (`Alerts: 2 active` on demo A) and Health list the same
   alerts. "See all on System → Health" (`alerts-see-all`) → `/system`.
2. Health also showed:
   - Live overcommit: `overcommit: auto`, effective factors, PSI, and the
     adjustment dated `10/24/2026, 9:20:00 AM`.
   - The GitHub API budget: `1,650 of 5,000 calls left this hour … runs out
     in 30m 0s, before it resets`, the idle-poll backoff line and the
     per-caller table.
   - Registered daemon counters: `sign-in verification (demo)`, including
     `proxy:admitted 50`.
3. Pending restart and unrunnable settings (`GET /api/settings`
   intercepted):
   - "Saved, not applied until restart: Max worker agents."
   - `Max worker agents` → `/settings/engine#maxWorkers`, with focus on
     `maxWorkers`.
   - `Sandbox vCPUs` → `/settings/sandboxes#sandboxCpus`, with focus on
     `sandboxCpus`.
4. Host readiness (`GET /api/settings/cli-run` intercepted with an
   unreachable host agent): "The host agent is not reachable, so no run:
   host server can start: dial unix /run/grain/hostd.sock: …".
5. Host-mode install notice on demo B: "Unmigrated host-mode install", with
   the command and the CLIs that cannot run (`claude`). See the
   observation about the command text.

### 3. Sandboxes

1. Demo A: host load, utilization, memory and swap, storage, the overcommit
   controller, and persistent sandboxes (`docs-preview`,
   `grain-staging`). No CI job was in flight, so there was no `CI sandboxes
   (N)` summary row this run. Its fold persistence is covered by
   `SandboxHealthPage.test.jsx` and was walked on 2026-11-11.
2. Demos B and C: "Not available: this deployment has no sandbox pool or
   host stats configured."

### 4. Caches (demo A)

- There are ten flush controls, each named after its target:
  - `Flush apt packages`
  - `Flush npm packages`
  - `Flush Python packages`
  - `Flush Go modules`
  - `Flush Docker Hub layers`
  - `Flush Playwright browsers`
  - `Flush the whole proxy cache`
  - `Flush the Actions cache of acme/widgets`
  - `Flush the Actions cache of acme/gadgets`
  - `Flush the whole Actions cache`
- I flushed `Flush npm packages`, then `Flush Go modules`. After a reload,
  each read `Go modules: 0 packages, 0 B` (or the npm equivalent), and its
  button was disabled.
- Demo C: "This deployment is not running a proxy cache…".

### 5. Logs, Top (demo A)

Searching for `merge-queue` matched 50 lines. A search with no match shows
"(no matching log lines)". Clearing the search and pressing Refresh
restores the tail. Top lists the daemon's processes.

### 6. Pipeline, Power (demo A)

1. Pipeline pause:
   - Dismissing "Pause the pipeline? Grain will stop starting new agent and
     CI runs…" leaves it running.
   - Accepting pauses it: "Paused — only tasks you Run now will start …
     Paused by alice@example.com (access code). Reason: walkthrough 4732
     pause". The pause survives a reload. The bell goes from
     `Alerts: 2 active` to `3 active`, and Health carries the same row with
     `Resume now`.
   - Dismissing "Resume the pipeline? …" keeps it paused. Accepting
     resumes it, and the bell is back to `Alerts: 2 active` after a reload.
2. Power (`POST /api/host/{restart,reboot,shutdown}` intercepted). For
   each of Restart grain, Reboot host and Shut down host:
   - Dismissing the confirmation sends no request.
   - A `202` shows "Restart requested. grain is coming back up…", "Reboot
     requested. This UI is unreachable until the host machine comes back
     up." or "Shutdown requested. grain stays down until somebody powers
     the machine back on."
   - A `500` shows `walkthrough: <action> refused`.
   - Nothing restarted.

### 7. CI (demo B, `acme/widgets` switched to `ciMode: grain` and restored)

1. Runs started with `POST /api/ci/runs` and settled through
   `POST /api/e2e/fake-ci/runs/{id}/finish`. Each row's open control is
   named after its own run, with no duplicates. Examples: `acme/widgets
   main, failure, started 11/13/2026, 3:11:14 AM`; `acme/widgets
   feature/ok, success, …`; `acme/widgets feature/open, pending, …`.
2. `Search runs` for `abc1241` narrows the table to that commit's single
   row.
3. Active sandboxes: "No CI job is on a machine right now."
4. Retry:
   - On the failed run, `Retry` → `/ci/fake-ci-4`.
   - On a run with a failing `unit` job, `Retry failed jobs` →
     `/ci/fake-ci-6`.
   - A successful run offers no Retry.
5. `Cancel run` on an open run: "Stop this run? The jobs in flight are
   told to give up and their sandboxes are destroyed. Optionally, say
   why…". Dismissing keeps the run. Accepting cancels it, which persists
   through a reload. The cancelled run offers Retry but not Retry failed
   jobs.
6. Direct link and reload work. `/ci/0000forgotten0000` → `no CI run
   "0000forgotten0000" -- a run is remembered for a while and then
   forgotten`.
7. Shared fault: "1 shared failure — acme/widgets · CI / e2e (Run go test
   ./...) failing the same way on 3 branches".

### 8. Metrics (demo A)

- The window choices are Last 24 hours, 7 days, 30 days and 90 days.
  Choosing 24 hours puts `?window=24h` in the URL, and it is kept through a
  reload.
- The release notes and throughput sections render.
- `500` on `/api/metrics` → "The report could not be loaded (walkthrough:
  metrics store unreadable). Refresh to try again."
- On `-unconfigured` (demo C), the release-notes panel still renders.

### 9. Settings → Upgrade

1. Demo D, classic:
   - With `GET /api/upgrade` held 2.5s, a progressbar shows.
   - Image-mode text: "Pulls the image CI published for the branch
     below…".
   - The target picker narrowed on `v3` to `v3.2.0` (allowed). `v3.6.0` …
     `v3.1.0` are marked "not allowed on this deployment".
   - With `POST /api/upgrade` intercepted with a `500`, `Upgrade` first
     raises "confirm upgrade — 2 runs are in flight … Upgrade anyway?".
     Confirming posts `{"branch":"main","afterTasksFinish":false}`, and the
     pane shows `walkthrough: upgrade intercepted, nothing pulled`.
2. Retracted release (status augmented):
   - "Release v3.5.0 was retracted. v3.6.0 has been published to fix
     forward from it…", with `Upgrade to v3.6.0` and `Move back to latest
     (v3.4.0)`.
   - A discarded RC (status augmented) shows its "Release candidate …"
     banner.
3. Demo E, classic: "Now running 4ae6438. In-VM self-upgrade is disabled
   on this deployment; upgrades and instance lifecycle are managed
   externally." with `Manage instance`.
4. Demo A, classic: "Not available…", and the SQLite Database Backend panel
   with its offline-only migration commands (`sudo systemctl stop
   grain-daemon.service`, `sudo ./scripts/setup.sh --db-driver postgres`,
   `sudo grain db check …`).
5. `/next/settings/upgrade` on demos A, E and D: finding F1.

### 10. Alerts bell and standing banners

1. Agent pause (demo B, real):
   - "Usage limit reached (antigravity) — Agent usage limit reached:
     nothing is being dispatched onto connection Gemini 2.5 Pro until
     8:59:18 AM (about 5h 48m)", with `Resume now` and `Open connection`.
   - `Resume now` cleared it (`Alerts: 2 active`).
   - The dated form for a pause ending on another day is covered by
     `deployment-operations-pause-date.spec.js`.
2. Reconciler down (config intercepted): "Reconcile loop stopped — … reconcile
   loop: walkthrough: reconcile loop panicked. Restart the daemon to
   recover." It is critical, with `Power`. The bell went from 2 to
   3 active (critical) and back to 2 when unrouted.
3. Config-repo sync (intercepted): "Settings are not being exported —
   Settings have not been exported to the state repository since
   11/10/2026, 9:17:27 AM: push rejected." It shows the date because the
   failure did not start today. The alert is critical and clears when the
   intercept is removed.
4. Host sandboxes (intercepted): adds a third alert, which clears when the
   intercept is removed.
5. Pipeline pause and manual mode: section 6.
6. Setup (demo C, real): "2 setup steps left — Still to do: connect a git
   host, add a model connection." with `Finish setup`.
7. Host-mode install banner (demo B): banner at 0–68px, with the rail and
   page header at 88/98px. It does not cover them.
8. Disconnected banner (demo C):
   - Failed polls (`/api/**` aborted): banner at 0–48px, with the rail and
     header at 68/78px. It reads "This deployment cannot be reached: the
     connection to it failed. Unreachable since 3:13:53 AM -- retrying
     automatically…", and it cleared without a reload once requests
     flowed.
   - A frozen daemon (`kill -STOP`) raises "it accepted the connection but
     sent no answer within 10 seconds". It cleared on `kill -CONT`, with no
     reload.

### Console & network

Leaving out the probes this walkthrough injected and the 304
`ERR_ABORTED` artefact of the conditional task poll, there were
0 `pageerror` exceptions and 0 console errors on the classic UI, except for
the `replaceAll` crash from the malformed stub noted above. On `/next/`,
the only errors were F1's two `404 GET /api/upgrade/targets`, which are
gone after the fix.

## Stubbed & intercepted boundaries

| Boundary | Mechanism | Why |
| --- | --- | --- |
| `POST /api/host/{restart,reboot,shutdown}` | `page.route` → `202` / `500` | Never restart, reboot or shut down the host. |
| `POST /api/upgrade` | `page.route` → `500` | Never self-upgrade. |
| `GET /api/upgrade` (2.5s delay; `retractedRelease`, `discardedRc` added; externally managed answer in the new e2e spec) | `page.route` delaying or augmenting the real answer | Loading state, and release states only a live registry produces. |
| `GET /api/config` (`reconcilerDown` + `dispatchBlocked`, `stateRepoSyncFailure`, `hostSandboxes`) | `page.route` augmenting the real answer | Causes only a live boundary can raise. Each was unrouted to confirm the alert clears. |
| `GET /api/settings` (`pendingRestart`, `unrunnable`) | `page.route` augmenting | Saved-but-unapplied settings need a restart cycle. |
| `GET /api/settings/cli-run` (`hostExecutor.hostd` unreachable) | `page.route` augmenting | No host agent in the sandbox. |
| `GET /api/metrics` | `page.route` → `500` | Failure state. |
| `/api/**` aborted; daemon `SIGSTOP`/`SIGCONT` | `page.route` abort; `kill` | Disconnected banner, both causes. |
| Fake CI verdicts | built-in `/api/e2e/fake-ci/runs/{id}/finish` | The demo's own driver inside the real server. |

Everything else ran against the real Go servers and the real built
frontends, through visible controls.

## Tests run

- `cd ui-ng && npx vitest run src/app/settings/settings-page.component.spec.ts`:
  12/12 pass.
- `cd ui-ng && npx playwright test e2e/next-upgrade-unavailable.spec.js
  e2e/next-full-system.spec.js`: 7/7 pass.
- `npx prettier --check` and `npx eslint` on the changed `ui-ng` files:
  both clean.
