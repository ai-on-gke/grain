# Saved Boards and Task Visualizations Walkthrough — 2026-11-20

Task 7545 follows
[2026-11-18-saved-boards-and-visualizations.md](2026-11-18-saved-boards-and-visualizations.md).
Every coverage item ran in headless Chromium through Playwright against a
real `grain demo` server over a throwaway embedded SQLite store, driving
visible controls (clicks, drags, Tab/Space/Enter, Back/Forward, reload).
Steps ran on `/next/` wherever a `/next/` surface exists, and on the
classic UI at `/` only where the page has not moved over yet.

**Result: no new defects.** Every scripted check passed on `2c44ffa0b`.
The change this run lands is this report. Three observations that are
not defects are recorded in section 4.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Commit | `2c44ffa0b` (`main`, 2026-11-20), reported by the server as `v3.14.0-dev.645+2c44ffa` |
| Build | `make frontend grain` (`ui/` and `ui-ng/` built and embedded in `bin/grain`) |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, `TMPDIR=/var/tmp/walk/tmp` (throwaway SQLite) |
| Browser | Playwright Chromium (headless, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), `1440×900` light, plus `iPhone 13` (`390×844`, touch) dark for layout |
| Toolchain | Go 1.26.10, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent, so `LOOK=1` was unavailable; layout was checked with `scrollWidth`/`clientWidth` and bounding boxes |

Sign-in posted `{code, name: "alice@example.com"}` to
`POST /api/auth/access-code`. The walkthrough scripts (`seed.mjs`,
`walk.js`, probes) and their output stayed in `/var/tmp/walk`, outside the
repository; no screenshots or browser artifacts are committed.

### Which UI each area ran against

`ui-ng/src/app/shell/nav.ts` and `ui-ng/src/app/app.routes.ts` have no
Boards, board, dashboard or report route, so these have **no `/next/` page
yet** and ran on classic: the Boards index, List/Kanban boards, Timeline
(Gantt) boards, Tree boards, Dashboards and Reports. On `/next/` the run
covered the Boards menu on `/next/tasks` (apply, save, delete), the rail's
Create → New board, viewer gating, and the `/next/telemetry` window.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| External GitHub and CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | No external GitHub/CI credentials in the sandbox |
| Agent landing a `grain/config` board PR | Checked up to the filed request task (#29) | `grain demo` runs no model worker |
| More board kinds, folders and task states | Seeded through `POST /api/boards` and `POST /api/tasks` (below) | Stock `grain demo` seeds four boards and no held or `keepRunning` task |
| Viewer role | A scoped `page.route` on `GET /api/config` setting `access` to `grain/viewer` with read scopes only (`tasks:read`, `boards:read`, `metrics:read`, `repos:read`, `ci:read`) | No identity-aware proxy in the sandbox |
| Store and metrics read failures | A scoped `page.route` returning `500 {"error":"store unavailable"}` on `GET /api/boards`, and `500 {"error":"metrics broken"}` on `GET /api/metrics*` | Embedded SQLite does not fail on demand |

Throwaway fixtures (board ids as assigned on this run):

- Same-named `Triage`: loose `list` `f=state:any:queued` (board-5), `WT Ops`
  `list` `f=state:any:running` (board-6), `WT Zeta` `kanban` (board-7).
- `WT Zeta Alpha`/`Beta`/`Gamma` (`list`, board-8..10).
- Custom-column Kanbans: `WT Submit Only` (`Asked to submit` →
  `awaiting_submit`, `Queued`; board-11) and `WT Reply Only` (`Asked for a
  reply` → `awaiting_reply`, `Queued`; board-12).
- `WT Widgets Tree` (`tree`, `f=repo:any:acme/widgets`, board-13).
- `WT Dash` (board-14): `State counts`, `Running only`
  (`f=state:any:running`), `Week timeline` (7d), `Throughput` (metrics, 7d);
  `WT Empty Dash` (board-15).
- Reports: `WT Nine` (all nine named sections, board-16), `WT Widgets
  Report` (`f=repo:any:acme/widgets`, board-17).
- `WT Gantt Widgets` (`gantt`, `f=repo:any:acme/widgets`, board-18).
- Tasks: `WT held proposal` (#27, `holdForApproval`, `proposedBy: "8"`),
  `WT standing task` (#28, `keepRunning`, approved to `queued`).
- Created through the UI during the run: board-19 (`/next/` Save board,
  then deleted through its confirmation) and task #29 (classic New board
  request).

## 2. Steps and outcomes

### 2.1 `/next/` `[next]`

| Step | Outcome |
| --- | --- |
| `/next/tasks` → Boards menu | All 18 boards listed. The three `Triage` buttons had distinct names: `Triage, outside every folder, filtered list, …`, `Triage, in WT Ops, filtered list, …`, `Triage, in WT Zeta, kanban, …`; every classic-only kind said `opens on the classic Boards page` |
| Apply seeded `Board` (Kanban) | Landed on `/boards/board-1` |
| Apply `Triage, in WT Ops` | Landed on `/boards/board-6?f=state:any:running` (the classic list it asks for) |
| Rail Create → `New board` from `/next/inbox` | Opened `/next/tasks` with the `Save current view as board:` bar. `Save board` (name `WT next throwaway`) → `POST /api/boards 201` (board-19); listed after reload |
| Delete board-19 via its `×` | Confirmation dialog: `Delete the saved board "WT next throwaway"? Boards are shared settings, so it goes for everyone on this deployment, not just this browser.` `Delete board` removed it from `/api/boards` |
| `/next/telemetry` → `30d`, `Refresh` | URL `?window=30d`; requested `/api/metrics?window=30d`; survived reload |
| Viewer on `/next/tasks` | Create menu had no `New board`; Boards menu had `0` delete buttons and no `Save current view…` |

### 2.2 Classic Boards index, ordering and history `[classic]`

- Rail read `boards 18`, matching `GET /api/boards` (18). Clicking it opened
  `/boards`. The index offers no search or sort controls.
- Order: the four seeded boards, loose boards, then folders `wt ops`,
  `wt zeta`. The three `Triage` links had distinct accessible names
  (`Triage, outside every folder` / `, in WT Ops` / `, in WT Zeta`).
- **Tap move in `WT Zeta`** (a section below the first): `Move WT Zeta
  Gamma` offered 19 slots named by neighbour and folder, none beside Gamma
  itself. `Move here, above WT Zeta Alpha` sent `PATCH
  /api/boards/board-10 200`, kept focus on `Move WT Zeta Gamma`, and after
  reload the folder read `Gamma, Alpha, Beta`.
- **Drag in `WT Zeta`**: dragging `Move WT Zeta Alpha` onto `WT Zeta Gamma`
  persisted `Alpha, Gamma, Beta` across reload.
- **History**: `WT Zeta Beta` → `/boards/board-9`; `Back` restored `/boards`
  with focus on the `WT Zeta Beta` link; `Forward` returned to board-9,
  which survived reload.
- **Keyboard**: Tab from the page reaches `new board`, then each board's
  `Move …` handle followed by its link, in display order.
- **Failed `GET /api/boards`**: `The boards could not be loaded (store
  unavailable).`; never `No boards yet`.

### 2.3 Asynchronous creation and viewer gating `[classic]`

- **Header `new board`** opened `/new` prefilled `Add a board` / `Add a
  saved board to grain. The board: - name: … - folder: … - kind: list …`.
  `/api/boards` was unchanged. Submitting it filed task #29 (`queued`,
  repo `grain/config`) and still created no board. `/next/task/29` showed
  `#29 Add a board`, `Waiting`, `grain/config`, `Queued: grain dispatches
  it on a coming cycle.` with the prewritten prompt.
- **Rail `Create options` → `New board`** from
  `/tasks?f=repo:any:acme/widgets` opened `/new` with the narrowing
  (`acme/widgets`) carried into the request.
- **Viewer**: `/boards` `new board` disabled, rail menu `New board`
  `aria-disabled`, `0` board move handles; board-1 `edit board` disabled,
  `0` card move handles; `WT Dash?f=repo:any:acme/widgets` `add panel` and
  all three `Keep` buttons disabled, `0` panel move handles.

### 2.4 List and Kanban `[classic]`

- **board-1 markers**: `merging` column present; #11 carried the blocked
  oval (`Blocked on: #8 (running)`); `keeps running` (#28), `held for
  approval by 8` (#27) and `imported` (#12) chips present.
- **Open**: `Fix flaky retry test` → `/tasks/2`; `Back` restored focus to
  that card link.
- **Tap move by id**: `Move task 28` offered `Move here, above task 2, “Fix
  flaky retry test”, in Waiting` (slots name id and title). Choosing it sent
  `POST /api/tasks/reorder {"ids":["28"],"beforeId":"2"}`, kept focus on
  `Move task 28`, and the Waiting order after reload was `28, 2, 11, 20,
  22, 26, 29`.
- **Keyboard move**: `Space` on `Move task 11` → `Stop moving task 11`;
  `Escape` → `Move task 11`. `Space`, `Tab` to `Move here, above task 22,
  …`, `Enter` sent `POST /api/tasks/reorder
  {"ids":["11"],"afterId":"20","beforeId":"22"}`.
- **Edit columns**: `Remove Merging` (the overlay also offers `Move Running
  left/right`), `Save`, reload → `6 tasks in no column (Merging, Deferred,
  Closed).`; `Reset to default` → `4 tasks in no column (Deferred,
  Closed).`
- **Custom columns** (no `edit columns` offered on either):

  | Board | "In no column" summary |
  | --- | --- |
  | `WT Submit Only` | `13 tasks in no column (Proposed, Running, Asked for a reply, Merging, Deferred, Closed).` |
  | `WT Reply Only` | `19 tasks in no column (Proposed, Running, Asked to submit, Merging, Deferred, Closed).` |

- **List board** `Triage, in WT Ops` drew rows #3 and #8 at
  `/boards/board-6?f=state:any:running`; the #3 row opened `/tasks/3`.

### 2.5 Timeline (Gantt) `[classic]`

- board-2 `Range` offered `Last 24 hours / Last 7 days / Last 30 days / Last
  90 days / Everything`; `Lanes` offered `No lanes / By repo / By state / By
  author`.
- `Last 24 hours` + `By state` survived a reload at the unchanged URL
  `/boards/board-2` (these are per-browser settings by design).
- One `.gantt-now` marker; `4 tasks outside this range.`; lanes ran
  `proposed, waiting, running, asked, merging, deferred` (Asked after
  Running). The demo seeds no dependency edges in range, so none are drawn.
- The bar for #1 opened `/tasks/1`; `Back` restored focus to that bar.
- `WT Gantt Widgets` kept `/boards/board-18?f=repo:any:acme/widgets`
  across reload.

### 2.6 Tree `[classic]`

`WT Widgets Tree` opened at `/boards/board-13?f=repo:any:acme/widgets` and
kept it across reload. Tasks nested under the tasks that filed them: #1,
#3, #8 and #26 at depth 2 (#26 under #25), #11 (under #8) and #27 at depth
3; no `grain/config` row appeared. The #11 row opened `/tasks/11`.

### 2.7 Dashboard `[classic]`

- `State counts` `2 running` → `/?f=state:none:deferred|closed&f=state:any:running`
  (2 rows); `Back` restored focus to that count link.
- Typing `pagination` set `?q=pagination&f=state:none:deferred|closed`,
  which survived reload. `Keep` was offered on `State counts` and `Week
  timeline`, **not** on `Throughput`.
- A panel `Open` and, after narrowing, the `1 running` count link each
  restored focus to themselves on `Back`.
- `add panel` opened `/new` (the request task form).
- **Reorder**: `Move panel 4, “Throughput”` offered `Move here, above panel
  1, “State counts”` …; choosing it sent `PATCH /api/boards/board-14 200`
  and after reload the order was `throughput, state counts, running only,
  week timeline`.
- `WT Empty Dash`: `This dashboard has no panels yet. “Add panel” files the
  task that adds one.`
- With `/api/metrics` → `500`, `Throughput` said `The metrics report could
  not be loaded.`; the other three panels rendered.

### 2.8 Report `[classic]`

- `WT Nine`: window `Last 30 days` set `?window=30d`; `refresh` requested
  `/api/metrics?window=30d&buckets=24`; both survived reload.
- Drew `Throughput`, `Latency`, `Backlog`. The six silent sections the board
  names explicitly each kept a move handle and a one-line `…: nothing to
  report over this window.` (by design, `ui/src/report.js`). The seeded
  whole-report `Report` (board-3) omits silent sections entirely, drawing
  `throughput, latency, capacity, backlog, github ci`.
- **Reorder**: `Move section 3, “Backlog”` → `Move here, above section 1,
  “Throughput”` persisted `backlog, throughput, latency` across reload.
- **Backlog chips vs their lists** (each `Back` restored focus to the chip):

  | Chip | Link | Rows in list |
  | --- | --- | --- |
  | `proposed 3` | `/?f=state:any:proposed` | 3 |
  | `waiting 7` | `/?f=state:any:queued` | 7 |
  | `running 2` | `/?f=state:any:running` | 2 |
  | `asked 1` | `/?f=state:any:awaiting_reply&f=completed:empty:` | 1 |

- Task reference `task 22` opened `/tasks/22`; `Back` restored focus to it.
- `WT Widgets Report` (`f=repo:any:acme/widgets`): every chip link carried
  `f=repo:any:acme/widgets`.

### 2.9 Widths, themes, console, network and destructive actions

- At `390×844` dark and `1440×900` light, `/boards`, board-1..4, `WT Dash`,
  `WT Nine` and `/next/tasks` had no horizontal overflow
  (`scrollWidth == clientWidth`), and no `pageerror` events.
- Console errors and `>= 400` responses were only the intentionally stubbed
  `500`s. `GET /api/tasks net::ERR_ABORTED` entries are polls cancelled by
  navigation.
- Destructive actions: the classic UI exposes no board delete. The `/next/`
  delete asked for confirmation and was exercised only on board-19, created
  in this run.

## 3. Bugs found

None.

## 4. Observations (not changed)

- **`/next/` New board saves directly.** The `/next/` rail's New board
  opens `Save current view as board:` and writes the board at once with
  `POST /api/boards`; classic New board files a `grain/config` request task
  instead. Both are intended by their code (`app.component.ts`'s create
  menu gates on `POST /api/boards`; `ui-ng/e2e/next-saved-boards.spec.js`
  covers it), but the two UIs present different creation contracts. Worth
  a product decision when Boards moves to `/next/`.
- **Throughput's `Open` carries a text search Keep ignores.** After typing
  `pagination`, `Throughput`'s `Open` went to
  `/system/metrics?q=pagination&f=state:none:deferred|closed&f=created:within:7`,
  while `Keep` is (correctly) not offered on that panel. The metrics page
  accepts the narrowing, so nothing is broken.
- **Report "asked" vs dashboard "asked".** The report's backlog chip `asked
  1` counts `awaiting_reply` without a completed attempt, while the
  dashboard's `10 asked` counts `awaiting_reply|awaiting_submit`. Each
  number matches the list it links to.

## 5. Tests run

| Suite | Result |
| --- | --- |
| Walkthrough (`/var/tmp/walk/walk.js`, 30 steps over the running demo server) | all passed (four steps re-run after fixing the script's own selectors) |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js` (desktop, phone, dark) on this build | 12 passed |
| `go test ./tests/hygiene/...` | passed |

No product code changed, so no new regression spec was added.
