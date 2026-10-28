# Recurring and Composed Work Automation UI Walkthrough Report

## Test Environment
- **Date**: 2026-09-30
- **Tested Base Commit**: `efbe97eb0fa8c7a738a51595f0972e38088564d3`
- **Branch**: `grain/task-1227`
- **Go Version**: `go1.26.6 linux/amd64`
- **Node.js**: `v20.19.0`, npm `10.8.2`
- **Browsers**: Chromium 151.0.7922.34 (Playwright v1.62.1), WebKit 26.5
- **Server Harness**: Live `grain demo` instance (`pkg/ui/server.go`) over isolated SQLite store per Playwright worker

---

## Executive Summary
A comprehensive end-to-end walkthrough of Grain's recurring and composed-work automation user interfaces was executed against a live `grain demo` server. All primary workflows—Reusable Task Templates, Composed Test Suites, Recurring Schedules, Suite Runs and Task Hierarchy, Repository-Declared Automation Entities, and Error Resilience—were thoroughly exercised in a real browser environment.

All **6 Playwright walkthrough scenarios** passed cleanly. In addition, the related workflow specs (`templates_workflows.spec.js`, `schedules_workflows.spec.js`, `suites_workflows.spec.js`), the full end-to-end suite (236 passed, 1 skipped across desktop and mobile browsers), the entire frontend unit suite (156 test files, 2,668 Vitest tests), and the Go backend test suites (`pkg/ui/...`, `cmd/grain/...`) all passed with zero regressions.

---

## Walkthrough Scenarios & Verifications

### 1. Reusable Task Templates (`/templates`)
- **Discovery, Search & Ordering**:
  - Navigated to `/templates` via sidebar rail.
  - Verified header presentation, template count badge, search filter field (`Search templates…`), and sort dropdown (`Custom order`, `Name (A–Z)`).
- **Template Authoring & Full Metadata Configuration**:
  - Created a fully configured template (`qual-audit-*`):
    - Target repository: `acme/widgets`
    - Base branch: `main`
    - Title, Name, and Markdown description instructions
    - Auto-merge flag enabled (`autoMerge: true`)
    - Custom template input fields (with options, default, required)
    - Capabilities, reads, writes, and custom fields.
- **Persistence & Direct Deep Linking**:
  - Modified template title and verified persistence across navigation and full page reloads.
  - Deep linked directly to `/templates/:id`, confirming all fields accurately pre-populated in `TemplateOverlay`.
- **"Use Template" Task Filing Flow**:
  - Opened `UseTemplateOverlay` from the template row's "Use…" action.
  - Verified pre-filled repository context and template variables.
  - Submitted "Create task"; verified task creation and immediate navigation to the newly filed task (`/tasks/:id`).
- **Dependency-Aware Deletion Safeguards**:
  - Created a recurring schedule referencing the template.
  - Attempted deletion of the template: confirmed server refusal with explanatory error banner (`"template is used by 1 schedule(s); repoint or delete those first"`).
  - Deleted the referencing schedule, then successfully deleted the throwaway template.

### 2. Composed Test Suites (`/suites`)
- **Suite Authoring & Run Modes**:
  - Created a composed suite configured with `until_clean` mode (`maxPasses = 3`).
  - Configured `Require approval` and `Auto-merge` policies.
  - Bound existing template `release-qualification`.
- **Inline Template Creation**:
  - Triggered `+ New template` button directly within `SuiteOverlay`.
  - Authored nested template `inline-gate-*` targeting `acme/widgets`.
  - Confirmed the newly created template was immediately selected in the parent suite's template picker without losing form state.
- **Live Composition Preview**:
  - Verified `CompositionPanel` dynamically rendered the `"What each pass files"` preview, updating in real time as templates, modes, and approval gates changed.
- **Mode Switching & Editing**:
  - Edited suite to switch execution mode to `count` with `2 passes` (`run 2×`).
  - Verified suite row badges reflected updated configuration (`run 2×` mode chip, template count chip).
- **Deletion**:
  - Confirmed destructive-action confirmation dialog and verified clean deletion of throwaway suite.

### 3. Recurring Schedules (`/schedules`)
- **Task-Filing Schedules**:
  - Created template-backed schedule referencing `release-qualification`.
  - Configured `Daily, at a time` recurrence (04:30).
  - Configured overlap policy to `Start it anyway` (`overlap: allow`).
  - Verified dynamic `CompositionPanel` preview heading `"What this schedule files"`.
  - Confirmed schedule row indicators: `Can overlap`, `Next run` timestamp, and target repository.
- **Suite-Running Schedules**:
  - Created schedule triggering `Release Qualification Suite` against `acme/widgets:main`.
  - Configured `Weekly, on a day and time` recurrence (Mondays at 07:00).
  - Verified schedule row presentation: `Suite: Release Qualification Suite`, `Mondays at 07:00`.
- **Lifecycle & Execution Controls**:
  - **Pause**: Paused schedule via `Pause` action; verified `Paused` chip displayed on schedule row.
  - **Resume**: Resumed schedule via `Resume` action; verified `Paused` chip cleared.
  - **Run Now**: Executed immediate firing via `Run now` button with confirmation dialog; verified `last ran` timestamp updated without disrupting future scheduled cadence.
  - **Cleanup**: Cleanly deleted throwaway schedule after verification.

### 4. Suite Runs & Execution Hierarchy (`/suites/runs/:id`)
- **Run Initiation**:
  - Triggered a suite run from the suite row's "Run…" button targeting `acme/widgets:main`.
  - Triggered a suite run from the generic `+ Run` action in the Runs header on `/suites`, selecting suite, repo, and branch.
  - Verified both runs registered in `/suites` runs list with status chips, repo badge, and base branch.
- **Pass Grouping & Task Hierarchy**:
  - Navigated to `/suites/runs/:id` via direct URL and row selection.
  - Verified `SuiteRunPane` layout: suite title, status chip, repo/branch chips (`acme/widgets`, `main`).
  - Verified pass sections (`Pass 1`) and individual task rows.
  - Confirmed task detail navigation (`/tasks/:id`) when clicking an individual task row within a pass.

### 5. Repository-Declared Automation (`docs/declarations.md`)
- **Provenance & Declared Display**:
  - Verified repository-declared items for `acme/widgets` seeded via `store.SyncRepoDeclarations`.
  - Inspected declared template `nightly-maintenance` (`.grain/templates/nightly.yml`).
  - Inspected declared schedule `Nightly Repo Maintenance Sweep` (`.grain/schedules/nightly.yml`).
  - Inspected declared suite `Smoke Verification Suite` (`.grain/suites/smoke.yml`).
- **Edit & Delete Refusal Enforcements**:
  - Attempted edit on declared template: verified 400 refusal with banner:
    `"nightly-maintenance is declared by acme/widgets in .grain/templates/nightly.yml, so it is not this deployment's to edit"`.
  - Attempted deletion of declared template: verified 400 refusal banner.
  - Attempted deletion of declared schedule: verified 400 refusal banner.
  - Attempted edit on declared suite: verified 400 refusal banner (`"smoke is declared by acme/widgets in .grain/suites/smoke.yml, so it is not this deployment's to edit"`).
  - Attempted deletion of declared suite: verified 400 refusal banner.

### 6. Resilience & Network Fault Injection
- **503 Network Fault Injection**:
  - Injected simulated `503 Service Temporarily Unavailable` on `POST /api/templates`.
  - Attempted template creation: confirmed non-destructive error handling, persistent error banner display, and preserved form inputs allowing immediate retry.
- **Route Fallback & Invalid ID Recovery**:
  - Navigated to non-existent template `/templates/non-existent-template-id`.
  - Verified automatic fallback redirection to `/templates` list without blank screen or unhandled exceptions.

---

## Test Execution Results

| Test Category | Suite / Command | Result |
| :--- | :--- | :--- |
| **Walkthrough E2E** | `ui/e2e/recurring-composed-walkthrough.spec.js` | **6 / 6 Passed** (25.2s) |
| **Workflow E2E** | `ui/e2e/templates_workflows.spec.js` + `schedules_workflows.spec.js` + `suites_workflows.spec.js` | **3 / 3 Passed** (11.2s) |
| **Full Playwright E2E** | `ui/e2e/*.spec.js` | **236 Passed, 1 Skipped** (4.9m) |
| **Frontend Unit & Component** | `cd ui && npm test` (Vitest) | **156 Files / 2,668 Passed** (157.95s) |
| **Backend Go UI & CLI** | `go test ./pkg/ui/... ./cmd/grain/...` | **All Passed** (60.5s) |

---

## Conclusion
All UI interactions, form controls, confirmation modals, deep-link routings, live previews, and declaration safety invariants for recurring and composed work automation function reliably and meet all specification requirements.
