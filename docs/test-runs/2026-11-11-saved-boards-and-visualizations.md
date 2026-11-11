# Saved boards and task visualizations walkthrough — 2026-11-11 (task 4299)

An interactive walkthrough of the Boards index and every board
presentation (`list`, `kanban`, `gantt` / timeline, `tree`, `panels`
dashboard and `report`), driven in a real headless Chromium through
Playwright against a real `grain demo` server over a throwaway embedded
SQLite store. The walkthrough scripts were ad-hoc Node scripts using the
Playwright library from `ui/node_modules`, kept under `/var/tmp/walk` and
not committed. Yesterday's report
(`2026-11-10-saved-boards-and-visualizations.md`) was read first. This run
repeated every standing coverage item against today's `main` and then
looked further at how list views and Kanban columns agree on the backlog
order.

One bug was found: the task list drew an orphaned stacked review at the
bottom, out of its backlog place, so list moves landed in the wrong
place. It was reproduced deterministically, fixed in this change, and
covered by a Vitest unit test and a Playwright E2E test (section 3).

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `84f79bfda` (`main`, 2026-11-11), then the same tree with the fix in section 3 |
| Server | `./bin/grain demo -addr 127.0.0.1:0 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, `TMPDIR` pointed at a throwaway directory |
| Frontend | `cd ui && npm ci && npm run build`, embedded by `make grain` |
| Browser | Playwright Chromium 1234 headless (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`); desktop `1440×900`, `iPhone 13` (`390×844`, touch), light and dark |
| Toolchain | Go `1.26.10`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent, so layout was checked with bounding boxes rather than `LOOK=1` |

Sign-in went through the visible access-code gate (`Your email`,
`Access code`, `Continue`) as `alice@example.com`.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub, CI, base-branch CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | No external GitHub or CI from the sandbox |
| Agent execution for board creation | Verified up to the filed `grain/config` task (section 2.2). Throwaway boards for the presentation walkthroughs were created with `POST /api/boards` | `grain demo` runs no model agent, so a requested board never lands |
| Throwaway boards | `Triage` ×3 (loose list `f=state:any:queued`, `WT Ops` list `f=state:any:running`, `WT Zeta` kanban), `WT Zeta Alpha`, `WT Zeta Beta`, `WT Ops Queue`, `WT Submit Only` (`awaiting_submit` column), `WT Reply Only` (`awaiting_reply` column), `WT Widgets Tree` (`f=repo:any:acme/widgets`), `WT Dash` (states, two untitled lists, `Running only` list, `Week timeline` gantt 7d, `Throughput` metrics 7d), `WT Empty Dash`, `WT Nine` (all nine report sections), `WT Widgets Report` (`f=repo:any:acme/widgets`) | The demo seeds only one board of each built-in kind |
| Held proposal, `keep_running` task, twin titles | `POST /api/tasks`: `WT held proposal` (`proposedBy: "8"`, `holdForApproval: true`), `WT standing task` (`keepRunning: true`), two `Duplicate card title` tasks | Only an agent's `propose_task` creates a held proposal, and the demo seeds neither of the others |
| Viewer role | `page.route` on `GET /api/config` rewriting `access` to `role: "grain/viewer"`, scopes `settings:read`, `tasks:read` (the same stub `ui/e2e/config-change-card.spec.js` uses) | No identity-aware proxy in the sandbox. Server-side `403` enforcement was verified by the 2026-11-10 run and not repeated |
| Load failures and slow loads | Scoped `page.route` returning `500` for `GET /api/boards`, `GET /api/tasks` and `GET /api/metrics`, and a 1.5 s delay on `GET /api/metrics` | Real SQLite neither fails nor stalls on demand |

Across every context there were **no** uncaught `pageerror`s. The only
`>= 400` responses were the expected pre-sign-in `401` on `/`, the
injected `500`s, and one `404` described in section 4.

## 2. Steps and outcomes

### 2.1 Boards index, rail, folders, ordering, history and load failure

- Clicked the rail's `Boards` entry and reached `/boards`. Its `main`
  landmark lists the seeded `Board`, `Timeline`, `Report` and `Tree`, and
  the rail badge counted every board. `/boards` offers no search or sort
  of its own; its prose sends narrowing to the task list.
- The three `Triage` boards carry distinct accessible names:
  `Triage, outside every folder`, `Triage, in WT Ops`,
  `Triage, in WT Zeta`. Their `Move …` handles and `Move here, above …`
  slots are named the same way.
- Mouse drag inside `WT Zeta`, the third section: dragged
  `Move WT Zeta Beta` onto `WT Zeta Alpha`. This sent
  `PATCH /api/boards/board-9 {"folder":"WT Zeta","orderKey":1048576}`,
  the order became `Triage, WT Zeta Beta, WT Zeta Alpha`, and it survived a
  reload. A first attempt aimed at the handle's off-screen page
  coordinates and started no drag; that was a fault in the script, not the
  page, and scrolling into view first fixed it.
- Keyboard: `Enter` on `Move WT Ops Queue` showed 18 slots. `Escape`
  cancelled and left focus on the handle. Picking it up again and choosing
  `Move here, at the end of WT Zeta` filed it in `WT Zeta`, kept focus on
  `Move WT Ops Queue`, and survived a reload.
- History: `Enter` on `Timeline` opened `/boards/board-2`. Back returned
  focus to the `Timeline` link, Forward reopened the board, and a reload
  kept it.
- Load failure: with `GET /api/boards` answering `500`
  (`database disk image is malformed`), `/boards` showed
  `The boards could not be loaded (database disk image is malformed).`
  with `Retry`. It never said `No boards yet`, and `Retry` recovered all
  17 boards. A cold `/boards/board-1` under the same fault showed the same
  explanation.

### 2.2 Asynchronous board creation and viewer gating

- From `/?f=repo:any:acme/widgets&q=retry`: `Create options` offers
  `New task`, `New session` and `New board`. `New board` opened `/new` with
  a description that names `tables/board.json`'s fields and carries
  ``query: `q=retry&f=repo:any:acme/widgets` ``. With `Queue immediately`
  unticked, `Create task` filed task 28: `proposed`, repo `grain/config`.
- The Boards index header's `New board`, left on `Queue immediately`,
  filed task 29: `queued`, `approved: true`, `grain/config`. Its task
  page opened. The board count stayed at 17, because a board appears only
  once an agent's state-repo change lands.
- Viewer (stubbed `/api/config`):
  - `New board` on `/boards`, `Edit board` and `New board` on every board
    kind, `Add panel` on the dashboard, and the rail's `New task` and
    `New board` were all disabled.
  - `Keep` was absent on `WT Dash?q=retry`.
  - `/boards`, list, kanban, dashboard and report boards rendered zero
    `.task-drag-handle`s, zero `draggable="true"` items and zero `Move …`
    buttons.
  - `Columns` stayed enabled, as it should: it edits a per-browser
    setting.

### 2.3 List and kanban

- `Board` (`/boards/board-1`):
  - The columns read `Proposed, Waiting, Running, Asked, Failed, Merging`,
    with `Merging` holding 5 and 21.
  - Task 11 carries `.badge-blocked` (`Blocked on: #8 (running)`).
  - The `keepRunning` card shows `keeps running`.
  - The held proposal shows `held for approval by 8`, and issue 12 shows
    `imported`.
  - The board summarises `2 tasks in no column (Deferred, Closed).`
- Card moves:
  - Picking up the second `Duplicate card title` card showed slots
    identifying tasks by id and title (`Move here, above task 26,
    “Duplicate card title”, in Waiting`). Moving 27 above 26 kept focus on
    `Move task 27`.
  - Dragged card 23 above card 2 by mouse.
  - Both orders survived a reload. Opening task 11 from its card and
    pressing `Escape` restored focus to that card's link.
- Column editor:
  - Renaming `Proposed` to `Ideas WT` and pressing `Escape` raised the
    `You have unsaved changes here. Leave and discard them?` confirm, and
    dismissing it kept the dialog open.
  - `Save` and a reload showed `Ideas WT`.
  - `Move Proposed right` from the keyboard kept focus on that button and
    persisted.
  - `Reset to default` with `Save` restored the defaults.
- Custom columns:
  - `WT Submit Only` hides `Columns` and summarises
    `20 tasks in no column (Proposed, Waiting, Running, Asked for a reply, Merging, Deferred, Closed).`
  - `WT Reply Only` summarises
    `26 tasks in no column (Proposed, Waiting, Running, Asked to submit, Merging, Deferred, Closed).`
- List boards: `WT Zeta Alpha` (no query) shows the `keeps running`
  marker. The queued `Triage` board led to the bug in section 3.

### 2.4 Timeline and gantt

- `Range` offers `Last 24 hours` through `Everything`. `Lanes` offers
  `No lanes`, `By repo`, `By state` and `By author`.
- Under `Everything` with `By state` the lanes read
  `proposed, waiting, running, asked, merging, deferred, closed`, so
  **Asked follows Running**. The today marker (`.gantt-now`) and 28 bars
  were drawn.
- Both settings survived a reload. `Last 24 hours` showed
  `3 tasks outside this range.`
- Clicking the first bar opened `/tasks/1`, and Back restored focus to
  that row's link.

### 2.5 Tree

- `WT Widgets Tree` nests 23 under 22; 1, 3 and 8 under the director
  session 13; and 11, the held proposal and both duplicates under 8.
  `grain/config` tasks are excluded by the board's repo filter.
- Typing `pagination` gave
  `/boards/board-13?q=pagination&f=repo:any:acme/widgets`, showing 8 with
  11 under it. A reload kept both the search and the board's filter.
- `Enter` on 11 opened its task page, and Back restored focus to the
  `/tasks/11` link.

### 2.6 Dashboard

- `WT Dash` shows `State counts`, two `Task list`s, `Running only`,
  `Week timeline` and `Throughput`.
- `Keep` offers:
  - `?q=retry` and `?f=state:any:queued` offered `Keep` on the four
    inheriting task panels. They did **not** offer it on `Running only`,
    which has its own query, or on `Throughput`, whose report neither
    narrowing changes.
  - `?f=repo:any:acme/widgets` also offered `Keep` on `Throughput`.
- `Keep this narrowing on “Throughput”` and `Add panel` each opened a
  prefilled `/new` request (`Narrow the panel "Throughput" on the
  dashboard "WT Dash"`) listing every panel with its `order_key`. Nothing
  changed synchronously.
- Reorder:
  - Keyboard: `Move panel 6, “Throughput”` to above panel 1 left focus on
    `Move panel 1, “Throughput”`.
  - Mouse: dragging `Move panel 4` onto `State counts` sent a `PATCH` and
    answered `200`.
  - Both orders came back from `GET /api/boards` and after a reload.
- Links:
  - Each of the six `Open` links went to its own list
    (`/?f=state:any:running` for `Running only`, `/system/metrics?…` for
    `Throughput`). Back restored focus to the same link by index, even
    among three identical `Open` links to the same URL.
  - Every state count (`5 Proposed`, `7 Waiting`, `2 Running`, `10 Asked`,
    `0 Failed`, `2 Merging`) matched the row count on the list it opens,
    and Back restored focus to it.
- Empty and error states:
  - `WT Empty Dash` reads
    `This dashboard has no panels yet. “Add panel” files the task that adds one.`
  - `GET /api/tasks` `500` drew
    `The tasks could not be loaded (tasks 500).` with `Retry` in each task
    panel.

### 2.7 Report

- The seeded `Report` draws `Throughput`, `Latency`, `Capacity`,
  `Backlog` and `GitHub CI`, with no "nothing to report" notes for the
  silent sections it leaves out.
- `WT Nine` draws `Throughput`, `Latency` and `Backlog`. It accounts for
  `Merge queue`, `Prompts`, `Tool use`, `Agents`, `Sandboxes` and
  `CI waits` with `<Section>: nothing to report over this window.`
- `Window`:
  - Choosing `Last 30 days` set `?window=30d`.
  - `Refresh` fetched `/api/metrics?window=30d&buckets=24`.
  - A reload kept `Last 30 days`.
- Moving `Move section 9, “CI waits”` above section 1 left focus on
  `Move section 1, “CI waits”`. The stored order became
  `checks, throughput, …, sandboxes`.
- Scoping:
  - `WT Widgets Report` requested `/api/metrics?repo=acme%2Fwidgets…` and
    says `Measured over the tasks in acme/widgets.`
  - A typed `?f=state:any:running&q=retry` says the search and state
    filter are not in force. Because a URL narrowing replaces a board's
    own narrowing, the repo scope goes with it in that URL. The toolbar
    keeps the board's filter when it writes the URL, as the tree board
    showed, so this is by design.
- Backlog links, unscoped and under `acme/widgets`:
  - Every chip (`proposed 5/4`, `waiting 7/6`, `running 2/2`,
    `asked 1/1`) matched the row count of the list it opens.
  - The `task 20` reference opened `/tasks/20`.
  - Back restored focus to the originating link every time.
- Loading and errors:
  - A 1.5 s delayed `/api/metrics` showed `aria-label="Loading the report"`.
  - A `500` showed
    `The report could not be loaded (metrics 500). Refresh to try again.`
  - `Refresh` recovered the report. The transient `metrics 500` toast
    cleared within 8 s.

### 2.8 Phone width, history and destructive actions

- At `390×844`, `document.documentElement.scrollWidth` was `390` on
  `/boards`, `board-1`, `board-2`, `board-4`, `WT Dash`, `WT Nine` and the
  queued `Triage` list.
- Typing a search on list, kanban, timeline, tree and dashboard boards
  replaces the current history entry rather than adding one: Back leaves
  the board and Forward returns with the search intact. This is the same
  on every kind and looks deliberate, so it was not filed.
- The UI exposes no delete for boards, panels or sections; removing one
  is a config task. The only destructive confirmation in this area is the
  column editor's discard confirm (section 2.3). The throwaway objects
  went with the throwaway store when the server stopped.

## 3. Bug found and fixed

### 3.1 The task list drew an orphaned stacked task last, so list moves landed out of place

- **Reproduction** (demo with `-human-review -merge-queue-states`):
  1. On `/boards/board-1`, move the queued review task 23
     (`Review: Default new workspaces to the paid tier`, stacked on task
     22, which is `awaiting_submit`) above task 2. `GET /api/tasks` now
     lists the queued tasks as `23, 2, 11, 13, 20, …`.
  2. Open `/?f=state:any:queued`, or a list board narrowed to it. The rows
     read `2, 11, 20, 27, 26, 29, 23`: the review is drawn **last**.
  3. Pick up `Move task 2` and choose `Move here, to the end of the list`.
     The page sends `POST /api/tasks/reorder {"ids":["2"],"afterId":"23"}`,
     and after a reload task 2 is still at the **top**.
- **Cause**: `groupByStack` in `ui/src/components/TaskList.jsx` nests a
  stacked task under the task it was generated from. When that task is not
  in the list, it falls back to a top-level row, but it appended those
  rows after every other row. A list move is told the on-screen neighbours
  it lands between (`moveTo`), so any move next to the misplaced row was
  sent relative to the review's real place at the top of the backlog. The
  Kanban board and the dispatcher both use the backlog order, so the two
  views of the same tasks disagreed.
- **Fix**: `groupByStack` decides first which stacked tasks have their
  parent in view, then keeps every other row in the order the backlog
  gives it. A nested review or merge fix is still nested exactly as
  before.
- **Verification**: on the rebuilt binary the list reads `23, 11, 20, 2`.
  `Move here, to the end of the list` on task 11 sent `afterId: "2"`, and
  after a reload the list and `GET /api/tasks` both put 11 last.
  Bounding boxes at desktop and phone, light and dark, show every row's
  handle and title on the same x, and no horizontal overflow.
- **Regression coverage**:
  - `ui/src/components/TaskList.test.jsx`, `keeps a stacked review whose
    task is filtered out in its backlog place`: checks the row order and
    that a move to the end names the last real row. It fails on the
    unfixed code.
  - `ui/e2e/list-stacked-order.spec.js`: drives the real demo
    (`-human-review`). It finds the orphaned review through the API, moves
    it to the top, and checks the list matches the backlog. It then moves
    a row to the end through the visible slot, checks the request body and
    the reloaded order, and restores the backlog in `finally`. It fails on
    the unfixed frontend and passes on the fixed one.

## 4. Observations not filed

- Opening `/new` for a board request (target `grain/config`) logs a
  console `404` for `GET /api/repos/grain/config/github-branches`. The
  form handles it silently and shows an empty optional base branch, so
  the only effect is console noise.
- With the stubbed viewer role, `Create options → New session` stays
  enabled while `New task` and `New board` are disabled. Sessions are
  outside this walkthrough's area and the server's own gate was not
  exercised here, so this was not filed.
- Each failed panel on a dashboard shows its own `role="alert"`, and a
  global toast repeats the error. Screen readers announce it once per
  panel, which is noisy but accurate.

## 5. Tests run

- `npx vitest run src/components/TaskList.test.jsx`: 124 passed, including
  the new test (confirmed failing without the fix).
- `npx vitest run src/components`: 181 files, **2656 tests passed**.
- Playwright against the fixed build: `list-stacked-order`, `board`,
  `boards`, `saved-boards-walkthrough`, `keyboard-open-rows`, `phone`,
  `role-gated-actions`, `defaults`, `page-header-rail-walkthrough`,
  `cli-run-locations`, `audit-settings-layout`,
  `auto-upgrade-settings-layout` and
  `repository_release_memory_walkthrough`: **168 passed, 1 skipped**.

### 5.1 CI on the first push

The first CI run on this branch failed `ui e2e` on one test (1129
passed): `e2e/new-session-first-message-sending.spec.js:88` (phone), which
waits for a queued follow-up message in a new session. Sessions are
outside this change, and nothing it touches is on that page. Against a
local build of this branch, that spec and `list-stacked-order.spec.js`
passed with `--repeat-each=3` (15 passed), so the failure was treated as
a timing flake and CI was re-run.
