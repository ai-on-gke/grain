# Deployment Settings and Operations Walkthrough

**Date:** 2026-10-05
**Branch:** `grain/task-1986`
**Commit tested:** `13f34523d` (`Merge pull request #2919 from bwsalmon/grain/task-1981`, the tip of `candidate`). The fixes below (`61f9883db`, `922b9df3a`, `2f8e13f54`) were then re-checked against rebuilt servers on fresh stores.
**Environment:** Debian GNU/Linux 12 (bookworm), Linux 6.1.0-53-amd64, 4 vCPU / 7 GiB, Go 1.26.6, Node.js 20.19.0, Playwright 1.62.1, Chromium 151 (headless shell, `chromium-1234`).
**Server under test:** this tree's own `grain demo -observability -addr 127.0.0.1:8420 -open=false`, built with `go build ./cmd/grain` after `npm ci && npm run build`. It ran over the throwaway embedded SQLite store the demo creates under `TMPDIR`, which was a scratch directory. Two more servers from rebuilt binaries (ports 8421 and 8422, each on a fresh store) were used to re-check the fixes.
**Model and tooling:** this sandbox had no `/home/debian/.gemini-api-key`, so nothing here called Gemini. The walkthrough uses no model.

**Setup note:** `npx playwright install chromium` failed with `SELF_SIGNED_CERT_IN_CHAIN`, because the sandbox's package-cache proxy answers the Playwright CDN with its own certificate. `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install chromium` worked.

## How it was driven

The walkthrough was a set of Node scripts using Playwright's `chromium` API, kept out of the tree. They drove the real built frontend through visible controls:

- tabs, sub-tabs and the tab strip;
- MUI selects, native selects and the Autocomplete branch picker;
- text and number fields with blur-to-save;
- buttons, chips and checkboxes;
- keyboard gestures: Tab, Enter, Home and Escape.

Each native `confirm()`/`prompt()` was answered on purpose: dismissed first, then accepted. Each script recorded:

- console errors and warnings;
- page errors;
- failed requests (other than `ERR_ABORTED` on aborted polls);
- every 4xx/5xx response.

Each mutation was confirmed by reading the matching `GET` (`/api/settings`, `/api/teams/default`, `/api/ci/runs/<id>`, `/api/pipeline-pause`), then by reloading or deep-linking the screen.

Sign-in was real. The account `operator` (one of the demo's seeded system admins) was created through the sign-in dialog's "create account" form, and its browser state was reused by later scripts.

This run started from the 2026-10-04 report. Most of its time went on what that run had not probed:

- the validation of every sandbox number box and of the GitHub host;
- the System panes' pointers to where their settings live;
- CI run filtering and the cancel-with-a-reason prompt;
- opening a CI run from a task's own CI status;
- the Upgrade pane's in-flight confirm dialog;
- the Metrics report's heading structure;
- the sandbox network and setup script round trip.

## Bugs found and fixed

| # | Bug | Reproduction (before the fix) | Fix | Regression coverage |
|---|-----|------------------------------|-----|---------------------|
| 1 | **The System panes sent operators to settings that are not where they said.** | Open `/admin/sandboxes`. The storage section says CI history's bounds "are on Settings → General, under CI history". General & Deployment has only the environment name, closed-task default and admins: CI history is on Engine & Concurrency. The same section calls the CI job cache and artifact bounds "the daemon's own `-ci-cache-max-*` / `-ci-artifact-max-*` flags", while the panels right under it say they are "editable in Settings", and they are (Engine & Concurrency, since grain/task-317). Every pointer there, and five on `/admin/caches`, named "Settings → Sandbox". The pane is called Admin, and the tab is Sandbox & Infrastructure. The unbacked-state dot said "Settings → Config repo". | Each pointer now names Admin and the tab's real label. The flag wording is replaced by the tab and section that hold those bounds. Re-driven on a rebuilt server: `/admin/sandboxes` lists `Admin → Sandbox & Infrastructure` once and `Admin → Engine & Concurrency` four times, with no flag names. `/admin/caches` lists `Admin → Sandbox & Infrastructure` three times. | `adminTabReferences.test.jsx` renders both panes and checks that every `X → Y` pointer is `Admin → <a label in ADMIN_TABS>`. Both tests fail with the fix stashed. e2e `deployment-operations-admin-pointers` (on the `-observability` demo) finds the CI history pointer, follows it to Engine & Concurrency and sees the CI history, CI job cache and CI artifacts sections. |
| 2 | **The Metrics report's sections were not headings.** | Open `/metrics` and list headings. The list is `grain`, `all tasks`, `metrics`, `release notes`. Throughput, Latency, Capacity, Backlog (and Scheduling / Tool use when present) were `body2` paragraphs, so a screen reader's heading navigation could not reach any section of the report. | The six section titles are `h3`, with the same styling as before (`font-size` 14px, weight 400, margin 0), beside the release notes' own `h3`. Re-driven: the heading list now holds throughput, latency, capacity and backlog. | `MetricsOverlay.test.jsx` "titles each section of the report with a heading" (fails with the fix stashed); e2e `deployment-operations-navigation` "titles each Metrics section with a heading". |
| 3 | **Any non-empty GitHub host was stored, including a URL or a value with a space.** | Admin → Integrations & Auth → Deployment-wide. Type `https://github.example.com/` into GitHub host and press Tab: `PUT {"githubHost":"https://github.example.com/"}` returned 200 and the store held it. `bad host` and `ghe.example.com:8443` were stored the same way. The key is restart-only, so nothing failed at the save. The daemon uses the value as a bare `host[:port]`: the git proxy forwards to it as it stands, and `github.APIHost` maps exactly `github.com` to `api.github.com`. After the next restart, every GitHub call would go to a host that does not exist. | `UpdateSettings` trims the value. It refuses a scheme (and suggests the host that was meant), a path, query, fragment, userinfo or whitespace, and a `:port` outside 1–65535. The field's helper text now says "A host name with an optional :port, such as github.com or ghe.example.com:8443 -- not a URL." Re-driven on a rebuilt server: `https://ghe.example.com/` got a 400 with `give "ghe.example.com" rather than "https://ghe.example.com/"` and `bad host` got a 400, and in both cases the box went back to the stored value. ` ghe.example.com:8443 ` was stored trimmed and survived a reload. | `pkg/ui/github_host_settings_test.go`: ten refused values, with the stored value left alone, and five accepted values stored trimmed (`[::1]:8443` and `127.0.0.1:9` included). Both tests fail with the fix stashed. |

## Coverage and outcomes

### Settings

- **Tab traversal at 700 px:** all 16 Admin tabs rendered with no console output. Each was selected, scrolled into view and wrote its own `/admin/<tab>` (General keeps `/admin`). The strip measured `scrollWidth` 1831 against 355 visible, and the page never overflowed.
- **Browser-local preferences (`/settings`):**
  - Choosing Dark and Oldest first wrote `grain.themeMode=dark` and `grain.timelineOrder=oldest`, and both were still selected after a reload (body background `rgb(15, 16, 19)`).
  - A fresh browser context opens on `auto` / `newest first`, so the preferences are per-browser, as intended.
- **General:** shows the environment name, "show closed tasks by default" and the System Administrators list. These were covered in depth on 10-03 and 10-04 and only re-read here.
- **Sandbox & Infrastructure, validation:**
  - vCPU `-1` → 400 `sandboxCpus must be 0 (unset) or at least 1`, and the box reverted to the stored value. `1.5` was stored as `1` (see notes). `4`, then blank, stored and cleared.
  - CPU overcommit `-1` → 400, and the box reverted to 2. `0.5` stored. The number box refuses typed letters.
  - Disk floor share `-0.1` → 400. `0.9` is stored (see notes).
  - Host memory reserve: `0`, `1024` and blank (stored as `-1`, "grain's default") all round-tripped.
- **Sandbox & Infrastructure, network and setup script:**
  - Sandbox network is a native select (`open` = "egress (outbound internet only)", `none`). Choosing `none` stored `networkAccess: "none"`.
  - A two-line setup script was stored, with its trailing newline trimmed.
  - Both, and the 0.9 share, survived a reload. All three were then restored (`open`, empty script, 0.1).
- **Restart-required, unrunnable, disk-floor and commitment notices** (settings response augmented; see stubs):
  - "Stored, but not runnable: Poll interval (stored 0s, running 10s)…" and "Saved, but not applied yet: GitHub host, Sandbox backend…" appear on every settings tab.
  - Engine & Concurrency marks the poll interval box itself.
  - Sandbox & Infrastructure shows "Kept free on this deployment's own volumes: /var/lib/grain (97.7 GB) holds 24.4 GB free… Never more than 25% of a volume is kept free, whatever is set here."
  - The commitment note reads "22 vCPU committed against the 4 this host has (5.5×)…".
- **GitHub configuration:** bug 3. Clearing the host gets 400 `githubHost cannot be empty`, and the box keeps the stored host.
- **Capabilities and secrets (Integrations & Auth → GitHub sandbox):**
  - There are 26 sub-tabs: Deployment-wide plus one per capability.
  - A fake `github-app/app-id` (`000000-fake-walk-1986`) was set. The box emptied (write-only), and `/api/settings` reported `set: true`.
  - Test credential, stubbed three ways:
    - an `ok:true` answer showed "Credential works", the detail, and "just now -- a point-in-time answer, not a badge";
    - a 500 showed `check exploded (stub)`;
    - the `ok:false` stub was malformed (`error` instead of `detail`), so it proved nothing. 10-04 covered that case.
  - Clear: dismissing the confirm left the secret set, and accepting cleared it (`set: false`). No real credential was stored.
- **Team settings → Models (agent and model selection, Gemini effort):**
  - The connection advertises `gemini-2.5-pro` and `gemini-2.5-flash`.
  - Default model `gemini-2.5-flash` and effort `high` (options: model default, low, medium, high) were both still selected after a reload.
  - Both were put back to none / Model default by keyboard alone (focus, Enter, Home, Enter), and the team row then held neither.
- **State repository and memory:** in the demo, Config repo says "This UI is not running inside a daemon that owns a config repository, so there is nothing to configure here." Memory has no Admin screen of its own beyond its capability sub-tab. Repository memory is covered by the 2026-10-04 repository/release/memory report.
- **Upgrade planning** (upgrader stubbed):
  - The idle image-mode status shows the in-flight warning ("2 runs are in flight…").
  - Upgrade on `candidate` opens the "Confirm upgrade" dialog. Cancel sent nothing, and Upgrade sent one `POST {"branch":"candidate","afterTasksFinish":false}`; the stubbed 500 showed `upgrade refused (stub)`.
  - With "wait until this deployment is idle" ticked, the button became "schedule upgrade" and posted `afterTasksFinish:true`. Its cancel sent `DELETE /api/upgrade/scheduled`.
  - The branch picker offered no options in the demo. The Upgrade button stays enabled on a whitespace-only branch, which is pinned on purpose.

### Sandboxes

- **Real demo:**
  - Host line: load 0.45/0.55/0.60 of 4 vCPU and memory 4096/16384 MB, with CPU and memory trends.
  - The storage section and the CI history, job cache and artifacts panels are present, plus bug 1.
- **Loaded host, 60 sandboxes** (stubbed): load 7.9 of 4 and memory 15800/16384. All 60 rows rendered, and the table scrolls inside its own container (`scrollWidth` 1345 in 611, `overflow-x: auto`) without overflowing the page.
- **500:** the pane showed only its title, and the app-wide banner said `sandbox host unreachable (stub)`. When the route recovered, the next 5 s poll filled the pane and the banner cleared.
- **Disabled:** "Not available: this deployment has no sandbox pool or host stats configured."
- **Host error:** "Host stats unavailable: cannot read /proc (stub)", and the empty sandbox list said "Nothing in flight, so no sandboxes exist."
- **Caches:** the proxy cache's six ecosystems with flush buttons, and the actions cache per repo. Both carry bug 1's pointers.

### CI

- **Active sandboxes and runs:** two runs held on `walk-a` and `walk-b` showed two active sandboxes and two Running rows, each saying "1 jobs: 1 running".
- **Filtering:**
  - The search `walk-a` narrowed both tables. `zzzz` gave "No CI run matches. 2 runs are here…".
  - Filter → Filter by… → conclusion → value offered only `cancelled ; running`, the conclusions present. Choosing cancelled left one row and the chip "conclusion is any of cancelled".
  - The filter survived a reload. Clear restored both rows. The narrowing is not in the URL.
- **Run, job, step and log output:**
  - A row's repo button opens `/ci/<id>`. The running run shows "cancel run", its job and the step "Wait for the browser", with "(waiting for output...)".
  - A finished run's job opened with Enter on the focused job button (`aria-expanded` false → true). "Show output" showed `observed through the sandbox`, and Escape returned to `/ci`.
- **Cancel:**
  - Dismissing the prompt left the run running.
  - Accepting with an empty reason cancelled it, and the run, job and sandbox all said cancelled.
  - On a third run, the reason `  walkthrough: <b>stop</b> this one  ` was stored trimmed and shown as literal text, before and after a reload of `/ci/<id>`.
- **Retry:** no retry or re-run control is exposed on a finished or cancelled run.
- **From a task link:** the task's CI field was stubbed onto task 4, because the demo runs no orchestrator to set one. Its "Show the run" link, pressed with Enter, opened the real cancelled run as an overlay over `/tasks/4`. That is by design (`CIRunOverlay`): the URL stays on the task, so a reload returns to the task rather than the run. Escape closed the overlay.

### System

- **Logs:**
  - The tail loads with a single `daemon` source.
  - Search: `demo` and `DEMO` matched the same lines, and `(` and `zzzz-none` showed "(no matching log lines)".
  - Refresh sent one `GET /api/logs…`, and the tab polled 3 times in 10.5 s.
- **Top:** rendered the process table, with auto-refresh and refresh. Covered in depth on 10-04.
- **Root shell:** there is still no Root shell entry anywhere in Admin, so there is no availability message to check.
- **Pipeline (real demo state):**
  - Enter in the reason box does not submit.
  - Pause with the reason `walk 1986: hold <i>dispatch</i>`: dismissing the confirm left it running, and accepting paused it.
  - After a reload, the pane and the global banner both show the reason as literal text.
  - Resume: dismissing left it paused, and accepting resumed it.
- **Power** (every POST intercepted):
  - Each dismissed confirm sent nothing, and each accept sent exactly one POST.
  - Restart (200) → "Restart requested…".
  - Reboot (500) → `reboot refused (stub)`.
  - Shutdown (connection reset) → "Shutdown requested…". The earlier refusal is still in the banner beside it, as noted on 10-03 and 10-04.

### Metrics

- **Sections:**
  - Release notes for week 40: 0 merged, 2 completed, 5 filed, and "Nothing has been written up for this week yet".
  - Throughput: 11 filed, 3 completed.
  - Latency table, with `*` on thin percentiles.
  - Capacity: 0.01 of 6, and 2 running.
  - Backlog: oldest queued is task 2.
  - Plus bug 2.
- **Windows:** 24 hours, 30 days and 90 days each wrote `?window=…` and requested `/api/metrics?window=…&buckets=24`. Going back to 7 days restored the bare `/metrics`. The deep link `?window=90d` opened on "Last 90 days", and `?window=<script>` fell back to 7 days with the parameter dropped. There was no page overflow at 800 px.
- **Navigation:** the rail's Metrics button opened `/metrics`, and Escape returned to `/`.
- **Loading, failure, empty and long values** were exercised with stubs on 10-04 and not repeated.

### Console and network

The only console errors were intended ones:

- the validation 400s from `PUT /api/settings`;
- the stubbed 500s (`/api/sandboxes`, `/api/host/reboot`, `/api/upgrade`, `/api/capabilities/github-sandbox/check`);
- the shutdown stub's connection reset;
- the demo-only `GET /api/repos/acme/widgets/github-branches` 404, because the demo has no GitHub.

There were no page errors and no warnings.

## Stubs (every intercepted route)

All stubs were Playwright `page.route` interceptions inside the browser. The server, frontend, store and gestures stayed real.

| Route | Why | What it returned |
|---|---|---|
| `POST /api/host/restart`, `/reboot`, `/shutdown` | These must never touch the host, and the demo advertises them as enabled. | 200 `{ok:true}`; 500 `{error:"reboot refused (stub)"}`; a connection reset. |
| `GET`/`POST /api/upgrade`, `DELETE /api/upgrade/scheduled` | The demo has no upgrader, and a real one would restart grain. | `{enabled:true, mode:"image", inFlightRuns:2}`; a 500 for the immediate POST; a scheduled status for the `afterTasksFinish` POST; an unscheduled status for the DELETE. |
| `POST /api/capabilities/github-sandbox/check` | Secret validation would call GitHub with a fake credential. | `ok:true` with a detail, a malformed `ok:false`, and a 500. |
| `GET /api/settings` (response augmented) | The demo runs no daemon, so `pendingRestart`, `unrunnable`, `sandboxDiskFloors` and `sandboxCommitment` are never set. | The real response plus `pendingRestart: ["githubHost","sandboxBackend"]`, `unrunnable: [{key:"pollInterval",stored:"0s",running:"10s"}]`, one disk floor, and a warning-level commitment. |
| `GET /api/sandboxes` | The demo host cannot be under pressure, fail, switch off, or carry 60 sandboxes. | The real response with a loaded host and 60 synthetic rows; then a 500 (then pass-through, to watch it recover); then `{enabled:false}`; then the real response with `host` removed and a `hostError`. |
| `GET /api/tasks`, `GET /api/tasks/4` (response augmented) | The demo runs no orchestrator, so no task carries the `ci` status that links to its run. | The real responses, with task 4 given `ci: {runId: <the real cancelled walk-b run>, conclusion: "cancelled"}`. |

CI execution itself was not stubbed. The runs were the demo's real CI engine, finished by its own `POST /api/e2e/ci/finish` and cancelled through the UI. Neither the secrets store nor the pipeline pause was stubbed.

## Tests run

- **Vitest:** the full suite (`npx vitest run`) after the first two fixes: 176 files, 2979 tests, all passed. After the GitHub host fix, `AdminOverlay.test.jsx` passed again (87 tests). The new tests fail with their fixes stashed.
- **Go:**
  - `go test ./pkg/ui/... ./cmd/grain/...` passed.
  - The new `github_host_settings_test.go` fails on every bad value with the fix stashed.
- **Playwright (Chromium):** all passed against the rebuilt frontend.
  - The new `deployment-operations-admin-pointers` and the extended `deployment-operations-navigation`: 8/8.
  - `deployment_operations_walkthrough`, `deployment-operations-escape-power`, `deployment-operations-metrics-window` and `observability`: 15/15.

## Left as notes, not changed

- **Fractional vCPUs are truncated.** Typing `1.5` into "sandbox vCPUs" stores 1 without saying so. The field is an integer count, so this is rounding rather than loss, but a refusal or a step hint would be clearer.
- **A disk floor share above the cap is stored as typed.** `0.9` is accepted and shown as 0.9, although the daemon never keeps more than 25% free. On a real daemon, the disk-floor note under the field says so ("Never more than 25% of a volume is kept free, whatever is set here"), as the stubbed check above shows. The demo reports no volumes, so the note never appears there.
- **A negative host memory reserve other than -1 is stored as typed.** `-5` was stored as -5 and shown blank, so it reads as "grain's default", the same as -1.
- **Identical names on the effort selects.** Every per-model "Reasoning effort" select on Team settings → Models has that same accessible name. The model is only given by the surrounding row, so a screen-reader user tabbing through them hears no difference. Existing unit and e2e tests address them by that exact label within the row, so renaming them is left for a change that updates those tests too.
- **The Integrations sub-tab is not in the URL.** A reload on a capability sub-tab returns to Deployment-wide.
- **The demo's GitHub host starts empty, and cannot be emptied again.** The demo seeds `githubHost: ""`, and the daemon (correctly) refuses to store an empty one. Once a host is typed, the demo can only be put back to a host, not to blank. A real daemon seeds `github.com` from its flag.
- **Carried over from 10-03 and 10-04:**
  - An error from a failed save or power action stays in the app-wide banner after the next one succeeds.
  - The stale "(Agents)" first-save banner cannot be reached in practice.
