# Recurring and Composed-Work Automation UI Walkthrough (2026-11-16)

- **Date:** 2026-11-16 (UTC), 10:05–10:50.
- **Task:** [Task #6334](/tasks/6334), branch `grain/53549e42/task-6334-walk-through-processes`, base `main`.
- **Commit tested:** `feb34cb22` (`v0.0.0-20261116095301-feb34cb22de8`, footer `feb34cb`) for the walkthrough; the fixes below were then verified on this branch's head over a fresh throwaway store.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`, Python `3.11.2`.
  - Built with `make frontend` (both `ui/` and `ui-ng/`) and `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain`. `make frontend` installs without dev dependencies, so `npm ci --include=dev` had to be re-run in `ui-ng/` (and `ui/`) before Playwright could load `@playwright/test`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` over a throwaway `TMPDIR` (embedded SQLite store), signed in through `POST /api/auth/access-code` as `alice@example.com`.
  - Browser: headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) driven by throwaway Playwright scripts outside the repository, timezone `UTC`, at `1440×900` and `390×844` (touch, mobile), light and dark.
  - No `/home/debian/.gemini-api-key` was mounted, so no live model was called and `LOOK=1` was unavailable. Screenshots were rendered (`ui/e2e/screenshot.mjs`, before = base binary, after = this branch, for `/next/processes/schedules`, `/next/processes/suites` and `/next/processes/declarations` at desktop and phone, light and dark). The file reader here cannot display PNGs, so layout was checked through the DOM instead: an overflow scan of every element under `main` against the viewport, chip `scrollWidth`, and bounding-box checks on the changed values.
- **Console and network:** zero `pageerror`s, zero Angular `ERROR` logs and zero `5xx` responses across the walkthrough. Every `4xx` was an intentional negative step: `400` for the 5-minute schedule, the missing required template field and the referenced-template delete, and `409` for Run now on a schedule whose previous firing was still open.

## Which UI each step ran against

`ui-ng/src/app/shell/nav.ts` routes Processes to `/next/processes`, and `ui-ng/src/app/app.routes.ts` carries `/next/processes[/:tab[/:itemId]]`, `/next/processes/suites/runs/:runId` and the `/next/schedules`, `/next/templates` and `/next/suites[/runs]/<id>` redirects. Every step below was walked on `/next/` first. The classic `/processes` was used only for controls `/next/` does not have yet:

| Step | `/next/` | Classic only (no `/next/` control yet) |
| --- | --- | --- |
| Navigation, tabs with counts, search, `?q=`, `?kind=`, sort, legacy links, Back/Forward | yes | rail count badge; the unified single table with `All`/`Workflow schedules`/`Triggers` kind chips (`/next/` uses four section tabs plus per-tab filter chips instead) |
| New menu (Schedule / Template / Suite + `.grain/` hint) | yes | — |
| Schedules: prompt/template/suite targets, cadences, 10-minute floor, `git_ref`, overlap, preview, Run now, Pause/Resume, Edit, Delete, task links | yes | `Reads`/`writes`, `GrantsPicker`, `Admission`, `Hosting`, `Keep running`, network, custom fields, searchable model picker |
| Templates: create with parameters, Launch task, dependency-aware delete | yes | `Task priority` (`1e3`, whole number), sandbox shape, `Reviewers`, `Admission`, `Hosting`, `Keep running`, `Interactive`, `GrantsPicker`, model picker, `New task from this template` |
| Suites: create, edit, run modes, pass limits, approval, auto-merge, delete; suite runs | yes | searchable Templates combobox and inline `+ New template` |
| Repository declarations, workflow schedules, triggers | yes | — |

## Stubs and seeded states

Every create, edit, run, pause, delete and render went through the real browser UI and the real `grain demo` server. Only these were stubbed or seeded:

1. **Agent execution:** the demo files tasks but runs no agents (`-fake-ci` for CI). Fired tasks therefore stay `queued`, which is what produced the overlap-held (`Suppressed — previous firing still open`) state below.
2. **Repository declarations:** files pushed with `git` straight to the simulated `acme/widgets.git` (`-github-sim`): `.grain/templates/decl-bump.yml` (`admission: durable`, `keep-running: true`), `.grain/suites/decl-sweep.yml` (`until-clean`, `max-passes: 2`), `.grain/schedules/decl-nightly.yml` (template, every 6 h), `.grain/schedules/decl-lint.yml` (workflow, every 12 h), `.grain/workflows/lint.yml` and `.grain/triggers/docs-guard.yml`. The first two pushes were deliberately invalid: a workflow with no `on:`, then one with no `on: schedule`.
3. **Off-target `git_ref` schedule:** `POST /api/schedules` with `recurrence: {kind: "git_ref", gitRef: {repo: "offlist/upstream", ref: "refs/heads/main"}}` (`sched-5`), because `offlist/upstream` cannot be picked in the dialog.

## Steps and outcomes

### 1. Processes, narrowing and links (`/next/`)

- Rail `Processes` → `/next/processes` by SPA navigation. Tabs read `Schedules & Triggers 2`, `Templates 3`, `Suites & Runs 1`, `Repo Workflows & Declarations 1`. They followed every later create and delete, and the declared rows after sync, without a reload (`Templates 4` after `walk-throwaway`, `Suites & Runs 2` after the suite).
- `/next/processes/templates?q=release` narrowed to one row. Switching tab kept `?q=` (`/next/processes/suites?q=release`), and Back, Forward and reload kept both tab and query.
- `?kind=template|suite|declaration` chose the matching tab. `/next/schedules`, `/next/templates/template-1`, `/next/suites/suite-1` and `/next/schedules/sched-1` landed on the matching tab with that item selected. `/next/suites/runs/1` → `/next/processes/suites/runs/1`, and `/next/processes/suites/runs/999` said `No suite run "999".` with no console error. Classic `/schedules`, `/templates` and `/suites[/<id>]` landed on `/processes?kind=…` and `/processes/<kind>s/<id>`.
- Template rows derive their runs from the schedules that fire them: `security-audit` read `· next: Nov 16, 2026, 10:23:11 AM UTC` while its schedule was on, and `· last: … · on demand` after Pause. Pause and Resume flipped it each time through the SPA tabs, without a reload. `Set up a GKE sandbox cluster` and an unused suite read `on demand`.
- Keyboard: Tab reaches the first schedule row (after the rail and toolbar), and Enter opens it in the inspector.

### 2. New menu (`/next/`)

- Header `New ▾` lists `Schedule`, `Template` and `Suite` plus `Workflow schedules and triggers are declared in a repository's .grain/, not created here.` Classic `+ New` has the same three items and the same `.grain/` hint.

### 3. Schedules (`/next/`, classic for the fields `/next/` lacks)

- New schedule → Template target (`security-audit`) → `Every N min`: the field has `min="10"`. Saving with `5` showed `recurrence: everyNMinutes must be at least 10` inside the open dialog. With `10` it saved, and the row read `Every 10 min · acme/widgets · next: … · overlap: skip`.
- Run now filed `#24` (shown under `Recent fired tasks & skips`, linking `/next/task/24`; Back returned to Processes). A second Run now was refused (`409`) with the server's sentence in the page's alert: `…this schedule is set to skip a run that would overlap one -- wait for it, or set "If the last run has not finished" to start it anyway`. That sentence names the classic field's label; `/next/` calls the control `Overlap policy` (noted, not changed).
- While `#24` stayed queued the schedule showed `Suppressed` on its row, and the inspector banner said `Suppressed — previous firing still open: … — Open task #24`, linking `/next/task/24`.
- **Bug 1 (fixed):** the inspector's `Next run` and `Last run` read `2026-11-16T10:23:11.320429227Z` while the row beside them read `Nov 16, 2026, 10:23:11 AM UTC`.
- Off-target `git_ref` (`sched-5`): the row reads `Suppressed · Trigger · On git ref: offlist/upstream @ refs/heads/main · next: suppressed`. The banner reads `Suppressed — watched repository not targeted: watches offlist/upstream, which is not on this deployment's target repo list`, and Configuration shows `Next run suppressed`. Pause worked (`Paused`, the next run disappeared), and so did Resume.
- Classic `+ New → Schedule`: the model picker offered only `Deployment default (gemini-2.5-pro)`, `gemini-2.5-pro` and `gemini-2.5-flash` (the demo connections' models); Admission offered `standard`, `bypass queue` and `durable`; Hosting offered `unattended` and `interactive console`. Saved with `bypass queue`, `interactive console`, `keep running` and `gemini-2.5-flash` → `sched-4` `{admission: "bypass", hosting: "console", keepRunning: true, model: "gemini-2.5-flash"}`.
- Cross-UI edit: `sched-4` renamed through the `/next/` dialog, which has none of those fields, kept `admission`, `hosting`, `keepRunning`, `model`, `customFields` and `nothingNeededAction` (PATCH sends only what the dialog edits). The `/next/` Configuration pane shows `Model` but not Admission, Hosting or Keep running; the composition preview does show `keepRunning on`.

### 4. Templates (`/next/`, classic for priority and New task)

- New template `walk-throwaway` (bound to `acme/widgets`, parameter `pkg`, required). The inspector showed the parameter schema and composition. `Launch task` with `pkg` empty → `field "pkg" is required` in the dialog; with `lodash` it filed `#26` and opened `/next/task/26`.
- Deleting `security-audit` through its confirmation (`Delete template "security-audit"?`) was refused with `template is used by 1 schedule(s) (…) and 1 suite(s) (…); repoint or delete the schedules and remove it from the suites first`, and the template stayed.
- Classic template `walk-classic-tmpl`: Task priority `1e3` saved as `1000` (intended: a whole number with an exponent). `2.5` and `0` were stopped by the field's own validation (`…nearest valid values are 2 and 3`, `…greater than or equal to 1`) and nothing changed. `New task from this template` opened `/new` with Task priority `1000`.

### 5. Suites and suite runs (`/next/`)

- **Bug 2 (fixed):** New suite → `Until clean`, `Maximum passes` typed as `0` → Save closed the dialog and saved `maxPasses: 3`, with the field still showing `0`. The dialog also pre-selects the first template and turns auto-merge on.
- **Bug 3 (fixed):** the suite row read `1 templates`, and its run row and pass header read `1 tasks`.
- From the suite's inspector, `Run suite` → `Start suite run` (repository and base left empty) filed suite run `#1` on `acme/widgets @ main` (the template is bound). `/next/processes/suites/runs/1` showed `Active`, `Until clean (max 3 passes) · currently on pass 1`, `Pass 1` and its task link (→ `/next/task/25`, Back returned to the run). The header `Run suite` opens the same dialog with a suite picker.
- `Cancel suite run` asked `Stop and close all open pass tasks in suite run #1 (acme/widgets)?`. Escape kept it running; confirming closed `#25`. The run moved to `Failed` with `pass 1 had a task that failed, or closed without completing` once the orchestrator reconciled; the page follows on its next poll. At `390×844` the `Failed` chip and pane fit (`documentElement.scrollWidth === 390`).
- `Delete` → `Delete suite "Walk throwaway suite"? Past suite runs keep their snapshots.` → removed, and `Suites (1)` without a reload.

### 6. Repository-declared items (`/next/`)

- `Repo Workflows & Declarations` → `Sync .grain/ declarations` switch on. The invalid workflow surfaced in `decl-sync-error`. **Bug 4 (fixed):** the server's two-line error (`…did not parse` + `ci: .grain/workflows/lint.yml: on: …`) ran together on one line.
- After the valid push, the sync (commit `c3b2581bb1ae`) listed 1 template, 1 suite, 3 schedules and 1 trigger. On Schedules & Triggers: `Declared nightly bump` and `Declared lint workflow` carry the `.grain` tag and no `System` chip. Each pane names `Declared in acme/widgets at .grain/schedules/<file>.yml. Update that file in git to modify this schedule.`, hides Edit and Delete, and disables Pause. The workflow schedule's Run now confirms `It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged.` The `docs-guard.yml` trigger row opens `/next/repos/acme/widgets/advanced`.
- Suppression wording: **Bug 5 (fixed):** a suppression the server sends without a `detail` fell back to `Previous firing is still open.` whatever its reason (for example `suiteRun` or `watchedRepoOffTarget`).

### 7. Phone, dark and overflow

- At `390×844` in light and dark, `/next/processes/schedules`, `/schedules/sched-3`, `/schedules/sched-4`, `/templates/template-2`, `/suites/suite-1`, `/suites/runs/1`, `/declarations` and the declared workflow schedule had no element past the viewport outside a scroll container, and no clipped chip.

## Bugs found

| # | Bug | Repro | Fix | Coverage |
| --- | --- | --- | --- | --- |
| 1 | `/next/` schedule Configuration printed `Next run`/`Last run`, and the Declarations card `Checked:`, as raw RFC 3339 (`2026-11-17T10:24:14.543896659Z`) | Open any schedule on `/next/processes/schedules` | `stamp()` (`formatStamp`), as the rows use; `sched-next-run`/`sched-last-run` test ids | unit + `next-processes-run-times.spec.ts` |
| 2 | Suite dialog saved a typed `0`/empty pass count as the default (`\|\| 3`, `\|\| 1`) while showing `0`; a fraction or `101` reached the server | New suite → Until clean → Maximum passes `0` → Save | Keep what was typed; refuse anything but a whole number 1–100 (`model.MaxSuitePasses`) in the dialog for Save and Preview | unit + e2e |
| 3 | `1 templates`, `1 tasks`, `1 params` | Suite with one template; its run | `countOf()` | unit + e2e |
| 4 | A multi-line declarations sync error ran onto one line | Push a workflow with no `on:`; turn on sync | `white-space: pre-line` on `decl-sync-error` | (visual; one style) |
| 5 | Undetailed suppression borrowed `Previous firing is still open.` | Any `suppressed` without `detail` | End at the reason's own label | unit |

Not changed, recorded for follow-up: selecting a row on `/next/processes` does not put the item in the URL, so a reload or a copied address lands on the tab's first row rather than the item. `/next/` deep links themselves work.

## Tests run

- `npx vitest run src/app/processes` in `ui-ng/`: 18 passed, including the new `formats a schedule's run times, keeps an undetailed suppression to its own reason, counts in the singular, and refuses a 0-pass suite in the dialog`.
- `npx playwright test e2e/next-processes-narrowing.spec.ts e2e/next-processes-walkthrough.spec.ts e2e/next-schedule-held-detail.spec.js e2e/next-suites-and-task-actions.spec.js e2e/next-full-system.spec.js e2e/next-processes-run-times.spec.ts` in `ui-ng/` (desktop, phone, dark): 40 passed.
- No classic (`ui/`) code was changed.
