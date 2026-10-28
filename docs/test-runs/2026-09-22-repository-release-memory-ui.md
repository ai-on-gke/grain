# Repository, Release, and Memory UI Walkthrough

## Scope and environment

- Repository: `bwsalmon/grain`
- Walkthrough commit tested: `27e7f17cfed7ec29dfe4b22c16b95dcc3f58e02f`
- Branch: `grain/task-308`
- Host: Debian bookworm, Linux amd64
- Go `1.26.6`; Node `20.19.0`; npm `10.8.2`
- Playwright `1.62.1`, Chromium `151.0.7922.34`
- Server: Real `grain demo` server (`go run ./cmd/grain demo -addr 127.0.0.1:0 -open=false`) with an embedded SQLite throwaway store under `/tmp`
- Frontend: Built production assets via `make frontend` into `pkg/ui/static`
- External-boundary simulation: Compiled helper `/tmp/releasehelper` used to simulate remote GitHub branch operations and trigger qualification plan reconciliation without mocking Grain APIs or React UI

All operations exercised real UI components, event handlers, client-side routing, and real server API endpoints over HTTP.

## Walkthrough

### 1. Repositories Index (`/repos`)

1. Opened the Repositories index both from the navigation rail ("Repos") and via direct URL (`/repos`).
2. Verified page layout and initial counts:
   - Header badge displayed `1` repository.
   - Seeded `acme/widgets` row displayed per-state status chips and `12 tasks` total.
3. Added two disposable known repositories to test search, sort, and responsive layout:
   - `acme/gadgets`
   - `example-corporation/exceptionally-long-repository-name-for-responsive-layout` (73 characters)
4. Tested repository search filtering:
   - Case-insensitive substring match (`WIDGETS`) immediately filtered list to 1 matching item (`acme/widgets`).
   - Non-matching query (`nonexistent-repo-query`) rendered the empty state: `"No repos match your search."`.
   - Clearing search restored all 3 repository rows.
5. Tested repository sorting controls:
   - **Name (A–Z)**: Ordered rows as `acme/gadgets`, `acme/widgets`, `example-corporation/...`. Reorder handles were omitted as expected.
   - **Active first**: Sorted by active task status; reorder handles omitted.
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
   - Set task title, description, and base branch (`release/task-308`).
   - Added a secondary writable repository (`acme/gadgets` targeting branch `schema/task-308`) via `WriteReposField`.
   - Added a read-only repository (`example-corporation/exceptionally-long-repository-name-for-responsive-layout`) via `ReadOnlyReposField`.
   - Submitted task creation and verified navigation to `/tasks`.
   - Opened the newly created task detail overlay and verified that base branch, write targets, and read-only repositories were persisted and displayed accurately.
4. Branch rules and branch management:
   - Added branch `task-308/demo-branch` using the branch form.
   - Observed status as pending and verified persistence across a full page reload.
5. Capabilities configuration:
   - Configured repository CI capabilities: Selected `GCP key`, saved, and confirmed persistence after reload.
   - Configured repository default capabilities: Selected `Self debug`, saved, and confirmed persistence after reload.
6. Prompt extension and setup command:
   - Entered a custom prompt extension (`"Standing prompt extension for acme/widgets walkthrough."`) and clicked `"Save prompt extension"`.
   - Entered a custom setup command (`"npm test && echo setup-done"`) and clicked `"Save setup command"`.
   - Reloaded the page and confirmed that both fields retained their saved values.
7. Declarations syncing (`.grain/workflows/*.yml`):
   - Tested invalid workflow YAML (missing `on:` block); verified that saving was rejected with a clear parser error alert.
   - Entered valid workflow YAML (`name: Valid Workflow`, `on: push`, `runs-on: ubuntu-latest`) and saved successfully.
   - Reloaded page and confirmed `.grain/workflows/test.yml` persisted.
   - Clicked `"Remove"` and saved workflows to confirm deletion.

### 3. Memory UI (`/settings` -> Agents & Models)

1. Navigated to `/settings` and selected the `"Agents & Models"` tab.
2. Inspected memory scopes:
   - Deployment memory: Verified content (`make fmt before pushing...`).
   - Repository memory (`acme/widgets`): Verified content (`This repo's CI needs make frontend before go vet...`).
3. Inspected author attribution chips:
   - Confirmed `"written by the memory agent"` chip on agent-authored memories.
   - Confirmed `"edited here"` chip on operator-modified memories.
4. Inspected memory documents:
   - Listed `release-checklist` ("what a release of this deployment actually needs, in order") and `flaky-e2e`.
   - Verified relative age chips (`written 5 days ago` / `written months ago`).
   - Clicked `"Open"` to inspect `release-checklist` in place; verified full document text (`Tag from main only...`).
5. Made a reversible correction in repository memory:
   - Appended a test note to `acme/widgets` memory and clicked `"Save"`.
   - Verified `"written today"` chip.
   - Reloaded the page and confirmed that the daemon persisted and read back the updated text.
   - Reverted memory back to the original content and saved to leave the store clean.

### 4. Releases (`/repos/acme/widgets/releases`)

1. Navigated directly to `/repos/acme/widgets/releases`.
2. Form validation for release creation:
   - Entered invalid release name containing spaces (`"invalid release name with spaces"`) and clicked `"Create release"`.
   - Verified server rejection and error alert display.
3. Created a valid release (`task-308-rel`):
   - Observed release status as `Provisioning` with candidate `task-308-rel.rc.1 -- cutting`.
   - Confirmed `"Cut new RC"` and `"Promote current RC"` action buttons were initially disabled while provisioning/cutting.
4. Qualification plan configuration:
   - Toggled switches: `"Promote automatically once qualification succeeds"` and `"Require approval before running qualification tasks"`.
   - Navigated to `/templates` and created a template titled `"Release qualification smoke test"`.
   - Returned to releases, added the template to the qualification plan via `"Add item"`, and saved the plan.
5. Simulated external boundary lifecycle:
   - **Error state simulation**: Transitioned candidate to error status via `/tmp/releasehelper mark-candidate-error`. Reloaded page and verified error badge `(Simulated remote git branch failure)`.
   - **Success state & Qualification synchronization**: Marked release provisioned, candidate cut, and triggered `orchestrator.SyncQualifications`. Reloaded page and observed candidate status transition to `task-308-rel.rc.1 -- active` and `"Promote current RC"` button enabled.
   - Verified that the generated qualification task row rendered in `.qualification-task-list`.
   - Clicked the qualification task link; verified direct in-app navigation to `/tasks/{id}` displaying `"Release qualification smoke test"`.
   - Executed a single browser Back navigation and verified immediate return to `/repos/acme/widgets/releases`.

### 5. Release Trains (Removed Feature Fallback)

1. Release train functionality was removed in PR #1814 (commit `33436cad69dd9dbf86edfcdb3c2b9cc8ef245922`).
2. Navigated directly to legacy URLs:
   - `/repos/acme/widgets/trains`
   - `/releasetrains`
3. Verified that legacy routes handle gracefully without client runtime crashes or unhandled exceptions.

## External-Boundary Fixture

Grain's demo mode runs with an in-memory or embedded SQLite database without live GitHub credentials. To exercise the release lifecycle without modifying Grain's production API or React UI, an external helper (`/tmp/releasehelper`) was used to interact directly with the throwaway SQLite store:
- `mark-candidate-error`: Simulates remote git failure by writing an error message and state to the candidate row.
- `mark-release-provisioned` & `mark-candidate-cut`: Advances release and candidate to active states.
- `sync-qualifications`: Invokes qualification task creation matching the qualification plan items.

All HTTP APIs, state transformations, UI rendering, routing, and browser history transitions executed through the real production code.

## Diagnostics and Results

- Unexpected browser console errors: **0**
- Component tests (`vitest`): **119 passed** across `RepoReleases.test.jsx`, `RepoList.test.jsx`, `MemoryPanel.test.jsx`, and `RepoPage.test.jsx`.
- End-to-end browser walkthrough (`playwright` with Chromium): **1 passed** (22.0s duration), covering all repository, release, and memory workflows.
