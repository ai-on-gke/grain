# Test Run Report: Sidebar Navigation Restructuring & Foldable Controls

- **Date**: 2026-09-25
- **Subject**: Sidebar Navigation Restructuring, Top Pinned Container, Foldable Boards & Sessions (`docs/test-plans/2026-09-25.md §2`)
- **Environment**: Linux x86_64 sandbox, Node.js v20.19.0, Vite v6.4.3, Vitest v4.1.11, Playwright (Chromium & WebKit) with pre-cached browsers at `/var/cache/playwright`, Go 1.24.
- **Target Branch**: `grain/task-665` on `bwsalmon/grain`
- **Result**: PASSED (143/143 unit test files, 2,538/2,538 unit tests; 183 passed, 1 skipped in Playwright E2E suite across Desktop, Tablet, and Mobile viewports; all Go packages passing).

---

## 1. Executive Summary

This test run executes the comprehensive end-to-end and unit verification for the sidebar navigation restructuring and foldable controls specified in `docs/test-plans/2026-09-25.md §2`.

The test run verified:
1. **Visual DOM Hierarchy & Layout Ordering**:
   - Primary `+ New task` action button and `Pinned` section render at the top below the user identity / brand mark.
   - Team-scoped destinations (`All tasks`, `Queued`, `Running`, `Finished`, `Boards`, `Sessions`, `Schedules`, `Templates`, `Suites`) are strictly enclosed inside the team container (`data-testid="team-section"`).
   - Global navigation destinations (`Repos`, `CI`, `Sandboxes`, `Caches`) render outside and directly below the team container.
   - Global footer destinations (`Settings`, `System`, `Metrics`) render in the fixed bottom rail.
2. **Pinned Items Management & Persistence**:
   - The "Customize pinned navigation" modal allows pinning Views, Custom Boards, and Sessions.
   - Pinned items appear in the dedicated pinned container at the top of the rail.
   - Navigation links route cleanly to the target views and boards.
   - Pinned selections persist deterministically across full page reloads via `localStorage` (`grain.pinnedNav.<username>`).
3. **Foldable Sections & LocalStorage State Persistence**:
   - Tasks breakdown, Boards, and Sessions sections support independent collapsible folding.
   - Toggling fold updates `aria-expanded` attributes (`true` <-> `false`) and shows/hides children without causing DOM jitter or side effects.
   - Fold states persist across full page reloads using `grain.sidebar.tasksExpanded`, `grain.sidebar.boardsExpanded`, and `grain.sidebar.sessionsExpanded`.
4. **Keyboard Accessibility**:
   - Full keyboard operability using `Tab` navigation.
   - Triggering fold toggles via both `Space` and `Enter` keys smoothly alters state and updates `aria-expanded`.
5. **Touch Targets & Pointer Disambiguation**:
   - On coarse pointer / touch viewports (tablet & mobile), section headings and chevron fold toggles meet the minimum 40px/44px touch target guidelines.
   - Tapping the fold chevron expands/collapses without triggering navigation, while tapping the heading link navigates to the respective index page (e.g. `/boards`).

---

## 2. Test Execution Matrix

| Test Suite | Spec File / Package | Scenario / Objective | Result | Execution Time |
| :--- | :--- | :--- | :--- | :--- |
| **Unit Tests** | `ui/src/components/Sidebar.test.jsx` | DOM hierarchy, section fold state, badge counts, team filtering | PASSED (65/65) | 1.9s |
| **Unit Tests** | `ui/src/components/CustomizePinnedOverlay.test.jsx` | Add view, add board, unpin item, reorder items | PASSED (3/3) | 1.2s |
| **Unit Tests** | `ui/src/pinnedNav.test.js` | Pinned navigation storage serialization and migration | PASSED (11/11) | 21ms |
| **E2E Tests** | `ui/e2e/sidebar-navigation.spec.js` | 1. Visual DOM Layout Hierarchy (User -> New Task -> Pinned -> Team -> Global -> Footer) | PASSED | 727ms |
| **E2E Tests** | `ui/e2e/sidebar-navigation.spec.js` | 2. Pinned Items: customization modal, item rendering, navigation, and persistence | PASSED | 3.7s |
| **E2E Tests** | `ui/e2e/sidebar-navigation.spec.js` | 3. Fold and Unfold: Boards, Sessions, and Tasks with localStorage persistence | PASSED | 1.2s |
| **E2E Tests** | `ui/e2e/sidebar-navigation.spec.js` | 4. Keyboard Accessibility: Tab focus, Enter/Space toggles, aria-expanded transitions | PASSED | 658ms |
| **E2E Tests** | `ui/e2e/sidebar-navigation.spec.js` | 5. Coarse Pointer / Touch Viewport Target Sizing & Tap Disambiguation | PASSED | 672ms |
| **Full E2E Matrix** | `ui/e2e/*.spec.js` (184 tests) | Comprehensive UI E2E test suite across Desktop (Chromium), Phone & Tablet (Chromium & WebKit) | PASSED (183 passed, 1 skipped) | 3m 35s |
| **Full Unit Suite**| `ui/src/**/*.test.jsx` (143 files) | All React and utility unit tests | PASSED (2,538 passed) | 2m 59s |
| **Go Test Suite**  | `./...` (All Go packages) | Backend model, store, UI server, orchestrator, MCP handlers | PASSED | 4m 12s |

---

## 3. Detailed Verification Walkthrough & Findings

### 3.1 Visual DOM Hierarchy Assertions
The DOM tree layout was validated against strict document order specifications:
```
<aside>
  ├── [1] User Identity & Brand Mark (heading "grain", current user button)
  ├── [2] Primary Action: "+ New task" button
  ├── [3] Pinned Container: "Pinned" heading + "Customize pinned items" button + pinned items
  ├── [4] Team Container: <Box data-testid="team-section">
  │     ├── Team Selector dropdown
  │     ├── All tasks & status filter sub-items (Queued, Running, Finished)
  │     ├── Foldable Boards section
  │     ├── Foldable Sessions section
  │     └── Team destinations (Schedules, Templates, Suites)
  ├── [5] Global Navigation Links:
  │     ├── Repos
  │     ├── CI
  │     ├── Sandboxes
  │     └── Caches
  └── [6] Footer:
        ├── Settings
        ├── System
        └── Metrics
</aside>
```
Document position assertions (`compareDocumentPosition & Node.DOCUMENT_POSITION_FOLLOWING`) confirmed the exact sequential rendering order without DOM elevation or visual dislocation.

### 3.2 Pinned Items Customization & Navigation
1. Opened the pinned customization overlay via `Customize pinned items`.
2. Added the `CI` view and a dynamically seeded custom board (`Pinned Board <timestamp>`).
3. Confirmed both items appeared under the Pinned section.
4. Clicked the pinned `CI` button and asserted navigation to `/ci`.
5. Clicked the pinned board button and asserted navigation to `/boards/:id`.
6. Reloaded the page and verified the pinned items persisted in `localStorage` under `grain.pinnedNav.<username>`.

### 3.3 Foldable Sections & State Persistence
1. Toggled the Tasks breakdown fold button (`data-testid="toggle-tasks-button"`):
   - Confirmed `aria-expanded` switched from `"true"` to `"false"`.
   - Confirmed child status items (`Queued`, `Running`, `Finished`) collapsed, while `All tasks` remained visible as the summary destination.
   - Asserted `localStorage.getItem("grain.sidebar.tasksExpanded") === "false"`.
2. Toggled the Boards fold button (`data-testid="toggle-boards-button"`):
   - Confirmed child boards collapsed.
   - Asserted `localStorage.getItem("grain.sidebar.boardsExpanded") === "false"`.
3. Toggled the Sessions fold button (`data-testid="toggle-sessions-button"`):
   - Confirmed sessions collapsed.
   - Asserted `localStorage.getItem("grain.sidebar.sessionsExpanded") === "false"`.
4. Triggered a hard page reload:
   - Verified that Tasks, Boards, and Sessions remained collapsed with `aria-expanded="false"`.
5. Re-expanded Boards and Tasks:
   - Confirmed state restored to `aria-expanded="true"` and `localStorage` updated to `"true"`.

### 3.4 Keyboard Accessibility
1. Focused each toggle button (`toggle-tasks-button`, `toggle-boards-button`, `toggle-sessions-button`) directly via keyboard.
2. Verified `Space` key toggles fold state between expanded and collapsed.
3. Verified `Enter` key toggles fold state between expanded and collapsed.
4. Confirmed screen-reader accessible `aria-expanded` attribute transitions immediately without DOM recreation.

### 3.5 Touch Target Sizing & Tap Disambiguation (Tablet & Mobile)
1. Tested under `@media (pointer: coarse)` with `TABLET` viewport (`820x1180`, `hasTouch: true`, `isMobile: true`).
2. Verified all `.sidebar-section-heading` and `.sidebar-section-toggle` elements maintain bounding boxes `>= 40px` in height and width.
3. Verified event target isolation:
   - Tapping the chevron button folds/unfolds the section without navigation.
   - Tapping the section heading text button navigates to the target page (`/boards`).

---

## 4. Environment & Command Logs

### Frontend Build
```bash
$ make frontend
find pkg/ui/static -mindepth 1 -not -name '.gitignore' -not -name '.gitkeep' -not -name 'placeholder.html' -delete
cd ui && npm ci && npm run build
✓ 1504 modules transformed.
../pkg/ui/static/index.html                     3.65 kB │ gzip:   1.72 kB
../pkg/ui/static/assets/index-Cou8kFkb.css     89.01 kB │ gzip:  14.77 kB
../pkg/ui/static/assets/index-BnRzNOxk.js   1,610.21 kB │ gzip: 476.02 kB
✓ built in 4.77s
```

### Playwright E2E Test Suite Run
```bash
$ cd work/ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/sidebar-navigation.spec.js
Running 5 tests using 1 worker

  ✓  1 [chromium] › e2e/sidebar-navigation.spec.js:24:3 › Sidebar Navigation Restructuring & Foldable Controls › 1. Visual DOM Layout Hierarchy: Top Pinned -> Team Container -> Global Navigation -> Footer (727ms)
  ✓  2 [chromium] › e2e/sidebar-navigation.spec.js:133:3 › Sidebar Navigation Restructuring & Foldable Controls › 2. Pinned Items: customization modal, item rendering, navigation, and persistence (3.7s)
  ✓  3 [chromium] › e2e/sidebar-navigation.spec.js:211:3 › Sidebar Navigation Restructuring & Foldable Controls › 3. Fold and Unfold: Boards, Sessions, and Tasks with localStorage persistence across reloads (1.2s)
  ✓  4 [chromium] › e2e/sidebar-navigation.spec.js:319:3 › Sidebar Navigation Restructuring & Foldable Controls › 4. Keyboard Accessibility: Tab focus, Enter/Space toggles, aria-expanded transitions (658ms)
  ✓  5 [chromium] › e2e/sidebar-navigation.spec.js:370:3 › Sidebar on Tablet & Coarse Pointer › 5. Coarse Pointer / Touch Viewport Target Sizing & Tap Disambiguation (672ms)

  5 passed (13.0s)
```

### Unit Test Suite Run
```bash
$ cd work/ui && npm test
Test Files  143 passed (143)
Tests       2538 passed (2538)
Duration    179.78s
```

---

## 5. Conclusion
All criteria set forth in `docs/test-plans/2026-09-25.md §2` for Sidebar Navigation Restructuring, Top Pinned Container, and Foldable Boards & Sessions have been verified and confirmed passing.
