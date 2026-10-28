# Recurring and composed work UI walkthrough: 2026-10-07

grain/task-2415. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server, with
the bugs it found fixed on the same branch. It follows
[2026-10-06-recurring-composed-ui.md](2026-10-06-recurring-composed-ui.md);
that run's four fixes all held (the suite count box, the preview's
wording, the default-repo note, and named dependents in a refused delete).

This run went after what the earlier ones had left open or not tried:

- monthly and time-zoned recurrences, and editing a schedule's cadence
  and zone;
- the sort menus and search on all three lists;
- a template with a required input that has no default, used from a
  schedule and a suite;
- where a scheduled suite run's "Scheduled" mark leads;
- a template held by a schedule and a suite at once.

## Environment

| | |
| --- | --- |
| Commit tested | `a63ae6f4b` (candidate tip, "Merge pull request #3121"), then this branch's commits on top |
| Branch | `grain/task-2415` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 |
| Server | `grain demo -addr 127.0.0.1:<port> -open=false`, built from this tree (`npm run build` into `pkg/ui/static`, then `go build ./cmd/grain`), throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, set by an init script |
| Viewport | 1400×900, browser zone UTC |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

`npx playwright install chromium` was run with
`NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` and succeeded
first time.

Exploration used throwaway Playwright scripts (`ui/.tmp-walk/`, excluded
through `.git/info/exclude` and not committed). Each launched Chromium
and logged every console error and warning, page error, failed request,
HTTP status ≥ 400 with its body, non-GET request with its body, and
`confirm()` (accepted or dismissed as the step needed). No screenshots or
traces were kept.

Two demo servers were used:

- `:18080` ran the candidate build, for the walkthrough itself.
- `:18081` ran this branch's first two fixes, to check them by hand.

## Stubs

Every create, edit, approve, run, pause, resume and delete below was a
click or keystroke in Chromium against the real server. Nothing mocked
grain's API or React.

1. **Suite-run progression (orchestrator boundary).** `grain demo` runs no
   orchestrator. A throwaway Go helper (`.tmp-suitestub/`, excluded and
   not committed) opened the demo's SQLite store with `sqlite.Open` and
   called the store methods the orchestrator calls:
   - **Run 2** (Release Qualification Suite, started from "+ Run"):
     `StartRun` + `FinishRun(success)` for tasks 16 and 17,
     `FireNextPass(2)` (filed tasks 18 and 19 in pass 2), then
     `StartRun(18)` so that task reads as running.
   - **Run 1** (`walk-suite-2415`, approval required, started from its
     row): task 14 was approved through its task page first; then
     `StartRun` + `FinishRun(14, "error", "stub: agent exited 1")` and
     `CompleteSuiteRun(1, failed, "pass 1 task 14 ended in error (stub)")`.
     The stub wrote that reason text.
   - **Run 3** (declared Smoke suite, started by a schedule's Run now):
     `StartRun` + `FinishRun(21, success)`, then
     `CompleteSuiteRun(3, succeeded)`.

   The stub never closes tasks, so they keep badging "Queued" in a
   finished run; the orchestrator would have settled them.
2. **Agent follow-up proposal (agent boundary).** `POST /api/tasks` with
   `{"proposedBy": "16", "status": "proposed"}` filed task 20, the
   request an agent's `propose_task` becomes.
3. **Repository declarations.** From the demo's own seed
   (`cmd/grain/demo.go`, `SyncRepoDeclarations` for `acme/widgets`). No
   GitHub poll was involved.

## Steps and outcomes

Outcomes were checked after a reload, by deep link, and against the
API's answer unless a step says otherwise.

### Schedules (`/schedules`)

- **Monthly in a zone (`sched-3`).** Repeat "Monthly", day 31, 23:30,
  zone Pacific/Auckland, overlap "Start it anyway". The POST carried
  `{"kind":"monthly","timeOfDay":"23:30","dayOfMonth":31}`,
  `"timeZone":"Pacific/Auckland"`, `"overlap":"allow"`. The row read
  "Monthly on day 31 at 23:30 Pacific/Auckland · Can overlap · Next run
  10/31/2026, 10:30:00 AM", which is 23:30 NZDT.
- **Cadence edit.** Reopening showed the zone, day and time as saved.
  Changing to Weekly, Friday, zone "UTC (default)" sent
  `"timeZone":""`, and after a reload the row read "Fridays at 23:30 ·
  Can overlap · Next run 10/9/2026, 11:30:00 PM". Reopened, every picker
  held the new value. Escape on an unedited editor closed it without
  asking.
- **Every N hours validation.** 0 was blocked by the browser ("Value must
  be greater than or equal to 1."), 1.5 likewise ("The two nearest valid
  values are 1 and 2."). 100000 was accepted, giving "every 100000h ·
  Next run 3/4/2038" (no upper bound; carried from earlier runs).
- **Pause, Run now, Resume.** Pause and Resume each PATCH `enabled` and
  close the pane. The paused row read "Paused · Held -- fires 10/9/2026,
  11:30:00 PM if resumed by then". Run now while paused asked 'Run
  "walk-2415 monthly" now? It starts the task "walk-2415 monthly"
  straight away; the schedule's own next run is unchanged.'; dismissing
  it sent nothing, accepting it filed task 13 (`scheduled: true`) and the
  row gained "· last ran 10/7/2026, 3:51:37 AM". Resume put "Next run"
  back with "last ran" kept.
- **Template-backed schedules.** Picking a template drops the content
  fields; the preview credits each row to "from the template", and the
  POST is `{"templateId":…,"repo":"","base":"",…}`.
- **Suite-running schedule (`sched-7`).** Fires "A suite", Smoke, branch
  `main`, every 6 hours. Run now asked '…starts a run of the suite "Smoke
  Verification Suite" straight away…' and started run 3 with
  `scheduleId: sched-7`.
- **Delete.** Each throwaway schedule was deleted from its editor;
  confirmations read 'Delete the schedule "…"? Tasks it already filed are
  not affected.' (or "Suite runs it already started are not affected."
  for a suite schedule).

### Sort and search

| List | Sort options | Result |
| --- | --- | --- |
| Schedules | Custom order, Title (A–Z), Next run, Newest first, Oldest first | each ordered as named; "Next run" put the 2038 schedule last |
| Templates | Custom order, Name (A–Z), Newest first, Oldest first | as named |
| Suites | Custom order, Name (A–Z), Most templates | as named |

The sort choice is not kept across a reload on any list (each returns to
Custom order). Search is case-insensitive ("WALK" matched), matches the
repo, and on suites the declaring file (`smoke.yml`); a miss reads "No
schedules/templates/suites match your search."

### Templates (`/templates`)

- **Template with inputs (`template-3`, `walk-tpl-2415`).** Title "Audit
  {{service}} for 2415", description "Check {{service}} at {{level}}.",
  inputs `service` (label Service, Required, no default) and `level`
  (options low/high, default high), `acme/widgets` @ `main`, reads
  `acme/docs`, auto-merge. The POST carried every value.
- **Used from a schedule.** Accepted. The schedule lists as "Audit  for
  2415" and the preview reads "Audit for 2415" / "Check at high." -- the
  required input is filled with nothing and nothing says so. Run now
  would file that title. See *Found and not fixed*.
- **Used from a suite** (below): the same blank substitution in the run
  preview and in the filed task 15.
- **Dependency-aware delete.** While two schedules and suite-2 used it,
  the confirm read 'Delete the template "walk-tpl-2415"? Schedules and
  suites using it must be repointed or deleted first.' Dismissing sent
  nothing; accepting got a 400 naming only the two schedules -- the suite
  was never mentioned (bug 2 below).

### Suites (`/suites`)

- **Create with an inline template.** "+ New template" inside the suite
  form opened the template form over it; saving `walk-inline-2415`
  returned to the suite form with its name kept and the new template
  already picked.
- **Modes and flags.** `walk-suite-2415`: templates walk-inline-2415 and
  walk-tpl-2415, "Run a fixed number of times" 2, Require approval. The
  POST carried `"mode":"count","count":2,"requireApproval":true`. Edited
  to "Run until no repo changes" max 4 with auto-merge off; after a
  reload the editor showed exactly that.
- **Delete** with confirmation 'Delete the suite "walk-suite-2415"? Runs
  already started from it are unaffected.'; `/suites/runs/1` still
  opened afterwards.

### Suite runs

- **From the row.** Run… on `walk-suite-2415` opened "Run a suite" with
  the suite picked and a per-template preview ("Auto-merge off, from the
  suite"; "Approval waits for approval, from the suite"). Branch
  `feature/walk-2415` gave run 1. The task from the repo-bound template
  kept its own `main`, as the help text says.
- **From "+ Run".** The picker listed all three suites, the declared one
  included; Release Qualification on `main` gave run 2.
- **Approval.** Run 1's tasks badge "Proposed"; its title link opened
  `/tasks/14`, Approve POSTed `/api/tasks/14/approve`, and Back returned
  to `/suites/runs/1`.
- **Presentations after the stubs.**
  - `/suites/runs/1`: "Failed", with "pass 1 task 14 ended in error
    (stub)".
  - `/suites/runs/2`: "Active · run 2×". Pass 1 (3): task 16 with
    follow-up 20 nested under it, then 17. Pass 2 (2): task 18 badged
    "Running", then 19.
  - `/suites/runs/3`: "Succeeded · Scheduled · run until clean (max 3)".
    "Scheduled" did not say or link to which schedule (bug 1).
  - `/suites/runs/99` fell back to the list with 'no suite run "99"'.

### Repository-declared rows (`docs/declarations.md`)

The declared template, suite and schedule were opened by encoded deep
link. Each reads "Declared by acme/widgets" and its editor says, e.g.,
"This suite is declared by acme/widgets in .grain/suites/smoke.yml, so it
cannot be edited or deleted here. Change that file in the repository and
grain picks it up within a couple of minutes. It can still be run from
the suites list." (the schedule's adds "Run now still fires it once").
Delete and Save are disabled on all three; on the suite editor every
field also matched `:disabled` (the other two are covered by e2e
scenario 9).

### Keyboard

From the suites search box, Tab walks the sort menu, then each suite's
Move handle, name link and Run…, then "+ Run" and each run's link. Enter
on a run link opens its pane; Escape closes it and returns focus to that
link. The new Scheduled link is a real `<a>`, so it is in the tab order;
it was focused and followed with Enter.

### Console and network

- No uncaught page errors on any page.
- `GET /api/repos/acme/widgets/github-branches` returns 404 whenever a
  repo is picked; there is no GitHub behind the demo.
- `GET /api/tasks` reports `net::ERR_ABORTED` on navigation: a poll
  cancelled by the page change.
- Every other 4xx was a refusal described above, and each was shown.

## Bugs found and fixed

1. **A scheduled suite run did not say which schedule started it.**
   - *Reproduction:* create a suite-running schedule, Run now, open the
     run (row or `/suites/runs/<id>`). The "Scheduled" chip is plain
     text; the run's `scheduleId` is in the API answer but nowhere in the
     UI. (Also noted, unfixed, on 10-06.)
   - *Fix:* `ScheduledChip` (`SuiteRunPane.jsx`), used by the run's row
     and its pane, is a link to `/schedules/<id>`. A plain click opens
     the schedule through App's new `openSchedule` and does not also open
     the run; a modified click opens a new tab.
   - *Coverage:* a `SuitesList.test.jsx` and a `SuiteRunPane.test.jsx`
     case, both failing with the fix reverted; e2e scenario 13, which
     failed on the unfixed build ("element(s) not found" for the link).
   - *Verified* on `:18081`: Enter on the chip opened `/schedules/sched-3`.
2. **Following that link to a deleted schedule showed nothing.** Found
   while checking fix 1: after deleting the schedule, the link left the
   address at `/schedules/sched-3` over the bare list with no pane and
   no notice, while a deep link to the same id says 'no schedule
   "sched-3"'.
   - *Fix:* `openSchedule` holds the id the way a URL-named one is held
     and refetches the list, so `keepIfListed` drops it with the same
     notice and puts the address back to `/schedules`.
   - *Coverage:* e2e scenario 13 deletes the schedule and follows the
     link; that step fails without this change (the notice never
     appears).
3. **A refused template delete named only the first kind of dependent.**
   - *Reproduction:* a template used by a schedule and a suite; Delete
     says 'template is used by 2 schedule(s) (…); repoint or delete those
     first'. Repoint them and the next Delete is refused again, now on
     the suite.
   - *Fix:* `DeleteTemplate` (`pkg/ui/templates.go`) gathers schedules,
     qualification plans and suites before refusing and names them all:
     'template is used by 1 schedule(s) ("Sweep") and 1 suite(s)
     ("Nightly sweep"); repoint or delete the schedules and remove it
     from the suites first'. A single kind reads exactly as before.
   - *Coverage:* `TestDeleteTemplateNamesEveryKindOfDependentAtOnce`,
     failing with the fix reverted; e2e scenario 13.

## Found and not fixed

- **A schedule or suite on a template whose required input has no
  default files that input blank, silently.** By design in
  `model.TemplateField.Required` ("an unattended firing … carries the
  default (empty, if that is what the default is)"), but the person
  attaching the template is at a form and is told nothing. Proposed as a
  follow-up on `candidate` with the reproduction.
- **Carried from earlier runs:** a schedule cannot set a template's
  inputs; a schedule cannot switch between filing a task and running a
  suite once created; Every-N-hours and pass counts have no upper bound;
  duplicate template names are accepted; list sort choices are not kept
  across a reload.
- A suite switched from count to until-clean keeps its old `count` in the
  API answer (run 1 reports `"count":2` beside `"mode":"until_clean"`).
  Nothing reads it in that mode, so it is noted only.

## Clean-up

The throwaway schedules, suite and templates were deleted from their
editors with confirmations accepted; only the seeded rows remained.
Suite runs and tasks cannot be deleted and stay in the throwaway store,
discarded with the demo.

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/ui` | pass; the new refusal test fails with the fix reverted |
| Vitest: `SuiteRunPane`, `SuitesList`, `SchedulesList`, `TemplatesList`, `CompositionPanel`, `src/App*` | pass (315 + 154); the two new cases fail with the fix reverted |
| Playwright `recurring-composed-walkthrough` (13 scenarios), `schedules_workflows`, `suites_workflows`, `templates_workflows` (chromium) | 16 / 16 pass; scenario 13 fails on the unfixed build |
| grain CI (full required suite) on the pushed branch | see the pull request |
