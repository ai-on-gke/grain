# Daily Exploratory UI Testing & Defect Discovery Report (2026-10-10)

## Executive Summary

- **Date:** 2026-10-10
- **Target Environment:** Staging Grain Web UI (`grain demo` with embedded store, `-observability -merge-queue-states -sensitive-hold -mechanical-port -merge-rules`)
- **Git Commit / Branch:** `003d11565` on `grain/task-2842` (tracking `candidate`)
- **Browsers Tested:** Chromium (headless & mobile emulation), WebKit (Safari mobile emulation)
- **Viewports Covered:** Desktop 1920×1080, Desktop 1440×900, Tablet 768×1024, Mobile 375×812
- **Test Personas:**
  - `operator` (System Admin / Deployment Administrator with `X-Grain-Admin-Mode: on`)
  - `zed` (Standard User / Non-Admin)
  - `carol` (Team Administrator for `payments` team, non-deployment admin)
- **Overall Verdict:** **PASSED / CLEAN**
  - No new UI bugs or visual/functional regressions were detected across the staging environment.
  - Newly merged candidate features (including user settings version display from commit `7f7564082` / PR #3339 and unsaved edit guards from commit `dc810e321` / PR #3322) were systematically tested and verified working as designed.
  - Automated UI test suite passed 100% (193 test files, 3,179 tests passed).
  - Both standard exploratory and deep interactive CRUD flows completed with 0 errors across Chromium and WebKit.

---

## Areas Explored & Journey Matrix

### 1. Teams Management & Per-Team Model Assignments
- **Personas Tested:** `operator` (System Admin), `carol` (Team Admin for `payments`), `zed` (Standard User).
- **Journeys Exercised:**
  - Navigated to `/teams/default/settings` across all 8 subtabs (`team`, `members`, `capacity`, `git`, `connections`, `models`, `executors`, `workflow`).
  - Inspected per-team model configurations (backend, models list, temperature, per-team token limits).
  - Switched to `payments` team as `carol`: verified team model settings access while confirming role isolation blocks Carol from deployment-wide admin overlays without admin credentials.
  - Verified resource limits and dispatch pause controls at team scope.

### 2. Remote Executor Management
- **Personas Tested:** `operator` (Admin Mode)
- **Journeys Exercised:**
  - Inspected `/admin` Sandboxes & Infrastructure tab and `/teams/default/settings/executors`.
  - Executed full remote executor token lifecycle:
    - Opened "Issue Token" dialog on `/teams/default/settings/executors`.
    - Filled token name `test-token-playwright-2842` and generated secret token.
    - Verified one-time token reveal modal with copy button and done confirmation.
    - Clicked "Revoke" on the newly issued token, confirmed action in the Revoke Executor Token modal (`Revoke Token` action), and verified immediate status update to revoked state.

### 3. Schedules, Templates & Suites Automation
- **Personas Tested:** `operator`, `zed`
- **Journeys Exercised:**
  - Navigated through `/schedules`, `/templates`, and `/suites`.
  - Schedules:
    - Opened existing schedule detail overlay ("Weekly Dependency & Vulnerability Audit").
    - Executed manual dispatch via "Run now".
    - Toggled pause/resume state.
  - Templates:
    - Created new template `nightly-e2e-exploratory-template` with title and description.
    - Verified form validation and persistence upon saving.
  - Suites:
    - Created new suite `Daily Exploratory Qualification Suite`.
    - Selected template steps via multiple combobox selection and saved successfully.

### 4. Merge Queue & CI Status Boards
- **Personas Tested:** `operator`
- **Journeys Exercised:**
  - Navigated to `/ci` status board, verified CI run list, status indicators, and filter controls.
  - Navigated to `/repos/acme/widgets` merge queue board with `-merge-queue-states` and `-mechanical-port` enabled.
  - Inspected batch states, queued PR entries, held gates, and mechanical porting markers.

### 5. Core Task Lifecycle & Bulk Operations
- **Personas Tested:** `operator`, `zed`
- **Journeys Exercised:**
  - Navigated to `/tasks`, tested search query synchronization in URL (`?q=merge&f=state:none:deferred|closed&f=kind:none:interactive`).
  - Verified browser history navigation (`back`/`forward`) maintains view state and query parameters without loss.
  - Opened `+ New task` form, tested validation requirements (disabled submission state when title or target repository is missing).
  - Selected `acme/widgets` target repository, filled title `Exploratory Discovery Task #2842` and instructions, and submitted.
  - Verified newly created task appears in the task list, opened task detail overlay, and posted an interactive comment.

### 6. Settings, Version Display, Preferences & Resilience
- **Personas Tested:** `operator`, `zed`
- **Journeys Exercised:**
  - Navigated to `/settings` user preferences overlay.
  - **Version Display Verification:** Verified `data-testid="settings-version-display"` displays version information matching commit `7f7564082` (`v1.2.0 · 003d115 · 2026-10-10 03:25Z`).
  - Appearance Preferences: Toggled theme mode to Dark and verified styling updates.
  - Timeline Preferences: Toggled conversation ordering to Oldest first.
  - Administrative Navigation: Traversed deployment Admin overlay across 8 core administrative tabs (`Sandboxes`, `Caches`, `General & Deployment`, `Engine & Concurrency`, `Sandbox & Infrastructure`, `Integrations & Auth`, `All Teams`, `All Users`).
  - Error Boundary Resilience:
    - Tested malformed and non-existent routes: `/tasks/99999`, `/ci/runs/99999`, `/repos/invalid/none`, `/tasks?state=invalidstate`, `/tasks?f=invalid:attr:val`.
    - Confirmed graceful 404 / fallback views without unhandled React errors or white screens.
    - Simulated offline network conditions and verified application stability.

### 7. Multi-Viewport & WebKit Responsiveness
- **Viewports & Engines:**
  - Desktop 1920×1080 (Chromium)
  - Desktop 1440×900 (Chromium)
  - Tablet 768×1024 (Chromium)
  - Mobile 375×812 (Chromium)
  - Mobile 375×812 (WebKit / Safari)
- **Results:**
  - Zero horizontal scroll overflow detected across all key views (`/tasks`, `/ci`, `/inbox`, `/settings`).
  - Form dialogs, cards, chips, and overlays resize and stack cleanly.

---

## Defect Discovery & Regression Analysis

- **Bugs Discovered:** 0 new defects.
- **Regressions Found:** 0 regressions.
- **Verification of Recent Candidate Features:**
  - `7f7564082` (*feat(cli,ui): add grain version command and display version in user settings*): Verified `settings-version-display` displays version string, short commit hash, and commit timestamp.
  - `dc810e321` (*test(ui): add e2e walkthrough for unsaved changes guard on task edit and custom fields*): Verified dirty state detection and confirmation prompt across Escape, Back, and browser navigation.
  - `8dd9070c8` (*preserve hook ordering across renders in CustomFieldsSection*): Verified stable render cycles when switching between tasks with and without custom fields.
  - `ba57807bf` (*show and control a team's own dispatch pause*): Verified team pause banner and controls.
  - `3c0b66dea` (*typed credential editor in Team settings*): Verified Git credentials and identity fields.

---

## Verification Commands & Test Artifacts

- Playwright Exploratory Test Suites:
  - Standard Exploratory Suite: `/tmp/exploratory/run_exploratory.js` (exercising 6 focus areas + multi-viewport Chromium/WebKit).
  - Deep Interactive Mutation Suite: `/tmp/exploratory/deep_exploratory_test.js` (task creation, comment posting, template creation, suite creation, schedule trigger, executor token issue & revoke lifecycle, settings toggling).
- Component & Unit Test Suite: `npm test` in `ui/` -> 193 test files passed, 3,179 tests passed.
