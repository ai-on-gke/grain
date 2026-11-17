# Saved Boards and Task Visualizations Walkthrough — 2026-11-17 (task 6589)

This walkthrough follows
[2026-11-15-saved-boards-and-visualizations.md](2026-11-15-saved-boards-and-visualizations.md).
Every coverage item ran in headless Chromium through Playwright against a
real `grain demo` server over a throwaway embedded SQLite store. Each step
used the UI's own controls. `/next/` was walked first wherever it has a page.
The classic UI at `/` was used only for pages that have not moved over.

One defect was found and fixed in `ui-ng/` (section 3):

- **Boards menu on `/next/tasks` silently dropped boards it cannot draw.**
  Applying a Kanban, timeline, report, dashboard or tree board did nothing
  visible. So did a list board narrowed in the classic grammar
  (`f=state:any:queued`). The URL stayed at bare `/next/tasks`, and the
  board's kind and narrowing were thrown away.

Nothing else misbehaved. The 77 classic board specs and the `/next/`
saved-boards spec passed before the fix as well as after it.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `07e43408c` (`main`, 2026-11-17 22:38Z; build stamp `v3.14.0-dev.132+07e4340`) |
| Fix commit | `a14432fec` on `grain/53549e42/task-6589-walk-through-saved-boards-and` |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, with `TMPDIR` set to a throwaway directory under `/var/tmp` |
| Frontends | `make frontend grain`. After the fix, `ui-ng` was rebuilt with `npm run build`, then `go build -o bin/grain ./cmd/grain` and the demo was restarted on a fresh store |
| Browser | Playwright Chromium 1234, headless (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Desktop `1440×900` and `iPhone 13` (`390×844`, touch), light and dark |
| Toolchain | Go from `go.mod`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent, so `LOOK=1` was unavailable. Layout was checked with bounding boxes and `scrollWidth`/`clientWidth` |

Sign-in posted the demo access code to `POST /api/auth/access-code` as
`alice@example.com`. The walkthrough scripts were ad-hoc Node scripts using
`ui/node_modules/@playwright/test`. They and their screenshots lived in
`/var/tmp/walk`, and none of them are committed.

`make frontend` installs with `npm ci --omit=dev`, which leaves no Playwright
in `ui/` or `ui-ng/`. Running `npm ci` in both was needed before any browser
work could start.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub, CI, base-branch CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | The sandbox has no external GitHub or CI |
| Agent that lands a requested board or panel | None. Checked up to the filed task (2.2, 2.7) | `grain demo` runs no model agent |
| Throwaway boards and tasks | `POST /api/boards` and `POST /api/tasks`, listed below | The demo seeds only `Board`, `Timeline`, `Report` and `Tree`, and no held or `keepRunning` task |
| Viewer role | Scoped `page.route` on `GET /api/config`. It set `access` to `{role: "grain/viewer", full: false, scopes: [<:read scopes only>]}` | No identity-aware proxy in the sandbox |
| Load failures | Scoped `page.route`: `500 {"error":"store unavailable"}` for `GET /api/boards`, and `500 {"error":"metrics broken"}` for `GET /api/metrics*` | Real SQLite does not fail on demand |

Throwaway fixtures created in the first store:

- **Three `Triage` boards:** a loose list (`f=state:any:queued`), a list in
  `WT Ops` (`f=state:any:running`), and a kanban in `WT Zeta`
- **More `WT Zeta` lists:** `WT Zeta Alpha`, `WT Zeta Beta` and
  `WT Zeta Gamma`
- **`WT Submit Only` kanban:** columns `Asked to submit` (`awaiting_submit`)
  and `Queued` (`queued`)
- **`WT Widgets Tree`:** `kind: tree`, `f=repo:any:acme/widgets`
- **`WT Dash`:** panels `State counts` (states), `Running only` (list,
  `f=state:any:running`), `Week timeline` (gantt, `7d`) and `Throughput`
  (metrics)
- **`WT Empty Dash`:** a dashboard with no panels
- **Reports:** `WT Nine`, with sections `throughput`, `latency`, `backlog`,
  `merges`, `prompts`, `tools`, `agents`, `sandboxes` and `checks`; and
  `WT Widgets Report`, with `f=repo:any:acme/widgets`
- **Tasks:** `#28 WT held proposal` (`proposedBy: "8"`,
  `holdForApproval: true`) and `#29 WT standing task` (`keepRunning: true`,
  approved into `queued`)

After the fix, a second store got `Triage` (`f=state:any:queued`), `Triage`
in `WT Ops` (`tab=running`), `WT Asked` (`tab=asked`), `WT Dash` (panels) and
`WT Very long dashboard name for phone widths` (report). Both stores were
thrown away with their `TMPDIR`.

## 2. Steps and outcomes

`[next]` marks a step run against `/next/` (`ui-ng/`). `[classic]` marks one
run against `/` (`ui/`).

**Which pages have moved to `/next/`:** `ui-ng/src/app/shell/nav.ts` and
`app.routes.ts` are unchanged from 2026-11-15.

- **On `/next/`:**
  - `/next/tasks`: the `Boards` menu, the save bar, the rail's
    `New → New board`, the List view and the Timeline view
  - `/next/workstreams`: goal and filer trees
  - `/next/telemetry`: the measurement report
- **Classic only:** these have no `/next/` page yet, and `/next/boards`
  falls through `**` to `/next/inbox`.
  - the `/boards` index
  - `/boards/:id` for every kind: Kanban, Gantt, Tree, dashboards and
    report boards
  - the `/new` board-request form

### 2.1 Boards index, folders, ordering and failure `[classic]`

- **Order and accessible names:** `/boards` lists the loose boards first:
  `Board`, `Timeline`, `Report`, `Tree`, `Triage`. Then come the `WT Ops`
  and `WT Zeta` folders. The three `Triage` links have distinct names
  (`Triage, outside every folder`, `Triage, in WT Ops`,
  `Triage, in WT Zeta`), and so do their handles (`Move Triage, in WT Ops`
  and so on). Uniquely named boards keep their plain names.
- **What the index offers:** each row shows its kind and query. The index
  has no search, sort or count controls; none are offered, so none were
  tested.
- **Mouse drag in a lower section:** dragging `Move WT Zeta Gamma` onto
  `WT Zeta Alpha` in `WT Zeta`, the third section, reordered it to
  `Gamma, Alpha, Beta`. The order survived a reload, and the `/next/` menu
  showed it too.
- **Keyboard and tap move:** pressing `Space` on `Move WT Zeta Beta`
  announced `Moving 1 board — choose where it goes`. The slots name boards
  by their distinct labels, for example
  `Move here, above Triage, in WT Ops`, `Move here, at the end of WT Ops`
  and `Move here, outside every folder`. No-op slots are left out.
  `Escape` cancelled.
- **Failed `GET /api/boards`:** the index says
  `The boards could not be loaded (store unavailable).` and offers `retry`.
  It does not say "No boards yet".

### 2.2 Asking for a board, and viewer gating `[classic]` and `[next]`

- **From the Boards index header** `[classic]`: `New board` opened `/new`
  with a template already written. The title was `Add a board`, the repo
  `grain/config`, and the description named the fields to fill in. I filled
  in the name `WT Requested` and the folder `WT Ops`, then pressed
  `create task`. That posted `POST /api/tasks` with `repo: "grain/config"`
  and `approved: true`, and filed `#27 WT board request from index` in
  `queued` against `grain/config`. That is the asynchronous contract: a
  task, and no direct `POST /api/boards`.
- **From the classic rail's create menu** `[classic]`: on
  `/tasks?q=retry&repo=acme%2Fwidgets`, `Create options` offered
  `new task`, `new session` and `new board`. `new board` opened `/new` with
  ``- query: `q=retry&f=repo:any:acme/widgets` `` already written, so the
  narrowing on screen came along.
- **From the `/next/` rail** `[next]`: `New → New board` opens the save bar
  on `/next/tasks`. That path is unchanged since 2026-11-15, and the spec
  `New board from the rail keeps the current Tasks narrowing…` passed.
- **Viewer role** (`grain/viewer`, read scopes only):
  - `/boards` `[classic]`: no `New board`, and no drag handles (0
    draggable).
  - `/boards/board-1` `[classic]`: no `Edit board` and no drag handles.
    Only `Columns` and `Edit columns` remain, and those edit this browser's
    own column layout (a localStorage key), not the shared board.
  - `/boards/board-5` `[classic]`: no `Edit board`.
  - Create menu `[classic]`: `New task` and `New board` are disabled, and
    `New session` stays enabled.
  - Dashboard `[classic]`: I opened a two-panel dashboard (`states` and
    `metrics`) narrowed with `?f=repo:any:acme/widgets`, which offers `Keep`
    to a writer. The viewer saw no `Keep`, `Add panel`, `Move panel …`,
    `Edit board` or `New board` button.
  - `/next/tasks` `[next]`: save and delete are hidden. The `/next/` spec's
    role checks passed.

### 2.3 Kanban `[classic]`

- **Columns on `board-1`:** `Proposed 3`, `Waiting 7`, `Running 2`,
  `Asked 10`, `Failed 0` and `Merging 2`. The merge-queue column reads
  "Merging". The note under the board reads
  `4 tasks in no column (Deferred, Closed).`
- **Card chips:**
  - `#11` is blocked and wears `badge-blocked` with
    `aria-label="Blocked on: #8 (running)"`.
  - `#28` carries `held for approval by 8`.
  - `#29` carries `keeps running`.
  - `#12 [#42] Search results drop the last page` carries `imported`.
- **Custom columns:** `WT Submit Only` collects only `awaiting_submit`, and
  its note reads `13 tasks in no column (Proposed, Running, Asked for a
  reply, Merging, Deferred, Closed).` "Asked for a reply" is kept apart from
  the "Asked to submit" column.
- **Tap or keyboard move:** I clicked `Move task 22` (handles are named by
  task id). The slots read
  `Move here, above task 2, “Fix flaky retry test”, in Waiting`, and so on,
  ending with `Move here, to the end of Waiting`. Choosing the first slot
  moved `#22` to the top. The order `22,2,11,20,26,27,29` survived a reload.
  `Enter` on a focused handle picked it up: the handle became
  `Stop moving task 29`, `aria-pressed=true`. `Escape` put it down.
- **Mouse drag:** dragging card `#20` onto card `#2` sent
  `POST /api/tasks/reorder {"ids":["20"],"afterId":"22","beforeId":"2"}`,
  and the order survived a reload. Both cards have to be on screen, so this
  needed a `1440×1800` viewport.
- **Column editor:** `Move Waiting left` plus `+ Add column` (`WT Col`)
  plus `Save` gave `Waiting, Proposed, Running, Asked, Failed, Merging,
  WT Col` across a reload. `Reset to default` restored the defaults.
- **Task detail:** a card's title link opens `/tasks/<id>`.

### 2.4 Timeline (gantt) `[classic]` and `[next]`

- **Pickers:** `Range` offers `Last 24 hours`, `Last 7 days`,
  `Last 30 days`, `Last 90 days` and `Everything`. `Lanes` offers
  `No lanes`, `By repo`, `By state` and `By author`.
- **Lanes by state:** `proposed, waiting, running, asked, merging, deferred,
  closed`, so Asked comes after Running.
- **Reload:** `Last 30 days` and `By state` survived it. They are this
  browser's settings by design, so the URL stays `/boards/board-2`.
- **Bars and markers:** the `gantt-now` marker sits at `left: 100%`. The
  bars carry titles such as `Proposed: waiting for 4h 11m, never
  dispatched`, and `1 task outside this range.` is shown.
- **Opening a task:** clicking `#3`'s bar opened `/tasks/3`. Back returned
  to `/boards/board-2` with focus on that row's title link.
- **`/next/` Timeline** `[next]`: `/next/tasks?view=timeline&tab=all`
  rendered with no errors.

### 2.5 Tree `[classic]` and `[next]`

- **Nesting:** on `board-4` and `WT Widgets Tree`, the nesting is
  `26<25`, `1<13`, `3<13`, `8<13`, `11<8` and `28<8`.
- **Narrowing:** `WT Widgets Tree` shows the bar
  `repo is any of acme/widgets`. It leaves out `#27`, which is in
  `grain/config`, and the 6 tasks with no repo. It was identical after a
  reload.
- **Opening a task:** clicking a row's title opened `/tasks/2`. Back
  returned to `/boards/board-13?f=repo:any:acme/widgets` with focus on the
  row's id link.
- **`/next/workstreams`** `[next]` rendered `2 streams shown` with no
  console errors.

### 2.6 Dashboard `[classic]`

- **`WT Dash`:** it opens on its own question,
  `state is none of deferred, closed`.
  - `State counts`: 24, linking each state.
  - `Running only`: 2 tasks, linking `/?f=state:any:running`.
  - `Week timeline`: `Showing 10 of 24 in this range.`
  - `Throughput`: `26 filed`, `11 completed`, linking
    `/system/metrics?f=…`.
- **Whole-board narrowing:**
  - **Typing `retry`:** this gave `?q=retry&f=state:none:deferred|closed`.
    `Keep this narrowing on …` was offered on `State counts` and
    `Week timeline` only. It was not offered on `Throughput`, whose report
    a search would not change.
  - **Panel with its own query:** `Running only` has one, so the search
    did not narrow it, as designed.
  - **`?f=repo:any:acme/widgets`:** this offers `Keep` on `Throughput` as
    well, and its Open link becomes `/system/metrics?f=repo:any:acme/widgets`.
- **Reorder:** tapping `Move panel 4, “Throughput”` and then the first
  `Move here, above panel 1, “State counts”` sent a `PATCH
  /api/boards/board-14` with the new `orderKey`s. Focus stayed on
  `Move panel 1, “Throughput”`, and the order survived a reload. I then
  restored it the same way.
- **Links and Back focus:**
  - `10 Asked` opened `/?f=state:none:deferred|closed&f=state:any:awaiting_reply|awaiting_submit`
    with 10 rows. Back returned focus to that `10 Asked` link.
  - The panel `Open` link to `/?f=state:any:running`, followed by Back,
    returned focus to that `Open` link.
- **Add panel:** it opened `/new` with `Add a panel to the dashboard "WT
  Dash"`. The description lists the current panels with their `order_key`s,
  so this is the asynchronous request path. I did not submit it.
- **Empty and error states:**
  - `WT Empty Dash` says `This dashboard has no panels yet. “Add panel”
    files the task that adds one.`
  - With `/api/metrics` returning 500, the throughput panel says
    `The metrics report could not be loaded.` and the other panels still
    render.

### 2.7 Report `[classic]` and `[next]`

- **Seeded `Report` (`board-3`):** sections `Throughput`, `Latency`,
  `Capacity`, `Backlog` and `GitHub CI`.
  - Choosing `Last 30 days` wrote `?window=30d`.
  - `Refresh` re-fetched `GET /api/metrics?window=30d&buckets=24`.
  - Reload kept `?window=30d` and `Last 30 days`.
- **`WT Nine`:** `Throughput`, `Latency` and `Backlog` render in full. The
  silent ones collapse to one line each, for example
  `Merge queue: nothing to report over this window.`, then `Prompts`,
  `Tool use`, `Agents`, `Sandboxes` and `CI waits`. The same happens at
  `?window=24h`.
- **Reordering `WT Nine`:** `Move section 9, “CI waits”` followed by the
  first slot sent `PATCH` with `checks` at `orderKey -1048576`. Focus
  stayed on `Move section 1, “CI waits”`, and the order survived a reload.
- **Repo scoping:** `WT Widgets Report` says
  `Measured over the tasks in acme/widgets.`, and its backlog links carry
  `f=repo:any:acme/widgets`.
- **Backlog chips against the lists they open:**

  | Report | Chip | Rows in the linked list | Ids |
  | --- | --- | --- | --- |
  | `board-3` | `Proposed 3` | 3 | `1,12,28` |
  | `board-3` | `Waiting 7` | 7 | `22,20,2,11,26,27,29` |
  | `board-3` | `Running 2` | 2 | `3,8` |
  | `board-3` | `Asked 1` | 1 | `4` |
  | `WT Widgets Report` | `Proposed 3` | 3 | `1,12,28` |
  | `WT Widgets Report` | `Waiting 6` | 6 | `22,20,2,11,26,29` |
  | `WT Widgets Report` | `Running 2` | 2 | `3,8` |
  | `WT Widgets Report` | `Asked 1` | 1 | `4` |

  Every count matches its list, and Back returned focus to the chip each
  time. The task reference `task 22` links to `/tasks/22`.
- **`/next/telemetry?window=30d`** `[next]`: `Refresh` re-fetched
  `/api/metrics?window=30d`, and the window survived a reload.

### 2.8 `/next/tasks` Boards menu `[next]`

- **Opening the menu:** it lists every board, with focus on the first one.
  Same-named boards carry their folder in the accessible name.
- **Before the fix:** I applied, in turn:
  - `Board` (kanban)
  - `Timeline` (gantt)
  - `Report`
  - `Tree`
  - `WT Dash` (panels)
  - `WT Widgets Tree` (`f=repo:any:acme/widgets`)

  Each one left the page at bare `/next/tasks`, showing the unchanged
  Backlog list, with no request and no message (section 3).
- **After the fix:**
  - **Labels:** those rows read `Board kanban ↗`, `Timeline timeline ↗`,
    and so on, with the title `Opens on the classic Boards page`. Their
    accessible names end `…, kanban, opens on the classic Boards page`.
    The loose `Triage` (`f=state:any:queued`) reads `filtered list ↗`.
  - **Applying one** opens `/boards/<id>` on the classic UI, which draws
    it.
  - **`/next/` boards stay put:** `WT Asked` (`tab=asked`) and `Triage` in
    `WT Ops` (`tab=running`) keep their plain names and still apply on
    `/next/tasks`.
- **Layout after the fix:** I measured every row at `1440×900` and
  `390×844`, light and dark.
  - Every apply button sits inside the menu, and the `×` stays to its
    right.
  - `WT Very long dashboard name for phone widths` ellipsizes its name
    (name `789..983`, folder `989..1040`, hint `1046..1097` in a
    `779..1107` button on desktop; `32..225`, `231..281`, `287..338` in
    `22..348` on phone), so the `report ↗` hint stays on screen.
- **Delete:** the `×` confirm flow was exercised only on the spec's own
  throwaway boards, and the `/next/` spec covers it. Nothing seeded was
  deleted.

### 2.9 Console and network

None of the walks logged a console error, a page error or a failed request,
apart from the deliberate 500s.

## 3. Bug found and fixed

### 3.1 `/next/tasks` Boards menu dropped boards it cannot draw

- **Reproduction:**
  1. Run `grain demo`.
  2. Open `/next/tasks` and click `Boards`.
  3. Click `Report`, or `Board`, `Timeline`, `Tree`, any dashboard, or any
     list board whose query uses `f=` or `sort=`.
- **Observed:** the URL stays `/next/tasks` and the page shows the unchanged
  Backlog list. `applyBoard` read only `tab`, `group`, `q`, `repo` and
  `view` out of `board.query`, and never looked at `board.kind`.
- **Expected:** a board should open as what it was saved as. `/next/` has
  no Kanban, Gantt, report, dashboard or tree board page, and no `f=`
  grammar. The rail's own rule (`shell/nav.ts`) is that a page that has not
  moved over is a plain link into the classic UI, so those boards should
  open there and say so.
- **Fix:**
  - **Deciding:** `ui-ng/src/app/tasks/board-labels.ts` gains
    `nextBoardParams(board)`. It returns the `/next/tasks` query for a
    `list` board whose query uses only `tab`, `group`, `q`, `repo` and
    `view`, and `null` for anything else. `classicBoardHint` supplies the
    Boards index's word for the kind.
  - **Applying:** `applyBoard` sends a `null` board to `/boards/<id>`.
  - **The menu row:** it shows a muted `<kind> ↗` hint that does not shrink
    (the name ellipsizes first), a title, and an accessible name ending
    "opens on the classic Boards page".
  - **The rule:** the design book records it under "Into the classic UI".
- **Coverage:**
  - **Unit:** `board-labels.spec.ts` gains 4 tests.
  - **E2E:** `ui-ng/e2e/next-saved-boards.spec.js` gains
    `a board /next/tasks cannot draw opens on its classic page and says so`,
    on desktop and phone. It checks the names, the hint's bounding box
    inside a long-named button, navigation to `/boards/<id>` for a kanban
    and an `f=` list, and that a `tab=asked` list still applies on
    `/next/tasks`.
  - **Without the fix**, I rebuilt `ui-ng` with the component change
    reverted, and both projects failed at the accessible-name assertion.
- **Classic UI:** unchanged, because the classic menu already opens
  `/boards/<id>`.

## 4. Follow-ups

None newly filed.

- **Board pages on `/next/`:** `/next/` still has no Boards index, no
  `/boards/:id` presentations, and no reading of `f=`/`sort=`. These remain
  the classic-UI migration backlog noted on 2026-11-15. With this fix the
  menu at least hands those boards to the classic UI rather than dropping
  them.
- **`/next/boards`:** it still falls through to `/next/inbox`. A redirect to
  the classic `/boards` would belong with that migration.

## 5. Tests run

| Suite | Result |
| --- | --- |
| `cd ui && npx playwright test --project=chromium e2e/saved-boards-walkthrough.spec.js e2e/boards.spec.js e2e/board.spec.js e2e/gantt.spec.js e2e/work-tree.spec.js e2e/report-backlog-links.spec.js e2e/board-return-focus.spec.js e2e/boards_workflows.spec.js` (base) | 77 passed |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js` (base) | 10 passed |
| Same spec with the component fix reverted, `-g "cannot draw"` | 2 failed (desktop, phone), as expected |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js` (fix) | 12 passed |
| `cd ui-ng && npx vitest run src/app/tasks/` | 4 files, 66 passed |
| `cd ui-ng && npm run lint` / `npm run build` | clean |
| `cd ui-ng && npx playwright test` (full `/next/` suite, fix) | see the PR's `ui-ng e2e` job and the closing note |
