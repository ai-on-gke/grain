# Deployment Settings and Operations Walkthrough

**Date:** 2026-10-06
**Branch:** `grain/task-2236`
**Commit tested:** `0fa005e3a` (`Merge pull request #3102 from bwsalmon/grain/task-2233`, the tip of `candidate`). The fixes below (`54ef4c961`, `1cd29da15`, `78501f5d0`) were then re-checked on a rebuilt server over a fresh store, and by the e2e suite.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with its bundled headless Chromium.
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`, built with `go build ./cmd/grain` after `npm ci && npm run build`. It ran over the throwaway embedded SQLite store the demo creates under `TMPDIR`, which was a scratch directory. A second server from a rebuilt binary (port 8421, fresh store) was used to re-check the fixes.
**Model and tooling:** this sandbox had no `/home/debian/.gemini-api-key`, so nothing here called Gemini. The walkthrough uses no model. The Gemini *settings* (model and reasoning effort) were exercised through the UI.

**Setup note:** `npx playwright install chromium` needs `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, because the sandbox's package-cache proxy answers the Playwright CDN with its own certificate (as on 10-05).

## How it was driven

The walkthrough was a set of Node scripts using Playwright's `chromium` API, kept out of the tree. They drove the real built frontend through visible controls:

- tabs and sub-tabs, by click and by keyboard (Tab, ArrowRight, Home, Enter, Escape);
- MUI selects, native selects, the default-model Autocomplete and the CI filter builder;
- text and number fields with blur-to-save, and form Save buttons;
- buttons, checkboxes and radio groups.

Each native `confirm()`/`prompt()` was answered on purpose: dismissed first, then accepted. Each script recorded:

- console errors and warnings;
- page errors;
- failed requests (other than `ERR_ABORTED` on aborted polls);
- every 4xx/5xx response;
- every mutating request (`PUT`/`POST`/`DELETE`), with secret values redacted in the script's own output.

Each mutation was confirmed by reading the matching `GET` (`/api/settings`, `/api/teams/default`, `/api/ci/runs/<id>`, `/api/pipeline-pause`), then by reloading or deep-linking the screen.

Sign-in was real. The account `operator` (one of the demo's seeded system admins) was created through the sign-in dialog's "create account" form, and its browser state was reused by later scripts. On the second server it signed in through the "log in" form.

This run started from the 2026-10-05 report and went past it into the connection editor's dirty state, keyboard traversal of the tab strips, the Access tab, the capability secret round trip, CI run starts from the UI, the CI filter builder and the stubbed CI conclusions.

## Bugs found and fixed

| # | Bug | Reproduction (before the fix) | Fix | Regression coverage |
|---|-----|------------------------------|-----|---------------------|
| 1 | **Access & Accounts raised the no-relay 501 as an error on every visit.** | Open `/admin/access` on any daemon without a relay client (the demo, and every daemon with no relay configured). `GET /api/relay-settings` answers 501 `this UI does not manage a relay client`. `RelayPanel` handed that to `showError`, so the app-wide banner turned red with that message. It stayed red after switching to General & Deployment. | `RelayPanel` ignores a 501. The panel already renders nothing until it has a view, so it stays hidden. Any other failure is still reported. Re-driven on the rebuilt server: no alert on Access or on the tab after it. | `RelayPanel.test.jsx`: "stays out of the way on a daemon with no relay client" (fails with the fix stashed) and "still reports a relay settings read that fails". e2e `deployment-operations-navigation`: "opens Access & Accounts on a daemon with no relay without an error" (fails on the unfixed frontend). |
| 2 | **An edited agent connection was thrown away without asking.** | Team settings → Connections → Edit on "gemini 2.5 pro". Change Concurrency to 3. "No unsaved changes" disappears and "Save connection" enables. Then press "← Connections" or Escape: the editor closes, with no prompt, and the edit is gone. Schedules, templates and suites already ask in this case (`Overlay`'s `isDirty`). | `ModelBackendPage` passes `Overlay` an `isDirty` that compares the latest draft with what is stored (or, for a new connection, with what it opened on), and also counts a typed-but-unset credential. Accepting the prompt on browser Back used to leave the editor open over the Team tab the URL had moved to, because the editor is not in the path. So `TeamSettingsOverlay` now closes it when Back or Forward lands on any tab other than Connections. Re-driven: "← Connections", Escape and Back each ask. Dismissing keeps the value (3). Accepting Back lands on `/teams/default/settings` with the Team tab selected. After Save (concurrency 2, `gemini-3-pro-preview` added), Escape leaves without a prompt, and the reloaded list says "Advertises: gemini-2.5-pro, gemini-2.5-flash, gemini-3-pro-preview". | `ModelBackendPage.test.jsx`: "leaves an untouched connection without asking" and "asks before Escape throws an edit away, and stays when told to" (fails with the fix stashed). e2e "asks before leaving an edited connection, and leaves it when told to" covers ← Connections, a dismissed Back and an accepted Back (fails on the unfixed frontend). |
| 3 | **The keyboard-focused tab scrolled out of view on narrow screens.** | At 700 px, focus General & Deployment on `/admin` and press ArrowRight repeatedly. Every other press leaves the focused tab mostly past the strip's right edge, under the scroll button: Engine & Concurrency 129 px out, All Users 77 px, Sandboxes 63 px, Pipeline 48 px, Executors 7 px. MUI scrolls only the *selected* tab into view. | `ui/src/tabStrip.js` `revealFocusedTab`, an `onFocus` on the strip, scrolls a focused tab into view with `inline: "nearest"`. It is set on all three scrollable strips: Admin, Team settings and the Integrations capability sub-tabs. Re-driven: all 15 ArrowRight stops are fully inside the strip. | `tabStrip.test.js`. e2e "keeps the keyboard-focused Admin tab in view on a narrow screen" walks the whole strip (fails without the fix). |

## Follow-up proposed (not fixed here)

- **The executor bootstrap command hardcodes `:8422`.** Admin → All Teams offers `curl … | bash -s -- --server 127.0.0.1:8422 …`. `TeamsPanel.getBootstrapCommand` appends `:8422` to the page's host name, whatever `grain daemon -executor-addr` is. That flag is empty by default, which disables remote execution. Nothing in `/api/config` reports the real listener. The Executors tab says `--server <server-addr:port>` instead. Fixing it needs a server-side field, so it was proposed as a separate task on `candidate`, with this reproduction.

## Coverage and outcomes

### Settings

- **Tab traversal at 700 px:** all 16 Admin tabs, each selected by click, wrote its own `/admin/<tab>` (General keeps `/admin`). The strip measured `scrollWidth` 1831 against 355 visible and scrolled with the selection (`scrollLeft` 0 → 1476). The page never overflowed (700/700). There are two scroll buttons. Keyboard: see bug 3.
- **Browser-local preferences (`/settings`):**
  - Dark and Oldest first wrote `grain.themeMode=dark` and `grain.timelineOrder=oldest`. Both were still checked after a reload, and the body background was `rgb(15, 16, 19)`.
  - ArrowRight from Dark moves within the radio group, to Auto.
  - Both were put back to Auto and Newest first.
- **Engine & Concurrency:**
  - Poll interval `ten seconds` → 400 `time: invalid duration`, and `0s` → 400 `pollInterval must be positive`. Both times the box reverted to 10s. `15s` was stored.
  - Merge batch size `0` and `-3` → 400 `mergeBatchSize must be at least 1`, and the box reverted to 3.
  - Max merges per repo `2` was stored.
  - CI runs kept `-2` was accepted, and blank stored 0.
  - After a reload the pane showed 15s and 2, the same as `GET /api/settings`.
  - The previous error stays in the banner after a later save succeeds (carried note).
- **Sandbox & Infrastructure:**
  - vCPUs: `1.5` → stored 1 (carried note), then `4`.
  - Memory: `-1` → 400 `sandboxMemoryMb must be 0 (unset) or at least 128`, and the box reverted. `16384` was stored.
  - Host memory reserve: `-5` was stored and shown blank. A negative reserve is documented as "grain's own" (`pkg/ui/settings.go`), so this is by design, and the 10-05 note about it is withdrawn. `2048` was stored.
  - Disk floor share `1.5` → 400 ("between 0 … and 1"), and `0.2` was stored. Floor minimum `-1` → 400.
  - Proxy cache size `-4` → 400 `packageCacheSizeGb must be 0 (unset) or at least 1`.
  - Sandbox network `none` was sent as `{"networkAccess":"none"}`.
  - Setup script `#!/bin/sh\necho walk-2236 <ok> & done` was stored and shown literally after a reload.
- **Integrations & Auth (deployment-wide):**
  - GitHub host `https://ghe.example.com/` → 400, and the box reverted. `ghe.example.com:8443` was stored. The "needs restart" chips are on GitHub host and plain-HTTP.
  - GCP service account count `-1` is stored as 1 (clamped by design).
  - GCP project `  walk-proj  ` was stored trimmed.
- **Restart-required and unrunnable notices** (with `pendingRestart`/`unrunnable` stubbed, see below):
  - General, Engine and Integrations all show "Stored, but not runnable: Poll interval (stored 0s, running 10s)…" and "Saved, but not applied yet: GitHub host, Sandbox backend…".
  - Logs (a diagnostic tab) shows neither.
  - Poll interval carries a "not runnable" chip, and its helper text says "Stored value 0s is not one grain can run on, so this deployment is running with 10s".
  - GitHub host's chip becomes "restart to apply", with "Changed, but not applied" helper text.
- **Agents and models (Team settings → Models / Connections):**
  - **Default model:** the default-model Autocomplete, typed "flash", offered `gemini-2.5-flash`. ArrowDown and Enter sent `PUT /api/teams/default {"defaultModel":"gemini-2.5-flash"}`.
  - **Effort, by keyboard:** the `gemini-2.5-pro` effort select, opened with Enter, offered Model default, low, medium and high. "high" sent `modelSettings:[{"model":"gemini-2.5-pro","effort":"high"}]`.
  - **Effort, by mouse:** "low" was chosen on flash.
  - **Max agent turns:** `-4` → 400 `maxAgentTurns cannot be negative`, and the box reverted. `40` was stored.
  - **After a reload:** flash is the default, the efforts read high and low, and turns read 40. Flash's effort was then reset to Model default.
  - **Connection editor:** see bug 2. The dirty and clean states read correctly ("No unsaved changes" and Save disabled when clean).
- **Capabilities and secrets (Integrations → GitHub sandbox):**
  - Setting a **fake** `github-app/app-id` sent one `PUT /api/secrets/github-app/app-id`. The box emptied, and the panel said "stored … write-only, never shown or read back", with the other key still "not set".
  - "Test credential" (stubbed) showed "Credential works" with the stub's detail. A stubbed 500 put `github said 401 (stub)` in the banner.
  - After a reload the value was not anywhere in the page's HTML.
  - Clear asks "Clear github-app/app-id? The stored value cannot be read back…". Dismissing sent nothing, and accepting sent one `DELETE`. Afterwards the panel listed both keys as missing.
  - The Integrations sub-tab is still not in the URL, so a reload returns to Deployment-wide (carried note).
- **State repository and memory:** Config repo says "This UI is not running inside a daemon that owns a config repository, so there is nothing to configure here". All Teams still lists each team's `grain/config-*` repository and clone command. The memory capability sub-tab and Team settings' memory section render. Neither has anything to edit in the demo.
- **Upgrade (stubbed as enabled, image mode, 2 runs in flight):**
  - The branch `candidate` was typed. Upgrade opened "Confirm upgrade" with "2 runs are in flight…". Cancel sent nothing.
  - Accepting sent `{"branch":"candidate","afterTasksFinish":false}`. The stub's 500 showed `upgrade refused (stub)`.
  - With "Wait until this deployment is idle" ticked, the button reads "Schedule upgrade" and the POST carried `afterTasksFinish:true`, with no confirm. Cancelling the schedule sent the `DELETE` and returned the form to "Schedule upgrade".
  - Database backend shows SQLite and a PostgreSQL DSN form. It was not submitted.
- **Access & Accounts:** see bug 1. Password sign-in, open registration and the Google OAuth section render. The account table's empty state reads "No authorized accounts configured".

### Sandboxes

- **Real demo:**
  - Host line: load 0.45 / 0.55 / 0.60 of 4 vCPU, 15%, 4096 / 16384 MB.
  - Storage, CI history, job cache and artifacts sections.
  - "Nothing in flight, so no sandboxes exist."
  - While two CI runs were held, CI's "active sandboxes (2)" listed `c8b99dc-0` and `c3cb476-0`.
- **Pressure and long table (stub):**
  - Load 15.20 / 12.10 / 9.00 of 4 vCPU, 97%, 15800 / 16384 MB, swap 3900 / 4096.
  - 60 synthetic rows (alternating agent and ci, some with long names), and "1269.5 GiB written by 60 live sandboxes".
  - The pane scrolled to row 59 with no page overflow.
- **Failure:** a 500 put `sandbox stats unavailable (stub)` in the banner and kept the last good reading on screen.
- **Disabled:** "Not available: this deployment has no sandbox pool or host stats configured."
- **Host error:** "Host stats unavailable: reading /proc/loadavg: permission denied (stub)".
- **Recovery:** removing the stub and pressing refresh restored the real host.

### CI

- **Start a run** (the page's own form):
  - "Start run" is disabled until a repo, a branch and a workflow are chosen.
  - "Load workflows" on acme/widgets@main listed `Observability — .grain/workflows/observability.yml`.
  - Two runs (`walk-2236-a`, `walk-2236-b`) were each answered 202. Each opened `/ci/<id>`, and each held a live sandbox.
  - The demo-only `GET /api/repos/acme/widgets/github-branches` 404 is expected: the demo has no GitHub.
- **States:**
  - Running (the job "Wait for the browser", "(waiting for output...)").
  - Success.
  - Cache hit: re-running `walk-2236-a` finished in 0 s with "1 jobs, all green · 1 cache hit", the job showed "cache hit", and the hit rate rose.
  - Cancelled.
  - Failure, errored, skipped and a no-conclusion run were stubbed (see below). The list showed "1 jobs: 1 failure / errored / skipped / running".
  - The failed run's step "Test `<script>alert(1)</script>`" was shown as literal text, with `exit 2`. Its 4000-character line wrapped inside the `pre` (`pre-wrap`, no page overflow), and the skipped step "Upload" was listed.
  - The errored run showed its reason, `sandbox: could not acquire a machine: no capacity (stub)`, and "No steps ran."
  - Only an unfinished run offers "cancel run". No retry or re-run control is exposed anywhere.
- **Cancel:**
  - The prompt "Stop this run? …Optionally, say why…": dismissing left the run running.
  - Accepting with `  walk 2236: <i>stop</i>  ` cancelled it, and the reason was stored trimmed.
  - After a reload, `/ci/<id>` shows "Cancelled: walk 2236: <i>stop</i>" as literal text.
- **Run, job, step and log:**
  - On `/ci/<id>`, Enter on the focused job button expanded it, and "show output" showed `observed through the sandbox`.
  - Escape returned to `/ci`.
  - The deep link `/ci/doesnotexist0000` says `no CI run "doesnotexist0000" -- a run is remembered for a while and then forgotten`.
- **Filtering:**
  - Filter → "filter by…" offers conclusion, repo, team, branch, job, started, finished and duration.
  - Conclusion → "is any of" offered only the conclusions present (cancelled, success). Choosing cancelled left the one cancelled row.
  - The filter survived a reload (it is kept in the browser, not in the URL).
- **From a task link:** not repeated (covered on 10-05 with the task CI field stubbed).

### System

- **Logs:**
  - The `daemon` source loads 300 lines and opens pinned to the end.
  - Search: `merge-queue` and `MERGE-QUEUE` both gave 50 lines. `(`, `zzzz-none` and `<b>` gave "(no matching log lines)".
  - Refresh sends exactly one `GET /api/logs/daemon?lines=500` per press (checked over four presses; an earlier pair was a coincident 5 s poll), and the pane polled twice in 10.5 s.
  - Tab order from the Logs tab: source → search → refresh → log → back.
- **Top:** the process table renders, with auto-refresh and refresh.
- **Root shell:** there is still no Root shell entry anywhere in Admin (searched the whole page), so there is no availability message to check.
- **Pipeline (real store):**
  - Enter in the reason box does not submit.
  - Pause: "Pause the pipeline? …". Dismissing left it running. Accepting stored `{"paused":true,"reason":"walk 2236: hold <i>dispatch</i>","byKind":"human","byId":"operator"}`.
  - After a reload, the pane and the global banner both show the reason literally.
  - Resume: "Resume the pipeline? …". Dismissing left it paused, and accepting resumed it.
- **Power** (every POST intercepted):
  - Each dismissed confirm sent nothing, and each accept sent exactly one POST.
  - Restart (200) → "Restart requested. grain is coming back up…".
  - Reboot (500) → `reboot refused (stub)`.
  - Shutdown (connection reset) → "Shutdown requested…". The earlier reboot refusal is still in the banner beside it (carried note).
  - Enter on a focused button opens the same confirm.

### Metrics

- **Headings:** metrics, release notes, throughput, latency, capacity, backlog.
- **Release notes:** week 40, 2026: 0 merged, 0 completed, 2 filed.
- **Throughput:** 11 filed, 3 completed, 2 attempts started.
- **Latency:** the table has `*` on thin percentiles.
- **Windows:** 24 hours, 30 days and 90 days each wrote `?window=…` and requested `/api/metrics?window=…&buckets=24`. 7 days restored the bare `/metrics`.
- **Deep links:** `?window=30d` opened on "Last 30 days", and `?window=bogus` fell back to 7 days with the parameter dropped.
- **Failure (stubbed 500):** "The report could not be loaded (metrics store unreachable (stub)). Refresh to try again." The release notes stay on screen.
- **Loading (response held):** a progress bar is shown.
- **Navigation:** the rail's Metrics button opened `/metrics`, and Escape returned to `/`. There was no overflow at 800 px.

### Console and network

The only console errors were intended ones:

- the validation 400s;
- the stubbed 500s;
- the shutdown stub's connection reset;
- the demo's 404 for `github-branches`;
- before fix 1, the `relay-settings` 501.

There were no page errors and no warnings.

## Stubs (every intercepted route)

All stubs were Playwright `page.route` interceptions inside the browser. The server, frontend, store and gestures stayed real.

| Route | Why | What it returned |
|---|---|---|
| `POST /api/host/restart`, `/reboot`, `/shutdown` | These must never touch the host, and the demo advertises them as enabled. | 200 `{ok:true}`; 500 `{error:"reboot refused (stub)"}`; a connection reset. |
| `GET`/`POST /api/upgrade`, `DELETE /api/upgrade/scheduled`, `GET /api/upgrade/targets` | The demo has no upgrader, and a real one would restart grain. | `{enabled:true, mode:"image", inFlightRuns:2}`; a 500 for the immediate POST; a scheduled status for the `afterTasksFinish` POST; an unscheduled status for the DELETE; `[]` for targets. |
| `POST /api/capabilities/github-sandbox/check` | Secret validation would call GitHub with a fake credential. | `ok:true` with a detail, then a 500 `github said 401 (stub)`. |
| `GET /api/settings` (response augmented) | The demo runs no daemon, so `pendingRestart` and `unrunnable` are never set. | The real response plus `pendingRestart: ["githubHost","sandboxBackend"]` and `unrunnable: [{key:"pollInterval",stored:"0s",running:"10s"}]`. |
| `GET /api/sandboxes` | The demo host cannot be under pressure, fail, switch off, or carry 60 sandboxes. | The real response with a loaded host and 60 synthetic rows; then a 500; then `{enabled:false}`; then the real response with `host` removed and a `hostError`; then pass-through. |
| `GET /api/ci/runs` (response augmented), `GET /api/ci/runs/stub*` | The demo's one workflow can only succeed, be cancelled or hit the cache. | The real list plus four synthetic runs (failure with a skipped step and a 4000-character log line, errored with a reason, skipped, and no conclusion), and their details. |
| `GET /api/metrics` | To see the failure and loading states. | A 500; then a held response, released after the check. |

CI execution itself was not stubbed. The real runs were the demo's real CI engine, released by its own `POST /api/e2e/ci/finish` and cancelled through the UI. Neither the secrets store nor the pipeline pause was stubbed. Only fake credential values were stored, and they were cleared afterwards.

## Tests run

- **Vitest:**
  - `RelayPanel.test.jsx` (7), `ModelBackendPage.test.jsx` (7), `TeamSettingsOverlay.test.jsx`, `App.overlays.test.jsx`, `AdminOverlay.test.jsx`, `CapabilitiesPanel.test.jsx` and `tabStrip.test.js`: all passed.
  - Each new test fails with its fix stashed.
- **Playwright (Chromium):** `deployment-operations-navigation.spec.js` passed 9/9 against the rebuilt frontend. Each of its three new tests was also run against a frontend built without the fix, and failed.
- **The full suites** (`go`, `frontend`, `ui e2e`) are grain CI's job on this branch.

## Left as notes, not changed

- **Effort select names:** every per-model "Reasoning effort" select still has that same accessible name (carried note).
- **Banner persistence:** an error from a failed save or power action stays in the app-wide banner after the next one succeeds (carried note). The same happens to the Sandboxes 500 after the disabled reading.
- **Fractional vCPUs:** `1.5` is still truncated to 1 without a word (carried note).
- **Integrations sub-tab:** the open sub-tab is not in the URL (carried note).
- **CI filter:** the filter is kept per browser rather than in the URL, so a copied `/ci` link does not carry it.
- **Withdrawn note:** the 10-05 note "a negative host memory reserve other than -1 is stored as typed" is withdrawn. Any negative value means "grain's default" by design.
