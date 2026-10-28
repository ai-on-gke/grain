# Deployment Settings and Operations Walkthrough

**Date:** 2026-10-04
**Branch:** `grain/task-1817`
**Commit tested:** `0e5560d0f` (`Merge pull request #2818 from bwsalmon/grain/task-1812`, the tip of `candidate`). The fixes below (`1022d96ea` and `900c02f84`) were then re-checked against a rebuilt server on a fresh store.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1, Chromium 151 (headless shell, `chromium-1234`).
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`, built with `go build ./cmd/grain` after `npm ci && npm run build`. It ran over the throwaway embedded SQLite store the demo creates under `TMPDIR`, which was a scratch directory. A second `grain demo -unconfigured` on port 8431 was started briefly for one check (see "Left as notes").
**Model and tooling:** this sandbox had no `/home/debian/.gemini-api-key`, so nothing here called Gemini. The walkthrough uses no model.

## How it was driven

The walkthrough was a set of Node scripts using Playwright's `chromium` API, kept out of the tree. They drove the real built frontend through visible controls:

- tabs and the tab strip's scroll buttons;
- MUI selects and native selects;
- text and number fields with blur-to-save;
- checkboxes, chips and their delete icons, and buttons;
- keyboard gestures: Tab, ArrowRight, Home, End, Enter, Escape and Backspace.

Each native `confirm()`/`prompt()` was answered on purpose: dismissed first, then accepted. Each script recorded console errors and warnings, page errors, failed requests (other than `ERR_ABORTED` on aborted polls) and every 4xx/5xx response. Each mutation was confirmed by reading the matching `GET` (`/api/settings`, `/api/teams/default`, `/api/ci/runs/<id>`, `/api/secrets`, `/api/pipeline-pause`) and then by reloading or deep-linking the screen.

The sign-in was real. The demo enables password auth and opens a modal sign-in dialog. While it is up, the rest of the page is hidden from role queries. The account `operator` (one of the demo's seeded system admins) was created through the dialog's "new user" tab, and the browser state was reused by later scripts. Later, the seeded `admin` account was created the same way, to restore the admin list after bug 3's reproduction.

This run started from the 2026-10-03 report and spent most of its time on what that run had not probed:

- deep links to unknown tabs;
- the refused-value round trip of every number box whose key folds a value;
- the System Administrators list;
- team model and Gemini effort selection;
- the restart-required and unrunnable notices (which the demo never produces on its own);
- the commitment note;
- synthetic CI job states;
- Upgrade scheduling and cancelling;
- long values on Metrics.

## Bugs found and fixed

| # | Bug | Reproduction (before the fix) | Fix | Regression coverage |
|---|-----|------------------------------|-----|---------------------|
| 1 | **A link naming no Admin tab opened a blank Admin.** | Open `/admin/nonsense`, or `/admin/LOGS` typed in capitals. The tab strip has no tab selected and `.pane-form` is empty. The path's segment was used as the tab id as it stood, and nothing matched it. Team settings already guards against this (`teamTabFor`). | `adminTabFor` in `AdminOverlay.jsx` matches the path's tab against `ADMIN_TABS` regardless of case (keeping the `accounts`/`admin` aliases), and falls back to the viewer's default. Re-driven: `/admin/nonsense` opens General & Deployment with its fields, and `/admin/LOGS` opens Logs. | `AdminOverlay.test.jsx` "opens General for a tab no Admin tab is named…" (fails with the fix stashed); e2e `deployment-operations-navigation` "opens General for an Admin link naming no tab". |
| 2 | **A refused or coerced count stayed in its box.** | `/admin/engine`, then type `0` into "Max merges per repo" and press Tab. The daemon refuses it (`maxMergesPerRepo must be at least 1`, 400) and the store keeps 1, but the box still shows `0` until a reload. The same happens on Integrations' "GCP service account count": the daemon silently stores a typed 0 as 1, and the box shows 0. Both fields are keyed `…-${value \|\| 1}`, so the refused value and the stored one give the same key and the box never remounts. | Both keys use the stored value itself (`?? ""`), as every other number box on the pane already does. Re-driven: after the refused 0, the box reads 1 and the store holds 1, and the same for the GCP count. | `AdminOverlay.test.jsx` "puts max merges per repo back when the daemon refuses 0" and "shows the GCP service account count the daemon stored, not the one typed" (both fail with the fix stashed). |
| 3 | **Removing a system administrator needed no confirmation, including removing yourself.** | General & Deployment → System Administrators. Click the delete icon on your own chip (`operator`): one `PUT {"adminAccounts":[…]}` goes out at once. `/api/config` then says `isAdmin: false`, and Admin collapses to Sandboxes and Caches, with only another administrator able to undo it. Backspace on a focused chip did the same. Removing the last listed admin also puts the deployment into bootstrap mode, where every user is an administrator. | `AdminAccountsSection` now `confirm()`s first, worded by `adminRemovalWarning` for the three cases (another admin, yourself, the last one). A dismissed confirm sends nothing. Re-driven: dismissing on your own chip (click, and Backspace) left the list and `isAdmin: true` unchanged. Accepting on `sysadmin` removed it, and re-adding put it back. | `AdminOverlay.test.jsx` "confirms before removing a system admin, and says so when it is you" and "warns that removing the last system admin opens Admin to everyone"; e2e `deployment-operations-navigation` "asks before removing a system admin". |

## Coverage and outcomes

### Settings

- **Tab traversal at 700 px:** all 16 Admin tabs, from General & Deployment through Upgrade, rendered with no console error. Each wrote its own `/admin/<tab>` (General keeps `/admin`).
  - The strip scrolls: `scrollWidth` 1831 against 355 visible. The selected tab scrolled into view every time, and the page itself never overflowed.
  - The left scroll button returned the strip to 0, and three presses of the right button moved it to 951.
  - Deep link `/admin/upgrade` plus a reload kept Upgrade selected. Unknown tabs: bug 1.
- **Keyboard:** from the focused Top tab, ArrowRight then Enter opened `/admin/pipeline`, End then Enter opened `/admin/upgrade`, and Home then Enter opened `/admin`. On Power, Tab cycles Restart → Reboot → Shut down → Back → the tab (the pane traps focus).
- **Integrations & Auth:** 41 tabs in total, which is the 16 Admin tabs plus 25 sub-tabs (Deployment-wide plus one per capability). The summary read "17 of 24 configured".
- **Team settings:** all 8 tabs deep-link (`/teams/default/settings/<tab>`) and were selected after load.
- **Agent and model selection (Team settings → Models):**
  - The team's connection advertises `gemini-2.5-pro` and `gemini-2.5-flash` (Antigravity).
  - Default model `gemini-2.5-flash` stored `defaultModel`, and its row then carried the Default chip after a reload.
  - The Gemini effort options were "Model default | low | medium | high". Choosing `high` stored `[{"model":"gemini-2.5-pro","effort":"high"}]` and still showed `high` after a reload.
  - Back to "Model default" by keyboard alone (focus, Enter, Home, Enter) removed the setting. Restoring the default model to none cleared `defaultModel`.
  - The deployment-wide `geminiModel`/`claudeModel` have no field in Admin any more; see "Left as notes".
- **General:**
  - The environment name `walk-1817 <b>staging</b> with a very long name indeed` was cut to the field's 32 characters and stored trimmed (31). The `<b>` showed as literal text in the field and the browser tab title (`walk-1817 <b>staging</b> with a — grain`). Clearing it restored the title "grain".
  - "Show closed tasks by default" stored true, then false.
- **Engine validation:** these poll-interval values were refused with the server's message, and the store kept `10s`:
  - `abc` (`time: invalid duration "abc"`);
  - `-5s` and `0` (`pollInterval must be positive`).

  `45s` saved and survived a reload, and `10s` was restored. "Max merges per repo" `7` saved, blank restored 1, and `0` was refused (bug 2).
- **Sandbox resources, network and bootstrap:**
  - vCPUs `3` and memory `4096` saved.
  - Network access (a native select) offered `open` and `none`; `none` saved.
  - The setup script `#!/bin/sh\necho walk-1817 bootstrap` saved and was intact after a reload.
  - Everything was then restored (0, 0, empty script, `open`).
  - The demo offers no sandbox backend control (`sandboxBackends: []`).
- **GitHub:**
  - "GitHub host" carries the outlined "needs restart" chip and "Takes effect when the daemon restarts, unlike every other setting here."
  - `ghe.walk.invalid` saved. Clearing it was refused (`githubHost cannot be empty`), and the store kept `ghe.walk.invalid`. The pane cannot put the host back to blank once one is stored; that store was thrown away when the server was rebuilt.
- **Restart-required and unrunnable notices:**
  - The demo is not inside a daemon (no `RunningConfig`), so `pendingRestart` and `unrunnable` are always null there by design.
  - With the real `GET /api/settings` response augmented (see stubs), General, Engine, Integrations and Sandbox each showed both warnings: "Stored, but not runnable: Poll interval (stored 0s, running 10s)…" and "Saved, but not applied yet: GitHub host, Sandbox backend…". Logs (a diagnostic tab) showed neither.
  - The poll-interval box carried a "not runnable" chip and the "Stored value 0s is not one grain can run on…" hint. GitHub host flipped to the filled "restart to apply" chip with "Changed, but not applied…".
- **Capabilities and secrets** (only fake values went into the demo's real secrets directory):
  - On GitHub sandbox → `github-app/app-id`, typing `fake-app-id-1817` and pressing Set stored the key, which was still "set" after a reload.
  - "Test credential" against the stubbed check:
    - `ok` showed "Credential works … checked as github-app/app-id · <time> -- a point-in-time answer";
    - `ok:false` showed "Credential refused 401 Bad credentials (stub)";
    - a 500 dropped the previous verdict and reported in the app banner.
  - Clear: dismissing kept the key, and accepting deleted it (`secrets: []`).
- **State repository and memory:**
  - Config repo says "This UI is not running inside a daemon that owns a config repository, so there is nothing to configure here." (expected in the demo).
  - The Memory and Gemini key capability sub-tabs opened with no console error.
- **Upgrade planning** (stubbed upgrader):
  - With 2 runs in flight, Upgrade opened "Confirm upgrade: 2 runs are in flight… Upgrade anyway?". Cancel posted nothing.
  - Ticking "after tasks finish" renamed the button "Schedule upgrade", which posted `{"branch":"candidate","afterTasksFinish":true}`. Cancelling the scheduled upgrade sent one `DELETE /api/upgrade/scheduled`.
  - A failed POST (500) showed `upgrader unavailable (stub)`.
  - "Check the upgrade" offered "Don't | File a proposal to approve | File it and queue it (default)"; it stored `off`, then `auto`.
  - Without the stub, the pane says it was started with neither `-upgrade-image` nor `-upgrade-src-dir`.
- **Browser-local appearance and timeline order (`/settings`):** covered by e2e `deployment_operations_walkthrough` test 1, which passed again. The 2026-10-03 walkthrough re-drove it by hand, and nothing in this area changed since.

### Sandboxes

- **Real demo, with a CI run holding a live sandbox:**
  - Host load, utilization, memory and swap, plus CPU and memory trend charts, and "No disk figures available".
  - The Storage section says "237.0 MiB written by 1 live sandbox".
  - The standalone `/sandboxes` table listed `cc02e86-0 ci host … ready`, and `/ci` listed "active sandboxes (1)" with repo, branch and job.
- **Stubbed `GET /api/sandboxes`:**
  - **Pressure:** load 15.8 of 4 vCPU and 99% utilization, with 60 rows (one 120-character name, and one `unhealthy` with an error). All 60 rendered. The table scrolls inside its own `MuiTableContainer` (2339 px of content in 704 px), and the page did not overflow at 1024 px. The unhealthy row shows its error text.
  - **500:** `host stats unavailable (stub)`, with the last reading kept on screen.
  - **`{enabled:false}`:** "Not available: this deployment has no sandbox pool or host stats configured."
  - **`hostError`:** "Host stats unavailable: reading /proc failed (stub)".
- **Commitment and pressure note (augmented settings):** "warning" and "ok" both print "5 workers + 2 mergers + 1 reserved Director + 3 CI jobs × 3 vCPU = 33 vCPU committed against the 4 this host has (8.3×). 11 × 3072 MiB = 33.0 GiB of memory, of the 15.7 GiB it has (210%)". The warning variant adds the "Some overcommitment is normal…" alert. The uncapped variant was injected too, but the script's matcher missed its wording, so it is not reported as checked.

### CI

- **Start runs from the visible form:** Start a run → repo `acme/widgets` → branch → Load workflows → Start run. It opened `/ci/<id>` running, `workflow_dispatch`, job `observe`, step "Wait for the browser (waiting for output...)".
- **Cancel:** this is a `prompt()` asking for an optional reason.
  - Dismissing it left the run running.
  - Accepting it cancelled the run and its job, and the Cancel control went away.
  - There is no re-run or retry control. Retries show only as a job's `retry n/1` chip.
- **States:**
  - **Real:**
    - running (held);
    - success (finished through the demo's `POST /api/e2e/ci/finish`);
    - cancelled;
    - a real **cache hit**: a second run on the same ref concluded success at once, and the list said "1 jobs, all green · 1 cache".
  - **Synthetic run** (stubbed `GET /api/ci/runs/synth1817`, built from a real run): a failure job with output, errored, skipped, a success cache hit, and a pending job. Each rendered with its own label, and the failed step's output (`lint: 3 problems` ×3) was shown. The pending job rendered as "running" (the stub gave it a status and no conclusion). The errored job's message was put in a field (`error`) the page does not read, so the walkthrough did not see that message rendered.
- **List and filtering:** six rows covered running, success and cancelled. Searching `walk2-1817-b` narrowed to 3 rows (the branch and its cache-hit re-run). `zzz-nothing` showed "No CI run matches. 5 runs are here…", and clearing restored the list.
- **Rows, links and reload:** Enter on a focused row button opened its run. On a finished run the job expands, and "Show output" shows `observed through the sandbox`. A reload keeps `/ci/<id>`, and Escape returns to `/ci`. The unknown `/ci/doesnotexist1817` shows `no CI run "doesnotexist1817" -- a run is remembered for a while and then forgotten` (one intended 404).
- **From a task link:** no demo task carries a `ci` block, so there was no link to follow on this store. That path stays covered by `observability.spec.js`.

### System

- **Logs:**
  - The tail loads 300 lines.
  - Search is a case-insensitive substring: `reconcile` and `RECONCILE` each matched 50 lines.
  - `(`, `.*` and `zzzz-nothing` each showed "(no matching log lines)" with no crash.
  - Refresh sent one `GET /api/logs/…`, and the tab polled 3 times in 11 s on its own.
- **Top:** auto-refresh on polls `/api/host/top`. Unticking it stopped every request over 6 s, and Refresh sent exactly one.
- **Root shell:** this build has no Root shell entry in Admin, Team settings or the rail (no "root shell" text anywhere in Admin), so there is no availability message to check.
- **Pipeline (real demo state):**
  - Pause with a 286-character reason containing `<script>` markup: dismissing the confirm left it running, and accepting paused it.
  - After a reload, the pane read "The pipeline is paused since …" with the reason as literal text, and the global banner said paused. There was no page overflow.
  - Resume: dismissing left it paused, and accepting resumed it.
- **Power:** the demo advertises all three switches, and every POST was intercepted (see stubs).
  - Each dismissed confirm sent nothing, and each accept sent exactly one POST.
  - Restart (200) → "Restart requested…".
  - Reboot (500) → `reboot refused (stub)`.
  - Shutdown (connection reset) → "Shutdown requested…". The earlier refusal stays in the banner (noted on 2026-10-03).

### Metrics

- **Sections:** Release notes (week 39, "Nothing has been written up for this week yet"), Throughput (11 filed, 3 completed), Latency, Capacity and Backlog.
- **Windows:** 24 hours, 30 days and 90 days each re-requested `?window=…&buckets=24`, re-spanned ("1d 0h", "30d 0h", "90d 0h") and wrote `?window=` into the URL. Back on 7 days, the bare `/metrics` returned.
  - The deep link `?window=30d` reopened on 30 days.
  - `?window=1d` and `?window=bogus` open on the default and drop the parameter, as `paths.js` documents (only the picker's own values are read).
- **Loading, failure, empty and long values** (stubbed):
  - A slow 503 showed a progress bar, then "The report could not be loaded (metrics store unavailable (stub))", with the release notes still rendered.
  - An all-zero report rendered "0 … 0.0/day" throughout, plus the sections' "nothing finished" text.
  - Every number multiplied to 12+ digits, and every short label lengthened by about 100 unbroken characters, caused no page overflow and no element past the viewport.
- **Navigation:** the rail's Metrics button opened `/metrics`, Escape returned to `/`, and Back returned to `/metrics`.

### Console and network

The only console errors over the whole walkthrough were intended ones:

- the validation 400s from `PUT /api/settings`;
- the stubbed 500s (`/api/sandboxes`, `/api/host/reboot`, `/api/upgrade`) and the stubbed 503 (`/api/metrics`);
- the shutdown stub's connection reset;
- the 404 for the unknown CI run;
- the demo-only `GET /api/repos/acme/widgets/github-branches` 404 (the demo has no GitHub);
- while signed in as a non-admin after bug 3's reproduction, `GET /api/state-repo` 403, with a matching console warning.

There were no page errors.

## Stubs (every intercepted route)

All stubs were Playwright `page.route` interceptions inside the browser. The server, frontend, store and gestures stayed real.

| Route | Why | What it returned |
|---|---|---|
| `POST /api/host/restart`, `/reboot`, `/shutdown` | These must never touch the host, and the demo advertises them as enabled. | 200 `{ok:true}`; 500 `{error:"reboot refused (stub)"}`; a connection reset. |
| `POST /api/capabilities/*/check` | Secret validation would call GitHub with a fake credential. | `ok:true`, then `ok:false` ("401 Bad credentials (stub)"), then a 500. |
| `GET`/`POST /api/upgrade`, `DELETE /api/upgrade/scheduled` | The demo has no upgrader, and a real one would restart grain. | The idle image-mode status (with `inFlightRuns: 2`, then 0), a scheduled status for the one scheduling POST, a 500 for the immediate POST, and an unscheduled status for the DELETE. |
| `GET /api/settings` (response augmented, three uses) | The demo runs no daemon, so `pendingRestart`, `unrunnable` and `sandboxCommitment` are never set. | The real response, plus `pendingRestart: ["githubHost","sandboxBackend"]` and `unrunnable: [{key:"pollInterval",stored:"0s",running:"10s"}]`; separately, plus a `sandboxCommitment` object at levels warning, ok and uncapped (the fixture from `SandboxCommitmentNote.test.jsx`). |
| `GET /api/sandboxes` | The demo host cannot be under pressure, fail, switch off, or carry 60 sandboxes. | The real response with a loaded host and 60 synthetic rows; then a 500; then `{enabled:false}`; then the real response with `host` removed and a `hostError`. |
| `GET /api/ci/runs/synth1817` | The demo's one workflow cannot produce errored, skipped or pending jobs. | A real finished run's payload, with five jobs re-labelled failure, errored, skipped, success (cache hit) and pending. |
| `GET /api/metrics*` | The demo store cannot fail, be empty, or hold huge values. | A 503 after 1.5 s; the real response with every number zeroed; the real response with every number and short label inflated. |

CI execution itself was not stubbed: the runs were the demo's real CI engine, finished by its own `POST /api/e2e/ci/finish`. The secrets store was not stubbed either.

## Tests run

- **Vitest:** the full suite (`npx vitest run`): 170 files, 2902 tests, all passed. The new tests in `AdminOverlay.test.jsx` each fail with the fix stashed.
- **Playwright (Chromium):** `deployment-operations-navigation` (with the two new tests), `deployment_operations_walkthrough` and `deployment-operations-escape-power`: 17/17 passed against the rebuilt frontend.
- **grain CI:** the `frontend` job passed on `900c02f84`.

## Left as notes, not changed

- **Stale first-save banner.** The Admin "Not configured yet" banner says Gemini model and Claude model "(Agents)" are required the first time, and `UpdateSettings` still refuses a first save without `geminiModel`/`claudeModel`. Admin has had no Agents tab or model fields since models moved to Team settings → Models. The daemon always seeds the config row from its flags, and even `grain demo -unconfigured` reports `configured: true`, so the banner cannot be reached in practice. It is noted rather than changed.
- **Lifetime counter against the sandbox table.** The Sandboxes pane said "No sandbox has been built since this deployment started counting them" while its own table listed a live CI sandbox. In the demo the lifetime counters are never fed. Whether a real daemon counts CI sandboxes there was not checked.
- **Carried over from 2026-10-03:**
  - An error from a failed save stays in the app-wide banner after the next save works.
  - The Upgrade button is enabled on a whitespace-only branch. It does nothing, which `UpgradePanel.test.jsx` pins down on purpose.
- **Opening CI from a task link** could not be walked on this store, because no demo task has a `ci` block.
