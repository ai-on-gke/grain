# Recurring and composed work UI walkthrough: 2026-10-05

grain/task-1985. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server, with
bugs fixed on the same branch. It follows
[2026-10-04-recurring-composed-ui.md](2026-10-04-recurring-composed-ui.md),
whose fixes all held: a paused row reads "Held -- fires …" rather than
"Next run", and the run form's Branch helper says which tasks take its
branch. This run went after what the earlier runs had not exercised:
editing schedules that already exist, switching what they fire,
time-zone round trips, and searching for declared rows.

## Environment

| | |
| --- | --- |
| Commit tested | `95a1f7369` (candidate tip, "Merge pull request #2918"), then this branch's commits on top |
| Branch | `grain/task-1985` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 |
| Server | `grain demo -addr 127.0.0.1:<port> -open=false`, built from this tree (`npm run build` into `pkg/ui/static`, then `go build ./cmd/grain`), throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, set by an init script |
| Viewport | 1400×900; one pass with `timezoneId: "America/Los_Angeles"` |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

`npx playwright install chromium` was run with
`NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`, which the
10-04 report says the package cache's CDN interception needs. It
succeeded first time.

Exploration used throwaway Playwright scripts (`ui/.tmp-walk/`, excluded
through `.git/info/exclude` and not committed). They used
`chromium.launch()` and logged every console error and warning, every page
error, every failed request, every HTTP status ≥ 400 with its body, every
non-GET request with its body, and every `confirm()`. Each `confirm()` was
accepted or dismissed depending on the step. No screenshots or traces
were kept.

Three demo servers were used:

- `:18080` ran the candidate build for the walkthrough.
- `:18081` ran the binary with the schedule fix (bug 1), to verify it.
- `:18082` ran the binary and frontend with both fixes, to verify bug 2.

## Stubs

Every create, edit, run, pause, resume and delete below was a click or
keystroke in Chromium against the real server. Nothing mocked grain's API
or React.

1. **Suite-run progression (orchestrator boundary).** `grain demo` runs no
   orchestrator. A throwaway Go helper (`.tmp-suitestub/`, excluded and not
   committed) opened the demo's SQLite store with `sqlite.Open` and called
   the same store methods the orchestrator calls:
   - Run 3 (Release Qualification Suite): `StartRun` + `FinishRun(success)`
     for tasks 15 and 16, then `CompleteSuiteRun(3, succeeded)`.
   - Run 1 (`walk-suite-1985`, which requires approval):
     - `StartRun` + `FinishRun(13, "error", "stub: agent exited 1")`.
       Task 13 was never approved, so it running at all is the stub's
       doing.
     - `CompleteSuiteRun(1, failed, "pass 1 task 13 ended in error (stub)")`.
       The stub wrote that reason text.
   - Run 2 (declared Smoke suite): `FinishRun(14, success)`, then
     `FireNextPass(2)` (task 17 in pass 2), then `StartRun` for task 17 so
     it reads as running.

   The browser was reloaded after the calls to read the result. Because
   the stub only finishes runs, it never closes tasks. That is why 15 and
   16 still badge "Queued" in a succeeded run and 13 badges "Proposed" in
   a failed one: the orchestrator would have settled them.
2. **Agent follow-up proposal (agent boundary).** `POST /api/tasks
   {"proposedBy": "14", "status": "proposed"}` filed task 18. This is the
   request an agent's `propose_task` becomes.
3. **Repository declarations.** These come from the demo's own seed
   (`cmd/grain/demo.go`, `SyncRepoDeclarations` for `acme/widgets`). No
   GitHub poll was involved.

## Steps and outcomes

Unless a step says otherwise, outcomes were checked after a reload, by deep
link, and against the API's answer.

### Baseline

Run on the candidate build before anything was touched:

- `e2e/recurring-composed-walkthrough.spec.js` (10 scenarios)
- `schedules_workflows`, `suites_workflows` and `templates_workflows`

All 13 passed.

### Schedules (`/schedules`)

- **Zoned monthly schedule, raw (`sched-3`).**
  - Fields: title, two-line description, `acme/widgets` @ `main`,
    Monthly day 31 at 23:30, time zone America/New_York.
  - The time label read "Time (America/New_York, EDT)". The helper says
    "1-31; a shorter month fires on its last day", so the 10-04
    "clamped silently" note is answered in the form now.
  - The row read "monthly on day 31 at 23:30 america/new_york · Next run
    11/1/2026, 3:30:00 AM". That is 03:30Z: 23:30 EDT, printed in the
    deployment's zone (UTC).
  - Reopening showed America/New_York. Choosing "UTC (default)" PATCHed
    `timeZone: ""`. The row dropped the zone and moved to "Next run
    10/31/2026, 11:30:00 PM", and the API no longer carried a `timeZone`.
- **Times are in the deployment's zone, not the browser's.** With the
  browser in America/Los_Angeles, the seeded daily-00:00 schedule still
  read "Next run 10/6/2026, 12:00:00 AM". That is by design
  (`TimeZoneContext.jsx`), but the row does not say which zone it means.
  Noted only.
- **Attach a template to an existing schedule.** Picking `security-audit`
  on `sched-3` hid the content fields. It is bound to `acme/widgets` @
  `main`, so the repo and branch boxes went too. Save then failed with
  "repo cannot be empty: a schedule with no target cannot fire" and the
  editor stayed open. **Bug 1.**
- **Retime a seeded template-backed schedule.** On `sched-1`
  (`release-qualification`, also bound), changing only the time to 01:15
  failed the same way. No schedule fired from a repo-bound template could
  be saved at all. **Bug 1.**
- **Detach.** Choosing "none -- fill in the fields below" brought the
  fields back with the schedule's own content. Save PATCHed `templateId:
  ""` and the content, and succeeded.
- **Pause, then run now while paused.**
  - Pause PATCHed `{"enabled":false}`. The row read "paused · Held --
    fires 10/31/2026, 11:30:00 PM if resumed by then".
  - The Run now confirmation read 'Run "walk-1985 zoned monthly" now? It
    starts the task "walk-1985 zoned monthly" straight away; the
    schedule's own next run is unchanged.' Dismissing it sent nothing and
    kept the editor open.
  - Accepting it POSTed `…/run` while the schedule was still paused. The
    row then read "… if resumed by then · last ran 10/5/2026, 3:50:32 AM".
  - Resume put "Next run" back, with the time unchanged.
- **Declared schedule Run now.** Its confirmation names the template ("a
  task from the template "nightly-maintenance""). Accepting it fired the
  schedule, and `lastRunAt` was set.
- **Delete.** 'Delete the schedule "walk-1985 zoned monthly"? Tasks it
  already filed are not affected.' Dismissing kept it; accepting deleted
  it.

### Templates (`/templates`)

- **Inline template from the suite editor.** `walk-inline-1985` was
  titled "Inline 1985 {{area}}" with no input declared. It saved, and
  the suite's task later carried the braces literally. That is by design:
  `model.Template.Fields` says that with no inputs "`{{anything}}` in the
  prose is prose". Noted only.
- **Dependency-aware delete.**
  - The confirmation read 'Delete the template "security-audit"?
    Schedules and suites using it must be repointed or deleted first.'
  - Deleting `security-audit` while suite-1 used it was refused with
    "template is used by 1 suite(s); remove it from those first". The
    editor stayed open and showed the message.
  - Once suite-2 was gone, `walk-inline-1985` deleted cleanly.
  - Only the seeded templates remained.

### Suites (`/suites`)

- **Validation.**
  - Add suite with no templates was refused with "a suite needs at least
    one template", and the form stayed open.
  - Number of times = 0 was blocked by the browser ("Value must be
    greater than or equal to 1.").
- **Create (`suite-2`, `walk-suite-1985`).**
  - The inline "+ New template" added its new template to the picker
    straight away.
  - Settings: count mode, 3 times, auto-merge off, approval on.
  - The row read "run 3× · 1 template · requires approval".
  - Reopening by `/suites/suite-2` showed every value.
- **Edit.** Switched to until-clean with max 2. The PATCH went through,
  and the row read "run until clean (max 2)".
- **Delete.**
  - The confirmation read 'Delete the suite "walk-suite-1985"? Runs
    already started from it are unaffected.' Dismissing kept the suite;
    accepting deleted it.
  - `/suites/runs/1` still opened afterwards.

### Suite runs

- **From the suite row.** "Run…" on `walk-suite-1985` showed the run
  form.
  - With the branch blank, the browser blocked submission ("Please fill
    out this field.").
  - With `feature/walk-1985`, it POSTed `/api/suite-runs` and run 1
    appeared.
- **From the generic "+ Run".**
  - The Suite picker listed all three suites, the declared one included.
  - Run was already enabled before a suite was picked, because the
    picker opens on its first suite.
  - Running Smoke Verification Suite on `main` gave run 2. Release
    Qualification Suite on `release/1.0` (from its row) gave run 3.
- **Presentations, after the stubs.**
  - The runs list shows status, repo, branch and "pass N · mode".
    Failed runs also show the reason. A run that waits for approval says
    "each pass's tasks wait for approval".
    - Run 3: "succeeded".
    - Run 2: "active · pass 2".
    - Run 1: "failed" with "pass 1 task 13 ended in error (stub)".
  - `/suites/runs/2` groups by pass:
    - Pass 1 (2): task 14, with follow-up 18 nested under it and counted
      in that pass.
    - Pass 2 (1): task 17, badged "Running".
  - Every row's badge has an accessible name: Queued, Proposed, Running.
- **Navigation.**
  - Clicking follow-up 18 opened `/tasks/18`, and Back returned to
    `/suites/runs/2`.
  - On `/suites/runs/3`, three Tabs reached task 16's title link
    (`/tasks/16`), and Enter opened it.
  - Escape on a run pane returned to `/suites`.
- **Placement noted.** Task 14 comes from the declared template, which is
  bound to `acme/widgets` with no branch. It carries no base, although
  the run was on `main`: a bound template's binding replaces the run's
  branch too (`Template.FiringTarget`). This is consistent with "binding
  wins", but a binding with no branch means the default branch, not the
  run's. Noted only.

### Repository-declared rows (`docs/declarations.md`)

- Each list marks its declared row "declared by acme/widgets".
- Each editor, opened by an encoded deep link, says "This … is declared
  by acme/widgets in .grain/…/….yml, so it cannot be edited or deleted
  here. Change that file in the repository and grain picks it up within a
  couple of minutes." The schedule adds "Run now still fires it once";
  the suite adds "It can still be run from the suites list".
- Save, Delete and Pause match `:disabled`, as do the suite's "+ New
  template" and the template's "+ Add input". The only enabled inputs
  left are the list page's own search boxes behind the pane.
- **Search.**
  - Searching suites for "acme/widgets" found nothing, although the
    declared suite's row says "declared by acme/widgets".
  - No list found a declared row by its file (`nightly.yml`).
  - The declared template only answered "declared" because its title
    happens to be "Declared Nightly Maintenance".
  - **Bug 2.**

### Sort, search, empty states

- **Sort.** Each list's sort menu was driven through every option:
  - Schedules: Custom order, Title (A–Z), Next run, Newest first, Oldest
    first. Next run gave 10/5 06:00, 10/6 00:00, 10/6 04:00, as it
    should.
  - Templates: Custom order, Name (A–Z), Newest first, Oldest first.
  - Suites: Custom order, Name (A–Z), Most templates.

  Each gave the expected order. `sched-1` and `sched-2` are seeded with
  the same `createdAt`, so their relative order under Newest/Oldest is a
  tie.
- The team filter beside it (All teams / admin) leaves the seeded rows,
  which are all the admin team's.
- Search is case-insensitive ("ACME/WIDGETS").
- "zzzz" gives "No schedules / templates / suites match your search."
- The empty runs list says "No suite runs yet."

### Console and network

- **No uncaught page errors** on any page.
- `GET /api/repos/<repo>/github-branches` returns 404 whenever a repo is
  picked. There is no GitHub behind the demo, so this is expected.
- Every 400 was a refused save or delete described above, and each was
  shown in the UI.
- `GET /api/tasks` reports `net::ERR_ABORTED` on navigation: a poll
  cancelled by the page change, as on 10-02 through 10-04.

## Bugs found and fixed

1. **A schedule fired from a repo-bound template could not be saved.**
   - *Reproduction:* open `/schedules/sched-1` (the seeded
     `release-qualification`, bound to `acme/widgets` @ `main`). Change
     the time and press Save. The PATCH carries `"repo":"","base":""`
     and the server answers 400 "repo cannot be empty: a schedule with
     no target cannot fire". Attaching `security-audit` to an inline
     schedule fails the same way.
   - *Cause:* the editor hides the repo and branch boxes for a bound
     template and sends them blank. `CreateSchedule` reads that blank as
     "the binding decides", but `UpdateSchedule` refused it before ever
     reaching the binding.
   - *Fix:* `pkg/ui/schedules.go` now takes a blank repo as leave-alone
     when the template the schedule will fire is bound. It is still
     refused otherwise, a detach included.
   - *Verified* in Chromium on the rebuilt server:
     - `sched-1` saved as "daily at 01:15".
     - `sched-2` attached to `security-audit` and saved.
     - Detaching it again saved.
   - *Coverage:*
     - `TestUpdateScheduleOnABoundTemplateAcceptsTheBlankRepoTheEditorSends`
       in `pkg/ui/schedules_test.go`.
     - e2e scenario 11 in `recurring-composed-walkthrough.spec.js`.
     - Both fail with the fix reverted.
2. **List search could not find a declared row by the repo or file that
   declares it.**
   - *Reproduction:* on `/suites`, search "acme/widgets". The declared
     Smoke Verification Suite, whose row reads "declared by
     acme/widgets", is not shown. On `/schedules`, search
     "schedules/nightly.yml": nothing.
   - *Fix:* `TemplatesList`, `SchedulesList` and `SuitesList` also match
     `declaredIn.repo` and `declaredIn.path`. `SchedulesList` already
     matches the template and suite chips on its rows in the same way.
   - *Verified* in Chromium on the rebuilt server: "acme/widgets" now
     finds Smoke Verification Suite.
   - *Coverage:* `SuitesList.test.jsx` and `SchedulesList.test.jsx`
     cases, both failing with the fix reverted.

## Found and not fixed

- **Timestamps don't name their zone.** List times are printed in the
  deployment's zone with no zone label, so a browser elsewhere cannot
  tell which zone they mean. Deliberate per `TimeZoneContext.jsx`; noted
  only.
- **Attaching a template replaces the schedule's own content.** Attaching
  and then detaching leaves the template's content, not the schedule's
  original (`sched-2` lost its `self-debug` grant). This is documented
  behaviour (`UpdateScheduleRequest`), but the editor does not warn
  before the save.
- **A bound template with no branch sends a suite run's task to the
  default branch** rather than the run's branch (see Suite runs).
- **Carried from 10-04:**
  - The follow-up filed with `proposedBy` carries the team's capabilities
    (gcp key, gemini key, self debug).
  - A dependency-refused delete does not name the dependents.
  - A template-backed schedule cannot set the template's inputs.
  - Every-N-hours and pass counts have no upper bound.

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/ui/` | pass; the new test fails with the fix reverted |
| Vitest: `SchedulesList`, `SuitesList`, `TemplatesList`, `SuiteRunPane`, `App.schedules` | all pass; the two new cases fail with the fix reverted |
| Playwright `recurring-composed-walkthrough`, `schedules_workflows`, `suites_workflows`, `templates_workflows` (chromium) | 14 / 14 pass; scenario 11 fails on the unfixed server |
| grain CI (full required suite) on the pushed branch | see the pull request |
