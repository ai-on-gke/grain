# 2026-10-10 recurring and composed work UI walkthrough

grain/task-2829. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server. It follows
[2026-10-09-recurring-composed-ui.md](2026-10-09-recurring-composed-ui.md) and
exercises the recurring and composed work UI comprehensively through visible UI
controls, inspecting state transitions, declared configurations, dependency-aware
actions, error banners, and deep link navigations.

This walkthrough confirmed:
- Search and sort filters on all three lists (`/templates`, `/suites`, `/schedules`).
- Creating and editing a fully declared template with custom fields (`service`, `severity`),
  repo selection, base branch, network access, capabilities (`Self debug`), and auto-merge toggling.
- Template deep linking via `/templates/:id`.
- Instantiating a task manually from a template using the "Use…" action and verifying
  token substitution (`{{service}}`, `{{severity}}`).
- Creating a suite with an inline template created on-the-fly (`+ New template`),
  run modes (`count` vs `until_clean`), pass ceiling controls, approval gates (`Require approval before each pass's tasks run`), and live composition preview (`What each pass files`).
- Scheduling work with diverse recurrence rules: Monthly on specific day/time in
  `Pacific/Auckland` timezone, and Weekly cadence in `UTC`.
- Schedule overlap policies (`Start it anyway` / `allow` and `Skip this run` / `skip`).
- Live composition preview inside schedule creation (`What this schedule files`).
- Pausing, resuming, and immediately dispatching tasks via the "Run now" action with
  interactive confirmation modals.
- Scheduling a composed suite run and initiating suite executions both from row-level
  "Run…" triggers and the top-level "+ Run" dialog.
- Progression of suite runs through multiple passes, verifying active, succeeded, and
  failed states, error banner reporting, scheduled run origin chips (`ScheduledChip`),
  and deep-link navigations to `/suites/runs/:id` (with graceful fallback for missing run IDs).
- Inspection of child tasks and proposed follow-up tasks nested under their parent tasks in the suite run detail pane.
- Enforced read-only preservation of repository-declared templates, suites, and schedules
  (disabling mutations and deletes, and displaying origin repository chips and file paths).
- Dependency-aware deletion protection refusing deletion of templates referenced by
  active schedules and suites with exhaustive multi-dependent error banners.
- Orderly teardown of user-created entities and full keyboard tab-navigation traversal.

## Environment

| | |
| --- | --- |
| Commit tested | `3dc993bee3f7f2a7cdb2d1a8384c4e4791a042b1` (tip of `grain/task-2829`) |
| Branch | `grain/task-2829` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 |
| Server | `grain demo -addr 127.0.0.1:<port> -open=false`, built from this tree (`npm run build` into `pkg/ui/static`, then `go build -tags grain_e2e ./cmd/grain`), throwaway SQLite store under a private `TMPDIR` |
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
  embedded SQLite database through `github.com/bwsalmon/grain/pkg/model`.
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
    seeded templates (1 matching row).
  - Searching for a nonexistent filter query resulted in 0 rows.
  - Sorting options (`Name`, `Newest first`, `Custom`) were applied without error.
- **Template Creation:**
  - Opened `+ New template` dialog.
  - Entered name `walk-tmpl-<timestamp>`, task title `Production Audit <timestamp>: Verify Security & Integration`,
    and description `Audit microservice {{service}} at severity {{severity}}.`.
  - Configured repository to `acme/widgets` and base branch to `main`. Checked `Auto-merge`.
  - Added declared inputs via `+ Add input`:
    - `service`: Label "Service Name", default "auth-service", required checkbox enabled.
    - `severity`: Label "Severity Level", options "low\nhigh", default "high".
  - Configured network access to `Egress` and capabilities to `Self debug`.
  - Clicked `Add template`. Template row rendered immediately in the list.
- **Inline Editing & Deep Linking:**
  - Clicked the newly created row to reopen `Edit template`. Modified title to append `(v2 Updated)` and clicked `Save`.
    Changes persisted accurately.
  - Navigated directly to `/templates/<id>`. The editor opened pre-filled with the template's details.
    Dismissed via `Cancel`.
- **Manual Task Filing ("Use…"):**
  - Clicked `Use…` button on the template row, opening `File a task from a template`.
  - Field inputs correctly populated labels and default values. Submitted with `Create task`.
  - Task was successfully created and enqueued at `/tasks/<id>` with the updated title.

### 2. Suites (`/suites`)
- **Search & Filtering:**
  - Filtered suites using search query `"Smoke"`. Found exactly 1 matching seeded suite.
- **Suite Creation with Nested Inline Template:**
  - Opened `+ New suite`.
  - Configured name `walk-suite-<timestamp>`.
  - Selected the created template `walk-tmpl-<timestamp>`.
  - Clicked `+ New template` inside the suite overlay, opening a nested template dialog.
  - Created inline template `walk-inline-<timestamp>` with title and description, and clicked `Add template`.
  - Verified return to the suite editor with the newly created inline template automatically selected.
  - Selected `Run mode`: "Run a fixed number of times" with `Number of times` set to 2.
  - Checked `Require approval before each pass's tasks run`.
  - Verified the live composition preview (`What each pass files`) dynamically rendered composed task cards.
  - Submitted `Add suite`. Suite row appeared in the list.
- **Suite Modification:**
  - Clicked suite row to edit. Switched mode to "Run until no repo changes (max passes)" and set `Max passes` to 3.
  - Saved successfully.

### 3. Schedules (`/schedules`)
- **Search:**
  - Verified schedule search filter responsiveness on `/schedules` (searching `"nightly"` matched 1 seeded row).
- **Schedule 1: Monthly Task Runner with Timezone & Overlap Policy:**
  - Opened `+ New schedule`.
  - Selected template `walk-tmpl-<timestamp>`.
  - Set cadence to `Monthly, on a day`, day `28`, time `22:30`.
  - Set timezone to `Pacific/Auckland`. Set overlap policy to "Start it anyway" (`allow`).
  - Saved. Schedule row rendered displaying recurrence `monthly on day 28 at 22:30 pacific/auckland`, `can overlap`, and calculated next run time.
- **Schedule Lifecycle Controls (Pause, Resume, Run Now):**
  - Reopened Schedule 1. Clicked `Pause`. Row reflected `Paused` status and `Held -- fires` notice.
  - Reopened Schedule 1 and clicked `Resume`. Row updated, clearing the paused badge.
  - Clicked `Run now`. Confirmed prompt dialog:
    `"Run \"Production Audit ...\" now? It starts a task from the template \"walk-tmpl-...\" straight away; the schedule's own next run is unchanged."`
    Task was immediately dispatched.
- **Schedule 2: Composed Suite Runner with Overlap Skip Policy:**
  - Created a second schedule. Switched `Fires` selector to `A suite`.
  - Selected suite `walk-suite-<timestamp>`, targeted repository `acme/widgets` and branch `main`.
  - Set cadence to `Weekly, on a day` (Monday at 05:00).
  - Set overlap policy to "Skip this run" (`skip`).
  - Submitted schedule. Row rendered showing suite association.
  - Clicked `Run now` with confirmation prompt, triggering an immediate suite run.

### 4. Suite Runs (`/suites` & `/suites/runs/:id`)
- **Launching Runs:**
  - Triggered a suite run from the suite's row `Run…` action on `Release Qualification Suite` targeting repository `acme/widgets` and branch `main`.
  - Started another suite run from the top-level `+ Run` generic header action.
- **Suite Runs List Inspection:**
  - Verified presence of suite runs with corresponding badges (`Active`, `Repo`, `Branch`).
  - Confirmed `ScheduledChip` on the scheduled run linking directly to its origin schedule `/schedules/sched-<id>`.
  - Followed `ScheduledChip` link directly into the schedule view and returned using browser back.
- **State Progression Verification:**
  - Advanced Run 1 (Success): Completed pass 1 tasks using `suitestub`, fired pass 2 (`fire-next-pass`), proposed a follow-up investigation task linked via `proposedBy` to the pass 1 task, and marked run as `succeeded`.
    Navigated to `/suites/runs/<id>`: dialog rendered `Succeeded` chip, `Pass 1` and `Pass 2` section headers, and the nested follow-up task row (`Follow-up Investigation from Pass 1`).
    Clicked the follow-up task row to navigate to its task detail page and returned using browser back.
  - Advanced Run 2 (Failure): Failed pass 1 task with error reason, marked run as `failed` with last error explanation.
    Navigated to `/suites/runs/<id>`: dialog rendered `Failed` chip and error message banner `pass 1 task ended in error (stub)`.
- **Missing Resource Handling:**
  - Navigated directly to `/suites/runs/999999`. Handled gracefully without errors or unhandled exceptions, maintaining page stability.

### 5. Repository-Declared Work (`docs/declarations.md`)
- **Declared Template:**
  - Located seeded template `nightly-maintenance` marked with chip `declared by acme/widgets`.
  - Opened editor. Confirmed declared notice:
    `"This template is declared by acme/widgets in .grain/templates/nightly.yml, so it cannot be edited or deleted here. Change that file in the repository and grain picks it up within a couple of minutes."`
    Confirmed `Save` and `Delete` buttons are disabled.
- **Declared Suite:**
  - Located declared suite `Smoke Verification Suite`. Confirmed declared notice pointing to `.grain/suites/smoke.yml` and disabled mutation controls.
- **Declared Schedule:**
  - Located declared schedule `Nightly Repo Maintenance Sweep`. Confirmed declared notice pointing to `.grain/schedules/nightly.yml` and disabled mutation controls.

### 6. Dependency-Aware Deletion Protection & Cleanup
- **Deletion Refusal:**
  - Attempted to delete template `walk-tmpl-<timestamp>` while attached to both Schedule 1 and Suite 1.
  - Accepted the browser `confirm()` deletion prompt.
  - Deletion was rejected with an error banner naming all dependents:
    `template is used by 1 schedule(s) ("Production Audit ...") and 1 suite(s) ("walk-suite-..."); repoint or delete the schedules and remove it from the suites first`.
- **Ordered Cleanup:**
  - Successfully deleted Schedule 1 and Schedule 2 after confirmation prompts.
  - Successfully deleted Suite after confirmation prompt.
  - Successfully deleted template `walk-tmpl-<timestamp>` cleanly without errors.
  - Successfully deleted inline template `walk-inline-<timestamp>` cleanly.
  - Verified all throwaway entities were completely removed from list views.

### 7. Keyboard Accessibility
- Verified focus traversal via `Tab` across list rows, opening item details with `Enter`, and dismissing overlays with `Escape`.

## Sort and Search Verification

| List | Filter / Query | Expected Result | Observed | Status |
| --- | --- | --- | --- | --- |
| `/templates` | Query: `"release"` | Shows only matching release templates | Exactly 1 matching template displayed | Pass |
| `/templates` | Query: `"nonexistent-..."` | Filters out all items | 0 rows displayed | Pass |
| `/templates` | Sort: `Name`, `Newest`, `Custom` | Rows rearrange according to sort criteria | Reorders correctly; no errors | Pass |
| `/suites` | Query: `"Smoke"` | Shows only matching smoke suite | Exactly 1 matching suite displayed | Pass |
| `/schedules` | Query: `"nightly"` | Filters schedules by matching text | Exactly 1 matching schedule displayed | Pass |

## Bugs and Findings

- **No New Bugs Discovered:** All tested interactions, dialog confirmations, input validations,
  and state updates functioned accurately according to specification.
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
| Playwright e2e (`recurring-composed-walkthrough.spec.js`) | 14 / 14 Pass | All 14 automated Playwright end-to-end scenarios passed (47.7s) |
| Playwright workflow suites (`templates_workflows`, `suites_workflows`, `schedules_workflows`) | 3 / 3 Pass | Dedicated workflow regression tests passed (11.3s) |
| End-to-end interactive browser walkthrough | Pass | Complete lifecycle verified against real server and real Chromium browser |
