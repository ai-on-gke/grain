# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-05 (02:55–04:30 UTC)

Commit tested: `98f2a031d` (base, `candidate` tip, v1.8.0-rc.1). The fixes
from this run are on `grain/task-1983`:

- `746f9a920`: a Tree board is narrowed by its query and its own search,
  and a modified click on a tree's task id opens a new tab.
- `423329de0`: e2e `5b` in `saved-boards-walkthrough.spec.js` covering
  both.
- `75d3af644`: the New board / Edit board prompt names tree boards among
  the kinds that show every task when their query is empty.
- `2060181da`: `blockedBy` names a blocking task once, even when two links
  name it.

The steps that found them were run again on a demo rebuilt from
`2060181da`. Those results are recorded under "After the fix" in each
finding.

## What is new since 2026-10-04

The `Tree` board kind (`8fa57d578`, "draw the work tree on a task's page
and as a Tree board") landed on `candidate` with v1.8.0-rc.1. The demo
seeds a fourth board, `Tree` (`board-4`). This run gave it the most
attention, because no earlier walkthrough covered it.

## Environment

- Debian 12 sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6,
  Playwright 1.62.1 with Chromium headless shell (build 1234).
- The frontend was built with `npm ci && npm run build` into
  `pkg/ui/static`, and the binary after that with
  `go build -o bin/grain ./cmd/grain`.
- The server was `bin/grain demo -addr 127.0.0.1:8420 -open=false` over
  its own throwaway embedded store, with `HOME` and the working directory
  set to `/tmp/demo`. The store was thrown away and the server restarted
  after each rebuild with a fix. The restart script tracks the server by
  pidfile: `pkill -f "grain demo"` also matches the sandbox shell running
  it and kills that shell.
- Real Chromium was driven by Playwright scripts kept outside the
  repository (`/tmp/wt`, not committed). The viewport was 1440x900, or
  1440x1600 for a whole dashboard. There was one phone pass with
  Playwright's `iPhone 13` device (390x844, touch).
- Every page had listeners for console errors and warnings, `pageerror`,
  `requestfailed` and HTTP status ≥ 400.
- `npx playwright install chromium` needs
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the
  package cache proxy, as before.
- There was no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed tooling was used.
- Identity: the seeded team member `alice`, set through
  `localStorage["grain.username"]`, as the e2e fixture does.

## Stubs and boundaries

- **State-repository reconciler.** New board, Edit board, Add panel and
  Keep each file a task against `grain/config-default`. A board appears
  only once an agent's pull request to `tables/board*.json` merges, and
  the demo has no agent or GitHub to do that.
  - The asynchronous half was exercised through the UI (step 2).
  - The boards such a merge would produce were stood in for with
    `POST /api/boards`:
    - `WT Tree` (`board-5`, kind tree,
      `f=repo:any:acme/widgets&q=pagination`, folder `WT Folder`);
    - `WT List` (`board-6`, `q=pagination`, `WT Folder`);
    - `WT Kanban` (`board-10`, one own column, Waiting =
      proposed|queued);
    - `WT Dash` (`board-12`), with six panels: states, a list scoped to
      running, a 7d gantt, 30d metrics scoped to acme/widgets, a list
      matching nothing, and an inheriting full-width list;
    - `WT Report` (`board-15`), with six sections, one of them retitled
      "Sandbox use", scoped to acme/widgets.
  - The store was discarded at the end. The UI has no board delete.
- **Network fault on `/api/metrics`.** In step 6 only, Playwright's
  `page.route` refused the request (`connectionrefused`) to show the error
  rendering. No response body was invented.
- **GitHub.** Opening any task requests
  `GET /api/repos/<repo>/github-branches`, which returns 404 because the
  demo has no GitHub. That 404 and the console error it logs are expected.
  Apart from the deliberate metrics fault, they are the only console
  errors in every log in this run.
  - `GET /api/tasks` poll requests that Chromium reports as `ERR_ABORTED`
    were filtered out, as the 2026-10-03 report explains.

## Steps and outcomes

1. **Boards index and rail.**
   - The rail entry (a `role=button` list item, not a link) read
     "boards 4", and "boards 6" after the two folder boards were added.
   - `/boards` lists the loose boards in server order, then the
     `WT Folder` heading with WT Tree and WT List. There is no search or
     sort input, by design. The only control on a row is a "Move …"
     handle.
   - Clicking WT Tree opened `/boards/board-5`. Back returned to `/boards`
     and Forward came back with the same rows.
   - Deep links:
     - `/boards/board-1` through `board-4` each open their board cold.
     - `/boards/board-99` and `/boards/` fall back to `/boards`.
2. **Asynchronous creation.**
   - Create options → New board, from `/?q=pagination`, opened New task
     prefilled with:
     - `Add a board`, `kind: list` and `` query: `q=pagination` ``;
     - the list of kinds, now ending in `tree (each task nested under the
       task whose run filed it, …)`;
     - repo `grain/config-default`, queued, auto-merge on.
   - Create task filed task 13 (`queued`, `grain/config-default`,
     `autoMerge: true`, with the whole prompt). The board count stayed at
     6, and the page stayed on `/?q=pagination`. Task 13 opens at
     `/tasks/13`.
   - New board from WT Tree carried
     `kind: tree (…)` and `` query: `q=pagination&f=repo:any:acme/widgets` ``.
   - Edit board on WT Tree carried the board's saved fields (folder
     `WT Folder`, kind tree, its query) and "being read right now narrowed
     to `q=pagination&f=repo:any:acme/widgets`".
   - **The prompt's note on empty queries named a Kanban board, a timeline
     and a report as showing every task, but not a tree board, which does
     the same** (finding 3).
3. **Tree (seeded Tree, WT Tree).**
   - The seeded Tree nests 1, 3 and 8 under 12, and 11 under 8. The other
     roots are newest first: 12, 10, 9, 7, 6, 5, 4, 2. Each row has its
     state badge, and 8, 6 and 5 have their PR links.
   - Clicking task id 11 opened `/tasks/11`, and Back returned. Enter on a
     focused task id opened `/tasks/12`.
   - **`/boards/board-4?q=zzzznothing`, `?q=pagination` and
     `?f=state:any:running` each still drew all twelve tasks. WT Tree, saved
     as `repo is acme/widgets` with `q=pagination`, also drew all twelve.
     The board had no search or filter control at all** (finding 1).
   - **A Ctrl-click on a task id navigated the same tab to `/tasks/8`
     instead of opening a new tab** (finding 1).
   - After the fix:
     - `?q=pagination` draws 8 with 11 nested under it.
     - `?f=state:any:running` draws 8 and 3 as roots, because their parent
       12 is narrowed out.
     - `?q=zzzznothing` says "No tasks match this search and these
       filters."
     - The search field rewrites the URL, and reload keeps both the field
       and the rows.
     - Ctrl-click opens `/tasks/8` in a new page, and the board stays on
       `/boards/board-4`.
     - WT Tree draws only its own query's tasks.
4. **List and kanban (seeded Board, WT Kanban).**
   - Columns: Proposed[1] Queued[2,11,12,13] Running[3,8] Needs
     you[4,6,7] Queued for merge[5].
   - Clicking a card opened `/tasks/1`, and Escape returned to the board.
   - Keyboard pickup of "Move task 13" offered three "Move here" slots in
     Queued. The first gave `13,2,11,12`, with focus back on "Move task
     13".
   - A mouse drag of 11 onto 2 gave `13,11,2,12`.
   - Both orders survived reload.
   - Columns dialog:
     - "+ Add column" → Save added a fifth column, which survived reload.
     - Reset to default → Save restored the five default columns.
   - WT Kanban (own columns) drew Waiting[1,13,11,2,12], had no Columns
     button, and said "8 tasks in no column (Running, Awaiting reply,
     Awaiting submit, Awaiting ack, Queued for merge, Deferred, Closed)."
5. **Timeline (seeded Timeline).**
   - Last 7 days drew 12 bars, said "1 task outside this range.", and
     showed one `.gantt-now` marker and one Blocked chip.
   - Opening Range from the keyboard offered last 24 hours / 7 days /
     30 days / 90 days / everything. Lanes offered no lanes, by repo, by
     state and by author.
   - 24 hours by state drew 11 bars ("2 tasks outside this range.") in
     seven lanes: proposed 1, queued 4, running 2, awaiting reply 1,
     awaiting submit 1, awaiting ack 1, deferred 1.
   - Reload kept `/boards/board-2`, "Last 24 hours" and "By state". Both
     are browser settings, not URL ones, by design.
   - Clicking the first bar opened `/tasks/1`. Back returned with the range
     and lanes kept.
   - **Task 11's Blocked chip read "Blocked on: #8 (running), #8
     (running)"** (finding 4). The kanban card's chip had the same
     duplicate.
   - After the fix, both read "Blocked on: #8 (running)", and
     `GET /api/tasks/11` returns `blockedBy: ["8"]`.
6. **Dashboard (WT Dash).**
   - The panels were states 11, running only 2, week 11, widgets 30d 12
     filed, nothing matches 0, and inherit list 11.
     - The state counts link to `/?…&f=state:any:<state>`.
     - The Running only "Open" link goes to `/?f=state:any:running`, which
       is a list of 2 rows.
     - List rows link to `/tasks/N`.
   - Typing "pagination" in "Search this dashboard…" made the URL
     `?q=pagination&f=state:none:deferred|closed&f=kind:none:interactive`.
     - The inheriting panels (states, week, inherit list) dropped to 2 and
       offered Keep.
     - The panels with their own query (running only, widgets 30d,
       nothing matches) kept their own answers and their Open links.
     - Reload kept the URL and every count. Back from a panel's Open
       returned to the narrowed URL.
   - There were 3 Keep buttons and 1 Add panel button.
   - A keyboard pickup of panel 6 offered 5 slots. Moving it to the top
     persisted `orderKey -1048576`, and focus was on "Move panel 1,
     “Inherit list”" (the 2026-10-04 fix still holds). Reload kept the
     order.
   - With `/api/metrics` refused, the metrics panel said "The metrics
     report could not be loaded."
7. **Report (WT Report, seeded Report).**
   - WT Report opens at `?f=repo:any:acme/widgets`. Backlog says "Oldest
     queued: task 2, waiting 3h 7m.", and clicking it opened `/tasks/2`.
   - A keyboard pickup of section 6 (Merge queue) to the top persisted.
     Focus was on "Move section 1, “Merge queue”", and reload kept the
     order.
   - The seeded Report draws 11 sections.
8. **Delete and confirmation.** The UI exposes no board delete: `/boards`
   and a board page offer only Move, edit board and new board. There is no
   other destructive control in this area, so none was exercised.
9. **Phone (390x844, touch).**
   - `/boards`, the Tree board (narrowed and not), kanban, timeline and the
     seeded report all had `scrollWidth` equal to `clientWidth` (390).
   - Tapping a tree task id opened the task.

## Findings

### Fixed in this task

1. **A Tree board ignored its query and could not be narrowed, and its
   links swallowed modified clicks.**
   - Reproduction (demo): open `/boards/board-4?q=zzzznothing`. All twelve
     tasks are drawn, and no search or filter control is shown.
     - A tree board saved with `f=repo:any:acme/widgets` draws every repo.
     - Ctrl/Cmd-click on a task id navigates in place.
   - Cause:
     - App hands board panes the whole team-scoped backlog, and each pane
       applies the shared narrowing itself, as `GanttChart` does.
     - The tree pane passed the rows straight to `forestFromTasks` and
       rendered no `NarrowingBar`.
     - `TaskTree`'s link always called `preventDefault`, where `RowLink`
       leaves modified clicks to the browser.
   - Fix (`746f9a920`):
     - New `TaskTreeBoard.jsx` narrows with `narrowTasks`, wears the same
       `NarrowingBar`, says when nothing matches, and is a `<main>`
       landmark like the list and kanban panes.
     - `TaskTree` lets Ctrl/Cmd/Shift/Alt and non-primary clicks through.
   - Regression coverage: `TaskTreeBoard.test.jsx` (4 cases, all failing
     against the old pane), and e2e `5b`.
2. **Accessibility note covered by the same change.** The old tree pane
   was a plain `div`. The new one is a `<main>`, so the Tree board has a
   main landmark.
   - The boards index, timeline, dashboard and report still have no
     `<main>` (only the list, kanban and a few other panes do). That is
     left as an observation: it is consistent across those panes and not
     specific to this run.
3. **The board prompt's empty-query note left out tree boards.**
   - `boardNarrowing` gives a tree board `NO_NARROWING`, so an empty query
     draws every task, closed ones included.
   - `QUERY_NOTE` said only "a Kanban board, a timeline or a report shows
     every task".
   - Fix (`75d3af644`): the note now names tree boards. A `boards.test.js`
     case pins the wording and the narrowing, and the existing per-kind
     prompt checks now include `tree`.
4. **`blockedBy` listed a task twice.**
   - Reproduction (demo): `GET /api/tasks/11` returns
     `blockedBy: ["8","8"]`. The timeline's and the kanban card's chips
     read "Blocked on: #8 (running), #8 (running)", and the MCP task
     summary would say "blocked on 8, 8".
   - Cause:
     - A follow-up proposed with its parent in `depends_on` carries both a
       `child-of` and a `depends-on` link to that parent. Both kinds
       `Blocks()`.
     - `taskFrom` (`pkg/ui/tasks.go`) appended one entry per link.
   - Fix (`2060181da`): each blocking task is named once.
   - Regression coverage: `TestBlockedByNamesATaskOnceWhateverLinksItTwice`
     in `pkg/ui/client_test.go`, which fails without the change.

### Observations (not changed)

- A refused `POST /api/boards` still uses up a board id. This run's
  refused shapes were:
  - a `tree` panel ("kind must be list, gantt, states or metrics");
  - a `capacity` section;
  - a kanban column naming `merge_queued` rather than `completed`.

  The ids they used were `board-7`–`9`, `11`, `13` and `14`. This is
  harmless and unchanged since 2026-10-03.
- A dashboard cannot hold a tree panel. That is consistent with the
  panel kinds the API documents, so it is not a defect.
- With no query, a Tree board draws done, deferred and closed tasks too,
  as the Kanban board and timeline do (`boardNarrowing`). This matches the
  task page's "n of m done" tree, and is now said in the board prompt.
- The rail's entries are `role=button` items rather than links, so they
  cannot be opened in a new tab. This is unchanged and outside this area's
  scope.
- No report section kind is prose, so a prose section could not be
  exercised.

## Automated verification

- Vitest, whole suite: 176 files, 2980 tests passed.
- Go: `go test ./pkg/ui -run 'TestBlockedByNamesATaskOnce|TestCreateTaskCarriesDependsOn'`
  passed. The new test fails on the base.
- Playwright on chromium, against the fixed build:
  `saved-boards-walkthrough.spec.js` (including the new `5b`),
  `boards.spec.js`, `board.spec.js` and `gantt.spec.js`: 42 passed.
- `5b` first asserted that a Ctrl-click opened a new page. It did in this
  sandbox, but grain CI's `ui e2e` Chromium opened none (run
  `e4dbef7ba457fc1e`). The case now asserts what the app controls: that it
  leaves the modified click unprevented and stays on the board.
- After the merge queue merged `candidate` (`13f34523d`) into the branch,
  grain CI's `go` job failed once at `5e237371e`. The failing test was
  `TestAWarmThatWorkedSaysSoAndSucceeds`
  (`tests/deploy/warm_npm_cache_test.go:91`, "the warm step said nothing
  about what it did"), which nothing on this branch touches.
  - It passed three times locally.
  - An uncached rerun of the same commit (`d3ea139353e89dfa`) passed every
    job.
  - `candidate` passes `go` at `13f34523d`.

  It is recorded here as a flake.
- No screenshots, traces or other browser artifacts are committed.
