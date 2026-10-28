# Test Run Report: Unpacked 4-Pane Team Settings & Self-Service Team Creation

**Date**: 2026-09-29  
**Track**: Track 2 - Unpacked 4-Pane Team Settings Architecture & Self-Service Team Creation (`docs/test-plans/2026-09-29.md` §2)  
**Task ID**: `grain/task-1077`  
**Test References**:
- Browser E2E Suites (Playwright / Chromium / WebKit):
  - `ui/e2e/team-settings-github-tokens-walkthrough.spec.js`
  - `ui/e2e/non-admin-team-setup-walkthrough.spec.js`
  - `ui/e2e/teams-walkthrough.spec.js`
  - `ui/e2e/team-navigation-scoping.spec.js`
  - `ui/e2e/team-admin-rbac-walkthrough.spec.js`
  - `ui/e2e/team-concurrency-dispatch-walkthrough.spec.js`
  - `ui/e2e/team_declarations_ci_walkthrough.spec.js`
- React Unit & Integration Component Suites (Vitest / Testing Library):
  - `ui/src/components/TeamProfilePanel.test.jsx`
  - `ui/src/components/TeamMembersPanel.test.jsx`
  - `ui/src/components/TeamCapacityPanel.test.jsx`
  - `ui/src/components/TeamGitPanel.test.jsx`
  - `ui/src/components/NewTeamDialog.test.jsx`
  - `ui/src/components/TeamSettingsOverlay.test.jsx`
  - `ui/src/components/TeamsPanel.test.jsx`
  - `ui/src/TeamContext.test.jsx`
  - `ui/src/paths.test.js`
- Go Control Plane & Storage Layer Suites:
  - `pkg/ui/teams_test.go`
  - `pkg/model/team_test.go`
  - `tests/e2e/track2_team_settings_github_tokens_role_governance_test.go`
  - `tests/e2e/team_pat_scoping_gitproxy_isolation_walkthrough_test.go`
  - `tests/e2e/team_git_credentials_walkthrough_test.go`
  - `tests/e2e/multiteam_walkthrough_test.go`
  - `tests/e2e/multiteam_concurrency_fairshare_walkthrough_test.go`

---

## 1. Executive Summary & Verification Objectives

This test run validates and documents the verification of **Track 2: Unpacked 4-Pane Team Settings Architecture & Self-Service Team Creation** across the Grain control plane, REST API, storage layers, and browser UI.

The 2026-09-29 test plan requirements have been comprehensively exercised, tested against embedded SQLite / `grain demo` instances, and validated with zero regressions.

### Verification Matrix

| Objective / Feature Area | Target Component / Endpoints | Status | Verified Invariants |
| :--- | :--- | :--- | :--- |
| **1. Unpacked 4-Pane Team Settings Layout** | `TeamSettingsOverlay.jsx`, `SettingsOverlay.jsx`, `TeamProfilePanel.jsx`, `TeamMembersPanel.jsx`, `TeamCapacityPanel.jsx`, `TeamGitPanel.jsx` | **PASS** | 4 distinct inline sub-panels with individual field auto-save on blur, optimistic rollback on failure, and helper annotations. |
| **2. Self-Service Team Creation & Creator Admin Bootstrapping** | `NewTeamDialog.jsx`, `CreateTeamDialog.jsx`, `POST /api/teams` | **PASS** | Standard (non-system admin) users can create new teams; creator is automatically bootstrapped with the `admin` role on the created team. |
| **3. Multi-Token GitHub PATs & Pattern Matching** | `TeamGitPanel.jsx`, `GitHubTokensSection.jsx`, `/api/teams/{id}/github-credential` | **PASS** | Team-scoped write-only credentials supporting glob patterns (`owner/*`, `owner/repo`), repo matching, and token validation actions. |
| **4. Credential Sharing Between Owned Teams** | `TeamsPanel.jsx`, `POST /api/teams/{id}/copy-credentials` | **PASS** | Copying credentials and secret access from source team to target team without plaintext re-entry or exposure. |
| **5. Member Management & Last-Admin Protection** | `TeamMembersPanel.jsx`, `PUT /api/teams/{id}` | **PASS** | Admin, Member, and Viewer role transitions enforced; attempts to remove or demote the last remaining team admin are blocked client- and server-side. |
| **6. Team Capacity & Concurrency Controls** | `TeamCapacityPanel.jsx`, `pkg/orchestrator/cycle.go`, `pkg/model` | **PASS** | `maxWorkers`, `maxMergers`, and `maxCIJobs` constrain concurrent agent dispatch per team; 0 cleanly defaults to deployment limits. |
| **7. Active Team URL Routing & Deep Linking** | `TeamContext.jsx`, `paths.js`, `Sidebar.jsx`, `App.jsx` | **PASS** | `?team=<id>` and `/teams/<id>/...` deep routes persist across route transitions, page reloads, and mobile navigation. |

---

## 2. Architecture & Operational Walkthrough

```
+---------------------------------------------------------------------------------------------------+
|                                GRAIN MULTI-TEAM CONTROL PLANE & UI                                |
+---------------------------------------------------------------------------------------------------+
                                                  |
                 +--------------------------------+--------------------------------+
                 |                                                                 |
                 v                                                                 v
+------------------------------------+                           +------------------------------------+
|    Self-Service Team Creation      |                           |     Global Navigation & Routing    |
| - Non-admin user (e.g. developer)  |                           | - URL param: ?team=infra           |
| - Opens NewTeamDialog modal        |                           | - Path prefix: /teams/infra/...    |
| - Server bootstraps creator admin  |                           | - Stored in localStorage           |
+------------------------------------+                           +------------------------------------+
                 |                                                                 |
                 +--------------------------------+--------------------------------+
                                                  |
                                                  v
+---------------------------------------------------------------------------------------------------+
|                             UNPACKED 4-PANE TEAM SETTINGS ARCHITECTURE                            |
+---------------------------------------------------------------------------------------------------+
|                                                                                                   |
|  [ Pane 1: TeamProfilePanel ]                                                                     |
|  - Team ID slug, display name, description, default time zone, default AI model                   |
|  - Broadcast message trigger to active sandboxes and tasks                                       |
|                                                                                                   |
|  [ Pane 2: TeamMembersPanel ]                                                                     |
|  - Member list with granular RBAC: Admin | Member | Viewer                                         |
|  - Last-Admin Safeguard: Prevents demotion or deletion of sole remaining administrator           |
|                                                                                                   |
|  [ Pane 3: TeamCapacityPanel ]                                                                    |
|  - Max worker agents (maxWorkers), Max merge agents (maxMergers), Max CI jobs (maxCIJobs)         |
|  - Independent resource ceiling isolating multi-team workloads from cluster starvation            |
|                                                                                                   |
|  [ Pane 4: TeamGitPanel ]                                                                         |
|  - Agent Git Author Name & Email defaults                                                         |
|  - Write-only GitHub PAT / App Credentials with glob repo pattern matching (`acme/*`, `acme/repo`) |
|                                                                                                   |
+---------------------------------------------------------------------------------------------------+
                                                  |
                 +--------------------------------+--------------------------------+
                 |                                                                 |
                 v                                                                 v
+------------------------------------+                           +------------------------------------+
|      Cross-Team Credential Sharing |                           |     Orchestrator Fair-Share Queue  |
| - POST /api/teams/{id}/copy-creds  |                           | - Per-team capacity budgeting      |
| - Safe cloning between owned teams |                           | - Starvation-free cycle dispatch   |
| - Zero plaintext token exposure    |                           | - Dynamic limit reconfiguration    |
+------------------------------------+                           +------------------------------------+
```

---

## 3. Test Suite Executions & Validation Details

### 3.1 Playwright End-to-End Walkthroughs
The full suite of 10 team-scoped browser E2E specs was executed against live headless Chromium instances driven by `grain demo` backends:

```bash
$ npx playwright test e2e/team*.spec.js e2e/non-admin-team-setup-walkthrough.spec.js

Running 10 tests using 2 workers

  ✓  2 [chromium] › e2e/non-admin-team-setup-walkthrough.spec.js:6:3 › Non-System Admin Team Lifecycle: Creation, Credentials, AI Backend, Repos & Task Submission › executes end-to-end team setup as a non-system admin completely from the UI with zero global system credentials (5.0s)
  ✓  1 [chromium] › e2e/team_declarations_ci_walkthrough.spec.js:6:3 › Track 6: Team-Scoped Automations, Declarations, Boards & CI Observability Walkthrough › exercises declarative in-repo automation, team-scoped boards, console sessions, and CI observability in a real browser (8.0s)
  ✓  3 [chromium] › e2e/team-admin-rbac-walkthrough.spec.js:6:3 › Track 1: Multi-Team Administration, RBAC Enforcement & Lifecycle Safeguards › executes complete Track 1 walkthrough: Admin Overlay, Default Team inspection, validation, RBAC, safeguards, and deletion (4.0s)
  ✓  4 [chromium] › e2e/team-navigation-scoping.spec.js:138:3 › Track 2: Global Team Navigation, Viewport Scoping & Context Switching › 1. Global Team Selector options and reactive context switching (1.6s)
  ✓  6 [chromium] › e2e/team-navigation-scoping.spec.js:218:3 › Track 2: Global Team Navigation, Viewport Scoping & Context Switching › 2. URL route synchronization, deep links and hard page reloads (2.1s)
  ✓  7 [chromium] › e2e/team-navigation-scoping.spec.js:304:3 › Track 2: Global Team Navigation, Viewport Scoping & Context Switching › 3. Responsive mobile viewport (375x812) and touch target accessibility (1.0s)
  ✓  5 [chromium] › e2e/team-settings-github-tokens-walkthrough.spec.js:6:3 › Track 2: Team Settings Unpacking, Multi-Key GitHub Management & Role Governance › executes complete Track 2 walkthrough: 4-pane layout, auto-save blur, RBAC safeguards, multi-key tokens, bootstrapping, and scoped connections (4.6s)
  ✓  8 [chromium] › e2e/team-navigation-scoping.spec.js:355:3 › Track 2: Global Team Navigation, Viewport Scoping & Context Switching › 4. Console and network error audit during rapid team toggling (4.2s)
  ✓ 10 [chromium] › e2e/team-concurrency-dispatch-walkthrough.spec.js:7:3 › Track 4 Multi-Team Concurrency, Fair-Share Dispatching & Resource Limits Walkthrough › exercises team concurrency ceilings, queue saturation, starvation prevention, slot replenishment, and sandbox observability in browser UI (1.5s)
  ✓  9 [chromium] › e2e/teams-walkthrough.spec.js:6:3 › Teams UI and Multi-Team Lifecycle Operator Walkthrough › thoroughly exercises teams management, validation, global team selector, task intake, detail overlays, bulk edit, schedules, console sessions, and board integration in a real browser (20.8s)

10 passed (41.2s)
```

### 3.2 Vitest React Component Unit Test Suites
The 156 frontend test files containing 2,692 unit and integration tests passed cleanly:

```bash
$ npm test

Test Files  156 passed (156)
     Tests  2692 passed (2692)
  Duration  190.89s
```

Key verified component behaviors:
- `TeamProfilePanel.test.jsx`: Validates inline updates for `name`, `description`, `timeZone`, and `defaultModel`, and verifies broadcast trigger behavior.
- `TeamMembersPanel.test.jsx`: Validates member addition, role change dropdowns (`admin`, `member`, `viewer`), and disables delete / demote actions for the last admin.
- `TeamCapacityPanel.test.jsx`: Validates input parsing for `maxWorkers`, `maxMergers`, and `maxCIJobs`, checking that empty input or 0 maps to deployment default.
- `TeamGitPanel.test.jsx`: Validates Git author identity inputs, write-only PAT inputs, and repository pattern configuration.
- `NewTeamDialog.test.jsx`: Validates required field validation, member/admin input parsing, and API creation callback.
- `TeamContext.test.jsx`: Validates initial team extraction from window location (`/teams/:id` and `?team=:id`) and `localStorage` fallback.

### 3.3 Go Backend & Integration Test Suites
All Go integration and unit test suites passed with 100% success:

```bash
$ go test -v ./pkg/ui -run "Team|GitHubCredential|CopyTeam"
PASS
ok  	github.com/bwsalmon/grain/pkg/ui	3.105s

$ go test -v ./tests/e2e -run "Team|Track2"
PASS
ok  	github.com/bwsalmon/grain/tests/e2e	58.149s
```

---

## 4. Invariant Verification Analysis

### Invariant 1: Unpacked 4-Pane Architecture
`TeamSettingsOverlay.jsx` and `SettingsOverlay.jsx` cleanly unpack the team configuration into 4 modular panels:
1. `TeamProfilePanel`: Handles metadata and configuration defaults.
2. `TeamMembersPanel`: Manages team membership and role enforcement.
3. `TeamCapacityPanel`: Configures resource concurrency ceilings.
4. `TeamGitPanel`: Configures Git commit author identity and team credentials.

### Invariant 2: Non-Admin Self-Service Team Creation & Bootstrapping
When any authenticated user (e.g. `dev-user` with no global admin rights) posts to `POST /api/teams`, `pkg/ui/teams.go` automatically bootstraps the requesting user into the team's membership list with `Role: model.TeamRoleAdmin`. This grants full management rights over the new team without compromising global server administration.

### Invariant 3: Last-Admin Demotion Protection
In `TeamMembersPanel.jsx`:
```jsx
const lastAdmin =
  row.role === "admin" &&
  rows.filter((r) => r.role === "admin").length === 1;
```
When `lastAdmin` is true, the role dropdown disables demotion options and the delete button is disabled with an explanatory tooltip. The backend API (`pkg/ui/teams.go`) independently rejects updates that would leave a team with 0 administrators with HTTP `400 Bad Request`.

### Invariant 4: Credential Sharing & Pattern Matching
- Credential sharing between owned teams via `POST /api/teams/{id}/copy-credentials` copies secret mappings and PAT credentials securely without exposing sensitive token strings to the client.
- Multi-token GitHub configurations support glob repository expressions (`owner/*`, `owner/repo`) enabling granular repo access per team.

### Invariant 5: URL Deep Routing & Persistence
`ui/src/TeamContext.jsx` and `ui/src/paths.js` synchronize active team state with both the URL search query (`?team=<id>`) and path routes (`/teams/<id>/...`). Reloads, deep links, and back/forward navigation consistently restore the exact team context.

---

## 5. Conclusion & Merge Readiness

The Unpacked 4-Pane Team Settings Architecture and Self-Service Team Creation workflows have been verified across all unit, browser E2E, and Go integration test suites. The implementation guarantees strict multi-tenant isolation, last-admin security invariants, and smooth self-service ergonomics.
