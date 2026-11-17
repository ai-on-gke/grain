# Deployment Operations Walkthrough — 2026-11-16

Task 6337: the observability and operator-control screens (System / Fleet,
Logs, Sandboxes, Caches, Top, Pipeline, Metrics / Telemetry, Power,
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners), walked in a real headless Chromium against real `grain demo`
servers. Following "Two UIs" in `PROMPT.md`, every step ran on `/next/` where
`ui-ng/src/app/shell/nav.ts` / `app.routes.ts` give it a page, and on classic
`/` only where they do not. Three bugs were found on `/next/` and fixed in
this branch, each with a Playwright regression test in
`ui-ng/e2e/next-deployment-operations.spec.js`.

## Environment

- **Commit tested:** `234824180` (`main` at the start of the run, build stamp
  `v3.12.0-dev.307+2348241`). The fixes were re-walked on binaries built from
  this branch (`v3.12.0-dev.308+1171dc8`, `v3.12.0-dev.309+f7b0ea0` and the
  final head).
- **Build:** `make frontend grain` (classic `ui`, `ui-ng`, `ui-hub` bundles
  embedded into `bin/grain`).
- **Servers:** `grain demo -addr 127.0.0.1:<port> -open=false
  -access-code-file <file>`, each with its own throwaway `HOME`/`TMPDIR`
  (embedded SQLite store), signed in through `POST /api/auth/access-code` as
  `alice@example.com`.

  | Demo | Extra flags | Used for |
  | --- | --- | --- |
  | A | `-observability -staging` | `/next/fleet` (all tabs, Top, Logs, pipeline gate, caches, daemon card), `/next/telemetry`, `/next/ci` fault cluster, legacy redirects |
  | B | `-github-sim -fake-ci -base-branch-ci -notifications -agent-paused -host-mode-install -reconcile=1s` | `/next/ci` start / cancel / re-run / forgotten run, classic bell, Health, install notice, disconnected banner, Power |
  | C | `-disable-self-upgrade -management-url=https://console.example.com/deployments/grain-prod` | `/next/settings/upgrade` externally managed notice and `Manage instance` link |
  | D | `-upgrade-image=docker.io/grainai/grain -upgrade-allowed-branches=main,latest` | `/next/settings/upgrade` target picker, check mode, Start upgrade (not pressed) |

- **Browser:** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`,
  `timezoneId: UTC`) at 1440×900, 1024×860 and 390×844 (phone, touch), light
  and dark.
- **Gemini:** `/home/debian/.gemini-api-key` is absent in this sandbox, so
  `LOOK=1` was unavailable. Screenshots were checked with DOM bounding-box
  assertions instead (see "Layout checks"). No screenshots or browser
  artifacts are committed.

## Which UI each step ran against

| Area | Ran against | Classic-only (no `/next/` page or control yet) |
| --- | --- | --- |
| System navigation, tabs, URLs, legacy links | `/next/fleet` (`Sandboxes`, `Hosts`, `Pauses & Controls`, `Daemon Logs`) | Classic 8-tab strip and phone section list |
| Health | classic `/system/health` | Whole tab: bell alerts list, GitHub API budget / idle-poll backoff, daemon counters, `pendingRestart` / `unrunnable` field links, host readiness note |
| Sandboxes | `/next/fleet` | Overcommit controller adjustments, `"CI sandboxes (N)"` fold, per-sandbox trends |
| Caches | `/next/fleet?tab=controls`, `/next/settings/caches` | Per-ecosystem / per-repository Flush buttons |
| Logs, Top | `/next/fleet?tab=logs`, `Host top` | — |
| Pipeline | `/next/fleet?tab=controls` | Manual-mode toggle |
| Power | `/next/fleet` (`Restart daemon`) | `Reboot host`, `Shut down host` (classic `/system/power`) |
| Metrics | `/next/telemetry` | 90d window, dashboard throughput link |
| CI | `/next/ci`, `/next/ci/runs/:id` | `"Active sandboxes (N)"` table |
| Settings → Upgrade | `/next/settings/upgrade` | Searchable target picker with release notes, retracted / discarded banners, image chooser, read-only check mode linking to General, database migration instructions |
| Alerts bell, disconnected banner, install notice | classic `/` | All three: `ui-ng` has no bell, no disconnected banner and no install notice |

## Findings and fixes

### F1 (fixed): Fleet made up a disk and a green uptime it was never given

- **Where:** `ui-ng/src/app/fleet/fleet-page.component.ts`.
- **Reproduction:** Demo A (`-observability`) answers `GET /api/sandboxes`
  with a `host` that has no `disks` and with no `daemon` record. Open
  `/next/fleet?tab=controls`. The Primary Disk card shows a `/` path pill,
  `0 MB / 0 MB` and `host volume`. Daemon Lifecycle shows a green
  `Up —` pill. Classic System → Sandboxes shows `No disk figures available.`
  and leaves out the daemon line in the same case.
- **Fix:** with no disk, the card shows `Not reported` with no path pill and
  no volume meta. With no `daemon.startedAt`, the pill is a neutral
  `Uptime not reported`.
- **Regression test:** `Fleet says a disk and an uptime it was not told are
  not reported` (also checks the reported state: `/var/lib/grain`,
  `2.0 GB / 8.0 GB`, `Up <age>`).

### F2 (fixed): Pause pipeline and Resume pipeline on Fleet acted without asking, and Resume silently cancelled a scheduled upgrade

- **Where:** `ui-ng/src/app/fleet/fleet-page.component.ts`.
- **Reproduction:** open `/next/fleet?tab=controls` and click `Pause pipeline`.
  `POST /api/pipeline-pause` goes out on the click, with no dialog. Clicking
  `Resume pipeline` sends `DELETE` the same way. Classic `PipelineControl.jsx`
  asks for both. When the pause is the scheduled-upgrade hold
  (`byKind: "automation"`, `byId: "upgrade-schedule"`), classic also says that
  resuming cancels the upgrade (`ui/src/alerts.js` `resumeCancelsUpgrade`).
  `/next/` threw the upgrade away without a word.
- **Fix:** both buttons go through Fleet's `<g-confirm-dialog>` with classic's
  wording. The upgrade-hold case is a destructive `Resume and cancel upgrade`
  confirm that quotes the pause reason (Cancel gets focus, per the dialog's
  destructive rule).
- **Regression tests:** `Fleet asks before pausing or resuming the pipeline,
  and says resuming cancels a scheduled upgrade` (Cancel and Escape send
  nothing; the upgrade-hold text appears). The existing banner-sync test and
  the Fleet unit tests now accept the dialog.

### F3 (fixed): A refused Start CI run or Quarantine left its dialog open with nothing said

- **Where:** `ui-ng/src/app/ci/ci-page.component.ts`.
- **Reproduction:** Demo B. Open `/next/ci` → `Run workflow`, enter
  `acme/widgets` / `main` and press `Start CI run`. The server answers 403
  `grain CI is not enabled for acme/widgets: set its CI Mode to "Use Grain
  CI" ...`. The error was written to the page's `ci-action-error` banner,
  which sits behind the dialog's scrim, so the dialog stayed open and nothing
  said why. `submitQuarantine` had the same path.
- **Fix:** both dialogs show the refusal in their own `role="alert"`
  (`ci-dialog-error`), which clears when the dialog is opened again.
- **Regression test:** `CI's Run workflow dialog says why a run was refused`.

## Observations (not changed)

- **`/next/ci` "Inspect affected runs" on Demo A** opens
  `/next/ci/runs/9119` and reports `no CI run "9119" -- a run is remembered
  for a while and then forgotten`. The `-observability` fixture's shared fault
  names GitHub check-run ids (`pkg/demo/demo_githubci.go`) that `/api/ci/runs`
  never lists (`runs: []`). The page handles it gracefully: one 404, no
  re-polling. It is a demo-data mismatch, not a UI fault.
- **Classic Power** keeps a previous `walkthrough: refused` error on screen
  after a later successful `Reboot host` (`Reboot requested.` shows beside
  it). This is cosmetic and classic-only (Fleet has no reboot), so it was left
  alone while classic is retired.
- **`/next/settings/upgrade`** offers the post-upgrade check mode as an
  editable select. Classic shows it read-only with a link to General. This is
  a deliberate `/next/` difference rather than a bug.
- **`/next/system/top`** lands on `/next/fleet` with `Host top` collapsed, so
  you need one more click to reach Top.

## Walkthrough steps and outcomes

1. **Navigation (`/next/`, Demo A).** Rail `Fleet` → `/next/fleet`. The footer
   build stamp → `/next/settings/upgrade`. Each tab's `aria-controls` names a
   `role="tabpanel"` whose `aria-labelledby` is the tab. `?tab=` survives
   reload. Back and Forward step through the tabs. Legacy links:
   `/next/system` and `/next/system/{health,top}` → `/next/fleet`;
   `/next/system/{power,pipeline}` → `?tab=controls`;
   `/next/system/logs?q=dispatch` and `/next/admin/logs?q=x` → `?q=…&tab=logs`;
   `/next/sandboxes?x=1` → `/next/fleet?x=1`; `/next/caches` →
   `/next/settings/caches`; `/next/system/upgrade` → `/next/settings/upgrade`;
   `/next/metrics?repo=acme/widgets&window=30d` and
   `/next/system/metrics?f=repo:any:acme/widgets` → `/next/telemetry` with the
   query kept. No console errors or failed requests on any Fleet tab.
2. **Sandboxes (`/next/fleet`).** CPU 15% of 4 cores, vCPU `Within cap 0 / 4`,
   memory 4.0 / 16.0 GB, CI waits table (`#23 · 2h · my-branch`), persistent
   sandboxes `docs-preview` (running) and `grain-staging` (restarting,
   `exited with status 1`), empty live-sandbox state. Disk and daemon states:
   F1.
3. **Logs and Top.** `Daemon Logs` shows `300 / 300 lines` with 200/500/1000
   and Refresh. `Host top` opens the `top` snapshot (`systemd`,
   `grain-daemon`).
4. **Pipeline.** Pause and Resume, before the fix: F2. After it, Pause shows
   `Pause the pipeline?`; Escape and Cancel send nothing; accepting raises the
   global `Pipeline paused (manual Run now allowed)` banner, which survives
   reload and clears on a confirmed Resume. On a phone the dialog sits at
   `x=16 w=358` inside the 390 px viewport.
5. **Caches.** `/next/fleet?tab=controls` lists apt 412 (3.00 GB), npm, pypi,
   go, docker and playwright entries, and Actions `20 entries · 3.00 GB /
   20.00 GB`. `/next/settings/caches` shows `704 cached packages · 5.34 GiB`.
   No flush was confirmed (both flush controls ask first; Escape-dismiss was
   covered by the 2026-11-15 spec).
6. **Power.** `/next/fleet` `Restart daemon` is disabled where
   `restartEnabled` is false (spec-covered). Classic `/system/power` (Demo B,
   `/api/config` overlaid with `restartEnabled`/`rebootEnabled`/`shutdownEnabled`):
   each button asks through `confirm()` (`Restart grain? …`, `Reboot the host
   machine? …`, `Shut down the host machine? …`). Dismissing sent no POST. A
   stubbed 500 reported `walkthrough: refused`, and a stubbed 200 reported
   `Reboot requested. This UI is unreachable until the host machine comes back
   up.` (see Observations).
7. **Metrics (`/next/telemetry`).** 24h / 30d / 7d each ride in `?window=`;
   throughput, cycle-time stages, outcomes, token budgets (`Model quota
   advisories (4)`) and the alphabetical SPACE rollup with workstream links
   render without console errors.
8. **CI (`/next/ci`, Demo B).** Starting a run on a repo outside grain CI
   mode: F3. With `acme/widgets` switched to `ci_mode: grain`,
   `Start CI run` → `/next/ci/runs/fake-ci-1`. `Cancel run` asks
   `Cancel CI run? Stop push checks on acme/widgets (main)?` and the run
   concludes `Cancelled`. `Re-run all` follows to `/next/ci/runs/fake-ci-2`.
   Each row's accessible name names its own run (`Cancelled push checks on
   acme/widgets main (fake-ci-2)`). Rows are `role="button" tabindex="0"` and
   open on Enter. Reload keeps `/next/ci/runs/fake-ci-2`.
   `/next/ci/runs/nope-gone-123` asks once (one 404) and says the run was
   forgotten. The shared fault cluster (`CI / e2e › Run go test ./...`, 3 runs,
   3 branches) shows on Demos A and B.
9. **Settings → Upgrade (`/next/`).** Demo C: `In-VM self-upgrade is disabled
   on this deployment; …` with `Manage instance` (`target="_blank"
   rel="noopener noreferrer"`). Demo D: `Target channel / branch: latest`,
   post-upgrade check mode, drain toggle, `Start upgrade` (not pressed),
   `Database: sqlite`. Demo A: `Not available: … neither -upgrade-image nor
   -upgrade-src-dir`.
10. **Alerts bell (classic, Demo B).** `Alerts: 3 active`: `v2.4.0 is ready
    to promote` (approve / reject), `A merge has been stalled for 2 hours`
    (`open #1`), `Usage limit reached (antigravity) … until 4:55 PM UTC`
    (`resume now`, `open connection`). `see all on system → health` →
    `/system` (Health).
11. **Standing banners (classic, Demo B).** `install-notice-banner`
    (`-host-mode-install`) sits at `y=0, 1440×68` above the rail and names
    `claude` and `Migration docs`. Disconnected banner: after `SIGSTOP` on the
    demo process it rose within 23 s with `it accepted the connection but sent
    no answer within 10 seconds. Unreachable since 10:58:20 AM -- retrying
    automatically …` at `y=0, 1440×48`, and cleared without a reload within
    1 s of `SIGCONT`.
12. **Phone (390×844).** `/next/fleet`, `/next/fleet?tab=controls`,
    `/next/ci`, `/next/telemetry` and `/next/settings/upgrade` have 0 px
    horizontal overflow.

## Layout checks

- Fleet KPI cards at 1440 and 1024 px: the four cards (`meter-cpu`,
  `meter-vcpu`, `meter-memory`, `meter-disk`) share one row with equal heights
  (128 / 164 px) and none overflows. The `Not reported` disk card keeps the
  row's height. On a phone it stacks at 98 px, without the meta line.
- The `Uptime not reported` pill stays inside the Daemon Lifecycle header at
  every width.
- The `ci-dialog-error` alert lies inside the `ci-run-dialog` box at desktop
  (`430×56`) and phone (`268×96`), in both themes.
- `make ui-screenshots PAGES=/next/fleet?tab=controls,/next/ci` and
  targeted shots of the three changed states (light and dark, desktop and
  phone) were taken for the pull request. They are not committed.

## Stubbed boundaries

All stubs are Playwright `page.route` overrides in the walkthrough browser,
or in the named spec. The real server, frontend and gestures stayed in the
loop.

| Route | Why | Stub |
| --- | --- | --- |
| `GET /api/config` (classic Power) | Demo has no restart / reboot / shutdown command | Real answer overlaid with `restartEnabled`, `rebootEnabled`, `shutdownEnabled: true` |
| `POST /api/host/{restart,reboot,shutdown}` | Never touch the host | `500 {"error":"walkthrough: refused"}`, then `200 {"ok":true}` |
| `POST /api/ci/runs` (F3 screenshots and spec) | A deterministic refusal | `403 {"error":"grain CI is not enabled for acme/widgets: …"}`; the walkthrough itself hit the real 403 |
| `GET /api/sandboxes` (F1 spec) | The worker demo has no sandbox pool | Host with and without `disks`, `daemon` with and without `startedAt` |
| `GET /api/pipeline-pause` (F2 spec) | Exercise the scheduled-upgrade hold | `{pause: {byKind: "automation", byId: "upgrade-schedule", …}}` |
| Demo process `SIGSTOP` / `SIGCONT` | Silent-daemon disconnected banner | Throwaway Demo B process only |

The upgrade boundary was not intercepted: `Start upgrade` was never pressed.

## Tests run

- `cd ui-ng && npm run format:check && npm run lint`: clean.
- `npx vitest run src/app/fleet src/app/ci`: 13 + 4 passed.
- `npm run test:e2e -- e2e/next-deployment-operations.spec.js
  e2e/next-full-system.spec.js`: all passed (the new confirmation test also
  passed `--repeat-each=3`).
