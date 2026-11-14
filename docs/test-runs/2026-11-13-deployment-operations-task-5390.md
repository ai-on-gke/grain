# Deployment Operations Walkthrough — 2026-11-13 (task 5390)

This run walked the System view and the pages that replace it, Settings →
Upgrade, the rail's CI page, alerts and the standing banners. Everything ran
in a real Playwright browser against a real `grain demo` server. It is the
second deployment-operations walkthrough of the day.
[The first one](2026-11-13-deployment-operations.md) (task 4732) walked the
classic UI in full and only spot-checked `/next/`. This run puts `/next/`
first, as `PROMPT.md` ("Two UIs") asks. The classic UI was walked only where
`/next/` has no page yet, or where a classic behaviour is the baseline
`/next/` should match.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/098073e4/task-5390-walk-through-system-health`.
- **Commit tested:** `e69d34223` (`main` at the start of the run; build stamp
  `v0.0.0-20261113135704-e69d34223665`). Each fix was re-verified on a binary
  built from the fix commits (stamp `…e69d34223665+dirty`).
- **Build:** `make frontend`, then `go build -o /home/debian/grain ./cmd/grain`.
  Versions: Go 1.26.10, Node 20.19.0.
- **Server:** `grain demo -addr 127.0.0.1:0 -open=false -access-code-file <file>`
  over its own throwaway embedded SQLite store. Sessions sign in as
  `alice@example.com` through `POST /api/auth/access-code`. The demo starts
  with `restartEnabled`, `rebootEnabled` and `shutdownEnabled` all false, no
  upgrader, no grain CI engine, and no proxy or Actions cache.
- **Browser:** Playwright 1.62.1 headless Chromium
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Viewports were
  1400×900 and 380/390×844, in light and dark. The browser was driven by
  throwaway scripts outside the checkout, using role, label and test-id
  locators on visible controls. Each step recorded console errors and every
  failed or ≥400 response. No screenshots or browser artefacts are committed.
- **Model/tooling:** the sandbox has no `/home/debian/.gemini-api-key`, so
  `LOOK=1` was not available. The file reader in this sandbox shows PNGs as
  raw bytes. Layout was therefore checked with bounding-box measurements (see
  "Layout check"), not by looking at the screenshots.

## Which UI each area ran against

| Area | `/next/` page | Ran against |
| --- | --- | --- |
| System → Sandboxes, Top, Logs, Pipeline, Restart | `/next/fleet` (tabs Sandboxes, Hosts, Pauses & Controls, Daemon Logs) | `/next/` |
| System → Caches | `/next/fleet?tab=controls` card + `/next/settings/caches` | `/next/` |
| System → Metrics | `/next/telemetry` | `/next/` |
| System → Health | **none** (no Health panel in `/next/`) | classic `/system/health` |
| System → Power (reboot / shut down host) | **none** (Fleet has Restart daemon only) | classic `/system/power` |
| Settings → Upgrade | `/next/settings/upgrade` | `/next/` |
| CI | `/next/ci`, `/next/ci/runs/:id` | `/next/` |
| Alerts bell | **none** (the Inbox "Blocked & alerts" group is the nearest) | classic header bell; `/next/inbox` |
| Disconnected banner, host-mode notice | **none** in `/next/` | classic |

## Findings

### F1 (fixed): classic System addresses fell through to the `/next/` Inbox

- **Reproduction:** open `/next/system/logs` (or `/next/system/health`,
  `…/sandboxes`, `…/caches`, `…/top`, `…/pipeline`, `…/power`,
  `…/upgrade`, `…/metrics?repo=acme/web`, `/next/admin/logs`,
  `/next/sandboxes` or `/next/caches`).
- **Observed:** every one landed on `/next/inbox`. `/next/system/metrics?repo=…`
  landed on `/next/inbox?repo=acme%2Fweb`. Only a bare `/next/system` and
  `/next/metrics?…` were redirected. The `system` redirect matched by prefix,
  so `system/logs` became `fleet/logs`, matched nothing, and went to `**`.
- **Fix:** `ui-ng/src/app/app.routes.ts` now has a `system/:tab` redirect
  table plus `admin/logs`, `sandboxes` and `caches` redirects, and the query
  is kept:
  - health, sandboxes and top go to `/fleet`
  - logs goes to `/fleet?tab=logs`
  - pipeline and power go to `/fleet?tab=controls`
  - caches goes to `/settings/caches`
  - metrics goes to `/telemetry`
  - upgrade goes to `/settings/upgrade`
- **Verified:** all 14 addresses land on the matching page.

### F2 (fixed): Fleet tabs were not history entries

- **Reproduction:** open `/next/fleet` and click Hosts, then Daemon Logs.
  Press Back.
- **Observed:** `pickTab` navigated with `replaceUrl: true`, and `?tab=` was
  read only once, in `ngOnInit`. So Back left Fleet altogether, or showed a
  stale panel.
- **Fix:** each tab click is now pushed onto history. The page subscribes to
  `queryParamMap` for as long as it is open, and loads the log lines when
  Back or Forward lands on Daemon Logs.
- **Verified:** Back goes from Logs to Hosts to Sandboxes, Forward goes back
  to Logs, and reload keeps the tab. The tab buttons keep their
  `aria-controls`/`aria-labelledby` pairing.

### F3 (fixed): Restart daemon reported a refused restart as under way

- **Reproduction:** on the demo (`restartEnabled: false`), open Fleet →
  Pauses & Controls and click Restart daemon, then confirm. In this run the
  POST was intercepted with a 500. The real daemon answers 404
  `errRestartUnavailable` (`pkg/ui/host.go`).
- **Observed:** the button was enabled. The handler swallowed every error and
  showed "Restart requested. Waiting for grain to come back…", plus a
  Restarting pill.
- **Fix:**
  - The button is disabled when `/api/config` says `restartEnabled: false`,
    with the tooltip "Restarting grain is not enabled for this deployment."
  - Only a dropped connection (status 0) counts as the restart under way. Any
    answer is reported as "Restart failed: <reason>" and clears the pill.
- **Verified:**
  - Disabled on the demo.
  - With `restartEnabled` stubbed true: Cancel sends nothing, and a 500
    answer shows the reason.
  - A successful answer still shows "Restart requested…". That path is
    unchanged by the fix and was walked with a 202 stub before it.

### F4 (fixed): the `/next/` build stamp led nowhere

- **Observed:** "grain v…" in the bottom bar was a `<span>`. In the classic
  UI the stamp opens Settings → Upgrade.
- **Fix:** it is now a link to `/settings/upgrade`, with a hover/focus
  underline and a `data-testid="build-stamp"`. It still ellipsizes, and it is
  still hidden on phones. The design book records both rules.

### F5 (fixed): a non-JSON error answer showed a JSON parser message

- **Reproduction:** stub `GET /api/metrics` to answer `502` with an HTML
  body, then open `/next/telemetry`.
- **Observed:** the error banner read `Unexpected token 'b', "boom" is not
  valid JSON`. Angular's fetch backend turns the parse failure into the
  error.
- **Fix:** `errorText` (`ui-ng/src/app/sessions/session-helpers.ts`) names
  the HTTP status when the body was not JSON: "The server answered 502 Bad
  Gateway." Every page that uses `errorText` gets the same wording.

### F6 (fixed): CI Re-run did not follow to the run it started

- **Reproduction:** with CI runs stubbed (the demo has no grain CI), open
  `/next/ci/runs/ci-fail-1` and click Re-run failed.
- **Observed:** `POST /api/ci/runs` carried
  `{taskId, repo, ref, recheck: true, recheckFailed: true}`, and the store
  selected the new run. The route's `:id` then pulled the inspector straight
  back to `ci-fail-1`, and the URL never changed. Re-run all and Run workflow
  did the same.
- **Fix:** after starting a run, the page opens it with `openRun(created.id)`,
  which navigates to `/ci/runs/<new id>`.
- **Verified:** the URL becomes `/next/ci/runs/ci-rerun-2` and that row is
  `aria-current`. The regression test fails without the fix.

### F7 (fixed): Start upgrade had no confirmation in `/next/`

- **Reproduction:** stub `GET /api/upgrade` as controllable, open
  `/next/settings/upgrade`, and click Start upgrade.
- **Observed:** `POST /api/upgrade {"branch":"v2.4.0","afterTasksFinish":true}`
  was sent on the click. The classic Upgrade pane `confirm()`s first.
- **Fix:** the click now opens a confirmation, "Upgrade grain to v2.4.0?".
  It says whether grain drains running tasks first ("Schedule upgrade") or
  restarts now (a destructive "Upgrade now").
- **Verified:** the unit test checks that Cancel posts nothing and that
  accepting posts. The walkthrough's intercepted 409 surfaced "stubbed:
  upgrade refused".

### F8 (fixed, wording): Fleet called hosts "Host Daemons (hostds)"

The tab and the card heading said "Host Daemons (hostds)" and "Host Daemons
(grain-agentd)". Hosts are called "hosts" in user-facing text (Settings →
Hosts already does), so both now say "Hosts". The `?tab=hostds` URL value is
unchanged.

### Gaps with no fix in this change (proposed as follow-ups)

- **No disconnected banner in `/next/`.** With every `/api/**` request
  aborted for 12 s, `/next/fleet` showed only an inline "Failed to fetch". No
  banner said the deployment was unreachable, and nothing announced the
  recovery. The classic banner works on both kinds of outage:
  - aborted requests: "This deployment cannot be reached: the connection to
    it failed…"
  - hanging requests: "…it accepted the connection but sent no answer within
    10 seconds…"

  In both cases it sits at y=0 above the rail (48 px tall; the rail starts
  below it) and clears without a reload.
- **No alerts bell in `/next/`, and no alert for a pipeline pause.** Pausing
  dispatch in Fleet → Pauses & Controls shows a "Paused" pill on Fleet only.
  The `/next/` Inbox ("Blocked & alerts") showed the same entries before,
  during and after the pause. The classic bell has no "see all" counterpart
  either: there, "see all" opens `/system` (Health).
- **System → Health and System → Power (reboot / shut down host) have no
  `/next/` page.** F1 sends their old addresses to Fleet.
- **`/next/` Settings → Upgrade is well short of the classic pane:**
  - no Image source card
  - no retracted or discarded target warnings (a stubbed `retracted: true`
    target is listed like any other)
  - release notes are shown as raw Markdown, so a breaking change is just
    `**Breaking:**` text
  - no searchable picker
  - no offline database-migration instructions (only "Database: sqlite")
  - the check mode is an editable select here, where the classic pane shows
    it read-only with a link to Settings → General
- **`/next/ci` has no active-sandboxes table.** A run id the daemon does not
  know shows "no CI run <id>", but `GET /api/ci/runs/<id>` is requested three
  times.

## Walkthrough steps & outcomes

### 1. Fleet (`/next/fleet`): navigation, tabs, URL, history

1. Rail → Fleet: lands on `/next/fleet`. The tabs are Sandboxes (0), Hosts
   (4), Pauses & Controls, Daemon Logs.
2. Each tab click: `?tab=hostds|controls|logs`, and no query for Sandboxes.
   Each panel is `role=tabpanel` and is labelled by its tab, and each tab's
   `aria-controls` names its panel.
3. Reload on `?tab=logs` keeps Logs.
4. Back and Forward: before the fix, Back left the page (F2). After it, they
   step through the tabs.
5. Keyboard: ArrowRight moves focus and selection to Hosts, and End moves to
   Daemon Logs.
6. Narrow window (380 px): the tab strip scrolls inside itself
   (`overflow-x: auto`, 646 px of content in a 348 px strip). The document
   does not scroll sideways (scrollWidth 380 = viewport).
7. Legacy addresses: see F1.

### 2. Sandboxes, Top, Logs (`/next/`)

- **Host strip:** CPU and load, vCPU allocation "Within cap 0 / — vCPU",
  memory, and disk (demo zeros).
- **Live sandboxes:** "No active task sandboxes right now." There is a
  diagnostic probe card.
- **Persistent sandboxes:** the empty state.
- **CI sandboxes:** the demo has none, so the folded "CI sandboxes (N)" row
  could not be shown.
- **Top:** "Host process snapshot is not enabled on this deployment."
- **Logs:**
  - 300 / 300 lines are tailed.
  - Searching "listening" narrows them to 1 line.
  - Refresh works.
  - The tail options are 200/500/1000. Choosing 1000 keeps 300 lines,
    because the demo has only 300.

### 3. Pauses & Controls (`/next/fleet?tab=controls`)

- **Pipeline:**
  - Pause (with a reason) goes straight to "Paused" with no confirmation. The
    classic Pause has none either, and the action is reversible.
  - It persists across a reload.
  - Resume returns it to "Dispatching".
- **Usage-limit pauses:** "No model connections are currently paused on
  quota limits."
- **Caches card:** "Package proxy cache not enabled on this host" and
  "Actions cache not enabled on this host". The Flush buttons have nothing to
  flush on the demo.
- **Restart daemon:** see F3.

### 4. Telemetry (`/next/telemetry`)

- The KPIs, Throughput & Merges, Run Outcomes, Cycle-Time Stages and tool
  cards render.
- Changing the window writes `?window=7d`. Choosing a repository writes
  `&repo=acme%2Fwidgets`. Both survive a reload, and "Clear scope" appears.
- `/next/metrics?repo=…&window=7d` keeps its query.
- The failure state is covered by F5.

### 5. CI (`/next/ci`)

- **Real demo:**
  - `ciMode` is github, so the list shows "0 of 0".
  - The shared-fault banner is present.
  - `/next/ci/runs/ci-does-not-exist-123` shows the run-not-found action
    error. There are 3 identical 404 requests.
- **Stubbed runs:** a failure with cached, failed and skipped jobs; a running
  run; and an errored run.
  - Each row is an `li role=button`, named by its own content, and opens on
    Enter or Space.
  - The failing run shows "Failed · Code", its jobs (Passed go Cached / Failed
    ui e2e / Skipped lint), Task #6 linking to `/next/task/6`, and the
    isolated-failure note.
  - Reload on `/next/ci/runs/ci-fail-1` keeps the run.
  - Re-run failed and Re-run all post the right bodies (F6).
  - Cancel run on the running run asks "Cancel CI run? Stop tests on
    acme/widgets (feature/webhook-retry)?". "Cancel run" sends
    `DELETE … {"reason":"Cancelled from CI workspace"}`, and the status
    becomes Cancelled.
  - The errored run shows the errored callout.

### 6. Settings → Upgrade (`/next/settings/upgrade`)

- **Real demo:**
  - The version pill reads `e69d342`, taken from `/api/config` because the
    demo has no upgrader.
  - The page says "Not available: … neither -upgrade-image nor
    -upgrade-src-dir …".
  - It shows "Database: sqlite".
- **Stub `selfUpgradeDisabled` + `managementUrl`:** the externally managed
  banner shows, with a "Manage instance" link to the given URL.
- **Stub controllable (`running.release: v2.3.1`):**
  - The target picker offers v2.4.0, v2.3.9 and v2.4.0-rc.1.
  - Release notes show, and Start upgrade behaves as in F7.
  - The intercepted 409 is shown.
- **From the build stamp:** see F4. `/next/system/upgrade` lands here (F1).

### 7. Classic-only steps

- **System tabs:**
  - Health, Logs, Sandboxes, Caches, Top, Pipeline, Metrics and Power each
    ride in `/system/<tab>`, with `aria-controls` → `system-tabpanel`.
  - Back and Forward step through the tabs, and reload keeps the tab.
  - `/system/upgrade` goes to `/settings/upgrade`.
  - `/admin/logs` goes to `/system/logs`.
  - `/sandboxes` and `/caches` go to their tabs.
  - `/metrics?repo=acme/widgets&window=30d` becomes
    `/system/metrics?f=repo:any:acme/widgets&window=30d`.
- **Health:** the GitHub API budget (1,650 of 5,000, "runs out in 30m 0s,
  before it resets") and the idle-poll backoff panel render.
- **Power:** "Not available" on the demo. Restart, reboot and shut down are
  disabled, so no confirmation could be reached. The intercepted POST routes
  saw nothing.
- **Bell:** "Alerts: 2 active" (a release ready to promote, a stalled merge).
  "See all" opens `/system` (Health).
- **Disconnected banner:** for both kinds of outage, see Gaps. It clears
  without a reload.

### Console & network

There were no uncaught page errors on any `/next/` page walked. Every
console error and failed request came from a deliberate stub or abort:

- the intercepted 500s, 404s and 409s
- the three 404s for an unknown CI run
- `ERR_FAILED` during the outage test

## Stubbed & intercepted boundaries

All stubs were Playwright `page.route` handlers in throwaway scripts. Each
spec stub is listed beside its test.

| Route | Why | Answer |
| --- | --- | --- |
| `POST /api/host/restart` | never restart the host | 500 `{error}` or 202 `{}` |
| `GET /api/config` (spec only) | turn `restartEnabled` on | real body + `restartEnabled: true` |
| `GET /api/metrics?…` | failure state | 500 text / 502 HTML |
| `GET/POST /api/ci/runs`, `GET/DELETE /api/ci/runs/{id}`, `…/log`, `…/artifacts` | demo has no grain CI | fixture runs; POST → new run id; DELETE → cancelled |
| `GET/POST /api/upgrade`, `/api/upgrade/targets`, `/api/upgrade/release-notes` | demo has no upgrader; never upgrade | fixture status/targets/notes; POST → 409 |
| `**/api/**` abort / hang | disconnected banner | aborted / never answered |

## Layout check

Fleet's Hosts tab and the bottom bar were measured at 1400×900 and 390×844,
in light and dark:

- Neither width scrolls the document sideways.
- On desktop the build stamp is 317 px wide and ellipsized. It sits left of
  the live-run count, in the 40 px bottom bar. On phones it is hidden, as
  before.
- The tabs read Sandboxes / Hosts / Pauses & Controls / Daemon Logs, each
  40 px tall, and the Hosts card heading is "Hosts".

## Tests run

- New: `ui-ng/e2e/next-deployment-operations.spec.js` (6 tests: the legacy
  redirects, tab history, the build stamp, restart gating and failure,
  non-JSON error text, and Re-run follow). The Re-run test was confirmed to
  fail without its fix.
- The full `ui-ng` Playwright suite (43 tests, including
  `next-full-system.spec.js`) passed on the first fix commit. The new spec's
  6 tests were re-run and pass on the final tree.
- `vitest run` for `src/app/{fleet,telemetry,sessions,ci,settings}` and
  `app.component.spec.ts` passes. `settings-page.component.spec.ts` now
  checks the upgrade confirmation.
- `eslint` and `prettier --check` pass on the changed files.
