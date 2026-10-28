# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-07 (03:00–04:00 UTC)

Commit tested: `61604b729` (base, `candidate` tip,
`v1.8.0-rc.1-318-g61604b729`). The fix from this run is on
`grain/task-2413`:

- `fix(ui): name a report's capacity section's handle after its heading`
  (`ui/src/report.js`), with a `ReportBoard.test.jsx` case and case 6b2 of
  `saved-boards-walkthrough.spec.js`.

The step that found it was run again on a demo rebuilt from the fixed tree.
That result is recorded under the finding.

The previous walkthrough of this area is
`2026-10-06-saved-boards-and-visualizations.md`. Its two fixes were
re-checked on this base and still hold:

- The board request's task page (task 13, `grain/config-default`) shows no
  "not on this deployment's target repo list" alert.
- A report section with a board-given title is headed at `h3`, like the
  others ("sandbox use").

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6,
  Playwright 1.62.1 with Chromium headless shell (build 1234).
- The frontend was built with `npm ci && npm run build` into
  `pkg/ui/static`, and the binary after that with
  `go build -o /tmp/grain ./cmd/grain`.
- The server was `grain demo -addr 127.0.0.1:8420 -open=false` with `HOME`
  and the working directory set to a throwaway `/tmp/demo`. The store was
  discarded when the server was rebuilt for the fix.
- Real Chromium was driven by Playwright scripts kept outside the
  repository (`/tmp/wt`, not committed).
  - The viewport was 1440x900, or 1440x1600–1800 for the dashboard and
    report. There was one phone pass with Playwright's `iPhone 13` device
    (390x844, touch).
  - Every page had listeners for console errors and warnings, `pageerror`,
    `requestfailed` and HTTP status ≥ 400.
- `npx playwright install chromium` needs
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` behind the
  package cache proxy.
- There was no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed tooling was used.
- Identity: the seeded team member `alice`, set through
  `localStorage["grain.username"]`, as the e2e fixture does.

## Stubs and boundaries

- **State-repository reconciler.** New board, Edit board, Add panel and
  Keep each file a task against `grain/config-default`. A board appears
  only once an agent's pull request to `tables/board*.json` merges, and the
  demo has no agent or GitHub to do that.
  - The asynchronous half was exercised through the UI (step 2, step 6).
  - The boards such a merge would produce were stood in for with
    `POST /api/boards`:
    - `WT List` (`board-5`, `q=pagination`, folder `WT Folder`);
    - `WT Tree` (`board-6`, `f=repo:any:acme/widgets`, `WT Folder`);
    - `WT Kanban` (`board-7`, own columns Waiting = proposed|queued and
      Moving = running);
    - `WT Dash` (`board-8`) with six panels:
      - states "Counts";
      - a list "Running only" scoped to `f=state:any:running`;
      - a 7d gantt "Week";
      - metrics "Widgets" scoped to acme/widgets;
      - a list "Nothing" scoped to `q=zzzznomatch`;
      - an inheriting list "Inherits";
    - `WT Report` (`board-9`, scoped to acme/widgets) with sections
      throughput, latency, runs, backlog, merges and sandboxes titled
      "Sandbox use";
    - `WT Gantt` (`board-10`, scoped to acme/widgets).
  - The UI has no board, panel or section delete (no "Delete"/"Remove"
    control in `ui/src` for any of them), so no destructive confirmation was
    reachable. The store was discarded instead.
- **Injected network faults** (step 6 only). Playwright's `page.route` was
  used to:
  - delay `/api/metrics` by 2.5s, to observe loading;
  - refuse `/api/metrics` (`connectionrefused`), to observe errors;
  - answer `/api/tasks` with a 500, to observe the error state on `/`
    and on each board kind.

  No success body was invented.
- **GitHub.** Opening a task requests
  `GET /api/repos/<repo>/github-branches`, which returns 404 because the
  demo has no GitHub. That 404 and its console error are expected.
  - Apart from the deliberate faults, they are the only console errors in
    every log in this run. There were no `pageerror`s.
  - `GET /api/tasks` and `/api/events` requests that Chromium reports as
    `ERR_ABORTED` were filtered out.

## Steps and outcomes

1. **Boards index and rail.**
   - The rail read "Boards 4" on a fresh store, and "Boards 10" once the
     stand-ins were added. `/boards` counted 10.
   - The index lists the loose boards (Board, WT Gantt, Timeline, Report,
     Tree, WT Kanban, WT Dash, WT Report), then the `WT Folder` heading
     with WT List and WT Tree. There is no search or sort input, by design.
   - Tab order from the rail reaches "new board" and then, per board,
     "Move <name>" followed by the board's link.
   - Clicking WT Tree opened `/boards/board-6?f=repo:any:acme/widgets`.
     Back returned to `/boards` with focus on the WT Tree link. Forward and
     reload kept the board.
   - Deep links:
     - `/boards/board-1` through `board-4` and `board-8` each open cold.
     - `/boards/board-99` and `/boards/` fall back to `/boards`.
     - `/boards/board-5?q=zzz` keeps the search and says "No tasks match
       your search."
2. **Asynchronous creation.**
   - Create options offers New task, New session, New team and New board.
   - New board, from `/?q=pagination&f=state:any:running`, opened New task
     prefilled with `Add a board`, `kind: list`,
     `` query: `q=pagination&f=state:any:running` ``, the six kinds and the
     query contract. The repo field read `grain/config-default`, with no
     alert.
   - Create task (retitled "WT2413 add a board") filed task 13: `queued`,
     `grain/config-default`, `autoMerge: true`, team `default`. The page
     returned to the narrowed list. `/boards` still counted 10.
   - Task 13's page has no off-list alert.
   - Edit board on WT Report carried its name, kind, query and its
     sections in drawn order (`1. throughput … 6. sandboxes "Sandbox
     use"`). Back left it without filing anything.
3. **List (WT List).**
   - Opens at `/boards/board-5?q=pagination` with rows 11 and 8. The search
     is over title and id (`taskFilters.js`), so task 13 does not match.
   - Clicking a row opened `/tasks/11`. Escape returned to the board URL
     with focus on the row's link. `j` then Enter opened a task too.
4. **Kanban (seeded Board, WT Kanban).**
   - Columns: Proposed[1] Queued[2,11,12,13] Running[3,8] Needs
     you[4,6,7] Queued for merge[5].
   - Clicking a card opened `/tasks/1`, and Escape returned. Enter on a
     card's title link also opened it, and Back returned focus to that
     link.
   - Keyboard pickup of "Move task 12" offered three slots, all in Queued.
     The first gave `12,2,11,13`, with focus back on "Move task 12". Escape
     during a pickup cancelled it and left focus on the handle.
   - A mouse drag of 11's handle onto 12 gave `11,12,2,13`. Reload kept it.
   - Columns dialog (opened with Enter on Columns):
     - Retitling column 3 to "In flight", moving it left, removing Queued
       for merge and adding "Parked" = Deferred, then Save, gave
       `Proposed[1] In flight[3,8] Queued[11,12,2,13] Needs you[4,6,7]
       Parked[9]`. "Off the board" updated live to "Queued for merge,
       Closed." Reload kept the result.
     - Cancel after Remove Parked discarded the edit.
     - Reset to default → Save restored the five columns.
     - Saving with column 1's title emptied fell back to the states' own
       label ("Proposed"). Removing every column and saving fell back to
       the default columns. Both are documented in `board.js`
       (`normalizeColumns`).
   - WT Kanban drew Waiting[11,1,12,2,13] Moving[3,8], had no Columns
     button, and said "6 tasks in no column (Awaiting reply, Awaiting
     submit, Awaiting ack, Queued for merge, Deferred, Closed)."
   - Phone: tapping "Move task 13" then the first slot moved 13 to the top
     of Queued.
5. **Timeline (seeded Timeline, WT Gantt).**
   - The default of last 7 days drew 12 bars with no lanes, and said "1
     task outside this range." Ticks ran Oct 1–Oct 7.
   - Range offers 24 hours, 7, 30 and 90 days, and everything. Lanes
     offers no lanes, by repo, by state and by author.
   - 24 hours by state drew Proposed 1, Queued 4, Running 2, Awaiting
     reply 1, Awaiting submit 1, Awaiting ack 1 and Deferred 1, and said "2
     tasks outside this range."
   - Reload kept the range and lanes. They are kept per browser, as the
     New board prompt says, and the URL stays `/boards/board-2`.
   - The `gantt-now` marker sits at 100% of the track.
   - Task 11 carries a Blocked badge, and its row title reads "Blocked on:
     #8 (running)". Bars have labels such as "Queued, blocked: waiting for
     44m, never dispatched".
   - Enter on a row's title link opened `/tasks/1`. Clicking a bar opened
     `/tasks/11`, and Escape returned to the board with the same lanes.
   - Search "pagination" rode in the URL (`?q=pagination`) and survived
     reload: Queued 1, Running 1. Search "zzzz" said "No tasks match this
     search and these filters in this range."
   - WT Gantt (acme/widgets) shares the browser's range and lanes, and drops
     task 13 (grain/config-default). Its 7d, 30d, 90d and everything ranges
     and its author and repo lanes all drew.
6. **Dashboard (WT Dash).**
   - The six panels drew. Counts read 11 open, split 1/4/2/3/1. Each state
     links to the task list with the board's question plus that state.
   - Whole-board search "pagination" put
     `?q=pagination&f=state:none:deferred|closed&f=kind:none:interactive` in
     the URL.
     - The inheriting panels (Counts, Week, Inherits) dropped to 2 and
       gained "Keep" buttons.
     - The panels with their own queries (Running only, Widgets, Nothing)
       were unchanged and offered no Keep.
     - Reload kept all of this.
   - Add panel opened New task titled `Add a panel to the dashboard "WT
     Dash"`. It listed the six panels with kind, width, range, scoping and
     `order_key`, and handed over the on-screen query.
   - Keyboard pickup of "Move panel 5, “Nothing”" offered five slots. The
     first moved it to the top, with focus on "Move panel 1, “Nothing”".
     Reload kept it.
   - A mouse drag on the header grip moved it after Counts.
     - The server's board row then read orderKeys Counts 1, Nothing 1.5,
       Running only 2, Week 3, Widgets 4, Inherits 6.
     - A fresh browser context drew the same order, so panel order is
       stored server-side, not in localStorage.
   - Links:
     - Running only's Open went to `/?f=state:any:running`, with 2 rows.
     - Widgets' Open opened the Metrics pane at
       `/metrics?f=repo:any:acme/widgets&f=created:within:7`.
     - Nothing's Open went to `/?q=zzzznomatch`.
     - Each list row links to `/tasks/<id>`.
   - Empty state: "Nothing" reads "Nothing here right now."
   - Loading: with `/api/metrics` delayed, Widgets read "Loading…" and then
     "11 filed / 3 completed". Its loading state has no `progressbar` role
     (observation).
   - Error: with `/api/metrics` refused, Widgets read "The metrics report
     could not be loaded." The other panels were unaffected.
   - With `/api/tasks` answering 500, `/` and every board kind showed the
     app's "500 Internal Server Error" alert above their empty bodies.
   - Phone: the dashboard, timeline, report and index have no horizontal
     overflow at 390px.
7. **Report (WT Report, seeded Report).**
   - WT Report asks `/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24`
     and says "Measured over the tasks in acme/widgets." Throughput read 11
     filed and 3 completed.
   - **The "runs" section's handle read "Move section 3, “Attempts”" over a
     heading reading "Capacity"** (finding 1).
   - Keyboard pickup of "Move section 6" offered five slots. The first put
     "Sandbox use" first, with focus on its handle. Reload kept it.
   - Clear dropped the repo scope. The request lost `repo=`, and throughput
     read 12 filed, task 13 included.
   - A search "pagination" rode in the URL. The report said "the rest of
     this question is not in force: the search for "pagination"" and did
     not re-ask the daemon.
   - A cold `/boards/board-9` restored the board's own scope.
   - "Oldest queued: task 2" is a link-styled button. It opened `/tasks/2`,
     and Escape returned to the scoped board URL.
   - The window picker (24h, 7d, 30d, 90d) re-asked with `window=24h`.
     **Reload put it back to 7 days and the URL never carried it**
     (finding 2).
   - The seeded Report names 11 sections. The four with data have `h3`
     headings, and the other seven each print "<label>: nothing to report
     over this window."

## Findings

### Fixed here

1. **A report's capacity section was named "Attempts" on its handle.**
   - Reproduction: a report board with a `runs` section (WT Report, or the
     seeded Report). The handle reads "Move section 3, “Attempts”", but the
     heading outline reads `h3` Throughput, Latency, Capacity, Backlog, so
     the handle names a section the page does not have. The same label
     printed "Attempts: nothing to report over this window." for an empty
     one.
   - Cause: `REPORT_SECTIONS` in `report.js` labels the kind "Attempts".
     Its body is `CapacitySection`, which it shares with the Metrics pane
     and which heads itself "Capacity". This was the 2026-10-06 report's
     first observation.
   - Fix: the label is "Capacity", matching the heading. The kind id
     (`runs`) and the New board summary line are unchanged.
   - Regression coverage:
     - `ReportBoard.test.jsx` "names a section's handle after the heading
       drawn under it" fails on the base with `Unable to find … "Move
       section 2, “Capacity”"`.
     - e2e `saved-boards-walkthrough.spec.js` case 6b2 asserts the heading,
       the handle, and that no handle says Attempts.
   - After the fix, on a demo rebuilt from the fixed tree, the handles
     read Throughput, Latency, Capacity, Backlog, Merge queue, Sandbox use.

### Re-confirmed, already proposed

2. **A report board's window does not survive reload or ride in the URL.**
   - The 2026-10-06 walkthrough (run on grain/task-2233) proposed this as
     a follow-up. It still reproduces on this base:
     - pick "Last 24 hours" on `/boards/board-9`;
     - the request carries `window=24h`;
     - the URL stays `/boards/board-9?f=repo:any:acme/widgets`;
     - reload shows "Last 7 days".
   - It was not refiled, to avoid a duplicate.

### Observations (not changed)

- A report section that has nothing to say prints a plain line with no
  heading (`ReportBoard.jsx`, grain/task-1356). On the seeded Report, 7 of
  the 11 handles therefore name a section that has no heading in the
  outline.
- The dashboard's search input has only a placeholder ("Search this
  dashboard…") and no `aria-label` or `<label>`.
- A metrics panel's "Loading…" has no `progressbar` or `status` role.
- Boards that tie on `orderKey` are listed by id as text, so `board-10` (WT
  Gantt) sits between `board-1` and `board-2`. This is unchanged and
  documented.
- The Boards index has no `<main>` landmark. The Tree board renders
  `<main class="main-column">`.
- A Week (gantt) panel's Open link goes to the task list, not to a
  timeline. That is consistent with the other inheriting panels.

## Automated verification

- Vitest: `ReportBoard.test.jsx`, `ReportBoard.sections.test.jsx` and
  `report.test.js`: 83 passed.
- Playwright on chromium, against the fixed build:
  `saved-boards-walkthrough.spec.js`: 16 passed.
- No screenshots, traces or other browser artifacts are committed.
