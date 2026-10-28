# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-02 (15:12–15:40 UTC)

Commit tested: `cf9d6156c` (base, `candidate` tip). Fixes from this run are
on `grain/task-1531` (`08e038f4c`, `131a336ce`, with e2e coverage in
`5c7223206`). The parts of the walkthrough that found them were run again on
the fixed build.

## Environment

- Debian 12.15 sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6.4.3,
  Playwright 1.62.1 with Chromium headless shell 151.
- The frontend was built with `npm ci && npm run build` into `pkg/ui/static`
  and the binary with `go build -o bin/grain ./cmd/grain`. It was served by
  `bin/grain demo -addr 127.0.0.1:8420 -open=false` over its own throwaway
  embedded SQLite store, with `HOME` and the working directory set to
  `/tmp/demo`.
- Real Chromium was driven by Playwright scripts kept outside the repository
  (`/tmp/wt`, not committed). The viewport was 1440x900, plus taller
  viewports (1800 and 2400) where a drag target or the whole report had to be
  on screen, and one phone pass at 390x844 with `hasTouch`/`isMobile`.
- Every page had listeners for console errors and warnings, `pageerror`,
  `requestfailed` and HTTP status ≥ 400.
- There was no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed tooling was used.

## Stubs and boundaries

- **State-repository reconciler.** "New board", "Edit board", "Add panel"
  and "Keep" file a task against the state repo (`grain/config-default` in
  the demo). A board only appears once an agent's PR to `tables/board*.json`
  merges, and the demo has no agent or GitHub for that.
  - The asynchronous half was exercised through the UI (step 2).
  - The resulting boards were stood in for with `POST /api/boards`:
    `WT List`, `WT Dash`, `WT Report`, `WT Gantt` and `WT Bad`.
  - They were removed with `DELETE /api/boards/{id}` at the end (all 204),
    because the UI has no board delete. This is the only data stub.
- **Identity.** The demo now seeds `admin`, `alice`, `operator` and others,
  and since `f57ce0cc7` ("only bootstrap the first user on an empty
  deployment") an account created through the "Create account" dialog is not
  a member of team `default`.
  - Filing the New board task as the new `wtuser` was refused, with the
    server's reason shown in the form: `user "wtuser" does not have
    permission to submit tasks for team "default"`. That is correct RBAC,
    not a board defect.
  - The rest of the walkthrough ran as the seeded member `alice`, set through
    `localStorage["grain.username"]`. That is the same identity the e2e
    fixture (`e2e/pageerrors.js`) uses.
  - The note from the 2026-10-01 run that a freshly created account can file
    the task is out of date.
- **GitHub.** Opening any task requests
  `GET /api/repos/<repo>/github-branches`, which returns 404 in the demo
  because it has no GitHub boundary, so the console logs one resource error.
  This is expected and not counted as a finding.
- `GET /api/tasks net::ERR_ABORTED` lines are in-flight polls cancelled by
  `page.goto`/reload, and are harmless.

## Steps and outcomes

1. **Boards index and rail.**
   - Rail "boards 3" → `/boards` lists Board (kanban), Timeline, Report. With
     the throwaway boards seeded, the rail and header both read 8.
   - The `WT Folder` heading groups its boards after the loose ones, in
     server `orderKey` order. The index offers no search or sort, by design.
   - Opening `WT List` from the index lands on
     `/boards/board-4?q=pagination` (the board's own query).
   - Typing a search rewrites `q` in place. The URL survives reload. Back
     returns to `/boards` and Forward to the narrowed board.
2. **Asynchronous creation.**
   - Create options → New board, opened on `/?q=pagination`, prefilled
     `Add a board` with ``query: `q=pagination` `` and `kind: list`.
   - As `alice`, Create task filed task #14 (queued, against
     `grain/config-default`) and returned to `/?q=pagination`. The board
     count stayed at 8, as the async contract requires.
   - Edit board on the kanban prefilled `Change the board "Board"` with the
     current row. Add panel and Keep on the dashboard listed every panel
     with its persisted `order_key`, and Keep marks the panel it is about.
   - **Bug 2 found here:** see below.
3. **Folders and ordering.**
   - Keyboard pickup on "Move WT Gantt" offered only legal slots (its own
     place left out) and moved it to "at the end of WT Folder".
   - Persisted as `folder: "WT Folder"`, `orderKey: 1048576`, and kept
     across reload.
   - Escape during a pickup removes every slot.
4. **List and kanban.**
   - List board: clicking a row opens `/tasks/11`, Escape returns, and
     Enter on a focused row opens it. Rows now have `tabIndex=0`: the
     2026-10-01 keyboard-access follow-up has landed.
   - Kanban Columns dialog:
     - "+ add column" titled "Parked" with state Deferred, then "Move Parked
       left" → Save, drew Parked before "Queued for merge".
     - The hidden-states note went from "2 tasks in no column (Deferred,
       Closed)" to "1 task … (Closed)", and reload kept it.
     - It is kept in this browser (localStorage), as the dialog says. A
       fresh browser context shows the default columns.
   - Card moves within Queued: keyboard pickup of task 12 → "Move here,
     above Fix flaky retry test" gave `12,2,11`. A mouse drag of task 11 to
     the top gave `11,12,2`. Both persisted across reload.
   - A tap on a card at 390px opened the task, and Back returned to the
     board.
5. **Timeline.**
   - Range offers 24h/7d/30d/90d/Everything, and lanes offer none/repo/
     state/author. 7d showed "1 task outside this range" and the today
     marker (`.gantt-now`).
   - 30d + By state drew one lane per state. The choice survived reload
     (localStorage, per browser, not in the URL). The same choice then
     applied to `WT Gantt`, because it is one browser-wide setting.
   - Clicking a bar opened `/tasks/1`, and Escape returned to
     `/boards/board-2`. A search narrowed the bars from 11 to 2 with
     `?q=pagination` in the URL.
   - No dependency links are drawn (task 11 is blocked by 8), which is
     unchanged. Nothing in GanttChart renders them.
6. **Dashboard.**
   - Typing "pagination" narrowed the inheriting panels (States, Inherit
     list, Week: 10 → 2). The scoped panels (Running only: 2, Nothing
     matches: 0, Widgets metrics: 11 filed / 3 completed) kept their own
     queries. Reload kept the narrowing.
   - Open on an inheriting panel went to `/?q=pagination&…` with the same
     count, and Back returned to the board. Open on "Running only" went to
     `/?f=state:any:running` (2 rows).
   - Clear restored the deployment default narrowing.
   - "Nothing matches" says "Nothing here right now."
   - Keyboard pickup of panel 6 ("Week") offered five "Move here, above …"
     slots, and the move to the top persisted (`orderKey -1048576`).
   - A native mouse drag of "States" onto "Widgets metrics" filed it at
     `orderKey 3.5`, and reload kept it. The first two drag attempts did
     nothing only because the target was below the 900px viewport, so the
     drag never reached it. That was a scripting issue, not a defect.
   - **Error and odd input panels (`WT Bad`).**
     - A metrics panel over `nobody/nothing` reads 0/0 with a title "Measured
       over the tasks in nobody/nothing.", which is correct.
     - A panel query of `f=nosuchattr:any:x` showed "Unknown condition:
       nosuchattr (not applied)". The 2026-10-01 follow-up has landed.
     - A panel query of `f=state:bogus:running` showed every task with no
       notice. **Bug 1 found here:** see below.
     - The server rejects a panel `range` outside its vocabulary at create
       time (`400 range must be "24h", …`).
7. **Report** (`WT Report`, all 11 section kinds, scoped to
   `f=repo:any:acme/widgets`).
   - The scope line read "Measured over the tasks in acme/widgets.".
   - Throughput, Latency, Attempts (drawn as "Capacity") and Backlog had
     data. The other seven printed "<Section>: nothing to report over this
     window.", the task-1356 fix.
   - The "task 2" link in Backlog opens the task.
   - Keyboard pickup of section 3 → "Move here, above section 1" put it
     first, and reload kept it.
   - The Window picker (24h/7d/30d/90d) re-asked
     `/api/metrics?repo=acme%2Fwidgets&window=30d&buckets=24`.
   - A search added "…not in force: the search for "pagination"".
8. **Delete and confirmation.** The UI still has no board delete. Close on
   the throwaway task #14 took effect at once with no confirmation, and
   Reopen is offered, so it is reversible. No other destructive control is
   exposed in this area.
9. **Phone (390x844, touch).** `/boards`, list, kanban, timeline, dashboard
   and report had no horizontal overflow.

## Findings

### Fixed in this task

1. **A filter comparison grain does not know was dropped in silence.**
   - Reproduction:
     - `/?f=state:bogus:running` showed the default list (10 rows). The
       address was rewritten to `/`, and nothing said a condition had been
       discarded.
     - A dashboard panel whose own query was `f=state:bogus:running` showed
       every task with no note.
     - In contrast, `/?f=nosuchattr:any:x` keeps its address and shows
       "Unknown condition: nosuchattr" (grain/task-1421).
   - Cause: `conditionFromQuery` returned `null` for any operator outside
     `OPERATORS`, so the condition never reached `unknownConditions`.
   - Fix (`08e038f4c`):
     - Keep any non-empty comparison when parsing.
     - `resolveConditions` leaves an unknown comparison out of the
       narrowing, and `unknownConditions` reports it with `badOp`. The chip
       and the panel note read "Unknown condition: state:bogus", and the
       chip's delete removes it.
     - Only comparisons outside the vocabulary are affected. A first
       attempt also rejected comparisons outside an attribute's own menu,
       but that broke "is any of Not set" on a custom text field, so it
       was narrowed.
   - After the fix: the address is kept, the chip is shown, and the
     list shows 12 rows, the same as the unknown-attribute case.
   - Regression coverage:
     - Unit tests in `taskFilters.test.js`, `paths.test.js` and
       `PanelBoard.test.jsx`, which all fail on the old parser.
     - E2e `6c` in `saved-boards-walkthrough.spec.js`, which fails on the
       old bundle and passes on the fixed one.
2. **The New board prompt called every board a dashboard.**
   - Reproduction: New board from `/?q=pagination` prefilled "Add a saved
     board (dashboard) to grain." above `kind: list`.
   - "Dashboard" is the name of the panels kind, so the filed task #14 gives
     its agent two answers to the kind question.
   - Fix (`131a336ce`): the first line now reads "Add a saved board to
     grain.", with a unit test for every non-panels kind.

### Observations (not changed)

- `f=state:before:2026-01-01` is a known comparison that the state
  attribute does not offer. It empties the list, but its chip ("state is
  before 2026-01-01") says what is applied, so it is not silent.
- The report's `runs` section is labelled "Attempts" in the vocabulary, on
  its move handle and in its empty line, but its body opens with a
  "Capacity" subheading. Both are accurate (it covers capacity and how
  attempts ended), but a screen-reader user and a sighted user hear two
  different names for it.
- The timeline range and lanes are one browser-wide setting, so a choice on
  one timeline board applies to every other. This is the documented design.
- No report section kind is prose. The vocabulary is all metrics plus the
  Backlog task list, so a "prose section" was not exercisable.

## Automated verification

- Vitest, whole suite: 158 files, 2747 tests passed. Of those,
  `taskFilters`/`paths`/`PanelBoard`/`NarrowingBar`/`boards` had 245 tests
  all passing.
- Playwright on chromium: 38 passed across
  `saved-boards-walkthrough.spec.js` (12, including the new `6c`),
  `boards.spec.js`, `board.spec.js` and `gantt.spec.js`.
- No screenshots, traces or other browser artifacts are committed.
