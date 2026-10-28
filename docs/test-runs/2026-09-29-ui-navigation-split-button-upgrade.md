# Walkthrough: UI Navigation, Sidebar Split-Button Intake & In-App Upgrade Dialog (§4)

## Build and Environment

- Tested branch: `grain/task-1079`
- Debian 12 sandbox; Node 20.19.0; Vite 6.4.3; Playwright 1.62.1; Chromium headless.
- Built production frontend with `npm run build` (`pkg/ui/static/`), executed against real backend and `grain demo` embedded store.
- Executed full Vitest suite (156 test files, 2,692 tests passed) and end-to-end browser walkthrough suite via Playwright (`ui/e2e/ui-navigation-split-button-upgrade.spec.js`, `ui/e2e/sidebar-navigation.spec.js`, `ui/e2e/upgrade.spec.js`).
- Verified full Go test suite (`go test ./pkg/... ./cmd/...`).

## Walkthrough and Verification Results

1. **Sidebar Split-Button Intake Dropdown**:
   - Verified that the `+ New task` button in the left sidebar rail renders as a split button (`ButtonGroup`) containing the primary action button and an attached dropdown trigger (`aria-label="Create options"`).
   - Verified clicking the dropdown chevron reveals the `#create-options-menu` with quick creation actions: "New task", "New session", "New team", and "New board".
   - Verified clicking "New session" launches the `NewSessionDialog` modal for creating standing chat sessions.
   - Verified clicking "New task" launches `NewTaskOverlay` modal for filing tasks.
   - Validated component tests in `ui/src/components/Sidebar.test.jsx` and end-to-end Playwright tests in `ui/e2e/ui-navigation-split-button-upgrade.spec.js`.

2. **Template Intake & Pre-Populated Parameter Interpolation**:
   - Verified template creation with declared inputs (e.g. `service_name`, `target_env`, `service`) via `/templates`.
   - Verified selecting a template via "Use…" action correctly opens `UseTemplateOverlay` / `NewTaskOverlay` with pre-populated parameters, capabilities, and repository targets.
   - Verified template input substitution upon creation correctly files the task with interpolated fields and opens the task detail overlay.

3. **In-App Upgrade Confirmation Dialog & Progress**:
   - Verified System Settings → Admin → Upgrade panel (`UpgradePanel.jsx`) correctly renders currently running version/image information (`Now running ghcr.io/bwsalmon/grain/grain:main`).
   - Verified that when active tasks are running (`inFlightRuns > 0`), initiating an immediate upgrade displays an active run alert (`.upgrade-in-flight`) and prompts the operator with an in-app confirmation modal (`Dialog` `#confirm-upgrade-dialog-title`) warning that active runs will be discarded.
   - Verified cancelling the confirmation dialog aborts the upgrade request without sending API mutations.
   - Verified confirming the upgrade executes `POST /api/upgrade` and transitions into running state with animated `GrainMark` canvas indicator.
   - Verified idle scheduled upgrades (`Wait until this deployment is idle`) allow waiting for tasks and CI to finish cleanly.

4. **Merged Task Dissolution Animation Removal & DOM Cleanliness**:
   - Verified that merged task dissolution animations (`MergedPatternRow`, `.dissolving-task`, and related keyframes) have been cleanly removed from `TaskBoard.jsx`, `TaskList.jsx`, and `style.css` without visual regressions or layout reflow jank.
   - Confirmed rapid task transitions across queued, running, merged, and closed states complete without stale DOM nodes or leftover animation artifacts.

5. **Dedicated Eigenmode Glyphs for Suites & Metrics**:
   - Verified dedicated Chladni plate eigenmodes are assigned and rendered:
     - `suites`: mode `3·6 (−)` at zoom 1.4 ("six lobes in clustered pairs").
     - `metrics`: mode `1·5 (−)` at zoom 1.5 ("tiered gauge between caps").
   - Verified sidebar navigation buttons for Suites and Metrics render these distinct SVG vector paths via `ItemGlyph`.

## Test Results Summary

- **Frontend Unit & Component Tests**: 156/156 test files passed (2,692/2,692 tests passed).
- **Playwright E2E Tests**:
  - `ui/e2e/ui-navigation-split-button-upgrade.spec.js`: 5/5 tests passed.
  - `ui/e2e/sidebar-navigation.spec.js`: 5/5 tests passed.
  - `ui/e2e/upgrade.spec.js`: 3/3 tests passed.
- **Backend Go Tests**: All packages passed across `pkg/...` and `cmd/...`.
