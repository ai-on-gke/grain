# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-03 (07:06–07:35 UTC)

Commit tested: `11606c9fc` (base, `candidate` tip). The fixes from this run
are on `grain/task-1626`: `b8218729c` (report board load failure) and
`4c8d509a9` (board prompts, with e2e coverage). The steps that found them
were run again on a demo rebuilt from `4c8d509a9`, and are recorded under
"After the fix".

## Environment

- Debian 12.15 sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6,
  Playwright 1.62.1 with Chromium headless shell 151.
- The frontend was built with `npm ci && npm run build` into
  `pkg/ui/static`, and the binary with `go build -o bin/grain ./cmd/grain`.
  - Build them one after the other. Building the binary at the same time as
    the frontend embeds the `placeholder.html` page, and the demo then
    serves a blank app. The first start of this run hit that.
- The binary was served by `bin/grain demo -addr 127.0.0.1:8420 -open=false`
  over its own throwaway embedded store, with `HOME` and the working
  directory set to `/tmp/demo`.
- Real Chromium was driven by Playwright scripts kept outside the
  repository (`/tmp/wt`, not committed). The viewport was 1440x900, with
  1100–2400px tall viewports where a dashboard or report had to be on
  screen whole, and one phone pass at 390x844 with `hasTouch`/`isMobile`.
- Every page had listeners for console errors and warnings, `pageerror`,
  `requestfailed` and HTTP status ≥ 400.
- `npx playwright install` failed with `SELF_SIGNED_CERT_IN_CHAIN`, because
  the package cache proxy uses its own CA (`grain-ca.crt`).
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright
  install --only-shell chromium` worked.
- There was no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed tooling was used.
- Identity: the seeded team member `alice`, set through
  `localStorage["grain.username"]`, as the e2e fixture does.

## Stubs and boundaries

- **State-repository reconciler.** New board, Edit board, Add panel and
  Keep file a task against the state repo (`grain/config-default` in the
  demo). A board only appears once an agent's pull request to
  `tables/board*.json` merges, and the demo has no agent or GitHub for that.
  - The asynchronous half was exercised through the UI (step 2).
  - The resulting boards were stood in for with `POST /api/boards`:
    `WT List`, `WT Kanban` (own columns), `WT Gantt`, `WT Dash` and
    `WT Report`. The first two were filed in `WT Folder`.
  - They were removed with `DELETE /api/boards/{id}` at the end (all 204),
    because the UI has no board delete.
  - One create had `width: "wide"` and was refused with `400 panel 6: width
    must be "third", "half" or "full"`, which is correct. On the first
    store, that refused create still used up the id `board-7`.
- **Network failure for `/api/metrics`.** To see how panels and the report
  render a failed or slow load, Playwright's `page.route` aborted
  `/api/metrics` (`connectionrefused`) or delayed it 4s before letting the
  real server answer.
  - That is a network fault, not a fake answer: no response body was
    invented.
  - It was used in steps 6 and 7 only. The regression test for what it
    found is a unit test, because the e2e suite no longer injects faults
    into grain's own API (`2026-10-02-e2e-own-api-stubs.md`).
- **GitHub.** Opening any task requests
  `GET /api/repos/<repo>/github-branches`, which returns 404 because the
  demo has no GitHub boundary. That logs one console resource error and is
  not a finding.
- **`GET /api/tasks net::ERR_ABORTED`.**
  - This is a correction to the last two reports, which put these lines
    down to polls cancelled by navigation.
  - A trace of an idle page shows otherwise. The first `/api/tasks` is a
    200, and every poll after it, every 3s, is answered `304 Not Modified`
    (the conditional poll in `api.js`).
  - Chromium reports each of those 304s as `ERR_ABORTED`. They are
    harmless, and they were filtered out of the logs below.

## Steps and outcomes

1. **Boards index and rail.**
   - The rail's "boards" entry shows the count: 3 seeded, and 9 with the
     throwaway boards before one duplicate was removed. It opens `/boards`,
     whose header count matches.
   - The index lists the loose boards first and then the `WT Folder`
     heading with its boards, in server order. It offers no search or sort,
     by design.
   - Opening `WT List` lands on `/boards/board-4?q=pagination`, and its 2
     rows match.
   - Typing "extract" rewrote `q` in place, giving 1 row. Reload kept both
     the URL and the field. Back went to `/boards` (the search replaced the
     entry rather than pushing one), and Forward returned.
   - Every seeded and throwaway board opened cold from its URL. A board
     whose query names a repo opens with that query in its address.
   - `/boards/board-99` and `/boards/board-` both fell back to `/boards`.
2. **Asynchronous creation.**
   - Create options → New board, opened on `/?q=pagination&sort=created:desc`,
     prefilled `Add a board` with `kind: list` and that query.
   - Create task filed a queued task against `grain/config-default`, and
     the board count was unchanged (8 → 8).
   - Edit board on `WT Dash` filed `WT: change the WT Dash board` the same
     way and returned to the dashboard it came from. The task detail shows
     the full prompt.
   - New board from the `WT Gantt` page carries `kind: gantt` and the
     board's `f=repo:any:acme/widgets`.
   - **Bugs 2 and 3 were found here.** See Findings.
3. **List and kanban.**
   - Clicking a card opens `/tasks/1`, and Escape returns to the board.
     Enter on a focused card (`tabindex=0`, `role=link`) opens `/tasks/2`,
     and Back returns.
   - `WT Kanban` (own columns Waiting/Going/Done) draws them, offers no
     Columns button, and notes "5 tasks in no column (Awaiting reply,
     Awaiting submit, Awaiting ack, Deferred, Closed)".
   - Columns dialog on the seeded Board:
     - The first column's "left" and the last column's "right" are
       disabled.
     - Removing every column says "No columns. Saving now puts the default
       board back.", and Cancel discarded it.
     - "+ Add column" with no title or states saved as "New column" (0
       cards) and survived reload.
     - Adding Queued to Proposed's states moved Queued out of its old
       column. The board then drew Proposed with 6 cards and an empty
       Queued.
     - Reset to default restored the five default columns.
   - Card moves within Queued:
     - A keyboard pickup of task 14 offered 4 slots and moved it to the
       top, giving `14,2,11,12,13`.
     - A mouse drag of task 13 onto task 2's card gave `14,13,2,11,12`.
     - Reload kept the order.
     - A drag into the Running column did nothing, and the task stayed
       `queued`.
     - Escape cancels a pickup.
   - Sorting (`?sort=created:desc`) removes the move handles.
4. **Timeline.**
   - Range offers last 24 hours / 7 / 30 / 90 days / everything:
     - 24h drew 12 bars, "2 tasks outside this range", with hourly ticks.
     - 30d and Everything drew all 14 bars.
     - The today marker (`.gantt-now`) is on every range.
   - Lanes offers no lanes / by repo / by state / by author. By state drew
     one lane per state, with counts.
   - Range and lanes survived reload. They are in localStorage, not the
     URL, as the 2026-10-02 report also says.
   - The Range select is operable from the keyboard (Enter, arrows,
     Enter).
   - Clicking the row for "Extract pagination query helper" opened
     `/tasks/11`, and Back returned with the range kept.
   - `WT Gantt` follows the same browser-wide range and lanes. Search there
     narrowed it to 2 bars, with `?q=pagination` added to the board's own
     `f=`, and reload kept it.
   - Each bar has an `aria-label` such as "Running: waited 20m, running for
     32m".
   - No dependency links are drawn (task 11 depends on 8). This is
     unchanged, and nothing in GanttChart renders them.
5. **Dashboard (`WT Dash`).**
   - Unnarrowed panels:
     - States: 1/5/2/3/1.
     - Running only: 2 rows.
     - Week: 10 bars.
     - Widgets metrics: 12 filed / 3 completed.
     - Nothing matches: "Nothing here right now."
     - Inherit list: 12.
   - Searching "pagination" narrowed only the inheriting panels (States
     0/3/1/0/0, Week 4 bars, Inherit list 4). The scoped panels kept their
     own queries. Reload kept the narrowing, and the URL carries the whole
     narrowing.
   - Every panel's Open is a link:
     - The inheriting panels go to `/?q=pagination` (4 rows).
     - Running only goes to `/?f=state:any:running` (2 rows).
     - Widgets metrics goes to
       `/metrics?f=repo:any:acme/widgets&f=created:within:30`.
     - Nothing matches goes to `/?q=zzzznothing` (0 rows).
     - Back returns to the narrowed dashboard every time.
   - Keep is offered only on the three inheriting panels.
     - Keep on "Inherit list" prefilled `Narrow the panel "Inherit list"…`.
       The prompt lists all six panels with their `order_key`s, marks
       panel 6, and asks for `q=pagination`.
     - Add panel prefilled the same panel list and that query.
   - Reordering panels:
     - A keyboard pickup of panel 5 offered 5 slots. "Above panel 1"
       persisted it at `orderKey -1048575`.
     - A mouse drag of States onto Widgets metrics put States before it,
       and reload kept that.
   - Loading and error, with the network stub:
     - A failed `/api/metrics` makes the metrics panel say "The metrics
       report could not be loaded."
     - A delayed one shows "Loading…" and then the totals.
6. **Report (`WT Report`, 10 named sections, scoped to
   `f=repo:any:acme/widgets`).**
   - The window line and "Measured over the tasks in acme/widgets." are
     shown.
   - Throughput, Latency, Attempts and Backlog had data. The other six
     each print "<Section>: nothing to report over this window."
   - Backlog's "task 2" opens the task.
   - A keyboard pickup of section 10 offered 9 slots, and the first put
     Sandboxes first (`orderKey -1048575`). Reload kept it.
   - Window → 30 days re-measured, giving 12 filed.
   - `f=repo:any:nobody/nothing` reads all zeroes, scoped to
     nobody/nothing.
   - The seeded Report board, which names no sections, prints all 11,
     Merge queue included.
   - **Bug 1 was found here** (with the `/api/metrics` network stub). See
     Findings.
7. **Delete and confirmation.**
   - The UI still has no board delete.
   - On the throwaway task filed in step 2, the task pane's close control
     closed the task at once, with no confirmation. That is the same as
     2026-10-02: it is reversible (Reopen), and the task was this run's own.
   - No other destructive control is exposed in this area.
   - Boards index: a keyboard pickup of "Move WT List" offered only legal
     slots, with its own place left out. "At the end of WT Folder"
     persisted `folder: "WT Folder"` and `orderKey 1048576`, and reload
     kept it.
8. **Phone (390x844, touch).**
   - `/boards`, list, kanban, timeline, dashboard and report all had
     `scrollWidth` equal to the viewport, so nothing overflows sideways.
   - Tapping a kanban card opened the task.

## Findings

### Fixed in this task

1. **A report board that failed to load spun forever.**
   - Reproduction:
     - Open `/boards/<report board>` while `GET /api/metrics` fails (a
       network refusal here).
     - The shared error banner says "Failed to fetch" and clears itself
       after 5s.
     - The report area then shows the "Loading the report" spinner
       indefinitely. Six seconds in, the only thing on the page was the
       spinner, so it reads as slow rather than broken.
     - The `/metrics` overlay renders the same `ReportBoard` and behaved
       the same way.
     - The dashboard's metrics panel already says "could not be loaded".
   - Fix (`b8218729c`):
     - `ReportBoard` keeps why the newest fetch failed. While it has no
       report, it shows "The report could not be loaded (Failed to fetch).
       Refresh to try again."
     - A successful Refresh clears the message.
     - A failure after a report has landed keeps the report.
   - After the fix: the message replaced the spinner, and Refresh with the
     network back drew the report.
   - Regression: the `ReportBoard.test.jsx` case "says the report could not
     be loaded instead of spinning for good" fails on the old component.
2. **Edit board left a Kanban board's own columns out of its prompt.**
   - Reproduction:
     - A kanban board carrying columns (Waiting/Going/Done) offers no
       column editor. `TaskBoard`'s `fixedColumns` sends the reader to
       Edit board.
     - Edit board's "What it says today" listed name, folder, kind,
       description and query, but not the columns.
     - The prompts for the other two kinds with child rows do list them
       (panels and sections).
   - Fix (`4c8d509a9`): the prompt now says "Its columns, left to right:"
     with each column's name and states. A board with no columns of its
     own says it follows the reader's browser.
3. **New board's menu of kinds pointed at notes the prompt did not carry.**
   - Reproduction: New board from a list wrote "panels (…, see the panels
     section below)" and "report (…; see the report section below)", but
     only a prompt for those kinds carries those sections.
   - Fix (`4c8d509a9`): the pointer is written only when the note is
     present.
   - Regression for 2 and 3:
     - Three `boards.test.js` cases, which fail on the old code.
     - E2e `2b` in `saved-boards-walkthrough.spec.js`, which drives the
       real Edit board and New board buttons.

### Observations (not changed)

- The Kanban columns dialog saves a column with no title or states as an
  empty "New column". It is harmless and visible, so it is not changed.
- After a keyboard pickup of a card, Tab goes to that card's own "Select"
  checkbox. The "Move here" slots come before it in the DOM, so they are
  reachable with Shift+Tab or by pointer, but not by the next Tab.
- The Keep and Add panel prompts always say "The query is the whole of
  what is on screen, this deployment's own default condition on state
  included". A dashboard opened from a deep link such as
  `?q=pagination` carries no state condition, so that sentence is
  inaccurate there, though harmless.
- The report's Backlog task references ("task 2") are buttons, not links,
  so they cannot be opened in a new tab. The dashboard's Open controls are
  real links.
- The report's Window choice and the timeline's range and lanes are not in
  the URL. This is the documented design: the timeline's is a browser-wide
  setting.
- No report section kind is prose, so a "prose section" was not
  exercisable. Dependencies are not drawn on the timeline (unchanged since
  2026-10-01).
- The browser tab title is the deployment name ("grain"), not the board's.
  This is by design (grain/task-69).

## Automated verification

- Vitest, whole suite: 167 files, 2816 tests passed. With the two fix
  files stashed, the 4 new unit tests fail.
- Playwright on chromium: 39 passed across
  `saved-boards-walkthrough.spec.js` (13, including the new `2b`),
  `boards.spec.js`, `board.spec.js` and `gantt.spec.js`.
- No screenshots, traces or other browser artifacts are committed.
