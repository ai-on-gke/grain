# Recurring & Composed Work UI Walkthrough (Task 3461)

Task 3461 (following earlier same-day walkthrough [2026-11-08-recurring-composed-ui.md](2026-11-08-recurring-composed-ui.md) from Task 3357). End-to-end browser walkthrough of Grain's recurring and composed-work automation UI — `/processes` and the schedules, workflow schedules, templates, suites, suite runs, and triggers it lists — against a real `grain demo` server over throwaway embedded SQLite stores, together with three product bug fixes and regression tests.

## Environment & Commit Tested

- **Commit tested:** `82a986f89ae884897f47f85b62202e842b5931a6` (`origin/main` at task start; binary reported `v0.0.0-20261108094035-82a986f89ae8`). Fixes were verified against the rebuilt server and frontend on top of that commit.
- **Server:** `go build -tags grain_e2e ./cmd/grain`, started via `grain demo -open=false -schedules -github-sim -fake-ci -scheduled-workflows -reconcile=500ms` (plus dedicated runs with `-sensitive-hold` and `-unconfigured`) over throwaway SQLite stores under `/tmp`.
- **Frontend:** Production Vite bundle built via `cd ui && npm ci && npm run build` (Node `v20.19.0`) and embedded into `pkg/ui/static`.
- **Browsers:** Playwright Chromium (`chromium-1234`) and WebKit (`webkit-2336`) from `/var/cache/playwright`, tested at `1440×900` / `1400×900` desktop, `1024×844`, `620×844`, and `390×844` phone (`hasTouch: true`) in light and dark themes, plus UTC and `America/New_York` timezones.
- **Gemini API key:** `/home/debian/.gemini-api-key` was not present in this sandbox, so `LOOK=1` was unavailable; `ui/e2e/screenshot.mjs` before/after contact sheets were generated and paired with DOM geometry (`scrollWidth <= clientWidth`, bounding-box containment) assertions.
- **Sandbox package note:** `sqlite3` (`3.40.1-2+deb12u2`) was installed via `apt-get` to seed a legacy empty-`base` `suite_run` row and would be convenient to preinstall in the stock sandbox image.

## Stubs & Execution Boundaries

No real LLM agents were dispatched (`grain demo` never dispatches agent sandboxes). Only external/execution boundaries and supported demo/store seams were stubbed:

1. **`grain demo` built-in boundaries (`-github-sim`, `-fake-ci`, `-scheduled-workflows`, `-schedules`, `-reconcile`):**
   - Simulated bare Git repository (`githubsim/acme/widgets.git` with `main`, `candidate`, and `release/1.0`) used to commit and push real `.grain/` declarations (`.grain/templates/`, `.grain/suites/`, `.grain/schedules/`, `.grain/workflows/`, `.grain/triggers/`).
   - Loopback fake-CI endpoint (`POST /api/e2e/fake-ci/runs/{id}/finish`) used solely to settle workflow schedule CI runs (`conclusion: "success"` vs `"failure"`) so the real reconciler exercised `review: on-failure`.
2. **`ui/e2e/releasehelper` (`complete-suite-pass`):**
   - Repo's own store helper binary (`go build ./ui/e2e/releasehelper`) used to mark queued suite-pass tasks answered so the real suite reconciler advanced Pass 1 → Pass 2 → `Succeeded` and settled max-passes exhaustion (`Failed`).
3. **Legacy empty-`base` `suite_run` seed (`sqlite3`):**
   - Because `SuiteRunOverlay` requires `Branch` and `.grain/schedules` requires `base:` on suite schedules, one settled `suite_run` row had `base` set to `''` in the throwaway SQLite database (`UPDATE suite_run SET base = '' WHERE id = …`) to exercise the legacy `"default branch"` rendering on both the `Suite runs` row and `SuiteRunPane`.
4. **Role-gating check (`ui/e2e/screenshot.mjs --access viewer`):**
   - Replaced `GET /api/config`'s `access` payload in-page with `grain/viewer` read-only scopes to verify write-control gating (`AccessGate`) on `/processes`.

## Walkthrough Steps & Outcomes

### 1. Processes Page (`/processes`), Counts, Chips, Narrowing, Sort & Legacy Redirects

- Signed in through the visible **Access code** form and clicked **Processes** in the left rail (`/processes`).
- Verified rail count badge progression: `9` on initial cold load before lazy trigger fetch (`3` schedules, `4` templates, `2` suites), updating to `10` once `GET /api/repos/acme/widgets/declarations` loaded the seeded trigger, and `15` after syncing repository declarations (`Schedules 4`, `Workflow schedules 2`, `Templates 6`, `Suites 2`, `Triggers 1`).
- Exercised every kind chip:
  - Single-chip narrowing (`?kind=schedule`, `?kind=workflow-schedule`, `?kind=template`, `?kind=suite`, `?kind=trigger`) and multi-chip selection (`?kind=template,suite`), confirming `All` resets `?kind=`.
  - Confirmed the **Suite runs** sub-section below the process list stays visible on `All` and whenever `Suites` is among the active kinds, and hides when narrowed exclusively to non-suite kinds.
- Exercised search (`?q=`), empty-match state (`"No processes match your search."`), and `NarrowingBar` (`Filter` → `Source` → `acme/widgets`, plus `?f=source:any:system` and toolbar `Clear`), verifying URL persistence across `page.reload()`, `page.goBack()`, and `page.goForward()`.
- Verified derived **Last run** / **Next run** columns on templates and suites: unscheduled items rendered `"on demand"`, while templates and suites referenced by enabled schedules rendered the earliest upcoming `nextRunAt` and latest `lastRunAt` derived across those schedules.
- Exercised all sort modes (`Next run` asc/desc, `Last run`, `Name`, `Kind`, and `Custom order` `?sort=custom`):
  - Reordered rows via keyboard (`Enter` to pick up, `Escape` to cancel without moving, `Enter` + `ArrowDown` + `Enter` to commit), pointer drag-and-drop, and `390×844` touch drag; confirmed custom order persisted in `localStorage` across reload.
- Verified all legacy routes redirect cleanly via `replaceState`:
  - `/schedules` → `/processes?kind=schedule`, `/schedules/sched-1` → `/processes/schedules/sched-1`
  - `/templates` → `/processes?kind=template`, `/templates/template-1` → `/processes/templates/template-1`
  - `/suites` → `/processes?kind=suite`, `/suites/suite-1` → `/processes/suites/suite-1`
- Verified missing-ID deep links (`/processes/schedules/no-such-sched`, `/processes/templates/no-such-tmpl`, `/processes/suites/no-such-suite`, `/processes/suites/runs/999999`) fall back to `/processes` and display an explanatory error alert (`no schedule "no-such-sched"`, etc.) rather than a blank pane.

### 2. Header `+ New` Menu & Context-Sensitive Creation

- On `All` (and multi-kind views), clicking `+ New` opened `[data-testid="new-process-kind"]` with **Schedule**, **Template**, **Suite**, and the footer hint linking `.grain/` (`"Workflow schedules and triggers are declared in a repository's .grain/ directory."` → `/docs/declarations`).
- When narrowed to a single creatable kind, the header button switched to `+ New schedule`, `+ New template`, or `+ New suite` and opened that creation overlay directly without a menu.
- When narrowed to non-creatable kinds (`Workflow schedules` or `Triggers`), `+ New` remained the menu button offering the three creatable kinds plus the `.grain/` hint.

### 3. Templates: Validation, Policy Verdicts, Filing (`New task` vs `Use…`), Composition & Deletes

- Configured a capability policy rule on `/settings/capabilities#capability-policy` allowing `acme/widgets` to hold `demo-cloud#cloud-key` while leaving `acme/gadgets` uncovered.
- Created **WT3461 Full Template** with whole-number **Task priority** validation (`0`, `-1`, `1.5`, and `abc` each rejected inline; `8` accepted), **Keep running** (`keep_running`), `Grain API` capability presets (including dangerous capability markers), `demo-cloud#cloud-key` policy verdicts (neutral when unbound, allowed on `acme/widgets`, denied warning on `acme/gadgets`), `acme/gadgets` read-only repo, network access, custom fields, and **Model** picker (confirming options matched only models advertised by enabled connections: `gemini-2.5-pro` and `gemini-2.5-flash`).
- Verified **New task from this template**:
  - On fieldless **WT3461 Full Template**, opened `NewTaskOverlay` pre-seeded with the template and **Task priority** `8`.
  - On parameterized template (`{{surface}}` input field), routed to `UseTemplateOverlay` (**Use a template**) instead of `NewTaskOverlay` so required template fields are collected before filing.
- Exercised **Use…** (`UseTemplateOverlay`) on:
  - Parameterized template (verified live title/body substitution in `CompositionPanel`).
  - Interactive (`live-chat`) template (verified explanatory live-session note in `UseTemplateOverlay`, and confirmed interactive templates are excluded from `ScheduleOverlay` and `SuiteOverlay` template pickers).
  - System repoless template (`Set up a GKE sandbox cluster`, confirming the repoless deployment note).
  - Unbound cross-repo template carrying both `acme/widgets` and `acme/gadgets` in `Reads` and `Writes` — uncovered **Bug 1** (fixed below).
- Reopened and edited templates; verified `390×844` phone-width composition preview kept long capability grant chips wrapped inside the dialog (`chipRight <= dialogRight`, `scrollWidth <= clientWidth`).
- Tested dependency-aware template deletion: attempting to delete **WT3461 Full Template** while referenced by a suite/schedule returned HTTP `400` naming the dependent suite/schedule in an error alert while keeping the template intact. Deleted throwaway template **WT3461 CrossRepo Unbound** (verifying `Cancel` on confirmation kept the overlay open, and `OK` deleted the row immediately).

### 4. Schedules: Task & Suite Schedules, Cadences, Overlap, Timezone, Lifecycle & Suppression

- Created raw task schedule **WT3461 Raw Every15m**:
  - Verified recurrence variants (`Every N minutes` with `< 10` minute floor refused by form validity and `15` accepted, `Every N hours`, `Daily`, `Weekly`, `Monthly`), overlap policy (`Skip` vs `Allow`), missing-branch warning from `BranchField`, `Keep running`, capabilities, network access, custom fields, and `Preferences` timezone rendering (`America/New_York` displaying `EST` beside **Time of day**).
  - Verified unsaved-edits guard (`DISCARD_PROMPT`) on `Escape`.
  - Edited cadence/title, toggled **Enabled** switch off (`Paused`) and back on (`Enabled`) directly from the row, verified dirty-edit **Run now** warning when unsaved changes exist, triggered **Run now** (confirming `lastRunAt` and spawned task chip updated in place without reload), and verified `409` overlap refusal when a run from the schedule was still active under `Skip` policy.
- Created template-backed schedule **WT3461 Template Sched** and suite-backed schedule **WT3461 Suite Sched**:
  - Confirmed composition preview showed template priority (`8`) and field provenance layers (`from the template`, `from the schedule`), and that creating, pausing, resuming, running now, and deleting immediately updated derived **Last run** / **Next run** on the target template and suite rows without reload.
- Verified off-target `git_ref` schedule suppression (`Suppressed` badge on row and warning alert inside `ScheduleOverlay` linking the suppressing task) and deleted throwaway schedules after testing confirmation dismiss/accept.

### 5. Suites & Suite Runs

- Created **WT3461 MultiPass Suite** (`Run until no repo changes or new tasks`, `maxPasses: 3`, `autoMerge`, `requireApproval` switches) and created an inline template (`+ New template` inside `SuiteOverlay`), confirming the newly created template was automatically ticked in the suite's template list.
- Verified renaming that template on `/processes` uncovered **Bug 3** in the toolbar `Filter → Template` menu (fixed below).
- Exercised `Run a fixed number of times` (`count = 0` rejected; positive integers accepted) and `maxPasses` bounds (`0` and `21` invalid).
- Started suite runs from:
  1. Suite-firing schedule **Run now** (confirming `Scheduled` provenance chip on the run row and inside `SuiteRunPane`, linking back to the schedule).
  2. Suite row **Run…** button on `release/1.0` (confirming `SuiteRunOverlay` preview showed both step priorities `7` and `8`).
  3. Generic **+ Run** button in the **Suite runs** header (`WT3461 Throwaway 1Pass Suite`).
- Inspected `SuiteRunPane` via row click and direct deep link (`/processes/suites/runs/<id>`):
  - Seeded a proposed follow-up task (`proposedBy` Pass 1 root task), completed Pass 1 via `releasehelper`, confirmed the reconciler advanced to **Pass 2**, completed Pass 2 cleanly, and verified **Succeeded** chip, **Pass 1** (with indented `.task-sublist` follow-up task) and **Pass 2** task groups, click-through to `/tasks/<id>`, and browser `Back` returning to `/processes/suites/runs/<id>`.
  - Triggered max-passes exhaustion on 1-pass `until_clean` suite when Pass 1 spawned a follow-up task, confirming **Failed** chip and failure reason rendered on both the `Suite runs` row and `SuiteRunPane`.
  - Confirmed the legacy empty-`base` `suite_run` row rendered `"default branch"` on both the list row and `SuiteRunPane`.
- Tested dependency-aware suite deletion (`400` refusal while scheduled), then deleted **WT3461 Throwaway 1Pass Suite** with confirmation and verified its historical **Failed** suite run remained listed under **Suite runs**.

### 6. Repository-Declared Processes & Malformed Declarations (`docs/declarations.md`)

- Pushed `.grain/templates/ triage.yml` & `opus.yml` (`model: claude-opus-5-5`), `.grain/suites/release-readiness.yml`, `.grain/schedules/triage-hourly.yml` & `readiness-daily.yml`, `.grain/workflows/nightly-e2e.yml` & `weekly-flake.yml` (`schedule:` + `review: on-failure`), and `.grain/triggers/design.yml` into `acme/widgets` `main`, then synced declarations on `/repos/acme/widgets/settings/advanced` — uncovering **Bug 2** when clicking **Processes** in the rail (fixed below).
- Confirmed every declared row displayed `Declared by acme/widgets` with its `.grain/...` path tooltip.
- Opened declared schedule, template, and suite overlays:
  - Confirmed inputs were read-only (`DeclaredFields`), `Save` and `Delete` controls were absent, an info banner explained that the declaration lives in `.grain/...` in `acme/widgets`, and `DELETE /api/templates/repo:template:acme:widgets:opus` was refused with HTTP `400` naming `.grain/templates/opus.yml`.
  - Confirmed `opus.yml` rendered `claude-opus-5-5 (no connection advertises it)` in `ModelPicker`.
- Opened workflow schedule `nightly-e2e`:
  - Confirmed its `Run now` confirmation dialog stated `"Start a run of \"nightly-e2e\" now? This starts the workflow run straight away; the regular schedule is unchanged."`
  - Settled Run 1 green (`conclusion: "success"`): no review task filed. Settled Run 2 red (`conclusion: "failure"`): `review: on-failure` automatically filed review task (`/tasks/20`) linked from the workflow schedule row.
- Clicked trigger row (`design.yml`) and confirmed navigation to `/repos/acme/widgets/settings/advanced` (also verified `-sensitive-hold`'s `payments.yml` trigger row).
- Pushed malformed `.grain/schedules/broken.yml` (`every: 3m` below 10-minute floor): confirmed `/repos/acme/widgets/settings/advanced` reported the parse error while `/processes` preserved all `15` last-good declared and stored rows without loss.

### 7. Additional Exploratory Checks (Console/Network Telemetry, Empty State, Viewer Role, Responsive & WebKit)

- **Console & page errors:** `0` uncaught `pageerror` events across all Chromium and WebKit runs; no unexpected console errors or 5xx responses (all 400/404/409 responses were intentional negative-test assertions).
- **Empty & system-protected states:** On `-unconfigured` after deleting all user schedules, suites, and templates, verified narrowings with zero rows showed `"No processes match your search."` and **Suite runs** showed `"No suite runs yet."`, while built-in `system` templates remained protected from deletion.
- **Role gating (`grain/viewer`):** Verified write controls (`+ New`, `Use…`, `Run…`, `+ Run`, schedule `Enabled` switch) are gated for read-only viewers.
- **Responsive & cross-browser:** Confirmed `scrollWidth <= clientWidth` across `1440px`, `1400px`, `1024px`, `620px`, and `390px` viewports in Chromium (light and dark), and verified `/processes` list + `/processes/suites/runs/<id>` deep-link rendering in WebKit.

## Bugs Found & Fixed in This Task

### Bug 1 — Unbound cross-repo template with `Reads` fails with HTTP 400 when filed via `Use…` against one of those repos (and previews target repo under `Reads`)

- **Reproduction:**
  1. Create an unbound template (`Repo` left blank) with `Reads: ["acme/widgets", "acme/gadgets"]` (offered by `ReadOnlyReposField` because an unbound template has no fixed target yet).
  2. Click **Use…** on `/processes`, select `Repo: acme/widgets` and `Base branch: main`.
  3. Observe `CompositionPanel` previews `Reads: acme/gadgets, acme/widgets` alongside `Repo: acme/widgets`, and clicking **Create task** fails with HTTP `400`: `"acme/widgets is this task's own repo, which its run already clones and pushes to -- it cannot also be a repo the run may only read"`. Because `UseTemplateOverlay` has no `Read-only repos` editor, the user cannot file the template against `acme/widgets`.
- **Root cause:** `model.ComposeTemplate` (`pkg/model/compose.go`) stripped the resolved target repo from `Writes` via `model.WritesBesides`, but copied `tmpl.Reads` (and `sched.Reads`) verbatim, and `Client.CreateTask` (`pkg/ui/client.go`) only stripped `target` from `reads` for `ProposedBy` tasks, not for reads inherited from an unbound template (`fromTemplate` in `pkg/ui/templates.go`).
- **Fix & regression coverage:** Added `model.ReadsBesides` in `pkg/model/task.go`, applied it in `composeTemplate` and `ComposeScheduleTask` (`pkg/model/compose.go`), and recorded `readsFromTemplate` / `writesFromTemplate` in `fromTemplate` (`pkg/ui/templates.go` & `pkg/ui/client.go`) so redundant template-inherited reads/writes are dropped even when `Repo` falls back to `DefaultTarget`, while explicit duplicate `Reads` typed on a request remain refused. Covered by `TestReadsBesidesDropsTheRepoTheTaskIsFiledAgainst` (`pkg/model/task_test.go`) and `TestFilingUnboundTemplateDropsTargetRepoFromReadsAndWrites` (`pkg/ui/templates_test.go`).

### Bug 2 — SPA navigation into `/processes` leaves `schedules`, `templates`, and `suites` stale beside freshly fetched `triggers` and `suiteRuns`

- **Reproduction:**
  1. Sync `.grain/` declarations on `/repos/acme/widgets/settings/advanced` (adding declared schedules, workflow schedules, templates, suites, and a trigger).
  2. Click **Processes** in the left rail.
  3. `triggers` (`1`) and `suiteRuns` fetch immediately on entering `view === "processes"`, while `schedules`, `workflow schedules`, `templates`, and `suites` display pre-sync counts and rows for >3 seconds because switching `view` resets `App.jsx`'s `setInterval(poll, POLL_INTERVAL_MS)` timer.
- **Root cause:** `App.jsx`'s `view === "processes"` effect only called `refreshSuiteRuns()`, relying on the 3-second poll effect (which includes `view` in its dependency list and restarts its interval on navigation) for `schedules`, `templates`, and `suites`.
- **Fix & regression coverage:** Updated the `view === "processes"` effect in `ui/src/App.jsx` to refresh `refreshSchedules()`, `refreshTemplates()`, and `refreshSuites()` alongside `refreshSuiteRuns()` whenever navigating to `/processes` after initial shell load (`shellLoadedRef.current`), avoiding duplicate requests on cold mount. Covered by `"refreshes schedules, templates and suites immediately when navigating into Processes"` in `ui/src/App.schedules.test.jsx`.

### Bug 3 — Saving/renaming a template in `TemplateOverlay` on `/processes` does not refresh `suites`

- **Reproduction:**
  1. Create a suite referencing template `"WT3461 Inline Suite Template"`.
  2. On `/processes`, open `"WT3461 Inline Suite Template"`, rename it to `"WT3461 Inline Suite Template Renamed"`, and click **Save**.
  3. Open toolbar **Filter → Template** before the next background poll: the old name `"WT3461 Inline Suite Template"` is still offered alongside the new name because `suites` still hold stale `items[].templateName` from `GET /api/suites`.
- **Root cause:** `ProcessesList.jsx` passed `onSaved={() => Promise.all([onRefreshTemplates?.(), onRefreshSchedules?.()])}` to `TemplateOverlay`, omitting `onRefreshSuites?.()`.
- **Fix & regression coverage:** Added `onRefreshSuites?.()` to `TemplateOverlay`'s `onSaved` handler in `ui/src/components/ProcessesList.jsx`. Covered in `ui/src/components/ProcessesList.test.jsx`.
