# Teams UI Walkthrough & Verification Run

## Overview
Comprehensive test run and browser-driven end-to-end walkthrough for Teams UI elements, bulk editing, templates, and multi-team lifecycle operation across the entire web application.

## Scope and environment

- Repository: `bwsalmon/grain`
- Branch: `grain/task-445`
- Host: Debian bookworm, Linux x86_64
- Go `1.26.6`; Node `20.19.0`; npm `10.8.2`
- Vitest `4.1.11`, React Testing Library `16.3.3`, Playwright `1.57.0` (Chromium headless)
- Server: Live `grain demo` instance managed per Playwright worker fixture
- Production Build: `make frontend` compiling Vite bundles into `pkg/ui/static`

## Subtasks Filed

Per task requirements to split the work into subtasks where each task brings up a new Grain instance to walkthrough and fix bugs:

1. `teams-walkthrough-mgmt`: "Walk through Teams management, custom slugs, and concurrency limit configuration UI"
2. `teams-walkthrough-tasks`: "Walk through task intake, details overlay, and bulk edit team assignment UI"
3. `teams-walkthrough-schedules-sessions`: "Walk through schedules, automation templates, and interactive console sessions team scoping UI"
4. `teams-walkthrough-boards-ci`: "Walk through multi-team boards, CI run isolation, and global team selector persistence UI"

## Standing Declarations & Documentation

- **Standing Walkthrough Declaration**: Added `.grain/templates/ui-walkthrough-teams.yml` declaring comprehensive Teams UI walkthrough coverage, steps, preconditions, and validation protocol.
- **Release Suite Integration**: Added `ui-walkthrough-teams` to `.grain/suites/release-ui-walkthrough.yml` alongside the existing 6 walkthrough chunks.
- **Documentation Update**: Updated `docs/ui-walkthrough.md` to declare seven walkthrough chunks instead of six and documented `ui-walkthrough-teams.yml`.

## Feature Implementations & Bug Fixes

- **Bulk Edit Team Support**: Added `team` selector and state management in `ui/src/components/BulkEditOverlay.jsx` allowing batch reassignment of target teams across multiple selected tasks via atomic `PATCH /api/tasks/:id` payloads.
- **Bulk Edit Unit Test**: Added unit test in `ui/src/components/BulkEditOverlay.test.jsx` confirming team row tick, dropdown selection, and PATCH payload generation.
- **Playwright Browser Walkthrough**: Added comprehensive browser-driven test suite in `ui/e2e/teams-walkthrough.spec.js` exercising the real UI against a live Grain instance.

## Test Results

### 1. Playwright End-to-End Walkthrough (`ui/e2e/teams-walkthrough.spec.js`)

Executed in headless Chromium against an ephemeral `grain demo` server:
```
Running 1 test using 1 worker
  ✓  1 [chromium] › e2e/teams-walkthrough.spec.js:6:3 › Teams UI and Multi-Team Lifecycle Operator Walkthrough › thoroughly exercises teams management, global team selector, task intake, detail overlays, bulk edit, and schedule scoping in a real browser (10.9s)

  1 passed (20.0s)
```

Covered UI scenarios:
1. **Teams Management in Settings**:
   - Navigated to Settings > Administrator settings > All Teams.
   - Verified default team card rendering with limit chips and protections.
   - Opened `Create Team` dialog and created team `qa-infra` with custom slug, admins (`debian, alice, admin`), members (`debian:admin, alice:admin, tester:member`), and custom limits (Max Workers: 4, Max Mergers: 2, Max CI Jobs: 8).
   - Edited `qa-infra` team limits to Max Workers: 6 and verified persisted values on the card.
2. **Global Team Selector in Sidebar**:
   - Tested `#team-selector` dropdown.
   - Switched to `QA & Infrastructure (qa-infra)` and verified `localStorage.getItem("grain.selectedTeam") === "qa-infra"`.
   - Switched back to `All teams`.
3. **Task Intake & Details**:
   - Filed a new task targeting `QA & Infrastructure (qa-infra)`.
   - Opened task in DetailOverlay and verified Team metadata display.
   - Clicked Edit in DetailOverlay, changed target team to `Default Team`, and saved.
4. **Bulk Edit Target Team Assignment**:
   - Selected multiple tasks with checkboxes in task list.
   - Opened Bulk Edit overlay, ticked `Team`, selected `QA & Infrastructure`, and applied bulk update.
5. **Schedule Creation with Team Scoping**:
   - Navigated to Schedules and opened `+ New schedule`.
   - Created schedule scoping tasks to `QA & Infrastructure (qa-infra)`.
6. **Deletion Safeguards and Cleanup**:
   - Verified `Default Team` card has no delete button.
   - Reassigned tasks back to default team via Bulk Edit.
   - Deleted `qa-infra` team from Settings > All Teams.

### 2. Frontend Unit Test Suite (Vitest)

```
Test Files  138 passed (138)
     Tests  2466 passed (2466)
  Duration  291.34s
```

All 2,466 unit tests in 138 test files passed without error.
