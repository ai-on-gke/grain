# Recurring and Composed-Work Automation UI Walkthrough (2026-11-17)

- **Date:** 2026-11-17 (UTC), 23:00–23:45.
- **Task:** [Task #6591](/tasks/6591), branch `grain/53549e42/task-6591-walk-through-processes`, base `main`.
- **Commit tested:** `07e43408c` (footer `07e4340`) for the walkthrough up to the first bug. Everything after that ran on this branch's fix (`b0d397822`) over a fresh throwaway store.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`.
  - Built with `make frontend`, `npm ci --include=dev` in `ui-ng/` and `ui/` (needed for Playwright), and `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` over a throwaway `TMPDIR` (embedded SQLite store). Signed in through `POST /api/auth/access-code` as `alice@example.com` (`ui-shared/e2e/accesscode.js`).
  - Browser: headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), driven by throwaway Playwright scripts kept outside the repository. Timezone `UTC`, at `1440×900` and `390×844` (touch, mobile), in light and dark.
  - No `/home/debian/.gemini-api-key` was present, so no live model was called and `LOOK=1` was unavailable. Screenshots were taken (`ui/e2e/screenshot.mjs --bin`, for `/next/processes/schedules`, `/next/processes/templates` and `/next/processes/suites/runs/1`, plus the new discard prompt over an edited schedule, at desktop and phone, light and dark). The file reader here shows PNGs only as bytes, so layout was checked through the DOM: `elementFromPoint` on the prompt, bounding boxes against the viewport, `scrollWidth` on chips and `documentElement`.
- **Console and network:** zero `pageerror`s and zero `5xx` responses. Every `4xx` was an intentional negative step: `400` for the 5-minute schedule, the forbidden watched repo `grain/config-ops`, and PATCH/DELETE of a declared template.

## Which UI each step ran against

`ui-ng/src/app/shell/nav.ts` routes Processes to `/next/processes`. `ui-ng/src/app/app.routes.ts` carries `/next/processes[/:tab[/:itemId]]`, `/next/processes/suites/runs/:runId` and the `/next/schedules`, `/next/templates` and `/next/suites[/runs]/<id>` redirects. Every step below was walked on `/next/` first. The classic `/processes` was used only for controls `/next/` still lacks:

| Area | `/next/` | Classic only (no `/next/` control yet) |
| --- | --- | --- |
| Processes list | section tabs with counts, status chips (All/Active/Paused/Event triggers/Suppressed), sort, `?q=`, `?kind=`, legacy links | rail count badge (`processes 15`); the single table with `All 15 / schedules 4 / workflow schedules 2 / templates 5 / suites 3 / triggers 1` kind chips |
| New menu | Schedule / Template / Suite + `.grain/` hint | New opening the narrowed kind directly (`?kind=template` → `+ new template` opens the template form). `/next/`'s Templates and Suites tabs still open the menu (recorded, not changed: the existing walkthrough spec relies on the menu there) |
| Schedules | prompt/template/suite targets, every-N-minutes floor, `git_ref`, overlap, Admission, Hosting, Keep running, GrantsPicker (incl. the `grain/ci-read` API group), searchable model picker, preview, Run now, Pause/Resume, Edit, Delete | `Reads`/`writes`, network, custom fields |
| Templates | name/title/prompt/repo/params, Admission, Hosting, Keep running, GrantsPicker, model picker, Launch task, dependency-aware delete | `Task priority`, `Interactive` switch, `New task from this template`, `Use…` with its interactive note |
| Suites & runs | create/edit, run modes, pass limits, approval, auto-merge, delete, Run suite from the pane and the header, runs list, direct run URLs | searchable Templates combobox and inline `+ New template` (`/next/` uses toggle chips) |
| Declarations, workflow schedules, triggers | yes | — |

## Stubs and seeded states

Every create, edit, run, pause and delete gesture below went through the real browser UI and the real `grain demo` server, as did all rendering. Only these were stubbed or seeded:

1. **Agent execution:** the demo files tasks but runs no agents (`-fake-ci` for CI), so fired tasks stay `queued`.
2. **Repository declarations:** files pushed with `git` straight to the simulated `acme/widgets.git` (`GET /api/e2e/scheduled-workflows`): `.grain/templates/decl-bump.yml` (`admission: durable`, `keep-running: true`), `.grain/suites/decl-sweep.yml`, `.grain/schedules/decl-nightly.yml` (template, every 6 h, `hosting: console`), `.grain/schedules/decl-lint.yml` (workflow), `.grain/workflows/lint.yml` and `.grain/triggers/docs-guard.yml`. The first push was deliberately invalid: a schedule with `title:` and a `*/12` cron.
3. **Off-target `git_ref` schedule:** one was seeded with `POST /api/schedules`. A second was saved through the `/next/` dialog itself, which accepts an off-target watched repo by design (`validateGitRefRecurrence` refuses only forbidden repos).
4. **Unbound template with Reads:** `POST /api/templates {reads: ["acme/widgets"]}` (`template-5`), since neither UI edits Reads on a new template.
5. **Base-less suite run (regression spec only):** the server now always fills in a run's base, or refuses one with no default branch. The spec therefore blanks `base` in the server's real `/api/suite-runs` answer through `page.route`.

## Steps and outcomes

### 1. Processes, navigation and narrowing (`/next/`)

- Rail `Processes` → `/next/processes` by SPA navigation. Tabs read `Schedules & Triggers 2`, `Templates 3`, `Suites & Runs 1`, `Repo Workflows & Declarations 1`, `Software Workflow & QA 5`, and followed every create and delete below without a reload. The `/next/` rail has no Processes count badge; classic shows `processes 15`.
- The existing `next-processes-narrowing` and `next-processes-walkthrough` specs (legacy links, `?kind=`/`?q=` through reload and Back/Forward, keyboard reorder) pass on this branch.
- At `390×844` the section tab strip runs past the viewport inside its own `overflow-x: auto` scroller, and `documentElement.scrollWidth` stays `390`.

### 2. Schedules (`/next/`)

- New → Schedule, inline prompt `Walk prompt schedule`, with Keep running, Admission `Bypass queue` (options `Standard`, `Bypass queue`, `Durable`), Hosting `Interactive console` (`Unattended`, `Interactive console`), Grant `api:grain/ci-read` (the demo's Grain API group), and Model typed `flash` in the searchable picker. The picker offered only `gemini-2.5-pro` and `gemini-2.5-flash`, the demo connection's models.
- `Every N min` has `min=10`. Saving `5` showed `recurrence: everyNMinutes must be at least 10` in the open dialog, and `10` saved as `sched-3`. The draft preview and the saved pane's composition both read `model gemini-2.5-flash schedule … keepRunning on … admission bypass … hosting console … capabilities …api#grain/ci-read`.
- Run now filed `#24` with `{admission: bypass, hosting: console, keepRunning: true, model: gemini-2.5-flash, capabilities: [api#grain/ci-read]}`. It appears under `Recent fired tasks & skips`, and the row's `next:`/`Last run` updated without a reload.
- The Configuration pane lists `Model` but not Admission, Hosting or Keep running; the composition preview shows all three.
- **Bug 1 (fixed):** an unsaved edit was dropped silently. Edit → change Base → Escape (or Cancel, the scrim, browser Back) closed the dialog and lost the edit, where classic asks (`useUnsavedEdits`).
- `git_ref` watching `grain/config-ops` was refused in the dialog: `recurrence.gitRef.repo: grain/config-ops not found: only grain/config exists`. Off-target `offlist/upstream` saves. Its row reads `Suppressed · Off-target git ref · Trigger · On git ref: offlist/upstream @ refs/heads/main · acme/widgets · next: suppressed`, and the banner reads `Suppressed — watched repository not targeted: watches offlist/upstream, which is not on this deployment's target repo list`. Pause → `Paused`. The `Suppressed` chip narrowed the list to it.

### 3. Templates

- `/next/` New → Template `walk-unbound`: no repo, Keep running, Admission `Durable`, Hosting `Interactive console`, Model `gemini-2.5-flash`. Its pane reads `Repository Chosen at launch`, and the composition shows `keepRunning on`, `admission durable`, `hosting console`, `model gemini-2.5-flash`.
- Classic `/processes?kind=template` → `+ new template` opened the template form directly. Task priority `2.5` → `…The two nearest valid values are 2 and 3.`, `0` → `Value must be greater than or equal to 1.`, and `1e3` was accepted and saved as `priority: 1000`, with `interactive: true`. `New task from this template` opened `/new` with Task priority `1000`.
- Classic `use…` on the interactive template shows `This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed.`
- Unbound template with `reads: ["acme/widgets"]`: `/next/` Launch task with repo `acme/widgets` filed `#27`, and classic `use…` filed `#28`. Both have `repo: acme/widgets` and no `reads`: the target was dropped from Reads instead of failing.

### 4. Suites and suite runs (`/next/`)

- `Release Qualification Suite` → `Run suite` with repository and base empty → `/next/processes/suites/runs/1`, `Active`, `Target: acme/widgets @ main`, `Run 2× · currently on pass 1`. The row reads `Active · Release Qualification Suite #1 · Pass 1 · acme/widgets @ main · 2 tasks`.
- New suite `Walk unbound suite` (member `walk-unbound`, which the dialog pre-selects as the newest template), `Until clean`, max `2`, auto-merge off, approval on. The draft preview reads `Until clean (max 2 passes)`, `approval waits for approval`, `admission durable`, `hosting console`. Running it on `acme/widgets` with no base → run `#2`, task `#26` `proposed` (waiting for approval).
- The server now fills in the run's base, or refuses a run with no default branch (`base is required: a suite run has no default branch to fall back to`), so a base-less run exists only in older data. The regression spec stubs one (stub 5): its row and pane read `acme/widgets @ default branch`, and its status chip is whole and on one line at `390×844`.

### 5. Repository declarations (`/next/`)

- The invalid push showed both errors under `decl-sync-error` once the page polled: the declared schedule's `title:` and the `*/12` cron's explanation, each on its own line.
- After the valid push and the next sync (about 2 min): `decl-bump`'s pane reads `Repo-declared read-only template from acme/widgets (.grain/templates/decl-bump.yml)` and offers only `Launch task` (no Edit/Delete, no GrantsPicker). `decl-sweep` reads `Declared in acme/widgets (.grain/suites/decl-sweep.yml).` with only `Run suite`. `Declared nightly bump` reads `Declared in acme/widgets at .grain/schedules/decl-nightly.yml. Update that file in git to modify this schedule.` with `Run now` and a disabled `Pause`. Its composition shows `admission durable template` and `hosting console schedule`: a template-backed schedule previews its own hosting.
- Declared rows carry `.grain` and no `System` chip. The trigger row reads `Declared docs-guard.yml · Trigger · .grain · acme/widgets · .grain/triggers/docs-guard.yml · next: on change`.
- PATCH and DELETE of the declared template were refused with `…is declared by acme/widgets in .grain/templates/decl-bump.yml, so it is not this deployment's to edit -- change the file there…`.
- No element in the declared schedule's pane overflowed its cell at either width.

## Bugs found

| # | Bug | Repro | Fix | Coverage |
| --- | --- | --- | --- | --- |
| 1 | `/next/` schedule, template and suite dialogs dropped an unsaved edit on Escape, Cancel, the scrim, browser Back or a rail link, without asking | `/next/processes/schedules/sched-1` → Edit → change Base → Escape | Each dialog snapshots its draft when it opens. Closing a changed draft asks `Discard unsaved changes?` once, the route guard asks the same, and `beforeunload` covers a reload. Save closes without asking | `next-processes-unsaved-and-runs.spec.ts`; D-9 unit test updated |

Recorded, not changed (parity gaps where `/next/` has no control yet, see the table above): kind chips and the rail count, New opening the narrowed kind, Task priority, Interactive, New task from this template, Reads/writes/network/custom fields on schedules, the searchable suite Templates combobox with inline `+ New template`, and Admission/Hosting/Keep running in the schedule Configuration pane.

## Tests run

- `npx vitest run src/app/processes` in `ui-ng/`: 73 passed.
- `npx playwright test e2e/next-processes-unsaved-and-runs.spec.ts`: 3 passed (desktop, plus phone for the run test).
- `npx playwright test e2e/next-processes-narrowing.spec.ts e2e/next-processes-walkthrough.spec.ts e2e/next-schedule-held-detail.spec.js e2e/next-suites-and-task-actions.spec.js e2e/next-full-system.spec.js e2e/next-processes-run-times.spec.ts e2e/next-processes-forms.spec.ts e2e/next-qa-workflow-studio.spec.js`: 48 passed and 2 failed (one test, `a refused schedule save is in view beside its actions`, on desktop and phone). That test cancels a filled-in draft, which now asks first; after updating it to accept the prompt it passes on both.
- `npx playwright test e2e/next-walkthrough-daemon-rbac-repos.spec.js e2e/next-processes-walkthrough.spec.ts`: 10 passed.
- No classic (`ui/`) code was changed.
