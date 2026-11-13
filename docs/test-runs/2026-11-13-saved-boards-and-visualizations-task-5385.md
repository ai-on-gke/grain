# Saved boards and task visualizations walkthrough — 2026-11-13 (task 5385)

This is the second boards walkthrough today. The first, by task 4727, is
[2026-11-13-saved-boards-and-visualizations.md](2026-11-13-saved-boards-and-visualizations.md).
This run repeated every coverage item against `main` at `e69d34223`.
It drove a real headless Chromium through Playwright against a real
`grain demo` server over a throwaway embedded SQLite store, using the
UI's own controls.

The new finding is on `/next/`. The rail (`ui-ng/src/app/shell/nav.ts`)
and the routes (`ui-ng/src/app/app.routes.ts`) have no Boards page, but
`/next/tasks` has a **Boards** menu (`saved-boards-btn`). It lists every
saved board, applies one, saves the current view as a board, and deletes
boards. Earlier boards walkthroughs only walked the classic pages, so
they never covered this menu. It had three bugs, and the classic rail had
one more. All four are fixed here (section 3). A fifth problem needs a
product decision and is proposed as a follow-up (section 4).

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `e69d34223` (`main`, 2026-11-13 13:57Z). Fixes re-checked at `71ea27ac7` and `0a046607a` |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, with `TMPDIR` set to a throwaway directory under `/var/tmp` |
| Frontends | `make frontend` (classic `ui/` and `ui-ng/`), embedded by `go build ./cmd/grain`. Rebuilt with `npm run build` in each after every fix |
| Browser | Playwright Chromium 1234, headless (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Desktop `1440×900` and `iPhone 13` (`390×844`, touch), light and dark |
| Toolchain | Go from `go.mod`, Node `v20.19.0`, Debian Linux `x86_64`, 7 vCPU |
| Gemini key | `/home/debian/.gemini-api-key` is absent. Layout was checked with bounding boxes and `scrollWidth`, not `LOOK=1` |

Sign-in used the demo's access code (`POST /api/auth/access-code`, as
`alice@example.com`) and put the session cookie into the browser context.
That is the same route the visible access-code form posts to.

The walkthrough scripts were ad-hoc Node scripts using the Playwright
library from `ui/node_modules`. They lived in `/var/tmp/walk` and are not
committed. Screenshots are not committed either.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub, CI, base-branch CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | The sandbox has no external GitHub or CI |
| Agent that lands a requested board | None. Checked up to the filed `grain/config` task (2.2) | `grain demo` runs no model agent |
| Throwaway boards and tasks | `POST /api/boards` and `POST /api/tasks`, listed below | The demo seeds only `Board`, `Timeline`, `Report` and `Tree`. Only an agent's `propose_task` makes a held proposal |
| Viewer role | `page.route` on `GET /api/config`, rewriting `access` to `grain/viewer` with scopes `settings:read`, `tasks:read` (the stub `ui/e2e/config-change-card.spec.js` uses) | No identity-aware proxy in the sandbox |
| Load failures | Scoped `page.route`: `500 {"error":"store unavailable"}` for `GET /api/boards`, `500` for `GET /api/metrics` | Real SQLite does not fail on demand |

The throwaway boards were:

- **Three `Triage` boards:** a loose list (`f=state:any:queued`), one in
  `WT Ops` (`f=state:any:running`), and a kanban in `WT Zeta`
- **Other lists:** `WT Zeta Alpha` and `WT Zeta Beta` in `WT Zeta`
- **`WT Submit Only` kanban:** columns `Asked to submit` (`awaiting_submit`)
  and `Queued` (`queued`)
- **`WT Widgets Tree`:** `f=repo:any:acme/widgets`
- **`WT Dash`:** panels `State counts`, `Running only`, `Asked`,
  `Week timeline` (gantt, 7d) and `Throughput` (metrics, 7d)
- **`WT Empty Dash`:** a dashboard with no panels
- **Reports:** `WT Nine` (all nine sections) and `WT Widgets Report`
  (`f=repo:any:acme/widgets`)

The throwaway tasks were `WT held proposal` (`proposedBy: "8"`,
`holdForApproval: true`) and `WT standing task` (`keepRunning: true`).

## 2. Steps and outcomes

`[next]` marks a step run on `/next/`, and `[classic]` a step run on the
classic UI. Boards, board presentations and the board-request form have
no `/next/` page yet: `/next/boards` and `/next/boards/board-1` both
redirect to `/next/inbox` (the `**` route). So every board presentation
step ran on the classic UI. The `/next/` steps are the task detail of a
filed board request, the `/next/tasks` Boards menu, and `/next/tasks`
narrowing.

### 2.1 Boards index, rail, folders, ordering, history and load failure `[classic]`

- **Rail:** `Boards` opened `/boards`. The rail count and the index count
  agreed: 4 seeded boards, then 15 with fixtures. The index offers no
  search or sort.
- **Folders:** loose boards come first, then `wt ops`, then `wt zeta`.
  The three Triage links are named `Triage, outside every folder`,
  `Triage, in WT Ops` and `Triage, in WT Zeta`. Their handles are named
  `Move Triage, in WT Zeta` and so on.
- **Mouse drag in a lower section:** dragging `Move WT Zeta Beta` onto
  `WT Zeta Alpha` (HTML5 drag, `locator.dragTo`) gave `… WT Zeta Beta,
  WT Zeta Alpha`. The order survived a reload.
- **Keyboard and tap move:** `Enter` on `Move Triage, in WT Zeta` turned
  it into `Stop moving Triage, in WT Zeta` and offered slots such as
  `Move here, above WT Zeta Alpha` and `Move here, at the end of WT
  Zeta`. Clicking that slot gave `WT Zeta Beta, Triage, WT Zeta Alpha`.
  Focus stayed on the moved board's handle, and the order survived a
  reload.
- **Keyboard reach:** tabbing through the index reached all 15 board
  links.
- **Direct URLs:** `/boards/board-1` through `board-4` drew `Board`,
  `Timeline`, `Report` and `Tree`. `/boards/board-nope` fell back to
  `/boards`. Back, Forward and reload kept the URL. The document title
  stays `grain` on every board.
- **Failed `/api/boards`:** the index said `The boards could not be
  loaded (store unavailable).` with `Retry`, not "No boards yet". A
  board URL gave the same message. **But the rail's Boards entry read
  `0`.** That is bug 3.3.

### 2.2 Asynchronous board creation and viewer gating

- `[classic]` **New board, from the Boards index header:** opened `/new`
  with title `Add a board`, target repo `grain/config`, a new branch, and
  a body listing the board's name, folder, kind, description and query.
- `[classic]` **New board, from the rail:** on `/tasks?f=state:any:running`,
  `Create options` offered `new task`, `new session` and `new board`.
  `new board` filed a body carrying `` query: `f=state:any:running` ``.
  `Create task` filed task 26 (`queued`, `grain/config`, `Add a board`)
  and went back to `/?f=state:any:running`. The board count stayed 15,
  because no board exists until an agent lands one.
- `[next]` **The filed task on `/next/task/26`:** shows `Waiting`,
  `grain/config`, the filer, and the board body under Details.
- `[classic]` **Viewer:** on the index, `New board` is disabled and
  there are no `Move` handles. The rail menu shows `New task` and `New
  board` disabled. On kanban, list and dashboard boards, `Edit board`
  and `New board` are disabled and nothing is `draggable`. On `WT Dash
  ?sort=title`, `Add panel` and both `Keep` offers are disabled.

### 2.3 List and kanban `[classic]`

- **Kanban columns on `Board`:** `Proposed 4, Waiting 5, Running 2, Asked
  10, Failed 0, Merging 2`. The merge-queue column reads **Merging**.
- **Cards:**
  - Task 11 carries `Blocked on: #8 (running)`.
  - `WT standing task` carries `keeps running`, titled `keep_running:
    restarted every time its run exits, …`.
  - `WT held proposal` carries `held for approval by 8`.
  - `[#42] Search results drop the last page` carries `imported`
    (`imported from GitHub issue acme/widgets#42`).
- **Opening a card:** clicking `Fix flaky retry test` opened `/tasks/2`.
  Back returned to `/boards/board-1` with focus on that card's
  `/tasks/2` link.
- **Move slots name tasks by id:** handles read `Move task 6`, `Move task
  14` and so on. Slots read `Move here, above task 6, "Cache the
  capability readiness probe", in Asked to submit`.
- **Tap move:** moving `Move task 17` to the first slot put `Document the
  depot retry budget` first in `Asked to submit`. Focus stayed on `Move
  task 17`. The order survived a reload. The backlog order is shared, so
  task 17 then sat between 5 and 6 on the timeline too.
- **No-column summary (`WT Submit Only`):** `12 tasks in no column
  (Proposed, Running, Asked for a reply, Merging, Deferred, Closed).` The
  board collects `awaiting_submit`, and the summary names the other
  asked state as **Asked for a reply**.
- **List board (`Triage`, `f=state:any:queued`):** 5 rows (2, 11, 20, 23,
  26), with the chip `state is any of waiting`.

### 2.4 Timeline and gantt `[classic]`

- **Controls:** `Range` offers `last 24 hours` through `everything`, and
  `Lanes` offers `no lanes`, `by repo`, `by state` and `by author`.
- **Lanes:** `Last 30 days` and `By state` gave `Proposed, Waiting,
  Running, Asked, Merging, Deferred, Closed`, so **Asked follows
  Running**.
- **Drawing:** 24 bars and the today marker (`.gantt-now`). Dependencies
  are carried in the row's text and label, as on 2026-11-13 (task 4727).
- **Reload:** both settings survived a reload. They are kept in the
  browser, and the URL stays `/boards/board-2`.
- **Opening a task:** clicking the task 3 bar opened `/tasks/3`. Back
  restored focus to `/tasks/3`.

### 2.5 Tree `[classic]`

- **Nesting in `WT Widgets Tree`:**
  - 23 sits under 22.
  - 1, 3 and 8 sit under 13.
  - 11 and the held proposal 24 sit under 8.
  - Closed task 10 is listed.
  - `grain/config` task 26 is excluded by the board's repo filter.
- **Search:** `pagination` gave
  `?q=pagination&f=repo:any:acme/widgets` with 2 rows. Both parts
  survived a reload.
- **Opening a row:** `Enter` on 11 opened `/tasks/11`. Back restored
  focus to that link.
- The seeded `Tree` board draws 26 task links.

### 2.6 Dashboard `[classic]`

- **`Keep` offers on `WT Dash`:**

  | Narrowing | Panels offered Keep |
  | --- | --- |
  | none | none |
  | `?q=retry`, `?f=state:any:queued`, `?sort=title`, `?f=priority:any:high` | `State counts`, `Week timeline` |
  | `?f=repo:any:acme/widgets`, `?f=author:any:alice@example.com` | `State counts`, `Week timeline`, `Throughput` |

  So Throughput is offered Keep only for narrowings its metrics scope
  takes (repo, author).
- **Links:**
  - Each panel's `Open` goes to its task list.
  - The throughput panel goes to `/system/metrics?…&f=created:within:7`.
  - State counts link per state. `Merging` goes to `state:any:completed`,
    the state the Merging column collects.
- **Back focus:** Back from `Running only` → `Open` (2 rows) and from the
  first state-count link (23 rows) put focus back on that link both
  times.
- **Scoping:** under `?f=repo:any:acme/widgets`, the panels that follow
  the board changed their `Open` to `/?f=repo:any:acme/widgets`. The
  `Running only` and `Asked` panels kept their own queries.
- **Add panel:** opened `/new` with title `Add a panel to the dashboard
  "WT Dash"`. Its body lists every panel with its `order_key`.
- **Reorder:** tap-moving `Move panel 5, "Throughput"` to the first slot
  put it first, kept focus on `Move panel 1, "Throughput"`, and survived
  a reload.
- **Failure and empty states:**
  - With `/api/metrics` failing, the throughput panel says `The metrics
    report could not be loaded.` and the other panels draw. This is
    unchanged from 2026-11-13 (task 4727): no reason, no Retry.
  - `WT Empty Dash` says `This dashboard has no panels yet. "Add panel"
    files the task that adds one.`
- **A query the board grammar does not split:** the `Asked` panel's
  query uses a comma (`awaiting_reply,awaiting_submit`). Its `Open` link
  carries it percent-encoded twice (`%252C`), which is how the board
  grammar keeps a comma inside one value. `5d` in
  `ui/e2e/saved-boards-walkthrough.spec.js` already covers such a
  value. This is a fixture mistake, not a bug: the grammar separates
  values with `|`.

### 2.7 Report `[classic]`

- **Sections drawn on `WT Nine`:** `throughput`, `latency` and `backlog`.
  The other six (`Merge queue`, `Prompts`, `Tool use`, `Agents`,
  `Sandboxes`, `CI waits`) are silent in the demo and are omitted. All
  nine still have `Move section N` handles.
- **Window:** `last 30 days` set `?window=30d`, which survived a reload.
  `?window=bogus` fell back to `Last 7 days` and dropped the parameter.
- **Refresh:** re-requested `/api/metrics?window=30d&buckets=24`.
- **Backlog chips against their lists:**

  | Chip | List | Rows | Back focus |
  | --- | --- | --- | --- |
  | `Proposed 4` | `/?f=state:any:proposed` | 4 | restored |
  | `Waiting 5` | `/?f=state:any:queued` | 5 | restored |
  | `Running 2` | `/?f=state:any:running` | 2 | restored |
  | `Asked 1` | `/?f=state:any:awaiting_reply&f=completed:empty:` | 1 | restored |

  The `task 20` reference links to `/tasks/20`.
- **Reorder:** moving `CI waits` (silent) to the top kept focus on its
  handle. It changed nothing drawn, as expected.
- **Repo scoping:** `WT Widgets Report` says `Measured over the tasks in
  acme/widgets.` and counts 17 filed. The seeded `Report` draws
  throughput, latency, capacity, backlog and `github ci`.

### 2.8 `/next/tasks` Boards menu `[next]`

This is new ground (section 3).

- **Before the fix:**
  - The menu listed all 15 boards, with buttons `Triage` ×3 and `Delete
    board Triage` ×3.
  - Each name button was 28px wide and each × was 228px wide, so the ×
    covered the row.
  - At 390px the menu opened at `x = -105`.
  - Clicking × deleted the board at once (`DELETE /api/boards/board-16`).
    There was no dialog, and focus fell to `<body>`.
- **Applying boards:** applying `Triage` (`f=state:any:queued`) or the
  `Report` board landed on plain `/next/tasks`, with nothing narrowed
  (section 4).
- **Save current view:** `+ Save current view…` on `?tab=asked&q=retry`
  created `board-16`, `kind: list`, `query: tab=asked&q=retry`, straight
  through `POST /api/boards`. On classic `/boards/board-16` that board
  shows 7 rows, including queued task 2: the classic board reads `q`
  and ignores `tab` (section 4).
- **`/next/tasks` and the classic list grammar:** `?f=state:any:queued`
  and `?f=state:any:awaiting_reply&f=completed:empty:` are ignored on
  `/next/tasks`, which stays on its Backlog tab. `?q=retry` searches only
  the open tab. So a report chip's link means something only on the
  classic list, which is where it points.

### 2.9 Phone width, dark mode, console and network

- `[classic]` `/boards`, `board-1`, `board-2`, the tree, the dashboard
  and the report showed no horizontal overflow (`scrollWidth ==
  clientWidth`) at 390 light, 390 dark and 1440 dark.
- **Console and network:** no `pageerror` in any context. The only
  `>= 400` responses were the injected `500`s.
- **Destructive actions:** the classic UI offers no board delete
  (`Delete`/`Remove` count 0, and `More page actions` holds no delete).
  `/next/` offered one, and it is now confirmed (3.1). Only throwaway
  boards were deleted.

## 3. Bugs found and fixed

### 3.1 `/next/` Boards menu deleted a shared board on one click

- **Reproduction:** open `/next/tasks`, click `Boards`, click any
  board's ×. `DELETE /api/boards/<id>` is sent at once, and the board is
  gone for every browser on the deployment. No dialog appears, and
  focus drops to `<body>`.
- **Fix (`ui-ng/src/app/tasks/tasks-page.component.ts`):** × now closes
  the menu, focuses the Boards button and asks through the page's
  `g-confirm-dialog`:
  - The title reads `Delete board <label>?`.
  - The message says boards are shared settings.
  - The dialog is destructive, so Cancel is focused.
  - Escape or Cancel deletes nothing and returns focus to `Boards`.
  - `Delete board` deletes, says so, and returns focus to `Boards`.

  This follows the design book's rule that destructive actions confirm.

### 3.2 Same-named boards had identical names in the `/next/` menu

- **Reproduction:** with three boards named `Triage` in different
  folders, the menu has three `Triage` buttons and three `Delete board
  Triage` buttons.
- **Fix:** `ui-ng/src/app/tasks/board-labels.ts` ports the classic
  `boardLabels` word for word. Same-named boards are announced as
  `Triage, in WT Ops` / `Triage, outside every folder`, which is what
  the classic `/boards` says. The folder is also drawn, muted, after the
  name.

### 3.3 Classic rail said `0` boards when `/api/boards` failed

- **Reproduction:** make `GET /api/boards` fail (here `page.route` → 500)
  and open `/boards`. The page says the boards could not be loaded, but
  the rail's Boards entry reads `0`.
- **Fix:** `ui/src/components/Sidebar.jsx` (`BoardsRailCount`, fed
  `boardsError` from `App.jsx`). With no boards held and an error, it
  shows the warning icon the Tasks entry uses for the same failure. The
  icon is named `Boards could not be loaded (<reason>)`. A held count is
  kept through a failing refresh. Boards has no `/next/` page, so there
  is no `/next/` twin to fix.

### 3.4 `/next/` Boards menu layout: names squeezed, menu off-screen on phones

- **Reproduction:**
  - At 1440 the menu is 228px wide, each name button is 28px with an
    ellipsis, and each × is 228px. The cause is that the generic
    `.boards-menu.pop-menu button { width: 100% }` (specificity 0,2,1)
    beat `.boards-menu-row .board-del-btn { width: 28px }` (0,2,0).
  - At 390 the menu opens at `x = -105`.
- **Fix (`ui-ng/src/app/tasks/tasks-timeline.css`):**
  - The row rules now carry `.boards-menu.pop-menu`.
  - The menu is `max-content` wide, capped at `min(360px, 100vw - 32px)`.
  - Below 600px the menu hangs from the button's left edge.
- **Rechecked at 1440 and 390, light and dark:**
  - The menu spans `903–1145` at 1440 and `16–258` at 390.
  - The × is 28px.
  - `Triage` with `WT Ops` is drawn in full.

### Regression coverage

- `ui-ng/e2e/next-saved-boards.spec.js` (run with `make test-e2e-ng`):
  - **Desktop test:** checks the twins' accessible names and the drawn
    folder, then the layout boxes. Then: × asks, Cancel is focused,
    Escape sends no `DELETE` and focuses `Boards`, and accepting deletes
    exactly one board and focuses `Boards`.
  - **`@phone` test:** checks that the menu lies inside the viewport and
    the boxes are right at 390px.
- `ui-ng/src/app/tasks/board-labels.spec.ts`: unique names, folder
  suffixes, and the id tie-break.
- `ui-ng/src/app/tasks/tasks-page.component.spec.ts`: the Saved Boards
  case now asserts the dialog. Cancel keeps the board, and accept
  deletes it.
- `ui/src/components/Sidebar.test.jsx`: `warns rather than counting 0
  when the boards could not be loaded`. Confirmed failing with
  `Sidebar.jsx` reverted.
- The e2e and layout assertions were not re-run against the unfixed
  build. The geometry and the missing dialog on that build are recorded
  above (2.8).

## 4. Proposed follow-up (not fixed here)

The `/next/` Boards menu and classic boards read two different query
grammars:

- Classic boards are `f=…&q=…&sort=…` over every kind (`list`,
  `kanban`, `gantt`, `tree`, `panels`, `report`).
- `/next/` writes and reads `tab=…&group=…&q=…&repo=…`.

Two problems follow:

- **Applying a board on `/next/` does nothing useful.** A board written
  in the classic grammar, or of any kind other than a plain list, lands
  on an unnarrowed `/next/tasks`.
- **A view saved on `/next/` changes meaning on classic.** It loses its
  tab there, and it skips the "New board files a task" contract the
  classic UI keeps.

Fixing this means deciding which grammar `/next/` speaks and what
applying a kanban or report board should do there. That is materially
separate, so it is filed as a follow-up with these reproductions.

## 5. Tests run

| Suite | Result |
| --- | --- |
| `cd ui && npx vitest run src/components/Sidebar.test.jsx` | 87 passed. The new test failed with the fix reverted |
| `cd ui-ng && npx vitest run src/app/tasks/` | 3 files, 43 passed |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js e2e/next-full-system.spec.js --project=desktop` | `next-full-system` 5 passed. The first run of the new spec failed on a locator strict-mode match (fixed) |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js --project=desktop --project=phone` (final build) | 3 passed |
| `cd ui && npx playwright test e2e/saved-boards-walkthrough.spec.js e2e/boards.spec.js e2e/board.spec.js e2e/role-gated-actions.spec.js e2e/board-return-focus.spec.js --project=chromium` (with the classic fix) | 75 passed |

## 6. Notes on standing guidance

- The memory note about `-sandbox-port-base` still applies only to
  `grain daemon`. `grain demo` was started without it, as on 2026-11-13
  (task 4727).
- `pkill -f "<pattern>"` inside `run_command` also matches the command's
  own shell and kills it. Use `pgrep -x grain` or a pid instead.
