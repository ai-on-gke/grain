# 2026-10-12 recurring and composed work UI walkthrough

Task 3213 (`grain/eu/task-3213`). This is an end-to-end walkthrough of
schedules, templates, suites and suite runs, in a real browser against a
real `grain demo` server. It follows
[2026-10-11-recurring-composed-ui.md](2026-10-11-recurring-composed-ui.md),
whose three fixes are in the tree tested here.

This pass covered ground the earlier ones did not:

- a schedule in a half-hour time zone (`Asia/Calcutta`);
- the upper bounds on the cadence boxes;
- template edits made *after* a schedule and a suite already use the template;
- the template's input options box;
- the suite pass-count bounds;
- keyboard reordering, including declared rows;
- Escape after using an autocomplete inside a dirty editor;
- a server that goes away mid-edit.

It found one bug and fixed it on this branch: **a template edit didn't reach
the schedules that fire from it until each one next fired.** The schedules
list, the Run now confirmation and the template-delete refusal went on naming
the schedule by the template's old title. Meanwhile the composition preview
beside them, and the task the next firing filed, already used the new one. See
[Bugs and fixes](#bugs-and-fixes).

## Environment

| | |
| --- | --- |
| Commit walked | `6babee9a2d1d` (`candidate` tip, base of `grain/eu/task-3213`; the demo footer read `v1.10.0-rc.1.0.20261012124358-6babee9a2d1d`) |
| Fix verified at | `c3c8a7a4f` (`grain/eu/task-3213`). The demo was restarted on the rebuilt binary over a fresh store. |
| Go | go1.26.6 linux/amd64 |
| Node / npm | v20.19.0 / 10.8.2 |
| Browser | `@playwright/test` 1.62.1, Chromium headless shell revision 1234 from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright` |
| Server | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /tmp/grain ./cmd/grain`, then `TMPDIR=<throwaway> /tmp/grain demo -addr 127.0.0.1:8899 -open=false` (embedded SQLite in a throwaway directory) |
| Browser identity | `localStorage["grain.username"] = "alice"` from an init script. Every request carried `X-Grain-Admin-Mode: on`, as `ui/playwright.config.js` does. |
| Viewport / zone | 1400×900, browser time zone UTC |
| Model / agents | None. The demo runs no agents. `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed tooling was used. |

Each step was a Node script that drove Playwright's Chromium through visible
controls: role and label locators, clicks, typing and key presses. Every page
logged:

- console errors and warnings;
- page errors;
- failed requests;
- every HTTP response ≥ 400, with its body;
- every `confirm()` and `beforeunload` text. The scripts accepted these unless
  a step says otherwise.

The scripts and browser artifacts stayed in `/tmp` and aren't committed.

## Stubs

- **Agent execution boundary (suite-run states).** The demo runs no agents.
  A throwaway Go program (`cmd/_walkstub`, never committed) opened the demo's
  own store with `sqlite.Open(sqlite.DefaultConfig(dir))` and stood in for
  agents finishing work:
  - `complete <ids>`: `Store.ObserveField(..., o.CompletedAt = now)`.
  - `close <ids>`: `Store.ObserveField(..., o.ClosedAt = now)`. This is a task
    closed without completing.
  - `propose <parent> <title>`: copies the parent with `PutTask` under a new
    `NewTaskID`. The copy has `Origin.Reason = proposal`, no approval, and one
    `LinkProposedBy -> parent` link, which is the shape a relayed proposal has.
  - `sync`: calls the real `orchestrator.SyncSuites`. Every pass-to-pass
    decision, every later pass's tasks, and each run's terminal status come
    from grain's own reconciler.
  - `runs`: `Store.ListSuiteRuns`, to read the result.

  Approvals, creates, edits, runs, deletes and every view went through the
  browser and the real server.
- **GitHub.** The demo has no GitHub, so
  `GET /api/repos/acme/widgets/github-branches` answers 404. The branch box
  offers no suggestions, and those console errors were ignored as an external
  boundary.

## Steps and outcomes

### 1. Schedules: a raw task in a half-hour zone (`/schedules`)

- **+ New schedule** with Fires "A task" and no template, filled in as follows:
  - base `main`;
  - title `walk3 raw kolkata` and a description;
  - read-only repo `acme/docs`;
  - auto-merge on;
  - network "None (this deployment only)";
  - nothing-needed "Leave for acknowledgement";
  - capability `gcp key`;
  - priority high, requested by `bob`;
  - Repeat "Daily" at `09:30`, time zone `Asia/Calcutta`, overlap "Start it
    anyway".

  Chromium's `Intl.supportedValuesOf` offers the legacy `Asia/Calcutta`
  spelling, not `Asia/Kolkata`, and the server accepted it. The time label
  followed the zone: `Time (Asia/Calcutta, GMT+5:30)`.
- **Live preview**: every row read "from the schedule", except Model, which
  read "the deployment decides".
- **Saved**: the row read `daily at 09:30 asia/calcutta | can overlap | Next run
  10/13/2026, 4:00:00 AM`, which is 09:30 IST in UTC. The deep link
  `/schedules/sched-3` reopened it after a reload with every value present. The
  API showed `base: main`, `reads`, `customFields`, `networkAccess: none`,
  `nothingNeededAction: ack` and `capabilities: [gcp-key]`.
- **Recurrence edits, each saved and re-read from the row**:
  - Monthly: day `0` was refused with "Value must be greater than or equal to
    1", and day `32` with "… less than or equal to 31". Day 31 at 23:45
    `America/New_York` saved as `monthly on day 31 at 23:45 america/new_york |
    Next run 11/1/2026, 3:45:00 AM`.
  - Weekly, Sunday 00:00 UTC, Skip: `sundays at 00:00 | Next run 10/18/2026,
    12:00:00 AM`.
  - Every N hours:
    - `0` and `-3` were refused as below 1.
    - `2.5` was refused as not whole ("The two nearest valid values are 2 and
      3").
    - `100000` was refused as above the cap ("less than or equal to 8784").
    - `168` saved: `every 168h | Next run` a week on.
- **Pause** (no confirm, since pausing can be undone): the row read `paused | Held
  -- fires 10/19/2026, 1:02:51 PM if resumed by then`, and still did after a
  reload.
- **Run now while paused**: the confirm read `Run "walk3 raw kolkata" now? It
  starts the task "walk3 raw kolkata" straight away; the schedule's own next
  run is unchanged.` The row then added `· last ran 10/12/2026, 1:02:53 PM`.
  The filed task 13 carried exactly what the schedule says, and was `author:
  schedule`, `scheduled: true` and approved.
- **Resume**: `Next run 10/19/2026, 1:02:51 PM · last ran …`, persisted across
  a reload.

### 2. Templates (`/templates`)

- **+ New template** `walk3-tmpl` was filled in fully:
  - title `Rotate {{secret}} for {{env}}` and a description using both inputs;
  - input `secret` (label "Secret name", required);
  - input `env` (options `staging`/`prod`, default `staging`);
  - repo `acme/widgets @ main`;
  - another writeable repo `grain/config-default`;
  - read-only repo `acme/docs`;
  - auto-merge on;
  - network Egress, nothing-needed Leave;
  - capability `self debug`;
  - priority low, area `backend`, requested by `carol`;
  - due `2026-12-01`, estimate `2`, customer-facing Yes.
- **Options box**: the first attempt typed `staging, prod` on one line. It was
  refused in place with `field "env": default "staging" is not one of its
  options`, and the pane stayed open with everything kept. The box's
  placeholder says "one per line". With one option per line it saved.
- **Reopened** at `/templates/template-3` and reloaded: every value came back,
  including both inputs, the write target (with its own base box), the chips
  and all six custom fields.
- **Edited** the title to `… (v2)`. Saved, then reloaded on the deep link.
- **Escape inside a dirty editor**:
  - In both the template editor and the schedule editor, I picked an Area value
    from its autocomplete, which closes the popup, and then pressed Escape.
  - Escape went to the pane, and the pane asked `You have unsaved changes here.
    Leave and discard them?` first.
  - A reload of a dirty editor raised `beforeunload`.

### 3. Using the template from a schedule

- **Fires "A task", template `walk3-tmpl`**:
  - Add was refused with `field "Secret name" is required and has no default,
    so a schedule cannot fill it -- give it a default or untick Required`. This
    showed both in the preview panel (`POST /api/schedules/composition` 400)
    and as a page alert.
  - I gave `secret` the default `api-token` in the template editor, and the
    schedule then saved: row `Rotate api-token for staging (v2) | daily at
    06:00 | template: walk3-tmpl`.
  - Its preview listed every value's source.
  - Run now (confirm: `It starts a task from the template "walk3-tmpl"`) filed
    task 14 with the template's reads, writes, network `open`, ack, capability
    and all six fields.
- **Template edited again** (title `(v3)`, capability `self debug` swapped for
  `gcp key`):
  - The schedule's preview said `Rotate api-token for staging (v3)` and
    `gcp-key`.
  - Its **row still said `(v2)`**, its Run now confirm named `(v2)`, and later
    the template's delete refusal named the schedule `"Rotate api-token for
    staging (v2)"`.
  - This was the bug fixed below. The firing that would have refreshed the copy
    was refused with a 409, because task 14 was still open and the schedule
    skips overlaps.

### 4. Suites (`/suites`)

- **+ New suite** `walk3-suite` with no templates: refused in place with `a
  suite needs at least one template`.
- Picked `walk3-tmpl`, then **+ New template** inline (`walk3-inline`, title
  `Inline check {{area}}`, input `area` defaulting to `ui`). It came back
  preselected: Templates read `walk3-tmpl, walk3-inline`.
- Run mode "Run a fixed number of times":
  - `0` was refused natively ("greater than or equal to 1").
  - `1000` **saved** (`count: 1000`). Neither the form nor the server sets an
    upper bound on a suite's pass count or max passes, while a schedule's
    every-N-hours box stops at 8784. This is noted below and not changed.
- Edited the suite to count `2`, require approval on, auto-merge off. After a
  reload `/suites/suite-2` showed exactly that.
- Switched to "until no repo changes", max passes `3`. The row read `run until
  clean (max 3) | 2 templates | requires approval`.
- A throwaway suite `walk3-throwaway` was created and deleted through its pane.
  The confirm read `Delete the suite "walk3-throwaway"? Runs already started
  from it are unaffected.`, and it was gone after a reload.
- Search matches suite names (`walk3` → 1); it does not match a template's name
  (`security-audit` → 0). Sorts offered: custom, name (A–Z), most templates.

### 5. Suite runs

- **From the suite row's Run…**:
  - The dialog preselected `walk3-suite` and previewed both templates, with
    each value's source.
  - A blank Branch was refused natively. On `main` it started run 2: active,
    "each pass's tasks wait for approval".
- **From the generic + Run**:
  - The options were in the page's own order (`Release Qualification Suite,
    Smoke Verification Suite, walk3-suite`), and the first was preselected.
    This is the 10-11 fix holding.
  - A blank Branch was refused natively. On `release-2.0` it started run 1.
- **From a suite schedule**:
  - + New schedule, Fires "A suite", `walk3-suite`, base `main`, weekly Saturday
    05:00: row `walk3-suite | saturdays at 05:00 | suite: walk3-suite | Next run
    10/17/2026, 5:00:00 AM`. On reopening, Fires is no longer offered (it is
    fixed at creation), and Suite stays editable.
  - Run now started run 3.
  - A second Run now was refused with a 409, shown as a page alert: `… its
    previous run has not finished yet, and this schedule is set to skip …`.
- **Declared suite**: the row's Run… on `Smoke Verification Suite` started a
  run on `main`.
- **Approval**: I approved run 2's pass-1 tasks (17, 18) and later its pass-2
  tasks (22, 23) with the Approve button on each task's page. After a reload no
  Approve button remained.
- **Progression (stub plus real `SyncSuites`)**:
  - Completed 17 and 18, proposed follow-up 21 from 17, closed 15 and completed
    16, then synced:
    - Run 1 failed with `pass 1 had a task that failed, or closed without
      completing`.
    - Run 2 moved to pass 2 (22 and 23), because pass 1 proposed something.
  - Approved and completed 22 and 23, then synced: run 2 succeeded.
- **Runs list**:
  - `walk3-suite | active | main | scheduled | pass 1 · run until clean (max 3)
    · each pass's tasks wait for approval`.
  - `walk3-suite | succeeded | main | pass 2 …`.
  - `Release Qualification Suite | failed | release-2.0 | pass 1 · run 2× |
    pass 1 had a task that failed, …`.
- **Run panes by direct URL**:
  - `/suites/runs/1`: Failed, with the error line. Pass 1 shows 15 as "Closed"
    and 16 as "Awaiting ack" (the state is the glyph's accessible name).
  - `/suites/runs/2`: Succeeded.
    - `Pass 1 (3)`: 17, then follow-up 21 nested under it as "Proposed", then
      18.
    - `Pass 2 (2)`: 22 and 23.
    - Each row carries its priority, read and write repos, and capability.
  - `/suites/runs/3`: Active, with a `Scheduled by sched-5` link.
- **Navigation**: clicking follow-up 21 in run 2's pane opened `/tasks/21`, and
  Back returned to `/suites/runs/2`. Enter on a run row's link opened its pane,
  and Escape returned to `/suites` with focus back on that row's link.

### 6. Repository-declared rows (`docs/declarations.md`)

For `nightly-maintenance` (template), `Smoke Verification Suite` (suite) and
`Nightly Repo Maintenance Sweep` (schedule):

- **The row**: the chip reads `Declared by acme/widgets`, and its `title` is
  `Declared in acme/widgets, .grain/<kind>/<file>.yml`. Searching
  `.grain/templates` or `.grain/schedules` finds the row.
- **The editor**:
  - It opens at its colon-carrying deep link (for example
    `/templates/repo:template:acme:widgets:nightly`) and survives a reload.
  - It shows the notice `This … is declared by acme/widgets in
    .grain/<kind>/<file>.yml, so it cannot be edited or deleted here. Change
    that file in the repository …`. The suite adds "It can still be run from
    the suites list", and the schedule adds "Run now still fires it once."
- **Disabled controls**: Save, Delete, the schedule's Pause, the template's
  "+ Add input" and the suite's inline "+ New template" are all disabled, and
  so are the text boxes.
- **What still works**: Close, Run now, Run… and Use… are enabled and work.
- **The API's own refusal** (`DELETE /api/templates/repo:template:…`) is a 400
  naming the file: `… is declared by acme/widgets in .grain/templates/nightly.yml,
  so it is not this deployment's to delete …`.
- **Custom order**: declared rows can be reordered like any other row. Custom
  order is a display order, not the declaration's.

### 7. Dependency-aware deletes and cleanup

- **`walk3-tmpl`**:
  - The confirm read `Delete the template "walk3-tmpl"? Schedules and suites
    using it must be repointed or deleted first.`
  - It was then refused with `template is used by 1 schedule(s) ("Rotate
    api-token for staging (v2)") and 1 suite(s) ("walk3-suite"); …`. That
    stale `(v2)` is the bug.
  - The pane stayed open on the deep link.
- **`walk3-inline`**: refused with `template is used by 1 suite(s)
  ("walk3-suite"); remove it from those first`.
- **`walk3-suite`**: refused with `suite is used by 1 schedule(s)
  ("walk3-suite"); repoint or delete those first`.
- **Cleanup in dependency order through each pane's Delete**:
  1. The three schedules. Their confirms said "Tasks it already filed are not
     affected" or "Suite runs it already started are not affected".
  2. The suite.
  3. Both templates.

  After reloads, the lists were back to the seeded rows: 3 schedules, 3
  templates and 2 suites.
- **Run 3's `Scheduled by sched-5` link**, followed after its schedule was
  deleted, landed on `/schedules` with `no schedule "sched-5"`.

### 8. Sort, search, missing IDs, keyboard, errors

**Sort and search:**

| List | Action | Observed |
| --- | --- | --- |
| `/schedules` | Next run | 10/13 00:00, 10/13 06:00, 10/13 13:00, 10/17 05:00, 10/19 06:00, 10/19 13:02 |
| `/schedules` | Title, Newest, Oldest, Custom | each reordered correctly |
| `/schedules` | search `kolkata` / `walk3-suite` / `nightly-maintenance` / `.grain/schedules` / `zzz` | the raw schedule / the suite schedule / the declared schedule (by template name) / the declared schedule / "No schedules match your search." |
| `/templates` | search `rotate` / `secret` / `.grain/templates` | `walk3-tmpl` / `walk3-tmpl` (input name) / `nightly-maintenance` |
| `/templates`, `/suites` | every sort | each reordered correctly |

The sort goes back to "Custom order" on reload. That is deliberate: the lists'
own comment keeps search and sort local.

**Missing IDs:** each of these falls back to its list with an alert naming the
missing ID, with no page errors:

- `/schedules/sched-999`
- `/templates/template-999`
- `/suites/suite-999`
- `/suites/runs/999999`
- `/suites/runs/abc`
- `/schedules/repo:schedule:acme:widgets:gone`

`/suites/runs/2/extra` opens run 2.

**Keyboard:**

- On all three lists, Tab from the search box goes to the sort select, then
  through each row's move handle, name link and row action.
- To reorder: Enter on a move handle picks the row up ("Moving 1 schedule —
  choose where it goes"). Tab then reaches the "Move here, …" slots, and Enter
  on one moves the row. That persisted across a reload, both for a schedule
  (declared, moved to the end) and for a template (moved to the top).
- Enter on **+ Run** opens the dialog, and Escape closes it.

**Server gone mid-edit:**

- With the schedule editor open and filled in, I stopped the demo and pressed
  Add.
- The pane stayed open with its input. The page showed `This deployment cannot
  be reached: the connection to it failed. Unreachable since 1:19:51 PM --
  retrying automatically; …`.
- Leaving the page raised `beforeunload`.

**Console and network:** across every script, the console errors were the
GitHub-branches 404s above, the 4xx answers to the refusals listed above, and
the connection-refused errors while the server was deliberately down. There
were no page errors. Two kinds of failed request showed up:

- `GET /api/tasks` polls aborted by navigation (`net::ERR_ABORTED`).
- Every successful `DELETE`, also reported as `net::ERR_ABORTED`. This is a
  Chromium/Playwright reporting artifact for a bodiless `204 No Content`, not a
  grain fault: a bare `fetch(…, {method: "DELETE"})` from the page resolved
  `ok` with status 204 and logged the same "failure". `curl -v` shows a plain
  `HTTP/1.1 204 No Content`.

## Bugs and fixes

| # | Bug | Fix | Regression coverage |
| --- | --- | --- | --- |
| 1 | A template-backed schedule keeps a display copy of its template's content (`Schedule.TemplateID`'s doc comment). It was only refreshed when the schedule fired or was itself saved. After a template edit, the schedules list, the Run now confirm and the template-delete refusal kept the old title, while the preview and the next firing used the new one. A firing skipped for overlap refreshes nothing, so a busy schedule could stay stale indefinitely. | `Client.UpdateTemplate` (`pkg/ui/templates.go`) calls the new `Store.ResyncTemplateSchedules`, which applies the copy a firing applies (`Schedule.syncTemplateCache`, factored out of `fireTaskSchedule`'s `recordFiring` callback) to every schedule naming the template. The schedule's own model choice is left alone, and Target/Base change only for a bound template, exactly as at firing. | `pkg/ui` `TestUpdateTemplateRefreshesItsSchedulesStraightAway` (fails without the fix: the schedule says `"Rotate api-token"` after the edit, and the refusal names it so). `ui/e2e/recurring-composed-walkthrough.spec.js` test 15 checks the row, the Run now confirm and the delete refusal in a real browser (fails without the fix). |

**Reproduction (before the fix):**

1. Create a template, then a schedule from it.
2. Edit the template's title.
3. Reload `/schedules`: the row still shows the old title.
4. Open it and press Run now: the confirm names the old title.
5. Delete the template: the refusal names the schedule by the old title.

Verified fixed in the browser at `c3c8a7a4f`: after a template title edit the
row, the Run now confirm and the refusal all read `Verify cache (v2)`.

**Noted, not changed:**

- **A suite's pass count and max passes have no upper bound** in the form or on
  the server (`model.Suite.Validate` only requires ≥ 1). `count: 1000` saved.
  Each pass files one task per template, so this is a policy question about how
  many tasks one gesture may queue, not a correctness bug. It is filed as a
  follow-up rather than capped here.
- The schedule rows' meta line is rendered in lower case by CSS, so zone names
  read `asia/calcutta`, `america/new_york`. Carried forward.
- Searching suites does not match the names of the templates inside them,
  while searching schedules matches a schedule's template name.
- The previous report's notes still hold:
  - Run now on a paused schedule doesn't mention the pause.
  - A template delete asks for confirmation before the server refuses it for
    its dependents.

## Tests run

| Suite | Result |
| --- | --- |
| `go test ./pkg/ui -run TestUpdateTemplateRefreshesItsSchedulesStraightAway` | pass with the fix, fail without it |
| `go test ./pkg/ui ./pkg/model ./pkg/orchestrator -run 'Schedul\|Template\|Suite\|Fir'` | pass |
| `npx playwright test e2e/recurring-composed-walkthrough.spec.js -g "15\."` | pass with the fix, fail without it |
| `npx playwright test e2e/recurring-composed-walkthrough.spec.js e2e/{schedules,suites,templates}_workflows.spec.js --project=chromium` | 18 passed |
| grain CI full suite on the pushed branch | see the pull request |
