# Daily Exploratory UI Testing & Defect Discovery — 2026-10-26

## 1. Environment & Browser Setup

- **Repository / Branch**: `bwsalmon/grain` (`grain/049f1e09/task-1143`)
- **Base Commit / Build Version**: `248e30231` (`v2.5.0-dev.258+248e302`, built `2026-10-26 12:58Z`)
- **Targets Exercised**:
  1. `grain demo` with `-github-sim -merge-queue-states -merge-rules -secrets-store -qualification -review-questions -observability -sensitive-hold -mechanical-port`
  2. `grain demo` with `-github-sim -merge-queue-states -merge-rules -secrets-store -qualification -review-questions -fake-ci -base-branch-ci -sensitive-hold -mechanical-port`
  3. `grain demo` with `-external-config-repo https://github.com/acme/grain-config.git -external-config-branch main`
  4. `grain daemon` with `-k8s-granule-sandboxes -k8s-workspace /home/debian -k8s-git-proxy-host 127.0.0.1 -sandbox-port-base=0`
- **Browsers & Viewports (Playwright `1.62.1`)**:
  - **Chromium (`chromium-1234`)**:
    - Desktop Wide (`1920x1080`, light)
    - Desktop Standard (`1440x900`, dark)
    - Tablet (`768x1024`, light)
    - Mobile (`375x812`, light)
    - Mobile (`375x812`, dark)
  - **WebKit (`webkit-2336`)**:
    - Desktop Standard (`1440x900`, light)
    - Mobile (`375x812`, light)
- **Diagnostic Listeners Attached**:
  - `page.on('pageerror')` for uncaught exceptions and unhandled promise rejections
  - `page.on('console')` for browser console errors and warnings
  - `page.on('response')` for HTTP `>= 400` status codes
  - Client-side React error boundary (`Something went wrong`) and layout overflow (`document.documentElement.scrollWidth > document.documentElement.clientWidth`) checks on every route and viewport

---

## 2. Multi-Browser Route & Viewport Sweep Summary

Across **7 passes** × **117 unique routes** (**819 total route loads**):

| Pass | Browser | Viewport | Color Scheme | Routes Loaded | Page Errors | Console Errors | Unexpected HTTP >=400 | Horizontal Overflows | Error Boundaries |
| :--- | :--- | :--- | :--- | ---: | ---: | ---: | ---: | ---: | ---: |
| 1 | Chromium | `1920x1080` | `light` | 117 | 0 | 0 | 0 | 0 | 0 |
| 2 | Chromium | `1440x900` | `dark` | 117 | 0 | 0 | 0 | 0 | 0 |
| 3 | Chromium | `768x1024` | `light` | 117 | 0 | 0 | 0 | 0 | 0 |
| 4 | Chromium | `375x812` | `light` | 117 | 0 | 0 | 0 | 0 | 0 |
| 5 | Chromium | `375x812` | `dark` | 117 | 0 | 0 | 0 | 0 | 0 |
| 6 | WebKit | `1440x900` | `light` | 117 | 0 | 0 | 0 | 0 | 0 |
| 7 | WebKit | `375x812` | `light` | 117 | 0 | 0 | 0 | 0 | 0 |
| **Total** | **2 Engines** | **4 Breakpoints** | **Light & Dark** | **819** | **0** | **0** | **0** | **0** | **0** |

> Note: Only the 2 intentionally missing route probes (`/tasks/99999` → `GET /api/tasks/99999` and `/ci/run-999` → `GET /api/ci/runs/run-999`) returned expected `404` responses (`14` across the 7 passes), and both rendered clean not-found states without triggering a crash or React error boundary.

**Route families covered in the 117-route matrix**:
- Top-level navigation: `[Tasks](/)`, `[New task](/new)`, `[Inbox](/inbox)`, `[Sessions](/sessions)`, `[Boards](/boards)` (`/boards/board-1`), `[Processes](/processes)` (schedules, templates, suites, runs, and `?f=kind:any:*` filters), `[CI](/ci)` (runs, jobs, steps), `[Repos](/repos)` (`/repos/acme/widgets`, `/repos/acme/infra`), `[Metrics](/metrics)`, `[Activity](/activity)`
- All 7 repository settings tabs: `/repos/acme/widgets/settings/{overview,ci,merge-queue,agents,branches,releases,advanced}`
- All 17 `[Settings](/settings)` sections + aliases: `/settings/{general,access,config-repo,models,agents,memory,tasks,repositories,git,capabilities,credentials,executors,engine,ci,sandboxes,caches,preferences}` and `/settings/{capability-servers,legacy-capabilities}`
- All 9 `[System](/system)` tabs: `/system/{health,logs,sandboxes,caches,top,pipeline,metrics,upgrade,power}`
- All 17 legacy `/admin/*` and `/system/*` redirects (e.g., `/admin/workflow` → `/settings/tasks`, `/admin/integrations` → `/settings/credentials`, `/admin/state` → `/settings/config-repo`, `/sandboxes` → `/system/sandboxes`, `/caches` → `/system/caches`, `/memory` → `/settings/memory`)
- Deep-linked task detail overlays (`/tasks/1` through `/tasks/20`) and malformed URL query string resilience (`/?f=invalid:::filter&sort=nonexistent:asc&q=%E0%A4%A`, `/processes?f=kind:any:nonexistent&sort=bad`, `/ci?f=status:any:unknown&q=***`)

---

## 3. Feature & Workflow Coverage Verification

### 3.1 Single-Tenant Access Gate, Models & Agent Connections
- **Access Code Gate (`AccessGate.jsx`)**:
  - Verified unauthenticated requests to `/api/tasks` return `401 Unauthorized`.
  - Submitted an invalid code (`WRONG-CODE`) on the sign-in screen and verified the alert *"That access code was not accepted."*
  - Verified case-insensitive access code normalization by signing in with `accessCode.toLowerCase()`, which succeeded (`200 OK`) and transitioned into `[Tasks](/)`.
- **Models & Connections (`[Settings → Models](/settings/models)`, `[Settings → Agents](/settings/agents)`)**:
  - Verified default model configuration (`gemini-2.5-pro`), effort levels, max turns, and agent connection status.
  - Verified `ModelSelect` search-by-typing and `Specific connection` pinning in `[New task](/new)` and `BulkEditOverlay`.

### 3.2 Remote Executor Management & Sandbox Configuration
- **Remote Executors (`[Settings → Executors](/settings/executors)`)**:
  - Issued a new executor token (`staging-pool-eu-1`) via `POST /api/executors/tokens` (`201 Created`, token prefix `gxt_`), verified the one-time token display banner, and revoked the token via `POST /api/executors/tokens/exec-tok-1/revoke` (`200 OK`).
- **Sandbox Configuration (`[Settings → Sandboxes](/settings/sandboxes)`)**:
  - **Custom OCI Image (`643751ac1`, `ed682ad96`)**: Verified that a multi-reference input (`"ghcr.io/a/b:1 ghcr.io/c/d:2"`) is rejected (`400 Bad Request`) with an inline validation alert, while a valid OCI reference (`"ghcr.io/acme/sandbox:v1"`) saves cleanly (`200 OK`).
  - **Auto Overcommit Bounds (`448711317`)**: Verified that entering `0.1` into `Auto CPU minimum` is refused by name with `"Couldn't save Auto CPU minimum: Auto CPU minimum must be at least 0.25; 0.1 was not saved."`
  - **Live Daemon Kubernetes Priority Classes**: On `grain daemon -k8s-granule-sandboxes`, verified that `[Settings → Sandboxes](/settings/sandboxes)` renders `Priority class for interactive sessions` (`grain-interactive`), `Priority class for agent runs` (`grain-agent`), and `Priority class for CI jobs` (`grain-ci`), and saves edits (`k8sPriorityClassAgent: "grain-agent-high"`) on blur.

### 3.3 Schedules, Templates & Suites Automation (`[Processes](/processes)`)
- **Schedules (`ScheduleOverlay`, `31e8c766d`, `538c480fd`)**:
  - Created an `everyNMinutes: 15` schedule (`sched-3`, `201 Created`) and verified that `everyNMinutes: 0` is rejected (`400 Bad Request`).
  - Verified schedule cadence options (daily, weekly, monthly, every N hours, every N minutes), overlap policies, pause/resume, and immediate trigger actions.
- **Templates & Suites (`TemplateOverlay`, `SuiteOverlay`, `f883f12fe`)**:
  - Opened `+ New` → `Template`, `Suite`, and `Schedule` dialogs on `[Processes](/processes)`.
  - Verified the new `Interactive (agent can chat with you)` checkbox on `TemplateOverlay` (`f883f12fe`), template field editor (`TemplateFieldsEditor`), and suite multi-template chaining and run modes (`count`, `until-clean`).

### 3.4 Merge Queue, CI Status Boards & Live Observability
- **Observability & Live Sandboxes (`[System → Sandboxes](/system/sandboxes)`, `[CI](/ci)`, `8b3a54f87`, `71f1454ef`)**:
  - Triggered a live CI run (`POST /api/e2e/ci/start` → `202`) and agent sandbox (`POST /api/e2e/sandbox/start` → `1-identity-check`).
  - Verified active CI and agent sandboxes render in `[System → Sandboxes](/system/sandboxes)` (`1-identity-check`, `ci-run-4-...`) and `[CI](/ci)`, and completed the run via `POST /api/e2e/ci/finish` (`200 OK`).
- **Merge Queue & Base Branch CI (`[Repos → acme/widgets](/repos/acme/widgets)`, `[CI](/ci)`)**:
  - Inspected merge queue batching states, CI run/job/step detail views (`/ci/run-1`, `/ci/run-2/jobs/lint/steps/0`), and `[Alerts](/ci)` panel with `-base-branch-ci`.

### 3.5 Core Task Lifecycle, Sessions, Inbox & Bulk Operations
- **Task Pause & Resume (`20ead6fe5`, `7a983c9ac`)**:
  - Opened `[Task #2](/tasks/2)` (`queued`), clicked `Pause`, verified the task transitioned to `paused` and the action button switched to `Resume`, then clicked `Resume` to return it to `queued`.
- **Force Merge on Running Task (`e0fdcc3f4`)**:
  - Opened `[Task #8](/tasks/8)` (`running` with open pull request `acme/widgets#104`) and verified `Force merge` is offered in the Actions menu while the run is still in progress.
- **Sessions (`[Sessions](/sessions)`, `5bd1c9b40`, `996d7e581`, `6032bf747`)**:
  - Opened `+ New session` on `[Sessions](/sessions)` (`1440x900`) and verified `Session name` and `Create session` share a single row (`Δy = 0px`), `Pin to sidebar` is present, and `Pause` is not shown before a session has started.
- **New Task Capabilities & Timezone Header (`[New task](/new)`, `896d1893e`, `59298d414`)**:
  - Opened `Advanced options` → `Capabilities` on `[New task](/new)` and verified that unticking the unready `Gemini key` capability keeps its row visible in the open menu so it can be re-ticked without closing the menu.
  - Filed a task (`#29`) from a browser context with `timezoneId: 'America/Chicago'` and verified the `POST /api/tasks` request carried `X-Grain-Time-Zone: America/Chicago`.
- **Bulk Operations & Filter URL Sync (`BulkEditOverlay`, `NarrowingBar`)**:
  - Selected multiple tasks on `[Tasks](/)` and verified `BulkEditOverlay` rows (`Repo`, `Base branch`, `Auto-merge`, `Model`, `Specific connection`, `Agent framework`, `Review`, `Prompt extension`).
  - Verified URL query param sync (`/?q=pagination&f=state:any:queued|running&sort=title:desc`) across browser Back/Forward/Reload.
- **Inbox (`[Inbox](/inbox)`)**:
  - Verified `Answer a question` and `Acknowledge all` flows with `-review-questions`.

### 3.6 Settings, Capabilities, External Config Repo & Offline Resilience
- **Settings Validation Recovery & Responsive Breakpoint (`[Settings → Engine](/settings/engine)`, `e1d84c076`)**:
  - Entered `-1` into `Max worker agents` (`400 Bad Request`), then restored the saved value `4` and verified the `"Couldn't save"` error banner cleared immediately (confirming the fix for the 2026-10-23 defect).
  - Resized `[Settings → Engine](/settings/engine)` from desktop (`1440x900`) to mobile (`375x812`) and verified the open section remained mounted (`e1d84c076`).
- **Capabilities & ConfigMaps (`[Settings → Capabilities](/settings/capabilities)`, `0cd7f2918`, `e991ba904`)**:
  - Created a `ConfigMap` (`app-config` with `settings.toml` in `/home/debian/.config-map/app-config`) on the `config-map` server tab, saved configuration, and verified zero horizontal overflow on mobile (`375x812`).
- **External Config Repo Refusal (`-external-config-repo`, `33e7a1229`)**:
  - Verified `[data-testid="external-config-repo-banner"]` and read-only inputs on `[Settings → Engine](/settings/engine)`, and verified that submitting a template write on `[Processes](/processes)` opens `[data-testid="config-refusal-dialog"]` with the rehearsed `git diff` patch against `https://github.com/acme/grain-config.git`.
- **Offline Network Interruption Recovery**:
  - Toggled `context.setOffline(true)` in Playwright, verified `[data-testid="disconnected-banner"]` appeared, and verified automatic dismissal upon `context.setOffline(false)`.

---

## 4. Defects Discovered & Tasks Proposed

Two distinct defects were identified, isolated to minimal deterministic reproductions, verified against `main` and active remote branches, and filed via `propose_task`:

### Defect 1: `demo: -observability overwrites grain_config with DefaultConfig(), wiping GitConnections and MCPServers`
- **Location**: `pkg/demo/demo_observability.go:147-165` (called after `store.PutConfig(ctx, storedCfg)` in `pkg/demo/demo.go:334-343`)
- **Symptom / Impact**:
  1. `configureObservabilityDemo` constructs a fresh `stored := model.DefaultConfig()` instead of reading `store.GetConfig(ctx)`, overwriting `GitConnections`, `MCPServers`, `SandboxBackend`, `GitHubHost`, and `GitHubInsecureHTTP` in `grain_config`.
  2. Navigating to `[New task](/new)` or `[Repos](/repos)` on `grain demo -observability` shows `[data-testid="no-git-connection"]` (*"This deployment has no git connection yet, so its tasks cannot clone or push a repository..."*), hides the repository picker on `[New task](/new)`, and leaves `Create task` disabled unless the user selects `No repo`.
  3. `[Settings → Capabilities](/settings/capabilities)` (`GET /api/capability-servers`) is missing both demo capability servers (`https://capabilities.demo.grain.example/cloud` and `https://capabilities.demo.grain.example/gcp`), and saving any setting in `[Settings](/settings)` triggers `cfg.Wake()` (`pkg/demo/demo.go:326-332`), which calls `capabilityServers.Update(stored.MCPServers)` with `nil` and detaches both demo capability servers from the live registry (`GET /api/capability-servers/methods` drops from `3` methods to `0`).
- **Proposed Task**: Filed via `propose_task` targeting `main`.

### Defect 2: `UI: Settings → Capabilities shows built-in servers as unknown and polls /api/capability-servers every 2s indefinitely in grain demo`
- **Location**: `pkg/demo/demo_capability_server.go:171-215`, `pkg/ui/capability_servers.go:164-195`, `ui/src/components/CapabilityServersPanel.jsx:703-712`
- **Symptom / Impact**:
  1. `ListCapabilityServers` (`pkg/ui/capability_servers.go:164-191`) calls `intree.EnsureBuiltins(configured)` on read, but only synthesizes a fallback `Status` when `sc.Builtin == "config-map"`, while `configureCapabilityServerDemo` (`pkg/demo/demo_capability_server.go:171-215`) never calls `intree.EnsureBuiltins` before passing `stored.MCPServers` to `reg.Update`.
  2. On `grain demo`, opening `[Settings → Capabilities](/settings/capabilities)` displays 6 built-in capability servers (`workspace`, `gchat`, `gcp`, `git`, `host-script`, `model-proxy`) with `status: null`, rendering status chip **`unknown`** with no `configSchema` (hiding the `Configure` button on configurable built-ins like `host-script` and `gchat`).
  3. Because `CapabilityServersPanel.jsx:705-712` treats `!s.status` on any non-disabled server as `pending`, `[Settings → Capabilities](/settings/capabilities)` polls `GET /api/capability-servers` and `invalidateCapabilityMethods()` in an infinite 2-second loop (`4` requests in `6.5s`) for as long as the page remains open.
- **Proposed Task**: Filed via `propose_task` targeting `main`.
