# Deployment Operations Walkthrough — 2026-11-14 (task 5636)

This run walked the System view and the `/next/` pages that replace it,
Settings → Upgrade, the rail's CI page, the alerts and the standing banners,
in a real Playwright browser against a real `grain demo` server. It follows
[the 2026-11-13 walkthrough](2026-11-13-deployment-operations-task-5390.md)
(task 5390), which fixed the classic System redirects, Fleet tab history, the
build stamp, restart refusals, Re-run follow and the upgrade confirmation on
`/next/`, and listed the gaps it left open. This run re-checked those fixes,
closed one of the gaps (no disconnected banner on `/next/`), and found and
fixed two new `/next/` defects.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-5636-walk-through-system-health`.
- **Commit tested:** `94eb0aced` (`main` at the start of the run). The
  baseline binary reported `v0.0.0-20261114114623-94eb0aceddc6`. Every fix was
  re-walked on a binary built from the fix commits.
- **Build:** `make frontend` (the `/next/` bundle lands in
  `pkg/ui/static-next`), then `go build -o /home/debian/grain ./cmd/grain`.
  Go 1.26.10, Node 20.19.0.
- **Server:** `grain demo -addr 127.0.0.1:<port> -open=false
  -access-code-file <file>` over its own throwaway embedded store. The
  browser signs in as `alice@example.com` with
  `POST /api/auth/access-code {code, name}`. (`grain demo` does not take
  `-sandbox-port-base`; only the daemon does.) The demo starts with
  `restartEnabled`, `rebootEnabled` and `shutdownEnabled` false, no upgrader,
  no grain CI engine (`ciMode: github`), and no proxy or Actions cache.
- **Browser:** Playwright headless Chromium from `ui-ng/node_modules`
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), at 1400×900 and
  390×844, light and dark. The browser was driven by throwaway scripts
  outside the checkout, using role, label and test-id locators on visible
  controls. Every step logged console errors, page errors, failed requests
  and every response ≥ 400. No screenshots or browser artefacts are
  committed.
- **Model/tooling:** there is no `/home/debian/.gemini-api-key` in this
  sandbox, so `LOOK=1` was not available. The sandbox file reader shows PNGs
  as raw bytes, so layout was checked with bounding-box measurements.

## Which UI each area ran against

| Area | `/next/` page | Ran against |
| --- | --- | --- |
| System → Sandboxes, Top, Logs, Pipeline | `/next/fleet` tabs | `/next/` |
| System → Caches | `/next/settings/caches`, Fleet controls card | `/next/` |
| System → Metrics | `/next/telemetry` | `/next/` |
| System → Health | **none** | classic `/system/health` |
| System → Power (reboot / shut down) | **none** (Fleet: restart only) | classic `/system/power` |
| Settings → Upgrade | `/next/settings/upgrade` | `/next/` |
| CI | `/next/ci`, `/next/ci/runs/:id` | `/next/` |
| Alerts bell | **none** (the dispatch banner covers pauses) | classic bell |
| Disconnected banner | **new in this run** | `/next/` |
| Host-mode install notice | **none** | not raised (needs a host-mode install) |

## Findings

### F1 (fixed): `/next/` never said the deployment had stopped answering

- **Reproduction:** open `/next/fleet`, then make every `/api/**` request
  fail (Playwright `route.abort()`), or hold every one open without an answer.
  Wait 30 s.
- **Observed (baseline `94eb0aced`):** nothing changed on screen. The data
  already loaded stayed up, looking current. There was no banner for a
  failed connection or for a held one, and nothing marked the recovery. The
  classic UI's `DisconnectedBanner` handles both cases.
- **Fix:**
  - A new `ConnectionStatus` service
    (`ui-ng/src/app/services/connection.service.ts`) gets its input from an
    HTTP interceptor on every `/api/` request.
  - A connection failure (status 0) or a gateway 502/503/504 raises the
    banner. The daemon's own `verifier_unavailable` 503 does not.
  - A polled read (`GET /api/tasks`, `GET /api/config`) that goes 10 s
    without an answer also raises it. Those reads are bounded by the same
    10 s, so a held connection no longer stalls the task feed's poll.
  - Any answer at all takes the banner down.
  - The shell shows a `role="status"` banner above the rail and the page,
    with the classic wording and the outage start time in the deployment's
    time zone, e.g. "This deployment cannot be reached: the connection to it
    failed. Unreachable since 12:13:40 PM — retrying automatically; this
    page will pick back up on its own once it answers again."
- **Verified (fix build):**
  - **Aborted requests:** the banner appears within one poll and reads as
    above.
  - **Held requests:** it reads "…it accepted the connection but sent no
    answer within 10 seconds…".
  - **Recovery:** both clear without a reload once requests pass again, and
    the URL is unchanged.
  - **Layout at 1400×900:** the banner is at y=0 and 35 px tall. The top
    bar and rail start at y=35, and the rail ends at the bottom of the
    viewport.
  - **Layout at 390×844:** the banner wraps to 90 px. The page does not
    scroll sideways (`scrollWidth` = viewport) in light or dark.

### F2 (fixed): pausing or resuming the pipeline on Fleet left the shell banner stale

- **Reproduction:** open `/next/fleet?tab=controls` and click "Pause
  pipeline". Then click "Resume pipeline".
- **Observed (baseline):**
  - After the pause, Fleet's own pill reads "Paused", but the shell's
    dispatch banner did not appear until the next navigation.
  - After the resume, the banner still read "Pipeline paused (manual Run now
    allowed) — paused by hand" on Fleet. The shell reads the pause from its
    `/api/config` poll, which runs once a minute and on navigation at most
    every 5 s.
- **Fix:** after a pause or resume, Fleet reads `/api/config` again into
  `AccessService`. The mutation has already cleared the cached copy.
- **Verified:** the banner appears with the pause and disappears with the
  resume, with no navigation. The new e2e test waits at most 5 s.

### F3 (fixed): Settings → Caches flushed caches with no confirmation

- **Reproduction:** open `/next/settings/caches` and click "Flush proxy
  cache" (or "Flush Actions cache").
- **Observed (baseline):** `POST /api/package-cache/flush` was sent on the
  click. The demo answered `501` because it has no cache. The Actions button
  posted `/api/actions-cache/flush` the same way. The matching buttons on
  Fleet → Pauses & Controls ask first, and so does the classic Caches tab.
- **Fix:** both buttons now open the shared confirmation dialog with Fleet's
  wording: "Flush package proxy cache?" or "Flush GitHub Actions cache?", a
  destructive "Flush cache" button, and focus on Cancel.
- **Verified:** Cancel sends nothing. "Flush cache" sends one POST, which
  the walkthrough and the spec intercept.

### Gaps still open (follow-ups proposed)

These were all listed by the 2026-11-13 run and are still true at
`94eb0aced`:

- **System → Health has no `/next/` page.** That includes the alerts list,
  the config-repo state, settings waiting on a restart, overcommit, the
  GitHub budget, daemon counters, the hosts note and the install notice.
  `/next/system/health` lands on Fleet.
- **There is no alerts bell in `/next/`.** Pipeline and usage pauses do
  show the shell's dispatch banner (since #1498), but these have no `/next/`
  surface: reconciler-down, config-repo sync failure, setup alerts, host
  sandboxes and red-base-branch repair.
- **System → Power has no reboot or shutdown on `/next/`.** Fleet offers
  Restart daemon only.
- **`/next/` Settings → Upgrade is short of the classic pane.** With stubbed
  targets:
  - a `retracted: true` target (v2.4.0) and a `discarded: true` RC are
    listed and preselected like any other, with no warning
  - release notes show as raw Markdown (`## v2.4.0 **Breaking:** …`)
  - there is no Image source card and no searchable picker
  - the database panel is only "Database: sqlite", with no offline
    migration steps
  - the check mode is an editable select, not a read-only value linking to
    Settings → General
- **Fleet → Sandboxes has no folded "CI sandboxes (N)" row**, and `/next/ci`
  has no active-sandboxes table.
- **There is no host-mode install notice on `/next/`.**

## Walkthrough steps & outcomes

### 1. Fleet (`/next/fleet`): navigation, tabs, URL, history

1. Rail → Fleet lands on `/next/fleet`. The tabs are Sandboxes (0), Hosts
   (4), Pauses & Controls and Daemon Logs. Each tab's `aria-controls` names
   its panel (`fleet-panel-sandboxes|hostds|controls|logs`).
2. Clicking each tab writes `?tab=hostds|controls|logs`. Sandboxes has no
   query.
3. Back from Daemon Logs returns to `?tab=controls`. Reload keeps "Pauses &
   Controls" selected.
4. At 390 px the tab strip scrolls inside itself (521 px of content in a
   358 px strip, `overflow-x: auto`). The document does not scroll sideways.
5. Legacy addresses (all landed as expected, query kept):

   | Address | Lands on |
   | --- | --- |
   | `/next/system`, `/system/health`, `/system/sandboxes`, `/system/top`, `/sandboxes` | `/next/fleet` |
   | `/next/system/logs` | `/next/fleet?tab=logs` |
   | `/next/admin/logs?q=x` | `/next/fleet?q=x&tab=logs` |
   | `/next/system/pipeline`, `/system/power` | `/next/fleet?tab=controls` |
   | `/next/system/caches`, `/caches` | `/next/settings/caches` |
   | `/next/system/metrics?repo=acme%2Fwidgets` | `/next/telemetry?repo=acme%2Fwidgets` |
   | `/next/metrics?window=7d` | `/next/telemetry?window=7d` |
   | `/next/system/upgrade` | `/next/settings/upgrade` |

### 2. Logs, Top, Sandboxes (`/next/`)

- **Logs:** 300 / 300 lines are tailed. Searching "listening" gives "0 / 300
  lines" and "(no matching log lines)". Refresh works.
- **Sandboxes:** "No active task sandboxes right now." The diagnostic probe
  card is shown, and the persistent sandboxes show their empty state.
- **Top:** "Host process snapshot is not enabled on this deployment."

### 3. Pauses & Controls (`/next/fleet?tab=controls`)

- **Pipeline:**
  - "Pause pipeline" pauses straight away. There is no confirmation, same as
    the classic UI; the action is reversible.
  - The pill reads "Paused", with "Reason: paused by hand alice@example.com
    (access code)".
  - The shell banner now follows the click (F2) and also shows on Inbox.
  - "Resume pipeline" returns the pill to "Dispatching" and clears the
    banner.
- **Caches card:** both Flush buttons are disabled with "…not enabled on this
  host". Their confirmations were already in place.
- **Restart daemon:** disabled on the demo, which is still correct (covered
  by the 11-13 spec).

### 4. Telemetry (`/next/telemetry`)

These sections render: Throughput & Merges, Cycle-Time Stages, Run Outcomes &
Agent Templates, Top Tools & CI Queue Causes, Token Usage, Cost & Concurrency
Budgets, Team Collaboration & Shipped Summary, Active Workstream Directions,
and Contributors & Actors. There were no console errors.

### 5. CI (`/next/ci`)

- **Real demo:** "CI · 0 of 0" with All / Running / Failed / Errored / Passed
  / Cancelled filters. The Repo, Branch and provider filters are shown, and
  so is the shared-infrastructure-fault cluster.
- **Unknown run:** `/next/ci/runs/does-not-exist-1` shows "this deployment
  has no grain CI wired", and `GET /api/ci/runs/does-not-exist-1` answers
  404.
- **Stubbed runs:** Re-run follow, cancel and the row naming were exercised
  by the existing spec (`next-deployment-operations.spec.js`), which passes.

### 6. Settings → Upgrade (`/next/settings/upgrade`)

- **Build stamp:** the stamp in the bottom bar opens `/next/settings/upgrade`.
- **Real demo:**
  - the version is `94eb0ac`, taken from `/api/config`
  - "Not available: this deployment was started with neither
    -upgrade-image nor -upgrade-src-dir…"
  - "Database: sqlite"
- **Stubbed controllable status with retracted, discarded and breaking-note
  targets:** see the gaps above.

### 7. Settings → Caches (`/next/settings/caches`)

The proxy and Actions toggles and summaries render. Flush now confirms (F3).

### 8. Classic-only steps

- **Health (`/system/health`):** loads as the default System tab (`/system`).
  It shows:
  - the GitHub API budget: 1,650 of 5,000 left, resets in 38m, runs out in
    30m, before the reset
  - the idle-poll backoff (2 of 3 PRs backed off)
  - the per-caller spend table
  - the daemon counters (`proxy:admitted 100`, …)
  - the two demo alerts
- **Bell:** "Alerts: 2 active". It lists "v2.4.0 is ready to promote…" with
  approve / reject / acknowledge / investigate, and "A merge has been stalled
  for 2 hours" with open #1 / acknowledge / investigate. "see all on system →
  health" opens `/system`.
- **Power (`/system/power`):** `/api/config` was stubbed to enable restart,
  reboot and shutdown, and the POSTs were intercepted. Each of "restart
  grain", "reboot host" and "shut down host" asks through a native
  `confirm()`:
  - "Restart grain? Every task currently running is interrupted…"
  - "Reboot the host machine? …"
  - "Shut down the host machine? … grain stays down until somebody powers
    the machine back on…"

  Dismissing sends nothing. Accepting with a 202 shows "Restart requested",
  "Reboot requested" or "Shutdown requested". Accepting with a 500 shows the
  refusal ("stubbed: refused"). No POST reached the server.

## Console & network

- `GET /api/config net::ERR_ABORTED` appeared on full-page `goto`. This is
  the browser cancelling the shell's in-flight read on navigation, and is
  benign.
- 404 for the unknown CI run is expected.
- 501 for `POST /api/package-cache/flush` on the demo (baseline, F3) is
  expected, because the demo has no cache.
- There were no page errors.

## Stubbed & intercepted boundaries

All stubs are Playwright `page.route` handlers. The real server, frontend and
browser stayed in the loop.

| Route | Why | Answer |
| --- | --- | --- |
| `**/api/**` abort / hold (walkthrough + spec) | raise the disconnected banner | aborted / never answered, then released |
| `POST /api/package-cache/flush`, `POST /api/actions-cache/flush` | never flush a real cache | 200 `{entries:0,bytes:0,cache:[]}` |
| `GET /api/config` (classic Power) | enable restart, reboot, shutdown | real body + the three flags true |
| `POST /api/host/{restart,reboot,shutdown}` | never mutate the host | 202 `{}` or 500 `{error}` |
| `GET/POST /api/upgrade`, `/api/upgrade/targets`, `/api/upgrade/release-notes` | demo has no upgrader; never upgrade | fixture status, targets and notes; POST → 409 |

## Tests

- **New unit tests:** `ui-ng/src/app/services/connection.service.spec.ts`
  (5 tests). They cover the reason sentences, raise and clear, a daemon
  error counting as reachable, the 10 s bound on polled reads, and other
  requests left alone.
- **New e2e tests** in `ui-ng/e2e/next-deployment-operations.spec.js`:
  - the disconnected banner (abort and hold, its position above the shell,
    clearing on its own)
  - the pause banner following Fleet's Pause and Resume
  - Settings → Caches flush confirmations

  All 9 tests in the file pass.
- **Full suites:** `vitest run src/app` passed (452 tests), and so did the
  full `ui-ng` Playwright suite, including `next-full-system.spec.js`.
