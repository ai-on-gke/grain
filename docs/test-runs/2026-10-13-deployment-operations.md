# Deployment Settings and Operations Walkthrough (2026-10-13)

**Date:** 2026-10-13
**Branch:** `grain/eu/task-3497` (task 3497), based on `candidate`
**Commit tested:** `c8f64c18e` (`Merge pull request #3679 from bwsalmon/grain/eu/task-3494`), the tip of `candidate`. The fixes below were then re-verified on the branch in the same browser against a rebuilt server.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with headless Chromium (`chromium-headless-shell-1234`).
**Server under test:** `grain demo -addr 127.0.0.1:18420 -open=false -observability`. It was built with `go build -tags grain_e2e` after `npm ci && npm run build` had written the frontend into `pkg/ui/static`. It ran over the throwaway embedded SQLite store that `grain demo` creates under `TMPDIR`. One server stayed up for the walkthrough, restarted once to pick up the rebuilt frontend, so each step saw what the previous steps had changed.
**Model and tooling:** this sandbox had no `/home/debian/.gemini-api-key`, so no Gemini API call was made. Gemini model and reasoning-effort selection was exercised through Team settings → Models & connections, where the demo's own fake catalog answers the connection test.
**Sandbox note:** `npx playwright install chromium` fails behind the package-cache proxy (`SELF_SIGNED_CERT_IN_CHAIN`) unless it is run with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.

## How it was driven

The walkthrough was driven by ad-hoc Playwright scripts in `ui/.walk/*.mjs`. These were excluded through `.git/info/exclude` and not committed. They launched headless Chromium against the demo server. Every request carried `X-Grain-Admin-Mode: on`, and `localStorage["grain.username"]` was seeded as `alice`, the demo's admin. Any other name is refused with "access denied … needs to request it".

Interaction went through visible controls:

- tabs, buttons, native and MUI selects, comboboxes, and text and number boxes saved on blur;
- `confirm()` and `prompt()` dialogs and MUI confirm dialogs, each both dismissed and accepted;
- keyboard Tab, Arrow and Escape.

Each script recorded browser console errors, page errors, every HTTP response of 400 or above, and every failed request. Mutations were confirmed by reloading, by deep links and by reading the matching API. No screenshots or traces were kept.

## Bugs found and fixed

### 1. System → Agents & Task Branches offered commit-identity boxes that never saved

**Reproduction (deterministic):**

1. Open `/admin/agents`.
2. Type `Walk Bot` into **Author name** and `walker@example.com` into **Author email**, then leave each box.
3. `PUT /api/settings {"agentGitName":"Walk Bot"}` answers **200**, and so does the email.
4. Reload. Both boxes are empty, and `GET /api/settings` carries neither field.

`not an email` was accepted with 200 as well.

**Cause:** the deployment-wide agent git identity was removed in `f386abe49` ("remove global target repos and git settings in favor of team ownership"). Each team now sets its own on Team settings → Git identity, and a team that sets none commits as grain's own (`mcp.DefaultGitIdentityName`/`Email`). The boxes outlived that change. They moved from a team's Models tab to this System tab in `a2a23a78a`. The JSON decoder ignores the unknown fields, so the save looked successful.

Team settings → Git identity had the same leftover: its "Leave empty for the deployment's own" helper and its placeholder read `settings.agentGitName`/`agentGitNameDefault`. The server reports neither field, so the placeholder was blank.

**Fix:**

- `AdminOverlay.jsx`: the Commit identity section is now a sentence. It says each team sets its identity in Team settings on the Git identity tab, and that a team setting none commits as `grain agent <grain-agent@localhost>`.
- `TeamSettingsOverlay.jsx` and `TeamGitPanel.jsx`: they name grain's own identity, taken from the new `ui/src/gitIdentity.js`, which mirrors the server's constants.

**Regression:**

- A unit test in `AdminOverlay.test.jsx` checks that no Author boxes are shown and that the pointer is.
- The existing Agents-tab test no longer asserts on a server field that does not exist.
- `ui/e2e/deployment-operations-identity-sandbox-errors.spec.js` checks the System tab and the Team Git identity placeholder in Chromium.

### 2. System → Sandboxes spun forever when the first sandbox-health read failed

**Reproduction (deterministic, stubbed):** stub `GET /api/sandboxes` to answer 500, then open `/admin/sandboxes`.

**Observed:**

- The "Loading sandbox health" spinner stays for as long as the pane is open, and there is no Refresh button.
- A new global snackbar appears on every 5 s poll.
- If a read fails after one has succeeded, the old figures stay on screen as though they were current.

**Fix:** `SandboxHealthPage.jsx` now keeps the latest failure.

- Before any read has succeeded, an error ("Sandbox health could not be read (…). This pane tries again every few seconds.") replaces the spinner.
- After a success, a warning says the figures come from the last read that succeeded.
- Refresh is offered in both cases.
- The snackbar fires once per run of failures, not once per poll.

**Regression:**

- Two unit tests in `SandboxHealthPage.test.jsx`. Both fail without the change.
- An e2e case that fails the read, sees the message, lets the next read through with **Refresh**, and sees the host figures return.

### 3. System → Logs, Upgrade and All Users lost a failed first read once the snackbar faded

**Reproduction (deterministic, stubbed):** stub `GET /api/logs`, `GET /api/upgrade` or `GET /api/users` to answer 500, open `/admin/logs`, `/admin/upgrade` or `/admin/users`, and wait about 6 s for the snackbar to go.

**Observed:**

- Logs and Upgrade show nothing below the tab strip.
- All Users shows "No user accounts found.", which reads as an empty deployment rather than as a failure.

**Fix:** each pane keeps the error and says it in place:

- `LogsPage.jsx`: "The logs could not be listed (…)".
- `UpgradePanel.jsx`: "This deployment's upgrade status could not be read (…)". This is shown only while the pane has no status at all, because a read that fails mid-upgrade is the restart the upgrade ends in.
- `UsersPanel.jsx`: "User accounts could not be loaded (…)".

**Regression:** one unit test per pane, each failing without its change, plus one e2e case per pane.

### Filed as a follow-up (not fixed here)

The same probe found three more panes with this fault:

- **Caches** (`/admin/caches`, failing `/api/package-cache`): its spinner never stops.
- **CI run list** (`/ci`, failing `/api/ci/runs`): its spinner never stops, and every poll raises a snackbar.
- **All Teams** (`/admin/teams`, failing `/api/teams`): shows only "Create team".

These are proposed as a follow-up task on `candidate` that depends on this one and carries the same reproduction and pattern.

## Coverage and outcomes

### Settings

- **Tabs and horizontal scrolling:** all 17 System tabs were opened by click, each on its own `/admin/<tab>` path. The tabs are:
  - General & Deployment and **Agents & Task Branches** (new since 10-12);
  - Access & Accounts, Engine & Concurrency, Sandbox & Infrastructure, Executors, Integrations & Auth;
  - All Teams, All Users, Config repo;
  - Logs, Sandboxes, Caches, Top, Pipeline, Power, Upgrade.

  At 1280 px the strip scrolls (scroll width 2009 px, client width 911 px), and the selected tab was inside the scroller's visible box every time. At 600 px no tab was left out of view either. ArrowRight ×17 wraps to the first tab, and no page overflowed horizontally. There is no Root shell tab, so there is no root-shell availability message to check, as in earlier walkthroughs. Bare `/system` lands on `/admin` (General).
- **Browser-local preferences (`/settings`):**
  - Selected Dark, Oldest first and time zone Asia/Tokyo. They were stored as `grain.themeMode=dark`, `grain.timelineOrder=oldest` and `grain.userTimeZone=Asia/Tokyo`, the body turned `rgb(15, 16, 19)`, and all three survived a reload.
  - Setting them back to Light, Newest first and Browser default cleared the order and zone keys.
  - Arrow keys move between the appearance radios.
- **General:** Environment name `walk-ops-2026-10-13` saved on blur and survived a reload. Clearing it saved too.
- **Agents & Task Branches:**
  - Max agent turns `-5` was refused by the server (`cannot be negative`) and the box rolled back. `2.5` was refused in the browser ("takes a whole number"), and `40` persisted.
  - Branch template `no-id-here` was refused (`must contain {id}`), as was `bad name/{id}..` (`produces invalid branch name "bad name/123.."`). `walk/{deployment}/t-{id}` persisted.
  - Deployment ID `UPPER_case` was refused with the server's rule, and `walk-dep` persisted.
  - The prompt extension persisted.
  - With the template cleared, the one-line suggestion appeared, and **Use it** saved `grain/{deployment}/task-{id}`, after which the suggestion went away.
  - Commit identity found bug 1.
- **Engine, Sandbox and GitHub validation:** covered field by field on 10-12 and unchanged since. This run re-checked the input inventory of each tab and the existing `deployment-settings-and-operations-inputs-walkthrough` spec, which passed.
- **Restart-required and unrunnable notices (stubbed):** "Stored, but not runnable: Poll interval (stored 0s, running 30s)" and "Saved, but not applied yet: GitHub host, Sandbox backend" appear on General, Agents, Engine and Integrations. They do not appear on Logs.
- **Agent and model selection (Gemini):** on `/t/default/settings/models`:
  - Typing `pro` in Default model offered only `gemini-2.5-pro`. Picking it sent `PUT /api/teams/default {"defaultModel":"gemini-2.5-pro"}`.
  - The gemini-2.5-flash effort options were Model default, low, medium and high. Choosing `high` sent `{"modelSettings":[{"model":"gemini-2.5-flash","effort":"high"}]}`.
  - Both survived a reload and matched `GET /api/teams/default`. The default was then set back to flash.
  - **Test connection** said "Connection valid" from the demo catalog. With `/api/agent-models` stubbed to 502, it showed the provider's error.
  - **Edit** opened the connection editor (agent type, authentication method, name, concurrency), and Escape closed it.
- **Capabilities and secrets:**
  - All 26 Integrations sub-tabs opened without a console error. They range from Deployment-wide, GCP key and Gemini key through to the Claude, OpenAI and Gemini proxies.
  - A dummy secret (`walk-dummy` / `token` / a non-credential string) was set with **Set secret**. The value box cleared at once, the secret name was listed, and the value never appeared in the DOM, before or after a reload. No real credential was stored.
- **State repository (stubbed, because the demo is not a daemon):**
  - Without the stub, the pane says it is "not running inside a daemon that owns a config repository".
  - With the stub, it shows the branch and head chips, working tree, last snapshot, and a settings-reset notice with its values and "and 3 more".
  - **Export now** moved the head chip to the new commit. **Dismiss** cleared the notice.
  - **Show files** listed the tree. A file read that failed showed its error.
  - **Back up secrets here** posted `{remote, branch, token:""}` and the pane then said "backed up to …".
- **Memory (new page):** `/memory` resolves to `/t/default/memory`. Editing a scope enabled only that scope's Revert and Save. Save sent `PUT /api/memory`, the edit survived a reload, and the original text was then restored.
- **Upgrade planning (stubbed upgrader):** stubbed with `inFlightRuns: 2`.
  - **Upgrade** opens a "Confirm upgrade" dialog that names the 2 runs it would discard. **Cancel** and Escape sent nothing, and Escape returned focus to Upgrade.
  - Accepting with branch `../evil;rm` got the stub's 400, which was shown.
  - `candidate` went to running, then failed with the stubbed health-check detail.
  - With "Wait until this deployment is idle" on, the button reads **Schedule upgrade**. The pane said "Waiting to upgrade to main, asked for by alice … 2 runs and 1 CI run still to finish", and **Cancel** removed the schedule.
  - PostgreSQL **Test Connection** showed the stub's error. **Migrate Database to PostgreSQL** posts at once; its guard is the server's confirmation-token step, which was stubbed to 409 here.
  - Release-notes previews 404 in the demo, which has no upgrader, and say so.

### Sandboxes

- **Real (demo):** host load average, utilization, memory and swap with trend sparklines, storage breakdown, proxy cache, CI job cache and CI history.
- **Stubbed pressure and long table:**
  - Load 31.5/28/20 of 4 vCPU, 98 % utilization, 16000/16384 MB memory and 4000/4096 MB swap all render.
  - A 60-row pool renders 61 table rows. The 9 failed probes show their error, and the last row scrolls into view.
  - The table scrolls inside its own box (scroll width 1826 px, client width 704 px), and the page does not overflow.
- **Error and empty states:**
  - `hostError` shows "Host stats unavailable: …".
  - `{enabled:false}` shows "Not available: this deployment has no sandbox pool or host stats configured."
  - A 500 found bug 2.

### CI

- **Start a run from the form:** **Start a run** → repo `acme/widgets` → Branch `walk/1013-a` → **Load workflows** → Observability → **Start run**.
  - The run opened at `/t/default/ci/<id>`, with its sandbox under **Active sandboxes (1)** and the sidebar's ci count at 1.
  - The step read `(waiting for output...)` until `/api/e2e/ci/finish` released it, after which the run was **success** in 6 s.
  - Reloading the run's URL reopened it with **Cancel run** shown.
- **Cache:** a second run on the same branch concluded at once as `1 jobs, all green · 1 cache hit`. The cache line became "1 entry · 696 B … 33% of 3 lookups hit".
- **Cancel:** dismissing **Cancel run**'s prompt left the run running. Accepting it cancelled the run (`1 jobs: 1 cancelled`).
  - For up to about 3 s after cancelling, the runs list row still read `1 jobs: 1 running`, until the next poll. This is cosmetic and was not changed.
- **Retry:** no retry control is exposed in the run pane. Rechecks are the agent's `retrigger_ci`.
- **Filtering:** the Search runs box and Filter menu were present. Filter persistence was covered on 10-12, and `ci_workflows.spec.js` passed.
- **Deep links:**
  - `/ci/<id>` rewrites to `/t/default/ci/<id>` and opens the run.
  - `/ci/0000000000000000` shows "no CI run … a run is remembered for a while and then forgotten" (404).
- **From a task link:** the demo now seeds no task with a CI verdict (`t.ci`), so **Show the run** was not reachable from a task. Its behaviour is covered by `App.ci.test.jsx` and `GrainCIStatus.test.jsx`.
- **Not reproduced:** the failure, errored and skipped conclusions, which the demo's single workflow cannot produce. They are covered by `CIPage.test.jsx`.

### System

- **Logs:**
  - Opens on the tail (scrollTop 9334 of 9334, 300 lines).
  - `dispatch` narrows to 50 lines. A regex-looking query `(unclosed[regex` is matched literally and shows `(no matching log lines)`.
  - Refresh and the source selector are present.
  - A failing `/api/logs` found bug 3.
- **Top:** the process table (systemd, grain-daemon, node) with its auto-refresh toggle and Refresh.
- **Pipeline:**
  - Typed the reason "walk 10-13 pause" and pressed **Pause pipeline**, then dismissed the confirm. Nothing changed.
  - Accepting the confirm paused the pipeline. After a reload the pane read "paused since … Reason: walk 10-13 pause", and `/tasks` showed the global banner with the reason.
  - **Resume pipeline** → accept resumed it.
- **Power (stubbed POSTs):** each button was clicked twice.
  - **Dismissed:** no request was sent for any of the three.
  - **Restart grain** accepted → 200 → "Restart requested…".
  - **Reboot host** accepted → connection reset → "Reboot requested…".
  - **Shut down host** accepted → 500 → the error appears.
  - Only three POSTs were sent in all, one per accepted confirm, and the host was never touched.

### Metrics

- **Sections:** Metrics, then Release notes (Weekly, Versions), Throughput, Latency, Capacity and Backlog.
- **Windows:**
  - `?window=24h`, `30d` and `90d` ask `/api/metrics?window=<w>&buckets=24`.
  - `bogus` falls back to `7d`, with the URL cleaned to `/metrics`.
- **Partial failure (stubbed):** "Not available in this report: throughput (database timeout; ref 7f3a)."
- **Full failure (stubbed 500):** "The report could not be loaded (metrics database unavailable). Refresh to try again."
- **Slow load (stubbed, 4 s):** shows a progress indicator.
- **Keyboard:** Tab cycles Back → Weekly → versions → backlog controls → Refresh, then back to Back, staying inside the page.
- **Layout:** no horizontal overflow.

## Observations (not changed)

- **CI runs list after a cancel:** the list row lags the run pane by up to one poll after a cancel (see CI above).
- **Settings-reset notice:** the sentence after "on this deployment." starts with the server's reason string as written, for example "the merged file omitted columns." with a lowercase first letter.
- **Expected demo errors:**
  - `GET /api/relay-settings` answers 501, because the demo has no relay.
  - `GET /api/repos/acme/widgets/github-branches` answers 404, because there is no GitHub without `-github-sim`.
  - `GET /api/upgrade/targets` and `GET /api/upgrade/release-notes` answer 404, because there is no upgrader.

## Stubs (browser-level `page.route`; the real server and UI otherwise)

| Route | Method | Why | Response |
|---|---|---|---|
| `/api/host/{restart,reboot,shutdown}` | POST | Never restart, reboot or power off the host | restart: 200 `{ok:true}`; reboot: aborted `connectionreset`; shutdown: 500 `{error:"intercepted host shutdown failure"}` |
| `/api/settings` | GET | The demo is not a daemon, so it never has a pending restart or a replaced unrunnable value | Real response plus `pendingRestart:["githubHost","sandboxBackend"]` and `unrunnable:[{key:"pollInterval",stored:"0s",running:"30s"}]` |
| `/api/upgrade`, `/api/upgrade/scheduled` | GET/POST/DELETE | The demo has no upgrader, and nothing may be upgraded | Idle image-mode status with `inFlightRuns: 2`. POST answers 400 for `..` or shell characters; an immediate POST answers 202 running and the next GET answers failed; a scheduled POST answers with `scheduled`, which DELETE clears |
| `/api/database/test`, `/api/database/upgrade` | POST | Never migrate the store | 400 and 409 with an "intercepted" error |
| `/api/state-repo`, `/api/state-repo/{sync,reset-notice,secrets-repo,secrets-key,files,file}` | GET/POST | The demo owns no config repository | A synthetic repository status, tree and file. One file read answers 500, and the key import answers 400 |
| `/api/agent-models` | GET | A failing provider (secret validation) | 502 with a provider error, once |
| `/api/sandboxes` | GET | Host pressure, a 60-row pool and failure states the demo cannot produce | Real response with heavy host figures and 60 synthetic rows; also `hostError`, a 500 and `{enabled:false}` |
| `/api/metrics` | GET | Partial, total-failure and slow reports | Real response plus `failed:[…]`; a 500; a 4 s delay |
| `/api/{logs,upgrade,users,package-cache,ci/runs,teams}` | GET | First-read failure probe (bugs 2 and 3, and the follow-up) | 500 `{error:"intercepted failure"}` |

GitHub needed no stub: the walkthrough used no GitHub-backed action. CI execution was the demo's real `pkg/ci.Service` with host-directory sandboxes, paced only by its own `/api/e2e/ci/finish` control.

## Tests run

- `npx vitest run` over `AdminOverlay`, `TeamGitPanel`, `TeamSettingsOverlay`, `SandboxHealthPage`, `UsersPanel`, `LogsPage` and `UpgradePanel`:
  - all pass with the fixes;
  - the 6 new tests each fail without their fix.
- `npx vitest run`: the whole unit suite, 201 files and 3321 tests, all passed.
- On Chromium, `npx playwright test --project=chromium` passed 63 tests in 1.3 min. The set was:
  - `deployment_operations_walkthrough`, `deployment-operations-*` (including the new `deployment-operations-identity-sandbox-errors`), `deployment-settings-and-operations-inputs-walkthrough`;
  - `observability`, `system_workflows`, `ci_workflows`, `system-overlay-tabs`;
  - `upgrade`, `ui-navigation-split-button-upgrade`;
  - `user-management-walkthrough`, `team-settings-github-tokens-walkthrough`.
