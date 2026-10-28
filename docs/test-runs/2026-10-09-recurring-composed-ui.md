# Recurring and composed work UI walkthrough: 2026-10-09

grain/task-2521. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server. It follows
[2026-10-07-recurring-composed-ui.md](2026-10-07-recurring-composed-ui.md) and
exercises the recurring and composed work UI comprehensively through visible UI
controls, inspecting state transitions, declared configurations, dependency-aware
actions, error banners, and deep link navigations.

This walkthrough confirmed:
- Search and sort filters on all three lists (`/templates`, `/suites`, `/schedules`).
- Creating and editing a fully declared template with custom fields (`service`, `severity`),
  repo selection, base branch, and auto-merge toggling.
- Template deep linking via `/templates/:id`.
- Instantiating a task manually from a template using the "Use…" action and verifying
  token substitution (`{{service}}`, `{{severity}}`).
- Creating a suite with an inline template created on-the-fly (`+ New template`),
  run modes (`count` vs `until_clean`), pass ceiling controls, approval gates, and auto-merge.
- Scheduling work with diverse recurrence rules: Monthly on specific day/time in
  `Pacific/Auckland` timezone, and Weekly cadence in `UTC`.
- Schedule overlap policies (`Start it anyway` / `Wait for it`).
- Pausing, resuming, and immediately dispatching tasks via the "Run now" action with
  interactive confirmation modals.
- Scheduling a composed suite run and initiating suite executions both from row-level
  "Run…" triggers and the top-level "+ Run" dialog.
- Live composition resolution queries triggered by target branch specification.
- Progression of suite runs through multiple passes, verifying active, succeeded, and
  failed states, error banner reporting, scheduled run origin chips (`ScheduledChip`),
  and deep-link navigations to `/suites/runs/:id` (with graceful fallback for missing run IDs).
- Enforced read-only preservation of repository-declared templates, suites, and schedules
  (disabling mutations and deletes, and displaying origin repository chips).
- Dependency-aware deletion protection refusing deletion of templates referenced by
  active schedules and suites with exhaustive multi-dependent error banners.
- Orderly teardown of user-created entities and full keyboard tab-navigation traversal.

## Environment

| | |
| --- | --- |
| Commit tested | `5123d46d7705e7cfd61df454a4bf1d60df620640` (tip of `grain/task-2521`) |
| Branch | `grain/task-2521` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 |
| Server | `grain demo -addr 127.0.0.1:<port> -open=false`, built from this tree (`npm run build` into `pkg/ui/static`, then `go build ./cmd/grain`), throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, set by an init script |
| Viewport | 1400×900, browser zone UTC |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

Playwright executed against Chromium headless shell. Exploration and execution ran via
an end-to-end automation harness exercising visible UI controls, listening for browser
console messages, page errors, dialog confirmations, and HTTP interactions.

## Stubs

To advance and qualify suite runs through realistic multi-pass and error states without
requiring live agent VMs or GitHub credentials:
- An ephemeral Go helper (`suitestub`) interacting directly with the demo instance's
  embedded SQLite database through `github.com/ai-on-gke/grain/pkg/model`.
- **Pass 1 Task Progression:** `store.StartRun` and `store.FinishRun` to advance pass 1
  task instances to `success` or `error` outcomes.
- **Next Pass Triggering:** `store.FireNextPass` to transition the active suite run to
  subsequent passes, generating pass 2 task records.
- **Run Completion:** `store.CompleteSuiteRun` to set terminal suite run states (`succeeded`
  and `failed` with descriptive `LastError` explanations).
- All helper binaries and temporary test harnesses were run in throwaway directories and
  excluded from the git repository.

## Steps and Outcomes

### 1. Templates (`/templates`)
- **Search & Sort:**
  - Searching for `"release"` immediately filtered list items to show only matching
    seeded templates. Clearing the search input restored all items.
  - Sorting options (`Name`, `Newest first`, `Custom`) were selected and applied without error.
- **Template Creation:**
  - Opened `+ New template` dialog.
  - Entered name `walk-tmpl-<timestamp>`, task title `Audit {{service}} for <timestamp>`,
    and description `Check microservice {{service}} at severity {{severity}}.`.
  - Configured repository to `acme/widgets` and base branch to `main`. Checked `Auto-merge`.
  - Added declared inputs via `+ Add input`:
    - `service`: Label "Service Name", default "auth-service", required checkbox enabled.
    - `severity`: Label "Severity Level", options "low\nhigh", default "high".
  - Clicked `Add template`. Template row rendered immediately in the list.
- **Inline Editing & Deep Linking:**
  - Clicked the newly created row to reopen `Edit template`. Modified description text and clicked `Save`.
    Changes persisted accurately.
  - Navigated directly to `/templates/<id>`. The editor opened pre-filled with the template's details.
    Dismissed via `Escape`.
- **Manual Task Filing ("Use…"):**
  - Clicked `Use…` button on the template row, opening `File a task from a template`.
  - Field inputs correctly populated labels and default values. Submitted with `Create task`.
  - Task was successfully created and enqueued.

### 2. Suites (`/suites`)
- **Search & Filtering:**
  - Filtered suites using search query `"Smoke"`. List filtered as expected.
- **Suite Creation with Nested Inline Template:**
  - Opened `+ New suite`.
  - Configured name `walk-suite-<timestamp>`.
  - Selected `Run mode`: "Run a fixed number of times" with `Number of times` set to 2.
  - Enabled `Require approval for each pass's tasks` and disabled `Auto-merge`.
  - Clicked `+ New template` inside the suite overlay, opening a nested template dialog.
  - Created inline template `walk-inline-<timestamp>` with title and description, and clicked `Add template`.
  - Verified return to the suite editor with the newly created inline template automatically selected.
  - Added the earlier template `walk-tmpl-<timestamp>` to the suite.
  - Attempting to add a template with a required field lacking a default correctly produced a clear form validation
    banner warning that unattended suites cannot fill missing inputs without defaults.
  - Submitted `Add suite`. Suite row appeared in the list.
- **Suite Modification:**
  - Clicked suite row to edit. Switched mode to "Run until no repo changes (max passes)" and set `Max passes` to 3.
  - Saved successfully.

### 3. Schedules (`/schedules`)
- **Search:**
  - Verified schedule search filter responsiveness on `/schedules`.
- **Schedule 1: Monthly Task Runner with Timezone:**
  - Opened `+ New schedule`.
  - Selected template `walk-tmpl-<timestamp>`.
  - Set cadence to `Monthly`, day `31`, time `23:30`.
  - Set timezone to `Pacific/Auckland`. Set overlap policy to "Start it anyway" (`allow`).
  - Saved. Schedule row rendered displaying recurrence `monthly on day 31 at 23:30 Pacific/Auckland`.
- **Schedule Lifecycle Controls (Pause, Resume, Run Now):**
  - Reopened Schedule 1. Clicked `Pause`. Dismissed unsaved prompt. Row reflected `Paused` status.
  - Reopened Schedule 1 and clicked `Run now`. Confirmed prompt dialog:
    `"Run ... now? It starts a task from the template ... straight away; the schedule's own next run is unchanged."`
    Task was immediately dispatched.
  - Clicked `Resume`. Row updated, clearing the paused badge.
  - Edited schedule cadence to `Weekly on Friday` in `UTC` and saved.
- **Schedule 2: Composed Suite Runner:**
  - Created a second schedule. Switched `Fires` selector to `A suite`.
  - Selected suite `walk-suite-<timestamp>`, targeted branch `main`.
  - Submitted schedule. Row rendered showing suite association.
  - Clicked `Run now` with confirmation prompt, triggering an immediate suite run.

### 4. Suite Runs (`/suites` & `/suites/runs/:id`)
- **Launching Runs:**
  - Triggered a suite run from the suite's row `Run…` action targeting branch `feature/walk-branch`.
  - Monitored composition preview query resolution (`GET /api/suites/:id/composition?...`) on branch input blur.
  - Started another suite run from the top-level `+ Run` action selecting `Smoke Verification Suite`.
- **Suite Runs List Inspection:**
  - Verified presence of active suite runs with corresponding badges (`Active`, `Repo`, `Branch`).
  - Confirmed `ScheduledChip` on the scheduled run linking directly to its origin schedule `/schedules/sched-<id>`.
- **State Progression Verification:**
  - Advanced Run 1: Completed task, fired next pass via stub, marked run as `succeeded`.
    Refreshed and opened run detail pane: header rendered `Succeeded` chip, pass plan summary, and both `Pass 1`
    and `Pass 2` task lists.
  - Advanced Run 2: Failed task with error reason, marked run as `failed`.
    Navigated via deep link `/suites/runs/<id>`: header rendered `Failed` chip and error message banner
    `pass 1 task ended in error`.
- **Missing Resource Handling:**
  - Navigated to `/suites/runs/99999`. Handled gracefully with "no suite run #99999" notice.

### 5. Repository-Declared Work (`docs/declarations.md`)
- **Declared Template:**
  - Located `nightly-maintenance` marked with chip `Declared by acme/widgets`.
  - Opened editor. Confirmed `Save` and `Delete` buttons are disabled, preventing tampering with git-declared definitions.
- **Declared Suite:**
  - Located declared suite. Confirmed read-only locking on inputs, and disabled `Save` and `Delete` actions.
- **Declared Schedule:**
  - Located declared schedule. Confirmed read-only mode and disabled controls.

### 6. Dependency-Aware Deletion Protection & Cleanup
- **Deletion Refusal:**
  - Attempted to delete template `walk-tmpl-<timestamp>` while attached to both Schedule 1 and Suite 1.
  - Confirmed deletion dialog prompt.
  - Deletion was rejected with an alert banner naming all dependents:
    `template is used by 1 schedule(s) ("Audit auth-service for ...") and 1 suite(s) ("walk-suite-..."); repoint or delete the schedules and remove it from the suites first`.
- **Ordered Cleanup:**
  - Successfully deleted Schedule 1 and Schedule 2 after confirmation prompts.
  - Successfully deleted Suite 1 after confirmation prompt.
  - Successfully deleted template `walk-tmpl-<timestamp>` and inline template `walk-inline-<timestamp>`.
  - Verified all throwaway records were removed from list views.

### 7. Keyboard Accessibility
- Verified focus traversal via `Tab` across search fields, sort selects, and action buttons on `/suites`,
  opening dialogs via `Enter`, and dismissing overlays with `Escape`.

## Sort and Search Verification

| List | Filter / Query | Expected Result | Observed | Status |
| --- | --- | --- | --- | --- |
| `/templates` | Query: `"release"` | Shows only matching release templates | Exactly 1 matching template displayed | Pass |
| `/templates` | Sort: `Name`, `Newest`, `Custom` | Rows rearrange according to sort criteria | Reorders correctly; no errors | Pass |
| `/suites` | Query: `"Smoke"` | Shows only matching smoke suite | Exactly 1 matching suite displayed | Pass |
| `/schedules` | Query: `"Release"` | Filters schedules by matching text | Filtered list accordingly | Pass |

## Bugs and Findings

- **No New Bugs Discovered:** All tested interactions, dialog confirmations, input validations,
  and state updates functioned accurately according to specification. Previous fixes from
  `2026-10-06` and `2026-10-07` held robustly.
- **Known Unaddressed Items (Carried Forward):**
  - A schedule or suite referencing a template with an unfilled required input without a default will
    refuse creation with a helpful form alert; however, template declarations themselves do not warn
    at creation time if an input is marked required without a default.
  - Sort preferences are stored in component state and do not persist across full browser reloads.
  - Succeeded/failed suite runs and associated task records persist in SQLite history and cannot be deleted
    from the UI (by design).

## Tests Run

| Test Suite | Result | Notes |
| --- | --- | --- |
| Go unit tests (`go test -v ./pkg/ui -run "Template\|Suite\|Schedule"`) | Pass | Verified API handlers, validation rules, dependency deletion checks |
| Frontend build (`npm run build`) | Pass | Built clean production bundle into `pkg/ui/static` |
| Playwright e2e (`recurring-composed-walkthrough.spec.js` + workflow specs) | 16 / 16 Pass | All 16 automated Playwright end-to-end scenarios passed |
| End-to-end interactive browser walkthrough (`full_walkthrough.mjs`) | Pass | Complete lifecycle verified against real server and real Chromium browser |
