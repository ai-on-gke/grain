# Deployment Operations Walkthrough — 2026-11-20

Task 7550 walked the observability and operator-control screens in real
headless Chromium against real `grain demo` servers. It covered System / Fleet
(Sandboxes, Hosts, Pauses & Controls, Daemon Logs, Host top), Telemetry,
Settings → Caches, Settings → Upgrade, the rail's CI page, the alerts and the
standing banners. It follows "Two UIs" in `PROMPT.md`: every step ran on
`/next/` where `ui-ng/src/app/shell/nav.ts` and `app.routes.ts` give it a page,
and on classic `/` only where they do not. It builds on the 2026-11-18 report.
That report's fixes still hold: the unmigrated-install notice on `/next/`,
Fleet's unreported disk and uptime, the pipeline confirmations, and refusals in
the CI dialogs.

This run found two new bugs on `/next/` and fixed both in this branch. One of
them had a classic counterpart, which is fixed too. Each fix has a regression
test.

## Environment

- **Commit tested:** `2c44ffa0b` (`main` at the start of the run). The build
  stamp read `v0.0.0-20261120080048-2c44ffa0b8af`. The fixes were re-walked on
  binaries built from this branch.
- **Build:** `make frontend`, then `go build -o ~/grain ./cmd/grain`. After each
  `ui-ng` change: `npm run build` in `ui-ng/`, then `go build` again.
- **Servers:** `grain demo -addr 127.0.0.1:0 -open=false -access-code-file
  <file>`. Each server had its own throwaway `HOME` and `TMPDIR` (embedded
  SQLite store). The browser signed in through `POST /api/auth/access-code`
  (`{code, name: "alice@example.com"}`).

  | Demo | Extra flags | Used for |
  | --- | --- | --- |
  | A | `-observability -staging` | `/next/fleet` (all tabs), legacy redirects, build stamp, `/next/telemetry`, `/next/settings/caches`, `/next/settings/upgrade`, phone widths |
  | B | `-github-sim -fake-ci -base-branch-ci -notifications -agent-paused -host-mode-install -reconcile=1s` | Inbox alerts, dispatch banner, `/next/ci` (cancel), pipeline pause/resume, Power, install notice, disconnected banner, classic Health, bell, Power and CI |
  | C | `-upgrade-image=docker.io/grainai/grain -upgrade-allowed-branches=main,latest -unconfigured` | `/next/settings/upgrade` image mode |
  | D | `-disable-self-upgrade -management-url=https://console.example.com/deployments/grain-prod` | `/next/settings/upgrade` externally managed |

- **Browser:** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`,
  `timezoneId: UTC`), at 1440×900 and 390×844.
- **Gemini:** `/home/debian/.gemini-api-key` is absent in this sandbox, so no
  Gemini-backed review ran. Layout was checked through the DOM (text, bounding
  boxes, scroll width). No screenshots or browser artifacts are committed.

## Which UI each step ran against

| Area | Ran against | No `/next/` page or control yet (classic only) |
| --- | --- | --- |
| System navigation, tabs, URLs, legacy links | `/next/fleet` (`Sandboxes`, `Hosts`, `Pauses & Controls`, `Daemon Logs`) | Classic 8-tab strip and phone section list |
| Health | classic `/system/health` | The whole tab: alert list, config-repo state, GitHub API budget and idle-poll backoff, daemon counters, `pendingRestart` / `unrunnable` field links, host readiness note |
| Sandboxes | `/next/fleet` | Overcommit controller adjustments, `"CI sandboxes (N)"` fold, per-sandbox trends |
| Caches | `/next/fleet?tab=controls`, `/next/settings/caches` | Per-repository Actions flush |
| Logs, Top | `/next/fleet?tab=logs`, `Host top` | — |
| Pipeline | `/next/fleet?tab=controls` | Manual-mode toggle |
| Power | `/next/fleet?tab=controls` (`Restart daemon`), classic `/system/power` | `Reboot host`, `Shut down host` |
| Metrics | `/next/telemetry` | Dashboard throughput-panel link |
| CI | `/next/ci`, `/next/ci/runs/:id`, classic `/ci` | `"Active sandboxes (N)"` table |
| Settings → Upgrade | `/next/settings/upgrade` | Searchable target picker and release notes, discarded-RC / retracted banners, `Image source` card, image chooser, read-only check mode linking to General |
| Alerts bell | classic `/` | The bell itself. `/next/` routes alerts into the Inbox's `Blocked & alerts` lane and the global dispatch banner |
| Disconnected banner, install notice | `/next/` | — |

## Findings and fixes

### F1 (fixed): Fleet drew unreported host CPU and memory as a measured, idle machine

- **Where:** `ui-ng/src/app/fleet/fleet-page.component.ts`.
- **Reproduction:** start Demo B, which has no sandbox backend.
  `GET /api/sandboxes` answers `{"enabled": false, "ciWaits": [...], ...}`
  with no `host` object. Open `/next/fleet`. The host strip read `Host CPU &
  Load — cores 0% Load 1/5/15m: 0.00 / 0.00 / 0.00` and `Host Memory 0 MB 0 MB
  Swap: 0 MB / 0 MB`, beside `Primary Disk Not reported`. A daemon that cannot
  read the host (`hostError`) drew the same zeros, and the error was never
  shown. Classic System → Sandboxes leaves the host section out in that case,
  and shows `Host stats unavailable: <hostError>` when the daemon sends one.
- **Fix:** with no `host`, the CPU card says `Not reported`
  (`meter-cpu-none`) and drops the cores pill and load line. With no
  `memoryTotalMB`, the memory card says `Not reported` (`meter-memory-none`)
  and drops the total pill and swap line. A `hostError` is shown as `Host stats
  unavailable: …` (`meter-host-error`). Real figures render as before.
- **Re-walk:** Demo B on the fixed build reads `Host CPU & Load Not reported ·
  vCPU Allocation Within cap 0 / — vCPU · Host Memory Not reported · Primary
  Disk Not reported`. Demo A still reads `4 cores 15%` and `16.0 GB 4.0 GB`.
- **Regression:** `Fleet says host CPU and memory were not reported rather
  than drawing zeros` in `ui-ng/e2e/next-deployment-operations.spec.js`. It
  checks three answers: `enabled: false`, `hostError`, and real figures.

### F2 (fixed): an ended CI run's unconcluded job still read "Running", with its clock ticking

- **Where:** `ui-ng/src/app/ci/ci-page.component.ts`. Classic counterpart:
  `ui/src/components/CIPage.jsx` (`jobSummary`).
- **Reproduction:** start Demo B and open `/next/ci`. The `-fake-ci` run
  `demo-recheck-1` is Running. Press `Cancel run` and confirm in `Cancel CI
  run? Stop pull_request checks on acme/widgets (…)?`. The run became
  `Cancelled` (`Cancel note: Cancelled from CI workspace`; `Cancelled 1` in
  the filter). Its job still read `Running test 5m 18s`, and the job's clock
  kept counting past the run's own `Duration 5m 16s`. Reloading did not change
  it. Classic `/ci` listed the same run as `cancelled … 1 job: 1 running`.
  The same state follows any run that ends without its jobs recording a
  conclusion, such as a daemon restart cutting a run off. The demo stub's
  `Cancel` sets only the run's conclusion.
- **Fix (`/next/`):** a job or step with no conclusion, under a run that is
  no longer active, is badged `Not finished` (neutral). Its duration ends at
  the run's `finishedAt`.
- **Fix (classic):** `jobSummary` counts running or pending jobs of a run that
  has a conclusion as `unfinished`: `1 job: 1 unfinished`.
- **Re-walk:** after Cancel run, `/next/ci` reads `Jobs & steps (1) Not
  finished test 6m 1s`, matching `Duration 6m 1s`, and reload keeps it.
- **Regression:** `an ended CI run's unfinished jobs say Not finished and stop
  their clock` (`ui-ng/e2e/next-deployment-operations.spec.js`), plus a
  `jobSummary` case in `ui/src/components/CIPage.test.jsx`.

### Observations (not changed)

- On Demo B, the Fleet `Sandboxes` tab count reads `0` and the live table is
  empty. On Demo A it reads `2` while the live table says `No active task
  sandboxes right now`: the count includes the two persistent sandboxes listed
  further down the tab. That is consistent, but easy to misread.
- On Demo C, the drain toggle on `/next/settings/upgrade` has no visible
  text. It is a `role="switch"` with `aria-label="Drain running tasks before
  restarting"`, so it is named correctly.
- The Health tab, the bell, the Reboot/Shut down controls and the
  Upgrade-picker features listed above as classic-only still have no `/next/`
  counterpart, as on 2026-11-18. They are tracked by the UI migration, not
  filed again here.

## Steps and outcomes

1. **Rail and build stamp (`/next/`, Demo A).** The rail lists Inbox,
   Workstreams, Tasks, Agents, Sessions, Processes, CI, Repos, Telemetry,
   Context and Fleet, then Settings. Fleet is System's `/next/` page. The build
   stamp (`grain v0.0.0-20261120080048-2c44ffa0b8af`) opens
   `/next/settings/upgrade`.
2. **Legacy addresses (Demo A).** Each address landed where expected:
   - `/next/system/{health,sandboxes,top}` → `/next/fleet`
   - `/next/system/logs` and `/next/admin/logs` → `/next/fleet?tab=logs`
   - `/next/system/{pipeline,power}` → `/next/fleet?tab=controls`
   - `/next/system/caches` and `/next/caches` → `/next/settings/caches`
   - `/next/system/upgrade` → `/next/settings/upgrade`
   - `/next/sandboxes` → `/next/fleet`
   - `/next/system/metrics?repo=acme%2Fwidgets` →
     `/next/telemetry?repo=acme%2Fwidgets`
   - `/next/metrics?repo=acme%2Fwidgets&window=7d` keeps both parameters.

   On classic: `/system/upgrade` → `/settings/upgrade`, `/admin/logs` →
   `/system/logs`, `/sandboxes` → `/system/sandboxes`, `/caches` →
   `/system/caches`, and `/metrics?window=7d` → `/system/metrics?window=7d`.
   Tab history (Back/Forward) and the tab–tabpanel tie are spec-covered and
   passed.
3. **Fleet → Sandboxes (Demo A).**
   - Host strip: `4 cores 15%`, load `0.45 / 0.55 / 0.60`, `Within cap 0 / 4
     vCPU`, memory `16.0 GB / 4.0 GB` with swap `0 MB / 4.0 GB`, and disk `Not
     reported`.
   - The live table shows its empty state.
   - Waiting on CI lists `#23 · 2h · my-branch · acme/widgets ·
     grain/task-23 · grain`.
   - Persistent sandboxes: `docs-preview` (running, `restarts: 0`, Start /
     Restart / Rebuild / Stop / Logs) and `grain-staging` (restarting,
     `restarts: 4`, `exited with status 1`).
4. **Fleet → Hosts.** `build-runner` (offline), `cloudtop-alice` (offline),
   `local` (connected, co-located, `login:antigravity`, `login:claude`) and
   `old-laptop` (revoked), with Rotate / Rename / Revoke / Delete. No host was
   changed.
5. **Logs and Top.** `Daemon Logs` shows `300 / 300 lines`, with Refresh, Copy,
   the `daemon` source picker, 200/500/1000 tail sizes and the filter box.
   `Host top` sits in the page header.
6. **Pipeline (Demo B, real server, no stub).**
   - `Pause pipeline` opens `Pause the pipeline? Grain will stop starting new
     agent and CI runs. Runs already in flight will continue.` Escape closed
     it with nothing paused.
   - Confirming showed `Paused · Reason: paused by hand alice@example.com
     (access code)`. The shell banner `Pipeline paused (manual Run now
     allowed) — paused by hand` appeared on every page, including the Inbox,
     and the top bar read `2 running · Pipeline paused`.
   - `Resume pipeline` asked `Resume the pipeline? Grain will start
     dispatching queued agent and CI runs again.`. Confirming returned the gate
     to `Dispatching` and cleared the banner.
7. **Caches.** On Demo A, `?tab=controls` and `/next/settings/caches` show the
   package proxy (apt 412 at 3.00 GB, npm, pypi, go, docker, playwright;
   `704 cached packages · 5.34 GiB`) and the Actions cache (`20 entries ·
   3.00 GB / 20.00 GB`). On Demo B both caches are off and both Flush buttons
   are disabled. The flush confirmation is spec-covered (`Settings -> Caches …
   asks before flushing when on`) and passed. No real entries were flushed.
8. **Power.**
   - **`/next/` (Demo B, `/api/config` overlaid with `restartEnabled: true`).**
     `Restart daemon` opens `Restart grain daemon? Interrupts in-flight agent
     runs …`. Cancel sent no POST. With `POST /api/host/restart` stubbed to
     500 (`systemctl refused: unit is masked`), one POST was sent and the
     refusal was reported. With the stub answering 202, a second POST was
     sent.
   - **Classic `/system/power`.** `Restart grain`, `Reboot host` and `Shut down
     host` each showed their confirm text (`Restart grain? Every task …`,
     `Reboot the host machine? …`, `Shut down the host machine? …`).
     Dismissing sent nothing. Accepting sent exactly one POST per press to the
     stubbed `/api/host/{restart,reboot,shutdown}`. The host was never touched.
9. **Telemetry (Demo A).**
   - Every section rendered: KPI strip, Throughput & Merges, Cycle-Time
     Stages, Run Outcomes & Agent Templates, Top Tools & CI Queue Causes,
     Quality checks, Token Usage/Cost/Budgets with 4 quota advisories, Team
     Collaboration, Active Workstream Directions, and Contributors & Actors
     (A–Z).
   - `24h` and `30d` write `?window=`, which survives reload.
   - Workstream links open `/next/task/10`, `/next/task/9` and
     `/next/task/20`.
   - No console errors.
10. **CI (Demo B).**
    - The page shows the shared fault cluster (`CI / e2e › Run go test ./... ·
      3 runs · 3 branches`, with `Inspect affected runs →`), the filters (All /
      Running / Failed / Errored / Passed / Cancelled), Repo and Branch
      pickers, and providers.
    - The open run shows Cancel run / Re-run all / Quarantine test, and its
      jobs and artifacts.
    - `/next/ci/runs/does-not-exist-42` asked once (one 404) and said `no CI
      run "does-not-exist-42"`.
    - Cancel run asked, then cancelled, and the result persisted through
      reload (F2).
    - Per-row names, Re-run failed following to the new run, and the Run
      workflow refusal are spec-covered and passed.
11. **Settings → Upgrade.**
    - Demo A: `2c44ffa`, `Not available: … neither -upgrade-image nor
      -upgrade-src-dir …`, and `Database: sqlite`.
    - Demo C: `Target channel / branch: latest`, `Post-upgrade check mode: File
      & queue check`, the drain toggle and `Start upgrade`, which was not
      pressed.
    - Demo D: `In-VM self-upgrade is disabled on this deployment; …` with
      `Manage instance` → `https://console.example.com/deployments/grain-prod`
      (`target="_blank"`).
12. **Alerts (Demo B).**
    - On `/next/`, the dispatch banner reads `Model quota exceeded — Gemini 2.5
      Pro development paused · resets 6:43 PM UTC (in 6h) — …`, with
      `Telemetry` and `Controls`. The Inbox's `Blocked & alerts` lane lists
      `The webkit e2e job looks flaky` (acknowledged) and `A merge has been
      stalled for 2 hours`.
    - The classic bell reads `Alerts: 3 active`: `v2.4.0 is ready to promote`
      (approve / reject), `A merge has been stalled for 2 hours` (`open #1`)
      and `Usage limit reached (antigravity) … until 6:49 PM UTC` (`resume
      now`, `open connection`). `see all on system → health` opens `/system`.
    - Classic Health shows the same three alerts, the full install notice,
      `GitHub API: 1,603 of 5,000 calls left this hour · resets in 38m 0s ·
      spending 3,300/h …` with the per-caller table, `Idle-poll backoff: 2 of
      3 tracked PRs backed off …`, and the `sign-in verification (demo)`
      counters.
13. **Standing banners (`/next/`, Demo B).**
    - The install notice (`install-banner`) stands above the rail on every
      page.
    - With every `/api/**` request aborted, the disconnected banner rose
      within one poll: `This deployment cannot be reached: the connection to
      it failed. Unreachable since 12:44:35 PM — retrying automatically; …`.
      Once requests were let through again, it cleared without a reload.
    - The held-connection case is spec-covered (`the disconnected banner
      stands above the shell …`) and passed.
14. **Phone (390×844, Demo A).** `/next/fleet`, `?tab=controls`, `?tab=logs`,
    `/next/telemetry`, `/next/settings/upgrade`, `/next/settings/caches` and
    `/next/ci` each have 0 px horizontal overflow and no console errors.

Across all of these, the only console errors and failed requests were the ones
caused on purpose: the 401 on `/next/` before sign-in, the 404 for the
forgotten run, the stubbed 500s, aborted `/api/**` calls while disconnected,
and `/api/tasks` polls aborted by navigation.

## Stubbed boundaries

All stubs are Playwright `page.route` overrides in the walkthrough browser. The
real server answered everything else.

| Route | Why | Answer |
| --- | --- | --- |
| `GET /api/config` (Demo B, Power only) | The demo does not enable host power actions | Real answer plus `restartEnabled`, `rebootEnabled`, `shutdownEnabled: true` |
| `POST /api/host/restart`, `/reboot`, `/shutdown` | Must never reach the host | `500 text/plain` (refusal), then `202 {ok: true}` |
| `/api/**` aborted for 12s | Raise the disconnected banner without stopping the daemon | Network abort, then unrouted |
| `GET /api/sandboxes`, `/api/ci/runs*` (new regression tests only) | Deterministic host/no-host and cancelled-run answers | Fixed JSON |

Pipeline pause and resume, and CI Cancel run, ran against the real demo server.
CI uses the demo's `-fake-ci` stub (`pkg/demo/demo_fakeci.go`), which is part
of the server.

## Tests run

- `npx playwright test e2e/next-deployment-operations.spec.js --project=desktop`
  in `ui-ng/`: 17 tests passed before the CI fix. The 3 new or affected tests
  were re-run after it and passed.
- `npx vitest run src/app/fleet src/app/ci` in `ui-ng/`: 13 + 4 passed.
- `npx vitest run src/components/CIPage.test.jsx` in `ui/`: 63 passed.
