# Saved Boards and Task Visualizations Walkthrough — 2026-11-18

Task 7016 follows
[2026-11-15-saved-boards-and-visualizations.md](2026-11-15-saved-boards-and-visualizations.md).
Every coverage item ran in headless Chromium through Playwright against a
real `grain demo` server over a throwaway embedded SQLite store. Steps ran
on `/next/` wherever a `/next/` surface exists, and on the classic UI at `/`
only for pages that have not moved over yet.

One defect was found on `/next/`: **applying a saved board from
`/next/tasks` opened a different question from the one the board asks.**
`/next/tasks` read only its own keys out of a board (`tab`, `group`, `q`,
`repo`, `view`). So the four boards `grain demo` seeds (`Board`, `Timeline`,
`Report`, `Tree`), every board written in the classic grammar
(`f=state:any:running`), and every Kanban, report, tree and dashboard board
opened as the unnarrowed Backlog list.

This run reproduced and fixed the defect on `cb1067558`. Meanwhile, task
6589 fixed the same defect on `main` in the same way
([2026-11-17-saved-boards-and-visualizations.md](2026-11-17-saved-boards-and-visualizations.md)).
When this branch merged `main`, this run's duplicate fix was dropped in
favour of `main`'s, and `main`'s was re-verified (section 3). The change
this run lands is this report.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `cb1067558` (`main`, 2026-11-18) |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci` with `TMPDIR` under `/var/tmp/walk/tmp` (throwaway SQLite) |
| Frontends | `make frontend grain` (`ui/`, `ui-ng/`, `ui-hub/` embedded). After the fix: `cd ui-ng && npm run build` then `make grain`, and the server restarted over a fresh store and reseeded |
| Browser | Playwright Chromium (headless, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Desktop `1440×900` and `iPhone 13` (`390×844`, touch), in light and dark |
| Toolchain | Go from `go.mod`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent, so `LOOK=1` was unavailable. Layout was checked with bounding boxes, `scrollWidth`/`clientWidth` and computed colours |

Sign-in posted `{code, name: "alice@example.com"}` to
`POST /api/auth/access-code`. The walkthrough scripts and the `before/` and
`after/` screenshots were kept in `/var/tmp/walk`, outside the repository,
and are not committed.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| External GitHub and CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | The sandbox has no external GitHub or CI credentials |
| Agent landing a `grain/config` board PR | Checked up to the `/new` task form that `New board` and `Add panel` open | `grain demo` runs no model worker |
| More board kinds, folders and task states | Seeded through `POST /api/boards` and `POST /api/tasks` (listed below) | Stock `grain demo` seeds four boards and no held or `keepRunning` task |
| Viewer role | A scoped `page.route` on `GET /api/config` setting `access` to `grain/viewer` with read scopes only (`tasks:read`, `boards:read`, `metrics:read`, `repos:read`, `ci:read`) | No identity-aware proxy in the sandbox |
| Store and metrics read failures | A scoped `page.route` returning `500 {"error":"store unavailable"}` on `GET /api/boards` and `500 {"error":"metrics broken"}` on `GET /api/metrics` | Embedded SQLite does not fail on demand |

Throwaway fixtures created on the demo server:

- **Same-named `Triage` boards:**
  - loose (`list`, `f=state:any:queued`, board-5)
  - `WT Ops` (`list`, `f=state:any:running`, board-6)
  - `WT Zeta` (`kanban`, board-7)
- **`WT Zeta` folder boards:** `WT Zeta Alpha`, `Beta` and `Gamma` (`list`,
  board-8..10).
- **Custom-column Kanbans:**
  - `WT Submit Only`: `Asked to submit` → `awaiting_submit`, and `Queued`
    (board-11).
  - `WT Reply Only`: `Asked for a reply` → `awaiting_reply`, and `Queued`
    (board-12).
- **Narrowed tree:** `WT Widgets Tree` (`tree`, `f=repo:any:acme/widgets`,
  board-13).
- **Dashboards:**
  - `WT Dash` (board-14) with four panels: `State counts`, `Running only`
    (`f=state:any:running`), `Week timeline` (7d) and `Throughput` (metrics,
    7d).
  - `WT Empty Dash` (board-15) with no panels.
- **Reports:** `WT Nine` (all nine sections, board-16) and
  `WT Widgets Report` (`f=repo:any:acme/widgets`, board-17).
- **Narrowed timeline:** `WT Gantt Widgets` (`gantt`,
  `f=repo:any:acme/widgets`, board-18).
- **Tasks:**
  - `WT held proposal` (#27, `holdForApproval: true`, `proposedBy: "8"`).
  - `WT standing task` (#28, `keepRunning: true`, approved into `queued`).

## 2. Steps and outcomes

`[next]` marks steps run on `/next/` (`ui-ng/`) and `[classic]` marks steps
run on `/` (`ui/`).

**Which surfaces have moved over.** Read from `ui-ng/src/app/shell/nav.ts`
and `app.routes.ts`.

- **On `/next/`:**
  - `/next/tasks`: the `Boards` saved-views menu, the save bar, List and
    Backlog reorder, and the Timeline view.
  - `/next/workstreams`: the filer and goal trees.
  - `/next/telemetry`: the measurement report.
- **Still classic-only:** there is no `/next/boards` route (`**` redirects it
  to `/next/inbox`). That covers:
  - the `/boards` index and its folder ordering
  - the `/boards/:id` pages: Kanban, Gantt, Tree, Dashboard and Report
  - the asynchronous board request form (`/new`)

### 2.1 `/next/tasks` Boards menu `[next]`

**Menu disclosure and same-named boards:**

- Opening `saved-boards-btn` focused the first board (`Board`), and
  `Escape` returned focus to `saved-boards-btn`.
- The three `Triage` boards carried distinct accessible names: `Triage,
  outside every folder`, `Triage, in WT Ops` and `Triage, in WT Zeta`.
  Uniquely named folder boards kept `aria-label = null`.

**Applying a board (bug, section 3):**

- **On `cb1067558`:**
  - Applying `Triage, in WT Ops` (`f=state:any:running`) navigated to bare
    `/next/tasks`, which is the Backlog, not the running tasks. The button
    read `Boards 18`.
  - Applying the seeded `Report` board did the same.
- **With this run's interim fix:**
  - Kanban, report, tree and dashboard boards were tagged `classic` and
    opened on `/boards/<id>`.
  - Exact classic clauses were carried over:
    - `f=state:any:running` opened `?tab=running`.
    - The seeded `Timeline` opened `?view=timeline`.
- **The fix that lands is `main`'s, from task 6589:** every board
  `/next/tasks` cannot draw as saved opens on `/boards/<id>`, tagged with
  its kind. That covers every kind except `list`, and any list carrying a
  classic `f=` or `sort=` clause.

**Failed `GET /api/boards`:** the menu showed `The boards could not be
loaded (store unavailable).`, never `No saved boards yet.`

**Rail `New → New board`:**

- From `/next/tasks?tab=asked&q=merge&view=timeline` it kept the URL,
  opened `board-save-form` and focused `board-name-input`.
- Saving `WT Next Saved` posted `{"kind":"list","name":"WT Next
  Saved","query":"tab=asked&q=merge&view=timeline"}`, returned focus to
  `saved-boards-btn` and named the board on the button.
- Both the board name and the URL survived a reload.

**Timeline:** `?view=timeline` on `tab=asked&q=merge` drew `Timeline · 1
tasks` with the `now` marker, and the URL survived a reload.

### 2.2 `/next/workstreams` and `/next/telemetry` `[next]`

- **Workstreams:** `/next/workstreams` listed 2 streams: `Add pagination to
  the tasks API` (1 needs a person, 1 blocked) and `Default new workspaces
  to the paid tier`.
- **Telemetry window:**
  - Choosing `30d` on `/next/telemetry` set `?window=30d`.
  - `Refresh` requested `/api/metrics?window=30d`.
  - The URL survived a reload.

### 2.3 Classic `/boards` index, ordering and history `[classic]`

- **Index:**
  - The rail showed `boards 19`.
  - The index has no search or sort controls (`0` inputs).
  - Loose boards come first, then the `WT Ops` and `WT Zeta` folders. The
    three `Triage` links had distinct accessible names.
- **Tap move in `WT Zeta` (below the first section):**
  - Tapping `Move WT Zeta Gamma` offered slots named by neighbour and folder
    (`Move here, above Triage, in WT Ops`, `Move here, at the end of WT
    Ops`, `Move here, above WT Zeta Alpha`, and so on).
  - No slot was offered beside Gamma itself.
  - `Move here, above WT Zeta Alpha` kept focus on `Move WT Zeta Gamma`, and
    after a reload the order was `[Gamma, Alpha, Beta]`.
- **Drag in `WT Zeta`:** dragging `Move WT Zeta Alpha` onto `WT Zeta Gamma`
  persisted `[Alpha, Gamma, Beta]` across a reload.
- **History:**
  - From `WT Zeta Beta` (`/boards/board-9`), `Back` restored `/boards` with
    focus on the `WT Zeta Beta` link.
  - `Forward` returned to `/boards/board-9`.
- **Failed `GET /api/boards`:** the index explained that the boards could
  not be loaded, and the rail dropped its count. It never said `No boards
  yet`.

### 2.4 Classic asynchronous creation and viewer gating `[classic]` / `[next]`

- **Header `New board`:** opened `/new`, the prewritten task form that files
  the `grain/config` board request. It did not create a board directly, so
  `/api/boards` was unchanged.
- **Viewer role:**
  - `[next]`:
    - The Boards menu showed no `×` delete buttons and no `Save current
      view…`.
    - The rail's `New board` entry was disabled, along with every other
      create entry.
  - `[classic]`:
    - On `/boards`, `New board` was disabled and the board move handles were
      absent (`0`).
    - On board-1, the Kanban card move handles were absent (`0`) and `Edit
      board` was disabled.
    - On `WT Dash?f=repo:any:acme/widgets`, `Add panel` and all three `Keep`
      buttons were disabled behind `tasks:write`
      (`data-access-denied="tasks:write"`), and there were no panel move
      handles.

### 2.5 Classic List and Kanban (`/boards/board-1`) `[classic]`

**Card markers:**

- Task #11 carried `.badge-blocked` (`Blocked on: #8 (running)`).
- The `Merging` column was present.
- `keeps running` (#28), `held for approval` (#27) and `imported` (#12)
  chips were all present.

**Opening a card:** clicking `Fix flaky retry test` opened `/tasks/2`, and
`Back` restored focus to that card's link.

**Moving cards:**

- Move slots name tasks by id as well as title: `Move here, above task 2,
  “Fix flaky retry test”, in Waiting`.
- Tapping `Move task 28` and then `Move here, to the end of Waiting` posted
  `POST /api/tasks/reorder {"ids":["28"],"afterId":"26"}`, kept focus on
  `Move task 28`, and survived a reload.
- `Space` on `Move task 2` changed it to `Stop moving task 2`, and `Escape`
  restored `Move task 2`.

**Edit columns:**

- `Remove Merging`, then `Save`, survived a reload and showed `6 tasks in no
  column (Merging, Deferred, Closed).`
- `Reset to default` restored the columns.

**Custom columns:** neither custom board offered `Edit columns`, and each
"in no column" summary named the asked state its own column does not
collect.

| Board | "In no column" summary |
| --- | --- |
| `WT Submit Only` | `13 tasks in no column (Proposed, Running, Asked for a reply, Merging, Deferred, Closed).` |
| `WT Reply Only` | `19 tasks in no column (Proposed, Running, Asked to submit, Merging, Deferred, Closed).` |

### 2.6 Classic Gantt, Tree, Dashboard and Report `[classic]`

**Gantt (`/boards/board-2`):**

- `Range` offered `Last 24 hours`, `Last 7 days`, `Last 30 days`, `Last 90
  days` and `Everything`. `Lanes` offered `No lanes`, `By repo`, `By state`
  and `By author`.
- `Last 24 hours` with `By state` survived a reload at the unchanged URL
  `/boards/board-2`.
- With those settings the chart showed `4 tasks outside this range.` and one
  `.gantt-now` marker.
- Lanes ran `Proposed, Waiting, Running, Asked, Merging, Deferred`, so
  `Asked` comes after `Running`.
- The bar for #1 opened `/tasks/1`, and `Back` restored focus to it.

**Tree (`WT Widgets Tree`):**

- It opened at `/boards/board-13?f=repo:any:acme/widgets` and kept that URL
  across a reload.
- `#26` nested at depth 2 and `#11` at depth 3, under the tasks that filed
  them.
- No `grain/config` row appeared.
- The `#11` row opened `/tasks/11`.

**Dashboard (`WT Dash`):**

- Typing `pagination` set `?q=pagination&f=state:none:deferred|closed`,
  which survived a reload.
- `Keep` was offered on `State counts` and `Week timeline` but not on
  `Throughput`, whose report a text search would not change.
- `Open` went to `/?f=state:none:deferred|closed` (23 rows), and `Back`
  restored focus to `Open`.
- `Add panel` opened `/new`.
- `WT Empty Dash` rendered `This dashboard has no panels yet. “Add panel”
  files the task that adds one.`
- With `/api/metrics` returning `500`, `Throughput` rendered `The metrics
  report could not be loaded.` and the other panels rendered normally.

**Report (`WT Nine`):**

- It drew `Throughput`, `Latency` and `Backlog` and omitted the six silent
  sections, while offering all 9 `Move section` handles and the window
  selector.
- Backlog chips, task links and repository scoping were exercised on
  2026-11-15 and are unchanged since `cb1067558`
  (`git log --since=2026-11-15 -- ui/src/components/ReportBoard.jsx` is
  empty).

### 2.7 Widths, themes, console, network and destructive actions

- **Boards menu at every width and theme:**
  - Measured on this run's interim build, at `1440×900` and `390×844`, in
    light and dark.
  - The menu fit the viewport (`x = 896` and `x = 16`, width `248`).
  - No board name was truncated (`scrollWidth <= clientWidth`), and the
    document did not overflow.
  - The tag sat after the name, vertically centred in its button.
  - `main`'s layout is covered by its own `@phone` spec in
    `next-saved-boards.spec.js`.
- **Console and network:**
  - There were no `pageerror` events.
  - The only `>= 400` responses were the intentionally stubbed `500`s.
  - `GET /api/tasks net::ERR_ABORTED` entries are polls cancelled by
    navigation.
- **Destructive actions:** the classic UI exposes no board delete control.
  The `/next/` delete confirmation is covered by `next-saved-boards.spec.js`
  against throwaway boards.
- **Observation (not changed):** a classic dashboard's panel `Open` links
  are all named "Open". Each sits inside its panel's titled header, so the
  link's purpose is clear in context. Dashboards have no `/next/` page yet,
  and classic is being deprecated, so this was left as it is.

## 3. Bug found (fixed on `main` by task 6589)

### 3.1 `/next/tasks` applied a classic board as the unnarrowed list

**Reproduction on `cb1067558`:**

1. Over `grain demo`, `POST /api/boards {"name":"X","kind":"list","query":"f=state:any:running"}`.
2. Open `/next/tasks`, then `Boards`, then `X`.

The URL becomes `/next/tasks` (the Backlog), not the running tasks. The
seeded `Board` (Kanban) and `Report` do the same.

**Cause:** `applyBoard` read only `tab`, `group`, `q`, `repo` and `view`,
and ignored the board's kind.

**This run's interim fix:**

- A `board-query.ts` translator that carried exact classic clauses over and
  sent everything else to `/boards/<id>`.
- A `next-saved-boards.spec.js` scenario. It failed on `cb1067558` with
  `Received string: ".../next/tasks"` and passed with the fix.

**What lands:**

- Merging `origin/main` brought in task 6589's equivalent fix:
  - `nextBoardParams` and `classicBoardHint` in `board-labels.ts`
  - spec `a board /next/tasks cannot draw opens on its classic page and says
    so @phone`
- The two fixes conflicted, and `main`'s was kept unchanged.
- With `ui-ng` rebuilt from the merge, `next-saved-boards.spec.js` passed:
  12 tests across the `desktop`, `phone` and `dark` projects.

The interim fix also translated an exact classic clause into a `/next/`
address (`f=state:any:running` → `?tab=running`), where `main`'s sends
every classic list to its classic page. That is an enhancement rather than
a fix, so it was not carried forward.

## 4. Follow-ups

None filed. Standalone `/boards` presentations (Kanban, Gantt, Tree,
Dashboard, Report) remain on the classic migration backlog. This fix routes
`/next/` readers to them rather than misdrawing them.

## 5. Tests run

| Suite | Result |
| --- | --- |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js --project=desktop -g "opens as it was saved"` (`cb1067558`) | 1 failed, reproducing the bug |
| Interim fix: `npx vitest run src/app/tasks/`, `npm run lint`, `npm run build` | 69 passed; lint clean; build clean |
| Interim fix: `npx playwright test e2e/next-full-system.spec.js e2e/next-saved-boards.spec.js` (all projects) | 16 passed |
| After merging `origin/main`: `npm run build`, then `npx playwright test e2e/next-saved-boards.spec.js` (all projects) | 12 passed |
| `go test ./tests/hygiene/...` | passed |
