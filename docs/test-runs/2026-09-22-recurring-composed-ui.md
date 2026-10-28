# Recurring and composed-work UI walkthrough — 2026-09-22

## Test subject and environment

- Commit: `33436cad69dd9dbf86edfcdb3c2b9cc8ef245922`
- Environment: Debian bookworm, Go 1.26.6, Node 20.19.0, npm 10.8.2.
- Browser: Playwright 1.57.0 with Chromium 151.0.7922.34 (Chrome for Testing) at 1440×1100.
- Built with `make frontend grain grit`, served by real `bin/grain demo -addr 127.0.0.1:<port> -open=false` backed by the embedded SQLite store (`sqlite.Open`/`model.New`) and `pkg/ui.Server`.
- Automated walkthrough runner: `ui/e2e/walkthrough_runner.mjs` executing all 8 phases end-to-end against real DOM and network endpoints.

## Walkthrough and outcome

All mutations (create, edit, run, pause, resume, run-now, and delete) were executed through real UI controls, verified through DOM states, list reloads, and deep-link direct URLs.

### 1. Initial lists and empty filter states
- Navigated across `/schedules`, `/templates`, and `/suites`.
- Zero counts and empty-state copy confirmed (`No schedules yet.`, `No templates yet.`, `No suites yet.`, `No suite runs yet.`).
- Tab navigation verified: focusing the search input, sort selector, and `+ New ...` action button in natural logical order.
- Filtering/searching with non-matching strings displayed `No schedules match your search.`, `No templates match your search.`, and `No suites match your search.`.

### 2. Template creation, validation, composition & editing
- Form validation tested by submitting blank required fields, verifying browser validation on `name` and `title`.
- Created comprehensive template `Chore template`:
  - Name, title, and description.
  - Bound repo `acme/widgets` and base branch `main`.
  - Additional writable repo `acme/extra-write` on `feature-branch`.
  - Read-only repo `acme/docs-repo`.
  - Auto-merge enabled, network egress override set to `Default`.
  - Capability enabled: `Self debug`.
  - Custom fields: text, date, number, boolean, enum, and multi-enum.
- List presentation verified: chips for repo, base branch, auto-merge, and capabilities rendered accurately.
- Reopened template via row click and direct URL `/templates/:id`: all fields, capabilities, and custom field values preserved.
- Edited template title to `Chore template updated` and verified persistence across page reload.
- Tested search filtering (matching query and non-matching query) and sort ordering by Name.

### 3. Suite creation, inline template & editing
- Created suite `Nightly Suite` in `until_clean` mode with `maxPasses = 4`, `requireApproval = true`, and `autoMerge = true`.
- Selected existing template `Chore template updated`.
- Executed inline template creation directly from the suite editor (`+ New template`): created `Lint step` (`Run linter across repo`), which immediately saved and was automatically preselected in the suite template list.
- Composition preview verified that effective titles, bodies, placements, write repos, read repos, capabilities, custom fields, and execution mode were accurately computed and rendered.
- Reopened and edited mode to `count` (2 passes), then reverted to `until_clean` (max 4), verified state persistence across reload.
- Tested search filtering and sort selector on `/suites`.

### 4. Schedule creation, recurrence variants & actions
- Created three distinct schedule types:
  1. **Template-backed schedule**: `Periodic cleanup of {{service}}`, target repo `acme/widgets`, cadence `Daily` at `09:15`, overlap policy `Start it anyway` (`allow`). Real-time composition preview resolved template values from `Chore template updated`.
  2. **Raw task schedule**: `Weekly security sweep`, target repo `acme/widgets` (branch `main`), cadence `Weekly` (Tuesday at `14:30`), overlap `Start it anyway`, with title and description.
  3. **Suite-running schedule**: `Nightly Suite`, target repo `acme/widgets`, cadence `Monthly` (Day 15 at `07:45`), overlap `Skip firing` (`skip`).
- Verified throwaway cadence variations: `Every N hours` (every 6 hours), `Daily`, `Weekly`, and `Monthly`.
- Deep-linked to `/schedules/:id`:
  - **Pause**: clicked `Pause`, verified `.MuiAlert`/chip updated to `Paused`, persisted across reload.
  - **Resume**: clicked `Resume`, verified `Paused` chip removed.
  - **Run now**: clicked `Run now`, verified confirmation dialog (`Run "Weekly security sweep" now?`), confirmed action, verified schedule row updated with `Last ran` timestamp while preserving the scheduled `Next run` timestamp.

### 5. Suite runs execution & inspection
- Started runs from both entrypoints:
  1. Direct suite row `Run…` button (repo `acme/widgets`, branch `main`).
  2. Runs header `+ Run` button (suite `Nightly Suite`, repo `acme/widgets`, branch `release-1`).
- Seeded multi-pass state matrix (`active`, `failed`, `succeeded` with 2 passes and child follow-up tasks linked via `LinkProposedBy`).
- Reloaded `/suites` and inspected runs list: verified status chips (`Active`, `Failed`, `Succeeded`), branch badges, pass counters, and error annotations.
- Navigated to deep-link `/suites/runs/:id` for the succeeded multi-pass run:
  - Verified Pass 1 and Pass 2 grouping headers.
  - Verified nested follow-up task representation (`Task #38 proposed follow-up`).
  - Clicked task row and verified direct navigation to task detail at `/tasks/:id`.

### 6. Repository-declared items & refusal
- Seeded repository declarations into SQLite via `store.SyncRepoDeclarations`:
  - Declared template: `repo:template:acme:widgets:nightly` (`acme/widgets` in `.grain/templates/nightly.yml`).
  - Declared schedule: `repo:schedule:acme:widgets:nightly` (`acme/widgets` in `.grain/schedules/nightly.yml`).
  - Declared suite: `repo:suite:acme:widgets:smoke` (`acme/widgets` in `.grain/suites/smoke.yml`).
- Verified declared rows render provenance indicators in their list rows.
- Verified mutation refusal:
  - Attempting to edit a declared template, schedule, or suite returns HTTP 400 with refusal message displayed in the error banner (e.g. `repo:template:... is declared by acme/widgets in .grain/templates/nightly.yml, so it is not this deployment's to edit`).
  - Attempting to delete a declared template, schedule, or suite displays confirmation and then returns HTTP 400 refusal in the error banner.

### 7. Dependency-aware deletions & cleanup
- Attempted to delete `Chore template updated` while referenced by active schedule `sched-3`:
  - Server returned HTTP 400: `template is used by 1 schedule(s); repoint or delete those first`.
  - Displayed dependency error banner in UI.
- Deleted all test schedules with confirmation modals (`Weekly security sweep`, `Periodic cleanup`, `Nightly Suite`).
- Deleted test suite `Nightly Suite` with confirmation dialog.
- Deleted test templates `Chore template updated` and `Lint step`.
- Verified that historical suite runs and their spawned task records remained intact in the database and UI.

### 8. Error resilience (503 error injection)
- Injected HTTP 503 on `GET /api/templates` and navigated to `/templates`.
- Verified UI displayed error banner with service unavailable message without crashing the application shell or navigation rail.

## Stubs and diagnostics

- **Execution boundary**: Since `grain demo` does not run active LLM background workers, task execution transitions and multi-pass suite progression were seeded via `ui/e2e/walkthrough_helper.go` using real Go store methods (`store.CreateSuiteRun`, `store.FireNextPass`, `store.PutTask`, `store.LinkProposedBy`, `store.CompleteSuiteRun`, `store.SyncRepoDeclarations`). All UI components, React hooks, API handlers, and SQLite queries ran in full production mode without mock handlers.
- **Diagnostics**:
  - Console and network logs were monitored throughout the walkthrough.
  - Expected 404 responses on `/api/repos/.../github-branches` (demo environment lacks live GitHub API connection; fallback to free-text branch entry worked as designed).
  - Expected 400 responses on declaration edit/delete refusals and schedule dependency deletions.
  - Expected 503 response on injected error test.
  - Zero unexpected JavaScript exceptions or server 500 errors.

## Verification

- Vitest unit tests: 2,336 tests passed across 128 test files.
- End-to-end automated runner (`ui/e2e/walkthrough_runner.mjs`): All 8 phases passed (100% success).
- Go packages built and verified cleanly (`bin/grain`, `bin/grit`).
