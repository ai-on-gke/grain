# Recurring and Composed-Work Automation UI Walkthrough (2026-10-26)

Task 1147 (`grain/049f1e09/task-1147`). End-to-end browser walkthrough of
schedules, templates, suites, suite runs, and repository-declared rows on
`[/processes](/processes)` against a real `grain demo` server in a headless
Chromium browser.

This walkthrough uncovered two defects, both fixed on this branch with unit,
component, and Playwright regression tests:

1. **Interactive template composition preview in `Use…` (`UseTemplateOverlay`
   and `Store.TemplateComposition`)**: opening `Use…` on an interactive
   template (`Interactive (agent can chat with you)` checked) hides the
   `Approved` checkbox and explains that the live chat opens as soon as the
   task is filed. Filing from `Use…` always approves the interactive task
   (`pkg/ui/client.go`). However, when `approvedByDefault` was `false` (or the
   `Approved` box had been unticked), `UseTemplateOverlay.jsx` queried
   `/api/templates/:id/composition?…&approved=false`, and
   `Store.TemplateComposition` in `pkg/model/compose.go` returned
   `Approval: waits for approval — chosen here` right below the notice saying
   the chat opens immediately. Even when `approved` was `true`, the preview
   credited `Approval` to `chosen here` (`LayerFiling`) rather than
   `from the template` (`LayerTemplate`).
2. **`[/processes](/processes)` `NarrowingBar` vs `GET /api/schedules` grammar
   and sort parity (`pkg/schedulefilter/filter.go`)**:
   `pkg/schedulefilter/filter.go` documents that any query string written by
   the `[/processes](/processes)` toolbar (`ui/src/processFilters.js`) can be
   pasted onto `GET /api/schedules` and asks the daemon the same question with
   the same attributes and sort orders. Three mismatches were found:
   - Narrowing or sorting by `Requires approval` (`f=requireApproval:any:yes`,
     `f=requireApproval:empty:`, or `sort=requireApproval:asc`) and pasting the
     query onto `GET /api/schedules` returned **HTTP 400**
     (`schedules have no attribute "requireApproval"`), because
     `requireApproval` was the only attribute out of the 20 in
     `PROCESS_ATTRIBUTES` omitted from `attributes` in
     `pkg/schedulefilter/filter.go`.
   - `sort=cadence:asc` on `[/processes](/processes)` orders schedules from
     shortest to longest cadence (`CADENCE_ORDER = ["everyNMinutes",
     "everyNHours", "daily", "weekly", "monthly"]`), whereas
     `GET /api/schedules?sort=cadence:asc` sorted cadence names alphabetically
     (`daily` before `everyNHours` before `weekly`).
   - `sort=overlap:asc` on `[/processes](/processes)` orders `"skip"` before
     `"allow"`, whereas `GET /api/schedules?sort=overlap:asc` sorted `"allow"`
     before `"skip"`.

See [Bugs found and fixed](#bugs-found-and-fixed) for full details.

## Environment

| Item | Value |
| --- | --- |
| Commit walked | `39d69aceebd3b98b65440f5b5d0e45adc92f7926` (`main` base of `grain/049f1e09/task-1147`) |
| Go / Node / npm | `go1.26.9 linux/amd64` / `v20.19.0` / `10.8.2`, Linux |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) |
| Build | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/grain ./cmd/grain` |
| Server | `TMPDIR=<throwaway> /var/tmp/grain demo -addr 127.0.0.1:0 -open=false -github-sim -reconcile 500ms -schedules -access-code-file <file>` |
| Sign-in | `POST /api/auth/access-code` with the demo's access code (`ui/e2e/accesscode.js`) |
| Viewports & themes | `1440×900` desktop and `390×844` mobile in both `light` and `dark` themes; UTC timezone |
| Gemini API key | `/home/debian/.gemini-api-key` is not present in this sandbox (`test -f` exits 1). Demo connection "Gemini 2.5 Pro" (`antigravity`) advertises `gemini-2.5-pro` and `gemini-2.5-flash`. |

## Stubs

- **Agent execution boundary (`walkseed`, throwaway Go helper in `/var/tmp`, not committed).**
  `grain demo` does not dispatch agents, so to exercise multi-pass `until_clean`
  convergence and `failed` suite-run states, the throwaway helper opened the
  demo's SQLite store (`sqlite.Open(sqlite.DefaultConfig(stateDir))`,
  `model.New`) and recorded only the observations and links an agent run leaves
  behind:
  - `complete-changed <taskID> <pr>`: adds `model.Link{Kind: model.LinkFixes, Target: "acme/widgets#<pr>"}` and sets `Observation.CompletedAt`;
  - `complete-clean <taskID>`: sets `Observation.CompletedAt` with no PR link;
  - `close <taskID>`: sets `Observation.ClosedAt` without `CompletedAt`;
  - `proposed-by <taskID> <parentID>`: adds `model.Link{Kind: model.LinkProposedBy, Target: parentID}` on a task created via `POST /api/tasks`.

  All suite-run pass transitions (`Pass 1` → `Pass 2` → `Succeeded`, and
  `Pass 1` → `Failed`) were driven by the real `grain demo` suite reconciler
  (`-reconcile 500ms` → `orchestrator.SyncSuites`).
- **`/api/config` route override for `approvedByDefault: false`.**
  Playwright `page.route("**/api/config", ...)` was used to simulate a
  deployment with `approvedByDefault: false` when reproducing and verifying
  Bug #1 in `UseTemplateOverlay`.
- **Simulated GitHub.** `-github-sim` served the simulated `acme/widgets`
  repository (`main` and `release/1.0`).

## Walkthrough Steps and Outcomes

### 1. Unified Processes list (`[/processes](/processes)`), navigation, search, sort, and narrowing

1. **Legacy route redirects and kind chips.**
   - Navigating to `[/schedules](/schedules)`, `[/templates](/templates)`, and
     `[/suites](/suites)` redirected cleanly to `[/processes?kind=schedule](/processes?kind=schedule)`,
     `[/processes?kind=template](/processes?kind=template)`, and
     `[/processes?kind=suite](/processes?kind=suite)`.
   - The `process-kind-filter` bar displayed counts for `All`, `Schedules`,
     `Workflow schedules`, `Templates`, `Suites`, and `Path triggers`.
2. **Search and empty state.**
   - Typing `release` in `Search processes…` filtered the list to matching rows;
     typing `zzzz-nonexistent-12345` showed the empty state with a working
     `Clear the search box` action.
3. **Narrowing bar and `GET /api/schedules` parity.**
   - Exercised conditions across `Source`, `Type`, `Name`, `Enabled`, `Cadence`,
     `Overlap`, and `Requires approval`, plus sorting by `Next run`, `Name`,
     `Kind`, `Cadence`, and `Requires approval`.
   - Discovered **Bug #2** when pasting `?f=requireApproval:any:yes`,
     `?f=requireApproval:empty:`, and `?sort=cadence:asc` onto
     `GET /api/schedules` (see [Bugs found and fixed](#bugs-found-and-fixed)).

### 2. Templates (`[/processes?kind=template](/processes?kind=template)`)

1. **Create a fully populated template (`Walkthrough Full Template`).**
   - Opened `+ New template` and populated:
     - Name (`Walkthrough Full Template`), Task title (`Audit {{service}} on {{env}} (dryRun={{dry_run}})`), and Task description referencing `{{service}}`, `{{env}}`, and `{{dry_run}}`.
     - Three input fields via `+ Add input`: `service` (required, no default), `env` (required, default `staging`), and `dry_run` (optional, default `false`).
     - Bound target repository `acme/widgets` and base branch `main`, plus an additional write target (`acme/docs` on `main`) and read repository (`acme/shared`).
     - Grain API grants (`tasks:read` preset via `ApiGrantPicker`) and capability method grant (`gcp-key` via `CapabilityMethodGrants`), plus `self-debug`.
     - Network access `Egress (outbound internet only)` (`open`), Nothing-needed action `Leave for acknowledgement` (`ack`), custom field `Priority = high`, `Auto-merge`, and `Interactive (agent can chat with you)`.
   - Verified `ModelPicker`: listed `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, and `gemini-2.5-flash`. Verified that disabling the provider connection under `[/settings/models](/settings/models)` marks both `Deployment default (gemini-2.5-pro, disabled connections only)` and `gemini-2.5-pro (disabled connections only)` with the disabled-connection caveat, and that a template seeded with an unadvertised model (`claude-opus-legacy`) displays `(no connection advertises this model)`.
2. **Edit, reload, and deep-link.**
   - Reopened the template row (`[/processes/templates/template-3](/processes/templates/template-3)`), edited the description, saved, reloaded, and confirmed all fields persisted.
3. **Instantiate via `Use…` (`UseTemplateOverlay`).**
   - Clicked `Use…` on the template row. Confirmed that the bound repository notice (`Files against acme/widgets -- this template is bound to it.`) and live-chat notice (`This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed.`) rendered, and that required input validation held `Create task` until `service` was filled (`payments`).
   - Discovered **Bug #1** in the "What this files" preview for interactive templates when `approvedByDefault` is `false` (see [Bugs found and fixed](#bugs-found-and-fixed)).
   - Submitted `Create task` and confirmed navigation straight to the newly filed interactive task (`[Task #13](/tasks/13)`).

### 3. Schedules (`[/processes?kind=schedule](/processes?kind=schedule)`)

1. **Recurrence variants and 10-minute floor.**
   - Exercised all 5 recurrence kinds in `ScheduleOverlay`:
     - `Every N minutes` (`everyNMinutes`): verified client/server floor validation rejecting `< 10` minutes and saving `20` minutes (`every 20m`).
     - `Every N hours` (`everyNHours`): verified minimum `1` and saved `6` hours (`every 6h`).
     - `Daily, at a time` (`daily`): verified time-of-day and timezone selection (`America/New_York`, `Europe/London`), rendering `daily at 09:00 America/New_York`.
     - `Weekly, on a day and time` (`weekly`): verified weekday + time + timezone (`Fridays at 09:15 Europe/London`).
     - `Monthly, on a day of the month` (`monthly`): verified day `1..31` + time.
2. **Task-filing (template-backed and raw) and suite-running schedules.**
   - Created a template-backed task schedule, a raw task schedule with custom fields/capabilities/network access, and a suite-running schedule (`Fires: A suite`) with required base branch `main`.
   - Confirmed live `CompositionPreview` layer attribution (`from the template`, `from the schedule`, `from the suite`, `from the repo`).
3. **Edit, Pause, Resume, Run now, and Delete.**
   - Edited schedule recurrence and overlap policy (`skip` ↔ `allow`) and confirmed persistence across reload.
   - Tested `Pause` (row shows `Paused` chip and `Held -- fires … if resumed by then` instead of promising a next run) and `Resume` (`Enabled`).
   - Tested `Run now`: verified confirmation dialog text, verified dismissing does not fire, and verified accepting files the task or starts the suite run immediately while leaving `Next run` unchanged and populating `Last run`.

### 4. Suites and Suite Runs (`[/processes?kind=suite](/processes?kind=suite)`)

1. **Suite creation, inline template creation, and unattended template validation.**
   - Confirmed that adding a template with a required input that has no default (`Walkthrough Full Template`) to a suite is refused on save by `model.ValidateUnattendedTemplate`.
   - Created `Walkthrough End-to-End Suite` with `Run mode: Run until no repo changes or new tasks` (`until_clean`), `Max passes: 3`, `Auto-merge`, `release-qualification`, and a second template `inline-suite-lint` created inline via `+ New template`.
   - Edited the suite to `Run a fixed number of times` (`count: 2`), reloaded to verify `run 2×`, and switched back to `until_clean` (`max 3`) for run execution.
2. **Suite Run #1 (from row `Run…` → multi-pass `until_clean` → `Succeeded`).**
   - Started Run #1 (`[/processes/suites/runs/1](/processes/suites/runs/1)`) from the suite row's `Run…` button targeting `acme/widgets` on `main`.
   - Pass 1 filed `[Task #13](/tasks/13)` and `[Task #14](/tasks/14)`. Filed follow-up `[Task #15](/tasks/15)` (`Follow-up: fix lint warning in pkg/server`) linked via `walkseed proposed-by 15 13`, completed `[Task #13](/tasks/13)` with a PR (`complete-changed 13 101`), and completed `[Task #14](/tasks/14)` cleanly (`complete-clean 14`).
   - Within one reconcile tick (`500ms`), `orchestrator.SyncSuites` advanced the run to `Pass 2` and filed `[Task #16](/tasks/16)` and `[Task #17](/tasks/17)`.
   - Completed both Pass 2 tasks cleanly (`complete-clean 16`, `complete-clean 17`); the reconciler transitioned Run #1 to `Succeeded`.
   - Verified `[/suites/runs/1](/suites/runs/1)` redirected to `[/processes/suites/runs/1](/processes/suites/runs/1)`, rendered `Succeeded`, grouped `Pass 1` and `Pass 2`, nested the proposed follow-up under its parent pass task, and navigated cleanly to task detail (`[Task #13](/tasks/13)`) where `.freshness .provenance-toggle` linked back to `Walkthrough End-to-End Suite run 1`.
3. **Suite Run #2 (from generic `+ Run` → `Failed`).**
   - Started Run #2 (`[/processes/suites/runs/2](/processes/suites/runs/2)`) from the top-level `+ Run` button on `[/processes](/processes)`.
   - Closed the first task of Pass 1 without completing (`walkseed close`) and completed the second cleanly (`walkseed complete-clean`).
   - Within one reconcile tick, Run #2 transitioned to `Failed` and displayed `pass 1 had a task that failed, or closed without completing`.

### 5. Repository-declared templates, suites, and schedules (`docs/declarations.md`)

1. **List presentation.**
   - Confirmed that `nightly-maintenance` (`[/processes/templates/repo:template:acme:widgets:nightly](/processes/templates/repo:template:acme:widgets:nightly)`), `Smoke Verification Suite` (`[/processes/suites/repo:suite:acme:widgets:smoke](/processes/suites/repo:suite:acme:widgets:smoke)`), and `Nightly Repo Maintenance Sweep` (`[/processes/schedules/repo:schedule:acme:widgets:nightly](/processes/schedules/repo:schedule:acme:widgets:nightly)`) each render the `.declared-chip` badge `Declared by acme/widgets` with tooltip `Declared in acme/widgets, .grain/<kind>/<file>.yml`.
2. **Read-only editors and API refusal.**
   - Opening each declared row displayed `.declared-notice` naming the repository and file path, disabled all form inputs and comboboxes (`aria-disabled="true"`), and disabled `Save`, `Delete`, `Pause`, and `+ New template` up front without sending `PATCH` or `DELETE` requests.
   - Direct `DELETE /api/templates/repo:template:acme:widgets:nightly` returned HTTP 400 (`is declared by acme/widgets in .grain/templates/nightly.yml`).
   - Confirmed that operational actions (`Use…` on a declared template, `Run…` on a declared suite, and `Run now` on a declared schedule) remain enabled and work as expected.
3. **Dependency-aware delete and cleanup.**
   - Verified that deleting a template used by a schedule and a suite is refused with HTTP 400 naming both the schedule and the suite (`template is used by 1 schedule(s) (…) and 1 suite(s) (…); repoint or delete the schedules and remove it from the suites first`).
   - Deleted throwaway schedules, suites, and templates in dependency order and verified their removal after reload.

## Bugs Found and Fixed

### 1. Interactive template composition preview in `Use…` showed `Approval: waits for approval — chosen here` when `approvedByDefault` was `false`

- **Reproduction:**
  1. Create an interactive template (`Interactive (agent can chat with you)` checked) on `[/processes?kind=template](/processes?kind=template)`.
  2. On a deployment where `approvedByDefault` is `false` (or after unticking `Approved`), click `Use…` on the interactive template row.
  3. `UseTemplateOverlay` hides the `Approved` checkbox and displays *"This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed."*
  4. Immediately below that notice, the "What this files" preview displayed `Approval: waits for approval — chosen here`, even though clicking `Create task` always approves an interactive task filed by a user (`pkg/ui/client.go`). Even when `approvedByDefault` was `true`, `Store.TemplateComposition` credited `Approval` to `LayerFiling` (`"chosen here"`) instead of `LayerTemplate` (`"from the template"`).
- **Root cause:**
  - `UseTemplateOverlay.jsx` initialized `approved` from `approvedByDefault` and passed `["approved", approved ? "true" : "false"]` to `/api/templates/:id/composition` even when `template.interactive` was `true` (where the `Approved` checkbox is not rendered).
  - `Store.TemplateComposition` in `pkg/model/compose.go` passed `approved` directly to `ComposeTemplateTask` instead of `approved || tmpl.Interactive` and left `comp.Sources.Approved` as `LayerFiling`.
- **Fix:**
  - In `pkg/model/compose.go` (`Store.TemplateComposition`): pass `approved || tmpl.Interactive` to `ComposeTemplateTask`, and when `tmpl.Interactive` is `true`, set `comp.Sources.Approved = LayerTemplate`.
  - In `ui/src/components/UseTemplateOverlay.jsx`: compute `const effectiveApproved = template?.interactive ? true : approved;` and pass `effectiveApproved` in both `previewPath` and `submit`.
- **Regression coverage:**
  - Go unit test: `TestInstantiatingAnInteractiveTemplateFilesAnInteractiveTask` in `pkg/ui/interactive_template_test.go`.
  - Component test: `files a chat from Use… and opens it` in `ui/src/components/InteractiveTemplate.test.jsx`.
  - Playwright E2E test: test 21 in `ui/e2e/recurring-composed-walkthrough.spec.js`.
- **Visual verification:**
  - Inspected `UseTemplateOverlay` before and after at `1440×900` and `390×844` in both `light` and `dark` themes; verified `Approval: approved when filed — from the template` and zero horizontal overflow (`scrollWidth - clientWidth === 0`).

### 2. `[/processes](/processes)` `NarrowingBar` vs `GET /api/schedules` parity for `requireApproval`, `sort=cadence`, and `sort=overlap`

- **Reproduction:**
  1. On `[/processes](/processes)`, add filter `Requires approval is any of Yes` (`?f=requireApproval:any:yes`) or `Requires approval is empty` (`?f=requireApproval:empty:`), or sort by `Requires approval` (`?sort=requireApproval:asc`), and paste the query string onto `GET /api/schedules`. The server returned **HTTP 400** (`schedules have no attribute "requireApproval"`).
  2. Sort schedules on `[/processes?kind=schedule&sort=cadence:asc](/processes?kind=schedule&sort=cadence:asc)`: the UI orders schedules from shortest to longest cadence (`everyNHours` before `daily` before `weekly`), whereas `GET /api/schedules?kind=schedule&sort=cadence:asc` sorted alphabetically (`daily` before `everyNHours` before `weekly`).
  3. Sort schedules by `overlap:asc`: the UI orders `skip` before `allow`, whereas `GET /api/schedules?sort=overlap:asc` sorted `allow` before `skip`.
- **Root cause:**
  - `pkg/schedulefilter/filter.go` omitted `"requireApproval"` from `attributes` and omitted `order: Cadences` and `order: Overlaps` on `"cadence"` and `"overlap"`.
- **Fix:**
  - In `pkg/schedulefilter/filter.go`: added `Cadences = []string{"everyNMinutes", "everyNHours", "daily", "weekly", "monthly"}` and `Overlaps = []string{"skip", "allow"}`, set `order: Cadences` on `"cadence"` and `order: Overlaps` on `"overlap"`, and added `"requireApproval": {typ: typeEnum, values: func(f Facts) []string { return nil }, order: yesNoOrder}`.
- **Regression coverage:**
  - Go unit tests: `TestFilterReadsTheProcessesPagesConditions` in `pkg/schedulefilter/filter_test.go` and `TestTheSchedulesAPIAnswersTheProcessesPagesFilter` in `pkg/ui/schedule_filter_test.go`.
  - Playwright E2E test: `each kind chip's address asks the API for the schedules it shows` in `ui/e2e/processes-narrowing.spec.js`.
