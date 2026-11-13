# Processes — schedules, templates, suites, suite runs and triggers walkthrough — 2026-11-13 (task 4729)

- **Date:** 2026-11-13 (UTC), 02:39–03:30.
- **Task:** task 4729, branch `grain/098073e4/task-4729-walk-through-processes`, base `main`.
- **Commit tested:** `4ae6438fd` (`v0.0.0-20261113022244-4ae6438fdcd4`, as the app's footer reported it). The fixes below were then checked on a rebuild of this branch (`1529c9215`, footer `v0.0.0-20261113031710-1529c9215a55`) over a fresh store, reseeded the same way.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`.
  - Playwright headless Chromium from `ui/node_modules` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, driven by throwaway Node scripts outside the checkout (not committed). Viewports `1440×900` (light) and `390×844` (light and dark).
  - Built with `cd ui && npm ci && npm run build`, `cd ui-ng && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms`, with `TMPDIR` set to a throwaway directory (embedded SQLite store).
  - There was no `/home/debian/.gemini-api-key`, so no model was called and `LOOK=1` was unavailable. I shot the pages with `ui/e2e/screenshot.mjs` (`contact-sheet.png`, not committed), but my file reader shows PNGs only as bytes. So layout was judged from bounding boxes, `scrollWidth` vs `clientWidth` and computed text, not by eye.
- **How it was driven:** each script signs in through the Access code page (email and code typed, Continue clicked). After that it acts only through visible controls: clicks, typing, `Tab`, `Enter`, `Escape`, and pointer drags. Every page recorded uncaught page errors, console errors and warnings, failed requests, `/api/` responses ≥ 400 (with bodies), every native dialog (its text, and whether it was accepted), and every non-GET `/api/` request with its body. The REST API (bearer access code) was used only to read back what a gesture persisted, and for the setup steps below.
- **Console baseline:** each sign-in logs two `401`s for the unauthenticated first load. `GET /api/tasks` shows as `net::ERR_ABORTED` whenever `page.goto` or a reload cancels the long read, which is the browser's own cancellation. Every other console error was the browser's line for an intended 400 listed below. The one exception is the Angular TypeError that bug 1 below describes.

## Stubs and setup

The server, store, built frontends, browser and gestures were real throughout. What did not go through the UI:

1. **Demo seed flags.** `-schedules -github-sim -fake-ci -scheduled-workflows` provide the seeded schedules, templates and suites, a local bare `acme/widgets.git` (githubsim) and the fake CI runner. No agents run and no external GitHub calls are made.
2. **Repository declarations.** I cloned the githubsim bare repo (`GET /api/e2e/scheduled-workflows` names it) and pushed these files to `main`:
   - `.grain/templates/decl-bump.yml`, which declares:
     - `priority: 7`, `admission: durable`, `hosting: console`, `keep-running: true`;
     - `model: claude-opus-4-retired`, which no connection serves;
     - `sandbox: {cpus: 4, memory-mb: 16384, disk-gb: 100}` (new since the last walkthrough);
     - an `ecosystem` input and `push: [refs/tags/v*]`.
   - `.grain/templates/decl-pair.yml` and `.grain/suites/decl-sweep.yml` (until-clean, max 3);
   - `.grain/schedules/decl-bump-sched.yml` (daily 03:00) and `decl-docs.yml` (every 6h);
   - `.grain/workflows/nightly-ci.yml` (cron) and `.grain/schedules/nightly-ci-candidate.yml` (`workflow:`, `review: always`);
   - `.grain/triggers/docs-guard.yml`.

   I then ticked "Read acme/widgets's own declarations" on the repo's Advanced tab in the browser. My first trigger file used a `name:` key. The tab showed `Last read failed: reading acme/widgets's declarations: .grain/triggers/docs-guard.yml: yaml: unmarshal errors: line 1: field name not found in type repodecl.rawTrigger`, which is a clear, honest error. Once I fixed the file, the daemon logged `acme/widgets at b9b8142f11a6 declares 2 template(s), 1 suite(s), 4 schedule(s), 1 trigger(s)`. On the two later rebuilt servers this toggle was flipped with `PUT /api/repos/acme/widgets/declarations`.
3. **Capability policy.** One rule, `PUT /api/capability-policy`: `grain#source` is admitted only for `other/*` repositories, so it is refused for tasks on `acme/widgets`.
4. **Off-target `git_ref` schedule.** One schedule watching `offlist/upstream refs/heads/main` (`POST /api/schedules`). The UI rightly offers no way to create one; the task asks how an existing one looks.
5. **Execution boundary.** The demo runs no agents, so:
   - `ui/e2e/releasehelper complete-suite-pass` and `sync-suites` (built with `-tags grain_e2e`, run against the throwaway store) finished both passes of one suite run;
   - Python's `sqlite3` module made two writes on the throwaway store:
     - `UPDATE suite_run SET base = ''` on one run, since the run form always sends a branch;
     - an `INSERT` of a `failed` run with no tasks and `last_error` set. The orchestrator ends a run this way when its next pass cannot be filed (`pkg/orchestrator/suites.go`, missing template).
6. **Viewer role.** The `access` block of `/api/config` was rewritten in the browser (`page.route`) to `grain/viewer` with `templates:write` but not `suites:write`, as `ui/e2e/role-gated-actions.spec.js` does. Access-code sign-in is always full access.
7. **No model connection was added.** The demo's own gemini connection advertises `gemini-2.5-pro` and `gemini-2.5-flash`.

## Walkthrough and outcomes

### 1. Processes list, kind chips, search, sort, reorder, URLs

- **Rail count.** The rail's `processes` count read `14` on `/` and `15` once `/processes` had loaded its triggers. That lazy trigger read is intentional (`Sidebar.jsx`) and earlier runs noted it too. The header count agreed (`processes 15`).
- **Kind chips.** `all 15 · schedules 5 · workflow schedules 2 · templates 5 · suites 2 · triggers 1`, with `aria-pressed` following the selection. Templates then Suites gave `?kind=template,suite` (8 rows), and All returned to `/processes`.
- **Derived Last run / Next run.**
  - `Declared Nightly Bump` showed `11/13/2026, 3:00:00 AM`, taken from its daily declared schedule.
  - `Declared docs pass` showed `8:47:43 AM`, taken from its 6-hourly schedule.
  - `release-qualification` showed `11/14/2026, 12:00:00 AM`, taken from its seeded daily schedule.
  - `security-audit`, both suites, the system template and my new templates showed `on demand`.
  - The trigger showed `on change`.
- **Search.** `bump` narrowed the chips to `all 2 · schedules 1 · templates 1` as `?q=bump`. Adding Templates gave `?kind=template&q=bump`, which survived a reload with the search box still reading `bump`.
- **History.** Narrowing replaces the current history entry rather than pushing one. Back went to the previous page (`?kind=suite`), and Forward returned to `/processes?kind=template&q=bump` with the chips and search restored.
- **Sort.** The Sort panel offers 19 attributes. I picked each of Kind, Source, Type, Name, Target repo, Base branch, Template, Suite, Enabled, Cadence, Time zone, Auto-approve proposals, Auto-merge, Overlap, Requires approval, Next run, Last run, Last outcome and Created:
  - each wrote `?sort=<key>:asc` and reordered the rows;
  - Next run is the default and writes no `sort`.

  Removing the sort row gave `Custom order` (`?sort=custom`).
- **Custom order.**
  - Keyboard: focusing the 4th `Move …` handle and pressing Enter offered 13 `Move here` slots. Enter on the first slot moved `Declared Nightly Bump` to the top.
  - Pointer: dragging the 3rd handle onto row 2 moved that row.
  - The order survived a reload.
- **Old links.**
  - `/schedules`, `/templates` and `/suites` landed on `/processes?kind=schedule|template|suite`.
  - `/schedules/sched-1`, `/templates/template-1` and `/suites/suite-1` landed on `/processes/<kind>/<id>` with the pane open.
  - `/suites/runs/999` landed on `/processes` with the alert `no suite run "999"`.

### 2. New menu

- With All (or two kinds) selected, `+ New` opens a menu with `schedule`, `template`, `suite` and the hint "Workflow schedules and triggers are declared in a repository's .grain/ directory -- declarations."
- With `?kind=schedule`, `template` or `suite`, the button reads `+ new schedule|template|suite` and opens that form directly, with no menu.

### 3. Schedules

- **Task-filing schedule `walk-raw-10min`.**
  - Fires `a task` / `a suite`.
  - The repeat options are every N hours, every N minutes, daily, weekly, monthly, and "when a git ref moves".
  - `Every N minutes = 5` was refused natively ("Value must be greater than or equal to 10"); `10` saved.
  - Admission offers standard, bypass queue and durable. Hosting offers unattended and interactive console. Network offers deployment default, egress and none.
  - The model picker listed `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro` and `gemini-2.5-flash`, and typing `flash` narrowed it to one option.
  - The preview before saving read Model `gemini-2.5-flash`, Keep running on, Admission `bypass queue` and Hosting `interactive console`, all "from the schedule". Capabilities were `api › ci-read · repo: acme/widgets` "from the repo".
  - Saved, the row read `every 10m … 11/13/2026, 3:07:05 AM Enabled`, unchanged after a reload.
  - The API read back `keepRunning: true, admission: bypass, hosting: console, model: gemini-2.5-flash, recurrence {everyNMinutes: 10}`. Reopened, the preview still named `gemini-2.5-flash` and `interactive console`.
- **Add grant… menu.**
  - It groups Repository access and Grain API presets (each with the scopes it grants; `deployment-ops` and `self-upgrade` are marked `dangerous`).
  - `grain server › source` is marked "may be refused by the capability policy for agent tasks (settings → capabilities)".
  - The three `release server ›` methods are marked "ci only: not a grant for agent tasks".
  - The menu holds 70 `menuitemcheckbox`es.
- **Suite-running schedule.** I picked `Release Qualification Suite` from a two-item picker (`Declared sweep`, `Release Qualification Suite`).
  - The preview listed both templates "from the template" and Auto-merge/Approval "from the suite".
  - Add schedule with Base branch empty was stopped by the browser's own "Please fill out this field." The helper text says "required: a suite run stacks its tasks against one branch", but the label has no `*` (see Observations).
  - With `main` it saved, and the row read `daily at 09:00 suite: release qualification suite … Enabled`.
- **`git_ref`.**
  - Watching `grain/config-retired` was refused with `recurrence.gitRef.repo: grain/config-retired not found: only grain/config exists`, shown as an alert while the form stayed open.
  - Escape on that dirty form asked "You have unsaved changes here. Leave and discard them?"
- **Suppressed off-target schedule.** The row showed:
  - the `suppressed` chip;
  - `suppressed: watches offlist/upstream, which is not on this deployment's target repo list`;
  - `suppressed` in Next run.

  The pane opens with "This schedule is suppressed and is not polling its watched ref … Add that repository to targetRepos under Settings so grain polls it." Pause worked and persisted through a reload: the row then reads `on ref move | paused`.
- **Pause, resume and Run now.**
  - Pause and Resume each send one `PATCH {"enabled":false|true}` and close the pane.
  - The paused row reads `Held -- fires 11/13/2026, 3:07:05 AM if resumed by then | paused`.
  - The declared workflow schedule's Run now confirmed with "It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged." I cancelled it.
- **Delete.** A throwaway schedule confirmed with "Delete the schedule "walk-throwaway"? Tasks it already filed are not affected." Cancel kept the pane and the row. OK removed both, and they stayed gone after a reload.

### 4. Templates

- **Task priority.** The box refuses `1.5` ("…The two nearest valid values are 1 and 2."), `0` and `-2` ("Value must be greater than or equal to 1"). `1e3` is accepted and saved as `priority: 1000`.
- **Sandbox shape (new).** The vCPU, memory and disk boxes have no native min or max. `64` vCPUs, `64` MiB and `2000` GiB pass the browser, but Save is refused by the server with a clear alert while the form stays open:
  - `sandbox: cpus is 64, and may be at most 32`;
  - `sandbox: memory-mb is 64, and must be at least 128`;
  - `sandbox: disk-gb is 2000, and may be at most 1024`.

  `1.5` is refused natively.
- **`walk-full-template`** saved with:
  - reviewers `ana, bo`, priority `1000`, sandbox `{cpus 4, memoryMb 16384, diskGb 100}`;
  - Interactive, Keep running, Durable, Interactive console;
  - network egress (`open`), model `gemini-2.5-flash`, and the `self-debug` Grain API preset.

  Reopening showed every value as saved.
- **Policy verdict.** Adding `grain server › source` gave the chip a `may be refused` verdict.
- **Rename refresh.** I renamed the template to `…-renamed` and back. The open suite pane's Templates chips and its preview carried the new name.
- **New task from this template** opened `/new` on the template with `priority = 1000` carried across. The New task form's sandbox boxes are blank; the template's shape fills them when the task is filed.
- **Use…** on the interactive template:
  - showed "This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed.";
  - its preview listed Keep running on, Admission `durable`, Hosting `interactive console`, Task priority `1000` and `Sandbox 4 vCPU, 16384 MiB, 100 GiB disk`, all "from the template".
- **Dependency-aware delete.** Deleting the template while `walk-suite` used it asked "Delete the template "walk-full-template"? Schedules and suites using it must be repointed or deleted first." On OK it was refused with `template is used by 1 suite(s) ("walk-suite"); remove it from those first`, and the pane stayed open.
- **Phone footer.** At 390px, light and dark, `delete` sits on its own row and `new task from this template`, `cancel` and `save` share the next. Every button is 37px tall, none overlap, and pane `scrollWidth == clientWidth`. No chip extends past the pane's edge, in the template pane or in a schedule pane with grant chips.

### 5. Suites

- **Templates picker.** It is searchable: `declared` narrowed it to `Declared docs pass` and `Declared Nightly Bump`.
- **Inline template.** `+ new template` opened the New template form inside the suite pane. Saving `walk-inline-template` added it to the suite's Templates chips.
- **Run mode.** The options are "run until no repo changes or new tasks" and "run a fixed number of times". `number of times = 0` is refused natively, and `2` is accepted.
- **Saving `walk-suite`.** With require approval on and auto-merge off it saved as `run 2× 3 templates requires approval`. The API read back `mode: count, count: 2, requireApproval: true, autoMerge: false`.
- **Viewer without `suites:write`.** The pane's `save` and `delete` were disabled, and `+ new template` stayed live, since the role holds `templates:write`.

### 6. Suite runs

- **Starting runs.** I started one run from `walk-suite`'s `run…` (branch `main`) and one from the header's generic `+ run` (`Release Qualification Suite`, `release/1.0`). Each sent one `POST /api/suite-runs` and appeared under Suite runs without a reload:
  - `walk-suite active … main pass 1 · run 2× · each pass's tasks wait for approval`;
  - `Release Qualification Suite active … release/1.0 pass 1 · run 2×`.

  The Run dialog's Branch box is required, enforced natively.
- **Finished run.** After setup item 5, the Release Qualification run read `succeeded … release/1.0 pass 2 · run 2×`. Its pane grouped `pass 1` (tasks 25, 26) and `pass 2` (tasks 28, 29), each task `answered`.
- **Base-less run.** It shows `default branch` on its row and pane.
- **Status chips.** At 1440 and 390 every status chip is whole (`scrollWidth == clientWidth`) and inside its row.
- **Direct URLs.** `/processes/suites/runs/<id>` opens the pane for runs 1, 2 and 3.
- **Failed run with no tasks.** The classic UI said "first pass is on the way", and the Angular runs list crashed: bugs 1 and 2 below.

### 7. Repository-declared items

- **Banner.** The declared template, suite, schedule and workflow schedule panes each open with "This … is declared by acme/widgets in .grain/<dir>/<file>.yml, so it cannot be edited or deleted here. Change that file in the repository and grain picks it up within a couple of minutes."
- **Suites and schedules.** The suite's banner adds "It can still be run from the suites list". The schedules' adds "Run now still fires it once".
- **Locked controls.** Counting controls that match `:disabled` (the form is a disabled fieldset):
  - the template leaves only `← templates`, `new task from this template` and `close` live;
  - the suite leaves `← suites` and `close`;
  - the schedule leaves `← schedules`, `run now` and `close`.

  There is no `Add grant…`, and typing into Name changed nothing. No PATCH, PUT or DELETE went to a `repo:` id.
- **Declared sandbox shape.** The declared template's shape appears in its schedule's and suite's previews as `Sandbox 4 vCPU, 16384 MiB, 100 GiB disk`.
- **Workflow rows.** They read `workflow … declared by acme/widgets daily at 04:00 template: review the "nightly-ci" workflow's scheduled run`, with no `System` chip, even though the API marks them `system: true`. The `workflow:` schedule adds `reviews every run`.
- **Trigger.** The `docs-guard.yml` row opened `/repos/acme/widgets/settings/advanced`, which lists `Path triggers: .grain/triggers/docs-guard.yml`.

### 8. Cross-cutting

- **Keyboard.** Tab from the search box reaches `filter`, the sort button, `+ run`, the kind chips, then the first row link. Enter opens that row's pane. Escape closes it and returns focus to the same row link.
- **Overflow.** On `/processes`, `?kind=suite`, the run panes, the template pane and the schedule pane, at 1440 and 390, light and dark, `scrollWidth == clientWidth` throughout.
- **Errors.** There were no page errors on the classic UI and no 5xx anywhere. The only console errors were the browser's lines for the intended 400s above. The exception is the Angular TypeError in bug 1, which is gone on the fixed build.
- **Angular `/next/processes`.** Checked at 1440 light and 390 dark, the Suites view lists the declared suite with its banner, member templates and composition preview, and the Suite runs view lists every run once the fix is in.

## Bugs found

### 1. Fixed: the Angular Processes page crashed on a suite run that had filed no tasks, hiding every run after it

- **Reproduction:**
  1. Have a suite run whose server row has `pass: 0, tasks: null`. A run the orchestrator ends because its next pass could not be filed looks like this; here it was setup item 5's inserted row (`GET /api/suite-runs/3` returns `"tasks":null`).
  2. Open `/next/processes/suites` and press "Suite runs (2)".
  3. Only the failed run is drawn; the active run is missing. The console logs `ERROR TypeError: Cannot read properties of null (reading 'length')` five times.
- **Cause:** the row template read `sr.taskCount ?? sr.tasks.length`. The server sends a Go nil slice as JSON `null`, and the model typed `tasks` as never null.
- **Fix:**
  - The row reads `sr.taskCount ?? sr.tasks?.length ?? 0`, and `SuiteRun.tasks` is typed `SuiteRunTask[] | null` (`ui-ng/src/app/processes/`). Every other reader already used `run.tasks ?? []`.
  - The Pass tag is hidden when `pass` is 0. The detail line says "ended without filing a pass", or "no pass filed yet" for an active run, instead of "currently on pass 0".
- **Verified in Chromium:**
  - both runs are listed (`Failed … #3 … · 0 tasks` and `Active … #1 Pass 1 … · 2 tasks`), with no console errors;
  - run 3's detail reads `Run 2× · ended without filing a pass`.
- **Regression test:** "lists a suite run that has filed no tasks (tasks: null) beside the others, naming no pass" (`processes-page.component.spec.ts`). It fails without the fix. `npm run lint` and `ng build` pass.

### 2. Fixed: the classic UI promised a "first pass on the way" for a suite run that had already failed, and labelled it "pass 0"

- **Reproduction:** with the same run, open `/processes/suites/runs/3`.
  - The pane reads `failed … pass 1 could not be filed … tasks 0 | This run has filed no tasks yet -- its first pass is on the way.`
  - The Suite runs row reads `failed … pass 0 · run 2×`.
- **Fix:**
  - `SuiteRunPane` says "This run ended without filing any tasks." for a run that is no longer `active`.
  - The row drops `pass 0 ·`, so it reads `run 2×` (`ui/src/components/SuiteRunPane.jsx`, `ProcessesList.jsx`).
- **Regression tests:** "does not promise a pass for a run that ended without filing one" (`SuiteRunPane.test.jsx`) and "names no pass on a suite run row that has not filed one" (`ProcessesList.test.jsx`). Both fail without the fix; the two files pass (34 tests).

## Observations, not fixed here

- **Sandbox boxes have no native bounds.** A template's sandbox boxes carry no `min`/`max`, so out-of-range values reach the server, which refuses them clearly. Native bounds of 1–32, 128–131072 and 1–1024 would catch them sooner, but the server-side refusal is correct and visible.
- **Suite schedule's Base branch has no `*`.** The field is `required` for a suite schedule and its helper text says so, but its label shows no `*`, unlike `every n hours *`.
- **Suite runs and the null task list.** The server reports a suite run with no tasks as `tasks: null`. Both UIs now tolerate it. Emitting `[]` server-side would protect any other client, but I left the API shape alone.
- **Pause closes the pane.** Pause and Resume close the schedule pane, so trying Run now right after a pause means reopening it. That looks deliberate (the list row shows the new state) but is worth knowing.
