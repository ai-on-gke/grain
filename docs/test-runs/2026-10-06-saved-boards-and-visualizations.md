# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-06 (19:20–20:10 UTC)

Commit tested: `8a2d35c78` (base, `candidate` tip,
`v1.8.0-rc.1.0.20261006184956-8a2d35c784da`). The fixes from this run are
on `grain/task-2233`:

- `fix(ui): stop warning that a team's config repo is off the target list`
  (`ui/src/state.js`, `repoTargetable`), with a `state.test.js` case.
- `fix(ui): head a report section's board title at h3, not h6`
  (`ReportBoard.jsx`), with a `ReportBoard.test.jsx` case.
- `test(e2e): a board request's task page does not call its repo off-list`
  (case 2 of `saved-boards-walkthrough.spec.js`).

The steps that found these were run again on a demo rebuilt from the fixed
tree. Those results are recorded under each finding.

The previous walkthrough of this area is
`2026-10-05-saved-boards-and-visualizations.md`. Its fixes were re-checked
on this base and still hold:

- A Tree board narrows by its query and its own search.
- `blockedBy` names each blocking task once. The timeline chip reads
  "Blocked on: #8 (running)".
- A moved panel or section keeps focus on its handle.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6, Playwright
  with Chromium headless shell (build 1234).
- The frontend was built with `npm ci && npm run build` into
  `pkg/ui/static`, and the binary after that with
  `go build -o /tmp/grain ./cmd/grain`.
- The server was `grain demo -addr 127.0.0.1:8420 -open=false` with `HOME`
  and the working directory set to a throwaway `/tmp/demo`. The store was
  discarded on every restart. The restart script tracks the server by
  pidfile.
- Real Chromium was driven by Playwright scripts kept outside the
  repository (`/tmp/wt`, not committed).
  - The viewport was 1440x900, or 1440x1600 for a dashboard. There was one
    phone pass with Playwright's `iPhone 13` device (390x844, touch).
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
  Keep each file a task against `grain/config-default`. A board appears only
  once an agent's pull request to `tables/board*.json` merges, and the demo
  has no agent or GitHub to do that.
  - The asynchronous half was exercised through the UI (step 2, step 6).
  - The boards such a merge would produce were stood in for with
    `POST /api/boards`:
    - `WT List` (`board-5`, `q=pagination`, folder `WT Folder`);
    - `WT Tree` (`board-6`, `f=repo:any:acme/widgets`, `WT Folder`);
    - `WT Kanban` (`board-7`, own columns Waiting = proposed|queued and
      Moving = running);
    - `WT Dash` (`board-8`) with six panels:
      - states;
      - a list scoped to running;
      - a 7d gantt;
      - metrics scoped to acme/widgets;
      - a list matching nothing;
      - an inheriting full-width list;
    - `WT Report` (`board-9`, scoped to acme/widgets) with six sections,
      one retitled "Sandbox use";
    - `WT Gantt` (`board-10`, scoped to acme/widgets).
  - The UI has no board delete, so the store was simply discarded.
- **Injected network faults** (step 6 only). Playwright's `page.route`
  was used to:
  - delay `/api/metrics` by 2.5s, to observe loading;
  - refuse `/api/metrics` (`connectionrefused`), to observe errors;
  - refuse `/api/boards`, for one observation.

  No response body was invented.
- **GitHub.** Opening a task requests
  `GET /api/repos/<repo>/github-branches`, which returns 404 because the
  demo has no GitHub. That 404 and its console error are expected.
  - Apart from the deliberate faults, they are the only console errors in
    every log in this run. There were no `pageerror`s.
  - `GET /api/tasks` poll requests that Chromium reports as `ERR_ABORTED`
    were filtered out.

## Steps and outcomes

1. **Boards index and rail.**
   - The rail read "Boards 4" (seeded Board, Timeline, Report, Tree), and
     "Boards 10" once the stand-ins were added.
   - `/boards` lists the loose boards, then the `WT Folder` heading with
     WT List and WT Tree. There is no search or sort input, by design.
   - Keyboard pickup of "Move WT Gantt" offered ten "Move here" slots,
     including "outside every folder" and "at the end of WT Folder". The
     first slot moved it to the top and focus stayed on "Move WT Gantt".
     Reload kept the order.
   - Clicking WT Tree opened `/boards/board-6?f=repo:any:acme/widgets`.
     Back returned to `/boards`, and Forward came back.
   - Deep links:
     - `/boards/board-1` through `board-4` each open their board cold.
     - `/boards/board-99` and `/boards/` fall back to `/boards`.
2. **Asynchronous creation.**
   - Create options → New board, from `/?q=pagination&f=state:any:running`,
     opened New task prefilled with:
     - `Add a board`, `kind: list` and
       `` query: `q=pagination&f=state:any:running` ``;
     - the six kinds;
     - the `tables/board.json` contract.
   - Create task filed task 13 (`queued`, `grain/config-default`,
     `autoMerge: true`). The page returned to the narrowed list, and
     `/boards` still counted 4.
   - **Task 13's page warned "grain/config-default is not on this
     deployment's target repo list, so nothing here can clone it…"**,
     though the server admits that repo and did not park the task
     (finding 1).
   - Edit board on WT Report carried its name, kind, query and its
     sections in drawn order (`merges, throughput, …, sandboxes "Sandbox
     use"` after step 7's reorder).
3. **List (WT List).**
   - Opens at `/boards/board-5?q=pagination` with 3 rows (8, 11, 13).
   - Clicking a row opened `/tasks/8`, and Escape returned to the board
     URL.
4. **Kanban (seeded Board, WT Kanban).**
   - Columns: Proposed[1] Queued[2,11,12,13] Running[3,8] Needs
     you[4,6,7] Queued for merge[5].
   - Keyboard pickup of "Move task 13" offered three slots, all in Queued.
     The first gave `13,2,11,12`, with focus back on "Move task 13".
   - A mouse drag of 11 onto 2 gave `13,11,2,12`. Reload kept it.
   - A mouse drag of task 1 into Queued changed nothing, which is
     documented as intended (`TaskBoard.jsx`, "Why a card cannot be
     dragged into another column").
   - Columns dialog ("Board columns"):
     - Retitling Running to "In flight", moving it left, removing Queued
       for merge and adding "Parked" = Deferred, then Save, gave
       `Proposed[1] In flight[3,8] Queued[13,11,2,12] Needs you[4,6,7]
       Parked[9]`.
       - "Off the board" updated live as states were moved.
       - Reload kept the result.
     - Cancel after Remove Parked discarded the edit.
     - Giving Running to column 1 took it out of column 3. A state lives
       in one column.
     - Enter on Columns focused the dialog. Escape closed it and focus
       went back to Columns.
     - Reset to default → Save restored the five columns.
   - WT Kanban drew Waiting[1,13,11,2,12] Moving[3,8], had no Columns
     button, and said "6 tasks in no column (Awaiting reply, Awaiting
     submit, Awaiting ack, Queued for merge, Deferred, Closed)."
   - Phone: tapping "Move task 12" then the first slot moved 12 to the top
     of Queued.
5. **Timeline (seeded Timeline, WT Gantt).**
   - Last 7 days drew 12 bars and said "1 task outside this range."
   - Range offers 24 hours, 7, 30 and 90 days, and everything. Lanes
     offers no lanes, by repo, by state and by author.
   - 24 hours by state drew 11 bars ("2 tasks outside this range.") in
     seven lanes: Proposed 1, Queued 4, Running 2, Awaiting reply 1,
     Awaiting submit 1, Awaiting ack 1, Deferred 1.
   - The `gantt-now` marker sits at the right edge of the track.
   - Task 11 carries a Blocked badge and "Blocked on: #8 (running)".
   - Typing "pagination" in the search wrote `?q=pagination` and left 3
     rows. Reload kept the URL, the search, "Last 24 hours" and "By
     state". Range and lanes are browser settings, by design.
   - Clicking a bar opened `/tasks/13`. Back returned with the range and
     search kept. Enter on a focused row link opened the task.
   - WT Gantt opened at `?f=repo:any:acme/widgets` with 10 rows.
6. **Dashboard (WT Dash).**
   - The panels read:

     | Panel | Content |
     | --- | --- |
     | All states | 11 (1/4/2/3/1) |
     | Running only | 2 |
     | This week | 11, "Showing 10 of 11" |
     | Widgets 30d | 11 filed / 3 completed |
     | Nothing matches | 0, "Nothing here right now." |
     | Inherit list | 11, "Showing 8 of 11" |

   - Panel links:
     - "Open" on each list or states panel links to the task list with
       the panel's query.
     - The metrics panel links to `/metrics?f=repo:any:acme/widgets`.
     - The state counts link to `…&f=state:any:<state>`.
     - List rows link to `/tasks/N`.
   - Its metrics request is `repo=acme/widgets&window=7d`. The "30d" was
     only the title this run gave it, and the panel's default window is
     the week.
   - Typing "pagination" in "Search this dashboard…" made the URL
     `?q=pagination&f=state:none:deferred|closed&f=kind:none:interactive`.
     - The inheriting panels dropped to 3 and offered three Keep buttons.
     - The panels with their own query kept their answers.
     - Reload kept all of it.
     - Running only's Open went to `/?f=state:any:running` (2 rows). Back
       returned to the narrowed URL.
     - Inherit list's Open carried the narrowing (3 rows), and the Queued
       count's link carried it too (2 rows).
   - Reorder:
     - Keyboard pickup of panel 6 offered 5 slots. The first moved it to
       the top, with focus on "Move panel 1, “Inherit list”".
     - A mouse drag of "Nothing matches" onto panel 2 put it second.
     - Reload kept both moves.
   - Keep on "All states" opened New task ("Narrow the panel …") naming
     panel 3 and the narrowed query. Create filed task 14. Add panel filed
     task 15. Both are `queued`, `grain/config-default`, auto-merge, and
     the dashboard stayed as it was.
   - A deep link `?q=Replace` (no default state condition) drew the
     deferred task 9, and Add panel's prompt carried `q=Replace`, which is
     faithful to the screen. Typing the same search adds the default
     condition, and the prompt carried that.
   - Loading and errors:
     - With `/api/metrics` delayed, the metrics panel said "Loading…".
     - With it refused, the panel said "The metrics report could not be
       loaded." and WT Report said "The report could not be loaded (Failed
       to fetch). Refresh to try again."
7. **Report (WT Report, seeded Report).**
   - WT Report opens at `?f=repo:any:acme/widgets` and requests
     `repo=acme/widgets&window=7d`.
   - Backlog names "task 2", and clicking it opened `/tasks/2`.
   - Keyboard pickup of section 6 (Merge queue) to the top persisted.
     Focus was on "Move section 1, “Merge queue”", and reload kept the
     order.
   - Sections with nothing to say read "Sandboxes: nothing to report over
     this window." and "Merge queue: …".
   - Window offers 24 hours and 7, 30 and 90 days. Picking 30 days
     re-requested with `window=30d`.
   - **The window is not in the URL, and reload went back to 7 days**
     (finding 3, follow-up).
   - **The board-given title "Sandbox use" was an `h6` among `h3`
     sections** (finding 2).
   - The seeded Report draws 11 sections, headed `h2` Report, then `h3`.
8. **Tree (seeded Tree, WT Tree).**
   - WT Tree drew 12 widgets tasks. `/boards/board-4?q=pagination` drew
     13, 8 and 11.
9. **Delete and confirmation.**
   - The UI exposes no board, panel or section delete. `/boards` and every
     board page offer only Move, Edit board and New board.
   - The only removal in this area is the Columns dialog's "Remove
     <column>". It edits a draft that needs Save and can be cancelled, and
     it was exercised in step 4.
   - Task 13 (throwaway, filed here) was closed from its page. Close acts
     at once without a confirmation. It is a task action, not a board one,
     and is noted only.
10. **Phone (390x844, touch).** `/boards`, Board, Timeline, WT Dash,
    WT Report and Tree all had `scrollWidth` equal to `clientWidth` (390).

## Findings

### Fixed in this task

1. **A board request's task said its repo was off the target list.**
   - Reproduction (demo):
     1. Use New board and Create task.
     2. Open the new task. Its Repos section shows "grain/config-default is
        not on this deployment's target repo list, so nothing here can
        clone it…".
     - The same happens for Edit board, Add panel and Keep tasks.
   - Cause:
     - The server admits every config repository (`model.IsConfigRepo`:
       `grain/config` and any `grain/config-<team>`) and never parks such a
       task.
     - The UI's `repoTargetable` only knew `config.configRepo`, which
       `/api/config` does not send, and the asked-about team's
       `configRepo`. The task page passes no team.
   - Fix: `repoTargetable` applies the server's rule, and also admits any
     team's declared `configRepo`.
   - Regression coverage:
     - `state.test.js` "admits a team's config repository without a team
       to ask about" fails on the base.
     - e2e `saved-boards-walkthrough.spec.js` case 2 asserts the warning
       is absent on the filed task. Run against a frontend built with the
       old `state.js`, it fails with `Received: 1`.
   - After the fix, the repo field reads `grain/config-default` and the
     page has no off-list alert.
2. **A report section's board-given title was an `h6`.**
   - Reproduction: a report board whose section has a `title` (WT Report's
     "Sandbox use"). The heading outline reads `h2` WT Report, `h3`
     Throughput, …, `h6` Sandbox use.
   - Cause: `ReportSection` drew the title as MUI `subtitle2`, whose
     element is `h6`. The sections' own names became `h3` in `922b9df3a`.
   - Fix: the title is an `h3`, styled as before.
   - Regression coverage: `ReportBoard.test.jsx` "heads a section the board
     titled at the level of the others", which fails on the base.
   - After the fix, the outline reads `H2:WT Report | H3:Sandbox use |
     H3:Latency | H3:Throughput`.

### Proposed as follow-up

3. **A report board's window does not survive reload or ride in the
   URL.**
   - The Metrics pane carries its picked window as `?window=`
     (`paths.js`, grain/task-1584). A report board holds the pick in
     component state (`ReportBoard.jsx`, "A board leaves both off").
   - So reload, a shared link or Back from a task resets a board to 7
     days.
   - This touches App's routing and `paths.js`, so it is filed as a
     separate task against `candidate`.

### Observations (not changed)

- Report section kind `runs` is labelled "Attempts" (`report.js`: handle,
  empty note, Edit board prompt) but its body is headed "Capacity"
  (`MetricsSections.jsx`). So the handle "Move section 4, “Attempts”" sits
  over a heading reading Capacity. Both names come from `63e716f3f`.
- Add panel's prompt always says the query includes "this deployment's own
  default condition on state". For a deep link like `?q=Replace` there is
  none on screen. The query it hands over is still exactly what is
  displayed, so only the sentence is off.
- If `/api/boards` fails on first load, `/boards` reads "Boards 0" with no
  error. A failed later poll throws and leaves the list and the open board
  alone. A single-endpoint failure is artificial; a real outage gets the
  app's unreachable banner.
- Boards that tie on `orderKey` are listed by id as text, so a new
  `board-10` sat between `board-1` and `board-2`. The board prompt tells
  agents to use word ids for this reason.
- The dashboard, timeline, report and boards index still have no `<main>`
  landmark (unchanged since 2026-10-05).

## Automated verification

- Vitest, whole suite: 184 files, 3064 tests passed.
- Playwright on chromium, against the fixed build:
  `saved-boards-walkthrough.spec.js`, `boards.spec.js`, `board.spec.js`
  and `gantt.spec.js`: 42 passed.
- No screenshots, traces or other browser artifacts are committed.
