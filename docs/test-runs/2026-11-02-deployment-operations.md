# Deployment Operations Walkthrough — 2026-11-02

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real browser against real `grain demo` servers (task
2406). It follows `2026-11-01-deployment-operations-2.md` and re-walks
the whole area on today's `main`, which has since removed remote executors
(#411) and merge batching (#443) and reworked parts of Settings.

- **Commit tested:** `36af7fc7` (`main`, the merge of #468). The fix below
  is on `grain/049f1e09/task-2406-walk-through-system-health`.
- **Date:** 2026-11-02, 08:53–09:40 UTC.
- **Result:** one bug found and fixed, which is that every CI run row's
  open control had the same name (§1). Everything else behaved as designed.
  One part of the brief no longer applies: connected remote executors were
  removed in #411 (§2, Sandboxes).

## Environment

- Debian 12, `linux/amd64`; Go 1.26.10; Node 20.19.0.
- `cd ui && npm ci && npm run build`, then
  `go build -tags grain_e2e -ldflags "-X github.com/ai-on-gke/grain/pkg/version.Release=v1.2.0" -o ~/bin/grain ./cmd/grain`,
  so the binary embeds the real production bundle, as
  `ui/e2e/global-setup.js` builds it. The build stamp reads
  "v1.2.0 · 36af7fc · 2026-11-02 08:52Z".
- Three demos, each over its own throwaway embedded store (`TMPDIR` set to
  a scratch directory). Each was signed in by typing its access code
  (`-access-code-file`) into the "Enter access code" page:
  - **A:** `grain demo -open=false -observability -staging -addr 127.0.0.1:4301`.
    This has the real `pkg/ci` service and job cache, host-directory CI
    sandboxes, the fake proxy and Actions caches, `top`, and two persistent
    (staging) sandboxes.
  - **B:** `grain demo -open=false -github-sim -fake-ci -base-branch-ci -reconcile 2s -agent-paused -host-mode-install -notifications -upgrade-image localhost:5055/x/grain -upgrade-allowed-branches latest,candidate,rc,main,v1.3.0,v1.2.0,v1.1.0 -addr 127.0.0.1:4302`.
  - **C:** `grain demo -open=false -unconfigured -addr 127.0.0.1:4303`.
- A loopback OCI registry on `127.0.0.1:5055` (a 20-line Node HTTP
  server) behind `-upgrade-image`. It lists nine tags and answers a fixed
  manifest digest.
- Playwright Chromium (`chromium-1234`, headless) from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900, 700×700
  and 390×844.
  - Each walk was a Node script driving `playwright`'s `chromium` through
    visible controls: role and label locators, clicks, typing, Tab, arrow
    keys and Enter, and native and MUI dialogs.
  - Each script recorded console errors, page errors, failed requests and
    HTTP ≥ 400 responses.
  - The scripts lived outside the repository and are not committed.
- No Gemini key is in this sandbox (`/home/debian/.gemini-api-key` is
  absent), so nothing used a model, and `LOOK=1` was not available.

## 1. Bug found and fixed

### Every CI run row's open control was named after the repo alone

- **Surface:** the rail's CI page (`/ci`), its runs table and its
  active-sandboxes table, and a repo's own CI runs (`RunList` and
  `ActiveSandboxes` in `ui/src/components/CIPage.jsx`).
- **Reproduction (demo A):**
  1. Start two runs of acme/widgets: "Start a run", then repo
     acme/widgets, branch `walk/held-1`, then "Load workflows", then
     "Start run". Then retry the cancelled one.
  2. Open `/ci` and Tab through the table, or list its buttons.
- **Result:**
  - Each row's one control, the link that opens the run, is a button
    whose name is its visible text: "acme/widgets" on every row. The
    accessibility snapshot showed `button "acme/widgets"` twice for two
    different runs.
  - On a repo's own page the control is the branch, so every run of
    `main` was "main".
  - The active-sandboxes table had the same problem, one "acme/widgets"
    per job.
  - A keyboard or screen-reader user could not tell which run a control
    opens.
- **Expected:** each control says which run it opens (WCAG 2.4.6 /
  2.4.9). Its visible text should still lead the name (2.5.3).
- **Fix:**
  - `runOpenLabel` names the run's control "acme/widgets walk/held-1,
    success, started 11/2/2026, 9:04:16 AM" ("main, success, started …" on
    a repo's page).
  - An active sandbox's control reads "acme/widgets walk/held-1, job
    observe, sandbox cb98068-0".
  - The visible text is unchanged.
- **Coverage:**
  - `CIPage.test.jsx` › "names each run's open control by its branch,
    result and start, not its repo alone".
  - New `e2e/ci-run-row-names.spec.js` over the real `-observability`
    demo. It starts two runs, finds each control by its own name, and
    opens the second from the keyboard. It fails on the unfixed bundle
    (`toHaveCount(1)`, received 0).
  - Tests that found a row by the bare repo or branch name now match its
    leading text: `CIPage.test.jsx`, `RepoPage.test.jsx`,
    `RepoRuns.test.jsx`, `App.ci.test.jsx`, `App.repos.test.jsx` and
    `e2e/release-run-ref-checks-walkthrough.spec.js`.

## 2. Walkthrough and outcomes

### Navigation, URLs, history (demo A)

- **Opening System:** the header rail's System button
  (`data-testid=top-bar-system`) opens `/system` with Health selected.
  It is reached by keyboard as the 32nd Tab stop from the top of the page.
- **Tabs:** each of health, logs, sandboxes, caches, top, pipeline, metrics
  and power was clicked and then reloaded. Each kept its URL
  (`/system/<tab>`; Health is the bare `/system`) and its selected tab.
- **Back and Forward** stepped metrics ⇄ power.
- **Arrow keys** move focus along the tablist and wrap from Power to
  Health. A tab is activated with Enter (manual activation, MUI's default).
- **Old links:**
  - `/admin/logs` and `/admin/logs?q=ci` → `/system/logs`. Logs keeps its
    search in component state, so there is no query to carry.
  - `/sandboxes` → `/system/sandboxes`
  - `/caches` → `/system/caches`
  - `/metrics?window=30d&repo=acme%2Fwidgets` →
    `/system/metrics?f=repo:any:acme/widgets&window=30d`
  - `/metrics?window=7d` → `/system/metrics?window=7d`
  - `/system/upgrade` → `/settings/upgrade`
  - `/system/health` → `/system`
  - `/system/bogus` → `/system`
- **The build stamp** is an `<a href="/settings/upgrade">`, and it opens
  Settings → Upgrade.
- **Phone (390×844):**
  - "Open navigation", then the drawer's System row, opens the section list
    (health … power, each with ›).
  - Every section opened from the list and survived a reload at
    `/system/<tab>`.
  - Every section has `scrollWidth == 390`.
  - The phone bar's bell reads "Alerts: 2 active". Its "see all on
    system → health" opens `/system/health`.
- **Narrow window (700×700):** no section scrolls sideways
  (`scrollWidth == clientWidth == 700`). No element runs past the right
  edge without a clipping or scrolling ancestor.

### Health

- **Demo B lists:**
  - the host-mode install notice, with Migration docs →
    `…/design/design/cli-run-location.design.md#migrating-a-host-mode-install`
    (the file exists);
  - the GitHub API budget table;
  - the two seeded notifications;
  - the connection pause ("Usage limit reached (antigravity)", connection
    Gemini 2.5 Pro, "Until 2:55:13 PM (about 5h 46m)", with Resume now and
    Open connection).
- **Injected rows** (stubs, §3):
  - "Saved, not applied until restart: Poll interval, GitHub host,
    Kubernetes sandbox pod image". The links are
    `/settings/engine#pollInterval`, `/settings/credentials#githubHost` and
    `/settings/sandboxes#k8sSandboxImage`. Following Poll interval and
    GitHub host left the field's input focused and in the viewport
    (top y=258 and y=453 of 900).
  - "Stored, but not runnable: Poll interval (stored 0s, running 30s)",
    linking the same way.
  - The config-repo backing state ("A merged change reset settings on this
    deployment"). Its **Config repo** opens `/settings/config-repo`.
  - **Power** on the restart row opens `/system/power`.
  - Health reads `/api/settings` once, when it opens. After the injection
    was lifted, the restart rows stayed until the pane was reopened or
    reloaded, while the bell's alerts cleared on the next poll.
    - That is how the pane is built (`HealthTab.jsx`). In practice the
      rows change only across a restart or a save in Settings, and either
      reopens the pane.
    - So it was noted rather than changed.
- **Bell popover, keyboard:** focus moves into the popover when it opens.
  Esc closes it with focus back on "Alerts: 3 active".

### Sandboxes (demo A)

- **Host:**
  - Load (0.45 / 0.55 / 0.60 of 4 vCPU), utilisation, memory and swap,
    with sparklines.
  - "No disk figures available."
- **Storage:**
  - Sandboxes, with nothing in flight.
  - The proxy cache, at 5.3 GiB.
  - CI history: 0 B of 64.0 MiB, with its retention.
  - The CI job cache, with "empty the job cache" and its hit-rate note.
  - CI artifacts, which are off, with the reason.
- **Budget:**
  - "within budget cap".
  - Committed 0.0 of 14.0 GiB and 0 of 4 vCPU. While a run was held this
    read "2.0 GiB … 2 of 4 vCPU (1 sandbox) · Pools: CI: 1 (2 vCPU /
    2.0 GiB)".
  - Cluster free and the roomiest node are shown.
- **Auto overcommit:** "overcommit: auto". It shows its effective factors
  (CPU 2.75×, memory 1.25×), bounds, targets, PSI and OOM kills, and the
  recent adjustment "CPU 2.50× → 2.75× — CPU PSI 8.4% under 12.5% calm
  band for 4 samples".
- **Reconciliation:** "0 accounted in ledger vs. 0 live", "in sync".
- **CI sandboxes:** while a run was held they fold behind the summary
  "ci sandboxes (1)" (`aria-expanded=false`).
- **Standing sandboxes:** the empty-state explanation, with
  `tables/standing_sandbox.json`.
- **Persistent sandboxes:**
  - docs-preview is running, reached at `/staging/docs-preview/`.
  - grain-staging is restarting, "exited with status 1", with 4 restarts,
    on port 4280.
  - Each has restart, stop, rebuild, logs and Raw.
  - With `commit`, `refresh` and `lastRefresh` added (stub), the row reads
    "running 1a2b3c4d5e6f · after_green" and "refreshed 2h ago: main moved;
    CI green at 1a2b3c4d".
  - With the list emptied (stub), it shows the explanation and the
    `tables/persistent_sandbox.json` pointer. `docs/persistent-sandboxes.md`
    exists.
- **Error state:** `/api/sandboxes` answering 500 (stub) shows "Sandbox
  health could not be read (stub: backend down). This pane tries again
  every few seconds."
- **Connected remote executors:** removed from grain in #411 (task 2324,
  "remove remote executors"), along with `ConnectedExecutors.jsx` and
  Settings → Executors' panel. The brief's line for them no longer applies.

### Caches (demo A)

- **Proxy cache:** apt (3.0 GiB), npm, Python, Go, Docker Hub and
  Playwright, each with its own named button ("Flush apt packages" …),
  then "Flush the whole proxy cache".
- **Actions cache:** "20 entries, 3.0 GiB of 20.0 GiB", with "Flush the
  Actions cache of acme/widgets", "… of acme/gadgets" and "Flush the whole
  Actions cache".
- **Flushing a throwaway entry:** "Flush the Actions cache of acme/gadgets"
  sent `POST /api/actions-cache/flush?repo=acme%2Fgadgets`. The panel went
  to "10 entries, 2.0 GiB", with acme/gadgets at 0 B, and that held
  through a reload.
- **No confirmation is asked, by design** (`PackageCachePanel.jsx`), as on
  10-29 through 11-01.
- **Hit history:** neither cache counts hits (`pkg/pkgcache`,
  `pkg/actionscache`). Only the CI job cache reports lookups, on Sandboxes
  and CI. This is the same gap the 11-01 run proposed as its own task.

### CI (demo A)

- **Opening it:** the rail's CI entry (a `role=button` list item, "CI 0")
  opens `/ci`.
- **Start a run:**
  - The form is repo (a `<select>`, acme/widgets) and branch
    `walk/held-1`. Load workflows and Start run stay disabled until a
    branch is given and workflows are loaded.
  - Start run posted
    `{"repo":"acme/widgets","branch":"walk/held-1","workflow":".grain/workflows/observability.yml"}`
    and followed to `/ci/b980688ec32853e5`, which survived a reload.
  - Esc closes it to `/ci`, Back reopens it, and Forward closes it again.
- **Active sandboxes:**
  - Collapsed: "active sandboxes (1)", `aria-expanded=false`.
  - Expanded: "1 CI sandbox running · longest 10s (observe, held-1)", then
    the row `cb98068-0 · acme/widgets · walk/held-1 · observe |
    Observability · 10s`.
- **Search:**
  - "held-1" keeps its run.
  - "zzz-nothing" gives "No CI run matches. 1 run is here, and none of
    them answers this …".
  - The search box is named by its placeholder, "Search runs…". The search
    survived a reload; it is kept in local storage, not in the URL.
- **Cancel run:**
  - The prompt reads "Stop this run? The jobs in flight are told to give
    up and their sandboxes are destroyed. Optionally, say why …".
  - Dismissing it left the run running.
  - Accepting it gave `cancelled`, leaving only **Retry**, and that held
    through a reload.
- **Retry (every job):** posted the run's own coordinates
  (`{"repo","ref","commit","event":"workflow_dispatch"}`) and followed to
  the new run `/ci/86b774ecd8fd76ac`.
- **Success:** releasing the held step (`POST /api/e2e/ci/finish`) made the
  run `success`, with no Retry (by design). Its job shows its sandbox
  `c86b774-0` and the step "Wait for the browser", and "show output" reads
  "observed through the sandbox".
- **Forgotten run id:** `/ci/00000000deadbeef` shows `no CI run
  "00000000deadbeef" -- a run is remembered for a while and then
  forgotten`. That holds through a reload.
- **Opening a run from a row:** the repo name opens it. The row itself is
  not a target, by design ("The run's own link, on the repo"). §1 is the
  naming fix for this control.
- **From a task:**
  - `POST /api/ci/runs {"taskId":"2"}` started task 2's suite. Task 2's
    page then says "grain CI is running against this task's branch …",
    with **Show the run**.
  - Show the run opens the run over the task, with focus in the pane. Its
    heading reads "asked by task 2".
  - Esc closes only the run, returns focus to Show the run, and leaves
    `/tasks/2`.
  - The URL stays `/tasks/2`, so a reload shows the task without the run.
    That is deliberate (`App.jsx` `openCIRun`: "an overlay opened from a
    repo or task is deliberately transient").
- **Mixed states** (the stubbed run `feedfacefeedface`):
  - The row reads "5 jobs: 1 failure, 1 errored, 1 skipped, 1 success,
    1 pending · 1 cache hit".
  - The overlay orders the jobs failures first: failure observe (exit 1,
    output "boom: assertion failed"), errored lint, success unit ("cache
    hit"), skipped docs, pending deploy.
  - It offers **Retry** and **Retry failed jobs**.
- **Retry failed jobs:** posted
  `{"repo":"acme/widgets","ref":"walk/mixed",…,"event":"push","jobs":["observe","lint"],"recheck":true}`
  to the real daemon. It followed to the new run `/ci/b69a2c5dc57c9d58`,
  which errored with "ci: no workflow of acme/widgets answers push of
  branch walk/mixed … (the run asked for observe, lint)". That is correct:
  the stub invented the push event, and the demo's only workflow is
  `workflow_dispatch`.
- **Shared fault:**
  - The card reads "acme/widgets · CI / e2e (Run go test ./...) failing the
    same way on 3 branches right now", with its branches and the common
    file `pkg/auth/session.go`.
  - With `ownerTaskId: "4"` added (stub), it reads "Assigned to task #4",
    and the link is `/tasks/4`.
  - The "1 shared failure" toggle folds the card.

### Logs, Top

- **Logs:**
  - The Source select (named "Source" via `aria-labelledby`) offers
    `daemon`.
  - "Search logs" for "zzzz-no-such-line" shows "(no matching log
    lines)".
  - "demo" leaves 300 lines, every one containing "demo".
  - Refresh works.
- **Top:** the process table (systemd, grain-daemon, node), busiest first.

### Pipeline (demo A)

- **The pane:** "The pipeline is running …", with a reason field, "pause
  pipeline", and "contact running agents (2)".
- **Pause:**
  - The prompt reads "Pause the pipeline? Grain will stop starting new
    agent and CI runs. Runs already in flight will continue."
  - Dismissing it sent nothing (the bell stayed at 2).
  - Accepting it took the bell to 3 with the manual-mode alert, "Paused —
    only tasks you Run now will start | No new tasks are being picked up
    since 9:06:29 AM … — paused by hand", with resume now.
  - The pause survived a reload.
- **Resume:** "Resume the pipeline? Grain will start dispatching queued
  agent and CI runs again." Dismissing it changed nothing. Accepting it
  cleared the alert (the bell went back to 2).

### Power (demo A, POSTs intercepted)

| Control | Prompt | Dismissed | Accepted (stubbed answer) | UI |
| :--- | :--- | :--- | :--- | :--- |
| Restart grain | "Restart grain? Every task currently running is interrupted, and this UI is unreachable for a few seconds …" | nothing sent | 200 | "Restart requested. grain is coming back up; …" (stays) |
| Reboot host | "Reboot the host machine? … unreachable until the machine comes back up." | nothing sent | connection reset | "Reboot requested. This UI is unreachable until the host machine comes back up." |
| Shut down host | "Shut down the host machine? … grain stays down until somebody powers the machine back on …" | nothing sent | 500 `{"error":"stub: shutdown refused"}` | "stub: shutdown refused" as a toast, gone within 4.5 s |

The three POSTs seen were `/api/host/restart`, `/api/host/reboot` and
`/api/host/shutdown`, all answered by the stub.

### Metrics

- **System → Metrics (demo A):**
  - **Release notes:** Weekly ("Week 44, 2026": 0 PRs merged, 7 tasks
    completed, 8 filed, a per-repo line, and "Nothing has been written up
    for this week yet …"). Versions v2.4.0 … v1.2.0, with `running` on
    v1.2.0.
  - **Throughput:** 18 filed (2.6/day) and 9 completed, with sparklines
    and the scaling note.
  - **Latency:** the table, with `*` on low-n percentiles.
  - Capacity, outcomes, backlog ("Oldest waiting task: task 2") and
    GitHub CI.
- **Window:**
  - The "Window" combobox offers last 24 hours, 7 days, 30 days and
    90 days.
  - Picking 90 days wrote `?window=90d`, which survived a reload.
  - The range line follows the window ("… · 30d 0h", "… · 7d 0h").
- **Task navigation:** "task 2" opens `/tasks/2`, and Back returns to
  `/system/metrics?window=90d`.
- **From a dashboard:**
  - A seeded panels board ("Walk throughput") holds a `metrics` panel over
    acme/widgets for 30 days. It shows 13 filed and 3 completed.
  - Its **Open** link goes to
    `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`. That
    page reads "repo is any of acme/widgets / created is within the last
    30 days", Window "Last 30 days", 13 filed (0.4/day), which agrees with
    the panel.
  - That survived a reload, and Back returned to `/boards/board-5`.
- **Failure:** `/api/metrics` answering 500 (stub) shows "The report could
  not be loaded (stub: metrics scan failed). Refresh to try again."
- **Loading:** with `/api/metrics` held 5 s (stub), a progressbar shows
  until the report lands.
- **Long values:** every count set to 98765432109 (stub). At 1440 and at
  390 px, `scrollWidth` equals the viewport, and no element runs past the
  right edge unclipped.
- **Empty:** `?f=repo:any:nobody/nothing` reads "Measured over the tasks in
  nobody/nothing." and shows zeros. The weekly release notes above it are
  deployment-wide by design ("across every repository").

### Settings → Upgrade (demo B, the upgrader wired)

- **Reached from** the build stamp and from `/system/upgrade`.
- **Branch picker:** "latest — the last promoted release", "candidate —
  the current release candidate (alias: rc)", "main — the head of main",
  v1.3.0, v1.2.0 and v1.1.0. `v1.3.0-rc.1` and `sha-deadbeef` are filtered
  out by `-upgrade-allowed-branches`.
- **Image:** picking v1.3.0 gives "Will pull
  localhost:5055/x/grain:v1.3.0". The "Image (optional)" field is present.
- **Release notes:** the preview really pulls the image (`docker pull`
  against the loopback registry). It answers 422, "could not fetch content
  descriptor", because the stub registry serves no image config. That is
  expected, as on 10-30 and 11-01.
- **In-flight warning:** "2 runs are in flight. Upgrading restarts this
  deployment. 2 would be lost (antigravity 2) …".
- **Upgrade, `POST /api/upgrade` intercepted:**
  - The form's Upgrade opens the "Confirm upgrade" dialog, which repeats
    the warning and ends "Upgrade anyway?".
  - Cancel sent nothing.
  - Upgrade against a 500 sent `{"branch":"v1.3.0","afterTasksFinish":false}`
    and showed "stub: registry refused the pull".
  - Upgrade against a 200 `phase: running` drew "upgrading…", Status
    running, "Step 1 of 2: pull (20s)", "◐ pull running … stub: pulling
    localhost:5055/x/grain:v1.3.0" and "○ restart pending". For that, the
    stub merged the running phase into the daemon's real status; on its
    own, a bare `{phase, branch, steps}` body reads as a deployment that
    cannot upgrade.
- **Check mode:** shown read-only. `upgrade-check-settings-link` opens
  `/settings/general`.
- **Database panel:** "Currently running on SQLite. Upgrade this
  deployment to a PostgreSQL database …", with the DSN field, Test
  connection and Migrate. Not pressed.

### Alerts bell

- **Agent/connection pause** (`-agent-paused`, demo B): shown as under
  Health. **Resume now** on Health cleared it (the bell went from 3 to 2),
  and it stayed cleared through a reload.
- **Pipeline pause / manual mode:** see Pipeline. It appears and clears.
- **Red base branch, real** (demo B, `-reconcile 2s`):
  - `POST /api/e2e/base-branch/red {"branch":"main"}` let the reconcile
    loop raise "acme/widgets main is red" (the bell went from 3 to 4).
  - `…/green` cleared it on the next poll (back to 3) without a reload.
- **Injected into `/api/config` and `/api/tasks`** (stub): red-base repair,
  reconciler-down, a failing config-repo sync and host sandboxes.
  - The bell went to "Alerts: 7 active, critical", grouped Deployment
    first.
  - "Settings are not being exported — Settings have not been exported to
    acme/grain-config since …: git push origin main: remote: Permission
    denied." **config repo** opens `/settings/config-repo`.
  - "main is red — repair #1 is waiting for approval. Other work targeting
    main waits until the repair merges." It offers open #1 (`/tasks/1`),
    submit and force merge.
  - "Reconcile loop stopped". **power** opens `/system/power`.
  - "No isolation: runs execute on this host", with its impact line.
  - All four are listed on Health too.
  - Lifting the injection cleared all four on the next poll (the bell went
    back to 3) without a reload. The stub strips the ETag, as the 11-01
    run found it must.
- **Setup (demo C):**
  - "2 setup steps left · Still to do: connect a git host, add a model
    connection." **finish setup** opens `/settings/git`.
  - With the deployment's `repos`/`targetRepos` and `/api/deployment`'s
    repositories emptied (stub): "3 setup steps left".
  - With an enabled connection added to `config.agentBackends` (stub): "1
    setup step left", on the next poll. Driving Settings → Models' "New
    connection" wizard is the settings walkthrough's, so the step was
    cleared by a stub here.
- **See all:** opens System → Health (`/system` on desktop,
  `/system/health` on a phone).

### Standing banners (demo B)

| Width | Install notice alone | With the disconnected banner raised |
| :--- | :--- | :--- |
| 1440 | notice y=0, 48 px; header y=48; main y=125 | disconnected y=0, 48 px; notice y=48, 48 px; app body y=96 |
| 390 | notice y=0, 128 px; phone bar y=128; main y=181 | disconnected y=0, 108 px; notice y=108, 128 px; app body y=236 |

- Nothing overlaps: each banner pushes the rail and page down.
- **The disconnected banner** was raised by refusing `/api/tasks` and
  `/api/config` (stub). It read "This deployment cannot be reached: the
  connection to it failed. Unreachable since 9:15:07 AM -- retrying
  automatically …". It cleared about 1 s after the routes were restored.
- **The install notice** cleared from both the banner and Health on the
  next poll once `installNotice` was dropped from `/api/config` (stub). The
  header then sat at y=0.

### Console and network

No page errors and no unexpected console errors on any surface. These
were seen and expected:

- The 401 on `/` before signing in.
- `ERR_ABORTED` on `/api/tasks` when navigating mid-poll.
- The 404 for the forgotten run id.
- The 404 for `/api/repos/acme/widgets/github-branches` on demo A, which
  wires no GitHub.
- The 422 release-notes read against the stub registry.
- The stubbed 500s and connection resets.

## 3. Intercepted or stubbed routes

| Route | Method | Why |
| :--- | :--- | :--- |
| `127.0.0.1:5055` (a Node HTTP server) | GET `/v2/`, `/v2/x/grain/tags/list`, HEAD/GET `/v2/x/grain/manifests/*` | A loopback registry for `-upgrade-image`. It lists nine tags and answers a fixed digest; there is no registry in this sandbox. |
| `/api/upgrade` | POST, GET | Never upgrade. The POST answered 500, then 200 with the daemon's real status plus `phase: running` and two steps. The GET answered the same once a POST had been made. |
| `/api/host/restart`, `/api/host/reboot`, `/api/host/shutdown` | POST | Never touch the host. Answered 200, a connection reset, and a 500 JSON body. |
| `/api/restart-watch` | GET | Caught by the same Power stub's pattern and answered `{}`. Harmless: it only watches for the daemon coming back. |
| `/api/settings` | GET | Added `pendingRestart` and `unrunnable`, which only a daemon with a running config to compare sets. |
| `/api/config` | GET | On demo B: added `reconcilerDown`, `hostSandboxes`, `stateRepoResetNotice` and `stateRepoSyncFailure`; dropped `installNotice`; refused, with `/api/tasks`, to raise the disconnected banner. On demo C: emptied the deployment's repositories, and added an enabled agent backend. ETag stripped. |
| `/api/deployment` | GET | On demo C, emptied repositories, for the "add a repository" step. |
| `/api/tasks` | GET | One task given `redBaseBranch: "main"` and a pull request, for the red-base repair alert. ETag stripped. |
| `/api/sandboxes` | GET | Added `commit`, `refresh` and `lastRefresh` to a persistent sandbox. Emptied the list. Answered 500. |
| `/api/ci/runs` (list) | GET | Prepended the mixed-state run, and added `ownerTaskId` to the shared fault. |
| `/api/ci/runs/feedfacefeedface` | GET | One run carrying failure, errored, skipped, cache-hit and pending jobs, which the demo's single always-green workflow cannot produce. The Retry failed jobs POST it led to was real. |
| `/api/metrics` | GET | Answered 500, held 5 s, and had its counts inflated, for the failure, loading and long-value states. |

Everything else was the demos' own state, driven through the UI. Some
setup went through the API instead:

- `POST /api/boards`, to seed the dashboard.
- `POST /api/ci/runs`, to start runs for task 2/3 and for the folded-CI
  check.
- The demos' e2e endpoints: `/api/e2e/ci/finish` and
  `/api/e2e/base-branch/{red,green}`.

## 4. Tests run

- `npx vitest run` (the whole UI suite): 3640 passed, after updating the
  three tests that found run rows by the bare name.
- `npx playwright test e2e/ci-run-row-names.spec.js e2e/release-run-ref-checks-walkthrough.spec.js e2e/ci-retry.spec.js`:
  4 passed, over a bundle rebuilt with the fix.
- `e2e/deployment_operations_walkthrough.spec.js`: all its tests passed in
  the same run.
- `e2e/ci-run-row-names.spec.js` over the unfixed bundle: failed as
  expected.
- `npx prettier --check` on the changed files: clean.
- **Screenshots:**
  - `make ui-screenshots PAGES=/ci,/system,/system/sandboxes,/system/metrics OUT=screenshots/after`
    rendered desktop (1440) and phone (390), light and dark. They are not
    committed.
  - The fix changes accessible names only, not anything drawn. It is
    asserted by role-and-name locators rather than by pixels.
