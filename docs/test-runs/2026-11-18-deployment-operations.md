# Deployment Operations Walkthrough — 2026-11-18

Task 7021 walked the observability and operator-control screens in a real
headless Chromium against real `grain demo` servers: System / Fleet, Logs,
Sandboxes, Caches, Top, Pipeline, Metrics / Telemetry, Power, Settings →
Upgrade, the rail's CI page, the alerts bell and the standing banners. It
follows "Two UIs" in `PROMPT.md`: every step ran on `/next/` where
`ui-ng/src/app/shell/nav.ts` / `app.routes.ts` give it a page, and on classic
`/` only where they do not. The run builds on the 2026-11-16 report, whose three
fixes (Fleet's unreported disk and uptime, the pipeline confirmations, and
refusals in the CI dialogs) still hold. One new bug was found on `/next/` and
fixed in this branch, with a Playwright regression test in
`ui-ng/e2e/next-deployment-operations.spec.js`.

## Environment

- **Commit tested:** `848f5271c` (`main` at the start of the run, build stamp
  `v3.14.0-dev.301+848f527`). The fix was re-walked on a binary built from
  this branch, stamped `v3.14.0-dev.301+848f527-walk`.
- **Build:** `make frontend grain`, then `npm run build` in `ui-ng` and
  `go build ./cmd/grain` for the fix.
- **Servers:** `grain demo -addr 127.0.0.1:<port> -open=false
  -access-code-file <file>`, each with its own throwaway `HOME` and `TMPDIR`
  (embedded SQLite store). The browser signed in through
  `POST /api/auth/access-code` (`{code, name: "alice@example.com"}`).

  | Demo | Extra flags | Used for |
  | --- | --- | --- |
  | A | `-observability -staging` | `/next/fleet` (all tabs, Logs, Controls, caches), `/next/telemetry`, legacy redirects, build stamp |
  | B | `-github-sim -fake-ci -base-branch-ci -notifications -agent-paused -host-mode-install -reconcile=1s` | dispatch banner, Inbox alerts, `/next/ci`, Power (restart), install notice, disconnected banner, classic Health / bell / tabs |
  | C | `-upgrade-image=docker.io/grainai/grain -upgrade-allowed-branches=main,latest -unconfigured` | `/next/settings/upgrade` image mode |
  | D | `-disable-self-upgrade -management-url=https://console.example.com/deployments/grain-prod` | `/next/settings/upgrade` externally managed |

- **Browser:** Playwright Chromium 1234 (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`,
  `timezoneId: UTC`) at 1440×900 and 390×844 (phone, touch), light and dark.
- **Gemini:** `/home/debian/.gemini-api-key` is absent in this sandbox, so
  `LOOK=1` was unavailable. Layout was checked with DOM bounding boxes. No
  screenshots or browser artifacts are committed.

## Which UI each step ran against

| Area | Ran against | No `/next/` page or control yet (classic only) |
| --- | --- | --- |
| System navigation, tabs, URLs, legacy links | `/next/fleet` (`Sandboxes`, `Hosts`, `Pauses & Controls`, `Daemon Logs`) | Classic 8-tab strip and phone section list |
| Health | classic `/system/health` | Whole tab: alerts list, GitHub API budget and idle-poll backoff, daemon counters, `pendingRestart` / `unrunnable` field links, host readiness note |
| Sandboxes | `/next/fleet` | Overcommit controller adjustments, `"CI sandboxes (N)"` fold, per-sandbox trends |
| Caches | `/next/fleet?tab=controls`, `/next/settings/caches` | Per-repository Actions flush |
| Logs, Top | `/next/fleet?tab=logs`, `Host top` | — |
| Pipeline | `/next/fleet?tab=controls` | Manual-mode toggle |
| Power | `/next/fleet?tab=controls` (`Restart daemon`) | `Reboot host`, `Shut down host` |
| Metrics | `/next/telemetry` | Dashboard throughput-panel link |
| CI | `/next/ci`, `/next/ci/runs/:id` | `"Active sandboxes (N)"` table |
| Settings → Upgrade | `/next/settings/upgrade` | Searchable target picker with release notes, discarded-RC / retracted banners, `Image source` card, image chooser, read-only check mode linking to General |
| Alerts bell | classic `/` | The bell. `/next/` routes alerts into the Inbox's `Blocked & alerts` lane and the global dispatch banner |
| Disconnected banner | `/next/` | — |
| Install notice | `/next/` (after the fix) and classic `/` | — |

## Findings and fixes

### F1 (fixed): `/next/` never showed the unmigrated-install notice

- **Where:** `ui-ng/src/app/app.component.ts`,
  `ui-ng/src/app/settings/settings-instance.component.ts`,
  `ui-ng/src/styles.css` and `ui-ng/src/app/api/models.ts`.
- **Reproduction:** start Demo B (`-host-mode-install`). `GET /api/config`
  answers with `installNotice` (`kind: "host-mode"`, `message`, `command:
  "sudo ./scripts/setup.sh"`, `affectedClis: ["claude: …"]`, `docsUrl`).
  Classic `/` shows `install-notice-banner` above the top bar, and classic
  System → Health and Settings → Upgrade show it in full. Open `/next/inbox`,
  `/next/fleet` or `/next/settings/upgrade`: nothing mentions it, because
  `ui-ng/src` never read `installNotice`. An operator on the new UI learned
  neither why `claude` would not run nor why an in-place upgrade was refused.
- **Fix:** the shell renders `install-notice-banner` as a standing,
  in-flow strip above the rail and page. It uses the same
  `g-root:has(...)` column rule as the disconnected banner, error colours,
  `role="status"` and no close button. It reads the capitalised message,
  `Cannot run until then: claude.` and a `Migration docs` link
  (`target="_blank" rel="noopener noreferrer"`). Settings → Upgrade shows the
  full notice (`Unmigrated host-mode install.`, the command to run, and the CLIs
  that lost their run location). The design book's standing-banner rule now
  names both banners.
- **Layout checked:** at 1440×900 the banner is at `y=0, 1440×53` and the rail
  starts at `y=53`. At 390×844 it is `390×108` with the shell below it. With
  every `/api/**` aborted, the disconnected banner stacks under it
  (`y=53, h=35`) and the rail moves to `y=89`. Both banners cleared on
  recovery (the disconnected one without a reload). There is 0 px horizontal
  overflow in light and dark at both widths. Dark is `rgb(140,29,24)` on
  `rgb(249,222,220)`, light `rgb(249,222,220)` on `rgb(65,14,11)`.
- **Regression test:** `an unmigrated install raises a standing notice above
  the shell and on Upgrade` (`/api/config` overlaid with a notice, then without
  one after a reload).

## Observations (not changed)

- **`/next/system/logs?q=dispatch`** (and `/next/admin/logs?q=x`) lands on
  `/next/fleet?q=dispatch&tab=logs` with the query kept. The Logs filter box
  does not take `q`. Classic `LogsPage.jsx` ignores it too, and nothing in
  `pkg/` or `cmd/` produces such a link, so this was left alone.
- **`/next/ci`** offers `Re-run all` beside `Cancel run` on a run that is
  still running (classic offers Retry only on concluded runs). This is a
  deliberate `/next/` affordance, not a fault.
- **Shared CI fault:** the demo's fault (`CI / e2e › Run go test ./...`, 3 runs
  on 3 branches) names no assigned task in `/api/ci/faults`, so the
  "task it is assigned to" step could not be exercised on demo data.
- **Classic Health alert times** (`Since 3:45:44 AM`) carry no zone label. The
  deployment's zone is UTC and the bell's pause line does say `11:15 AM UTC`.
- **The classic-only Health tab** has no `/next/` home yet: the alerts
  list, the GitHub API budget and idle-poll backoff panel, the daemon
  counters, and the field links for settings that are pending a restart or
  not runnable. Neither do `Reboot host` and `Shut down host`. A follow-up
  was proposed to port them to Fleet.

## Walkthrough steps and outcomes

1. **Navigation (`/next/`, Demo A).** The rail's `Fleet` link →
   `/next/fleet`. The four tabs each name their panel through
   `aria-controls`, and the panel's `aria-labelledby` names the tab back. Each
   click writes `?tab=`, which survives reload, and Back / Forward step through
   the tabs. The build stamp `grain v3.14.0-dev.301+848f527` →
   `/next/settings/upgrade`. Legacy links: `/next/system` and
   `/next/system/{health,sandboxes,top,nonsense}` → `/next/fleet`;
   `/next/system/{pipeline,power}` → `?tab=controls`;
   `/next/system/logs?q=dispatch` → `/next/fleet?q=dispatch&tab=logs`;
   `/next/admin/logs?q=x` → `?q=x&tab=logs`; `/next/sandboxes?x=1` →
   `/next/fleet?x=1`; `/next/caches?y=2` and `/next/system/caches` →
   `/next/settings/caches` (query kept); `/next/system/upgrade` →
   `/next/settings/upgrade`; `/next/system/metrics?window=30d` and
   `/next/metrics?repo=acme%2Fwidgets&window=7d` → `/next/telemetry` with the
   query kept. No console errors or failed requests.
2. **Sandboxes (`/next/fleet`).** Showed CPU 15% of 4 cores (load
   0.45/0.55/0.60), vCPU `Within cap 0 / 4`, memory 4.0 / 16.0 GB with swap
   0 / 4.0 GB, and disk `Not reported`. The live table showed its empty state.
   The CI-waits table listed `#23 · 2h · my-branch · grain`. Persistent
   sandboxes listed `docs-preview` (running, `restarts: 0`) and
   `grain-staging` (restarting, `restarts: 4`, `exited with status 1`).
3. **Logs and Top.** `Daemon Logs` showed `300 / 300 lines`, with Refresh,
   Copy, a `daemon` source picker, 200/500/1000 tail sizes and the `Filter log
   lines` box. `Host top` is in the page header.
4. **Pipeline and caches (`?tab=controls`).** Showed `Dispatching` with
   `Pause pipeline`, the quota-pause list, and the package proxy cache (apt
   412 at 3.00 GB, npm, pypi, go, docker, playwright). The Actions cache showed
   `20 entries · 3.00 GB / 20.00 GB`, with both Flush buttons. The pause/resume
   and flush confirmations are spec-covered (`Fleet asks before pausing…`,
   `Settings -> Caches … asks before flushing`) and passed this run. No flush
   was confirmed against real entries.
5. **Power (`/next/`, Demo B).** With `/api/config` overlaid
   `restartEnabled: true`, `Restart daemon` opens `Restart grain daemon?
   Interrupts in-flight agent runs …`. Escape and Cancel sent no POST. A
   stubbed 500 sent one POST and was reported as failed, as the spec asserts,
   and a stubbed 200 sent the second. Reboot and Shut down are classic-only:
   all 8 classic tabs loaded at `/system/<tab>` on Demo B.
6. **Metrics (`/next/telemetry`, Demo A).** `24h`, `7d` and `30d` each write
   `?window=` and survive reload. Every section rendered: Throughput & Merges;
   Cycle-Time Stages; Run Outcomes & Agent Templates; Top Tools & CI Queue
   Causes; Quality checks; Token Usage, Cost & Concurrency Budgets; Team
   Collaboration & Shipped Summary; Active Workstream Directions; and
   Contributors & Actors (A–Z). The page links into 3 tasks. No console errors.
7. **CI (`/next/ci`, Demo B).** The page showed the fault cluster, a
   `Running` merge-recheck run with Cancel run / Re-run all / Quarantine test,
   and its jobs and artifacts. Each row's accessible name names its own run:
   `Running pull_request checks on acme/widgets grain-merge-queue/…/main @
   abcdef0 (demo-recheck-1)`. `/next/ci/runs/does-not-exist-42` asked once (one
   404) and said the run is forgotten. Start, cancel, re-run (all, and failed
   only, following to the new run) and unique row names are spec-covered and
   passed.
8. **Settings → Upgrade (`/next/`).**
   - Demo C showed `Target channel / branch: latest`, `Post-upgrade check mode:
     File & queue check`, the drain toggle and `Start upgrade`, which was not
     pressed.
   - Demo D said `In-VM self-upgrade is disabled on this deployment; …`,
     with `Manage instance` (`target="_blank"`).
   - Demo A said `Not available: … neither -upgrade-image nor
     -upgrade-src-dir`.
   - All three showed `Database: sqlite`.
   - Demo B, after F1, showed the full install notice.
9. **Alerts (Demo B).**
   - On `/next/`, the dispatch banner shows `Model quota exceeded — Gemini 2.5
     Pro development paused · resets 11:08 AM UTC (in 5h 55m)` with
     `Telemetry` and `Controls` (→ `/next/fleet?tab=controls`). The Inbox
     lists the release, stalled-merge and paused-run items.
   - The classic bell shows `Alerts: 3 active`: `v2.4.0 is ready to promote`
     (approve / reject), `A merge has been stalled for 2 hours` (`open #1`)
     and `Usage limit reached (antigravity) … until 11:15 AM UTC` (`resume
     now`, `open connection`). `see all on system → health` → `/system`.
   - Classic Health shows the same three alerts, the full install notice,
     `GitHub API: 1,117 of 5,000 calls left this hour … runs out in 29m 57s`
     with the per-caller table, `Idle-poll backoff: 2 of 3 tracked PRs …`
     and the `sign-in verification (demo)` counters.
10. **Standing banners (`/next/`, Demo B).** The install notice is covered in
    F1. The disconnected banner rose with every `/api/**` aborted and cleared
    on its own once requests were let through. The held-connection case is
    spec-covered (`the disconnected banner stands above the shell …`,
    `next-disconnected-banner.spec.js`) and passed.
11. **Phone (390×844).** `/next/fleet`, `?tab=controls`, `?tab=logs`,
    `/next/ci`, `/next/telemetry` and `/next/settings/upgrade` have 0 px
    horizontal overflow and no console errors.

## Stubbed boundaries

All stubs are Playwright `page.route` overrides in the walkthrough browser or
in the named spec. The real server, frontend and gestures stayed in the loop.

| Route | Why | Stub |
| --- | --- | --- |
| `GET /api/config` (`/next/` Power) | The demo has no restart command | Real answer overlaid with `restartEnabled: true` |
| `POST /api/host/restart` | Never restart the host | `500 {"error":"walkthrough: refused"}`, then `200 {"ok":true}` |
| `GET /api/config` (F1 spec only) | The worker demo is not a host-mode install | Real answer overlaid with an `installNotice`, then without one |
| `**/api/**` aborted (walkthrough) | Raise the disconnected banner without stopping the demo | `route.abort()` for 25 s, then unrouted |

No upgrade, reboot, shutdown or cache flush was sent to the server. The
walkthrough itself used Demo B's real `-host-mode-install` notice.

## Tests run

- `cd ui-ng && npm run format:check && npm run lint`: clean.
- `npx vitest run src/app/app.component src/app/settings`: 86 passed.
- `npx playwright test e2e/next-deployment-operations.spec.js
  e2e/next-full-system.spec.js e2e/next-disconnected-banner.spec.js`: 25
  passed, including the new F1 test.
