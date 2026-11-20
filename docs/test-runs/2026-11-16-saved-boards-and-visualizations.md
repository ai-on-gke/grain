# Saved Boards and Task Visualizations Walkthrough — 2026-11-16

This is the fifth saved-boards and visualizations walkthrough (task 6332),
following
[2026-11-15-saved-boards-and-visualizations.md](2026-11-15-saved-boards-and-visualizations.md)
and the three runs it links. Every coverage item was exercised in headless
Chromium through Playwright against a real `grain demo` server over a
throwaway embedded SQLite store. `/next/` was walked first wherever a `/next/`
surface exists; the classic UI at `/` was used only for pages that have not
migrated yet.

Two defects were found and fixed in this change (section 3). Both are on the
seam between the two UIs, in the saved boards they share:

1. **`/next/tasks` Boards menu did nothing for any board it cannot draw.**
   Choosing a Kanban, timeline, report, tree or dashboard board — including the
   four seeded boards `Board`, `Timeline`, `Report` and `Tree` — or a list
   narrowed in the classic grammar (`f=state:any:queued`) landed on the bare,
   unnarrowed task list. The trigger still read `Boards` and nothing was said.
2. **The classic `/boards/<id>` page ignored the `tab` of a board saved on
   `/next/`.** A board saved as `tab=asked&q=retry` opened on every task
   matching `retry`, queued and running ones included.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `feb34cb22` (`main`, 2026-11-16) |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci` over a throwaway `TMPDIR` under `/var/tmp` |
| Frontends | `make frontend` (`ui/`, `ui-ng/`, `ui-hub/`) embedded into `./bin/grain`; rebuilt after each fix and the server restarted and re-seeded |
| Browser | Playwright Chromium (headless, `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), `1440×900` and `390×844`, light and dark |
| Toolchain | Go from `go.mod`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent, so `LOOK=1` was unavailable. The PNGs could not be viewed in this sandbox either, so layout was checked with bounding boxes and `scrollWidth`/`clientWidth` |

Sign-in posted `{code, name: "alice@example.com"}` to `POST /api/auth/access-code`.
Walkthrough scripts and screenshots were written to `/var/tmp/walk`, outside
the repository, and are not committed.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| External GitHub and CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | No external GitHub or CI credentials in the sandbox |
| Agent landing a `grain/config` board PR | Verified up to the filed `POST /api/tasks` record (2.6) | `grain demo` runs no model worker |
| Extra boards, folders and task states | Seeded with `POST /api/boards` and `POST /api/tasks` (listed below) | Stock `grain demo` seeds four boards and no held or `keepRunning` task |
| Viewer role | Scoped `page.route` on `GET /api/config` setting `access` to `grain/viewer` with read scopes only | No identity-aware proxy in the sandbox |
| Store read failure | Scoped `page.route` answering `GET /api/boards` (and separately `GET /api/boards/*`) with `500 {"error":"store unavailable"}` | Embedded SQLite does not fail on demand |

Throwaway fixtures (re-created after each server restart):

- **Same-named `Triage` boards:** loose `list` (`f=state:any:queued`), `WT Ops`
  `list` (`f=state:any:running`), and `WT Zeta` `kanban`.
- **`WT Zeta Alpha` / `Beta` / `Gamma`:** `list` boards in `WT Zeta`.
- **Custom-column kanbans:** `WT Submit Only` (`Asked to submit` →
  `awaiting_submit`, `Queued`) and `WT Reply Only` (`Asked` → `awaiting_reply`,
  `Queued`).
- **`WT Widgets Tree`:** `tree`, `f=repo:any:acme/widgets`.
- **`WT Dash`:** `State counts`, `Running only` (`f=state:any:running`),
  `Week timeline` (7d), `Throughput` (7d). **`WT Empty Dash`:** no panels.
- **Reports:** `WT Nine` (nine named sections) and `WT Widgets Report`
  (`f=repo:any:acme/widgets`).
- **`WT Next Native`:** `list`, `tab=asked&q=retry`, the query `/next/`'s
  Boards menu writes.
- **Tasks:** `WT held proposal` (`proposedBy: "8"`, `holdForApproval: true`) and
  `WT standing task` (`keepRunning: true`).

### Which UI each step ran against

`/next/` has the task list, its Boards menu and save bar, its Timeline view,
`/next/workstreams` trees and `/next/telemetry`. Neither `ui-ng/src/app/shell/nav.ts`
nor `ui-ng/src/app/app.routes.ts` has a Boards index or any `/boards/<id>`
presentation (kanban, list board, gantt, tree, dashboard, report). Those steps
ran on the classic UI, and they have **no `/next/` page yet**.

## 2. Walkthrough steps and outcomes

### 2.1 `/next/tasks` Boards menu `[next]`

- The menu lists all 18 boards with distinct accessible names for the
  `Triage` boards (`Triage, outside every folder` / `in WT Ops` / `in WT Zeta`).
- **Before fix (3.1):** applying each of `board-1` (Kanban), `board-2`
  (Timeline), `board-3` (Report), `board-4` (Tree), `board-5`
  (`Triage`, `f=state:any:queued`), `board-6`, `board-13`, `board-14` and
  `board-17` from `/next/tasks` left the URL at `/next/tasks`, showed the
  Backlog's 5 rows, and kept the trigger at `Boards 17`.
- **After fix:** each of those is a link (`board-apply-<id>`, `href="/boards/<id>"`)
  showing its kind and `↗` (`Board Kanban ↗`, `Triage List ↗`,
  `WT Dash Panels ↗`). Each is announced as, for example, `Board, Kanban,
  opens in the classic UI`. Choosing `board-1` opened `/boards/board-1`, and
  `board-5` opened `/boards/board-5?f=state:any:queued`. `WT Next Native`
  stayed a button and applied in place
  (`/next/tasks?tab=asked&q=retry`).
- **Layout after fix:** at `1440×900` and `390×844`, light and dark, every menu
  row is `34px` tall with no clipped name (`scrollWidth <= clientWidth`).
  The `28px` × sits right after each name, and the menu stays on screen
  (`x = 892..1145` on desktop, `x = 16..269` on phone).
- With `GET /api/boards` failing, the menu still says why the boards could not
  be loaded (2026-11-15 fix, unchanged).

### 2.2 `/next/tasks` list, timeline and markers `[next]`

- Tabs: `Backlog 5`, `Needs approval 4`, `Running 2`, `Asked 2`,
  `Submit & land 10`, `Deferred 2`, `Closed`, `All 25`.
- On `?tab=all&view=timeline`, completed tasks `#5` and `#24`, which are in the
  merge queue, read `Merging`. `awaiting_submit` tasks read
  `Ready to submit`.
- As a viewer, the Backlog shows `0` drag handles, and the Boards menu shows no
  `×` and no `+ Save current view…`.

### 2.3 `/next/telemetry` `[next]`

- Clicking `telemetry-window-30d` set `?window=30d`, which survived a reload with
  `aria-pressed="true"`. `telemetry-refresh` re-requested
  `GET /api/metrics?window=30d`.

### 2.4 Classic `/boards` index, folders, ordering and failures `[classic — no /next/ page]`

- Loose boards come first, then `WT Ops` and `WT Zeta`. The index has no
  search or sort control.
- HTML5-dragging `Move WT Zeta Gamma` onto `WT Zeta Alpha` (a section below
  the first) persisted `[Triage, Gamma, Alpha, Beta]` across a reload.
- Tapping `Move WT Zeta Gamma` offered 19 slots naming neighbour and folder
  (`Move here, above Triage, in WT Zeta`, `Move here, at the end of WT Zeta`).
  Choosing the last one restored `[Triage, Alpha, Beta, Gamma]` and kept focus
  on `Move WT Zeta Gamma`.
- Clicking `WT Zeta Alpha`, then `Back`, restored `/boards` with focus on
  `WT Zeta Alpha`; `Forward` returned to the board.
- With `GET /api/boards` failing, `/boards/board-1` said `The boards could not be
  loaded (store unavailable).` and offered `retry`. With only `GET /api/boards/*`
  failing, the board still rendered from the list. `/boards/board-999`
  fell back to `/boards`.

### 2.5 Classic list and Kanban `[classic — no /next/ page]`

- `board-1` columns: `proposed 4`, `waiting 5`, `running 2`, `asked`,
  `failed`, `merging 2`. The merge-queue column reads `Merging`.
- Markers: `#11` carries `.badge-blocked`, `#12` `imported`, `#27`
  `held for approval by 8`, and `#28` `keeps running`.
- Tapping `Move task 22` offered slots identifying tasks by id and title
  (`Move here, above task 2, “Fix flaky retry test”, in Waiting`). Choosing one
  posted `POST /api/tasks/reorder {"ids":["22"],"beforeId":"2"}` and kept focus
  on `Move task 22`. `Space` turned it into `Stop moving task 22`.
- `Edit columns` → `Remove Merging` → `Save` survived a reload as `6 tasks in no
  column (Merging, Deferred, Closed).`, and `Reset to default` restored
  `4 tasks in no column (Deferred, Closed).`
- `WT Submit Only` reads `14 tasks in no column (Proposed, Running, Asked for a
  reply, Merging, Deferred, Closed).`, and `WT Reply Only` reads `20 tasks in no
  column (Proposed, Running, Asked to submit, Merging, Deferred, Closed).` Each
  names the other kind of asking.
- **Before fix (3.2):** `/boards/board-18` (`WT Next Native`,
  `tab=asked&q=retry`) opened at `?q=retry` over `all tasks` and listed
  queued `#2` and `#14`–`#16`. **After fix:** it opens at
  `?q=retry&f=state:any:awaiting_reply|failed` (`state is any of asked (reply),
  failed`; `No tasks match your search.`, the same answer `/next/` gives), and
  that survives a reload. `board-5` still opens at `?f=state:any:queued`.

### 2.6 Classic asynchronous creation and viewer gating `[classic; /next/ save bar noted]`

- Header `new board` on `/boards` opened `/new` titled `Add a board` with the
  board template in its description. `create task` sent exactly one
  `POST /api/tasks` and returned to `/boards`, and `/api/boards` stayed at 18
  boards. The board is requested, not written.
- `/next/` has no asynchronous board request: its rail `New → New board` opens
  the save bar, which writes `POST /api/boards` directly (as recorded on
  2026-11-15).
- **Viewer:** `new board` and `edit board` were disabled on `board-1`, `board-5`,
  `board-14`, `board-16` and the index, and `add panel` was disabled on
  `board-14`. There were no `Keep` buttons and no `Move …` handles (`0`).

### 2.7 Classic gantt and tree `[classic; /next/ Timeline in 2.2]`

- `board-2`: `Lanes` → `By state` drew `proposed 4, waiting 5, running 2, asked
  10, merging 2, deferred 2, closed 1` (Asked after Running), and that
  survived a reload at the bare URL. `.gantt-now` rendered. Pressing `Enter` on
  the first bar opened `/tasks/1`, and `Back` restored focus to `Add a dark mode
  toggle`.
- `WT Widgets Tree` opened at `/boards/board-13?f=repo:any:acme/widgets`, which
  survived a reload. It excluded `grain/config` tasks, nested children under
  the task that filed them, and a row opened `/tasks/11`.

### 2.8 Classic dashboard `[classic — no /next/ page]`

- State-count links (`4 Proposed`, `5 Waiting`, `2 Running`, `10 Asked`,
  `0 Failed`, `2 Merging`) and the four panel `Open` links led to their lists
  (`Running only` → `/?f=state:any:running`, 2 rows). Browser `Back` from each
  restored focus to the link that was followed.
- Typing `pagination` set `?q=pagination&f=state:none:deferred|closed`, which
  survived a reload. It offered `Keep` on `State counts` and `Week timeline`
  but not on `Throughput`; `?f=repo:any:acme/widgets` offered it on all three.
- `add panel` opened `/new`, and `Escape` returned to the board with focus on
  `Add panel`.

### 2.9 Classic report `[classic; /next/telemetry in 2.3]`

- `WT Nine` drew only `throughput`, `latency` and `backlog`; the six silent
  sections were omitted. `WT Widgets Report` scoped to `acme/widgets` and drew
  `throughput`, `latency`, `capacity`, `backlog` and `github ci`.
- Backlog chips matched their lists: `Proposed 2` (`#1`, `#12`), `Waiting 5`,
  `Running 2`, and `Asked 1` (`/?f=state:any:awaiting_reply&f=completed:empty:` → `#4`).
  `Back` restored focus to each chip. `task 22` links to `/tasks/22`, and
  `GET /api/metrics?window=7d&buckets=24` matched those counts.

### 2.10 Widths, themes, console and network

- At `390×844`, `/next/tasks`, `board-1`, `board-14`, `board-16` and `board-18`
  had no horizontal overflow (`scrollWidth - clientWidth = 0`).
- There were no `pageerror` or console errors across the runs, and no
  `>= 400` responses other than the intentionally stubbed `500`s.
- Deleting a board is only offered by `/next/`'s menu `×` behind a confirmation
  dialog. It was not re-exercised against seeded boards this run (it was
  covered on 2026-11-15 and is unchanged).

## 3. Bugs found and fixed

### 3.1 `/next/` Boards menu dropped the kind and narrowing of boards it cannot draw

> **Superseded on `main`:** the 2026-11-17 walkthrough (task 6589, PR #1819)
> landed the same fix independently (`board-labels.ts`'s `nextBoardParams` /
> `classicBoardHint`, announced "opens on the classic Boards page"). Merging
> `main` into this branch took that version whole, so this branch's `/next/`
> code for 3.1 was dropped. What stays here is fix 3.2 and its e2e scenario.

- **Reproduction:** on stock `grain demo`, open `/next/tasks`, click `Boards`,
  and choose `Board` (or `Timeline`, `Report`, `Tree`). `applyBoard` read only
  `tab`, `group`, `q`, `repo` and `view` from the board's query, so the page
  navigated to bare `/next/tasks`.
- **Fix:** `board-labels.ts`'s `opensHere(b)` is true only for a `list` board
  whose query uses only those keys. Any other board is rendered as a link to
  its classic `/boards/<id>` page, showing its kind (`boardKindName`, the
  classic UI's names) and `↗`, and announced as `<name>, <kind>, opens in the
  classic UI`. The rule is in the design book's navigation section.
- **Coverage:** unit tests in `board-labels.spec.ts`, and the
  `ui-ng/e2e/next-saved-boards.spec.js` scenario "a board this page cannot draw
  opens on its classic page".

### 3.2 Classic board page ignored a `/next/` board's tab

- **Reproduction:** save `tab=asked&q=retry` from `/next/tasks` (or
  `POST /api/boards {"kind":"list","query":"tab=asked&q=retry"}`), then open
  `/boards/<id>`. Classic `parseNarrowing` reads `q` and the legacy `repo=` but
  not `tab`.
- **Fix:** classic `boards.js`'s `classicBoardQuery` turns `tab=` into the state
  filter that `/next/` tab lists (`task-view.ts` `TABS`; `all` →
  `state:none:closed`). It runs before `narrowingFromQuery` in
  `boardNarrowing`. A query carrying its own `f=` or `sort=` is left as written.
  This is a classic fix for a bug users hit today, paired with 3.1 on the
  `/next/` side.
- **Coverage:** a `boards.test.js` unit test, and the same e2e scenario opening
  the `/next/`-saved board on its classic page.

### Reviews

- **Design:** the classic-link rows reuse the menu row's metrics (`13px`, `34px`
  rows, `on-surface` colour, no underline, the same hover and `focus-visible`
  ring as the buttons). The kind label uses the folder label's `12px` muted
  style. Bounding boxes were checked at both widths and both themes (2.1).
- **Functional:** both fixes were verified end to end against `grain demo`. The
  new scenario's link assertions cannot pass on the base, which renders no link
  for any board.
- **Adversarial:** a `/next/`-grammar query that also carries `f=`/`sort=` is
  left alone, a classic-grammar list is never applied as a `/next/` list, and
  a list with an empty query still applies in place. The new links are plain
  `href`s, so middle-click and keyboard `Enter` work.

## 4. Follow-ups

None filed. What is left of the two-grammar gap, `/next/` reading classic
`f=`/`sort=` narrowings in place rather than handing them to the classic page,
is the follow-up already proposed by task 5385. Standalone `/boards`
presentations remain on the classic UI's migration backlog.

## 5. Tests run

| Suite | Result |
| --- | --- |
| `cd ui-ng && npm run lint` | clean |
| `cd ui-ng && npm run build` | clean |
| `cd ui-ng && npx vitest run src/app/tasks` | 66 passed |
| `cd ui-ng && npx playwright test e2e/next-saved-boards.spec.js` | 11 passed |
| `cd ui && npx vitest run src/boards.test.js` | 62 passed |
| `cd ui && npx playwright test e2e/boards.spec.js --project=chromium` | 17 passed |
| `npx prettier --check` on changed files | clean |
