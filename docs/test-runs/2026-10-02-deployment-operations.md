# Deployment Settings and Operations Walkthrough

**Date:** 2026-10-02
**Branch:** `grain/task-1534`
**Commit tested:** `cf9d6156c` (`Merge pull request #2609 from bwsalmon/grain/task-1553`), then the two fixes below on top of it (`94afdc85a`), re-checked against a rebuilt server
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1, Chromium 151 (headless shell, `chromium-1234`)
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`, built with `go build ./cmd/grain` after `npm ci && npm run build`. It ran over the throwaway embedded SQLite store the demo creates under `TMPDIR` (a scratch directory), and was rebuilt and restarted, with a fresh store, after the fixes.
**Model and tooling:** no `/home/debian/.gemini-api-key` was present in this sandbox, so nothing here called Gemini. The walkthrough uses no model.

## How it was driven

The walkthrough was a set of Node scripts using Playwright's `chromium` API, kept out of the tree. They drove the real frontend through visible controls: tabs, text fields with blur-to-save, native and MUI selects, radio groups, buttons, the keyboard (Tab, Home/End, Enter, Escape), and native `confirm()` dialogs, which were answered on purpose (dismissed, then accepted) rather than left to Playwright's default.

Each script recorded console errors and warnings, page errors, failed requests, and every 4xx/5xx response. Each mutation was confirmed through `GET /api/settings` or `GET /api/config`, then by reloading or reopening the screen.

The sign-in was real. The demo enables password auth, so the account `operator` (one of the demo's seeded system admins) was created through the "new user" tab of the sign-in dialog, and the browser state from that sign-in was reused by later scripts.

This run started from the 2026-10-01 report (`2026-10-01-deployment-operations.md`) and spent most of its time on what that run had not reached or had left as notes: leaving a pane by the keyboard, Power's success path, a CI run opened from a task, the sandbox commitment and stale/host-error states, and the Gemini connection editor.

Setup notes, unchanged from the earlier run:

- `npx playwright install chromium` needs `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the package-cache proxy.
- `requestfailed … net::ERR_ABORTED` on `/api/tasks` polls is the deliberate `304 Not Modified` ETag path, not a fault.

## Bugs found and fixed

| # | Bug | Reproduction (before the fix) | Fix | Regression coverage |
|---|-----|------------------------------|-----|---------------------|
| 1 | Escape threw away a typed setting. | `/admin` → click Environment name → type `esc-typed-env` → press Escape. The pane closes to `/`, and `GET /api/settings` still has `environmentName: ""`; reopening shows the old value. The same text dismissed with "← Back" **is** saved (`back-typed-env` was stored), because the button's click blurs the field first, and settings save on blur. An unmounted field fires no blur, so Escape never saved. | `Overlay` blurs a focused text field before an Escape close, so Escape commits the field the way every other way out of a pane does. An invalid value still fails validation: `bogus` in Poll interval + Escape → `PUT` 400 `pollInterval: time: invalid duration "bogus"` shown in the banner, store unchanged. | `Overlay.test.jsx` (pane and centered dialog: blur happens before close; both fail without the fix) and e2e `deployment-operations-escape-power.spec.js`. |
| 2 | A confirmed Restart/Reboot/Shut down gave no sign it had been accepted. | Power (stubbed, see below) → Restart grain → accept: one `POST /api/host/restart` → 200, and the screen is identical to after a dismissed confirm. A reboot whose connection drops is the same. (Noted, not changed, by the 2026-10-01 run.) | The Power tab shows "Restart requested. …", "Reboot requested. …" or "Shutdown requested. …" once the host answers 2xx or the connection drops, and clears it when the next action is confirmed. A refusal (500) still goes to the error banner and acknowledges nothing. | `AdminOverlay.test.jsx` "power actions" (4 tests; the two acknowledgement tests fail without the fix) and e2e `deployment-operations-escape-power.spec.js`. |

The e2e spec stubs only `/api/config` (to turn the three power switches on) and the three `/api/host/*` POSTs; the Escape test uses no stub.

After the rebuild, both were re-driven in the browser:

- Escape from General saved `esc-typed-env`; the tab title became `esc-typed-env — grain` and reopening `/admin` showed it.
- Power: dismissing each confirm sent nothing. Accepting sent exactly one POST each. Restart showed "Restart requested", reboot (connection reset) replaced it with "Reboot requested", and shutdown (500) showed only `shutdown refused (stub)`.

## Coverage and outcomes

### Settings

- **Tab traversal:** all 16 Admin tabs (General & Deployment, Access & Accounts, Engine & Concurrency, Sandbox & Infrastructure, Executors, Integrations & Auth, All Teams, All Users, Config repo, Logs, Sandboxes, Caches, Top, Pipeline, Power, Upgrade) rendered with no console error, and each wrote its own `/admin/<tab>` (General keeps `/admin`). Team settings has 8 tabs (Team, Members, Capacity, Git identity, Connections, Models, Executors, Workflow & Tasks).
- **Horizontal scrolling and keyboard:** at 700 px the tab strip scrolls (scrollWidth 1831 vs clientWidth 355). From the selected tab, End moves focus to Upgrade and scrolls the strip to it (scrollLeft 1476); Enter opens `/admin/upgrade`.
- **Appearance and timeline order (browser-local):** Dark + Oldest first wrote `grain.themeMode=dark` and `grain.timelineOrder=oldest`, the body turned `rgb(15, 16, 19)`, and both radios were still checked after a reload. Choosing Auto and Newest first removed both keys.
- **General settings and dirty state:** autosave on blur. Typing then switching tab saved (`tab-typed-env`); typing then "← Back" saved; typing then Escape lost the value until bug 1 was fixed. Typing and then reloading discards the value (no blur happens). That is the browser's own behaviour, and no `beforeunload` guard exists.
- **Validation:** Poll interval `bogus` → 400 shown in the banner, the field reverted, store unchanged.
- **Agent and model selection (Gemini):** Team settings → Connections → edit "Gemini 2.5 Pro" (Antigravity). The demo cannot read agy's catalogue, so the "Gemini model" field is free text with "this deployment cannot read agy's model catalog -- type a model name". Effort offers model default, low, medium and high.
  - Typing `gemini-2.5-flash` and tabbing away does **not** save on its own: this editor keeps a draft with an explicit "Save model" button.
  - Picking effort `high` commits the whole draft. `agentBackends` then read `gemini-2.5-flash` / `high`.
  - Typing a further model name and pressing Escape (or "← Connections") leaves the draft unsaved without a prompt. It is the same for Back and Escape, so it is noted rather than changed (see notes).
- **Pipeline (real demo state, not the host):**
  - Pause with reason `walkthrough 2026-10-02`: dismissing the confirm left `pipelinePause` null. Accepting stored `{paused:true, reason, byKind:"human", byId:"operator"}`.
  - After a reload, the tab and the global banner both say paused, with the reason.
  - Resume: dismissing left it paused, and accepting cleared it.
- **Sandbox commitment notice (stubbed settings):** a `warning` commitment renders "6 workers + 2 mergers + 1 reserved Director + 4 CI jobs × 4 vCPU = 52 vCPU committed against the 16 this host has (3.3×), held against a 32 vCPU sandbox budget … 104.0 GiB of memory, of the 64.0 GiB it has (163%) …".
- **Config repo, Top, Power and Upgrade without stubs:** each shows its own "not available" notice: no config repository, no process snapshot, power not enabled, and neither `-upgrade-image` nor `-upgrade-src-dir`.
- **Root shell:** there is still no Root shell entry in this build's Admin tabs, System links or sidebar, so there was no availability message to check.
- **Not repeated from 2026-10-01:** GitHub host, the restart-required and unrunnable notices, capability secrets and credential checks, and upgrade planning were all exercised in depth yesterday on code that has not changed since. The related e2e specs (`deployment_operations_walkthrough`, `upgrade`, `team-settings-github-tokens-walkthrough`, `provider-connection-lifecycle-walkthrough`) were re-run green on this branch instead.

### Sandboxes (Admin → Sandboxes)

- **Real demo data:** the host line (load 0.45/0.55/0.60 of 4 vCPU, 15%, 4096/16384 MB), the CPU and memory trends, and the storage, proxy-cache, CI-history and job-cache figures render. "Nothing in flight, so no sandboxes exist" shows when idle.
- **Stale reading and host error (stub):** `sandboxReading.stale` with `ageMS: 95000` and a `hostError` render "Host stats unavailable: read /proc/loadavg: permission denied (stub)" and "As of 95s ago: the probe running now has not come back yet…".
- **500 on refresh:** the error is shown beside the last data, which stays up.
- **`{enabled:false}`:** shows "Not available: this deployment has no sandbox pool or host stats configured." No page-level horizontal overflow at 1024 px.
- **Long tables:** host pressure, disks under their floor, the failed row and the 40-row table were covered by the 2026-10-01 run and by `deployment_operations_walkthrough` test 6, which passed again here.

### CI

- **Start a run:** started from the visible form against `acme/widgets` `grain/task-3`, it opened its overlay at `/ci/<id>` showing running, `workflow_dispatch`, "cancel run", the job `observe`, its sandbox, and step "Wait for the browser" with "(waiting for output...)".
- **List:** back on `/ci`, the run shows in "active sandboxes (1)" and in the runs list ("1 jobs: 1 running"). Rows open through the repo link, which is a real button and so reachable by Tab/Enter; the row itself is not focusable and needs no focus of its own.
- **Finish and deep link:** after the demo's own `POST /api/e2e/ci/finish`, the deep link `/ci/<id>` loads success, "7s", and completed timestamps.
- **Unknown run:** `/ci/doesnotexist123` says `no CI run "doesnotexist123" -- a run is remembered for a while and then forgotten` (one intended 404).
- **From a task link:** the task's grain CI verdict comes from the orchestrator's observation of the branch, which the demo never makes, so `GET /api/tasks` and `/api/tasks/3` were given a `ci` block naming the real run (see stubs).
  - The task pane then reads "grain CI: failure against 0123456789ab. observe failed. This is grain's own CI…", with "show the run".
  - Clicking it opens the real run's overlay over the task while the URL stays `/tasks/3`. That is deliberate (`App.jsx` `openCIRun`: a run opened from a task or repo is transient), so it is not a bug.
- **Controls:** cancel is still the only run control; there is no re-run or retry. Errored/skipped/cancelled states, cache hits and cancel confirmation were covered by the 2026-10-01 run and by `deployment_operations_walkthrough` test 7 and `observability.spec.js`, which passed again.

### System

- **Logs:**
  - The tail loads 300 lines.
  - Refresh fetches once, and the tab polls every 5 s on its own. There is no auto-refresh toggle in this build; the 2026-10-01 report's "unticking it" refers to Top.
  - Search is a plain case-insensitive substring: `[4242]` matched all 300 lines, and `(`, `[a-` and `*` showed "(no matching log lines)" because no line contains them. They are not treated as regular expressions and nothing crashed.
- **Top, Root shell, Pipeline, Power:** as above.

### Metrics

- **Sections:** Release notes (week 39 numbers and "Nothing has been written up for this week yet"), Throughput (11 filed, 3 completed, rates per day), Latency, Capacity and Backlog all render.
- **Windows:** last 24 hours, 7, 30 and 90 days each re-query and re-span ("1d 0h", "30d 0h", "90d 0h"). The week picker offers the one week the demo has.
- **Window persistence:** the window is still not in the URL, so a reload returns to "Last 7 days". A follow-up task was proposed for this (see below).
- **Narrow layout:** at 390 px there is no document overflow.
- **Failure states:** loading and failure (a slow 503) were covered by the 2026-10-01 run and by `deployment_operations_walkthrough` test 9.

### Console and network

Over the whole walkthrough, the only console errors were intended ones:

- the stubbed 500s (`/api/host/shutdown`, `/api/sandboxes`);
- the reboot stub's connection reset;
- the validation 400 from `PUT /api/settings`;
- the 404 for the unknown CI run;
- the demo-only `GET /api/repos/acme/widgets/github-branches` 404 (the demo has no GitHub).

No page errors.

## Stubs (every intercepted route)

All stubs were Playwright `page.route` interceptions inside the browser. The server, frontend, store and gestures stayed real. Stubs that begin with `route.fetch()` modify the real server response rather than replacing it.

| Route | Why | What it returned |
|---|---|---|
| `GET /api/config` | Power is not enabled in the demo. | Real response plus `restartEnabled`, `rebootEnabled` and `shutdownEnabled` set to true. |
| `POST /api/host/restart`, `/reboot`, `/shutdown` | These must never touch the host. | 200 `{ok:true}`; a connection reset; 500 `{error:"shutdown refused (stub)"}`. |
| `GET /api/tasks`, `GET /api/tasks/3` | The demo's orchestrator never observes a branch, so no task carries a grain CI verdict. | Real response, with task 3 given `ci: {runId: <the real run started above>, commit, conclusion:"failure", detail}`. The `If-None-Match` request header was dropped so the real server answered 200 rather than 304. |
| `GET /api/settings` | The demo has no sandbox host to compute a commitment for. | Real response plus a `warning`-level `sandboxCommitment`. |
| `GET /api/sandboxes` | The demo host cannot go stale or fail. | Real response with `sandboxReading.stale` and `hostError`, then a 500, then `{enabled:false}`. |

The CI execution boundary was not stubbed: the run was a real demo run, finished by the demo's own `/api/e2e/ci/finish`. No GitHub, upgrade or secret-validation boundary was needed this time; those were covered with stubs on 2026-10-01.

## Tests run

- **Vitest:** the full suite (`npx vitest run`): 158 files, 2749 tests, all passed. The new tests are in `Overlay.test.jsx` (2) and `AdminOverlay.test.jsx` (4); each new acknowledgement and blur test fails with its fix stashed.
- **Playwright (Chromium):**
  - The new spec `e2e/deployment-operations-escape-power.spec.js` plus `e2e/deployment-operations-navigation.spec.js`: 5/5 passed.
  - Related specs: `deployment_operations_walkthrough`, `system_workflows`, `system-overlay-tabs`, `team-settings-github-tokens-walkthrough`, `provider-connection-lifecycle-walkthrough`, `upgrade` and `observability`: 18/18 passed.
  - Specs that press Escape on overlays (bug 1 changes what Escape does): `access-accounts-iap-walkthrough`, `fields`, `team-scoped-repos-autosave-walkthrough`, `teams-walkthrough`, `repos_workflows`, `task_detail_workflows`, `suites_workflows`, `new-user-workflow-walkthrough`, `user-auth-walkthrough`, `track6-navigation-merged-dissolve` and `repo-ci-capabilities`. 25/25 passed. Full grain CI covers the rest.

## Left as notes, not changed

- **Metrics window:** still not in the URL. Proposed as a follow-up.
- **Provider connection editor:** unsaved model text is dropped without a prompt on Back or Escape, while "Save model" shows the draft is dirty. It is consistent between the two gestures, and an unsaved-changes guard is a design choice for that pane rather than a defect.
- **Team settings tab:** not in the URL (carried over from 2026-10-01).
- **Sandbox staleness wording:** the line says "the sandbox storage above" even when the Host section above it is showing only an error.
- **Unused component:** `SettingsPlanPreview.jsx` is still imported nowhere.
