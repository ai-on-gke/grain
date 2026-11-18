# Deployment Operations Walkthrough — 2026-11-17

Task 6594 covered the observability and operator-control screens: System / Fleet,
Logs, Sandboxes, Caches, Top, Pipeline, Metrics / Telemetry, Power,
Settings → Upgrade, the rail's CI page, the alerts bell and the standing
banners. They were walked in a real headless Chromium against real `grain demo`
servers. Following "Two UIs" in `PROMPT.md`, every step ran on `/next/` where
`ui-ng/src/app/shell/nav.ts` and `app.routes.ts` give it a page, and on
classic `/` only where they do not. Four bugs were found on `/next/` and fixed
in this branch. Each has a Playwright regression test in
`ui-ng/e2e/next-deployment-operations.spec.js`.

## Environment

- **Commit tested:** `b10fad991` (`main` at the start of the run, build stamp
  `v3.14.0-dev.134+b10fad9`). The fixes were re-walked on binaries built from
  this branch.
- **Build:** `make frontend grain`, then `cd ui-ng && npm run build` and
  `make grain` after each change. The classic `ui`, `ui-ng` and `ui-hub`
  bundles are embedded in `bin/grain`.
- **Servers:** `grain demo -addr 127.0.0.1:<port> -open=false
  -access-code-file <file>`, each with its own throwaway `HOME`/`TMPDIR`
  (embedded SQLite store). The browser signed in through
  `POST /api/auth/access-code` as `alice@example.com`.

  | Demo | Extra flags | Used for |
  | --- | --- | --- |
  | A | `-observability -staging` | `/next/fleet` (all tabs, Top, Logs, controls, caches), `/next/telemetry`, legacy redirects, disconnected banner (`SIGSTOP`), classic Health / Sandboxes / Caches / Power, Settings field links |
  | B | `-github-sim -fake-ci -base-branch-ci -notifications -agent-paused -host-mode-install -reconcile=1s` | Install notice, `/next/ci` start / cancel / re-run / forgotten run / fault cluster, pipeline pause, classic bell, Fleet without a sandbox backend |
  | C | `-disable-self-upgrade -management-url=https://console.example.com/deployments/grain-prod` | `/next/settings/upgrade` externally managed notice |
  | D | `-upgrade-image=docker.io/grainai/grain -upgrade-allowed-branches=main,latest` | `/next/settings/upgrade` target and check mode, classic Upgrade database panel |
  | E | `-unconfigured` | Setup alert (no git host, no model connection) |

- **Browser:** Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`,
  `timezoneId: UTC`). Widths: 1440×900, 1024×860 and 390×844 (phone, touch),
  in light and dark.
- **Gemini:** this sandbox has no `/home/debian/.gemini-api-key`, so `LOOK=1`
  was unavailable and the file reader here cannot display PNGs. Layout was
  checked with DOM bounding boxes instead (see "Layout checks").
  `make ui-screenshots PAGES=/next/fleet,/next/fleet?tab=controls,/next/settings/sandboxes`
  was run for the pull request. No screenshots or browser artifacts are
  committed.

## Which UI each step ran against

| Area | Ran against | Classic-only (no `/next/` page or control yet) |
| --- | --- | --- |
| System navigation, tabs, URLs, legacy links | `/next/fleet` (`Sandboxes`, `Hosts`, `Pauses & Controls`, `Daemon Logs`) | Classic 8-tab strip and phone section list |
| Health | classic `/system/health` | The whole tab: bell alert list, GitHub API budget / idle-poll backoff, daemon counters, `pendingRestart` / `unrunnable` field links, overcommit adjustments, host readiness note |
| Settings field links from Health | `/next/settings/<section>#<key>` (F4) | — |
| Sandboxes | `/next/fleet` | Overcommit controller adjustments, `"CI sandboxes (N)"` fold, per-sandbox trends |
| Caches | `/next/fleet?tab=controls`, `/next/settings/caches` | Per-ecosystem and per-repository Flush buttons |
| Logs, Top | `/next/fleet?tab=logs`, `Host top` (F3) | — |
| Pipeline | `/next/fleet?tab=controls` | Manual-mode toggle |
| Power | `/next/fleet` (`Restart daemon`) | `Reboot host`, `Shut down host` (classic `/system/power`) |
| Metrics | `/next/telemetry` | 90d window, dashboard throughput link |
| CI | `/next/ci`, `/next/ci/runs/:id` | `"Active sandboxes (N)"` table |
| Settings → Upgrade | `/next/settings/upgrade` | Searchable target picker with release notes, retracted / discarded banners, image chooser, read-only check mode linking to General, database migration commands |
| Alerts bell | classic `/` | The bell itself: `ui-ng` has no bell. `/next/` shows pauses and reconciler-down through its dispatch banner and config-repo sync failures on its own, but setup, host-sandboxes and red-base-branch repair alerts never appear there (follow-up proposed) |
| Disconnected banner | `/next/` | — |
| Host-mode install notice | `/next/` (F1), classic | — |

## Findings and fixes

### F1 (fixed): `/next/` never showed the host-mode install notice

- **Where:** `ui-ng/src/app/app.component.ts`, `ui-ng/src/styles.css`.
- **Reproduction:** start Demo B (`-host-mode-install`). Classic `/` shows
  `install-notice-banner` above the top bar: "This install is still host mode,
  which v2.3.0 no longer supports: … Cannot run until then: claude. Migration
  docs". `/next/inbox` on the same server showed nothing. `GET /api/config`
  carries `installNotice`, but nothing in `ui-ng/` read it. An operator on the
  new UI was never told that some agent CLIs cannot run.
- **Fix:** the shell renders the notice as a standing strip in the error
  colours, the same way as the disconnected banner (`.conn-banner`, so the
  shell becomes a column under it). It capitalises the message, names the
  affected CLIs, links `Migration docs` (`target="_blank"`, `http(s)` only)
  and has no close button, as classic does. It clears on the next config read
  once the install is migrated.
- **Walk after the fix (Demo B):** the banner sits at `y=0`, 1440×53 on
  desktop and 390×108 on a phone. The rail starts at `y=53`, the top bar at
  `y=53` / `y=108`, and there is 0 px horizontal overflow at 1440, 1024 and
  390 px, in light and dark.
- **Regression test:** `the host-mode install notice stands above the shell
  and clears once the install is migrated`.

### F2 (fixed): Fleet drew host CPU, memory and a green vCPU cap it was never given

- **Where:** `ui-ng/src/app/fleet/fleet-page.component.ts`.
- **Reproduction:** Demo B has no sandbox backend, so `GET /api/sandboxes`
  answers `{"enabled":false,"ciWaits":[…]}` with no `host` and no `budget`.
  `/next/fleet` showed `— cores` / `0%` / `Load 0.00 / 0.00 / 0.00`, Host
  Memory `0 MB` / `0 MB` / `Swap: 0 MB / 0 MB`, and a green `Within cap`
  `0 / — vCPU`. These are readings of a host nobody measured. This is the same
  class of bug as the 2026-11-16 disk card (F1 there).
- **Fix:** with no host, the CPU and memory cards say `Not reported`, without
  the pill or meta line. With neither a budget nor a host, the vCPU card says
  `Not reported` instead of `Within cap`. Reported values render as before.
- **Regression test:** `Fleet says host CPU, memory and vCPU it was not told
  are not reported`. It also checks the reported state: `4 cores`, `15%`,
  `16.0 GB`, `2 / 8 vCPU`, `Within cap`.

### F3 (fixed): `/system/top` landed on Fleet with Host top folded

- **Where:** `ui-ng/src/app/app.routes.ts`, `fleet-page.component.ts`.
- **Reproduction:** open `/next/system/top`. It redirected to `/next/fleet`
  with `Host top` collapsed (an observation in the 2026-11-16 report), so the
  classic Top address did not show Top.
- **Fix:** `/system/top` now redirects to `/next/fleet#top`, and Fleet opens
  and loads the snapshot when it is opened at `#top`. Plain `/next/fleet` still
  starts folded.
- **Regression test:** `the classic /system/top address opens Fleet's host
  top snapshot`.

### F4 (fixed): a Settings `#<key>` link did not reach the setting

- **Where:** `ui-ng/src/app/settings/settings-page.component.ts`.
- **Reproduction:** classic System → Health links each setting that is saved
  but waiting on a restart (`pendingRestart`), or stored but not runnable
  (`unrunnable`), as `/settings/<section>#<key>`: `Kubernetes namespace →
  /settings/sandboxes#k8sNamespace`, `GitHub host →
  /settings/capabilities#githubHost`, `Sandbox backend →
  /settings/sandboxes#sandboxBackend`. Classic scrolls to and focuses the
  field. `/next/settings/sandboxes#k8sNamespace` resolved only element ids and
  `card-<id>` test ids, so it stopped at the top of the section with
  `document.activeElement` = `BODY`.
- **Fix:** a fragment also finds the field by its key
  (`data-testid="input-<key>"`). A small `FIELD_ANCHORS` map covers
  `sandboxBackend` and `githubHost`, whose fields are marked differently. The
  page scrolls to the field and focuses its control. Card fragments
  (`#credentials`) still only scroll.
- **Regression test:** `a Settings #<key> link scrolls to and focuses that
  setting's field`.

## Observations (not changed)

- **No alerts bell on `/next/`.** Classic's bell (`ui/src/alerts.js`
  `collectAlerts`) lists the setup alert (`2 setup steps left`, `finish
  setup`), the host-sandboxes warning, the red-base-branch repair alerts,
  staged releases and stalled merges, each with its action. `/next/` covers
  the pauses and reconciler-down through its dispatch banner, config-repo
  sync failures (2026-11-16 fix) and notifications through Inbox → Blocked &
  alerts. On Demo E (`-unconfigured`), no `/next/` page mentions the missing
  git host or model connection. Porting the bell is new UI with its own design
  review, so it is proposed as a follow-up rather than done here.
- **`/next/ci` status filters and `/next/fleet` log search** narrow the view
  but are not written to the URL. A reload resets them. `?q=` is still read
  on arrival from a legacy `/system/logs?q=` link.
- **Classic Power** still shows a previous `walkthrough: refused` beside a
  later `Shutdown requested.` (noted on 2026-11-16; classic-only and
  cosmetic).
- **Classic Settings → Upgrade** aborted `GET /api/tasks` 11 times in 3.5 s
  (`net::ERR_ABORTED`, client-side aborts, no failed answers). These are
  classic polling restarts, harmless and classic-only.
- **Demo data:** the `-observability` and `-fake-ci` shared fault names
  GitHub check-run id `9119`, which `/api/ci/runs` does not list. `Inspect
  affected runs` opens `/next/ci/runs/9119`, which says the run was forgotten
  after one 404 (same as 2026-11-16).
- **Pending restart cannot be driven in a demo:** `pendingRestart` compares
  against the daemon's `RunningConfig`, which `grain demo` does not have. So
  saving `k8sNamespace` or `githubInsecureHttp` leaves it `null`. F4 was
  walked with `GET /api/settings` overlaid (see Stubs).
- **Upgrade loading spinner on `/next/`** was not observed. With
  `GET /api/upgrade` delayed 3 s, the form had already rendered at 800 ms. The
  first status read appears not to go through that exact path, so the stub
  did not hold it.

## Walkthrough steps and outcomes

1. **Navigation (`/next/`, Demo A).**
   - Rail `Fleet` → `/next/fleet`. The build stamp `grain
     v3.14.0-dev.134+b10fad9` → `/next/settings/upgrade` on Demos A, C and D.
   - Tabs `Sandboxes 2`, `Hosts 4`, `Pauses & Controls`, `Daemon Logs`: each
     tab's `aria-controls` names a panel whose `aria-labelledby` is the tab,
     and the tab rides in `?tab=`. Reload kept `?tab=logs`. Back went to
     `?tab=controls` and Forward to `?tab=logs`.
   - Legacy links:
     - `/next/system`, `/next/system/health` and `/next/system/sandboxes` →
       `/next/fleet`; `/next/system/top` → `/next/fleet#top` (F3).
     - `/next/system/pipeline` and `/next/system/power` →
       `/next/fleet?tab=controls`.
     - `/next/system/logs?q=dispatch` → `/next/fleet?q=dispatch&tab=logs`;
       `/next/admin/logs?q=x` → `/next/fleet?q=x&tab=logs`.
     - `/next/sandboxes?x=1` → `/next/fleet?x=1`; `/next/caches?y=2` →
       `/next/settings/caches?y=2`; `/next/system/caches` →
       `/next/settings/caches`.
     - `/next/system/metrics?repo=acme/widgets` →
       `/next/telemetry?repo=acme%2Fwidgets`;
       `/next/metrics?repo=acme%2Fwidgets&window=30d` → the same with
       `window=30d` kept.
     - `/next/system/upgrade` → `/next/settings/upgrade`.
   - No console errors or failed requests on any Fleet tab.
2. **Sandboxes (`/next/fleet`).**
   - Demo A: 4 cores / 15% / load 0.45 / 0.55 / 0.60, `Within cap 0 / 4
     vCPU`, memory 4.0 / 16.0 GB, disk `Not reported`, empty live-sandbox
     state, the CI waits table (`#23 · 2h · my-branch`), and persistent
     sandboxes `docs-preview` (running) and `grain-staging` (restarting,
     `exited with status 1`).
   - Demo B, before the fix: F2. After it, all three cards say `Not reported`.
   - Classic `/system/sandboxes` (Demo A): host line, `No disk figures
     available.`, storage breakdown, `overcommit: auto`, effective factors
     and dated adjustments (`CPU 2.50× → 2.75× … (10/24/2026, 9:20:00 AM)`).
     There is no `"CI sandboxes (N)"` fold because the demo has no CI
     sandboxes.
3. **Health (classic, Demos A and B).**
   - Install-notice alert with `sudo ./scripts/setup.sh` and the CLI list.
   - GitHub API panel: `140 of 5,000 calls left this hour · resets in 37m
     57s · spending 3,300/h`, a runs-out warning, idle-poll backoff `2 of 3
     tracked PRs backed off`, and the per-caller table.
   - The `sign-in verification (demo)` daemon counters, then the bell's
     alerts in full.
   - Field links (`GET /api/settings` overlaid): `Kubernetes namespace`,
     `GitHub host` and `Sandbox backend`. Clicking the first opened
     `/settings/sandboxes#k8sNamespace` with that input focused. The same
     links on `/next/`: F4.
4. **Logs and Top.**
   - `Daemon Logs` shows `300 / 300 lines` with 200 / 500 / 1000, Refresh and
     Copy. Typing `dispatch` narrows it to `50 / 300 lines`.
   - `Host top` and `/next/system/top` (after F3) show the `top` snapshot
     (`systemd`, `grain-daemon`).
5. **Pipeline (`/next/fleet?tab=controls`, Demo B).**
   - `Pause pipeline` asks `Pause the pipeline? Grain will stop starting new
     agent and CI runs. …`. Confirming raises `Pipeline paused (manual Run now
     allowed) — paused by hand`, which survives reload.
   - In the classic bell the alert reads `Paused — only tasks you Run now will
     start … Since 11:33:41 PM`, with `resume now`.
   - `Resume pipeline` asks `Resume the pipeline? …`. Confirming clears the
     alert, and the dispatch banner falls back to the model-quota pause.
6. **Caches.**
   - `/next/fleet?tab=controls` lists the proxy cache per ecosystem and
     `Actions Cache 20 entries · 3.00 GB / 20.00 GB`. `Flush package cache`
     asks `Flush package proxy cache? …`, and Escape dismisses it with no
     request sent.
   - Classic `/system/caches` names each Flush after what it flushes (`Flush
     npm packages`, `Flush the Actions cache of acme/widgets`, …). By design
     (`PackageCachePanel.jsx`) these ask no confirmation, since nothing is
     lost that a re-download does not put back.
   - `Flush npm packages` (throwaway demo entries) emptied the npm entry. It
     stayed `npm: 0 (0 B)` after a reload on both UIs, and
     `/next/settings/caches` then read `573 cached packages · 4.56 GiB`. A
     flushed ecosystem's button is disabled.
7. **Power.**
   - Classic `/system/power` (Demo A, `/api/config` overlaid with
     `restartEnabled`, `rebootEnabled` and `shutdownEnabled`): each of
     `Restart grain`, `Reboot host` and `Shut down host` asks through
     `confirm()` (`Shut down the host machine? Every task currently running is
     interrupted, …`). Dismissing sent no POST.
   - A stubbed 500 showed `walkthrough: refused`, and a stubbed 200 showed
     `Shutdown requested. grain stays down until somebody powers the machine
     back on.`.
   - `/next/fleet`'s `Restart daemon` is spec-covered (disabled where restart
     is not enabled; a refused restart says so).
8. **Metrics (`/next/telemetry`, Demo A).**
   - `24h`, `7d` and `30d`. The overview cards, throughput chart (Nov 10–16),
     cycle-time stages (`p50 40m · p90 1.4h (n=3)` …) and run outcomes
     (`succeeded 8 (88.9%)`) render.
   - Also: token usage with `Model quota advisories (4)`, per-model rows, and
     the alphabetical, non-ranked SPACE summary.
   - No console errors.
9. **CI (`/next/ci`, Demo B with `acme/widgets` set to `ci_mode: grain`).**
   - The row's accessible name names its own run (`Running pull_request
     checks on acme/widgets grain-merge-queue/… @ abcdef0
     (demo-recheck-1)`).
   - `Run workflow` → `Start CI run` → `/next/ci/runs/fake-ci-1`. `Cancel
     run` asks `Cancel CI run? Stop push checks on acme/widgets (main)?`;
     Escape sent nothing, and confirming concluded it `Cancelled`.
   - Reload kept `/next/ci/runs/fake-ci-1`. `Re-run all` followed to
     `/next/ci/runs/fake-ci-2`, and Enter on a focused row opens it.
   - `/next/ci/runs/nope-gone-123` made one 404 and says the run is
     forgotten.
   - The status filters (`Running` / `Failed` / `Errored` / `Passed` /
     `Cancelled` / `All`) narrow the list. The shared fault cluster (`CI / e2e
     › Run go test ./...`, 3 runs, 3 branches) shows.
   - Re-run failed is spec-covered (`Re-run failed follows to the run it
     starts`).
10. **Settings → Upgrade.**
    - Demo C: `In-VM self-upgrade is disabled on this deployment; …` with
      `Manage instance`.
    - Demo D: `Target channel / branch: latest`, `Post-upgrade check mode:
      File & queue check`, the drain toggle, `Start upgrade` (never pressed;
      `POST /api/upgrade/start*` was intercepted anyway and saw 0 calls), and
      `Database: sqlite`. The running version
      `v3.14.0-dev.134+b10fad9` comes from `/api/config` because the demo's
      `GET /api/upgrade` has no version.
    - Demo A: `Not available: … neither -upgrade-image nor
      -upgrade-src-dir`.
    - Classic Upgrade (Demo D): `This deployment runs on SQLite.` with the
      offline-only `systemctl stop` / `setup.sh --db-driver postgres` /
      `grain db check` commands.
11. **Alerts bell (classic).**
    - Demo B: `Alerts: 4 active`. They are the pipeline pause; `v2.4.0 is
      ready to promote` (approve / reject); `A merge has been stalled for 2
      hours` (`open #1`); and `Usage limit reached (antigravity)` … `until
      11/18/2026, 5:21 AM UTC (about 5h 47m)` (the date shows because it is
      not today), with `resume now` and `open connection`. The last row is
      `see all on system → health`.
    - Demo E: `2 setup steps left — Still to do: connect a git host, add a
      model connection.` with `finish setup`.
    - `/next/` shows the agent pause as `Model quota exceeded — Gemini 2.5
      Pro development paused · resets Nov 18, 5:21 AM UTC (in 5h 56m)`.
12. **Standing banners (`/next/`).**
    - Install notice: F1.
    - Disconnected banner (Demo A, `SIGSTOP` on the demo process): it rose
      after 13 s with `it accepted the connection but sent no answer within 10
      seconds. Unreachable since 11:36:19 PM — retrying automatically; …` at
      `y=0, 1440×53`. It cleared without a reload 30 ms after `SIGCONT`,
      staying on `/next/fleet?tab=logs`.
    - The failed-connection case is spec-covered.
13. **Phone (390×844, light and dark).** `/next/fleet` with the install
    banner has 0 px horizontal overflow. The banner wraps to 108 px and the
    top bar sits below it.

## Layout checks

- Install banner: `y=0`, full width, at 1440 / 1024 / 390 px. The rail
  (`y=53.4`) and top bar (`y=53.4`, phone `y=107.9`) start at its bottom edge,
  and the bottom bar ends inside the viewport. These are asserted in the F1
  test (desktop) and were measured at the other widths.
- Fleet KPI cards with `Not reported` keep the row's height. Nothing overflows
  at 1440, 1024 or 390 px.
- The F4 test asserts that the focused field is in the viewport.

## Stubbed boundaries

All stubs are Playwright `page.route` overrides in the walkthrough browser or
the named spec. The real server, frontend and gestures stayed in the loop.

| Route | Why | Stub |
| --- | --- | --- |
| `GET /api/config` (classic Power) | The demo has no restart / reboot / shutdown command | Real answer overlaid with `restartEnabled`, `rebootEnabled`, `shutdownEnabled: true` |
| `POST /api/host/{restart,reboot,shutdown}` | Never touch the host | `500 {"error":"walkthrough: refused"}`, then `200 {"ok":true}` |
| `GET /api/settings` (Health field links, F4 walk) | `grain demo` has no `RunningConfig`, so nothing is ever pending | Real answer overlaid with `pendingRestart: ["k8sNamespace","githubHost"]`, `unrunnable: [{key:"sandboxBackend",…}]` |
| `GET /api/upgrade` (loading state) | Hold status open to look for a spinner | Delayed 3 s, then passed through |
| `POST /api/upgrade/start*` | Never upgrade | 500; it was never called |
| `GET /api/config` (F1 spec only) | A deterministic notice that can be cleared | Real answer plus `installNotice`, then without it. The walk itself used Demo B's real `-host-mode-install` notice |
| `GET /api/sandboxes` (F2 spec only) | A host-less answer and a measured one | `{enabled:false}`, then a host plus budget. The walk itself used Demo B's real answer |
| `GET /api/host/top` (F3 spec only) | A deterministic snapshot | Two `top` lines. The walk itself used Demo A's real snapshot |
| Demo process `SIGSTOP` / `SIGCONT` | Silent-daemon disconnected banner | Throwaway Demo A process only |

## Tests run

- `cd ui-ng && npm run format:check && npm run lint`: clean.
- `npx vitest run src/app/fleet src/app/settings src/app/app.component.spec.ts`:
  6 files, 87 tests passed.
- `npx playwright test e2e/next-deployment-operations.spec.js`: 20 passed,
  including the four new tests.
- `npx playwright test e2e/next-full-system.spec.js`: 5 passed (every
  destination in desktop / phone × light / dark, plus contrast).
