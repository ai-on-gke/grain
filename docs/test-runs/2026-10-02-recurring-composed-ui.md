# Recurring and composed work UI walkthrough: 2026-10-02

grain/task-1533. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server, with
bugs fixed on the same branch. It follows
[2026-10-01-recurring-composed-ui.md](2026-10-01-recurring-composed-ui.md).
Both follow-ups that run proposed have since landed: task rows open from
the keyboard, and a template-backed schedule's cached title has field
defaults filled in. This run re-checked both of them, and both held.

## Environment

| | |
| --- | --- |
| Commit tested | `cf9d6156c` (candidate tip, "Merge pull request #2609"), then this branch's own commits on top |
| Branch | `grain/task-1533` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium revision 1234 (headless shell) |
| Server | `grain demo -addr 127.0.0.1:18080 -open=false` built from this tree (`go build ./cmd/grain`, after `npm run build` into `pkg/ui/static`), throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, set by an init script (the demo's seeded user) |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

The exploration was done with throwaway Playwright scripts under `/tmp`
(not committed). They used `chromium.launch()`, logged every console
error and warning, every page error, every failed request and every HTTP
status ≥ 400, and answered every `confirm()` with accept or dismiss
depending on the step. The committed regression coverage is listed under
"Tests run".

Setup notes for the next run:

- `npx playwright install chromium` needs
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the
  sandbox's package proxy.
- Build the frontend before `go build`. The binary embeds whatever is in
  `pkg/ui/static`.
- Don't stop the demo with `pkill -f '/tmp/grain demo'` from a
  `run_command`. The pattern matches the command's own shell, which
  kills the rest of the chain with it, including a rebuild.

## Setup outside the area under test

The demo's target repo list holds only `acme/widgets`. A template or
schedule that writes to another repo is refused at save with "acme/docs
is not on this deployment's target repo list …". That refusal is
correct, and it was seen in this run too. To exercise writes and reads
at all, `POST /api/repos` added `acme/widgets-docs` and `acme/shared` to
the list (and, on the second server, `acme/aardvark`). This is the call
the Repos page's own "add" makes. It is configuration, not a stub.

## Stubs

Everything below went through the real server's own code paths. Nothing
mocked the API or the React UI.

1. **Suite-run progression (orchestrator boundary).** `grain demo` runs
   no orchestrator, so a run never leaves pass 1 or `active` by itself.
   A throwaway Go helper (`.tmp-suitestub/`, excluded and not committed)
   opened the demo's SQLite store and called the store methods the
   orchestrator's `SyncSuites` calls:
   - `Store.FireNextPass` put runs 2 and 3 on pass 2.
   - `Store.CompleteSuiteRun(3, SuiteRunFailed, "pass 2 never came back clean: task 22 failed its checks")`
     failed run 3.
   - `Store.CompleteSuiteRun(2, SuiteRunSucceeded, "")` completed run 2.
     The stub forced this after two passes of a "run 3×" suite. That is
     the stub's doing, not a fault in the UI.

   After each call the browser was reloaded to read the result.
2. **Agent follow-up proposal (agent boundary).** `POST /api/tasks
   {"proposedBy": "17", "status": "proposed"}` filed task 25. This is
   the request an agent's `propose_task` becomes, from suite task 17.
3. **Repository declarations.** These come from the demo's own seed
   (`cmd/grain/demo.go`, `store.SyncRepoDeclarations` for
   `acme/widgets`). No GitHub poll was involved.

## Steps and outcomes

Every create, edit or run was a click or keystroke in Chromium. Unless a
step says otherwise, outcomes were checked after a reload, by
deep-linking, and against the API's answer.

### Templates (`/templates`)

- **Sort and search.** Name (A–Z), Newest first, Oldest first and Custom
  order each reordered the rows. "walk" narrowed the list and the header
  counted the matches. "zzzz" showed "No templates match your search."
  Search and sort reset on reload. That is by design, since they are
  local state.
- **Create, fully populated.** The template `walk-tmpl-1533` was created
  with these fields:
  - title `Audit {{service}} for 1533` and a description using
    `{{service}}`
  - one input (`service`, label `Service`, options `api`/`web`, default
    `api`)
  - repo `acme/widgets` on `main`, write `acme/widgets-docs`, read
    `acme/shared`
  - auto-merge, network "None", capability `gcp-key`, priority `high`,
    requested-by `qa-1533`

  The POST answered 201 and every field was stored. (Before the target
  repo list was widened, the same form was refused with the allowlist
  message, kept every value, and stayed open. That is correct.)
- **Duplicate name.** Running the create a second time by accident filed
  a second `walk-tmpl-1533` (`template-4`). Template names are not
  unique, so pickers then show two identical names. See "Found and not
  fixed".
- **Reopen and edit.** Deep link `/templates/template-3`. Every field
  pre-filled, including the input's name, label, options and default.
  The title and priority were edited and saved (PATCH 200), and both
  held after a reload.
- **Delete with confirmation.** On the duplicate, dismissing the
  confirmation left the editor open. Accepting it deleted the row.
- **Use….** The default was `api`. Choosing `web` updated the preview
  ("Audit web …"). Create task opened `/tasks/13`, which was filed with
  the substituted title and description, the repo, base, reads, writes,
  capability, fields, network `none` and auto-merge.
- **Dependency-aware delete.** These ran at the end of the walkthrough:
  - While `sched-4` used the template, delete was refused with "template
    is used by 1 schedule(s); repoint or delete those first".
  - The seeded `release-qualification` was refused the same way.
  - After `sched-4` was deleted, the refusal became "template is used by
    1 suite(s); remove it from those first".
  - After the suite was deleted, it deleted cleanly. The inline template
    did too.
  - Only the three seeded templates remained.

### Schedules (`/schedules`)

- **Options.** Fires is "A task" or "A suite". Repeat is Every N hours,
  Daily, Weekly or Monthly. Each variant swaps in its own fields: hours;
  time; time plus day of week; time plus day of month, with the hint
  "1-31; a shorter month fires on its last day". Overlap is "Skip this
  run" or "Start it anyway".
- **Validation.**
  - An empty title is blocked by the browser.
  - "Every N hours" of 0 is refused (≥ 1), and so is 1.5 (step).
  - Day of month 32 is refused (≤ 31).
  - A suite-backed schedule with no base branch is blocked on the
    required base.
  - Escape closes the form.
- **Task schedule with its own content.** The schedule had these
  settings:
  - weekly, Friday 08:30, America/New_York
  - overlap "Start it anyway"
  - base `main`, write `acme/widgets-docs`, read `acme/shared`
  - network Egress, capability Self debug
  - priority low, requested-by

  It stored `{"kind":"weekly","timeOfDay":"08:30","weekday":"friday","timeZone":"America/New_York"}`
  with `nextRunAt` `2026-10-09T08:30:00-04:00`. The row showed "Can
  overlap". The preview ("What this schedule files", "as filled in
  above") followed every field as it was typed. See bugs 2 and 3: the row
  said "Fridays at 08:30" next to "Next run … 12:30 PM", and the repo had
  started on `acme/shared`.
- **Template-backed schedule.** Daily 07:15 from `walk-tmpl-1533`. The
  form says the template binds the repo and replaces the content fields.
  The row reads "Audit api for 1533 (edited)", with the defaults filled
  in (yesterday's follow-up holds), and "Template: walk-tmpl-1533".
- **Suite-backed schedule.** Monthly on day 31, Release Qualification
  Suite. The preview reads "2 passes of these tasks" and lists both
  templates, each with its source layer. The row reads "monthly on day 31
  at 09:00 · Suite: Release Qualification Suite", with next run 31 Oct.
  The preview shows the templates' bound `acme/widgets` rather than the
  run's repo. That is correct: a template's binding beats the run's repo
  (`model.ComposeSuiteTask`).
- **Edit.** The editor said "Edit schedule". Zone, time, day and overlap
  reopened as saved. The preview said "as edited above, not as saved".
  The title edit persisted across a reload.
- **Pause and resume.** Pause sent PATCH `{"enabled":false}` and closed
  the editor. The row showed "Paused" after a reload, and the editor then
  offered Resume. Resume cleared the chip. The paused row still reads
  "Next run …". See "Found and not fixed".
- **Run now.** Dismissing the confirmation ("Run "…" now? It starts the
  task "…" straight away; the schedule's own next run is unchanged.")
  fired nothing. Accepting it filed task 14 and added "last ran …" to the
  row, which persisted across a reload. Run now on the suite schedule
  started suite run 1, marked "Scheduled" in the runs list and in its
  pane.
- **Delete.** Dismissing the confirmation kept the row. Accepting it
  removed the row. A deep link to the deleted id fell back to the list
  with `no schedule "sched-3"`. Unknown template, suite and run ids
  behave the same way.

### Suites (`/suites`)

- **Create.** Picked `walk-tmpl-1533`, then "+ New template" made
  `walk-inline-1533` inline. It came back selected, and the suite's name
  was kept. Mode "until clean", Max passes 0 (refused, ≥ 1), then 2.
  Require approval was checked. The row read "run until clean (max 2) · 2
  templates · Requires approval".
- **Edit.** Changed to "fixed number" with 3 runs, approval off and
  auto-merge off. After a reload, the row read "run 3× · 2 templates",
  and reopening showed every value.
- **Search and sort.** "walk" narrowed the rows to 1 and the header
  counted 1. A search with no matches shows "No suites match your
  search." Sorting offers Custom order, Name (A–Z) and Most templates
  (there is no Newest or Oldest, unlike the other two lists), and each
  reordered the rows.
- **Delete.** Accepting the confirmation ("Runs already started from it
  are unaffected") deleted the suite. Its run stayed listed as
  "Succeeded".

### Suite runs

- **Starting.**
  - Run… on the suite row opened "Run a suite" with that suite
    preselected and a per-template preview ("3 passes of these tasks").
  - "+ Run" opened the same form with the first suite preselected. An
    empty branch was refused.
  - Both POSTed `/api/suite-runs` and appeared as Active in the runs
  list.

  See bug 3: the repo field started on `acme/shared`, and the preview
  was composed against no repo at all.
- **Pass grouping and follow-ups.** `/suites/runs/2` (direct link and
  reload), after stub 1, shows "Pass 1" (3) and "Pass 2" (2). Each task
  row carries the template's priority, a suite chip, the repo, read and
  write chips, and GCP key. The proposed follow-up 25 from stub 2 sits
  indented under task 17.
- **Finished and failed.**
  - Run 2 reads "Succeeded".
  - Run 3 reads "Failed", with its reason under the heading and on its
    row in the list.
  - Run 1, fired by the schedule, reads "Active … Scheduled".
- **Task detail.** Clicking the follow-up opened `/tasks/25`, and Back
  returned to `/suites/runs/2` with the pane open.

### Repository-declared rows (`docs/declarations.md`)

- **Rows.** Each list marks its declared row "Declared by acme/widgets".
  The chip's title names the file (".grain/templates/nightly.yml" and so
  on).
- **Editors.** The editors open as "Template", "Suite" and "Schedule",
  with the notice "This template is declared by acme/widgets in
  .grain/templates/nightly.yml, so it cannot be edited or deleted here.
  Change that file in the repository and grain picks it up within a
  couple of minutes." Save, Delete and Pause are disabled and Cancel reads
  "Close". Enter in a field sent nothing. The only write the UI sent to a
  declared id in the whole run was `POST …/run`.
- **Run now and Run….** Run now on the declared schedule filed a task
  and stamped "last ran". The schedule kept its declared name
  (yesterday's fix holds). Run… on the declared suite started a run.
- **Links.** See bug 1: every declared row's own link was broken when
  opened as a link.

### Console, network and keyboard

- **Page errors.** No uncaught page errors on any page.
- **Branch listing.** `GET /api/repos/<repo>/github-branches` → 404
  ("GitHub branch listing is not available on this UI server"), whenever
  a repo is picked. The demo has no GitHub behind it, so this is
  expected.
- **Aborted task list.** `GET /api/tasks` reports `net::ERR_ABORTED` on
  most page loads. It is the app cancelling a superseded poll, and is
  harmless.
- **Aborted DELETEs.** Each successful DELETE reported both a 204
  response and `requestfailed net::ERR_ABORTED`. The UI awaited the 204
  before closing, and the row was gone after a reload, so nothing visible
  went wrong.
- **Keyboard.** Tab order on `/suites` is: rail, "+ New suite", search,
  sort, then each row's links and buttons. The template, suite, schedule
  and run rows and the run pane's task rows open with Enter. Scenario 7
  of the e2e spec passed.

## Bugs found and fixed

1. **A declared row's own link opened nothing (fixed).**
   - *Reproduction:* on `/templates`, open `nightly-maintenance` in a new
     tab, or copy its link. The href is
     `/templates/repo%3Atemplate%3Aacme%3Awidgets%3Anightly`, because
     `RowLink` encodes the id. Loading it lands on `/templates` with the
     snackbar `no template "repo%3Atemplate%3Aacme%3Awidgets%3Anightly"`.
     The same happens for the declared suite and schedule. A plain click
     worked only because it never loads the href.
   - *Cause:* `parsePath` used the raw path segment as the id.
   - *Fix:* `paths.js` decodes the id segment for schedules, templates,
     suites, suite runs and boards. A segment with invalid
     percent-encoding is kept as written.
   - *Coverage:* a `paths.test.js` case, which fails without the fix, and
     e2e scenario 8, which opens each declared row's own href in a new
     tab.
2. **A zoned schedule's row contradicted itself (fixed).**
   - *Reproduction:* create a weekly Friday 08:30 schedule in
     America/New_York. The row reads "Fridays at 08:30 … Next run
     10/9/2026, 12:30:00 PM", because the next run is drawn in the
     viewer's zone (UTC here).
   - *Fix:* `describeRecurrence` appends a non-UTC zone: "Fridays at
     08:30 America/New_York". Every-N-hours rows and UTC rows are
     unchanged.
   - *Coverage:* a `SchedulesList.test.jsx` case, which fails without the
     fix, and e2e scenario 8, which creates a daily New York schedule and
     checks the chip across a reload.
3. **New schedules and suite runs started on the alphabetically first
   repo (fixed).**
   - *Reproduction:* add `acme/aardvark`, or any repo sorting before
     `acme/widgets`, to the target list. "+ New schedule" and "Run a
     suite" then start on `acme/aardvark`, although the deployment's
     default target is `acme/widgets`. A schedule saved without looking
     closely files against the wrong repo. In "Run a suite", the preview
     also composed against no repo ("the repo this run targets") while
     the field showed one.
   - *Fix:* a `defaultRepoChoice(config, options)` helper in `state.js`
     picks `config.defaultTarget` when the field offers it, and the
     first option otherwise. `ScheduleOverlay` and `SuiteRunOverlay` use
     it for the field's initial value. `SuiteRunOverlay` also seeds its
     preview with it.
   - *Coverage:* a `SchedulesList.test.jsx` case and a
     `SuitesList.test.jsx` case, which also asserts the preview request
     names the repo. Both fail without the fix. Verified in Chromium
     against the rebuilt demo.

## Found and not fixed

- **A task's state is not exposed to assistive technology in task rows
  (proposed as a follow-up).** In the suite-run pane, as everywhere
  `TaskRow` is used, state is a coloured dot with a `title` tooltip. The
  accessibility tree has no state for the row: "17 Audit api … Prioritylow
  suite acme/widgets …". A screen-reader user cannot tell queued from
  running from proposed in a run's pane. This is app-wide, so it gets its
  own task.
- **Template names are not unique.** Two templates named
  `walk-tmpl-1533` were accepted, and every template picker (schedule,
  suite, Use…) then shows two identical entries. This is a product
  decision (refuse, or disambiguate in pickers), so it is noted here
  rather than changed.
- **A paused schedule still shows "Next run …".** The Paused chip sits
  beside it, so this is low impact. Noted only.
- **The Run now confirmation for the declared schedule names the
  schedule's own title as the task** ("starts the task "Nightly Repo
  Maintenance Sweep""). The task it files is titled from its template,
  "Declared Nightly Maintenance". Low impact. Noted only.
- **A dependency-refused delete does not name the dependents** ("used by
  1 schedule(s)"). Noted only.
- **"+ New template" stays enabled in a declared suite's read-only
  editor.** It creates a real deployment template that the suite can
  never be saved with. Noted only.
- **Suites sort offers no Newest or Oldest**, unlike Templates and
  Schedules. Noted only.
- **The time label reads "Time (UTC, UTC)"** when no zone is set. Noted
  only.

## Tests run

| Suite | Result |
| --- | --- |
| Vitest: `paths.test.js`, `SchedulesList.test.jsx`, `SuitesList.test.jsx`, `TemplatesList.test.jsx` | pass. The three new cases fail with the fixes reverted |
| Vitest, full suite (`npx vitest run`) | 2746 / 2747. The one failure, `RepoPage.test.jsx` "emptying an override goes back to showing the global value", is untouched by this change and passed 64 / 64 when run on its own, with and without this change: a load flake |
| Playwright `e2e/recurring-composed-walkthrough.spec.js` (chromium) | 8 / 8 pass, including the new scenario 8 |
| Playwright `schedules_workflows`, `suites_workflows`, `templates_workflows`, `team_declarations_ci_walkthrough`, `paths` (chromium, 3 workers) | 19 / 19 pass |
| grain CI (full required suite) on the pushed branch | see the pull request |
