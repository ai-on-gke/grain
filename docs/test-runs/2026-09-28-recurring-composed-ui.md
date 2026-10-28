# Recurring and Composed Work Automation UI Test Run Report (2026-09-28)

## Test Environment
- **Date**: 2026-09-28
- **Tested Base Commit**: `d1c6295ff8795b8bfd73432104989f8a99268800`
- **Branch**: `grain/task-945`
- **Go Version**: `go1.26.6 linux/amd64`
- **Node.js**: `v20.19.0`, npm `10.8.2`
- **Test Runner**: Playwright Chromium (headless shell 1234) against live `grain demo` HTTP server (`pkg/ui/server.go`) backed by SQLite embedded store

---

## Executive Summary
A complete end-to-end walkthrough of Grain's recurring and composed-work automation user interfaces was executed in a real browser against a live `grain demo` backend server. All functional domains—Templates, Suites, Schedules, Suite Runs, Repository-declared entities (`docs/declarations.md`), and Error Resilience—were thoroughly exercised, inspected, and validated against design and operational criteria.

All **6 Playwright walkthrough scenarios** in `ui/e2e/recurring-composed-walkthrough.spec.js` passed cleanly. In addition, the full Playwright E2E suite (190 passed, 1 skipped across desktop, tablet, and mobile profiles), frontend Vitest component tests (151 test files, 2,623 passed), and backend Go test suites (`pkg/ui/...`, `tests/e2e/...`) passed with 100% success.

---

## Walkthrough Scenarios & Verifications

### 1. Reusable Task Templates (`/templates`)
- **Navigation, Search, and Sorting**:
  - Navigated to `/templates` via sidebar rail navigation.
  - Verified list presentation, search filtering (`Search templates…`), and sorting dropdown options (Custom order, Name A–Z, Newest first, Oldest first).
- **Template Authoring & Configuration**:
  - Created a fully configured template (`qual-audit-*`):
    - Target repository: `acme/widgets`
    - Base branch: `main`
    - Title, Name, and Markdown description
    - Auto-merge enabled (`autoMerge: true`)
    - Custom template input fields (variables), capabilities, and declared custom field values.
- **Persistence & Direct Deep Linking**:
  - Edited template title to verify update persistence across page reload.
  - Deep linked directly to `/templates/:id`, verified fields pre-populated in `TemplateOverlay`.
- **Use Template Flow**:
  - Opened "Use…" action dialog (`UseTemplateOverlay`), verified pre-filled repository context and template variables.
  - Triggered "Create task" submission; verified successful task filing and immediate navigation to `/tasks/:id`.
- **Dependency-Aware Deletion Safeguards**:
  - Linked a newly created recurring schedule to the template.
  - Attempted deletion while referenced: verified server refusal with an explicit explanatory error banner (`"template is used by 1 schedule(s); repoint or delete those first"`).
  - Deleted referencing schedule and confirmed clean template deletion.

### 2. Composed Test Suites (`/suites`)
- **Suite Creation & Run Modes**:
  - Created a new composed suite with `until_clean` mode, setting `maxPasses = 3`.
  - Configured `Require approval` and `Auto-merge` switches.
  - Linked existing template `release-qualification`.
- **Inline Template Authoring**:
  - Triggered `+ New template` directly from inside `SuiteOverlay`.
  - Created nested template `inline-gate-*` targeting `acme/widgets`.
  - Confirmed the newly created template was immediately selected in the parent suite's template picker without leaving the suite overlay.
- **Live Composition Preview**:
  - Verified `CompositionPanel` dynamically rendered the live preview (`"What each pass files"`), reflecting cadence rules, auto-merge policies, and approval constraints in real-time.
- **Editing & Mode Transition**:
  - Edited suite to switch mode to `count` with `2 passes` (`run 2×`).
  - Saved and verified suite row badge reflected `run 2×` and `2 templates`.
- **Cleanup**:
  - Deleted throwaway test suite with confirmation dialog.

### 3. Recurring Schedules (`/schedules`)
- **Task-Filing Schedules**:
  - Created template-backed schedule referencing `release-qualification`.
  - Configured `Daily, at a time` recurrence (04:30).
  - Configured overlap policy to `Start it anyway` (`overlap: allow`).
  - Confirmed live composition preview heading `"What this schedule files"`.
  - Verified row chips: `Can overlap`, `Next run`, and `acme/widgets`.
- **Suite-Running Schedules**:
  - Created schedule firing `Release Qualification Suite` against `acme/widgets:main`.
  - Configured `Weekly, on a day and time` cadence (Mondays at 07:00).
  - Verified row presentation: `Suite: Release Qualification Suite`, `Mondays at 07:00`.
- **Lifecycle Controls**:
  - Paused schedule via `Pause` action; verified `Paused` chip displayed on schedule row and cadence suspended.
  - Resumed schedule via `Resume` action; verified `Paused` chip cleared and next-run restored.
  - Executed `Run now` with confirmation dialog; verified `last ran` timestamp updated without altering regular recurrence.
  - Cleanly deleted throwaway schedule with confirmation.

### 4. Suite Runs & Hierarchical Tasks (`/suites/runs/:id`)
- **Run Launching**:
  - Launched suite run from suite row `Run…` action targeting `acme/widgets:main`.
  - Launched suite run from generic `+ Run` button on the Runs section header with suite selector dropdown.
  - Verified run recorded in `/suites` runs list with status chip, repository context, and trigger attribution.
- **Pass Grouping & Execution Hierarchy**:
  - Navigated to `/suites/runs/:id` via direct URL and row click.
  - Verified `SuiteRunPane` layout: suite title, status chip (`Active`, `Succeeded`, `Failed`), repo/branch chips (`acme/widgets`, `main`).
  - Verified pass sections (`Pass 1`, `Pass 2`) and individual task rows (`.task-row`).
  - Confirmed task detail navigation (`/tasks/:id`) when clicking individual tasks within a pass.

### 5. Repository-Declared Automation (`docs/declarations.md`)
- **Provenance & Display**:
  - Verified repository-declared items for `acme/widgets` seeded via `store.SyncRepoDeclarations`.
  - Inspected declared template `nightly-maintenance` (`.grain/templates/nightly.yml`).
  - Inspected declared schedule `Nightly Repo Maintenance Sweep` (`.grain/schedules/nightly.yml`).
  - Inspected declared suite `Smoke Verification Suite` (`.grain/suites/smoke.yml`).
- **Modification & Deletion Refusal**:
  - Attempted to edit declared template: verified 400 refusal with banner:
    `"nightly-maintenance is declared by acme/widgets in .grain/templates/nightly.yml, so it is not this deployment's to edit"`.
  - Attempted to delete declared template: verified 400 refusal banner.
  - Attempted to delete declared schedule: verified 400 refusal banner explaining repository declaration ownership.

### 6. Error Resilience & Network Fault Injection
- **503 Network Interruption**:
  - Injected simulated `503 Service Temporarily Unavailable` network fault on `POST /api/templates`.
  - Verified error banner caught and displayed message gracefully without crashing the UI or corrupting application state.
- **Routing Fallbacks**:
  - Tested navigation to non-existent resource IDs (`/schedules/sched-non-existent-9999`, `/templates/tmpl-non-existent-9999`, `/suites/suite-non-existent-9999`).
  - Verified clean redirect/fallback to respective collection views without uncaught console exceptions.

---

## Stubs and Boundaries
- **Agent/Execution Boundary**: The demo server (`grain demo`) executes against the seeded in-memory demo store and mock dispatcher without spawning external agent VMs.
- **External Network/Service Boundary**: External VCS / GitHub APIs are not reached; all interactions take place against Grain's local API and repository declarations.

---

## Test Execution Matrix

| Test Case | Tooling | Results |
|:---|:---|:---|
| Templates Walkthrough (CRUD, deep-link, use, dependency delete) | Playwright Chromium | **PASS** (2.4s) |
| Suites Walkthrough (modes, inline templates, live composition) | Playwright Chromium | **PASS** (3.2s) |
| Schedules Walkthrough (recurrences, overlap, pause/resume, run-now) | Playwright Chromium | **PASS** (5.6s) |
| Suite Runs Walkthrough (launching, pass grouping, direct URLs) | Playwright Chromium | **PASS** (1.0s) |
| Repository-Declared Automation (provenance, 400 refusal banners) | Playwright Chromium | **PASS** (1.1s) |
| Error Resilience (503 fault injection, 404/ID fallbacks) | Playwright Chromium | **PASS** (1.3s) |
| Full UI Vitest Suite (151 test files, 2,623 tests) | Vitest | **PASS** (2623 / 2623 passed) |
| Full Playwright E2E Suite (191 tests) | Playwright (Chromium/WebKit) | **PASS** (190 passed, 1 skipped) |
| Backend Go UI Tests (`pkg/ui/...`) | `go test` | **PASS** (100%) |
| Backend Go E2E Tests (`tests/e2e/...`) | `go test` | **PASS** (100%) |

---

## Conclusion
Grain's recurring and composed-work automation UI is fully functional, robust against failure conditions, and compliant with all architectural and behavioral specifications.
