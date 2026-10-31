# Recurring and Composed-Work Automation UI Walkthrough (2026-10-30)

Task 1903 (`grain/049f1e09/task-1903`). End-to-end walkthrough of
[Processes](/processes): schedules, workflow schedules, templates,
suites, suite runs and path triggers. It ran in a real headless Chromium
driven by Playwright against a real `grain demo` server. Every create,
edit, run, pause, delete, reorder and navigation gesture went through
visible UI controls. The API was used only to read back what a gesture
persisted, except where [Stubs](#stubs) says otherwise.

Two defects were found and fixed on this branch (see
[Bugs found and fixed](#bugs-found-and-fixed)):

1. A repository-declared workflow schedule's pane told the reader they
   could change its cadence here, while Pause and Save were disabled.
2. The "Next run" sort used past-date wording.

## Environment

| Item | Value |
| --- | --- |
| Commit walked | `e1b148a4cc5985b26fadf2c9d5fc5fad428bbfcd` (`main`, base of this branch); fixes re-walked on this branch's head |
| Go / Node / npm | `go1.26.10 linux/amd64` / `v20.19.0` / `10.8.2`, Linux |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC |
| Build | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/grain ./cmd/grain` |
| Server | `TMPDIR=<throwaway> /var/tmp/grain demo -addr 127.0.0.1:18080 -open=false -github-sim -fake-ci -scheduled-workflows -sensitive-hold -reconcile 500ms -schedules -access-code-file <file>`; a second one on `:18081`, built from the fixed tree, to re-walk the fixes |
| Sign-in | The "Enter access code" page, typed in the browser |
| Viewports | 1440×1400 / 1440×2000 desktop for the walk; 1440×900 and 390×844, light and dark, for the fixes |
| Gemini key | `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling (and no `LOOK=1`) was used. The demo's connection advertises `gemini-2.5-pro` (the deployment default) and `gemini-2.5-flash`. |

The walkthrough scripts were throwaway files under `/var/tmp/walk/s` and
are not committed. Neither are the screenshots.

## Stubs

- **Agent execution boundary.** `grain demo` runs no agents. A throwaway Go
  helper (`/var/tmp/walkseed`) recorded only what an agent run leaves
  behind. It was built from a scratch `cmd/walkseed`, which was deleted
  before committing. It opens the demo's SQLite store with
  `sqlite.Open(sqlite.DefaultConfig(dir))` / `model.New`. Its verbs:
  - `complete-clean <task>`: `Observation.CompletedAt`.
  - `complete-changed <task> <pr>`: a `LinkFixes` link to `acme/widgets#<pr>`, then `CompletedAt`.
  - `close <task>`: `Observation.ClosedAt` with no completion.
  - `proposed-by <task> <parent>`: a `LinkProposedBy` link. It was set on a follow-up task created with `POST /api/tasks`, which is what an agent's `propose_task` produces.

  Every suite-run transition was made by the real reconciler
  (`-reconcile 500ms`): pass 1 → pass 2 → succeeded, and pass 1 → failed.
- **Simulated GitHub and fake CI** (`-github-sim -fake-ci -scheduled-workflows -sensitive-hold`).
  Repository declarations were pushed with plain `git` to the simulated
  `acme/widgets` bare repository that `GET /api/e2e/scheduled-workflows`
  names. The files were:
  - `.grain/templates/walk-declared.yml`;
  - `.grain/suites/walk-sweep.yml` (count 2, require-approval);
  - `.grain/schedules/walk-nightly.yml` (template, daily 04:00);
  - `.grain/schedules/walk-sweep.yml` (suite, Fridays 05:00);
  - `.grain/workflows/nightly.yml` (`on: schedule` cron `11 2 * * *`);
  - `.grain/triggers/docs.yml` (`on: merge`, `paths: [docs/**]`).

  The orchestrator re-reads declarations every two minutes
  (`declarationPollInterval`). Without `-sensitive-hold`, the demo has
  declarations off for acme/widgets and removes them on start.
- **One API-seeded row.** `walk-unserved`, a template whose `model` is
  `claude-opus-5-5`, was created with `POST /api/templates`, because the
  UI only offers models a connection serves. It exists to show how such
  a model is presented.

## Steps and outcomes

### 1. Processes list, narrowing, sort, reorder, old links

1. The rail's **Processes** entry (count 6 on the fresh store) opened
   `/processes` with chips **All 6 · Schedules 2 · Workflow schedules 0 ·
   Templates 3 · Suites 1 · Triggers 0**. Once the declarations were
   read, the chips read **All 18 · Schedules 7 · Workflow schedules 1 ·
   Templates 6 · Suites 3 · Triggers 1**. Each chip's count is over the
   search, so `?q=security` made it **All 1 · Templates 1**.
2. **Templates** wrote `?kind=template` and turned New into `+ New
   template`. Search wrote `&q=security`. Both survived reload. Inbox →
   Back returned to the same narrowed URL, and Forward went to the inbox
   again. **All** dropped `kind=` and kept `q=`. The kind chips work
   from the keyboard (Enter narrows, Space toggles back).
3. **Sort**, through the `Attribute for sort 1` picker:
   - Last run → `sort=last:asc`;
   - Name → `sort=name:asc`;
   - Kind → `sort=kind:asc` (schedules, templates, suites);
   - Next run → the default, so no `sort=`.

   The picker offers all 19 process attributes. The direction picker for
   Next run read "Oldest first / Newest first". That is the second fix
   below, and it now reads "Soonest first / Latest first".
4. **Custom order and reorder.** `?sort=custom` shows the page's own
   order. Keyboard: focus `Move walk-inline`, press Enter, then Enter on
   "Move here, above docs.yml" (17 slots offered). The row moved to the
   top and stayed there after reload. Pointer: dragging `Move Weekly
   Dependency & Vulnerability Audit` onto the first row moved it to the
   top, and that survived reload too.
5. **Derived runs.** A template fired by a schedule shows that
   schedule's next run. After **Run now** it also shows the last run,
   with no reload. A suite shows the next run of its suite schedule, and
   its last run is its newest run. Rows nothing fires read "on demand";
   the trigger reads "on change". Per `docs/design/settings-panel.md`
   §5.1, a template counts only schedules whose `templateId` is that
   template. So `walk-inline`, which reaches a schedule only through a
   suite, stays "on demand". That is by design.
6. **Old links.**
   - `/schedules`, `/templates` and `/suites` landed on
     `/processes?kind=schedule|template|suite`.
   - `/templates/template-2`, `/suites/suite-1` and `/schedules/sched-1`
     opened those items under `/processes/…`.
   - `/suites/runs/2` landed on `/processes/suites/runs/2`.
   - Links naming a missing item fell back to `/processes` and said
     so: `/templates/security-audit` (a name, not an id) gave `no
     template "security-audit"`, `/processes/suites/runs/77` gave `no
     suite run "77"`, and `/processes/schedules/nope` gave `no schedule
     "nope"`.

### 2. New menu

`+ New` offered **Schedule · Template · Suite** and the hint "Workflow
schedules and triggers are declared in a repository's .grain/ directory --
declarations". The hint links to
`https://github.com/ai-on-gke/grain/blob/main/docs/declarations.md`. With
the chips narrowed to Templates, Schedules or Suites, the button read
`+ New template` / `+ New schedule` / `+ New suite` and opened that form
directly.

### 3. Schedules

1. **Raw task schedule**: title `Walk raw {{date}}`, base `main`, model
   `gemini-2.5-flash`, overlap **Start it anyway**, network **None**,
   **Every N minutes**.
   - The model picker offered only "Deployment default (gemini-2.5-pro)",
     `gemini-2.5-pro` and `gemini-2.5-flash`.
   - The browser refused 5 ("Value must be greater than or equal to 10";
     helper text "At least 10; counted from the last firing") and 10.5
     ("…The two nearest valid values are 10 and 11"). 10 saved.
   - The preview credited every row to the schedule as fields changed.
   - The row read `every 10m · can overlap`, with the next run ten
     minutes out.
2. **Run now** asked `Run "Walk raw {{date}}" now? It starts the task …
   the schedule's own next run is unchanged.` The row's last run filled
   in with no reload. Task 22 carried model `gemini-2.5-flash`, network
   `none` and base `main`. `{{date}}` is not a placeholder grain knows,
   so it stayed literal.
3. **Pause** (no confirmation, since it is reversible) closed the pane.
   The row read "Held -- fires … if resumed by then · paused" and kept
   it through reload. **Resume** restored it.
4. **Edit**: reopening showed every saved value. Changing the title and
   switching to **Daily** 09:30 saved as `daily at 09:30`, with the next
   run 10/31 09:30, and survived reload.
5. **Template schedule** (`walk-full`, every 2 h). The form said "Fires
   against acme/widgets on main -- the selected template is bound to that
   repo, so this schedule does not choose one". The preview credited
   every row to the template. The template's row picked up the next run
   with no reload.
6. **Suite schedule** (Fires **A suite**, `Walk suite`). An empty base was
   refused ("required: a suite run stacks its tasks against one branch").
   With `main` and **Monthly** (day 1, 09:00) it saved as `monthly on day
   1 at 09:00 · suite: walk suite`, and the suite's row showed 11/1 09:00
   with no reload.
7. **Run now on a suite schedule** started a run carrying a **scheduled**
   chip. The chip is a link to `/processes/schedules/sched-6` and opened
   that schedule. Deleting the schedule turned the suite's next run back
   to "on demand" and left the run in place.
8. **Grants.** The Add grant… menu lists the Grain API presets (task-admin,
   ci-read, dispatch-control, deployment-ops, release-manager,
   self-debug, …), each with what it grants, before the scopes. Picking
   `ci-read` saved as `api:ci-read`.

### 4. Templates

1. **Full template** `walk-full`:
   - title `Walk {{service}} check`, with input `service` (label "Which
     service?", default `api`);
   - bound to `acme/widgets @ main`, read-only `acme/shared`;
   - reviewers `ana,bo`, auto-merge, **Interactive**;
   - network **Egress**, nothing-needed **Leave for acknowledgement**;
   - model `gemini-2.5-pro`, grant `ci-read`;
   - priority high, requested-by `walker`, estimate 2.

   `GET /api/templates` showed every value. Reopening showed them in the
   form. A description edit survived reload.
2. **Use…** said "This template files a live chat: the agent can talk with
   you while it works, and the chat opens as soon as the task is filed."
   The preview credited every row to the template, including "Approval:
   approved when filed". Typing `billing` and leaving the field
   recomposed the preview to "Walk billing check". **Create task**
   opened `/tasks/23`.
3. **Unserved model.** `walk-unserved` showed `claude-opus-5-5 (no
   connection advertises it)` in its field and as the last picker
   option, after the served models.
4. **Dependency-aware delete.** Delete asked `Delete the template
   "walk-full"? Schedules and suites using it must be repointed or deleted
   first.` Dismissing it left the pane open. Accepting it showed the
   server's refusal (HTTP 400): "template is used by 1 schedule(s) ("Walk
   api check") and 1 suite(s) ("Walk suite"); repoint or delete the
   schedules and remove it from the suites first". After the schedule
   and suite were deleted, it deleted cleanly.

### 5. Suites

`Walk suite` was created from `+ New suite` with `walk-full`, plus a
template created inline from the form's `+ New template` (`walk-inline`,
which came back selected).

- Run modes: "Run until no repo changes or new tasks" (Max passes, 5 by
  default) and "Run a fixed number of times" (Number of times; 0 and 101
  refused by the browser).
- It saved as fixed ×3 with **Require approval** and **Auto-merge**. The
  row read `run 3× · 2 templates · requires approval`.

### 6. Suite runs

1. **From the suite row** (`Walk suite` → Run…). The dialog listed "3
   passes of these tasks" with each value's source. The Branch field is
   required and the browser refused an empty one. With `main` it started
   run 1.
2. **From the generic `+ Run`**, in the page header: `Release
   Qualification Suite`, branch `main`, started run 2. The Suite runs list
   showed both runs as `active · pass 1`.
3. **Failed.** Closing task 24 without completing it failed run 1. The
   list and `/processes/suites/runs/1` read "failed · pass 1 had a task
   that failed, or closed without completing".
4. **Running → succeeded.** Run 2's pass 1 completed (26 with a PR link,
   27 clean), and follow-up 28 was proposed by 26. The reconciler then
   filed pass 2 (29, 30). `/processes/suites/runs/2` showed "Pass 1 · 3"
   with the follow-up nested under its parent, and "Pass 2 · 2".
   Completing pass 2 turned the open page to **succeeded** with no
   reload.
5. Clicking the follow-up opened `/tasks/28`, and Back returned to the
   run page.

### 7. Repository-declared rows

1. Every declared row carried a "Declared by acme/widgets" chip whose
   title names the file. That covers the template, suite, both
   schedules, the workflow schedule (`.grain/workflows/nightly.yml`) and
   the trigger.
2. Each declared pane said "This … is declared by acme/widgets in
   `<file>`, so it cannot be edited or deleted here. Change that file in
   the repository and grain picks it up within a couple of minutes."
   - The suite's pane adds "It can still be run from the suites list";
     the schedules' panes add "Run now still fires it once."
   - Every input was `:disabled`, and so were Delete, Save and (on
     schedules) Pause. Run now stayed enabled.
   - No PATCH, PUT or DELETE was sent. Only the composition-preview
     POSTs went out.
3. **The declared workflow schedule's pane contradicted itself.** That is
   the first fix below.
4. The trigger row `docs.yml` opened
   `/repos/acme/widgets/settings/advanced`, which read "Read at
   09db7a9af2ba: 1 template(s), 1 suite(s), 3 schedule(s), 1 trigger(s)"
   and "Path triggers: .grain/triggers/docs.yml".

### 8. Keyboard, console, cleanup

- Tab from `+ New` reaches Search processes…, Filter, the sort button,
  `+ Run`, the top-bar buttons, each kind chip, then the rows. Enter on a
  row's name link opens it, and Escape closes it back to `/processes`.
- The browser console showed one error: the 401 the sign-in page itself
  answers before the access code is entered. Network failures were the
  expected 400 on the refused template delete and `net::ERR_ABORTED`
  polls cut short by navigation.
- Every throwaway schedule, suite and template was deleted through its
  pane's Delete, each after a confirmation that names it and what is
  unaffected, and none was there after reload. Suite runs and filed
  tasks remain, as the confirmations say.

### 9. Existing specs

`npx playwright test` with `processes-narrowing`, `processes-walkthrough`,
`recurring-composed-walkthrough`, `processes_workflows`,
`workflow-schedules-candidate-walkthrough` and `processes-phone-layout`:
39 passed on the fixed tree.

## Bugs found and fixed

### 1. A declared workflow schedule said its cadence could be changed here

- **Reproduction.** Run `grain demo -github-sim -fake-ci
  -scheduled-workflows -sensitive-hold` and push
  `.grain/workflows/nightly.yml` with `on: schedule: [{cron: "11 2 * *
  *"}]` to the simulated acme/widgets. After the next declaration read,
  open `/processes/schedules/repo:schedule:acme:widgets:workflow.nightly`.
  The pane first says it "cannot be edited or deleted here". The
  built-in-job note under it then ends "You can change how often it
  runs, what it does when a firing overlaps the last one, and whether it
  runs at all", while Pause and Save are disabled.
- **Cause.** `ScheduleOverlay.jsx` printed the note for every
  `schedule.system` row. A repository's workflow schedule is also a
  system row, since it fires `system:workflow-review:<workflow>`, so it
  got wording meant only for grain's own undeclared schedules.
- **Fix.** When the schedule is declared, the note ends "How often it
  runs, what it does when a firing overlaps the last one, and whether it
  runs at all are set in `<declared file>`." grain's own schedules keep
  the old sentence.
- **Coverage.**
  - `ProcessesList.test.jsx`, "tells a declared workflow schedule's
    reader where its cadence is changed…": the declared row's note,
    Pause disabled, and the undeclared system row's note unchanged. It
    fails on `main`.
  - `workflow-schedules-candidate-walkthrough.spec.js` test 1 opens the
    declared candidate schedule in the browser and checks the same.

### 2. "Next run" sorted "Oldest first / Newest first"

- **Reproduction.** On `/processes`, the sort button reads "Next run ·
  Oldest first", and its direction options are "Oldest first / Newest
  first". A next run is in the future, so "newest" meant the run
  furthest off. The 2026-10-29 report noted this as a non-defect.
- **Fix.** `directionLabels` (`taskFilters.js`) words a date attribute
  flagged `future` (only Processes' `next`) as "Soonest first / Latest
  first". Last run and every task date keep "Oldest/Newest".
- **Coverage.**
  - `processFilters.test.js`: the next-run and last-run labels.
  - `processes-narrowing.spec.js` now picks "Latest first".

### Rendering

`ui/e2e/screenshot.mjs` was run against both servers for
`/processes/schedules/repo:schedule:acme:widgets:workflow.nightly` and
`/processes`, at desktop and phone widths in light and dark (before: the
`main` build; after: this branch). Bounding boxes were checked at
1440×900 and 390×844 in both themes:

- the note lies inside the pane (704px of 1208px; 357px of 390px);
- the document has no horizontal overflow;
- the note uses the theme's secondary text colour;
- the sort button, reading "next run soonest first", fits inside the
  viewport (x 817–971 of 1440; 16–171 of 390).

## Notes, not defects

- **A failed run's base chip is hidden.** In the Suite runs list at
  1440px, a failed run's row hides its `main` chip
  (`.chip-overflow`). The failure reason and the long mode hint take the
  width, and ChipStrip drops the last chip. That is the documented
  degradation (`style.css`, "what falls out of the width disappears
  whole"), and the run's page still shows the branch.
- **The scheduled chip names an id.** Its accessible name is "Scheduled
  by sched-6", not the schedule's title, and it keeps linking to the
  schedule after that is deleted. App then reports `no schedule
  "sched-6"`.
- **A paused schedule's pane has no paused notice.** Only the row says
  it is paused; the pane's only sign is the Resume button.
- **A schedule row omits its branch.** A raw schedule with base `main`
  shows `acme/widgets` on its row, while a bound template's row shows
  `acme/widgets @ main`.
