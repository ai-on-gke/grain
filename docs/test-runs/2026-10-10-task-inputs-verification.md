# Verification Walkthrough: Task Intake, Detail Edit, and Filter Inputs

**Date**: 2026-10-10  
**Suite**: `ui/e2e/task_inputs_verification_walkthrough.spec.js`  
**Target Areas**: Task Creation / Intake (`NewTaskOverlay.jsx`, `CustomFields.jsx`), Task Detail & Edit (`EditTaskForm.jsx`, `Target.jsx`, `Actions.jsx`, `TaskChat.jsx`), Task Filtering & Narrowing (`NarrowingBar.jsx`, `taskFilters.js`).  
**Environment**: Chromium browser running against live embedded `grain demo` instance (`-addr 127.0.0.1:0 -open=false`).

---

## Executive Summary

An end-to-end audit and interactive verification of every input element across task intake, detail edit, and filter surfaces was conducted. All input controls were verified in a real browser for:
1. Keyboard focus, character typing, and tab ordering.
2. Form validation error states and submit gating (e.g. required title/repo constraints).
3. Draft auto-save and discard reset operations.
4. Rich custom field types (text, number, date, enum, multi-select autocomplete, and boolean).
5. Advanced settings (quotas, model, agent backend, network access, prompt override, interactive session checkbox).
6. Detail in-place editing, Target repos/branches with blur persistence, auto-merge toggle, and conversation chat composer.
7. Search field live filtering, condition editor attribute/operator/value pickers, and filter removal.
8. Visual readability and theme styling consistency across both Light and Dark themes (`data-theme="light"` / `data-theme="dark"`).

All automated test scenarios completed cleanly with zero regressions.

---

## 1. New Task Intake (`NewTaskOverlay.jsx`)

### 1.1 Form Validation & Submit Gating
- Verified that on opening the `+ New task` dialog, the **Create task** button is disabled by default (`disabled` attribute) while required inputs (Title and valid repository configuration) are empty.
- Once a valid title is typed and repository selection is satisfied (either selecting a declared repo like `acme/widgets` or selecting `No repo`), the **Create task** button immediately transitions to enabled.

### 1.2 Input Operation & Tab Ordering
- Focused `Title` input via keyboard focus; confirmed `:focus` and `:focus-visible` styling.
- Tabbed directly from `Title` (`TextField`) into `Description` (`TextField` multiline) using standard keyboard navigation (`Tab`).
- Typed multi-line text into `Description`; verified character inputs and line breaks.

### 1.3 Draft Management & Discard Draft Reset
- Typed temporary values into `Title` and `Description`.
- Verified that **Discard draft** button appears in the toolbar.
- Clicked **Discard draft**; verified that inputs are cleared back to empty strings, draft storage is pruned, and **Create task** button returns to disabled.

### 1.4 Target Team & Repository Pickers
- **Target team**: Opened team dropdown selector (`Select` name="team"); confirmed default team and list of available teams.
- **Repository Picker (`RepoField`)**:
  - Tested `no-repo` switch option ("No repo"), which disables branch selection requirements.
  - Selected declared repository (`acme/widgets`); confirmed appearance of `Base branch` (`BranchField`).
  - Tested "Create a new branch" checkbox; verified conditional appearance of `New branch name` input. Checked typing and unchecking behaviors.

### 1.5 Custom Fields (`CustomFields.jsx`)
Exercised all declared custom field types from `GET /api/config`:
- **Enum Select (`Priority`)**: Opened dropdown and picked `"high"`; confirmed display value.
- **Multi-select Autocomplete (`Area`)**: Selected multiple tags (`"backend"`, `"frontend"`); verified chip creation inside `Autocomplete` container.
- **Text Input (`Requested by`)**: Typed string values; verified character persistence.
- **Number Input (`Estimate (days)`)**: Typed numerical values; verified HTML5 number field constraints.
- **Boolean Select (`Customer-facing`)**: Exercised three-state select (`Yes`, `No`, `Not set`); selected `Yes`.
- **Date Input (`Due date`)**: Filled ISO date value `2026-11-20`.

### 1.6 Advanced Options (Accordion)
- Expanded `Advanced options` accordion.
- **Interactive session**: Verified checkbox toggle between background execution and live chat mode.
- **Network access**: Operated select dropdown between `Deployment default`, `Egress (outbound internet only)`, and `None`.
- **Agent findings (`nothingNeededAction`)**: Selected `Leave for acknowledgement`.
- **Prompt extension override**: Typed custom override text in multiline `TextField`.
- **Sandbox shape quotas**: Operated numeric inputs for `vCPUs`, `Memory (MiB)`, and `Disk (GiB)`.

---

## 2. Task Detail & Edit (`EditTaskForm.jsx`, `Target.jsx`, `TaskChat.jsx`)

### 2.1 In-Place Title & Description Editing
- Navigated to `/tasks/:id`.
- Clicked `Edit` button, opening `EditTaskForm`.
- Edited `Title` and `Description` with markdown list elements.
- Clicked `Save`; verified that changes rendered immediately in the detail header and description view, and persisted across full page reloads.

### 2.2 Target Repo & Branch (`Target.jsx`)
- Operated inline `Base branch` input (`BranchField`).
- Edited branch to `release-v1` and triggered `blur` event; verified that onBlur persistence correctly retained the modified branch.

### 2.3 Auto-merge Toggle (`Actions.jsx`)
- Operated `Auto-merge once checks pass` checkbox toggle.
- Confirmed state change and checked status update.

### 2.4 Detail Custom Fields (`CustomFieldsSection.jsx`)
- Clicked `Edit fields` in task detail panel.
- Modified custom field values (updated `Priority` to `high`, `Requested by` to `Platform Infra Lead`, and `Estimate` to `7`).
- Saved custom fields; verified that `.declared-row` elements in the detail panel reflected updated values.

### 2.5 Task Comment Composer (`TaskChat.jsx`)
- Located conversation composer (`.cs-message-input__content-editor` / `[placeholder='Reply...']`).
- Focused input, typed comment message, and clicked `Comment` button.
- Verified comment appeared in the transcript timeline.

---

## 3. Task Filtering & Narrowing (`NarrowingBar.jsx`, `taskFilters.js`)

### 3.1 List Search Text Field
- Tested `ListSearchField` (`placeholder="Search tasks…"`).
- Typed substring query `Bump the Go toolchain`; verified immediate client-side list filtering to matching task.
- Cleared search input; verified full list reappeared.

### 3.2 Condition Editor & Filter Dropdowns
- Clicked filter button to expand condition editor.
- Clicked `Filter by…` / `Add filter`; verified attribute selection menu containing system fields (`State`, `Repo`, `Team`) and custom fields (`Priority`, `Area`, etc.).
- Picked `Priority` attribute, selected operator and value `"high"`.
- Verified list narrowed exclusively to tasks carrying `Priority: high`.
- Verified condition URL synchronization.
- Clicked `Remove filter` button; confirmed filter condition was removed and list restored.

---

## 4. Theme & Visual Styling Verification

- Toggled `document.documentElement.dataset.theme` between `"light"` and `"dark"`.
- Verified text contrast, background palette, input borders, and focused outlines across both light and dark themes.
- Confirmed zero color bleeding or un-themed native components in task overlay, detail panel, and narrowing toolbar.

---

## Test Execution Output

```
Running 3 tests using 1 worker

  ✓  1 [chromium] › e2e/task_inputs_verification_walkthrough.spec.js:33:3 › Task Intake, Detail Edit, and Filter Inputs Verification Walkthrough › 1. New Task Overlay: verify and operate all intake input fields, keyboard focus, validation, draft reset, and themes (6.6s)
  ✓  2 [chromium] › e2e/task_inputs_verification_walkthrough.spec.js:240:3 › Task Intake, Detail Edit, and Filter Inputs Verification Walkthrough › 2. Task Detail & Edit: verify EditTaskForm, Target repos/branches, custom fields, comment composer, and capabilities (2.6s)
  ✓  3 [chromium] › e2e/task_inputs_verification_walkthrough.spec.js:358:3 › Task Intake, Detail Edit, and Filter Inputs Verification Walkthrough › 3. Task Filtering & Narrowing: search text input, filter dropdowns, condition values, reset/blur, light & dark themes (2.8s)

  3 passed (18.8s)
```
