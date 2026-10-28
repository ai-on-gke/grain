# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-13 (11:41–12:20 UTC)

Commit tested: `25cd6f264` (`Merge pull request #3656 from
bwsalmon/grain/eu/task-3532`, tip of `origin/candidate`, checked out as
`grain/eu/task-3494`). The two fixes below were rebuilt into a fresh
server and re-walked.

Prior walkthroughs: `2026-10-12-saved-boards-and-visualizations.md` and
the ones it lists. Since then, candidate has changed the team switcher and
the creation dialogs' team pickers (`829b5a6b7`, `31f151707`), so this run
re-walked the asynchronous creation contract on the new picker. It also
went where the earlier runs had not: the Kanban column editor from the
keyboard, report prompts read with a picked window, HTML5 drags of
dashboard panels, focus return after a task is closed (by view kind),
malformed board rows and URLs, and the phone profile's column editor.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Playwright
  (from `ui/node_modules`) driving Chromium headless shell 151.
- Frontend: `cd ui && npm ci && npm run build` into `pkg/ui/static`.
  Server: `go build -o /tmp/grain ./cmd/grain` after the frontend build.
- Server: `/tmp/grain demo -addr 127.0.0.1:8787 -open=false` from a
  throwaway working directory, i.e. `grain demo`'s own embedded SQLite
  store and seed. Restarted on a fresh directory after each fix.
- Browser: standalone Playwright scripts (not committed) against the real
  server. Desktop was 1440×1000 (1440×1800 for the panel drags, so that
  Playwright's own scrolling did not move the page mid-drag). Phone was
  iPhone 13 (390×844, `hasTouch`, `isMobile`). Every script recorded
  `pageerror`, console errors, responses ≥ 400 and failed requests.
- Identity: `localStorage["grain.username"] = "alice"`, plus
  `X-Grain-Admin-Mode: on` (the e2e suite's default).
- Gemini: this sandbox has no key at `/home/debian/.gemini-api-key`, so no
  Gemini-backed model or tooling was used. No flow here needed one.
- `npx playwright install chromium` needs
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  behind the package-cache proxy.

## Stubs and stand-ins

- **Merged board changes (stand-in, not a stub).** A board is created by
  an agent's pull request against the state repository, and a sandbox
  cannot merge one. After the real request flow (step 2), the boards an
  agent would have produced were written with `POST /api/boards`, the
  endpoint the import ends at. They all went in folder `WT3494`:
  - `WT3494 Running`: list, `f=state:any:running`.
  - `WT3494 Kanban`: its own columns, Waiting = queued + proposed and
    Doing = running.
  - `WT3494 Gantt`: `f=repo:any:acme/widgets`.
  - `WT3494 Dash`: six panels. States; an inheriting list; "Running only";
    "Widgets 30d" metrics on `acme/widgets`; a 24h full-width gantt;
    "Nobody" on `nobody/nothing`.
  - `WT3494 Report`: all eleven sections, latency retitled `Lat`, scoped
    to `acme/widgets`.
  - `WT3494 Empty`: a dashboard with no panels.
  - `WT3494 Tree`: `q=pagination`.
  - `WT3494 Odd Gantt`: query `sort=created:sideways&f=created:within:abc`.

  They lived only in the throwaway store.
- **`/api/metrics`**: a 3 s delay, then → 500 (`page.route`).
- **`GET /api/boards` → 500** (`page.route`), on a board deep link.
- **GitHub boundary.** Task detail asks
  `GET /api/repos/<repo>/github-branches`, which answers 404 in demo mode.
  That 404 and its console line were the only errors outside the stubbed
  failures.

## Steps and outcomes

1. **Boards index, rail, URLs, history.**
   - The rail shows `Boards 4` and leads to `/t/default/boards`: `Board`,
     `Timeline`, `Report` and `Tree`, count 4, `New board`, and one
     `Move <name>` handle per board. The index has no search or sort.
     Its one combobox is the team switcher.
   - Deep links:
     - `/boards/board-1` → `/t/default/boards/board-1`.
     - `/t/default/boards/board-2?window=30d` kept its query.
     - `/boards/board-999` and `/boards/` → the index.
     - `/t/default/boards/board-3/` → `/t/default/boards/board-3`.
     - `/boards/board-1?q=pagination` kept the narrowing.
   - History: index → Timeline → `← Boards` → Back → Back → Forward →
     reload ended on the Timeline board with its heading.
   - Tab order from the top reaches the rail, `New board`, then each
     board's `Move` handle and link in turn.
   - No page errors.
2. **Asynchronous creation contract.**
   - `New board` opens `New task` at `/t/default/new`, prefilled with the
     board prompt. Its repository is `grain/config-default` and auto-merge
     is on. Since `829b5a6b7` the team is `default` (yesterday it was
     `admin`).
   - Appending a request ("a list board called 'WT3494 Running' …") and
     pressing `Create task` returned to the index.
   - `GET /api/tasks/13`: `queued`, approved, `grain/config-default`,
     branch `grain/task-13`, `autoMerge: true`, team `default`, with the
     appended text at the end of its prompt. Boards stayed at 4 in the
     API and on the rail. The board appears only when that task's change
     merges, and the stand-ins above are that merge.
   - `Edit board` on `Board?q=pagination` is titled
     `Change the board "Board"`. It says the board has no columns of its
     own and names the live `q=pagination` narrowing. Back returned to
     the narrowed board.
3. **List and Kanban.**
   - `WT3494 Running` lists tasks 8 and 3. Opening a row by link and
     Enter, then pressing Escape, returns focus to that row's link.
   - `WT3494 Kanban` draws Waiting 4 (1, 2, 11, 13) and Doing 2 (3, 8),
     with the note "6 tasks in no column (Awaiting reply, … Closed)".
   - Keyboard moves:
     - Picking up task 13 moves focus to `Stop moving task 13`. Three
       slots are offered, all in Waiting.
     - Escape cancels and returns focus to `Move task 13`.
     - Enter on the first slot moved 13 to the top and kept focus on its
       handle. The order survived reload.
   - A mouse drag (real pointer steps) of task 8 above task 3 in Doing
     persisted across reload.
   - On the phone, tapping `Move task 11` and then the first slot moved
     11 to the top.
   - The seeded `Board`'s `Columns` editor:
     - The first column's left arrow and the last column's right arrow
       are disabled.
     - Moving columns with the keyboard and saving changed the board, and
       the layout survived reload (`grain.board.columns` in
       localStorage).
     - `+ Add column` added "New column", and `Remove` took it away.
     - **Focus was lost at either end and on removal: finding 1.**
4. **Timeline.**
   - `WT3494 Gantt` on "Last 7 days": 10 rows, one `.gantt-now` at 100 %,
     ticks Oct 7…13, one `Blocked` chip, "1 task outside this range."
   - Seeded `Timeline` across ranges:
     - Last 24 hours: 10 rows, hour ticks, "2 tasks outside this range."
     - Last 90 days: 12 rows, no note.
   - Lanes:
     - By state gave 9 state lanes.
     - By author gave `alice`, `debian`, `director`.
   - Range and lanes survived reload. They are browser preferences by
     design (`gantt.js`), so the URL stays `/t/default/boards/board-2`.
     `?range=7d&lanes=repo` on a deep link is ignored and dropped.
   - Keyboard: the Range select opens with Enter, and ArrowUp + Enter
     picked 30 days.
   - Clicking a bar (accessible label "Queued: waiting for 8m, never
     dispatched") opens the task, and Back restores the timeline with its
     settings.
   - A row's title link opened by Enter returns focus to the link on
     Escape.
   - Tapping a bar on the phone opens the task.
5. **Dashboard.**
   - Every panel drew:
     - State counts: 10 = 1 + 3 + 2 + 3 + 1.
     - Task list: "Showing 8 of 10."
     - Running only: 2.
     - Widgets 30d: "12 filed / 3 completed".
     - The 24h timeline: 10 rows, "1 task outside this range."
     - Nobody: "Nothing here right now."
   - Searching `pagination` narrowed only the three inheriting panels.
     Each grew `Keep`, and the scoped panels kept their own answers. URL
     and result survived reload. `Keep` on “Task list” files a task
     marking panel 2 `<-- this one`. `Add panel` lists all six panels in
     order and carries the narrowing as its query.
   - Links, each followed after a fresh load:
     - "1 proposed" → 1 row, "2 queued" → 2, "2 running" → 2,
       "3 needs you" → 3, "1 queued for merge" → 1.
     - Running only `Open` → 2 rows, Nobody `Open` → 0, the Timeline and
       Task list `Open` → the inheriting list (9).
     - Widgets 30d `Open` →
       `/metrics?f=repo:any:acme/widgets&f=created:within:30`, which says
       "Measured over the tasks in acme/widgets."
     - Each survived reload.
   - A deep link narrowed to `f=repo:any:acme/web` (a repo with no tasks)
     zeroes the inheriting panels and leaves "Running only" at 2.
   - Panel moves:
     - Keyboard: panel 1 to the end offered five slots, and focus stayed
       on `Move panel 6, “State counts”`.
     - HTML5 drags (`dragTo` on the header grip) moved “State counts”
       above “Running only” and “Timeline” above “Nobody”. Both survived
       reload.
     - Phone: tapping `Move panel 6` and then the first slot moved it to
       the top.
   - A dashboard with no panels says “This dashboard has no panels yet.
     “Add panel” files the task that adds one.”
6. **Report.**
   - `WT3494 Report` asked
     `/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24`. It shows
     eleven handles, `Move section 1, “Throughput”` …
     `Move section 11, “Merge queue”`, the retitled `Lat`, and “Measured
     over the tasks in acme/widgets.”
   - A keyboard move of section 11 to the top kept focus on
     `Move section 1, “Merge queue”` and persisted.
   - Window offers 24h / 7d / 30d / 90d. 90d wrote `&window=90d` and
     re-asked with `window=90d`.
   - A search on the report says “A report is measured over whole
     distributions, so the rest of this question is not in force: the
     search for "pagination".”
   - The backlog's `task 2` button is the section's link into the data.
   - The seeded `Report` asks the unscoped `window=7d`.
   - **`Edit board` / `New board` on a report read at 90 days said
     nothing of the 90 days: finding 2.**
   - The task asked for prose sections. None exist: the eleven section
     kinds are all measurements or task-backed (backlog), and the server
     refuses any other kind.
7. **Tree.** `WT3494 Tree` nests task 11 under task 8, with state chips
   and the PR link `acme/widgets#104`. The task numbers are the links.
8. **Malformed rows and URLs.**
   - `POST /api/boards` refuses an unknown board, panel or section kind,
     an unknown column state and an unknown panel width, each with a
     message naming the allowed values.
   - A stored query is not validated (by design, `pkg/ui/boards.go`).
     `WT3494 Odd Gantt` opens with the sort normalized to `created:asc`
     and a chip “created is within the last abc days” that filters
     nothing (observation 2).
   - Other URLs:
     - `?f=state:any:bogus` on the Kanban: empty columns and “No tasks
       match this search and these filters.”
     - `?f=%E0%A4%A` (malformed UTF-8): dropped.
     - `?q=%ZZ`: re-encoded as `%25ZZ`.
     - A report's `?window=forever`: dropped back to 7 days.
     - No page errors.
9. **Loading / error states (stubbed).**
   - Metrics delayed: the panel says “Loading…”.
   - Metrics 500: the panel says “The metrics report could not be
     loaded.” The report says “The report could not be loaded (500
     Internal Server Error). Refresh to try again.” Once unstubbed,
     `Refresh` drew the report.
   - Boards 500 on a `board-6` deep link: the URL was kept, and the board
     recovered on the next poll once unstubbed.
10. **Phone (390×844, touch).** No page-level horizontal overflow on the
    index or on any board kind. Kanban columns scroll inside their own
    container. The column editor does not overflow, and a tapped arrow
    keeps focus with the fix.
11. **Focus return after closing a task.** Opened from the keyboard (link
    + Enter), Escape returns focus to the link on a list, a Kanban card, a
    gantt row and a dashboard list panel. Opened by a mouse click on a
    gantt bar or a panel row, Escape leaves focus on `<body>`, because
    nothing was focused to return to. That is ordinary for a pointer, and
    the list and Kanban return focus only because the click landed on
    their title link.
12. **Delete and confirmation.** No board, panel or section delete exists
    in the UI (`8. Deletion safety` in the e2e spec asserts the same).
    Boards go by a state-repository change. The only destructive control
    reached was the column editor's `Remove`, which is a draft until Save.
    `Cancel` and Escape on a changed editor ask first (unit-covered). The
    throwaway boards and task 13 lived only in the demo store, discarded
    with it.

## Findings

### 1. The Kanban column editor lost keyboard focus at either end and on removal (fixed here)

Reproduction: on `/boards/board-1`, press `Columns`, focus
`Move Queued left`, and press Enter. Queued becomes the first column, its
left arrow is disabled, and `document.activeElement` is the dialog. A
second Enter does nothing. `Remove <column>` from the keyboard does the
same, because the button pressed is unmounted.

Fix: `BoardColumnsOverlay` now moves focus once the draft re-renders:
- after a move to either end, to the column's other arrow;
- after a removal, to the Remove of the column that slid into its place
  (or the one before it, or `+ Add column` when none are left);
- after `+ Add column`, into the new column's title.

Re-walked on a fresh server, focus now lands on `Move Queued right`, on
`Remove Queued for merge`, and on `Column 6 title`.

Regression coverage: two `BoardColumnsOverlay.test.jsx` cases, and e2e
`4c` in `saved-boards-walkthrough.spec.js`. Both fail with the fix
reverted (unit: 2 failed, 15 passed; e2e: `toBeFocused`).

### 2. `Edit board` and `New board` on a report dropped its picked window (fixed here)

Reproduction: open `/boards/board-3`, pick Window → Last 90 days (the URL
gains `?window=90d`), and press `Edit board` (or `New board`). The
prompt says `query: (empty …)` and nothing about 90 days. The window is
the address bar's `window=`, not part of the narrowing these prompts
carry, so “change it on screen until it is right and then click the
button” (`BoardPage.jsx`) lost the one setting a report has.

Fix: `editBoardTask` / `newBoardTask` take the picked window (App passes
`boardWindow`). On a report they say “The report is being read right now
over the last 90 days (its Window picker) …” and name the condition that
keeps it, `f=created:within:90`, the encoding `REPORT_NOTE` already
documents. Other kinds, and a report with no pick, are unchanged.

Regression coverage: two `boards.test.js` cases, and e2e `7c2`. Both fail
with the fix reverted.

### Observations (not changed)

1. **Aborted `/api/tasks` polls in the network log are 304s.** Each
   conditional poll the server answers `304` is also reported by Chromium
   as `requestfailed … net::ERR_ABORTED` (no body under `no-store`). That
   is about 70 lines in a few minutes of walking, and it is noise rather
   than a fault. Anybody filtering failed requests in a walkthrough should
   expect it.
2. **A stored query with a malformed condition draws a chip that filters
   nothing.** `f=created:within:abc` shows “created is within the last
   abc days” over every task. The server stores queries unread by design.
   Only a hand-edited row can carry one.
3. **On a phone, the timeline's day ticks touch.** At 390 px, adjacent
   7-day tick labels overlap by 1–2 px (`Oct 10` ends at 294, `Oct 11`
   starts at 293), and `Oct 13` runs to 391 of 390. It is legible, but
   tight.
4. **A timeline panel's `Open` drops the panel's range.** It opens the
   inheriting task list (9 tasks) rather than the 10 rows the 24h panel
   draws. A range is overlap with a window rather than a created-date
   condition, so it has no list equivalent. The metrics panel, whose
   window is a created-date bound, does carry `created:within:30`.
5. **A mouse-opened gantt bar or panel row returns focus to `<body>` on
   Escape** (step 11). The keyboard path is fine.

## Automated tests

- `npx vitest run src/boards.test.js
  src/components/BoardColumnsOverlay.test.jsx
  src/components/BoardsPage.test.jsx src/components/TaskBoard.test.jsx
  src/components/ReportBoard.test.jsx src/components/PanelBoard.test.jsx
  src/App.test.jsx`: 7 files, 237 passed (13.4 s).
- `npx playwright test e2e/saved-boards-walkthrough.spec.js
  e2e/boards.spec.js e2e/board.spec.js e2e/gantt.spec.js
  e2e/boards_workflows.spec.js --project=chromium`: 49 passed (59.6 s).
- Each new test was also run against a build with its fix reverted and
  failed there: `4c` with `toBeFocused`, `7c2` with `toHaveValue`.
