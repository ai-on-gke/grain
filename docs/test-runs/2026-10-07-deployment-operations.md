# Deployment Settings and Operations Walkthrough

**Date:** 2026-10-07
**Branch:** `grain/task-2416`
**Commit tested:** `afcf15814` (`Merge pull request #3124 from bwsalmon/grain/task-2414`, the tip of `candidate`). The fix below (`d2dfde382`) was then re-checked on a rebuilt server over a fresh store, and by the e2e suite.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1 with its bundled headless Chromium.
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`, built with `go build ./cmd/grain` after `npm ci && npm run build`. It ran over the throwaway embedded SQLite store the demo creates under `TMPDIR`, which was a scratch directory. A second server from a rebuilt binary (port 8421, fresh store) was used to re-check the fix.
**Model and tooling:** this sandbox had no `/home/debian/.gemini-api-key`, so nothing here called Gemini. The walkthrough uses no model. The Gemini *settings* (model and reasoning effort) were exercised through the UI.

**Setup note:** `npx playwright install chromium` needs `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, because the sandbox's package-cache proxy answers the Playwright CDN with its own certificate (as on 10-05 and 10-06).

## How it was driven

The walkthrough was a set of Node scripts using Playwright's `chromium` API, kept out of the tree (`ui/.walk/`, listed in `.git/info/exclude`). They drove the real built frontend through visible controls: tabs by click and keyboard, MUI and native selects, blur-to-save text and number boxes (filled, and typed key by key where the browser's own number parsing mattered), buttons, checkboxes, radio groups, the "Confirm upgrade" dialog, and every native `confirm()`/`prompt()`, each dismissed first and then accepted.

Each script recorded console errors and warnings, page errors, failed requests (other than `ERR_ABORTED` on aborted polls), every 4xx/5xx response, and every mutating request. Each mutation was confirmed by reading the matching `GET` (`/api/settings`, `/api/teams/default`, `/api/ci/runs/<id>`, `/api/pipeline-pause`), then by reloading or deep-linking the screen.

Sign-in was real. The account `operator` (one of the demo's seeded system admins) was created through the sign-in dialog's "new user" form, and its browser state was reused by later scripts; the same was done on the second server.

This run started from the 2026-10-06 report and went past it into: the Engine tab's merge gather window and CI queue fields; number boxes holding text the browser cannot parse; the Metrics report's per-section failure list (`MetricsReport.Failed`, new on `candidate` since 10-06); Logs scroll pinning across a poll; Top's auto-refresh switch; CI cancel with a note, a bogus run link, and a run opened from a task's grain CI notice; the upgrade dialog's Escape and Cancel and its running and failed steps; and standing-sandbox failures.

## Bug found and fixed

| Bug | Reproduction (before the fix) | Fix | Regression coverage |
|-----|------------------------------|-----|---------------------|
| **A settings number box cleared or truncated what was typed.** | Admin → Engine & Concurrency. Set "CI job cache entries" to 500 and leave the box (stored 500). Click it, select all, type `5e` and press Tab. The browser reports the box's value as `""`, the pane took that for an emptied box and sent `PUT /api/settings {"ciCacheEntries":0}`: the setting went back to grain's default without a word, and the box showed blank. Likewise Sandbox & Infrastructure → "Sandbox vCPUs" `1.5` sent `{"sandboxCpus":1}` (the carried "fractional vCPUs" note), and `1e3` in "CI output per run (KiB)", which the number box accepts as 1000, sent `{"ciRunOutputKb":1}`. Team settings' "Max agent turns" and the auto-submit boxes had the same `parseInt`. | `ui/src/numberBox.js` `readNumberBox` reads the box on blur. `validity.badInput`, a non-finite value, or a fraction where a whole number is wanted is refused with a sentence naming the field ("CI job cache entries: what was typed is not a number, so it was not saved.", "Sandbox vCPUs takes a whole number; 1.5 was not saved."). Nothing is sent, and the box goes back to the stored value it opened on. An emptied box still resets the setting, and `1e3` is read as 1000. Admin's integer and fractional boxes and Team settings' integer boxes use it. Re-driven on the rebuilt server: `5e` kept 500, `1e3` stored 1000, an emptied box stored 0, `1.5` vCPUs sent nothing, `0.2e` in the disk floor share kept 0.1, and Max agent turns `2.5` kept 40 after a reload. | `numberBox.test.js` (6). `AdminOverlay.test.jsx` "refuses a fractional vCPU count rather than saving it truncated" (fails with the fix stashed). e2e `deployment-operations-number-boxes.spec.js`: "keeps a setting when its number box is left half-typed" and "refuses a fractional vCPU count and reads an exponent as a number", both failing against a frontend built without the fix. jsdom has no `badInput`, so the half-typed case is covered only in the real browser. |

No follow-up task was proposed: nothing else found needed a change.

## Coverage and outcomes

### Settings

- **Tab traversal:** all 16 Admin tabs, selected by click in order, each wrote its own `/admin/<tab>` (General keeps `/admin`) with no console error other than the expected `501 GET /api/relay-settings` on Access, which raised no banner (fixed on 10-06). The strip measured `scrollWidth` 1831 against 1031 visible at 1400 px and had scrolled to 800 by Upgrade. No "Root shell" tab exists (removed in grain/task-193).
- **Browser-local preferences (`/settings`):** Dark and Oldest first were still checked after a reload, stored as `grain.themeMode=dark` / `grain.timelineOrder=oldest`, body background `rgb(15, 16, 19)`. Both were put back to Auto and Newest first.
- **General & Deployment:**
  - Environment name `  staging <walk> & 2416 www…` was stored trimmed and cut to the box's 30-character limit, shown literally in the header and after a reload, then cleared.
  - Adding `OPERATOR` (an existing admin in another case) said "Account is already in admin accounts" and sent nothing. `walk-admin-2416` was added; Delete on its chip asked "Remove walk-admin-2416 from the system administrators? …". Dismissing sent nothing, accepting sent the list without it.
- **Engine & Concurrency (new fields this run):**
  - Merge gather window: `soon` → 400 `time: invalid duration`, box reverted. `-5m` stored `-5m0s` (a negative window turns the wait off, `pkg/ui/settings.go:78`). `0s` stored blank, meaning grain's default. `90s` stored `1m30s`.
  - CI queue age boost: `abc` → 400; `-1m` → 400 "ciAgeBoost cannot be negative: it is the floor that stops a low-ranked job waiting for ever…"; `45m` stored `45m0s`.
  - CI capacity reserved after: `x` → 400; `-10s` stored (negative is "off", as above); `2h` stored `2h0m0s`.
  - CI job cache entries `-1` → 400 "must be 0 (grain's own default) or a bound above it"; `500` stored. CI output per run: see the bug.
  - After a reload every box matched `GET /api/settings`.
- **Sandbox & Infrastructure:** sandbox network is a select (egress / none). The setup script `#!/bin/sh\nset -eu\necho 'walk-2416 <ok> & done'\n` saved on blur and was stored without its final newline (the box trims, harmless for a shell script). vCPUs: see the bug.
- **Integrations & Auth (deployment-wide):** GitHub host `https://ghe.example.com/path` → 400 "githubHost is a host name, not a URL: give \"ghe.example.com\"…"; `ghe.example.com:99999` → 400 "has no usable :port"; `ghe.example.com` stored. Clearing it → 400 `githubHost cannot be empty` and the stored host stayed: by design, the host is required once set (`pkg/ui/settings.go:1934`), though the demo starts with it blank.
- **Restart-required and unrunnable notices** (stubbed, see below): Engine shows "Stored, but not runnable: Poll interval (stored 0s, running 10s)…" and "Saved, but not applied yet: GitHub host, Sandbox backend." Poll interval carries a "not runnable" chip and the helper "Stored value 0s is not one grain can run on, so this deployment is running with 10s — the value shown here. Save to store it."
- **Agents and models (Team settings → Models):** two models (`gemini-2.5-pro`, `gemini-2.5-flash`), each with a "Reasoning effort" select. Opened by keyboard (Enter), it offers Model default, low, medium, high. "medium" on gemini-2.5-pro sent `modelSettings:[{"model":"gemini-2.5-pro","effort":"medium"}]`, read "medium" after a reload, and Model default sent `modelSettings:[]`. Max agent turns: see the bug.
- **Capabilities, secrets, state repository, memory and Access** were walked on 10-06 and not repeated beyond the tab traversal; Config repo still says "This UI is not running inside a daemon that owns a config repository, so there is nothing to configure here."
- **Upgrade (stubbed as enabled, image mode, 1 run in flight):**
  - The pane says "Now running ghcr.io/bwsalmon/grain:candidate (afcf158)" and "1 run is in flight…".
  - Upgrade with branch `candidate` opened the "Confirm upgrade" dialog ("1 run is in flight. … Upgrade anyway?", buttons Cancel and Upgrade). Escape and Cancel each closed it and sent nothing.
  - Accepting sent `{"branch":"candidate","afterTasksFinish":false}`. The button read "Upgrading…" and was disabled; the status read "Step 2 of 3: migrate" with ✓ pull image, ◐ migrate (with its detail) and ○ restart.
  - A failed status rendered "Status failed", a 300-character detail that wrapped without overflowing the page, and "✗ pull image failed manifest unknown (stub)"; the button returned to "Upgrade".
  - "Hold the queue until the check is done" saved `upgradeCheckPausesPipeline:true` and stayed checked after a reload. The check-mode select offers Don't, File a proposal to approve, and File it and queue it (default).

### Sandboxes

- **Real demo:** load 0.45 / 0.55 / 0.60 of 4 vCPU, 15%, 4096 / 16384 MB, CPU and memory trend charts, the Storage, proxy cache, CI history, job cache and artifact sections.
- **Pressure and long table (stub):** load 15.20 / 12.10 / 9.00, 97%, 15800 / 16384 MB, swap 3900 / 4096. 60 synthetic rows: the table sits in its own `overflow-x: auto` wrapper (1876 px of table in 704 px), and the page itself did not overflow at 1400 px or at 390 px.
- **Standing sandboxes (stub):** a failed `director` reads "failed", its 230-character error, "3 failed launches in a row; next attempt …" and 2 restarts; a running `relay` reads "running" with its start time.
- **Error, disabled and host-error states (stub):** a 500 kept the last good reading on screen and raised "sandbox backend unreachable (stub)", which stayed in the banner after the next good refresh (the carried banner-persistence note). `{enabled:false}` reads "Not available: this deployment has no sandbox pool or host stats configured." A missing host with `hostError` reads "Host stats unavailable: reading /proc/loadavg: permission denied (stub)" and "Nothing in flight, so no sandboxes exist."

### CI

- **Empty state:** "No CI runs yet. Start a run above, against a repo and a branch…", with the job cache line and "No CI job is on a machine right now."
- **Start a run:** "Start run" stays disabled until a repo and branch are given. `acme/widgets` / `main`, Load workflows, Observability, Start run sent `POST /api/ci/runs` and opened `/ci/893510f305582633`. Active sandboxes (1) listed `c893510-0`. The branch box's `GET /api/repos/acme/widgets/github-branches` is a 404 in the demo (no GitHub), with no banner.
- **Running → success:** the deep link showed "running", "Cancel run" and the step "Wait for the browser" with "(waiting for output...)". The demo's own `POST /api/e2e/ci/finish` released it; within one poll it read "success" and the step output "observed through the sandbox". After a reload it was still success, and Cancel run was gone.
- **Cancel:** a second run on `feature/walk-2416`. Cancel run asks "Stop this run? The jobs in flight are told to give up and their sandboxes are destroyed. Optionally, say why…". Dismissing sent nothing. Accepting with `walk 2416: <stopping> on purpose & checking the note` sent the `DELETE` with that reason; the run read "cancelled" and "Cancelled: walk 2416: <stopping> on purpose & checking the note", literally, before and after a reload.
- **Bogus link:** `/ci/doesnotexist0000` reads `no CI run "doesnotexist0000" -- a run is remembered for a while and then forgotten`; Escape returns to `/ci` with both runs listed.
- **Conclusions (stub):** a failure run with a 120-character branch, a retried job ("retry 1/1", "first attempt failed: sandbox lost (stub)"), a 3000-line step log containing a 5000-character line, a skipped step, a cache-hit job and a skipped job; an errored run with no jobs ("could not read .grain/workflows/ci.yml: yaml: line 3 (stub)", no cancel button); a running run with a pending job and a running job with its activity line. The list summarises them as "3 jobs: 1 failure, 1 skipped, 1 success · 1 cache hit", "no jobs", and "2 jobs: 1 running, 1 pending". The failing step's log opens scrolled to its end (`scrollTop` 53867 of 54405) and wraps (`pre-wrap`), and the page did not overflow. No retry or re-run button is exposed for a finished run.
- **From a task link (stub on the task's `ci` field, pointing at a real run):** task 3 reads "grain CI is running against this task's branch (0123456789ab). Nothing is held by it yet…" and, once failed, "grain CI: failure against 0123456789ab. go test failed (stub)." "Show the run" opens the real run over the task. The URL stays `/tasks/3` and no history entry is added, so Back leaves the task and a reload shows the task without the run. `App.jsx` documents this as intended ("an overlay opened from a repo or task is deliberately transient"), so it is recorded rather than changed.

### System

- **Logs:** the daemon source shows 300 lines and opens scrolled to the end. Searching `merge-queue` left 50 lines, all matching; `  MERGE-QUEUE  ` matched the same 50 (trimmed, case-insensitive); `zzz-nothing` read "(no matching log lines)". Scrolled to the top, the 5-second poll did not pull the view back down, nor did Refresh. The Source select offers `daemon`.
- **Top:** auto-refresh is on by default and polled `GET /api/host/top` once in 6 s; switched off, it polled none in the next 6 s.
- **Root shell:** no tab and no "run as root" control, as intended since grain/task-193.
- **Pipeline (real):** "Pause pipeline" with the reason `walk 2416 <maintenance> & check` asked "Pause the pipeline? Grain will stop starting new agent and CI runs…". Dismissing sent nothing; accepting stored `{"paused":true,"reason":"walk 2416 <maintenance> & check","byKind":"human","byId":"operator"}`. The tab, and the banner on the task list, read the reason literally, and so did both after a reload. Resume asked "Resume the pipeline?…"; dismissing kept it paused, accepting stored `paused:false`.
- **Power (every POST intercepted):** Restart grain, dismissed, sent nothing; accepted, with a stubbed 200, said "Restart requested. grain is coming back up…". Reboot host, accepted, with a stubbed 500, cleared that message and showed "reboot refused (stub)". Shut down host, dismissed, sent nothing. Tab from the Power tab reaches Restart grain, Reboot host and Shut down host in order.

### Metrics

- **Sections:** release notes (Week 40, 2026, with "Nothing has been written up for this week yet…"), throughput, latency (with the "* Fewer samples…" footnote), capacity and backlog; "task 2" links to the oldest queued task.
- **Windows:** last 24 hours, 7 days, 30 days and 90 days each requested `?window=<w>&buckets=24`, wrote `/metrics?window=<w>` (7 days keeps the bare path), and changed the totals (9, 11, 12, 12 tasks filed). 90 days survived a reload.
- **Partial failure (stub on `failed`):** "Not available in this report: throughput (reading task_run.prompt: no such column (stub)), latency (xxxx…)." as an alert, with a 400-character error wrapping inside the page. The log reference is already folded into the server's sentence (`apierror` writes "(ref …)"), so the page has nothing more to add.
- **Failure, loading and long values (stub):** a 500 after a good report kept the report and raised "metrics store unreadable (stub)"; a held response showed one progress bar; `tasksFiled` 123456789012 and 17636684144.6/day fit, and the next good fetch cleared the failure. No horizontal overflow at 1400 or 390 px.

## Stubs (Playwright `page.route`, browser side only)

| Route | Why | What it answered |
|-------|-----|------------------|
| `POST /api/host/{restart,reboot,shutdown}` | Must never reach a host. | 200 `{ok:true}` for the first, then 500 `reboot refused (stub)`. Every other method passed through. |
| `GET /api/settings` (response augmented) | The demo runs no daemon, so `pendingRestart` and `unrunnable` are never set. | The real response plus `pendingRestart: ["githubHost","sandboxBackend"]` and `unrunnable: [{key:"pollInterval",stored:"0s",running:"10s"}]`. |
| `GET`/`POST /api/upgrade` | The demo has no upgrade mechanism. | Enabled, image mode, 1 run in flight; after the POST, a running status with three steps; then a failed one. `GET /api/upgrade/targets` was left to the demo's 404. |
| `GET /api/sandboxes` | The demo host cannot be loaded, fail, switch off or carry 60 sandboxes. | The real response with a loaded host, 60 rows and two standing sandboxes; then a 500; then `{enabled:false}`; then the real response with `host` removed and a `hostError`; then pass-through. |
| `GET /api/ci/runs`, `GET /api/ci/runs/stub*` | The demo's one workflow can only succeed, be cancelled or hit the cache. | The real list plus three synthetic runs (failure, errored, running with a pending job) and their details. |
| `GET /api/tasks`, `GET /api/tasks/3` (response augmented) | A task's grain CI verdict is written by the daemon. | The real task 3 plus `ci: {runId: <a real run>, commit, conclusion}`, running and then failure. |
| `GET /api/metrics` | To see the partial-failure, failure, loading and long-value states. | The real report plus `failed`; a 500; a held response; the real report with very large throughput figures. |

CI execution itself was not stubbed: runs were the demo's real CI engine, released by its own `POST /api/e2e/ci/finish` and cancelled through the UI. The pipeline pause, settings, team settings and admin list were real writes to the throwaway store. No credential was stored.

## Tests run

- **Vitest:** `numberBox.test.js` (6), `AdminOverlay.test.jsx` (88) and `TeamSettingsOverlay.test.jsx` all passed. The new AdminOverlay test fails with the fix stashed.
- **Playwright (Chromium):** `deployment-operations-number-boxes.spec.js` passed 2/2 against the rebuilt frontend, and both tests failed against a frontend built without the fix.
- **The full suites** (`go`, `frontend`, `ui e2e`) are grain CI's job on this branch.

## Left as notes, not changed

- **Banner persistence:** an error from a failed save, power action or sandbox reading stays in the app-wide banner after the next one succeeds (carried).
- **CI run from a task:** not in the URL, so Back and reload drop it. Documented as intended in `App.jsx`.
- **GitHub host:** cannot be cleared once set (by design); the demo is the one place it starts blank.
- **Setup script:** stored trimmed, so a trailing newline is dropped.
- **Integrations sub-tab and CI filter:** still not in the URL (carried).
- **Withdrawn note:** "Fractional vCPUs: `1.5` is still truncated to 1 without a word" is fixed by this change.
