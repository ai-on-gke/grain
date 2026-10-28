# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-15 (01:07–01:55 UTC)

Commit tested: `9788ecb1e` (`Merge pull request #3832 from
bwsalmon/grain/eu/task-3744`, tip of `origin/candidate`, checked out as
`grain/eu/task-3751`). The fix below (`e4108c509`) was rebuilt into a
fresh server and re-walked.

Prior walkthroughs: `2026-10-13-saved-boards-and-visualizations.md` and
the ones it lists. Both of its fixes (column-editor focus and a report's
window in the prompts) are in the tested commit and held. This run went
after that report's observation 5 (focus lost after a mouse-opened task),
then went further: off-title clicks on every surface, multi-select block
moves on the Kanban, column-editor edge cases (blank title, a state
claimed by two columns), sorts and history on a board, folder moves on
the index from the keyboard and by touch, stubbed metrics and boards
failures, and two tabs reordering the same dashboard.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Playwright
  (from `ui/node_modules`) driving Chromium headless shell 151.
- Frontend: `cd ui && npm ci && npm run build` into `pkg/ui/static`.
  Server: `go build -o /tmp/grain ./cmd/grain` after the frontend build.
- Server: `/tmp/grain demo -addr 127.0.0.1:8787 -open=false` from an
  empty throwaway directory, i.e. `grain demo`'s own embedded store and
  seed. It keeps nothing across a restart. It was restarted once, after
  the fix, and the stand-in boards were written again.
- Browser: standalone Playwright scripts (not committed) against the real
  server. Desktop was 1440×1000 (1440×1800 for dashboard and report
  drags). Phone was iPhone 13 (390×844, touch). Every script recorded
  `pageerror`, console errors, responses ≥ 400 and failed requests
  (except Chromium's `ERR_ABORTED` on 304 polls, observation 1 of the
  2026-10-13 run).
- Identity: `localStorage["grain.username"] = "alice"`, plus
  `X-Grain-Admin-Mode: on` (the e2e suite's default).
- Gemini: this sandbox has no `/home/debian/.gemini-api-key`, so no
  Gemini-backed model or tooling was used. No flow here needed one.
- `npx playwright install chromium` needs
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  behind the package-cache proxy.

## Stubs and stand-ins

- **Merged board changes (stand-in, not a stub).** An agent's pull request
  against the state repository is what creates a board, and a sandbox
  cannot merge one. So after the real request flow (step 2), the boards
  that agent would have produced were written with `POST /api/boards`,
  the endpoint the import ends at:
  - `WT3751 Running`: list, `f=state:any:running`.
  - `WT3751 Kanban`: its own columns, Waiting = queued + proposed and
    Doing = running.
  - `WT3751 Gantt`: `f=repo:any:acme/widgets`.
  - `WT3751 Dash`: six panels. States; an inheriting list; "Running only";
    "Widgets 30d" metrics on `acme/widgets`; a 24h full-width gantt;
    "Nobody" on `nobody/nothing`.
  - `WT3751 Report`: throughput, latency retitled `Lat`, backlog and
    prompts, scoped to `f=repo:any:acme/widgets&q=ignored`.
  - `WT3751 Empty`: a dashboard with no panels.
  - `WT3751 Tree`: `q=pagination`, no folder.

  All but the Tree are in folder `WT3751`. They lived only in the
  throwaway store.
- **`/api/metrics`** (`page.route`): a 2.5 s delay, then a 500.
- **`GET /api/boards` → 500** (`page.route`), on a dashboard deep link.
- **`GET /api/boards` aborted** in a second tab (`page.route`), to hold
  that tab's copy of a dashboard stale for step 9.
- **GitHub boundary.** Task detail asks
  `GET /api/repos/<repo>/github-branches`, which answers 404 in demo mode.
  That 404 and its console line were the only errors outside the stubbed
  failures.

## Steps and outcomes

1. **Boards index, rail, URLs, history.**
   - The rail reads `Boards 4` and opens `/t/default/boards`: `Board`,
     `Timeline`, `Report`, `Tree`, count 4. The index has no search or
     sort and no inputs of its own.
   - Deep links:
     - `/boards/board-2` → `/t/default/boards/board-2`.
     - `/t/default/boards/board-4?q=pagination` kept its narrowing.
     - `/boards/board-nope` → the index.
     - `/t/nosuchteam/boards/board-1` → `/t/default/boards/board-1`.
     - `/t/default/boards/board-1/extra` → `/t/default/boards/board-1`.
   - History: index → `Report` → Back → Forward → reload ended on the
     Report board.
   - No page errors.
2. **Asynchronous creation contract.**
   - `New board` opens `New task` at `/t/default/new`, with a 2,745-char
     board prompt ("Add a saved board to grain. …").
   - Appending "Make a list board called 'WT3751 Running' showing only
     running tasks." and pressing `Create task` returned to the index.
   - The new task was `GET /api/tasks/13`: `queued`, approved,
     `grain/config-default`, branch `grain/task-13`, `autoMerge: true`,
     team `default`. The appended text ends its `description`.
   - Boards stayed at 4 in the API: the board appears only when that
     task's change merges, and the stand-ins above are that merge.
   - `Add panel` and `Keep` on a dashboard open the same flow with
     "Add a panel to grain's saved board `board-8` …" and "Give one panel
     of grain's saved board `board-8` … a question of its own". Escape
     returns to the dashboard with no dialog left.
   - `Edit board` on the report names `acme/widgets`, the 90-day window,
     the ignored search and the `Lat` title.
3. **List and Kanban.**
   - `WT3751 Running` lists tasks 3 and 8. Enter on a row's link opens
     the task, and Escape returns focus to the link. After a task has
     been opened, the board's URL carries the board's own query
     (`?f=state:any:running`), with no `Keep` offered: that is the same
     question, written out.
   - `WT3751 Kanban` draws Waiting 4 and Doing 2, with "6 tasks in no
     column (…)". A board with its own columns offers no column editor
     (`fixedColumns`), only the seeded `Board` does.
   - Keyboard: picking up task 13 focuses `Stop moving task 13`, with 3
     slots. Enter on the first slot moved it to the top and kept focus
     on `Move task 13`. The order survived reload.
   - Multi-select block move: tasks 1 and 2 selected, task 1 picked up
     (slots: "above Add a board", "to the end of Waiting"). Both moved
     together to the top in order, and this survived reload.
   - A mouse drag of the last Waiting card onto the first put it first,
     and this survived reload.
   - Clicking a card's title opens the task, and Escape returns focus to
     the title.
   - Seeded `Board`'s `Columns` editor:
     - `+ Add column` focuses `Column 6 title`.
     - A blank title still leaves two arrows and a Remove.
     - Picking `Running` for the new column takes it out of the existing
       Running column. The editor says a state can be in only one column.
       On save the board drew `Running=0` and a new `Running=2`. This
       survived reload (`grain.board.columns`).
     - `Remove` and Save took it away again.
     - Escape on a changed editor asks `window.confirm`. Playwright
       dismisses that, and the editor stayed open.
4. **Timeline.**
   - `WT3751 Gantt` on 7 days: 10 rows, one `.gantt-now` at 100 %,
     ticks Oct 9…15, one `Blocked` chip, "1 task outside this range."
   - Seeded `Timeline` across ranges:
     - 24 hours: 10 rows, 3-hourly ticks, 2 outside.
     - 7 days: 11 rows, 1 outside.
     - 30 days and 90 days: 12 rows, none outside.
     - Everything: 12 rows, ticks Oct 8…14.
   - Lanes:
     - By repo: `acme/widgets`, `grain/config-default`.
     - By state: 9 state lanes.
     - By author: `alice`, `debian`, `director`.
   - 7 days + By repo survived reload, with the URL unchanged (browser
     preferences by design).
   - A `pagination` search narrowed to 2 rows in one lane. URL
     `?q=pagination`, and this survived reload.
   - The Range picker is reached by Tab (34 stops from the top).
   - Clicking a bar ("Queued, blocked: waiting for 48m, never
     dispatched") opens task 11.
   - Bars are `role="img"` and not tab stops. The row's title link is
     the keyboard path.
   - **After a bar click, Escape left focus on `<body>`: finding 1.**
5. **Dashboard.**
   - Panels:
     - States: 1/2/2/3/1.
     - Inherit: "Showing 8 of 9."
     - Running only: 2 rows.
     - Widgets 30d: 12 filed / 3 completed.
     - Day: 8 rows.
     - Nobody: "Nothing here right now."
   - Searching `pagination` narrowed only the inheriting panels (States
     0/1/1/0/0, Inherit 2, Day 2), and each grew `Keep`. Running only,
     Widgets 30d and Nobody were unchanged, with no `Keep`. URL and
     result survived reload.
   - `Open` on each panel landed on a list showing the panel's count
     (2, 2, 2, 0). Metrics opened `/metrics?f=repo:any:acme/widgets&
     f=created:within:30`.
   - The `Proposed 1` state link opened a list of 1, and Back returned
     to the dashboard.
   - Keyboard: moving panel 6 to the top kept focus on
     `Move panel 1, “Nobody”` and persisted (server `orderKey` below the
     first).
   - A mouse drag of a grip onto the first panel moved it there, and this
     survived reload.
   - The empty dashboard says "This dashboard has no panels yet. “Add
     panel” files the task that adds one." Its toolbar and `Add panel`
     remain.
6. **Report.**
   - `WT3751 Report` opens on `?q=ignored&f=repo:any:acme/widgets`. It
     has four handles (`Move section 1, “Throughput”` … `“Prompts”`), the
     retitled `Lat`, and a note that the search is "ignored".
   - Window offers 24h/7d/30d/90d. 90d wrote `&window=90d` and survived
     reload.
   - A keyboard move of section 4 to the top kept focus on
     `Move section 1, “Prompts”` and persisted with the window kept in
     the URL.
   - The seeded `Report` draws Throughput, Latency, Capacity, Backlog, ….
   - Prose sections: none exist. `POST /api/boards` refuses an unknown
     section kind ("kind must be "throughput", … or "merges"") and an
     unknown panel range ("range must be "24h", … or "all""), so the
     "word it could not use" notes are reachable only through a
     hand-written state-repository row.
7. **Tree.** `WT3751 Tree` (`?q=pagination`) nests task 11 under task 8,
   with `Repairing` and `acme/widgets#104`.
8. **Boards index folders and order.**
   - Keyboard: picking up `Move WT3751 Tree` (no folder) offers a slot
     above every board plus "Move here, at the end of WT3751". "Move
     here, above WT3751 Running" moved it into the folder, kept focus on
     its handle, and persisted (`folder: "WT3751"`).
   - Phone: after the folder was reset with `PATCH`, the same move by tap
     worked.
   - No horizontal overflow at 390 px on the index, the Tree, Kanban,
     Timeline, dashboard or report.
9. **Concurrency.**
   - Two tabs had `WT3751 Dash` open, and tab B's board polls were held
     (stub above).
   - Tab A moved `Inherit` to the end. Stale tab B then moved its second
     panel to the end.
   - The server ended with `Inherit` first again: B's whole-array PATCH
     reverted A's move (observation 2).
10. **Sorting and history on a board.**
    - On the Kanban, `Backlog order` → `Sort by…` → Created wrote
      `?sort=created:desc`. It withdrew every move handle and re-ordered
      the cards, and this survived reload.
    - Back from there skips past the board. Narrowing replaces rather
      than pushes, by design (`App.jsx`), and Forward returns.
11. **Loading and error states (stubbed).**
    - Report: a progressbar while the metrics were delayed, then "could
      not be loaded (stubbed failure)".
    - Dashboard: the metrics panel says "Loading…", then "The metrics
      report could not be loaded." The other panels kept their 10 task
      rows.
    - `/api/boards` 500 on a dashboard deep link: the URL was kept. The
      index drew with count 0 under the app's error banner (the poll's
      `showError`), as in the earlier run.
12. **Delete and confirmation.** The UI has no board, panel or section
    delete: no `DELETE` on boards anywhere in `ui/src`. Boards go by a
    state-repository change. The column editor's `Remove` is a draft
    until Save, and a changed editor asks before closing. The stand-ins
    and task 13 lived only in the demo store, which was discarded with
    it.

## Findings

### 1. A task opened by a click off its title gave focus back to `<body>` (fixed here)

Reproduction: on `/boards/board-2` (Timeline), click any `.gantt-bar`.
The task pane opens. Press Escape: the pane closes, and
`document.activeElement` is `<body>`. The same happens for:
- a task-list row clicked to the right of its title (`/boards/board-5`);
- a Kanban card clicked on its body (`/boards/board-6`);
- a dashboard list panel's row and a timeline panel's bar
  (`/boards/board-8`).

This is the 2026-10-13 run's observation 5. A keyboard user is dropped
at the top of the document.

Cause: those rows are plain `<div>`/`<li>` elements with an `onClick`.
The pane hands focus back to whatever held it on opening, which after
such a click is `<body>`.

Fix: `focusRowLink` (`ListPrimitives.jsx`) focuses the row's own
`RowLink` before the row's click opens the task. `TaskList`,
`TaskBoard`'s card and `GanttChart`'s row call it, and the dashboard
panels render those same components. Re-walked on a fresh server, Escape
now returns focus to the row's title link on all five surfaces.

Regression coverage:
- `GanttChart.test.jsx`, `TaskBoard.test.jsx` and `TaskList.test.jsx`
  each have a "puts focus on the row's/card's link" case. All three fail
  with the fix reverted (3 failed, 162 passed).
- e2e `5c` in `saved-boards-walkthrough.spec.js`.

### Observations (not changed)

1. **The 2026-10-13 timeline-panel `Open` gap remains.** A 24h timeline
   panel's `Open` lands on the inheriting list, not on the panel's range.
   No list condition expresses "overlaps this window".
2. **Dashboard panel and report section reorders are last-writer-wins
   across tabs.** A tab holding a stale copy of a board sends the whole
   `panels` array and silently reverts a move made elsewhere (step 9).
   `panelOrder.js` documents this as a deliberate choice: `PutBoard`
   rewrites every row anyway, and the other writer is the
   state-repository import. The window is one board poll in practice,
   and the cost is a dashboard in the wrong order until somebody moves a
   panel again. So this was not filed.
3. **A failed `/api/boards` load draws the index as "Boards 0".** The
   error banner explains it, and the next good poll recovers. The index
   itself cannot tell "failed" from "empty".
4. **Two columns can share a title.** Moving a state into a new column
   leaves the old column empty under its old name, so the board can draw
   two "Running" columns. The editor's own note explains the one-column
   rule.

## Automated tests

- `npx vitest run src/boards.test.js
  src/components/BoardColumnsOverlay.test.jsx
  src/components/BoardsPage.test.jsx src/components/TaskBoard.test.jsx
  src/components/ReportBoard.test.jsx src/components/PanelBoard.test.jsx
  src/components/GanttChart.test.jsx src/components/TaskList.test.jsx
  src/components/TaskTreeBoard.test.jsx src/App.test.jsx`: 10 files,
  361 passed.
- `npx playwright test e2e/saved-boards-walkthrough.spec.js
  e2e/boards.spec.js e2e/board.spec.js e2e/gantt.spec.js
  e2e/boards_workflows.spec.js e2e/keyboard-open-rows.spec.js
  e2e/tasks.spec.js --project=chromium`: 60 passed (54.6 s).

---

# Second run: 2026-10-15 (18:54–19:20 UTC)

Commit tested: `c15ab8048` (`Merge pull request #3953 from
bwsalmon/grain/eu/task-4119`, tip of `origin/candidate`, checked out as
`grain/eu/task-4025`). Both fixes below were rebuilt into a fresh server
(`go build` after `npm run build`), and the affected surfaces were walked
again.

This run went after what the first run above covered thinly:
- the Tree board's toolbar (sort, filters, Clear) and its links;
- the seeded Kanban's column moves, and drags across columns;
- every panel's `Open` against the list it lands on;
- empty narrowing across a whole dashboard;
- report sections in an order of the board's own, and a failed refresh;
- copyable (team-less) hrefs;
- the close affordance on the board-request task this run filed.

## Environment

Same as the first run, except as noted here:
- Debian `x86_64`, Go 1.26.6, Node.js 20.19.0.
- Chromium headless shell 151, driven by standalone Playwright scripts
  (not committed).
- `/tmp/grain demo -addr 127.0.0.1:8787 -open=false` from an empty
  directory. It was restarted once, after the Tree fix, which reset the
  store.
- Desktop 1440×1000 (1440×1800 for dashboard and report), plus an
  iPhone 13 profile.
- `grain.username = alice`, `X-Grain-Admin-Mode: on`.
- No `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed
  model or tooling was used. No flow here needed one.

## Stubs and stand-ins

- **Merged board changes (stand-in).** A sandbox cannot merge the
  state-repository pull request that creates a board. After the real
  `New board` flow (step 2), the boards were written with
  `POST /api/boards`, folder `WT4025`, in the throwaway store only:
  - `WT4025 Widgets tree`: tree, `f=repo:any:acme/widgets`.
  - `WT4025 Dash`: panels. States, an inheriting list, "Queued newest"
    (`f=state:any:queued&sort=created:desc`), a 7d full-width gantt,
    and "Widgets 7d" metrics.
  - `WT4025 Report`: throughput, backlog, merges, checks, cycles and runs,
    on `f=repo:any:acme/widgets`.

  A duplicate `WT4025 Dash` came from reseeding after the restart. It was
  left in place, and the index shows it.
- **`/api/metrics` → 500** (`page.route`): first on a report Refresh,
  then on a fresh load of the report.
- **GitHub boundary.** In demo mode task detail's
  `GET /api/repos/<repo>/github-branches` answers 404 and logs a console
  error. Outside the stubs, that was the only error any script recorded.

## Steps and outcomes

1. **Index and history.**
   - The rail opens `/t/default/boards`, count 7. The `WT4025` folder
     holds its boards, after the four seeded ones.
   - From the keyboard, `New board` is 33 Tab stops from the top.
   - Index → `WT4025 Widgets tree` → task 12 → Back → Back → Forward →
     reload ended on the board, with its query in the URL and its 12 rows.
   - Team-less links resolve:
     - `/boards/board-5` → `/t/default/boards/board-5?f=repo:any:acme/widgets`.
     - `/tasks/11` → `/t/default/tasks/11`.
     - `/?sort=created:desc&f=state:any:queued` → the team's task list,
       2 rows.
2. **Asynchronous creation.**
   - `New board` (reached by keyboard) opens `/t/default/new` with the
     2,745-char board prompt.
   - Appending "Make a tree board called 'WT4025 Widgets tree' …" and
     pressing `Create task` filed task 13: `queued`, approved, repo
     `grain/config-default`, branch `grain/task-13`, `autoMerge: true`.
     The appended text ends its description.
   - `GET /api/boards` was unchanged until the stand-in was written.
   - On the tree, `Edit board` opens a prompt naming `board-5`, its
     folder, kind and query.
3. **Tree.**
   - The seeded `Tree` nests 1, 3 and 8 under 12, and 11 under 8, which
     matches `generatedFrom` in `/api/tasks`.
   - `WT4025 Widgets tree` opens on its saved query (`Filters (1)`), and
     the query survives reload.
   - Searching `pagination` narrowed to 8 → 11 and wrote `?q=pagination`.
     It survived reload, and `Clear` restored every row.
   - Clicking a row's id opens the task, and Escape returns focus to that
     link.
   - **Sort: `Sort by…` → Title wrote `?sort=title:asc` and the tree did
     not move: finding 1.** After the fix, the roots read 6, 12 (1, 8 → 11,
     3), 2, 4, … and the order survived reload.
   - No overflow at 390 px.
4. **Kanban (seeded `Board`).**
   - In `Columns`, the first column's left arrow and the last column's
     right arrow are disabled.
   - Enter on `Move Proposed right` twice kept focus on that button. Save
     drew Queued | Running | Proposed | …. That order survived reload and
     a second tab.
   - `Reset to default` then Save restored the seeded order.
   - Dragging a Queued card onto Running changed nothing, server state
     included. That is by design: a task's state is derived
     (`TaskBoard.jsx`, "Why a card cannot be dragged into another column").
   - Picking up task 1, alone in Proposed, offers no slots. Escape puts
     it down and keeps focus on its handle.
5. **Timeline (seeded `Timeline`).**
   - On 7 days: 10 rows and one `.gantt-now` at 100 %. Task 11's row
     carries `gantt-row-blocked` and a "blocked" chip. Nothing on it names
     task 8 (observation 2).
   - Everything + By state gave 9 state lanes and 11 rows (the director's
     session row is left out, as on the list). Both settings survived
     reload.
   - Enter on a row's title opens it, and Escape returns focus to the
     title.
   - Phone: tapping a bar opens task 1, with no horizontal overflow.
6. **Dashboard (`WT4025 Dash`).**
   - Each panel's `Open` landed on a list with the panel's own count:
     States 9 → 9, Inherit 9 → 9, Queued newest 2 → 2, Week 9 → 9.
     Metrics opened `/metrics?f=repo:any:acme/widgets&f=created:within:7`.
   - The States panel's `Running` link opened the 2 running tasks, and
     Back returned to the dashboard.
   - A gantt-panel bar opens its task, and Escape returns focus to the
     row's link.
   - `?q=zzzznothing`: the inheriting panels read 0. They show "Nothing
     here right now." or "No tasks match … in this range." and each grows
     `Keep`. "Queued newest" and the metrics panel are unchanged, with no
     `Keep`.
   - With `?q=gemini`, the inheriting panels read 1. `Keep` on Inherit
     opens the "Give one panel … a question of its own" prompt, and Escape
     returns to `?q=gemini` with no dialog left.
   - A keyboard move of panel 5 to the top kept focus on
     `Move panel 1, “Widgets 7d”` and survived reload. The server holds
     `orderKey -1048575`.
   - `Add panel` opens the "Add a panel to … `board-8`" prompt.
7. **Report (`WT4025 Report`).**
   - It opens on `?f=repo:any:acme/widgets` and says "Measured over the
     tasks in acme/widgets." It has six handles, Throughput … Capacity.
   - Window 30 days wrote `&window=30d`.
   - A mouse drag of `Move section 6` onto section 1 put Capacity first.
     The order and the window survived reload.
   - "Oldest queued: task 2" opens task 2, and Escape returns focus to
     that button.
   - **The backlog caption said "Read it against the capacity above"
     while Capacity was drawn last: finding 2.**
   - Stubbed 500 on Refresh: the report already on screen stays, and the
     app banner reads "500 Internal Server Error". On a fresh load:
     "The report could not be loaded (500 Internal Server Error). Refresh
     to try again." Both match `ReportBoard.jsx`'s stated rule.
8. **Closing the throwaway task.** Task 13 offers `Close`. It closes at
   once, with no confirmation, and offers `Reopen`. It is reversible, so
   no confirmation is owed. The UI still has no board, panel or section
   delete. Everything here lived only in the demo store.
9. **Phone (iPhone 13).** No horizontal overflow on `board-1`, `-2`, `-4`,
   `-5`, `-7` or `-8`.

## Findings

### 1. A Tree board's sort changed the URL but not the tree (fixed here)

Reproduction:
1. Open `/t/default/boards/board-4`.
2. Click `Backlog order` → `Sort by…` → `Title`.

The URL gains `?sort=title:asc`, but the rows stay in id order (12, 1, 3,
8, 11, 10, …). The toolbar's word for the unsorted order, "Backlog order …
the one a drag changes", was wrong for the tree as well: the tree draws
newest first and has no drag.

Cause: `forestFromTasks` (`workTree.js`) re-sorted roots (newest first)
and siblings (by id), which discarded the order `narrowTasks` produced.

Fix:
- `forestFromTasks(rows, { ordered: true })` keeps the rows' own order
  for roots and siblings. Follow-ups stay under the task that filed them.
- `TaskTreeBoard` passes `ordered` when the narrowing has a sort, and
  names its unsorted order "Newest first".

Regression coverage:
- `workTree.test.js`: "keeps the rows' own order when they arrive sorted".
- `TaskTreeBoard.test.jsx`: "orders roots and siblings by the sort it is
  given" and "names its unsorted order for what it is".
- All three fail with the fix reverted.
- e2e: `work-tree.spec.js`, "a Tree board's sort orders the tasks under
  each parent".

### 2. The backlog caption pointed at a capacity section "above" that a report board need not have there (fixed here)

Reproduction: a report board with sections `backlog` then `runs` (or with
Capacity dragged to the end). The Backlog caption says "Read it against
the capacity above".

Fix: the caption now says "the report's capacity" (`MetricsSections.jsx`).

Regression coverage: `ReportBoard.test.jsx`, "does not place capacity
above the backlog on a board that reorders them".

### Observations (not changed)

1. **The first run's observations 1–4 still hold.** Nothing here changed
   them.
2. **Timelines draw no dependency.** A blocked task's row says "blocked",
   but neither the row nor the chip names the task it waits on (task 11 on
   task 8). Task detail does name it. This is a presentation gap rather
   than wrong data, so it was not filed.
3. **A hand-written board URL that carries only `?sort=`** drops the
   board's saved filters. For example, `/boards/board-5?sort=title:asc`
   draws 13 rows, not 12. That is `App.jsx`'s stated rule: a link that
   says what it wants is not overruled by the board. The UI's own sort
   writes the board's filters into the URL beside the sort, so this is
   reached only by editing the URL.
4. **`Close` on a task asks nothing.** It is reversible (`Reopen`).

## Automated tests (second run)

- `npx vitest run` over `boards`, `workTree`, `BoardColumnsOverlay`,
  `BoardsPage`, `TaskBoard`, `ReportBoard`, `ReportBoard.sections`,
  `PanelBoard`, `GanttChart`, `TaskList`, `TaskTreeBoard` and `App`: 12
  files, 418 passed.
- `npx playwright test e2e/saved-boards-walkthrough.spec.js
  e2e/boards.spec.js e2e/board.spec.js e2e/gantt.spec.js
  e2e/boards_workflows.spec.js e2e/work-tree.spec.js e2e/metrics.spec.js
  --project=chromium`: 57 passed (1.1 m).
