# UI Navigation Redesign, Bottom User Profile & Merged Task Dissolve Walkthrough

## Build and Environment

- Tested branch: `grain/task-954`
- Debian 12 sandbox; Node 20.19.0; Vite 6.4.3; Playwright 1.62.1; Chromium.
- Built production frontend with `npm run build` (`pkg/ui/static/`), executed against real backend and `grain demo` embedded store.
- Ran full unit test suite (150 test suites, 2,629 tests) via Vitest and end-to-end browser walkthrough test suite via Playwright.

## Walkthrough and Verification Results

1. **User Profile Tile Relocation**:
   - Relocated user identity tile and settings shortcut (`data-testid="user-tile"`) from the top brand section to the bottom footer rail across Desktop and Mobile drawer layouts.
   - Verified that user profile sits directly below Admin/Metrics navigation list buttons and above the build version stamp.
   - Verified gear icon and tile click opens User Settings dialog properly on both desktop viewports (1440×900) and mobile drawers (390×844).
   - Validated DOM structure and vertical coordinate assertions in `ui/src/components/Sidebar.test.jsx`, `ui/e2e/sidebar-navigation.spec.js`, and `ui/e2e/track6-navigation-merged-dissolve.spec.js`.

2. **Merged Task Backlog Grouping & Dissolve Animation**:
   - Restored and verified dedicated Merged tasks filtering and breakdown navigation in the left sidebar rail.
   - Implemented `MergedPatternRow` rendering 16 Chladni eigenmodes with physics-based grain particle offsets when tasks transition into merged state.
   - Updated `TaskList.jsx` and `TaskBoard.jsx` state tracking with unconditional `prevTasksRef` reference updates on every render pass to prevent stale diffs and avoid infinite re-render cycles.
   - Updated `ui/src/board.js` (`groupIntoColumns`) to retain dissolving task cards in completed/closed columns during animation.
   - Verified CSS keyframe transitions (`task-row-merge-collapse` and `board-card-merge-collapse`) with graceful opacity fading and scale transitions.

3. **Pinned Navigation Customization & Persistence**:
   - Validated custom icon (ItemGlyph / Chladni pattern) and color assignment for pinned navigation items.
   - Verified that customized pinned navigation entries persist reliably across browser reloads (`page.reload()`).

4. **Live CI Output Streaming & Task Detail**:
   - Verified embedded live CI output streaming frame (`LiveAttemptTranscript` & `GrainCIStatus`) within task detail overlay during active runs.
   - Verified prompt view dialog opens and closes with focus restoration.

5. **Auto-Hiding Custom Scrollbars**:
   - Verified auto-hiding custom scrollbar styles (`scrollbar-width: thin`, `overflow-y: auto`) across sidebar, transcripts, and modals without layout shift or horizontal document overflow.

## Test Results Summary

- **Unit & Integration Tests**: 150/150 test files passed (2,629/2,629 tests passed).
- **Playwright E2E Tests**:
  - `ui/e2e/track6-navigation-merged-dissolve.spec.js`: 5/5 tests passed.
  - `ui/e2e/sidebar-navigation.spec.js`: 5/5 tests passed.
- **Lint & Formatting**: `npm run format` ran cleanly across all modified and new files.
