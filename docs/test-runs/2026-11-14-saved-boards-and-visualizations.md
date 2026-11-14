# Saved boards and task visualizations walkthrough — 2026-11-14 (task 5631)

This is the third boards walkthrough. The earlier two are
[2026-11-13-saved-boards-and-visualizations.md](2026-11-13-saved-boards-and-visualizations.md)
(task 4727) and
[2026-11-13-saved-boards-and-visualizations-task-5385.md](2026-11-13-saved-boards-and-visualizations-task-5385.md)
(task 5385). This run repeated every coverage item against `main` at
`3266698ea`. It drove headless Chromium through Playwright against a
real `grain demo` server over a throwaway embedded SQLite store, using
the UI's own controls.

The classic board pages behaved as the 2026-11-13 runs left them. The
new finding is the **Timeline view on `/next/tasks`** (`?view=timeline`),
which no earlier boards walkthrough covered. It had two bugs, both
fixed here (section 3):

- A still-open task's bar stopped where the task was filed, so it
  looked finished.
- A board saved from the Timeline view came back as the list.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `3266698ea` (`main`, 2026-11-14 11:07Z). Fix re-checked at `96db6074f` |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, with `TMPDIR` set to a throwaway directory under `/var/tmp` |
| Frontends | `make frontend` (classic `ui/` and `ui-ng/`), embedded by `go build ./cmd/grain`. `ui-ng` was rebuilt with `npm run build` after the fix |
| Browser | Playwright Chromium 1234, headless (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Desktop `1440×900` and `iPhone 13` (`390×844`, touch), light and dark |
| Toolchain | Go from `go.mod`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent. Layout was checked with bounding boxes and `scrollWidth`, not `LOOK=1` |

Sign-in posted the demo's access code to `POST /api/auth/access-code`
as `alice@example.com`, which is what the visible access-code form
does. The request body takes `name`, not `email`.

The walkthrough scripts were ad-hoc Node scripts using
`ui-ng/node_modules/@playwright/test`. They lived in `/var/tmp/walk` and
are not committed. Neither are screenshots.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub, CI, base-branch CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | The sandbox has no external GitHub or CI |
| Agent that lands a requested board | None. Checked up to the filed `grain/config` task (2.2) | `grain demo` runs no model agent |
| Throwaway boards and tasks | `POST /api/boards` and `POST /api/tasks`, listed below | The demo seeds only `Board`, `Timeline`, `Report` and `Tree` |
| Viewer and contributor roles | `page.route` on `GET /api/config`, rewriting `access` to `grain/viewer` (every non-dangerous `:read` scope, as `builtinAccessRoles` grants) or `grain/contributor` (those plus `tasks:write`, `ci:write`) | No identity-aware proxy in the sandbox |
| Load failures | Scoped `page.route`: `500 {"error":"store unavailable"}` for `GET /api/boards`, and `500` for `GET /api/metrics` | Real SQLite does not fail on demand |

The throwaway boards were:

- **Three `Triage` lists:** a loose one (`f=state:any:queued`), one in
  `WT Ops` (`f=state:any:running`), and a kanban in `WT Zeta`
- **More `WT Zeta` lists:** `WT Zeta Alpha`, `WT Zeta Beta` and
  `WT Zeta Gamma`
- **`WT Submit Only` kanban:** columns `Asked to submit`
  (`awaiting_submit`) and `Queued` (`queued`)
- **`WT Widgets Tree`:** `f=repo:any:acme/widgets`
- **`WT Dash`:** panels `State counts`, `Running only`
  (`f=state:any:running`), `Week timeline` (gantt, 7d) and `Throughput`
  (metrics, 7d)
- **`WT Empty Dash`:** a dashboard with no panels
- **Reports:** `WT Nine` (all nine sections) and `WT Widgets Report`
  (`f=repo:any:acme/widgets`)

The throwaway tasks were `WT held proposal` (`proposedBy: "8"`,
`holdForApproval: true`) and `WT standing task` (`keepRunning: true`).

## 2. Steps and outcomes

`[next]` marks a step run on `/next/`, and `[classic]` a step run on the
classic UI.

**No `/next/` page yet:** the Boards index, every board presentation
(list, kanban, gantt, tree, dashboard, report) and the board-request
form. `ui-ng/src/app/shell/nav.ts` has no Boards entry, and
`ui-ng/src/app/app.routes.ts` has no `boards` route. `/next/boards`
falls through `**` to `/next/inbox`. On `/next/`, boards appear only in
the **Boards** menu on `/next/tasks` (2.8), and visualization only in
its **Timeline** view (2.9).

### 2.1 Boards index, rail, folders, ordering, history and load failure `[classic]`

- **Opening it:** the rail's `Boards 16` button opened `/boards`. The
  header count and the rail agreed: 4 seeded boards, then 16 with
  fixtures. The index offers no search or sort.
- **Folders:**
  - Loose boards come first, then `WT Ops` and `WT Zeta`, each headed
    by its name.
  - The three same-named boards have distinct accessible names on both
    the link and its handle: `Triage, outside every folder`,
    `Triage, in WT Ops` and `Triage, in WT Zeta`. Uniquely named boards
    keep their bare names.
- **Dragging in a section below the first:**
  - `Move WT Zeta Gamma` dragged (HTML5 drag) onto `WT Zeta Alpha` put
    Gamma above Alpha.
  - Tapping `Move WT Zeta Beta`, then `Move here, at the end of WT
    Zeta`, put Beta last and kept focus on `Move WT Zeta Beta`.
  - Both orders survived a reload.
  - Slots name their neighbour by name and folder (`Move here, above
    Triage, in WT Zeta`), and no slot is offered next to the picked
    board itself.
- **History:** opening `WT Zeta Alpha`, then Back, landed on `/boards`
  with focus on that link. Forward returned to `/boards/board-8`.
- **Load failure:** with `GET /api/boards` failing, the page explains
  that the boards could not be loaded, not "No boards yet". The rail
  shows `Boards` with no count rather than `0`.

### 2.2 Asynchronous board creation and viewer gating `[classic]`

- **Rail create menu → New board** (from `/?f=state:any:running`):
  - It opened `/new`, prefilled with `Add a board` in `grain/config`.
  - The description lists the board's name, folder, kind, description
    and query, with `` query: `f=state:any:running` `` carried over from
    the list.
  - `create task` posted `POST /api/tasks` and filed task 26 (`Add a
    board`), queued for an agent. No board was written directly.
- **Boards index header → New board:** it opened the same form with
  `query: <see below>`. Escape returned to `/boards` with focus on `New
  board`.
- **Viewer** (`grain/viewer`, every `:read` scope):
  - `New board`, `Edit board` and `Add panel` are present but disabled.
  - There are no task, panel or board move handles, and no `Keep`.
  - `/next/tasks` shows the Boards menu with no `×` and no `Save
    current view…`.
  - `Edit columns` on the seeded `Board` stays enabled. This is right:
    it stores columns in this browser's `localStorage` (`ui/src/board.js`
    `saveColumns`), not on the server.
- **Contributor** (`tasks:write`, no `boards:write`):
  - `New board`, `Edit board` and `Add panel` are enabled, because each
    files a task.
  - Kanban and list cards have move handles (23 and 5), since task
    order is `tasks:write`.
  - Board and panel reorder handles are hidden, since they `PATCH
    /api/boards` (`boards:write`).

### 2.3 List and kanban `[classic]`

- **Cards:**
  - The merge-queue column is headed `Merging`.
  - Task 11 carries `Blocked on: #8 (running)`.
  - `WT standing task` carries `keeps running`, and `WT held proposal`
    carries `held for approval by 8`.
  - Task 12 (`[#42] Search results drop the last page`) carries
    `imported`.
- **Opening a card:** `Fix flaky retry test` opened `/tasks/2`. Back
  restored focus to that card's link.
- **Moving cards:**
  - Tapping `Move task 23`, then the first slot, sent `POST
    /api/tasks/reorder {"ids":["23"],"beforeId":"2"}`. Focus stayed on
    `Move task 23`, and the order survived a reload.
  - Slots name tasks by id and title (`Move here, above task 2, “Fix
    flaky retry test”, in Waiting`). No slot is offered beside the
    picked card.
  - Space on a handle picks it up (`Stop moving task 2`), and Escape
    puts it down.
  - Dragging a card onto another column changes nothing and sends
    nothing. Columns are states, so this is expected.
- **Column definitions:** in `Edit columns`, `Move Asked left` kept
  focus on the button. `Remove Merging`, then `Save`, gave `proposed,
  waiting, asked, running, failed` and the note `4 tasks in no column
  (Merging, Deferred, Closed)`. That survived a reload, and `Reset to
  default` restored all six columns.
- **Custom columns:** `WT Submit Only` hides `Edit columns`. Its note
  reads `12 tasks in no column (Proposed, Running, Asked for a reply,
  Merging, Deferred, Closed)`, so it tells `Asked for a reply` apart
  from the `Asked to submit` column it does collect.

### 2.4 Timeline and gantt `[classic]`

- **Controls:**
  - The `Range` options are 24 hours, 7, 30 and 90 days, and
    everything. The `Lanes` options are none, repo, state and author.
- **Changes:**
  - `last 24 hours` with `by state` gave `3 tasks outside this range`.
  - The lanes ran `Proposed 4, Waiting 5, Running 2, Asked 9, Merging
    1, Deferred 1`, so Asked comes after Running.
- **Reload:** both settings survived a reload. They live in this
  browser, not the URL, which stays `/boards/board-2`.
- **Bars:**
  - The `.gantt-now` marker is drawn.
  - Each bar's accessible name gives the filing time and how long the
    task has been open (`still open after 4h 1m`).
  - Task 11's row names its blocker.
  - Opening `Fix flaky retry test` went to `/tasks/2`. Back restored
    focus to the link.

### 2.5 Tree `[classic]`

- `WT Widgets Tree` opened at `/boards/board-12?f=repo:any:acme/widgets`,
  with a `Repo is any of acme/widgets` chip. The URL and the chip
  survived a reload.
- Tasks nest under their filer. Task 23 sits under 22. Tasks 1, 3 and 8
  sit under the Director Session (13). Tasks 11 and `WT held proposal`
  (24, `proposedBy: 8`) sit under 8.
- Task 26 (`grain/config`) is narrowed out.
- Clicking row `25` opened `/tasks/25`. Back returned to the narrowed
  tree.

### 2.6 Dashboard `[classic]`

- **Whole-board narrowing:**
  - Typing `pagination` into `Search this dashboard…` set
    `?q=pagination&f=state:none:deferred|closed`, which survived a
    reload.
  - `State counts` and `Week timeline` narrowed to 2 and offered `Keep`.
  - `Running only` kept its own question (2), and `Throughput` offered
    no `Keep`, because a search does not change its report.
  - Narrowing to `?f=repo:any:acme/widgets` does offer `Keep` on
    `Throughput`.
  - That `Keep` opened `/new` with a task that gives the panel a
    question of its own and lists every panel with its `order_key`.
- **Links:**
  - `5 Waiting` links to
    `/?f=state:none:deferred|closed&f=state:any:queued`, which shows 5
    rows.
  - `Open` on `Running only` links to `/?f=state:any:running`, which
    shows 2 rows.
  - Browser Back restored focus to the link pressed in both cases.
- **Add panel:** it opened `/new` with `Add a panel to the dashboard
  "WT Dash"`.
- **Failure and empty states:**
  - With `/api/metrics` failing, `Throughput` says `The metrics report
    could not be loaded.`, and the other panels draw.
  - `WT Empty Dash` says `This dashboard has no panels yet. “Add panel”
    files the task that adds one.`

### 2.7 Report `[classic]`

- **Sections:** `WT Nine` draws `throughput`, `latency` and `backlog`.
  The six silent sections (`Merge queue`, `Prompts`, `Tool use`,
  `Agents`, `Sandboxes`, `CI waits`) are omitted, but all nine have
  `Move section N, “…”` handles.
- **Window:** `last 30 days` set `?window=30d`, which survived a reload.
  `Refresh` re-requested `/api/metrics?window=30d&buckets=24`.
- **Backlog chips against their lists:**

  | Chip | List | Rows | Back focus |
  | --- | --- | --- | --- |
  | `Proposed 4` | `/?f=state:any:proposed` | 4 | restored |
  | `Waiting 5` | `/?f=state:any:queued` | 5 | restored |
  | `Running 2` | `/?f=state:any:running` | 2 | restored |
  | `Asked 1` | `/?f=state:any:awaiting_reply&f=completed:empty:` | 1 | restored |

  The `task 20` reference links to `/tasks/20`.
- **Reorder:** tap-moving `Backlog` to the top drew `backlog,
  throughput, latency`, kept focus on `Move section 1, “Backlog”`, and
  survived a reload.
- **Repo scoping:** `WT Widgets Report` says `Measured over the tasks in
  acme/widgets.` and counts 17 filed.

### 2.8 `/next/tasks` Boards menu `[next]`

- **Opening it:**
  - The menu lists all 16 boards, and focus goes to the first.
  - It is a disclosure, so Tab moves through it, not the arrow keys.
  - Escape closes it and puts focus back on `saved-boards-btn`.
- **Names:** same-named boards are announced as `Triage, in WT Ops` and
  so on. A uniquely named board's folder is drawn after its name with
  `aria-hidden`, so it is announced by name alone.
- **Save current view:**
  - On `?tab=asked&q=retry`, it posted `{"kind":"list","query":"tab=asked&q=retry"}`,
    and the trigger then read `Boards WT Next Saved`.
  - Saving a second board with the same name and view is allowed. The
    menu then labels them `WT Next Saved (board-17), outside every
    folder` and `(board-18)`.
- **Delete:** `×` opens `Delete board WT Next Saved (board-18), outside
  every folder?`. Escape deletes nothing and returns focus to the
  trigger. Accepting sent exactly `DELETE /api/boards/board-17` and
  `/board-18`. Both were throwaway boards made in this run.
- **Applying a classic board:** applying `Triage, in WT Ops`
  (`f=state:any:running`) landed on an unnarrowed `/next/tasks`. This is
  the known two-grammar gap that task 5385 proposed as a follow-up
  (section 4 of its report). It was not re-filed.

### 2.9 `/next/tasks` Timeline view `[next]`

This is new ground (section 3).

- **Before the fix:**
  - On `?tab=proposed&view=timeline`, task 1 (filed 4h ago, still
    proposed) was drawn at `left 0%, width 4%`, and `WT held proposal`
    at `left 94%, width 4%`.
  - On `?tab=asked`, task 4 (asking for 2h) was drawn at `left 47.5%,
    width 4%`.
  - Every open task looked like it had ended moments after filing,
    while the classic gantt says `still open after 4h 1m` for the same
    task.
- **After the fix:** every open task's bar reaches the right-hand
  `now` edge (task 1 at 100%, task 4 at 100%). Task 7, whose work ended
  an hour after filing, still stops at 25%.
- **Opening a task:** clicking a timeline row's title opened the side
  pane at `?view=timeline&task=2`. That survived a reload. Back
  returned to `?view=timeline`.
- **Saving the view:**
  - Before the fix, `Save current view…` on `?tab=asked&view=timeline`
    saved `tab=asked`, so the board reopened as the list.
  - It now saves `tab=asked&view=timeline` and reopens on the Timeline.
  - A board with no view puts the list back.

### 2.10 Phone width, dark mode, console and network

- **Overflow:** `/boards`, `board-1`, `board-2`, the tree, the
  dashboard, the report and `/next/tasks?view=timeline` had no
  horizontal overflow (`scrollWidth == clientWidth`) at 390 light and
  390 dark.
- **Timeline bars:** on the Timeline tabs (proposed, asked and backlog),
  every bar sat inside its track at 1440 and 390, light and dark.
- **Console and network:** no `pageerror` in any context. The only
  `>= 400` responses were the injected `500`s.
- **Destructive actions:** the classic UI still offers no board delete.
  `/next/` asks first (2.8). Only throwaway boards were deleted.

## 3. Bugs found and fixed

### 3.1 `/next/` Timeline drew still-open tasks as finished

- **Reproduction:**
  1. Open `/next/tasks?tab=proposed&view=timeline` on a fresh `grain
     demo`.
  2. Task 1 (`Add a dark mode toggle`) has only `createdAt`. Its bar is
     4% wide, at the left edge.
- **Cause:** `timelineItems` ended every non-running task's bar at
  `endedAt || closedAt || updatedAt || createdAt`. A task still waiting,
  asking or held has only `createdAt`, so its bar ended where it
  started.
- **Fix:** the new `timelineEndMs` in `ui-ng/src/app/tasks/task-view.ts`
  ends a bar at `endedAt`, else `closedAt`, else now while the task is
  unfinished. Only a finished task (`completed`, `closed`, `failed`)
  with neither time recorded falls back to `updatedAt`, then
  `createdAt`. This matches the classic gantt (`ui/src/gantt.js`
  `spanOf`).

### 3.2 A board saved from the `/next/` Timeline came back as the list

- **Reproduction:**
  1. On `/next/tasks?tab=asked&view=timeline`, choose `Boards → Save
     current view…`.
  2. The posted query is `tab=asked`.
  3. Applying the board from the list view stays on the list.
- **Fix (`tasks-page.component.ts`):**
  - `currentBoardQuery` adds `view=timeline` when that view is open.
  - `applyBoard` navigates with `view` and sets the local view mode, so
    a board with no view reopens on the list.
  - The classic UI ignores the extra parameter.

### Regression coverage

- **Unit tests:** `ui-ng/src/app/tasks/task-view.spec.ts` covers
  `timelineEndMs` for open, ended and finished-without-time tasks.
  `tasks-page.component.spec.ts` expects `view: null` when applying a
  board with no view.
- **e2e tests** in `ui-ng/e2e/next-saved-boards.spec.js`:
  - `the Timeline view runs a still-open task's bar up to now` asserts
    with bounding boxes that the bar reaches the track's right edge.
  - `a board saved from the Timeline view opens on the Timeline again`
    asserts the posted query, then applies the board and a plain list
    board.
- **Checked against the unfixed source:** with the source fix reverted
  and the spec kept, both e2e cases failed.

## 4. Follow-ups

None new. The `/next/` versus classic board-grammar gap (2.8) is
already proposed by task 5385.

## 5. Tests run

| Suite | Result |
| --- | --- |
| `cd ui-ng && npx vitest run src/app/tasks/` | 3 files, 46 passed. Before updating the spec, one assertion failed on the new `view: null` key |
| `cd ui-ng && npm run lint` | clean |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js --project=desktop -g Timeline` (fix reverted) | 2 failed, as expected |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js --project=desktop --project=phone` | 5 passed |
| `cd ui-ng && npx playwright test e2e/next-full-system.spec.js e2e/next-saved-boards.spec.js e2e/next-task-interactions.spec.js` | 14 passed |

### Screenshots

`node ui/e2e/screenshot.mjs` shot
`/next/tasks?tab=proposed&view=timeline` and
`/next/tasks?tab=asked&view=timeline` at desktop and phone, light and
dark, on the base (`before`) and with the fix (`after`). They were not
committed. With no Gemini key they could not be described by `LOOK=1`.
The geometry checks in 2.10 stand in for looking at them.

## 6. Notes on standing guidance

- `ui/node_modules` from `make frontend` has no `@playwright/test`.
  `make ui-screenshots` and ad-hoc scripts need `npm ci` in `ui/` first.
  `ui-ng` needs `npm ci` too (the Makefile's `test-e2e-ng-suite` does
  this).
- `POST /api/auth/access-code` takes `{"code","name"}`. A body with
  `email` is refused with "give your email address with the access
  code".
