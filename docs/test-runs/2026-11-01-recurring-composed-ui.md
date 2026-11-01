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

---

# Second pass, task 2236 — Recurring and composed-work automation UI (2026-11-01)

A second, independent walkthrough of the same area later the same day, on a
newer `main`. It reached different ground from the first pass above (task
2088) and found one more defect, fixed on its own branch.

Task 2236 (`grain/049f1e09/task-2236`). An end-to-end walkthrough of
[Processes](/processes): schedules, workflow schedules, templates, suites,
suite runs and path triggers. It ran in a real headless Chromium driven by
Playwright against a real `grain demo` server. Every create, edit, run,
pause, delete, reorder and navigation gesture went through visible UI
controls. The API was used only to read back what a gesture persisted,
except where [Stubs](#stubs) says otherwise.

One defect was found and fixed on this branch (see
[Bug found and fixed](#bug-found-and-fixed)): a template's numeric task
priority could not be seen or changed in the template form, and no
composition preview mentioned it.

## Environment

| Item | Value |
| --- | --- |
| Commit walked | `86eff5045d17310ea2af176437a320a348521521` (`main`, "Merge pull request #340", the base of this branch); the fix was re-walked on this branch's head |
| Date | 2026-11-01, 04:44–05:20 UTC |
| Go / Node / npm / Playwright | `go1.26.10 linux/amd64` / `v20.19.0` / `10.8.2` / `1.62.1`, Linux |
| Browser | Playwright Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, chromium_headless_shell-1234), timezone UTC |
| Build | `cd ui && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/grain ./cmd/grain` |
| Server | `TMPDIR=<throwaway> /var/tmp/grain demo -addr 127.0.0.1:18080 -open=false -github-sim -fake-ci -scheduled-workflows -sensitive-hold -reconcile 500ms -schedules -access-code-file <file>`. A second demo on `:18081`, built from the fixed tree, was used to render the fix. |
| Sign-in | The "Enter access code" page, typed in the browser |
| Viewports | 1440×1400 desktop for the walk; 1440×900 and 390×844 (`isMobile`, `hasTouch`), light and dark, for the fix |
| Gemini key | `/home/debian/.gemini-api-key` is absent in this sandbox, so no Gemini-backed tooling (and no `LOOK=1`) was used. The demo's connection advertises `gemini-2.5-pro` (the deployment default) and `gemini-2.5-flash`. |

Every script recorded page errors, console errors, HTTP ≥ 400 responses,
failed requests and every non-GET `/api` call. The scripts were throwaway
files under `/var/tmp/walk/s` and are not committed. Neither are the
screenshots.

## Stubs

- **Agent execution boundary.** `grain demo` runs no agents. A throwaway Go
  helper (`/var/tmp/walkseed`) recorded only what an agent run leaves
  behind. It was built from a scratch `cmd/walkseed`, which was deleted
  before committing. It opens the demo's SQLite store with
  `sqlite.Open(sqlite.DefaultConfig(dir))` and `model.New`. Its verbs:
  - `complete-clean <task>`: sets `Observation.CompletedAt`.
  - `complete-changed <task> <pr>`: adds a `LinkFixes` link to
    `acme/widgets#<pr>`, then sets `CompletedAt`.
  - `close <task>`: sets `Observation.ClosedAt` with no completion.
  - `proposed-by <task> <parent>`: adds a `LinkProposedBy` link. It was
    set on follow-up task 29, created with `POST /api/tasks`, which is what
    an agent's `propose_task` produces.

  Every suite-run transition was made by the real reconciler
  (`-reconcile 500ms`): pass 1 → pass 2 → succeeded, and pass 1 → failed.
- **Simulated GitHub and fake CI** (`-github-sim -fake-ci
  -scheduled-workflows -sensitive-hold`). Repository declarations were
  pushed with plain `git` to the simulated `acme/widgets` bare repository
  that the server log names. The files were:
  - `.grain/templates/walk-declared.yml`;
  - `.grain/suites/walk-sweep.yml` (count 2, require-approval);
  - `.grain/schedules/walk-nightly.yml` (template, daily 04:00);
  - `.grain/schedules/walk-sweep.yml` (suite, Fridays 05:00);
  - `.grain/workflows/nightly.yml` (`on: schedule` cron `11 2 * * *`);
  - `.grain/triggers/docs.yml` (`on: merge`, `paths: [docs/**]`).

  Within the two-minute declaration poll these replaced the demo's own
  seeded declarations (`nightly-maintenance`, `payments.yml`, `Smoke
  Verification Suite`), because the repository's files are the source of
  truth.
- **Two API-seeded templates**, both for presentation the UI cannot
  produce:
  - `walk-unserved` (model `claude-opus-5-5`), because the UI only offers
    models a connection serves;
  - `walk-prio` (`priority: 9`), because before the fix the form had no
    priority field. That is the bug below.
- **Playwright route stub**, removed after the check: `**/api/suite-runs*`
  answered `500 {"error":"synthetic suite-runs 500"}`.

## Steps and outcomes

### 1. Processes list, narrowing, history, old links

1. The rail's **Processes** entry read 9 on a cold load of `/inbox` and 10
   once Processes had opened. The chips read **All 10 · Schedules 3 ·
   Workflow schedules 0 · Templates 4 · Suites 2 · Triggers 1**. Once the
   declarations were read, they read **All 12 · Schedules 4 · Workflow
   schedules 1 · Templates 4 · Suites 2 · Triggers 1**. See
   [Notes](#notes-not-defects) on the cold count.
2. The kind chips are a multi-select:
   - Schedules wrote `?kind=schedule`;
   - adding Workflow schedules wrote `?kind=schedule,workflow`, and so on;
   - selecting all five collapsed back to `/processes`.

   Search `release` wrote `?q=release` and narrowed each chip's count
   (**All 3 · Schedules 1 · Templates 1 · Suites 1**). With Templates it
   became `?kind=template&q=release`. Reload kept the URL, the search box
   value and the counts. `/inbox` → Back returned to the same URL, and
   Forward went to `/inbox` again.
3. Keyboard: Enter on a focused chip narrowed to `?kind=suite`, and Space
   toggled it back to `/processes`.
4. Old links:
   - `/schedules`, `/templates` and `/suites` landed on
     `/processes?kind=schedule|template|suite`.
   - `/suites/suite-1`, `/templates/template-1` and `/schedules/sched-1`
     opened those items' editors under `/processes/…`.
   - Missing items fell back to `/processes` with an alert:
     `/suites/runs/1` before any run existed (`no suite run "1"`),
     `/templates/nope` (`no template "nope"`) and
     `/processes/suites/runs/99` (`no suite run "99"`).
5. `?q=zzzznothing` read "No processes match your search." with every chip
   at 0. The Suite runs list below the rows is not narrowed by search.

### 2. New menu

`+ New` opened **Schedule · Template · Suite** and the hint "Workflow
schedules and triggers are declared in a repository's .grain/ directory --
declarations". The hint links to
`https://github.com/ai-on-gke/grain/blob/main/docs/declarations.md`.

- Narrowed to Schedules, Templates or Suites alone, the button read `+ New
  schedule` / `+ New template` / `+ New suite` and opened that form
  directly.
- Narrowed to Triggers or to Workflow schedules (neither can be created
  here), it stayed `+ New` and opened the menu.

### 3. Schedules

1. **Raw task schedule** (`Walk raw {{date}}`, sched-3): base `main`,
   model `gemini-2.5-flash`, reads `acme/docs`, network **None**, overlap
   **Start it anyway**, **Every N minutes**.
   - The model picker offered only "Deployment default (gemini-2.5-pro)",
     `gemini-2.5-pro` and `gemini-2.5-flash`.
   - The minutes field refused 5 ("Value must be greater than or equal to
     10"; helper "At least 10; counted from the last firing") and 10.5
     ("…The two nearest valid values are 10 and 11"). 10 saved.
   - The live preview credited every row to the schedule.
   - The row read `every 10m · can overlap`, with the next run ten minutes
     after creation.
   - `{{date}}` is not a placeholder grain expands. The filed tasks are
     titled literally, as documented.
2. **Edit.** Adding `acme/docs` as a write target was refused by the
   server with a clear alert: "acme/docs is not on this deployment's target
   repo list … an operator widens targetRepos under Settings." Navigating
   away from the unsaved edit raised the browser's leave-page prompt. A
   valid edit added the **Grain API presets → ci-read** grant
   (`api:ci-read`) and saved; the API read it back.
   - The grant picker stays open after a pick, to ask what the grant
     applies to, and Escape there also closes the pane.
3. **Pause / Resume / Run now.** Each closes the pane, as
   `ScheduleOverlay.jsx` documents.
   - Pause sent `PATCH {"enabled":false}`. The row then read "Held --
     fires 11/1/2026, 4:58:21 AM if resumed by then · paused", before and
     after reload.
   - Resume sent `{"enabled":true}`.
   - Run now asked `Run "Walk raw {{date}}" now? It starts the task "Walk
     raw {{date}}" straight away; the schedule's own next run is
     unchanged.` The row's last run filled in with no reload and kept its
     next run. Reload showed the same. The scheduler (`-schedules`) later
     fired it on its own at the ten-minute mark.
4. **Suite-running schedule.** "A suite" → `Release Qualification Suite`,
   base `main`, weekly.
   - Submitting it empty, the browser refused the base field.
   - The preview read "2 passes of these tasks", each row credited to the
     template, suite or deployment.
   - The row read `mondays at 09:00 · suite: release qualification suite`.
     The **suite's** row then showed next run 11/2/2026 9:00 AM, with no
     reload.
5. **Template-bound schedule.** Picking `security-audit` reduced the form
   to template, model, repeat, time zone and overlap. The template option
   list was "none -- fill in the fields below", `walk-declared`,
   `security-audit` and `release-qualification`. Model `gemini-2.5-flash`
   was credited "from the schedule"; every other row came "from the
   template". After **Run now**, the confirmation named the template, and
   the **template's** row showed last run 4:52:57 AM and next run 9:00 AM
   with no reload.

### 4. Templates

1. **Fully populated template** (`walk-full`):
   - name, title, description, repo `acme/widgets`, base `main`;
   - reads `acme/docs`, reviewer `alice`;
   - **Auto-merge** and **Interactive** on;
   - network **Egress**, model `gemini-2.5-flash` (only served models
     were offered), legacy capability `gcp key`;
   - custom fields priority high, requested-by, due date and estimate.

   Pressing Enter in the plain comma-separated Reviewers box submitted the
   form, which is standard HTML, and the rest was filled in as an edit. The
   PATCH persisted every value. Reopening showed each one back.
2. **Use… on the interactive template.** The dialog showed "This template
   files a live chat: the agent can talk with you while it works, and the
   chat opens as soon as the task is filed." in place of the Approved box,
   and a preview with every row "from the template". **Create task**
   opened `/tasks/24`, which the API reports as `interactive: true` with
   the template's model, network, reads and capability.
3. **Unserved model.** `walk-unserved` showed `claude-opus-5-5 (no
   connection advertises it)` in its field and as the last picker option,
   after the served models.
4. **Numeric priority.** `walk-prio` (priority 9) filed priority-9 tasks
   from Use… (task 33) and from a bound schedule's Run now (task 34).
   However, neither its form nor any preview said so. This is the bug
   below.
5. **Dependency-aware delete.** A schedule bound to `walk-full` was created
   from the form, and `Walk suite` (§5) already used it. Delete asked
   `Delete the template "walk-full"? Schedules and suites using it must be
   repointed or deleted first.`
   - Dismissing left the pane open.
   - Accepting showed the server's refusal (HTTP 400): "template is used
     by 1 schedule(s) ("Walk full template check") and 1 suite(s) ("Walk
     suite"); repoint or delete the schedules and remove it from the suites
     first".
   - After the schedule and suite were deleted, it deleted cleanly.

### 5. Suites

1. **Validation and default mode.** `+ New suite` with nothing filled in
   was refused by the browser on the name. Run mode defaulted to "Run until
   no repo changes or new tasks" with Max passes 5.
2. **Templates, including one made inline.** `walk-full` was picked, and
   the form's `+ New template` opened a nested template form.
   `walk-inline` was saved from it and came back selected, with the suite's
   name kept.
3. **Fixed count.** "Run a fixed number of times" refused 0 ("…greater
   than or equal to 1") and 101 ("…less than or equal to 100").
4. **Save.** Fixed ×3 with **Require approval** and Auto-merge on saved as
   `run 3× · 2 templates · requires approval`.
5. **Edit.** Unchecking Auto-merge sent a PATCH carrying the revision.

### 6. Suite runs

1. **From the suite row.** `Walk suite` → **Run…** showed "3 passes of
   these tasks" with each value's source. An empty branch was refused
   ("Please fill out this field"). With `main` it started run 1.
2. **From the generic `+ Run`.** `Release Qualification Suite`, branch
   `main`, started run 2. The suite options were all three suites,
   including the declared `walk sweep`.
3. **Active.** The Suite runs list read `Release Qualification Suite ·
   active · pass 1 · run 2×` and `Walk suite · active · pass 1 · run 3× ·
   each pass's tasks wait for approval`. Run 1's tasks were `unapproved`.
4. **Failed.** Closing task 25 failed run 1. The list and
   `/processes/suites/runs/1` read "failed · pass 1 had a task that
   failed, or closed without completing".
5. **Running → succeeded.**
   - Task 27 completed with a PR link (shown "on merge queue") and 28
     completed clean, and follow-up 29 was proposed by 27.
   - The reconciler filed pass 2 (30, 31). `/processes/suites/runs/2`
     showed "Pass 1 · 3", with follow-up 29 nested under 27, and "Pass 2 ·
     2".
   - Completing pass 2 turned the list row to **succeeded**. The suite's
     own row then showed last run 4:56:30 AM (its newest run) and the next
     run of its schedule.
6. Clicking the follow-up on the run page opened `/tasks/29`, and Back
   returned to `/processes/suites/runs/2`. Run 1 still rendered, named
   `Walk suite`, after that suite was deleted.

### 7. Repository-declared rows

1. **Provenance chips.** Every declared row carried a "Declared by
   acme/widgets" chip whose title names the file: the template, suite,
   both schedules, the workflow schedule (`.grain/workflows/nightly.yml`)
   and the trigger (`.grain/triggers/docs.yml`).
2. **Read-only panes.** Each declared pane said "This … is declared by
   acme/widgets in `<file>`, so it cannot be edited or deleted here. Change
   that file in the repository and grain picks it up within a couple of
   minutes."
   - The suite's pane adds "It can still be run from the suites list";
     the schedules' panes add "Run now still fires it once."
   - Each pane had 0 enabled inputs, and Delete, Save and (on schedules)
     Pause were disabled. Run now stayed enabled.
   - No PATCH, PUT or DELETE was sent, only composition-preview POSTs.
3. **Trigger.** The `docs.yml` row opened
   `/repos/acme/widgets/settings/advanced`, which read "Read at
   d4673429d3e5: 1 template(s), 1 suite(s), 3 schedule(s), 1 trigger(s)"
   and "Path triggers: .grain/triggers/docs.yml".

### 8. Sort and custom order

1. **Sort.** The `Attribute for sort 1` picker offers all 19 process
   attributes:
   - Last run → `?sort=last:asc`, oldest first;
   - Name → `?sort=name:asc`;
   - Kind → `?sort=kind:asc`;
   - Next run → the default, so no `sort=`; the button reads "Next run ·
     Soonest first".

   Custom order is not an attribute: **Remove sort 1** wrote
   `?sort=custom`, and the button reads "Custom order".
2. **Keyboard reorder.** `Move walk-full` → Enter offered 18 "Move here,
   above …" slots, and Enter on the first moved it to the top. Focus stayed
   on `Move walk-full`, and the order survived reload.
3. **Pointer reorder.** Dragging `Move Walk suite` onto the `docs.yml`
   item row moved it to the top, and that survived reload. A first attempt
   that dropped on the column-header row did nothing, as it should.
4. **Where the order lives.** The order is kept per browser in
   localStorage (`grain.list-order.processes`, `listOrder.js`), so a fresh
   browser context does not see it and no request is sent. That is by
   design.

### 9. Keyboard, states, phone, console, cleanup

1. **Tab order.** Tab from `+ New` reaches Search processes…, Filter, the
   sort button, `+ Run`, the top-bar buttons, each kind chip, then the
   rows. Enter on a row's name link opened its editor, and Escape closed
   it back to `/processes`.
2. **Error state.** With `/api/suite-runs` stubbed to 500, the page showed
   the alert "synthetic suite-runs 500" and still listed its 15 rows.
3. **Phone.** At 390×844, `/processes` and `/processes/suites/runs/2`
   had no horizontal overflow, and the chip strip spans the width.
4. **Console and network.** The browser console showed only:
   - the 401 the sign-in page answers before the access code is entered;
   - the expected 400s, from the refused write target and the refused
     template delete;
   - `net::ERR_ABORTED` `/api/tasks` polls cut short by navigation.
5. **Cleanup.** Every throwaway schedule, suite and template was deleted
   through its pane's Delete, each after a confirmation naming it and what
   is unaffected:
   - "Tasks it already filed are not affected";
   - "Runs already started from it are unaffected";
   - "Suite runs it already started are not affected".

   The chips read All 12 again after reload. Suite runs and filed tasks
   remain, as the confirmations say.

### 10. Existing and new specs

The fixed tree passed 41 of 41 tests in `npx playwright test` over:
`template-priority` (new), `processes-narrowing`, `processes-walkthrough`,
`recurring-composed-walkthrough`, `processes_workflows`,
`schedule-every-n-minutes`, `built-in-template` and
`suite-run-dialog-order`. `go test ./pkg/ui/` passed, as did vitest
`CompositionPanel`, `UseTemplateOverlay`, `InteractiveTemplate`,
`TemplateFields` and `ProcessesList` (36 tests).

## Bug found and fixed

### A template's task priority was invisible and uneditable in the UI

- **Reproduction.**
  1. On `grain demo`, `POST /api/templates` with `{"name":"walk-prio",
     "title":"Walk priority nine","repo":"acme/widgets","priority":9}`
     (or declare `priority: 9` in `.grain/templates/*.yml`).
  2. Open `/processes/templates/<id>`: the form has no priority field.
  3. Open Use… on it: the preview lists Title through Approval, with no
     priority.
  4. Create the task: `GET /api/tasks/<id>` reports `priority: 9`. A
     schedule bound to the template files priority-9 tasks too.

  So a template that jumps every task it files up the queue looks exactly
  like any other, and only the API can change that priority.
- **Cause.** Numeric task priorities (`98eb06ac`) reached the new-task
  form, the task editor and the task detail pane, but not
  `TemplateOverlay.jsx`, and `ui.composedTaskFrom` never emitted a
  priority field.
- **Fix.**
  - The template form has a **Task priority** number field (min 1; blank
    means the default 5). On an edit, emptying it sends `priority: 0`,
    which the server already treats as a clear.
  - The composed-task preview adds a **Task priority** row, credited
    "from the template", whenever the resolved priority is not the
    default. So the Use…, schedule and suite previews all say it, and the
    common case adds no row, matching the task detail pane.
- **Coverage.**
  - `TestCompositionShowsATemplatesOwnPriority` (`pkg/ui`): the template
    and bound-schedule previews carry `priority: 9 (template)`, and a
    template with no priority has no row.
  - `ui/e2e/template-priority.spec.js`: sets 8 in the form, checks the
    POST, reopens, reads the Use… preview row, files the task (priority
    8), then empties the field and checks the PATCH and the cleared
    template. On `main` it fails at the missing field.
- **Rendering.** On a demo built from the fix, the template pane and the
  Use… dialog were shot at 1440×900 and 390×844, light and dark, with
  bounding boxes checked:
  - The field lines up with Reviewers (x 261, width 704 on desktop; x 17,
    width 357 on phone), and its helper text ends inside it.
  - The preview row ("Task priority | 9 | from the template") sits inside
    the dialog (x 444–996 of 420–1020; 16–374 of 0–390), directly under
    Approval.
  - Its label uses the theme's secondary text colour.
  - Neither page overflows horizontally.

## Notes, not defects

- **The rail's Processes count is one short on a cold load.** It reads 9
  until Processes has been opened once, then 10. Path triggers are fetched
  only on the way into Processes (`App.jsx`, "Fetched on the way into that
  page"), so a deep link elsewhere counts schedules, templates and suites
  but not triggers. It is a documented trade-off, not a fault.
- **The Suite runs list ignores search.** With `?q=` matching nothing, the
  page says "No processes match your search." above an unfiltered Suite
  runs list.
- **Two kinds of "priority".** The demo deployment declares a custom field
  named `priority` (low/medium/high), so the template form now shows both
  that field and the numeric **Task priority**. The labels differ, and the
  numeric one is the one dispatch orders by.
- **A closed task in a failed run shows no state chip.** On
  `/processes/suites/runs/1`, the closed task 25's row shows no state chip,
  while its unapproved sibling reads "unapproved". The run's own failure
  line says why the run failed.
