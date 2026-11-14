# Processes — schedules, templates, suites, suite runs and triggers under `/next/` — 2026-11-13 (task 5387)

- **Date:** 2026-11-13 (UTC), 14:03–14:40.
- **Task:** task 5387, branch `grain/098073e4/task-5387-walk-through-processes`, base `main`.
- **Commit tested:** `e69d34223` (base). The fixes below were then checked on a rebuild of this branch over a fresh store, reseeded the same way.
- **Name:** an earlier walkthrough today (task 4729) already holds `2026-11-13-recurring-composed-ui.md`. That run walked the classic UI at `/` in depth; this one walks the Angular UI at `/next/` first, as "Two UIs" in `PROMPT.md` asks.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go 1.26, Node.js v20.19.0.
  - Playwright headless Chromium from `ui-ng/node_modules` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), timezone UTC, driven by throwaway Node scripts outside the checkout (not committed). Viewports `1440×900` light and `390×844` dark.
  - Built with `cd ui && npm ci && npm run build`, `cd ui-ng && npm ci && npm run build`, then `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms`, with `TMPDIR` set to a throwaway directory (embedded SQLite store).
  - There was no `/home/debian/.gemini-api-key`, so no model was called and `LOOK=1` was unavailable. I shot the pages with `ui/e2e/screenshot.mjs` (not committed), but my file reader shows PNGs only as bytes. Layout was judged from bounding boxes, `scrollWidth` vs `clientWidth` and the rendered text, not by eye.
- **How it was driven:** each script signs in on the access-code page (name and code typed, Continue clicked). After that it acts only through visible controls: clicks, typing, `Escape`, Back/Forward and reload. Every page recorded page errors, console errors and warnings, failed requests, `/api/` responses ≥ 400 with their bodies, native dialogs, and every non-GET `/api/` request with its body. The REST API (bearer access code) was used only for the setup below and to read back what a gesture persisted.
- **Console baseline:** each sign-in logs one `401` for the unauthenticated first load. `GET /api/tasks` shows as `net::ERR_ABORTED` when a navigation cancels the long read. Every other console error was the browser's line for a 400 listed below.

## Stubs and setup

The server, store, built frontends, browser and gestures were real throughout. What did not go through the UI:

1. **Demo seed flags.** `-schedules -github-sim -fake-ci -scheduled-workflows` provide the seeded schedules, templates and suite, a local bare `acme/widgets.git` (githubsim) and the fake CI runner. No agents run: the demo's execution boundary files tasks that never start.
2. **Repository declarations.** I cloned the githubsim bare repo and pushed to `main`:
   - `.grain/templates/decl-bump.yml`: `priority: 7`, `admission: durable`, `interactive: true`, `keep-running: true`, and `model: claude-opus-4-retired`, which no connection serves;
   - `.grain/templates/decl-docs.yml`, and `.grain/suites/decl-sweep.yml` (until-clean, max 3);
   - `.grain/schedules/decl-bump-sched.yml` (daily 03:00);
   - `.grain/workflows/nightly-ci.yml` (cron) and `.grain/schedules/nightly-ci-candidate.yml` (`workflow:`, `review: always`);
   - `.grain/triggers/docs-guard.yml`.
3. **Declarations switch.** `/next/` has no control for "Read acme/widgets's own declarations" yet, so I ticked it on the classic repo page's Advanced tab (`/repos/acme/widgets/settings/advanced`). On the rebuilt server it was flipped with `PUT /api/repos/acme/widgets/declarations`.
4. **Off-target `git_ref` schedule.** One schedule watching `offlist/upstream refs/heads/main`, made with `POST /api/schedules` on the first server. On the rebuilt server I also made one through the `/next/` dialog, which accepts it (see Schedules).
5. **Suite runs.** Started through the `/next/` Run suite dialog. One extra run on each server, for the deep-link checks, came from `POST /api/suite-runs`.

## Which UI each step ran against

| Area | UI |
| --- | --- |
| Processes list, tabs, filters, legacy links | `/next/` |
| Schedules: create, edit, pause, resume, run now, delete | `/next/` |
| Templates: create, launch, edit, delete | `/next/` |
| Suites: create, run, runs list, run pane | `/next/` |
| Declared rows (templates, suite, schedules, workflow schedules) | `/next/` |
| Turning declarations on for a repo | classic only (`/repos/<repo>/settings/advanced`) |

Some parts of the task have no `/next/` control yet, so they could not be walked there:

- the kind chips, search, sort and custom order (`?kind=`, `?q=`, `?sort=`). `/next/` uses four tabs instead: Schedules & Triggers, Templates, Suites & Runs, and Repo Workflows & Declarations;
- the header's New menu;
- in the schedule and template dialogs: grants and `GrantsPicker`, Admission, Hosting, Keep running, network, custom fields, reads/writes, the agent and model picker, and the template's Task priority;
- a trigger row of its own (a declared trigger is listed only as a file on the Declarations tab);
- "New task from this template" and the suite dialog's inline "+ New template".

The classic UI's versions of these were walked in depth earlier today (`2026-11-13-recurring-composed-ui.md`). I did not repeat that walk.

## Walkthrough (`/next/`)

### Processes list

- **Rail.** Processes opens `/next/processes` and lands on Schedules & Triggers. The rail item has no count.
- **Tabs.** On the seeded store the tab counts read `Schedules & Triggers 2 · Templates 3 · Suites & Runs 1 · Repo Workflows & Declarations 1`. Once declarations were read they rose to 7 · 5 · 2 · 1 without a reload.
- **Legacy links.** `/next/schedules`, `/next/templates` and `/next/suites` land on their tabs. Before the fix, `/next/suites/suite-1` and `/next/processes/suites/runs/1` fell through to the inbox (bug 1).
- **`?kind=template&q=bump`.** The query is ignored and the Schedules tab opens, because `/next/` has no kind or search narrowing (see above).
- **Filters.** On Schedules: All, Active, Paused, Event triggers and Suppressed. On Templates: All, Deployment, Repo-declared and Built-in. Each narrows the list, and Suppressed shows the off-target schedule alone.

### Schedules

- **Every N minutes.** Saving `5` sent `everyNMinutes: 5` and got back `400 recurrence: everyNMinutes must be at least 10`. The dialog stayed open and showed nothing, because the error banner was behind the dialog's scrim in the inert page (bug 2). The box's own `min` was `5` (bug 3). After the fix the dialog says "recurrence: everyNMinutes must be at least 10". `20` with Allow overlap then saved as `Every 20 min · overlap: allow`.
- **Edit.** I changed the title and switched the cadence to Daily. That sent a PATCH with the revision, the row updated in place, and both survived a reload.
- **Run now.** This sent `POST /api/schedules/sched-4/run`. The pane then showed `Last:` beside `Next:` with no reload.
- **Delete.** The confirmation reads "Delete schedule? Remove schedule "walk: lifecycle edited" permanently?". Escape kept the row. Confirming removed it.
- **Suite-running schedule.** With Firing target set to Suite (the declared suite), repo `acme/widgets`, base `main` and Weekly, the preview showed the composed tasks, including the declared template's `model claude-opus-4-retired`, `admission durable`, `keepRunning on` and the "Nobody has accepted that set yet" note. It saved as `Declared sweep · Weekly · Monday 09:00`.
- **Template-target schedule.** Firing target Template opens a template picker ("Declared docs pass ▾") in place of the title and prompt fields.
- **`git_ref`.** Watching `offlist/upstream` at `refs/tags/v*` was refused by the server ("git_ref trigger needs a full `ref:`…"), shown in the dialog after the fix. `refs/tags/latest` on `offlist/upstream` was accepted. That is by design: `validateGitRefRecurrence` refuses only forbidden repositories (the secrets backup, retired config aliases), and an off-target watch is accepted but suppressed.
- **Suppressed schedule.**
  - The row carries a `Suppressed` chip.
  - The pane says "Suppressed (watchedRepoOffTarget): watches offlist/upstream, which is not on this deployment's target repo list".
  - Before the fix, the pane also said `Next: 2027-11-14T14:14:23Z`, the far-future placeholder kept for a schedule that files nothing (bug 4). It now reads `Next: suppressed`.
  - Pause sent `{"enabled":false}`, and the row turned `Paused`. Resume put it back to `Suppressed`.
- **Times.** `Next:`/`Last:` are shown as raw RFC 3339 strings (`2026-11-14T09:00:00Z`) rather than local, readable times. This is cosmetic and not changed here.

### Templates

- **Create.** I created `walk-tmpl` with a `pkg` parameter, bound to `acme/widgets`. It was selected with its parameter schema.
  - Every time it was selected, the page asked for `GET /api/templates/template-3/composition`, and the server answered `400 field "pkg" is required` (bug 5).
- **Launch from template.** The dialog asked for `pkg (required)` and offered "Approved". Create task sent `POST /api/tasks {"templateId":"template-3","fields":{"pkg":"left-pad"},…}` and went to `/next/task/21`.
- **Edit.** Renaming to `walk-tmpl-renamed` sent a PATCH with the revision. The list updated, and the new name survived a reload.
- **Delete.** The confirmation reads "Delete template? Delete template "walk-tmpl-renamed"?". Confirming removed it.
- **Declared template.** It shows a `Repo-declared` chip and "Repo-declared read-only template from acme/widgets (.grain/templates/decl-bump.yml)". Only "Launch task" is offered, with no Edit or Delete. The composition preview shows `priority 7`, `admission durable`, `keepRunning on` and `model claude-opus-4-retired`.

### Suites and suite runs

- **New suite.** The dialog preselects the first schedulable template. Clicking the template you meant to add therefore *unselects* it when it happens to be first, and Save suite stays disabled with no hint why. I hit this twice in the walk. I recorded it below rather than changing it.
- **Pass count 0.** This keeps Save suite disabled.
- **Run suite.** Run suite from the suite's pane and from the header both open the same dialog (suite picker, Repository, Base branch). Start suite run sent `POST /api/suite-runs` and selected the new run.
  - Its pane reads "Active · Declared sweep · Run #1 … Until clean (max 3 passes) · currently on pass 1 · Pass 1 (2 tasks) #22 · Bump dependencies proposed, #23 · Refresh docs proposed". The declared template asks for `admission: durable`, nobody has accepted it, so its tasks were filed as proposals.
  - Suite runs (2) lists both runs.
- **Base branch.** Start suite run stays disabled while Base branch is empty, and the server refuses a base-less manual run ("base is required: a suite run has no default branch to fall back to"). So no base-less run could be made from the UI. Where one exists, the row and pane read "default", and the suite pane's run list read `acme/widgets @  (pass 1)` (bug 6).
- **Task links.** I did not click the run pane's task links (`#22`, `#23`) in this walk. Launch from template did land on task detail (`/next/task/21`).

### Declared schedules, workflow schedules and triggers

- **Declared schedules.** These show a `.grain` chip and "Declared in acme/widgets at .grain/schedules/<file>. Update that file in git to modify this schedule.". They offer Run now and Pause, with no Edit or Delete.
- **Workflow schedules.** The two workflow schedules (`nightly-ci` from its cron, and `Nightly CI candidate`) carry no `System` chip. Pause is disabled on them.
  - Run now sends `POST /api/schedules/repo%3Aschedule%3Aacme%3Awidgets%3Aworkflow.nightly-ci/run` straight away, with no confirmation, which is consistent with "starts the run straight away".
- **Declarations tab.** It lists every declared file with its kind, including `.grain/triggers/docs-guard.yml trigger`. A trigger has no row or pane of its own under `/next/`.
- **A failed read.** My first trigger file had no `template:`. The tab showed "Sync / validation error: reading acme/widgets's declarations: .grain/triggers/docs-guard.yml: a trigger needs a `template:` to file", which is clear.
  - After I pushed the fix, nothing re-read the repository for over a minute, until the declarations switch was flipped again. A failed read seems to stay failed until something else prompts a re-read. I recorded this below; I did not chase it.

### Phone width and dark theme

- At `390×844` dark, `/next/processes/suites/runs/1` opens the run's pane directly (after the fix), with a Back button. `scrollWidth` equals `clientWidth` (390/390).
- The new dialog error stays inside the dialog at phone width: the bounding boxes are asserted in the e2e spec.

## Bugs found and fixed

All six are in `ui-ng/src/app/processes/processes-page.component.ts` and `ui-ng/src/app/app.routes.ts`. The classic UI does not have them (its pages already handle these cases), so `ui/` is unchanged.

1. **Deep links fell through to the inbox.** Repro: open `/next/suites/suite-1` or `/next/processes/suites/runs/1`, and you land on `/next/inbox`.
   - Fix: new routes `processes/:tab/:id` and `processes/suites/runs/:runId`, plus `schedules/:id`, `templates/:id` and `suites/:id` redirects. The page selects the item they name (on a phone, it opens the item's pane), and the top-row auto-select stands aside.
   - Back and Forward between two deep links restore each selection.
2. **A refused save gave no reason.** Repro: New schedule, Every N min `5`, Save. You get a 400, the dialog stays open, and nothing in it says why.
   - Fix: every Processes dialog shows the action's error itself (`proc-dialog-error`, `role="alert"`), and the page banner waits until the dialog closes.
   - A dialog now opens with no error left over from an earlier action.
3. **Every N minutes allowed 5.** The box's `min` is now `10`, the server's floor.
4. **A suppressed schedule named a next run.** Its pane now says `Next: suppressed`.
5. **A template with a required, default-less field fetched a composition it could not get.** Selecting it no longer asks for one, so there is no 400 on every selection.
6. **A base-less suite run's label.** Its row, pane and the suite's run list now say "default branch", as the classic pane does.

**Regression coverage.**

- `ui-ng/e2e/next-processes-walkthrough.spec.js` covers deep links to a suite, a schedule and a suite run started through the UI; the in-dialog error and its `min="10"` (desktop and `@phone`, contained within the dialog); and `Next: suppressed`. All three tests fail on the base build and pass on the fix.
- A unit test in `processes-page.component.spec.ts` covers the template deep link and the skipped composition.
- `npm run lint` passes, `vitest` passes on `src/app/processes` (7 tests) and `app.component.spec.ts`, and `next-full-system.spec.js` passes (8 tests, desktop/phone, light/dark, contrast).

## Observations, not fixed here

- **New suite preselects a template.** Clicking the first template unselects it, and Save suite stays disabled with no hint why. Starting empty, or saying why Save is disabled, would be clearer. This is a design decision, so I left it.
- **Pass count 0 gives no hint.** Save suite is disabled with no message.
- **Raw timestamps.** `Next:`/`Last:` show RFC 3339 strings.
- **A failed declarations read is not retried when the branch moves.** It stayed failed until the declarations switch was flipped again.
- **`/next/` parity gaps.** See "Which UI each step ran against". A follow-up task was proposed for these.
- **The `/next/` dialogs were not reviewed by three independent reviewers.** The design book's review mandate asks for that, but this run had no separate reviewer agents. The change is small (routing, an error line inside existing dialogs, and labels) and follows the existing `banner banner-error` style.
