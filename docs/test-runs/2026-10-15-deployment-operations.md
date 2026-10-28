# Deployment Settings and Operations Walkthrough (2026-10-15)

**Date:** 2026-10-15
**Branch:** `grain/eu/task-3754` (task 3754), based on `candidate`
**Commit tested:** `9788ecb1e` (`Merge pull request #3832 from bwsalmon/grain/eu/task-3744`), the tip of `candidate`. The fixes below were then re-verified on the branch in the same browser against a rebuilt server.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with headless Chromium (`chromium-headless-shell-1234`).
**Server under test:** `grain demo -addr 127.0.0.1:18420 -open=false -observability -as alice`. It was built with `go build -tags grain_e2e` after `npm ci && npm run build` had written the frontend into `pkg/ui/static`, and it ran over the throwaway embedded SQLite store `grain demo` creates under `TMPDIR`. It was restarted once, onto the rebuilt binary, to re-verify the fixes.
**Model and tooling:** this sandbox had no `/home/debian/.gemini-api-key`, so no Gemini API call was made. Gemini model and reasoning-effort selection was exercised on Team settings → Models & connections, against the demo's own Antigravity connection, which advertises `gemini-2.5-pro` and `gemini-2.5-flash`.
**Sandbox note:** as on 10-13, `npx playwright install chromium` needs `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt` behind the package-cache proxy.

## How it was driven

Ad-hoc Playwright scripts in `ui/.walk/*.mjs` drove the walkthrough. They were excluded through `.git/info/exclude` and not committed. Each one launched headless Chromium against the demo server, sent `X-Grain-Admin-Mode: on` on every request and seeded `localStorage["grain.username"] = "alice"`.

All interaction went through visible controls: tabs, radios, native and MUI selects, comboboxes, text and number boxes saved on blur, buttons, native `confirm()`/`prompt()` dialogs (each both dismissed and accepted), MUI confirm dialogs, and keyboard Tab, Arrow and Escape.

Every script logged console errors, page errors, every HTTP response of 400 or above, and every failed request. Mutations were confirmed by reloading, by deep links and by reading the matching API. No screenshots or traces were kept.

## Bugs found and fixed

### 1. Five operations panes hid a failed first read

**Reproduction (deterministic, stubbed):**

1. In the browser, answer one GET with `500 {"error":"intercepted failure"}`.
2. Open the pane and wait 7.5 s, until the snackbar has faded.

| Pane | Failed read | Observed |
|---|---|---|
| System → Caches (`/admin/caches`) | `/api/package-cache` or `/api/actions-cache` | "Loading caches" spinner for as long as the pane is open |
| CI (`/t/default/ci`) | `/api/ci/runs?…` | "Loading CI runs" spinner forever, plus a snackbar on every 5 s poll |
| System → Top (`/admin/top`) | `/api/host/top` | nothing below the tab strip |
| System → Config repo (`/admin/state`) | `/api/state-repo` | nothing below the tab strip |
| System → All Teams (`/admin/teams`) | `/api/teams` | only the heading and **Create Team**, which reads as a deployment with no teams |

The 10-13 walkthrough found Caches, the CI list and All Teams and proposed them as a follow-up. That follow-up had not landed on `candidate`, so it was fixed here. Top and Config repo are new findings.

**Fix:** each pane now does what `SandboxHealthPage`, `LogsPage` and `UpgradePanel` already do. It keeps the latest read error and shows it in place:

- **Before any read has succeeded:** an error instead of the spinner or the blank.
- **After a read has succeeded:** a warning that the figures shown are from the last successful read.
- **Panes that do not poll:** Top's **Refresh** and **Try again** on Config repo and All Teams.
- **Polling panes:** one snackbar per run of failures rather than one per poll.

The changed files are `CachesPage.jsx`, `CIPage.jsx`, `TopPage.jsx`, `StateRepoPanel.jsx` and `TeamsPanel.jsx`.

**Regression:**

- Unit tests in `TopPage.test.jsx`, `CachesPage.test.jsx`, `StateRepoPanel.test.jsx` and `TeamsPanel.test.jsx`. All four fail without the fix.
- `ui/e2e/deployment-operations-failed-reads.spec.js` runs all five panes in Chromium. Each test fails the read, checks the message and that no spinner is left, then lets the read through and checks the pane recovers.

**Not changed:** with `/api/settings` failing, the General tab is blank. The System overlay itself depends on settings, so this belongs to the overlay shell rather than to one pane, and it was left as an observation.

## Coverage and outcomes

### Settings

- **Tabs and horizontal scrolling:**
  - All 17 System tabs were opened by click, each on its own `/admin/<tab>` path.
  - At 1280 px the strip scrolls (scroll width 2009 px, client width 911 px), and the selected tab was inside the scroller's visible box every time.
  - At 600 px, `/admin/upgrade` opened with Upgrade scrolled into view.
  - ArrowRight ×17 wraps back to the first tab, and no page overflowed horizontally.
  - Bare `/system` lands on `/admin`. `/admin/nonsense` keeps its URL and shows General.
- **Browser-local preferences (`/settings`):**
  - Dark, Oldest first and Asia/Tokyo were stored as `grain.themeMode=dark`, `grain.timelineOrder=oldest` and `grain.userTimeZone=Asia/Tokyo`. The body turned `rgb(15, 16, 19)`, and all three survived a reload.
  - ArrowLeft from Dark selected Light.
  - Setting Light, Newest first and Browser default cleared the order and zone keys.
- **General:** Environment name `walk-ops-2026-10-15` saved on blur and survived a reload. Clearing it saved too.
- **Agents & Task Branches:**

  | Field | Refused | Accepted and persisted |
  |---|---|---|
  | Max agent turns | `-5` (server: `cannot be negative`, box rolled back); `2.5` (browser, no request) | `40`, then `0` |
  | Branch template | `no-id-here` (`must contain {id}`) | `walk/{deployment}/t-{id}`, then cleared |
  | Deployment ID | `UPPER_case` (server rule shown) | — |

  The "Switch to grain/{deployment}/task-{id}" suggestion shows while the template is empty.
- **Engine:**
  - **Refused:** Poll interval `abc` and `0s`; Max merges `-1`; CI queue age boost `nonsense`; and a 20-digit CI cache size. The last one's 400 carries Go's raw `json: cannot unmarshal number … of type int`.
  - **Persisted:** `15s`, then back to `10s`; and Merge batch size `0`, which reads back as the default.
  - **Merge gather window `-5m`:** accepted and persisted as `-5m0s`. This is deliberate. A negative window turns the wait off (`pkg/ui/settings.go`, `model.Config.MergeGatherWindow`). It was then cleared back to the default through the UI.
- **Sandbox resources, network and bootstrap:**
  - **Refused:** vCPUs `-2`; memory `12` (`at least 128`); disk floor `1.5`.
  - **Persisted:** vCPUs `4`, then cleared; network `none`, then `open`; bootstrap script `echo walk-2026-10-15`, then cleared.
  - CPU overcommit `0` is accepted, and means unbounded.
- **Host commitment (stubbed settings):** with an over-committed `sandboxCommitment`, Sandbox & Infrastructure says "8 workers + 2 mergers + 6 CI jobs × 4 vCPU = 64 vCPU committed against the 8 this host has (8.0×) … 128.0 GiB … of the 32.0 GiB it has (400%)".
- **Restart-required and unrunnable notices (stubbed):** "Stored, but not runnable: Poll interval (stored 0s, running 30s)" and "Saved, but not applied yet: GitHub host, Sandbox backend" appear on General, Engine, Sandbox and Integrations, and not on Logs.
- **GitHub configuration (Integrations → Deployment-wide):**
  - `https://github.com/` was refused ("a host name, not a URL").
  - `ghe.example.com:8443` persisted across a reload.
  - Blank was refused ("cannot be empty").
  - `github.com` was restored.
  - **Note:** the box first showed empty, because the demo stores no host. Clearing a host that was set is therefore refused, although the box started blank.
- **Agent and model selection (Gemini):** on `/t/default/settings/models`:
  - Typing `flash` into Default model offered only `gemini-2.5-flash`. Picking it sent `PUT /api/teams/default {"defaultModel":"gemini-2.5-flash"}`.
  - The effort choices for `gemini-2.5-pro` were model default, low, medium and high. `high` sent `{"modelSettings":[{"model":"gemini-2.5-pro","effort":"high"}]}`.
  - Both survived a reload and matched `GET /api/teams/default`.
  - ArrowDown opens the combobox and Escape closes it.
  - Both were restored, to "None" and model default.
- **Capabilities and secrets:**
  - All 27 Integrations sub-tabs opened without a console error.
  - **Dummy secret:** `walk-dummy`/`token` was set with a non-credential string. The value box cleared at once, the name was listed, and the value never appeared in the DOM before or after a reload.
  - **Delete secret:** a confirm was raised and accepted. `DELETE /api/secrets/walk-dummy` was sent, and the secret was gone after a reload.
  - **Gemini key, real demo:** setting a dummy `gcp-key-minter` turned the capability "ready". **Test credential** then reported the demo's own "Credential refused … nothing here is real".
  - **Gemini key, stubbed check:** a stubbed success showed "Credential works". A stubbed 502 cleared that result and raised the error.
  - **Clear:** dismissing the native confirm sent nothing. Accepting it sent `DELETE /api/secrets/gcp-key-minter/value`, and the capability returned to "not ready".
  - No real credential was stored.
- **State repository:**
  - The demo is not a daemon, and says so: "not running inside a daemon that owns a config repository".
  - A failed read found bug 1.
- **Memory:**
  - `/memory` resolves to `/t/default/memory`, which shows three scopes.
  - Editing one enabled only that scope's Save and Revert.
  - Save sent `PUT /api/memory`, and the edit survived a reload. The original text was then restored and verified.
- **Upgrade planning (stubbed upgrader):**
  - **Confirm dialog:** with `inFlightRuns: 2`, **Upgrade** opens "confirm upgrade — 2 runs are in flight …". **Cancel** and Escape both sent nothing, and Escape returned focus to Upgrade.
  - **Bad branch:** `../evil;rm` got the stub's 400, which was shown.
  - **Failed upgrade:** `candidate` went to running, then failed with the stubbed health-check detail.
  - **Scheduled upgrade:** "Wait until this deployment is idle" relabels the button **Schedule upgrade**. The pane said "Waiting to upgrade to candidate", and **Cancel** sent `DELETE /api/upgrade/scheduled` and removed the schedule.

### Sandboxes

- **Real (demo):** load average, utilization, memory and swap with sparklines, the storage breakdown, proxy cache, CI job cache and CI history.
- **Stubbed pressure and long table:**
  - Load 31.5/28/20 of 4 vCPU, 98 %, 16000/16384 MB memory and 4000/4096 MB swap all render.
  - 60 sandboxes render 61 rows. The 9 failed probes show their error chip.
  - The table scrolls in its own box (1707 px inside 704 px), the page does not overflow, and the last row scrolls into view.
  - Sparklines accumulate across polls.
- **Health, error and empty states:**
  - `hostError` shows "Host stats unavailable: …".
  - `{enabled:false}` shows "Not available: this deployment has no sandbox pool or host stats configured."
  - A 500 shows "Sandbox health could not be read …". This is the 10-13 fix, and **Refresh** recovers from it.

### CI

- **Start a run:** **Start a run** → repo `acme/widgets` → Branch `walk/1015-a` → **Load workflows** → **Start run**.
  - The run opened at `/t/default/ci/<id>`, with its sandbox listed under **Active sandboxes (1)**.
  - It stayed running until `/api/e2e/ci/finish` released it, after which it was **success** in 6 s.
  - Reloading the run's URL reopened it.
- **Run, jobs, steps and log:**
  - The overlay lists the job, and the job expands to its sandbox, its step and **show output**.
  - Escape returns to `/t/default/ci`.
  - `/ci/<id>` rewrites to `/t/default/ci/<id>`.
  - `/t/default/ci/0000000000000000` says "no CI run … remembered for a while and then forgotten".
- **Cache:** a second run on the same branch concluded at once, and the cache line moved to "1 entry · 698 B".
- **Cancel:**
  - **Cancel run** raises a `prompt()`. Dismissing it left the run running.
  - Accepting it with "walk cancel" cancelled the run, whose pane reads "Cancelled: walk cancel".
  - As on 10-13, the active-sandbox row lagged by about one poll.
- **Retry:** no retry or rerun control is exposed. Rechecks are the agent's `retrigger_ci`.
- **Filtering:** searching `1015-c` narrowed the list to that run. The Filter menu opened.
- **States (stubbed run):** one run with five jobs rendered:
  - failure, auto-expanded, with `exit 1` and its 3 KB log wrapping without page overflow;
  - errored;
  - success with **cache hit**;
  - skipped;
  - pending.
- **From a task link:** the demo seeds no task with a CI verdict, so this was not reachable, as on 10-13. `App.ci.test.jsx` and `GrainCIStatus.test.jsx` cover it.

### System

- **Logs:**
  - Opens on the tail (scrollTop 9334 of 9334, 300 lines).
  - `dispatch` narrows to 50 lines. `(unclosed[regex` is matched literally and shows "(no matching log lines)".
  - **Refresh** sends one request, and the pane polls on its own while open.
  - A failed read shows the 10-13 message.
- **Top:**
  - The process table (systemd, grain-daemon, node) with Auto-refresh and Refresh.
  - A failed read found bug 1.
- **Root shell:** no tab, by design. It was removed in grain/task-193, and `SystemOverlay.test.jsx` asserts that it is absent.
- **Pipeline:**
  - **Pause pipeline** with reason "walk 10-15 pause", then dismissing the confirm, changed nothing.
  - Accepting it paused the pipeline. After a reload the pane read "paused since … Reason: walk 10-15 pause".
  - `/t/default/tasks`, `/tasks` and `/t/default/ci` all showed the global banner with the reason and **resume now**.
  - **Resume pipeline** → accept resumed it.
- **Power (stubbed POSTs):**
  - **Dismissed:** each of Restart grain, Reboot host and Shut down host raises a native confirm naming its cost. Dismissing it sent nothing.
  - **Accepted:**
    - Restart: 200, then "requested".
    - Reboot: connection reset, then "requested".
    - Shut down: 500, shown in the snackbar.
  - Exactly three POSTs were sent, and the host was never touched.

### Metrics

- **Sections:** Metrics, Release notes, Throughput, Latency, Capacity and Backlog, with a "Last 7 days" window control and Refresh.
- **Windows:**
  - `?window=24h`, `30d` and `90d` ask `/api/metrics?window=<w>&buckets=24`.
  - `bogus` falls back to `7d`, and the URL is cleaned to `/metrics`.
- **Failure states (stubbed):**
  - A partial failure shows "Not available in this report: throughput (database timeout; ref 7f3a)."
  - A 500 shows "… could not be loaded (metrics database unavailable). Refresh to try again."
  - A 4 s delay shows a progress indicator.
- **Layout:** no horizontal overflow.

## Observations (not changed)

- **General with a failed settings read:** with `/api/settings` failing, General & Deployment renders nothing below the tabs. See bug 1.
- **CI cache size error:** a 20-digit value surfaces Go's raw JSON unmarshal error rather than a sentence.
- **Shut down host failure:** shown only in the snackbar, which fades. Restart and reboot leave their "requested" line in place.
- **CI runs list after a cancel:** lags by up to one poll, unchanged since 10-13.
- **Expected demo errors:**
  - `GET /api/relay-settings` answers 501.
  - `GET /api/repos/acme/widgets/github-branches` answers 404, because the demo has no GitHub.
  - `GET /api/upgrade/targets` and `/api/upgrade/release-notes` answer 404, because the demo has no upgrader.
  - `GET /api/tasks` is aborted on navigation away.

## Stubs (browser-level `page.route`; the real server and UI otherwise)

| Route | Method | Why | Response |
|---|---|---|---|
| `/api/host/{restart,reboot,shutdown}` | POST | Never restart, reboot or power off the host | restart: 200 `{ok:true}`; reboot: aborted `connectionreset`; shutdown: 500 `{error:"intercepted host shutdown failure"}` |
| `/api/settings` | GET | The demo never has a pending restart, an unrunnable value or host stats for a commitment | Real response plus `pendingRestart`, `unrunnable`, and separately an over-committed `sandboxCommitment` |
| `/api/upgrade`, `/api/upgrade/scheduled` | GET/POST/DELETE | The demo has no upgrader, and nothing may be upgraded | Idle image-mode status with `inFlightRuns: 2`. POST answers 400 for `;`/`..`. An immediate POST goes to running, then failed. A scheduled POST adds `scheduled`, and DELETE removes it |
| `/api/capabilities/gemini-key/check` | POST | Secret-validation boundary (the real demo answer was also exercised) | 200 `ok:true`, then 502 |
| `/api/sandboxes` | GET | Pressure, a 60-row pool and failure states the demo cannot produce | Real response with heavy host figures and 60 synthetic rows; also `hostError`, `{enabled:false}` and 500 |
| `/api/ci/runs/fa11fa11fa11fa11` | GET | Failure, errored, skipped and pending conclusions, which the demo's one workflow cannot produce | A synthetic five-job run |
| `/api/metrics` | GET | Partial, total-failure and slow reports | Real response plus `failed:[…]`; a 500; a 4 s delay |
| `/api/{package-cache,actions-cache,teams,host/top,pipeline-pause,state-repo,settings,executors,ci/runs,metrics,logs,secrets}` | GET | First-read failure probe (bug 1) | 500 `{error:"intercepted failure"}` |

GitHub needed no stub: no GitHub-backed action was taken. CI execution was the demo's real `pkg/ci.Service` with host-directory sandboxes, paced only by its own `/api/e2e/ci/finish`.

## Tests run

- `npx vitest run` over `TopPage`, `CachesPage`, `StateRepoPanel`, `TeamsPanel` and `CIPage`: 108 tests, all passed.
  - The 4 new unit tests each fail with the component changes stashed.
- `npx playwright test --project=chromium e2e/deployment-operations-failed-reads.spec.js`: 5 passed.
