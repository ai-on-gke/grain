# Recurring and composed work UI walkthrough: 2026-10-06

grain/task-2235. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server, with
the bugs it found fixed on the same branch. It follows
[2026-10-05-recurring-composed-ui.md](2026-10-05-recurring-composed-ui.md).
Both of that run's fixes held: a schedule on a repo-bound template saves,
and list search finds a declared row by its repo and file.

This run concentrated on what the earlier runs had not covered:

- a task-filing schedule with every field filled in, followed through to
  the task it files;
- a schedule that runs a suite, and editing it;
- template inputs used from the Use… form, a schedule and a suite;
- the suite form's mode switch;
- the composition preview's wording on every form that has one.

## Environment

| | |
| --- | --- |
| Commit tested | `0fa005e3a` (candidate tip, "Merge pull request #3102"), then this branch's commits on top |
| Branch | `grain/task-2235` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 |
| Server | `grain demo -addr 127.0.0.1:<port> -open=false`, built from this tree (`npm run build` into `pkg/ui/static`, then `go build ./cmd/grain`), throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, set by an init script |
| Viewport | 1400×900 |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

`npx playwright install chromium` was run with
`NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` and succeeded
first time.

Exploration used throwaway Playwright scripts (`ui/.tmp-walk/`, excluded
through `.git/info/exclude` and not committed). They used
`chromium.launch()` and logged the following:

- every console error and warning;
- every page error;
- every failed request;
- every HTTP status ≥ 400, with its body;
- every non-GET request, with its body;
- every `confirm()`.

Each `confirm()` was accepted or dismissed as the step needed. No
screenshots or traces were kept.

Two demo servers were used:

- `:18080` ran the candidate build, for the walkthrough itself.
- `:18081` ran the binary and frontend with this branch's fixes, to
  verify them.

## Stubs

Every create, edit, approve, run, pause, resume and delete below was a
click or keystroke in Chromium against the real server. Nothing mocked
grain's API or React.

1. **Suite-run progression (orchestrator boundary).** `grain demo` runs no
   orchestrator. A throwaway Go helper (`.tmp-suitestub/`, excluded and
   not committed) opened the demo's SQLite store with `sqlite.Open` and
   called the same store methods the orchestrator calls:
   - **Run 2** (`walk-suite-2235`, which requires approval). Task 17 was
     approved first through its task page's Approve button. The helper
     then called:
     - `StartRun` + `FinishRun(17, "error", "stub: agent exited 1")`;
     - `CompleteSuiteRun(2, failed, "pass 1 task 17 ended in error (stub)")`.
       The stub wrote that reason text.
   - **Run 1** (Release Qualification Suite, started by a schedule's Run
     now):
     - `StartRun` + `FinishRun(success)` for tasks 14 and 15;
     - `FireNextPass(1)`, which filed tasks 20 and 21 in pass 2;
     - `StartRun(20)`, so that task reads as running.
   - **Run 3** (declared Smoke suite): `StartRun` + `FinishRun(19,
     success)`, then `CompleteSuiteRun(3, succeeded)`.

   The stub only finishes runs and never closes tasks, so the tasks keep
   badging "Queued" in a finished run. The orchestrator would have
   settled them.
2. **Agent follow-up proposal (agent boundary).** `POST /api/tasks` with
   `{"proposedBy": "14", "status": "proposed"}` filed task 22. This is the
   request an agent's `propose_task` becomes.
3. **Repository declarations.** These come from the demo's own seed
   (`cmd/grain/demo.go`, `SyncRepoDeclarations` for `acme/widgets`). No
   GitHub poll was involved.

## Steps and outcomes

Unless a step says otherwise, outcomes were checked after a reload, by
deep link, and against the API's answer.

### Schedules (`/schedules`)

- **Fully populated task-filing schedule (`sched-3`, "walk-2235 weekly
  raw").** The form was filled in as follows:
  - `acme/widgets` @ `release/1.0`, with "also writes"
    `grain/config-default` and reads `acme/docs`;
  - a two-line description and auto-merge on;
  - network "None (this deployment only)" and nothing-needed "Leave for
    acknowledgement";
  - capabilities Gemini key + Memory;
  - every custom field: priority high, area frontend, requested-by,
    due date, estimate 2, customer-facing Yes;
  - Weekly, Wednesday 07:45, with overlap "Start it anyway".

  The POST carried every value. The row read "Wednesdays at 07:45 · Can
  overlap · Next run 10/7/2026, 7:45:00 AM". Reopening
  `/schedules/sched-3` after a reload showed every field as saved.
- **Run now.**
  - The confirmation named the task.
  - The filed task 13 carried the schedule's base, reads, writes,
    network, nothing-needed action, capabilities and every custom field,
    with `scheduled: true`.
  - The row gained "· last ran …".
- **Pause, run while paused, resume, delete.**
  - Pause read "Paused · Held -- fires 10/7/2026, 7:45:00 AM if resumed
    by then".
  - Run now while paused fired and updated "last ran".
  - Resume put "Next run" back.
  - Dismissing the delete confirmation ('Delete the schedule "walk-2235
    weekly raw"? Tasks it already filed are not affected.') kept the
    schedule. Accepting it deleted the schedule.
  - A deep link to the deleted id fell back to the list with 'no
    schedule "sched-3"'.
- **Suite-running schedule (`sched-4`).**
  - With Fires = A suite, the form drops the content fields and says the
    branch is "required: a suite run stacks its tasks against one
    branch".
  - Add with the branch blank was blocked by the browser ("Please fill
    out this field.").
  - Daily 03:10 Asia/Tokyo gave "Next run 10/7/2026, 6:10:00 PM", which
    is 03:10 JST on 10/8.
  - The preview, once a suite was picked, listed "2 passes of these
    tasks" with each template's card.
  - Run now ('…starts a run of the suite "Release Qualification Suite"
    straight away…') created suite run 1 with `scheduleId: sched-4`. The
    run's pane and list row say "Scheduled".
- **Editing a suite schedule (second server).**
  - The editor has no Fires picker, so a schedule cannot switch between
    filing a task and running a suite once created.
  - Switching its suite from Smoke to Release Qualification saved, and
    the row and title followed the new suite.
  - Run now twice with overlap "Skip this run": the first started run 1.
    The second was refused with a 409, and the pane showed "its previous
    run has not finished yet, and this schedule is set to skip a run that
    would overlap one -- wait for it, or set "If the last run has not
    finished" to start it anyway".
- **Escape in an open picker.**
  - Pressing Escape on the Area autocomplete after its popup has closed
    reaches the pane.
  - The pane asks "You have unsaved changes here. Leave and discard
    them?" before closing, so no input is lost silently.

### Templates (`/templates`)

- **Fully populated template (`template-3`, `walk-tpl-2235`).**
  - Title "Audit {{service}}" and description "Check {{service}} at level
    {{level}}."
  - Two inputs:
    - `service`: label Service, default `api`, required;
    - `level`: options low/high, default high.
  - Reads `acme/docs`, auto-merge on, network Egress, nothing-needed ack,
    model gemini-2.5-flash, capability Self debug, priority low,
    estimate 3.

  Saved and listed.
- **Edit and reopen.**
  - Added the label "Level", changed the description and added area
    infra. The PATCH omits an unchanged model, and the server kept it.
  - After a reload every value was back.
  - Setting Model back to "Team default" and saving cleared it on the
    server.
- **Use… (template row).**
  - The form asks for Service (prefilled `api`) and Level (a picker).
  - A blank required Service was blocked by the browser.
  - Filing with Service `billing`, Level low and no repo chosen opened
    `/tasks/16`, "Audit billing", in `acme/widgets`, the deployment's
    default repo. It carried the template's reads, network, capability
    and fields.
  - The preview had said "the repo this run targets" for that repo.
    **Bug 3.**
- **From a schedule.**
  - Picking `walk-tpl-2235` in a new schedule hides the content fields.
  - The preview showed the defaults filled in ("Audit api", "Check api
    at level high.").
  - The schedule still cannot set the template's inputs (carried, see
    below).
- **From a suite.** The suite's preview uses the same defaults.
- **Dependency-aware delete.**
  - The confirmation reads 'Delete the template "walk-tpl-2235"?
    Schedules and suites using it must be repointed or deleted first.'
  - Each refusal was shown in the pane, which stayed open:
    - `walk-tpl-2235`, which a suite used, was refused with "template is
      used by 1 suite(s); remove it from those first".
    - `release-qualification` was refused with "… 1 schedule(s); repoint
      or delete those first".
    - Suite `suite-1`, which a schedule ran, was refused with "suite is
      used by 1 schedule(s)…".
  - None of the refusals said which schedule or suite was in the way.
    **Bug 4.**
  - After the dependents were deleted, the templates deleted cleanly.

### Suites (`/suites`)

- **Create (`suite-2`, `walk-suite-2235`).**
  - Picked `walk-tpl-2235`, then "+ New template" for an inline
    `walk-inline-2235`.
  - The inline form replaces the suite form. Saving it returned to the
    suite with the name and the first template intact, and the new
    template appended.
  - Max passes 0 was blocked by the browser. 1,000,000 was accepted (the
    "no upper bound" note is carried).
- **Edit.**
  - Switched the mode to "Run a fixed number of times". The "Number of
    times" box showed 1000000, the max passes, while the preview beside
    it said "one pass".
  - On a fresh suite (max passes 5), the same switch showed 5. Pressing
    Add without touching the box POSTed `"count":1`, so the suite saved
    as run 1×. **Bug 1.**
  - Count 3, auto-merge off and approval on saved. The row reads "run 3×
    · 2 templates · Requires approval", and reopening showed every value.
- **Preview wording.**
  - The "What this files" preview labelled its model row "model", the
    only lowercase term.
  - It said "none" for a model left to the team, and credited it to
    "team".
  - The Use… form's own choices were credited to "filing".
  - It printed stored values ("open", "ack") where the form words them
    "Egress (outbound internet only)" and "Leave for acknowledgement".
  - In a suite it said an unset branch was "the repo's default branch",
    although every suite run names a branch and its tasks take it.
  - **Bug 2.**
- **Delete.** Deleting the throwaway suites, with the confirmation
  accepted, removed them. `/suites/runs/2` still opened afterwards.

### Suite runs

- **From the suite row.**
  - Run… on `walk-suite-2235` opened "Run a suite" with that suite
    picked.
  - The branch helper says "every task from a template not bound to a
    repo of its own targets this branch".
  - Running it on `feature/walk-2235` gave run 2.
- **From the generic "+ Run".** The Suite picker listed all four suites,
  the declared one included. Smoke on `main` gave run 3.
- **Approval.**
  - Run 2's tasks badge "Proposed", and its pane says "each pass's tasks
    wait for approval".
  - Its task title link opened `/tasks/17`. Approve POSTed
    `/api/tasks/17/approve`, and Back returned to `/suites/runs/2`.
- **Presentations, after the stubs.**
  - `/suites/runs/1` reads "Active · Scheduled · run 2×".
    - Pass 1 (3): task 14, with follow-up 22 nested under it and counted
      in the pass, then task 15.
    - Pass 2 (2): task 20, badged "Running", then task 21.
  - `/suites/runs/2` reads "Failed", with "pass 1 task 17 ended in error
    (stub)".
  - `/suites/runs/3` reads "Succeeded · run until clean (max 3)".
  - Each badge has an accessible name. The running badge's moving mark is
    an image inside the named badge, and `role="img"` makes it
    presentational, by design (`TaskList.jsx`).
- **Missing ids.** `/suites/runs/99`, `/suites/suite-99` and
  `/templates/template-99` each fall back to their list with "no suite
  run / suite / template …".

### Repository-declared rows (`docs/declarations.md`)

The declared template, suite and schedule were each opened by encoded
deep link. Each row reads "Declared by acme/widgets". Each editor says,
for example: "This suite is declared by acme/widgets in
.grain/suites/smoke.yml, so it cannot be edited or deleted here. Change
that file in the repository and grain picks it up within a couple of
minutes. It can still be run from the suites list." The schedule's note
adds "Run now still fires it once". Delete is disabled on all three.

### Keyboard

- From the suites search box, four Tabs reach the first row's "Run…".
- Enter opens "Run a suite", with focus in the dialog's container.
- Escape closes it and returns focus to "Run…".
- In the New schedule pane, Tab walks every control in order (pickers,
  text boxes, Cancel, Add schedule) and wraps back to "← Schedules".

### Console and network

- **No uncaught page errors** on any page.
- `GET /api/repos/<repo>/github-branches` returns 404 whenever a repo is
  picked. There is no GitHub behind the demo, so this is expected.
- `GET /api/tasks` reports `net::ERR_ABORTED` on navigation and while
  pickers open: a poll cancelled by the page change, as on earlier days.
- Every 400 and 409 was a refused save, delete or run described above,
  and each was shown in the UI.

## Bugs found and fixed

1. **The suite form's count box showed the wrong number after a mode
   switch, and saved a different one.**
   - *Reproduction:* on `/suites`, open "+ New suite", name it, pick a
     template, and leave Max passes at 5. Switch Run mode to "Run a
     fixed number of times". The "Number of times" box shows 5 and the
     preview says "one pass". Press Add suite: the POST carries
     `"count":1`.
   - *Cause:* `SuiteOverlay.jsx` swaps two uncontrolled TextFields in
     one ternary slot without a `key`, so React reuses one `<input>` and
     its DOM value.
   - *Fix:* each box is keyed and opens on the value the form holds for
     it.
   - *Coverage:*
     - a `SuitesList.test.jsx` case;
     - e2e scenario 12 in `recurring-composed-walkthrough.spec.js`.

     Both fail with the fix reverted. The e2e read 7, where 1 was
     expected.
   - *Verified* on `:18081`: the box reads 1 and saves 1.
2. **The composition preview printed wire names.**
   - *Symptoms:*
     - the Model row was labelled "model";
     - an unset model read "none", credited to "team";
     - the Use… form's choices were credited to "filing";
     - stored values read "open" and "ack";
     - an unset branch in a suite read "the repo's default branch".
   - *Fix:* `CompositionPanel.jsx` now does the following:
     - labels the Model row;
     - says "the team's default model";
     - words the team and filing layers ("the team decides", "chosen
       here");
     - reads network and nothing-needed values the way their pickers
       do;
     - says "the branch this run targets" for a suite's unset branch.
   - *Coverage:* `CompositionPanel.test.jsx` cases, which fail with the
     fix reverted, and e2e scenario 12.
3. **The Use… preview said an unchosen repo was "the repo this run
   targets".**
   - *Cause:* a filing is not a run. `Client.CreateTask` sends a task
     with no repo to the deployment's default target repo.
   - *Fix:* the preview now says "the deployment's default repo" for
     that case.
   - *Coverage:* a `CompositionPanel.test.jsx` case.
4. **A refused delete gave a count but did not say which schedule or
   suite was in the way.** This was noted on 10-04 and 10-05.
   - *Fix:* `DeleteTemplate` and `DeleteSuite` (`pkg/ui`) now name each
     dependent by title, or by id for an untitled schedule. For example:
     'template is used by 2 suite(s) ("Release Qualification Suite",
     "verify-2235"); remove it from those first'.
   - *Team scope:* only dependents in a team the caller can read are
     named. Others are counted as "N in teams you cannot see", so a
     refusal gives away no more than the lists do.
   - *Coverage:*
     - the three existing refusal tests in `pkg/ui`
       (`TestDeleteTemplateRefusesWhileASchedulePointsAtIt`,
       `…WhileASuiteStillUsesIt`, `TestDeleteSuiteRefusesWhileAScheduleStillRunsIt`)
       now check the name, and fail with the fix reverted;
     - `TestARefusedTemplateDeleteDoesNotNameAnotherTeamsSchedule`
       (`pkg/ui/cross_team_template_test.go`) seeds a red-team schedule
       on a blue template through the store, since the API refuses to
       wire one. A blue admin's refusal names blue's schedule, counts
       red's, and never prints red's title. It fails with the fix
       reverted;
     - e2e scenario 12.

     Scenario 1's pattern was updated for the new wording.
   - *Verified* on `:18081` for both the template and suite refusals.

## Found and not fixed

- **A schedule cannot set a template's inputs.** A template-backed
  schedule or suite always files the inputs' defaults. Carried from
  10-04; it needs a new stored field, so it is a feature rather than a
  fix.
- **A schedule cannot switch between filing a task and running a suite
  once created.** The editor has no Fires picker. It is unclear whether
  this is intended.
- **A suite run started by a schedule says "Scheduled" but does not link
  to that schedule.**
- **Duplicate template names are accepted.** Two templates both named
  `walk-inline-2235` were saved. The pickers fall back to "name — title
  (id)", so they stay distinguishable.
- **Carried from earlier runs:**
  - Every-N-hours and pass counts have no upper bound (1,000,000 passes
    was accepted).
  - The follow-up filed with `proposedBy` carries the team's
    capabilities.
  - Timestamps are printed in the deployment's zone without naming it.

## Clean-up

The throwaway schedule, suites and templates were deleted through their
editors' Delete buttons, with confirmations accepted. Only the seeded
rows remained. Suite runs and tasks cannot be deleted and stay in the
throwaway store, which is discarded with the demo.

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/ui -run 'TestDeleteTemplate\|TestDeleteSuite\|TestARefusedTemplateDelete'` | pass; all four fail with the fix reverted |
| Vitest: `CompositionPanel`, `SuitesList`, `TemplatesList`, `SchedulesList` | 146 / 146 pass; the new cases fail with the fixes reverted |
| Playwright `recurring-composed-walkthrough`, `schedules_workflows`, `suites_workflows`, `templates_workflows` (chromium) | 15 / 15 pass; scenario 12 fails on the unfixed build |
| grain CI (full required suite) on the pushed branch | see the pull request |
