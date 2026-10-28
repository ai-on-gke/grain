# Repository, Merge Queue, Compare, and Release UI Walkthrough (2026-10-22)

## Environment & Setup

- **Commit tested**: `747f82455` (branch `grain/049f1e09/task-178`)
- **Platform**: Linux `6.12.76+deb13-amd64` x86_64, Node.js `v20.19.0`, Go `1.24`, Playwright Chromium (headless)
- **Server command**:
  ```bash
  /tmp/grain demo -addr 127.0.0.1:0 -open=false \
    -access-code-file <temp-access-code> \
    -github-sim -fake-ci -scheduled-workflows \
    -secrets-store -merge-rules -reconcile=500ms
  ```
- **Viewports & themes exercised**: Desktop (`1280×800`) and mobile portrait (`390×844`) across light and dark themes.

## External Boundary Stubs & Helpers

No part of Grain's HTTP API or React frontend was mocked or intercepted during the browser walkthrough. Only external system boundaries unavailable in an isolated sandbox were backed by Grain's built-in demo simulators and E2E helper:

1. **`-github-sim`**: Creates a local bare Git repository (`githubsim/acme/widgets.git`) and simulates GitHub's branch/PR/comparison endpoints against it. We cloned this bare repo over `file://`, pushed a branch (`release/1.0`) with `RELEASE.md` for `/compare/main...release/1.0`, and pushed `.grain/templates/lint-audit.yml` and `.grain/settings.yml` to `main` to exercise real Git-backed declaration syncing and branch diffs.
2. **`-fake-ci`**: Seeds deterministic Grain CI suite runs, jobs, logs, and merge-rule histories for `acme/widgets`.
3. **`-secrets-store`**: Provides an ephemeral encrypted file-backed secret store so custom release secrets (`NPM_PUBLISH_TOKEN`) can be written and read back through `/api/repos/acme/widgets/release/secrets`.
4. **`-merge-rules`**: Seeds two merge-rule histories and declaration defaults on `acme/widgets`.
5. **`cmd/e2e-release-helper` (`/tmp/releasehelper`)**: Connects directly to the demo process's SQLite store (via `/api/e2e/release-helper` discovery) to advance the external GitHub release branch/tag provisioning state (`mark-candidate-error`, `mark-release-provisioned`, `mark-candidate-cut`, `sync-qualifications`) that would otherwise require live GitHub remote branch operations.

---

## Walkthrough Steps & Outcomes

### 1. Repository List (`/repos`) & Settings → Repositories (`/settings/repositories`)

1. **Navigation & count badge**:
   - Opened `/` and clicked the `Repos` button in the left navigation rail; navigated to `/repos`.
   - Verified the header count badge read `1` (`acme/widgets`), and deep-linked directly to `/repos` with a full page reload.
2. **Adding repositories & long-name layout**:
   - Navigated to `/settings` (`Settings → General`), added `acme/gadgets` and `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name` to `Target repositories`, blurred the input to save, and returned to `/repos`.
   - Verified the `/repos` count badge updated to `3` and all three repositories rendered with activity/task summaries.
   - Resized the viewport to `390×844` (mobile portrait) and verified `document.documentElement.scrollWidth <= document.documentElement.clientWidth` (`reposPhoneOverflow: false`) even with the 82-character repository name.
3. **Search & Sort controls**:
   - Typed `gadgets` into `Search repositories`; verified only `acme/gadgets` remained visible (`1` row).
   - Typed `no-such-repo-xyz`; verified the empty message `"No repositories match \"no-such-repo-xyz\"."` appeared. Cleared search.
   - Exercised all four `Sort` options:
     - `Name (A–Z)`: ordered `acme/gadgets`, `acme/widgets`, `organization-with-a-very-long-name/...`; drag handles hidden (`0`).
     - `Active first`: ordered `acme/widgets`, `acme/gadgets`, `organization-with-a-very-long-name/...`; drag handles hidden.
     - `Most tasks`: ordered `acme/widgets` (13 tasks), `acme/gadgets`, `organization-with-a-very-long-name/...`; drag handles hidden.
     - `Custom order`: restored all `3` `button.task-drag-handle` reorder handles.
4. **Pointer and keyboard/tap reordering**:
   - Clicked the first row's `Move ...` handle (`button.task-drag-handle`); verified the `Moving 1 repo` banner and `button.reorder-slot-button` drop slots appeared. Clicked the last drop slot; verified the row moved to the bottom and the banner closed.
   - Focused the first row's `Move ...` handle and pressed `Enter`, focused the last drop slot and pressed `Enter`; verified the row moved to the bottom and focus returned directly to the moved row's own `Move ...` button (`activeElement` matched the moved row's handle `aria-label`) rather than falling to `<body>`.
   - Reloaded `/repos` and verified the custom order persisted via `localStorage` (`grain:repoOrder`).
5. **Settings → Repositories table (`/settings/repositories`)**:
   - Opened `/settings/repositories` and verified all 3 repositories rendered with columns for Repository, Base branch (`main`), Declarations (`Read` / `Not read`), CI (`Grain CI` / `GitHub CI`), and Git connection (`Not listed`), with links targeting the corresponding `/repos/<owner>/<name>` tabs.

### 2. Repository Detail, Task Count Link & Scoped Task Filing (`/repos/acme/widgets`)

1. **Overview & state chips**:
   - Clicked `acme/widgets` from `/repos` to open `/repos/acme/widgets`.
   - Verified the header linked `acme/widgets` to `https://github.com/acme/widgets` (`target="_blank"`) and rendered per-state chips (`proposed 1`, `queued 2`, `running 2`, `awaiting reply 2`, `awaiting submit 2`, `queued for merge 2`, `deferred 1`, `closed 1`, `blocked 1`) alongside the `13 tasks filed here` link.
   - Clicked `← Repos` to return to `/repos`, then re-opened `/repos/acme/widgets`.
2. **Task count link & browser Back**:
   - Clicked `13 tasks filed here`; verified navigation to `/?f=repo:any:acme/widgets` with filtered task rows visible.
   - Used browser `Back` (`page.goBack()`); verified navigation returned cleanly to `/repos/acme/widgets`.
3. **Filing a task from the repo page**:
   - Clicked `New task` on `/repos/acme/widgets`; verified the `New task` overlay opened at `/new?repo=acme%2Fwidgets` with `Repos` preselected to `acme/widgets`.
   - Entered title and description, added `acme/gadgets` via `Another repo (lands a change there too)` with base branch `candidate`, and added `organization-with-a-very-long-name/repository-with-an-equally-long-descriptive-name` under `Read-only repos`.
   - Opened the `Read-only repos` autocomplete menu and pressed `Escape`; verified `Escape` closed only the menu while keeping the `New task` drawer and typed inputs intact.
   - Clicked `Create task`; verified the overlay closed and returned to `/repos/acme/widgets`, with the repo task count incrementing to `14 tasks filed here`.

### 3. Branch Rules, Capabilities, Instructions, Declarations & Workflows

1. **Branch rules (`/repos/acme/widgets/settings/branches`)**:
   - Added branch `task-396/operator-branch` cut from `main`; reloaded and verified it persisted in the branch list.
   - Submitted `task-396/operator-branch` a second time; verified the inline notice `"task-396/operator-branch is already on this repo's list"` appeared without duplicating the row.
2. **CI & Default capabilities**:
   - On the `CI` tab (`/repos/acme/widgets/settings/ci`), selected `GCP key` under `CI capabilities` (`PUT /api/repos/acme/widgets/ci-capabilities`).
   - On the `Agents` tab (`/repos/acme/widgets/settings/agents`), selected `Self debug` under `Default capabilities` (`PUT /api/repos/acme/widgets/capabilities`).
   - Reloaded and verified both capability chips persisted on their respective tabs.
3. **Prompt extension, setup command & declaration syncing**:
   - Cloned `githubsim/acme/widgets.git`, committed `.grain/templates/lint-audit.yml` and `.grain/settings.yml` (`prompt-extension` and `setup-command`) to `main`, and pushed.
   - On the `Advanced` tab (`/repos/acme/widgets/settings/advanced`), toggled `Read acme/widgets's own declarations (.grain/templates, .grain/suites, .grain/schedules, .grain/workflows, .grain/merge-rules.yml, .grain/settings.yml)` off and back on, waited for the 500ms reconciler tick, and reloaded.
   - Verified the declaration status line reported `Read at 59ba43d5ea23: 1 template(s), 0 suite(s), 0 schedule(s), 0 trigger(s)`.
   - Switched to the `Agents` tab and verified the declared prompt extension (`Declared repo instructions from settings.yml.`) and declared setup command (`echo declared-setup`) were displayed alongside the operator override fields (`Prompt extension for acme/widgets` and `Setup command for acme/widgets`), which saved on blur and persisted across reloads.
4. **CI workflows (`.grain/workflows/*.yml`)**:
   - On the `CI` tab, clicked `Add workflow`, entered `e2e-test.yml`, typed invalid YAML (`invalid: [yaml: broken`), and blurred; verified the error alert appeared.
   - Replaced with valid workflow YAML and blurred; reloaded and verified `.grain/workflows/e2e-test.yml` persisted.
   - Clicked `Remove workflow .grain/workflows/e2e-test.yml`, accepted the confirmation dialog, reloaded, and verified `"grain declares no workflows for this repo."` returned.
5. **Repo-scoped release configuration & work ownership (`/repos/acme/widgets/settings/releases`)**:
   - Added custom secret `NPM_PUBLISH_TOKEN` via `Add custom secret`; verified it appeared in the secrets table with a `Set` chip.
   - Toggled work ownership buttons (`the repository default: on` / `say nothing` and `release: off` / `on`) and verified the `Automatic releases` switch stayed synchronized without a reload.

### 4. Merge Queue, Base-Branch Gate Warnings & Merge Batch Panel

1. **CI mode switching & base-branch gate warnings**:
   - Opened `/repos/acme/gadgets/settings/ci`, enabled `Watch acme/gadgets's default branch`, and selected `Use GitHub CI`.
   - Verified the red warning alert appeared on `/repos/acme/gadgets` (`Overview` and `CI` tabs) and on `/repos/acme/gadgets/releases`:
     > `"Nothing is testing acme/gadgets's default branch, and the merge queue is merging onto it anyway. grain CI is not enabled for this repository..."`
   - Verified the `CI` tab carried a red notification badge dot and the alert's `Go to CI Mode` button navigated to `/repos/acme/gadgets/settings/ci` and scrolled the `CI Mode` radio group into view.
   - Switched `CI Mode` to `Use Grain CI`; verified the base-branch gate alert and red tab dot cleared immediately without a page reload.
   - Switched `CI Mode` to `No CI`; verified the amber warning alert appeared explaining that tasks and the merge queue will not wait for any checks. Restored `Use Grain CI`.
2. **Strict branch up-to-date rule & unfixable checks**:
   - Enabled `Require branches to be up to date before merging` on `acme/widgets`.
   - In `Checks a task cannot fix`, attempted to add `grain-ci`; verified the server refused with an explanation that `grain-ci` judges one exact commit.
   - Added `cla/google` and attempted to add `CLA/GOOGLE`; verified the case-insensitive duplicate refusal (`acme/widgets already declares CLA/GOOGLE unfixable`).
3. **Global merge queue settings (`/settings/engine`)**:
   - Edited `Max merges per repo branch` (`2`), `Merge batch size` (`3`), and `Merge gather window` (`45s`); reloaded `/settings/engine` and verified all three values persisted.
4. **Merge batch panel on repo page and task page**:
   - On `/repos/acme/widgets` (`Overview`), inspected the `Merge batches` panel showing `2 changes on main`, status `testing`, `5m of 90m`, temp branch `grain-merge-queue/e157d468/main at abcdef012345`, and members `#101 Rotate the Gemini signing key` and `#102 Cache the capability readiness probe`.
   - Clicked `Rotate the Gemini signing key` in the batch list; verified it opened `/tasks/5` in place, rendered the task-level `Merge batch` panel on `/tasks/5`, and browser `Back` returned to `/repos/acme/widgets`.

### 5. Branch Comparison (`/repos/acme/widgets/compare/<base>...<head>`)

1. **Valid comparison & reload**:
   - Pushed `RELEASE.md` to branch `release/1.0` in `githubsim/acme/widgets.git`.
   - On `/repos/acme/widgets/settings/branches`, entered Base `main` and Head `release/1.0`, and clicked `Compare`.
   - Verified navigation to `/repos/acme/widgets/compare/main...release/1.0`, inspected the rendered unified diff for `RELEASE.md`, and reloaded the direct URL to confirm it rendered identically.
2. **Unknown ref handling**:
   - Navigated to `/repos/acme/widgets/compare/main...nonexistent-branch-999`.
   - Verified the server classified the git error as `502 Bad Gateway` (`upstream_unavailable` with a log reference ID) and the UI rendered an error alert while keeping the `← acme/widgets` back button interactive.

### 6. Releases & Release Trains (`/repos/acme/widgets/releases`)

1. **Validation & creation**:
   - Opened `/repos/acme/widgets/releases`, typed `bad release name` into `New release name`, and clicked `Create release`; verified the error alert explained the naming rules.
   - Created release `v2026.10`; verified navigation to `/repos/acme/widgets/releases/v2026.10`, status `Provisioning`, candidate `v2026.10.rc.1 -- cutting`, and disabled `Cut new RC` / `Promote current RC` buttons while cutting.
2. **Qualification plan & candidate lifecycle**:
   - Toggled `Promote automatically once qualification succeeds` and `Require approval before running qualification tasks` and clicked `Save qualification plan`.
   - Used `/tmp/releasehelper` to simulate a remote git branch failure (`mark-candidate-error`), reloaded, and verified `Simulated remote git branch failure` rendered on the candidate card.
   - Used `/tmp/releasehelper` to transition the release to provisioned and the candidate to cut (`mark-release-provisioned`, `mark-candidate-cut`, `sync-qualifications`), reloaded, and verified `v2026.10.rc.1 -- active` rendered and `Promote current RC` became enabled.
   - Clicked `Promote current RC`, confirmed the dialog (`"Promote v2026.10.rc.1 to v2026.10? This moves acme/widgets's v2026.10 branch to v2026.10.rc.1."`), and verified the status updated to `promoting`.
3. **Fallback routes & responsive check**:
   - Visited `/repos/acme/widgets/trains` and `/releasetrains` and verified clean fallback rendering with zero console/page errors.
   - Checked `/repos/acme/widgets/releases/v2026.10` at `390×844` and verified zero horizontal overflow (`releasePhoneOverflow: false`).

---

## Bugs Found & Fixed

### Bug 1: Unsaved edits on `RepoPage` were silently discarded when leaving via `Releases`, `Compare`, `All runs for this repo`, or the header `System` / `Settings` buttons

- **Reproduction**:
  1. Open `/repos/acme/widgets/settings/ci`, click `Add workflow`, and type `unsaved.yml` into `File` (leaving the `YAML` field empty so the row is unsaved).
  2. Click the `Releases` button in the repo header, or switch to `Branches` and click `Compare`, or switch to `Overview` and click `All runs for this repo`, or click `System` / `Settings` in the top-right header.
  3. **Observed**: The page navigated away immediately without asking `You have unsaved changes here. Leave and discard them?`, losing the half-written workflow (or unsaved release job box edits).
- **Fix**:
  - Guarded the `Releases` button (`ui/src/components/RepoPage.jsx`), the `Compare` form submit (`ui/src/components/RepoPage.jsx`), and the `All runs for this repo` callback (`ui/src/components/RepoPage.jsx` and `ui/src/App.jsx`) with `leavingLosesEdits() && !window.confirm(DISCARD_PROMPT)`, and wired `confirmLeaveView()` to the header `System` and `Settings` buttons in `ui/src/App.jsx`.
- **Regression coverage**:
  - Unit test in `ui/src/components/RepoPage.test.jsx`: `"asks before discarding unsaved workflow edits when leaving via Releases, Compare, or All runs for this repo"`.
  - E2E test in `ui/e2e/repo-tabs-walkthrough.spec.js`: `"2. a half-written CI workflow survives a look at Releases, and leaving asks first"`.

### Bug 2: Opening a task or the `New task` overlay from `RepoPage` used `history.replaceState` instead of `pushState`, breaking browser `Back`

- **Reproduction**:
  1. Navigate from `/repos` to `/repos/acme/widgets`.
  2. Click a task link in the `Merge batches` panel (opening `/tasks/5`) or click `New task` (opening `/new?repo=acme%2Fwidgets`).
  3. Click browser `Back`.
  4. **Observed**: In `ui/src/App.jsx`, `otherTab` evaluated `view === "repos" && !!openRepo && shown.view === "repos" && shown.repo === openRepo` without checking whether a task overlay (`openTaskId`) or new-task overlay (`showNewTask`) was opening or closing. As a result, `App.jsx` called `window.history.replaceState` instead of `pushState`, overwriting `/repos/acme/widgets` in the session history so browser `Back` skipped `/repos/acme/widgets` and landed on `/repos`.
- **Fix**:
  - Updated `otherTab` in `ui/src/App.jsx` to require `!openTaskId && !showNewTask && !shown.taskId && !shown.showNewTask` so opening or closing a task or new-task overlay over `RepoPage` pushes a new history entry.

### Bug 3: Modified clicks (`Ctrl+click`, `Cmd+click`, `Shift+click`, middle-click) on `<Link href=...>` in `RepositoriesSection`, `RepoList`, and `NoGitConnectionNotice` navigated in-place

- **Reproduction**:
  1. Open `/settings/repositories` or `/repos`.
  2. `Ctrl+click` (or `Cmd+click` / middle-click) a repository link or tab link (`Grain CI`, `github-token`, `Set up a Git connection`).
  3. **Observed**: The `onClick` handler unconditionally called `evt.preventDefault()` and navigated the current tab in-place in addition to (or instead of) opening the link in a new browser tab.
- **Fix**:
  - Added `if (evt.button !== 0 || evt.metaKey || evt.ctrlKey || evt.shiftKey || evt.altKey) return;` guards before `evt.preventDefault()` in `ui/src/components/settings/RepositoriesSection.jsx`, `ui/src/components/RepoList.jsx`, and `ui/src/components/NoGitConnectionNotice.jsx`.
- **Regression coverage**:
  - Unit test in `ui/src/components/settings/RepositoriesSection.test.jsx`: `"lets modified clicks on repository and section links open in a new tab without navigating in place"`.

### Bug 4: Demo seed data omitted `MergeQueueState` on the batch's completed task, hiding `<MergeBatchPanel>` when clicking into the task from the repo page

- **Reproduction**:
  1. Start `grain demo` and open `/repos/acme/widgets`.
  2. In the `Merge batches` panel, click `#101 Rotate the Gemini signing key` (task `#5`).
  3. **Observed**: `/tasks/5` opened, but the task detail view did not render `<MergeBatchPanel>` because `DetailOverlay.jsx` only fetches `/api/tasks/<id>/merge-batch` when `task.mergeQueueState` is `"queued"` or `"batching"`, and `seedDemo` in `pkg/demo/demo.go` only set `MergeQueueState = "batching"` on `awaitingSubmit` (`#6`), not on `completed` (`#5`).
- **Fix**:
  - Updated `seedDemo` in `pkg/demo/demo.go` to set `MergeQueueState = "batching"`, `MergeQueueStateAt`, and `MergeQueueNote` on `completed.ID` (`#5`) to match the seeded `MergeBatch` row.
