# Recurring and Composed-Work Automation UI Walkthrough (2026-11-18)

- **Date:** 2026-11-18 (UTC), 04:15–04:45.
- **Task:** [Task #7018](/tasks/7018), branch `grain/53549e42/task-7018-walk-through-processes`, base `main`.
- **Commit tested:** `a6fdad20c` for the walkthrough. The fix below (`675c2af1e`) was then checked on a second, fresh demo built from this branch.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`.
  - Built with `make frontend`, then `npm ci --include=dev` in `ui-ng/` and `ui/` (`make frontend` installs without dev dependencies, which removes Playwright), and `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` over a throwaway `TMPDIR` (embedded SQLite store). The fixed build ran the same way on `:8421` over its own store. Signed in through `POST /api/auth/access-code` as `alice@example.com` (`ui-shared/e2e/accesscode.js`).
  - Browser: headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), driven by throwaway Playwright scripts kept outside the repository. Timezone `UTC`, at `1440×900` and at `390×844` (touch, mobile), in light and dark.
  - There was no `/home/debian/.gemini-api-key`, so no live model was called and `LOOK=1` was not available. The file reader here shows PNGs only as bytes, so layout was checked through the DOM instead: bounding boxes against the dialog and the viewport, and `documentElement.scrollWidth`.
- **Console and network:** zero `pageerror`s and zero `5xx` responses. Every `4xx` came from a deliberate negative step: `400` for the 5-minute schedule, `400` for the forbidden watched repo `grain/config-ops`, `400` for deleting a template that a schedule and a suite still use, and `400` for PATCH/DELETE of a declared template.

## Which UI each step ran against

`ui-ng/src/app/shell/nav.ts` routes Processes to `/next/processes`. `ui-ng/src/app/app.routes.ts` carries `/next/processes[/:tab[/:itemId]]`, `/next/processes/suites/runs/:runId` and the `/next/schedules`, `/next/templates` and `/next/suites[/runs]/<id>` redirects. Every step below ran on `/next/`. Nothing was walked on classic this time: yesterday's run ([2026-11-17](2026-11-17-recurring-composed-ui.md)) walked the classic-only controls, and nothing in `ui/` changed since then.

These controls still have no `/next/` equivalent, so those steps have no `/next/` page yet:

- the rail's Processes count badge, and the single table's kind chips (`/next/` uses section tabs with counts);
- New opening the narrowed kind directly (`/next/`'s New is always the menu);
- `New task from this template`, and `Use…` with its interactive note (`/next/` has `Launch task`);
- `Reads`/`writes`, network and custom fields on schedules and templates;
- the suite's searchable Templates combobox with inline `+ New template` (`/next/` uses toggle chips).

**Task priority and the Interactive switch on templates had no `/next/` control either. This branch adds both (see "Bugs found").**

## Stubs and seeded states

Every create, edit, run, pause, enable and delete gesture below went through the real browser UI and the real `grain demo` server, and so did all rendering. Only these were stubbed or seeded:

1. **Agent execution:** the demo files tasks but runs no agents (`-fake-ci` for CI), so fired tasks stay `queued` or `proposed`.
2. **Repository declarations:** files were pushed with `git` straight to the simulated `acme/widgets.git` named in the server log: `.grain/templates/decl-bump.yml` (`admission: durable`, `keep-running: true`, `priority: 7`), `.grain/suites/decl-sweep.yml`, `.grain/schedules/decl-nightly.yml` (template, `every: {hours: 6}`, `hosting: console`) and `.grain/triggers/docs-guard.yml`. The first push used the wrong keys on purpose (`description:` and `recurrence:`). Declarations for `acme/widgets` were switched on with the UI toggle under Repo Workflows & Declarations.

## Steps and outcomes

### 1. Processes, navigation and narrowing (`/next/`)

- Rail `Processes` from `/next/inbox` → `/next/processes` by SPA navigation. The tabs read `Schedules & Triggers 2 | Templates 3 | Suites & Runs 1 | Repo Workflows & Declarations 1 | Software Workflow & QA 5`. They followed every create below without a reload, ending at `8 | 5 | 3`.
- Status chips: `All`, `Active`, `Paused`, `Event triggers`, `Suppressed`. Sort starts on `Custom order`.
- Legacy links: `/next/schedules`, `/next/templates`, `/next/suites` and `/next/suites/runs/1` each landed on the matching Processes view.
- Derived run times: `release-qualification` reads `next: Nov 19, 2026, 12:00:00 AM UTC` (from `sched-1`). `security-audit` read `on demand` until a template schedule was saved for it, then `next: Nov 18, 2026, 10:23:44 AM UTC`, without a reload. The suite went the same way: `on demand`, then `next: Nov 18, 2026, 09:00:00 AM UTC`. `Walk suite`, started by hand, reads `last: … 04:26:39 AM UTC · on demand`.

### 2. Schedules (`/next/`)

- New menu: `Schedule`, `Template`, `Suite`, then `Workflow schedules and triggers are declared in a repository's .grain/, not created here.`
- **Inline prompt schedule:**
  - `Every N min` = `5` was refused in the open dialog with `recurrence: everyNMinutes must be at least 10`. `10` saved it as `sched-3`.
  - Set Admission `Bypass queue`, Hosting `Interactive console` and Keep running.
  - The model picker offered only `gemini-2.5-pro` and `gemini-2.5-flash`, the demo connection's models. Typing `flash` narrowed it to one.
  - The draft preview reads `model gemini-2.5-flash schedule … keepRunning on schedule admission bypass schedule hosting console schedule capabilities …api#ci-read={"repo":"acme/widgets"} repo`.
- **Run now** filed `#24` and updated `Last run` in place. `Recent fired tasks & skips` lists `#25 · Walk prompt schedule (edited) queued` within a second of the second Run now, and its link opens `/next/task/<id>`.
- **Pause and resume:** Pause → row `Paused`, button `Resume`, and the row drops its `next:` while paused. Both states survive a reload. Resume → `Enabled`, `next:` back.
- **Unsaved edits:** Edit → change Base → Escape asked `Discard unsaved changes?` once. Discard dropped the change.
- **Edit:** title `Walk prompt schedule (edited)` and `Allow overlap`. The row and pane followed without a reload (`overlap: allow`).
- **Template target:** `security-audit`. The dialog note reads `Admission, hosting, grants and keep running come from the selected template, and stay in sync with it.` Saved `Every 6 hr` as `sched-4`.
- **Suite target:** `Release Qualification Suite`, Daily, saved as `sched-5`.
- **`git_ref`:**
  - Watching `grain/config-ops` was refused in the dialog with `recurrence.gitRef.repo: grain/config-ops not found: only grain/config exists`.
  - `offlist/upstream` saved as `sched-6`. Its row reads `Suppressed Walk gitref Trigger On git ref: offlist/upstream @ refs/heads/main · acme/widgets · next: suppressed`, and its banner reads `Suppressed — watched repository not targeted: watches offlist/upstream, which is not on this deployment's target repo list`.
  - The `Suppressed` chip narrowed the list to that one row.

### 3. Templates (`/next/`)

- **New → Template `walk-full`:**
  - Settings: repo `acme/widgets`, parameter `pkg` with default `lodash`, Keep running, Admission `Durable`, Hosting `Interactive console`, Model `gemini-2.5-pro` (picked by typing `pro`).
  - New parameters start Required. The switch shows it, and the launch form reads `pkg (required)`.
  - The composition reads `walk: bump lodash · from walk-full … keepRunning on template admission durable template hosting console template`.
- **Launch task** filed `#26` with `{title: "walk: bump lodash", admission: durable, hosting: console, keepRunning: true, model: gemini-2.5-pro}` and opened `/next/task/26`.
- **Rename** to `walk-full-renamed`: the row followed without a reload, and the Suite dialog's member chips showed the new name.
- **Dependency-aware delete:** deleting `security-audit` asked `Delete template? Delete template "security-audit"?`. The server then refused with `template is used by 1 schedule(s) ("Security: Dependency vulnerability scan") and 1 suite(s) ("Release Qualification Suite"); repoint or delete the schedules and remove it from the suites first`, shown in the page. The template stayed.
- **New on this branch: Task priority and Interactive** (fixed build on `:8421`):
  - `2.5`, `0` and `high` each show `Use a whole number, 1 or more.`, set `aria-invalid` and disable Save.
  - `1e3` reads back as `1000` on blur and saves as `priority: 1000`. Interactive saves as `interactive: true`, and the Configuration pane reads `Priority 1000` and `Interactive on`.
  - Reopening the edit shows both. Clearing the priority sends `0`, which takes the template back to the default.
  - Layout: at both widths and in both themes, the input, its error and the switch sit inside the dialog, and `scrollWidth` equals the viewport (`1440`/`390`).

### 4. Suites and suite runs (`/next/`)

- **New → Suite `Walk suite`:**
  - The newest template (`walk-full-renamed`) starts selected. Added `release-qualification`, mode `Until clean`, max `2`, approval on, auto-merge off.
  - The draft preview reads `Until clean (max 2 passes) … approval waits for approval suite`.
- **Run from the suite pane** (`acme/widgets`, base `main`) → `/next/processes/suites/runs/1`. The pane reads `Active · Walk suite · Run #1 · Target: acme/widgets @ main · Until clean (max 2 passes) · currently on pass 1 · Pass 1 (2 tasks) #27 · walk: bump lodash proposed #28 · … proposed`.
  - The row reads `Active Walk suite #1 Pass 1 acme/widgets @ main · 2 tasks`.
  - `#27` opened `/next/task/27`, and Back returned to the run.
  - At `390×844` nothing in the run pane ran past the viewport.
- **The header's generic `Run suite`** lists both suites. It prefills Base with `main` and keeps `Start suite run` disabled while Base is blank. That matches the server, which refuses a blank base (`base is required: a suite run has no default branch to fall back to`). No base-less run can be started from either UI any more, so the `default branch` presentation is only reachable through old data. `next-processes-unsaved-and-runs.spec.ts` still covers it with a stubbed run, and passes.

### 5. Repository declarations (`/next/`)

- **Invalid push:** the Declarations tab shows `.grain declarations: enabled · sync error` and each refused file on its own line: `.grain/templates/decl-bump.yml: yaml: unmarshal errors: line 3: field description not found…`, `.grain/suites/decl-sweep.yml: no template "decl-bump" -- a suite runs the templates this repository declares…`, and `.grain/schedules/decl-nightly.yml: … field recurrence not found…`.
- **After the corrected push** (`Synced commit: 6fd60a87628e`):
  - **Template:** `Repo-declared decl-bump decl: bump deps · acme/widgets (.grain/templates/decl-bump.yml) · next: …10:38:33 AM UTC`. Its banner reads `Repo-declared read-only template from acme/widgets (.grain/templates/decl-bump.yml)`. The pane has no Edit, no Delete and no GrantsPicker, and Configuration shows `Source acme/widgets … Priority 7`.
  - **Server refusals:** PATCH and DELETE are refused with `…is declared by acme/widgets in .grain/templates/decl-bump.yml, so it is not this deployment's to edit -- change the file there…`.
  - **Schedule:** the row reads `Enabled Declared nightly bump .grain Every 6 hr · acme/widgets`. There is no `System` chip and the pane reads `Declared in acme/widgets at .grain/schedules/decl-nightly.yml…`. Pause is disabled, with no Edit or Delete, and Run now fires at once.
  - **Suite:** `Run 1× decl-sweep .grain 1 template`.
  - **Trigger:** the row reads `Declared docs-guard.yml Trigger .grain acme/widgets · .grain/triggers/docs-guard.yml · next: on change`. Its `Open repository settings` link opened `/next/repos/acme/widgets/advanced`.
- No workflow schedule was declared this run. `next-processes-walkthrough.spec.ts` covers its Run now wording, and passes.

## Bugs found

| # | Bug | Repro | Fix | Coverage |
| --- | --- | --- | --- | --- |
| 1 | The `/next/` template dialog had no Task priority or Interactive control, so a template's priority and live-chat setting could only be set from classic (the coverage list's "Task priority whole-number and `1e3` validation … interactive switch") | `/next/processes/templates` → New → Template: no priority field, no Interactive switch | The dialog takes Task priority with `/next/` batch edit's whole-number rule (`taskPriority`: `1e3` → `1000`; blank = default; clearing on edit sends `0`) and an Interactive switch. Both are part of the unsaved-edit snapshot. Configuration names Interactive when it is on | `ui-ng/e2e/next-template-priority-interactive.spec.ts` |

Recorded, not changed:

- **Fired tasks matched by title.** `Recent fired tasks & skips` finds a schedule's tasks by title (`scheduledTasksFor`), because a filed task carries `scheduled: true` but no schedule id. Renaming a schedule therefore hides the tasks it fired before the rename. Fixing that needs the server to record the schedule on the task, which is materially separate from this walkthrough.
- **Hard-coded `main`.** The Run suite dialog's Base prefill is `main` for every repository. The server has no default-branch lookup to offer anything better.

## Tests run

- `npx vitest run src/app/processes` in `ui-ng/`: 76 passed.
- `npx playwright test e2e/next-processes-forms.spec.ts e2e/next-processes-unsaved-and-runs.spec.ts e2e/next-processes-walkthrough.spec.ts e2e/next-processes-narrowing.spec.ts e2e/next-suites-and-task-actions.spec.js e2e/next-full-system.spec.js e2e/next-template-priority-interactive.spec.ts` in `ui-ng/`: 47 passed across the desktop, phone and dark projects.
- Screenshots: `ui/e2e/screenshot.mjs --bin <fixed build> --pages /next/processes/templates,/next/processes/templates/template-1` at desktop and phone, light and dark, plus the template dialog with the new fields in all four combinations (not committed).
- No classic (`ui/`) code was changed.
