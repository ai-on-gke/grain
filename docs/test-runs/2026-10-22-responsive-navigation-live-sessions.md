# Responsive Navigation & Live-Session UI Walkthrough — 2026-10-22

## 1. Environment & Commit Tested

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/049f1e09/task-182`
- **Base Commit Tested**: `747f8245502f719f8fca60f125e9bc545128819e` (`docs(release): add release notes for v2.1.0`)
- **Platform**: Linux `6.1.0-45-cloud-amd64` (Debian 12 Bookworm sandbox VM, x86_64)
- **Toolchains**: Go `go1.26.9 linux/amd64`, Node.js `v20.19.0`, npm `10.8.2`
- **Browsers**:
  - Playwright `1.62.1` Chromium (`151.0.7922.34`)
  - Playwright `1.62.1` WebKit (`26.5`)
- **Server Under Test**: Real `grain demo` binary (`./cmd/grain`) serving the production Vite frontend bundle (`pkg/ui/static`) over a throwaway embedded SQLite store (`-addr 127.0.0.1:0 -open=false -access-code-file ...`), authenticated via `POST /api/auth/access-code`.
- **Gemini Key Check**: `/home/debian/.gemini-api-key` was absent in this sandbox; `grain demo` does not launch real external LLM processes, so external/live boundaries (standing alerts, offline disconnection, 503 session-manager startup, and live SSE/JSONL log stream chunks) were exercised through targeted Playwright network interceptions documented in Section 2 while keeping the real Go server, SQLite store, React state tree, and browser layout in the loop.

---

## 2. Documented Stubs & Boundary Interceptions

Only external or live system boundaries not active in a local `grain demo` process were intercepted in Playwright:

1. **Standing Deployment Alerts (`GET /api/config`)**:
   - Augmented the real `/api/config` response (`route.fetch()` + JSON merge) with simultaneous standing alerts to exercise `collectAlerts(config)` and the `AlertsButton` (`[data-testid="alerts-button"]`) popover across desktop, tablet, and phone:
     - `reconcilerDown: true` (`critical`: "Reconcile loop stopped")
     - `stateRepoSyncFailure: { repo: "acme/state", since: "2026-10-22T09:00:00Z", error: "push rejected" }` (`critical`: "Settings are not being exported")
     - `hostSandboxes: true` (`warning`: "No isolation: runs execute on this host")
     - `pipelinePause: { paused: true, reason: "Scheduled maintenance window" }` (`warning`: "Deployment paused")
     - `agentPause: { paused: true, pauses: [{ connection: "conn-1", connectionName: "Primary Claude", model: "claude-sonnet-4-6", reason: "Quota limit reached until 12:00 UTC", secondsRemaining: 3600, canLift: true }] }` (`warning`: "Usage limit reached (claude-sonnet-4-6)")
     - `deployment: { targetRepos: [] }` (`info`: setup steps remaining)
2. **Offline / Daemon Unreachable State (`context.setOffline(true)` & 503 Polls)**:
   - Used `context.setOffline(true)` and temporary `503 Service Unavailable` route responses on `/api/tasks` and `/api/console-sessions` to exercise `[data-testid="disconnected-banner"]` (`"Cannot reach server — showing last known state"` + `Retry now`), error snackbars (`role="alert"`), composer draft retention when sending while offline, and automatic recovery upon `context.setOffline(false)` without a page reload.
3. **Session Manager Startup Failure (`GET /api/console-sessions` & `POST /api/console-sessions` -> HTTP 503)**:
   - Intercepted `/api/console-sessions` with `503 {"error": "session manager starting"}` on initial load of `/session/director` and during session creation to verify that `App.jsx` does not falsely claim the session was deleted while `sessionsLoaded` is still `false`, displays the error snackbar cleanly, and recovers automatically once the next poll succeeds.
4. **Live Assistant Stream (`GET /api/tasks/<id>/logs/stream`)**:
   - Intercepted `/api/tasks/<id>/logs/stream` to emit realistic newline-delimited JSON `assistant_text` events followed by a settled comment on `/api/tasks/<id>`, verifying live rendering of `.task-chat-bubble-streaming` and smooth transition into the settled conversation thread.

All other interactions — navigation, URL routing (`paths.js`), task creation/editing/batch selection, board creation and visualization switching, standing session creation/reply/cancel/restart/capability updates/deletion, process filtering and overlays, settings mutations, and localStorage persistence — ran unstubbed against the real `grain demo` server.

---

## 3. Architectural Context Verified

Since the 2026-10-15 walkthrough, three major shell changes landed and were verified end-to-end:

1. **Single-Tenant Reset (`docs/single-tenant-reset.md`)**:
   - Multi-tenant teams, the team switcher (`[data-testid="team-selector"]`), team-scoped rail box (`[data-testid="team-section"]`), team home page, Team settings, User settings, Admin mode switch, `"All teams"`, `"New team"`, and `[data-testid="user-tile"]` have been retired.
   - All routes are root-scoped (`/inbox`, `/`, `/boards`, `/sessions`, `/session/<id>`, `/processes`, `/ci`, `/repos`, `/settings/<section>`, `/system/<tab>`).
2. **Merged `PageHeader` Rail (`components/PageHeader.jsx`)**:
   - On desktop and tablet (`width >= 700px`), the legacy global `TopBar` (`[data-testid="top-bar"]`) is gone. Each page's header is drawn inside `[data-testid="page-header"]` at the top of the main column, showing the section glyph, ` / `-separated breadcrumb (`[data-testid="page-header-title"]`, with the final segment `[data-testid="top-bar-page"]` carrying `aria-current="page"`), portalled `PageHeaderActions` (`[data-testid="page-header-actions"]`) with an automatic overflow menu (`[data-testid="page-header-more"]`, `aria-label="More page actions"`), and `GlobalButtons` (`[data-testid="page-header-globals"]`: `[data-testid="alerts-button"]`, `[data-testid="top-bar-system"]`, `[data-testid="top-bar-settings"]`) at the far right.
   - On phones (`width < 700px`), `PhoneNav` renders a static top `AppBar` with `Open navigation`, the animated `GrainMark`, `grain` wordmark, optional `environmentName` chip, `[data-testid="alerts-button"]`, `[data-testid="phone-settings"]`, and `New task` (`+`). Opening the drawer slides out `<Drawer aria-label="Navigation">` containing `Sidebar` with `pinFooter={true}` (`System` row + `[data-testid="build-stamp"]` pinned at the bottom).
3. **Processes, Settings & System Consolidation (`docs/design/settings-panel.md`)**:
   - Schedules, Templates, Suites, and Triggers are unified under `/processes` (`Processes <count>` in the rail), with legacy `/schedules`, `/templates`, and `/suites` redirecting via `replaceState` to `/processes?kind=schedule|template|suite`.
   - Memory moved to `/settings/memory` (redirected from `/memory`).
   - Sandboxes, Caches, and Metrics moved to `/system/sandboxes`, `/system/caches`, and `/system/metrics` (redirected from `/sandboxes`, `/caches`, and `/metrics`), while `/admin` redirects to `/settings/general` and `/admin/<status-tab>` redirects to `/system/<status-tab>`.

---

## 4. Walkthrough Matrix & Step-by-Step Outcomes

### 4.1 Viewport & Browser Matrix

Every primary route and interactive surface was exercised in **both Chromium 151.0.7922.34 and WebKit 26.5** across:

| Layout Class | Viewport(s) | Shell Variant | Overflow (`scrollWidth <= clientWidth`) | Console / Page / 5xx Errors |
| :--- | :--- | :--- | :--- | :--- |
| **Desktop** | `1440×900`, `1280×500` (short) | Persistent left `Sidebar` (`232px`) + `PageHeader` rail | `1440 / 1440` (`PASS`) | `0 / 0 / 0` (`PASS`) |
| **Tablet (iPad)** | `820×1180` (portrait), `1180×820` (landscape) | Persistent left `Sidebar` (`232px`) + `PageHeader` rail (`More page actions` overflow when needed) | `820 / 820`, `1180 / 1180` (`PASS`) | `0 / 0 / 0` (`PASS`) |
| **Phone** | `390×844` (portrait), `844×390` (landscape), `320×568` (narrow) | Top `PhoneNav` bar + modal `<Drawer aria-label="Navigation">` (`pinFooter`) | `390 / 390`, `844 / 844`, `320 / 320` (`PASS`) | `0 / 0 / 0` (`PASS`) |

### 4.2 Header Rail, Sidebar, Create Menu & Pinned Navigation

- **`PageHeader` Rail (`1440×900` & `820×1180`)**:
  - Verified `[data-testid="page-header"]` renders exactly once per view, with `[data-testid="page-header-actions"]` preceding `[data-testid="page-header-globals"]` in DOM/Tab order so keyboard users reach page-specific controls before global buttons.
  - Verified `[data-testid="alerts-button"]`, `[data-testid="top-bar-system"]`, and `[data-testid="top-bar-settings"]` sit at the right-hand end of the header rail with zero overlap or clipping.
  - Verified `grain`'s own labels render lowercase (`tasks`, `inbox`, `boards`, `sessions`, `processes`, `ci`, `repositories`, `settings / general`, `system / health`), while user-named entities preserve their case in `[data-testid="top-bar-page"]` (`boards / Walkthrough Kanban`, `sessions / Director`, `repositories / acme/widgets`).
- **`PhoneNav` & Drawer (`390×844` & `320×568`)**:
  - Verified `Open navigation`, `GrainMark`, `grain` heading, `[data-testid="alerts-button"]`, `[data-testid="phone-settings"]`, and `New task` (`+`) fit on a single `52px` toolbar row with zero horizontal overflow even at `320px`.
  - Verified opening the drawer displays `Sidebar` with `System` and `[data-testid="build-stamp"]` pinned at the footer, locks background scrolling, traps keyboard focus, closes on `Escape` or destination selection (both mouse click and keyboard `Enter`/`Space`), and restores focus to `Open navigation`.
- **Create Menu (`+ New task` & `Create options`)**:
  - Clicked `+ New task` to open `NewTaskOverlay` (`/new`).
  - Clicked `Create options` split-button dropdown and verified `New task`, `New session`, and `New board` menu items (and confirmed no `New team` item exists).
- **Pinned Items & `CustomizePinnedOverlay`**:
  - Clicked `Customize pinned items` (`Customize`) in the rail to open `<Dialog aria-label="Customize pinned navigation">`.
  - Selected `Type: Session`, `Item: Director`, clicked `Pin item`, verified `Pinned items (2)` listed `Inbox` and `Director`, clicked `Save`, and verified `Director` appeared under `Pinned` in the rail with `svg[data-glyph="director"]`.
  - Reloaded the page (`page.reload()`) and opened the phone drawer at `390×844` to confirm persistence via `localStorage` (`grain:pinned-nav`).

### 4.3 Primary Views, Settings Sections, System Tabs & Legacy Redirects

- **Inbox (`/inbox`)**:
  - Verified `Inbox 4` count badge in rail, split triage list + detail pane on desktop/tablet, and single-column list -> detail drill-in with `← Back` button on phone (`390×844`).
- **Tasks (`/`, `/tasks/<id>`, `/new`)**:
  - Verified state filter rows (`Proposed`, `Queued`, `Running`, `Awaiting reply`, `Awaiting submit`, `Queued for merge`, `Deferred`, `Closed`, `Blocked`) in the rail update `?state=<state>` in the URL and support browser Back/Forward.
  - Verified `BatchActionsBar` sticky footer clearance on phone and tablet so the last `.task-row` is never covered when scrolled to the bottom.
  - Opened task detail pane (`/tasks/1`, `/tasks/4`, `/tasks/7`), verified full-viewport modal sizing on phone (`width: 390px`) and constrained overlay sizing on desktop/tablet, and verified long unbroken identifiers wrap cleanly at `320×568`.
- **Boards (`/boards` & `/boards/<id>`)**:
  - Exercised all 5 board visualizations on desktop, tablet, and phone:
    1. **Kanban (`kind: "kanban"`)**: multi-column cards, horizontal column scroll container, column editor overlay.
    2. **Timeline / Gantt (`kind: "gantt"`)**: `Range` and `Lanes` pickers wrap cleanly on `320px` and `390px` phones; `.gantt-now` indicator introduces zero sideways scroll (`scrollWidth <= clientWidth`).
    3. **Dashboard / Panels (`kind: "panels"`)**: panel grid and empty-state prompt.
    4. **Report (`kind: "report"`)**: throughput/latency/outcome sections and window selector (`?window=30d`).
    5. **Task Tree (`kind: "tree"`)**: hierarchical parent/child dependency tree rendering.
- **Processes (`/processes` & Overlays)**:
  - Verified unified `Processes` list with kind filters (`schedule`, `template`, `suite`, `trigger`), search input, and sort selector.
  - Opened Schedule, Template, and Suite detail overlays (`/processes/schedules/<id>`, `/processes/templates/<id>`, `/processes/suites/<id>`), verified unsaved-changes confirmation (`DISCARD_PROMPT`) on browser Back when a schedule form is dirty, and verified legacy redirects `/schedules`, `/templates`, and `/suites`.
- **CI (`/ci` & `/ci/<runId>`)**:
  - Verified CI run list, branch/outcome filter bars, cache summary line, and run detail overlay.
- **Repositories (`/repos`, `/repos/acme/widgets`, `/releases`, `/compare/...`)**:
  - Verified repository index list, repo detail view (`/repos/acme/widgets`), state counts wrapping inside the column on tablet (`820×1180`), `Releases` subpage (`/repos/acme/widgets/releases`), settings tabs (`/repos/acme/widgets/settings/ci`), and pull request diff comparison (`/repos/acme/widgets/compare/main...<head>`).
- **Settings (`/settings/<section>` — all 16 sections)**:
  - Exercised all 16 sections across all 6 groups:
    - *Deployment*: `general`, `access`, `config-repo`
    - *Agents*: `models`, `agents`, `memory` (plus `/memory` -> `/settings/memory` redirect)
    - *Work*: `tasks`, `repositories`
    - *Connections*: `git`, `credentials`, `executors`
    - *Infrastructure*: `engine`, `ci`, `sandboxes`, `caches`
    - *This browser*: `preferences` (Appearance `Auto` / `Light` / `Dark`, `Time zone`, `Task timeline`, `Pinned navigation`)
  - Verified left section nav with `/` quick-filter on desktop, `<select data-testid="settings-section-select">` on narrow tablet, and section list -> section drill-in with `[data-testid="settings-back"]` (`aria-label="All settings"`) on phone (`390×844`).
- **System (`/system/<tab>` — all 9 tabs)**:
  - Exercised all 9 tabs: `health`, `logs`, `sandboxes`, `caches`, `top`, `pipeline`, `metrics` (including expanding release notes in `grain versions` with `aria-expanded="true"`), `upgrade`, and `power`.
  - Verified legacy redirects `/sandboxes` -> `/system/sandboxes`, `/caches` -> `/system/caches`, `/metrics` -> `/system/metrics`, `/admin` -> `/settings/general`, and `/admin/sandboxes` -> `/system/sandboxes`.

### 4.4 Alerts Popover, Offline Banner & Error Snackbar Stacking

- Verified `AlertsButton` (`[data-testid="alerts-button"]`) with 6 simultaneous alerts (`reconciler-down`, `state-sync-failing`, `host-sandboxes`, `pipeline-pause`, `agent-pause`, `setup`) displays badge count `6` sorted by severity (`critical` -> `warning` -> `info`), opens the `Alerts` dialog popover on click, and closes on `Escape` with focus restored to `[data-testid="alerts-button"]`.
- Verified `DisconnectedBanner` (`[data-testid="disconnected-banner"]`) appears once when offline (`context.setOffline(true)`) or when polls return `503`, stacks cleanly above `.app-shell` while error snackbars (`role="alert"`) float without obscuring the session composer's `Send`, `Attach files`, `Edit <title> capabilities`, or `Delete session` buttons at `1440×900`, `820×1180`, and `390×844`, and clears automatically when connectivity returns (`context.setOffline(false)`).

### 4.5 Standing Chat Sessions & Task-Bound Interactive Consoles

- **Sessions List (`/sessions`) & Session View (`/session/<id>`)**:
  - Opened `/sessions`, searched sessions via `Search sessions…`, opened `Director` (`/session/director`), and verified the `PageHeader` rail breadcrumb displays `sessions / Director` on desktop/tablet and `<h2>Director</h2>` on phone.
  - Verified deep-linking to `/tasks/<id>` for a session's backing task redirects via `replaceState` to `/session/<sessionId>` without exposing task-only controls.
  - Verified deep-linking to a deleted or non-existent session (`/session/deleted-ghost-session`) falls back to `SessionsPage` with the warning alert `"There is no session “deleted-ghost-session” here. It may have been deleted; pick another below."`
- **Creating, Messaging, Streaming, Stopping, Restarting & Deleting Sessions**:
  - Created a new standing session (`Triage Bot`) via `+ New session` (`NewSessionDialog`), verified navigation to `/session/triage-bot`, and verified returning to `/sessions` immediately displays the `"Your turn"` status chip.
  - Sent a message containing Markdown headings, a GFM table, a fenced `sh` code block, and a long unbroken token; verified horizontal overflow is contained inside `<pre>` and table scroll wrappers (`scrollWidth <= clientWidth` on document).
  - Exercised `Stop` (`POST /api/console-sessions/<id>/cancel`) to cancel a queued/running session turn and verified immediate transition back to `Awaiting reply` (`waiting for your reply`).
  - Exercised `Start a fresh chat` (`POST /api/console-sessions/<id>/restart`) with `"Close this chat and start a fresh one"` (`carryOver: false`) and `"Start fresh with summary"` (`carryOver: true`).
  - Exercised `Edit Director capabilities` (`#director-capabilities-content`), toggled capabilities, network access, Google Chat space, and `File its tasks approved`.
  - Exercised the `WorkTree` disclosure (`Provenance · Work from task 12: 0 of 4 done`) on `/session/director`.
  - Exercised `Delete session`: dismissed the confirmation dialog first (`window.confirm` -> Cancel) and confirmed the session remained open; accepted the confirmation dialog (`window.confirm` -> OK) and confirmed `DELETE /api/console-sessions/<id>` removed the session and navigated back to `/session` (`Director`).
- **Task-Bound Interactive Console (`/tasks/<id>` with `interactive: true`)**:
  - Created an interactive task (`POST /api/tasks` with `interactive: true`), opened `/tasks/<id>` in `DetailOverlay` at `390×844` (portrait) and `844×390` (landscape), verified `<TaskChat>` rendering, message sending, and zero horizontal overflow across orientation flips.

---

## 5. Bugs Found & Fixed

### Bug 1: Standing session `PageHeader` rail breadcrumb displayed raw session `id` slug instead of session `title`

- **Symptom**:
  On desktop and tablet (`width >= 700px`), opening any standing session (`/session/director` or `/session/night-shift-2`) rendered the raw session slug in the `PageHeader` rail (`sessions / director` or `sessions / night-shift-2`) instead of the session's human-readable `title` (`sessions / Director` or `sessions / Night shift`). Because `ConsoleSessionPage.jsx` hides its own `<h2>{session.title}</h2>` with `.visually-hidden` when `inRail` is true (`!isPhone`), the session's actual title was hidden from screen on desktop and tabletwhenever the slug differed from the title (for example, when a duplicate title was disambiguated as `night-shift-2`).
- **Root Cause**:
  In `ui/src/App.jsx` line 2954, `viewTitle` computed the session breadcrumb as:
  ```js
  : view === "session" && openSession
    ? `sessions / ${openSession.name || openSession.id}`
  ```
  However, `ConsoleSessionSetting` returned by `GET /api/console-sessions` (`pkg/ui/console_sessions.go`) has `{ id, title, description, taskId, capabilities, ... }` and no `name` property (`openSession.name` is always `undefined`), so the expression always fell back to `openSession.id`.
- **Deterministic Reproduction**:
  1. Start `grain demo` and open `/sessions` at `1440×900`.
  2. Click `+ New session`, enter `Release Coordinator`, and click `Create`.
  3. Observe `[data-testid="page-header-title"]` at `/session/release-coordinator`: before the fix, its text was `"sessions/release-coordinator"` and `[data-testid="top-bar-page"]` was `"release-coordinator"` instead of `"sessions/Release Coordinator"` and `"Release Coordinator"`.
- **Fix**:
  Updated `ui/src/App.jsx` line 2954 to read `openSession.title || openSession.name || openSession.id`.
- **Regression Coverage**:
  - Added unit test `"titles a standing session's header rail by its title rather than its id slug"` in `ui/src/App.pageheader.test.jsx`.
  - Added E2E assertions on `page.getByTestId("page-header-title")` and `page.getByTestId("top-bar-page")` in `ui/e2e/responsive-navigation-walkthrough.spec.js`.

### Bug 2: Creating or deleting a standing session did not refresh `App`'s task list, leaving the new session's status chip blank on `/sessions` until the next poll tick

- **Symptom**:
  After `POST /api/console-sessions` was updated to synchronously create and park the session's backing task (`model.FileConsoleSessionTask`), creating a new standing session via `+ New session` and navigating back to `/sessions` before the next background poll tick rendered the new session row with no `<SessionStatus />` chip (`taskById.get(session.taskId)` was `undefined`) instead of `"Your turn"`.
- **Root Cause**:
  In `ui/src/App.jsx`, `createStandingSession` and `deleteStandingSession` only called `await refreshSessions()` without `refreshList()`, so `tasks` in `App.jsx` did not include the newly created (or closed) backing task until the next poll interval.
- **Deterministic Reproduction**:
  1. Open `/sessions`, click `+ New session`, create `Release Coordinator`, and immediately click `Sessions` in the left rail.
  2. Observe the `Release Coordinator` row in `.template-list`: before the fix, the `"Your turn"` status chip was absent until the background task poll ran.
- **Fix**:
  Updated `createStandingSession` and `deleteStandingSession` in `ui/src/App.jsx` to run `await Promise.all([refreshSessions(), refreshList()])`.
- **Regression Coverage**:
  - Added unit test `"refreshes the task list when creating a standing session so its status shows on the sessions list"` in `ui/src/App.test.jsx`.
  - Added E2E assertion in `ui/e2e/responsive-navigation-walkthrough.spec.js` verifying that clicking `Sessions` in the rail immediately after creating a session displays `"Your turn"` on the new session's row.

---

## 6. Automated Test Verification

- **Frontend Unit Tests (`vitest`)**:
  - `ui/src/App.pageheader.test.jsx`: 14 passed (14)
  - `ui/src/App.test.jsx`: 44 passed (44)
- **Playwright E2E Suite (`chromium` & `webkit`)**:
  - `ui/e2e/responsive-navigation-walkthrough.spec.js`: 72 passed (36 Chromium + 36 WebKit)
