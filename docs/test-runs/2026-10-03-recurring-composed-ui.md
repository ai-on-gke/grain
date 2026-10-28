# Recurring and composed work UI walkthrough: 2026-10-03

grain/task-1628. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server, with
bugs fixed on the same branch. It follows
[2026-10-02-recurring-composed-ui.md](2026-10-02-recurring-composed-ui.md).
That run's three fixes (declared rows' own links, a zoned schedule naming
its zone, new schedules and runs starting on the default repo) all held.
Of the items it noted and did not fix, this run fixed three: "Time (UTC,
UTC)", "+ New template" live in a declared suite's editor, and the Run now
confirmation naming the wrong title.

## Environment

| | |
| --- | --- |
| Commit tested | `11606c9fc` (candidate tip, "Merge pull request #2687"), then this branch's own commits on top |
| Branch | `grain/task-1628` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 |
| Server | `grain demo -addr 127.0.0.1:18080 -open=false`, built from this tree (`npm run build` into `pkg/ui/static`, then `go build ./cmd/grain`), throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, set by an init script |
| Viewport | 1400×900 |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

Exploration used throwaway Playwright scripts (`ui/.tmp-walk/`, excluded
through `.git/info/exclude` and not committed). They used
`chromium.launch()` and logged:

- every console error and warning, and every page error
- every failed request, and every HTTP status ≥ 400 with its body
- every non-GET request with its body
- every `confirm()`, which was then accepted or dismissed depending on
  the step

No screenshots or traces were kept.

Two demo servers were used. The first ran the candidate build for the
walkthrough. The second ran the rebuilt binary with this branch's fixes,
to verify them. Each store is thrown away when its process stops, so the
second one starts from the seed again.

## Setup outside the area under test

`POST /api/repos` added `acme/widgets-docs` and `acme/shared` to the
target repo list, on each server. This is the call the Repos page's own
"add" makes, so it is configuration, not a stub. Without it the demo only
targets `acme/widgets` and `grain/config-default`, and a write or read
repo is refused at save (seen and documented on 10-02).

## Stubs

Every create, edit, run, approve and delete in this report was a click or
keystroke in Chromium against the real server. Nothing mocked grain's API
or React.

1. **Suite-run progression (orchestrator boundary).** `grain demo` runs
   no orchestrator, so a run never leaves pass 1 or `active` and no task
   ever runs. A throwaway Go helper (`.tmp-suitestub/`, excluded and not
   committed) opened the demo's SQLite store and called the store methods
   the orchestrator calls:
   - `Store.StartRun` for task 15, which made it read as running.
   - `Store.FireNextPass` on runs 1 and 3, which put them on pass 2.
   - `Store.CompleteSuiteRun(3, SuiteRunFailed, "pass 2 never came back clean: task 22 failed its checks")`.
     The reason text was written by the stub, and task 22 is in fact in
     run 1. Only its presentation was under test.
   - `Store.CompleteSuiteRun(1, SuiteRunSucceeded, "")`. Completing a "run
     2×" run after two passes is what SyncSuites would do. Its tasks
     never ran, which is the stub's doing.

   The browser was reloaded after the calls to read the result.
2. **Agent follow-up proposal (agent boundary).** `POST /api/tasks
   {"proposedBy": "20", "status": "proposed"}` filed task 25. This is the
   request an agent's `propose_task` becomes, from suite task 20.
3. **Repository declarations.** These come from the demo's own seed
   (`cmd/grain/demo.go`, `SyncRepoDeclarations` for `acme/widgets`). No
   GitHub poll was involved.

## Steps and outcomes

Unless a step says otherwise, outcomes were checked after a reload, by
deep link, and against the API's answer.

### Templates (`/templates`)

- **Sort.** The options are Custom order, Name (A–Z), Newest first and
  Oldest first. Each produced the expected order.
- **Search.**
  - "walk" and "SECURITY" (case-insensitive) narrowed the list.
  - "Declared" matched the declared template through its title.
  - "zzzz" showed "No templates match your search."
  - Search covers name, title and repo only, so "acme/shared" (a read)
    and "backend" (a field) found nothing. Noted only.
- **Create, fully populated (`walk-tmpl-1628`).** The template had these
  fields:
  - title `Audit {{service}} for 1628` and a two-line description
  - input `service`, label `Service`, options `api`/`web`, default `api`
  - repo `acme/widgets` on `main`, write `acme/widgets-docs`, read
    `acme/shared`
  - auto-merge, network None, capabilities GCP key and Self debug
  - priority high, area backend, requested-by `qa-1628`, due date
    2026-12-01, estimate 2.5, customer-facing

  The first attempt typed the options as `api, web` and was refused with
  `field "Service": default "api" is not one of its options`. The
  placeholder does say "one per line", so this was the tester's error.
  The form stayed open and kept every value, which is correct. Retyped
  one per line, the POST stored every field.
- **Escape inside the form.** Escape in an open Capabilities menu closes
  the menu only. But Escape after picking an Area (Autocomplete closed)
  closed the whole editor and threw the filled-in form away. The same
  thing happened on the schedule form. See "Found and not fixed" (since
  fixed on `candidate`).
- **Reopen and edit.**
  - Deep link `/templates/template-5`: every field was pre-filled,
    including the input's options and the chips (read, capabilities,
    area).
  - The title was changed to "… (edited)" and the priority to low (PATCH
    200). Both held after a reload and on reopening.
- **Use….** The default `api` was offered, with a preview of every
  field "from the template". Choosing `web` and Create task opened
  `/tasks/13`, "Audit web for 1628 (edited)". It had the repo, base,
  reads, writes, both capabilities, network `none`, auto-merge and every
  custom field.
- **Dependency-aware delete.** These ran at the end of the walkthrough.
  - While `sched-4` used the template, delete was refused with "template
    is used by 1 schedule(s); repoint or delete those first". The editor
    stayed open with the message.
  - The seeded `release-qualification` was refused the same way.
  - After `sched-4` was deleted, the refusal became "template is used by
    1 suite(s); remove it from those first".
  - After the suite was deleted, both throwaway templates deleted
    cleanly.
  - Only the three seeded templates remained.

### Schedules (`/schedules`)

- **Form.**
  - Fires offers A task or A suite.
  - The Template picker, Target team, Repos, Base branch, writes, Title,
    Description, reads, Auto-merge, Network, Capabilities and the
    deployment's fields are all there.
  - Repeat offers Every N hours, Daily, Weekly or Monthly.
  - The Time zone list has 420 IANA zones. Chromium names Kolkata
    `Asia/Calcutta`.
  - Overlap offers Skip this run or Start it anyway, each with its own
    hint.
- **Validation.** Each of these was blocked by the browser and the form
  stayed open:
  - an empty title
  - Every N hours = 0
  - a suite-backed schedule with no base branch
  - day of month 0
- **Task schedule with its own content (`sched-3`).** The schedule had
  these settings:
  - every 6h, Start it anyway
  - repo `acme/widgets` on `main`, write `acme/widgets-docs`, read
    `acme/shared`
  - network None, GCP key, priority high, area infra

  The preview ("What this schedule files", "as filled in above") showed
  every one of them before save. The stored row matched, and the list row
  read "every 6h · Can overlap".
- **Template-backed schedule (`sched-4`).** Weekly, Sunday 23:30,
  Asia/Calcutta, from `walk-tmpl-1628`.
  - The form said the template binds the repo and supplies the content.
  - The preview showed the template's fields with `{{service}}` filled
    from its default.
  - The row read "Sundays at 23:30 Asia/Calcutta". `nextRunAt` was
    `2026-10-04T18:00:00Z`, which is Sunday 23:30 IST.
  - The schedule form offers no way to set the template's input. A
    template-backed schedule always files with the default. Noted only.
- **Suite-backed schedule (`sched-5`).** Monthly on day 29 at 09:00,
  Europe/London, Release Qualification Suite on `main`.
  - The preview listed "2 passes of these tasks", with each template.
  - The next run, 2026-10-29 09:00 UTC, is correct, because London is
    back on GMT by then.
  - Before the fix, the time label for the default zone read "Time (UTC,
    UTC)". See bug 1.
- **Sort.** Custom order, Title (A–Z), Next run, Newest first and Oldest
  first each produced the expected order.
- **Search.** "1628" and "suite" matched titles. "walk-tmpl", the
  template named on a row's own chip, found nothing. See bug 5.
- **Edit.** `sched-3` was changed to Daily 05:15 America/New_York with a
  new title (PATCH 200). After a reload the row read "Daily at 05:15
  America/New_York", with the next run at 09:15 UTC.
- **Pause and resume.**
  - Pause sends `{"enabled": false}` straight away with no confirmation.
    That is reasonable, since it is reversible.
  - The row gains "Paused" and still shows "Next run …". That was noted
    on 10-02 and is noted again here.
  - The button becomes Resume, and Resume restores the row.
- **Run now.**
  - Dismissing the confirmation sent nothing. Accepting it POSTed
    `/run`, filed task 14 and stamped "· last ran …" on the row, with the
    next run unchanged.
  - On the suite-backed schedule, the confirmation said "starts a run of
    the suite …". Accepting it started suite run 1, marked Scheduled.
  - On the declared schedule, the confirmation named the schedule's own
    title as the task, but the task filed was "Declared Nightly
    Maintenance". See bug 3.
- **Delete.** Dismissing the confirmation kept the schedule, and
  accepting it deleted the schedule. For the suite-backed schedule the
  confirmation said "Tasks it already filed are not affected", although
  it files suite runs. See bug 4.

### Suites (`/suites`)

- **Sort and search.** Sort offers Custom order, Name (A–Z) and Most
  templates. "smoke" narrowed the list, and "zzzz" showed "No suites
  match your search."
- **Create (`walk-suite-1628`) with an inline template.**
  - An empty name was blocked.
  - The Templates picker lists each template with its title and repo.
    Escape in it closes the picker only.
  - "+ New template" opened the template editor over the suite. Adding
    `walk-inline-1628` there returned to the suite with its name kept and
    both templates chosen. The preview showed "What each pass files" for
    each one.
- **Run modes.**
  - "Run until no repo changes or new tasks" shows Max passes. "Run a
    fixed number of times" shows Number of times.
  - 0 is refused for both.
  - 21 was accepted. There is no upper bound, and the server took it too.
- **Edit.** Until clean, max 4, require approval on, auto-merge off
  (PATCH 200). After a reload the row read "run until clean (max 4) · 2
  templates · Requires approval", and the reopened editor showed each
  setting.
- **Delete.** Dismissing the confirmation ("Runs already started from it
  are unaffected") kept the suite. Accepting it deleted the suite, and
  its run stayed listed.

### Suite runs

- **Starting.**
  - Run… on the suite row opened "Run a suite" with that suite selected,
    the default repo and a per-template preview. An empty branch was
    blocked. With `main` it POSTed `/api/suite-runs` and run 2 appeared
    as Active.
  - "+ Run" opened the same form. It preselected the most recently used
    suite (`walk-suite-1628`), not the first in the list. Choosing
    Release Qualification Suite on `release-1628` started run 3.
- **Approval.** Run 2's suite requires approval, so both its tasks were
  filed `proposed`. Neither its row nor its pane said so. They read like
  a run whose tasks are queued. See bug 2. Task 17 was opened from the
  pane with Enter, and Approve was clicked on its detail page
  (`POST /api/tasks/17/approve`).
- **Passes and outcomes** (after stub 1). Each run was checked by direct
  link and after a reload.
  - `/suites/runs/1` reads "Succeeded … Scheduled", "run 2×", with "Pass
    1" (2) and "Pass 2" (2). Task 15 carries a "Running" state image.
  - `/suites/runs/3` reads "Failed" with its reason under the heading,
    with Pass 1 (3) and Pass 2 (2). The proposed follow-up 25 is indented
    under task 20 and counted in its pass.
  - The runs list shows each run's status, repo, branch, "Scheduled" and
    the failure reason.
- **Task detail.** Clicking follow-up 25 opened `/tasks/25`, and Back
  returned to `/suites/runs/3` with the pane open.
- **Unknown ids.** `/suites/runs/999`, `/suites/runs/abc`,
  `/schedules/sched-999`, `/templates/nope` and `/suites/nope` each
  corrected to their list with a snackbar such as `no suite run "999"`.
- **Run of a deleted suite.** Run 2 stayed listed and openable after its
  suite was deleted. It is still "Active", because no orchestrator runs
  in the demo to settle it.

### Repository-declared rows (`docs/declarations.md`)

- **Rows.** Each list marks its declared row "Declared by acme/widgets",
  and the chip's title names the file: `.grain/templates/nightly.yml`,
  `.grain/suites/smoke.yml` and `.grain/schedules/nightly.yml`.
- **Deep links.** The encoded href opens the right editor, headed
  "Template", "Suite" or "Schedule".
- **Notice.** Each editor says "This … is declared by acme/widgets in
  <file>, so it cannot be edited or deleted here. Change that file in the
  repository …". The schedule's notice adds "Run now still fires it
  once". The suite's adds "It can still be run from the suites list".
- **Refusals.** Save, Delete and Pause were disabled, and Cancel reads
  Close.
- **Fields.** Every field still took typing, and "+ Add input" and the
  suite's "+ New template" still worked. The latter files a real
  deployment template. See bug 2b.
- **Writes.** The only write the UI sent to a declared id was Run now's
  `POST …/run`.

### Console, network and keyboard

- **Page errors.** No uncaught page errors on any page.
- **Branch listing.** `GET /api/repos/<repo>/github-branches` → 404
  ("GitHub branch listing is not available on this UI server") whenever
  a repo is picked. The demo has no GitHub behind it, so this is
  expected.
- **Refused saves and deletes.** Every 400 came from a refused save or
  delete described above, and each was shown in the UI.
- **Aborted DELETEs.** Each successful DELETE also reported
  `requestfailed net::ERR_ABORTED`, as seen on 10-02. The UI awaits the
  response before closing, and the row was gone after a reload.
- **Keyboard on `/schedules`.** The Tab order is: + New schedule, search,
  sort, then each row's move handle and link. The run pane's task rows
  open with Enter.
- **Keyboard in a declared editor (after the fix).** Tab reaches the
  MUI selects (which do not open on click), Run now, Close and Back.

## Bugs found and fixed

1. **The default zone's time label read "Time (UTC, UTC)".**
   - *Reproduction:* "+ New schedule", Repeat Daily.
   - *Fix:* `ScheduleOverlay` drops the abbreviation when it is the
     zone's own name. Europe/London still reads "Time (Europe/London,
     GMT+1)".
   - *Coverage:* a `SchedulesList.test.jsx` case, and e2e scenario 9.
2. **Declared editors took edits that could never be saved, and a
   suite run waiting on approval did not say so.**
   - a. *Run reproduction:* edit a suite to "Require approval" and run
     it. The run's row says "pass 1 · run until clean (max 4)" and its
     pane says the same, while every task is `proposed` and nothing
     moves until somebody approves it. The suite's own row did say
     "Requires approval".
     *Fix:* `describeRunPlan` in `SuiteRunPane.jsx` adds "each pass's
     tasks wait for approval" for such a run, on its row and in its
     pane.
     *Coverage:* `SuiteRunPane.test.jsx` and `SuitesList.test.jsx`
     cases, and e2e scenario 9.
   - b. *Editor reproduction:* open the declared Smoke Verification
     Suite and click "+ New template". It files a real template, which
     the suite can then never be saved with. In every declared editor
     the fields also accepted typing.
     *Fix:* a `DeclaredFields` wrapper (`Declared.jsx`) puts each
     editor's fields in a fieldset that is disabled for a declared row.
     The action row (Close, Run now) is left outside it. MUI selects are
     a div, which a fieldset does not reach, so they stop taking pointer
     events instead. They stay focusable by keyboard, but with Save
     disabled nothing chosen in one is ever sent.
     *Coverage:* `TemplatesList`, `SuitesList` and `SchedulesList` cases,
     plus a case checking an ordinary editor stays editable. E2e
     scenario 5 now asserts the field is disabled where it used to type
     into it, and scenario 9 checks the declared suite. Verified in
     Chromium: with the fix, the only enabled controls in a declared
     editor are Back, Close and (schedules) Run now, and typing changes
     nothing.
3. **Run now's confirmation on a template-backed schedule named a task
   that would not be filed.**
   - *Reproduction:* Run now on the declared "Nightly Repo Maintenance
     Sweep". The confirmation says it starts the task "Nightly Repo
     Maintenance Sweep", but the task filed is "Declared Nightly
     Maintenance".
   - *Fix:* the confirmation now says it starts 'a task from the
     template "nightly-maintenance"'. Verified in Chromium against the
     task actually filed (task 13 on the second server).
   - *Coverage:* e2e scenario 9.
4. **Deleting a suite-backed schedule said "Tasks it already filed are
   not affected"** although it starts suite runs.
   - *Fix:* the confirmation now says "Suite runs it already started are
     not affected". Verified in Chromium.
5. **Schedule search ignored the template or suite named on the row.**
   - *Fix:* the match covers `templateName` and `suiteName` as well as
     title and repo.
   - *Coverage:* a `SchedulesList.test.jsx` case, and e2e scenario 9.

All seven new unit cases fail with the fixes reverted and pass with them.

## Found and not fixed

- **Escape, a backdrop click or "← Back" threw away a filled-in
  schedule, template or suite form without asking.** Seen on the
  candidate tested (`11606c9fc`): fill "+ New template", pick an Area,
  press Escape, and the pane closed with nothing kept. This run proposed
  it as a follow-up, but `candidate` has since landed the fix
  (`8af545853`, "ask before Escape or Back discards an edited schedule,
  template or suite"). This branch was merged with it, and the declared
  editors' disabled fieldset sits inside that change's tracked form.
- **A template-backed schedule cannot set the template's inputs.** It
  always files with each input's default. This may be intended, so it is
  noted only.
- **A paused schedule still shows "Next run …".** Noted on 10-02 too.
- **Template search does not cover reads, writes or field values.**
  Noted only.
- **A suite's fixed run count has no upper bound.** 21 was accepted.
  Noted only.
- **A run's tasks show no textual state except Running.** The
  `TaskRow` accessibility follow-up from 10-02 covers this.
- **The 10-02 notes** "a dependency-refused delete does not name the
  dependents" and "Template names are not unique" still hold.

## Tests run

| Suite | Result |
| --- | --- |
| Vitest: `SchedulesList`, `SuitesList`, `TemplatesList`, `SuiteRunPane` | 136 / 136 pass. The 7 new cases fail with the fixes reverted |
| Vitest, full suite (`npx vitest run`) | 2818 / 2818 pass (167 files) |
| Playwright `e2e/recurring-composed-walkthrough.spec.js` (chromium) | 9 / 9 pass, including the new scenario 9 and the changed scenario 5 |
| Playwright `schedules_workflows`, `suites_workflows`, `templates_workflows`, `team_declarations_ci_walkthrough`, `paths` (chromium, 3 workers) | 20 / 20 pass |
| grain CI (full required suite) on the pushed branch | see the pull request |
