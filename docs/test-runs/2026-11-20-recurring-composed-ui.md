# Recurring and Composed-Work Automation UI Walkthrough (2026-11-20)

- **Date:** 2026-11-20 (UTC), 12:29–13:25.
- **Task:** [Task #7547](/tasks/7547), branch `grain/53549e42/task-7547-walk-through-processes`, base `main`.
- **Commit tested:** `2c44ffa0b` (`origin/main`) for the walkthrough. The fixes below (`51d11a6bf`, `b0f1dd21d`) were then checked with the `ui-ng` Playwright suite over a `grain demo` built from this branch.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`.
  - Built with `make frontend`, then `npm ci --include=dev` in `ui-ng/` (the `make frontend` install drops Playwright), and `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain`.
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` over a throwaway `TMPDIR` (embedded SQLite store). Signed in through `POST /api/auth/access-code` as `alice@example.com`.
  - Browser: headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), driven by throwaway Playwright scripts kept outside the repository. Timezone `UTC`. Viewports `1440×900` and `390×844` (touch, mobile).
  - There was no `/home/debian/.gemini-api-key`, so no live model was called. Layout was checked through the DOM: bounding boxes against the dialog and viewport, and `documentElement.scrollWidth`.
- **Console and network:** zero `pageerror`s and zero `5xx` responses. Every `4xx` came from a deliberate negative step:
  - `400` for the 5-minute schedule;
  - `400` for the forbidden watched repository `grain/config-ops`;
  - `400` for a `git_ref` without a full `ref:`;
  - `409` for Run now over an unfinished skip-overlap run;
  - `400` for deleting a template still in use;
  - `400` for saving a suite whose template has a required field with no default;
  - `400` for `DELETE` of a declared template.

## Which UI each step ran against

Processes is on `/next/`: `ui-ng/src/app/shell/nav.ts` routes the rail's Processes entry to `/next/processes`. `ui-ng/src/app/app.routes.ts` carries these routes:

- `/next/processes[/:tab[/:itemId]]`;
- `/next/processes/suites/runs/:runId`;
- the `/next/schedules`, `/next/templates` and `/next/suites[/runs]/<id>` redirects.

**Every step below ran on `/next/`.** Classic (`/`) was not walked. No classic page was found broken, and no classic code changed. Since the 2026-11-18 report, these controls have gained a `/next/` equivalent:

- New task from this template;
- the Launch dialog's interactive note;
- schedule Reads, Writes, Network and Custom fields;
- the suite's searchable Templates combobox with inline `+ New template`;
- template Task priority and Interactive.

These still have no `/next/` equivalent:

- **Kind chips.** `/next/` uses section tabs with counts, plus per-tab status chips (`All / Active / Paused / Event triggers / Suppressed` on schedules, and `All / Deployment / Repo-declared / Built-in` on templates). The narrowing rides in the path (`/processes/<tab>`) and `?q=`, `?sort=`; there is no `?kind=`.
- **Template Reads, Writes, Network and Custom fields.** The `/next/` template dialog has none of them, although the schedule dialog's note says a template-backed schedule takes them from its template. So "an unbound template with `Reads` drops the chosen target repository from `Reads`" could not be walked on `/next/`. It is filed as a follow-up (below).

## Stubs and seeded states

Every create, edit, run, pause, resume, cancel and delete gesture went through the real browser UI and the real `grain demo` server, and so did all rendering. Only these were stubbed or seeded:

1. **Agent execution.** The demo files tasks but runs no agents (`-fake-ci` for CI). Fired tasks stay `queued` or `proposed`. Failed suite runs came from the UI's own **Cancel suite run**: the server closes the pass's tasks and the reconciler marks the run `failed`.
2. **Repository declarations.** Files were pushed with `git` to the simulated `acme/widgets.git` that `-scheduled-workflows` names in the server log:
   - `.grain/templates/{decl-bump,docs-check}.yml`
   - `.grain/suites/decl-sweep.yml`
   - `.grain/schedules/{decl-nightly,decl-ci}.yml`
   - `.grain/workflows/nightly-ci.yml`
   - `.grain/triggers/docs-guard.yml`

   Declarations were switched on with the UI toggle on the Repo Workflows & Declarations tab.
3. **A model no connection serves.** The picker only offers models that a connection advertises, so `sched-2` was given `model: legacy-model-x` with `PATCH /api/schedules/sched-2`. This stands in for old data.
4. **A role without `suites:write`.** An access-code caller is always unlimited (`pkg/ui/access_roles.go`). So, as `next-processes-parity.spec.ts` does, the browser's `/api/config` answer carried `access: {role: "grain/template-author", scopes: [... "templates:write", "suites:read"]}`. Server enforcement was not exercised by this step.

## Steps and outcomes

### 1. Processes, navigation and narrowing

- **Rail.** From `/next/inbox`, the rail reads `Processes (6)`. A click goes to `/next/processes` by SPA navigation, and the tabs read `Schedules & Triggers 2 | Templates 3 | Suites & Runs 1 | Repo Workflows & Declarations 1 | Software Workflow & QA 0`. The counts followed every create and delete below without a reload, reaching `5 | 7 | 2` after the throwaway deletes. The QA count read 3 after first load.
- **Narrowing.**
  - `/next/processes?kind=templates&q=release` lands on the Templates list with search `release`, and survives a reload. (`?kind=` is the classic parameter; `/next/` keeps it only as far as the tab.)
  - `/next/processes/schedules?q=weekly` deep-links to a single row.
  - From there, the Templates tab keeps `?q=weekly`. Typing `walk` replaces it: `/processes/templates?q=walk`. Back goes to `/processes/schedules?q=weekly` with the box reading `weekly`. Forward and reload go back to `?q=walk`.
- **Sort.** The options are `Custom order`, `Next run`, `Last run`, `Name` and `Kind`. `Name` writes `?sort=name`.
  - Custom order moves a focused row with `Alt+ArrowDown`, and a pointer drag moves row 1 to row 4. Both persist through a reload.
- **Legacy links.** `/next/schedules`, `/next/templates`, `/next/suites`, `/next/suites/suite-1`, `/next/templates/template-1` and `/next/suites/runs/1` each land on the matching Processes view or item.
- **Derived run times.**
  - `security-audit` read `on demand` until a schedule fired it, then `next: Nov 20, 2026, 06:43:18 PM UTC`, reached by SPA navigation.
  - `Release Qualification Suite` went from `on demand` to `next: Nov 21, 2026, 09:00:00 AM UTC` once a suite schedule was saved.
  - A suite started by hand reads `last: …12:47:48 PM UTC · on demand`.

### 2. New menu

- The header's `New ▾` lists `Schedule`, `Template`, `Suite`, then the hint `Workflow schedules and triggers are declared in a repository's .grain/, not created here.`
- Narrowed to Templates, the header shows `Launch from template` and `+ New template`. Narrowed to Suites, it shows `+ New suite`, which opens that kind directly.
- **Keyboard (bug 4, fixed):** Enter on `New ▾` opened the menu but left focus on the button, and the arrow keys did nothing.

### 3. Schedules

- **Inline prompt schedule `Walk prompt schedule`:**
  - Settings: Keep running on, Admission `Bypass queue`, Hosting `Interactive console`, Network `Egress (outbound internet only)`.
  - The menus offer Admission `Standard / Bypass queue / Durable`, Hosting `Unattended / Interactive console`, and Network `Deployment default / Egress / None`.
  - The model picker lists only the demo connection's `gemini-2.5-pro` and `gemini-2.5-flash`. Typing `flash` narrowed it to one.
  - **Every N min:** `5` was refused in the open dialog with `recurrence: everyNMinutes must be at least 10`, and `10` saved it as `sched-3`.
  - **Draft preview:** `model gemini-2.5-flash schedule … network open schedule … keepRunning on schedule admission bypass schedule hosting console schedule capabilities https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"} repo customFields priority: medium schedule`.
  - **The saved pane** lists `Model gemini-2.5-flash`, `Admission Bypass queue`, `Hosting Interactive console`, `Keep running on` and `Network Egress (outbound internet only)`.
- **Run now:**
  - Filed `#24`. `Last run` updated in place, and `Recent fired tasks & skips` shows `#24 · Walk prompt schedule queued`.
  - On `sched-1`, Run now filed `#26` with the snackbar `Triggered schedule — filed task #26. Open task`. A second Run now was refused (`409`) with `…set "If the last run has not finished" to start it anyway` — a label `/next/` does not have (bug 1, fixed).
  - The `#26` link opens `/next/task/26`.
- **Pause and resume:** Pause gives `Paused`, the `next:` is dropped and the button reads `Resume`, and this survives a reload. Resume brings back `Enabled` and `next:`.
- **Unsaved edits:**
  - Edit, then change Base, then Escape asks `Discard unsaved changes? You have unsaved changes to this schedule. Leave and discard them?` once. Discard closes. The dialog's Cancel asks the same.
  - Back with a dirty edit asks once. **Bug 2 (fixed):** after Cancel, a second Back skipped a page and, on the first page of a visit, raised the browser's own "leave site" prompt.
  - A full-document Back raises the browser's `beforeunload` once.
- **Edit:** title `Walk prompt schedule (edited)` and `Allow overlap`. The row and pane followed without a reload (`overlap: allow`).
- **Fired tasks after a rename:** after the rename, earlier fired tasks drop out of `Recent fired tasks`. This was recorded on 2026-11-18 (matched by title) and is still so.
- **Template target (`security-audit`, every 6 hr, `sched-5`):**
  - The dialog reads `Set by template security-audit — repository and base branch come from its binding. Admission, hosting, grants, reads, writes, network, custom fields and keep running come from the selected template, and stay in sync with it.`
  - Model `The template's model`.
- **Suite target (`Release Qualification Suite`, daily 09:00, `sched-6`):** the preview reads `Run 2× acme/widgets @ main …`.
- **`git_ref`:**
  - Watching `grain/config-ops` is refused with `…grain/config-ops not found: only grain/config exists`.
  - A bare `offlist/upstream` in the ref field is refused with `git_ref trigger needs a full ref: …`.
  - Watched repository `offlist/upstream` with `refs/heads/main` saved as `sched-4`. Its row reads `Suppressed Walk gitref Trigger On git ref: offlist/upstream @ refs/heads/main · acme/widgets · next: suppressed · overlap: skip`, and its detail line reads `Suppressed — watched repository not targeted: watches offlist/upstream, which is not on this deployment's target repo list`.
  - The `Suppressed` chip narrows to it. Pause works on it: `Paused Walk gitref … Resume`.
- **Unserved model:** the pane reads `Model legacy-model-x`. In the dialog the button reads `Model: legacy-model-x (not advertised by any connection)` with `Use default`, and the list keeps `legacy-model-x` above the served models.
- **Delete:**
  - `Delete schedule? Remove schedule "Walk gitref" permanently?`: Cancel kept it, and Delete removed it (gone after a reload).
  - `sched-3` was deleted the same way.

### 4. Templates

- **New template `walk-full`:**
  - Fields: title `walk: bump {{pkg}}`, repository `acme/widgets`, parameter `pkg` (Required), Keep running, Interactive, Admission `Durable`, Hosting `Interactive console`, Model `gemini-2.5-pro` (typed `pro`).
  - Task priority: `2.5`, `0` and `high` each show `Use a whole number, 1 or more.` with `aria-invalid=true` and Save disabled. `1e3` reads back as `1000`.
  - **Grants:**
    - The API scope picker lists the 20 Grain API groups (`api:grain/ci-read` … `api:ops/triage`).
    - Added `api:grain/external-read@acme/widgets`, plus a deliberately long `…api#ci-read@acme/widgets-with-a-very-long-repository-name-to-wrap`.
    - No chip overran the dialog, and `scrollWidth` was `1440`.
    - The demo has no capability policy, so no verdict badge showed. Verdicts are covered by `next-capability-policy-pickers.spec.js`.
  - **Pane:** `Model gemini-2.5-pro · Priority 1000 · Interactive on`.
  - **Composition:** `keepRunning on template admission durable template hosting console template priority 1000 template capabilities …`.
- **New task from this template:**
  - On `walk-full` (it has a field), it opens `Launch task from template` instead, with `pkg (required)`, `Files against acme/widgets — this template is bound to it.` and the interactive note `This template files a live chat…`.
  - On `release-qualification`, after editing its priority to `7`, it opens New task with `Task priority default 7, from the template`.
- **Launch:** `pkg = lodash` filed `#27` and opened `/next/task/27`. The task reads `{title: "walk: bump lodash", priority: 1000, admission: durable, hosting: console, keepRunning: true, interactive: true, model: gemini-2.5-pro}` with both grants.
- **Rename:** `walk-inline` became `walk-inline-renamed`. The suite pane reads `Member templates (2) 1. walk-inline-renamed`, reached by SPA navigation without a reload.
- **Footer at phone width (bug 3, fixed):** the template edit footer was 88 px tall, with `Save template` alone on a second row.
- **Dependency-aware delete:** `Delete template? Delete template "release-qualification"?` was refused in the page with `template is used by 1 schedule(s) (…) and 2 suite(s) ("Release Qualification Suite", "Walk suite"); repoint or delete the schedules and remove it from the suites first`. The throwaway `walk-inline-*` templates were deleted once their suite was gone.

### 5. Suites and suite runs

- **New suite `Walk suite`:**
  - The newest template (`walk-full`) starts selected, and its `✓` option unselects it.
  - `+ New template` stacks the template dialog. Saving `walk-inline` adds it as a member and keeps the members already chosen (checked again with three members plus a fourth created inline).
  - Settings: `Until clean`, max `2`, approval on. The preview reads `Until clean (max 2 passes) … approval waits for approval suite`.
  - Adding `walk-full` (required `pkg`, no default) is refused on save with `template "walk-full": field "pkg" is required and has no default, so a suite cannot fill it -- give it a default or untick Required`.
- **Role without `suites:write` (stub 4):**
  - `+ New suite`, `Run suite` and `Delete` are disabled.
  - In `?edit=1`, `Save suite` is disabled with the tip `Your role grain/template-author does not hold suites:write, which this needs. Ask an admin for a role that includes it.`, and `+ New template` stays enabled in place.
- **Run from the suite pane** (`acme/widgets` and `main`, prefilled) went to `/next/processes/suites/runs/1`. It reads `Active Walk suite · Run #1 · Target: acme/widgets @ main · Until clean (max 2 passes) · currently on pass 1 · Pass 1 (2 tasks) #28 · walk: inline check proposed #29 · … proposed`. `#28` opened `/next/task/28`, and Back returned to the run.
- **The header's generic `Run suite`** (`proc-run-suite-btn`) opens with `Release Qualification Suite`. A blank Base keeps `Start suite run` disabled. `main` started run `#2`.
- **The Suites & Runs tab** reads `Suites (3) | Suite runs (2)`.
- **Cancel suite run:** `Cancel active suite run? Stop and close all open pass tasks in suite run #2 (acme/widgets)?` → `Stop suite tasks`.
  - The tasks read `closed` at once. Without a reload, the row and pane turned `Failed` within about 15 s: `ended on pass 1 · pass 1 had a task that failed, or closed without completing`.
  - A cancelled run is shown as `failed` because `SuiteRunStatus` has no cancelled state. This is recorded, not changed.
- **Phone:** at `390×844` nothing in the run pane or the template-schedule pane ran past the viewport, and the `Active` status chip measured `69×24` with no clipping (`scrollWidth == clientWidth`).
- **Base-less runs:** every way the UI or the server offers to start a run requires a base. So the `default branch` presentation is reachable only through old data, and `next-processes-unsaved-and-runs.spec.ts` covers it (passes).
- **Delete:** `Delete suite? Delete suite "Walk suite"? Past suite runs keep their snapshots.` → deleted.

### 6. Repository declarations

- **Refused files:** the first push used `title:` in two schedules, and its workflow had no `on: schedule`. The Declarations tab shows `.grain declarations: enabled · sync error` and names each refused file and reason. A corrected push synced on the next 2-minute poll: `Synced commit: 569054836334`, with every file listed under `Discovered .grain/ files`.
- **Declared schedule `decl-nightly`:**
  - Row: `Enabled decl-nightly .grain Every 6 hr · acme/widgets`.
  - Pane: `Declared in acme/widgets at .grain/schedules/decl-nightly.yml. Update that file in git to modify this schedule.` Configuration reads `Source acme/widgets … Admission Durable · Hosting Interactive console · Keep running on`.
  - Pause is disabled with the tip `Declared in … — edit in repository.`. There is no Edit or Delete.
  - The composition ends with the server's note that a declaration asking for `admission: durable` files for a person's approval until its set is accepted.
- **Workflow schedule (`decl-ci`):**
  - Row: `Enabled Review last night's "nightly-ci" workflow run .grain Every 12 hr`. There is no `System` chip.
  - Run now's tip reads `Starts a CI run of the declared workflow straight away, to be reviewed when it finishes.` Pause is disabled.
  - The workflow's own `on: schedule` cron adds a second row with the same title (`Daily at 04:00`). Only the cadence tells the two apart.
- **Template `decl-bump`:**
  - Row: `Repo-declared decl-bump decl: bump deps · acme/widgets (.grain/templates/decl-bump.yml)`.
  - Banner: `Repo-declared read-only template from acme/widgets (.grain/templates/decl-bump.yml).` The only action is `Launch task`: no Edit, no Delete, no GrantsPicker.
  - `?edit=1` opens nothing.
  - `DELETE` is refused with `…is declared by acme/widgets in .grain/templates/decl-bump.yml, so it is not this deployment's to delete -- change the file there…`.
- **Suite `decl-sweep`:** the pane reads `Declared in acme/widgets (.grain/suites/decl-sweep.yml).` The only action is `Run suite`, and `?edit=1` opens nothing.
- **Trigger `docs-guard.yml`:**
  - Row: `Declared docs-guard.yml Trigger .grain acme/widgets · .grain/triggers/docs-guard.yml · next: on change`.
  - The pane shows the declared YAML. `Open repository settings` links to `/next/repos/acme/widgets/advanced`.

## Bugs found

| # | Bug | Reproduction | Fix | Coverage |
| --- | --- | --- | --- | --- |
| 1 | Run now on a skip-overlap schedule with an open run is refused with "set \"If the last run has not finished\" to start it anyway". Only the classic page has that label; on `/next/` the setting is the Overlap policy's `Allow overlap` chip. | `/next/processes/schedules/sched-1` → Run now twice → read the second message. | `scheduleBusyError` (`pkg/ui/schedules.go`) names both: `set its overlap policy to "Allow overlap" ("If the last run has not finished" on the classic page)`. The workflow-schedule wording is unchanged. | `pkg/ui/schedule_busy_error_test.go` |
| 2 | Cancelling the `Discard unsaved changes?` question that Back raises restored the address bar but left the history index behind. The next Back skipped a page, and on the first page of a visit raised the browser's leave-site prompt. | Load `/next/processes/schedules/sched-1`, click the `sched-2` row, Edit, change Base, `history.back()`, Cancel, `history.back()` again: no in-app question; `beforeunload` instead. | `withRouterConfig({canceledNavigationResolution: "computed"})` in `ui-ng/src/app/app.config.ts`. This also covers the repository pages' leave guards. | `ui-ng/e2e/next-processes-back-and-footer.spec.ts` (failed before the fix, passes after) |
| 3 | At phone width the template edit dialog's footer (`New task from this template`, Cancel, Save template) wrapped to two rows, 88 px tall. | `390×844`, `/next/processes/templates/template-1` → Edit → measure the footer buttons. | Below 600px the button shows `New task` and keeps `aria-label="New task from this template"`. | same spec (failed before, passes after) |
| 4 | The header `New ▾` menu, opened from the keyboard, left focus on New. The arrow keys did nothing, so the menu items could be reached only by Tab. | Focus `New ▾` → Enter → ArrowDown: focus stays on New. | A keyboard-opened menu focuses its first item. Arrow keys, Home and End move through the items, and Escape gives focus back to New. | same spec |

Recorded, not changed:

- **Template Reads, Writes, Network and Custom fields on `/next/`.** The dialog has none of them (classic `TemplateOverlay.jsx` has), and the schedule dialog's note says a schedule takes them from its template. Filed as a follow-up.
- **A cancelled suite run reads `Failed`.** Cancel closes the pass's tasks, and the reconciler then marks the run failed with `pass 1 had a task that failed…`. A distinct cancelled state needs a model change.
- **Fired tasks matched by title** (from 2026-11-18): still so.
- **Two workflow-review rows with one title** when a workflow schedule also has its own `on: schedule` cron.

## Tests run

- `go test ./pkg/ui -run 'TestScheduleBusyError|TestRunScheduleNowWorkflow'`: pass.
- `npx vitest run src/app/processes` in `ui-ng/`: 83 passed.
- `npx playwright test` in `ui-ng/` over `grain demo` built from this branch, across the desktop, phone and dark projects:
  - `e2e/next-processes-back-and-footer.spec.ts`, `next-processes-parity.spec.ts`, `next-processes-narrowing.spec.ts`, `next-processes-forms.spec.ts` and `next-processes-walkthrough.spec.ts`: 43 passed.
  - Earlier in the run: `next-processes-unsaved-and-runs.spec.ts`, `next-processes-run-times.spec.ts`, `next-template-priority-interactive.spec.ts` and `next-schedule-bound-target.spec.ts` passed too (51 passed, before the New-menu change).
- No classic (`ui/`) code changed. No screenshots or browser artifacts are committed.
