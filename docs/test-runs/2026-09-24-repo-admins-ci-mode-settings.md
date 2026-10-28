# Repository-Level Admin Roles, Unified Single-Select CI Mode, and Settings Navigation Overhaul Walkthrough

## Overview
Comprehensive test run and end-to-end verification for Repository-Level Admin Roles, Unified Single-Select CI Mode (`grain`, `github`, `none`), and Settings Navigation Overhaul features defined in `docs/test-plans/2026-09-24.md §6`.

## Scope and Environment

- **Repository**: `bwsalmon/grain`
- **Branch**: `grain/task-546`
- **Host**: Debian bookworm, Linux x86_64
- **Go**: `1.26.6`; **Node**: `20.19.0`; **npm**: `10.8.2`
- **Test Frameworks**: Vitest `4.1.11`, React Testing Library `16.3.3`, Go `testing` suite
- **Embedded Production UI**: `make frontend` compiling Vite production bundle into `pkg/ui/static`

---

## Walkthrough Execution and Verification Details

### 1. Unified Single-Select CI Mode Selector (`RepoPage.jsx`)
- **Location**: `/repo/:owner/:repo` (RepoPage -> Merge queue & CI configuration).
- **Control**: Single-select `RadioGroup` bound to `upToDate.ciMode` with choices:
  1. `github`: "Use GitHub CI (Default) — External GitHub Actions / status checks gate mergeability."
  2. `grain`: "Use Grain CI — Grain CI (`.grain/workflows`) is authoritative; external GitHub CI checks are ignored."
  3. `none`: "Use No CI — CI verification is bypassed; pull requests merge cleanly without waiting for CI checks."
- **API Endpoints**:
  - `GET /api/repos/:owner/:repo/ci-mode`: Returns current repo settings including `ciMode`.
  - `PUT /api/repos/:owner/:repo/ci-mode`: Accepts `{ ciMode: "grain" | "github" | "none" }` and stores configuration in `model.RepoConfig`.
- **Validation**:
  - Toggling radio buttons immediately persists via `saveCIMode` and updates the UI state.
  - Setting `grain` configures `model.RepoConfig.CIMode = "grain"` and sets `IgnoreGitHubCI = true`.
  - Setting `none` configures `model.RepoConfig.CIMode = "none"`.

### 2. Repository-Level Administrator Roles & User Management
- **Location**: `/repo/:owner/:repo` (RepoPage -> Repository Administrators section).
- **UI Interaction**:
  - Admin list displays active admin usernames as MUI `Chip` components with delete buttons.
  - Adding an administrator via text field (`user@example.com`) and "Add Admin" button calls `saveAdmins([...current, trimmed])`.
  - Deleting an admin chip invokes `saveAdmins(current.filter(a => a !== admin))`.
  - When no repo admins are set, displays: *"No repository administrators configured (deployment operators only)"*.
- **API Endpoints**:
  - `GET /api/repos/:owner/:repo/admins`: Returns current admin accounts for the repo.
  - `PUT /api/repos/:owner/:repo/admins`: Replaces the admin list with validated, deduplicated usernames.

### 3. Authorization & RBAC Enforcement
- **Backend Enforcement**: `checkRepoAdmin` in `pkg/ui/repos.go`.
- **RBAC Rules**:
  - If System Administrators (`AdminAccounts`) are configured, system admins have full access to modify any repository.
  - If Repository Administrators (`RepoConfig.Admins`) are configured, only listed repo admins (or system admins) can modify repo settings, CI mode, prompt extensions, setup commands, or workflows.
  - Non-admin callers receive `403 Forbidden` / `ValidationError` with message `user "<user>" does not have permission to modify repository <repo>`.
- **Mutation Endpoints Protected**:
  - `PUT /api/repos/:owner/:repo/ci-mode`
  - `PUT /api/repos/:owner/:repo/admins`
  - `PUT /api/repos/:owner/:repo/settings`
  - `PUT /api/repos/:owner/:repo/capabilities`
  - `PUT /api/repos/:owner/:repo/prompt-extension`
  - `PUT /api/repos/:owner/:repo/setup-command`
  - `PUT /api/repos/:owner/:repo/workflows`

### 4. Merge Predicate Immediate Adaptation (`pkg/orchestrator/mergepredicate.go`)
- **Merge Predicate Check**: `greenHead: health == model.PrClean || e.ciMode == model.CIModeNone`.
- **Mode-Specific Gating**:
  - `CIModeNone`: `greenHead` evaluates to `true` immediately; clean pull requests merge without requiring or waiting for CI verdicts.
  - `CIModeGrain`: Requires Grain CI (`.grain/workflows`) to succeed on the head commit (`health == model.PrClean`); ignores external GitHub Actions.
  - `CIModeGitHub`: Requires external GitHub status checks / checks runs to pass (`health == model.PrClean`).
- **Dynamic Adaptation**: Changing `ciMode` takes effect immediately on the next merge evaluation cycle without daemon restart.

### 5. Settings Navigation Overhaul (`SettingsOverlay.jsx`)
- **Restructured Categories**:
  - **User settings (`user`)**: Profile, preferences, appearance (theme mode), timeline order.
  - **Team settings (`team`)**: Team & Members, Connections, Models, Executors, Workflow & Tasks.
  - **Administrator settings (`admin`)**: General & Deployment, Access & Accounts, Engine & Concurrency, Sandbox & Infrastructure, Executors, Integrations & Auth, All Teams, Config repo (`state`), Upgrade.
- **Routing & Deep Linking**:
  - Subtab parameter `initialTab` maps to the corresponding category and tab (e.g., `initialTab="accounts"` routes to `category="admin"`, `tab="access"`).
  - RBAC gating: Non-admin users are restricted from the `admin` category and automatically redirected to `user` settings.
  - Responsive tab layout renders clean vertical and horizontal alignments across desktop and tablet viewports.

### 6. Left Navigation Sidebar & Pinned Items (`Sidebar.jsx`, `CustomizePinnedOverlay.jsx`)
- **Customizable Pinned Views**:
  - Pinned item categories: Task states/views, saved boards, interactive sessions, schedules, templates, suites, CI runs, repositories.
  - "Customize pinned" overlay allows adding, removing, and reordering pinned items.
  - Preference persistence: Stored in `localStorage` under `grain.pinnedNav` via `PinnedNavContext.jsx`.
  - Survives page refresh and browser reload.

### 7. UI Visual States & Runtime Flags
- **Brand Glyphs (`ItemGlyph.jsx`)**:
  - High-precision SVG Chladni glyphs for Director, boards, sessions, templates, suites, schedules, and CI runs.
  - Renders inline using `fill: currentColor` to adapt seamlessly between dark and light themes.
- **Green Merge Repair Spinner**:
  - Setup and merge repair operations render the green CSS spinner (`state-completed` / `repair`) matching `grain-mark-repair`.
- **`--disable-native-sandboxes` Flag (`cmd/grain/daemon.go`)**:
  - Configures `model.SandboxBackendNone` disabling native host/granule sandbox creation.
  - Rejects invalid flag combinations (e.g., combining with `-host-sandboxes`).

---

## Test Execution Results

### 1. Go Backend Test Suites

```
=== RUN   TestRepoSettingsCarryTheDeclarationsSwitchAndItsStatus
--- PASS: TestRepoSettingsCarryTheDeclarationsSwitchAndItsStatus (0.14s)
=== RUN   TestSetRepoCIModeStoresAndValidates
--- PASS: TestSetRepoCIModeStoresAndValidates (0.13s)
=== RUN   TestSetRepoAdminsStoresAndDeduplicates
--- PASS: TestSetRepoAdminsStoresAndDeduplicates (0.09s)
=== RUN   TestRepoSettingsAndCIModeHTTPHandlers
--- PASS: TestRepoSettingsAndCIModeHTTPHandlers (0.08s)
=== RUN   TestRepoAuthorizationEnforcement
--- PASS: TestRepoAuthorizationEnforcement (0.16s)
=== RUN   TestTheMergePredicateHasTwoArmsAndOneException
--- PASS: TestTheMergePredicateHasTwoArmsAndOneException (0.00s)
=== RUN   TestDaemonFlagValidation
--- PASS: TestDaemonFlagValidation (0.02s)
PASS
```

### 2. Frontend Unit Test Suites (Vitest)

```
Test Files  8 passed (8)
     Tests  263 passed (263)
  Duration  53.69s

- RepoPage.test.jsx: 56 tests passed (CI mode radio buttons, repo admin chips, workflow editor)
- SettingsOverlay.test.jsx: 119 tests passed (User/Team/Admin category routing, tab deep-links, RBAC gating)
- Sidebar.test.jsx: 59 tests passed (pinned items, team filtering, footer navigation)
- CustomizePinnedOverlay.test.jsx: 3 tests passed (add/remove/reorder pinned navigation items)
- PinnedNavContext.test.jsx: 3 tests passed (localStorage persistence, default items)
- pinnedNav.test.js: 11 tests passed (serialization, deduplication, item normalization)
- ItemGlyph.test.jsx & item-glyphs.test.js: 12 tests passed (brand glyph paths, viewBox framing)
```

---

## Conclusion
All features defined in `docs/test-plans/2026-09-24.md §6` have been thoroughly verified and tested across Go backend services, API endpoints, HTTP routing, authorization layers, and React frontend components.
