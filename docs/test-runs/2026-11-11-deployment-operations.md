# Deployment Operations Walkthrough — 2026-11-11

System (Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics, Power),
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners, walked in a real browser against real `grain demo` servers.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/098073e4/task-4304-walk-through-system-health`.
- **Commit tested:** `aa94cd88f` (`main` at the start of the run; build
  stamp `v0.0.0-20261111083912-aa94cd88f166 · aa94cd8 · 2026-11-11 08:39Z`).
  The fix below was re-verified on `f76ee9ce4` (build stamp
  `v0.0.0-20261111091513-f76ee9ce4f48 · f76ee9c`).
- **Build:** `make frontend` (Vite bundle embedded in `pkg/ui/static`) then
  `go build ./cmd/grain`; every server is `grain demo -open=false
  -access-code-file <file>` over its own throwaway SQLite store, signed in
  through `POST /api/auth/access-code` as `alice@example.com`.
- **Browser:** Playwright 1.x headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), `timezoneId: UTC`.
  Viewports: 1440×900 light, 640×800 (narrow), 390×844 dark and light.
- **Model/tooling:** the sandbox carries no `/home/debian/.gemini-api-key`,
  so no Gemini-backed tooling was used, and `LOOK=1` screenshot description
  was unavailable; layout was checked with DOM geometry (bounding boxes,
  `scrollWidth`) rather than by eye.
- **Demo topologies:**

| Demo | Port | Flags | Used for |
| --- | --- | --- | --- |
| A | 7801 | `-observability -staging` | System tabs, Sandboxes, Caches, Logs, Top, Pipeline, Power, Metrics, Health links, config-driven alerts |
| B | 7802 | `-github-sim -fake-ci -base-branch-ci -notifications -agent-paused -host-mode-install -reconcile=1s -scheduled-workflows` | CI runs/retry/cancel/faults, agent pause, install notice, red base branch, GitHub budget |
| C | 7803 | `-unconfigured` (fixed binary) | setup alerts, empty states, disconnected banner (failed polls and `SIGSTOP`) |
| D | 7804 | `-upgrade-image=docker.io/grainai/grain -upgrade-allowed-branches=main,latest,v3.2.0 -agent-paused` (fixed binary) | Settings → Upgrade in image mode, re-verification of the fix |
| E | 7805 | `-disable-self-upgrade -management-url=https://console.example.com/deployments/grain-prod -observability` (fixed binary) | externally managed upgrade |

## Findings

### F1 (fixed): a usage-limit pause ending on another day read as today

- **Where:** the alerts bell, System → Health, Settings → Models,
  Settings → model defaults, and a queued task's pane — everything built on
  `pauseMessage` / `pausedUntil` (`ui/src/alerts.js`).
- **Reproduction (deterministic):** demo B (`-agent-paused`); intercept
  `GET /api/config` and move `agentPause.until` and each
  `agentPause.pauses[].until` three days out; open the bell.
- **Observed:** `Agent usage limit reached: nothing is being dispatched onto
  connection Gemini 2.5 Pro until 9:13:06 AM (about 72h 0m).` — the clock
  time alone, which reads as this morning. The same row's scope line
  already said `Until 11/14/2026, 9:40:08 AM`; only the sentence dropped
  the day.
- **Cause:** both helpers formatted the instant with `formatTime` (clock
  time only) instead of `formatMoment`, which `time.js` provides for
  exactly this case and which the pipeline pause's "since" already uses.
- **Fix:** `pauseMessage(entry, zone, now)` and `pausedUntil(pause, zone,
  now)` use `formatMoment`: the clock time when the pause ends today in
  the deployment's zone, the date and time otherwise.
- **Verified:** demo D on the rebuilt binary reads `until 11/14/2026,
  9:24:49 AM (about 72h 0m)` for the moved pause and `until 3:24:02 PM
  (about 5h 59m)` for the demo's own same-day pause.
- **Regression coverage:** a unit test in `ui/src/alerts.test.js` ("names
  the day of a pause that does not end today") and a Playwright spec
  `ui/e2e/deployment-operations-pause-date.spec.js`, which fails on the
  pre-fix bundle (`Expected substring: "until 11/14/2026"`, received
  `until 9:40:08 AM`) and passes with the fix.

### Observations (no change made)

- **Cache flushes have no confirmation.** That is deliberate
  (`PackageCachePanel.jsx`: "A flush needs no confirmation and gets none:
  nothing is lost that a re-download does not put back"). Each Flush button
  is named after its target (`Flush npm packages`, `Flush the Actions cache
  of acme/gadgets`, `Flush the whole proxy cache`, `Flush the whole Actions
  cache`, …), and a flushed kind's button is disabled once it holds 0
  entries.
- **Pipeline, Power and Cancel run confirm with the browser's native
  `confirm()` / `prompt()`**, not an in-page dialog. They behave correctly:
  dismissing sends nothing.
- **The `Image source` card is gone.** Commits `6d99ffd04` / `c1a183ce2`
  ("remove release-assets image source and pull from registries") removed
  it, so that part of the brief is out of date. The pane says "Pulls the
  image CI published for the branch below" instead, and the existing
  `upgrade-image-chooser.spec.js` asserts the card is absent.
- **Retry is offered only on a run that ended without passing**
  (`retryableRun`). A successful run has no Retry, and a cancelled run has
  Retry but not "Retry failed jobs" (nothing narrower to retry), as
  `ci-retry.spec.js` describes.
- **The CI page's "Search runs…" text is not carried in the URL** (reload
  clears it). Metrics' window choice replaces the history entry rather
  than pushing one, so Back leaves Metrics instead of stepping back to the
  previous window. The window does ride the URL through reload. Neither
  looks like a defect.
- **`net::ERR_ABORTED` on `/api/tasks` every poll** is how Chromium reports
  the bodiless `304 Not Modified` the conditional task-list poll gets. Each
  one directly follows a recorded `304` response; it is not a failed read.
- **`404 GET /api/repos/acme/widgets/github-branches`** on demo A is the
  server saying "GitHub branch listing is not available on this UI server"
  on a demo without GitHub. The branch field copes.
- **`400 GET /api/upgrade/release-notes?branch=v3`** comes from the
  debounced release-notes preview while a partial branch is typed. The
  server's message names the allowed branches, which is useful feedback.

## Walkthrough steps & outcomes

All PASS unless noted. The first attempt at several steps failed on a
locator of the walkthrough's own (a native dialog, a header-rail control,
a route glob); each was re-run with a corrected locator and the outcome
below is the re-run's.

### 1. System navigation, tabs, URLs, layouts (demo A)

1. Header rail `System` (`top-bar-system`) → `/system`, Health selected.
2. Every tab clicked: Health `/system`, Logs `/system/logs`, Sandboxes
   `/system/sandboxes`, Caches `/system/caches`, Top `/system/top`, Pipeline
   `/system/pipeline`, Metrics `/system/metrics`, Power `/system/power`.
   For each, `aria-selected="true"` and `aria-controls="system-tabpanel"`,
   and the panel's `aria-labelledby` is the tab's id.
3. Reload on `/system/power` keeps Power. Back → `/system/metrics`,
   Forward → `/system/power`.
4. Keyboard: focus Health, ArrowRight + Enter → Logs (`/system/logs`).
5. Old links: `/admin/logs` → `/system/logs`; `/sandboxes` →
   `/system/sandboxes`; `/caches` → `/system/caches`;
   `/metrics?window=24h&repo=acme%2Fwidgets&q=flaky` →
   `/system/metrics?q=flaky&f=repo:any:acme/widgets&window=24h` (query kept
   and migrated); `/system/upgrade` → `/settings/upgrade`;
   `/system/bogus` → `/system`.
6. Rail build stamp (`build-stamp`) → `/settings/upgrade` (demo D).
7. 640×800: the tab strip scrolls inside itself (`scrollWidth 730`,
   `clientWidth 280`, `scrollLeft 370` on Power) with no page overflow.
8. 390×844 dark: drawer → System row → section list (Health, Logs,
   Sandboxes, Caches, Top, Pipeline, Metrics, Power). Every section opened
   with `scrollWidth === 390`.

### 2. Health

1. Bell (`Alerts: 2 active` on demo A) and Health list the same alerts.
   "See all on System → Health" → `/system`. `Open #1` on the stalled-merge
   notification → `/tasks/1`.
2. Live sandbox overcommit status (`overcommit: auto`, effective factors,
   PSI, adjustment `10/24/2026, 9:20:00 AM`).
3. GitHub API budget and idle-poll backoff panel (`GitHub API: 1,650 of
   5,000 calls left this hour … At that rate it runs out in 30m 0s`,
   per-caller table). Registered daemon health counters
   (`sign-in verification (demo)`: `proxy:admitted 51`, …).
4. Settings pending a restart and stored-but-unrunnable (`GET /api/settings`
   intercepted to report `pendingRestart: ["maxWorkers"]` and `unrunnable:
   [{key: "sandboxCpus", stored: 64, running: 4}]`). `Max worker agents` →
   `/settings/engine#maxWorkers`, focus on `<input name="maxWorkers">`.
   `Sandbox vCPUs` → `/settings/sandboxes#sandboxCpus`, focus on
   `<input name="sandboxCpus">`. The pending row's Power link →
   `/system/power`.
5. Host readiness (`GET /api/settings/cli-run` intercepted). Unreachable:
   "The host agent is not reachable, so no run: host server can start: dial
   unix …". Reachable but missing an entry: "The host agent's allowlist on
   the host has no capabilities entry missing-cli-tool. It is grain's own
   entry…".
6. Host-mode install notice in Health (demo B): "Unmigrated host-mode
   install", the command, "CLIs that lost their run location: claude", and
   Migration docs.

### 3. Sandboxes (demo A)

1. Host commitment and pressure, budget (`within budget cap`, `Committed:
   0.0 GiB of 14.0 GiB memory · 0 of 4 vCPU`), cluster free figures,
   overcommit controller, reconciliation, storage, CI history, CI job
   cache and CI artifacts panels.
2. Deployment zone: with `timeZone` set to `America/New_York` through
   `PUT /api/settings`, the overcommit adjustment moved from `10/24/2026,
   9:20:00 AM` to `10/24/2026, 5:20:00 AM`. Restored afterwards.
3. A real CI run (`.grain/workflows/observability.yml`) and an agent
   sandbox (`POST /api/e2e/sandboxes/other`). The agent row
   `1-identity-check agent host …` sits above the `CI sandboxes (1)` summary
   row. That row is folded on load (`aria-expanded="false"`), stays open
   across a reload once opened, and stays closed across a reload once
   closed again.
4. Persistent sandboxes: `docs-preview` (running) and `grain-staging`
   (restarting, `exited with status 1`, 4 restarts). Running commit and last
   refresh (`/api/sandboxes` augmented with `commit`/`refresh`/`lastRefresh`,
   which the demo does not seed) → `running 1a2b3c4d5e6f · follows
   candidate`, `refreshed 2h ago: candidate moved; CI green at 1a2b3c4d`.
   Empty state with its director link.
5. `500` on `/api/sandboxes` shows the error. Refresh recovers. 60 synthetic
   rows with long names cause no page overflow.

### 4. Caches (demo A)

Proxy cache total 5.3 GiB across six ecosystems. Actions cache `20 entries,
3.0 GiB of 20.0 GiB` with per-repository rows. Refresh. `Flush npm packages`
→ `npm packages: 0 packages, 0 B` after reload, button then disabled.
`Flush the Actions cache of acme/gadgets` → its row gone after reload. Ten
distinctly named flush controls.

### 5. Logs, Top (demo A)

The log pane follows the tail on load (`scrollTop` at the bottom of 5396px).
`Search logs` "merge-queue" → 50 of 300 lines, all matching. Clearing it
restores 300. A no-match search → "(no matching log lines)". A `500` on
`GET /api/logs/<source>` during Refresh raises the page's error; the next
Refresh clears it. The log region is focusable (`tabindex="0"`). Top lists
`grain-daemon` among its processes.

### 6. Pipeline, Power (demo A)

1. Pause with reason "walkthrough pause": dismissing the confirmation ("Pause
   the pipeline? Grain will stop starting new agent and CI runs…") leaves
   it running. Accepting pauses it in manual mode ("Paused — only tasks you
   Run now will start … Paused by alice@example.com (access code). Reason:
   walkthrough pause"), which persists through reload. Bell goes from
   `Alerts: 2 active` to `3 active`, with the "Paused — only tasks you Run
   now will start" row (Resume now) also on Health.
2. Resume: dismissing keeps it paused. Accepting resumes, the alert clears
   (`Alerts: 2 active`) and the state persists through reload.
3. Power (`POST /api/host/{restart,reboot,shutdown}` intercepted). For each
   of Restart grain, Reboot host and Shut down host: dismissing the native
   confirm sends 0 requests. `202` shows "Restart requested… / Reboot
   requested… / Shutdown requested…". `500` shows `walkthrough: <action>
   refused` and drops the requested notice. `GET /api/config` still 200
   afterwards: nothing restarted.

### 7. CI (demo B, `acme/widgets` switched to `ciMode: grain`)

1. Runs started with `POST /api/ci/runs` and settled through
   `POST /api/e2e/fake-ci/runs/{id}/finish` (failure, success, a failing
   `unit` job, left pending). Each row's open control is named after its
   own run (`acme/widgets main, failure, started 11/11/2026, 9:07:04 AM`,
   …), with no duplicates.
2. Failed run → pane with `Retry` and `Retry failed jobs` and the `unit`
   job's output toggle. `Retry failed jobs` follows to `/ci/fake-ci-5`.
   `Cancel run` there raises "Stop this run? … Optionally, say why".
   Dismissing keeps it running; accepting gives conclusion `cancelled`,
   persisted through reload. `Retry` on the cancelled run follows to
   `/ci/fake-ci-6` (no "Retry failed jobs" offered). `← Back` → `/ci`.
3. Direct link and reload on a run id work. A forgotten id
   (`/ci/0000forgotten0000`) → `no CI run "0000forgotten0000" -- a run is
   remembered for a while and then forgotten`.
4. Shared fault: "1 shared failure … acme/widgets · CI / e2e (Run go test
   ./...) failing the same way on 3 branches". With `ownerTaskId` added
   (intercept): "Assigned to task #3 (fix/flaky-session)" → `/tasks/3`.
5. States (multi-job run via `/api/ci/runs/{id}` intercept): failure,
   errored, cache hit (cached success), success, pending and skipped each
   drawn with their own label.
6. Search runs "abc1240" narrows to that commit's runs. Active sandboxes
   summary on demo B: "No CI job is on a machine right now". On demo A it
   was "1 CI sandbox running · longest 5m 31s (observe, main)". The CI page
   at 390px has no overflow.
7. No run in these demos is linked to a task, so "open a run from a task
   link" had nothing to open there. Fault → task navigation is covered in
   step 4.

### 8. Metrics (demo A)

Sections: release notes, throughput, latency, capacity, backlog, GitHub CI.
Window combobox (last 24 hours / 7 days / 30 days / 90 days) →
`?window=24h` and `?window=30d`, kept through reload with the figures
changing (14.0/day filed over 24h, 0.7/day over 30d). Release notes Weekly
(week counts, by direction) and Versions (v3.2.0 chip → notes "Released
2026-11-09 from commit cba7d44bb…"). Narrowing
`?q=flaky&f=repo:any:acme/widgets&f=state:any:open` → "Measured over the
tasks in acme/widgets.". Empty repository window renders. A slow (3s)
report shows a progressbar. A `500` → "The report could not be loaded
(walkthrough: metrics store unreadable). Refresh to try again." and the
header Refresh recovers. Task link `#20` → `/tasks/20`. Count link →
`/?f=state:any:running`. 390px: no overflow. Dashboard: a saved `panels`
board with a `metrics` panel (`f=repo:any:acme/widgets`, 7d) →
`.panel-open` → `/system/metrics?f=repo:any:acme/widgets`, "Measured over
the tasks in acme/widgets." (board deleted afterwards).

### 9. Settings → Upgrade (demos D and E)

1. A progressbar shows while `GET /api/upgrade` is held 2.5s.
2. `Now running f76ee9c.` (no ref file: the build's version from
   `/api/config`).
3. Database Backend: "This deployment runs on SQLite. The store is moved
   only with the daemon stopped, never from here…" with the `setup.sh`
   migration commands.
4. Upgrade check mode is read-only. Its link "Settings → General → Updates"
   → `/settings/general#updates`, focus in `upgradeCheckMode`.
5. Target picker: `latest`, `main` and `v3.2.0` allowed, `candidate`,
   `v3.5.1` … marked "not allowed on this deployment". Typing `v3` narrows
   to the v3 tags.
6. Will pull `docker.io/grainai/grain:main`. With an image typed:
   `docker.io/grainai/grain:custom-build-42`. Release notes render for
   `v3.2.0` (no breaking-change line in that release's notes).
7. Upgrade (`POST /api/upgrade` intercepted with a `500`): posted
   `{"branch":"main","afterTasksFinish":false}` and the pane shows
   `walkthrough: upgrade intercepted, nothing pulled`.
8. Retracted running release (status augmented with `retractedRelease`):
   "Release v3.5.0 was retracted. v3.6.0 has been published to fix forward
   from it…" with `Upgrade to v3.6.0` and `Move back to latest (v3.4.0)`.
9. Another edition (status augmented): not among the chips until "Switch
   edition…" is pressed.
10. 390px: no overflow.
11. Demo E: `upgrade-externally-managed` "In-VM self-upgrade is disabled on
    this deployment; upgrades and instance lifecycle are managed
    externally." Manage instance link `href=https://console.example.com/…`,
    `target=_blank`. No upgrade form, no check-mode link. `/system/upgrade`
    → `/settings/upgrade`.

### 10. Alerts bell and standing banners

1. Agent pause (demo B, real): "Usage limit reached (antigravity) … until
   3:06:23 PM (about 5h 54m)". `Open connection` → `/settings/models`.
   `Resume now` clears it (`Alerts: 2 active`, `agentPause.paused` false
   after reload). Resume time on another day: finding F1, fixed and
   re-verified.
2. Pipeline pause and manual mode: section 6.
3. Reconciler down (intercept): "Reconcile loop stopped … Restart the daemon
   to recover." critical, `Power` → `/system/power`. It clears when the
   cause does.
4. Config-repo sync (intercept). Export: "Settings are not being exported
   … since 11/8/2026, 9:17:27 AM: push rejected" (a date, since not today).
   External pull: "Configuration is not being pulled from
   https://github.com/acme/grain-config.git since 11/11/2026, …". Both lead
   to `Config repo`, are critical, and clear.
5. Host sandboxes (intercept): adds the "No isolation" warning
   (`Alerts: 3 active`), which clears.
6. Setup (demo C; counts walked by intercepting `config.agentBackends` and
   `config.deployment.targetRepos`): "2 setup steps left — connect a git
   host, add a model connection" → Finish setup `/settings/git`. "1 setup
   step left — connect a git host" → `/settings/git`. "1 setup step left —
   add a model connection" → `/settings/models`.
7. Red base branch (demo B, real reconciler, fake-ci verdict):
   `POST /api/e2e/base-branch/red` → "acme/widgets main is red … grain has
   filed no repair for it". The demo runs no pipeline steward to file
   repairs, so the repair link was intercepted on `/api/tasks`: "main is
   red — repair #5 …" → `Open #5` → `/tasks/5`. It clears once the task is
   no longer a repair. `POST /api/e2e/base-branch/green` clears the red
   notice.
8. Phone bar bell (390px): the popover stays inside the viewport
   (x 81.8, width 269.5).
9. Host-mode install banner (demo B): at 1440, banner 0–68px with rail and
   header at 68px and main at 145px. At 390, banner 0–128px, header at
   128px, `scrollWidth 390`. Removing `installNotice` (intercept) clears it.
10. Disconnected banner (demo C). Failed polls (all `/api/**` aborted): at
    1440, banner 0–48px, rail and header at 48px. At 390, 0–108px, header at
    108px. "This deployment cannot be reached: the connection to it failed.
    Unreachable since 9:26:14 AM -- retrying automatically…"; it clears
    without a reload once requests flow. Frozen daemon (`kill -STOP`): the
    banner rose after 24s with "it accepted the connection but sent no
    answer within 10 seconds", and cleared 83ms after `kill -CONT`, with no
    reload.

### Console & network

Excluding the probes this walkthrough injected and the 304
`ERR_ABORTED` artefact above: 0 `pageerror` exceptions and 0 console errors
in every session. The only other 4xx responses were the expected `404` for
a forgotten CI run, the demo's "not available" `github-branches` `404`, and
the release-notes `400` while typing.

## Stubbed & intercepted boundaries

| Boundary | Mechanism | Why |
| --- | --- | --- |
| `POST /api/host/{restart,reboot,shutdown}` | `page.route` → `202` / `500` | Never restart, reboot or shut down the host. |
| `POST /api/upgrade` | `page.route` → `500` | Never self-upgrade. |
| `GET /api/upgrade` (delay; `retractedRelease`, `discardedRC`; `otherEditionImageRepos`) | `page.route` delaying or augmenting the real answer | Loading state, and release states only a live registry produces. |
| `GET /api/config` (`agentPause.until` +3 days; `reconcilerDown`; `stateRepoSyncFailure`; `hostSandboxes`; `installNotice` removed; setup-step fields) | `page.route` augmenting the real answer | Causes only a live boundary can raise. Each was unrouted to confirm the alert clears. |
| `GET /api/settings` (`pendingRestart`, `unrunnable`) | `page.route` augmenting | Saved-but-unapplied settings need a restart cycle. |
| `GET /api/settings/cli-run` (`hostExecutor.hostd`) | `page.route` augmenting | No host agent in the sandbox. |
| `GET /api/sandboxes` (persistent `commit`/`lastRefresh`; empty; `500`; 60 rows) | `page.route` | The demo seeds no running commit. Error and long-table states. |
| `GET /api/ci/runs` (`faults[].ownerTaskId`), `GET /api/ci/runs/{id}` (multi-job states) | `page.route` augmenting | The demo's fault has no owner; fake CI runs carry at most one job. |
| `GET /api/tasks` (one task's `redBaseBranch`) | `page.route` augmenting | The demo runs no pipeline steward to file a repair. |
| `GET /api/logs/<source>`, `GET /api/metrics` | `page.route` → `500` / delay | Error and loading states. |
| `/api/**` aborted; daemon `SIGSTOP`/`SIGCONT` | `page.route` abort; `kill` | Disconnected banner, both causes. |
| Fake CI verdicts, agent sandbox, red/green base branch | built-in `/api/e2e/fake-ci/runs/{id}/finish`, `/api/e2e/sandboxes/other`, `/api/e2e/base-branch/{red,green}` | The demos' own drivers inside the real server. |

Everything else ran against the real Go servers and the real built frontend,
through visible controls. No screenshots or browser artefacts are committed.
