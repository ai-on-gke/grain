# Deployment Operations Walkthrough — 2026-10-30

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners. Everything was walked in a real browser against real `grain demo`
servers. This run concentrated on what the 2026-10-29 run
(`2026-10-29-deployment-operations.md`) left undriven: the upgrader with a
registry wired, and the reconciler-down, config-sync and host-sandbox
alerts. It also opened Metrics from a dashboard's throughput panel and
checked the Metrics failure, loading and empty states.

- **Commit tested:** `f4582ac8` (`main`, the merge of #215), then the fixes
  below on `grain/049f1e09/task-1906`.
- **Date:** 2026-10-30
- **Environment:**
  - Debian 12, `linux/amd64`, 7 vCPU and 15 GiB; Go 1.26; Node 20.19.0.
  - `grain` was built with `go build -tags grain_e2e -ldflags "-X …/pkg/version.Release=v1.2.0" ./cmd/grain`
    over the Vite production bundle (`npm ci && npm run build`), the same
    way `ui/e2e/global-setup.js` builds it.
  - Three demos, each over its own throwaway embedded SQLite store
    (`TMPDIR` set to a scratch directory). Each was signed in by typing
    its access code into the "Enter access code" page:
    - **A:** `grain demo -observability` on `:4301`. This has the real
      `pkg/ci` service, the job cache, host-directory CI sandboxes, the
      fake proxy and Actions caches, and `top`.
    - **B:** `grain demo -github-sim -fake-ci -base-branch-ci -agent-paused -host-mode-install -notifications -upgrade-image localhost:5055/x/grain`
      on `:4302`. `-upgrade-image` wires the real image-mode upgrader.
      It never restarts anything.
    - **C:** `grain demo -unconfigured` on `:4303`.
  - Playwright Chromium (`chromium-1234`, headless) from
    `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900 and
    390×844. Each walk was a Node script driving `playwright`'s
    `chromium` through visible controls (role and name locators, clicks,
    typing, native dialogs). Each script recorded console errors, page
    errors, failed requests and HTTP ≥ 400 responses.
  - There was no Gemini key in this sandbox (`/home/debian/.gemini-api-key`
    is absent), so nothing used a model.

## 1. Bugs found and fixed

### Bug 1: "Reconcile loop stopped" named the fix but offered no way to it

- **Surface:** the alerts bell and System → Health
  (`ui/src/alerts.js`, `ui/src/components/AlertsButton.jsx`).
- **Reproduction:** on demo B, add `reconcilerDown: true` to
  `GET /api/config` (stub below; only a daemon whose reconcile loop has
  died sets it), then open the bell. The row reads "This deployment's
  reconcile loop has stopped -- no tasks are being dispatched or
  checked. Restart the daemon to recover." It has no button.
  - The other alerts carry their way out ("Config repo", "Open
    connection", "Finish setup", "Resume now").
  - Health's own restart-pending notice already has a **Power** button
    to System → Power. Power's "Restart grain" is the restart this alert
    asks for.
- **Fix:** an `open-power` action labelled **Power** on the
  reconciler-down alert. It closes the popover and opens System → Power
  (`openSystem("power")`), from the bell and from Health's list alike.
- **Coverage:**
  - `AlertsButton.test.jsx` › "opens System → Power, where Restart grain
    is, for a stopped reconcile loop".
  - `e2e/deployment-operations-alert-power-flush-names.spec.js` › "a
    stopped reconcile loop's alert opens System → Power, from the bell and
    from Health". It covers the bell, the Health row, and at 390 px a
    bounding-box check that the button stays inside its row and the row
    stays on screen.

### Bug 2: two buttons on System → Caches were both named "Flush the whole cache"

- **Surface:** System → Caches (`PackageCachePanel.jsx`,
  `CachesPage.jsx`).
- **Reproduction:** on demo A, open `/system/caches` and list the
  buttons' accessible names: `Flush apt packages`, …, `Flush the Actions
  cache of acme/widgets`, and **two** `flush the whole cache`. One empties
  the proxy cache and the other the Actions cache.
  - The per-row buttons were named for their rows on purpose ("six
    buttons all called 'Flush' read as one word six times").
  - The two whole-cache buttons undid that. `system_workflows.spec.js`
    could only tell them apart with `.first()`.
- **Fix:** `aria-label="Flush the whole proxy cache"` and
  `aria-label="Flush the whole Actions cache"`. The visible text is
  unchanged.
- **Coverage:**
  - `CachesPage.test.jsx` finds each button by its own name and checks
    its visible text.
  - The new e2e spec checks both names, that nothing still answers to
    the old shared name, and (by bounding box) that the proxy button sits
    above the Actions one.
  - `system-tabs.spec.js`, `system_workflows.spec.js`,
    `PackageCachePanel.test.jsx` and `SandboxHealthPage.test.jsx` now use
    the specific names.

## 2. Walkthrough and outcomes

### Navigation, URLs, history (demo A)

- The header rail's System button opens `/system` with Health selected.
- Each tab clicked, then reloaded, kept its URL and its selected tab:
  `/system/logs`, `/system/sandboxes`, `/system/caches`, `/system/top`,
  `/system/pipeline`, `/system/metrics`, `/system/power`.
- Back and Forward stepped through the tab history (metrics → pipeline →
  metrics).
- Old links:
  - `/admin/logs` → `/system/logs`.
  - `/sandboxes` → `/system/sandboxes`.
  - `/caches` → `/system/caches`.
  - `/metrics?window=30d&repo=acme%2Fwidgets` →
    `/system/metrics?f=repo:any:acme/widgets&window=30d`. The query is
    kept, with `repo=` turned into the narrowing.
  - `/system/upgrade` → `/settings/upgrade`.
  - `/system/health` → `/system`.
  - An unknown `/system/bogus` → `/system` (Health).
- The build stamp at the foot of the rail ("v1.2.0 · f4582ac · 2026-10-30
  12:31Z") opens `/settings/upgrade`.
- **Phone (390×844):**
  - "Open navigation" opens the drawer. The System row at its foot opens
    the section list (Health … Power, each with ›).
  - Every section opened from the list and survived a reload at
    `/system/<tab>` (Health stays `/system/health`).
  - No section scrolls sideways (`scrollWidth == clientWidth == 390`), and
    no element runs past the right edge without a scrolling or clipping
    ancestor.
  - The bell is on the phone bar ("Alerts: 2 active"), and its "See all on
    System → Health" opens `/system/health`.

### Health

- **Demo B lists:**
  - the host-mode install notice ("Unmigrated host-mode install …",
    the setup.sh command, "claude: no host binary …", Migration docs);
  - the GitHub API budget with its per-caller table;
  - the two seeded notifications (Approve/Reject/Acknowledge/Investigate);
  - the connection pause ("Usage limit reached (antigravity)", scoped to
    connection Gemini 2.5 Pro, "Until 6:41:46 PM (about 5h 54m)", with
    Resume now and Open connection).
- The injected reconciler-down, config-sync and host-sandbox alerts
  (below) were listed on Health as in the bell.
- **Restart-pending row:** "Saved, not applied until restart: …" offers
  only **Power**. It has no link to the Settings section that holds the
  setting, and nothing in the UI maps a setting key to its section. Filed
  as a follow-up rather than fixed here (§5). The stored-but-not-runnable
  row links to Settings → Engine.

### Sandboxes (demo A)

- **Host:** load 0.45/0.55/0.60 of 4 vCPU, 15 % utilisation, memory and
  swap, with sparklines. "No disk figures available."
- **Storage:**
  - Sandboxes: the empty, idle text.
  - Proxy cache: 5.3 GiB, pointing to System → Caches and Settings → Caches.
  - CI history: 0 B of 64 MiB.
  - CI job cache: 1 KiB, 1 of 4096, 14 % hit rate, with "Empty the job
    cache".
  - CI artifacts: disabled, with its reason.
- **Budget:** "within budget cap", committed 0.0 of 14.0 GiB, cluster
  free, roomiest node. The auto overcommit line shows its effective
  factors and bounds.
- While a CI run was held, the CI sandboxes were folded behind their
  summary row.

### Caches (demo A)

- **Proxy cache:** apt 3.0 GiB, npm, Python, Go, Docker Hub and
  Playwright, each with a named Flush, then the whole-cache button
  (Bug 2).
- **Actions cache:** 20 entries, 3.0 GiB of 20.0 GiB (acme/widgets 2.0 GiB,
  acme/gadgets 1.0 GiB).
- **Flush the Actions cache of acme/gadgets** (throwaway demo entries)
  sent `POST /api/actions-cache/flush?repo=acme%2Fgadgets`. The panel went
  to "10 entries, 2.0 GiB", acme/gadgets 0 B, and held that through a
  reload.
- **No confirmation was asked**, as on 2026-10-29. This is by design:
  the panels say nothing is lost that a re-download or rebuild does not
  put back.

### CI (demo A)

- **Open and start:**
  - The rail's CI entry opens `/ci`.
  - "Start a run" → repo `acme/widgets`, branch `walk/held-1`, then
    "Load workflows" (one workflow, auto-selected), then "Start run". It
    followed to `/ci/80ea017428bd1570`, which survived a reload.
  - "Start run" stays disabled until a workflow is loaded.
- **Active sandboxes:**
  - Collapsed, they read "active sandboxes (1)" with "1 CI sandbox running
    · longest 2s (observe, held-1)".
  - Expanded, the table shows `c80ea01-0 · acme/widgets · walk/held-1 ·
    observe | Observability · 7s`.
  - The runs list has a "Search runs…" box and the Filter control.
- **Run overlay:**
  - A run opens from its repo button as an overlay at `/ci/<id>`.
  - Esc closes it to `/ci`. Back reopens it and Forward closes it again.
- **Cancel run:**
  - The prompt reads "Stop this run? The jobs in flight are told to give
    up and their sandboxes are destroyed. …".
  - Dismissing it left the run open. Accepting it marked the run
    cancelled ("1 job: 1 cancelled"), leaving only **Retry**.
- **Retry (every job)** on the cancelled run followed to a new
  `/ci/61a55f272a36f034`.
- **Success:** releasing the held step (`POST /api/e2e/ci/finish`) turned
  it green. A successful run offers no Retry, by design
  (`retryableRun`). Expanding the job shows its sandbox, its step and
  "show output".
- **Cache hit:** a later run of the same ref finished in 0 s with its job
  marked "cache hit".
- **Task's run:** `POST /api/ci/runs {taskId: "2"}`, then on `/tasks/2`,
  "Show the run" displays the errored run inline ("no workflow of
  acme/widgets answers push of branch grain/task-2 …").
- **Forgotten run id:** `/ci/00000000deadbeef` opens the overlay saying
  `no CI run "00000000deadbeef" -- a run is remembered for a while and
  then forgotten`. Its 404 is the expected answer.
- **Failure, errored, skipped and cache-hit states** came from one
  stubbed run (`/api/ci/runs/feedfacefeedface`, below). It showed
  **Retry** and **Retry failed jobs**.
  - Retry failed jobs posted `{"jobs":["observe"],"recheck":true,…}` to
    the real daemon and followed to the new `/ci/71d2b789a7d4266b`.
- **Shared fault:** the card reads "acme/widgets · CI / e2e … failing the
  same way on 3 branches", with its branches and common files. The
  fault-owner line was covered by the 2026-10-29 fix and its spec.

### Logs, Top

- **Logs:**
  - Searching "zzzz-no-such-line" shows "(no matching log lines)".
  - Searching "ci" filters the tail to matching lines.
  - Refresh works.
- **Top:** the process table (systemd, grain-daemon, node), busiest first.

### Pipeline (demo A)

- **Pause:** typing a reason and pressing "Pause pipeline" asks "Pause the
  pipeline? Grain will stop starting new agent and CI runs. Runs already
  in flight will continue."
  - Dismissing it sent nothing, and the bell stayed at 2.
  - Accepting it took the bell to 3. The new alert is manual mode,
    "Paused — only tasks you Run now will start", with the reason, since
    when, and Resume now.
- The pause and its reason survived a reload.
- **Resume:** "Resume pipeline" asks "Resume the pipeline? …". Accepting
  it cleared the alert (bell back to 2).

### Power (demo A, POSTs intercepted)

| Control | Dismissed | Accepted (stubbed answer) | UI |
| :--- | :--- | :--- | :--- |
| Restart grain | nothing sent | 200 | "Restart requested. grain is coming back up; …" |
| Reboot host | nothing sent | connection reset | "Reboot requested. This UI is unreachable until the host machine comes back up." |
| Shut down host | nothing sent | 500 `stub: shutdown refused` | the error, as a toast (gone after about 5 s) |

### Metrics

- **System → Metrics (demo A):**
  - Weekly release notes (Week 43, hero numbers, the per-repo line, the
    empty write-up note).
  - Versions, with `running` on v1.2.0.
  - Throughput: 18 filed, 9 completed, sparklines and a scaling note.
  - The latency table.
- **Windows:** `?window=90d` reads "8/1/2026 → 10/30/2026 · 90d 0h" and
  survives a reload.
- **Task navigation:** the "task 2" button opens `/tasks/2`, and Back
  returns to `/system/metrics`.
- **From a dashboard** ("Walk dashboard", seeded with
  `POST /api/boards`, holding a 30-day throughput panel narrowed to
  acme/widgets):
  - The panel shows 13 filed and 3 completed, titled "Measured over the
    tasks in acme/widgets.".
  - Its **Open** went to
    `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`, which
    reads "repo is any of acme/widgets · … · 30d 0h · Measured over the
    tasks in acme/widgets.".
  - That survived a reload, and Back returned to `/boards/board-5`.
- **Failure:**
  - `/api/metrics` answered 500 (stub). The page said "The report could
    not be loaded (stub: metrics scan failed). Refresh to try again.".
  - The dashboard panel said "The metrics report could not be loaded."
- **Loading:** with `/api/metrics` held for 6 s, a progressbar shows
  until the report lands.
- **Empty:** `?f=repo:any:nobody/nothing` reads "Measured over the tasks
  in nobody/nothing." and shows zeros.

### Settings → Upgrade (demo B, with the upgrader wired)

- **Reached from:** the build stamp, and from `/system/upgrade` (which
  redirects).
- **Check mode:** "What grain does after an upgrade lands is set in
  Settings → General → Updates.". The pointer is a button
  (`upgrade-check-settings-link`), not a control on this page.
- **Image pane:**
  - The description names image mode.
  - The in-flight warning reads "2 runs are in flight … 2 would be lost
    (antigravity 2)".
  - "Will pull localhost:5055/x/grain".
  - The demo sends no `running`, so there is no "Now running" line. The
    build stamp still names v1.2.0.
- **Branch picker:**
  - It lists Standard branches (latest, candidate (alias: rc), main, each
    explained) and Released versions (v1.3.0, v1.2.0, v1.1.0). Those are
    the stub registry's tags filtered by `-upgrade-allowed-branches`, so
    `v1.3.0-rc.1` and `sha-deadbeef` are dropped.
  - Picking v1.3.0 gave "Will pull localhost:5055/x/grain:v1.3.0".
  - Typing an image gave "Will pull localhost:5055/other/grain:v9.9.9".
    Clearing the image restored the default.
- **Release notes:** picking a version asks
  `/api/upgrade/release-notes`. That really runs `docker pull` of the
  tag; against the stub registry it fails and the pane shows "Could not
  read the release notes v1.3.0 carries: …" (a 422). Expected.
- **Upgrade, `POST /api/upgrade` intercepted:**
  - It opens "confirm upgrade", which repeats the at-risk runs and ends
    "Upgrade anyway?".
  - Cancel sent nothing.
  - Accepted against a 500, it showed `stub: registry refused the pull`.
    The body was `{"branch":"v1.3.0","afterTasksFinish":false}`.
  - Accepted against a 200 `phase: pulling`, it showed the pulling step's
    detail.
- **Database panel:** SQLite → PostgreSQL DSN, Test connection, Migrate.
  Not pressed.

### Alerts bell

- **Agent/connection pause** (`-agent-paused`): as under Health. Wording,
  scope, until-time, Resume now and Open connection are all there.
- **Pipeline pause / manual mode:** see Pipeline. It appears and clears.
- **Reconciler-down, failing config-repo sync and host sandboxes**, by
  injection into `/api/config` on demo B:
  - The bell went to "Alerts: 6 active, critical", grouped
    Deployment-first.
  - "Settings are not being exported — Settings have not been exported
    to acme/grain-config since 10/30/2026, 12:00:30 PM: git push origin
    main: remote: Permission denied." Its **Config repo** button opened
    `/settings/config-repo`.
  - "Reconcile loop stopped" (Bug 1).
  - "No isolation: runs execute on this host", with its impact line.
  - All three were listed on Health too.
  - Removing the injection cleared all three (bell back to 3).
- **Setup** (demo C):
  - "2 setup steps left · Still to do: connect a git host, add a model
    connection." "Finish setup" opened `/settings/git`, the first step.
  - With the deployment row's repositories also stripped (stub): "3
    setup steps left … add a repository". The impact line still names
    only git and models, deliberately: a missing repository blocks
    nothing (`alerts.js`).
- **Red base branch:** `POST /api/e2e/base-branch/red {"branch":"main"}`
  on demo B raised no bell row within 8 s. That demo has no `-reconcile`
  loop to file the notification. The repair alert itself was covered on
  2026-10-29 (its Bug 3) and by `alerts.test.js`, and was not repeated.
- **See all:** opens System → Health on desktop and `/system/health` on a
  phone.

### Standing banners (demo B)

| Width | Install notice alone | With the disconnected banner raised |
| :--- | :--- | :--- |
| 1440 | notice y=0, 48 px; header and rail start at y=48 | disconnected y=0, 48 px; notice y=48, 48 px; header and rail at y=96 |
| 390 | notice y=0, 128 px; phone bar at y=128 | disconnected y=0, 108 px; notice y=108, 128 px; phone bar at y=236 |

- Nothing overlaps.
- The disconnected banner ("This deployment cannot be reached: …") was
  raised by aborting `/api/tasks` and `/api/config`. It cleared about 6 s
  after the routes were restored, and the layout went back to the
  install-notice-only figures.

### Console and network

No page errors and no unexpected console errors on any surface. These
were seen and expected:

- `ERR_ABORTED` on `/api/tasks` when navigating mid-poll.
- The 401 on `/` before signing in.
- The 404 for the forgotten run id.
- The 404 for `/api/repos/acme/widgets/github-branches` on demo A, which
  wires no GitHub.
- The 422 release-notes read against the stub registry.
- The stubbed 500s and connection reset.

## 3. Intercepted or stubbed routes

| Route | Method | Why |
| :--- | :--- | :--- |
| `127.0.0.1:5055` (a Node HTTP server) | GET `/v2/x/grain/tags/list`, HEAD `/v2/x/grain/manifests/*` | A loopback registry for `-upgrade-image`. It lists eight tags and answers a fixed digest; there is no registry in this sandbox. |
| `/api/upgrade` | POST | Never upgrade. Answered 500 and then 200 (`phase: pulling`). |
| `/api/host/restart`, `/api/host/reboot`, `/api/host/shutdown` | POST | Never touch the host. Answered 200, a connection reset and a 500. |
| `/api/config` | GET | The real answer plus `reconcilerDown`, `stateRepoSyncFailure` and `hostSandboxes`, which only a live daemon sets. Also, on demo C, the deployment row with its repositories and git credential stripped, to raise the "add a repository" step. Aborted, with `/api/tasks`, to raise the disconnected banner. |
| `/api/ci/runs/feedfacefeedface` | GET | One run carrying failure, errored, skipped and cache-hit jobs, which the demo's single always-green workflow cannot produce. The Retry POSTs it led to were real. |
| `/api/metrics` | GET | Answered 500 for the failure states, and held 6 s for the loading state. |

Everything else was the demos' own state, driven through the UI. Two
pieces of setup went through the API: `POST /api/boards` to seed the
dashboard, and `POST /api/ci/runs {taskId}` to give a task its own run.
The demos' e2e endpoints were also used (`/api/e2e/ci/finish`,
`/api/e2e/base-branch/{red,green}`).

## 4. Tests run

- `npx vitest run src/components/AlertsButton.test.jsx src/alerts.test.js src/components/CachesPage.test.jsx src/components/PackageCachePanel.test.jsx src/components/SandboxHealthPage.test.jsx src/App.test.jsx src/components/SystemPage.test.jsx`:
  200 passed.
- `npx playwright test e2e/deployment-operations-alert-power-flush-names.spec.js e2e/system-tabs.spec.js e2e/system_workflows.spec.js e2e/deployment-operations-see-all-fault-owner.spec.js`:
  7 passed.
- `make ui-screenshots PAGES=/system/caches,/system OUT=screenshots/after`
  covered desktop and phone, light and dark. The alert row with its
  Power button was also shot at 390 px. Screenshots are not committed.

## 5. Follow-ups

- **Health's "Saved, not applied until restart" row links only to Power.**
  It does not link to the Settings section holding each pending setting.
  The brief, and `settings-panel.md` §2.1's "each with its link", expect
  one. That needs a setting-key → section map the UI does not have yet
  (each section only tags its fields with `data-setting-id`). Proposed as
  its own task.
