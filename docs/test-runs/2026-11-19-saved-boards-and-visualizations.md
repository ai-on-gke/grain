# Saved Boards and Task Visualizations Walkthrough — 2026-11-19

Task 7305 follows
[2026-11-18-saved-boards-and-visualizations.md](2026-11-18-saved-boards-and-visualizations.md).
Every coverage item ran in headless Chromium through Playwright against a
real `grain demo` server over a throwaway embedded SQLite store. Steps ran
on `/next/` wherever a `/next/` surface exists (`ui-ng/`), and on the
classic UI at `/` (`ui/`) where a page has not moved over yet.

Five defects were found on `/next/tasks` (`ui-ng/`), reproduced
deterministically, fixed in this task, covered by unit and Playwright
regression tests (`desktop`, `@phone`, `@dark`), and re-verified against a
rebuilt `grain demo` server:

1. **Active board matching claimed classic boards were active and missed
   reordered `/next/` queries:** `/next/tasks?q=merge` displayed a classic
   Kanban board (`WT Merge Kanban`, `query: "q=merge"`) as the active board
   on the Boards pill, while applying a `/next/` list board saved as
   `q=merge&tab=asked` left the pill showing the board count instead of the
   board's name.
2. **Long Boards menus overflowed the viewport and slid behind the phone
   bottom bar:** `.boards-menu.pop-menu` had no `max-height` or vertical
   scrolling; with 19 boards on a `390×844` phone it ran from `y = 148` to
   `y = 851`, pushing `+ Save current view…` (`y = 809..845`) behind
   `nav.compact-nav` (`y = 788..844`) and off the bottom of the screen.
3. **Failed initial `GET /api/tasks` drew a fake `0`-task Timeline and
   `Ready to run 0`:** on `/next/tasks?view=timeline`, when the initial
   `GET /api/tasks` returned `500`, the page rendered `1h ago | Timeline ·
   0 tasks | now` under the error banner; Backlog showed `Ready to run 0`.
4. **Cold-loading `/next/tasks?newBoard=1` as a read-only viewer opened the
   Save Board form:** the `?newBoard=1` effect evaluated `canWriteBoards()`
   before `GET /api/config` resolved (when `AccessService` still defaults to
   full access), opening `board-save-form` for a viewer lacking
   `boards:write`.
5. **Backlog rows offered an enabled `⋮ → Defer` action to read-only
   viewers:** a viewer lacking `tasks:write` had drag handles and move items
   hidden, but still saw every Backlog row's `⋮` menu with an enabled
   `Defer` button that fired `POST /api/tasks/{id}/defer`.

All classic `/boards` surfaces (index, folders, ordering, async creation,
viewer gating, list, kanban, custom columns, gantt, tree, dashboard, and
report) behaved as expected.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `17339dd82` (`main`, 2026-11-19) |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci` with `TMPDIR` under `/var/tmp/walk-7305/tmp` (throwaway SQLite) |
| Frontends | `make frontend grain` (`ui/`, `ui-ng/`, `ui-hub/` embedded). After fixes: `cd ui-ng && npm run build`, then `make grain`, server restarted over a fresh throwaway store and reseeded |
| Browser | Playwright Chromium (headless, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Desktop `1440×900` and phone `390×844` (touch), in light and dark |
| Toolchain | Go from `go.mod`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent, so `LOOK=1` was unavailable. Screenshots (`before-*` and `after-*`, kept in `/var/tmp/walk-7305/screenshots` outside git) and live layout were verified via bounding boxes, `scrollWidth`/`clientWidth`, `scrollHeight`/`clientHeight`, computed styles, and `@axe-core/playwright` |

Sign-in posted `{code, name: "alice@example.com"}` to
`POST /api/auth/access-code`. Walkthrough scripts, logs, and screenshots
were kept in `/var/tmp/walk-7305` outside the repository and are not
committed.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| External GitHub and CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | The sandbox has no external GitHub or CI credentials |
| Agent landing a `grain/config` board PR | Exercised through `/new` and actual `POST /api/tasks` submission (verifying the resulting `grain/config` task `#29` without mutating `/api/boards`, then closing `#29`) | `grain demo` runs no model worker |
| Additional board kinds, folders, and task states | Seeded through `POST /api/boards` and `POST /api/tasks` (listed below) | Stock `grain demo` seeds four boards (`Board`, `Timeline`, `Report`, `Tree`) and no held proposal or `keepRunning` task |
| Viewer role | Scoped `page.route` on `GET /api/config` setting `access` to `grain/viewer` with read scopes only (`tasks:read`, `boards:read`, `metrics:read`, `repos:read`, `ci:read`) | No identity-aware proxy in the sandbox |
| Store, task-list, and metrics read failures | Scoped `page.route` returning `500` on `GET /api/boards` (`store unavailable`), `GET /api/tasks` (`tasks db offline`), and `GET /api/metrics` (`metrics broken`), plus a delayed gate on `GET /api/metrics` for loading state | Embedded SQLite does not fail or stall on demand |

Throwaway fixtures created on the demo server:

- **Same-named `Triage` boards:**
  - loose (`list`, `f=state:any:queued`, `board-5`)
  - `WT Ops` (`list`, `f=state:any:running`, `board-6`)
  - `WT Zeta` (`kanban`, `board-7`)
- **`WT Zeta` folder boards:** `WT Zeta Alpha`, `WT Zeta Beta`, `WT Zeta
  Gamma` (`list`, `board-8`..`board-10`).
- **Custom-column Kanbans:**
  - `WT Submit Only` (`board-11`): `Asked to submit` (`awaiting_submit`),
    `Queued` (`queued`).
  - `WT Reply Only` (`board-12`): `Asked for a reply` (`awaiting_reply`),
    `Queued` (`queued`).
- **Narrowed tree:** `WT Widgets Tree` (`tree`, `f=repo:any:acme/widgets`,
  `board-13`).
- **Dashboards:**
  - `WT Dash` (`board-14`) with four panels: `State counts` (`states`),
    `Running only` (`list`, `f=state:any:running`), `Week timeline`
    (`gantt`, `7d`), and `Throughput` (`metrics`, `7d`).
  - `WT Empty Dash` (`board-15`) with no panels.
- **Reports:** `WT Nine` (all nine sections: `throughput`, `latency`,
  `backlog`, `merges`, `prompts`, `tools`, `agents`, `sandboxes`, `checks`,
  `board-16`) and `WT Widgets Report` (`f=repo:any:acme/widgets`,
  `board-17`).
- **Query-matching probe boards:** `WT Merge Kanban` (`kanban`, `q=merge`,
  `board-18`) and `WT Reversed Asked` (`list`, `q=merge&tab=asked`,
  `board-19`).
- **Throwaway tasks:**
  - `WT held proposal` (`#27`, `holdForApproval: true`, `proposedBy: "8"`,
    `heldBy: "8"`).
  - `WT standing task` (`#28`, `keepRunning: true`, `approved: true`,
    `queued`).

## 2. Steps and outcomes

`[next]` marks steps run on `/next/` (`ui-ng/`) and `[classic]` marks steps
run on `/` (`ui/`).

**Surface inventory** (checked in `ui-ng/src/app/shell/nav.ts` and
`ui-ng/src/app/app.routes.ts`):

- **On `/next/`:**
  - `/next/tasks`: `Boards` saved-views menu, Save Board bar, rail `New →
    New board`, List and Backlog reorder, Timeline view (`?view=timeline`).
  - `/next/workstreams`: filer and goal trees.
  - `/next/telemetry`: measurement report (`?window=`, `?repo=`, Refresh).
- **Classic-only (no `/next/boards` route yet; `**` redirects to
  `/next/inbox`):**
  - `/boards` index and folder ordering.
  - `/boards/:id` presentations: Kanban, Gantt, Tree, Dashboard, and
    Report.
  - Classic asynchronous board/panel request form (`/new` targeting
    `grain/config`).

### 2.1 `/next/tasks` Boards menu, creation, deletion, and Timeline `[next]`

- **Menu disclosure, focus, and same-named boards:**
  - Opening `saved-boards-btn` focused the first item (`board-apply-board-1`).
  - The three `Triage` boards carried distinct accessible names:
    - `Triage, outside every folder, filtered list, opens on the classic Boards page`
      (`Delete board Triage, outside every folder`)
    - `Triage, in WT Ops, filtered list, opens on the classic Boards page`
      (`Delete board Triage, in WT Ops`)
    - `Triage, in WT Zeta, kanban, opens on the classic Boards page`
      (`Delete board Triage, in WT Zeta`)
  - Applying `Triage, in WT Ops` opened `/boards/board-6`; browser `Back`
    returned to `/next/tasks`.
- **Active board matching (Bug 3.1):**
  - Before fix: `/next/tasks?q=merge` showed `Boards WT Merge Kanban`;
    applying `WT Reversed Asked` (`q=merge&tab=asked`) navigated to
    `?tab=asked&q=merge` and showed `Boards 19`.
  - After fix: `/next/tasks?q=merge` shows `Boards 19`; applying `WT
    Reversed Asked` shows `Boards WT Reversed Asked`.
- **Long Boards menu overflow (Bug 3.2):**
  - Before fix: with 19 boards on `390×844` phone, `.boards-menu` measured
    `y = 148, height = 703` (`bottom = 851 > 844`), putting `+ Save current
    view…` (`y = 809..845`) behind `nav.compact-nav` (`y = 788..844`).
  - After fix: on `390×844` phone `.boards-menu` measures `y = 148, height
    = 626` (`bottom = 774 <= 788`), `overflow-y: auto`, `scrollHeight =
    703`, `clientHeight = 626`, zero axe violations. On `1440×900` desktop
    it measures `y = 113, height = 472`.
- **Rail `New → New board`, persistence, and delete confirmation:**
  - From `/next/tasks?tab=asked&q=merge&view=timeline`, `New → New board`
    kept the URL and Timeline view, opened `board-save-form`, and focused
    `board-name-input`.
  - Saving `WT Next Saved` sent `POST /api/boards` with
    `{"kind":"list","name":"WT Next Saved","query":"tab=asked&q=merge&view=timeline"}`,
    returned focus to `saved-boards-btn`, and showed `Boards WT Next Saved`
    across a reload.
  - Clicking `×` on `WT Next Saved` opened `confirm-dialog` naming the
    board and warning it affects everyone on this deployment, with `Cancel`
    focused. `Escape` cancelled without `DELETE` and restored focus to
    `saved-boards-btn`. Confirming sent `DELETE /api/boards/board-20` and
    removed the board.
- **Failed `GET /api/boards`:** menu displayed `The boards could not be
  loaded (store unavailable).` (`data-testid="boards-load-error"`) and
  never `No saved boards yet.`
- **Timeline (`?view=timeline`) and failed `GET /api/tasks` (Bug 3.3):**
  - On `tab=running&view=timeline`, axis drew `1h ago | Timeline · 2 tasks |
    now` with bars (`width = 38.9px`, `height = 10px`). Clicking `#3` opened
    the detail pane `#3 Bump the Go toolchain to 1.24` at
    `?tab=running&view=timeline&task=3`.
  - Before fix: a `500` on initial `GET /api/tasks` drew `1h ago |
    Timeline · 0 tasks | now` under the error alert, and Backlog showed
    `Ready to run 0`.
  - After fix: the error alert shows without `tasks-timeline` or
    `timeline-empty`, and Backlog shows `Ready to run` with no fake `0`.
- **Viewer role gating on `/next/tasks` (Bugs 3.4 & 3.5):**
  - Boards menu showed `0` delete buttons and no `+ Save current view…`;
    rail `New` menu disabled all 8 create items including `New board`.
  - Before fix: cold-loading `/next/tasks?newBoard=1` as a viewer opened
    `board-save-form` before `/api/config` returned, and Backlog rows
    rendered 6 `⋮` buttons whose enabled `Defer` item posted
    `POST /api/tasks/28/defer`.
  - After fix: cold-loading `/next/tasks?newBoard=1` as a viewer keeps
    `board-save-form` hidden and strips `?newBoard=1` once `/api/config`
    resolves; Backlog rows render `0` `⋮` menus and `0` drag handles.

### 2.2 `/next/workstreams` and `/next/telemetry` `[next]`

- **`/next/workstreams` (Tree):**
  - Typing `pagination` updated the URL to `/next/workstreams?q=pagination`
    and survived a reload with the search input populated.
  - Opening `Add pagination to the tasks API` navigated to
    `/next/workstreams/director:director:8?q=pagination` and nested child
    nodes `#11` (`Extract pagination query helper`, `Blocked`) and `#27`
    (`WT held proposal`, `Proposed`) under `#8` (`Running`).
  - Clicking `#11` selected it in the detail pane (`?node=11`).
  - Zero serious/critical axe violations; zero horizontal overflow on phone
    in light and dark.
- **`/next/telemetry` (Measurement report):**
  - Selecting `30d` updated the URL to `?window=30d`; pressing `Refresh`
    requested `GET /api/metrics?window=30d`.
  - Selecting repository `acme/widgets` requested
    `GET /api/metrics?window=7d&repo=acme%2Fwidgets`, updated the URL to
    `?window=7d&repo=acme%2Fwidgets`, and persisted `Repo: acme/widgets`
    across a reload.
  - Task links (`#10`, `#9`, `#20`) navigated to `/next/task/<id>`.
    *(Observation, not changed: `ui-ng` has no cross-route browser-Back
    focus-restoration mechanism yet, so Back from `/next/task/10` to
    `/next/telemetry` leaves focus on `document.body`; classic Report does
    restore focus, verified in section 2.7.)*
  - With `GET /api/metrics` returning `500`, `telemetry-error` rendered
    `metrics broken`.
  - Zero serious/critical axe violations; zero horizontal overflow on phone
    in light and dark.

### 2.3 Classic `/boards` index, folders, ordering, history, and error `[classic]`

- **Index and folders:**
  - Rail showed `Boards 19`. Index exposed `0` search/sort inputs.
  - Loose boards appeared first, followed by `WT Ops` and `WT Zeta`.
  - Same-named `Triage` links carried distinct `aria-label`s: `Triage,
    outside every folder`, `Triage, in WT Ops`, and `Triage, in WT Zeta`.
- **Reordering in `WT Zeta` (section below the first):**
  - Tap-moving `Move WT Zeta Gamma` offered 20 slots named by neighbour and
    folder and no slot beside `WT Zeta Gamma` itself.
  - Choosing `Move here, above WT Zeta Alpha` sent `PATCH /api/boards/board-10`,
    kept focus on `Move WT Zeta Gamma`, and persisted `[WT Zeta Gamma, WT
    Zeta Alpha, WT Zeta Beta]` across a reload.
  - Dragging `Move WT Zeta Alpha` onto `WT Zeta Gamma` sent `PATCH` (`200`)
    and persisted `[WT Zeta Alpha, WT Zeta Gamma, WT Zeta Beta]` across a
    reload.
- **Back/Forward/reload:** opening `WT Zeta Beta` (`/boards/board-9`),
  pressing `Back` returned to `/boards` with focus restored to
  `href="/boards/board-9"`, and `Forward` returned to `/boards/board-9`.
- **Failed `GET /api/boards`:** rail showed `Boards could not be loaded
  (store unavailable)` without a board count, and the index never said `No
  boards yet`.

### 2.4 Classic asynchronous creation and viewer gating `[classic]`

- **Header `New board` and Rail `Create options → New board`:** both opened
  `/new` prefilled with title `Add a board` and the board-specification
  template targeting `grain/config`.
- **Submitted request inspection:** submitting the form posted
  `POST /api/tasks` (`201`), creating task `#29` (`state: queued`,
  `repo: grain/config`, `title: "Add a board"`), while `GET /api/boards`
  remained at `19` boards — verifying the asynchronous creation contract.
  Task `#29` was then closed.
- **Viewer role (`grain/viewer`, read-only scopes):**
  - Rail `Create options` marked `New task` and `New board`
    `aria-disabled="true"`.
  - `/boards`: `New board` disabled, `0` board move handles.
  - `/boards/board-1` (Kanban): `Edit board` disabled, `0` card move
    handles, `0` draggable elements.
  - `/boards/board-5` (List): `0` draggable/move handles.
  - `WT Dash?f=repo:any:acme/widgets`: `Add panel` and all 3 `Keep` buttons
    disabled, `0` panel move handles.

### 2.5 Classic List and Kanban (`/boards/board-1`, `board-5`, custom columns) `[classic]`

- **Card markers:** `Merging` column present; `#11` carried `.badge-blocked`
  (`Blocked on: #8 (running)`); `#28` showed `keeps running`; `#27` showed
  `held for approval by 8`; `#12` showed `imported`.
- **Detail navigation:** clicking `#2` opened `/tasks/2`, and `Back`
  restored focus to `href="/tasks/2"`. Clicking a row in list board
  `board-5` opened `/tasks/28`, and `Back` returned to
  `/boards/board-5?f=state:any:queued`.
- **Card moves (tap, keyboard, mouse drag):**
  - Move slots identified tasks by id and title (`Move here, above task 2,
    “Fix flaky retry test”, in Waiting`).
  - Tap-moving `#28` above `#2` posted `POST /api/tasks/reorder` (`200`),
    kept focus on `Move task 28`, and survived a reload.
  - `Space` on `Move task 2` switched it to `Stop moving task 2`; `Escape`
    restored `Move task 2`.
  - Mouse dragging `Move task 28` onto `#26` posted
    `POST /api/tasks/reorder` (`200`) and persisted across reload.
- **Column editing and reordering (`Edit columns`):**
  - Reordering `Waiting` left of `Proposed` and saving persisted column
    order `[Waiting, Proposed, Running, Asked, Failed, Merging]` across
    reload.
  - Removing `Merging` and saving persisted across reload and reported
    `7 tasks in no column (Merging, Deferred, Closed).`
  - `Reset to default` restored default order and `Merging`.
- **Custom columns:** neither board offered `Edit columns`, and each "in no
  column" summary distinguished the omitted asked state:
  - `WT Submit Only`: `14 tasks in no column (Proposed, Running, Asked for
    a reply, Merging, Deferred, Closed).`
  - `WT Reply Only`: `20 tasks in no column (Proposed, Running, Asked to
    submit, Merging, Deferred, Closed).`

### 2.6 Classic Timeline / Gantt (`/boards/board-2`) and Tree (`board-4`, `board-13`) `[classic]`

- **Gantt (`/boards/board-2`):**
  - `Range` offered `Last 24 hours`, `Last 7 days`, `Last 30 days`, `Last
    90 days`, `Everything`; `Lanes` offered `No lanes`, `By repo`, `By
    state`, `By author`.
  - Setting `Last 24 hours` + `By state` persisted across a reload at
    `/boards/board-2`, showing `4 tasks outside this range.`, one
    `.gantt-now` marker, and lanes in order `Proposed, Waiting, Running,
    Asked, Merging, Deferred, Closed` (`Asked` after `Running`).
  - Clicking `#1`'s bar opened `/tasks/1`, and `Back` restored focus to
    `href="/tasks/1"`.
  - At `Range = Everything`, dependency SVG paths rendered cleanly.
- **Tree (`board-4` and `WT Widgets Tree` `board-13`):**
  - `board-4` rendered 29 task rows.
  - `WT Widgets Tree` opened at `/boards/board-13?f=repo:any:acme/widgets`,
    kept its URL across reload, excluded non-`acme/widgets` depot task
    `#14`, and nested `#26` (`x=275`) under `#25` (`x=248`), `#8`
    (`x=275`) under `#13` (`x=248`), and `#11` and `#27` (`x=301`) under
    `#8` (`x=275`).
  - Clicking `#11` opened `/tasks/11`, and `Back` returned to
    `/boards/board-13?f=repo:any:acme/widgets`.

### 2.7 Classic Dashboard (`board-14`, `board-15`) and Report (`board-3`, `board-16`, `board-17`) `[classic]`

- **Dashboard (`WT Dash` and `WT Empty Dash`):**
  - Typing `pagination` set `?q=pagination&f=state:none:deferred|closed`
    and survived a reload. `Keep` was offered on `State counts` and `Week
    timeline`, NOT on `Throughput` (metrics unaffected by text search) and
    NOT on `Running only` (which carries its own `f=state:any:running`
    query and kept its 2 running rows).
  - With `?f=repo:any:acme/widgets`, `Keep` WAS offered on `Throughput`
    (alongside `State counts` and `Week timeline`).
  - Tap-reordering `Running only` above `State counts` sent
    `PATCH /api/boards/board-14` (`200`) and persisted across reload; mouse
    dragging `Move panel 2` onto `Move panel 1` also sent `PATCH` (`200`).
  - Clicking `Open` (`/?f=state:any:running`, 2 rows) and pressing `Back`
    restored focus to `Open`. Clicking `5 Waiting` inside `State counts`
    (`/?f=state:none:deferred|closed&f=state:any:queued`, 5 rows) and
    pressing `Back` restored focus to `5 Waiting`.
  - `Add panel` opened `/new`.
  - `WT Empty Dash` showed `This dashboard has no panels yet. “Add panel”
    files the task that adds one.`
  - With `GET /api/metrics` delayed then returning `500`, `Throughput`
    rendered `Loading…` while pending and `The metrics report could not be
    loaded.` on error while sibling panels rendered normally.
- **Report (`board-3`, `WT Nine`, `WT Widgets Report`):**
  - Selecting `Last 30 days` set `?window=30d`, `Refresh` requested
    `/api/metrics?window=30d&buckets=24`, and `?window=30d` survived a
    reload.
  - `WT Nine` drew the 3 populated sections (`Throughput`, `Latency`,
    `Backlog`), omitted the 6 silent sections, and offered all 9 `Move
    section` handles. Tap-reordering `Latency` before `Throughput` sent
    `PATCH /api/boards/board-16` (`200`) and persisted `[Latency,
    Throughput, Backlog]` across reload; mouse drag also sent `PATCH`
    (`200`).
  - `WT Widgets Report` requested
    `/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24`.
  - On `board-3`, every backlog chip matched its underlying list count and
    restored focus on `Back`:
    - `Proposed 3` → `/?f=state:any:proposed` (3 rows) → `Back` focused `Proposed 3`
    - `Waiting 6` → `/?f=state:any:queued` (6 rows) → `Back` focused `Waiting 6`
    - `Running 2` → `/?f=state:any:running` (2 rows) → `Back` focused `Running 2`
    - `Asked 1` → `/?f=state:any:awaiting_reply&f=completed:empty:` (1 row) → `Back` focused `Asked 1`
  - Following task reference `task 22` (`/tasks/22`) and pressing `Back`
    restored focus to `task 22`.

## 3. Bugs found and fixed

### 3.1 `/next/tasks` `activeBoardName` claimed classic boards and missed reordered `/next/` queries

- **Reproduction on `17339dd82`:** create `{"name":"WT Merge Kanban","kind":"kanban","query":"q=merge"}` and `{"name":"WT Reversed Asked","kind":"list","query":"q=merge&tab=asked"}`. Open `/next/tasks?q=merge` (Boards pill shows `WT Merge Kanban`); apply `WT Reversed Asked` (URL becomes `?tab=asked&q=merge`, pill drops back to count `19`).
- **Fix:** added `canonicalNextBoardQuery(b)` in `ui-ng/src/app/tasks/board-labels.ts` (returning `null` for any board `nextBoardParams(b)` routes to classic, and canonicalizing `tab, group, q, repo, view` order/encoding otherwise) and updated `activeBoardName` in `ui-ng/src/app/tasks/tasks-page.component.ts` to match via `canonicalNextBoardQuery(b)` and display `boardLabel(match)`.

### 3.2 `/next/tasks` `.boards-menu` overflowed past the bottom of the viewport and behind the phone bottom bar

- **Reproduction on `17339dd82`:** seed ~19 boards and open the Boards menu on `/next/tasks` at `390×844`. `.boards-menu` spans `y = 148..851`, covering `nav.compact-nav` (`y = 788..844`) and overflowing the `844px` viewport.
- **Fix:** bounded `.boards-menu.pop-menu` in `ui-ng/src/app/tasks/tasks-timeline.css` with `max-height: min(460px, calc(100dvh - 180px)); overflow-y: auto; overscroll-behavior: contain;` (`max-height: calc(100dvh - 230px);` below `600px`).

### 3.3 Failed initial `GET /api/tasks` drew `Timeline · 0 tasks` and `Ready to run 0`

- **Reproduction on `17339dd82`:** stub `GET /api/tasks` with `500` and open `/next/tasks?view=timeline`. The Timeline draws `1h ago | Timeline · 0 tasks | now` under the error alert; switching to List shows `Ready to run 0`.
- **Fix:** gated Timeline rendering and Backlog's `ready-count` number on `store.loaded()` in `ui-ng/src/app/tasks/tasks-page.component.ts`, showing `Loading timeline…` only while loading without error and omitting the chart/count when the initial read failed.

### 3.4 Cold-loading `/next/tasks?newBoard=1` as a viewer opened the Save Board bar

- **Reproduction on `17339dd82`:** intercept `GET /api/config` with a `150ms` delay returning `grain/viewer` (read-only scopes) and navigate directly to `/next/tasks?newBoard=1`. `board-save-form` opens before `/api/config` resolves.
- **Fix:** waited on `this.access.configLoaded()` inside the `?newBoard=1` effect and guarded `@if (boardSaveOpen() && canWriteBoards())` in `ui-ng/src/app/tasks/tasks-page.component.ts`.

### 3.5 Backlog rows offered an enabled `⋮ → Defer` menu to read-only viewers

- **Reproduction on `17339dd82`:** open `/next/tasks` as `grain/viewer` (lacking `tasks:write`). Every Backlog row still shows its `⋮` button with an enabled `Defer` action that posts `POST /api/tasks/{id}/defer`.
- **Fix:** gated `.row-menu-wrap` on `(canMove() && sort() === "run") || canDeferTask(t)` and the `Defer` item on `canDeferTask(t)` in `ui-ng/src/app/tasks/tasks-page.component.ts`.

## 4. Follow-ups

None filed. Standalone `/boards` presentations (Kanban, Gantt, Tree,
Dashboard, Report) remain on the classic-to-`/next/` migration backlog.

## 5. Tests run

| Suite | Result |
| --- | --- |
| `cd ui-ng && npm run lint && npm run format:check` | Passed (0 errors) |
| `cd ui-ng && npm test` | 76 files, 831 tests passed |
| `cd ui-ng && npm run build && cd .. && make grain` | Built cleanly (0 warnings in touched files) |
| `cd ui-ng && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright npx playwright test e2e/next-saved-boards.spec.js e2e/next-full-system.spec.js e2e/next-tasks-intake-regressions.spec.js` | 26 passed (`desktop`, `phone`, `dark`) |
| `go test ./tests/hygiene/...` | Passed |
| Live walkthrough (`walk-next.mjs`, `walk-classic*.mjs`, `walk-deep.mjs`, `screenshot.mjs`) | 0 page errors, 0 console errors, 0 unexpected HTTP `>= 400`, 0 axe serious/critical violations |
