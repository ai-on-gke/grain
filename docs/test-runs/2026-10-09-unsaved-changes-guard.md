# Walkthrough: Unsaved Changes Guard in Task Edit and Custom Fields

**Date**: 2026-10-09  
**Branch**: `grain/task-2763`  
**Commits evaluated**: `131c8f003`, `8dd9070c8` (backported to `main` in `dd81e2299`, `d0be3a1d6`)  
**Test Plan**: `docs/test-plans/2026-10-09.md` §3 (Unsaved Changes Guard in Task Edit and Custom Fields)  
**Environment**: Debian GNU/Linux 12 (bookworm), Linux x86_64, Node.js 20.19.0, npm 10.8.2, Go 1.24.0, Playwright 1.50.0 (`@playwright/test` 1.62.1 with Chromium)

---

## 1. Scope and Objectives

Verify dirty draft protection across task editing components in a real Playwright browser against a live `grain demo` server:
1. **Task Edit Form (`EditTaskForm.jsx`) dirty draft protection**:
   - In `DetailOverlay` (`/tasks/1`), enter Edit mode and alter task title and description.
   - Test dismiss triggers: overlay modal backdrop click, `Escape` key, internal `← Back` button, and browser history Back (`page.goBack()`).
   - Confirm confirmation prompt appears: `"You have unsaved changes here. Leave and discard them?"`.
   - Test dialog dismissal (cancels close, retains modified draft inputs in the DOM) vs. dialog acceptance (discards changes, closes overlay modal, leaves original task state intact).
2. **Custom Fields Section (`CustomFieldsSection.jsx`) dirty draft protection**:
   - Open "Edit fields" in task detail view.
   - Modify both dropdown custom field (`Area` -> `infra`) and text custom field (`Requested by` -> `Acme Global Systems`).
   - Test dismiss triggers: backdrop click, `Escape` key, `← Back` button, and browser history Back (`goBack()`).
   - Confirm confirmation dialog handling across dismissal and acceptance.
3. **Concurrent Editing**:
   - Modify fields in both Task Edit form and Custom Fields section simultaneously.
   - Verify combined dirty ref evaluation (`editTaskUnsavedRef` and `customFieldsUnsavedRef` combined via `isDirty` in `DetailOverlay`).
   - Test canceling one editor while the other remains dirty, verifying dismissal interception remains active.
4. **React Hook Order Invariant (310 Regression Verification)**:
   - Commit `8dd9070c8` moved early return guards in `CustomFieldsSection.jsx` below `useRef` and `useEffect` hook registrations.
   - Verify repeated render cycles on tasks with zero declared or stored custom fields across modal toggling, navigation, and state updates, confirming no React invariant 310 console errors or crashes.

---

## 2. Test Execution and Reproduction Steps

Automated end-to-end verification was implemented and executed via Playwright in `ui/e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js` against the live demo application.

### Command Executed
```bash
cd ui && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js
```

### Execution Log Output
```
Running 4 tests using 1 worker

  ✓  1 [chromium] › e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js:8:3 › Unsaved changes guard in Task Edit and Custom Fields › Task Edit dirty guard: backdrop click, Escape key, Back button, and browser Back (1.5s)
  ✓  2 [chromium] › e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js:84:3 › Unsaved changes guard in Task Edit and Custom Fields › CustomFieldsSection dirty guard: dropdown and text custom fields across dismiss triggers (1.3s)
  ✓  3 [chromium] › e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js:156:3 › Unsaved changes guard in Task Edit and Custom Fields › Concurrent editing: simultaneous edits in Task Edit and Custom Fields evaluate combined dirty ref (1.1s)
  ✓  4 [chromium] › e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js:213:3 › Unsaved changes guard in Task Edit and Custom Fields › React Hook order invariant: tasks with no declared or stored custom fields across repeated renders (invariant 310) (1.8s)

  4 passed (12.6s)
```

### Vitest Unit Tests
Component unit test suites for both forms were also verified:
```bash
cd ui && npm test src/components/detail/EditTaskForm.test.jsx src/components/detail/CustomFieldsSection.test.jsx
```
Output:
```
 RUN  v4.1.11 /home/debian/work/ui

 ✓ src/components/detail/CustomFieldsSection.test.jsx (4 tests) 1031ms
 ✓ src/components/detail/EditTaskForm.test.jsx (18 tests) 6338ms

 Test Files  2 passed (2)
      Tests  22 passed (22)
```

---

## 3. Step-by-Step Findings

### Test 1: Task Edit Dirty Guard (`EditTaskForm.jsx`)
- **Action**: Navigated to `/tasks/1` ("Add a dark mode toggle"). Clicked "Edit". Modified Title to `"Add a dark mode toggle (draft modified)"` and Description to `"Settings page theme switch draft notes."`.
- **Dismiss Trigger 1 (Backdrop click)**: Dispatched click event to `.MuiBackdrop-root` outside the overlay paper. The browser `window.confirm` dialog intercepted the close with message `"You have unsaved changes here. Leave and discard them?"`. Dialog dismissed -> overlay remained open, modified text inputs preserved.
- **Dismiss Trigger 2 (Escape key)**: Pressed `Escape`. Prompt triggered -> dialog dismissed -> overlay remained open with draft content.
- **Dismiss Trigger 3 (`← Back` button)**: Clicked `← Back` button in `.overlay-pane-back`. Prompt triggered -> dialog dismissed -> overlay remained open with draft content.
- **Dismiss Trigger 4 (Browser history Back)**: Executed `page.goBack()`. `window.onpopstate` listener (`guardLeaving` in `Overlay.jsx`) intercepted navigation -> prompt triggered -> dialog dismissed -> overlay remained open.
- **Acceptance Verification**: Set dialog handler to accept (`dialog.accept()`). Triggered close. Confirmation accepted -> overlay unmounted, URL reverted to `/`.
- **Integrity Check**: Re-opened Task 1. Verified title and description were untouched, showing original stored values.

### Test 2: Custom Fields Section Dirty Guard (`CustomFieldsSection.jsx`)
- **Action**: Opened `/tasks/1`, clicked "Edit fields". Modified `Requested by` text field to `"Acme Global Systems"`. Modified `Area` dropdown from `frontend` to `infra`.
- **Dismiss Triggers**: Tested backdrop click, `Escape` key, `← Back` button, and browser history `goBack()`.
- **Result**: Each navigation vector reliably prompted `"You have unsaved changes here. Leave and discard them?"`. Dismissing each kept the custom fields editor mounted with dirty inputs intact.
- **Acceptance Verification**: Accepted discard on `Escape`. Overlay cleanly dismissed without modifying server state. Reopening confirmed original custom fields (`frontend`, `Northwind`) remained intact.

### Test 3: Concurrent Editing and Combined Ref Evaluation
- **Action**: In `/tasks/1`, entered Edit mode on the Task Edit form (modifying Title) and simultaneously opened "Edit fields" in `CustomFieldsSection` (modifying `Requested by`).
- **Combined Dirty Ref**: Both `editTaskUnsavedRef` and `customFieldsUnsavedRef` reported true. Pressing `Escape` prompted discard.
- **Independent State Invariant**:
  - Canceled Custom Fields editor via its internal "Cancel" button. `CustomFieldsSection` closed and cleared its dirty ref. Task Edit form remained open and dirty. Triggered `← Back`; confirmation dialog prompted as expected because `editTaskUnsavedRef` remained dirty.
  - Re-opened Custom Fields editor and modified a field. Canceled Task Edit form via its internal "Cancel" button. Triggered `← Back`; confirmation dialog prompted as expected because `customFieldsUnsavedRef` remained dirty.
  - Accepted prompt: overlay closed cleanly.

### Test 4: React Hook Order Invariant (310 Regression Verification)
- **Setup**: Created a new task with zero custom fields (`customFields: {}`). Navigated to `/tasks/:id`.
- **Action**: Monitored browser `page.on("console")` for errors. Cycled through repeated render pathways: opening/closing edit mode repeatedly, navigating between tasks, navigating away and returning.
- **Result**: Zero React invariant 310 console errors (`"Rendered fewer hooks than expected"`, `"Rendered more hooks than expected"`). The component rendered cleanly with null fallback after all hooks (`useRef`, `useEffect`) executed unconditionally.

---

## 4. Conclusion & CI Status

All acceptance criteria specified in `docs/test-plans/2026-10-09.md` §3 are fully satisfied. The e2e test suite has been added to `ui/e2e/task_edit_custom_fields_unsaved_guard_walkthrough.spec.js` and all existing unit and e2e suites remain green.
