# Daily Exploratory UI Testing & Defect Discovery Report (2026-10-09)

## Executive Summary

- **Date:** 2026-10-09
- **Target Environment:** Staging Grain Web UI (`grain demo` with embedded store, `-observability -merge-queue-states -sensitive-hold -mechanical-port`)
- **Git Commit / Branch:** `d83c53b47` on `grain/task-2747` (tracking `candidate`)
- **Browsers Tested:** Chromium (headless & mobile emulation), WebKit (Safari mobile emulation)
- **Viewports Covered:** Desktop 1920×1080, Desktop 1440×900, Tablet 768×1024, Mobile 375×812
- **Test Personas:**
  - `operator` (System Admin / Deployment Administrator)
  - `zed` (Standard User / Non-Admin)
  - `carol` (Team Administrator for `payments` team, non-deployment admin)
- **Overall Verdict:** **PASSED / CLEAN**
  - No new UI bugs or visual/functional regressions were detected across the staging environment.
  - All recent fixes (unsaved edits guards in DetailOverlay/CustomFieldsSection, hook ordering preservation, team settings scope enforcement, long token wrapping) are verified in place and functioning reliably.
  - Automated UI test suite passed 100% (193 test files, 3,174 tests passed).

---

## Areas Explored & Journey Matrix

### 1. Teams Management & Per-Team Model Assignments
- **Personas Tested:** `operator` (System Admin), `carol` (Team Admin for `payments`), `zed` (Standard User).
- **Journeys Exercised:**
  - Navigated to `/teams/payments/settings/models` as `carol`: verified that deployment-wide settings (Max Agent Turns, Prompt Extension, Task Branch Template, Deployment ID, Git Commit Identity) are completely hidden from team-only administrators and no unauthorized `PUT /api/settings` calls are triggered on field blur.
  - Tested team switching between `default` and `payments` teams across navigation rails and headers.
  - Exercised New Team creation modal (`NewTeamDialog`): verified that leaving Members and Admins blank is accepted by the client, correctly letting the server default creator to team admin.
  - Verified resource limit inputs (Max Workers, Max Mergers, Max CI Jobs) and team-scoped dispatch pause toggles.

### 2. Remote Executor Management
- **Personas Tested:** `operator` (Admin Mode)
- **Journeys Exercised:**
  - Navigated to `/admin/executors` and `/teams/default/settings/executors`.
  - Created a new remote executor token with a 100+ character continuous unbroken string name (`very-long-unbroken-executor-token-name-without-any-spaces-to-verify-layout-overflow-handling-1234567890`).
  - Verified token card rendering and one-time secret modal: ensured `overflow-wrap: anywhere` properly wraps unbroken names without causing horizontal page scrolling or pushing the Active/Revoked status chips off-screen at 375px mobile viewport.
  - Tested token revocation workflow and confirmation dialog layout.

### 3. Schedules, Templates & Suites Automation
- **Personas Tested:** `operator`, `zed`
- **Journeys Exercised:**
  - Navigated through `/schedules`, `/templates`, and `/suites`.
  - Opened schedule detail overlay for existing schedules, edited title and configuration.
  - Verified unsaved changes guard: pressing Escape or clicking Back displays confirmation dialog (`"You have unsaved changes here. Leave and discard them?"`). Dismissing keeps edits; accepting discards changes and closes overlay.
  - Verified schedule pause/resume and "Run now" actions properly prompt for unsaved edits before executing.
  - Tested template parameter inspection and creation flows.

### 4. Merge Queue & CI Status Boards
- **Personas Tested:** `operator`
- **Journeys Exercised:**
  - Navigated to `/ci` status board and inspected active/recent runs across repositories.
  - Deep-linked into individual run details (`/ci/runs/...`), inspecting job timeline, step status badges, and terminal output.
  - Verified `/repos/acme/widgets` repository merge queue board with `-merge-queue-states` and `-mechanical-port` enabled.
  - Checked responsive collapsing of CI job list and status badges on tablet (768×1024) and mobile (375×812) viewports.

### 5. Core Task Lifecycle & Bulk Operations
- **Personas Tested:** `operator`, `zed`
- **Journeys Exercised:**
  - Exercised "+ New Task" creation modal, repo selection dropdown, capability selectors, and task draft caching.
  - Tested Task Detail Overlay on `/tasks/18`: verified `CustomFieldsSection` hook stability, capability toggles, and edit task form.
  - Verified unsaved edit confirmation guard in `EditTaskForm`: modified task title, verified Escape triggers confirmation prompt, and verified discard behavior.
  - Multi-selection & Bulk Actions: Selected multiple task rows via checkboxes on `/tasks`, verified bulk action toolbar visibility and counter, and verified "Clear selection".
  - Search and Filter URL synchronization: Tested `?state=queued&q=Port`, verified back and forward history buttons accurately restore filter state and search queries without losing view state.

### 6. Settings, Storage & Error Boundary Resilience
- **Personas Tested:** `operator`, `carol`
- **Journeys Exercised:**
  - Navigated to `/settings` user profile and preferences overlay.
  - Tested invalid and non-existent route resilience:
    - `/tasks/99999` & `/tasks/not-a-number`: Graceful 404 handling without unhandled React errors or white screens.
    - `/ci/nonexistent-run-id`: Proper error message rendered in view container.
    - `/teams/nonexistent/settings/models`: Clean fallback handling.
    - `/repos/invalid/missing-repo`: Clean 404 message.
  - Verified responsive layout across Chromium and WebKit on mobile viewport (375×812) with zero detected horizontal scroll overflows (`docWidth === scrollWidth === 375`).

---

## Defect Discovery & Regression Analysis

- **Bugs Discovered:** 0 new defects.
- **Regressions Found:** 0 regressions.
- **Verification of Recent Candidate Fixes:**
  - `8dd9070c8` (*preserve hook ordering across renders in CustomFieldsSection*): Verified stable render cycles when transitioning between tasks with and without custom fields.
  - `131c8f003` (*ask before leaving an edited task or custom fields*): Verified dirty state detection and confirmation prompt across Escape, Back, and browser Back navigation.
  - `ba57807bf` (*show and control a team's own dispatch pause*): Verified team pause banner and controls.
  - `3c0b66dea` (*typed credential editor in Team settings*): Verified Git credentials and identity fields.

---

## Verification Commands & Test Artifacts

- Playwright browser suite: Chromium and WebKit executions across 4 viewports (`/tmp/exploratory/deep_exploratory_test.js`, `/tmp/exploratory/test_webkit_mobile.js`).
- Unit & Component Test Suite: `npm test` in `ui/` -> 193 test files passed, 3,174 tests passed.
