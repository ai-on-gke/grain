# Saved boards and task visualizations walkthrough — 2026-11-06

## Commit and environment

- **Commit tested:** `022be124` (`main`, "Merge pull request #774"), on
  branch `grain/098073e4/task-3159-walk-through-saved-boards-and`. The
  three fixes below were built on top of it and checked again in the
  browser.
- **Date:** 2026-11-06, 10:01–10:45 UTC.
- **Server:** the real `grain demo`, built with `go build ./cmd/grain`
  (go1.26.10) after `npm ci && npm run build` in `ui/` (Node v20.19.0;
  the production Vite bundle embedded from `pkg/ui/static`). Each server
  ran over its own throwaway embedded store (`TMPDIR` pointed at a
  scratch directory) with `-open=false -access-code-file <file>
  -human-review -merge-queue-states`:
  - `127.0.0.1:18801`: `022be124` unchanged, for §1–§10 and the before
    screenshots;
  - `127.0.0.1:18802`: rebuilt with the two UI fixes, for the after
    screenshots and the layout checks in "Bugs found".

  `-github-sim` was meant to be on as well; with `-human-review` it kept
  the demo from starting at all (bug 3), so both servers ran without it.
- **Browser:** Playwright 1.62.1 Chromium (`/var/cache/playwright`),
  driven by ad-hoc Node scripts through visible controls (not
  committed). Sign-in went through the "Access code" page each time.
  Desktop was 1440×1000 (1280×900 for the layout checks); phone was
  390×844 (`iPhone 13` descriptor, `isMobile`, `hasTouch`). Every script
  recorded page errors, console errors, HTTP ≥ 400 responses, failed
  requests and every non-GET `/api` call.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so no
  Gemini-backed tooling ran and `LOOK=1` was not used. The file reader
  shows PNGs as bytes, so the screenshots were checked by bounding boxes
  (see "Bugs found").

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`** (task 23, "WT held
   proposal: tidy the retry docs", `proposedBy: "8"`,
   `holdForApproval: true`, a `proposalKey`). The demo seeds no held
   proposal: only an agent's `propose_task` with `hold_for_approval`
   files one, and `grain demo` runs no agent.
2. **Stand-in boards, created over `POST /api/boards`.** The product's
   own creation path files a task against `grain/config` for an agent to
   turn into a pull request; that contract was exercised first (§2). The
   stand-ins, all with `orderKey` 0:
   - `WT List` (board-5, `list`, `f=state:any:running`, folder `WT3159`)
   - `WT Kanban Reply` (board-6, `kanban`; columns Todo
     (`proposed`,`queued`) / Doing (`running`) / Talking
     (`awaiting_reply`), folder `WT3159`)
   - `WT Kanban Submit` (board-7, `kanban`; columns Sign off
     (`awaiting_submit`) / Doing (`running`), folder `WT3159`)
   - `WT Gantt` (board-8, `gantt`, `q=retry`, folder `Ops`)
   - `WT Tree` (board-9, `tree`, `q=pagination`, folder `Ops`)
   - `WT Tree Ref` (board-10, `tree`, `f=externalRef:any:CA-204`,
     folder `Ops`)
   - `WT Held` (board-11, `kanban`, `f=humanReview:filled:`, folder
     `Ops`)
   - `WT Dash` (board-12, `panels`, `f=repo:any:acme/widgets`, folder
     `Ops`; panels `states`, an inheriting `list` "Inherit", a `list`
     "Running only" with its own query at `third` width, `metrics`, a
     `gantt` 24h at `full` width, and a `list` "Nothing" over
     `q=zzzznothing`)
   - `WT Empty Dash` (board-13, `panels`, no panels)
   - `WT Report Widgets` (board-15, `report`, `f=repo:any:acme/widgets`;
     sections backlog titled "Waiting work", throughput, tools, agents)
   - `WT Report All` (board-16, `report`, all 13 `model.ReportSections`)

   A first `WT Report All` named `capacity` (a heading, not a section
   kind) and was refused with a 400 listing the 13 valid kinds; the id
   `board-14` it would have had stays unused.
3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*` passed through, then given a `tools` census and
     `agents` telemetry in the shapes `ReportBoard.sections.test.jsx`
     uses. The demo's runs record neither, so this is the only way
     those sections draw in a browser.
   - `**/api/metrics*` delayed 1.5 s then `500 {"error":"synthetic
     metrics 500"}`, for the metrics panel's loading and error states
     and the report's failed Refresh.
   - `**/api/tasks` (GET) answered `500 {"error":"synthetic tasks
     500"}`, for a dashboard whose task load fails.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- The rail's `Boards 4` (a `role=button` row) opened `/boards`: Board /
  kanban, Timeline / timeline, Report / report, Tree / tree, each with
  its description. The index has no search or sort control (0 inputs, 0
  comboboxes); its header carries `new board`.
- With the stand-ins the rail read `boards 15` and the index showed the
  unfiled boards first, then folders `Ops` and `WT3159`. Every row has a
  named handle (`Move <board>`), titled "Drag this to reorder, or tap it
  to pick this up and choose where it goes".
- **Dragging in a section below the first.** `WT Dash`'s handle dropped
  on the top edge of `WT Gantt` (both in `Ops`) sent `PATCH
  /api/boards/board-12 {"folder":"Ops","orderKey":-1048576}` and put it
  first in `Ops`; the order survived a reload.
- All four seeded boards and every stand-in opened from a direct URL
  with no horizontal scroll at 1440 px. `/boards/board-999` returned to
  `/boards` (what `saved-boards-walkthrough` 1 pins).
- `WT List` from the index opened `/boards/board-5?f=state:any:running`;
  Back returned to `/boards` (15 boards), Forward and reload came back
  to the board.

### 2. Requesting a board (the asynchronous contract)

- **Rail:** `Create options` → menu `new task | new session | new
  board`. `new board` opened `/new` with the title "Add a board" and the
  description that asks for name, folder, kind, description and query,
  lists the six kinds, explains the query syntax and says the board is a
  row in `tables/board.json` of this deployment's state repository.
  Filed as "Add a board (WT3159 via rail)" it became task 24, `queued`
  against `grain/config`. The board list did not change: no board
  appears until an agent's pull request merges.
- **Boards index header:** `new board` opened the same draft. With
  "queue immediately" unticked it was filed as task 25, `proposed`
  against `grain/config`.
- On a board, `add panel` (dashboard) drafted "Add a panel to the
  dashboard "WT Dash"" listing the six panels with their widths, own
  queries and `order_key`s; `edit board` drafted "Change the board "WT
  Dash"" with its name, folder, kind, query and panels; `new board` on
  `WT Kanban Reply` drafted a kanban request. `discard draft` cleared
  each without filing anything.

### 3. List and Kanban

- `WT List` showed 3 and 8; a row opened `/tasks/3`, Escape returned to
  the board with its query.
- **Seeded `Board`:** columns Proposed / Waiting / Running / Asked /
  Failed / Merging, and "2 tasks in no column (Deferred, Closed)."
  - The merge-queue column reads **merging**, holding 5 ("grain ·
    Waiting for CI") and 20 ("grain · Waiting for an approval GitHub
    requires").
  - Task 11's card wears the **blocked oval** (`badge-blocked`,
    "Blocked on: #8 (running)") in Waiting.
  - The held proposal 23 carries **"held for approval by 8"**.
  - The imported issue 12 carries **imported** ("imported from GitHub
    issue acme/widgets#42").
  - The human-review-held change 21 (from `-human-review`) sits in
    Asked with a **Human review** chip and the badge "Asked you to submit
    acme/widgets#107 — held for a person's review: …". Its review, task
    22, carries `review` and its base `grain/task-21`.
- **No-column notes on custom columns:** `WT Kanban Reply` (collects
  `awaiting_reply` only) said "12 tasks in no column (Asked to submit,
  Merging, Deferred, Closed)."; `WT Kanban Submit` (collects
  `awaiting_submit` only) said "14 tasks in no column (Proposed,
  Waiting, Asked for a reply, Merging, Deferred, Closed)." Narrowed to
  `f=repo:any:acme/widgets`, the latter counted 12, so the note follows
  the narrowing. Narrowed to the state no column collects, the board
  said nothing matched as well (bug 2).
- `WT Held` (`f=humanReview:filled:`) showed only 21, under the chip
  "Human review is set".
- **Opening a card:** the title of card 11 opened `/tasks/11`; Escape
  returned to `/boards/board-1` with focus on that card's title.
- **Moving cards:**
  - Keyboard: Enter on `Move task 22` offered "Move here, above Fix
    flaky retry test, in Waiting", "…above Extract pagination query
    helper…", "…to the end of Waiting"; Enter on a slot sent `POST
    /api/tasks/reorder {"ids":["22"],"afterId":"2","beforeId":"11"}`
    and kept focus on `Move task 22`.
  - Mouse (HTML5 drag): task 2 dropped on the last card sent `{"ids":
    ["2"],"afterId":"11","beforeId":"24"}`.
  - Phone tap: tapping `Move task 24` then the first slot sent
    `{"ids":["24"],"beforeId":"22"}`.
  - Each order (22, 11, 2, 24 then 24, 22, 11, 2) was the same after a
    reload.
- **Column editor (`edit columns`):** every icon button is named; `Move
  Proposed left` and `Move Merging right` are disabled. `Move Failed
  left` kept focus on itself. Renaming column 2 to "Up next" and
  pressing Escape raised the native "You have unsaved changes here.
  Leave and discard them?"; dismissing it kept the dialog open. Saved,
  the board drew Proposed / Up next / Running / Failed / Asked /
  Merging, the same after a reload (`localStorage
  ['grain.board.columns']`). A board with its own columns (`WT Kanban
  Reply`) ignored it. `reset to default` and save put the six default
  columns back.

### 4. Timeline / gantt

- `Range` offers last 24 hours / 7 / 30 / 90 days / everything; `Lanes`
  offers no lanes / by repo / by state / by author (no "by owner",
  although tasks now carry an owner).
- The default (7 days) drew 23 bars, "1 task outside this range." and
  one `.gantt-now`; ticks Oct 31 … Nov 6.
- **24 hours + by repo:** acme/widgets, grain/config, no repo, "3 tasks
  outside this range.", ticks every three hours. This survived a reload
  (the pickers read Last 24 hours / By repo) and the URL stays bare.
- **Everything + by state:** proposed, waiting, running, asked, merging,
  deferred, closed. **By author:** debian, director, grain,
  issue-import.
- **Dependencies:** task 11's bar reads "Waiting, blocked: waiting for
  51m, never dispatched"; dependencies are drawn as that state, with no
  arrows.
- A bar click opened `/tasks/1`; Escape returned with focus on
  `a.gantt-row-title`.
- `WT Gantt` cold-opened on `?q=retry` with 8 bars, in the reader's
  lanes (by author), the same after a reload.

### 5. Tree

- **Seeded `Tree`:** the director session nests 1, 3 and 8; 8 nests 11
  and the held proposal 23 (filed by 8).
- `WT Tree` (`?q=pagination`) showed 8 → 11, the same after a reload.
- `WT Tree Ref` (`f=externalRef:any:CA-204`, chip "External ref is any
  of CA-204") showed 8 → 11, 23: 23 inherited CA-204 from task 8, which
  proposed it.
- `/boards/board-4?f=state:any:running|queued` nested under the chip
  "state is any of running, waiting".
- The toolbar search `retry` wrote `/boards/board-4?q=retry` (8 rows)
  and survived a reload with the box still reading "retry".
- A row of `WT Tree` opened its task.

### 6. Dashboard (`WT Dash`)

- Panels: state counts, Inherit ("Showing 8 of 16."), Running only,
  Throughput, Timeline ("Showing 10 of 14 in this range. 2 tasks outside
  this range."), Nothing ("Nothing here right now.").
- **Each `Open` goes to the panel's own question:**
  `/?f=repo:any:acme/widgets` for the inheriting panels,
  `/?f=state:any:running`, `/?q=zzzznothing`, and
  `/system/metrics?f=repo:any:acme/widgets` for Throughput.
- **State tiles link to lists of the same size:** proposed 3, waiting
  3, running 2, asked 4 (`awaiting_reply|awaiting_submit`), failed 0,
  merging 2 (`completed`), each opened in its own page and counted.
- **Whole-board narrowing:** the search `pagination` wrote
  `?q=pagination&f=repo:any:acme/widgets`; the inheriting panels' links
  and the metrics link took it, the own-query panels did not. It
  survived a reload; clearing it went back to `?f=repo:any:acme/widgets`.
- **Reorder:** keyboard `Move panel 6, "Nothing"` → the first slot sent
  `PATCH /api/boards/board-12 {"panels":[…]}` and moved focus to `Move
  panel 1, "Nothing"`; a mouse drag of panel 3 onto panel 1 made
  Inherit first. After a reload: Inherit / Nothing / State counts /
  Running only / Throughput / Timeline.
- **Loading and error:** with metrics delayed then 500, Throughput read
  "Loading…", then "The metrics report could not be loaded."
- **Failed task load:** with `/api/tasks` answering 500 the dashboard
  drew every panel empty — "0 proposed … 0 merging", "Nothing here right
  now.", the timeline's "No tasks yet." — with the 500 only in the
  five-second error banner, and the rail read "tasks 0". This is the
  app shell's deliberate fallback (App.jsx renders "on whatever did
  land"); it is recorded under Observations, not fixed here.
- **Empty:** `WT Empty Dash`: "This dashboard has no panels yet. “Add
  panel” files the task that adds one."

### 7. Report

- **Seeded `Report`** (no sections named) drew Throughput, Latency,
  Capacity, Backlog and GitHub CI, omitting every silent section.
- **`WT Report All`** names all 13 and accounts for each silent one:
  "Tool use / Prompts / CI waits / CI queueing / Scheduling / Sandboxes
  / Merge queue / Agents: nothing to report over this window."
- **Window:** the picker offers last 24 hours / 7 / 30 / 90 days. 90
  days wrote `?window=90d` and asked `/api/metrics?window=90d`; a
  reload kept it (picker "Last 90 days"); Refresh asked once more.
  `?window=bogus` fell back to "Last 7 days" and dropped the parameter.
- **Links:** the backlog chips proposed 4 / waiting 4 / running 2 /
  asked 1 link to `/?f=state:any:…` lists of 4 / 4 / 2 / 1 rows (asked
  via `f=state:any:awaiting_reply&f=completed:empty:`), and "task 2"
  links to `/tasks/2`.
- **`WT Report Widgets`** said "Measured over the tasks in
  acme/widgets." and asked `/api/metrics?repo=acme%2Fwidgets…`. Unstubbed
  it drew Waiting work and Throughput, with "Tool use / Agents: nothing
  to report over this window."; with the census stub (§Stubs 3) both
  drew (the agents table with `system:pipeline-steward`).
- **Reorder:** keyboard `Move section 4, "Agents"` → the first slot
  sent `PATCH /api/boards/board-15 {"sections":[{"kind":"agents",…
  "orderKey":-1048566},…]}` and moved focus to `Move section 1,
  "Agents"`; after a reload: Agents, Waiting work, Throughput, Tool use.
- `?q=retry` on it printed "A report is measured over whole
  distributions, so the rest of this question is not in force: the
  search for "retry"."
- **Failed Refresh** (metrics 500): the alert "synthetic metrics 500";
  the sections already drawn stayed.

### 8. Narrowing values a board names

- `/boards/board-4?f=state:any:bogus`: "State is any of “bogus”
  (unknown)", titled "“bogus” is not a value State has, so it matches no
  task" (what `saved-boards-walkthrough` 5d pins).
- `f=blocked:any:maybe`: "Blocked is any of Not blocked" over 0 rows;
  `f=humanReview:any:product_decision`: an ordinary chip over 0 rows
  (bug 1).
- `f=owner:any:me` read "Owner is any of me (debian)".

### 9. Destructive actions

- No board page and no part of `/boards` has a delete or remove control
  (checked by role on `/boards` and every board). Boards are removed
  through `grain/config`.
- The column editor's `Remove <col>` acts only on this browser's draft,
  behind the unsaved-changes confirm.
- The throwaway board requests 24 and 25 were closed with each task
  page's `Close` (`POST /api/tasks/{24,25}/close
  {"close_pull_request":false}`), with no confirmation, which fits Close
  being reversible. Both stores were discarded afterwards.

### 10. Phone, keyboard, console and network

- At 390×844, `/boards` and all 15 board pages had `scrollWidth ==
  innerWidth`.
- Tab from the top of `/boards` reaches the rail, `new board`, the
  alerts and System/Settings buttons, then each board's handle and link
  in order.
- No uncaught page errors. The only errors were expected: the
  pre-sign-in `401` on `/`; `404 /api/repos/<repo>/github-branches`
  when a task overlay opens without `-github-sim`; the synthetic 500s.
  Playwright reports every `/api/tasks` poll answered `304 Not
  Modified` (the poll sends `If-None-Match`; `curl` confirms the 304)
  as `net::ERR_ABORTED`; nothing failed.

## Bugs found

1. **A board narrowed by a blocked, auto-merge or held value that does
   not exist read as a real filter.** (Fixed here.)
   - Reproduction: `/boards/board-4?f=blocked:any:maybe` (or any board,
     or `/?f=blocked:any:true`). The chip reads "Blocked is any of Not
     blocked" over an empty board: `blocked`'s `labelOf` words every
     value but `yes` as "Not blocked". `autoMerge:any:yes` reads "Off"
     the same way. `humanReview:any:product_decision` (the category is
     `product-decision`) reads like the real filter over an empty board.
   - Cause: `unknownValues` (`ui/src/taskFilters.js`) only checks an
     attribute that says `closed: true`, and only `state` did.
   - Fix (`ui/src/taskAttributes.js`): `blocked` (yes/no) and
     `autoMerge` (on/off), both computed by this build, and
     `humanReview`, whose `order` is `model.HumanReviewCategories`
     ("every category request_human_review accepts"), say `closed:
     true`. `origin` is left open: its `order` omits `imported`, which
     `originOf` returns (see Observations).
   - After, on 18802: "Blocked is any of “true” (unknown)" in the
     warning colour, titled "“true” is not a value Blocked has, so it
     matches no task"; `?f=blocked:any:yes` reads "Blocked is any of
     Blocked" and shows card 11.
   - Coverage: `taskFilters.test.js` "marks a value a yes/no or
     held-category attribute does not have" (fails without the fix);
     e2e `saved-boards-walkthrough.spec.js` 5d2.
2. **A narrowed Kanban said nothing matched when what matched was in no
   column.** (Fixed here.)
   - Reproduction: `WT Kanban Reply` (columns Todo, Doing and Talking;
     none collects `awaiting_submit`) at `?f=state:any:awaiting_submit`:
     "8 tasks in
     no column (Asked to submit)." and, under it, "No tasks match this
     search and these filters." Eight tasks matched.
   - Fix (`TaskBoard.jsx`): when the narrowing matches tasks the note
     counts as in no column, the empty message says "None of the tasks
     this search and these filters match is in one of these columns."
     A narrowing that matches nothing still says "No tasks match…".
   - Coverage: `TaskBoard.test.jsx` "does not say nothing matches when
     what matches is in no column" (fails without the fix); e2e
     `saved-boards-walkthrough.spec.js` 2c, extended with the narrowed
     case.
3. **`grain demo -human-review -github-sim` did not start.** (Fixed
   here.)
   - Reproduction: the demo exits with "-human-review: seeding the held
     change's review: base "grain/task-21" does not exist on
     acme/widgets". `-review-questions` and `-sensitive-hold` seed the
     same review-on-the-held-branch shape and start fine beside
     `-github-sim`.
   - Fix (`pkg/demo/demo_human_review.go`): push the held change's
     branch to the simulated repo first, as those two do.
   - Coverage: `TestHumanReviewDemoSeedsWithGitHubSim` (the review's
     base and the pushed branch), beside the two sibling tests. The
     rebuilt binary starts with both flags.

**Layout checks for 1 and 2** (only text changed, in elements that
already existed). `e2e/screenshot.mjs` before (18801) and after (18802),
pages `/boards/board-6?f=state:any:awaiting_submit`,
`/boards/board-1?f=blocked:any:true` and
`/boards/board-11?f=humanReview:any:product_decision`, desktop and
phone, light and dark; the PNGs could not be looked at here, so by
bounding boxes on 18802 at 1280×900 and 390×844 in both themes:

- the chip "Blocked is any of “true” (unknown)" is 227×24 px and sits
  inside its 1048 px (desktop) / 390 px (phone) narrowing bar;
- the new empty sentence wraps inside the board column's width (1048 px
  desktop, 390 px phone), below the no-column note, in the secondary
  text colour (`rgb(108,104,94)` on `rgb(247,246,243)` light,
  `rgb(149,145,137)` on `rgb(15,16,19)` dark);
- no page scrolls horizontally.

Runs:
- `vitest`: 3907 tests passed in 252 files.
- Playwright `saved-boards-walkthrough`, `board`, `boards`,
  `boards_workflows`, `gantt` and `report-backlog-links`: 61 passed,
  against a bundle built with both UI fixes.
- `go test ./pkg/demo -run 'HumanReview|ReviewQuestionsDemoSeeds|SensitiveHoldDemoSeeds'`
  and `go test ./pkg/cli -run 'Attribute|Vocabulary'` passed.

## Observations (not fixed here)

- **A failed task load draws boards as empty.** With `/api/tasks`
  answering 500, every board and panel says "Nothing here right now." /
  "No tasks yet." / 0 tiles, the rail says "tasks 0", and the reason is
  only in a five-second banner. The shell does this deliberately
  (App.jsx's `Promise.allSettled` start-up renders "on whatever did
  land"); telling "no tasks" from "tasks did not load" is an app-wide
  change, filed as a follow-up.
- **`origin` differs between the UI and `grain list`.** The UI's
  `originOf` answers `imported` for an imported issue, but `imported`
  is not in the attribute's `order`, and `pkg/cli`'s `taskOrigin` /
  `taskOrigins` have no `imported` at all, so `grain list -filter
  origin:any:imported` cannot find what the UI's filter does. Filed as a
  follow-up.
- The `-human-review` held change's run is seeded as finished ten
  minutes before the task is filed, so its timeline bar reads "Asked:
  never dispatched, 0s old when it ended". Harmless demo data.
- The timeline's Lanes offer "by author" but not "by owner", now that
  tasks carry an owner.
- Chips are lowercased by the theme (`MuiChip` `textTransform:
  lowercase`), so a narrowing on an external ref shows "ca-204" for
  `CA-204`; the chip's text content keeps the case.
- Still true from earlier runs: a board created from `/boards` says
  `query: <see below>`; the report window picker replaces the history
  entry.
