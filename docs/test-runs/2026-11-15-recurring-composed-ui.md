# Recurring and Composed-Work Automation UI Walkthrough (2026-11-15)

- **Date:** 2026-11-15 (UTC), 04:58–05:55.
- **Task:** [Task #5889](/tasks/5889), branch `grain/53549e42/task-5889-walk-through-processes`, base `main`.
- **Commit tested:** `5a9bce62c` (`v0.0.0-20261115040402-5a9bce62c102`, footer `5a9bce6`). All fixes below were re-verified on a clean rebuild over a fresh throwaway SQLite store.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Node.js `v20.19.0`, Python `3.11.2` (`sqlite3` `3.40.1`).
  - Headless Chromium from `ui/node_modules/playwright` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone `UTC`, driven through visible browser controls at `1440×900` (light and dark) and `390×844` (light and dark).
  - Built with `cd ui && npm ci && npm run build`, `cd ui-ng && npm ci && npm run build`, and `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` over a throwaway `TMPDIR`.
  - No `/home/debian/.gemini-api-key` was mounted in this sandbox, so no live LLM connection was called and `LOOK=1` was unavailable. Before/after screenshots and `contact-sheet.png` were rendered with `ui/e2e/screenshot.mjs` across `/next/processes`, `/next/processes/templates`, `/next/processes/suites`, and `/next/processes/declarations` at desktop (`1440×900`) and phone (`390×844`) in light and dark (not committed to git).
- **Console and network baseline:** Unauthenticated initial load returns `401` before Access Code sign-in (`alice@example.com`). After sign-in, zero uncaught page errors (`pageerror`), zero Angular `ERROR` console logs, and zero `5xx` responses occurred across the entire walkthrough. The only `4xx` responses were intentional negative validation tests documented below (plus Bug 6 before its fix).

## UI Surface Split (`/next/` vs Classic `/`)

Per the Two UIs migration rule (`ui-ng/src/app/shell/nav.ts` and `ui-ng/src/app/app.routes.ts`), `/next/processes` (`ProcessesPageComponent`) was walked first for every flow it implements, and classic `/processes` was walked for features that have not moved over to `/next/processes` yet:

| Area | Walked on `/next/` (`/next/processes`) | Still classic-only (`/processes`) |
| --- | --- | --- |
| Navigation, sections, filters, search, URLs | Rail `Processes`, 4 section tabs with counts (`Schedules & Triggers`, `Templates`, `Suites & Runs`, `Repo Workflows & Declarations`), subbar filter chips, top-bar `/` search (`Filter this view`), `?kind=` / `?q=` deep links, legacy `/schedules[/<id>]`, `/templates[/<id>]`, `/suites[/<id>]`, `/processes/suites/runs/<id>` | Unified single-table kind chips (`All`, `Workflow schedules`, `Triggers`), 19-attribute Sort menu + `Custom order` pointer/keyboard reorder (`?sort=custom`), derived Next/Last run columns on template and suite rows, rail total count badge |
| New menu | Rail `New ▾` menu (`New schedule`, `Task from template`, `Run a suite`) + per-tab header primary/tonal buttons (`New schedule`, `Launch from template`, `New template`, `Run suite`, `New suite`) | Single `+ New` dropdown with `.grain/` hint that collapses to `+ New <kind>` when narrowed to one creatable kind |
| Schedules | Create/edit prompt, template, and suite schedules; `Every N min` (10-min floor), `Every N hr`, `Daily`, `Weekly`, `Monthly`, `GitHub workflow`, `Git ref push` (forbidden repo refusal + off-target `Suppressed` chip, banner, `Next: suppressed`, Pause/Resume); overlap policy; draft & saved composition preview; Pause, Resume, Run now (including workflow confirmation), fired task link, Delete confirmation | Schedule-level `reads`/`writes`, `GrantsPicker` (Grain API presets + capability policy verdicts), `Admission`, `Hosting`, `Keep running`, `Network access`, custom fields, searchable `<ModelPicker>`, unsaved-edit prompt on task links |
| Templates | Create/edit template with comma-separated parameters + field label/default/required metadata + auto-merge; `Launch task` (`POST /api/tasks` and `POST /api/tasks/from-template`, unbound `Reads` overlap handling); rename propagation to suites/schedules; dependency-aware Delete refusal | Template-level `Task priority` (`1e3` / whole-number check), sandbox shape inputs, `Reviewers`, `Admission`, `Hosting`, `Keep running`, `Interactive` switch, `GrantsPicker`, `<ModelPicker>`, `New task from this template` → `/new` priority carry-over, interactive live-chat banner inside `Use…` |
| Suites & Suite runs | Create/edit suite (`Fixed pass count` / `Until clean`, pass counts, template chips, `Require approval`, `Auto-merge`, draft/saved composition preview), viewer RBAC gating, Run suite from inspector and header, Suite runs list & inspector (`Active`, `Succeeded`, `Failed`, base-less `default branch`, 0-pass failure, multi-pass grouping, `↳ Spawned` child tasks, task links, cancel active run) | Searchable template combobox inside Suite form and inline `+ New template` nested drawer inside Suite overlay |
| Repository declarations | `Repo Workflows & Declarations` tab (`Sync .grain/ declarations` switch, YAML line-error banner, capability `Accept declaration` with global-scope check, discovered `.grain/` files with trigger links to `/next/repos/<owner>/<name>/advanced`, controller workflow YAML overrides), plus `.grain` tags, read-only banners, and disabled/hidden mutating buttons on declared schedules, workflow schedules, templates, and suites | Dedicated `Workflow schedules` and `Triggers` kind-chip views on the unified table |

## Stubs and Seeded States

Every UI gesture, validation check, and render ran in real headless Chromium against the real `grain demo` HTTP server and SQLite store. Only unavailable external/agent execution boundaries were seeded:

1. **Demo flags:** `-schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` provided the local bare `acme/widgets.git` repository, fake CI runner, and 500ms schedule/declaration/suite reconcilers without running live LLM agents.
2. **Repository `.grain/` declarations (pushed via `git` to simulated `acme/widgets.git` `main`):**
   - First push: invalid `.grain/triggers/docs-guard.yml` (`name: invalid-key`) to verify sync error surfacing.
   - Second push: valid `.grain/templates/decl-bump.yml` (`priority: 7`, `admission: durable`, `hosting: console`, `keep-running: true`, unserved `model: claude-opus-4-retired`, `sandbox: {cpus: 4, memory-mb: 16384, disk-gb: 100}`, `push: [refs/tags/v*]`, `ecosystem` options field, `grain#source` capability), `.grain/templates/decl-pair.yml`, `.grain/suites/decl-sweep.yml` (`until_clean`, `max-passes: 3`), `.grain/schedules/decl-bump-sched.yml` (`daily` `03:00`), `.grain/schedules/decl-docs.yml` (`every: {hours: 6}`), `.grain/workflows/nightly-ci.yml` (`cron: "0 4 * * *"`), `.grain/schedules/nightly-ci-candidate.yml` (`workflow: nightly-ci`, `base: candidate`, `review: always`), and `.grain/triggers/docs-guard.yml`.
3. **Capability policy (`PUT /api/capability-policy`):** Added rule restricting `https://grain.dev/capabilities/grain#source` to `repos: ["other/*"]` so grants targeting `acme/widgets` evaluate as `may be refused`.
4. **Off-target `git_ref` schedule (`POST /api/schedules`):** Seeded `sched-3` watching `offlist/upstream @ refs/heads/main` (outside `targetRepos: ["acme/widgets"]`) to verify `watchedRepoOffTarget` suppression.
5. **Unbound template with overlapping `Reads` (`POST /api/templates`):** Seeded `walk-unbound-reads` (`repo: ""`, `reads: ["acme/widgets"]`, `interactive: true`, `keepRunning: true`, `admission: "durable"`, `hosting: "console"`, `priority: 9`) to test launching an unbound template on `/next/` when `Reads` includes the chosen target repository.
6. **Suite run edge states (SQLite `grain.db` on the throwaway store + task closures via `POST /api/tasks/{id}/close`):**
   -Cleared `base = ''` on suite run `#1` to exercise base-less suite run rendering (`default branch`), since the Run dialog requires a branch;
   - Linked follow-up task `#31` (`proposer_task_id = '27'` and `task_link` `proposed-by` `'27'`), attached pass 2 tasks (`#34`, `#35`) and completed suite run `#2` as `succeeded` across 2 passes;
   - Inserted suite run `#3` (`status = 'failed'`, `pass = 0`, `base = ''`, `last_error = 'pass 1 could not be filed: template missing'`) with zero `suite_run_task` rows.
7. **Viewer role (`page.route("**/api/config")`):** Rewrote `access` on one check per UI to `grain/viewer` holding `templates:write` without `suites:write` (`["tasks:read", "schedules:read", "templates:read", "templates:write", "suites:read", "repos:read"]`).

## Walkthrough Steps and Outcomes

### 1. Processes Navigation, Tabs, Chips, Search, Sort, Reorder, and URLs

- **`/next/processes` (Angular UI):**
  - Clicking `Processes` in the left rail navigated from `/next/inbox` to `/next/processes` via SPA routing and populated all four section tabs (`Schedules & Triggers`, `Templates`, `Suites & Runs`, `Repo Workflows & Declarations`).
  - Pressing `/` focused `Filter this view`; typing `Bump` narrowed the active list immediately to matching items (`1` schedule).
  - Subbar filter chips on `Schedules & Triggers` (`All`, `Active`, `Paused`, `Event triggers`, `Suppressed`), `Templates` (`All`, `Deployment`, `Repo-declared`, `Built-in`), and `Suites & Runs` (`Suites (N)` / `Suite runs (M)`) filtered rows accurately with `aria-pressed`.
  - `/next/schedules`, `/next/templates`, and `/next/suites` redirected to `/next/processes/<tab>`. Item links (`/next/schedules/sched-2`, `/next/templates/template-1`, `/next/suites/suite-1`, `/next/processes/suites/runs/<id>`) initially fell through `**` to `/next/inbox` (Bug 3, fixed and re-verified). Missing IDs (`/next/suites/runs/999`) now stay on Suites runs and show `No suite run "999".` without trapping mobile layout.
- **Classic `/processes`:**
  - Rail count updated from `processes 21` on `/` to `processes 22` once `/processes` loaded repository path triggers.
  - Derived `Next run` / `Last run`: scheduled templates (`Declared Nightly Bump`, `Declared Docs Pass`, `release-qualification`) and scheduled suite (`Release Qualification Suite`) displayed next/last timestamps derived from their active schedules; unscheduled templates and suites showed `on demand`; path trigger `docs-guard.yml` showed `on change`; off-target `git_ref` schedule showed `Suppressed` and `suppressed` next-run text.
  - Narrowing to `?kind=template&q=bump` persisted across full page reload and browser `Back`/`Forward` history.
  - `?sort=custom` exposed `Move …` handles (22 handles); pressing `Enter` on handle 3 and `Enter` on the first `Move here` slot reordered via keyboard, and pointer-dragging handle 2 onto handle 1 reordered via mouse.
  - `/schedules`, `/templates`, `/suites` landed on `/processes?kind=<kind>`; `/schedules/sched-2`, `/templates/template-1`, `/suites/suite-1`, and `/processes/suites/runs/1..3` opened the matching item or run overlay.

### 2. New Menu

- **`/next/`:** Opening rail `New ▾` listed `New task`, `Start a workstream`, `New session`, `Task from template`, `Run a suite`, `New schedule`, and `New board`. Initially, cancelling `/next/processes/schedules?new=1` left `?new=1` on the URL so clicking `New ▾` → `New schedule` a second time failed to reopen the dialog (Bug 2, fixed and re-verified). Stage header contextual buttons (`New schedule`, `Launch from template`, `New template`, `Run suite`, `New suite`) opened their respective drawers directly.
- **Classic `/processes`:** Header `+ New` opened a menu with `Schedule`, `Template`, `Suite` and the `.grain/` declaration note for workflow schedules and triggers; narrowing to `?kind=template` replaced the menu with direct `+ New template`.

### 3. Schedules

- **`/next/processes/schedules`:**
  - Created prompt-backed (`walk-ng-10min`), template-backed (`Declared Nightly Bump`), `git_ref` tag-triggered (`walk-ng-gitref` on `acme/widgets @ refs/tags/v1.0.0` with `Allow overlap`), and suite-running (`Release Qualification Suite`) schedules.
  - Tested `Every N minutes = 5` and forbidden `git_ref` watched repo `grain/config-infra`: both initially failed silently in the open drawer because `Every N minutes` had `min="5"` and `store.actionError` rendered behind the `inert` modal scrim (Bug 1, fixed and re-verified: `min="10"` and in-dialog `role="alert"` error banner inside the drawer bounds).
  - Off-target `git_ref` schedule (`sched-3` watching `offlist/upstream @ refs/heads/main`): displayed `Suppressed` pill and banner `Suppressed (watchedRepoOffTarget): watches offlist/upstream, which is not on this deployment's target repo list`, paused cleanly (`enabled: false`), and resumed (`enabled: true`). Initially rendered sentinel `Next: 2027-11-16T...` (Bug 4, fixed to `Next: suppressed` on both row and inspector).
  - Composition previews on template-backed (`Declared Nightly Bump`) and suite-backed schedules rendered `model` (`claude-opus-4-retired` / `gemini-2.5-pro`), `keepRunning: on`, `admission: durable`, `hosting: console`, `priority: 7`, `sandbox: 4 vCPU, 16384 MiB, 100 GiB disk`, and `capabilities` + `comp-layer` source chips cleanly inside `.comp-box` (`364px === 364px` at `390×844` phone width).
  - `Run now` on `walk-ng-10min` filed task `#23`; clicking `#23 · walk-ng-10min` navigated to `/next/task/23`. Editing title to `walk-ng-10min-edited` and deleting throwaway schedules via `ConfirmDialogComponent` persisted across reload.
- **Classic `/processes`:**
  - Verified full schedule overlay fields (`reads`/`writes`, `GrantsPicker` Grain API presets + capability policy verdicts, `Admission` `Standard`/`Bypass queue`/`Durable`, `Hosting` `Unattended`/`Interactive console`, `Keep running`, `Network access`, custom fields, searchable `<ModelPicker>` advertising `gemini-2.5-pro` and `gemini-2.5-flash`).
  - Modifying a schedule's title and clicking its fired `task 23` link prompted once (`"You have unsaved changes here. Leave and discard them?"`) and stayed on the schedule when dismissed.

### 4. Templates

- **`/next/processes/templates`:**
  - Created parameterized template `walk-ng-param-tmpl` (`pkg, ver`, label `Package name`, default `v2.0.0`, required toggle). Initially, inspecting a template whose required field had no default triggered `400 Bad Request` on `GET /api/templates/{id}/composition` (Bug 6, fixed by supplying a `{{field}}` preview placeholder for required fields only; after review task 6003, an optional field with no default is left out of the preview query as a real filing leaves it, so the preview stays the firing's own answer).
  - Launched unbound interactive template `walk-unbound-reads` (`reads: ["acme/widgets"]`) targeting `acme/widgets`: succeeded (`201 Created`, navigated to `/next/task/24`) because the server/filing path drops the chosen target repo from `reads` rather than failing.
  - Renamed `walk-ng-param-tmpl` → `walk-ng-param-tmpl-renamed` while referenced by `walk-ng-suite`: switching to `Suites & Runs` updated both `Member templates` and `Composition preview` (`· from walk-ng-param-tmpl-renamed`) without a page reload.
  - Dependency-aware delete: clicking `Delete` → `Delete template` while `walk-ng-suite` referenced it closed the confirmation modal and surfaced `template is used by 1 suite(s) ("walk-ng-suite"); remove it from those first`.
- **Classic `/processes`:**
  - `Task priority`: `1.5` failed whole-number `checkValidity()`; `1e3` saved as `priority: 1000`.
  - `New task from this template` on `walk-classic-full-tmpl` (no input fields) opened `/new` with `Task priority = 1000` carried across; on `Declared Nightly Bump` (has `ecosystem` input field) it opened the `Use…` dialog instead.
  - `Use…` on interactive `walk-unbound-reads` displayed: `"This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed."`
  - At `390×844` phone width, footer buttons measured `37px` tall (`Delete` at `top: 2465`; `New task from this template`, `Cancel`, `Save` on one row at `top: 2509` with `8px` spacing and zero overlap; `scrollWidth === clientWidth === 390`).

### 5. Suites and Suite Runs

- **`/next/processes/suites`:**
  - Created `walk-ng-suite` (`Fixed pass count = 2`, `Require approval` on, `Auto-merge` off), previewed draft composition, then edited to `Until clean` (`Maximum passes = 3`) and verified persistence.
  - Under `grain/viewer` (with `templates:write` and `suites:read`, lacking `suites:write`): `New suite` and `Run suite` header buttons were hidden and inspector `Run suite`, `Edit`, and `Delete` buttons were disabled.
  - Started suite run `#1` from the suite inspector `Run suite` button and suite run `#2` from the stage header `Run suite` button (`Release Qualification Suite` on `release/1.0`).
  - Verified all three suite run states under `Suite runs (3)`:
    - `Active` (`walk-ng-suite #1`, base-less): initially rendered `@ default` on row/inspector and dangling `Run #1 · acme/widgets @ (pass 1)` in suite detail `Recent runs` (Bug 5, fixed to `default branch` everywhere and `(pass 0)` omitted on 0-pass runs).
    - `Succeeded` (`Release Qualification Suite #2`, `release/1.0`, `ended on pass 2`): grouped `Pass 1 (2 tasks)` with `↳ Spawned #31 Follow-up: patch CVE found in pass 1 (proposed)` and `Pass 2 (2 tasks)`; clicking a task link navigated to `/next/task/28` and browser `Back` returned cleanly.
    - `Failed` (`Release Qualification Suite #3`, 0 tasks): rendered `Run 2× · ended without filing a pass`, `Started by schedule sched-1`, and error banner `pass 1 could not be filed: template missing` with zero console errors.
  - At `390×844` phone width (light and dark), every status pill (`Succeeded` `98px`, `Active` `69px`, `Failed` `67px`) stayed whole (`scrollWidth === clientWidth`) and inside its row bounds, with `documentElement.scrollWidth === 390`.
- **Classic `/processes`:**
  - Verified searchable Templates picker in Suite overlay, inline `+ New template` nested creation form, and confirmed that `grain/viewer` (holding `templates:write` without `suites:write`) had Suite `Save` disabled while `+ New template` remained enabled.

### 6. Repository-Declared Items, Workflow Schedules, and Triggers

- **`/next/processes`:**
  - Toggling `Sync .grain/ declarations` ON with invalid `.grain/triggers/docs-guard.yml` surfaced `Sync / validation error: reading acme/widgets's declarations: .grain/triggers/docs-guard.yml: yaml: unmarshal errors: line 1: field name not found in type repodecl.rawTrigger`.
  - Pushing valid `.grain/` files synced commit `7641fcf5b7db` (`2` templates, `1` suite, `4` schedules, `1` trigger); `Accept declaration` recorded acceptance with global-scope enforcement.
  - Declared templates, suites, and schedules displayed `.grain` / `Repo-declared` badges, read-only banners naming `acme/widgets` and the `.grain/...` path, hid `Edit`/`Delete`, and disabled `Pause`.
  - Workflow schedules (`type: "workflow"`) carried no `System` chip; `Run now` initially fired without confirmation or workflow-specific tooltip (Bug 7, fixed so `Run now` confirms that it starts a CI run of the declared workflow straight away while leaving next run unchanged, and discovered `.grain/triggers/*` rows link to `/next/repos/acme/widgets/advanced`).
- **Classic `/processes`:**
  - Confirmed workflow schedules showed `Workflow` (no `System` chip), `Run now` prompted `"It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged."`, and clicking trigger `docs-guard.yml` opened `/repos/acme/widgets/settings/advanced`.

## Bugs Found and Fixed in This Task

All seven defects found were in `/next/` (`ui-ng/`) and were fixed in this branch with unit and Playwright e2e coverage:

1. **Modal dialog errors rendered behind `inert` scrim + `Every N minutes` allowed `min="5"` (`ui-ng/src/app/processes/processes-page.component.ts`):**
   - **Repro:** Open `New schedule`, pick `Every N min` (`min="5"`), enter `5` or choose `Git ref push` with forbidden watched repo `grain/config-infra`, and click `Save schedule`. Server returns `400`, `store.actionError` renders behind `.scrim` inside `<section [attr.inert]="modalOpen() ? '' : null">`, and zero alert appears inside the open dialog.
   - **Fix:** Set `min="10"` on `Every N minutes` without coercing typed numbers to `15`, added `dialogError` rendered as `<p class="banner banner-error" role="alert" data-testid="proc-dialog-error">` inside all six modal dialogs, and cleared `dialogError` on open/close.
2. **Rail `New ▾` menu actions (`?new=1`, `?launch=1`, `?run=1`) failed to reopen after cancelling (`processes-page.component.ts`):**
   - **Repro:** Click rail `New ▾` → `New schedule`, click `Cancel` (URL remains `/next/processes/schedules?new=1`), then click `New ▾` → `New schedule` again; nothing opens.
   - **Fix:** Strip consumed one-shot query params (`new`, `launch`, `launchTemplate`, `run`, `runSuite`) with `replaceUrl: true` once handled and reset `handledDeepLinkKey` when empty.
3. **Item deep links (`/next/schedules/<id>`, `/next/templates/<id>`, `/next/suites/<id>`, `/next/processes/suites/runs/<id>`) fell through to `/next/inbox`, and `?kind=` / `?q=` were ignored (`app.routes.ts`, `processes-page.component.ts`):**
   - **Repro:** Navigate to `/next/schedules/sched-2` or `/next/processes/suites/runs/1`; router wildcard `**` redirects to `/next/inbox`.
   - **Fix:** Added item and suite-run routes/redirects in `app.routes.ts`, bound `itemId`, `runId`, `kind`, and `q` inputs in `ProcessesPageComponent`, and guarded nonexistent IDs (`/next/suites/runs/999`) with an error alert without trapping mobile detail view.
4. **Off-target `git_ref` and event-trigger schedules displayed sentinel `Next: 2027-11-16T...` (`processes.models.ts`, `processes-page.component.ts`):**
   - **Repro:** Inspect an off-target `git_ref` schedule (`watchedRepoOffTarget`); `Next:` renders the server's 1-year sentinel timestamp `2027-11-16T...`.
   - **Fix:** Added `formatScheduleNextRun(s)` returning `"suppressed"` for suppressed off-target/event schedules and `"on ref move"` / `"on workflow run"` for active event triggers on both list rows and the inspector.
5. **Base-less and 0-pass suite runs showed `"default"` instead of `"default branch"` and dangling `@ (pass 0)` (`processes-page.component.ts`):**
   - **Repro:** Inspect a suite with a base-less run (`base: ""`) and a 0-pass failed run (`pass: 0`); `Recent runs of this suite` renders `Run #3 · acme/widgets @ (pass 0)` and the run row/inspector render `@ default`.
   - **Fix:** Standardized empty `base` to `"default branch"` across the run row, run inspector, and recent-runs list, and omitted `(pass 0)` when `pass <= 0`.
6. **Selecting a template with an undefaulted required parameter logged a `400` on `GET /api/templates/{id}/composition` (`processes-page.component.ts`):**
   - **Repro:** Create a template with required field `pkg` and no default; selecting it fires `GET /api/templates/{id}/composition` without `field.pkg` and logs `400 Bad Request`. Also, renaming a template while a suite/schedule remained selected in another tab left cached `suiteComposition` / `scheduleComposition` stale until re-clicked.
   - **Fix:** Supplied `f.default || f.options?.[0] || '{{' + f.name + '}}'` during inspector preview and refreshed active `selectedSuiteId` / `selectedScheduleId` compositions on template/suite updates.
7. **Workflow schedule `Run now` lacked immediate-CI confirmation and `.grain/triggers/*` rows had no link to repository Advanced settings (`processes-page.component.ts`):**
   - **Repro:** Click `Run now` on a workflow schedule (`type: "workflow"`); it fires without explaining that it starts a CI run straight away. On `Declarations`, `.grain/triggers/docs-guard.yml` is plain text.
   - **Fix:** Added confirmation dialog and tooltip explaining immediate workflow CI execution on `type === "workflow"`, ensured workflow actor chip shows `workflow` (not `grain`/`System`), and linked discovered trigger paths to `['/repos', ...parts, 'advanced']`.

## Review Mandate Verification (`ui-design-book.design.md` §11)

- **Design review:** Zero hardcoded hex values added; reuses existing Material 3 surface/error tokens, `.tabular` alignment, and `g-confirm-dialog`. Desktop (`1440×900`) and phone (`390×844`) screenshots in light and dark (`screenshots/before` and `screenshots/after`) verified distinct themes, zero horizontal overflow (`scrollWidth === clientWidth`), and zero serious/critical axe-core violations.
- **Functional review:** Verified end-to-end in Playwright (`ui-ng/e2e/next-processes-walkthrough.spec.ts` and `ui-ng/e2e/next-full-system.spec.js`) across `desktop`, `phone`, and `dark` projects, plus Vitest unit tests (`processes-page.component.spec.ts`).
- **Adversarial review:** Caught and fixed mobile empty-inspector trap on nonexistent deep-linked IDs (`/next/suites/runs/999` and `/next/schedules/missing`), verified 3-segment custom-host repository paths in `repoAdvancedRoute`, and confirmed bundle budgets pass with zero warnings.
