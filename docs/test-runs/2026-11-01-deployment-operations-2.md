# Deployment Operations Walkthrough — 2026-11-01 (second run)

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners. Everything was walked in a real browser against real `grain demo`
servers. This run builds on `2026-10-30-deployment-operations.md` and
covers what that run left undriven or follow-up work has since changed.
The same day's earlier run is `2026-11-01-deployment-operations.md`
(task 2091); this one is task 2239:

- the Health rows' links into Settings, added since as the follow-up that
  run filed;
- persistent sandboxes' running commit and last refresh;
- the shared fault's owner task;
- the red base branch, raised and cleared by the demo's real reconcile
  loop;
- the "add a repository" setup step;
- the Upgrade pane's in-progress steps.

- **Commit tested:** `d9b4f615` (`main`, the merge of #344), then the fix
  below on `grain/049f1e09/task-2239`.
- **Date:** 2026-11-01
- **Environment:**
  - Debian 12, `linux/amd64`; Go 1.26; Node 20.19.0.
  - `grain` was built with `go build -tags grain_e2e -ldflags "-X …/pkg/version.Release=v1.2.0" ./cmd/grain`
    over the Vite production bundle (`npm ci && npm run build`), as
    `ui/e2e/global-setup.js` builds it.
  - Four demos, each over its own throwaway embedded SQLite store (`TMPDIR`
    set to a scratch directory). Each was signed in by typing its access
    code (`-access-code-file`) into the "Enter access code" page:
    - **A:** `grain demo -observability -staging` on `:4301`. This has the
      real `pkg/ci` service, the job cache, host-directory CI sandboxes,
      the fake proxy and Actions caches, `top`, and two persistent
      (staging) sandboxes.
    - **B:** `grain demo -github-sim -fake-ci -base-branch-ci -reconcile 2s -agent-paused -host-mode-install -notifications -upgrade-image localhost:5055/x/grain`
      on `:4302`. Adding `-reconcile 2s` this time let the real reconcile
      loop raise and clear the red-base-branch alert.
    - **C:** `grain demo -unconfigured` on `:4303`.
    - **D:** the fixed build, `grain demo` on `:4304`, for the after
      screenshots.
  - Playwright Chromium (`chromium-1234`, headless) from
    `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900, 700×700
    and 390×844, in light and dark.
    - Each walk was a Node script driving `playwright`'s `chromium` through
      visible controls: role and label locators, clicks, typing, Tab and
      Enter, native dialogs.
    - Each script recorded console errors, page errors, failed requests
      and HTTP ≥ 400 responses.
  - There was no Gemini key in this sandbox (`/home/debian/.gemini-api-key`
    is absent), so nothing used a model.

## 1. Bug found and fixed

### Health's restart-pending and not-runnable links opened the card, not the setting

- **Surface:** System → Health's "Saved, not applied until restart" and
  "Stored, but not runnable" rows (`ui/src/components/system/HealthTab.jsx`,
  `ui/src/settingsIndex.js` `settingLocation`).
- **Reproduction:**
  1. On demo B, add `pendingRestart: ["githubHost"]` to `GET /api/settings`.
     The demo has no running daemon config to differ from what is stored,
     so this list is always empty there.
  2. Open `/system` at 1440×900.
  3. Click **GitHub host** in the "Saved, not applied until restart" row.
- **Result:**
  - The link goes to `/settings/credentials#credentials`, the top of the
    Legacy capabilities card.
  - The `githubHost` field is at y=1489 of a 900 px window (y=2305 of
    844 at 390 px), off screen.
  - Focus is on an unrelated button.
  - Poll interval only looked right because it is the first field of its
    card (`#polling`).
- **Expected:** the link lands on the setting it names, as a pick in
  Settings' own search already does (`SettingsPage.jsx` `pick`, which
  targets `#<field id>`).
- **Fix:**
  - `settingLocation` also returns `target`, the field's `data-setting-id`,
    and builds its `href` from it (`/settings/<section>#<id>`).
  - Health passes `target` to `onOpenSettings`.
  - SettingsPage already scrolls a `data-setting-id` hash into view, then
    focuses and highlights it. `settingsIndex.test.jsx` already checks that
    every field entry draws its `data-setting-id`.
- **After the fix**, the link goes to `/settings/credentials#githubHost`:
  - At 1440: the field is at y=429–548, and its input is focused.
  - At 390: the field is at y=369–527, and its input is focused.
  - Both hold in light and dark.
- **Coverage:**
  - `settingsPlan.test.js` › "maps each setting key to its Settings
    section, card and field".
  - `SystemPage.test.jsx`, updated to the field hrefs and `onOpenSettings`
    targets.
  - `e2e/deployment-operations-alert-power-flush-names.spec.js`, which now
    asserts each landing field is `toBeInViewport`.
  - New `e2e/deployment-operations-pending-restart-field.spec.js`. It
    asserts the GitHub host field is in the viewport and focused after the
    click, and again on a cold `/settings/credentials#githubHost` deep link.
    It fails without the fix (`Received: "/settings/credentials#credentials"`).

## 2. Walkthrough and outcomes

### Navigation, URLs, history (demo A)

- **Opening System:**
  - The header rail's System button opens `/system` with Health selected.
  - It is reachable by keyboard: Tab 30 times from the top of the page,
    then Enter opens `/system`.
- **Tabs:** each tab clicked, then reloaded, kept its URL and its selected
  tab: `/system/logs`, `/system/sandboxes`, `/system/caches`,
  `/system/top`, `/system/pipeline`, `/system/metrics` and `/system/power`.
  Health is the bare `/system`.
- **Back and Forward** stepped pipeline ⇄ metrics.
- **Old links:**
  - `/admin/logs` → `/system/logs`
  - `/sandboxes` → `/system/sandboxes`
  - `/caches` → `/system/caches`
  - `/metrics?window=30d&repo=acme%2Fwidgets` →
    `/system/metrics?f=repo:any:acme/widgets&window=30d`
  - `/system/upgrade` → `/settings/upgrade`
  - `/system/health` → `/system`
  - `/system/bogus` → `/system`
  - `/admin/logs?q=ci` → `/system/logs` without its query. That is
    consistent: Logs keeps its search in component state, never in the
    URL (`LogsPage.jsx`), so there is no query to carry.
- **The build stamp** ("v1.2.0 · d9b4f61 · 2026-11-01 05:05Z", an `<a>`)
  opens `/settings/upgrade`.
- **Phone (390×844):**
  - "Open navigation", then the drawer's System row, opens the section list
    (health … power, each with ›).
  - Every section opened from the list and survived a reload at
    `/system/<tab>`.
  - No section scrolls sideways (`scrollWidth == clientWidth == 390`), and
    no element runs past the right edge without a clipping or scrolling
    ancestor.
  - The phone bar's bell reads "Alerts: 2 active", and its "see all on
    system → health" opens `/system/health`.
- **Narrow window (700×700):** no page-level sideways scroll. The
  Sandboxes live table (1168 px) scrolls inside its own box (420 px), and
  the System pane scrolls vertically on its own.

### Health

- **Demo B lists:**
  - the host-mode install notice, with its Migration docs link to
    `github.com/ai-on-gke/grain/…/cli-run-location.md#migrating-a-host-mode-install`;
  - the GitHub API budget table;
  - the two seeded notifications;
  - the connection pause ("Usage limit reached (antigravity)", connection
    Gemini 2.5 Pro, "Until 11:07:56 AM (about 5h 56m)", with Resume now
    and Open connection).
- **Restart-pending row** (stub below): "Saved, not applied until restart:
  Poll interval, GitHub host, Kubernetes sandbox pod image."
  - Each setting is a link: `/settings/engine#pollInterval`,
    `/settings/credentials#githubHost`, `/settings/sandboxes#k8sSandboxImage`.
    Before the fix they were the card anchors (§1).
  - **Power** opens `/system/power`.
- **Not-runnable row:** "Stored, but not runnable: Poll interval (stored
  0s, running 30s). …" links the same way.
- **Bell popover, keyboard:** focus moves into the popover when it opens,
  Esc closes it with focus back on the bell, and Enter on the bell opens it.

### Sandboxes (demo A)

- **Host:** load, utilisation, memory and swap, with sparklines.
  "No disk figures available."
- **Storage:** sandboxes, proxy cache (5.3 GiB), CI history, CI job cache
  with "empty the job cache", and CI artifacts (disabled, with its reason).
- **Budget:** "within budget cap". Committed 0.0 of 14.0 GiB, then
  2.0 GiB, 2 vCPU and "Pools: CI: 1" while a run was held.
  - Cluster free and the roomiest node are shown.
  - The auto overcommit line shows its effective factors, bounds, targets,
    PSI, OOM kills and its recent adjustments.
  - Reconciliation reads "in sync".
- **CI sandboxes in the live table:** while a run was held they fold
  behind "ci sandboxes (1) · 1 CI sandbox · 2 vCPU / 2.0 GiB · longest 55s
  (cfc5cb4-0)" (`aria-expanded=false`). Expanding it shows the
  `cfc5cb4-0 · ci · host · ready` row with its load, memory and disk
  trends.
- **Persistent sandboxes:**
  - docs-preview is running, reached at `/staging/docs-preview/`.
  - grain-staging is restarting, "exited with status 1", with 4 restarts,
    on port 4280.
  - Each has restart, stop, rebuild and logs.
  - With `commit` and `lastRefresh` added (stub), the row reads "running
    1a2b3c4d5e6f · after_green" and "refreshed 2h ago: main moved; CI green
    at 1a2b3c4d".
  - With the list emptied (stub), it shows the explanation and the
    `tables/persistent_sandbox.json` pointer.
- **Error state:** `/api/sandboxes` answering 500 (stub) shows "Sandbox
  health could not be read (500 Internal Server Error). This pane tries
  again every few seconds."
- **Remote executors:** two model servers, with their address, frameworks,
  uptime and active jobs.

### Caches (demo A)

- **Proxy cache:** apt, npm, Python, Go, Docker Hub and Playwright, each
  with its own named Flush button, then "Flush the whole proxy cache".
- **Actions cache:** 20 entries, 3.0 GiB of 20.0 GiB, with per-repo
  buttons and "Flush the whole Actions cache".
- **Flushing throwaway entries:** "Flush the Actions cache of acme/gadgets"
  sent `POST /api/actions-cache/flush?repo=acme%2Fgadgets`. The panel went
  to "10 entries, 2.0 GiB", acme/gadgets 0 B, and held that through a
  reload.
- **No confirmation is asked, by design** (`PackageCachePanel.jsx`: "A
  flush needs no confirmation and gets none"), as on 2026-10-29 and 10-30.
- **Hit history:** neither cache records hits. `pkg/pkgcache` and
  `pkg/actionscache` report sizes only, so there is no hit history to
  show. Only the CI job cache has a hit rate, on Sandboxes and CI. See §5.

### CI (demo A)

- **Starting a run:**
  - The rail's CI entry opens `/ci`.
  - "Start a run": repo `acme/widgets`, branch `walk/held-1`. "Start run"
    stays disabled until "Load workflows" is pressed.
  - Starting it followed to `/ci/fc5cb4168d64093b`, which survived a reload.
  - Esc closes the overlay to `/ci`, Back reopens it, and Forward closes it.
- **Active sandboxes:**
  - Collapsed, they read "active sandboxes (1)".
  - Expanded: "1 CI sandbox running · longest 38s (observe, held-1)", then
    the table `cfc5cb4-0 · acme/widgets · walk/held-1 · observe |
    Observability · 38s`.
- **Search:**
  - "held-1" narrows the list to its run.
  - "zzz-nothing" gives "No CI run matches. 1 run is here …".
  - The search survived a reload.
- **Cancel run:**
  - The prompt reads "Stop this run? …". Dismissing it left the run open.
  - Accepting it gave `cancelled`, leaving only **retry**. That held
    through a reload.
- **Retry** (every job) followed to the new run `/ci/0451dbf7c390932c`.
- **Success:** releasing the held step (`POST /api/e2e/ci/finish`) made it
  `success`, with no Retry (by design). Expanding the job shows its
  sandbox, its step "Wait for the browser" and "show output", which reads
  "observed through the sandbox".
- **Forgotten run id:** `/ci/00000000deadbeef` shows `no CI run
  "00000000deadbeef" -- a run is remembered for a while and then forgotten`.
- **Mixed states** (stubbed run `feedfacefeedface`):
  - The row reads "5 jobs: 1 failure, 1 errored, 1 skipped, 1 success,
    1 pending · 1 cache hit".
  - The overlay shows each state, `exit 1` on the failed step, and **retry**
    and **retry failed jobs**.
  - Retry failed jobs posted
    `{"jobs":["observe","flaky"],"recheck":true,…}` to the real daemon.
    The daemon answered 400 because `flaky` is a job the stub invented and
    the workflow does not have. That is expected; the 10-30 run covered the
    successful follow-through with a real job name.
- **Shared fault:**
  - The card reads "acme/widgets · CI / e2e (Run go test ./...) failing
    the same way on 3 branches", with its branches and common files.
  - With `ownerTaskId: "4"` added (stub), it reads "Assigned to task #4
    (grain/task-4)", and the link opens `/tasks/4`.

### Logs, Top

- **Logs:**
  - Searching "zzzz-no-such-line" shows "(no matching log lines)".
  - Searching "demo" leaves 300 lines, every one containing "demo".
  - Refresh works. The source picker offers `daemon`.
- **Top:** the process table (systemd, grain-daemon, node), busiest first.

### Pipeline (demo A)

- **Pause:** the prompt reads "Pause the pipeline? Grain will stop
  starting new agent and CI runs. Runs already in flight will continue."
  - Dismissing it sent nothing (bell stayed at 2).
  - Accepting it took the bell to 3, with the manual-mode alert:
    "Paused — only tasks you Run now will start … — walkthrough pause",
    since 5:22:01 AM, with Resume now.
  - The pause survived a reload.
- **Resume:** "Resume pipeline?" asks "… start dispatching queued agent and
  CI runs again". Accepting it cleared the alert (bell back to 2).

### Power (demo A, POSTs intercepted)

| Control | Prompt | Dismissed | Accepted (stubbed answer) | UI |
| :--- | :--- | :--- | :--- | :--- |
| Restart grain | "Restart grain? Every task currently running is interrupted …" | nothing sent | 200 | "Restart requested. grain is coming back up; …" |
| Reboot host | "Reboot the host machine? …" | nothing sent | connection reset | "Reboot requested. This UI is unreachable until the host machine comes back up." |
| Shut down host | "Shut down the host machine? … stays down until somebody powers the machine back on …" | nothing sent | 500 `{"error":"stub: shutdown refused"}` | "stub: shutdown refused", as a toast, gone within about 4 s |

A plain-text 500 body shows as "500 Internal Server Error". That is
expected: the daemon's `writeError` always answers in JSON.

### Metrics

- **System → Metrics (demo A):**
  - Release notes (weekly, with the week picker "Week 43, 2026"), and
    versions v1.2.0–v1.10.0 with `running` on v1.2.0.
  - Throughput: 18 filed, 9 completed, with rates, sparklines and the
    scaling note.
  - The latency table, with `*` on low-n percentiles, and capacity,
    backlog and GitHub CI.
- **Window:** the "Window" combobox (named by `aria-labelledby`) offers
  last 24 hours, 7 days, 30 days and 90 days. Picking 90 days wrote
  `?window=90d`. `?window=7d` survives a reload.
- **Task navigation:** "task 2" opens `/tasks/2`, and Back returns to
  `/system/metrics?window=7d`.
- **From a dashboard:** a seeded panels board ("Walk throughput") holds a
  `metrics` panel narrowed to acme/widgets over 30 days.
  - The panel shows 13 filed and 3 completed.
  - Its **Open** went to
    `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`, which
    reads "repo is any of acme/widgets · Measured over the tasks in
    acme/widgets.".
  - That survived a reload, and Back returned to `/boards/board-6`.
  - A report board's throughput section has no Open. It is drawn over
    `/api/metrics` itself.
- **Failure:** `/api/metrics` answering 500 (stub) shows "The report could
  not be loaded (stub: metrics scan failed). Refresh to try again."
- **Loading:** with `/api/metrics` held for 5 s, a progressbar shows until
  the report lands.
- **Long values:** every count set to 98765432109 (stub). At 390 px the
  page still has `scrollWidth == 390`, and the long numbers end by x=281.
- **Empty:** `?f=repo:any:nobody/nothing` reads "Measured over the tasks
  in nobody/nothing." and shows zeros.

### Settings → Upgrade (demo B, with the upgrader wired)

- **Reached from** the build stamp and from `/system/upgrade`.
- **Check mode:** shown read-only. Its pointer
  (`upgrade-check-settings-link`) opens `/settings/general`.
- **In-flight warning:** "2 runs are in flight … 2 would be lost
  (antigravity 2)".
- **Image:** "Will pull localhost:5055/x/grain".
- **Branch picker:** latest, candidate (alias: rc), main, v1.3.0, v1.2.0
  and v1.1.0. `v1.3.0-rc.1` and `sha-deadbeef` are filtered out by
  `-upgrade-allowed-branches`. Picking v1.3.0 gives "Will pull
  localhost:5055/x/grain:v1.3.0".
- **Release notes:** `/api/upgrade/release-notes` answers 422 against the
  stub registry. Expected, as on 10-30.
- **Upgrade, `POST /api/upgrade` intercepted:**
  - "confirm upgrade" repeats the at-risk runs and ends "Upgrade anyway?".
  - Cancel sent nothing.
  - Accepted against a 500, it shows "stub: registry refused the pull".
    The body was `{"branch":"v1.3.0","afterTasksFinish":false}`.
  - Accepted against a 200 `phase: running` (with `GET /api/upgrade`
    answering the same), it draws "Step 1 of 2: pull (18s)", "◐ pull
    running … stub: pulling localhost:5055/x/grain:v1.3.0" and "○ restart
    pending".
- **Database panel:** "Currently running on SQLite. Upgrade this deployment
  to a PostgreSQL database …", with the DSN field. Not pressed.

### Alerts bell

- **Agent/connection pause** (`-agent-paused`): as under Health.
- **Pipeline pause / manual mode:** see Pipeline. It appears and clears.
- **Red base branch, real:** `POST /api/e2e/base-branch/red {"branch":"main"}`
  on demo B (now running `-reconcile 2s`).
  - The reconcile loop confirmed it red, re-ran the failing job, and raised
    `pipeline.base-red:acme/widgets@main`.
  - The bell went to 4, with "acme/widgets main is red — grain CI on `main`
    acme/widgets is confirmed red: at 4caf7a5933f9 ("failure: test"). grain
    has filed no repair for it …", with acknowledge and investigate.
  - `…/green` cleared the notification ("cleared notification …" in the
    log), and the bell went back to 3 without a reload.
- **Red-base repair, plus reconciler-down, failing config sync and host
  sandboxes**, injected into `/api/config` and one task in `/api/tasks`
  (stub):
  - The bell went to "Alerts: 7 active, critical", grouped Deployment
    first.
  - "Settings are not being exported — … to acme/grain-config since …:
    git push origin main: remote: Permission denied." **Config repo**
    opens `/settings/config-repo`.
  - "main is red — repair #1 is awaiting submit. Other work targeting main
    waits until the repair merges." **open #1** opens `/tasks/1`, and it
    also offers submit and force merge.
  - "Reconcile loop stopped". **power** opens `/system/power`.
  - "No isolation: runs execute on this host", with its impact line.
  - All four are listed on Health too.
  - Removing the injection cleared all four on the next poll (bell back to
    3).
  - A first try kept the repair row until a reload. That was the stub: it
    passed the server's ETag on a rewritten body, so the next poll's 304
    kept the injected task. With the ETag stripped, it clears without a
    reload.
- **Setup (demo C):**
  - "2 setup steps left · Still to do: connect a git host, add a model
    connection." **finish setup** opens `/settings/git`.
  - With `config.deployment`'s `repos` and `targetRepos` and
    `/api/deployment`'s repositories emptied (stub): "3 setup steps left ·
    Still to do: connect a git host, add a model connection, add a
    repository."
- **See all:** opens System → Health on desktop and `/system/health` on a
  phone.

### Standing banners (demo B)

| Width | Install notice alone | With the disconnected banner raised |
| :--- | :--- | :--- |
| 1440 | notice y=0, 48 px; rail and header start at y=48; main at y=125 | disconnected y=0, 48 px; notice y=48; rail and header at y=96; main at y=173 |
| 390 | notice y=0, 128 px; phone bar at y=128 | disconnected y=0, 108 px; notice y=108, 128 px; phone bar at y=236 |

- Nothing overlaps.
- The disconnected banner ("This deployment cannot be reached: the
  connection to it failed. Unreachable since … -- retrying automatically
  …") was raised by refusing `/api/tasks` and `/api/config`.
- Within 12 s of the routes being restored it cleared, and the layout went
  back to the install-notice-only figures at both widths.

### Console and network

No page errors and no unexpected console errors on any surface. These
were seen and expected:

- `ERR_ABORTED` on `/api/tasks` when navigating mid-poll.
- The 401 on `/` before signing in.
- The 404 for the forgotten run id.
- The 404 for `/api/repos/acme/widgets/github-branches` on demo A, which
  wires no GitHub.
- The 422 release-notes read against the stub registry.
- The 400 for the stub's invented job name.
- The stubbed 500s and connection reset.

## 3. Intercepted or stubbed routes

| Route | Method | Why |
| :--- | :--- | :--- |
| `127.0.0.1:5055` (a Node HTTP server) | GET `/v2/x/grain/tags/list`, HEAD/GET `/v2/x/grain/manifests/*` | A loopback registry for `-upgrade-image`. It lists nine tags and answers a fixed digest; there is no registry in this sandbox. |
| `/api/upgrade` | POST, GET | Never upgrade. The POST answered 500 and then 200 `phase: running`; the GET answered the same running status after the 200. |
| `/api/host/restart`, `/api/host/reboot`, `/api/host/shutdown` | POST | Never touch the host. Answered 200, a connection reset, and a 500 (plain text, then JSON). |
| `/api/settings` | GET | Added `pendingRestart` and `unrunnable`, which only a daemon with a running config to compare sets. |
| `/api/config` | GET | Added `reconcilerDown`, `stateRepoSyncFailure` and `hostSandboxes`, which only a live daemon sets. On demo C, emptied the deployment's repositories. Refused, with `/api/tasks`, to raise the disconnected banner. |
| `/api/deployment` | GET | On demo C, emptied repositories, for the "add a repository" step. |
| `/api/tasks` | GET | One task given `redBaseBranch: "main"` and an open pull request, for the red-base repair alert (the demo's loop files no repair). ETag stripped. |
| `/api/sandboxes` | GET | Added `commit`, `refresh` and `lastRefresh` to a persistent sandbox. Emptied the list. Answered 500. |
| `/api/ci/runs` (list) | GET | Prepended the mixed-state run, and added `ownerTaskId` to the shared fault. |
| `/api/ci/runs/feedfacefeedface` | GET | One run carrying failure, errored, skipped, cache-hit and pending jobs, which the demo's single always-green workflow cannot produce. The Retry POST it led to was real. |
| `/api/metrics` | GET | Answered 500, held 5 s, and had its counts inflated, for the failure, loading and long-value states. |

Everything else was the demos' own state, driven through the UI. Two
pieces of setup went through the API: `POST /api/boards` to seed the
dashboard, and the demos' e2e endpoints (`/api/e2e/ci/finish`,
`/api/e2e/base-branch/{red,green}`).

## 4. Tests run

- `npx vitest run src/settingsPlan.test.js src/settingsIndex.test.jsx src/components/SystemPage.test.jsx src/components/SettingsPage.test.jsx src/alerts.test.js src/components/AlertsButton.test.jsx src/App.test.jsx`:
  174 passed.
- `npx playwright test e2e/deployment-operations-pending-restart-field.spec.js e2e/system-tabs.spec.js e2e/system-health-phone-deep-link.spec.js e2e/deployment-operations-navigation.spec.js e2e/deployment-operations-admin-pointers.spec.js`:
  17 passed.
- `npx playwright test e2e/deployment-operations-alert-power-flush-names.spec.js`:
  3 passed.
- The new spec, run over a build without the fix, failed as expected.
- `npx prettier --check src e2e`: clean.
- **Screenshots** of `/system/health` (with the pending row) and the
  landing on `/settings/credentials`, before (demo B, unfixed) and after
  (demo D, fixed), at 1440×900 and 390×844, light and dark. They were
  checked by bounding box (§1). Screenshots are not committed.

## 5. Follow-ups

- **Caches have no hit history.** The brief asks for the proxy and Actions
  caches' "sizes and hit history". Neither `pkg/pkgcache` nor
  `pkg/actionscache` counts hits, so System → Caches can only show sizes;
  only the CI job cache has a hit rate. Recording hits means new daemon
  counters and API fields, so it was proposed as its own task rather than
  done here.
