# Recurring and Composed-Work Automation UI Walkthrough (2026-11-01)

Task 2236 (`grain/049f1e09/task-2236`). An end-to-end walkthrough of
[Processes](/processes): schedules, workflow schedules, templates, suites,
suite runs and path triggers. It ran in a real headless Chromium driven by
Playwright against a real `grain demo` server. Every create, edit, run,
pause, delete, reorder and navigation gesture went through visible UI
controls. The API was used only to read back what a gesture persisted,
except where [Stubs](#stubs) says otherwise.

One defect was found and fixed on this branch (see
[Bug found and fixed](#bug-found-and-fixed)): a template's numeric task
priority could not be seen or changed in the template form, and no
composition preview mentioned it.

## Environment

| Item | Value |
| --- | --- |
| Commit walked | `86eff5045d17310ea2af176437a320a348521521` (`main`, "Merge pull request #340", the base of this branch); the fix was re-walked on this branch's head |
| Date | 2026-11-01, 04:44–05:20 UTC |
| Go / Node / npm / Playwright | `go1.26.10 linux/amd64` / `v20.19.0` / `10.8.2` / `1.62.1`, Linux |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, chromium_headless_shell-1234), timezone UTC |
| Build | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/grain ./cmd/grain` |
| Server | `TMPDIR=<throwaway> /var/tmp/grain demo -addr 127.0.0.1:18080 -open=false -github-sim -fake-ci -scheduled-workflows -sensitive-hold -reconcile 500ms -schedules -access-code-file <file>`. A second demo on `:18081`, built from the fixed tree, was used to render the fix. |
| Sign-in | The "Enter access code" page, typed in the browser |
| Viewports | 1440×1400 desktop for the walk; 1440×900 and 390×844 (`isMobile`, `hasTouch`), light and dark, for the fix |
| Gemini key | `/home/debian/.gemini-api-key` is absent in this sandbox, so no Gemini-backed tooling (and no `LOOK=1`) was used. The demo's connection advertises `gemini-2.5-pro` (the deployment default) and `gemini-2.5-flash`. |

Every script recorded page errors, console errors, HTTP ≥ 400 responses,
failed requests and every non-GET `/api` call. The scripts were throwaway
files under `/var/tmp/walk/s` and are not committed. Neither are the
screenshots.

## Stubs

- **Agent execution boundary.** `grain demo` runs no agents. A throwaway Go
  helper (`/var/tmp/walkseed`) recorded only what an agent run leaves
  behind. It was built from a scratch `cmd/walkseed`, which was deleted
  before committing. It opens the demo's SQLite store with
  `sqlite.Open(sqlite.DefaultConfig(dir))` and `model.New`. Its verbs:
  - `complete-clean <task>`: sets `Observation.CompletedAt`.
  - `complete-changed <task> <pr>`: adds a `LinkFixes` link to
    `acme/widgets#<pr>`, then sets `CompletedAt`.
  - `close <task>`: sets `Observation.ClosedAt` with no completion.
  - `proposed-by <task> <parent>`: adds a `LinkProposedBy` link. It was
    set on follow-up task 29, created with `POST /api/tasks`, which is what
    an agent's `propose_task` produces.

  Every suite-run transition was made by the real reconciler
  (`-reconcile 500ms`): pass 1 → pass 2 → succeeded, and pass 1 → failed.
- **Simulated GitHub and fake CI** (`-github-sim -fake-ci
  -scheduled-workflows -sensitive-hold`). Repository declarations were
  pushed with plain `git` to the simulated `acme/widgets` bare repository
  that the server log names. The files were:
  - `.grain/templates/walk-declared.yml`;
  - `.grain/suites/walk-sweep.yml` (count 2, require-approval);
  - `.grain/schedules/walk-nightly.yml` (template, daily 04:00);
  - `.grain/schedules/walk-sweep.yml` (suite, Fridays 05:00);
  - `.grain/workflows/nightly.yml` (`on: schedule` cron `11 2 * * *`);
  - `.grain/triggers/docs.yml` (`on: merge`, `paths: [docs/**]`).

  Within the two-minute declaration poll these replaced the demo's own
  seeded declarations (`nightly-maintenance`, `payments.yml`, `Smoke
  Verification Suite`), because the repository's files are the source of
  truth.
- **Two API-seeded templates**, both for presentation the UI cannot
  produce:
  - `walk-unserved` (model `claude-opus-5-5`), because the UI only offers
    models a connection serves;
  - `walk-prio` (`priority: 9`), because before the fix the form had no
    priority field. That is the bug below.
- **Playwright route stub**, removed after the check: `**/api/suite-runs*`
  answered `500 {"error":"synthetic suite-runs 500"}`.

## Steps and outcomes

### 1. Processes list, narrowing, history, old links

1. The rail's **Processes** entry read 9 on a cold load of `/inbox` and 10
   once Processes had opened. The chips read **All 10 · Schedules 3 ·
   Workflow schedules 0 · Templates 4 · Suites 2 · Triggers 1**. Once the
   declarations were read, they read **All 12 · Schedules 4 · Workflow
   schedules 1 · Templates 4 · Suites 2 · Triggers 1**. See
   [Notes](#notes-not-defects) on the cold count.
2. The kind chips are a multi-select:
   - Schedules wrote `?kind=schedule`;
   - adding Workflow schedules wrote `?kind=schedule,workflow`, and so on;
   - selecting all five collapsed back to `/processes`.

   Search `release` wrote `?q=release` and narrowed each chip's count
   (**All 3 · Schedules 1 · Templates 1 · Suites 1**). With Templates it
   became `?kind=template&q=release`. Reload kept the URL, the search box
   value and the counts. `/inbox` → Back returned to the same URL, and
   Forward went to `/inbox` again.
3. Keyboard: Enter on a focused chip narrowed to `?kind=suite`, and Space
   toggled it back to `/processes`.
4. Old links:
   - `/schedules`, `/templates` and `/suites` landed on
     `/processes?kind=schedule|template|suite`.
   - `/suites/suite-1`, `/templates/template-1` and `/schedules/sched-1`
     opened those items' editors under `/processes/…`.
   - Missing items fell back to `/processes` with an alert:
     `/suites/runs/1` before any run existed (`no suite run "1"`),
     `/templates/nope` (`no template "nope"`) and
     `/processes/suites/runs/99` (`no suite run "99"`).
5. `?q=zzzznothing` read "No processes match your search." with every chip
   at 0. The Suite runs list below the rows is not narrowed by search.

### 2. New menu

`+ New` opened **Schedule · Template · Suite** and the hint "Workflow
schedules and triggers are declared in a repository's .grain/ directory --
declarations". The hint links to
`https://github.com/ai-on-gke/grain/blob/main/docs/declarations.md`.

- Narrowed to Schedules, Templates or Suites alone, the button read `+ New
  schedule` / `+ New template` / `+ New suite` and opened that form
  directly.
- Narrowed to Triggers or to Workflow schedules (neither can be created
  here), it stayed `+ New` and opened the menu.

### 3. Schedules

1. **Raw task schedule** (`Walk raw {{date}}`, sched-3): base `main`,
   model `gemini-2.5-flash`, reads `acme/docs`, network **None**, overlap
   **Start it anyway**, **Every N minutes**.
   - The model picker offered only "Deployment default (gemini-2.5-pro)",
     `gemini-2.5-pro` and `gemini-2.5-flash`.
   - The minutes field refused 5 ("Value must be greater than or equal to
     10"; helper "At least 10; counted from the last firing") and 10.5
     ("…The two nearest valid values are 10 and 11"). 10 saved.
   - The live preview credited every row to the schedule.
   - The row read `every 10m · can overlap`, with the next run ten minutes
     after creation.
   - `{{date}}` is not a placeholder grain expands. The filed tasks are
     titled literally, as documented.
2. **Edit.** Adding `acme/docs` as a write target was refused by the
   server with a clear alert: "acme/docs is not on this deployment's target
   repo list … an operator widens targetRepos under Settings." Navigating
   away from the unsaved edit raised the browser's leave-page prompt. A
   valid edit added the **Grain API presets → ci-read** grant
   (`api:ci-read`) and saved; the API read it back.
   - The grant picker stays open after a pick, to ask what the grant
     applies to, and Escape there also closes the pane.
3. **Pause / Resume / Run now.** Each closes the pane, as
   `ScheduleOverlay.jsx` documents.
   - Pause sent `PATCH {"enabled":false}`. The row then read "Held --
     fires 11/1/2026, 4:58:21 AM if resumed by then · paused", before and
     after reload.
   - Resume sent `{"enabled":true}`.
   - Run now asked `Run "Walk raw {{date}}" now? It starts the task "Walk
     raw {{date}}" straight away; the schedule's own next run is
     unchanged.` The row's last run filled in with no reload and kept its
     next run. Reload showed the same. The scheduler (`-schedules`) later
     fired it on its own at the ten-minute mark.
4. **Suite-running schedule.** "A suite" → `Release Qualification Suite`,
   base `main`, weekly.
   - Submitting it empty, the browser refused the base field.
   - The preview read "2 passes of these tasks", each row credited to the
     template, suite or deployment.
   - The row read `mondays at 09:00 · suite: release qualification suite`.
     The **suite's** row then showed next run 11/2/2026 9:00 AM, with no
     reload.
5. **Template-bound schedule.** Picking `security-audit` reduced the form
   to template, model, repeat, time zone and overlap. The template option
   list was "none -- fill in the fields below", `walk-declared`,
   `security-audit` and `release-qualification`. Model `gemini-2.5-flash`
   was credited "from the schedule"; every other row came "from the
   template". After **Run now**, the confirmation named the template, and
   the **template's** row showed last run 4:52:57 AM and next run 9:00 AM
   with no reload.

### 4. Templates

1. **Fully populated template** (`walk-full`):
   - name, title, description, repo `acme/widgets`, base `main`;
   - reads `acme/docs`, reviewer `alice`;
   - **Auto-merge** and **Interactive** on;
   - network **Egress**, model `gemini-2.5-flash` (only served models
     were offered), legacy capability `gcp key`;
   - custom fields priority high, requested-by, due date and estimate.

   Pressing Enter in the plain comma-separated Reviewers box submitted the
   form, which is standard HTML, and the rest was filled in as an edit. The
   PATCH persisted every value. Reopening showed each one back.
2. **Use… on the interactive template.** The dialog showed "This template
   files a live chat: the agent can talk with you while it works, and the
   chat opens as soon as the task is filed." in place of the Approved box,
   and a preview with every row "from the template". **Create task**
   opened `/tasks/24`, which the API reports as `interactive: true` with
   the template's model, network, reads and capability.
3. **Unserved model.** `walk-unserved` showed `claude-opus-5-5 (no
   connection advertises it)` in its field and as the last picker option,
   after the served models.
4. **Numeric priority.** `walk-prio` (priority 9) filed priority-9 tasks
   from Use… (task 33) and from a bound schedule's Run now (task 34).
   However, neither its form nor any preview said so. This is the bug
   below.
5. **Dependency-aware delete.** A schedule bound to `walk-full` was created
   from the form, and `Walk suite` (§5) already used it. Delete asked
   `Delete the template "walk-full"? Schedules and suites using it must be
   repointed or deleted first.`
   - Dismissing left the pane open.
   - Accepting showed the server's refusal (HTTP 400): "template is used
     by 1 schedule(s) ("Walk full template check") and 1 suite(s) ("Walk
     suite"); repoint or delete the schedules and remove it from the suites
     first".
   - After the schedule and suite were deleted, it deleted cleanly.

### 5. Suites

1. **Validation and default mode.** `+ New suite` with nothing filled in
   was refused by the browser on the name. Run mode defaulted to "Run until
   no repo changes or new tasks" with Max passes 5.
2. **Templates, including one made inline.** `walk-full` was picked, and
   the form's `+ New template` opened a nested template form.
   `walk-inline` was saved from it and came back selected, with the suite's
   name kept.
3. **Fixed count.** "Run a fixed number of times" refused 0 ("…greater
   than or equal to 1") and 101 ("…less than or equal to 100").
4. **Save.** Fixed ×3 with **Require approval** and Auto-merge on saved as
   `run 3× · 2 templates · requires approval`.
5. **Edit.** Unchecking Auto-merge sent a PATCH carrying the revision.

### 6. Suite runs

1. **From the suite row.** `Walk suite` → **Run…** showed "3 passes of
   these tasks" with each value's source. An empty branch was refused
   ("Please fill out this field"). With `main` it started run 1.
2. **From the generic `+ Run`.** `Release Qualification Suite`, branch
   `main`, started run 2. The suite options were all three suites,
   including the declared `walk sweep`.
3. **Active.** The Suite runs list read `Release Qualification Suite ·
   active · pass 1 · run 2×` and `Walk suite · active · pass 1 · run 3× ·
   each pass's tasks wait for approval`. Run 1's tasks were `unapproved`.
4. **Failed.** Closing task 25 failed run 1. The list and
   `/processes/suites/runs/1` read "failed · pass 1 had a task that
   failed, or closed without completing".
5. **Running → succeeded.**
   - Task 27 completed with a PR link (shown "on merge queue") and 28
     completed clean, and follow-up 29 was proposed by 27.
   - The reconciler filed pass 2 (30, 31). `/processes/suites/runs/2`
     showed "Pass 1 · 3", with follow-up 29 nested under 27, and "Pass 2 ·
     2".
   - Completing pass 2 turned the list row to **succeeded**. The suite's
     own row then showed last run 4:56:30 AM (its newest run) and the next
     run of its schedule.
6. Clicking the follow-up on the run page opened `/tasks/29`, and Back
   returned to `/processes/suites/runs/2`. Run 1 still rendered, named
   `Walk suite`, after that suite was deleted.

### 7. Repository-declared rows

1. **Provenance chips.** Every declared row carried a "Declared by
   acme/widgets" chip whose title names the file: the template, suite,
   both schedules, the workflow schedule (`.grain/workflows/nightly.yml`)
   and the trigger (`.grain/triggers/docs.yml`).
2. **Read-only panes.** Each declared pane said "This … is declared by
   acme/widgets in `<file>`, so it cannot be edited or deleted here. Change
   that file in the repository and grain picks it up within a couple of
   minutes."
   - The suite's pane adds "It can still be run from the suites list";
     the schedules' panes add "Run now still fires it once."
   - Each pane had 0 enabled inputs, and Delete, Save and (on schedules)
     Pause were disabled. Run now stayed enabled.
   - No PATCH, PUT or DELETE was sent, only composition-preview POSTs.
3. **Trigger.** The `docs.yml` row opened
   `/repos/acme/widgets/settings/advanced`, which read "Read at
   d4673429d3e5: 1 template(s), 1 suite(s), 3 schedule(s), 1 trigger(s)"
   and "Path triggers: .grain/triggers/docs.yml".

### 8. Sort and custom order

1. **Sort.** The `Attribute for sort 1` picker offers all 19 process
   attributes:
   - Last run → `?sort=last:asc`, oldest first;
   - Name → `?sort=name:asc`;
   - Kind → `?sort=kind:asc`;
   - Next run → the default, so no `sort=`; the button reads "Next run ·
     Soonest first".

   Custom order is not an attribute: **Remove sort 1** wrote
   `?sort=custom`, and the button reads "Custom order".
2. **Keyboard reorder.** `Move walk-full` → Enter offered 18 "Move here,
   above …" slots, and Enter on the first moved it to the top. Focus stayed
   on `Move walk-full`, and the order survived reload.
3. **Pointer reorder.** Dragging `Move Walk suite` onto the `docs.yml`
   item row moved it to the top, and that survived reload. A first attempt
   that dropped on the column-header row did nothing, as it should.
4. **Where the order lives.** The order is kept per browser in
   localStorage (`grain.list-order.processes`, `listOrder.js`), so a fresh
   browser context does not see it and no request is sent. That is by
   design.

### 9. Keyboard, states, phone, console, cleanup

1. **Tab order.** Tab from `+ New` reaches Search processes…, Filter, the
   sort button, `+ Run`, the top-bar buttons, each kind chip, then the
   rows. Enter on a row's name link opened its editor, and Escape closed
   it back to `/processes`.
2. **Error state.** With `/api/suite-runs` stubbed to 500, the page showed
   the alert "synthetic suite-runs 500" and still listed its 15 rows.
3. **Phone.** At 390×844, `/processes` and `/processes/suites/runs/2`
   had no horizontal overflow, and the chip strip spans the width.
4. **Console and network.** The browser console showed only:
   - the 401 the sign-in page answers before the access code is entered;
   - the expected 400s, from the refused write target and the refused
     template delete;
   - `net::ERR_ABORTED` `/api/tasks` polls cut short by navigation.
5. **Cleanup.** Every throwaway schedule, suite and template was deleted
   through its pane's Delete, each after a confirmation naming it and what
   is unaffected:
   - "Tasks it already filed are not affected";
   - "Runs already started from it are unaffected";
   - "Suite runs it already started are not affected".

   The chips read All 12 again after reload. Suite runs and filed tasks
   remain, as the confirmations say.

### 10. Existing and new specs

The fixed tree passed 41 of 41 tests in `npx playwright test` over:
`template-priority` (new), `processes-narrowing`, `processes-walkthrough`,
`recurring-composed-walkthrough`, `processes_workflows`,
`schedule-every-n-minutes`, `built-in-template` and
`suite-run-dialog-order`. `go test ./pkg/ui/` passed, as did vitest
`CompositionPanel`, `UseTemplateOverlay`, `InteractiveTemplate`,
`TemplateFields` and `ProcessesList` (36 tests).

## Bug found and fixed

### A template's task priority was invisible and uneditable in the UI

- **Reproduction.**
  1. On `grain demo`, `POST /api/templates` with `{"name":"walk-prio",
     "title":"Walk priority nine","repo":"acme/widgets","priority":9}`
     (or declare `priority: 9` in `.grain/templates/*.yml`).
  2. Open `/processes/templates/<id>`: the form has no priority field.
  3. Open Use… on it: the preview lists Title through Approval, with no
     priority.
  4. Create the task: `GET /api/tasks/<id>` reports `priority: 9`. A
     schedule bound to the template files priority-9 tasks too.

  So a template that jumps every task it files up the queue looks exactly
  like any other, and only the API can change that priority.
- **Cause.** Numeric task priorities (`98eb06ac`) reached the new-task
  form, the task editor and the task detail pane, but not
  `TemplateOverlay.jsx`, and `ui.composedTaskFrom` never emitted a
  priority field.
- **Fix.**
  - The template form has a **Task priority** number field (min 1; blank
    means the default 5). On an edit, emptying it sends `priority: 0`,
    which the server already treats as a clear.
  - The composed-task preview adds a **Task priority** row, credited
    "from the template", whenever the resolved priority is not the
    default. So the Use…, schedule and suite previews all say it, and the
    common case adds no row, matching the task detail pane.
- **Coverage.**
  - `TestCompositionShowsATemplatesOwnPriority` (`pkg/ui`): the template
    and bound-schedule previews carry `priority: 9 (template)`, and a
    template with no priority has no row.
  - `ui/e2e/template-priority.spec.js`: sets 8 in the form, checks the
    POST, reopens, reads the Use… preview row, files the task (priority
    8), then empties the field and checks the PATCH and the cleared
    template. On `main` it fails at the missing field.
- **Rendering.** On a demo built from the fix, the template pane and the
  Use… dialog were shot at 1440×900 and 390×844, light and dark, with
  bounding boxes checked:
  - The field lines up with Reviewers (x 261, width 704 on desktop; x 17,
    width 357 on phone), and its helper text ends inside it.
  - The preview row ("Task priority | 9 | from the template") sits inside
    the dialog (x 444–996 of 420–1020; 16–374 of 0–390), directly under
    Approval.
  - Its label uses the theme's secondary text colour.
  - Neither page overflows horizontally.

## Notes, not defects

- **The rail's Processes count is one short on a cold load.** It reads 9
  until Processes has been opened once, then 10. Path triggers are fetched
  only on the way into Processes (`App.jsx`, "Fetched on the way into that
  page"), so a deep link elsewhere counts schedules, templates and suites
  but not triggers. It is a documented trade-off, not a fault.
- **The Suite runs list ignores search.** With `?q=` matching nothing, the
  page says "No processes match your search." above an unfiltered Suite
  runs list.
- **Two kinds of "priority".** The demo deployment declares a custom field
  named `priority` (low/medium/high), so the template form now shows both
  that field and the numeric **Task priority**. The labels differ, and the
  numeric one is the one dispatch orders by.
- **A closed task in a failed run shows no state chip.** On
  `/processes/suites/runs/1`, the closed task 25's row shows no state chip,
  while its unapproved sibling reads "unapproved". The run's own failure
  line says why the run failed.
