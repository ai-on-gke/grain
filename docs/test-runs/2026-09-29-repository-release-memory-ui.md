# Repository, Release, and Memory UI Walkthrough Report (2026-09-29)

## Scope and Environment

- **Repository**: `bwsalmon/grain`
- **Walkthrough Commit Tested**: `57e519bde4c1b77139bd0cd12b629de0f30ba726`
- **Branch**: `grain/task-1069`
- **Operating System / Host**: Debian bookworm, Linux amd64
- **Toolchain Versions**: Go `1.26.6`, Node `20.19.0`, npm `10.8.2`
- **Browser Automation Engine**: Playwright `1.62.1` (Chromium `151.0.7922.34`, WebKit `26.5`)
- **Server**: Real `grain demo` Go server (`go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false`) with an embedded SQLite throwaway store under `/tmp`
- **Frontend**: Production asset build via `vite build` into `pkg/ui/static`
- **External Boundary Simulation**: Compiled helper `/tmp/grain-e2e-*/grain-release-helper-e2e` (`ui/e2e/releasehelper/main.go`) driving remote git / GitHub candidate cut, qualification plan reconciliation, and error simulation without synthetic frontend mocking

All operator workflows were exercised in real browsers against the live daemon and throwaway SQLite database.

---

## Walkthrough Execution and Verified Workflows

### 1. Repositories Index (`/repos`)

1. **Navigation from Rail and Direct URL**:
   - Navigated to `/` and clicked `"Repos"` in the primary navigation rail; confirmed client router navigated to `/repos` and rendered the Repos header with repository glyph.
   - Tested cold direct navigation to `/repos`; confirmed immediate rendering with zero layout flashes or console errors.
2. **Page Layout, Counts, and Seeded Rows**:
   - Header badge correctly displayed total active repository count.
   - Seeded `acme/widgets` row displayed per-state status chips and task count (`12 tasks`).
3. **Repository Search, Sorting, and Long-Name Layout**:
   - Added disposable known repositories via configuration API: `acme/gadgets` and `example-corporation/exceptionally-long-repository-name-for-responsive-layout` (73 characters).
   - Tested case-insensitive search (`WIDGETS`); confirmed instant filtering to matching `acme/widgets` row.
   - Tested non-matching query (`nonexistent-repo-query`); confirmed empty search state (`"No repos match your search."`).
   - Verified sorting modes:
     - **Name (A–Z)**: Alphabetical ascending order (`acme/gadgets`, `acme/widgets`, `example-corporation/...`); drag handles omitted.
     - **Most tasks**: Ordered by task volume (`acme/widgets` first); drag handles omitted.
     - **Custom order**: Restored drag/tap reorder handles on each repository row.
4. **Reordering via Pointer / Keyboard / Tap Affordances**:
   - Picked up repository row via `MoveHandle` (`button.task-drag-handle`); verified `MovingBar` banner (`"Moving 1 repo"`).
   - Placed repository into target position via `MoveSlot` (`button.reorder-slot-button`).
   - Reloaded page and verified custom repository order persisted in `localStorage`.
5. **Responsive Layout**:
   - Tested 390px mobile viewport; confirmed 73-character repository name truncated cleanly without horizontal page scroll or overflow (`scrollWidth === clientWidth`).

### 2. Repository Detail & Scoped Workflows (`/repos/acme/widgets`)

1. **Navigation & Back Navigation**:
   - Clicked `acme/widgets` row to navigate to `/repos/acme/widgets`.
   - Followed task count link (`"12 tasks filed here"`) to `/?f=repo:any:acme/widgets`.
   - Executed browser Back navigation and verified clean return to `/repos/acme/widgets`.
2. **Task Filing with Preselection, Persistence & Multi-Repo Relationships**:
   - Clicked `"New task"` on the repository detail page; confirmed repository selector defaulted and locked to `acme/widgets`.
   - Added extra writeable repository `acme/gadgets` with base branch `candidate` via `WriteReposField`.
   - Added read-only repository `example-corporation/exceptionally-long-repository-name-for-responsive-layout` via `ReadOnlyReposField`.
   - Submitted task creation; confirmed return to repository page and new task creation in database with configured relationships.
3. **Branch Management & Comparison**:
   - Added branch `task-396/operator-branch` using the branch form; verified branch appeared in list and persisted across hard reload.
   - Verified branch comparison inputs (Base and Head branches); clicked `"Compare"` and verified routing to `/repos/acme/widgets/compare`. Returned cleanly via browser back navigation.
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
7. **Work Ownership Settings**:
   - Tested repository-level work ownership toggle (`"the repository default: on"`), verified status chip update (`"Runs this repository's work"`), and reset state cleanly.

### 3. Memory UI (`/team-settings` -> Models tab)

1. **Deployment & Repository Memory Scopes**:
   - Navigated to `/team-settings` and opened `"Models"` tab.
   - Inspected deployment-wide memory (`"This deployment"`, `"Memory carried by every run"`).
   - Inspected repo-scoped memory (`"Memory about acme/widgets"`).
   - Verified author attribution chips (`"written by the memory agent"`, `"edited here"`).
2. **Memory Documents**:
   - Displayed documents list (`release-checklist`, `flaky-e2e`) with author/timestamp metadata.
   - Opened `release-checklist` in place; verified markdown contents (`"Tag from main only..."`).
3. **Reversible Correction in Throwaway Store**:
   - Edited `acme/widgets` memory text with test annotation (`"Tested during operator walkthrough."`) and clicked `"Save"`.
   - Verified `"written today"` attribution chip appeared immediately.
   - Reloaded `/team-settings` (Models tab) and verified daemon persisted and returned updated text.
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
3. **External Boundary Simulation via Release Helper**:
   - **Simulated Error State**: Invoked `mark-candidate-error`; reloaded UI and verified error banner with simulated failure reason (`"Simulated remote git branch failure during candidate cut"`).
   - **Simulated Active/Cut State**: Invoked `mark-release-provisioned`, `mark-candidate-cut`, and `sync-qualifications`; reloaded UI and verified candidate transitioned to `active`, and `"Promote current RC"` button became enabled.

### 5. Release Trains Fallback Routes

1. Navigated to `/repos/acme/widgets/trains`; verified clean fallback render without unhandled exceptions.
2. Navigated to `/releasetrains`; verified clean fallback render without unhandled exceptions.

---

## Outcomes and Stub Summary

| Boundary / Workflow | Mechanism | Verification Method | Outcome |
|---|---|---|---|
| Repos index & sorting | Real React state & `localStorage` | Playwright pointer/drag | Passed |
| Scoped task filing | Real Go API & SQLite | Form submission & assertions | Passed |
| Multi-repo read/write | Real Go API & SQLite | Form submission (`WriteReposField`, `ReadOnlyReposField`) | Passed |
| Branch rules & comparison | Real Go API & SQLite | Form submission & navigation | Passed |
| Capabilities & prompts | Real Go API & SQLite | Form submission & reload | Passed |
| Declarations sync | Real Go API & SQLite | YAML validation & CRUD | Passed |
| Work ownership | Real Go API & SQLite | Button toggles & state chips | Passed |
| Deployment & repo memory | Real Go API & SQLite | Document expansion & inline edit | Passed |
| Release lifecycle | External helper simulation | Helper DB transitions & UI polling | Passed |
| Qualification plan | Real Go API & SQLite | Checkbox toggles & persistence | Passed |
| Release trains routes | Client-side routing | URL navigation | Passed |

No blocking regressions or defects were found during execution.
