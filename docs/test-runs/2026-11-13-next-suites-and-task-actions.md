# Walkthrough: `/next/` empty-pass suite runs, New-task blockers, and batch Close (`docs/test-plans/2026-11-13-afternoon.md` §5)

## Environment

- **Repository**: `ai-on-gke/grain`, branch `grain/098073e4/task-5487-walkthrough-next-empty-pass` ([Task #5487](/tasks/5487), filed from [Task #5366](/tasks/5366)).
- **Base commit tested**: `ab445d443` (`v0.0.0-20261114062634-ab445d443378`, auditing PR #1453 / `1529c9215` and PR #1445 / `f83f19776`, `7a24c809b`).
- **Build**: `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (embedding both Angular `ui-ng/` at `/next/` and React `ui/` at `/`).
- **Browser**: Playwright 1.62.1 headless Chromium (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`) across `390×844` (phone, with and without `hasTouch: true` / `isMobile: true`), `1024×860` (medium), `1440×900` (desktop), and `2048×1200` (wide), in `light` and `dark` themes, plus `prefers-reduced-motion: reduce`.
- **Live servers**:
  1. **Live SQLite `grain daemon -sandbox-port-base=0`** (`docs/test-runs/2026-11-13-next-suites-and-task-actions/walkthrough.mjs`): started without `-default-target-repo` (`defaultTarget: ""`), `-poll-interval 1s`, `-max-workers 2`, `-max-mergers 0`, backed by real bare Git repositories served by `ghsim.go` (`pkg/github/githubsim` + `git http-backend`) and authenticated through `tests/iap/iapsim.go` (`alice@example.com` operator, `viewer@example.com` `grain/viewer`, and `contrib@example.com` `ops/widgets-contributor` restricted to `acme/widgets`), with a thin local HTTP forwarder injecting a genuine upstream `500` on 1 of 2 `POST /api/tasks/{id}/close` calls for partial-failure testing (zero `page.route` interception on the live-daemon walk).
  2. **`grain demo` workers** (`ui-ng/e2e/fixtures.ts` and `ui/e2e/screenshot.mjs`): used for `make ui-screenshots` (`before` and `after` contact sheets), the three-perspective UI review across all four breakpoints, and `ui-ng/e2e/next-suites-and-task-actions.spec.js`.
- **Helpers**: `docs/test-runs/2026-11-13-next-suites-and-task-actions/{walkthrough.mjs,ghsim.go,fake-agy.py}`.

---

## Walkthrough items & empirical results

### 1. `/next/processes/suites` (and classic `/processes`) with `pass: 0, tasks: null` (PR #1453 / `1529c9215`)

- **Live reconciliation setup**:
  - Created template `template-1` (`Audit pass check`) and suite `Nightly audit suite` (`mode: "count"`, `count: 1`), then launched **Run #1** via `POST /api/suite-runs` (`pass: 1`, 1 queued task).
  - Seeded **Run #2** as an `active` `suite_run` row whose `suite_run_item` referenced a deleted template (`declared:template:acme:widgets:deleted-template`). Within 1 s, the daemon's live `orchestrator.SyncSuites` reconciler hit `MissingSuiteTemplateError` in `FireNextPass` and transitioned Run #2 to `status: "failed"`, `pass: 0`, `tasks: null`, `error: "pass 1 could not be filed: template declared:template:acme:widgets:deleted-template no longer exists"`.
  - Launched **Run #3** (`pass: 1`, 1 queued task) so `GET /api/suite-runs` returned Run #2 (`pass: 0, tasks: null`) ahead of subsequent Run #1 (`pass: 1`).
- **Results across `1440×900` and `390×844`, `light` and `dark`**:
  - **Uncaught page / console errors**: `0`.
  - **Horizontal overflow**: `0 px` (`scrollWidth === innerWidth` across all four viewport/theme combinations).
  - **axe-core serious/critical violations**: `0`.
  - **Runs list**: `[data-testid="suite-run-row-2"]` rendered `"Failed Nightly audit suite #2 acme/widgets @ main · 0 tasks"` with **no** `"Pass 0"` pill (`row2HasPass0: false`). Subsequent `[data-testid="suite-run-row-1"]` rendered `"Pass 1"` cleanly after Run #2 (`row1SubsequentRendered: true`).
  - **Inspector detail**: selecting Run #2 rendered `[data-testid="suite-run-inspector"]` with `"ended without filing a pass"`, the error banner `"pass 1 could not be filed: template declared:template:acme:widgets:deleted-template no longer exists"`, and `0` `[data-testid^="suite-pass-"]` cards. Navigating back (mobile `← Back` on `390×844`) and selecting subsequent Run #1 expanded its `"Pass 1"` card and task link cleanly.
  - **Classic `/processes/suites/runs/2` spot-check**: rendered `"This run ended without filing any tasks."` with no `"pass 0"` text (`hasPass0: false`).

### 2. `/next/` New-task sheet validation & viewer gating, plus classic `NewTaskOverlay` touch/hover check (PR #1445 / `f83f19776`, `7a24c809b`)

- **`/next/inbox` and `/next/tasks` (`c` shortcut, `[data-testid="rail-new-task"]` at `1440×900`, and `[data-testid="phone-new-task"]` at `390×844`)**:
  - **(a) Missing title and no template**: submitting focused `[data-testid="nt-title"]`, set `aria-invalid="true"`, displayed `[data-testid="new-task-error"]` `"Give the task a title."`, and sent `0` `POST /api/tasks` requests.
  - **(b) Missing repository with No-repo unselected (daemon without `-default-target-repo`)**:
    - **Before fix (Bug 1)**: `[data-testid="nt-repo"]` falsely read `"Deployment default target"` even though `defaultTarget` was empty, and clicking submit sent `POST /api/tasks` (`{ repo: "", noRepo: false }`), failing over the network with `400 Bad Request` (`"The task was not filed: no repo given, and this deployment has no default target repo configured"`).
    - **After fix**: `[data-testid="nt-repo"]` reads `"Select a repository…"`, and submitting blocks client-side with `"Pick a repository, or choose No repository."`, marks `[data-testid="nt-repo"]` `.is-invalid` with `aria-describedby="new-task-error"`, focuses the Repository button, and sends `0` `POST /api/tasks` requests.
  - **(c) Custom branch mode with empty branch name**: selecting `acme/widgets`, checking `[data-testid="nt-new-branch"]`, and submitting with an empty branch name focused `[data-testid="nt-new-branch-name"]`, set `aria-invalid="true"`, showed `"Name the new branch, or untick the new-branch box."`, and sent `0` network requests.
  - **(d) Clean filing once completed**: filling `feat/walk-5487-desktop` / `feat/walk-5487-phone` filed via `POST /api/tasks` (`201 Created`) and closed the sheet; opening via `[data-testid="rail-new-task"]` / `[data-testid="phone-new-task"]` and choosing `"No repository (answer in a comment)"` filed cleanly (`noRepo: true`, `201 Created`).
  - **(e) Viewer & repo-scoped `!canFile()` gating**:
    - As `viewer@example.com` (`grain/viewer`): `[data-testid="rail-new-task"]` and `[data-testid="phone-new-task"]` are `disabled` with `gTip` `"Your role grain/viewer does not hold tasks:write, which this needs. Ask an admin for a role that includes it."`, and pressing `c` does not open the sheet.
    - As `contrib@example.com` (`ops/widgets-contributor`, holding `tasks:write` on `acme/widgets` only): opening the sheet and selecting `acme/api` displays `[data-testid="new-task-denied"]` (`"Your role ops/widgets-contributor does not hold tasks:write on this repository, which this needs. Ask an admin for a role that includes it."`) and disables `[data-testid="new-task-submit"]`.
    - **Before fix (Bug 2)**: pressing `Ctrl+Enter` inside `[data-testid="nt-title"]` while `!canFile()` bypassed the disabled submit button and fired `POST /api/tasks` (`403 Forbidden`). **After fix**: `file()` guards `!this.canFile()` (`ctrlEnterBypassedCanFile: false`, `0` network calls).
- **Classic `/` (`/new` `NewTaskOverlay`) real-browser touch & hover check (`7a24c809b`)**:
  - **Mobile touch (`390×844`, `hasTouch: true`, `isMobile: true`, `page.tap('[data-create-blocked]')`)**:
    - With empty title and no template: tapping `[data-create-blocked]` opened `[role="tooltip"]` `"Give the task a title, or pick a template."` at `+250 ms`, stayed open past synthetic mouse events at `+1200 ms` (`touchStillOpenAt1200ms: true`), and auto-dismissed by `+3400 ms` (`touchAutoClosedAt3400ms: true`).
    - With title filled and no repository / `No repo` unticked: tapping `[data-create-blocked]` opened `"Pick a repo under Repos, or choose No repo."`, stayed completely inside the `390 px` viewport (`touchRepoTipFits390px: true`), and auto-dismissed after ~3 s (`touchRepoTipAutoClosed: true`).
  - **Desktop hover (`1440×900`)**: hovering `[data-create-blocked]` opened `"Give the task a title, or pick a template."`, and moving the pointer away closed it cleanly (`desktopHoverClosedOnLeave: true`).
  - **Viewer (`viewer@example.com`)**: `[data-create-blocked]` was omitted (`classicCreateBlockedCountOnViewer: 0`) and `Create task` remained disabled (`classicCreateDisabled: true`), yielding cleanly to `AccessGate` without double tooltips.

### 3. `/next/tasks` batch Close confirmation, partial-failure handling, and `task(s)` pluralization

- **Batch Close confirmation (`1440×900` and `390×844`, 1 vs 2+ live tasks)**:
  - **Before fix (Bug 3)**: `[data-testid="close-confirm"]` read `"Close 1?"` / `"Close 2?"` (missing `"task"` / `"tasks"`), pressing `Escape` failed to cancel `confirmClose()` while closing an open `?task=<id>` inspector pane on desktop (`confirmDismissedOnEscape: false`, `paneKeptOpenOnEscape: false`), and clearing selection or switching tabs left `confirmClose()` stale.
  - **After fix**:
    - 1 selected task reads `"Close 1 task? Running work stops and open pull requests are left behind; this cannot be undone."`
    - 2 selected tasks read `"Close 2 tasks? Running work stops and open pull requests are left behind; this cannot be undone."`
    - Opening confirmation moves focus to `[data-testid="close-keep"]`; pressing `Escape` or clicking `Keep` cancels `confirmClose`, leaves any open `?task=<id>` inspector pane open (`paneKeptOpenOnEscape: true`), and restores focus to `[data-testid="batch-close"]`. Confirming (`[data-testid="close-yes"]`) closes both tasks and shows `"Closed 2 tasks"` with no `Undo`.
- **Simulated partial failure (1 of 2 tasks returning HTTP 500 on `POST /api/tasks/{id}/close`)**:
  - **Before fix (Bug 4 — P0 dishonest snackbar & selection loss)**: when `#19` closed (`200`) and `#20` failed (`500`), `/next/tasks` displayed `[data-testid="action-error"]` `"1 of 2 did not go through: simulated upstream close failure"`, simultaneously showed snackbar **`"Closed 2 tasks Undo"`**, unchecked the failed task `#20`, and wired `Undo` to reopen both `#19` and `#20`.
  - **After fix**: `[data-testid="action-error"]` reads `"1 of 2 tasks failed: simulated upstream close failure"`, the snackbar honestly reads **`"Closed #19"`** (`Undo` reopens `#19` only), and failed `#20` remains checked (`failedTaskKeptSelected: true`).
- **Remaining literal `task(s)` pluralization (and Bug 5 stranded-task count)**:
  - `/next/processes/suites` (`Cancel suite run` → `Stop suite tasks`): `"Closed 1 active suite task."` and `"Closed 2 active suite tasks."` (was `"Closed N active suite task(s)."`).
  - `/next/settings/general` (`Broadcast to active tasks`): `"Broadcast delivered to 1 task"` and `"Broadcast delivered to 2 tasks"` (was `"Broadcast delivered to N task(s)"`).
  - `/next/settings/repositories` (`Check stranded tasks`):
    - **Before fix (Bug 5)**: `settings-code.component.ts` counted `res.targetReposStranded.length` (the number of stranded **repositories**) instead of summing `entry.tasks.length`, reporting `"1 open task(s) would be stranded..."` even when `acme/api` had **2** stranded open tasks!
    - **After fix**: sums `entry.tasks.length` across `targetReposStranded` (and reads `targetReposStranded` in `startRemoveRepo` as well), reporting `"1 open task would be stranded by current repository list."` and `"2 open tasks would be stranded by current repository list."`.

---

## Bugs found and fixed

| #   | Severity | Component                                                                                                            | Defect                                                                                                                                                                                                                                                                                      | Fix & verification                                                                                                                                                                                                                                                                                                    |
| --- | -------- | -------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | P1       | `ui-ng/src/app/create/{new-task-form.ts,new-task-sheet.component.ts}`, `shared/pill-select.*`                        | When `defaultTarget` was unset, `nt-repo` read `"Deployment default target"` and `validate()` skipped repository validation, firing a doomed `400` `POST /api/tasks`                                                                                                                        | `nt-repo` shows `"Select a repository…"`, `validate()` blocks inline with `"Pick a repository, or choose No repository."` (or `"Pick a repository."`), marks `.is-invalid`, and focuses `nt-repo`; covered by `new-task-form.spec.ts`, `new-task-sheet.component.spec.ts`, and `next-suites-and-task-actions.spec.js` |
| 2   | P1       | `ui-ng/src/app/create/new-task-sheet.component.ts`                                                                   | `Ctrl+Enter` called `file()` without checking `!this.canFile()`, bypassing the disabled submit button (`403 Forbidden`)                                                                                                                                                                     | Guarded `file()` with `if (this.busy()                                                                                                                                                                                                                                                                                |     | !this.canFile()) return;`; covered in `new-task-sheet.component.spec.ts`and`next-suites-and-task-actions.spec.js` |
| 3   | P1       | `ui-ng/src/app/tasks/tasks-page.component.ts`                                                                        | Inline batch Close prompt omitted `"task"` / `"tasks"`, `Escape` failed to cancel `confirmClose()` (closing the open detail pane instead), and clearing selection/switching tabs left `confirmClose()` stale                                                                                | Pluralized `"Close 1 task?"` / `"Close 2 tasks?"`, wired `Escape` and `Keep` (`close-keep`) with focus restoration to `batch-close`, and cleared `confirmClose` on empty selection or tab switch                                                                                                                      |
| 4   | P0       | `ui-ng/src/app/tasks/{tasks.store.ts,tasks-page.component.ts}`                                                       | On partial batch failure (1 of 2 tasks HTTP 500), snackbar falsely claimed `"Closed 2 tasks"`, unchecked the failed task, and reopened failed IDs on `Undo`                                                                                                                                 | `TasksStore.fanOut` tracks `lastLanded` and formats `"1 of 2 tasks failed: …"`; batch actions uncheck, announce, and `Undo` only `lastLanded`                                                                                                                                                                         |
| 5   | P1       | `ui-ng/src/app/settings/settings-code.component.ts`, `settings-instance.component.ts`, `processes-page.component.ts` | `checkReposPlan` counted stranded repositories instead of stranded tasks (`1` for 2 tasks on 1 repo), `startRemoveRepo` missed `targetReposStranded`, and three strings spelled literal `task(s)`                                                                                           | Summed `entry.tasks.length` across `targetReposStranded` and pluralized all three `task` / `tasks` messages; covered in `settings-page.component.spec.ts` and `processes-page.component.spec.ts`                                                                                                                      |
| 6   | P1       | `ui-ng/src/app/shared/{pill-select.css,surface-controls.css}`, `tasks/tasks-page.component.css`                      | Open `.batch-bar` failed WCAG 2.1 AA contrast: dark-mode `.pill-select-btn.is-inverse` was overridden by `:root[data-theme="dark"] .pill-select-btn` (`1.09:1`), and light-mode `.batch-bar .btn-text:hover` / `.pickup-bar .btn-text:hover` inherited `#e6ecf5` under `#a8c7fa` (`1.44:1`) | Scoped `:root[data-theme="dark"] .pill-select-btn:not(.is-inverse)` and set inverse `color-mix` hover background on `.batch-bar` / `.pickup-bar` `.btn-text:hover`; verified `0` axe violations across `desktop`, `phone`, and `dark`                                                                                 |
| 7   | P1       | `ui-ng/src/app/create/new-task-sheet.component.css`, `shared/pill-select.css`                                        | Caught in Design review: `.side-sheet` lacked `box-sizing: border-box` on `width: 100vw` mobile (`x = -1, width = 391` on `390×844`), and `.pill-select-btn` kept `140ms` transitions under `prefers-reduced-motion: reduce`                                                                | Added `box-sizing: border-box` (`border-left: none` on mobile full-bleed) to `.side-sheet` (`x = 0, width = 390`) and `@media (prefers-reduced-motion: reduce) { .pill-select-btn { transition: none; } }` (`0s`)                                                                                                     |

---

## Three-perspective UI review (`design/design/ui-design-book.design.md` §11)

### 1. Design review

- **Inspected artifacts & viewports**: `ui/screenshots/before/contact-sheet.png` and `ui/screenshots/after/contact-sheet.png` (`2000×1278` composites of `1440×900` desktop and `390×844` phone in light `topBarAvgRed = 247` and dark `topBarAvgRed = 30`), `/tmp/walk-5487-shots-after/*.png`, and live `grain demo` measurements across `390×844`, `1024×860`, `1440×900`, and `2048×1200`.
- **Checked rules**:
  - **Tokens & palette (§2)**: `0` hex literals in `pill-select.css`, `surface-controls.css`, `tasks-page.component.css`, and `new-task-sheet.component.css`; all error and inverse states use `--md-sys-color-*` tokens.
  - **Toolbar alignment & hierarchy (§4, §7)**: inside `.batch-bar`, `Set priority` (`batch-priority`) and `Close` (`batch-close`) share vertical centre (`toolbarCenterDeltaPx = 0` at all four widths); one `<h1>` per page (`h1Count = 1`); `0 px` horizontal overflow at `390`, `1024`, `1440`, and `2048` wide.
  - **Findings raised & re-verified**:
    1. **P1 ( §4 "A dialog fits the screen" )**: on `390×844`, `.side-sheet` measured `width: 391px, x: -1px` because `border-left: 1px` added onto `width: 100vw` without `box-sizing: border-box`. Fixed in `new-task-sheet.component.css` and pinned with bounding-box assertions in `next-suites-and-task-actions.spec.js`; re-verified `{"x":0,"y":0,"width":390,"height":844}`.
    2. **P1 ( §6 "prefers-reduced-motion: reduce turns all of it off" )**: `.pill-select-btn` reported `transitionDuration: "0.14s, 0.14s"` under `reducedMotion: "reduce"`. Fixed in `pill-select.css`; re-verified `reducedTransition: "0s"`.

### 2. Functional review

- **Steps & requests verified end to end**:
  - `/next/processes/suites` Runs subview renders `pass: 0, tasks: null` runs (`"0 tasks"`, `"ended without filing a pass"`, error banner, no `"Pass 0"` pill), expands subsequent `pass >= 1` runs, and cancels open suite tasks via `POST /api/tasks/{id}/close` with singular/plural snackbar copy.
  - `/next/` New-task sheet blocks missing title, missing repository (when `defaultTarget` is unset), and empty custom branch name before any `POST /api/tasks` request, clears inline error state immediately on field edit, files cleanly once valid, and disables filing for `grain/viewer` and out-of-scope repositories.
  - `/next/tasks` batch Close opens confirmation on live (`asked`/`running`) tasks with focus on `Keep` (`focusOnConfirmOpen: "close-keep"`), cancels on `Escape` while keeping `?task=<id>` open and restoring focus (`focusRestoredAfterEscape: "batch-close"`), cancels on `Keep`, confirms via `POST /api/tasks/{id}/close`, and on partial failure leaves failed rows checked while scoping snackbar and `Undo` (`POST /api/tasks/{id}/reopen`) strictly to succeeded IDs.

### 3. Adversarial review

- **Probes & outcomes**:
  - **`Ctrl+Enter` RBAC bypass when `!canFile()`**: reproduced on `HEAD` (`403` network call fired despite disabled button); fixed in `file()` and verified `0` requests sent.
  - **Partial batch failure data integrity**: reproduced P0 false `"Closed 2 tasks"` snackbar and unchecked failed row on `HEAD`; fixed via `TasksStore.lastLanded` and verified in both Vitest and Playwright.
  - **Stale `confirmClose` across Clear / tab switch**: verified clicking `Clear` or switching tabs resets `confirmClose` (`staleConfirmAfterClear: false`).
  - **Rapid double-submit & XSS**: synchronous `btn.click(); btn.click()` on `[data-testid="new-task-submit"]` sent exactly `1` `POST /api/tasks` request (`rapidDoubleSubmitPostCount: 1`); `<img src=x onerror="window.__xss=1">` in task titles rendered strictly as escaped text (`xssTriggered: false`).
  - **Global CSS collision & bundle budgets (§4, §9)**: `.pill-select-btn.is-invalid` is compound-scoped; production `ng build` succeeded with `0` warnings and `Initial total: 447.35 kB` (`< 450 kB` cap).

---

## Automated test verification

- **Frontend formatting (`make fmt-frontend`)**: passed (`ui`, `ui-ng`, `ui-hub`, `ui-shared`).
- **`ui-ng` Vitest (`cd ui-ng && npm test -- --run`)**: `49 passed` test files (`450 passed` tests).
- **`ui-ng` ESLint & build (`cd ui-ng && npm run lint && npm run build`)**: `0` lint issues, `0` build warnings.
- **`ui-ng` Playwright e2e (`ui-ng/e2e/next-suites-and-task-actions.spec.js` + full suite including `next-full-system.spec.js`)**: `9/9` passed in `next-suites-and-task-actions.spec.js` across `desktop`, `phone`, and `dark`; `next-full-system.spec.js` passed all `5/5` desktop/phone light/dark + contrast checks.
- **Repository hygiene (`go test ./tests/hygiene/... ./pkg/ports/... ./pkg/ui/...`)**: passed.
