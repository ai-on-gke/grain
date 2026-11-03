# Deployment Operations Walkthrough — 2026-11-03

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real browser against real `grain demo` servers (task
2704). It follows `2026-11-02-deployment-operations.md` and re-walks the
whole area on today's `main`. Since then `main` has added Settings → Hostds,
the hostd readiness note on Health and the header rail changes. Today's walk
leaned on those and on the corners earlier runs had not pressed.

- **Commit tested:** `0fdeff3b` (`main`, the merge of #540). The fixes below
  are on `grain/049f1e09/task-2704-walk-through-system-health`.
- **Date:** 2026-11-03, 03:01–03:50 UTC.
- **Result:** two bugs were found and fixed (§1):
  - The bell's red-base-branch notification drew its markdown body as text
    and offered no way to its repair. Its body also named the branch
    before its repository.
  - System's tabs named no tabpanel.

  Everything else behaved as designed (§2).

## Environment

- Debian 12, `linux/amd64`; Go 1.26.10; Node 20.19.0.
- The build:
  - `cd ui && npm ci && npm run build`.
  - Then `go build -tags grain_e2e -ldflags "-X github.com/ai-on-gke/grain/pkg/version.Release=v1.2.0" -o ~/bin/grain ./cmd/grain`.
  - The binary embeds the real production bundle, as
    `ui/e2e/global-setup.js` builds it.
  - The build stamp reads "v1.2.0 · 0fdeff3 · 2026-11-03 02:22Z".
- Each demo ran over its own throwaway embedded store (`TMPDIR` set to a
  scratch directory). Each was signed in by typing its access code
  (`-access-code-file`) into the "Enter access code" page:
  - **A:** `grain demo -open=false -observability -staging -addr 127.0.0.1:4301`.
    It has the real `pkg/ci` service, host-directory CI sandboxes, the fake
    proxy and Actions caches, `top`, and two persistent sandboxes.
  - **B:** `grain demo -open=false -github-sim -fake-ci -base-branch-ci -reconcile 2s -agent-paused -host-mode-install -notifications -host-executor hostd-down -upgrade-image localhost:5055/x/grain -upgrade-allowed-branches latest,candidate,rc,main,v1.3.0,v1.2.0,v1.1.0 -addr 127.0.0.1:4302`.
  - **C:** `grain demo -open=false -unconfigured -addr 127.0.0.1:4303`.
  - **D:** `grain demo -open=false -host-executor hostd -addr 127.0.0.1:4304`.
    This runs a real grain hostd on a throwaway allowlist that is missing
    grain's own entries.
  - **E:** `grain demo -open=false -github-sim -fake-ci -base-branch-ci -addr 127.0.0.1:4305`,
    on the binary rebuilt with the fixes. It was used to check them live.
- A loopback OCI registry on `127.0.0.1:5055` (a small Node HTTP server)
  sat behind `-upgrade-image`. It lists nine tags and answers a fixed
  manifest digest.
- Playwright Chromium (`chromium-1234`, headless), from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900, 700×700
  and 390×844.
  - Each walk was a Node script driving `playwright`'s `chromium` through
    visible controls: role and label locators, clicks, typing, Tab and
    Enter, and native and MUI dialogs.
  - Each recorded console errors, page errors, failed requests and HTTP ≥
    400 responses.
  - The scripts lived outside the repository and are not committed.
- No Gemini key is in this sandbox (`/home/debian/.gemini-api-key` is
  absent), so nothing used a model and `LOOK=1` was not available.

## 1. Bugs found and fixed

### The red-base alert drew markdown as text and offered no way to its repair

- **Surface:** the alerts bell (`AlertsButton.jsx`, rows from
  `notifications.js`'s `notificationAlerts`) and the Inbox's Notifications
  group, for the base-branch reconciler's `pipeline.base-red:<repo>@<branch>`
  notification (`raiseBaseRed`, `pkg/orchestrator/pipelinenotifications.go`).
- **Reproduction (demo B, or E on the unfixed binary):**
  1. `POST /api/e2e/base-branch/red {"branch":"main"}`. This runs the real
     reconciler, which confirms `main` red and raises the notification.
  2. Open the bell. The row reads: "grain CI on \`main\` acme/widgets is
     confirmed red: at 55d6e7c72de2 ("failure: test"). grain has filed no
     repair for it …". The backticks are drawn literally, and the branch
     comes before its repository.
  3. With a repair open, the reconciler sets `taskId`, adds `open-repair`
     to `actions`, and writes "[Task #N](/tasks/N) is repairing it; …"
     into the body (`TestAConfirmedRedBaseRaisesBaseRedAndTheGreenVerdictClearsIt`
     pins this). The bell then showed that link syntax as text and offered
     only Acknowledge and Investigate. Nothing on the row opened the repair.
     The Inbox's row did have an "Open task" button, but its sentence had
     the same raw markup.
- **Expected:** the row reads as prose and offers the repair the
  notification names, as the task-sourced "main is red — repair #N" alert
  does with "Open #N".
- **Fix:**
  - `notificationAlerts` now leads with `{kind: "open-task", label: "Open #N"}`
    when a notification has a `taskId`. That is any notification about a
    task, so the seeded "A merge has been stalled for 2 hours" (task 1) now
    offers it too.
  - `notificationSentence` reduces a markdown link to its text and inline
    code to its contents. Both the bell and the Inbox use it.
  - The body now reads "grain CI on acme/widgets \`main\` is confirmed
    red: …".
- **Coverage:**
  - `notifications.test.js`: "opens the task a notification names, before
    anything else", and "draws a markdown body's links and inline code as
    their text".
  - The orchestrator's base-red test now pins the body's opening.
  - New `e2e/deployment-operations-base-red-notification.spec.js`. It drives
    a real red verdict on `-github-sim -fake-ci -base-branch-ci`, and stubs
    only the repair's id and sentence on `GET /api/notifications?open=1`,
    because filing a repair is the pipeline steward's job and the demo runs
    none. It asserts that the row has no backtick or `](/tasks/` and that
    "Open #1" opens `/tasks/1`. Then it drives green and sees the row go.
  - Against the unfixed bundle this spec failed (`Received string: "…grain
    CI on \`main\` acme/widgets … [Task #1](/tasks/1) …"`).
- **Checked live on demo E (fixed binary):**
  - The real row reads "grain CI on acme/widgets main is confirmed red: at
    4e351da5ec83 …".
  - With the repair stubbed, it reads "… Task #1 is repairing it …" with
    **open #1**, which opens `/tasks/1`.

### System's tabs named no panel

- **Surface:** `SystemPage.jsx`'s tab strip (desktop and narrow windows).
- **Reproduction:** open `/system` at 1440 px and look for
  `getByRole("tabpanel")`. There is none (a 30 s timeout).
  - No `Tab` has an `id` or `aria-controls`.
  - The open tab's content sits loose under the strip.
  - A repository's tabs (`RepoPage.jsx`'s `RepoTabPanel`) do this properly.
- **Expected:** the WAI-ARIA tabs pattern. The selected tab controls a
  `tabpanel` that it labels, so assistive technology can tie the content to
  its tab (WCAG 1.3.1 and 4.1.2).
- **Fix:**
  - Each tab is `id="system-tab-<id>"`.
  - The open tab's content is a `role="tabpanel"` with
    `id="system-tabpanel"` and `aria-labelledby` set to its tab.
  - Only the selected tab has `aria-controls`, because only its panel is in
    the document.
  - On a phone there is no tab strip, so the section heading names the page
    and nothing changes there.
  - The wrapper is an unstyled block `Box`, so nothing is drawn differently
    (see §4, screenshots).
- **Coverage:**
  - `SystemPage.test.jsx`: "puts the open tab in a tabpanel its tab names
    and controls".
  - `e2e/system-tabs.spec.js`: "each System tab opens a tabpanel named
    after it", which clicks all eight tabs. It failed on the unfixed bundle
    (`element(s) not found`).

## 2. Walkthrough and outcomes

### Navigation, URLs, history (demo A)

- **Opening System:**
  - The header rail's System button (`top-bar-system`) opens `/system` on
    Health.
  - It is the 30th Tab stop from the top of the page, and Enter opens it.
- **Tabs:**
  - Each of the eight tabs was clicked and then reloaded. Each kept
    `/system/<tab>` (Health is the bare `/system`) and its selected tab.
  - Back and Forward stepped metrics ⇄ power.
- **Old links:**
  - `/admin/logs?q=ci` → `/system/logs`
  - `/sandboxes` and `/sandboxes?tab=ci` → `/system/sandboxes`
  - `/caches` and `/caches?x=1` → `/system/caches`
  - `/metrics?window=30d&repo=acme%2Fwidgets` →
    `/system/metrics?f=repo:any:acme/widgets&window=30d`
  - `/system/upgrade` → `/settings/upgrade`
  - `/system/health` and `/system/bogus` → `/system`
  - Only Metrics has a query that means anything, and it is carried over.
    The other panes keep no state in the URL, so dropping a stray `?x=1`
    is correct.
- **Build stamp:** "v1.2.0 · 0fdeff3 · 2026-11-03 02:22Z" is a link to
  `/settings/upgrade`, and clicking it opens Settings → Upgrade.
- **Narrow window (700×700):** none of the eight sections scrolls sideways
  (`scrollWidth == clientWidth == 700`).
- **Phone (390×844):**
  - "Open navigation", then the drawer's System row, opens the section list
    (health … power, each with ›).
  - Sandboxes, Metrics and Caches each opened from the list, survived a
    reload at `/system/<tab>`, and measured `scrollWidth == 390`.
  - "All of System" (←) returns to `/system`.
  - The phone bar's bell reads "Alerts: 2 active".

### Health

- **Demo B lists:**
  - The host-mode install notice, with Migration docs →
    `…/design/design/cli-run-location.design.md#migrating-a-host-mode-install`.
  - **The hostd readiness note:** "grain hostd is not reachable, so no run:
    host server can start: grain hostd is not reachable at …: grain hostd is
    not running (grain-hostd.service is stopped, failing, or restarting for
    an upgrade); on the host, systemctl status grain-hostd and journalctl -u
    grain-hostd say why (dial unix …: no such file or directory)".
  - The GitHub API budget table, and the idle-poll backoff line ("2 of 3
    tracked PRs backed off · poll interval median 8m 0s …").
  - The seeded notifications and the connection pause.
- **The hostd note arrives about 5 s after the pane.**
  `GET /api/settings/cli-run` took 5.00 s on every call against the
  missing socket. That is `pkg/hostd/client.go` deliberately riding out a
  hostd restart before it reports hostd unreachable, so it was noted, not
  changed.
- **Demo D (a real hostd missing grain's own entries):** four
  `hostd-entry-missing` notes (host-git, grain-gchat, grain-gcp,
  grain-host-script). Each gives `sudo systemctl start
  grain-hostd-allowlist.service` and the YAML to paste. At 390 px the pane
  measured `scrollWidth == 390`, because the YAML block scrolls inside its
  own box.
- **Injected rows** (stub, §3):
  - "Saved, not applied until restart: Poll interval, GitHub host,
    Kubernetes sandbox pod image" and "Stored, but not runnable: Poll
    interval (stored 0s, running 30s)".
  - Following each link landed on its field with the input focused and on
    screen:

    | Link | Lands on | Focused input | Top (of 900) |
    | :--- | :--- | :--- | :--- |
    | Poll interval | `/settings/engine#pollInterval` | `pollInterval` | y=258 |
    | GitHub host | `/settings/credentials#githubHost` | `githubHost` | y=453 |
    | Kubernetes sandbox pod image | `/settings/sandboxes#k8sSandboxImage` | `k8sSandboxImage` | y=463 |

  - "A merged change reset settings on this deployment", with **config
    repo**.
  - **power** on the restart row.

### Sandboxes (demo A)

- **Two CI runs held** (walk/held-1 and walk/held-2): the live table folds
  them behind "ci sandboxes (2)", which reads "2 CI sandboxes · 4 vCPU /
  4.0 GiB · longest 10s (c341db5-0)", with `aria-expanded=false`.
- **Fold state:**
  - Opening it, then reloading, kept `aria-expanded=true`.
  - Closing it, then reloading, gave `false`.
  - The state is held in local storage (`CI_SANDBOXES_STORAGE_KEY`).
- **The rest of the pane:**
  - The standing sandboxes explanation.
  - The persistent sandboxes: docs-preview is running at
    `/staging/docs-preview/`, and grain-staging is restarting, "exited with
    status 1", with 4 restarts.
  - Host, budget and overcommit, as on 11-02.
  - The error, empty and persistent `commit`/`lastRefresh` states were
    walked on 11-02 with the same stubs. The code is unchanged since then,
    so they were not re-stubbed.

### Caches (demo A)

- **Named controls:** ten, each with its own name: "Flush apt packages",
  "Flush npm packages", "Flush Python packages", "Flush Go modules", "Flush
  Docker Hub layers", "Flush Playwright browsers", "Flush the whole proxy
  cache", "Flush the Actions cache of acme/widgets", "Flush the Actions
  cache of acme/gadgets" and "Flush the whole Actions cache".
- **Flushing a throwaway entry:** "Flush the Actions cache of acme/gadgets"
  sent `POST /api/actions-cache/flush?repo=acme%2Fgadgets`. The panel went
  to "10 entries, 2.0 GiB of 20.0 GiB", and that held through a reload.
  - No confirmation is asked. That is by design: a flush loses nothing a
    re-download does not restore, as the pane says.
- **Refresh** re-reads the pane.

### CI (demo A)

- **Opening it:** the rail's CI entry opens `/ci`.
- **Start a run:**
  - Repo defaults to "—", and Load workflows stays disabled until both
    repo and branch are given. The branch has no default, by design, as the
    form's comment explains.
  - Two runs were started from the form, and each followed to its run
    (`/ci/341db5a22b0e291d`, `/ci/816a249d04f8b029`).
- **Active sandboxes:** collapsed to "active sandboxes (2)"
  (`aria-expanded=false`), then expanded: "2 CI sandboxes running · longest
  8s (observe, held-1)".
- **Cancel run:**
  - Opened from the row's control "acme/widgets walk/held-1, …".
  - Dismissing the prompt ("Stop this run? …") left it running.
  - Accepting it sent `DELETE /api/ci/runs/341db5a22b0e291d {"reason":""}`,
    which left only **retry**, and that held through a reload.
- **Retry:** posted
  `{"repo":"acme/widgets","ref":"walk/held-1","commit":…,"event":"workflow_dispatch"}`
  and followed to `/ci/ab7cc8afd4e5a36c`.
- **Forgotten run id:** `/ci/00000000deadbeef` shows `no CI run
  "00000000deadbeef" -- a run is remembered for a while and then forgotten`.
- **Mixed states** (the stubbed run `feedfacefeedface`):
  - The row reads "5 jobs: 1 failure, 1 errored, 1 skipped, 1 success, 1
    pending · 1 cache hit".
  - The overlay orders failure first, with its output shown.
  - It offers **retry** and **retry failed jobs**.
- **Retry failed jobs:** posted
  `{"repo":"acme/widgets","ref":"walk/mixed",…,"event":"push","jobs":["observe","lint"],"recheck":true}`
  to the real daemon and followed to `/ci/4380f5103d3e2f50`.
- **Shared fault:** "failing the same way on 3 branches right now". With
  `ownerTaskId: "4"` (stub) it reads "Assigned to task #4", linking to
  `/tasks/4`.
- **Search:** "held-2" narrows the runs list.
- Opening a run from a task, and the shared-fault fold, were walked on 11-02
  and are unchanged since.

### Logs, Top

- **Logs:**
  - "Search logs" for "zzzz-no-such-line" shows "(no matching log lines)".
  - "demo" leaves 300 lines, all containing "demo".
- **Top:** the process table, busiest first.

### Pipeline (demo A)

- **Pause:**
  - Dismissing "Pause the pipeline? Grain will stop starting new agent and
    CI runs. Runs already in flight will continue." sent nothing. The bell
    stayed at 2.
  - Accepting it sent `POST /api/pipeline-pause {"reason":""}`.
  - The bell went to 3 with "Paused — only tasks you Run now will start |
    No new tasks are being picked up since 3:14:35 AM … — paused by hand",
    with resume now.
  - The pause survived a reload.
- **Resume:**
  - Dismissing "Resume the pipeline? …" changed nothing.
  - Accepting it sent `DELETE /api/pipeline-pause`, and the bell went back
    to 2.

### Power (demo A, POSTs intercepted)

| Control | Prompt | Dismissed | Accepted (stubbed answer) | UI |
| :--- | :--- | :--- | :--- | :--- |
| Restart grain | "Restart grain? Every task currently running is interrupted, and this UI is unreachable for a few seconds …" | nothing sent | 200 | "Restart requested. grain is coming back up; …" |
| Reboot host | "Reboot the host machine? … unreachable until the machine comes back up." | nothing sent | connection reset | "Reboot requested. This UI is unreachable until the host machine comes back up." |
| Shut down host | "Shut down the host machine? … grain stays down until somebody powers the machine back on by some other means." | nothing sent | 500 `{"error":"stub: shutdown refused"}` | "stub: shutdown refused" |

### Metrics (demo A)

- **Sections:** release notes (weekly and versions), throughput, latency,
  capacity, backlog and GitHub CI.
- **Window:** the options are last 24 hours, 7 days, 30 days and 90 days.
  Picking 90 days wrote `?window=90d`, which survived a reload.
- **States:**
  - A failed read (stub, 500) reads "The report could not be loaded (stub:
    metrics down). Refresh to try again."
  - A read held 4 s (stub) shows a progress bar.
- **Into the tasks:** the backlog's "task 2" opens `/tasks/2`. The
  capacity chips link to `/?f=state:any:proposed` and the like.
- **From a dashboard's throughput panel:**
  - A seeded `panels` dashboard had a metrics panel with
    `query: "f=repo:any:acme/widgets"` and `range: "30d"`.
  - Its **Open** link is
    `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`.
  - Metrics opens narrowed to "repo is any of acme/widgets", on "Last 30
    days", and the narrowing held through a reload.
  - A first seed had wrongly put `window=30d` in the panel's query. The
    panel ignores that, because its window is its `range` field
    (`panels.js`'s `panelMetricsWindow`), so it was not a bug.

### Settings → Upgrade (demo B)

- `/system/upgrade` → `/settings/upgrade`.
  - `GET /api/upgrade` answered in 5.3 s, held up by the same unreachable
    hostd dial as Health's note.
- **The panel:**
  - The install notice and the in-flight warning ("2 runs are in flight
    … 2 would be lost (antigravity 2)").
  - Per-framework interruption notes, Branch (required), "Image
    (optional)", "wait until this deployment is idle", and the database
    panel ("test connection", "migrate database to postgresql", not
    pressed).
- **Check mode:** "Settings → General → Updates" is a link button
  (`upgrade-check-settings-link`), and it opens `/settings/general`.
- **The Upgrade button:**
  - With Branch empty, pressing it only shows the browser's "Please fill
    out this field."
  - With Branch `v1.3.0`, the panel says "Will pull
    localhost:5055/x/grain:v1.3.0" and reads release notes (a 422, since the
    stub registry serves no image config, as on earlier runs).
- **Upgrading, `POST /api/upgrade` intercepted:**
  - Upgrade opens "confirm upgrade", which repeats the warning and ends
    "Upgrade anyway?".
  - Cancel sent nothing.
  - Upgrade sent `{"branch":"v1.3.0","afterTasksFinish":false}`. The 500
    answer was shown: "stub: registry refused the pull".

### Alerts bell

- **Red base branch, real** (demo B):
  - `POST /api/e2e/base-branch/red` raised "acme/widgets main is red" in
    the bell 28 s later, with the bell going from 2 to 3.
  - `…/green` cleared it 29 s later.
  - Both are inside the 30 s notifications poll (`notifications.js`'s
    `POLL_MS`), which is the delay by design. The wording of the row is
    §1's bug.
- **Injected into `/api/config` and `/api/tasks`** (stub): reconciler-down,
  a failing config-repo sync, host sandboxes, the reset notice, and a
  red-base repair on task 1.
  - The bell went to "Alerts: 7 active, critical", with Deployment first.
  - "Settings are not being exported — Settings have not been exported to
    acme/grain-config since …: git push origin main: remote: Permission
    denied." It offers config repo.
  - "main is red — repair #1 is waiting for approval … Other work targeting
    main waits until the repair merges." It offers open #1, submit and
    force merge.
  - "Reconcile loop stopped", with power.
  - "No isolation: runs execute on this host", with its impact line.
  - Lifting the injection took the bell back to 3 on the next poll, without
    a reload.
- **Connection pause** (`-agent-paused`): "Usage limit reached
  (antigravity) … until 9:05:09 AM (about 5h 57m)", with resume now and
  open connection. **resume now** on Health cleared it (3 → 2), and it
  stayed cleared through a reload.
- **Pipeline pause and manual mode:** see Pipeline. The alert appears and
  clears.
- **Setup (demo C):**
  - "2 setup steps left · Still to do: connect a git host, add a model
    connection." **finish setup** opens `/settings/git`.
  - The no-repository step was walked on 11-02 by stub and is unchanged.
- **See all:** opens System → Health (`/system` on desktop).

### Standing banners (demo B)

| Width | Install notice alone | With the disconnected banner raised | After recovery |
| :--- | :--- | :--- | :--- |
| 1440 | notice y=0, 48 px; System button y=72 | disconnected y=0, 48 px; notice y=48; System button y=120 | notice y=0; System button y=72 |
| 390 | notice y=0, 128 px | disconnected y=0, 108 px; notice y=108, 128 px | notice y=0, 128 px |

- Each banner pushes the rail and the page down, and nothing overlaps.
- **The disconnected banner** was raised by refusing `/api/config` and
  `/api/tasks` (stub). It read "This deployment cannot be reached: the
  connection to it failed. Unreachable since 3:17:51 AM -- retrying
  automatically; …", and it cleared within 5 s of the routes being
  restored.
- **The install notice** went once `installNotice` was dropped from
  `/api/config` (stub). The System button then sat at y=24.

### Console and network

No page errors and no unexpected console errors on any surface. These were
seen and expected:

- The 401 on `/` before signing in.
- `ERR_ABORTED` on `/api/tasks` when navigating mid-poll.
- The 404 for the forgotten run id.
- The 404 for `/api/repos/acme/widgets/github-branches` on demo A, which
  wires no GitHub.
- The 422 release-notes read against the stub registry.
- The stubbed 500s, refusals and connection resets.

## 3. Intercepted or stubbed routes

| Route | Method | Why |
| :--- | :--- | :--- |
| `127.0.0.1:5055` (a Node HTTP server) | GET `/v2/`, `/v2/x/grain/tags/list`, HEAD/GET `/v2/x/grain/manifests/*` | A loopback registry for `-upgrade-image`; this sandbox has none. |
| `/api/upgrade` | POST | Never upgrade. Answered 500. |
| `/api/host/restart`, `/api/host/reboot`, `/api/host/shutdown` | POST | Never touch the host. Answered 200, a connection reset, and a 500 JSON body. |
| `/api/restart-watch` | GET | Answered `{}` while Power was stubbed. It only watches for the daemon coming back. |
| `/api/settings` | GET | Added `pendingRestart` and `unrunnable`, which only a daemon with a running config to compare sets. |
| `/api/config` | GET | On demo B: added `reconcilerDown`, `hostSandboxes`, `stateRepoResetNotice` and `stateRepoSyncFailure`; dropped `installNotice`; and was refused, with `/api/tasks`, to raise the disconnected banner. ETag and If-None-Match stripped. |
| `/api/tasks` | GET | Task 1 was given `redBaseBranch: "main"` and a pull request, for the red-base repair alert. |
| `/api/notifications?open=1` | GET | On demo E and in the new spec: the real base-red notification was given the repair's `taskId` and sentence. Filing a repair is the pipeline steward's job, which no demo runs. |
| `/api/ci/runs` (list) | GET | Prepended the mixed-state run, and added `ownerTaskId` to the shared fault. |
| `/api/ci/runs/feedfacefeedface` | GET | One run with failure, errored, skipped, cache-hit and pending jobs, which the demo's one workflow cannot produce. The Retry failed jobs POST it led to was real. |
| `/api/metrics` | GET | Answered 500, and was held 4 s, for the failure and loading states. |

Everything else was the demos' own state, driven through the UI. Some setup
went through the API instead:

- `POST /api/boards`, to seed the dashboard. It was deleted afterwards.
- The demo's e2e endpoints `/api/e2e/base-branch/{red,green}`.

## 4. Tests run

- `npx vitest run` (the whole UI suite): 3686 passed.
- `go test ./pkg/orchestrator/ -run 'BaseRed|Notification'`: ok.
- `npx playwright test` over a bundle rebuilt with the fixes, 19 passed:
  - `e2e/deployment-operations-base-red-notification.spec.js`
  - `e2e/system-tabs.spec.js`
  - `e2e/deployment-operations-navigation.spec.js`
  - `e2e/system-health-phone-deep-link.spec.js`
  - `e2e/alerts-panel-layout.spec.js`
- Both new e2e tests failed against the unfixed bundle.
- `npx prettier --check` on the changed UI files, and `gofmt -l` on
  `pkg/orchestrator`: both clean.
- **Screenshots:**
  - `make ui-screenshots PAGES=/system,/system/sandboxes,/system/power,/ci`
    was run on `main` (`OUT=screenshots/before`) and on the fix
    (`OUT=screenshots/after`), at desktop (1440) and phone (390), light and
    dark. They are not committed.
  - No model was available to look at them, so the before and after shots
    were pixel-diffed with PIL instead:
    - Every shot differs only in the rail's build stamp (x 18–210, y
      873–881, the commit) and, on a phone, the 22×22 px corner at (53, 15).
    - Health on desktop also differs in one block, y 632–763. That block
      holds the seeded notifications' relative "Since …" times and the
      fix's own new **Open #1** on "A merge has been stalled for 2 hours",
      a seeded notification that names task 1.
    - Everything above y 632 on Health, and every other page, is
      pixel-identical. So the tabpanel wrapper moves nothing.
