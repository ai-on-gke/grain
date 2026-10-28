# Repository, Release, and Memory UI Walkthrough (2026-09-23)

## Scope and environment

- Repository: `bwsalmon/grain`
- Walkthrough commit tested: `401aa3acfed52855b9a5dbdb91933dd60ee4abc6`
- Branch: `grain/task-396`
- Host: Debian bookworm, Linux amd64
- Go `1.26.6`; Node `20.19.0`; npm `10.8.2`
- Playwright `1.62.1`, Chromium `151.0.7922.34`, WebKit `26.5`
- Server: Real `grain demo` server (`go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false`) with an embedded SQLite throwaway store under `/tmp`
- Frontend: Built production assets via `make frontend` into `pkg/ui/static`
- External-boundary simulation: Compiled helper `/tmp/releasehelper` used to simulate remote GitHub branch operations and trigger qualification plan reconciliation without mocking Grain APIs or React UI

All operations exercised real UI components, event handlers, client-side routing, and real server API endpoints over HTTP.

## Walkthrough

### 1. Repositories Index (`/repos`)

1. Opened the Repositories index both from the navigation rail ("Repos") and via direct URL (`/repos`).
2. Verified page layout and counts:
   - Header badge displayed active repository count.
   - Seeded `acme/widgets` row displayed per-state status chips and task count (`12 tasks`).
3. Added disposable known repositories via configuration API to test search, sort, and responsive layout:
   - `acme/gadgets`
   - `example-corporation/exceptionally-long-repository-name-for-responsive-layout` (73 characters)
4. Tested repository search filtering:
   - Case-insensitive substring match (`WIDGETS`) immediately filtered list to matching repository (`acme/widgets`).
   - Non-matching query (`nonexistent-repo-query`) rendered empty state: `"No repos match your search."`.
   - Clearing search restored all repository rows.
5. Tested repository sorting controls:
   - **Name (A–Z)**: Ordered rows alphabetically (`acme/gadgets`, `acme/widgets`, `example-corporation/...`). Reorder handles were omitted as expected.
   - **Most tasks**: Placed `acme/widgets` (12 tasks) first; reorder handles omitted.
   - **Custom order**: Restored drag/tap reorder handles on each repository row.
6. Reordered repositories using keyboard and tap affordances:
   - Activated `MoveHandle` (`button.task-drag-handle`) to pick up a row.
   - Observed `MovingBar` banner (`"Moving 1 repo"`).
   - Clicked `MoveSlot` (`button.reorder-slot-button`) to move and drop the item.
   - Reloaded the page and confirmed custom repository order persisted in `localStorage`.
7. Responsive mobile layout:
   - Resized viewport to 390px width.
   - Verified that the 73-character repository name truncated cleanly without horizontal page scroll or overflow (`scrollWidth === clientWidth`).

### 2. Repository Detail & Scoped Workflows (`/repos/acme/widgets`)

1. Opened the `acme/widgets` detail page and returned to the repos list using the back button.
2. Verified task count navigation:
   - Clicked the `"12 tasks filed here"` link.
   - Navigated to `/?f=repo:any:acme/widgets` with the repo filter active.
   - Executed a single browser Back navigation and returned directly to `/repos/acme/widgets`.
3. Filed a new task from the repository detail page:
   - Clicked `"New task"`.
   - Verified that repository preselection automatically defaulted to `acme/widgets`.
   - Set task title and description.
   - Submitted task creation and verified return to the repository page.
4. Branch rules and branch management:
   - Added branch `task-396/operator-branch` using the branch form.
   - Observed status and verified persistence across a full page reload.
5. Capabilities configuration:
   - Configured repository CI capabilities: Selected `GCP key`, saved, and confirmed persistence after reload.
   - Configured repository default capabilities: Selected `Self debug`, saved, and confirmed persistence after reload.
6. Prompt extension and setup command:
   - Entered a custom prompt extension (`"Standing prompt extension for acme/widgets walkthrough."`) and clicked `"Save prompt extension"`.
   - Entered a custom setup command (`"npm test && echo setup-done"`) and clicked `"Save setup command"`.
   - Reloaded the page and confirmed that both fields retained their saved values.
7. Declarations syncing (`.grain/workflows/*.yml`):
   - Tested invalid workflow YAML (`invalid: [yaml: broken`); verified that saving was rejected with a clear parser error alert.
   - Entered valid workflow YAML (`name: Test CI`, `on: push: branches: [main]`, `runs-on: ubuntu-latest`) and saved successfully.
   - Reloaded page and confirmed `.grain/workflows/e2e-test.yml` persisted.
   - Clicked `"Remove"` and saved workflows to confirm deletion.

### 3. Memory UI (`/settings` -> Models tab)

1. Navigated to `/settings` and selected the `"Models"` tab.
2. Inspected memory scopes:
   - Deployment memory: Verified content (`make fmt`).
   - Repository memory: Verified `acme/widgets` memory (`make frontend`).
   - Author attribution chips: Verified `"written by the memory agent"` and `"edited here"`.
3. Documents list:
   - Displayed documents `release-checklist` and `flaky-e2e` with author/timestamp metadata (`"written ... months ago"`).
   - Clicked `"Open"` on `release-checklist` to view document in place; verified full body text (`"Tag from main only..."`).
4. Reversible correction in throwaway store:
   - Edited `acme/widgets` memory to append `"Tested during operator walkthrough."` and clicked `"Save"`.
   - Verified `"written today"` attribution chip appeared immediately.
   - Reloaded `/settings` (Models tab) and verified that the daemon persisted and returned the updated text.
   - Reverted the memory edit back to original text and saved cleanly.

### 4. Releases and Qualification (`/repos/acme/widgets/releases`)

1. Navigated to `/repos/acme/widgets/releases`.
2. Form validation for release creation:
   - Attempted creating a release with spaces (`invalid release name with spaces`); verified validation error banner displayed.
   - Created a valid release (`rel-<timestamp>`); verified release status transitioned to `Provisioning` and candidate `.rc.1` was created in state `cutting`.
   - Verified that action buttons `"Cut new RC"` and `"Promote current RC"` were initially disabled during cutting.
3. Qualification plan configuration:
   - Toggled `"Promote automatically once qualification succeeds"` and `"Require approval before running qualification tasks"`.
   - Saved qualification plan and confirmed settings retained.
4. External boundary simulation via `/tmp/releasehelper`:
   - **Simulated Error State**: Invoked `mark-candidate-error`; reloaded UI and verified error banner with simulated failure reason (`"Simulated remote git branch failure during candidate cut"`).
   - **Simulated Active/Cut State**: Invoked `mark-release-provisioned`, `mark-candidate-cut`, and `sync-qualifications`; reloaded UI and verified candidate transitioned to `active`, and `"Promote current RC"` button became enabled.

### 5. Release Trains Fallback Routes

1. Navigated to `/repos/acme/widgets/trains` and verified route fallback renders cleanly without unhandled errors.
2. Navigated to `/releasetrains` and verified route fallback renders cleanly without unhandled errors.

## Verification & Automated Tests

All functionality verified via automated Playwright end-to-end tests and unit test suites:
- `ui/e2e/repository_release_memory_walkthrough.spec.js`: Automated full operator walkthrough spec (passed).
- `ui/e2e/repo-pane.spec.js`: Repository pane e2e tests (passed).
- `ui/e2e/repo-ci-capabilities.spec.js`: Repository CI capabilities tests (passed).
- `ui/e2e/memory.spec.js`: Memory UI e2e tests (passed).
- `npm run test:e2e`: Full Playwright e2e test suite (162 passed, 1 skipped).
- Vitest unit tests: 133 tests passed across `RepoReleases.test.jsx`, `RepoList.test.jsx`, `RepoPage.test.jsx`, `MemoryPanel.test.jsx`, `RepoReleaseWorkflowSettings.test.jsx`.

## Stubs and Mocks

No UI mocks were used. The walkthrough was executed against a live `grain demo` Go HTTP backend serving embedded SQLite database records to a real Chromium browser instance. External GitHub git reconciler operations (such as remote branch creation and PR promotion) were simulated at the database layer via `/tmp/releasehelper` executing native model store state transitions.
