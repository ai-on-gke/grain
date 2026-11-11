# 2026-11-08 Deployment Operations Walkthrough

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real Chromium browser against real `grain demo` servers
(task 3360). This follows `2026-11-07-deployment-operations.md`.

- **Commit tested:** `10df6284` (`main`, merge of #913). The fixes and
  regression tests are on `grain/098073e4/task-3360-walk-through-system-health`.
- **Date:** 2026-11-08, 08:08–09:00 UTC.
- **Result:** two scoped bugs, both "a moment from another day printed as a
  clock time alone". Both are reproduced, fixed, and covered by unit and
  Playwright tests (§1). One design gap goes to a follow-up because it needs a
  design decision (§1.3). Everything else behaved as designed (§2).

## Environment

- Debian 12 sandbox, `linux/amd64`; Go 1.26.10; Node 20.19.0; Playwright
  Chromium from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Build:** `cd ui && npm ci && npm run build`, then
  `go build -tags grain_e2e -o ~/bin/grain ./cmd/grain`. The build stamp at the
  foot of the rail read `v0.0.0-20261108074221-10df62848045 · 10df628 · 2026-11-08 07:42Z`.
  After the fixes, the UI and binary were rebuilt and demo A was restarted to
  verify them.
- **Gemini:** there was no `/home/debian/.gemini-api-key` in this sandbox, so
  no `LOOK=1`. I checked layout with DOM geometry instead: bounding boxes,
  `scrollWidth`, and wrapping of the changed line.
- **Servers.** Each ran over its own throwaway embedded SQLite store
  (`TMPDIR=/tmp/w/<x>`). Each browser signed in through the visible
  "Enter access code" page using the code the demo printed.
  - **A `:4201`:** `grain demo -open=false -observability -staging` (real CI
    engine and job cache, sandboxes, proxy/Actions caches, logs, top,
    pipeline pause, persistent staging sandboxes).
  - **B `:4202`:** `grain demo -open=false -github-sim -fake-ci -base-branch-ci -reconcile 2s -agent-paused -host-mode-install -notifications -host-executor hostd-down`
    (connection usage-limit pause, unmigrated host-mode notice, unreachable
    host agent, GitHub API budget, shared CI fault).
  - **C `:4203`:** `grain demo -open=false -unconfigured` (setup alerts).
  - **D `:4204`:** `grain demo -open=false -upgrade-image 127.0.0.1:4255/x/grain -upgrade-allowed-branches latest,candidate,rc,main,v3.2.0,v3.1.1,v3.1.0`
    (upgrade picker), over a loopback OCI registry (§3).
- Harness: throwaway Node scripts driving `playwright`'s `chromium` through
  role, label and test-id locators. Each one recorded `console` errors,
  `pageerror`s, failed requests and every `>= 400` response. The scripts are
  not committed.

## 1. Findings

### 1.1 Overcommit adjustments printed the browser's clock time alone — fixed

- **Surface:** System → Health and System → Sandboxes,
  "Recent overcommit adjustments" (`SandboxOvercommitStatus`,
  `ui/src/components/SandboxHealthPage.jsx`).
- **Reproduction (demo A):** open `/system/sandboxes`. The demo's one
  adjustment is dated `2026-10-24T09:20:00Z`
  (`pkg/demo/demo_observability.go`), but on 2026-11-08 it read
  `CPU 2.50× → 2.75× — CPU PSI 8.4% under 12.5% calm band for 4 samples (9:20:00 AM)`.
  That looks like something from earlier today. The line used
  `new Date(at).toLocaleTimeString()`, so it also ignored the deployment's
  zone, which every other timestamp follows through `time.js`.
- **Fix:** the line now uses `formatDateTime(adj.at, zone)` with
  `useTimeZone()`. On the rebuilt binary it reads
  `(10/24/2026, 9:20:00 AM)` and wraps inside its column at 390 px and 1440 px
  (`scrollWidth` equals the viewport, no clipped rows).
- **Regression:** `SandboxHealthPage.test.jsx`
  ("dates each adjustment, in the deployment's zone", rendered under
  `TimeZoneProvider zone="Asia/Tokyo"`), plus the Playwright test
  `ui/e2e/deployment-operations-dated-times.spec.js` "an overcommit adjustment
  from another day carries its date".

### 1.2 Alert and pause "Since"/"Until" dropped the day — fixed

- **Surface:** every row in the alerts bell and on System → Health
  (`AlertsButton.jsx`'s meta line), the pipeline pause sentence (the alert,
  and System → Pipeline's `PipelineControl.jsx`), and the inbox's
  "· since" (`InboxPage.jsx`).
- **Reproduction (demo A):** make `GET /api/config` report
  `stateRepoSyncFailure.since` = three days ago and `reconcilerDown` with
  `dispatchBlocked[0].since` = two days ago (§3). The bell row's own sentence
  said `Settings have not been exported to grain/config since 11/5/2026, 8:35:26 AM`,
  while its meta line said **`Since 8:35:26 AM`**. The reconcile-loop row
  likewise said `Since 8:35:26 AM`. All of these went through `formatTime`,
  which prints the clock time alone. Alerts and pauses can stand for days (a
  stalled merge, a red base branch, a sync failure, a connection paused until
  next week), so an old alert reads as this morning's.
- **Fix:** `time.js` gains `formatMoment(value, zone, now)`. It returns the
  clock time when the moment is today in the deployment's zone, and the date
  and time otherwise. The five call sites above use it. The usage-limit
  sentences (`pauseMessage`, `pausedUntil`) already print the remaining
  duration, and the disconnected banner prints its age, so they keep
  `formatTime`. On the rebuilt binary the rows read
  `Since 11/5/2026, 8:37:02 AM` and `Since 11/6/2026, 8:37:02 AM`, and
  today's demo alerts still read `Since 6:40:07 AM`.
- **Regression:** `time.test.js` `formatMoment` (same day, other day,
  "today" decided in the zone rather than UTC, invalid input),
  `AlertsButton.test.jsx` "dates a since that is not today", and the
  Playwright test "an alert raised days ago says since which day".
- **Before/after:** `make ui-screenshots`-style shots
  (`ui/e2e/screenshot.mjs --pages /system/sandboxes,/system/health --demo-args -observability`)
  at desktop and phone widths, in light and dark. "Before" came from the base
  binary (`--bin`). They are attached to the task, not committed.

### 1.3 A daemon that accepts connections but never answers raises no banner — follow-up

- **Reproduction (demo C):** with `/system` open, `kill -STOP` the demo's
  process. After 40 s the page still showed no disconnected banner, and kept
  rendering its last data as though it were live. After `kill -CONT` it
  carried on. Aborting `/api/*` in the browser instead raised the banner
  within 4 s (§2.9).
- **Cause:** `api()` (`ui/src/api.js`) sets no request timeout, and the shell's
  polls wait for the request in flight before starting the next one. A frozen,
  deadlocked or overloaded daemon (or proxy) that holds the TCP connection
  open therefore never produces the `TypeError` that `isUnreachable` keys on.
- **Why not fixed here:** a timeout needs a design choice. How long counts as
  hung on a loaded deployment? `/api/restart-watch` is a long poll and must be
  exempt. A slow-but-alive daemon must not flap the banner. I filed a focused
  follow-up task with this reproduction.

## 2. Walkthrough steps and outcomes

### 2.1 Entry points, tabs, URLs and legacy links — PASS

- The rail's **System** button opened `/system` (Health). The build stamp
  (`data-testid=build-stamp`, a link) opened `/settings/upgrade`.
- Clicking each tab moved the URL through `/system/logs`, `…/sandboxes`,
  `…/caches`, `…/top`, `…/pipeline`, `…/metrics` and `…/power`, and Health
  back to `/system`. Back went to `/system/power` and then
  `/system/metrics`, and Forward returned to `/system/power`. A reload kept
  Power selected. With a tab focused, ArrowRight moved focus to the next tab
  and Enter opened it.
- Old links: `/system/upgrade` → `/settings/upgrade`, `/admin/logs` →
  `/system/logs`, `/sandboxes` → `/system/sandboxes`, `/caches` →
  `/system/caches`, and `/metrics?window=7d` → `/system/metrics?window=7d`
  (query kept). None produced a console error or failed request.
- **Phone (390×844, touch):** "Open navigation" → the drawer's **System** row
  opened `/system`, showing the section list (health … power). The bell is on
  the bar on every System tab, on `/ci` and on `/settings/upgrade`. On every
  one of those pages `scrollWidth` was 390. `/ci`'s runs table scrolls inside
  its own container.

### 2.2 Health — PASS (after §1.1, §1.2)

- Demo A's Health shows the overcommit status, the GitHub API budget
  (`1,650 of 5,000 calls left this hour · resets in 38m 0s · spending 3,300/h. At that rate it runs out in 30m 0s`),
  idle-poll backoff (`2 of 3 tracked PRs backed off …`), the per-caller table,
  sign-in verification counters, and the same alerts as the bell, each with
  its action buttons.
- Demo B shows the unmigrated host-mode install notice
  (`claude: no host binary …`, "Migration docs") and the unreachable host
  agent note, which names the socket and the `systemctl`/`journalctl`
  commands.
- With pending-restart and unrunnable settings injected (§3), Health listed
  "Saved, not applied until restart: Environment name, Auto CPU maximum." and
  "Stored, but not runnable: Auto CPU minimum (stored 9, running 1)." Each link
  opened its anchor and focused the labelled input:
  - `/settings/general#environmentName` focused "Environment name".
  - `/settings/sandboxes#sandboxOvercommitCpuMax` focused "Auto CPU maximum".
  - `/settings/sandboxes#sandboxOvercommitCpuMin` focused "Auto CPU minimum".
- "Config repo" on the sync-failure row opened `/settings/config-repo`.

### 2.3 Sandboxes — PASS

- Host load, CPU and memory, storage (sandboxes, proxy cache, CI history, CI
  job cache with "Empty the job cache", CI artifacts), the budget
  (`within budget cap`, committed vs cap, cluster free), the auto overcommit
  controller (factors, bounds, targets, PSI, OOM kills, adjustments),
  ledger reconciliation, and the standing-sandboxes empty state all render.
- With two held CI runs live, the live table folded them under
  "CI sandboxes (2)".

### 2.4 Caches — PASS

- Proxy cache (6 ecosystems, total 5.3 GiB) and Actions cache
  (`20 entries, 3.0 GiB of 20.0 GiB`, `acme/widgets: 2.0 GiB`,
  `acme/gadgets: 1.0 GiB`), each with named Flush buttons.
- I flushed the throwaway **Playwright browsers** row. Its Flush button then
  disabled, and the row stayed empty after a reload. Refresh works.
- **Note:** a flush asks for no confirmation. That is deliberate
  (`PackageCachePanel.jsx`: "nothing is lost that a re-download does not put
  back"), not a missing confirmation.

### 2.5 CI (`/ci`) — PASS

- I started two runs through the visible "Start a run" form (Repo → Branch →
  Load workflows → Workflow → Start run) on `walk/held-1` and `walk/held-2`.
  "Active sandboxes (2)" starts collapsed (`aria-expanded=false`).
- Unknown run ids (`/ci/c999999-nope`, `/ci/99999999`) open the run pane with
  `no CI run "…" -- a run is remembered for a while and then forgotten`.
- **Cancel run** on the open run prompts ("Stop this run? …"). Dismissing it
  kept the run going. Accepting with a reason gave `cancelled` and
  `Cancelled: Stopped during walkthrough 3360`, which survived a reload.
- I released the held step with `/api/e2e/ci/finish`, the demo's own test hook,
  and the run went to `success` with no Retry button. Retry is offered only on
  errored, failure and cancelled runs (`RETRYABLE`). **Retry** on the cancelled
  run followed to a new run id (`/ci/f7ec8b7a…`). Retrying again with the same
  inputs came back as a real `cache hit`.
- "Retry failed jobs" is not offered on a cancelled run
  (`retryFailed` excludes `cancelled`), as designed.
- The shared CI fault (`acme/widgets · CI / e2e`, 3 branches, common file
  `pkg/auth/session.go`) shows on demo A, and on demo B as "1 shared failure"
  that expands and collapses.
- The branch box asks `GET /api/repos/acme/widgets/github-branches`, which
  returns 404 on demo A because it has no GitHub. The field still accepts a
  typed branch. That is console noise only, not a broken control.

### 2.6 Logs, Top, Pipeline, Power — PASS

- **Logs:** 300 lines, in a `tabindex=0` region. Search narrows to matching
  lines and shows "(no matching log lines)" for a miss. Refresh works.
- **Top:** `ps`/`top` snapshot in a `role=region` `<pre>`.
- **Pipeline:**
  - **Pause:** dismissing the native confirm left the pipeline running.
    Accepting with reason "walkthrough 3360" gave
    `Paused — only tasks you Run now will start …` (manual mode). The bell went
    to "Alerts: 3 active", with a row saying "Run now still starts the task it
    is pressed on. — walkthrough 3360" and "resume now". The pause survived a
    reload.
  - **See all:** "See all on System → Health" opened `/system`.
  - **Resume:** Resume (confirm) cleared the pause, and the bell went back to
    2.
- **Power**, with POSTs intercepted (§3):
  - Each button's native confirm names its consequence.
  - Dismissing Restart sent nothing.
  - Restart → 202 showed "Restart requested. grain is coming back up…".
  - Reboot → 500 showed the error toast.
  - Shut down → 202 showed "Shutdown requested…".
  - Exactly three POSTs went out, and the host was never touched.

### 2.7 Metrics — PASS

- Window combobox (24 h / 7 d / 30 d / 90 d). Choosing 24 h set
  `?window=24h`, which survived a reload.
- Filtering `repo:acme/widgets` set `?q=repo:acme/widgets&window=24h`, which
  also survived a reload.
- Throughput, latency and the other sections render. The release-notes panel
  shows the weekly summary and the versions list.
- Task links (`#10 → /tasks/10`) open tasks.
- A 500 on `/api/metrics` shows its error message.
- **From a dashboard:** a panels board with a `metrics` panel (seeded through
  the real `POST /api/boards`, since boards are otherwise made by asking the
  director). Opened at `?q=repo:acme/widgets`, its **Open** link went to
  `/system/metrics?q=repo:acme/widgets`, keeping the narrowing.

### 2.8 Settings → Upgrade — PASS

- Demo A (no `-upgrade-image`) says it is not available and why. The database
  panel names SQLite and the stop → `setup.sh --db-driver postgres` →
  `grain db check` commands.
- On demo D, the build stamp → `/settings/upgrade` showed:
  - the image source (release assets with registry fallback);
  - the branch picker, listing the registry's real tags
    (`latest`, `candidate`, `main`, `v3.2.0`, `v3.1.1`, `v3.1.0`);
  - "Will pull 127.0.0.1:4255/x/grain:v3.1.1" after choosing v3.1.1;
  - the upgrade check mode as a read-only pointer to "Settings → General →
    Updates";
  - the runs that would be lost (2).
- **Upgrade:** opens "Confirm upgrade". Cancel sent nothing. Confirming
  POSTed `{"branch":"v3.1.1","afterTasksFinish":false}` to the intercepted
  `/api/upgrade`, and the page showed the stubbed refusal.
- `release-notes?branch=v3.1.1` returned 422 because the server really ran
  `docker pull` and my stub registry serves no blobs. That is a limitation of
  the stub.

### 2.9 Alerts bell and standing banners — PASS (after §1.2)

- **Bell rows, wording and links:**
  - **Connection usage-limit pause (B):** names the connection, the resume
    time and the time remaining. "Resume now" cleared it, and it stayed
    cleared after a reload.
  - **Pipeline pause / manual mode (A):** §2.6.
  - **Setup (C):** "2 setup steps left — connect a git host, add a model
    connection", with "finish setup".
  - **Injected rows (§3):** reconcile loop stopped (critical, "power"),
    settings not being exported ("config repo"), and "No isolation: runs
    execute on this host". All of them cleared once the injection stopped:
    5 → 2 active.
- **Disconnected banner:** aborting `/api/*` raised "This deployment cannot be
  reached … Unreachable since …" in 4 s at 1440 and at 390. It sits in the
  page flow at the top (0–48 px desktop, 0–108 px phone). The bell, the System
  button and the phone's "Open navigation" sit below it with no overlap, and
  the rail's build stamp is uncovered. It cleared 3 s after the routes came
  back. A frozen daemon does not raise it (§1.3).
- **Host-mode install notice (B):** a 68 px strip at the top of every page,
  above the rail and header.

## 3. Intercepted / stubbed boundaries

Every flow kept the real `grain demo` server, store, router and built bundle.

| Boundary | Mechanism | Why |
| --- | --- | --- |
| Host power | `page.route` on `POST /api/host/{restart,reboot,shutdown}` → 202 / 500 | Never restart, reboot or power off the sandbox |
| Self-upgrade | `page.route` on non-GET `/api/upgrade` → 500 `{"error":"upgrade refused: walkthrough stub"}` | Never upgrade |
| OCI registry | Loopback Node server on `127.0.0.1:4255` (`/v2/`, tags list, manifest HEAD/GET) | Let demo D's real tag listing run offline |
| Live-boundary alerts | `page.route` adding fields to the real `GET /api/config` (`reconcilerDown` + `dispatchBlocked`, `stateRepoSyncFailure`, `hostSandboxes`) and `GET /api/settings` (`pendingRestart`, `unrunnable`) | Their causes need a stopped reconcile loop, a broken remote, a host-sandbox daemon or a restart |
| Metrics failure | `page.route` on `/api/metrics` → 500 | Error state |
| Network outage | `page.route` aborting `/api/*` (`connectionrefused`) | Raise and lift the disconnected banner |
| Held CI step | The demo's own `POST /api/e2e/ci/finish` | Conclude a held run |
| Dashboard | Real `POST /api/boards` (panels board with a metrics panel) | Boards are otherwise made through the director |
| Frozen daemon | `kill -STOP` / `-CONT` of demo C | §1.3 reproduction |

## 4. Console, pageerror and network audit

- `pageerror`: 0 across every run.
- Every `>= 400` response was expected:
  - the pre-sign-in 401 on `/`;
  - an aborted `/api/tasks` during navigation;
  - the deliberate unknown-run 404s;
  - demo A's `github-branches` 404 (§2.5);
  - the stub-registry 422 (§2.8);
  - the deliberate 500s and aborts of §3.

## 5. Tests run

- `npx vitest run`: 257 files, 4027 tests, all passing.
- `npx playwright test --project=chromium`, all 20 passing:
  - `deployment-operations-dated-times.spec.js` (new);
  - `system-tabs`, `system_workflows`, `alerts-panel-layout`,
    `host-sandboxes-alert`, `deployment_operations_walkthrough` and
    `system-health-phone-deep-link`.
