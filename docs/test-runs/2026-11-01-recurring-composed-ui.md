# Test run — 2026-11-01 — Processes: schedules, templates, suites, suite runs and triggers

An end-to-end walkthrough of grain's recurring and composed-work UI: the
Processes page, and the schedules, templates, suites, suite runs and triggers
it lists. It ran in a real Chromium against a real `grain demo` server.

Run by task 2088 (attempt 2; attempt 1 was paused on a usage limit before
it pushed anything).

## Environment

- Tested `main` at `df38c2b5` (Merge pull request #333). The two fixes below
  are on top of it, at `2d49ce7f` and `d74f58b2`.
- UI built with `npm ci && npm run build` into `pkg/ui/static`. `grain` built
  with `go build -tags grain_e2e ./cmd/grain`, so the demo embeds the real
  UI. Go 1.26.10, Node 20.19.0, Playwright 1.62.1 (Chromium,
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- Server:
  `grain demo -addr 127.0.0.1:4290 -open=false -access-code-file … -schedules -github-sim -fake-ci -scheduled-workflows`.
  It uses a throwaway embedded store under `/tmp/grain-demo-*`, served over
  plain HTTP on loopback. The browser signed in through
  `POST /api/auth/access-code`, which gives the context its session cookie.
- Browser: ad-hoc Playwright scripts outside the committed suite (not
  committed), run at 1400×900. The phone and dark checks ran at 390×900
  with `colorScheme: dark`. Each script logged console errors and warnings,
  page errors, failed requests and every response ≥ 400.
- No Gemini key was present at `/home/debian/.gemini-api-key`, so nothing
  used Gemini. The demo's connections advertise `gemini-2.5-pro` (the
  deployment default) and `gemini-2.5-flash`.

## Stubs and seeds

Every create, edit, run, pause, resume and delete went through visible
controls in the browser, against the real server. Only these stood in for a
boundary the demo does not run:

| What | How | Why |
|---|---|---|
| Repository declarations | Committed `.grain/templates/{nightly,docs,opus}.yml`, `.grain/suites/sweep.yml`, `.grain/schedules/{nightly,sweep}.yml`, `.grain/workflows/nightly.yml` (cron `11 2 * * *`) and `.grain/triggers/design.yml` into the `-github-sim` bare repo (`main` and `candidate`). Then ticked **Read acme/widgets's own declarations** on the repo's Advanced tab in the browser. | `-scheduled-workflows` reads declarations from the sim repo, which starts empty (`demo.log`: "push .grain files there"). The real `declarations` reconciler did the sync: "acme/widgets at 2d969cf14094 declares 2 template(s), 1 suite(s), 3 schedule(s), 1 trigger(s)". |
| Agent finishing a suite pass | `ui/e2e/releasehelper -port 4290 complete-suite-pass`, then `sync-suites` (one cycle of `orchestrator.SyncSuites`). | No agents run in the demo. The helper finishes the newest active run's current-pass tasks the way an agent's closing comment would. The pass-advance logic is the real reconciler's. |
| An agent's `propose_task` | `POST /api/tasks` with `proposedBy: "25"` (a suite-run task). | Records the `proposed-by` link that an agent's proposal records, so a follow-up exists under a suite-run task. |
| CI executing a workflow run | `POST /api/e2e/fake-ci/runs/fake-ci-1/finish {"conclusion":"failure","job":"test"}` | `-fake-ci` accepts runs but never executes them. |
| Probes, not gestures | `curl` reads of `/api/schedules`, `/api/templates`, `/api/suite-runs`. One `curl POST /api/schedules` with `everyNMinutes: 5`. One in-page `fetch` create+DELETE of a throwaway template. | To confirm what was persisted, that the server enforces the ten-minute floor itself, and how Chromium reports a 204 (see "Not bugs"). |

## Steps and outcomes

### Processes page, chips, search, query

1. Opened **Processes** from the rail at `/processes`. The rail count
   (6 on the seeded demo, 13 after the declarations sync, 20 at the end)
   always matched **All N** and the rows. No console errors.
2. Kind chips, each pressed on and off: Schedules `?kind=schedule` (4 rows),
   Workflow schedules `?kind=workflow` (1), Templates `?kind=template` (5),
   Suites `?kind=suite` (2), Triggers `?kind=trigger` (1). With one creatable
   kind chosen, the header's button became **+ New schedule**, **+ New
   template** or **+ New suite** and opened that form directly. With
   Workflow schedules, Triggers or two kinds, it stayed **+ New** with the
   menu.
3. Templates + Suites with the search "declared" gave
   `?kind=template,suite&q=declared`, and every chip's count followed the
   search. Reload kept the search and the 3 rows.
4. Narrowing replaces the history entry rather than pushing one, by design
   (`App.jsx`: "Going somewhere pushes; narrowing where you already are
   replaces"). Rail → Inbox, then Back, returned to
   `/processes?kind=template,suite&q=declared` with the field, chips and
   rows restored. Forward and Back again behaved the same.
5. Old links all landed on the matching view or item:
   - `/schedules` → `?kind=schedule`
   - `/templates` → `?kind=template`
   - `/suites` → `?kind=suite`
   - `/suites/suite-1` → `/processes/suites/suite-1` (Edit suite pane)
   - `/schedules/sched-1` and `/templates/template-1` → their panes
6. Missing ids (`/processes/suites/runs/999`, `…/runs/abc`,
   `/processes/schedules/nope`, `/processes/templates/nope`,
   `/processes/suites/nope`) fall back to `/processes` with an alert, for
   example `no suite run "999"`.
7. Junk query values:
   - `?kind=bogus` is dropped, and `?kind=schedule,bogus` keeps the schedule
     chip.
   - A search matching nothing (`?q=✓`) shows "No processes match your
     search." with every chip at 0.
   - `?sort=bogus:up` gave **bug 1** (below).
8. Sort: the default is **Next run Soonest first**. Last run (oldest/newest),
   Name (A–Z/Z–A) and Kind (first/last) each wrote `?sort=<key>:<dir>` and
   survived reload. A suite's last run is its newest run's start, and a
   template's is that of the schedules firing it. The column and the sort
   read the same field.
9. Custom order: removing the sort key gives **Custom order**
   (`?sort=custom`) and a **Move …** handle on every row. Each way of moving
   worked:
   - Tap: the handle, then **Move here, above …**.
   - Keyboard: focus the handle, Enter, focus the slot, Enter.
   - Pointer drag: `dragTo`.
   - Escape cancels a pick.
   The order survived reload.
10. Keyboard: Tab from the search reaches Filter, Sort, the Suite runs
    **+ Run**, the header buttons, every chip, then each row's name link and
    its Use…/Run… button. Enter on a row link opens its pane.
11. Phone (390 px) and dark: `/processes`, `?kind=suite`, a suite run pane,
    a schedule pane, a template pane and a declared schedule pane. None
    scrolled sideways, and nothing reached past the viewport.

### New menu

The **+ New** menu offers Schedule, Template and Suite, and the hint
"Workflow schedules and triggers are declared in a repository's .grain/
directory -- declarations." links to the doc.

### Schedules

1. **Raw task schedule** (New → Schedule, Template "None"):
   - Fires offers A task and A suite.
   - Repeat offers every N hours, every N minutes, daily, weekly and
     monthly.
   - Every N minutes = 5 was refused by the browser ("Value must be greater
     than or equal to 10."), and the helper says "At least 10". The server
     also refuses it: `recurrence: everyNMinutes must be at least 10`.
   - Saved at 15 minutes with **Start it anyway** as the overlap policy.
     The row read `every 15m | can overlap | … | next 4:07:34 AM`. The
     `-schedules` loop fired it on time later (last 4:07:35, next 4:22:34).
2. **Model picker**: Deployment default (gemini-2.5-pro), gemini-2.5-pro and
   gemini-2.5-flash, only what the connections advertise. On a schedule
   firing a template, the empty choice reads "The template's model".
   Schedules firing a suite have no model picker.
3. **Edit** (deep link `/processes/schedules/sched-3`): changed the title,
   added the Grain API grant `api:self-debug` from **Add grant…** (the
   listing groups the Grain API presets task-admin, ci-read,
   dispatch-control, deployment-ops (dangerous), release-manager,
   self-debug and others), set Priority = Low, Network = Egress, and
   Read-only repos = acme/upstream. After saving and reloading, every value
   came back in the pane.
4. **Writes**: adding `acme/downstream` (not a target repo) was refused in
   the live **What this schedule files** preview and again on save:
   "acme/downstream is not on this deployment's target repo list … an
   operator widens targetRepos under Settings." The preview follows edits
   ("as filled in above").
5. **Weekly / monthly / zone**:
   - Weekly Friday 17:45 America/New_York read
     `fridays at 17:45 america/new_york`, next 11/6 22:45 UTC (EST after
     DST ends on 11/1). The time label said "EDT", which was right at the
     time.
   - Monthly day 31 read `monthly on day 31 …`, next 11/30, clamped.
6. **From a template**: chose `wt-full-template`, daily at 09:30.
   - The template row's next run changed from "on demand" to 9:30 with no
     reload.
   - **Run now** confirmed with "Run "WT: full template staging" now? It
     starts a task from the template "wt-full-template" straight away; the
     schedule's own next run is unchanged.", and the schedule row and the
     template row both showed last 3:57:15 without a reload, and again after
     one.
7. **Firing a suite** (Release Qualification Suite, every 6 h):
   - Leaving Base branch empty was refused by `required` ("required: a
     suite run stacks its tasks against one branch"). With `main` it saved.
   - The suite row's next run changed from "on demand" to the schedule's
     with no reload.
   - **Run now** started a run that the Suite runs list marks **scheduled**.
     That chip opens `/processes/schedules/sched-5`.
8. **Pause** (no confirm, as it is reversible): the row read
   "Held -- fires … if resumed by then | paused", including after reload,
   and the pane offered **Resume**. Resume returned it to Enabled.
9. **Delete** confirms with "Delete the schedule "…"? Tasks it already filed
   are not affected.". Dismissing kept the pane. Accepting removed the row,
   and the template it fired went back to "on demand" with no reload. Three
   throwaway schedules were deleted this way.
10. **Unsaved edits**: Escape on a dirty pane asks "You have unsaved changes
    here. Leave and discard them?", and so does a page navigation
    (beforeunload). **Cancel** discards without asking, as `Overlay.jsx`
    documents ("already somebody saying 'throw it away'").

### Templates

1. **New template** with every field filled:
   - Name, a title with `{{environment}}`, a description, and an input
     `environment` with options staging and production (one per line) and
     default staging.
   - Repo acme/widgets @ main, reviewers ana and bo, auto-merge on,
     **Interactive** on, network None, model gemini-2.5-flash.
   - Grant `api:ci-read`, Priority High.
   - Saved, reloaded and reopened: every field came back, and the API row
     matches.
   - Typing the options comma-separated was refused by the server: "field
     "environment": default "staging" is not one of its options". This is
     correct (options are one per line), and the message is clear.
2. **Use…**:
   - The pane says "This template files a live chat: the agent can talk with
     you while it works, and the chat opens as soon as the task is filed."
   - The environment picker offers "Default (staging)", staging and
     production, and the "What this files" table lists each value "from the
     template".
   - **Create task** filed task 20 and opened `/tasks/20`.
3. A **model no connection serves**: the declared template `opus.yml`
   (`model: claude-opus-5-5`) shows "claude-opus-5-5 (no connection
   advertises it)" in its pane. Use… lists Model claude-opus-5-5. A schedule
   on it offers "The template's model", gemini-2.5-pro and gemini-2.5-flash.
4. **Dependency-aware delete** of `release-qualification`:
   - The confirm reads "Delete the template …? Schedules and suites using it
     must be repointed or deleted first." Dismissing keeps it.
   - Accepting is refused (400) with "template is used by 1 schedule(s)
     ("Release Qualification: Run sanity checks & unit suites") and 1
     suite(s) ("Release Qualification Suite"); repoint or delete the
     schedules and remove it from the suites first", and the row stays.
5. Used from a schedule (Schedules 6) and from a suite (below).

### Suites

1. **New suite** "WT suite":
   - Picked security-audit, then **+ New template** inline
     (`wt-inline-template`), which was added to the suite's selection on
     save.
   - Run mode **Run until no repo changes or new tasks**, Max passes 0
     (refused: ≥ 1), then 3.
   - **Require approval** on, and auto-merge (on by default) turned off.
   - The row read "run until clean (max 3) | 2 templates | requires
     approval".
2. **Edit**: switched to a fixed number of times, 3. The row read `run 3×`.
3. **Delete** a throwaway suite ("WT fail suite"). The confirm reads "Delete
   the suite "…"? Runs already started from it are unaffected.", and the row
   went.
4. Two templates named `wt-inline-template` exist because earlier, failed
   attempts at this step saved one each. Pickers tell them apart by id
   ("wt-inline-template — WT inline template task (template-6)").

### Suite runs

1. **From the suite row** (Release Qualification Suite → Run…, branch main):
   - The dialog shows "what each pass files", for example "2 passes of these
     tasks".
   - The run (id 1) appeared as active, pass 1, with tasks 22 and 23 filed.
2. **From the generic + Run** (Suite: WT suite, branch release/1.0): run 2
   showed "pass 1 · run 3× · each pass's tasks wait for approval".
3. A declared suite (Declared sweep) can be run from its row, and its pane
   says so ("It can still be run from the suites list").
4. **Finished**: three `complete-suite-pass` + `sync-suites` rounds took run
   2 to **succeeded**. `/processes/suites/runs/2` groups 6 tasks under pass 1,
   pass 2 and pass 3.
5. **Failed**: run 3 (WT fail suite, count 2) finished pass 1. Its suite and
   then its template were deleted through the UI, and two sync cycles ended
   it **failed** with "pass 2 could not be filed: template template-7 no
   longer exists", in the list and in the pane.
6. **Scheduled**: see Schedules 7. **Active**: run 1.
7. **Follow-up**: task 33, proposed by task 25, is listed under 25 in run 2's
   pass 1 ("tasks 7"), marked unapproved. Its link opens `/tasks/33`.
8. Task links in the pane (for example task 25) open `/tasks/25`. Back
   returns to `/processes/suites/runs/2`. Enter on a focused run row opens
   that run.

### Repository-declared rows

Every declared template, suite, schedule (template- and suite-firing) and the
workflow schedule shows **declared by acme/widgets** on its row. Its pane
opens with, for example:

> This schedule is declared by acme/widgets in .grain/schedules/nightly.yml,
> so it cannot be edited or deleted here. Change that file in the repository
> and grain picks it up within a couple of minutes. Run now still fires it
> once.

Delete, Pause, Save, + Add input, + New template and Add grant… are disabled,
and no input is editable (0 of 21, 6, 6, 8 and 5). Run now stays available.

- The workflow schedule's Run now started `fake-ci-1`. Its row briefly read
  "Last run (none): starting" before the reconciler named the run, under a
  second later. Once that run was finished as a failure, the row read "Last
  run fake-ci-1 on main: failure 4:14:01 AM, reviewed in task 37". Both
  links work (`/ci/fake-ci-1`, `/tasks/37`).
- The trigger row `design.yml` (next "on change") opens
  `/repos/acme/widgets/settings/advanced`, which lists "Path triggers:
  .grain/triggers/design.yml".

## Bugs found and fixed

### 1. An unknown sort key in a link leaves the sort button with no label

**Reproduction:** open `/processes?sort=bogus:up` (or `/?sort=bogus:up` on
the task list).
- The toolbar's sort button has no text, only its icon.
- Opening it shows "Then by…" over no rows, and no note.
- The unknown key had no row to remove it from, so it stayed in the URL
  (normalised to `sort=bogus:asc`) until the address was edited by hand.

**Cause:**
- `NarrowingBar` labelled the button `sort.length > 0 ? describeSort(…) :
  orderLabel`. `describeSort` and `SortRow` both skip keys with no
  attribute, so one unknown key produced an empty label and an empty
  editor.
- `processNarrowingFromSearch` kept any key, unlike `parseKinds`, which
  drops unknown kinds.

**Fix (`2d49ce7f`):**
- `NarrowingBar` goes by the keys it has an attribute for, so the label
  falls back to the view's own order and the editor shows "Sort by…" with
  its note.
- The Processes parser drops unknown keys, and with none left uses the
  default order, so the address normalises to `/processes`.

**Coverage:** `NarrowingBar.test.jsx` ("labels the sort by the keys it
knows…"), `processFilters.test.js` (`?sort=bogus:up`,
`?sort=bogus,name:desc`) and `e2e/processes-narrowing.spec.js` ("a link's
unknown sort key falls back to the default order"). The unit tests fail
without the fix.

### 2. A workflow schedule's Run now confirm says the review happens "straight away"

**Reproduction:** open the declared workflow schedule and press **Run
now**. The confirm reads "It starts a CI run of the workflow it was declared
in, to be reviewed when it finishes straight away; …".

**Fix (`d74f58b2`):** the review clause follows "straight away": "It starts
a CI run of the workflow it was declared in straight away, to be reviewed
when it finishes; …".

**Coverage:** `e2e/workflow-schedules-candidate-walkthrough.spec.js`'s
`runNow` helper asserts the wording. All 5 tests pass.

## Not bugs (recorded so the next pass does not chase them)

- **`requestfailed … /api/tasks net::ERR_ABORTED` every 3 s.** These are the
  app's conditional polls (`api.js`, `poll: true`, `cache: "no-store"` plus
  `If-None-Match`): the server answers 304, and Chromium reports that to
  Playwright as aborted. The page itself gets the 304.
- **`requestfailed DELETE … net::ERR_ABORTED` on every successful delete.**
  The server answers 204. A bare in-page `fetch` DELETE gets 204 in
  JavaScript, and Playwright still reports it as aborted, so this is the
  same reporting quirk, not the app.
- **Suite runs' "+ Run" sits in the page header rail.** `ListHeader`
  deliberately draws a second list's count and action into the rail
  (`PageHeader.jsx`). That is also why it comes early in the Tab order.
- **Cancel on a dirty pane does not ask.** This is documented in
  `Overlay.jsx`.
- **The Read declarations checkbox flips only after its save returns.**
  Playwright's `check()` reports "did not change its state", but `click()`
  then waiting works.

## Not reached

- Real agent runs and real CI execution. Both were stubbed as above.
- Writes to a second repository, because the demo's only target is
  acme/widgets (refused correctly).
- A trigger actually firing on a merge.

## Tests run

- `npx vitest run` (the full UI unit suite) on the fixed tree: 228 files,
  3578 tests, all passed.
- On the fixed build, `npx playwright test` of
  `e2e/processes-narrowing.spec.js`, `e2e/processes-walkthrough.spec.js`,
  `e2e/recurring-composed-walkthrough.spec.js`,
  `e2e/schedule-every-n-minutes.spec.js` and
  `e2e/suite-run-dialog-order.spec.js`: 32 passed.
- `npx playwright test e2e/workflow-schedules-candidate-walkthrough.spec.js`:
  5 passed.
