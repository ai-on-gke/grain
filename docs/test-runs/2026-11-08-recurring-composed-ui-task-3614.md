# Recurring and composed work UI walkthrough -- 2026-11-08 (task 3614)

Task 3614. A second walkthrough that day of the Processes page and
everything it lists -- schedules, workflow schedules, templates, suites,
suite runs and triggers -- in a real Chromium against a real `grain demo`
server. Task 3357's run of the same morning is
`2026-11-08-recurring-composed-ui.md`; this file carries the task number
so that report is kept, as the other same-day reruns are
(`…-task-3458.md`, `…-task-3463.md`). It found three bugs, all fixed in this
change.

## What was tested

- **Commits:** the base build is `49587a69` (`origin/main` when the run
  started, "Merge pull request #953"), built from a separate worktree. The
  fixed build is this change, `0907e276`
  (`v0.0.0-20261108130219-0907e27606e6`); its product code is the same as
  `f5452482`, as the two commits after it only add tests. The same scripted
  walkthrough ran against both binaries, so every "before" below comes
  from the same gestures as its "after".
- **Server:** `go build -tags grain_e2e ./cmd/grain` (Go 1.26.10), run as
  `grain demo -addr 127.0.0.1:0 -open=false -access-code-file … -schedules
  -github-sim -fake-ci -scheduled-workflows -legacy-capability -reconcile
  500ms`, with `TMPDIR` pointing at a scratch directory. That makes the
  store a throwaway embedded SQLite file, which was removed afterwards.
- **Frontend:** `npm ci && npm run build` in `ui/` (Node 20.19.0), embedded
  in each binary.
- **Browser:** Playwright Chromium (`/var/cache/playwright`, chromium-1234),
  headless, timezone UTC. The walk ran at 1440×900 and switched to
  390×844 for the phone checks. Screenshots were taken with
  `ui/e2e/screenshot.mjs --full-page` at desktop and phone width, in light
  and dark, for `/processes?kind=workflow`, the declared schedule's pane
  and `/processes/templates/template-1`. They were attached to the task and
  are not committed.
- **How it was driven:** a throwaway Playwright script outside the
  checkout, not committed. It signs in on the "Access code" page and then
  acts only through visible controls. Each step soft-fails, so one missed
  selector cannot hide the rest of the run. The page logged `pageerror`,
  console errors, `/api/` responses of 400 or more (with their bodies),
  failed requests, every DELETE, and every dialog's text along with whether
  it was accepted or dismissed. The REST API was used only to read back
  what a gesture had saved, and for the stubs below.
- **Model:** this sandbox had no `/home/debian/.gemini-api-key`, so there
  was no Gemini-backed run and no `LOOK=1`. Layout was judged from DOM
  bounding boxes and `scrollWidth`/`clientWidth`, not by looking at
  images. The demo deployment advertises `gemini-2.5-pro` (the default) and
  `gemini-2.5-flash`.
- **Result:** 46 of 46 steps passed against both builds. The three bug
  checks differ between the builds, as listed under Bugs.

### Stubs and seeding

| Boundary | How it was stood in for |
| --- | --- |
| A repository's `.grain/` | Pushed to the simulated GitHub's bare repo (`GET /api/e2e/scheduled-workflows` names it): `workflows/nightly-ci.yml` (cron `15 3 * * *`), `templates/decl-bump.yml` (an `ecosystem` input field, `priority: 7`, `model: retired-model-2024`, `api:tasks-read`), `templates/decl-pair.yml` (`interactive: true`), `suites/decl-sweep.yml`, `schedules/decl-bump-sched.yml` (`template: decl-bump` **and** `capabilities: [api:ci-read@acme/widgets]`, daily 04:30), `triggers/docs-guard.yml`. They were then turned on from the UI: Repos → acme/widgets → Advanced → "Read acme/widgets's own declarations". |
| Capability policy | `PUT /api/capability-policy`: deny `…/cloud#cloud-viewer` to tasks on acme/widgets, and admit `…/cloud#cloud-key` on acme/gadgets only. These target the demo's own capability server. |
| Agents finishing a suite pass | `ui/e2e/releasehelper complete-suite-pass`, which marks the newest active run's current-pass tasks completed as an agent's closing comment would. The demo's 500 ms reconcile loop then advances the run. |
| An agent proposing a follow-up | A plain task filed with `POST /api/tasks`, then a `task_link (…, 'proposed-by', <pass-1 task>, 0)` row written straight into the store. `proposedBy` is daemon-only over the API. |
| A legacy base-less suite run | `UPDATE suite_run SET base='' WHERE id=<run A>` in the store, after run A finished. |
| A workflow's CI run finishing | `POST /api/e2e/fake-ci/runs/<id>/finish {conclusion: success}` (`-fake-ci`). |

Every create, edit, run, pause, resume and delete went through the browser
UI against the real server.

## Steps and outcomes

### Processes page

1. **Rail and count.** From `/`, the rail's Processes link reads `11`. On
   `/processes` both the link and the header read `12`, and the page has
   12 rows. The kind chips read All 12, Schedules 3, Workflow schedules 1,
   Templates 5, Suites 2, Triggers 1. The rail counts triggers only once
   the page has fetched them, which `App.jsx` does lazily on that view (the
   same as task 3357 recorded).
2. **Kind chips.** Each chip narrows the table to its kind and writes
   `?kind=<id>`. With one creatable kind picked, New becomes
   `+ New schedule`, `+ New template` or `+ New suite` and opens that pane
   directly. Workflow schedules and Triggers keep the `+ New` menu. Suite
   runs are listed only when suites are among the kinds shown.
3. **Search and narrowing.** With Schedules and Templates picked and
   "Declared" searched, the URL is `?kind=schedule,template&q=Declared` and
   the page shows three rows. A reload keeps the URL, the search box and
   the rows. Back goes to `?kind=schedule` and Forward restores the search.
   A search that matches nothing shows "No processes match your search."
4. **New menu.** It lists Schedule, Template and Suite, plus "Workflow
   schedules and triggers are declared in a repository's .grain/ directory
   -- declarations", which links to `docs/declarations.md`.
5. **Filter.** Filter → Filter by… → Enabled adds the condition and
   writes `?f=enabled:any:`.
6. **Sort.** The default is "Next run, Soonest first", with no move
   handles. Sort 1 was switched to Last run, then Name, Kind and back to
   Next run. Each choice rewrote the query, reordered the rows and
   survived a reload. "Remove sort 1" leaves "Custom order", and the move
   handles appear.
7. **Reorder.** On `/processes?sort=custom` (a legacy link), dragging row 1
   onto row 3 with the pointer moved it. Then, from the keyboard: focus
   "Move <name>", press Enter, focus "Move here, to the end of the list",
   press Enter. That moved the row to the end, and the order survived a
   reload.
8. **Legacy links.** `/schedules`, `/templates` and `/suites` land on
   `/processes?kind=schedule|template|suite`. `/suites/suite-1` lands on
   `/processes/suites/suite-1` with "Edit suite" open.
9. **Derived runs.** `release-qualification` takes its next run from
   sched-1 (11/9/2026, 12:00 AM), and "Declared Dependency Bump" takes its
   from the declared schedule (4:30 AM). Unscheduled templates and suites
   read "on demand".

### Repository declarations

10. **Declared schedule** (`/processes/schedules/repo%3Aschedule%3Aacme%3Awidgets%3Adecl-bump-sched`).
    The pane names `.grain/schedules/decl-bump-sched.yml`, and Save, Pause
    and Delete are disabled. **Bug 1** is in its "What this schedule files"
    preview (see Bugs).
11. **Server refusals.** A PATCH of the declared schedule and a DELETE of
    the declared template or suite each return 400, naming the repository
    and the file ("… is declared by acme/widgets in
    .grain/schedules/decl-bump-sched.yml, so it is not this deployment's to
    edit …").
12. **Declared template.** It is locked: Save and Delete are disabled. Its
    Model box reads "retired-model-2024 (no connection advertises it)".
    Because it has an input field, "New task from this template" opens
    Use… ("File a task from a template"), which asks for "Package
    ecosystem". It does not open New task.
13. **Declared interactive template.** Use… shows what it files and the
    interactive template's note.
14. **Declared suite.** It is locked, names `.grain/suites/decl-sweep.yml`,
    and its preview note is part of Bug 1.
15. **Workflow schedule.** The row is "Declared by acme/widgets", and its
    pane names `nightly-ci.yml`. **Bug 2** is a `🔒 System` chip on this
    row. Run now asks: 'Run "Review last night's "nightly-ci" workflow run"
    now? It starts a CI run of the workflow it was declared in straight
    away, to be reviewed when it finishes; the schedule's own next run is
    unchanged.' Accepting started `fake-ci-1`. Once that run was finished
    green, the row reads "Last run fake-ci-1 on main: green …, no review
    needed".
16. **Trigger.** The chip's title is "Declared in acme/widgets,
    .grain/triggers/docs-guard.yml". Clicking the row opens
    `/repos/acme/widgets/settings/advanced`, which lists the file.

### Templates

17. **Priority validation.** `+ New template`, with Task priority 0, -2 or
    2.5: each Save is refused in the pane, with "a whole number, 1 or
    more". 8 is accepted.
18. **Fully populated template.** Keep running on. Interactive was switched
    on to read its hint, then off again. Legacy capability "Example legacy
    capability" was added. The repo was bound to acme/widgets. In "Add
    grant…", the groups are Grain API presets, the per-area "Grain API
    scopes: …" groups, and Capability server methods.
    - `cloud-viewer` is listed disabled: "the capability policy denies
      …#cloud-viewer to this task on acme/widgets (rule 1: deny …)".
    - `cloud-key` carries `data-policy-verdict="denied"`, since the policy
      admits it on acme/gadgets only.
    - `api:tasks:read` was added from the Grain API group.
    - The Model picker offers only "Deployment default (gemini-2.5-pro)",
      `gemini-2.5-pro` and `gemini-2.5-flash`, not the unserved model.
19. **Saved and reopened** after a reload: priority 8, Keep running on,
    Interactive off, model `gemini-2.5-pro`. The server holds
    `capabilities: [api:tasks:read, example-legacy]`.
20. **Phone footer.** At 390×844 the template pane's footer has **Bug 3**.
21. **Edit.** The title was changed to "Audit widget boundaries v2", and
    the row still showed it after a reload.
22. **New task from this template.** It opens New task with the Template
    box on `walk-full-template` and Priority `8`.
23. **Built-in template.** "Set up a GKE sandbox cluster" opens with Save
    disabled.
24. **Dependency-aware delete.** Once a schedule and a suite used the
    template, Delete asked 'Delete the template "walk-full-template"?
    Schedules and suites using it must be repointed or deleted first.' The
    server refused it with 400 "template is used by 1 schedule(s) ("Audit
    widget boundaries v2") and 1 suite(s) ("Walk Composed Suite"); repoint
    …", shown in an alert, and the template stayed. After the
    schedule and the suite were deleted, the template's own Delete removed
    it (step 41).

### Suites

25. **New suite.** `+ New suite` "Walk Composed Suite". "+ New template"
    inside it created `walk-inline-tmpl` in a nested pane, which closed and
    left the suite pane open with the new template picked. Then:
    `walk-full-template` added, Run mode "Run a fixed number of times" with
    count 2, "Require approval" on, Auto-merge off. The "What each pass
    files" preview shows Task priority 8 and the approval. At 390px the
    preview's two grant chips stay inside it, and the pane does not scroll
    sideways (`scrollWidth 389 = clientWidth 389`).
26. **Edit.** Approval was turned off and saved. After a reload: approval
    off, auto-merge off, count 2.
27. **Throwaway suite.** "Throwaway Suite" was created in until-clean mode
    with max passes 3. Delete → 'Delete the suite "Throwaway Suite"? Runs
    already started from it are unaffected.' Dismissing it kept the row;
    accepting it removed the row without a reload.

### Schedules

28. **Raw task schedule.** Every N minutes = 5 is refused: "At least 10;
    counted from the last firing" is shown, the native validity reads
    "Value must be greater than or equal to 10", and the pane stays open.
    It was saved at 15, with "Start it anyway", Keep running, Network
    "Egress (outbound internet only)" and "Leave for acknowledgement". The
    preview shows keep running. The row reads "every 15m · Can overlap",
    and the server holds `keepRunning: true, overlap: allow, networkAccess:
    open, nothingNeededAction: ack, everyNMinutes: 15`.
29. **Template schedule.** Weekly, Wednesday, America/New_York, from
    `walk-full-template`. The preview shows its priority. At 390px the
    preview's grant chips stay inside it, and the pane does not overflow.
    On save, the template row's next run went from "on demand" to
    11/11/2026, 2:00 PM without a reload.
30. **Edit, pause, reload.** The schedule was edited to Monthly on day 31,
    and the row now reads "monthly on day 31 at 09:00 America/New_York".
    Pause made the row read "Held -- fires 11/30/2026, 2:00:00 PM if
    resumed by then · Paused". The template's next run went back to "on
    demand" without a reload. Both survived a reload.
31. **Resume and Run now.** After Resume, Run now asks 'Run "Audit widget
    boundaries v2" now? It starts a task from the template
    "walk-full-template" straight away; …'. Dismissing it filed nothing
    (the template's last run stayed —). Accepting it filed a task, and the
    template and schedule rows both took that last run without a reload. A
    second Run now under "Skip this run" was refused with 409 "its previous
    run has not finished yet, and this schedule is set to skip …", shown in
    the pane.
32. **Suite schedule.** Fires "A suite", Walk Composed Suite, base main,
    daily at 06:00. The preview reads "2 passes of these tasks". The suite
    row's next run went from "on demand" to 11/9/2026, 6:00 AM without a
    reload. Run now asked 'Run "Walk Composed Suite" now? It starts a run
    of the suite …' and started a scheduled run.
33. **Validation.** An empty `+ New schedule` cannot be added: the title
    field reports "Please fill out this field" and the pane stays open.

### Suite runs

34. **From the suite row.** "Run…" on the Walk Composed Suite row, base
    main → **Run**. This is run A.
35. **Passes.** A proposed follow-up was seeded under run A's pass-1 task
    (stub). Passes 1 and 2 were then completed (stub), and the reconcile
    loop finished run A as **Succeeded**.
36. **From the generic action.** Generic `+ Run` under Suite runs, "Declared
    Release Sweep", base `release/1.0`. This is run B. Its only task was
    closed from its own task page (`/tasks/<id>`, Close), and the run went
    **Failed** with "pass 1 had a task that failed, or closed without
    completing".
37. **Base-less run.** Run A was made base-less (stub). Its Suite runs row
    reads "Walk Composed Suite · Succeeded · acme/widgets · default branch ·
    pass 2". Run B's row reads "Declared Release Sweep · Failed ·
    acme/widgets · release/1.0 · pass 1 · …".
38. **Direct URL.** `/processes/suites/runs/<A>` groups its tasks under
    "Pass 1" and "Pass 2", lists "Proposed follow-up from suite pass 1",
    and shows "default branch". Clicking the first task link went to
    `/tasks/<id>`, the link's own `href`.
39. **Keyboard.** On `/processes?kind=template`, Tab reaches a row link
    ("release-qualification"), Enter opens its pane, and Escape closes it.

### Cleanup

40. **Throwaway schedules.** All three were deleted from their panes, and
    each dialog named the schedule: 'Delete the schedule "…"? Tasks it
    already filed are not affected.', or '… Suite runs it already started
    are not affected.' for the suite schedule. After a reload, `GET
    /api/schedules` lists only the demo's and the repository's own.
41. **Throwaway suite and templates.** The suite was deleted, then
    `walk-full-template` and `walk-inline-tmpl`, each after its
    confirmation. The demo's own items and the repository declarations were
    left as they were.

### Console, network and dialogs

- **Page errors:** none.
- **Console errors:** three "Failed to load resource" lines. Two are the
  deliberate refusals above: the dependency 400 and the overlap 409. The
  third is a 401 from the app's first request on `/`, made before the access
  code had been entered.
- **API errors:** the only `/api/` responses of 400 or more are those two
  refusals.
- **Failed requests:** Chromium reported `net::ERR_ABORTED` on `GET
  /api/tasks` reloads cut short by navigation, and on each DELETE. Each of
  those DELETEs was confirmed applied by reading the list back after a
  reload. The server answers them `204 No Content`, so this is how
  Chromium reports those responses, not a product failure.
- **Dialogs:** 14 confirmations, each naming its object. The dismissed
  ones (a suite delete and a Run now) left nothing changed.

## Bugs

### 1. A declared schedule's preview dropped its own capabilities and called the file an edit

`docs/declarations.md` lets a `.grain/schedules/*.yml` carry
`capabilities:` beside `template:`, and its firings get both. The stored
composition already said so: `GET /api/schedules/<id>/composition` gives
`[api:tasks-read, api:ci-read@acme/widgets]` from `[template, schedule]`.
The pane, though, draws "What this schedule files" from `POST
/api/schedules/composition`. `ScheduleOverlay`'s `previewRequest()` never
sent the schedule's capabilities with a template, and
`DraftScheduleComposition` ignored them whenever a template was named.

- **Before (`49587a69`):** the preview of `decl-bump-sched` listed the
  template's `api:tasks-read` alone, under "as edited above, not as saved"
  -- on a pane where nothing can be edited. The declared suite's preview
  had the same note.
- **After:** the draft adds the schedule's own grants beside the
  template's, exactly as `model.ComposeScheduleTask` does: legacy
  admission and hosting capabilities are folded the same way, and the
  sources read `[template, schedule]`. The preview shows
  `api:ci-read@acme/widgets`. Locked declared schedules and suites now
  read "declared in .grain/schedules/decl-bump-sched.yml" or "declared in
  .grain/suites/decl-sweep.yml".

A form can't produce the pair, because `CreateSchedule` refuses
`capabilities` beside `templateId`. So the pane sends them only for the
schedule's own saved template.

- **Regression:** `TestDraftScheduleCompositionKeepsAScheduleCapabilitiesBesideItsTemplates`
  (`pkg/ui/composition_test.go`). Before the fix it fails with "draft
  sources = [], want [template schedule]". `ProcessesList.test.jsx` "previews
  a declared schedule with its own capabilities, as declared" asserts the
  POST body and the note.

### 2. A declared workflow schedule's row claimed to be one of grain's own

A workflow schedule fires `system:workflow-review:<name>`, so
`ui.Schedule.System` is true on the wire (`ScheduleOverlay` relies on it).
`buildProcessRows` copied it onto the row, and `ProcessesList` then drew
`🔒 System` with the tooltip "One of grain's own standing schedules: it can
be paused, not edited or deleted." That was on a row the repository
declares and that nobody can pause from here: Pause is disabled and refused
by the server.

- **Before:** the row's text reads "…Declared by acme/widgets🔒 Systemdaily
  at 03:15…".
- **After:** `system` is set only when the schedule is neither a workflow
  schedule nor declared. The row carries only its Declared chip.
- **Regression:** `processes.test.js` "marks only grain's own standing
  schedules as system, not a workflow schedule's system template", and
  `ProcessesList.test.jsx` "does not mark a declared workflow schedule as
  one of grain's own". Both fail before the fix.

### 3. The template pane's footer collided at phone width

At 390×844 the footer did not wrap and had no gap. "New task from this
template" broke onto two lines, and stretched Cancel and Save with it.

- **Before:**
  - Delete is at 17–81 px, height 37.
  - "New task from this template" is at 81–230 px, height 61. That is 0 px
    from Delete.
  - Cancel and Save are both 61 px tall.
- **After:** the footer wraps with a gap, the button group keeps to the
  right, and "New task from this template" stays on one line.
  - Delete keeps its own line (17–81 px, 37 px).
  - "New task from this template", Cancel and Save share the next line:
    44–230, 238–302 and 310–374 px, each 37 px tall.
  - `scrollWidth 389 = clientWidth 389`.
  - Suite panes got the same footer wrap, so a long Delete/Cancel/Save row
    cannot collide there either.
- **Regression:** `ui/e2e/processes-phone-layout.spec.js` "A template
  pane's footer on a phone" asserts every button is under 44 px tall,
  inside the pane, and at least 4 px from any button on its line. Against
  the base `TemplateOverlay` it fails with "New task from this template is
  61px tall".

## Tests run

- `go test ./pkg/ui/... ./tests/hygiene/...` passed (hygiene includes
  `TestCoreSaysNoGoogleTokens` and gofmt).
- `npx vitest run` (the whole UI suite) passed.
- `npx playwright test --project=chromium` passed 40/40 for
  `processes-phone-layout`, `recurring-composed-walkthrough`,
  `processes-narrowing`, `processes-keep-running`, `template-priority` and
  `built-in-template`.
- `npx prettier --check` on the touched UI files.
