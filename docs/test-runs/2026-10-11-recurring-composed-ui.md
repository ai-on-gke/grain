# 2026-10-11 recurring and composed work UI walkthrough

grain/task-2959. An end-to-end walkthrough of schedules, templates, suites and
suite runs in a real browser against a real `grain demo` server. It follows
[2026-10-10-recurring-composed-ui.md](2026-10-10-recurring-composed-ui.md). This
pass went past the scripted cases in `ui/e2e`: a fully populated raw-task
schedule checked field by field after save and reload, a task filed by Run now
compared with what its schedule says, keyboard use of declared editors,
deleting dependencies while a run is still in flight, the order of the run
dialog, and deep links to missing IDs.

It found three bugs and fixed all three on this branch:

1. **Declared editors took a choice from the keyboard.** On a repository-declared
   template, suite or schedule, the fields sit in a disabled fieldset, but the
   MUI selects were only kept from opening by pointer events. Tab to one, press
   ArrowDown and then Enter, and the select changed: a declared schedule's
   Repeat read "Daily", a declared suite's run mode read "Run a fixed number of
   times", and a declared template's network read "Egress". None of it could be
   saved, and nothing said so.
2. **A suite run went stuck as "active" for good once its template was
   deleted.** The templates page refuses a delete only while a suite or a
   schedule names the template. It doesn't refuse while a run already started
   from that suite still needs it. Each later pass re-reads the templates, so
   deleting the suite and then its template between passes made every reconcile
   cycle fail with `template template-3 no longer exists`. The run's row and
   pane went on saying "active" with no error.
3. **The generic "+ Run" dialog preselected the newest suite.** It listed the
   suites in API order (newest first) while the page above lists them by name
   or by the chosen sort. So it preselected a suite that wasn't the list's first
   row.

## Environment

| | |
| --- | --- |
| Commit walked | `85ee4d669730661037fdcc3e40072e91f248237d` (`candidate` tip, base of `grain/task-2959`) |
| Fixes verified at | `75d01ea7f0ea1d52813e5ab37395c474e40e0255` (`grain/task-2959`), demo restarted on the fixed build |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 / 10.8.2 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`. `npx playwright install` fails in this sandbox with `SELF_SIGNED_CERT_IN_CHAIN`, but the cached browser works. |
| Server | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /tmp/grain ./cmd/grain`, then `TMPDIR=<throwaway> /tmp/grain demo -addr 127.0.0.1:8899 -open=false` (embedded SQLite in a throwaway dir) |
| Browser identity | `localStorage["grain.username"] = "alice"` set by an init script; every request carries `X-Grain-Admin-Mode: on`, as `ui/playwright.config.js` does |
| Viewport / zone | 1400×900, browser time zone UTC |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` isn't present in this sandbox, so no Gemini-backed tooling was used. |

Each step was a Node script that drove Playwright's Chromium through visible
controls: role/label locators, clicks, typing and keys. Every page logged
console errors and warnings, page errors, failed requests, every HTTP response
≥ 400, and every `confirm()` text, which the scripts accepted. The scripts and
browser artifacts stayed in `/tmp` and aren't committed.

## Stubs

- **Agent execution boundary (suite-run states).** The demo runs no agents, so
  a throwaway Go program (`cmd/_walkstub`, deleted afterwards and never
  committed) opened the demo's own SQLite store with
  `sqlite.Open(sqlite.DefaultConfig(dir))` and stood in for agents finishing
  work:
  - `complete <ids>`: `Store.ObserveField(..., o.CompletedAt = now)`, which is
    what a finished run records.
  - `close <ids>`: `Store.ObserveField(..., o.ClosedAt = now)`, a task closed
    without completing.
  - `propose <parent> <title>`: copies the parent with `PutTask`, giving it a
    new ID, `Origin.Reason = proposal`, no approval, and one
    `LinkProposedBy -> parent` link. That is the shape `relayProposedTasks`
    files.
  - `sync`: calls the real `orchestrator.SyncSuites`, so every pass-to-pass
    decision, the next pass's tasks and the run's terminal status come from
    grain's own reconciler, not from the stub.
  - `runs`: `Store.ListSuiteRuns`, to read the result.

  Every create, edit, approve, run, delete and view went through the browser
  and the real server.
- **GitHub.** `GET /api/repos/acme/widgets/github-branches` answers 404 in the
  demo, which has no GitHub. The branch box's suggestions are empty, and these
  two console errors were ignored as an external boundary.

## Steps and outcomes

### 1. Schedules: raw task, every field (`/schedules`)

- **+ New schedule**, Fires "A task", no template. I set:
  - Base branch `main`, title `walk raw schedule`, description.
  - Read-only repo `acme/docs`, auto-merge on, network "None (this deployment
    only)", "When the agent finds nothing to do" set to "Leave for
    acknowledgement", capability `gcp key`.
  - Fields: priority high, requested by `bob`, due date `2026-12-01`, estimate
    `3`.
  - Repeat "Monthly", day `31`, `23:45`, time zone `America/New_York`, overlap
    "Start it anyway".
  - The time label followed the zone: `Time (America/New_York, EDT)`.
- **Validation:**
  - First try with another writeable repo, `acme/other`: refused in the form
    with the target-repo list named ("acme/other is not on this deployment's
    target repo list … The list currently holds `acme/widgets`"). The live
    preview (`POST /api/schedules/composition`) answered 400 with the same
    reason while it was filled in. The pane stayed open.
  - Every N hours `0`: "Value must be greater than or equal to 1."
  - Every N hours `1.5`: refused as not a whole number.
  - Blank title: "Please fill out this field."
- **Saved:** row `walk raw schedule | acme/widgets | monthly on day 31 at 23:45
  america/new_york | can overlap | Next run 11/1/2026, 3:45:00 AM`. That is
  31 October 23:45 EDT in UTC, which is correct.
- **Reopened** at `/schedules/sched-3`, then reloaded on the deep link: every
  value came back, including the chips (read-only repo, capability) and the
  custom fields.
- **Pause:** the row reads `paused | Held -- fires 11/1/2026, 3:45:00 AM if
  resumed by then`, and still does after a reload. The pane shows Resume.
- **Run now while paused:** confirm `Run "walk raw schedule" now? It starts the
  task "walk raw schedule" straight away; the schedule's own next run is
  unchanged.` The row added `· last ran 10/11/2026, 9:23:36 PM`.
  - The filed task (13) carried exactly what the schedule says: reads
    `acme/docs`, auto-merge, network `none`, nothing-needed `ack`, capability
    `gcp-key`, and the four custom fields. Its author is `schedule`, it's
    marked scheduled, and it's approved.
  - Its task detail shows the "scheduled" provenance.
- **Resume:** the row shows `Next run 11/1/2026, 3:45:00 AM · last ran …`.
- **Edits:**
  - Weekly, Friday 07:15, zone back to "UTC (default)", overlap Skip: row
    `fridays at 07:15 | Next run 10/16/2026, 7:15:00 AM`. A reload shows
    `timeZone` empty.
  - Every 6 hours: `every 6h | Next run` six hours on.
- **Escape on a dirty form** asked `You have unsaved changes here. Leave and
  discard them?`.

### 2. Templates (`/templates`)

- **+ New template** `walk-tmpl`:
  - Title `Audit {{service}} at {{severity}}`.
  - Input 1 `service` ("Service name", required, no default). Input 2
    `severity` (options `low`/`high`, default `high`).
  - Repo left "—" (unbound), read-only repo `acme/docs`, auto-merge on, network
    Egress, nothing-needed Leave, capability `self debug`, priority low,
    requested by `carol`.
  - Saved, reopened at `/templates/template-3`: every value persisted.
- **Edit:** the title got ` (v2)`, saved, then the deep link reloaded showed it.
- **Used from a schedule while `service` had no default:** Add schedule was
  refused in the form with `field "Service name" is required and has no
  default, so a schedule cannot fill it -- give it a default or untick
  Required`. Carried forward from 10-10, and the wording is clear.
- **Gave `service` the default `billing`**, saved, and a reload showed it.
- **The schedule then saved:** row `Audit billing at high (v2) | every 24h |
  template: walk-tmpl`. The form said `Title, description, reads, … come from
  the selected template, and stay in sync with it.`
- **Use… from the template row:**
  - The dialog showed both inputs (severity offered `default (high)`, `low`,
    `high`) and a "what this files" preview.
  - With `payments` and `low` it filed task 23: title `Audit payments at low
    (v2)`, description substituted, reads, capabilities and network `open`
    carried, approved.

### 3. Suites (`/suites`)

- **+ New suite** `walk-suite`:
  - Picked `walk-tmpl`, then **+ New template** inline (`walk-inline`, "Inline
    check"), which came back preselected.
  - Run mode "Run a fixed number of times", count `2`, require approval, and
    auto-merge.
  - Row: `walk-suite | run 2× | 2 templates | requires approval`.
- **Reopened** at `/suites/suite-2`: items `template-3,template-4`, count 2,
  both boxes ticked.
- **Switched** to "until no repo changes", max passes `3`. Saved, reloaded: `run
  until clean (max 3)`.

### 4. Suite runs

- **From the suite row's Run…:**
  - The dialog preselected `walk-suite`, and its preview listed both templates
    with each value's source (from the template / from the suite / the
    deployment decides).
  - Run 1 started (active, pass 1, "each pass's tasks wait for approval").
- **From the generic + Run:**
  - The dialog preselected `walk-suite` again, and its options read
    `walk-suite, Smoke Verification Suite, Release Qualification Suite` while
    the page above listed `Release Qualification Suite` first: bug 3.
  - Picked Release Qualification. A blank Branch was refused natively.
  - On `release-1.0` it started run 2. Its tasks went to `main` because both of
    its templates are bound to `acme/widgets @ main`, which the dialog's hint
    says.
- **From a suite schedule:**
  - + New schedule, Fires "A suite". Add with no suite chosen was refused with
    `choose a suite for this schedule to run`.
  - Then `walk-suite`, base `main`, weekly Sunday 05:00: row `walk-suite |
    sundays at 05:00 | suite: walk-suite`.
  - Run now (confirm: `It starts a run of the suite "walk-suite"`) started run
    3, marked "scheduled".
  - A second Run now was refused with a 409, shown as `… its previous run has
    not finished yet, and this schedule is set to skip …`.
- **Approval:** approved run 1's pass-1 tasks (14, 15) with the Approve button
  on each task's page.
- **Progression (stub plus real `SyncSuites`):**
  - Completed 14, 15 and 16, proposed follow-up 20 from 14, and closed 17.
  - Run 2 failed with `pass 1 had a task that failed, or closed without
    completing`. Run 1 moved to pass 2 (tasks 21 and 22).
  - Completed 21 and 22, and run 1 succeeded.
- **Run panes by direct URL:**
  - `/suites/runs/1`: Succeeded. `Pass 1` (3: 14, follow-up 20 nested under 14,
    15) and `Pass 2` (2). Each row's state is in its glyph's accessible name,
    for example "Awaiting ack, filed automatically by a suite run".
  - `/suites/runs/2`: Failed, error line shown, and 17 reads "Closed".
  - `/suites/runs/3`: Active, "scheduled" chip.
  - Clicking follow-up 20 in run 1's pane opened `/tasks/20`, and Back returned
    to `/suites/runs/1`.

### 5. Repository-declared rows (`docs/declarations.md`)

For `nightly-maintenance` (template), `Smoke Verification Suite` (suite) and
`Nightly Repo Maintenance Sweep` (schedule):

- The row chip reads `Declared by acme/widgets`, with the tooltip `Declared in
  acme/widgets, .grain/<kind>/<file>.yml`.
- Searching for `.grain/` finds each one.
- The editor opens at its colon-carrying deep link (for example
  `/schedules/repo:schedule:acme:widgets:nightly`) with the notice `This
  schedule is declared by acme/widgets in .grain/schedules/nightly.yml, so it
  cannot be edited or deleted here. Change that file in the repository …`. The
  suite adds "It can still be run from the suites list", and the schedule adds
  "Run now still fires it once."
- Save and Delete are disabled, and so is the schedule's Pause. Run now is
  enabled. Text boxes and checkboxes are disabled.
- **Bug 1:** every MUI select in those editors (Repeat, overlap, run mode,
  network, nothing-needed, team) could still be focused and changed from the
  keyboard. See the fix below.

### 6. Dependency-aware deletes and cleanup

- Deleting `walk-tmpl`: confirm `Delete the template "walk-tmpl"? Schedules and
  suites using it must be repointed or deleted first.`, then refused with
  `template is used by 1 schedule(s) ("Audit billing at high (v2)") and 1
  suite(s) ("walk-suite"); …`.
- Deleting `walk-inline`: refused with `template is used by 1 suite(s)
  ("walk-suite"); remove it from those first`.
- Deleting `walk-suite`: refused with `suite is used by 1 schedule(s)
  ("walk-suite"); repoint or delete those first`.
- Cleanup in dependency order: the three throwaway schedules (confirms say
  "Tasks it already filed are not affected" or "Suite runs it already started
  are not affected"), then the suite, then both templates. All succeeded.
  After reloads, the lists were back to the seeded 3 schedules, 3 templates and
  2 suites.
- Run 3's "scheduled" chip, followed after its schedule was gone, landed on
  `/schedules` with `no schedule "sched-5"`.
- **Bug 2:**
  - Run 3 was still active, and its suite and both templates were now deleted.
  - Completing its pass-1 tasks and syncing failed every time with
    `orchestrator: advancing suite run 3: template template-3 no longer exists`.
  - The run stayed "active" in the list and the pane, with no error.
  - On the fixed build, the same sync ends it as Failed with `pass 2 could not
    be filed: template template-3 no longer exists`, which the pane shows.

### 7. Sort, search, missing IDs, keyboard

**Sort and search:**

| List | Action | Observed |
| --- | --- | --- |
| `/templates` | search `audit` | `security-audit`, `walk-tmpl` (2) |
| `/suites` | search `walk` | `walk-suite` (1) |
| `/schedules` | search `billing` / `acme/widgets` / `zzz` | 1 / 6 / 0 with "No schedules match your search." |
| `/templates` | sorts: custom, name, newest, oldest | each reordered correctly |
| `/suites` | sorts: custom, name, most templates | each reordered correctly |
| `/schedules` | sorts: custom, title, next run, newest, oldest | "next run" ordered 00:00, 03:23, 06:00, 21:18, 21:26 on 10/12, then 10/18 |

**Missing IDs:** `/schedules/sched-999`, `/templates/template-999`,
`/suites/suite-999`, `/suites/runs/999999` and `/suites/runs/abc` each fall
back to their list with an alert naming the missing ID (`no suite run "abc"`).
No page errors.

**Keyboard:**

- On all three lists, Tab from the search box goes to the sort select, then
  through each row's drag handle, name link and row action.
- Enter on a run row's link opens `/suites/runs/1`, and Escape closes it.
- Enter on **+ Run** opens the dialog, and Escape closes it.

**Console and network:** across every script the only console errors were the
GitHub-branches 404s above and the 4xx answers to the refusals listed above.
The only failed requests were `GET /api/tasks` polls aborted by navigation
(`net::ERR_ABORTED`). There were no page errors.

## Bugs and fixes

| # | Bug | Fix | Regression coverage |
| --- | --- | --- | --- |
| 1 | Declared editors' selects changed from the keyboard | `DeclaredFields` (`ui/src/components/Declared.jsx`) captures keydown on the fieldset and swallows every key but Tab and Escape that lands on a `role="combobox"`. It also marks each select `aria-disabled="true"`. The selects stay focusable, so the values can still be read. | `SchedulesList.test.jsx` "keeps a declared schedule's selects from changing by keyboard" (fails without the fix); `e2e/recurring-composed-walkthrough.spec.js` test 9 tries ArrowDown and Enter on the declared suite, schedule and template |
| 2 | A suite run stuck as "active" after its template was deleted | `resolveSuiteTemplates` returns a typed `model.MissingSuiteTemplateError` (same message as before). `syncSuiteRun` ends the run as Failed on it, with `pass N could not be filed: …`. Store errors are still retried. | `pkg/orchestrator` `TestSyncSuitesFailsTheRunWhenItsTemplateIsDeletedBetweenPasses` (fails without the fix) |
| 3 | The "+ Run" dialog preselected the newest suite and listed them in API order | `SuitesList` passes the dialog every suite in the page's own order, sort included | `SuitesList.test.jsx` "offers the suites in the page's own order and preselects its first row" (fails without the fix) |

**Noted, not changed:**

- Run now on a paused schedule fires without mentioning the pause. The confirm
  already says what it starts, and firing a paused schedule once is a
  legitimate thing to do.
- A template delete asks for confirmation before the server refuses it for its
  dependents. Carried forward from 10-10.
- The templates page could also refuse to delete a template while an active run
  still needs it. With bug 2 fixed, such a run now fails visibly instead, and
  that also covers a declared template removed from its repository, which no
  delete check can refuse.

## Tests run

| Suite | Result |
| --- | --- |
| `npx vitest run src/components/SchedulesList.test.jsx src/components/SuitesList.test.jsx src/components/TemplatesList.test.jsx` | 142 passed |
| `npx playwright test e2e/recurring-composed-walkthrough.spec.js e2e/{suites,schedules,templates}_workflows.spec.js --project=chromium` | 17 passed |
| `go test ./pkg/orchestrator -run 'TestSyncSuites\|Suite'` and `go test ./pkg/model -run Suite` | pass |
| grain CI full suite on the pushed branch | see the pull request |
