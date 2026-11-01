# Deployment Operations Walkthrough — 2026-11-01

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners. Everything was walked in a real browser against real `grain demo`
servers. This run concentrated on what changed since the 2026-10-30 run
(`2026-10-30-deployment-operations.md`):

- Health's pending-restart and not-runnable rows now link to their
  Settings cards (#2df36db6). This run clicked every restart-only key.
- Settings shrank to Models and Sandboxes (#3d26e211). This run checked
  that each link into Settings from these screens still lands on a live
  section.
- The red-base-branch alert, raised for real on a `-reconcile` demo.
- Persistent sandboxes with a running commit and a last refresh,
  connected remote executors, and long Sandboxes tables at 390 px.

## Summary

- **Commit tested:** `a71723af` (`main`, the merge of #336), then the fix
  below on `grain/049f1e09/task-2091`.
- **Date:** 2026-11-01
- **Environment:**
  - Debian 12, `linux/amd64`, 7 vCPU; Go 1.26.10; Node 20.19.0.
  - `grain` was built with `go build -tags grain_e2e -ldflags "-X …/pkg/version.Release=v1.2.0" ./cmd/grain`
    over the Vite production bundle (`npm ci && npm run build`), the same
    way `ui/e2e/global-setup.js` builds it.
  - Three demos, each over its own throwaway embedded SQLite store
    (`TMPDIR` set to a scratch directory). Each was signed in by typing an
    access code (`-access-code-file`) into the "Enter access code" page:
    - **A:** `grain demo -observability` on `:4301`. This has the real
      `pkg/ci` service, host-directory CI sandboxes, the fake proxy and
      Actions caches, logs and `top`.
    - **B:** `grain demo -github-sim -fake-ci -base-branch-ci -agent-paused -host-mode-install -notifications -reconcile 5s -upgrade-image localhost:5055/x/grain`
      on `:4302`. `-upgrade-image` wires the real image-mode upgrader. It
      never restarts anything.
    - **C:** `grain demo -unconfigured` on `:4303`.
    - A fourth, `-observability` on `:4304`, ran the fixed build to check
      the fix.
  - Playwright Chromium (`chromium-1234`, headless) from
    `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900 and
    390×844. Each walk was a Node script driving `playwright`'s
    `chromium` through visible controls (role, label and test-id locators,
    clicks, typing, keyboard, native dialogs). Each script recorded
    console errors, page errors, failed requests and HTTP ≥ 400
    responses.
  - There was no Gemini key in this sandbox (`/home/debian/.gemini-api-key`
    is absent), so nothing used a model.

## 1. Bugs found and fixed

### Bug 1: the empty Persistent sandboxes panel's example named grain's retired repository

- **Surface:** System → Sandboxes, the Persistent sandboxes panel
  (`ui/src/components/PersistentSandboxesPanel.jsx`).
- **Reproduction:** on demo A, open `/system/sandboxes` with no
  persistent sandbox defined. The panel reads "To add one, ask the
  director to set one up — e.g. 'set up a staging environment for
  bwsalmon/grain on main'".
  - `bwsalmon/grain` is grain's own old repository, retired on
    2026-10-28 when core moved to `ai-on-gke/grain`.
  - No deployment's director can stage it. An operator copying the
    sentence would file work against a repository that is not theirs.
  - The Standing sandboxes panel beside it uses a placeholder
    (`ghcr.io/acme/worker:latest`).
- **Fix:** the example is now "'set up a staging environment for
  acme/web on main'", a placeholder like its neighbour's.
- **Coverage:**
  - `PersistentSandboxesPanel.test.jsx` checks the new example, and that
    the panel names no `bwsalmon`.
  - New `e2e/deployment-operations-persistent-example.spec.js` runs over
    the `-observability` demo at 1440×900 and 390×844. It checks the same
    text on `/system/sandboxes`, and by bounding box that the panel stays
    inside the viewport.
  - At 390 px the panel measured x=12, width=366.

## 2. Walkthrough and outcomes

### Navigation, URLs, history (demo B)

- The header rail's System button opens `/system` with Health selected.
  The tabs are health, logs, sandboxes, caches, top, pipeline, metrics
  and power.
- Each tab clicked, then reloaded, kept its URL and its selected tab:
  `/system/logs`, `/system/sandboxes`, `/system/caches`, `/system/top`,
  `/system/pipeline`, `/system/metrics`, `/system/power`, `/system`.
- Back went from `/system` to `/system/power`, and Forward returned.
- Old links:
  - `/admin/logs?q=ci` → `/system/logs`. Logs keeps no search in its
    URL, so there is no query to carry.
  - `/sandboxes` → `/system/sandboxes`.
  - `/caches` → `/system/caches`.
  - `/metrics?window=30d&repo=acme%2Fwidgets` →
    `/system/metrics?f=repo:any:acme/widgets&window=30d`.
  - `/system/upgrade` → `/settings/upgrade`.
  - `/system/health` → `/system`.
  - `/system/bogus` → `/system`.
- The build stamp ("v1.2.0 · a71723a · 2026-11-01 03:38Z") opens
  `/settings/upgrade`.
- **Phone (390×844):**
  - Every Sandboxes layout (many rows, folded, expanded) gave
    `scrollWidth == clientWidth == 390`.
  - No element ran past the right edge without a scrolling or clipping
    ancestor.
  - "See all on System → Health" opens `/system/health`.

### Health (demo B)

- **What it lists:**
  - the host-mode install notice (setup.sh command, "claude: no host
    binary …", Migration docs to `docs/design/cli-run-location.md`);
  - the GitHub API budget with its per-caller table;
  - the seeded notifications (Approve/Reject/Acknowledge/Investigate);
  - the connection pause ("Usage limit reached (antigravity)", connection
    Gemini 2.5 Pro, "Until 9:41:45 AM (about 5h 58m)", with Resume now
    and Open connection).
- **Pending-restart and not-runnable settings** (`/api/settings`
  intercepted, below):
  - `pendingRestart` was set to every key in `restartRequired`, 19 of
    them. The row named all 19, each as a link.
  - Every link was clicked in turn.
    - `sandboxBackend` went to `/settings/sandboxes#backend-and-size`.
    - The 16 `k8s*` keys went to `/settings/sandboxes#kubernetes`.
    - `githubHost` and `githubInsecureHttp` went to
      `/settings/credentials#credentials`.
  - In each case the card was visible and on screen, and it held the
    field (`[data-setting-id=<key>]`, count 1).
  - The not-runnable row read "Stored, but not runnable: GitHub host
    (stored x, running y). …", linked the same way.
- **Resume now** on the connection pause took the bell from 4 to 3. The
  pause was gone from Health and stayed gone through a reload.

### Sandboxes (demo A)

- **Live data:**
  - Host load, utilisation, memory and swap, with sparklines. "No disk
    figures available."
  - Storage: sandboxes, proxy cache 5.3 GiB, CI history 0 B of 64 MiB, CI
    job cache with "empty the job cache", CI artifacts disabled.
  - Budget: "within budget cap", committed 4.0 of 14.0 GiB. Pools: agent
    1, CI 1. Cluster free, roomiest node.
  - The auto overcommit line, with its factors, bounds, PSI, the last
    adjustment and reconciliation "in sync".
- **CI fold:** a CI run held in flight (started from `/ci`) plus
  `POST /api/e2e/sandboxes/other` gave "ci sandboxes (1)" folded under
  its summary row.
- **Long table** (`/api/sandboxes` padded with 30 CI and 12 agent rows,
  below):
  - "ci sandboxes (31)": 18 rows folded, 49 expanded.
  - Long task slugs, an OOM-killed row and a health-probe error row all
    drew.
  - Nothing overflowed at 1440 or 390.
- **Persistent sandboxes** (injected):
  - `staging-widgets` read "acme/widgets@main · 0123456789ab · on-push ·
    refreshed 10m ago: main moved", running 2h 0m, 1 restart, port
    18080, with restart/stop/rebuild/logs/Raw.
  - A backoff row showed its error, wrapped.
- **Empty:** the persistent panel's explanation (Bug 1), then "Nothing in
  flight, so no sandboxes exist."
- **Model servers:** `runner-infra-worker-01` and
  `runner-frontend-worker-01`, each with address, frameworks, uptime and
  active jobs. They are listed in every state, including disabled and
  failing.
- **Disabled** (`enabled: false`): "Not available: this deployment has no
  sandbox pool or host stats configured."
- **Failure** (500): "Sandbox health could not be read (500 Internal
  Server Error). This pane tries again every few seconds."

### Caches (demo A)

- **Proxy cache:** six named Flush buttons, then "Flush the whole proxy
  cache".
- **Actions cache:** "Flush the Actions cache of acme/widgets", "… of
  acme/gadgets", then "Flush the whole Actions cache". The names were
  distinct (the 2026-10-30 fix holds).
- **Flushing (throwaway demo entries):**
  - "Flush npm packages" and "Flush Go modules" sent
    `POST /api/package-cache/flush?kind=npm` and `?kind=go`.
  - After a reload, "Go modules: 0 packages, 0 B" stayed, and its Flush
    was disabled.
- **No confirmation was asked**, as on 2026-10-29 and 2026-10-30. This
  is by design: the panels say nothing is lost that a re-download or
  rebuild does not put back.

### CI (demo A)

- **Start a run** (an inline form, not a dialog):
  - Repo select → `acme/widgets`, Branch `walk/held-1`, then "Load
    workflows" ("Observability — .grain/workflows/observability.yml").
  - "Start run" stays disabled until a workflow is loaded.
  - It followed to `/ci/d9e470de7dcb0ec4`, which survived a reload. Esc
    went back to `/ci`.
- **Active sandboxes:**
  - Collapsed, they read "active sandboxes (1)" with "1 CI sandbox running
    · longest 21s (observe, held-1)".
  - Expanded, the row reads `cd9e470-0 · acme/widgets · walk/held-1 ·
    observe Observability`.
- **Filtering:** "Search runs…" with `walk/states` left one run row.
- **Shared fault:** "acme/widgets · CI / e2e (Run go test ./...) failing
  the same way on 3 branches right now", with the branches and common
  files. With `ownerTaskId` injected, it adds "Assigned to task #2
  (feature/audit-log)".
- **States** (the stubbed run `feedfacefeedface`, below):
  - Its jobs showed failure, errored, success with "cache hit", skipped
    and pending, summed as "5 jobs: 1 failure, 1 errored, 1 skipped, 1
    success, 1 pending".
  - The run offered Retry and Retry failed jobs.
- **Retry failed jobs** posted `{"jobs":["observe","build"],"recheck":true,…}`
  to the real daemon, which answered 400. The stub's `build` job is not
  in the demo's workflow, so the refusal is expected.
- **Retry** posted the run's repo, ref, commit and event, and followed to
  the new `/ci/c4dcc7f0f423e4f8`. That survived a reload, running.
- **Cancel run** on it:
  - The prompt reads "Stop this run? The jobs in flight are told to give
    up and their sandboxes are destroyed. Optionally, say why …".
  - Accepting it marked the run cancelled, leaving Retry.
- **Forgotten run id:** `/ci/00000000deadbeef` says `no CI run
  "00000000deadbeef" -- a run is remembered for a while and then
  forgotten`. Esc returns to `/ci`.

### Logs, Top (demo A)

- **Logs:**
  - The source select, "Search logs" and Refresh sit in the page header
    rail.
  - "zzzz-no-such" gives "(no matching log lines)". "merge-queue" leaves
    only the merge-queue lines.
  - The log view is a named tab stop (`tabindex=0`, "Log lines").
- **Top:** the process table (systemd, grain-daemon, node), busiest first.

### Pipeline (demo A)

- **Pause:**
  - With reason "walkthrough pause", "Pause pipeline" asks "Pause the
    pipeline? Grain will stop starting new agent and CI runs. Runs already
    in flight will continue."
  - Dismissing it sent nothing, and the bell stayed at 2.
  - Accepting it sent `POST /api/pipeline-pause {"reason":"walkthrough pause"}`
    and took the bell to 3. The new alert reads "Paused — only tasks you
    Run now will start", with the reason, since when, and Resume now.
- The pause and its reason survived a reload.
- **Resume:** "Resume pipeline" asks "Resume the pipeline? Grain will
  start dispatching queued agent and CI runs again.". Accepting it sent
  `DELETE /api/pipeline-pause` and took the bell back to 2.
- "contact running agents (2)" is present but was not pressed.

### Power (demo A, POSTs intercepted)

| Control | Dismissed | Accepted (stubbed answer) | UI |
| :--- | :--- | :--- | :--- |
| Restart grain | nothing sent | 200 | "Restart requested. grain is coming back up; this UI is unreachable for a few seconds and then picks up where it was." |
| Reboot host | nothing sent | connection reset | "Reboot requested. This UI is unreachable until the host machine comes back up." |
| Shut down host | nothing sent | 500, text body | a "500 Internal Server Error" toast |

The real handlers (`pkg/ui/host.go`) answer failures as JSON
`{"error": …}`, which the toast shows verbatim. The plain-text stub only
shows that a body which is not JSON falls back to the status line.

### Metrics

- **System → Metrics (demo A):**
  - Release notes: Weekly (Week 43 numbers, the per-repo line, the empty
    write-up note) and Versions (v2.4.0 … v1.2.0, `running` on v1.2.0).
  - Throughput: 18 filed, 9 completed, with sparklines.
  - The latency table, with its small-n asterisks.
  - Capacity, backlog ("Oldest waiting task: task 2"), and GitHub CI.
  - No horizontal overflow at 1440.
- **Windows:** `?window=90d` reads "8/3/2026 … → 11/1/2026 … · 90d 0h".
- **Narrowing:** `?f=repo:any:acme/widgets&window=7d` reads "Measured
  over the tasks in acme/widgets." (12 filed) and survives a reload.
- **Tasks:** the "task 2" button opens `/tasks/2`. Back returns to the
  narrowed URL.
- **Failure:** `/api/metrics` answering 500 (stub) gives "The report
  could not be loaded (stub: metrics scan failed). Refresh to try
  again."
- **Loading:** with the answer held 5 s, a progressbar shows, and it is
  gone once the answer lands.
- **Empty:** `?f=repo:any:nobody/nothing` reads "Measured over the tasks
  in nobody/nothing." and shows zeros.
- **From a dashboard** ("Ops walk dashboard", seeded with
  `POST /api/boards` and deleted afterwards, holding a metrics panel
  narrowed to `f=repo:any:acme/widgets&f=created:within:30`):
  - The panel shows "13 filed 3 completed".
  - Its Open went to
    `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`, which
    reads "… · 30d 0h · Measured over the tasks in acme/widgets.".
  - That survived a reload, and Back returned to `/boards/board-5`.
  - With `/api/metrics` failing, the panel says "The metrics report could
    not be loaded."

### Settings → Upgrade (demo B, with the upgrader wired)

- **Reached from:** the build stamp, and from `/system/upgrade` (which
  redirects).
- **Check mode:** "What grain does after an upgrade lands is set in
  Settings → General → Updates.". The pointer opens `/settings/general`
  with the Updates card on screen (y 280–762 of 900) and focus inside it.
- **Image pane:**
  - The in-flight warning reads "2 runs are in flight … 2 would be lost
    (antigravity 2)".
  - "Will pull localhost:5055/x/grain".
- **Branch picker:**
  - It lists Standard branches (latest, candidate (alias: rc), main, each
    explained) and Released versions (v1.3.0, v1.2.0, v1.1.0).
    `v1.3.0-rc.1` and `sha-deadbeef` from the stub registry are dropped.
  - Picking v1.3.0 gave "Will pull localhost:5055/x/grain:v1.3.0".
  - Typing an image gave "Will pull localhost:5055/other/grain:v9.9.9".
    Clearing it restored the default.
- **Release notes:** picking a version asks
  `/api/upgrade/release-notes`. That really runs `docker pull` against
  the stub registry, which fails, and the pane shows "Could not read the
  release notes v1.3.0 carries: …" (a 422). Expected.
- **Upgrade, `POST /api/upgrade` intercepted:**
  - It opens "confirm upgrade", which repeats the at-risk runs and ends
    "Upgrade anyway?".
  - Cancel sent nothing.
  - Accepted against a 500, the body was
    `{"branch":"v1.3.0","afterTasksFinish":false}` and the toast read
    `stub: registry refused the pull`.
  - Accepted against a 200 running status (built on the real `GET
    /api/upgrade` answer), the pane read "upgrading… Branch v1.3.0 Status
    running … Step 1 of 1: pull ◐ pull running …".
- **Database panel:** SQLite → PostgreSQL DSN, Test connection, Migrate.
  Not pressed.

### Alerts bell

- **Agent/connection pause** (`-agent-paused`):
  - Wording, scope, until-time, Resume now and Open connection are all
    there.
  - Open connection → `/settings/models`. Resume now clears it (see
    Health).
- **Pipeline pause / manual mode:** see Pipeline. It appears and clears.
- **Red base branch:** `POST /api/e2e/base-branch/red {"branch":"main"}`
  on demo B (now with `-reconcile 5s`) raised a real notification within
  a poll: "acme/widgets main is red — grain CI on `main` acme/widgets is
  confirmed red: at 1135e42186b5 ("failure: test"). grain has filed no
  repair for it …", with Acknowledge and Investigate.
- **Red-base repair** (`/api/tasks` intercepted to give task 2 a
  `redBaseBranch` and pull request; the demo files no repair task):
  - "main is red — repair #2 … is awaiting submit. Other work targeting
    main waits until the repair merges." It offers Open #2, Submit and
    Force merge.
  - Open #2 → `/tasks/2`.
- **Reconciler-down, failing config-repo sync and host sandboxes** (by
  injection into `/api/config`):
  - The bell went to "Alerts: 8 active, critical", grouped
    Deployment-first.
  - "Settings are not being exported — Settings have not been exported
    to acme/grain-config since …: git push origin main: remote:
    Permission denied." Its Config repo button opened
    `/settings/config-repo`.
  - "Reconcile loop stopped", with Power.
  - "No isolation: runs execute on this host", with its impact line.
  - Removing the injection cleared all of them (bell back to 4).
- **Setup** (demo C):
  - "2 setup steps left · Still to do: connect a git host, add a model
    connection." "Finish setup" opened `/settings/git`, a live section
    with Add connection.
  - With the deployment's `repos` and `targetRepos` stripped (stub), it
    adds "add a repository". The impact line still names only git and
    models, deliberately.
- **See all:** opens `/system` on desktop and `/system/health` on a
  phone.
- **Keyboard:**
  - The bell is reached by Tab (22 presses from the top of
    `/system`).
  - Enter opens the popover with focus inside it.
  - Escape closes it and returns focus to the bell.

### Standing banners (demo B)

| Width | Install notice alone | With the disconnected banner raised |
| :--- | :--- | :--- |
| 1440 | notice y=0, 48 px; header and rail start at y=48 | disconnected y=0, 48 px; notice y=48, 48 px; header and rail at y=96 |
| 390 | notice y=0, 128 px; phone bar at y=128 | disconnected y=0, 108 px; notice y=108, 128 px; phone bar at y=236 |

- Nothing overlaps, and the page below starts where the bar ends.
- The disconnected banner was raised by refusing every `/api/` request.
  It appeared 4.3 s after the outage began.
- After a 12 s outage it cleared 10.4 s after the routes were restored.
  Then the layout went back to the install-notice-only figures.
- **Slow recovery (not fixed, §5):**
  - The main poll backs off while the daemon is unreachable, at 3 s,
    6 s and 12 s, capped at 30 s (`pollBackoff`, `App.jsx`).
  - `/api/restart-watch` keeps polling every 3 s and succeeded 1.1 s
    after the restore. Even so, the banner stayed up until the backed-off
    poll's next tick.

### Console and network

No page errors and no unexpected console errors on any surface. These
were seen and expected:

- The 401 on `/` before signing in.
- `ERR_ABORTED` on `/api/tasks` when navigating mid-poll.
- The 404 for the forgotten run id.
- The 404 for `/api/repos/acme/widgets/github-branches` on demo A, which
  wires no GitHub.
- The 422 release-notes read against the stub registry.
- The 400 for the stubbed run's nonexistent job.
- The stubbed 500s, the connection reset and the refused connections.

## 3. Intercepted or stubbed routes

| Route | Method | Why |
| :--- | :--- | :--- |
| `127.0.0.1:5055` (a Node HTTP server) | GET `/v2/x/grain/tags/list`, HEAD/GET `/v2/x/grain/manifests/*` | A loopback registry for `-upgrade-image`. It lists nine tags and answers a fixed digest; there is no registry in this sandbox. |
| `/api/upgrade` | POST | Never upgrade. Answered 500, then 200 with the real status plus `phase: running` and one pull step. |
| `/api/host/restart`, `/api/host/reboot`, `/api/host/shutdown` | POST | Never touch the host. Answered 200, a connection reset and a 500. |
| `/api/settings` | GET | The real answer with `pendingRestart` set to every `restartRequired` key and one `unrunnable` entry, which only a daemon running with different settings reports. |
| `/api/config` | GET | The real answer plus `reconcilerDown`, `stateRepoSyncFailure` and `hostSandboxes`, which only a live daemon sets. On demo C, the deployment's `repos`/`targetRepos` emptied. |
| `/api/tasks` | GET | Task 2 given `redBaseBranch: main` and a pull request, because the demo files no repair task. |
| every `/api/` route | any | Refused (`connectionrefused`) for 12 s to raise the disconnected banner. |
| `/api/sandboxes` | GET | Padded with 42 rows and two persistent sandboxes; emptied; `enabled: false`; 500. Demo A has two live sandboxes and no persistent ones. |
| `/api/ci/runs`, `/api/ci/runs/feedfacefeedface` | GET | One extra run carrying failure, errored, skipped, cache-hit and pending jobs, which the demo's single always-green workflow cannot produce, and an `ownerTaskId` on the shared fault. The Retry and Cancel POSTs it led to were real. |
| `/api/metrics` | GET | Answered 500 for the failure states, and held 5 s for the loading state. |

Everything else was the demos' own state, driven through the UI. Setup
through the API: `POST /api/boards` (deleted afterwards) to seed the
dashboard, and the demos' e2e endpoints (`/api/e2e/sandboxes/other`,
`/api/e2e/base-branch/red`).

## 4. Tests run

- `npx vitest run src/components/PersistentSandboxesPanel.test.jsx`:
  11 passed.
- `npx playwright test e2e/deployment-operations-persistent-example.spec.js e2e/deployment-operations-alert-power-flush-names.spec.js e2e/system-tabs.spec.js`:
  5 passed. The new spec failed before the UI bundle was rebuilt with the
  fix.
- `make ui-screenshots PAGES=/system/sandboxes DEMO_ARGS=-observability`
  covered desktop and phone, light and dark. A 390 px element crop of
  the panel was also taken. Screenshots are not committed.

## 5. Follow-ups

- **The disconnected banner outlives the outage by up to the poll
  backoff (≤ 30 s).** This bites most right after Power → Restart grain,
  whose own message promises "a few seconds". `/api/restart-watch`
  already sees the daemon back within 3 s. Proposed as its own task
  rather than changed here: it touches the app-wide poll loop.
