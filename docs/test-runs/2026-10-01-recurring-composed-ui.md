# Recurring and composed work UI walkthrough: 2026-10-01

grain/task-1358. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server, with
bugs fixed on the same branch. It follows
[2026-09-30-recurring-composed-ui.md](2026-09-30-recurring-composed-ui.md).
That run recorded the declared-row refusals as working because the server
answered 400 after Save or Delete was clicked. This run treats that as the
bug: the UI should say a row is declared before anyone tries to edit it.

## Environment

| | |
| --- | --- |
| Commit tested | `ca6e69c2f` (candidate tip, "Merge pull request #2500"), then this branch's own commits on top |
| Branch | `grain/task-1358` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 / 10.8.2 |
| Browser | Playwright 1.62.1, Chromium 151.0.7922.34 (chromium-1234, headless shell) |
| Server | `grain demo -addr 127.0.0.1:18080 -open=false` built from this tree (`go build ./cmd/grain`), frontend built with `npm run build` into `pkg/ui/static`, throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, the demo's seeded user (the same thing `e2e/pageerrors.js` does) |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

Setup notes for the next run:

- `npx playwright install chromium` fails with `SELF_SIGNED_CERT_IN_CHAIN`
  behind the sandbox's package proxy. Run it with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`.
- `go build ./cmd/grain` embeds whatever is in `pkg/ui/static` when it runs.
  Build the frontend first or the server serves only `placeholder.html` and
  every route 404s.
- Without `grain.username` set, a first visit opens the create-account
  dialog over everything (by design). An unknown username gets 403 on every
  API call ("does not have access to this server"). `alice` is the seeded
  user.

## Stubs

Everything below went through the real server's own code paths. Nothing
mocked the API or the React UI.

1. **Suite-run progression (orchestrator boundary).** `grain demo` runs no
   orchestrator, so a run never leaves pass 1 or `active` by itself. A
   throwaway Go helper (not committed; it lived in an ignored `.tmp-suitestub/`)
   opened the demo's SQLite store and called the store methods the
   orchestrator's `SyncSuites` calls: `Store.FireNextPass` (to put a run on
   pass 2), `Store.CompleteSuiteRun(..., SuiteRunSucceeded, "")`, and
   `Store.CompleteSuiteRun(..., SuiteRunFailed, "<reason>")`. After each
   call the browser was reloaded to read the result.
2. **Agent follow-up proposal (agent boundary).** A proposed follow-up was
   filed with `POST /api/tasks {"proposedBy": "<suite task id>", "status":
   "proposed"}`. That is the request an agent's `propose_task` becomes,
   so it stands in for an agent filing one.
3. **Repository declarations.** These come from the demo's own seed
   (`cmd/grain/demo.go`, `store.SyncRepoDeclarations` for `acme/widgets`:
   `.grain/templates/nightly.yml`, `.grain/suites/smoke.yml`,
   `.grain/schedules/nightly.yml`). No GitHub poll was involved.

## Steps and outcomes

Unless a step says otherwise, every create, edit or run was a click or
keystroke in Chromium, and every outcome was checked after a reload and,
where the page has its own URL, by deep-linking to it.

### Templates (`/templates`)

- **Sort and search.** Name (A–Z), Newest first, Oldest first and Custom
  order each reordered the rows as expected. A search with no matches shows
  "No templates match your search."
- **Create, fully populated.** Filled in name, a title and description with
  `{{service}}`, and an input (`service`, label, options `api`/`web`,
  default `api`). Bound the template to `acme/widgets` on `main`, added a
  write repo, a read repo, auto-merge, network `none`, a capability
  (`gcp-key`), and the custom fields priority `high` and requested-by. All
  of it persisted (checked in the `GET /api/templates` body).
- **Write repo off the allowlist.** Choosing `acme/docs` as a write repo
  was refused with a clear message ("acme/docs is not on this deployment's
  target repo list …"). The form kept its values. Correct behaviour.
- **Reopen and edit.** Deep link `/templates/template-3` plus a reload
  pre-filled every field. Editing the title and saving persisted across a
  reload.
- **Use…** opened the use form with the input defaulted. "Create task"
  filed the task and navigated to it, with the input substituted into the
  title (`Audit api …`).
- **Dependency-aware delete.** While a schedule used the template, delete
  was refused with "template is used by 1 schedule(s); repoint or delete
  those first". For `security-audit`, which a suite uses, it was refused
  with "template is used by 1 suite(s); remove it from those first". After
  deleting the schedule and the throwaway suite, the throwaway templates
  deleted cleanly. Only the seeded three remained.

### Schedules (`/schedules`)

- **Validation.** "Add schedule" with an empty title and "Every N hours" set
  to 0 was blocked by the browser's own validation on both fields. A
  suite-backed schedule without a base branch was blocked on the required
  base (the form says "required: a suite run stacks its tasks against one
  branch").
- **Task schedule with its own content.** Weekly, America/New_York, overlap
  "Start it anyway", repo `acme/widgets`, base `main`, a description, one
  write and one read repo, network "Egress", priority `low`. Persisted as
  `{"kind":"weekly","timeOfDay":"09:00","weekday":"monday","timeZone":"America/New_York"}`
  with next run 13:00Z (09:00 EDT). The row shows "Can overlap".
- **Template-backed schedule.** Daily, from the walkthrough template. The
  row shows "Template: walk-tmpl-…" and the next run.
- **Suite-backed schedule.** Monthly, Release Qualification Suite,
  `acme/widgets@main`. The row shows "Suite: Release Qualification Suite"
  and "Monthly on day 1 at 09:00". The preview read "2 passes of these
  tasks".
- **Lifecycle.**
  - Pause added a "Paused" chip that survived a reload. The editor then
    offered "Resume", and resuming cleared the chip.
  - Run now: dismissing the confirmation left the editor open with nothing
    fired. Accepting it added "last ran …" to the row, and the filed task
    appeared in the backlog.
  - Run now on the suite-backed schedule started a run marked "scheduled"
    in the runs list.
  - Delete: dismissing the confirmation kept the row. Accepting it removed
    the row, which stayed gone after a reload.
- **Preview.** "What this schedule files" followed every edit, labelled "as
  filled in above" for a new schedule and "as edited above, not as saved"
  for an existing one.

### Suites (`/suites`)

- **Create.** Picked the walkthrough template, then made a second template
  inline with "+ New template". It was selected in the suite straight away
  and the suite's name was kept. Mode "until clean" with max 2 passes and
  "require approval". The row read `run until clean (max 2) | 2 templates |
  requires approval`.
- **Search and sort.** Search narrowed the rows. "Most templates" reordered
  them.
- **Edit.** Changed to "fixed number" with 3 passes, approval off and
  auto-merge off. After a reload the row read `run 3× | 2 templates`.
- **Delete.** Dismissing the confirmation kept the suite. Accepting it
  deleted the suite, and runs already started from it stayed listed.

### Suite runs

- **Starting runs.** Started one from the suite row's "Run…" and one from
  the generic "+ Run" (pick suite, repo, branch). Both appeared in the runs
  list as `active` and opened at `/suites/runs/<id>`, including on a reload
  of the deep link. Unknown ids (`/suites/runs/9999`, `/schedules/sched-404`)
  fall back to the list.
- **Pass grouping.** After stub 1 put the walkthrough run on pass 2, the
  pane showed "Pass 1" and "Pass 2" groups of two tasks each, with the
  template's defaults substituted, its custom fields, and its read/write
  repo and capability chips.
- **Finished and failed.** After stub 1 completed the runs, the walkthrough
  run read `succeeded` and the Release Qualification run read `failed`.
  The failed run showed its reason in both the runs list and the pane.
- **Follow-ups.** The proposed follow-up from stub 2 is in the backlog as
  `proposed`, linked with `proposedBy`.
- **Task detail.** Clicking a task row in the pane opened `/tasks/<id>`, and
  Back returned to the run.

### Repository-declared rows (`docs/declarations.md`)

The seeded declared template, suite and schedule were opened from their
rows and by deep link (`/templates/repo:template:acme:widgets:nightly` and
so on). See bugs 1 and 2.

### Console, network and keyboard

- **Console and network.** No uncaught page errors on any of these pages.
  The only recurring failed request is `GET
  /api/repos/acme/widgets/github-branches` → 404 "GitHub branch listing is
  not available on this UI server". The branch picker asks for it whenever
  a repo is chosen, and the demo has no GitHub behind it. It is logged to
  the console and is harmless here.
- **Keyboard.** See bug 3.

## Bugs found and fixed

1. **Declared rows looked editable and failed only when you tried (fixed).**
   - *Reproduction:* open `/templates`, click `nightly-maintenance`, edit
     the title, press Save.
   - *Before:* the row looked like any other. The editor said "Edit
     template" and offered Save, Delete and (for the schedule) Pause, and
     each one answered 400 ("… is declared by acme/widgets in
     .grain/templates/nightly.yml, so it is not this deployment's to
     edit"). The API had always sent `declaredIn {repo, path}`; the
     frontend never read it.
   - *After:* each declared row carries a "Declared by acme/widgets" chip
     whose tooltip names the file. The editor opens as "Template", "Suite"
     or "Schedule" with a notice: "This template is declared by
     acme/widgets in .grain/templates/nightly.yml, so it cannot be edited
     or deleted here. Change that file in the repository and grain picks it
     up within a couple of minutes." Save, Delete and Pause are disabled,
     Cancel reads "Close", and Enter in a field submits nothing.
   - *What still works:* Run now on a declared schedule, which the server
     allows because it is a firing rather than an edit. A declared suite
     can still be run.
   - *Coverage:* `TemplatesList.test.jsx` and `SchedulesList.test.jsx`
     cases, and scenario 5 of `e2e/recurring-composed-walkthrough.spec.js`,
     rewritten. It asserts the chip, tooltip, notice, disabled buttons and
     reload/deep-link behaviour, and checks that the UI sends no
     PATCH/DELETE for a declared id.
2. **Running a declared schedule renamed it (fixed, server).**
   - *Reproduction:* `POST /api/schedules/repo:schedule:acme:widgets:nightly/run`,
     or Run now in the UI. The row changes from "Nightly Repo Maintenance
     Sweep" to its template's title, "Declared Nightly Maintenance".
   - *Cause:* every firing refreshes a schedule's title from the template
     or suite it fires (a display cache). A declared schedule's title is
     its own `name:` from the file (repodecl's `parseSchedule`), so every
     firing renamed it until the next poll of the repository named it back.
   - *Fix:* `Store.recordFiring` keeps a declared schedule's title.
   - *Coverage:* `TestRunningADeclaredScheduleKeepsItsDeclaredName` in
     `pkg/ui/declarations_test.go`, which fails without the fix ("title":
     "Bump dependencies"), and the "last ran" check in e2e scenario 5.
3. **Rows could not be opened from the keyboard (fixed).**
   - *Reproduction:* on `/schedules`, press Tab repeatedly. Focus goes from
     the sort control through each row's drag handle and back to the page,
     and never lands on a row. The same holds on `/templates` and
     `/suites` and for suite-run rows. Rows are `div`/`li` with only an
     `onClick`.
   - *Fix:* a shared `openableRowProps` in `ListPrimitives.jsx` makes those
     rows focusable and opens them on Enter or Space pressed on the row
     itself. Buttons inside the row ("Use…", "Run…", the drag handle) keep
     their own keys.
   - *Coverage:* a `TemplatesList.test.jsx` case (Enter on "Use…" does not
     open the editor; Enter on the row does) and e2e scenario 7 (all three
     lists plus a run row, opened from the keyboard against the real
     server).
4. **Small inconsistencies (fixed).**
   - The schedule editor labelled network `open` as "Open (reach the
     internet)", while the template and new-task forms say "Egress
     (outbound internet only)" for the same value. It now matches.
   - The template delete confirmation mentioned only schedules, though
     suites block it too. It now says "Schedules and suites using it …".
   - The Suites header counted every suite while a search was active,
     unlike Templates and Schedules. It now counts the matching rows
     (`SuitesList.test.jsx` case).

## Found and not fixed (proposed as follow-ups)

- **Task rows anywhere cannot be opened from the keyboard.** This includes
  the main task list and the suite-run pane. In the pane, Tab only ever
  reaches "← Suites". The row is the shared `TaskRow` in `TaskList.jsx`,
  used across the whole app, so the change is broader than this area. It
  is proposed as its own task.
- **A template-backed schedule's row shows the template's raw
  placeholders.** For example, "Audit {{service}} 11287", while every task
  it files reads "Audit api 11287" because the input's default is filled
  in. The title the store caches for the schedule is the unfilled template
  title, both when it is created and at every firing. Proposed as its own
  task.
- **Some selects include their current value in their accessible name.**
  For example "Network access Deployment default", "Priority medium" and
  "Time zone UTC (default)", where "Repeat" and "Run mode" are named by
  their label alone. Low impact; noted only.

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/ui/ -run 'Declared\|Declares'` | pass; the new test fails without the store fix |
| `go test ./pkg/ui/... ./pkg/model/... ./pkg/repodecl/... ./pkg/orchestrator/...` | all pass |
| Vitest, full suite (`npx vitest run`) | 156 files, 2683 tests pass |
| Playwright `e2e/recurring-composed-walkthrough.spec.js` (chromium) | 7 / 7 pass |
| Playwright `templates_workflows`, `schedules_workflows`, `suites_workflows`, `team_declarations_ci_walkthrough` with the walkthrough (chromium, 3 workers) | 11 / 11 pass |
| grain CI (full required suite) on the pushed branch | see the pull request |
