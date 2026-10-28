# Daily Exploratory UI Test Run — 2026-10-22

- **Task**: `grain/049f1e09/task-186`
- **Test Plan**: `docs/test-plans/2026-10-22.md`
- **Branch / Base Commit**: `candidate` @ `e1c57b453885` (`v1.14.0-rc.1.0.20261022095526-e1c57b453885`)
- **Targets Exercised**:
  1. `grain demo` (with embedded production UI bundle and scenario flags `-observability -merge-queue-states -sensitive-hold -mechanical-port -merge-rules -secrets-store -qualification`)
  2. Live `grain daemon` (`-sandbox-backend none` on an isolated `-data-dir` store)
- **Browsers & Viewports**:
  - Playwright 1.62.1 Chromium (`151.0.7922.34`) at `1920x1080`, `1440x900` (desktop), `768x1024` (tablet), and `375x812` (`isMobile: true, hasTouch: true`, coarse pointer)
  - Playwright 1.62.1 WebKit (`2336`) at `1440x900` (desktop) and `375x812` (mobile)

---

## 1. Passive Route & Breakpoint Sweep

Swept **71 routes** across **6 browser/viewport passes** (**426 route loads** total):

- **Routes**:
  - Top-level views: `/`, `/inbox`, `/tasks`, `/board`, `/timeline`, `/tree`, `/ci`, `/processes`, `/processes?kind=schedule`, `/processes?kind=template`, `/processes?kind=suite`, `/repos`, `/repos/acme/widgets`, `/repos/acme/widgets/releases`, `/repos/acme/widgets/releases/2.1`, `/boards`, `/boards/board-1`, `/boards/board-2`, `/boards/board-3`, `/boards/board-4`, `/sessions`, `/director`, `/session/director`, `/memory`
  - Settings sections (15): `/settings`, `/settings/general`, `/settings/access`, `/settings/config-repo`, `/settings/models`, `/settings/agents`, `/settings/memory`, `/settings/tasks`, `/settings/repositories`, `/settings/git`, `/settings/credentials`, `/settings/executors`, `/settings/engine`, `/settings/ci`, `/settings/sandboxes`, `/settings/caches`, `/settings/preferences`
  - System tabs (9): `/system`, `/system/health`, `/system/logs`, `/system/sandboxes`, `/system/caches`, `/system/top`, `/system/pipeline`, `/system/metrics`, `/system/upgrade`, `/system/power`
  - Legacy `/admin/*` & `/system/*` redirects (16): `/admin`, `/admin/general`, `/admin/agents`, `/admin/models`, `/admin/git`, `/admin/workflow`, `/admin/access`, `/admin/accounts`, `/admin/engine`, `/admin/sandbox`, `/admin/executors`, `/admin/integrations`, `/admin/state`, `/admin/logs`, `/admin/upgrade`, `/system/models`, `/system/state`, `/metrics`
  - Seeded task detail overlays: `/tasks/1` through `/tasks/22`
- **Results**:
  - **Horizontal overflows** (`document.documentElement.scrollWidth > clientWidth`): `0 / 426`
  - **Blank / crash screens / error boundary triggers**: `0 / 426`
  - **Page errors (`pageerror`)**: `0` in Chromium across all 4 viewports
  - **Network errors**: Only expected `404` on `GET /api/repos/acme/widgets/github-branches` when running `grain demo` without `-github-sim`

---

## 2. Feature & Workflow Coverage Summary

| Area | Journeys & Assertions Exercised | Verdict |
|---|---|---|
| **Access Gate & Auth** | Unauthenticated `GET /api/tasks` rejected (`401`); invalid access code shows `role="alert"` (`"That access code was not accepted."`) without clearing input; valid code signs in case-insensitively and sets session cookie. | **PASS** |
| **Settings Split & Guardrails** | Traversed all 15 `/settings/<section>` routes and 9 `/system/<tab>` routes; verified legacy `/admin/*` and `/system/<old>` redirects; verified `PUT /api/settings` validation (`maxWorkers: -1` rejected with `400 {"error":"maxWorkers must be at least 1"}`). | **PASS** |
| **Kubernetes Sandbox Settings (`4c7da2279`)** | Verified live `grain daemon` offers `kubernetes` and `kubernetes-granule` in `sandboxBackends`, persists all 13 Kubernetes settings (`k8sNamespace`, `k8sSandboxImage`, `k8sGranuleImage`, `k8sWorkspace`, `k8sGitProxyHost`, `k8sSandboxService`, `k8sNodeSelector`, `k8sServiceAccount`, `k8sDeployment`, `k8sContainer`, `k8sKubectl`, `k8sPrivileged`, `k8sNetworkPolicy`) on `/settings/sandboxes`, and populates `pendingRestart` (14 keys). | **PASS** |
| **Config Repo IaC Modes (`41762bd9b`)** | Verified live `grain daemon` `/settings/config-repo` Internal mode (`"Export now"`) vs. External read-only IaC mode (`POST /api/state-repo/remote` -> `"Pull now"`, `data-testid="external-config-repo-banner"` visible across repo-managed sections, inputs disabled, and `PUT /api/settings` rejected with `409 Conflict`). | **PASS** |
| **Remote Executor Management** | Issued join token on `/settings/executors` (`201`, one-time `grain-exec-...` secret banner), revoked token (`DELETE` -> `200`), and inspected `/system/sandboxes` host commitment, cache bounds, and connected executor pool table. | **PASS** |
| **Processes: Templates, Suites & Schedules (`dc16c6371`, `010a23f08`, `f1d62a42f`)** | Verified unified `/processes` list and `?kind=` / `?q=` URL sync; created Template (`201`); verified Suite `maxPasses: 101` rejected (`400 {"error":"max passes must be at most 100"}`) and valid `until_clean` Suite creation (`201`); verified Schedule `everyNHours: 0` rejected (`400`), base branch whitespace trimmed (`"  release/1.0  "` -> `"release/1.0"`), two-tab stale save rejected (`409` with stale alert & `Reload` recovery), and `Run now` spawning a task. | **PASS** |
| **Merge Queue & CI Status Boards (`aa7b785f0`)** | Started CI run on `/ci` (`acme/widgets`, `main`, `Load workflows`, `Bypass cache`, `Start run`) -> deep-linked to `/ci/<id>`, survived cold page reload, cancelled run cleanly (`Cancel run` -> `cancelled`), and handled `/ci/nonexistent-run-999` without crashing. | **PASS** |
| **Repo Releases & Qualification** | Inspected `/repos/acme/widgets/releases` qualification progress (`Pending approval`, `Succeeded`, `Failed`) and created release `2.2` (`/repos/acme/widgets/releases/2.2`). Found stale `"under Templates"` copy (filed below). | **PASS (with minor copy defect)** |
| **Core Task Lifecycle & Deferred Approval (`2ea857d5b`)** | Verified `writes` validation on `POST /api/tasks` (rejects repos outside `targetRepos` and duplicate `(repo, base)`). Verified in-place `Approve` and `Withdraw approval` on a `deferred` task keep `state: "deferred"` while toggling `approved`. | **PASS** |
| **Provenance Disclosure & TaskTree (`d23f96d33`, `be3bc8e16`, `a4b7cac89`)** | On `375x812` coarse-pointer viewport, verified `.work-tree .provenance-toggle` meets `>= 40px` touch target height (`40px`), starts collapsed (`aria-expanded="false"`), expands on click to show `.provenance-links` and `TaskTree`, navigates on `.task-tree-row` click, and resets to collapsed when switching tasks. | **PASS** |
| **Inbox Pre-Fetch & PendingQuestion (`b0a21c2bb`, `2fc026c20`)** | Verified `/inbox` pre-fetches `.inbox-question` for `awaiting_reply` tasks; verified `PendingQuestion` callout preserves unsent draft across 3s background poll ticks and transitions task back to `queued` on `Send`. | **PASS** |
| **Console Sessions (`49cfebd4f`, `882d00120`, `3a81d0003`)** | Verified navigating to a console session's backing task (`/tasks/12`) redirects to `/session/director`; verified `WorkTree` disclosure renders in `ConsoleSessionPage`; verified `POST /api/console-sessions/director/restart` (`carryHandover: false`) allocates a fresh backing task (`#24`). | **PASS** |
| **Saved Boards, Bulk Ops & Task Filters (`0b296198a`, `a4b7cac89`, `ea3ec549a`)** | Verified Kanban (`board-1`), Timeline (`board-2`), Report (`board-3`), and Tree (`board-4`) boards; verified `New board` opens pre-filled `NewTaskOverlay`; verified Shift-click range selection on `/tasks` and `/board`; verified narrowing `/board` with active selection displays `"none of them shown here"` warning in `BatchActionsBar`; verified `Bulk edit…` dialog, complex filter URL sync (`?sort=updated:asc&f=state:any:queued|running`), and unknown filter attribute chip resilience (`?f=unknown_field:is:foo`). | **PASS** |
| **Client Recovery & Offline Resilience** | Navigated 10 unknown/malformed routes on Chromium & WebKit mobile (`375x812`) with `0` blank screens or crashes. Simulated network outage (`context.setOffline(true)`): verified `[data-testid="disconnected-banner"]` (`"Unreachable … retrying automatically"`) appears after 2 failed poll ticks and clears automatically upon reconnect. | **PASS** |

---

## 3. Defects Discovered & Tasks Proposed

Checked in-flight branches on `049f1e09` (`task-104`, `task-180`, `task-220`) to avoid filing duplicates for already-fixed issues (Settings Memory dirty dot/leave guard, `App.jsx` & `RepoPage.jsx` `leavingLosesEdits()` guards, `ConfigRepoSection` `onStatusChange` forwarding, `RepositoriesSection` modified click guard, and `CachesSection`/`TasksSection`/`ConnectedExecutors`/`alerts.js` upgrade tab pointers).

Filed **3 new defect tasks** against `branch: "candidate"`:

1. **`demo: scenario flags (-merge-rules, -sensitive-hold, -mechanical-port) clobber each other's SyncRepoDeclarations on acme/widgets`**
   - **Location**: `pkg/demo/demo.go:1073`, `pkg/demo/demo_merge_rules.go:94`, `pkg/demo/demo_sensitive.go:59`, `pkg/demo/demo_mechanical_port.go:165`
   - **Symptom**: Each scenario helper calls `store.SyncRepoDeclarations(ctx, repo, ...)` with a partial `model.RepoDeclarations` containing only its own declarations (`Templates`/`Suites`/`Schedules` in `seedDemo`, `MergeRules` in `-merge-rules`, `Triggers` in `-sensitive-hold`, `Settings` in `-mechanical-port`). Because `SyncRepoDeclarations` replaces the entire declaration set for `acme/widgets` and clears `last_error`, enabling `-merge-rules`, `-sensitive-hold`, or `-mechanical-port` wipes out `seedDemo`'s repo-declared template, suite, and schedule, and `-mechanical-port` wipes out `-merge-rules` and `-sensitive-hold`'s declarations and `last_error`.
2. **`UI: state-sync-failing alert and Inbox StateSyncGroup still offer "Open System" (no-op on System -> Health) instead of opening Settings -> Config repo`**
   - **Location**: `ui/src/alerts.js:334`, `ui/src/components/system/HealthTab.jsx:97`, `ui/src/components/InboxPage.jsx:431-462`, `ui/src/App.jsx:2981, 3208`
   - **Symptom**: After `StateRepoPanel` moved from `/system/state` to `/settings/config-repo` (`Settings → Config repo`), the critical `state-sync-failing` alert (`"Settings are not being exported"`) and `InboxPage`'s `StateSyncGroup` still render an `"Open System"` button (`kind: "open-system"`). On `System → Health`, `HealthTab.jsx` passes `onOpenSystem={() => {}}` to `<AlertsPanel>`, making `"Open System"` a complete no-op; and from the top-bar Alerts popover or `/inbox`, clicking `"Open System"` navigates to `/system` instead of `/settings/config-repo`.
3. **`UI: stale navigation copy ("Settings -> Git connections", "under Templates") and intercepted modified clicks in NoGitConnectionNotice, RepoList, and RepoReleases`**
   - **Location**: `ui/src/components/NoGitConnectionNotice.jsx:30-39`, `ui/src/components/RepoList.jsx:431-440`, `ui/src/components/RepoReleases.jsx:631, 875`
   - **Symptom**: `NoGitConnectionNotice.jsx` renders `"Settings → Git connections"` (the section in `SETTINGS_SECTIONS` is `"Git"`, `Settings → Git`), `RepoReleases.jsx` still refers to `"under Templates"` (replaced by `Processes`), and both `NoGitConnectionNotice.jsx` and `RepoList.jsx` call `evt.preventDefault()` unconditionally on modified clicks (`Ctrl+click`, `Cmd+click`, `Shift+click`, middle-click) instead of allowing the browser to open `/settings/git` in a new tab.
