# Recurring and Composed Work Automation UI Walkthrough — 2026-10-22

Task 179 (`grain/049f1e09/task-179`). This is an end-to-end walkthrough of
schedules, templates, suites, suite runs, and repository-declared rows, in a
real Chromium browser against a real `grain demo` server. It follows
[2026-10-15-recurring-composed-ui.md](2026-10-15-recurring-composed-ui.md).
Since then `candidate` has merged the unified `/processes` surface
(`design/design/settings-panel.design.md` §5, `2b832a8eb`) and the single-tenant reset
(`docs/single-tenant-reset.md`), and this pass covered those changes end to
end:

- the unified `/processes` table combining `Schedule`, `Workflow`, `Template`,
  `Suite`, and `Trigger` rows, with single- and multi-kind filter chips
  (`?kind=schedule,suite`), search across names/descriptions/repos/cadences/modes,
  all five sort modes (`Next run`, `Last run`, `Name (A–Z)`, `Kind`, and
  `Custom order` with drag handles and `localStorage` persistence), and
  derived `Last run` / `Next run` columns on templates and suites;
- legacy redirects from `/schedules`, `/templates`, `/suites`, and their item
  and run paths (`/schedules/:id`, `/templates/:id`, `/suites/:id`,
  `/suites/runs/:id`) into `/processes`;
- the deployment-scoped `ModelPicker` across templates and schedules, showing
  models served by enabled connections, models on disabled connections only
  (`(disabled connections only)`), and models no connection advertises
  (`(no connection advertises it)`).

It found one bug introduced when `SchedulesList`, `TemplatesList`, and
`SuitesList` were merged into `ProcessesList.jsx`, fixed on this branch with
unit and Playwright E2E regression tests:

1. **In `ProcessesList.jsx`, schedule `Run now` did not refresh `suiteRuns` or
   `tasks`, and saving an edited template or suite did not refresh `schedules`
   on the same `/processes` table.**

See [Bugs and fixes](#bugs-and-fixes).

## Environment

| | |
| --- | --- |
| Commit walked | `747f8245502f719f8fca60f125e9bc545128819e` (`candidate` tip, base of `grain/049f1e09/task-179`) |
| Go | `go1.26.9 linux/amd64` |
| Node / npm | `v20.19.0` / `10.8.2` |
| Browser | `@playwright/test` `1.62.1`, Chromium headless shell from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Server | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /tmp/grain ./cmd/grain`, then `/tmp/grain demo -state-dir <throwaway> -addr 127.0.0.1:18082 -github-sim -reconcile 500ms` |
| Auth / session | Signed in via `POST /api/auth/access-code` using the demo access code printed on startup (`ui/e2e/accesscode.js`) |
| Viewports / themes | Desktop `1440×900` and phone `390×844`, light and dark colour schemes, browser time zone UTC (plus schedule-level `America/New_York` recurrence tests) |
| Model / agents | None. `grain demo` runs no real agents. `/home/debian/.gemini-api-key` is not present in this sandbox (`test -f /home/debian/.gemini-api-key` exited 1). |

Every walkthrough script drove Playwright's Chromium through visible UI
controls (role/label locators, clicks, typing, keyboard navigation, and native
`window.confirm` dialogs) while recording:

- `page.on("pageerror")` and `page.on("console")` (errors and warnings);
- `page.on("requestfailed")` and every HTTP response `>= 400` with status and
  body;
- every `window.confirm` prompt text.

Zero uncaught page errors, zero unexpected console errors, and zero failed
network requests occurred across the entire walkthrough.

## Stubs

- **Agent execution boundary (`/tmp/seed-helper`, never committed).** Because
  `grain demo` does not dispatch real agents, a small Go helper opened the
  demo server's embedded SQLite store via `sqlite.Open(sqlite.DefaultConfig(stateDir))`
  to simulate agent task outcomes at the store boundary and invoke the real
  `orchestrator.SyncSuites` reconciler:
  - `complete-pass-changed <suiteRunId> <passNum>`: marks pass tasks completed
    with branch/PR activity (`Branch = "grain/pass-1"`, `PRNum = 101`), spawns
    a child follow-up task with a `proposed-by` link to the pass task, and
    calls `orchestrator.SyncSuites` so the real server advances an
    `until_clean` suite run from Pass 1 to Pass 2.
  - `complete-pass-clean <suiteRunId> <passNum>`: marks pass tasks completed
    with no branch/PR changes (`NothingNeededReason = "All checks clean"`) and
    calls `orchestrator.SyncSuites` so the real server transitions the suite
    run to `succeeded`.
  - `fail-pass <suiteRunId> <passNum>`: closes the pass task without
    completing and calls `orchestrator.SyncSuites` so the real server
    transitions the suite run to `failed` with `"1 task(s) did not complete"`.
  - `make-schedule-due <scheduleId>`: backdates `NextRunAt` in the SQLite
    store by 10 minutes while paused so the real UI renders the overdue
    `Not firing` warning chip and `.schedule-overdue` banner when resumed.

  All creation, editing, launching, pausing, resuming, deleting, and rendering
  passed through the real browser UI and the real `grain demo` HTTP server.
- **Simulated GitHub (`-github-sim`).** Started `grain demo` with `-github-sim`
  so `GET /api/repos/acme/widgets/github-branches` served simulated branch
  names without external GitHub network access.

## Steps and outcomes

### 1. Unified `/processes` list, kind filters, search, sort, custom order, and legacy redirects

1. **Initial state (`GET /processes`)**:
   - The table rendered all 8 seeded processes across kinds: `All 8`,
     `Schedules 3`, `Workflow schedules 1`, `Templates 2`, `Suites 1`,
     `Triggers 1`.
   - Each row displayed its kind chip (`Schedule`, `Workflow`, `Template`,
     `Suite`, `Trigger`), name link, secondary chips (cadence, target
     template/suite, suite mode, or `Declared by acme/widgets`), repository
     and branch, derived or direct `Last run` and `Next run` timestamps, and
     row action (`Enabled`/`Paused` chip, `Use…` button, or `Run…` button).
2. **Single- and multi-kind filter chips**:
   - Clicking `Schedules 3` updated the URL to `/processes?kind=schedule`,
     narrowed the table to 3 schedule rows, and changed the primary create
     button from `+ New` to `+ New schedule`.
   - Clicking `Suites 1` while `Schedules 3` was active selected both kinds
     (`/processes?kind=schedule,suite`, 4 rows) and restored the `+ New`
     menu button.
   - Clicking `Triggers 1` alone (`/processes?kind=trigger`, 1 row with
     `Next run` showing `on change`) kept `+ New` as a dropdown menu offering
     only creatable kinds (`Schedule`, `Template`, `Suite`) with the hint
     `Workflow schedules and triggers are declared in a repository's .grain/ directory.`
   - Clicking `All 8` cleared `?kind=` and restored all 8 rows.
3. **Search and empty filter state**:
   - Typing `"Vulnerability"` into `Search processes…` narrowed the list to 1
     row (`Weekly Dependency & Vulnerability Audit`).
   - Searching by cadence (`"mondays"`), branch (`"release"`), or suite mode
     matched the corresponding rows.
   - Typing `"nonexistent-xyz-999"` rendered the empty state
     `No processes match the current filter.`
4. **Sort modes (`Next run`, `Last run`, `Name (A–Z)`, `Kind`, `Custom order`)**:
   - Cycled through all 5 sort options via the `Sort` combobox, verifying the
     `?sort=` query parameter (`last`, `name`, `kind`, `custom`) and row
     ordering.
   - In `Custom order`, verified every row rendered an interactive
     `.task-drag-handle` button (`aria-label="Move <name>"`), picked up the
     first row with `Space`/`click` (announcing `Moving 1 process` in the live
     status banner), and confirmed that filtering the picked row out via
     search automatically cancelled the pickup.
5. **Legacy route redirects**:
   - Navigating to `/schedules`, `/templates`, and `/suites` redirected to
     `/processes?kind=schedule`, `/processes?kind=template`, and
     `/processes?kind=suite`.
   - Navigating to `/schedules/:id`, `/templates/:id`, `/suites/:id`, and
     `/suites/runs/:id` redirected to `/processes/schedules/:id`,
     `/processes/templates/:id`, `/processes/suites/:id`, and
     `/processes/suites/runs/:id` and opened the corresponding overlay.
   - Navigating to `/processes/schedules/nonexistent-id`,
     `/processes/templates/nonexistent-id`, `/processes/suites/nonexistent-id`,
     and `/processes/suites/runs/nonexistent-id` showed the error toast
     (`no schedule "nonexistent-id"`, etc.) and fell back cleanly to
     `/processes`.

### 2. Templates: creation, model picker states, `Use…`, editing, and dependency-aware delete

1. **Create a fully populated template (`e2e-audit-template`)**:
   - Clicked `+ New` → `Template` to open `New template`.
   - Verified client-side validation: before filling `Template name` and
     `Task title`, `Add template` remained `disabled`.
   - Populated:
     - `Template name`: `e2e-audit-template`
     - `Task title`: `[{{env}}] Audit {{service}} security`
     - `Task description`: `Perform full security audit of {{service}} in {{env}}.`
     - Two template inputs via `+ Add input`:
       - `env` (label `Target Environment`, default `staging`, options
         `staging, prod`, `Required` checked)
       - `service` (label `Service Name`, default `auth-api`)
     - `Repository`: `acme/widgets`, `Base branch`: `main`
     - Extra writable repository via `+ Add repo`: `acme/docs` on branch
       `release`
     - Read-only repository: `acme/infra`
     - `Auto-merge`: checked
     - `Network`: `Open (full internet)`
     - `If nothing is needed`: `Leave for acknowledgement`
     - `Capabilities`: `Self-debug` and `Docker`
     - Custom fields: `Priority` = `high`, `Area` = `security, backend`,
       `Requested by` = `sec-team@acme.test`, `Target date` = `2026-11-15`,
       `Estimate (days)` = `3`, `Customer-facing` = `Yes`.
2. **Agent & model picker (`ModelPicker`) with enabled, disabled, and unserved models**:
   - Seeded two connections on `/api/connections`: an enabled connection
     advertising `claude-sonnet-4-6` and `gemini-2.5-pro`, and a disabled
     connection advertising `disabled-conn-model-1`.
   - Opened the `Model` autocomplete in `TemplateOverlay`:
     - Enabled connection models (`claude-sonnet-4-6`, `gemini-2.5-pro`) were
       listed as active options.
     - `disabled-conn-model-1` was absent from the active suggestion list.
   - Saved with `claude-sonnet-4-6`, then tested how the picker renders when a
     template carries `disabled-conn-model-1` or `unserved-phantom-model-99`:
     - With `disabled-conn-model-1`, the field rendered the warning helper
       text `disabled-conn-model-1 (disabled connections only)` and annotated
       the option in the dropdown.
     - With `unserved-phantom-model-99`, the field rendered the warning helper
       text `unserved-phantom-model-99 (no connection advertises it)` and
       annotated the option in the dropdown.
     - Restored the template's model to `claude-sonnet-4-6` through the UI and
       saved.
3. **Instantiate a task via `Use…` (`UseTemplateOverlay`)**:
   - Clicked `Use…` on the `e2e-audit-template` row.
   - Verified `UseTemplateOverlay` rendered a dropdown for `Target Environment`
     (with options `staging` and `prod`), a text input for `Service Name`
     (defaulted to `auth-api`), and the live `CompositionPanel` preview showing
     resolved title `[staging] Audit auth-api security`, `acme/widgets @ main`,
     extra write `acme/docs @ release`, read `acme/infra`, model
     `claude-sonnet-4-6 (from the template)`, capabilities `docker, self-debug`,
     network `open (full internet)`, and `Wait for someone to acknowledge`.
   - Selected `prod`, typed `billing-worker`, and clicked `Create task`.
   - Verified navigation to `/tasks/<id>` with title
     `[prod] Audit billing-worker security`, description
     `Perform full security audit of billing-worker in prod.`, and all
     template settings applied.
4. **Edit and reopen**:
   - Opened `/processes/templates/<id>`, updated the description, saved,
     reloaded `/processes`, and reopened the template to confirm persistence.
5. **Dependency-aware delete refusal**:
   - While `e2e-audit-template` was referenced by both a schedule and a suite,
     clicking `Delete` in `TemplateOverlay` and accepting the confirmation
     dialog returned `409 Conflict` and displayed the inline error naming both
     dependents (`template is used by 1 schedule(s) (...) and 1 suite(s) (...); repoint or delete the schedules and remove it from the suites first`)
     while keeping the template intact.

### 3. Suites: creation, inline template creation, run modes, composition preview, edit, and delete

1. **Create an `until_clean` suite with inline template creation (`E2E Full Qualification Suite`)**:
   - Clicked `+ New` → `Suite` to open `New suite`.
   - Filled `Name`: `E2E Full Qualification Suite`, `Description`:
     `Runs security audit and inline smoke check until clean`.
   - Selected `e2e-audit-template` in the `Templates` multi-select and filled
     its per-suite input overrides (`env` = `prod`, `service` = `edge-proxy`).
   - Clicked `+ New template` inside `SuiteOverlay` to open the nested inline
     `New template` dialog, created `e2e-inline-smoke-tmpl` (`Smoke check {{component}}`,
     repo `acme/widgets`, base `main`, input `component` defaulting to `router`),
     and confirmed it was automatically appended to the suite's selected
     templates list.
   - Verified `Run mode`: `Run until a pass makes no changes` (`until_clean`),
     set `Max passes` to `4`, checked `Require human approval for tasks`, and
     checked `Auto-merge task changes`.
   - Verified the live `CompositionPanel` preview displayed `Up to 4 passes of these tasks (stops after a pass makes no changes)`,
     both pass tasks with resolved titles, and `Tasks wait for human approval`.
   - Clicked `Add suite` and confirmed persistence on `/processes` and after
     reload.
2. **Create, edit, and delete a `count`-mode throwaway suite (`E2E Count Mode Throwaway Suite`)**:
   - Created a second suite with `Run mode` set to
     `Run a fixed number of times` (`count`) and `Number of times` = `2`
     (verifying the spinbutton defaulted to `1` on mode switch and previewed
     `2 passes of these tasks`).
   - Reopened the suite from `/processes`, edited `Number of times` to `3` and
     unchecked `Auto-merge task changes`, clicked `Save`, and confirmed the
     row chip updated from `2 passes` to `3 passes`.
   - Reopened the throwaway suite, clicked `Delete`, verified the confirmation
     dialog named `"E2E Count Mode Throwaway Suite"`, accepted, and verified
     removal from `/processes`.

### 4. Schedules: task-filing, template-backed, and suite-running schedules, recurrence variants, overlap, pause/resume, `Run now`, and overdue notice

1. **Raw task-filing schedule (`E2E Raw Task Schedule`)**:
   - Created a schedule with `Fires` = `A task`, `Template` = `None (write task inline)`,
     repo `acme/widgets`, base `candidate`, extra write `acme/docs @ main`,
     read `acme/infra`, recurrence `Every N hours` (`4` hours), overlap policy
     `Allow (start a new run anyway)`, model `gemini-2.5-pro`, `Auto-merge`,
     capability `Self-debug`, network `Egress (outbound internet only)`, and
     custom field `Priority` = `medium`.
   - Verified the row appeared on `/processes` with `every 4h` and
     `acme/widgets @ candidate`.
2. **Template-backed schedule with model override and non-UTC timezone**:
   - Created a schedule firing `e2e-audit-template` with input overrides
     (`env` = `staging`, `service` = `payment-gateway`), schedule-level model
     override `gemini-2.5-pro` (verifying `CompositionPanel` displayed
     `gemini-2.5-pro (from the schedule, overriding claude-sonnet-4-6 from the template)`),
     recurrence `Weekly` on `Wednesday` at `14:30`, and timezone
     `America/New_York`.
   - Verified the row chip displayed `wednesdays at 14:30 (America/New_York)`
     and `Template: e2e-audit-template`.
3. **Suite-running schedule and recurrence variants (`daily`, `monthly`)**:
   - Created a schedule with `Fires` = `A suite`, `Suite` =
     `E2E Full Qualification Suite`, repo `acme/widgets`, base `main`,
     recurrence `Monthly` on day `15` at `03:15` (`day 15 at 03:15`), then
     edited it to `Daily` at `01:30` (`daily at 01:30`) and verified persistence.
4. **Pause, resume, and overdue (`Not firing`) presentation**:
   - Clicked `Pause` inside `ScheduleOverlay`: the schedule row updated to show
     the `Paused` chip and `—` for `Next run`.
   - Clicked `Resume`: the schedule row returned to `Enabled` with its
     computed `Next run`.
   - Using `/tmp/seed-helper make-schedule-due` to backdate `NextRunAt` by 10
     minutes, confirmed the schedule row rendered the warning chip
     `Not firing` and opening `ScheduleOverlay` displayed the `.schedule-overdue`
     alert explaining that the schedule's next run time has passed without the
     reconciler firing it.
5. **`Run now` (task-filing, suite-running, and busy `409 Conflict`)**:
   - On the raw task schedule, clicked `Run now`, verified the confirmation
     dialog `Run "E2E Raw Task Schedule" now?`, accepted, and verified
     `lastRunAt` updated and a new task was filed.
   - On the suite-running schedule, clicked `Run now`: verified a new suite run
     appeared immediately in `Suite runs` on `/processes` with the `Scheduled`
     chip (`ScheduledChip`) linking back to `/processes/schedules/<id>`, and
     the suite row's derived `Last run` updated in lockstep with the schedule
     row.
   - While that suite run was still `running` and the schedule's overlap policy
     was `Skip if previous run is still active`, clicking `Run now` again
     returned `409 Conflict` and surfaced the error toast explaining the
     previous run is still active.
6. **Delete throwaway schedule**:
   - Created and deleted a throwaway schedule via `Delete` in `ScheduleOverlay`,
     verifying the confirmation dialog and persistence after reload.

### 5. Suite runs: row `Run…`, generic `+ Run`, multi-pass progression, follow-up tasks, failure state, and provenance links

1. **Launch from suite row `Run…` and generic `+ Run` button**:
   - Clicked `Run…` on the `E2E Full Qualification Suite` row: verified
     `SuiteRunOverlay` preselected `E2E Full Qualification Suite`, listed
     which templates inherit the target branch vs. keep their own bound
     branch, and started a run on `acme/widgets @ main`.
   - Clicked the top-level `+ Run` button in the `/processes` toolbar: verified
     `SuiteRunOverlay` opened with the first suite preselected and allowed
     switching suites before launching.
2. **Multi-pass `until_clean` progression, proposed follow-up nesting, and `Succeeded` state**:
   - Opened the running suite run at `/processes/suites/runs/<id>` (`SuiteRunPane`):
     verified the `Running` status chip, `Pass 1` heading, and 2 pass-1 task
     rows with accessible status badges (`role="img"` with non-empty
     `aria-label`).
   - Executed `/tmp/seed-helper complete-pass-changed <runId> 1`:
     - Reloaded `/processes/suites/runs/<id>` and verified Pass 1 showed the
       child follow-up task nested under its parent task inside
       `.task-sublist`, while the reconciler automatically created `Pass 2`
       with 2 new pass-2 tasks.
   - Executed `/tmp/seed-helper complete-pass-clean <runId> 2`:
     - Reloaded `/processes/suites/runs/<id>` and verified the suite run
       transitioned to the `Succeeded` chip with both `Pass 1` and `Pass 2`
       completed.
3. **Failure presentation (`Failed` chip and error summary)**:
   - Executed `/tmp/seed-helper fail-pass <run2Id> 1` on the second suite run:
     - Verified the `.suite-run-row` on `/processes` and the `SuiteRunPane`
       header displayed the red `Failed` status chip alongside the failure
       reason `1 task(s) did not complete`.
4. **Task-detail navigation and provenance link back to the suite run**:
   - Clicked a pass task inside `SuiteRunPane` to navigate to `/tasks/<id>`.
   - Expanded the provenance toggle (`.freshness .provenance-toggle`) and
     verified it displayed `pass 1 of E2E Full Qualification Suite run <runId>`
     with a clickable link back to `/processes/suites/runs/<runId>`.

### 6. Repository-declared templates, suites, and schedules (`docs/declarations.md`)

1. **List-level chips and tooltips**:
   - Verified all three seeded repository-declared rows on `/processes` —
     template `nightly-maintenance` (`.grain/templates/nightly.yml`), suite
     `Smoke Verification Suite` (`.grain/suites/smoke.yml`), and workflow
     schedule `nightly` (`.grain/workflows/nightly.yml`) — rendered the
     `Declared by acme/widgets` chip with tooltip
     `Declared in acme/widgets, <path>` and an external link icon opening the
     declaration in a new tab (`target="_blank"`).
2. **Read-only editor enforcement (`DeclaredNotice` and `DeclaredFields`)**:
   - Opened each declared row (`/processes/templates/repo:template:acme:widgets:nightly`,
     `/processes/suites/repo:suite:acme:widgets:smoke`,
     `/processes/schedules/repo:schedule:acme:widgets:nightly` and
     `repo:schedule:acme:widgets:0`):
     - Each overlay displayed the `.declared-notice` banner naming the
       repository (`acme/widgets`) and file path (`.grain/templates/nightly.yml`,
       `.grain/suites/smoke.yml`, `.grain/workflows/nightly.yml`) and
       explaining that edits must be committed to that file rather than saved
       in the UI.
     - All form inputs inside `DeclaredFields` were disabled.
     - `Save`, `Delete`, and `Pause` buttons were disabled up front; `Close`
       (and `Run now` on declared schedules) remained enabled.
   - Confirmed at the API boundary that direct `PATCH` and `DELETE` requests
     against `/api/templates/repo:...`, `/api/suites/repo:...`, and
     `/api/schedules/repo:...` were rejected with `400 Bad Request`
     (`declared in acme/widgets (.grain/...); edit the file in that repository instead`).

## Bugs and fixes

### 1. `ProcessesList.jsx`: schedule `Run now` left `suiteRuns` and `tasks` stale, and editing a template or suite left adjacent schedule rows stale on `/processes`

- **Reproduction**:
  1. Open `/processes` (where schedules, templates, suites, and `Suite runs`
     are rendered together on one screen).
  2. Click a template row (e.g. `release-qualification`) that a schedule fires,
     change `Task title` to `Release Qualification v2`, and click `Save`.
  3. Observe on `/processes` that the template row shows the new title, while
     the schedule row directly above it still shows the old title until the
     page is manually reloaded.
  4. Similarly, edit a suite's `Name` and click `Save`: the suite row updates,
     while any schedule row firing that suite still shows `Suite: <old name>`
     and the old schedule title.
  5. Open a suite-running schedule on `/processes` and click `Run now`:
     `ScheduleOverlay` closes back to `/processes` and updates the schedule
     row's `Last run` timestamp, but `Suite runs` at the bottom of `/processes`
     still shows `No suite runs yet.` (and the suite row's derived `Last run`
     column stays `—`) until a manual reload.
- **Root cause**:
  When `SchedulesList`, `TemplatesList`, and `SuitesList` were consolidated
  into `ui/src/components/ProcessesList.jsx`, the overlay `onSaved` callbacks
  at lines 449, 460, and 472 kept their single-resource refresh handlers:
  - `editingSchedule` passed `onSaved={onRefreshSchedules}` (whereas
    `SuiteRunOverlay` at line 508 passed
    `onStarted={() => Promise.all([onRefreshRuns(), onRefreshTasks()])}`).
  - `editingTemplate` passed `onSaved={onRefreshTemplates}` without
    `onRefreshSchedules`, even though `/api/schedules` derives a
    template-backed schedule's `title` and `templateName` from the template.
  - `editingSuite` passed `onSaved={onRefreshSuites}` without
    `onRefreshSchedules`, even though `/api/schedules` derives a suite-backed
    schedule's `title` and `suiteName` from the suite.
- **Fix**:
  Updated `ui/src/components/ProcessesList.jsx` so:
  - `editingSchedule`'s `onSaved` calls
    `Promise.all([onRefreshSchedules?.(), onRefreshRuns?.(), onRefreshTasks?.()])`;
  - `editingTemplate`'s `onSaved` calls
    `Promise.all([onRefreshTemplates?.(), onRefreshSchedules?.()])`;
  - `editingSuite`'s `onSaved` calls
    `Promise.all([onRefreshSuites?.(), onRefreshSchedules?.()])`.
- **Regression coverage**:
  - Unit test in `ui/src/components/ProcessesList.test.jsx`:
    `refreshes runs and tasks on schedule Run now, and refreshes schedules when a template or suite is saved`.
  - Playwright E2E tests in `ui/e2e/recurring-composed-walkthrough.spec.js`
    (tests 13 and 15): verified that clicking `Run now` on a suite schedule
    immediately renders the new `.suite-run-row` on `/processes` without
    navigating or reloading, and saving a template or suite edit immediately
    updates the schedule row on `/processes` without `page.reload()`.
