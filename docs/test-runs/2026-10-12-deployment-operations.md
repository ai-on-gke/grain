# Deployment Settings and Operations Walkthrough (2026-10-12)

**Date:** 2026-10-12
**Branch:** `grain/eu/task-3214` (task 3214), based on `candidate`
**Commit tested:** `6babee9a2` (`Merge pull request #3533 from bwsalmon/grain/eu/task-3258`), the tip of `candidate`; the two fixes below were then re-verified on the branch.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with headless Chromium (`chromium-headless-shell-1234`).
**Server under test:** `grain demo -addr 127.0.0.1:18420 -open=false -observability`, built with `go build -tags grain_e2e` after `npm ci && npm run build` into `pkg/ui/static`, over the throwaway embedded SQLite store `grain demo` creates under `TMPDIR`. One server stayed up for the whole walkthrough (it was restarted twice to pick up the rebuilt frontend), so mutations made in one step were visible to the next.
**Model and tooling:** this sandbox carried no `/home/debian/.gemini-api-key`, so no Gemini API calls were made. Gemini model and reasoning-effort selection was exercised through the Team settings Models tab only.

## How it was driven

Ad-hoc Playwright scripts (`ui/.walk/*.mjs`, excluded through `.git/info/exclude` and not committed) launched headless Chromium against the demo server. Like the e2e fixtures, every request carried `X-Grain-Admin-Mode: on`, and each page had `localStorage["grain.username"]` seeded. Without that seed a demo browser opens on the Create account dialog, because the demo names loopback callers but leaves `authenticated` false. Interaction went through visible controls: tabs, buttons, MUI selects and comboboxes, text and number boxes saved on blur, `confirm()`/`prompt()` dialogs (both dismissed and accepted), mouse-wheel scrolling, and keyboard Tab/Arrow/Escape. Each script also recorded:

- browser console errors and page errors;
- every HTTP response of 400 or above;
- every failed request.

Mutations were confirmed by reloading, by deep links, and by reading the corresponding API. Screenshots and traces were not kept.

Baseline: the existing scripted specs passed on the base commit before any change. That was 51 tests in 1.1 min on Chromium: `deployment_operations_walkthrough`, `deployment-operations-*`, `deployment-settings-and-operations-inputs-walkthrough`, `observability`, `system_workflows`, `ci_workflows`, `system-overlay-tabs`, `upgrade` and `ui-navigation-split-button-upgrade`.

## Bugs found and fixed

### 1. System → Logs: clearing a search threw a tail reader to the oldest line, then back

**Reproduction (deterministic):**

1. Open `/admin/logs`. The pane opens at the newest line (`scrollTop == scrollHeight - clientHeight`, 9334 px here).
2. Type `no-such-log-line` into Search logs. The pane shows `(no matching log lines)`.
3. Clear the box.

**Observed:** `scrollTop` was 0, so the reader was looking at the oldest of 500 lines. Up to 5 s later the next poll threw them back to the bottom. Narrowing the text had clamped the scroll to the top while LogsPage still thought the reader was pinned to the tail, and only a new `lines` array re-applied the pin.

**Expected:** a reader following the tail stays on the tail through a search. A reader who scrolled up before searching keeps their place when the search is cleared, as they did before.

**Fix:** `ui/src/components/LogsPage.jsx`. The scroll-to-tail effect now also runs when the query changes. The page also records whether the reader was pinned when a search began (`pinnedBeforeSearch`) and restores that when the box is cleared, so the scroll caused by the text shrinking cannot decide it for them.

**Regression:** `ui/e2e/deployment-operations-logs-tail.spec.js`, two tests in a real browser, because jsdom has no layout or scroll clamping:

- clearing a search returns a tail reader to the newest line at once;
- a reader who wheel-scrolled up, searched and cleared stays scrolled up across a poll.

The first test fails without the fix (`Expected: 9442, Received: 0`), and both pass with it on Chromium.

### 2. CI run heading read "asked by 2" for a task's run

**Reproduction:** open task 2 (it carries a CI verdict) and press **Show the run**. The overlay heading reads `… · asked by 2 · …`. Task-started runs record the bare task id as `requestedBy`; see `pkg/orchestrator/ci.go`, `recheck.go`, `stallretry.go` and `pkg/ui/ci.go`. On a deployment that becomes "asked by 3214", a number with nothing to say what it counts. Every other requester is either a principal id or a `kind:detail` name grain gives itself (`merge-batch:…`, `release:…`, `post-merge:…`).

**Fix:** `requesterLabel` in `ui/src/components/CIPage.jsx` renders a bare numeric requester as `task N`. Other requesters are unchanged, and nothing stored changes.

**Regression:** unit test `names a task that asked for a run as a task` in `ui/src/components/CIPage.test.jsx`. `CIPage.test.jsx` and `LogsPage.test.jsx` pass: 62 tests.

## Coverage and outcomes

### Settings

- **Tabs and horizontal scrolling:** all 16 System tabs were opened by click, each landing on its own `/admin/<tab>` path:
  - General & Deployment, Access & Accounts, Engine & Concurrency, Sandbox & Infrastructure, Executors, Integrations & Auth;
  - All Teams, All Users, Config repo;
  - Logs, Sandboxes, Caches, Top, Pipeline, Power, Upgrade.

  The selected tab was scrolled into view every time. The tab strip scrolls (scroll width 1831 px against client widths of 911 px at 1280 and 255 px at 600). ArrowRight ×16 wraps back to the first tab. The pages showed no document-level horizontal overflow at 1280 or 375 px. No Root shell tab exists, so there is no root-shell availability messaging to check, consistent with the earlier walkthroughs.
- **Browser-local preferences (`/settings`):** Dark and Oldest first were selected. They persisted across a reload (`grain.themeMode=dark`, `grain.timelineOrder=oldest`, body background `rgb(15, 16, 19)`) and were then set back to Light and Newest first.
- **General:** Environment name `walk-ops-2026-10-12` saved on blur (`PUT /api/settings` 200) and survived a reload. A 300-character name is cut to the input's 32-character maximum and saved. An empty name saved and survived a reload.
- **Engine:** `abc` and `0s` poll intervals were refused with 400 and rolled back to the stored value. The server's reason (`pollInterval: time: invalid duration "abc"`) appeared in a visible alert. `45s` persisted across a reload. Max merges `-3` was refused (`must be at least 1`), and `2` persisted.
- **Sandbox:** vCPUs `0` sends nothing (0 means unset, matching what was stored), and `4` persisted. Memory `12` was refused (`must be 0 (unset) or at least 128`), and `8192` persisted. CPU overcommit `-1` was refused. Disk `1e9` was read as 1000000000 GiB and saved with no warning; see Observations.
- **GitHub configuration:** GitHub host `not a host/../x` was refused with the server's explanation, and `ghe.example.com` persisted.
- **Restart-required and unrunnable notices (stubbed, see below):**
  - "Saved, but not applied yet: GitHub host, Sandbox backend" and "Stored, but not runnable: Poll interval (stored 0s, running 30s)" appear on the General, Engine and Integrations tabs.
  - Poll interval carries a "not runnable" chip, and GitHub host carries "restart to apply".
  - Neither notice appears on the diagnostic tabs (Logs, Power).
- **Agent and model selection (Gemini):** on `/teams/default/settings/models`, the gemini-2.5-flash reasoning effort was set to `high` (effort options: model default/low/medium/high). Typing `flash` in the default-model box offered only `gemini-2.5-flash`, which was picked. The UI sent `PUT /api/teams/default {"modelSettings":[{"model":"gemini-2.5-flash","effort":"high"}]}` and `{"defaultModel":"gemini-2.5-flash"}`, and both survived a reload.
- **Capabilities and secrets:**
  - The Integrations tab lists a sub-tab per capability.
  - A fake secret `walk-ops-secret/token` (value `fake-walkthrough-value-not-a-credential-7731`, never a real credential) was entered in a `type=password` box and saved with `PUT /api/secrets/walk-ops-secret/token`.
  - After a reload it is listed by key only, and the value appears nowhere in the DOM.
  - Deleting it asks first: dismissing kept it; accepting sent `DELETE` and it was gone after a reload.
- **State repository:** the Config repo tab says "This UI is not running inside a daemon that owns a config repository, so there is nothing to configure here." The demo has no state repository, and this was left as the real answer rather than stubbed.
- **Memory:** the Models tab's memory editor for acme/widgets enables Revert and Save only once edited. An unsaved edit raises the browser's leave-page prompt: dismissing stayed on the page, accepting left it. A saved edit survived a reload and was then saved back to its original text, also confirmed after a reload.
- **Upgrade planning (stubbed `GET/POST /api/upgrade`):**
  - The idle image-mode panel renders.
  - Branches `../evil` and `main; rm -rf /` were refused, and the stub's 400 reason was shown.
  - A `running` upgrade is followed by the panel's 3 s poll to a `failed` phase with its detail, which survives a reload.
  - Release notes and targets are the demo's real "upgrading is not available" answers.
  - The scheduled upgrade and its cancel are covered by `upgrade.spec.js`, which passed.

### Sandboxes

- **Real state:** one live agent sandbox (`POST /api/e2e/sandboxes/other`) was shown with host load, CPU and memory, the storage breakdown (1.2 GiB written by 1 live sandbox), the proxy cache, and CI history.
- **Stubbed pressure:** 60 sandboxes with names up to ~110 characters, mixed ready/not-ready, OOM-killed and stats-error rows. The host was set to load 15.20 / 12.10 / 9.00 on 4 vCPU, 99 % CPU, 15800/16384 MB memory and 3900/4096 MB swap.
  - All 60 rows render.
  - The table scrolls horizontally inside its own container: 1846 px wide in a 704 px container at 1280 px, and 1468 px in 342 px at 375 px, with no page overflow.
  - The last row scrolls into view.
- **Error states:** a 500 shows "failed to sample sandboxes". `enabled:false` shows "Not available: this deployment has no sandbox pool or host stats configured."

### CI

- **Start a run from the form:**
  - **Start a run** → repo select `acme/widgets` → Branch `walk/ci-1` → **Load workflows** → Observability → **Start run**.
  - The run opens at `/t/default/ci/<id>` and its sandbox appears under Active sandboxes (1).
  - The step shows `(waiting for output...)` until `POST /api/e2e/ci/finish` releases it, after which the run is **success** in 34 s.
  - Reloading the run's URL reopens it.
- **Open from a row:** pressing Enter on the row's repo button opens the run with its job and step.
- **Deep links:** `/ci/0000000000000000` shows "no CI run … a run is remembered for a while and then forgotten" (404). `/ci/%2e%2e` falls back to the task list.
- **States seen for real:**
  - **cache:** a second run on the same branch is a **cache hit** (`1 jobs, all green · 1 cache hit`, 0 s);
  - **pending/running:** the held run, shown as running with 0 s queued;
  - **success:** the finished held run;
  - **cancelled:** the cancel walkthrough's run;
  - **errored:** task 2's push run ("no workflow of acme/widgets answers push…").
- **Not reproduced:** failure and skipped need a workflow that fails, which the demo's single workflow cannot produce. Both are covered by `CIPage.test.jsx`.
- **Cancel:** **Cancel run** prompts. Dismissing left the run running, and accepting cancelled it (`1 jobs: 1 cancelled`).
- **Retry:** no retry control is exposed in the run pane; rechecks are the agent's `retrigger_ci`.
- **Run filtering:** Filter → Filter by… → conclusion → `cancelled` narrows the list to one run, and the narrowing survives a reload. It is kept per browser, not in the URL: a typed `/ci?f=…` is ignored by design. Searching `zzznomatch` shows "No CI run matches. 3 runs are here…" with a Clear.
- **From a task link:** task 2 → **Show the run** opens the run overlay without changing the URL (deliberately transient). Escape closes it and returns focus to **Show the run**. This step found bug 2 above.
- **Sidebar count:** the sidebar's `ci` count reads 0 idle, 1 while a run holds a sandbox, and 0 after it finishes.

### System

- **Logs:**
  - The tail opens at the newest line, and Search logs filters case-insensitively (`merge-queue` → 50 of 300 lines; `MERGE-QUEUE` → 50).
  - A query with no match shows `(no matching log lines)`.
  - Refresh does not move a reader who scrolled up, and a wheel-scroll-up survives the 5 s poll.
  - Source lists `daemon`.
  - Clearing a search found bug 1 above.
- **Top:** the process table (systemd, grain-daemon, node) with its auto-refresh toggle and Refresh button.
- **Pipeline:** the reason box, then **Pause pipeline** → confirm.
  - Dismissing left the pipeline running (`paused=false`).
  - Accepting paused it. The pane and the global banner on `/tasks` both say "paused by hand", and that survived a reload.
  - **Resume pipeline** → confirm → `paused=false`.
- **Power (stubbed POSTs):** each button was clicked twice.
  - **Dismissed:** no request was sent for any of the three.
  - **Restart grain** accepted → 200 → "Restart requested…".
  - **Reboot host** accepted → connection reset → "Reboot requested…" (a reset is what a real reboot looks like).
  - **Shut down host** accepted → 500 → the error appears once, in the global snackbar.
  - **Keyboard:** Tab from the Power tab reaches Restart → Reboot → Shut down → Back, and focus stays inside the pane. Escape closes it.
  - **Host safety:** the host was never touched.
- **Sandbox health** is under Sandboxes above.

### Metrics

- **Sections:** Release notes (Weekly, Versions), Throughput, Latency, Capacity and Backlog, each with its own heading.
- **Windows:** `?window=24h`, `30d` and `90d` ask `/api/metrics?window=<w>&buckets=24`. `bogus` and `-5d` fall back to the default seven days with the URL cleaned to `/metrics`.
- **Partial failure (stubbed):** `failed:[{section:"throughput",error:"database timeout; ref 7f3a"}]` gives "Not available in this report: throughput (database timeout; ref 7f3a)."
- **Full failure (stubbed):** a 500 gives "The report could not be loaded (metrics database unavailable). Refresh to try again." plus the global error.
- **Loading (stubbed, 4 s delay):** shows a progress indicator.
- **Long values (stubbed):** 9876543210 filed, 1234567.9/day and 123456789012 runs render with no horizontal overflow.

## Observations (not changed)

- **Sandbox disk size:** `Sandbox disk (GiB)` accepts any positive size (`1e9` GiB saved) with no notice. The disk is thin-provisioned ("grows as it is written"), so this is not obviously unrunnable. Whether an upper bound or a warning belongs here is a product call, so it was left alone.
- **Aborted polls:** Chromium reports each conditional `/api/tasks` poll that the server answers `304 Not Modified` as `net::ERR_ABORTED`. `api.js` deliberately sends `cache: "no-store"` with its own `If-None-Match`, and the app reads the 304 correctly. This is harness noise, not a failure.
- **Expected demo errors:** `GET /api/relay-settings` answers 501 on Access & Accounts (the demo has no relay). `GET /api/repos/acme/widgets/github-branches` answers 404 (no GitHub without `-github-sim`). Both are handled and logged as console resource errors only.
- **Formatting:** large metric counts render without thousands separators (`9876543210`).

## Stubs (browser-level `page.route`, real server and UI otherwise)

| Route | Method | Why | Response |
|---|---|---|---|
| `/api/host/{restart,reboot,shutdown}` | POST | Never restart, reboot or power off the sandbox host | restart: 200 `{ok:true}`; reboot: aborted `connectionreset`; shutdown: 500 `{error:"intercepted host shutdown failure"}` |
| `/api/settings` | GET | The demo is not a daemon, so it never has a pending restart or a replaced unrunnable value | Real response plus `pendingRestart:["githubHost","sandboxBackend"]`, `unrunnable:[{key:"pollInterval",stored:"0s",running:"30s"}]` |
| `/api/upgrade` | GET/POST | The demo has no upgrader, and nothing may be upgraded | Idle image-mode status; POST: 400 for `..` or shell characters, otherwise 202 `running`, after which GET answers `failed` with a health-check detail |
| `/api/sandboxes` | GET | Host pressure and a 60-row pool that the demo cannot produce | Real response with 60 synthetic rows and heavy host figures; also a 500 and `{enabled:false}` |
| `/api/metrics` | GET | Partial, total-failure, slow and very large reports | Real response plus `failed:[…]`; a 500; a 4 s delay; inflated counters |

GitHub and secret validation needed no stub: the walkthrough used no GitHub-backed or validating action beyond the GitHub host setting, which the server validates itself. CI execution was the demo's real `pkg/ci.Service` with host-directory sandboxes, paced only by its own `/api/e2e/ci/finish` control.

## Tests run

- Before any change, on Chromium: the deployment-operations e2e set listed above, 51 passed.
- `ui/e2e/deployment-operations-logs-tail.spec.js` on Chromium: 2 passed with the fix, 1 failed without it.
- `npx vitest run src/components/CIPage.test.jsx src/components/LogsPage.test.jsx`: 62 passed.
- After the fixes, on Chromium: the same deployment-operations e2e set plus the new spec gave 52 passed and 1 failed. The failure was `deployment-operations-navigation.spec.js` › "keeps the keyboard-focused tab in view on Team settings tabs at 700 px", which this change does not touch. The walkthrough's own demo server was still running on the 4-core sandbox during that run. Re-run alone with `--repeat-each=3`, the spec passed 54/54, so the failure is load-sensitive flakiness, not a regression.
