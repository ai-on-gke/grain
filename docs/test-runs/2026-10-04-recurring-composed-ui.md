# Recurring and composed work UI walkthrough: 2026-10-04

grain/task-1816. An end-to-end walkthrough of schedules, templates, suites
and suite runs in a real browser against a real `grain demo` server, with
bugs fixed on the same branch. It follows
[2026-10-03-recurring-composed-ui.md](2026-10-03-recurring-composed-ui.md).
Everything that run fixed held: "Time (UTC)" without the doubled zone,
read-only declared editors, the template-backed Run now confirmation, the
suite-backed delete confirmation and schedule search by template or
suite. This run fixed the "paused schedule still shows Next run" item,
which the 10-02 and 10-03 runs had both left noted.

## Environment

| | |
| --- | --- |
| Commit tested | `0e5560d0f` (candidate tip, "Merge pull request #2818"), then this branch's own commit on top |
| Branch | `grain/task-1816` |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 |
| Server | `grain demo -addr 127.0.0.1:18080 -open=false`, built from this tree (`npm run build` into `pkg/ui/static`, then `go build ./cmd/grain`), throwaway SQLite store under a private `TMPDIR` |
| Browser identity | `localStorage["grain.username"] = "alice"`, set by an init script |
| Viewport | 1400×900 |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

`npx playwright install chromium` failed with `SELF_SIGNED_CERT_IN_CHAIN`
against the package cache's CDN interception until it was run with
`NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`. Worth baking
into the sandbox image or the e2e setup script.

Exploration used throwaway Playwright scripts (`ui/.tmp-walk/`, excluded
through `.git/info/exclude` and not committed). They used
`chromium.launch()` and logged every console error and warning, every page
error, every failed request, every HTTP status ≥ 400 with its body, every
non-GET request with its body, and every `confirm()`, which was accepted
or dismissed depending on the step. No screenshots or traces were kept.

Two demo servers were used. The first (`:18080`) ran the candidate build
for the walkthrough. The second (`:18081`) ran the rebuilt binary with
this branch's fix, to verify it, and was then killed on purpose to see
the server-down state.

## Setup outside the area under test

`POST /api/repos` added `acme/widgets-docs` and `acme/shared` to the
target repo list on the first server: the call the Repos page's own "add"
makes, so it is configuration, not a stub. Without it the demo only
targets `acme/widgets` and `grain/config-default`.

## Stubs

Every create, edit, run, approve and delete below was a click or
keystroke in Chromium against the real server. Nothing mocked grain's API
or React.

1. **Suite-run progression (orchestrator boundary).** `grain demo` runs no
   orchestrator. A throwaway Go helper (`.tmp-suitestub/`, excluded and not
   committed) opened the demo's SQLite store with `sqlite.Open` and called
   the store methods the orchestrator calls:
   - `Store.StartRun` for tasks 16 and 17, so they read as running.
   - `Store.FinishRun(stub-r17, "error", "stub: agent exited 1")`.
   - `Store.FireNextPass` on runs 2 and 1, putting both on pass 2. Run 1's
     pass-1 tasks were never approved, so firing its pass 2 is the stub's
     doing, not something SyncSuites would do.
   - `Store.CompleteSuiteRun(2, failed, "pass 2 never came back clean: task 17 ended in error")`.
     The reason text was written by the stub.

   The browser was reloaded after the calls to read the result.
2. **Agent follow-up proposal (agent boundary).** `POST /api/tasks
   {"proposedBy": "16", "status": "proposed"}` filed task 23, the request
   an agent's `propose_task` becomes, from suite task 16.
3. **Repository declarations.** These come from the demo's own seed
   (`cmd/grain/demo.go`, `SyncRepoDeclarations` for `acme/widgets`). No
   GitHub poll was involved.

## Steps and outcomes

Unless a step says otherwise, outcomes were checked after a reload, by deep
link, and against the API's answer.

### Baseline

`e2e/recurring-composed-walkthrough.spec.js` passed 9/9 on the candidate
build before anything else was touched.

### Templates (`/templates`)

- **Sort.** Custom order, Name (A–Z), Newest first and Oldest first each
  gave the expected order.
- **Search.** "SECURITY" (case-insensitive) → 1 row, "declared" → the
  declared template, "acme/widgets" → all 3, "zzzz" → "No templates match
  your search."
- **Create, fully populated (`walk-tmpl-1816`).** Title `Audit {{service}}
  for 1816`, a two-line description, input `service` / `Service` with
  options `api`, `web` (one per line) and default `api`, repo
  `acme/widgets` on `main`, write `acme/widgets-docs`, read `acme/shared`,
  auto-merge, network None, nothing-needed "Leave for acknowledgement",
  capabilities gcp key + self debug, priority high, area backend,
  requested-by `qa-1816`, due 2026-12-01, estimate 2.5, customer-facing
  Yes. The POST carried every one of them and the stored row
  (`template-3`) matched.
- **Escape.** Escape in the open Capabilities menu closed the menu only.
  Escape with the form dirty now asks "You have unsaved changes here.
  Leave and discard them?"; dismissing it kept the pane and every value
  (the 10-03 follow-up, `8af545853`, holds).
- **Reopen and edit.** `/templates/template-3` pre-filled every field,
  including the input's options and the read, capability and area chips.
  Title → "… (edited)", priority → low (PATCH 200); both held after a
  reload and on reopening.
- **Use….** The preview listed every field "from the template" with the
  default `api` substituted. Choosing `web` and Create task opened
  `/tasks/13`, "Audit web for 1816 (edited)", with repo, base, reads,
  writes, both capabilities, network `none`, nothing-needed `ack`,
  auto-merge and every custom field.

### Schedules (`/schedules`)

- **Validation** (browser-blocked, form kept open): empty title;
  Every N hours 0, -3 and 1.5; day of month 0 and 32; a suite schedule
  with no base branch ("required: a suite run stacks its tasks against one
  branch").
- **Accepted edges.** Every N hours = 10000 saved (next run 2027-11-24),
  and monthly on day 31 saved as "monthly on day 31 at 09:00" with the
  next run 10/31. `model.nextMonthly` clamps day 31 to a shorter month's
  last day; the row does not say so. Both noted only.
- **Raw task schedule, fully populated (`sched-5`).** Repo `acme/widgets`
  on `main`, write `acme/widgets-docs`, read `acme/shared`, auto-merge,
  network None, self debug, priority high, area infra, weekly Friday
  17:45 Europe/Berlin, Start it anyway. The time label read "Time
  (Europe/Berlin, GMT+2)". The preview showed every value "from the
  schedule" before save. The row read "fridays at 17:45 europe/berlin ·
  can overlap · Next run 10/9/2026, 3:45:00 PM" (15:45Z, correct for
  CEST).
- **Template-backed schedule (`sched-6`).** Picking `walk-tmpl-1816` hid
  the content fields, set the model to "The template's model", and
  previewed every field "from the template". Daily 03:15 saved with only
  `templateId`, recurrence and overlap in the POST.
- **Suite-running schedule (`sched-7`).** Fires: A suite, suite
  `walk-suite-1816`, `acme/widgets` @ `main`, every 12h. The preview
  showed "2 passes of these tasks" with each template's fields and the
  suite's auto-merge/approval.
- **Edit.** `sched-3` retitled and moved from every 10000h to daily 05:30:
  the PATCH retimed it to the next 05:30, as `UpdateSchedule` documents.
- **Pause / resume.** PATCH `{"enabled":false}` / `{"enabled":true}`, with
  no confirmation (reversible). The paused row read "paused · Next run
  10/4/2026, 5:30:00 AM", a firing that will not happen. **Bug 1.**
- **Run now.** The confirmation names what it starts ('It starts the task
  "Walk raw 1816 edited"' / 'It starts a run of the suite
  "walk-suite-1816"'). Dismissing it left the editor open and sent
  nothing; accepting it POSTed `…/run`, and the row gained "last ran …"
  with the next run unchanged. The suite schedule's Run now started run 3,
  marked "scheduled".
- **Delete.** Dismissing the confirmation kept `sched-4`; accepting
  deleted it, and `/schedules/sched-4` then corrected to the list with
  `no schedule "sched-4"`. The suite schedule's confirmation said "Suite
  runs it already started are not affected."

### Suites (`/suites`)

- **Sort** offers Custom order, Name (A–Z), Most templates.
- **Create (`walk-suite-1816`).** With no template, Add suite was refused
  "a suite needs at least one template", shown as an alert with the form
  kept. "+ New template" opened a stacked template editor; saving
  `walk-inline-1816` returned to the suite with it already chosen and the
  suite's name kept. Max passes 0 was browser-blocked; 101 was accepted
  (no upper bound, as 10-03 noted for the fixed count). Auto-merge is
  ticked by default, matching the hint.
- **Edit.** `/suites/suite-2` → "Run a fixed number of times" (2), Require
  approval on, auto-merge off. The PATCH carried them and the row read
  "run 2× · 2 templates · requires approval". Auto-merge off does not show
  on the row (noted only).

### Suite runs

- **From the suite row** ("Run…"): `walk-suite-1816` on `acme/widgets` @
  `main` → run 1, "pass 1 · run 2× · each pass's tasks wait for approval",
  tasks 14–15 `proposed`.
- **From the generic "+ Run"**: Run with nothing chosen was blocked.
  Release Qualification Suite on `acme/widgets-docs` @ `feature-x` → run 2.
  Both of its templates are bound to `acme/widgets` @ `main`, and the
  preview said so ("from the template"); the tasks filed (16, 17) were on
  `acme/widgets` @ `main`, matching docs/composition.md "Placement". The
  Branch box's helper said "every task this run files targets this branch
  … -- bwsalmon/agents#642's own stacking". **Bug 2.**
- **Presentations** (after stub 1, by direct URL and after a reload):
  - `/suites/runs/1`: "active · run 2× · each pass's tasks wait for
    approval", Pass 1 (2) and Pass 2 (2).
  - `/suites/runs/2`: "failed" with the reason under the heading, Pass 1
    (3) and Pass 2 (2); follow-up 23 indented under task 16 and counted in
    its pass.
  - `/suites/runs/3`: "scheduled", one pass.
  - The runs list shows status, repo, branch, "scheduled" and the failure
    reason.
- **Task detail.** Clicking follow-up 23 opened `/tasks/23`; Back returned
  to `/suites/runs/2`. In run 3, Tab to task 19's title link and Enter
  opened `/tasks/19`; Approve there POSTed `…/approve` and the task went
  `queued`.
- **Unknown ids.** `/suites/runs/999`, `/suites/runs/abc`, `/suites/nope`,
  `/templates/nope` and `/schedules/sched-4` (deleted) each corrected to
  their list with a snackbar naming the id.
- **Run of a deleted suite.** Run 1 stayed openable after suite-2 was
  deleted (still "active": no orchestrator settles it in the demo).

### Dependency-aware delete

Each delete asked "Delete the template "…"? Schedules and suites using it
must be repointed or deleted first."

- `walk-tmpl-1816` while `sched-6` and suite-2 used it: refused "template
  is used by 1 schedule(s); repoint or delete those first", editor kept
  open with the message. The seeded `release-qualification` was refused
  the same way.
- After `sched-6` and `sched-7` were deleted: "template is used by 1
  suite(s); remove it from those first".
- Suite delete: dismissing kept suite-2; accepting ("Runs already started
  from it are unaffected.") deleted it.
- Then both throwaway templates deleted cleanly, and `sched-3`/`sched-5`
  were deleted through the UI. Only the seeded rows remained: 3 templates,
  2 suites, 3 schedules.

### Repository-declared rows (`docs/declarations.md`)

- Each list marks its declared row "declared by acme/widgets", and the
  chip's title names the file: "Declared in acme/widgets,
  .grain/templates/nightly.yml", `.grain/suites/smoke.yml`,
  `.grain/schedules/nightly.yml`.
- Each editor opened by encoded deep link says "This … is declared by
  acme/widgets in <file>, so it cannot be edited or deleted here. Change
  that file in the repository and grain picks it up within a couple of
  minutes." The schedule adds "Run now still fires it once"; the suite
  "It can still be run from the suites list".
- Save, Delete and Pause are disabled, as are "+ Add input" and "+ New
  template"; every field matches `:disabled`. Only Back, Close and
  (schedule) Run now are live.

### Console, network, keyboard, failure

- **No uncaught page errors** on any page.
- `GET /api/repos/<repo>/github-branches` → 404 whenever a repo is picked
  (no GitHub behind the demo; expected).
- Every 400 was a refused save or delete described above, shown in the UI.
- `GET /api/tasks` reported `net::ERR_ABORTED` on navigation (a poll
  cancelled by the page change), and each DELETE that closes the pane
  reported `ERR_ABORTED` as on 10-02/10-03; the rows were gone after a
  reload.
- **Server down.** With `:18081` killed under an open `/schedules`, "+ New
  schedule" still opened, Add schedule kept the form and its title and
  showed the alert "Failed to fetch"; polls logged `grain: poll failed`
  warnings. The raw browser message is terse but not silent (noted only).

## Bugs found and fixed

1. **A paused schedule's row promised a next run it will not make.**
   - *Reproduction:* open any schedule, Pause, reload `/schedules`. The row
     reads "paused · Next run <time>". `Store.DueSchedules` reads only
     enabled rows, so nothing fires then; resuming later fires it at once.
     A paused row past its time also took the red overdue styling.
   - *Fix:* `SchedulesList.jsx` draws a paused row as "Held -- fires
     <time> if resumed by then", or "Held -- fires as soon as it is
     resumed (was due <time>)" once the time has gone, without the overdue
     class. Verified in Chromium on the rebuilt server (`sched-2`: "paused
     · Held -- fires 10/5/2026, 6:00:00 AM if resumed by then", back to
     "Next run …" after Resume).
   - *Coverage:* a `SchedulesList.test.jsx` case and e2e scenario 10, both
     failing with the fix reverted.
2. **The suite-run form's Branch helper was wrong and leaked an internal
   reference.**
   - *Reproduction:* Suites → "+ Run". The helper says "every task this run
     files targets this branch … -- bwsalmon/agents#642's own stacking",
     while a bound template's task keeps its own repo and branch (run 2
     above).
   - *Fix:* it now reads "every task from a template not bound to a repo
     of its own targets this branch, and (with auto-merge) lands back on
     it". Verified in Chromium.
   - *Coverage:* e2e scenario 10.

## Found and not fixed

- **A template-backed schedule cannot set the template's inputs**; it
  always files with each input's default. Possibly intended (10-03 note).
- **A run's tasks show no textual state except the badge's label.** The
  `TaskRow` accessibility follow-up from 10-02 covers this.
- **Bounds:** every N hours, a suite's max passes and its fixed count have
  no upper limit (10000h and 101 accepted); monthly day 31 is clamped
  silently in shorter months.
- **The follow-up filed with `proposedBy: 16` carried gcp key, gemini key
  and self debug** although task 16 had none. That is proposal
  capability inheritance from the team, outside this area; not
  investigated.
- **The 10-02 notes** "a dependency-refused delete does not name the
  dependents" and "Template names are not unique" still hold.

## Tests run

| Suite | Result |
| --- | --- |
| Vitest: `SchedulesList`, `SuitesList`, `SuiteRunPane` | 116 / 116 pass; the new case fails with the fix reverted |
| Playwright `e2e/recurring-composed-walkthrough.spec.js` (chromium) | 10 / 10 pass, including the new scenario 10, which fails on the unfixed build |
| Playwright `schedules_workflows`, `suites_workflows`, `templates_workflows` and the walkthrough together (chromium) | 13 / 13 pass on the final tree |
| grain CI (full required suite) on the pushed branch | see the pull request |
