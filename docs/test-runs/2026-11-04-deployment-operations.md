# Deployment Operations Walkthrough — 2026-11-04

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real browser against real `grain demo` servers (task
2965). It follows `2026-11-03-deployment-operations.md` and re-walks the
whole area on today's `main`. Since then `main` has changed how
`GET /api/tasks` answers (live tasks only, without descriptions, gzipped;
closed tasks page in separately), so the task-sourced alerts (the red-base
repair row) were checked against the new list.

- **Commit tested:** `0c8855db` (`main`, the merge of #701). The fix below
  is on `grain/049f1e09/task-2965-walk-through-system-health`.
- **Date:** 2026-11-04, 12:42–13:25 UTC.
- **Result:** one bug was found and fixed (§1): Settings → Upgrade was a
  blank pane for as long as its first read took, which is five seconds
  behind an unreachable grain hostd. One server-side cause is proposed as
  a follow-up (§1). Everything else behaved as designed (§2).
- **`main` does not build at this commit.** `pkg/cli/main.go` has
  `case "login"` twice in `dispatchCLI`'s switch (lines 610 and 684):
  `duplicate case "login" (constant of type string) in expression switch`.
  `Tests / go` fails at its Build step on `main` itself. To get a binary to
  walk, `"login"` was dropped from the second case **in the working tree
  only**. That edit is not committed: the base-branch reconciler owns
  `main`'s repair.

## Environment

- Debian 12, `linux/amd64`; Go 1.26.10; Node 20.19.0.
- The build:
  - `cd ui && npm ci && npm run build`.
  - Then `go build -tags grain_e2e -ldflags "-X github.com/ai-on-gke/grain/pkg/version.Release=v1.2.0" -o ~/bin/grain ./cmd/grain`
    (with the local compile fix above).
  - The build stamp reads "v1.2.0 · 0c8855d-dirty · 2026-11-04 12:41Z".
- Each demo ran over its own throwaway embedded store (`TMPDIR` set to a
  scratch directory). Each was signed in by typing its access code
  (`-access-code-file`) into the "Enter access code" page:
  - **A:** `grain demo -open=false -observability -staging -addr 127.0.0.1:4301`.
  - **B:** `grain demo -open=false -github-sim -fake-ci -base-branch-ci -reconcile 2s -agent-paused -host-mode-install -notifications -host-executor hostd-down -upgrade-image localhost:5055/x/grain -upgrade-allowed-branches latest,candidate,rc,main,v1.3.0,v1.2.0,v1.1.0 -addr 127.0.0.1:4302`.
  - **C:** `grain demo -open=false -unconfigured -addr 127.0.0.1:4303`.
  - **D:** `grain demo -open=false -host-executor hostd -addr 127.0.0.1:4304`
    (a real grain hostd on a throwaway allowlist missing grain's own
    entries).
  - **E:** `grain demo -open=false -host-executor hostd-down -upgrade-image localhost:5055/x/grain -addr 127.0.0.1:4305`,
    on the binary rebuilt with the fix, to check it live.
- A loopback OCI registry on `127.0.0.1:5055` (a small Node HTTP server)
  sat behind `-upgrade-image`. It lists nine tags and answers a fixed
  manifest digest, but serves no image config.
- Playwright Chromium (`chromium-1234`, headless), from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900, 700×700
  and 390×844.
  - Each walk was a Node script driving `playwright`'s `chromium` through
    visible controls: role and label locators, clicks, typing, Tab, Enter
    and arrow keys, and native and MUI dialogs.
  - Each recorded console errors, page errors, failed requests and HTTP ≥
    400 responses.
  - The scripts lived outside the repository and are not committed.
- No Gemini key is in this sandbox (`/home/debian/.gemini-api-key` is
  absent), so nothing used a model and `LOOK=1` was not available.
  Layout was checked by bounding boxes instead (§4).

## 1. Bug found and fixed

### Settings → Upgrade was blank while its status was being read

- **Surface:** Settings → Upgrade (`UpgradePanel.jsx`), reached from the
  build stamp, from Settings' section list and from the old
  `/system/upgrade` link.
- **Reproduction (demo B):**
  1. Open `/system/upgrade`. It redirects to `/settings/upgrade`.
  2. For about five seconds the pane under the "upgrade" heading holds
     only the check-mode sentence and the install notice: no form, no
     database panel, no progress indicator (`getByRole("progressbar")`
     finds none at 1.5 s).
  3. `curl` of `GET /api/upgrade` with the access code took 5.01 s.
     The handler resolves every CLI's run location for its upgrade
     interruption notes (`daemon.Config.upgradeInterruptNotes` →
     `ResolveCLIRuns`), which asks grain hostd what it offers, and the
     demo's hostd is unreachable.
- **Cause:** `UpgradePanel` returned `null` while `status === null` and no
  load error had arrived. So the pane was indistinguishable from one with
  nothing to show for the whole read.
- **Fix:** while the first read is outstanding the pane draws a centred
  `CircularProgress` labelled "Reading the upgrade status", the same
  pattern Metrics ("Loading the report") and CI ("Loading CI runs") use.
  The failed-read alert is unchanged.
- **Coverage:**
  - `UpgradePanel.test.jsx`: "shows that the upgrade status is being read
    until the first read answers". It failed against the unfixed
    component (`Unable to find an accessible element with the role
    "progressbar" and name "Reading the upgrade status"`).
  - New `e2e/deployment-operations-upgrade-loading.spec.js`. It holds
    `GET /api/upgrade` in the browser, opens `/system/upgrade`, expects the
    redirect and the progress indicator, lets the read through to the real
    demo, and expects the indicator to go and the pane's answer ("Not
    available: …") to appear. It failed against the unfixed bundle
    (`element(s) not found`) and passes on the fixed one.
- **Checked live on demo E (fixed binary):** the indicator is visible
  from the first frame. It is centred in the pane at 1440 px (centre
  offset 0, pane x 488–1248) and at 390 px (centre offset 0, pane x
  12–378). It is replaced by the form after 5.5 s.
- **Follow-up proposed (not fixed here):** the five seconds themselves.
  Every `GET /api/upgrade` re-resolves the run locations through an
  unreachable hostd, and the Upgrade pane polls it. That is a server change
  in `pkg/daemon`, separate from this UI fix.

## 2. Walkthrough and outcomes

### Navigation, URLs, history (demo A)

- **Opening System:** the header rail's System button (`top-bar-system`)
  opens `/system` on Health.
- **Tabs:**
  - Each of the eight tabs was clicked and then reloaded. Each kept
    `/system/<tab>` (Health is the bare `/system`) and its selected tab.
  - Back went `/system/metrics` → `/system/pipeline`, and Forward returned
    to `/system/metrics`.
  - Each tab's `tabpanel` is labelled by its tab (yesterday's fix holds).
- **Old links:**
  - `/admin/logs` → `/system/logs`.
  - `/sandboxes` → `/system/sandboxes`.
  - `/caches` → `/system/caches`.
  - `/metrics?window=7d&repo=acme/widgets` →
    `/system/metrics?f=repo:any:acme/widgets&window=7d`, so the query is
    kept and translated.
  - `/system/upgrade` → `/settings/upgrade`.
  - `/system/bogus` → `/system` (Health).
  - `/admin/logs?q=orchestrator` lands on `/system/logs` without `q`.
    Logs keeps no state in its URL (`LogsPage.jsx` reads none), so there
    is nothing for the query to carry into. This is by design, not a
    dropped query.
- **Build stamp:** "v1.2.0 · 0c8855d-dirty · 2026-11-04 12:41Z" is a link
  to `/settings/upgrade`, and clicking it opens Settings → Upgrade.
- **700 px window:** the tab strip scrolls (scroll width 730 in a 340 px
  scroller). Opening `/system/power` scrolls the selected tab fully into
  view (x 546–636 in a 296–636 scroller). The tables on Sandboxes scroll
  inside their own `overflow-x: auto` wrappers (1168 px of table in 420 px),
  and the page itself does not scroll sideways (scroll width 700).
- **Phone (390 px, demo B):**
  - "Open navigation" opens the drawer. Its System row opens `/system`,
    which is the section list: health, logs, sandboxes, caches, top,
    pipeline, metrics, power.
  - The bell is in the phone's bar ("Alerts: 2 active"). Its panel fits
    the window (x 16–374), and "see all on system → health" opens
    `/system/health`.
  - Nothing scrolls sideways on Health, Sandboxes or CI (scroll width 390).

### Health

- **Demo A:** overcommit auto status, the GitHub API budget ("1,650 of
  5,000 calls left this hour · resets in 38m 0s · spending 3,300/h. At
  that rate it runs out in 30m 0s, before it resets.") with the idle-poll
  backoff line and the per-caller table, the sign-in counters, and the two
  seeded notifications, the same rows the bell lists.
- **Demo B:**
  - The host-mode install notice heads the tab, with its "Migration docs"
    link to `design/design/cli-run-location.design.md#migrating-a-host-mode-install`.
  - The usage-limit pause row reads "Agent usage limit reached: nothing
    is being dispatched onto connection Gemini 2.5 Pro until 6:44:56 PM
    (about 5h 50m)", with **resume now** and **open connection**.
- **Demo D:** the hostd readiness note names each missing allowlist entry
  (`host-git`, `grain-gchat`, `grain-gcp`, …) with the systemctl command
  and the YAML to paste.
- **Saved-but-not-applied and stored-but-not-runnable** (stubbed
  `/api/settings`, §3):
  - "Saved, not applied until restart: Auto CPU maximum, Environment
    name." The links go to `/settings/sandboxes#sandboxOvercommitCpuMax`
    and `/settings/general#environmentName`, and the row has a **power**
    button.
  - "Stored, but not runnable: Auto CPU minimum (stored 0.1, running 1)."
  - Each of the three links was followed. Each landed with its
    `[data-setting-id]` in the viewport and keyboard focus inside it, on
    the field itself ("auto cpu maximum", "environment name", "auto cpu
    minimum").
  - Dropping the stub cleared both rows.
- **Config-repo backing state** (stubbed `stateRepoSyncFailure`): the
  Health row reads "Configuration is not being pulled", then
  "Configuration has not been pulled from acme/grain-config since
  11/4/2026, 11:00:00 AM: fatal: could not read from remote repository."

### Sandboxes (demo A)

- **Host, storage and budget:**
  - Load, utilization, memory and swap.
  - "No disk figures available".
  - The storage breakdown: the proxy cache at 5.3 GiB, the CI history
    ("0 B of 64.0 MiB"), the CI job cache with **empty the job cache**,
    and "This deployment keeps no CI artifacts".
  - The budget: "within budget cap · Committed: 0.0 GiB of 14.0 GiB memory
    · 0 of 4 vCPU", the cluster free line, and overcommit auto with its
    effective factors and the latest adjustment.
  - Reconciliation "in sync".
- **CI sandboxes fold:** two CI runs were started from the CI page and
  held (`walk/held-1`, `walk/held-2`).
  - The live table folds them behind "ci sandboxes (2)", reading "2 CI
    sandboxes · 4 vCPU / 4.0 GiB · longest 49s (c9a20fc-0)", with
    `aria-expanded=false`.
  - Clicking it opened it, and a reload kept `true`.
  - Focusing it and pressing Enter closed it, and a reload kept `false`.
  - No agent or standing sandbox was live, so nothing listed above the
    fold. The standing-sandboxes explanation shows.
- **Persistent sandboxes:**
  - docs-preview is running at `/staging/docs-preview/`, and grain-staging
    is restarting, "exited with status 1", with 4 restarts.
  - Stubbed (§3), grain-staging got a commit, a refresh policy, a last
    refresh and a 70-character name. It read "running 1a2b3c4d5e6f ·
    after_green" and "refreshed 2h ago: candidate moved; CI green at
    1a2b3c4d", and nothing overflowed.
  - Stubbed empty, the section shows its explanation.
- **Failure** (stubbed 500): "Sandbox health could not be read (500
  Internal Server Error). This pane tries again every few seconds."

### Caches (demo A)

- **Named controls:** ten, each named for what it flushes: "Flush apt
  packages", "Flush npm packages", "Flush Python packages", "Flush Go
  modules", "Flush Docker Hub layers", "Flush Playwright browsers",
  "Flush the whole proxy cache", "Flush the Actions cache of
  acme/widgets", "Flush the Actions cache of acme/gadgets" and "Flush the
  whole Actions cache".
- **Usage against the ceiling:** "20 entries, 3.0 GiB of 20.0 GiB", with
  acme/widgets at 2.0 GiB and acme/gadgets at 1.0 GiB.
- **Flushing a throwaway entry:** "Flush the Actions cache of
  acme/gadgets" sent `POST /api/actions-cache/flush?repo=acme%2Fgadgets`,
  and after a reload acme/gadgets read 0 B.
  - No confirmation is asked. This is unchanged from earlier runs and by
    design: the pane says a flush loses "nothing … that a rebuild does not
    put back".
- **Refresh** re-reads the pane.

### CI (demo A)

- **Opening it:** the rail's CI entry opens `/ci`. **Start a run** and
  **Refresh** sit in the page header.
- **Start a run** (twice, through the form):
  - Repo is chosen from "—, acme/widgets, Other…".
  - Branch is typed (`walk/held-1`, then `walk/held-2`), then Load
    workflows, then Workflow "Observability — .grain/workflows/observability.yml",
    then **start run**.
  - Each followed to its run (`/ci/9a20fc5c70d3a9a8`, `/ci/5406b11606ccda3b`).
- **Active sandboxes:** collapsed to "active sandboxes (2)"
  (`aria-expanded=false`), then expanded: "2 CI sandboxes running ·
  longest 35s (observe, held-1)", with each sandbox's row.
- **Search:** "held-2" narrows the runs list to that run, and the search
  survives a reload.
- **The run overlay:** opened from the row control "acme/widgets
  walk/held-2, running, started …". It shows the job, its sandbox
  `c5406b1-0`, the step "Wait for the browser" and "(waiting for
  output...)".
- **Cancel run:**
  - Dismissing the prompt ("Stop this run? The jobs in flight are told to
    give up and their sandboxes are destroyed. …") sent nothing, and the
    run stayed running.
  - Accepting it sent `DELETE /api/ci/runs/5406b11606ccda3b {"reason":""}`.
    The overlay then offered only **retry**, and that held through a
    reload ("cancelled").
- **Retry (every job):** posted
  `{"repo":"acme/widgets","ref":"walk/held-2","commit":…,"event":"workflow_dispatch"}`
  and followed to `/ci/865126ad0a0e5caf`.
- **Forgotten run id:** `/ci/00000000deadbeef` shows "no CI run
  "00000000deadbeef" -- a run is remembered for a while and then
  forgotten", with **← back**.
- **Mixed states** (the stubbed run `feedfacefeedface`, §3):
  - The row reads "5 jobs: 1 failure, 1 errored, 1 skipped, 1 success, 1
    pending · 1 cache hit".
  - The overlay orders failure, errored, success (with "cache hit"),
    skipped, pending, and offers **retry** and **retry failed jobs**.
  - **Retry failed jobs** posted
    `{"repo":"acme/widgets","ref":"walk/mixed",…,"event":"push","jobs":["unit","e2e"],"recheck":true}`
    to the real daemon and followed to `/ci/3a8780ba66bf9b2d`.
- **Shared fault:** "acme/widgets · CI / e2e (Run go test ./...) failing
  the same way on 3 branches right now". With `ownerTaskId: "4"` (stub)
  it reads "Assigned to task #4", linking to `/tasks/4`.

### Logs, Top (demo A)

- **Logs:**
  - It reads `GET /api/logs/daemon?lines=500`, 300 lines, and the tail is
    scrolled to the bottom.
  - Source offers "daemon".
  - Searching "merge-queue" narrows it to 50 lines, and "zzzz-nothing"
    reads "(no matching log lines)".
  - **refresh** re-reads.
- **Top:** the process table, busiest first, with **refresh**.

### Pipeline (demo A)

- **Pausing:**
  - With reason "walkthrough pause", **pause pipeline** asks "Pause the
    pipeline? Grain will stop starting new agent and CI runs. Runs already
    in flight will continue."
  - Dismissing it changed nothing.
  - Accepting sent `POST /api/pipeline-pause {"reason":"walkthrough pause"}`.
- **Manual mode:**
  - The panel reads "Paused — only tasks you Run now will start … Paused
    by operator (access code). Reason: walkthrough pause", with **resume
    pipeline**.
  - The bell went from 2 to 3 active. Its new row, "Paused — only tasks
    you Run now will start", says "Run now still starts the task it is
    pressed on. — walkthrough pause" and offers **resume now**.
  - The pause held through a reload.
- **Resuming:** **resume pipeline** asks "Resume the pipeline? Grain will
  start dispatching queued agent and CI runs again". Accepting it sent
  `DELETE /api/pipeline-pause`. The panel returned to running, and the bell
  to 2 active.
- **Contact running agents (2)** is offered and was not pressed.

### Power (demo A, POSTs intercepted)

| Control | Prompt | Dismissed | Accepted (stubbed answer) | UI |
| :--- | :--- | :--- | :--- | :--- |
| Restart grain | "Restart grain? Every task currently running is interrupted, and this UI is unreachable for a few seconds while the service comes back." | nothing sent | 200 `{"ok":true}` | "Restart requested. grain is coming back up; …" |
| Reboot host | "Reboot the host machine? Every task currently running is interrupted, and this UI will be unreachable until the machine comes back up." | nothing sent | connection reset | "Reboot requested. This UI is unreachable until the host machine comes back up." |
| Shut down host | "Shut down the host machine? … grain stays down until somebody powers the machine back on by some other means." | nothing sent | 500 `{"error":"stub: shutdown refused"}` | "stub: shutdown refused" |

### Metrics (demo A)

- **Sections:**
  - Release notes: the weekly view, "by direction" with links to `/tasks/10`
    and `/tasks/9`, and versions v3.1.1 to v1.2.0.
  - Throughput, with rates and trends.
  - Latency, capacity, backlog (links to `/?f=state:any:…`) and GitHub
    CI.
- **Window:**
  - The header's Window picker offers the last 24 hours, 7 days, 30 days
    and 90 days.
  - "Last 30 days" wrote `?window=30d` and redrew the span "· 30d 0h".
    That survived a reload.
- **Narrowing:**
  - `?f=repo:any:acme/widgets` shows "filters (1)", with tasks filed at 12
    rather than 18.
  - `?f=repo:any:acme/nonexistent&window=24h` shows zeros and the
    hint "a longer window is the thing to try first".
- **From a dashboard:** a seeded board with a metrics panel scoped to
  acme/widgets. The panel's **Open** link
  (`/system/metrics?f=repo:any:acme/widgets`) opened System → Metrics
  narrowed the same way. The board was deleted afterwards.
- **States** (stubs): a 500 reads "could not be loaded (500 Internal
  Server Error). Refresh to try again.", and a read held 4 s shows the
  "Loading the report" progress bar.
- **Into the tasks:** "#10" opened `/tasks/10`.

### Settings → Upgrade (demo B, POST intercepted)

- **Reaching it:** `/system/upgrade` → `/settings/upgrade`, and so does the
  build stamp. The pane was blank for the 5 s read (§1).
- **The panel:**
  - The install notice.
  - The in-flight warning and per-framework interruption notes.
  - Branch, a searchable combobox: "latest — the last promoted release",
    "candidate — the current release candidate (alias: rc)", "main — the
    head of main", v1.3.0, v1.2.0, v1.1.0, and "v1.0.0 — not allowed on
    this deployment".
  - Image, "wait until this deployment is idle", and the database panel
    ("test connection", "migrate database to postgresql", not pressed).
- **Check mode:** shown read-only. Its "settings → general → updates"
  link opens `/settings/general`.
- **The Upgrade button:**
  - With Branch empty, pressing it gives only "Please fill out this
    field."
  - With v1.3.0 it says "Will pull localhost:5055/x/grain:v1.3.0". The
    release notes read fails (422) because the stub registry serves no
    image config, and the pane says so in its notes line.
- **Upgrading:**
  - "confirm upgrade" repeats the warning ("2 runs are in flight … 2 would
    be lost (antigravity 2) … Upgrade anyway?").
  - Cancel sent nothing.
  - Confirming sent `{"branch":"v1.3.0","afterTasksFinish":false}`. The
    stubbed 500 was shown in a snackbar: "stub: registry refused the
    pull".

### Alerts bell

- **Demo B, real causes:**
  - **Connection pause** (`-agent-paused`, a usage limit on Gemini 2.5
    Pro): "Usage limit reached (antigravity)", with the resume time and
    how long that is.
    - **open connection** opens `/settings/models`.
    - **resume now** sent `DELETE /api/pause?connection=gemini-pro`. The
      row went, and the bell went from 3 to 2 active.
  - **Red base branch:** `POST /api/e2e/base-branch/red {"branch":"main"}`
    ran the real reconciler. The bell rose to 3, with "grain CI on
    acme/widgets main is confirmed red: at d156b44a08ff ("failure: test").
    grain has filed no repair for it (its triage is off here, or a person
    is to file one)."
    - `…/green` cleared it. The bell was back to 2 within 28 s (the
      reconciler at 2 s, then the bell's poll).
  - **see all on system → health** opens `/system` on Health.
- **Demo C (`-unconfigured`):**
  - "2 setup steps left — Still to do: connect a git host, add a model
    connection. Tasks wait until the deployment has a git host and a model
    connection." **finish setup** opens `/settings/git`.
  - The demo carries a repository, so the no-repository step is not
    raised. It was walked by stub on 11-02, and its code is unchanged.
- **Injected** (stubbed `/api/config` and `/api/tasks` on demo B, §3):
  - The bell read "Alerts: 6 active, critical".
  - **Reconcile loop stopped:** "This deployment's reconcile loop has
    stopped -- no tasks are being dispatched or checked. Restart the
    daemon to recover." **power** opens `/system/power`.
  - **Configuration has not been pulled** from acme/grain-config … .
    **config repo** opens `/settings/config-repo`.
  - **Host sandboxes:** "Every dispatched run executes in a plain directory
    on the machine this daemon runs on …".
  - **Red-base repair:** "main is red — repair #1 is awaiting submit /
    Other work targeting main waits until the repair merges", with
    **open #1** (→ `/tasks/1`), **submit** and **force merge**.
    - This row is built from `GET /api/tasks`, which now answers only
      live tasks. A repair waiting on its pull request is live, so the
      change does not lose it.
  - Dropping the stubs cleared all four. The bell went back to 2 active,
    and Health's rows went.

### Standing banners

- **Host-mode install notice (demo B):**
  - A full-width strip at the top. At 1440 px it spans y 0–48, and the
    header rail (System button) sits below it at y 68.
  - At 390 px it spans y 0–128, and the phone bar's bell sits at y 136.
  - It is `position: static`, so it pushes the rail and page down and
    covers nothing.
  - It names "Cannot run until then: claude" and links to Migration docs.
- **Disconnected banner (demo B):**
  - Raised by refusing `/api/config` and `/api/tasks` in the browser (§3).
    It read "This deployment cannot be reached: the connection to it
    failed. Unreachable since 1:02:17 PM -- retrying automatically; this
    page will pick back up on its own once it answers again."
  - It spans y 0–48, static, above the rail (System at y 116).
  - It cleared on its own once the routes answered again.

### Console and network

No page errors and no unexpected console errors on any surface. These were
seen and expected:

- The 401 on `/` before signing in.
- The 404 for the forgotten run id.
- The 404 for `/api/repos/acme/widgets/github-branches` on demos A, C and
  D, which wire no GitHub.
- The 422 release-notes read against the stub registry.
- The stubbed 500s, refusals and connection resets.

## 3. Intercepted or stubbed routes

| Route | Method | Why |
| :--- | :--- | :--- |
| `127.0.0.1:5055` (a Node HTTP server) | GET `/v2/`, `/v2/x/grain/tags/list`, HEAD/GET `/v2/x/grain/manifests/*` | A loopback registry for `-upgrade-image`; this sandbox has none. |
| `/api/upgrade` | POST | Never upgrade. Answered 500. |
| `/api/upgrade` | GET | In the new e2e spec only: held until released, then passed to the real demo. |
| `/api/host/restart`, `/api/host/reboot`, `/api/host/shutdown` | POST | Never touch the host. Answered 200, a connection reset, and a 500 JSON body. |
| `/api/restart-watch` | GET | Answered `{}` while Power was stubbed. It only watches for the daemon coming back. |
| `/api/settings` | GET | Added `pendingRestart` and `unrunnable`, which only a daemon with a running config to compare sets. |
| `/api/config` | GET | On demo B: added `reconcilerDown`, `hostSandboxes` and `stateRepoSyncFailure`; and was refused, with `/api/tasks`, to raise the disconnected banner. If-None-Match stripped. |
| `/api/tasks` | GET | Task 1 was given `redBaseBranch: "main"`, a pull request and `state: "queued"`, for the red-base repair alert. Filing a repair is the pipeline steward's job, which no demo runs. |
| `/api/ci/runs` (list) | GET | Prepended the mixed-state run, and added `ownerTaskId` to the shared fault. |
| `/api/ci/runs/feedfacefeedface` | GET | One run with failure, errored, skipped, cache-hit and pending jobs, which the demo's one workflow cannot produce. The Retry failed jobs POST it led to was real. |
| `/api/sandboxes` | GET | Gave a persistent sandbox a commit, refresh policy, last refresh and long name; then emptied the persistent list; then answered 500. |
| `/api/metrics` | GET | Answered 500, and was held 4 s, for the failure and loading states. |

Everything else was the demos' own state, driven through the UI. Some setup
went through the API instead:

- `POST /api/boards`, to seed the dashboard. It was deleted afterwards.
- The demo's e2e endpoints `/api/e2e/base-branch/{red,green}`.

## 4. Tests run

- `npx vitest run src/components/UpgradePanel.test.jsx`: 79 passed. The
  new test failed against the unfixed component.
- `npx playwright test` over a bundle rebuilt with the fix, with the local
  `pkg/cli` compile fix in the working tree so the suite's binary builds:
  - First run, on the unfixed bundle, 19 of 20 passed:
    `e2e/system-tabs.spec.js`, `e2e/deployment-operations-navigation.spec.js`
    and `e2e/deployment-operations-failed-reads.spec.js` all passed, and
    the one failure was the new spec.
  - Second run, on the fixed bundle, 3 passed:
    `e2e/deployment-operations-upgrade-loading.spec.js` and
    `e2e/auto-upgrade-settings-layout.spec.js` (desktop and phone).
- `npx prettier --check` on the changed UI files: clean.
- **Screenshots:**
  - `node e2e/screenshot.mjs --pages /settings/upgrade,/system --out screenshots/after`
    on the fixed binary, at desktop (1440) and phone (390), light and dark.
    They are not committed.
  - The loaded pane is unchanged by the fix. Only the loading state is
    new, and it is transient, so it was measured live on demo E instead
    (§1): centred at both widths.
