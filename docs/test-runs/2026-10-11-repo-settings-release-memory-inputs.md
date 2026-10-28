# Test Run: Repository Settings, Releases & Tags, and Memory Views Inputs Verification

**Date**: 2026-10-11  
**Origin**: Task 2853  
**Target Areas**:
- Repository Settings & Rules (`ui/src/components/RepoPage.jsx`, `RepoReleaseWorkflowSettings.jsx`)
- Releases & Tags (`ui/src/components/RepoReleases.jsx`, `RepoReleaseWorkflowSettings.jsx`)
- Repository Memory & Instructions (`ui/src/components/MemoryPanel.jsx`)

---

## 1. Executive Summary

This test run validates and exercises all interactive form inputs, editors, validation rules, auto-save mechanisms, dirty state tracking, and deletion prompts across the Repository Settings, Releases & Tags, and Memory views.

All behaviors were exercised through a dedicated end-to-end Playwright automation suite (`ui/e2e/repo-release-memory-inputs-walkthrough.spec.js`) running against the live `grain demo` environment with Chromium headless shell, alongside Vitest component test suites (142 tests passing across all 4 components).

---

## 2. Test Execution & Coverage Details

### 2.1 Repository Settings & Rules (`RepoPage.jsx`, `RepoReleaseWorkflowSettings.jsx`)
- **Branch Addition Input (`branchName`)**:
  - Validated input pattern filtering via `identifierField` (`[A-Za-z0-9_.-]+`).
  - Tested invalid branch naming (e.g. `feature..invalid` containing `..`); verified server error response and UI notification.
  - Successfully created valid branch `feat-<timestamp>` and confirmed it rendered in the branch list.
  - Tested duplicate branch submission; verified informational status message ("is already on this repo's list; nothing new was added").
- **Compare Branches Form**:
  - Exercised `Base branch` and `Head branch` text fields with branch names.
  - Verified `Compare` action button transitions to enabled state upon providing branch values.
- **CI Mode Selector**:
  - Toggled `RadioGroup` options between Grain CI (`Make grain CI the gate for acme/widgets`) and GitHub Actions (`Use GitHub CI for acme/widgets`).
  - Verified persistence and active state selection.
- **Capabilities Multi-Select & Chips**:
  - Exercised multi-select combobox for default capabilities.
  - Verified capability selection (`Self debug`), chip rendering, and keyboard accessibility (`Escape` key dismissal).
- **Prompt Extension & Setup Command Auto-Save**:
  - Input custom text into `Prompt extension for acme/widgets` and `Setup command for acme/widgets`.
  - Triggered `onBlur` auto-save events without requiring an explicit save button.
  - Reloaded page and verified persisted values from server.
- **Workflow Declarations (`.grain/workflows/*.yml`)**:
  - Added new workflow entry via `Add workflow`.
  - Input workflow file path (`test-input-validator.yml`) and verified automatic path prefixing (`.grain/workflows/`).
  - Tested invalid YAML syntax; verified error alert (`.MuiAlert-filledError`) rendered upon blur.
  - Corrected with valid YAML specification; verified error cleared and workflow persisted across page reload.
  - Verified workflow deletion confirmation dialog (`Remove workflow .grain/workflows/test-input-validator.yml`).

### 2.2 Releases & Tags (`RepoReleases.jsx`, `RepoReleaseWorkflowSettings.jsx`)
- **Release Settings Inputs**:
  - Exercised `baseBranchJobs` (`go, lint, unit-tests`) and post-merge jobs in `RepoReleaseWorkflowSettings`.
  - Verified form submission and persistent reload.
- **New Release Name Input (`releaseName`)**:
  - Tested whitespace rejection: entering `invalid release name with spaces` showed inline/server rejection.
  - Tested path traversal rejection: entering `release..bad` showed invalid release name error.
  - Created valid release `rel-<timestamp>` and verified immediate selection in the release dropdown combobox.
- **Qualification Plan Configuration**:
  - Added qualification items; configured `Repeat` numeric input (`min="1"`).
  - Verified template selector dropdown and dependency relationship configuration.
- **Release Deletion Confirmation**:
  - Triggered `Delete release` button.
  - Verified browser confirmation dialog (`Remove release rel-xxx? This drops the release and its candidates from grain.`).
  - Accepted confirmation dialog and verified release removal, transitioning to empty release state.

### 2.3 Repository Memory & Instructions (`MemoryPanel.jsx`)
- **Standing Memory Textarea**:
  - Located repository-specific memory textarea (`Memory about acme/widgets`).
  - Tested character modifications and verified `Save acme/widgets memory` button state.
  - Verified `Revert acme/widgets memory` button restores previous persisted value.
  - Saved modified memory text and verified persistence across page reloads.
- **Document Editors**:
  - Opened memory document `release-checklist`.
  - Verified `Summary of release-checklist` input and character limit helper text.
  - Verified `Document: release-checklist` body multiline editor.
  - Tested modifications, reversion, and drawer toggling (`Open` / `Close`).
- **Unsaved Changes Guard & Dirty Tracking**:
  - Modified memory textarea to dirty state without clicking Save.
  - Pressed `Escape` to close modal; verified browser alert:
    `"You have unsaved changes here. Leave and discard them?"`.
  - Dismissed alert to remain on page, then restored original state and cleanly saved.

---

## 3. Automated Test Verification Results

### Vitest Unit Tests
```
Test Files  4 passed (4)
     Tests  142 passed (142)
  Duration  27.14s
```
- `src/components/RepoPage.test.jsx`: 73 passed
- `src/components/RepoReleases.test.jsx`: 28 passed
- `src/components/MemoryPanel.test.jsx`: 24 passed
- `src/components/RepoReleaseWorkflowSettings.test.jsx`: 17 passed

### Playwright E2E Tests
```
Running 3 tests using 1 worker
  ✓  1 [chromium] › e2e/repo-release-memory-inputs-walkthrough.spec.js › 1. Repository Settings & Rules
  ✓  2 [chromium] › e2e/repo-release-memory-inputs-walkthrough.spec.js › 2. Releases & Tags
  ✓  3 [chromium] › e2e/repo-release-memory-inputs-walkthrough.spec.js › 3. Repository Memory & Instructions
  3 passed (17.5s)
```
