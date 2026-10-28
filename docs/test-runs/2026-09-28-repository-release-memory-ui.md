# Repository, Release, and Memory UI Walkthrough (2026-09-28)

## Scope and environment

- Repository: `bwsalmon/grain`
- Walkthrough commit tested: `d1c6295ff8795b8bfd73432104989f8a99268800`
- Branch: `grain/task-944`
- Host: Debian bookworm, Linux amd64
- Go `1.26.6`; Node `20.19.0`; npm `10.8.2`
- Playwright `1.62.1`, Chromium `151.0.7922.34`, WebKit `26.5`
- Server: Real `grain demo` server (`go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false`) with an embedded SQLite throwaway store under `/tmp`
- Frontend: Built production assets via `make frontend` into `pkg/ui/static`
- External-boundary simulation: Compiled helper `/tmp/grain-e2e-*/grain-release-helper-e2e` (`ui/e2e/releasehelper/main.go`) used to simulate remote GitHub branch operations and trigger qualification plan reconciliation without mocking Grain APIs or React UI

All operations exercised real UI components, event handlers, client-side routing, and real server API endpoints over HTTP.

## Walkthrough

### 1. Repositories Index (`/repos`)

1. **Navigation from Rail and Direct URL**:
   - Navigated to `/` and clicked `"Repos"` from the navigation rail; confirmed client router navigated to `/repos` and rendered the Repos header.
   - Tested direct page load at `/repos`; confirmed cold render without flashes or layout jumps.
2. **Page layout, counts, and seeded rows**:
   - Header badge displayed total active repository count.
   - Seeded `acme/widgets` row displayed per-state status chips and task count (`12 tasks`).
3. **Repository search, sorting, and long-name layout**:
   - Added disposable known repositories via configuration API: `acme/gadgets` and `example-corporation/exceptionally-long-repository-name-for-responsive-layout` (73 characters).
   - Tested case-insensitive search (`WIDGETS`), confirming immediate filtering to `acme/widgets`.
   - Tested non-matching query (`nonexistent-repo-query`), confirming empty state display (`"No repos match your search."`).
   - Verified sorting modes:
     - **Name (A–Z)**: Alphabetical ascending order (`acme/gadgets`, `acme/widgets`, `example-corporation/...`); drag handles omitted.
     - **Most tasks**: Ordered by task volume (`acme/widgets` first); drag handles omitted.
     - **Custom order**: Restored drag/tap reorder handles on each repository row.
4. **Reordering via Pointer / Keyboard / Tap Affordances**:
   - Picked up repository row via `MoveHandle` (`button.task-drag-handle`); verified `MovingBar` banner (`"Moving 1 repo"`).
   - Placed repository into target position via `MoveSlot` (`button.reorder-slot-button`).
   - Reloaded page and verified that custom repository order persisted in `localStorage`.
5. **Responsive layout**:
   - Tested 390px mobile viewport; confirmed 73-character repository name truncated cleanly without horizontal page scroll or overflow (`scrollWidth === clientWidth`).

### 2. Repository Detail & Scoped Workflows (`/repos/acme/widgets`)

1. **Navigation & Back Navigation**:
   - Clicked `acme/widgets` row to navigate to `/repos/acme/widgets`.
   - Followed task count link (`"12 tasks filed here"`) to `/?f=repo:any:acme/widgets`.
   - Executed browser Back navigation and verified clean return to `/repos/acme/widgets`.
2. **Task Filing with Preselection & Persistence**:
   - Clicked `"New task"` on the repository detail page.
   - Verified that repository selector automatically defaulted and locked to `acme/widgets`.
   - Submitted task creation; confirmed return to repository page and new task creation in database.
3. **Branch Management & Comparison**:
   - Added branch `task-396/operator-branch` using the branch form; verified branch appeared in list and persisted across hard reload.
   - Verified branch comparison inputs (Base and Head branches).
4. **Capabilities Configuration (CI and Default)**:
   - Configured CI capabilities (`GCP key`), saved, and verified persistence across page reload.
   - Configured Default capabilities (`Self debug`), saved, and verified persistence across page reload.
5. **Prompt Extension and Setup Command**:
   - Set custom prompt extension (`"Standing prompt extension for acme/widgets walkthrough."`) and clicked `"Save prompt extension"`.
   - Set custom setup command (`"npm test && echo setup-done"`) and clicked `"Save setup command"`.
   - Reloaded page and verified both configuration fields retained their saved values.
6. **Declarations Syncing (`.grain/workflows/*.yml`)**:
   - Tested invalid workflow YAML (`invalid: [yaml: broken`); verified rejection with inline error alert.
   - Entered valid workflow YAML (`name: Test CI`, `on: push: branches: [main]`, `runs-on: ubuntu-latest`) and saved successfully.
   - Reloaded page and confirmed `.grain/workflows/e2e-test.yml` persisted.
   - Removed workflow and verified clean deletion.

### 3. Memory UI (`/settings` -> Models tab)

1. **Deployment & Repository Memory Scopes**:
   - Navigated to `/settings` and opened `"Models"` tab.
   - Inspected deployment-wide memory (`"This deployment"`, `"Memory carried by every run"`).
   - Inspected repo-scoped memory (`"Memory about acme/widgets"`).
   - Verified author attribution chips (`"written by the memory agent"`, `"edited here"`).
2. **Memory Documents**:
   - Displayed documents list (`release-checklist`, `flaky-e2e`) with author/timestamp metadata.
   - Opened `release-checklist` in place; verified markdown contents (`"Tag from main only..."`).
3. **Reversible Correction in Throwaway Store**:
   - Edited `acme/widgets` memory text with test annotation (`"Tested during operator walkthrough."`) and clicked `"Save"`.
   - Verified `"written today"` attribution chip appeared immediately.
   - Reloaded `/settings` (Models tab) and verified daemon persisted and returned updated text.
   - Reverted memory edit back to original content and saved cleanly.

### 4. Releases and Qualification (`/repos/acme/widgets/releases`)

1. **Release Creation and Validation**:
   - Navigated to `/repos/acme/widgets/releases`.
   - Tested invalid release name with spaces; verified validation error banner displayed.
   - Created valid release (`rel-<timestamp>`); verified release status transitioned to `Provisioning` and candidate `.rc.1` was created in state `cutting`.
   - Verified action buttons `"Cut new RC"` and `"Promote current RC"` were disabled during cutting.
2. **Qualification Plan Configuration**:
   - Toggled `"Promote automatically once qualification succeeds"` and `"Require approval before running qualification tasks"`.
   - Saved qualification plan and confirmed settings retained.
3. **External Boundary Simulation via release helper**:
   - **Simulated Error State**: Invoked `mark-candidate-error`; reloaded UI and verified error banner with simulated failure reason (`"Simulated remote git branch failure during candidate cut"`).
   - **Simulated Active/Cut State**: Invoked `mark-release-provisioned`, `mark-candidate-cut`, and `sync-qualifications`; reloaded UI and verified candidate transitioned to `active`, and `"Promote current RC"` button became enabled.

### 5. Release Trains Fallback Routes

1. Navigated to `/repos/acme/widgets/trains`; verified clean fallback render without unhandled exceptions.
2. Navigated to `/releasetrains`; verified clean fallback render without unhandled exceptions.

## Automated Verification & Test Results

- **Playwright E2E Walkthrough Spec**: `ui/e2e/repository_release_memory_walkthrough.spec.js` passed (16.8s).
- **Related E2E Suites**:
  - `ui/e2e/repo-pane.spec.js`: passed
  - `ui/e2e/repo-ci-capabilities.spec.js`: passed
  - `ui/e2e/memory.spec.js`: passed
  - `ui/e2e/repo-ownership-ci-security.spec.js`: passed
- **Full Playwright E2E Suite**: 190 passed, 1 skipped.
- **Vitest Unit Suite**: 2,623 unit tests passed across 151 test files.
- **Go Backend Packages**: `pkg/ui` (all tests passed), `pkg/model/sqlite` (passed), `go vet ./...` (clean).

## Stubs and Mocks

No React UI or Grain API endpoints were mocked or intercepted with synthetic client responses. The walkthrough executed against a real Go `grain demo` HTTP server backed by SQLite. External GitHub reconciler actions (e.g. remote git branch state transitions and qualification reconciliation) were driven through direct database model state transitions using the compiled `/tmp/grain-e2e-*/grain-release-helper-e2e` helper binary.
