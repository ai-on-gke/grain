# Recurring and Composed-Work Automation UI Walkthrough (2026-11-19)

- **Date:** 2026-11-19 (UTC), 03:03–04:16.
- **Task:** [Task #7307](/tasks/7307), branch `grain/53549e42/task-7307-walk-through-processes`, base `main`.
- **Commit tested:** `17339dd82` for the initial live walkthrough. All four fixes on this branch were then verified against a fresh build (`/var/tmp/gw/grain-after`) and through the full `/next/` Processes Playwright suite.
- **Environment:**
  - Debian Linux sandbox (`linux/amd64`), Go `1.26.10`, Node.js `v20.19.0`.
  - Built `ui/`, `ui-ng/`, and `ui-hub/` into `pkg/ui/static`, `pkg/ui/static-next`, and `pkg/hub/server/static`, then compiled `go build -tags grain_e2e -o /var/tmp/gw/grain ./cmd/grain` (and `/var/tmp/gw/grain-after` after fixes).
  - Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file <file> -schedules -github-sim -fake-ci -scheduled-workflows -reconcile 500ms` over a throwaway `TMPDIR` (embedded SQLite store). Signed in via `POST /api/auth/access-code` as `alice@example.com`.
  - Browser: headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), driven by Playwright scripts kept outside the repository in `/var/tmp/gw/`. Timezone `UTC`, tested at `1440×900` (desktop) and `390×844` (phone, touch), in light and dark themes.
  - `/home/debian/.gemini-api-key` was absent in this sandbox, so no live model was invoked and `LOOK=1` was unavailable. Layout, alignment, chip containment, and horizontal overflow (`scrollWidth <= clientWidth`) were checked via DOM bounding boxes alongside `before`/`after` contact sheets (`ui/e2e/screenshot.mjs`, kept outside the repo).
- **Console and network:** zero `pageerror`s and zero unexpected `5xx` responses across the walkthrough. Every `4xx` response came from a deliberate negative test: `400` for the 5-minute schedule, `409` for `Run now` on an `overlap: skip` schedule with an open task, `400` for `grain/config-ops` as a watched `git_ref` repository, `400` for wildcard `refs/tags/v*` on `git_ref`, `400` for setting `hosting` directly on a template-backed schedule, `400` for deleting a template still referenced by a schedule and suite, and `400` for PATCH/DELETE on a repo-declared template.

## Which UI each step ran against

`ui-ng/src/app/shell/nav.ts` routes Processes to `/next/processes`, and `ui-ng/src/app/app.routes.ts` handles `/next/processes[/:tab[/:itemId]]`, `/next/processes/suites/runs/:runId`, and the legacy `/next/schedules[/:id]`, `/next/templates[/:id]`, and `/next/suites[/runs][/:id]` redirects.

Every walkthrough step ran against `/next/processes` (including the newly landed parity controls from commit `4f8feff15`). Classic `/processes` was inspected once for comparison only (confirming its single-table `All` view and kind chips `All 23 | Schedules 9 | Workflow schedules 2 | Templates 8 | Suites 3 | Triggers 1`), and no classic `ui/` files were modified.

Intentional `/next/` design differences documented in `design/design/ui-design-book.design.md`:

- `/next/processes` uses section tabs (`Schedules & Triggers`, `Templates`, `Suites & Runs`, `Repo Workflows & Declarations`, `Software Workflow & QA`) with per-tab filter chips rather than one mixed `All` table with kind chips.
- `Templates` and `Suites & Runs` open `+ New template` / `+ New suite` directly in the header; `Schedules & Triggers`, `Repo Workflows & Declarations`, and `Software Workflow & QA` keep the `New ▾` menu.
- Custom-order reordering in `/next/processes` uses HTML5 drag handles (`li.proc-li[draggable="true"]`) and keyboard `Alt+ArrowUp` / `Alt+ArrowDown` on row buttons; there are no separate tap Up/Down move buttons on phone width.

## Stubs and seeded states

Every create, edit, pause/resume, run, cancel, and delete gesture—and all rendering—went through the real browser UI and the real `grain demo` server. Only these external/unavailable boundaries were stubbed or seeded:

1. **Agent execution & CI:** `grain demo` files real tasks and runs the real schedule, suite, declaration, and scheduled-workflow reconcilers (`-schedules -fake-ci -scheduled-workflows -reconcile 500ms`), without launching sandboxed agents; `-fake-ci` accepts workflow runs and `POST /api/e2e/fake-ci/runs/{id}/finish` settled `fake-ci-2` as `success`.
2. **Repository declarations:** `.grain/` files were committed and pushed with `git` directly to the simulated bare repository reported by `GET /api/e2e/scheduled-workflows` (`acme/widgets.git`): first an invalid set (to verify sync errors), then a valid set containing `.grain/templates/decl-bump.yml`, `.grain/suites/decl-sweep.yml`, `.grain/schedules/decl-nightly.yml`, `.grain/workflows/decl-smoke.yml`, `.grain/schedules/decl-wf-candidate.yml`, and `.grain/triggers/docs-guard.yml`. Declarations were toggled on via the UI switch on `Repo Workflows & Declarations`.
3. **Unreachable/synthetic run & role states:**
   - Because the server refuses blank `base` on new suite runs (`base is required`), a base-less completed multi-pass run (`#901`, `base: ""`, `scheduleId: "sched-6"`, two passes) was inspected by intercepting `GET /api/suite-runs` and `GET /api/suite-runs/901` in Playwright, then unrouting.
   - A held skip entry (`taskId: "24"`, `outcome: "held"`) on `sched-1` was injected via Playwright route interception on `GET /api/schedules` to verify the held/suppressed task link navigation, then unrouted.
   - A role lacking `suites:write` (while holding `templates:write`) was exercised by overriding `access` on `GET /api/config` in Playwright, then unrouted.
   - An unserved model schedule (`model: "retired-phantom-99"`) was seeded via `POST /api/schedules` to inspect how the detail pane and edit model picker render a model no enabled connection advertises, then deleted via the UI.

## Steps and outcomes

### 1. Processes navigation, rail badge, narrowing, sort, reorder, and legacy links (`/next/`)

- **Rail badge & SPA navigation:** Starting on `/next/inbox`, the rail `Processes` destination showed its neutral badge (`6` initially). Clicking it SPA-navigated to `/next/processes` and refreshed `GET /api/templates`, `/api/schedules`, `/api/suite-runs`, `/api/declarations`, `/api/suites`, `/api/qa/flows`, `/api/settings`, `/api/repos/acme/widgets/declarations`, `/api/qa/flow-versions`, and `/api/schedules/sched-1/composition`.
  - **Bug #1 found:** Once `acme/widgets` had a synced trigger (`.grain/triggers/docs-guard.yml`), a fresh browser load of `/next/inbox` showed `22` on the rail badge instead of `23`, jumping to `23` only after clicking `Processes`. Fixed on this branch so `ProcessesStore` reads configured repos from `AccessService.rawConfig()` and runs deduped `loadDeclarationStatuses()` off-page.
- **Tabs & filter chips:** Tabs read `Schedules & Triggers 2 | Templates 3 | Suites & Runs 1 | Repo Workflows & Declarations 1 | Software Workflow & QA 3` initially and updated live without reload across every create/delete. Schedules status chips: `All`, `Active`, `Paused`, `Event triggers`, `Suppressed`.
- **Search (`?q=`), `?kind=`, `?sort=`, reload, and Back/Forward:**
  - Typing `Weekly` into `Filter this view` updated the URL to `?q=Weekly`, filtered to `sched-2`, survived reload, and preserved `?q=Weekly&sort=name` across tab switches and Back/Forward.
  - Sort options: `Custom order`, `Next run`, `Last run`, `Name`, `Kind` on Schedules & Templates; `Custom order`, `Next run`, `Last run`, `Name` on Suites (`Kind` drops back to `Custom order` on Suites).
  - **Bug #4 found:** Visiting `/next/processes?kind=triggers` landed on `Schedules & Triggers` with `All` active (listing every schedule instead of narrowing to triggers). Fixed on this branch so `?kind=triggers` / `?kind=trigger` activates `Event triggers` on arrival and restores `All` when navigating back away from `?kind=triggers`.
- **Custom-order reorder:** Pressing `Alt+ArrowDown` on `sched-row-sched-1` moved it below `sched-2`, announced `"Moved to position 2 of 2."` in `proc-move-note`, and persisted across reload (`localStorage`). HTML5 `dragTo` on `li.proc-li` moved it back. Keyboard `Alt+ArrowDown`/`Alt+ArrowUp` also reordered rows on Templates and Suites.
- **Derived run times:** `release-qualification` showed `next: Nov 20, 2026, 12:00:00 AM UTC`; `security-audit` and `Release Qualification Suite` showed `on demand` until schedules targeting them were saved, at which point both updated to `next: …` without reload.
- **Legacy links:** `/next/schedules`, `/next/schedules/sched-1`, `/next/templates`, `/next/templates/template-1`, `/next/suites`, `/next/suites/suite-1`, and `/next/suites/runs/999` all resolved to their matching `/next/processes/...` views (with `No suite run "999".` banner for the missing ID).
- **New menu & narrowed direct buttons:** On `Schedules & Triggers`, `New ▾` opened `Schedule`, `Template`, `Suite`, plus the hint `"Workflow schedules and triggers are declared in a repository's .grain/, not created here."`. On `Templates` the header button became `+ New template` directly; on `Suites & Runs` it became `+ New suite` directly.

### 2. Schedules (`/next/`)

- **Raw task-filing schedule (`sched-4`):**
  - `Every N min` = `5` was refused in-modal with `recurrence: everyNMinutes must be at least 10`. Changing to `10` allowed saving.
  - Exercised Admission (`Bypass queue`), Hosting (`Interactive console`), Keep running (`on`), Network (`None (this deployment only)`), Reads (`acme/docs`), Writes (`acme/widgets @ release/1.0`), all 6 declared Custom fields (`priority: high`, multi-enum `area: [backend, frontend]`, `requested-by: QA-Walkthrough`, `due-date: 2026-12-01`, `estimate: 3`, `customer-facing: Yes`), GrantsPicker (`Grain API` group `api:grain/ci-read` scoped to `acme/widgets`, plus verified `Capability method` tab showing `cloud#cloud-key`), and searchable model picker (only `gemini-2.5-pro` and `gemini-2.5-flash` from the enabled connection; searching `flash` narrowed to `gemini-2.5-flash`).
  - **Capability policy verdicts:** Saving a `deny` rule for `https://grain.dev/capabilities/api#grain/ci-read` on `/next/settings/capabilities#capability-policy` caused `sched-grants` in the Schedule edit dialog to badge `grain/ci-read: acme/widgets` with `denied by policy` and display the explanatory note linking back to `/next/settings/capabilities#capability-policy`.
  - **Composition preview & containment:** Both draft and saved composition previews displayed `model gemini-2.5-flash`, `admission bypass`, `hosting console`, `keepRunning on`, `network none`, `reads`, `writes`, `customFields`, and long capability grant chips with zero overflow at `1440×900` and `390×844` (`scrollWidth === clientWidth`).
  - **Pause / Resume / Reload:** Pausing set the row to `Paused`, dropped `next:`, survived reload; resuming restored `Enabled` and `next:`.
  - **Edit, change-only PATCH, and unsaved-edit guard:** Reopening `sched-4` restored every field. Dirtying `Base` and pressing `Escape` showed `Discard unsaved changes?` once; clicking `Cancel` kept the dialog open; browser Back while dirty also prompted once. Saving a title-only change sent a change-only PATCH omitting unchanged `reads`, `writes`, `networkAccess`, `customFields`, `admission`, `hosting`, `keepRunning`, and `capabilities`.
  - **Overlap `skip` vs `allow` and Run now:** First `Run now` filed `#24` and updated `Last run`. A second `Run now` while `overlap: skip` and `#24` remained open surfaced the server's `409 Conflict` explanation in the page banner. Switching overlap to `Allow overlap` and clicking `Run now` filed `#25` and listed `#25 · Walk raw task schedule (edited)` under `Recent fired tasks & skips`; clicking it navigated to `/next/task/25` and Back returned to `/next/processes/schedules/sched-4`. Following a held skip link (`#24 → held`) also navigated cleanly to `/next/task/24` and back.
- **Template-backed & suite-running schedules:**
  - Created template-backed schedules for `security-audit` (`Every 6 hr`) and `walk-full-renamed` (`Daily`), confirming the template dialog note (`Admission, hosting, grants, reads, writes, network, custom fields and keep running come from the selected template, and stay in sync with it.`), composition previews carrying `priority 1000`, `admission durable`, `hosting console`, and `keepRunning on`, and server refusal (`400`) when attempting to pass `hosting` directly on a template-backed schedule.
  - Created a suite-running schedule for `Release Qualification Suite` (`Daily`), updating the suite row's `next:` live.
- **`git_ref` branch & tag triggers:**
  - Watching `grain/config-ops` was refused in-modal (`recurrence.gitRef.repo: grain/config-ops not found: only grain/config exists`).
  - Off-target `offlist/upstream @ refs/heads/main` saved as `sched-7` with `Suppressed` warning chip, detail banner (`Suppressed — watched repository not targeted`), `next: suppressed`, and narrowed under the `Suppressed` chip. Pausing it worked cleanly (`Paused`, `enabled: false`), and resuming restored `Suppressed`.
  - Wildcard `refs/tags/v*` was refused in-modal (`needs a full ref: such as refs/tags/latest or refs/heads/main`); literal `refs/tags/v1.0.0` on `acme/widgets` saved cleanly and appeared under `Event triggers` with `next: on ref move`.
- **Unserved model & delete:** Seeding `sched-9` with `model: "retired-phantom-99"` rendered `retired-phantom-99` in the Configuration/Composition panes and labelled it `not advertised by any connection` in the edit model picker trigger and popover. Deleting `sched-9` prompted `Delete schedule?`, removed the row without reload, and decremented the rail badge (`12 -> 11`).

### 3. Templates (`/next/`)

- **Fully populated template (`walk-full`):**
  - Task priority rejected `2.5`, `0`, `-3`, and `high` (`Use a whole number, 1 or more.`, `aria-invalid="true"`, Save disabled); `1e3` normalized to `1000` on blur.
  - Configured Admission `Durable`, Hosting `Interactive console`, Keep running `on`, Interactive `on`, searchable model picker (`pro` → `gemini-2.5-pro`), parameter `pkg` (default `lodash`, `required: true`), and `api:grain/ci-read` grant (verified `denied by policy` verdict on `tmpl-grants` under the temporary deny policy).
- **Filing from templates:**
  - Parameterized interactive `walk-full`: clicking `New task from this template` routed to `launch-template-dialog` (not `new-task-sheet`), showed `"This template files a live chat: the agent can talk with you while it works, and the chat opens as soon as the task is filed."`, hid the Approved switch, and filed `#27` opening `/next/task/27`.
  - Non-parameterized `walk-preserve-check` (`priority: 42`): clicking `New task from this template` opened `new-task-sheet` with `nt-priority` prefilled to `42`.
  - Unbound template with `Reads: ["acme/widgets", "acme/docs"]` launched targeting `acme/widgets`: filed `#28` with `reads: ["acme/docs"]` (dropping the chosen target repo rather than failing).
  - **Bug #3 found:** The `/next/` Template dialog had no `Base`, `Network access`, `Reads`, `Writes`, or `Custom fields` inputs (so creating an unbound template with `Reads` required API or classic), and `tmpl-config-pane` omitted `Admission`, `Hosting`, `Keep running`, `Network`, `Reads`, and `Writes`. Fixed on this branch (with `Base` disabled until a bound repo is set, change-only PATCH, unsaved-edit snapshot, and full E2E verification of creating an unbound template with `Reads` in `/next/` and launching it).
- **Edit, rename, phone footer, composition, and dependency-aware delete:**
  - At `390×844` phone width, `walk-full`'s edit dialog footer buttons (`New task from this template`, `Cancel`, `Save template`) stayed one line tall (`h <= 40px`) and non-overlapping, and long grant chips in `g-composition-view` stayed inside the pane (`scrollWidth: 390`).
  - Renaming `walk-full` → `walk-full-renamed` refreshed referencing suites' member lists and composition previews without reload.
  - Attempting to delete `security-audit` asked for confirmation and displayed the server's dependency-aware refusal naming both the schedule and suite referencing it; the template remained.

### 4. Suites and suite runs (`/next/`)

- **Suite creation & editing (`suite-2`, `Walk composed suite`):**
  - Used the searchable `suite-templates-combobox` (`walk-full`), created `walk-inline-tmpl` inline via `+ New template` (stacking `template-dialog` over an `inert` `suite-dialog` and auto-selecting the new template), tested `Until clean` (`maxPasses: 2`), `Require approval`, `Auto-merge`, and `Preview composition`.
  - **Role gating:** With `access` lacking `suites:write` while holding `templates:write`, `Save suite` disabled while `+ New template` stayed enabled in place.
  - **Delete:** Created and deleted a throwaway suite (`Walk throwaway suite`) with confirmation (`Delete suite? ... Past suite runs keep their snapshots.`).
- **Suite runs:**
  - Started Run `#1` (`Walk composed suite`) from the suite pane (`suite-open-run-dialog-btn`); verified blank `Base` disables `Start suite run`, navigated to `/next/processes/suites/runs/1`, inspected Pass 1 (`#29`, `#30`, `#31`, `proposed`), followed `#29` to `/next/task/29` and returned with Back.
  - Started Run `#2` (`Release Qualification Suite`) from the generic header `Run suite` (`proc-run-suite-btn`) → `/next/processes/suites/runs/2`.
  - Cancelled Run `#2` via `Cancel suite run` (`Stop suite tasks`), closing `#32` and `#33`; the reconciler transitioned Run `#2` to `Failed` with error banner `"pass 1 had a task that failed, or closed without completing"`.
  - At `390×844` phone width, `Failed` (`67×24`) and `Active` (`69×24`) status chips stayed whole on single lines with zero horizontal overflow (`scrollWidth: 390`).
  - Verified base-less multi-pass scheduled run (`#901`, stubbed) renders `acme/widgets @ default branch` on both row and pane, `Started by schedule sched-6`, Pass 1 (2 tasks, `PR opened`) and Pass 2 (1 task).
  - **Bug #2 found:** Searching `?q=zzzz` on Suites, Suite runs, and Declarations showed `"No suites configured yet."`, `"No suite runs recorded yet."`, and a blank list respectively even when items existed. Fixed on this branch to show `"No suites match this filter."`, `"No suite runs match this filter."`, and `"No repositories match this filter."`.

### 5. Repository declarations (`/next/`)

- **Invalid push (`48b459ae25d0`):** Enabling `.grain/` declarations on `acme/widgets` via the UI switch displayed `Sync / validation error` listing all three malformed files with exact YAML/reference explanations.
- **Valid push (`f7f20f7d62ca`):** Synced all 6 `.grain/` files cleanly (`Synced commit: f7f20f7d62ca`):
  - **Template (`decl-bump`):** `Repo-declared` badge, `acme/widgets (.grain/templates/decl-bump.yml)`, read-only banner, no Edit/Delete/GrantsPicker, `?edit=1` refused to open dialog, server PATCH/DELETE refused with repository/file instructions, composition preview showed `priority 7`, `admission durable`, `hosting console`, `keepRunning on`, and declared capability hold note.
  - **Suite (`decl-sweep`):** `.grain` chip, `Declared in acme/widgets (.grain/suites/decl-sweep.yml)`, no Edit/Delete, `?edit=1` refused.
  - **Task schedule (`decl-nightly`):** `.grain` chip, `Declared in acme/widgets at .grain/schedules/decl-nightly.yml`, Pause disabled, no Edit/Delete, `?edit=1` refused.
  - **Workflow schedules (`decl-smoke` & `decl-wf-candidate`):** Neither carried a `System` chip; `Run now` opened `"Run workflow schedule now? ... It starts a CI run of the workflow it was declared in straight away, to be reviewed when it finishes; the schedule's own next run is unchanged."`. Confirming `Run now` on `decl-wf-candidate` started `-fake-ci` run `fake-ci-2` on `candidate`, which settled cleanly via `POST /api/e2e/fake-ci/runs/fake-ci-2/finish`.
  - **Trigger (`docs-guard.yml`):** Listed on `Schedules & Triggers` (`Declared docs-guard.yml Trigger .grain acme/widgets · .grain/triggers/docs-guard.yml · next: on change`), inspected inline with YAML preview, and `Open repository settings` navigated to `/next/repos/acme/widgets/advanced` showing `docs-guard.yml`.
  - **Inline YAML & workflow editor:** `View YAML` on `Declarations` expanded inline (`decl-inline-yaml`) cleanly; `Add workflow YAML` opened and dismissed on Escape.

### 6. Dark mode & phone width audit

- Walked 13 `/next/processes` routes (list views, detail panes, declared items, suite runs, declarations, and `?new=1` schedule/template/suite dialogs) at `390×844` in dark mode: `0` console/page errors, `0px` horizontal overflow (`overflowX: 0`), `0` dialog overflows, and `0` clipped controls.

## Bugs found

| # | Bug | Repro | Fix | Coverage |
| --- | --- | --- | --- | --- |
| 1 | Off-page rail `Processes` count badge missed synced `.grain/triggers` until `/next/processes` was visited | Enable declarations with a `.grain/triggers/*.yml` file; open `/next/inbox` in a fresh context (`22`), then click `Processes` (jumps to `23`) | Read configured repos from `AccessService.rawConfig()` in `ProcessesStore.declaringRepos` and invoke deduped `loadDeclarationStatuses()` from `refresh()` off-page (`ui-ng/src/app/processes/processes.store.ts`) | `ui-ng/src/app/app.component.spec.ts`, `ui-ng/e2e/next-processes-walkthrough-fixes.spec.ts` |
| 2 | Suites, Suite runs, and Declarations empty states misreported filtered searches (`?q=`) as having no configured items (or rendered blank) | With suites/runs/repos present, visit `/next/processes/suites?q=zzzz` ("No suites configured yet."), switch to Runs ("No suite runs recorded yet."), or visit `/next/processes/declarations?q=zzzz` (blank) | Distinguish filtered empty (`"No suites match this filter."`, `"No suite runs match this filter."`, `"No repositories match this filter."`) from truly empty stores (`ui-ng/src/app/processes/processes-page.component.ts`) | `ui-ng/src/app/processes/processes-page.component.spec.ts`, `ui-ng/e2e/next-processes-walkthrough-fixes.spec.ts` |
| 3 | `/next/` Template dialog had no `Base`, `Network access`, `Reads`, `Writes`, or `Custom fields` controls (blocking unbound template `Reads` creation in `/next/`), and `tmpl-config-pane` omitted `Admission`, `Hosting`, `Keep running`, `Network`, `Reads`, and `Writes` | `/next/processes/templates` → `+ New template`: no Base/Network/Reads/Writes/Custom fields; `saveTemplateDialog` hardcoded `reads: []`, `writes: []`, `customFields: {}` | Added `Base` (disabled unless bound repo is set), `Network access`, `Reads`, `Writes`, and declared `Custom fields` to the `/next/` Template dialog with change-only PATCH and unsaved-edit snapshot; added `Admission`, `Hosting`, `Keep running`, `Network`, `Reads`, and `Writes` to `tmpl-config-pane` (`processes.models.ts`, `processes-page.component.ts`) | `ui-ng/src/app/processes/processes-page.component.spec.ts`, `ui-ng/e2e/next-processes-walkthrough-fixes.spec.ts` |
| 4 | `/next/processes?kind=triggers` did not narrow `Schedules & Triggers` to triggers on arrival | Visit `/next/processes?kind=triggers`: `All` stayed pressed and every time-based schedule rendered | Added `?kind=` effect in `ProcessesPageComponent` to activate `Event triggers` when `?kind=trigger(s)` and restore `All` when navigating away from `?kind=triggers` | `ui-ng/src/app/processes/processes-page.component.spec.ts`, `ui-ng/e2e/next-processes-walkthrough-fixes.spec.ts` |

## Tests run

- `cd ui-ng && npm run lint && npm run format:check`: clean.
- `cd ui-ng && npm test`: 76 test files, 830 tests passed (including 32 in `processes-page.component.spec.ts` and 28 in `app.component.spec.ts`).
- `go test ./tests/hygiene`: passed.
- `cd ui-ng && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/next-processes-walkthrough-fixes.spec.ts e2e/next-processes-parity.spec.ts e2e/next-processes-narrowing.spec.ts e2e/next-processes-forms.spec.ts e2e/next-processes-unsaved-and-runs.spec.ts e2e/next-processes-walkthrough.spec.ts e2e/next-processes-run-times.spec.ts e2e/next-template-priority-interactive.spec.ts e2e/next-schedule-held-detail.spec.js e2e/next-suites-and-task-actions.spec.js e2e/next-full-system.spec.js`: **70 passed** across `desktop`, `phone`, and `dark` projects.
- Screenshots: `ui/e2e/screenshot.mjs` captured `before` (base `17339dd82`) and `after` (fixed build) at desktop (`1440×900`) and phone (`390×844`) in light and dark across `/next/inbox`, `/next/processes?kind=triggers`, `/next/processes/templates/template-1`, `/next/processes/templates?new=1`, and `/next/processes/suites?q=zzzz` (kept outside the repo; attached to the closing note).
