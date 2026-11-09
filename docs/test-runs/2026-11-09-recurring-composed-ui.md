# Processes — schedules, templates, suites, suite runs and triggers walkthrough — 2026-11-09 (task 3717)

- **Date:** 2026-11-09 (UTC)
- **Task:** task 3717, branch `grain/098073e4/task-3717-walk-through-processes`, base `main`.
- **Commit tested:** `8ea343b02137` (`v0.0.0-20261109043852-8ea343b02137`). Re-verified on a rebuilt binary over the same checkout with the locked-overlay `GrantsPicker` / `DeclaredFields` fix applied.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`.
  - Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, headless Chromium `1234`), tested across `1440×900`, `1024×900`, `620×900`, and `390×844` (phone) viewports in both light and dark color schemes.
  - Built frontend and server via `cd ui && npm run build` and `go build -tags grain_e2e -o /tmp/grain-walk/grain ./cmd/grain`.
  - Server started with `grain demo -addr 127.0.0.1:0 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` over a fresh throwaway `TMPDIR` (embedded SQLite store).
  - `/home/debian/.gemini-api-key` was not present in this sandbox, so no real LLM calls were made; no screenshots or browser artifacts were committed.

## Stubs and setup

Every navigation, filter, sort, reorder, form fill, validation, confirmation dialog, and DOM check ran through the real Chromium browser against the real `grain demo` server and embedded SQLite store. Only unavailable external/execution boundaries or store states unreachable without running live agent sandboxes were seeded or stubbed:

1. **Demo flags (`-schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms`):** Built-in `grain demo` seed flags providing local bare Git repositories (`githubsim` with `acme/widgets.git`), fake CI runs (`fakeci`), seeded schedules/templates/suites, and declared `.grain/workflows/nightly-ci.yml` + `.grain/schedules/nightly-ci-candidate.yml` workflow schedules without calling external GitHub APIs or launching agent VMs.
2. **Deployment connections & capability policy (`PUT /api/deployment`, `PUT /api/capability-policy`):** Registered two model connections on the throwaway server — `gemini-direct` (`enabled: true`, advertising `gemini-2.5-pro` and `gemini-2.5-flash`, deployment default `gemini-2.5-pro`) and `claude-retired` (`enabled: false`, advertising `claude-opus-4-retired`) — so `ModelPicker` exercised active models, deployment default resolution, disabled connections, and unserved declared models. Configured capability policy rules (`deny` on `cloud#cloud-viewer` for `acme/widgets`, repository-restricted `release#*`, plus Grain's built-in `ci_only` rule on `repo#read`) so `GrantsPicker` exercised `denied`, `may be refused`, and `CI only` verdicts.
3. **Repository declarations in local `githubsim` (`git push` + `POST /api/repos/acme/widgets/declarations/sync`):** Pushed `.grain/templates/decl-bump.yml` (with `model: claude-opus-4-retired`, `priority: 7`, `admission: durable`, `hosting: console`, `inputs: [ecosystem]`, `push: [refs/tags/v*]`, and `capabilities`), `.grain/templates/decl-pair.yml`, `.grain/suites/decl-sweep.yml`, `.grain/schedules/decl-bump-sched.yml`, and `.grain/triggers/docs-guard.yml` to `acme/widgets.git` on `main`, then synced declarations via the real server endpoint.
4. **Fake CI run completion (`POST /api/e2e/fake-ci/runs/<id>/finish`):** Used the `-tags grain_e2e` fake-CI test hook to finish CI runs started by clicking `Run now` on workflow schedules (`conclusion: "success"` and `conclusion: "failure"`) so the reconciler settled their review outcomes.
5. **Suite-run pass settlement & base-less legacy row (`ui/e2e/releasehelper` + SQLite row seed):** Because no live agent runner executes spawned tasks in `grain demo`, `releasehelper complete-suite-pass` and `releasehelper sync-suites` settled pass 1 and pass 2 tasks for Suite Run A. Two SQLite rows were written directly on the throwaway store before reloading the browser: an `INSERT INTO task_link` (`kind = 'proposed-by'`) linking a proposed follow-up task to pass 1's spawned task, and `UPDATE suite_run SET base = ''` on Suite Run A to exercise how a base-less suite run (reachable via API/legacy rows, as the UI's `RunSuiteOverlay` requires a non-empty `Base branch`) renders `"default branch"` on both its list row and `SuiteRunPane`.

---

## Walkthrough coverage and outcomes

### 1. Processes list (`/processes`), kind chips, search, filter, sort, custom reorder & legacy redirects

- **Rail entry & counts:** Clicked `Processes` in the left rail (`/processes`). Before first visiting `/processes`, the rail badge read `15` (4 schedules + 2 workflow schedules + 6 templates + 3 suites); upon opening `/processes`, `App.jsx` fetched `/api/repos/acme/widgets/declarations` for triggers and updated both the rail badge and page header count to `16`.
- **Kind chips & counts:** Verified all six kind chips and counts: `All 16`, `Schedules 4`, `Workflow schedules 2`, `Templates 6`, `Suites 3`, `Triggers 1`. Clicking single chips (`?kind=schedule`, `?kind=workflow`, `?kind=template`, `?kind=suite`, `?kind=trigger`) and multi-kind combinations (`Shift`/multi-toggle producing `?kind=schedule,template`, showing `10` rows) updated the table and `aria-pressed` states accurately. Confirmed the `Suite runs` section appears below the table only on `All` and `Suites`, and hides on `Schedules`, `Workflow schedules`, `Templates`, and `Triggers`.
- **Derived `Last run` / `Next run`:** Confirmed templates and suites scheduled by enabled schedules displayed the earliest firing time derived from those schedules (for example, `release-qualification` showed `11/10/2026, 12:00:00 AM` from `Release Qualification: Run sanity checks & unit suites`; `WT Nightly dependency bump` showed `11/10/2026, 4:30:00 AM` from `Declared Nightly Bump`), unscheduled templates and suites (`security-audit`, `Release Qualification Suite`, `Declared Release Sweep`, `Set up a GKE sandbox cluster`) displayed `on demand`, and triggers (`docs-guard.yml`) displayed `on change`.
- **Search & NarrowingBar filters:** Entered `until clean` in search (`?q=until+clean`); kind chip counts narrowed dynamically (`All 1`, `Suites 1`, all others `0`), matching the `until clean (max 3)` mode chip on ` smoke-suite`. Opened `Filter`, narrowed by `Enabled` and `f=source:any:acme/widgets` (`10` repository-declared rows), and cleared back to `16` with `Clear`.
- **Sort & Custom order reorder (keyboard/tap + pointer drag):** Cycled `Sort` (`button[title="Choose the order this list is shown in"]`) across `Next run` (`soonest first` / `latest first`), `Last run`, `Name`, and `Kind`, verifying URL `?sort=` synchronization. Removed the active sort (`Remove sort 1`) to enter `Custom order` (`?sort=custom`), exposing drag handles (`Move <name>`). Reordered rows first via keyboard/tap pickup-and-slot (`Enter`/`Click` on `Move Weekly Dependency & Vulnerability Audit` -> `Move here, above ...` slot button) and then via HTML5 pointer drag (`li0.dragTo(li3)`), confirming the custom order persisted in `localStorage` across full page reload.
- **Query persistence & history navigation:** Navigated to `/processes?kind=template&q=bump`, reloaded (`F5`), and stepped through browser `Back` and `Forward`, confirming chip states, search box text, and filtered rows stayed synchronized with the URL.
- **Legacy URL redirects:** Verified all legacy routes redirected cleanly into Processes:
  - `/schedules` -> `/processes?kind=schedule`
  - `/templates` -> `/processes?kind=template`
  - `/suites` -> `/processes?kind=suite`
  - `/schedules/sched-1` -> `/processes/schedules/sched-1` (opening `ScheduleOverlay`)
  - `/templates/template-1` -> `/processes/templates/template-1` (opening `TemplateOverlay`)
  - `/suites/suite-1` -> `/processes/suites/suite-1` (opening `SuiteOverlay`)
  - `/suites/runs/999` (non-existent run ID) -> `/processes` with non-crashing error alert `no suite run "999"`.

### 2. Header `+ New` menu vs single-kind direct open

- On `All` (`/processes`), `Workflow schedules` (`?kind=workflow`), `Triggers` (`?kind=trigger`), and multi-kind (`?kind=schedule,template`), the header button read `+ New` and opened the menu offering `Schedule`, `Template`, and `Suite`, followed by the non-interactive hint explaining that workflow schedules and triggers are declared in `.grain/` inside a repository with a link to `docs/declarations.md`.
- When narrowed to a single creatable kind, the button switched label and bypassed the menu to open that creation overlay directly:
  - `?kind=schedule` -> `+ New schedule` -> opened `New schedule`
  - `?kind=template` -> `+ New template` -> opened `New template`
  - `?kind=suite` -> `+ New suite` -> opened `New suite`

### 3. Templates: full creation, validation, `ModelPicker`, `GrantsPicker` verdicts, filing (`New task` vs `Use…`), editing, conflict & dependency-aware delete

- **Validation & `ModelPicker` / `GrantsPicker`:** Opened `+ New template` and created `walk-full-template`:
  - Tested `Task priority` whole-number validation: `0`, `-1`, and `1.5` each kept `Save` blocked with inline helper error `"a whole number, 1 or more; default 5, higher numbers run first"`; entering `8` cleared the error.
  - Opened `ModelPicker`: verified only models advertised by enabled connections (`Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro`, `gemini-2.5-flash`) were selectable, while `claude-opus-4-retired` from disabled connection `claude-retired` was omitted from new picks; on declared template `WT Nightly dependency bump` (`model: claude-opus-4-retired`), `ModelPicker` explicitly flagged `claude-opus-4-retired (no connection advertises it)`. Also confirmed disabling the default model's connection surfaces the unserved warning on `Deployment default`.
  - Exercised `GrantsPicker`: added Grain API preset/scopes (`api:tasks:read`, `api:tasks:propose@acme/widgets`, plus a `dangerous`-badged write scope) and capability server methods; verified `cloud#cloud-viewer` was disabled with `denied` when `Repository` was `acme/widgets`, `may be refused` appeared when `Repository` was cleared (`policyRepo=""`), and `repo#read` / `release#*` displayed `CI only` and could not be picked for task runs.
  - Configured `Repository` (`acme/widgets`), `Base branch` (`main`), additional `Writes` & `Reads`, `Reviewers`, `Auto-merge`, `Interactive` (live chat), `Keep running` (`keep_running`), `Admission` (`durable`), `Hosting` (`console`), `Network access` (`open`), `When nothing is needed` (`ack`), and custom fields (`priority`, `requested-by`, `due-date`, `estimate`, `customer-facing`), then saved and confirmed persistence via reload and deep-link `/processes/templates/<id>`.
- **Filing via `New task from this template` vs `Use…`:**
  - On `walk-full-template` (no `inputs`), clicking `New task from this template` navigated to `/new` pre-populated with `walk-full-template`, carrying `Task priority` = `8`, `Interactive`, `Keep running`, and its grants across.
  - On declared template `WT Nightly dependency bump` (declares input `ecosystem`), clicking `New task from this template` opened `UseTemplateOverlay` (`File a task from a template`) with the required `ecosystem` field instead of `/new`.
  - Clicking row action `Use…` on `walk-full-template` opened `UseTemplateOverlay`, displaying the interactive live-chat note (`"This template files a live chat: ..."`), `Approval: approved when filed (template)`, and `Priority: 8 (template)`. Submitting created a live chat task whose persisted record carried `priority: 8, interactive: true, keepRunning: true`.
- **Edit, unsaved-changes guard & stale-save `409` conflict:** Reopened `walk-full-template`, typed an unsaved edit, pressed `Escape`, and confirmed the discard dialog intercepted closure (`Cancel` kept the editor open). Simulated a concurrent save in the background (`PUT /api/templates/<id>`), clicked `Save` in the stale editor, and verified the `409` conflict banner offered `Reload`, loading the fresher revision cleanly before saving our update.
- **Phone-width composition previews & dependency-aware delete:** Attached `walk-full-template` to both a schedule (`walk-tmpl-sched`) and a suite (`walk-suite`). Verified at `390×844` (phone viewport) that both composition previews rendered `Priority: 8` credited to the template and wrapped long repo-scoped grant chips (`api:tasks:propose@acme/widgets`, method references) cleanly inside the dialog (`scrollWidth <= clientWidth`). Attempting to delete `walk-full-template` while referenced returned `400` and displayed an inline error naming both `walk-tmpl-sched` and `walk-suite` at once. After deleting the throwaway schedule and suite, deleting `walk-full-template` succeeded after native confirmation.

### 4. Schedules: task-filing & suite-running, recurrences, 10-minute floor, `git_ref` suppression, overlap, live row updates

- **Raw task schedule & recurrence variants:** Created `walk-raw-sched`:
  - Verified empty-title refusal and the `Every N minutes` 10-minute floor (`5` refused validation; `15` passed and saved).
  - Cycled and verified `Every N hours`, `Daily`, `Weekly`, and `Monthly` recurrences plus explicit timezone presentation, `Overlap policy` (`skip`, `queue`, `allow`), `Keep running` (`keep_running: true`), `Network access`, custom fields, `GrantsPicker` (`api:tasks:read`), and `ModelPicker`.
- **Off-target `git_ref` suppression:** Created `walk-gitref-sched` targeting a `git_ref` outside target repositories; verified `/processes` rendered the `suppressed` chip without horizontal overlap and opening `ScheduleOverlay` displayed the suppression warning banner.
- **Template-backed schedule & live row synchronization without reload:** Created `walk-tmpl-sched` firing `walk-full-template`:
  - Confirmed composition preview credited template grants and priority `8` to the template and schedule grants to the schedule, and showed `walk-full-template`'s row on `/processes` immediately flip from `on demand` to `walk-tmpl-sched`'s `Next run` timestamp **without reloading the page**.
  - Clicked `Pause` -> both `walk-tmpl-sched` (`Paused`, next run `—`) and `walk-full-template` (`on demand`) updated live without reload.
  - Clicked `Resume` -> `Next run` restored on both rows immediately without reload.
  - Clicked `Run now` (confirming dialog prompt) -> spawned the task and updated `Last run` on both `walk-tmpl-sched` and `walk-full-template` live without reload.
  - Clicked `Run now` a second time while the spawned task was still open under `overlap: skip` -> server answered `409` and UI surfaced the overlap refusal alert cleanly.
- **Suite-backed schedule:** Created `walk-suite-sched` targeting `walk-suite`:
  - Verified blank `Base branch` validation refusal before entering `main` and saving.
  - Confirmed `walk-suite`'s row on `/processes` updated from `on demand` to `walk-suite-sched`'s `Next run` without reload.
  - Clicked `Run now` -> created a `Scheduled` suite run in `Suite runs`; clicking the `Scheduled` chip on that suite run opened `walk-suite-sched` in `ScheduleOverlay`.

### 5. Suites: create, inline `+ New template`, modes, pass limits, approval, auto-merge & delete

- Opened `+ New suite` and created `walk-suite`:
  - Clicked inline `+ New template` inside `SuiteOverlay`, created `walk-inline-tmpl`, and confirmed `TemplateOverlay` closed back into the still-open `SuiteOverlay` with `walk-inline-tmpl` automatically selected alongside `walk-full-template`.
  - Exercised `Run mode` (`Fixed pass count` with count `2` vs `Until clean` with max passes `3`), `Require approval` (confirming preview explained passes wait for human approval), and `Auto-merge`.
  - Saved, reloaded, reopened `/processes/suites/<id>`, edited mode/pass limit, and verified throwaway suite deletion (`Cancel` kept the suite; accepting `Delete` removed it cleanly).

### 6. Suite runs: row `Run…` & header `+ Run`, states, `"default branch"`, deep links, pass grouping, follow-ups & task navigation

- **Launching from both entry points:**
  - Started **Suite Run A** from `walk-suite`'s row `Run…` button (verifying the modal noted which templates inherit the run's base branch).
  - Started **Suite Run B** from the `Suite runs` section header `+ Run` button, picking the suite from the chooser.
- **States, pass grouping, `"default branch"`, follow-ups & deep link:**
  - Observed `running` presentation on Suite Run A, settled pass 1 and pass 2 via `releasehelper`, seeded a `proposed-by` follow-up task on pass 1's task, and cleared `suite_run.base` to `""` to test base-less rendering.
  - Reloaded `/processes` and navigated directly to `/processes/suites/runs/<runA_id>` (including full reload on the deep link): confirmed both the `Suite runs` row and `SuiteRunPane` rendered `default branch` when `base` was empty, grouped spawned tasks under `pass 1` and `pass 2` headers, rendered the proposed follow-up task link, and settled as `succeeded`.
  - On **Suite Run B**, clicked the pass 1 spawned task link inside `SuiteRunPane`, confirming client-side navigation to `/tasks/<taskId>`. Closed that task without completing it and synced suites: Suite Run B settled as `failed` with summary `"pass 1 had a task that failed, or closed without completing"`.

### 7. Repository-declared templates, suites, schedules, workflow schedules & triggers (`docs/declarations.md`)

- **Declared badges & provenance:** Confirmed all `.grain/` items displayed `Declared by acme/widgets` chips whose tooltips named both repository and file path (e.g., `Declared in acme/widgets, .grain/templates/decl-bump.yml`), while the built-in template displayed `🔒 System`.
- **Read-only overlays & explanations:** Opened declared template `WT Nightly dependency bump`, system template `Set up a GKE sandbox cluster`, declared suite `Declared Release Sweep`, and declared schedule `Declared Nightly Bump`. Each displayed a top `DeclaredNotice` naming `acme/widgets` and the exact `.grain/...` file (or built-in system explanation), kept `Save` and `Delete` disabled upfront rather than failing silently on submit, and still permitted non-mutating actions (`New task from this template`, `Use…`, schedule `Run now`, suite `Run…`). Confirmed tasks filed from `decl-bump` carried its `push:` ref allowance (`refs/tags/v*`).
- **Workflow schedules & `Run now`:** Inspected both `Review last night's "nightly-ci" workflow run` (`main`, `on_failure`) and `Review last night's "nightly-ci" workflow run on candidate` (`candidate`, `review: always`). Clicking `Run now` prompted:
  > `Run "Review last night's "nightly-ci" workflow run on candidate" now? It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged.`
  > Finishing `fake-ci-1` (`candidate`, `review: always`) with `success` filed its review task and updated the row's `Last run`; finishing `fake-ci-2` (`main`, `on_failure`) with `failure` filed its failure review task, whereas a passing run on `on_failure` filed no task and noted the green run on the row.
- **Triggers:** Clicked trigger row `docs-guard.yml` (`/processes/triggers/acme/widgets/.grain/triggers/docs-guard.yml`) and confirmed it navigated directly to `/repos/acme/widgets/settings/advanced` with `.grain/triggers/docs-guard.yml` displayed under the repository's declarations.

### 8. Responsive viewports, themes, keyboard reachability, cleanup & telemetry

- Deleted all throwaway `walk-*` schedules, suites, and templates through the UI and reloaded `/processes`, confirming the count returned cleanly to `16`.
- Audited `/processes` and open overlays across `1440×900`, `1024×900`, `620×900`, and `390×844` in both light and dark themes: `documentElement.scrollWidth <= clientWidth` held at every breakpoint and theme (`0` horizontal overflow).
- Tested keyboard opening and focus restoration: pressing `Enter` on a row's `a.row-link` opened its overlay, and pressing `Escape` closed the overlay and returned `document.activeElement` to that row's `a.row-link`.
- **Browser & network telemetry:** `0` uncaught `pageerror` exceptions, `0` unexpected console errors (aside from the expected initial `401` prior to submitting the access code), and `0` `5xx` HTTP responses across the entire walkthrough.

---

## Bugs found and fixed

### 1. Fixed in this task: Read-only declared and system overlays exposed active-looking `GrantsPicker` and inline action buttons

- **Deterministic reproduction:**
  1. Start `grain demo -schedules -github-sim` and sign in.
  2. Open `/processes/templates/system:gke-sandbox-cluster` (or any repository-declared template/schedule such as `/processes/templates/repo:template:acme:widgets:nightly` or `/processes/schedules/repo:schedule:acme:widgets:nightly`, or a repository-declared suite `/processes/suites/repo:suite:acme:widgets:smoke`).
  3. Observe the `Grants` section and inline form buttons (`Add grant…`, per-grant `Remove …`, `+ New template`, `+ Add input`):
     - `TemplateOverlay` and `ScheduleOverlay` rendered `<GrantsPicker>` without passing `disabled={locked}` / `disabled={!!declaredIn}`, so `Add grant…` and per-grant `Remove` buttons were rendered in full active primary styling even though `<DeclaredFields>` wrapped them in `<fieldset disabled>` (causing clicks to silently do nothing).
     - `<DeclaredFields>` in `ui/src/components/Declared.jsx` styled disabled text inputs and selects, but did not style native `:disabled` `.MuiButtonBase-root` controls (`color: "action.disabled"`, `pointerEvents: "none"`) or standalone `.MuiChip-deleteIcon` elements outside `.MuiAutocomplete-root`.
- **Fix:**
  - Passed `disabled={locked}` to `<GrantsPicker>` in `ui/src/components/TemplateOverlay.jsx` and `disabled={!!declaredIn}` in `ui/src/components/ScheduleOverlay.jsx` so `Add grant…` and grant-removal buttons are omitted on read-only templates and schedules.
  - Updated `DeclaredFields` in `ui/src/components/Declared.jsx` to apply `pointerEvents: "none"` to `& .MuiChip-deleteIcon` and muted disabled styling (`color: "action.disabled"`, `borderColor: "action.disabledBackground"`, `pointerEvents: "none"`) to `& .MuiButtonBase-root:disabled`.
- **Regression coverage:**
  - `ui/src/components/BuiltInTemplate.test.jsx`: verified system templates, declared templates, and declared schedules omit `Add grant…` and `Remove …` grant buttons.
  - `ui/e2e/recurring-composed-walkthrough.spec.js` (test 9): verified in Chromium against a live server that declared suites disable pointer events on `+ New template` and declared/system templates render zero `Add grant…` buttons.

---

## Minor observation (no action taken)

- **Left-rail `Processes` count excludes repository triggers until `/processes` is visited once:** `App.jsx` fetches `/api/repos/<repo>/declarations` for `processTriggers` lazily when `view === "processes"` becomes active, so on initial app load at `/` the rail badge shows `15` (schedules + workflow schedules + templates + suites) and updates to `16` once `/processes` is opened (`Sidebar.jsx` documents counting triggers read so far to avoid eager per-repo git reads on unrelated pages). Left unchanged as intentional lazy loading.
