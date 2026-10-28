# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-04 (03:25–04:10 UTC)

Commit tested: `f56fe0d45` (base, `candidate` tip, v1.7.0-rc.1). The fixes
from this run are on `grain/task-1814`:

- `f7f57aed5`: keyboard focus after moving a dashboard panel or report
  section.
- `4bec5a202`: a dashboard list panel says when it shows only some of its
  tasks.
- `713e4898b`: e2e `6d` in `saved-boards-walkthrough.spec.js` covering both.

The steps that found them were run again on a demo rebuilt from
`4bec5a202`. Those results are recorded under "After the fix".

## Environment

- Debian 12 (bookworm) sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0,
  Vite 6, Playwright 1.62.1 with Chromium headless shell (build 1234).
- The frontend was built with `npm ci && npm run build` into
  `pkg/ui/static`, and only after that the binary with
  `go build -o bin/grain ./cmd/grain`. Building the binary first embeds the
  placeholder page, as the 2026-10-03 report warns.
- The binary was served by `bin/grain demo -addr 127.0.0.1:8420 -open=false`
  over its own throwaway embedded store, with `HOME` and the working
  directory set to `/tmp/demo`. The store was thrown away and the server
  restarted twice, after each rebuild with a fix.
- Real Chromium was driven by Playwright scripts kept outside the
  repository (`/tmp/wt`, not committed). The viewport was 1440x900, with
  1100–2400px tall viewports where a whole dashboard or report had to be on
  screen. There was one phone pass with Playwright's `iPhone 13` device
  (390x844, touch).
- Every page had listeners for console errors and warnings, `pageerror`,
  `requestfailed` and HTTP status ≥ 400.
- `npx playwright install` needs
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the
  package cache proxy, as before.
- There was no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed tooling was used.
- Identity: the seeded team member `alice`, set through
  `localStorage["grain.username"]`, as the e2e fixture does.

## Stubs and boundaries

- **State-repository reconciler.** New board, Edit board, Add panel and
  Keep file a task against `grain/config-default`. A board only appears
  once an agent's pull request to `tables/board*.json` merges, and the demo
  has no agent or GitHub to do that.
  - The asynchronous half was exercised through the UI (step 2).
  - The boards such a merge would produce were stood in for with
    `POST /api/boards`:
    - `WT List` and `WT Kanban` (own columns Waiting/Going/Done), filed in
      `WT Folder`.
    - `WT Gantt` (`f=repo:any:acme/widgets`).
    - `WT Dash`, with six panels: states, a list scoped to running, a 7d
      gantt, 30d metrics scoped to acme/widgets, a list matching nothing,
      and an inheriting full-width list.
    - `WT Report`, with all ten named sections, one retitled
      "Sandbox use", scoped to acme/widgets.
    - Before the first restart only: `WT Edge Dash` (an unknown attribute,
      a 24h metrics panel, a sort-only states panel, a priority-sorted
      list), `WT Empty Dash` and `WT Empty Report`.
    - After the restart: `WT Dup Dash` (two identical list panels and a
      states panel).
  - The stores were discarded at the end. The UI has no board delete.
- **Network faults on `/api/metrics`.** To see the loading and error
  rendering, Playwright's `page.route` was used in step 6 only:
  - once to abort the request (`connectionrefused`);
  - once to delay it 4s and then let the real server answer.

  No response body was invented.
- **GitHub.** Opening any task requests
  `GET /api/repos/<repo>/github-branches`, which returns 404 because the
  demo has no GitHub. That 404 and the console error it logs are expected,
  and they are the only console errors in every log below apart from the
  deliberate network faults.
  - `GET /api/tasks` 304 polls that Chromium reports as `ERR_ABORTED` were
    filtered out, as the 2026-10-03 report explains.

## Steps and outcomes

1. **Boards index and rail.**
   - The rail's "boards" entry showed 8, and so did the `/boards` header.
   - The index lists the loose boards in server order, then the
     `wt folder` heading with WT List and WT Kanban. The index has no search
     or sort field (no inputs at all), by design.
   - Clicking WT List opened `/boards/board-4?q=pagination` with 2 rows.
     Typing "extract" in the search field rewrote it to `?q=extract`
     (1 row).
     - Reload kept the URL, the field and the row.
     - Back went to `/boards` and Forward returned, still with 1 row.
   - Every board opened cold from its URL. A board whose query names a repo
     (WT Gantt, WT Report) opens with that query in its address.
   - `/boards/board-99`, `/boards/` and the id of a create that was refused
     (`board-5` on the first store) all fell back to `/boards`.
2. **Asynchronous creation.**
   - Create options → New board, from `/?q=pagination&sort=created:desc`,
     opened the New task form prefilled with:
     - `Add a board`, `kind: list`, and that query;
     - the kind menu;
     - the state-repository note;
     - repo `grain/config-default`, queued immediately, auto-merge on.
   - With a name filled in, Create task filed task 14 (`queued`,
     `grain/config-default`, auto-merge). The board count stayed 8 → 8, and
     the page stayed on the list it was filed from.
   - The task detail shows the full prompt, including
     `query: q=pagination&sort=created:desc`.
   - After the restart:
     - New board from the WT Gantt page carried `kind: gantt` and
       `` query: `f=repo:any:acme/widgets` ``.
     - Edit board on WT Kanban carried "Its columns, left to right" with
       Waiting/Going/Done, which is the 2026-10-03 fix and still holds.
     - Both were filed as queued tasks 13 and 14. The board count was
       unchanged.
3. **List and kanban (seeded Board).**
   - Columns: Proposed[1] Queued[2,11,12,13,14] Running[3,8] Needs
     you[4,6,7] Queued for merge[5].
   - Clicking a card opened `/tasks/1`, and Escape returned. Enter on a
     focused card title opened `/tasks/2`, and Back returned.
   - Keyboard pickup of task 14 offered 4 slots. "Move here" at the top
     gave `14,2,11,12,13`.
   - A mouse drag of 13 onto 2 gave `14,13,2,11,12`.
   - Both orders survived reload.
   - A drag from Queued onto a Running card moved nothing, and the task
     stayed queued.
   - Columns dialog:
     - Moving Running left with Enter on "Move Running left", renaming
       column 1 to "Ideas" and adding a column "Parked" all applied on
       Save and survived reload. They are kept in
       `localStorage["grain.board.columns"]`.
     - The dialog lists the states that are off the board ("Deferred,
       Closed").
     - Reset to default → Save restored the five default columns.
   - WT Kanban (own columns) drew Waiting[1,14,13,2,11,12] Going[3,8]
     Done[5]. It offers no Columns button, and its note says "5 tasks in no
     column (Awaiting reply, Awaiting submit, Awaiting ack, Deferred,
     Closed)".
4. **Timeline (seeded Timeline, WT Gantt).**
   - Last 7 days: 13 bars, "1 task outside this range.", daily ticks Sep 28
     to Oct 4, and one `.gantt-now` marker.
   - Opening Range from the keyboard (Enter) offered 24h/7d/30d/90d/
     everything.
     - 24 hours gave 12 bars, "2 tasks outside this range." and 3-hourly
       ticks.
     - Lanes → by state gave seven lanes with counts summing to 12.
   - Reload kept the URL (`/boards/board-2`), the range and the lanes. Both
     settings are in localStorage, not the URL, by design.
   - Clicking the bar of "Extract pagination query helper" opened
     `/tasks/11`. Back returned with the range and lanes kept. Enter on a
     focused row title opened `/tasks/1`.
   - WT Gantt drew 10 bars in 24h, the same 12 minus the two
     `grain/config-default` board requests.
     - Searching "pagination" made the URL
       `?q=pagination&f=repo:any:acme/widgets` with 2 bars, and reload kept
       that.
     - Searching "zzzznothing" said "No tasks match this search and these
       filters in this range."
   - Each bar has an `aria-label`, for example "Queued: waiting for 43m,
     never dispatched".
   - Dependencies: task 11 depends on 8 and is blocked. The timeline still
     draws no link, which is by design (`gantt.js`, "What is deliberately
     not here"). It also gives no blocked marker: the row's badge is plain
     `badge-queued`, and its tooltip says "waiting for 43m, never
     dispatched". The list row and the card do carry "blocked". This is
     proposed as a follow-up (see Findings).
5. **Dashboard (WT Dash, WT Edge Dash, WT Empty Dash, WT Dup Dash).**
   - Unnarrowed panels:
     - States: 12, with columns 1/5/2/3/1.
     - Running only: 2 rows.
     - Week: header 12 and "Showing 10 of 12 in this range.".
     - Widgets metrics: 12 filed / 3 completed.
     - Nothing matches: "Nothing here right now."
     - Inherit list: header 12 and **8 rows with no note** (finding 2).
   - Searching "pagination" narrowed only the panels that follow the
     dashboard: States 0/1/1/0/0, Week 2, Inherit list 2. Running only,
     Widgets metrics and Nothing matches kept their own questions.
     - The URL carries the whole narrowing, and reload kept it.
     - Keep appeared only on the three inheriting panels.
   - Every Open link was followed, with Back after each:
     - inheriting panels → `/?q=pagination&f=state:none:deferred|closed&f=kind:none:interactive`
       (2 rows);
     - Running only → `/?f=state:any:running` (2);
     - Widgets metrics →
       `/metrics?f=repo:any:acme/widgets&f=created:within:30`;
     - Nothing matches → `/?q=zzzznothing` (0).

     Back returned to the narrowed dashboard every time.
   - Clicking a panel row opened the task, and Escape returned.
   - Keep on Inherit list prefilled `Narrow the panel "Inherit list" on the
     dashboard "WT Dash"`. Its prompt lists all six panels with
     `order_key`s, marks panel 6, and asks for the on-screen query. Add
     panel prefilled the same panel list. Escape closed the form.
   - Edge panels:
     - A panel query naming `bogus` said "Unknown condition: bogus (not
       applied)."
     - A sort-only states panel counts every task, with "2 in no column".
     - A metrics panel with no query of its own follows the board's
       `q=pagination`.
     - `range: "all"` on a metrics panel was refused by the API (`panel 2:
       range must be "24h", "7d", "30d" or "90d"`).
   - An empty dashboard says "This dashboard has no panels yet. “Add panel”
     files the task that adds one."
   - Reordering:
     - A keyboard pickup of panel 6 offered 5 slots, and putting it above
       panel 1 persisted `orderKey -1048575`.
     - A mouse drag of the States header onto Nothing matches persisted
       `4.5`.
     - Reload kept both.
     - **After the keyboard move, focus was on "Move panel 6, “Nothing
       matches”" rather than on the panel that moved** (finding 1).
   - Loading and error, with the network stub:
     - A refused `/api/metrics` makes the metrics panel say "The metrics
       report could not be loaded."
     - A 4s delay shows "Loading…" and then 12 filed / 3 completed.
6. **Report (WT Report, seeded Report).**
   - WT Report opens at `?f=repo:any:acme/widgets`.
     - Throughput: 11 filed, 3 completed.
     - Latency has a table and Capacity has figures.
     - Backlog: "Oldest queued: task 2". Clicking it opened `/tasks/2`.
     - Tool use, Prompts, CI waits, Scheduling, Sandboxes and Merge queue
       each say "nothing to report over this window".
     - The retitled section heads itself "sandbox use".
   - A keyboard pickup of section 10 (Merge queue) offered 9 slots. Putting
     it first persisted, and reload kept it. **Focus after the move was on
     "Move section 10, “Sandbox use”"** (finding 1 again).
   - With `/api/metrics` refused, the report said "The report could not be
     loaded (Failed to fetch). Refresh to try again." With the network back,
     Refresh drew all 10 sections.
   - With it delayed, the seeded Report showed its toolbar and spinner, and
     then 11 sections, Merge queue included.
7. **Delete and confirmation.**
   - The UI still exposes no board delete (no delete or remove control on
     `/boards`), and no other destructive control in this area.
   - On task 13, this run's own throwaway request, the task pane's Close
     closed the task at once with no confirmation. After reload it offers
     Reopen. That is unchanged from earlier runs: it is reversible.
   - Boards index:
     - A keyboard pickup of "Move WT List" offered only legal slots,
       including "outside every folder" and "at the end of WT Folder".
     - The second persisted `orderKey 1048576` in WT Folder, and focus
       returned to "Move WT List".
     - Reload kept the order.
8. **Phone (390x844, touch).**
   - `/boards`, a list, kanban, timeline, dashboard and report all had
     `scrollWidth` equal to `clientWidth` (390).
   - Tapping panel 2's handle and then the first slot moved "Running only"
     to the top, with focus on its own handle (fixed build).
   - Tapping a kanban card opened the task.

## Findings

### Fixed in this task

1. **A keyboard move of a dashboard panel or report section left focus on
   a different panel or section.**
   - Reproduction: on any dashboard with three or more panels, focus "Move
     panel 6, …", press Enter, focus "Move here, above panel 1, …" and
     press Enter.
     - The panel moves and the move persists.
     - Focus is on "Move panel 6, “Nothing matches”", which is the panel
       that slid into the old place. The next Enter picks up that panel.
     - The report does the same ("Move section 10, “Sandbox use”" after
       moving Merge queue to the top).
   - Cause: panels and sections are React-keyed by their position
     (`panel-N` / `section-N`, the names a move uses). When the board comes
     back reordered, each element stays in place and is handed the next
     child's contents. `MoveHandle`'s focus return then lands on the old
     position.
     - Kanban cards (task id) and the boards index (board id) have stable
       keys and were not affected.
   - Fix (`f7f57aed5`):
     - `childRenderKeys` in `childOrder.js` keys each child by its stored
       row, counted so that identical panels still differ.
     - Moves keep naming children by position.
   - After the fix:
     - WT Dash: focus is on "Move panel 1, “Inherit list”" and "Move panel
       5, “States”".
     - WT Dup Dash (two identical list panels): "Move panel 1, “State
       counts”" and "Move panel 3, “State counts”".
     - A second Enter picks up the moved panel.
     - WT Report: focus is on "Move section 1, “Merge queue”".
   - Regression coverage:
     - `PanelBoard.test.jsx` "leaves focus on the moved panel's own handle"
       and `ReportBoard.test.jsx` "leaves focus on the moved section's own
       handle". Both fail on the old keys.
     - E2e `6d` in `saved-boards-walkthrough.spec.js`.
2. **A dashboard list panel cut to eight rows without saying so.**
   - Reproduction: a list panel over more than eight tasks shows a header
     count of 12 and 8 rows, with nothing saying that 4 are not drawn.
     - `LIST_PANEL_ROWS`'s own comment says the panel "says how many more
       there are". Nothing has done so since the panel was written
       (`fc81a306d`).
     - The timeline panel beside it says "Showing 10 of 12 in this range."
   - Fix (`4bec5a202`):
     - The list panel prints "Showing 8 of 12." above its rows, in the
       style of the panel's other notes.
     - It prints nothing when every row fits.
   - After the fix: Inherit list read "Showing 8 of 10." on the new store.
     Running only (2 rows) and the narrowed Inherit list (2 rows) had no
     note.
   - Regression coverage:
     - Two `PanelBoard.test.jsx` cases. "says how much of a list panel it
       left out" fails without the fix.
     - E2e `6d`, which fails against the base commit's components.

### Proposed as a follow-up

- **The timeline does not show that a task is blocked.** Task 11 (depends
  on 8) reads "waiting for 43m, never dispatched" on its bar and tooltip.
  Its row has no blocked marker, though the list row and the kanban card
  carry "blocked", and `gantt.js` says dependencies "are on screen, as the
  Blocked chip a row and a card already carry". Filed as a candidate-branch
  follow-up.

### Observations (not changed)

- A refused `POST /api/boards` still uses up a board id (`board-5` and
  `board-10` here), as the 2026-10-03 report noted. It is harmless: the id
  is never served, and its URL falls back to `/boards`.
- After a keyboard pickup, the "Move here" slots sit in DOM order before
  their target rows. For a card or panel picked up at the end, the slots
  are reached with Shift+Tab rather than Tab. This is the shared
  `ReorderPickup` design, unchanged.
- The New board prompt's state-repository note names
  `tables/board_column.json` and `tables/board_section.json` whatever the
  kind. It names `tables/board_panel.json` only for a panels board, which a
  `boards.test.js` case pins on purpose.
- The report's Backlog "task 2" reference is a button, not a link. The
  report's Window and the timeline's range and lanes are not in the URL.
  The browser tab title is the deployment name. All of these are by design,
  and unchanged since 2026-10-03.
- No report section kind is prose, so a prose section could not be
  exercised.

## Automated verification

- Vitest, whole suite: 170 files, 2901 tests passed.
- Playwright on chromium, run against the fixed build:
  `saved-boards-walkthrough.spec.js`, `boards.spec.js`, `board.spec.js` and
  `gantt.spec.js`. There were 39 passed before `6d` was added, and `6d`
  passes on its own.
- Grain CI `frontend` job passed on `713e4898b`.
- No screenshots, traces or other browser artifacts are committed.
