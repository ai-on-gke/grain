# Deployment operations walkthrough — 2026-10-23

Task 276: CI, Sandboxes, Caches, System operations, deployment-wide
alerts and Metrics, walked in a real browser against a real `grain demo`.

## Commit and environment

- **Tested:** `2811ac7dd` (candidate tip, "Merge pull request #4535"), then
  the fix commit on `grain/049f1e09/task-276` rebuilt and re-walked.
- **Server:** `go build ./cmd/grain` after `make frontend` (the built UI is
  embedded), run as
  `GRAIN_E2E_ENDPOINTS=1 grain demo -addr 127.0.0.1:18420 -open=false -observability -access-code-file <tmp>`
  over the demo's own throwaway embedded store. A fresh store for the
  after-fix pass.
- **Browser:** Playwright `@playwright/test` 1.62 Chromium headless
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), signed in through
  `POST /api/auth/access-code` as the e2e fixture does, at 1440×900 and
  390×844, light and dark. Every page listener recorded console
  errors/warnings, page errors, failed requests and HTTP ≥ 400.
- **Sandbox:** Debian VM, 8 vCPU, 15 GiB, Go 1.26.9, Node 20.19. No
  `/home/debian/.gemini-api-key` was present, so screenshots were checked
  by reading the bounding boxes and page text rather than by Gemini.
- Walkthrough scripts lived outside the repository; no screenshots or
  browser artefacts are committed.

## Stubs (intercepted routes)

The real server, real frontend and real gestures stayed in the loop. The
observability demo already provides a real `ci.Service` + on-disk job
cache + `HostSandboxes` + sandbox-health adapter, fixed host stats/top,
demo proxy and Actions caches, a real secrets store, and power hooks that
*refuse* (so nothing here could reboot anything even unstubbed). Only
these were intercepted with `page.route`:

| Route | Why | What it drove |
|---|---|---|
| `POST /api/e2e/ci/finish` (demo's own hook, not a stub) | The demo workflow waits on a `.finish` file | Completing the held run so the second identical run is a real cache hit |
| `POST /api/host/reboot` → 200 `{}` | Show the success path the demo refuses | "Reboot requested…" info alert |
| `POST /api/host/shutdown` → `abort("connectionreset")` | A host going down drops the connection | Treated as accepted: "Shutdown requested…" |
| `POST /api/host/restart` → 500 | Failure path | Error toast shown, no "requested" alert |
| `GET /api/config` (real response, fields overlaid) | Only a live fault can raise these | `reconcilerDown`, `stateRepoSyncFailure`, `hostSandboxes`, `agentPause` (connection pause with `until`/`secondsRemaining`/`canLift`) — then removed to watch them clear on the next poll |
| `**/api/**` → `abort("connectionrefused")` for 12 s | Live-connection boundary | Disconnected banner, then its clearing once unrouted |
| `GET /api/metrics` → 500 `{error}` | Failure state | "The report could not be loaded (metrics store unavailable). Refresh to try again." |

## Steps and outcomes

### CI (`/ci`)
1. Rail → CI → **Start a run** (acme/widgets, `main`, Load workflows,
   Start run). The run overlay opened on `/ci/<id>` with **Cancel run**,
   job `observe`, its sandbox name and "(waiting for output...)". Escape
   closed it back to `/ci`; deep-linking the run URL and reloading
   reopened it on the running run.
2. **Active sandboxes (1)** listed the CI sandbox with repo, branch, job
   and running-for. System → Sandboxes meanwhile showed "204.0 MiB written
   by 1 live sandbox."
3. After `/api/e2e/ci/finish` the run read success, 12 s, "1 jobs, all
   green"; the job cache line went to 1 entry. A second identical run
   completed in 0 s as "1 jobs, all green · 1 cache hit", its job marked
   "cache hit"; the hit rate line moved to 25% of 4 lookups.
4. A third run on branch `cancel-me`: **Cancel run** → cancelled within
   ~1 s, and stayed cancelled across reload of its deep link.
5. `/ci/0000000000000000` (never existed / forgotten) and
   `/ci/not-a-hex-id!` both opened the overlay with
   `no CI run "<id>" -- a run is remembered for a while and then forgotten`
   over the intact listing.
6. Rows open from their result/branch link buttons (keyboard reachable);
   no retry control is exposed on a manual run (none expected).
7. **Bug 1** (fixed): the job cache line said `0 B of 256.0 MB` while
   System → Sandboxes said `0 B of 256.0 MiB` for the same cache.

### Sandboxes (`/sandboxes` → `/system/sandboxes`)
- `/sandboxes` redirects to System → Sandboxes. Host line (load of 4
  vCPU, utilisation, memory, swap), CPU/memory trends, "No disk figures
  available." (the demo has none), Storage with live-sandbox bytes, the
  proxy cache pointer, CI history (`0 B of 64.0 MiB`, retention line), CI
  job cache (`0 B of 256.0 MiB`, hit-rate note, **empty the job cache**),
  CI artifacts ("keeps no CI artifacts"). No console errors.

### Caches (`/caches` → `/system/caches`)
- Proxy cache: six ecosystems totalling 5.3 GiB; Actions cache: 20
  entries, 3.0 GiB of 20.0 GiB, per repo.
- Flushed the throwaway **Playwright browsers** and **Docker Hub layers**
  rows and the `acme/gadgets` Actions entry: each button disables while it
  runs, the row drops to 0, and the result persisted across reload. Per
  the panel's own comment, a flush deliberately asks for no confirmation
  (nothing is lost that a re-download does not put back).
- **Bug 2** (fixed): all eight per-row buttons were named just "Flush".

### System operations
- **Logs:** journal tail renders; Source, Search logs and Refresh live in
  the page header. **Top:** fixed demo snapshot, header + three rows.
- **Pipeline:** Pause with a reason → `window.confirm` ("Pause the
  pipeline? …"); dismissing left it running, accepting showed "The
  pipeline is paused since 2:57:25 PM … Reason: walkthrough maintenance
  window", surviving reload. Resume → confirm → running again, and
  survives reload.
- **Power:** each of Restart grain / Reboot host / Shut down host shows its
  own confirm; dismissing sent **0** POSTs. Accepting against the real
  demo showed its refusal ("grain demo does not reboot its host: nothing
  here is real"). With the stubs above: success alert, dropped connection
  treated as accepted, 500 surfaced as an error.
- **Admin/non-admin:** not applicable to this tree. The daemon is
  single-tenant (docs/access-code.md): whoever holds the access code is
  the owner, there is no role or "Admin mode" toggle in `ui/src`, and
  System's tabs are unconditional (`SYSTEM_TABS`).

### Deployment-wide alerts
- Since grain/task-4008 the standing banners are rows of the top bar's
  **Alerts** popover; the disconnected banner is the one standing banner
  left. There is no loopback-trust warning any more (loopback is not
  trusted; a stale comment in `alerts.js` still said otherwise — fixed).
- Baseline "Alerts: 1 active" (setup step left). Pausing the pipeline
  made it 2 with "Deployment paused"; **Resume now** in the panel confirmed
  and cleared it. A 300-character reason wrapped inside the 440 px panel
  (no horizontal overflow).
- Overlaid faults: "Alerts: 5 active, critical", grouped Deployment then
  Connection: "Settings are not being exported" (with Since and **Config
  repo**), "Reconcile loop stopped", "No isolation: runs execute on this
  host", the setup row (**Finish setup**), and "Usage limit reached" on
  connection Gemini 2.5 Pro "until 5:59:51 PM (about 3h 0m)" with **Resume
  now** / **Open connection**. Removing the overlay cleared them on the
  next poll without reload.
- Disconnected: "This deployment cannot be reached: the connection to it
  failed. Unreachable since … -- retrying automatically" at the top
  (48 px), cleared ~10 s after the route was released.
- **Bug 3** (fixed): on a 390 px phone the popover sat at x 16, width 374,
  i.e. flush against the right edge (right gutter 0).

### Metrics (`/metrics` → `/system/metrics`)
- Metrics is a System tab; the rail deliberately has no footer entry
  (Sidebar.test.jsx asserts its absence), so it was opened by URL and tab.
- Release notes (Weekly, Week 42 with counts and the "nothing written up"
  note; Versions list), then throughput, latency (with the `*` small-n
  footnote), capacity, backlog and the remaining sections.
- Window is a "Last 7 days" select; `?window=bogus` falls back to 7 days.
  `?repo=acme%2Fwidgets` is rewritten to the narrowing `?f=repo:any:acme/widgets`.
- A task reference opened `/tasks/2`; Back returned to Metrics.
- 500 from `/api/metrics` showed the inline failure line plus the error
  toast; Refresh retries.
- No throughput panel exists on the seeded `/boards` page in this demo,
  so the dashboard → Metrics path was not reachable here.

### Console / network
- Only recurring noise: `404 GET /api/repos/acme/widgets/github-branches`
  (the demo has no GitHub; the branch field falls back to free text) and
  `ERR_ABORTED GET /api/tasks` on navigation. No page errors.

## Bugs fixed in this task

| # | Where | Repro (before) | After | Coverage |
|---|---|---|---|---|
| 1 | `CIPage.jsx` kept its own `formatBytes` that stopped at "MB" | `/ci`: "Job cache: 1 entry · 697 B of 256.0 MB" vs Sandboxes "256.0 MiB"; 20 GiB would read "20480.0 MB" | "0 B of 256.0 MiB"; uses `PackageCachePanel`'s formatter | `CIPage.test.jsx` (MiB/KiB, 20 GiB) |
| 2 | `PackageCachePanel.jsx`, `CachesPage.jsx` | Accessible names: `Flush ×6, Flush the whole cache, Flush ×2, …` | "Flush apt packages" … "Flush the Actions cache of acme/widgets" | `CachesPage.test.jsx`, `PackageCachePanel.test.jsx`, `e2e/system-tabs.spec.js` |
| 3 | `AlertsButton.jsx` popover `maxWidth: calc(100vw - 16px)` vs MUI's 16 px margin threshold | 390×844: panel x 16, width 374, right gutter 0 | width 358, right gutter 16 | `e2e/alerts-panel-layout.spec.js` asserts a ≥ 15 px right gutter |

Unit tests run: CIPage, CachesPage, PackageCachePanel, AlertsButton,
alerts, SandboxHealthPage, SystemPage — 175 passed.

## Notes for the next run

- The task brief's "Admin mode", "loopback-trust warning", "banner strip"
  and "Metrics at the foot of the rail" no longer describe this tree (see
  above); the next brief could be updated.
- `make frontend` (vite) took ~10 minutes in this sandbox and left the
  guest unresponsive for a while; it also runs `npm ci --omit=dev`, which
  removes `@playwright/test` from `ui/node_modules` — install Playwright
  separately for ad-hoc scripts.
