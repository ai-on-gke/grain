# Recurring and composed work UI walkthrough — 2026-10-23

Task 274 (`grain/049f1e09/task-274`). This is an end-to-end pass over
schedules, templates, suites, suite runs and repository-declared rows on
the `/processes` surface. It ran in a real Chromium against a real
`grain demo` server. It follows
[2026-10-22-recurring-composed-ui.md](2026-10-22-recurring-composed-ui.md)
and covers what `candidate` has gained since then, including the
`/processes` phone layout (`f82f5ee81`) and the trigger-routing and
unsaved-edit guards (`97fcdeab6`).

It found one defect, fixed on this branch with unit and Playwright
regression tests: with the only provider connection disabled, the model
pickers' "Deployment default" entry did not say that only disabled
connections serve it. See [Bugs and fixes](#bugs-and-fixes). Nothing
else reproduced as a defect. A few observations are listed under
[Notes](#notes-not-filed).

## Environment

| | |
| --- | --- |
| Commit walked | `66b743a5def3b4fe04866dcbea5456ca58081264` (`candidate` tip, base of this branch) |
| Go / Node / npm | `go1.26.9 linux/amd64` / `v20.19.0` / `10.8.2`, Linux 6.1 |
| Browser | `playwright` 1.62.1, Chromium headless shell from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Build | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/grain ./cmd/grain` |
| Server | `TMPDIR=<throwaway> grain demo -addr 127.0.0.1:18090 -open=false -github-sim -reconcile 2s -schedules -access-code-file <file>`: embedded SQLite in a temp directory, seeded demo data, the suite and schedule reconcilers running every 2s, and no agents dispatched |
| Sign-in | `POST /api/auth/access-code` with the demo's access code; the session cookie was set on the browser context |
| Viewports / themes | 1440×900 for the walk; overflow sweep at 1440×900 and 390×844 in light and dark; browser time zone UTC |
| Model / key | `/home/debian/.gemini-api-key` is **not present** in this sandbox (`test -f` exits 1), so no Gemini-backed tooling (`LOOK=1`) was used. The demo's one connection is "Gemini 2.5 Pro" (antigravity), advertising `gemini-2.5-pro` and `gemini-2.5-flash`. |

Every script used one Chromium harness. It recorded:

- `pageerror`;
- `console` errors and warnings;
- `requestfailed`, ignoring aborted navigations;
- every response with status 400 or above, with its body;
- every native `confirm` text.

There were no uncaught page errors. Every console error was the
browser's own "Failed to load resource" line for a 4xx the walk
deliberately provoked, and each of those is listed below with the step
that caused it. There were no failed requests.

## Stubs

- **Agent execution boundary (`walkseed`, a throwaway Go helper, not committed).**
  `grain demo` never runs agents, so a pass task's outcome has to come
  from somewhere. The helper opened the demo's own store
  (`sqlite.Open(sqlite.DefaultConfig(<demo dir>))`, `model.New`) and
  recorded only what an agent run would have left behind:
  - `complete-changed <task> <pr>`: a `fixes acme/widgets#<pr>` link plus
    `Observation.CompletedAt`, i.e. a run that opened a pull request;
  - `complete-clean <task>`: `CompletedAt` alone;
  - `close <task>`: `Observation.ClosedAt`, i.e. a run that ended without
    completing;
  - `proposed-by <task> <parent>`: a `proposed-by` link, the provenance
    an agent's `propose_task` writes.

  The pass transitions themselves were made by the demo's own suite
  reconciler (`-reconcile 2s` → `orchestrator.SyncSuites`), not by the
  helper.
- **Follow-up proposal.** The follow-up task (#23) was filed with
  `POST /api/tasks` (unapproved, so proposed), then linked to its parent
  with `walkseed proposed-by`. This stands in for an agent's
  `propose_task`.
- **Presentation-only seeds through the real API.** These were used
  only where the UI offers no gesture to reach the state:
  - a template whose model no connection advertises (`claude-opus-legacy`);
  - a template with a required input and no default;
  - two short-lived schedules for the paused Run-now and the
    Fires/zone-switch probes.

  Each was deleted again afterwards.
- **Fault injection.** Playwright `page.route` delayed `/api/schedules`
  by 2.5s for the loading check, and answered it with a 500 for the
  error check.
- **GitHub.** `-github-sim` serves the simulated `acme/widgets`. Nothing
  reached a real GitHub.

Every create, edit, pause, resume, run, approve and delete went through
visible controls in the browser. The exceptions are the seeds and
deletes named above, and the API-boundary refusal checks under
[Declared rows](#6-repository-declared-templates-suites-and-schedules).

## Steps and outcomes

### 1. Processes list, templates kind: filter, search, sort

1. `/processes` initially showed `All 8`: schedules 3 (two operator
   schedules and the declared `Nightly Repo Maintenance Sweep`), workflow
   schedules 0, templates 3, suites 2 and triggers 0. The `Suite runs`
   section read `No suite runs yet.`
2. The `Templates` chip moved the URL to `/processes?kind=template` and
   left 3 rows. Searching `security` left `security-audit` alone.
   `?kind=template&q=security` deep-linked to the same single row.
3. The Sort menu offered `Next run`, `Last run`, `Name (A–Z)`, `Kind` and
   `Custom order`. `Name (A–Z)` set `&sort=name` and ordered
   `nightly-maintenance`, `release-qualification`, `security-audit`.
4. Template rows showed derived `Next run` from enabled schedules that
   fire them. `release-qualification` showed `10/24/2026, 12:00 AM`, from
   `sched-1`. Templates that no schedule fires showed `on demand`.

### 2. Templates: create fully populated, model picker, edit, reopen, use

1. `+ New template`, then every field was filled:
   - name and a task title with `{{area}}`, plus a description;
   - one input, `area`, labelled "Area to audit", with options
     `auth`/`billing`, default `auth`, required;
   - repo `acme/widgets`, base `release/1.0`;
   - writes `acme/gadgets` and reads `acme/docs`;
   - auto-merge on, network `None (this deployment only)`, nothing-needed
     `Leave for acknowledgement`;
   - model `gemini-2.5-flash` and capability `GCP key`;
   - fields: priority `high`, requested-by `ops-team`, estimate `3`.
2. **Validation.** `Add template` was refused with HTTP 400. The alert
   read "acme/gadgets is not on this deployment's target repo list, so it
   cannot be listed as a repo to write to … The list currently holds
   `acme/widgets` -- an operator widens targetRepos under Settings." The
   pane stayed open with every value kept. `Remove acme/gadgets`, then
   `Add template`, saved it as `template-3`. The API returned exactly the
   fields entered: `fields`, `reads`, `networkAccess: none`,
   `nothingNeededAction: ack`, `model`, `capabilities: [gcp-key]` and
   `customFields`. The global error alert outlived the successful retry
   until its 5s auto-hide (`App.jsx` `showError`); this is noted, not
   filed.
3. **Model picker.** Its options were `Deployment default (gemini-2.5-pro)`,
   `gemini-2.5-pro` and `gemini-2.5-flash`, which are exactly the two
   models the enabled connection advertises.
   - A template saved with `claude-opus-legacy` reopened showing
     `claude-opus-legacy (no connection advertises it)`, kept as the
     final option so the form does not silently drop it.
   - A schedule picking that template showed `Model:
     claude-opus-legacy, from the template` in its preview, and its own
     picker read `The template's model`.
   - With the connection disabled under Settings → Models (§7), every
     listed model read `(disabled connections only)`, but the default did
     not. That is [bug 1](#bugs-and-fixes).
4. **Deep link and reload.** `/processes/templates/template-3` opened
   `Edit template` with every field persisted:
   - model `gemini-2.5-flash`, base `release/1.0`;
   - network `None (this deployment only)`, nothing-needed
     `Leave for acknowledgement`;
   - priority `high`, estimate `3`, the `acme/docs` read chip and the
     `GCP key` capability.
5. **Edit and reopen.** Changed the description and pressed `Save`. After
   a reload, reopening the row showed the new description.
6. **Use template.** `Use…` on the row opened "File a task from a
   template":
   - "Area to audit" was preselected to `auth`;
   - it read "Files against acme/widgets @ release/1.0 -- this template
     is bound to it.";
   - the "What this files" preview credited each value to the template.

   Picking `billing` changed the preview title to `Audit billing 38868`.
   `Create task` filed #13 and navigated to `/tasks/13`. The API showed
   the task carrying the template's model, capabilities, reads, network,
   base, auto-merge and fields.
7. **Required input.** A template with a required input and no default
   was used. Pressing Create with the box empty was blocked by the
   browser's required validation, with no request sent. Spaces alone
   pass that check, so the server refused them with `field "svc" is
   required` (alert). The preview's composition request returned 400
   with the same message while the input was empty. This is noted, not
   filed.

### 3. Schedules: task-filing and suite-running, recurrence, overlap, preview, validation

1. **Template-backed, weekly, zoned, overlap allowed.**
   - Settings: Fires `A task`, Template `walk-tmpl-…`, Repeat
     `Weekly, on a day and time`, day `Friday`, time `09:15`, time zone
     `Europe/London`, and "If the last run has not finished" set to
     `Start it anyway`.
   - The time label read `Time (Europe/London, GMT+1)`.
   - The preview credited every value "from the template", except
     Approval, which was credited "from the schedule".
   - After save, the row read `Fridays at 09:15 Europe/London ·
     Template: walk-tmpl-… · Can overlap`, with next run
     `10/30/2026, 9:15 AM`. It was identical after a reload.
2. **Raw task, every N hours, with every content field.**
   - Pressing `Add schedule` on an empty form was blocked client-side
     (the Title is required), with no request sent.
   - Entered: title, description, base `release/1.0`, read `acme/docs`,
     model `gemini-2.5-flash`, network `Egress`, nothing-needed `Leave
     for acknowledgement`, capability `Self debug`, priority `low` and
     area `frontend`.
   - `Every N hours` = `0` was blocked by the browser's minimum ("Value
     must be greater than or equal to 1."). With `6` it saved as `sched-4`.
   - The preview credited everything "from the schedule". The API row
     matched: `networkAccess: open` is the stored value of the "Egress"
     option.
   - The row read `every 6h`, with next run six hours out.
3. **Suite-running, monthly.**
   - Fires `A suite` swapped every content field for a suite picker and
     a required base. The helper text read "required: a suite run stacks
     its tasks against one branch".
   - The preview listed "3 passes of these tasks", with each template's
     values and Auto-merge and Approval credited "from the suite".
   - With the base empty, `Add schedule` was held by the browser's
     required check on `base`, with focus moved to it.
   - With `main`, `Monthly` and day `31`, it saved as `monthly on day 31
     at 09:00 · Suite: Walk suite …`, next run `10/31/2026, 9:00 AM`.
4. **Edit.** Changing `sched-4` to `Daily, at a time` at `05:45` and
   saving gave `daily at 05:45`, next run `10/24/2026, 5:45 AM`, kept
   after a reload.
5. **Pause and resume** (no confirmation, since both are reversible).
   - Pause closed the pane. The row then read `Held -- fires 10/24/2026,
     5:45 AM if resumed by then` with a red `Paused` chip, kept after a
     reload. The pane then offered `Resume`.
   - Resume restored the time and `Enabled`.
6. **Run now.**
   - The confirm read `Run "Raw walk …" now? It starts the task "Raw
     walk …" straight away; the schedule's own next run is unchanged.`
   - Dismissing it filed nothing and left the pane open, with the task
     count unchanged at 13.
   - Accepting it filed one task (14), closed the pane, and set `Last
     run` on the row with the next run unchanged. Both held after a
     reload.
   - Run now on a paused schedule also fired once and stayed paused,
     showing a last run and `Held -- …`.
7. **Run now on the suite schedule.** The confirm named the suite.
   Without a reload, a `.suite-run-row` appeared under Suite runs:
   `active · scheduled · pass 1 · run 3× · each pass's tasks wait for
   approval`.
8. **Switching and zones on an existing schedule.**
   - Setting a template-backed schedule's Template to `None -- fill in
     the fields below` copied the template's title into the form.
     Saving cleared `templateId`.
   - `Fires` is offered on new schedules only (`ScheduleOverlay.jsx`
     says so), so an existing schedule cannot be switched between task
     and suite.
   - Changing a daily `08:00` schedule's zone from UTC to `Asia/Tokyo`
     moved its next run from `2026-10-24T08:00Z` to `2026-10-23T23:00Z`,
     which is correct.
9. **Template picker with duplicate names.** A second template named
   `security-audit` was accepted. The schedule's Template picker then
   distinguished the two as `security-audit — dup (template-7)` and
   `security-audit — Security: Dependency vulnerability scan
   (template-2)` (`templateLabels.js`). This is by design.

### 4. Suites: create, inline template, modes, limits, approval, auto-merge, delete

1. Searching `until clean` under `?kind=suite` left `Smoke Verification
   Suite`.
2. **Create.**
   - `+ New suite`: name; Run mode `Run until no repo changes or new
     tasks`.
   - Max passes `0` was blocked by its minimum of 1; it was set to `2`.
   - `Require approval before each pass's tasks run` ticked, auto-merge
     on, and the full template picked.
   - `+ New template` inside the suite opened a stacked `New template`.
     Saving it returned to the suite with the new template attached.
   - `Add suite` produced the row `run until clean (max 2) · 2 templates
     · Requires approval`.
3. **Edit.** Run mode `Run a fixed number of times`, count `3`, then
   Save. After a reload the row read `run 3×`. The API showed
   `mode: count, count: 3, requireApproval: true, autoMerge: true` with
   2 items.
4. **Dependency-aware delete.**
   - Deleting the suite while a schedule fired it showed the confirm
     `Delete the suite "Walk suite …"? Runs already started from it are
     unaffected.` Accepting got a 400 with the alert `suite is used by 1
     schedule(s) ("Walk suite …"); repoint or delete those first`, and
     the pane stayed open.
   - After the schedule was deleted, the same gesture deleted the suite,
     and the row was gone after a reload.
   - Its runs (1, 3, 4) were kept, and `/processes/suites/runs/4` still
     opened, named after the deleted suite.

### 5. Suite runs: row Run…, generic + Run, states, passes, follow-ups, task navigation

1. **`Run…` on the `Release Qualification Suite` row.** "Run a suite"
   preselected that suite. Its preview said "2 passes of these tasks"
   and the branch hint explained which tasks take the branch. Branch
   `main` and `Run` started run 2, which appeared under Suite runs.
2. **Generic `+ Run`.** It preselected the suite the table leads with.
   The picker listed all three suites (the declared one included).
   Picking `Walk suite …` changed the preview to "3 passes", and the run
   started (run 3). A third start (run 4) showed the row count going
   from 3 to 4 immediately and staying 4 after a reload.
3. **Active, pass 1.**
   - `/processes/suites/runs/2` showed the `Active` chip, `acme/widgets
     main`, `run 2×`, a `Tasks 2` count, a `Pass 1` heading and two task
     rows with a `Queued` status image.
   - Runs 1 and 3 showed `Proposed` rows and "each pass's tasks wait for
     approval".
   - Run 1 also carried a `Scheduled` link to `/processes/schedules/sched-5`.
4. **Pass progression with a follow-up.**
   - Stubs: `complete-changed 17 101`, `complete-clean 18`, then follow-up
     #23 filed and linked `proposed-by 17`.
   - Within one reconcile cycle the run moved to pass 2, with tasks 24
     and 25 filed.
   - The pane grouped `Pass 1` (3) and `Pass 2` (2). Under task 17
     (`Queued for merge`), #23 `Proposed` was nested in its own sub-list.
5. **Task-detail navigation.** Clicking task 17 in the pane went to
   `/tasks/17`. The provenance toggle read `pass 1 of Release
   Qualification Suite run 2`, linked to `/processes/suites/runs/2`, and
   listed `acme/widgets#101 PR open` and the planned follow-up #23.
   Browser Back returned to the run pane.
6. **Approval through the UI.** `Approve` on `/tasks/19` and `/tasks/20`
   moved both to `queued`.
7. **Finished: succeeded.** `complete-clean 24`, `complete-clean 25` →
   run 2 `Succeeded`, both passes kept.
8. **Finished: failed.** `close 19`, `complete-clean 20` → run 3 `Failed`.
   Its pane showed the paragraph `pass 1 had a task that failed, or
   closed without completing` and task 19 with a `Closed` image.
9. **Direct URLs.**
   - `/suites/runs/2` redirected to `/processes/suites/runs/2`.
   - `/processes/suites/runs/9999` fell back to `/processes` with the
     alert `no suite run "9999"`.
   - Run 1's `Scheduled` link, after its schedule had been deleted, fell
     back with `no schedule "sched-5"`.

### 6. Repository-declared templates, suites and schedules

1. **List.** The three declared rows carried a `Declared by acme/widgets`
   chip, and its tooltip named the repository and file:
   - `nightly-maintenance`: `Declared in acme/widgets, .grain/templates/nightly.yml`;
   - `Smoke Verification Suite`: `…, .grain/suites/smoke.yml`;
   - `Nightly Repo Maintenance Sweep`: `…, .grain/schedules/nightly.yml`.

   Each row link was percent-encoded and deep-linked correctly.
2. **Editors refuse up front, with an explanation.** Each pane opened with
   `.declared-notice`, for example "This template is declared by
   acme/widgets in .grain/templates/nightly.yml, so it cannot be edited
   or deleted here. Change that file in the repository and grain picks
   it up within a couple of minutes."
   - The suite notice adds "It can still be run from the suites list.",
     and the schedule notice "Run now still fires it once."
   - Every input was disabled.
   - `Save`, `Delete` and, on the schedule, `Pause` were disabled. `Close`
     and `Run now` stayed enabled.
   - `+ Add input` (template) and `+ New template` (suite) sit inside the
     disabled fieldset. Playwright reported them not clickable, and
     nothing opened.
3. `Use…` on the declared template and `Run…` on the declared suite
   opened their dialogs normally.
4. **API boundary.** `DELETE` on each declared id returned 400
   `<id> is declared by acme/widgets in <path>, so it is not this
   deployment's to …`.

### 7. Model picker against connection state

Under Settings → Models, `Disable` on "Gemini 2.5 Pro" (the only
connection) changed the template, schedule and existing-template pickers
to these options:

- `Deployment default (gemini-2.5-pro)`
- `gemini-2.5-pro (disabled connections only)`
- `gemini-2.5-flash (disabled connections only)`

That is [bug 1](#bugs-and-fixes). After `Enable`, the plain labels came
back.

### 8. Keyboard, loading, error states, layout

- **Keyboard.**
  - Tab reaches a schedule row's link. Enter opens
    `/processes/schedules/sched-1`. Escape closes back to
    `?kind=schedule` and returns focus to the row's link.
  - `+ New` opens a menu by Enter, with focus on `Schedule`. Arrow keys
    move to `Suite`, and Enter opens New suite.
  - 60 Tabs inside the dialog never left it. Escape closed it, and focus
    returned to `+ New`.
  - Tab reaches a `.suite-run-row` link, and Enter opens its run.
  - Escape on a dirty form asks "You have unsaved changes here. Leave
    and discard them?"
- **Loading.** With `/api/schedules` delayed 2.5s, the table rendered the
  other kinds at once. The schedule rows joined when the response
  arrived, with no flash of an error.
- **Error.** With `/api/schedules` failing (500), the page rendered the
  other kinds, showed the alert `boom`, and counted `Schedules 0`. This
  is noted below.
- **Overflow sweep.** Every page below was clean: document `scrollWidth`
  equals the viewport, and no unclipped element passes the right edge.
  - Widths and themes: 1440×900 and 390×844, light and dark.
  - Pages: `/processes`, `?kind=schedule`, `/processes/schedules/sched-2`,
    `/processes/templates/template-1`, `/processes/suites/suite-1`, runs
    2 and 3, and the declared schedule.

### 9. Throwaway data removed

Every schedule, suite and template the walk created was deleted through
its pane's `Delete`.

- **Confirms.**
  - Schedules: `Delete the schedule "…"? Tasks it already filed are not
    affected.` For suite schedules the second sentence is `Suite runs it
    already started are not affected.`
  - Templates: `Delete the template "…"? Schedules and suites using it
    must be repointed or deleted first.`
- **Dismiss.** Dismissing each confirm first kept the row and the pane.
- **Dependency-aware template delete.** Deleting the full template while
  in use got a 400. The alert read `template is used by 1 schedule(s)
  ("Audit auth …") and 1 suite(s) ("Walk suite …"); repoint or delete
  the schedules and remove it from the suites first`.

  Once both were gone it deleted cleanly, and so did the inline and
  orphan-model templates.
- **What stays.** Seeded rows were left untouched. Suite runs and the
  tasks they filed stay, as the confirms promise.

## Bugs and fixes

### 1. The "Deployment default" model entry hid that only disabled connections serve it

- **Reproduction:**
  1. Run `grain demo`.
  2. Under Settings → Models, press `Disable` on "Gemini 2.5 Pro".
  3. Open `/processes`, choose `+ New` → `Template` (or `Schedule`), and
     open `Model`.
  4. The first option reads `Deployment default (gemini-2.5-pro)`, while
     the explicit `gemini-2.5-pro` option below it reads
     `gemini-2.5-pro (disabled connections only)`.

  The same label was used by New task, Edit task and Bulk edit.
- **Why it matters:** the default is what every form starts on. A
  template or schedule saved on it read as servable while no enabled
  connection could run it, and only the explicit entries said so.
- **Cause:** `teamDefaultLabel` in `ui/src/state.js` took only the model
  name. `modelOptions` put the "(disabled connections only)" caveat on
  explicit entries but kept the caller's empty label as-is.
- **Fix:**
  - `teamDefaultLabel(teamDefaultModel, models)` now returns `Deployment
    default (gemini-2.5-pro, disabled connections only)` when the
    default is advertised only by disabled connections.
  - A default no connection lists at all (a provider's `auto`) is left
    unchanged, because connections never advertise those.
  - `ModelPicker`, `NewTaskOverlay`, `EditTaskForm` and `BulkEditOverlay`
    pass their `connectionModels` through.
- **Regression coverage:**
  - Unit: `state.test.js` ("says when only disabled connections serve
    the team default") and `ModelPicker.test.jsx` ("says when the
    deployment default is served only by disabled connections").
  - Playwright: test 19 in `ui/e2e/recurring-composed-walkthrough.spec.js`.
    It disables the demo's connection through Settings, checks the
    template and schedule pickers, and re-enables in a `finally`. Run
    against the unfixed `state.js` it fails, receiving `Deployment
    default (gemini-2.5-pro)`.
  - The whole spec (19 tests) passes on Chromium.
- **Look:** the template Model picker was shot open with the connection
  disabled, before and after, at 1440×900 and 390×844, light and dark.
  At 390px the longer label wraps to two lines inside the listbox
  (option 357px wide in a 390px viewport) with no sideways overflow.

## Notes (not filed)

- **Schedules failing to load.** If `/api/schedules` fails, the transient
  alert auto-hides after 5s. `/processes` then shows `Schedules 0`,
  which cannot be told apart from having no schedules until the next
  poll succeeds. This is the app-wide "deliberately quiet" poll policy
  (`App.jsx`), not specific to this page.
- **Global error alert.** A refusal's global alert stays up for its 5s
  even after the corrected save succeeds, the same app-wide `showError`
  behaviour.
- **Template `Last run`.** A template row's `Last run` is derived only
  from the schedules that fire it (§5.1). A task filed through `Use…`,
  or as a suite pass, does not count.
- **Run pane `Scheduled` link.** Its accessible name is `Scheduled by
  sched-5` (the id), not the schedule's title.
- **Overdue presentation not re-exercised.** Producing the overdue
  `Not firing` state needs a past-due `nextRunAt`. No UI gesture makes
  one, and it was covered by the 2026-10-22 walkthrough, so it was not
  re-exercised.
