# Processes — schedules, templates, suites, suite runs and triggers walkthrough — 2026-11-11 (task 4301)

- **Date:** 2026-11-11 (UTC), 08:35–09:35.
- **Task:** task 4301, branch `grain/098073e4/task-4301-walk-through-processes`, base `main`.
- **Commit tested:** `ec371e80b27d` (`v0.0.0-20261111073721-ec371e80b27d`, as the app's footer reported it). The two fixes below were then checked in the same browser against the same live server: the Go fix by its unit test, the UI fix through a Vite build of this branch proxying to that server (see "Stubs and setup", item 7).
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`, 7 CPUs), Go `1.26.10`, Node.js `v20.19.0`.
  - Playwright headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), driven by scratch Node scripts (`@playwright/test`'s `chromium`), not committed. Viewports `1440×900`, `1024×900`, `620×900` and `390×844`, light and dark.
  - Frontend built with `cd ui && npm ci && npm run build`. Server built with `go build -tags grain_e2e -o /tmp/gw/grain ./cmd/grain` and started as `grain demo -addr 127.0.0.1:0 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` with `TMPDIR` pointing at a throwaway directory (embedded SQLite store).
  - There was no `/home/debian/.gemini-api-key` in this sandbox, so no model calls were made and `LOOK=1` was unavailable. Screenshots were taken with `ui/e2e/screenshot.mjs` but not committed. Layout was judged from bounding boxes and computed styles rather than by eye.

## Stubs and setup

Every create, edit, run, pause, delete, filter, sort, reorder and navigation below was a click or keystroke in Chromium against the real `grain demo` server and its store. The only things not done through the UI:

1. **Demo seed flags.** `-schedules -github-sim -fake-ci -scheduled-workflows` supply the seeded schedules, templates and suites, a local bare `acme/widgets.git` (githubsim) and the fake CI runner. No external GitHub calls are made and no agent VMs are started.
2. **Repository declarations.** I cloned the githubsim bare repo (`GET /api/e2e/scheduled-workflows` names it) and pushed seven `.grain/` files to `main` (and the same commit to `candidate`):
   - `.grain/workflows/nightly-ci.yml` and `.grain/schedules/nightly-ci-candidate.yml` (`review: always`);
   - `.grain/templates/decl-bump.yml`: `priority: 7`, `admission: durable`, `hosting: console`, `keep-running`, an `ecosystem` input, `push: refs/tags/v*`, and `model: claude-opus-4-retired`, which no connection serves;
   - `.grain/templates/decl-pair.yml` and `.grain/suites/decl-sweep.yml` (until-clean, max 3);
   - two schedules sharing the overlap group `nightly-group`: `decl-bump-sched.yml` (daily) and `decl-safety.yml` (every 6h);
   - `.grain/triggers/docs-guard.yml`.

   I then ticked "Read acme/widgets's own declarations" on the repo's Advanced tab in the browser. It read back `Read at 033721dd4ba5: 2 template(s), 1 suite(s), 4 schedule(s), 1 trigger(s)`.
3. **Model connection.** I added a Claude connection `walk-claude` advertising `claude-opus-5-5`, `claude-sonnet-5-5` and `claude-haiku-4-5` through Settings → Models → New connection (UI), next to the demo's own `gemini 2.5 pro` connection. Later I disabled and re-enabled the gemini connection from the same page.
4. **Capability policy.** One rule, `PUT /api/capability-policy`: deny `grain#source` for tasks on `acme/widgets`.
5. **Off-target `git_ref` schedule.** One schedule watching `offlist/upstream` (`POST /api/schedules`). The UI correctly offers no way to create one, but the task asks how an existing one looks.
6. **Execution boundary.** The demo runs no agents, so:
   - `ui/e2e/releasehelper complete-suite-pass` / `sync-suites` (built with `-tags grain_e2e`, run against the throwaway store) finished suite run passes;
   - `POST /api/e2e/fake-ci/runs/fake-ci-1/finish` (`conclusion: failure`) finished the CI run that a workflow schedule's Run now started;
   - two SQLite writes on the throwaway store: an `INSERT INTO task_link` (`proposed-by`) making task 30 a follow-up of pass 1's task 26, and `UPDATE suite_run SET base = ''` on run 2. The run form always sends a branch, so a base-less run is only reachable through the API or a legacy row.
7. **Viewer role.** The `access` block of `/api/config` was rewritten in the browser (`page.route`) to `grain/viewer` scopes, with and without `templates:write`, as `ui/e2e/role-gated-actions.spec.js` does. Access-code sign-in is always full access, and this sandbox has no identity provider that could carry a real role.
8. **Fixed-UI check.** To check the UI fix without restarting the server (and losing the run states above), I ran `npx vite` from this branch with a scratch config pointing its `/api` proxy at the demo's port. The app and its API traffic were otherwise unchanged.

## Walkthrough and outcomes

### 1. Processes list, kind chips, search, sort, reorder, URLs

- **Rail count.** The rail's `Processes` count read `13` on `/` and `14` once `/processes` had loaded its triggers. That lazy trigger read is intentional (`Sidebar.jsx`) and was noted by earlier runs. The header count agreed.
- **Kind chips.** `All 14 · Schedules 4 · Workflow schedules 2 · Templates 5 · Suites 2 · Triggers 1`, with `aria-pressed` tracking. Clicking chips adds kinds (`?kind=schedule,workflow,…`), and selecting all of them collapses back to `All` (`/processes`). The `Suite runs` section shows only on `All` and `Suites`.
- **Derived Last run / Next run.** Templates and suites show times taken from the schedules that fire them:
  - `release-qualification` showed `11/12/2026, 12:00 AM`, from its daily schedule;
  - `WT Declared docs pass` showed `2:40:52 PM`, from the 6-hourly declared schedule;
  - `security-audit`, both suites and the system template showed `on demand`;
  - the trigger showed `on change`.

  Once I had created a daily schedule on `walk-full-template` and a weekly one on `walk-suite`, their rows picked up `11/12 9:00 AM` and `11/16 9:00 AM` without a reload. After those schedules were deleted, both rows went back to `on demand`, again without a reload.
- **Search.** `bump` narrowed the chips to `All 2 · Schedules 1 · Templates 1`, as `?q=bump`, and survived a reload.
- **History.** Narrowing replaces the current history entry rather than pushing one. `App.jsx` treats it as the same page, as list filters elsewhere are, so Back went to the previous page (`/`) and Forward came back to `/processes?kind=template&q=bump` with the chips and search restored.
- **Sort.** `?sort=name:asc`, `kind:asc`, `last:desc`, the default `next` and `custom` all reordered the rows, and the button's label followed.
- **Custom order.**
  - Keyboard: focusing `Move Declared Nightly Bump` and pressing Enter showed 13 `Move here, above …` slots. Choosing the fourth moved the row there.
  - Pointer: dragging the 5th handle onto row 1 moved that row to the top.
  - The order survived a reload.
- **Old links.**
  - `/schedules`, `/templates` and `/suites` landed on `/processes?kind=schedule|template|suite`.
  - `/schedules/sched-1`, `/templates/template-1` and `/suites/suite-1` landed on `/processes/<kind>/<id>` with the pane open.
  - `/suites/runs/999` landed on `/processes` with the alert `no suite run "999"`.

### 2. New menu

- The header's `+ New` offers `Schedule`, `Template` and `Suite`, plus the hint "Workflow schedules and triggers are declared in a repository's .grain/ directory".
- Narrowed to a single creatable kind, the button becomes `+ New schedule`, `+ New template` or `+ New suite` and opens that form directly.
- Narrowed to workflow schedules or triggers, it is the plain menu.

### 3. Schedules

- **Raw task-filing schedule** `walk-raw-schedule`, all set through the form:
  - every 10 minutes, `Start it anyway`;
  - `Bypass queue` (its help: "Admitted past the worker and merger limits…"), `Interactive console`, `Keep running`, network `none`;
  - model `claude-sonnet-5-5`, picked by typing `son` into the searchable picker, which narrowed the list to that one option.

  The picker listed exactly the five models the two enabled connections advertise. The composition preview listed Model, Keep running, Admission and Hosting as "from the schedule". The stored record matched.
- **Ten-minute floor.** The minutes box carries `min=10`. Submitting `5` is blocked by the browser's own validation and nothing is saved.
- **Edit, pause, resume, run now.**
  - Edit + Save renamed the row in place.
  - Run now confirmed with "It starts the task … straight away; the schedule's own next run is unchanged." The row's `Last run` updated.
  - Pause showed `Held -- fires 11/11/2026, 8:58:20 AM if resumed by then | paused`, which persisted through a reload. Resume restored `Enabled`.
  - The saved schedule's preview still named `claude-sonnet-5-5` and `interactive console`.
- **`git_ref` schedules.**
  - Watching `grain/config-retired` was refused with `recurrence.gitRef.repo: grain/config-retired not found: only grain/config exists`, shown as an alert while the form stayed open.
  - Watching `acme/widgets refs/heads/main` saved, with the row reading `on ref acme/widgets refs/heads/main … on ref move`.
  - The seeded off-target schedule (setup item 5) showed:
    - a `Suppressed` chip;
    - the detail line `suppressed: watches offlist/upstream, which is not on this deployment's target repo list`;
    - `suppressed` in Next run;
    - a pane note saying to add the repository to `targetRepos`.

    Pause worked. Once paused, the server stops reporting the suppression (it isn't polling anyway), and the row shows `on ref move | paused`.
- **Template- and suite-running schedules.**
  - The schedule on `walk-full-template` previewed the template's model, network, Keep running, `durable`, `interactive console` and `Task priority 1000`, all "from the template".
  - The schedule on `walk-suite` previewed the suite's `waits for approval` and `off` auto-merge.
- **Held firing and its task link.** I switched the 10-minute schedule to `Skip this run` while its earlier task was still open. At 09:08:20 the reconciler held the firing (`#23 from the previous firing is still open (queued)`), and:
  - the row showed `not firing: the task its last firing filed is still open, task 23`;
  - the pane explained the hold and linked `task 23`.

  With an unsaved Description edit, clicking that link asked exactly once per click: Cancel kept the edit and the pane, OK went to `/tasks/23`. **Bug 2** (below) was here: Back from the task did not return to the schedule.
- **Delete.** Every delete confirmed with what it does, for example "Delete the schedule "walk-suite"? Suite runs it already started are not affected." Cancel kept the pane. OK removed the row, and the template's and suite's derived Next run fell back to `on demand` without a reload.

### 4. Templates

- **Task priority.** The box refuses `1.5`, `0` and `-2` natively ("Please enter a valid value…", "Value must be greater than or equal to 1"). `1e3` is accepted and saved as `priority: 1000`.
- **`walk-full-template`** saved with:
  - reviewers `ana, bo`, `Interactive`, `Keep running`, `Durable`, `Interactive console`;
  - network `egress`, model `claude-haiku-4-5` (searched with `haiku`).

  **Bug 1** (below): the `Base branch: main` I typed was silently dropped, because the template has no repo.
- **Grant menu.**
  - `grain server › source` is marked "may be refused by the capability policy for agent tasks".
  - The three `release server ›` methods are disabled as "ci only: not a grant for agent tasks".
  - Grain API presets are offered as `menuitemcheckbox`es. I added `release-manager`, and its long chip stayed inside the pane at 390px.
- **New task from this template** opened New task on `walk-full-template` with `Task priority 1000` carried across. On `walk-input-template` (input `pkg`) it opened Use… instead, with the `pkg` field.
- **Use… on the interactive template** showed "This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed". The preview listed Keep running, Admission `durable`, Hosting `interactive console` and Task priority `1000`.
- **Use… on an unbound template with `Reads: acme/widgets`**, targeting `acme/widgets`: the preview read `Reads | none`, and Create task filed task 32 on `acme/widgets` with no reads, rather than failing.
- **Rename refresh.** Renaming the template from its pane (twice) updated its schedule's `template:` chip, the open suite pane's Templates chips and that suite's preview, all without a reload.
- **Dependency-aware delete.** Deleting the template while a schedule and a suite used it asked "…Schedules and suites using it must be repointed or deleted first.". On OK it was refused with `template is used by 1 schedule(s) ("walk-full-template task") and 1 suite(s) ("walk-suite"); repoint or delete the schedules and remove it from the suites first`, and the pane stayed open.
- **Phone footer.** At 390px the template footer wraps onto two rows (`Delete`, `New task from this template` / `Cancel`, `Save`). Every button is 37px tall (one line), none overlap, and nothing overflows, in light and dark.

### 5. Suites

- **Creating `walk-suite`.**
  - The searchable Templates picker narrowed to `walk-full-template` on `full`.
  - `+ New template` opened the template form over the suite. Saving `walk-inline-tmpl` returned to the suite with it selected.
  - Run mode `Run a fixed number of times`: the count box refused `0`, and I set `2`.
  - `Require approval` on, auto-merge off. The row read `run 2× | 2 templates | requires approval`.
- **Viewer role** (`suites:read` only, setup item 7):
  - `+ New suite`, the row's `Run…`, and the pane's `Delete` and `Save` were disabled.
  - `+ New template` stayed in the same place: disabled without `templates:write`, enabled with it.
- **Delete.** Confirm with Cancel, then OK, removed the suite.

### 6. Suite runs

- **Starting runs.**
  - Run 1 was started from `walk-suite`'s row `Run…`, on branch `main`. Its preview listed each pass's tasks with priority, admission and hosting, and said which tasks take the run's branch.
  - Run 2 was started from the generic `+ Run`, which preselects the first suite in the table's order. I switched it to `Release Qualification Suite` on `release-1`.
- **States.**
  - Run 1 was `active` with its tasks `unapproved`. After I closed both tasks from their task pages it became `failed`: "pass 1 had a task that failed, or closed without completing".
  - Run 2 reached `succeeded` after both passes were settled with the release helper.
- **Base-less run (run 2).** Showed `default branch` on its row and in its pane.
- **Pass grouping and follow-up.** Run 2's pane grouped tasks under `pass 1` (3) and `pass 2` (2), with seeded follow-up task 30 nested under task 26.
- **Deep links and navigation.**
  - `/processes/suites/runs/2` survived a reload.
  - Task links opened `/tasks/<id>`, and Back returned to the open run pane.
- **Status chips.** At 390px the `succeeded` and `failed` chips are whole (label `scrollWidth == clientWidth`).

### 7. Repository-declared rows

- **Notices.** Declared template, suite, schedule and workflow schedule panes each opened with a notice naming the repo and file. For example: "This schedule is declared by acme/widgets in .grain/schedules/decl-bump-sched.yml, so it cannot be edited or deleted here… Run now still fires it once."
- **Disabled controls.**
  - `Save`, `Delete`, `Pause`, `+ Add input`, input removals and `+ New template` were all disabled, and `Add grant…` was absent.
  - `New task from this template`, `Use…`, `Run now` and suite `Run…` stayed live.
- **No mutating requests.** No PATCH, PUT or DELETE was sent to a `repo:` id at any point.
- **System template.** The system template said it is built into grain. Workflow schedule rows carry `declared by acme/widgets`, with no `System` chip.
- **Workflow Run now.** It confirmed "It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged." Finishing `fake-ci-1` as a failure gave the row `Last run fake-ci-1 on candidate: failure …, reviewed in task 31`.
- **Trigger.** The `docs-guard.yml` row opened `/repos/acme/widgets/settings/advanced` with `.grain/triggers/docs-guard.yml` on it.
- **Unserved model.** The declared template's model reads `claude-opus-4-retired (no connection advertises it)` in its pane.
- **Default model on disabled connections only.** With the gemini connection disabled, a new schedule's default reads `Deployment default (gemini-2.5-pro, disabled connections only)`, and the two gemini models are flagged the same way in the list.

### 8. Cross-cutting

- **Keyboard.** Tab from search reaches Filter, Sort, `+ Run`, the kind chips and then the first row link. Enter opens the row's pane, and Escape closes it and returns focus to the same row link.
- **Overflow.** `/processes`, `?kind=suite`, a schedule pane, a suite run pane and a declared suite pane at 1440, 1024, 620 and 390, in light and dark: `scrollWidth == clientWidth` everywhere, 0 page errors, 0 console errors, 0 5xx.
- **Aborted requests.** Every DELETE shows as `net::ERR_ABORTED` in Chromium's `requestfailed` *after* its `204` response arrived. That is Chromium's report for a body-less response, not a cancelled mutation. The `GET /api/tasks` aborts are the refresh cancelling itself on navigation.
- **Cleanup.** Every `walk-*` schedule, template and suite was deleted through the UI, and `/processes` read `All 14` again after a reload.

## Bugs found

### 1. Fixed: a new template's Base branch was dropped without a word when it named no repo

- **Reproduction:**
  1. Open `+ New template`, leave Repos empty, and type `main` in Base branch.
  2. Save. The template is saved and `GET /api/templates` shows no `base`.
  3. Reopen the template, type `main` in Base branch again, and Save. The edit is refused: `base needs a repo to name a branch of: bind this template to a repo, or leave base empty`.
- **Cause:** `CreateTemplate` used `parseBinding`, which discards a base when there is no repo. `UpdateTemplate` checks the pair after the same call.
- **Fix:** `CreateTemplate` (`pkg/ui/templates.go`) refuses the pair with the same error, so the form keeps what was typed and says why.
- **Regression test:** `TestCreateTemplateRefusesABaseWithNoRepo` (`pkg/ui/templates_test.go`). `go test ./pkg/ui/` passes.

### 2. Fixed: Back from a schedule's suppressed task skipped the schedule

- **Reproduction:**
  1. Give an `overlap: skip` schedule a firing that comes due while its last task is open.
  2. Open the schedule's pane and click `task N` in its "not firing" note.
  3. Press Back, or the task page's `← back`. You land on `/processes` with the pane closed.

  A suite run's task links come back to the run pane. Measured with `history.length`: the schedule link pushed 2 entries, the suite run link 1.
- **Cause:** the link called `onClose()` and then `onOpenTask()`. `openTask` loads the task asynchronously, so the closed pane's `/processes` was pushed before `/tasks/N`.
- **Fix:** the link opens the task without closing the pane first, as `SuiteRunPane`'s task rows do (`ui/src/components/ScheduleOverlay.jsx`). The ask-once discard guard (`discarded` ref) is unchanged.
- **Verified in Chromium:**
  - one history entry;
  - Back and the in-app back both reopen `/processes/schedules/sched-3`, clean or dirty;
  - the unsaved-edit prompt still appears exactly once.
- **Regression test:** "comes back to the schedule from the suppressed task it links to" (`ui/src/App.schedules.test.jsx`). It fails without the fix and passes with it, and the existing ask-once test still passes.

## Observations, not fixed here

- **Cramped name column at tablet widths.** Between 620 and about 700px, the ≤900px grid (`1.25rem 6.5rem minmax(0,1fr) auto`) leaves the name column about 108px, narrower than the 104px kind column beside it. Chips such as `run until clean (max 3)` then ellipsize; that one has no title, so its pass limit can't be read at that width. Ellipsis inside a lone chip is the documented behaviour (`style.css`, `.task-row .chips`). Above 700px and on phones (where rows wrap) everything fits. Filed as a follow-up rather than redesigning the grid here.
- **Use… doesn't flag an unserved model.** The Use… preview for a template whose model no connection serves reads `Model | claude-opus-4-retired | from the template`, without the "(no connection advertises it)" that the template's own pane shows.
