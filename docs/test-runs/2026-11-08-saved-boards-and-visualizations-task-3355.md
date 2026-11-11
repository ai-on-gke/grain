# Saved boards and task visualizations walkthrough — 2026-11-08 (task 3355)

A second walkthrough of the same commit ran alongside this one and
landed as `2026-11-08-saved-boards-and-visualizations.md`; it fixed a
failed boards load reading as "No boards yet" and the index's pointer at
a New board button. This run found and fixed a different bug.

## Commit and environment

- **Commit tested:** `6d15dc3a` (`main`, "Merge pull request #912"), on
  branch `grain/098073e4/task-3355-walk-through-saved-boards-and`
  (task 3355, attempt 2; attempt 1 pushed nothing). The binary reports
  `v0.0.0-20261108080820-6d15dc3a27aa`. The fix below was then built on
  top of it and checked again in the browser.
- **Date:** 2026-11-08, 08:18–08:50 UTC.
- **Toolchain:** Debian sandbox, `linux/amd64`, Go 1.26.10, Node.js
  v20.19.0. `cd ui && npm ci && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain`, so the embedded
  `pkg/ui/static` bundle was the one under test.
- **Servers** (each over its own throwaway embedded store, `TMPDIR`
  pointing at a scratch directory):
  - `127.0.0.1:18820`: `6d15dc3a` unchanged, for every step below:
    `grain demo -open=false -access-code-file <file> -github-sim
-fake-ci -human-review -merge-queue-states -base-branch-ci`.
    (`-observability` was tried first and refused beside `-fake-ci`:
    "each wire a CI of their own; pick one" — a clear message, not a
    bug.)
  - `127.0.0.1:18821`: rebuilt with the fix, plain `grain demo
-open=false -access-code-file <file>`, for the after checks.
- **Browser:** Playwright Chromium from `ui/package-lock.json`
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`), headless, timezone
  UTC. Desktop 1400×900, phone the `iPhone 13` descriptor (390×844,
  touch). Driven by throwaway Node scripts, not committed, that sign in
  through the "Access code" page and then act only through visible
  controls. Every page logged `pageerror`, console errors and warnings,
  `/api/` responses ≥ 400 with their bodies, failed requests, every
  dialog, and every non-GET `/api/` request with its body. The REST API
  was used only to seed the stand-ins below and to read back what a
  gesture had persisted.
- **Gemini:** `/home/debian/.gemini-api-key` is absent, so no
  Gemini-backed model or tooling ran and `LOOK=1` was not used. The file
  reader shows PNGs as bytes, so the layout was checked by bounding
  boxes (see the fix).

## Stubs and stand-ins

The server, store, built frontend, browser and gestures were real
throughout.

1. **Held proposal** (task 23, "WT held proposal: tidy the retry docs"),
   filed with `POST /api/tasks` and `proposedBy: "8"`,
   `holdForApproval: true` and a `proposalKey`. Only an agent's
   `propose_task` with `hold_for_approval` files one, and `grain demo`
   runs no agent.
2. **Stand-in boards, `POST /api/boards`.** The product's own creation
   path files a task against `grain/config` for an agent to turn into a
   pull request, and no agent runs here. That contract was exercised
   first (§2) and these rows stand in for its result:
   - `board-5` WT List (`list`, `f=state:any:running`, folder `WT3355`)
   - `board-6` WT Kanban Submit (`kanban`, columns Sign off
     `awaiting_submit` / Doing `running`, folder `WT3355`)
   - `board-7` WT Kanban Reply (`kanban`, columns Todo
     `proposed,queued` / Doing `running` / Talking `awaiting_reply`,
     folder `WT3355`)
   - `board-8` WT Gantt (`gantt`, `q=retry`, folder `Ops`)
   - `board-9` WT Tree (`tree`, `q=pagination`, folder `Ops`)
   - `board-10` WT Dash (`panels`, `f=repo:any:acme/widgets`, folder
     `Ops`; panels states, list "Inherit", list "Running only"
     `f=state:any:running` at third width, metrics, gantt 24h at full
     width, list "Nothing" `q=zzzznothing`)
   - `board-11` WT Report Widgets (`report`, `f=repo:any:acme/widgets`;
     sections backlog titled "Waiting work", throughput, tools, agents)
   - `board-13` WT Report All (`report`, all 13 `model.ReportSections`)

   The first try at `board-13` spelled two kinds with hyphens
   (`ci-queueing`, `github-ci`). It was refused with a 400 that lists all
   13 valid kinds, so the id `board-12` stays unused.

3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*` delayed 2 s, then `500 {"error":"synthetic
metrics 500"}`, for the metrics panel's loading and error states.
     The same 500 without the delay was used for a failed report Refresh.
   - `GET **/api/tasks` answered `500 {"error":"synthetic tasks 500"}`,
     for a dashboard whose task load fails.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

| #   | Step                                                                                    | Outcome                                                                                                                                                                                                                                                                                                                         | Verdict |
| --- | --------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Rail "Boards", then `/boards`                                                           | Rail reads `boards 12`. The index shows "Boards 12", the two explanatory paragraphs, and sections: loose (Board, Timeline, Report, Tree), then `Ops`, then `WT3355`. Each row has a handle, a name link, a kind chip and its description (or query, or "every task"). The index offers no search or sort, and none is expected. | PASS    |
| 2   | Mouse-drag WT Tree above WT Gantt (inside `Ops`, a section below the first)             | `PATCH /api/boards/board-9 {"folder":"Ops","orderKey":-1048576}`; order Ops: WT Tree, WT Gantt, …                                                                                                                                                                                                                               | PASS    |
| 3   | Mouse-drop WT Kanban Reply (in `WT3355`) on the `Ops` heading                           | `PATCH …/board-7 {"folder":"Ops","orderKey":-2097152}`; it is now first in Ops.                                                                                                                                                                                                                                                 | PASS    |
| 4   | Keyboard: focus "Move WT List", Enter                                                   | A "Moving 1 board" bar and 13 slots, one above every other board plus "outside every folder", "at the end of Ops" and "at the end of WT3355". Escape removes all of them.                                                                                                                                                       | PASS    |
| 5   | Pick up again, focus "Move here, above Board", Enter                                    | `PATCH …/board-5 {"folder":"","orderKey":-1048576}`; WT List is first in the loose section.                                                                                                                                                                                                                                     | PASS    |
| 6   | Reload                                                                                  | Same order; `GET /api/boards` agrees row by row.                                                                                                                                                                                                                                                                                | PASS    |
| 7   | Click WT Gantt, then Back, then Forward                                                 | `/boards/board-8?q=retry` → `/boards` (order intact) → `/boards/board-8?q=retry`.                                                                                                                                                                                                                                               | PASS    |
| 8   | Focus the Report row link, Enter                                                        | `/boards/board-3`.                                                                                                                                                                                                                                                                                                              | PASS    |
| 9   | `/boards/board-12` (an id that was never created)                                       | Lands back on `/boards`.                                                                                                                                                                                                                                                                                                        | PASS    |
| 10  | Rail access to a board: Customize sidebar → Type Board → Item WT Tree → Pin item → Save | The Item list offers all 12 boards. "WT Tree" appears under Pinned and is still there after a reload. Clicking it opens `/boards/board-9?q=pagination` with the rail entry selected. (The rail lists no boards beyond pinned ones, see Observations.)                                                                           | PASS    |
| 11  | Tab from "New board" through the index                                                  | Handle, then name link, for each board in order, then into the rail. Nothing is skipped and focus is never trapped.                                                                                                                                                                                                             | PASS    |

### 2. Requesting a board (the asynchronous contract)

| #   | Step                                               | Outcome                                                                                                                                                                                                                                                                                                                                                                                                                                      | Verdict |
| --- | -------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Rail "Create options" menu                         | New task / New session / New board.                                                                                                                                                                                                                                                                                                                                                                                                          | PASS    |
| 2   | New board (rail), retitle, add a line, Create task | `/new` opens on "Add a board", repo `grain/config`, a written prompt (name / folder / `kind: list` / description / `query: <see below>`, the kinds menu, the query encoding, and where the rows live in `tables/*.json`). Submitting files task 24, then 25 on a rerun, `queued`, `approved`, auto-merge on, holding `api:self-debug`, `github#read-issues`, `grain#ci` and `grain#source`. No board row appears: `/api/boards` stays at 12. | PASS    |
| 3   | New board from the Boards index header             | Same prompt, filed as task 26, `queued`, against `grain/config`. Boards still 12.                                                                                                                                                                                                                                                                                                                                                            | PASS    |
| 4   | New board from WT List's own page header           | The prompt carries `query: \`f=state:any:running\`` and "That query is the view the board was created from".                                                                                                                                                                                                                                                                                                                                 | PASS    |
| 5   | Edit board on WT Tree                              | "Change grain's saved board `board-9` ("WT Tree")", listing what it says today (folder Ops, kind tree, `q=pagination`), then "What to change".                                                                                                                                                                                                                                                                                               | PASS    |

### 3. List and Kanban

| #   | Step                                                                    | Outcome                                                                                                                                                                                                                                                                | Verdict      |
| --- | ----------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------ |
| 1   | `/boards/board-1` (seeded Kanban, browser columns)                      | Proposed 3 / Waiting 6 / Running 2 / Asked 10 / Failed 0 / **Merging** 2 (tasks 5 and 20); "2 tasks in no column (Deferred, Closed)."                                                                                                                                  | PASS         |
| 2   | Chips                                                                   | Card 11 has the blocked oval (`badge-blocked`, "Blocked on: #8 (running)"). Card 23 has "held for approval by 8" (`chip-held`). Card 12 has `imported` (`chip-imported`).                                                                                              | PASS         |
| 3   | Card title click                                                        | `/tasks/1`; Back returns to the board.                                                                                                                                                                                                                                 | PASS         |
| 4   | Columns editor: rename column 1 "Ideas", "Move Waiting left", Save      | Columns: Waiting, Ideas, Running, Asked, Failed, Merging. Same after a reload (kept in this browser, as the editor says). "Reset to default" + Save puts the default layout back.                                                                                      | PASS         |
| 5   | Keyboard pick-up "Move task 2", then "Move here, to the end of Waiting" | `POST /api/tasks/reorder {"ids":["2"],"afterId":"26"}`; 2 is last in Waiting, still after a reload. **Two of the five slots had the same name** ("Move here, above WT rail board 3355, in Waiting", for tasks 24 and 25): the bug below.                               | FAIL → fixed |
| 6   | Mouse-drag card 22 above card 11                                        | `POST /api/tasks/reorder {"ids":["22"],"beforeId":"11"}`; order persists.                                                                                                                                                                                              | PASS         |
| 7   | `board-6` WT Kanban Submit (Sign off = `awaiting_submit` only)          | "15 tasks in no column (Proposed, Waiting, **Asked for a reply**, Merging, Deferred, Closed)." No browser "Columns" button; "Edit board" instead.                                                                                                                      | PASS         |
| 8   | `board-7` WT Kanban Reply (Talking = `awaiting_reply` only)             | "12 tasks in no column (**Asked to submit**, Merging, Deferred, Closed)." At `?f=state:any:awaiting_submit`: "8 tasks in no column (Asked to submit)." with "None of the tasks this search and these filters match is in one of these columns." (the 11-06 fix holds). | PASS         |
| 9   | `board-5` WT List: search "pagination", reload, Enter on a row, Back    | URL `?q=pagination&f=state:any:running`, one row (8), the box still reads "pagination" after the reload. Enter opens `/tasks/8`, and Back returns to the same URL.                                                                                                     | PASS         |
| 10  | Phone: tap "Move task 1", tap "Stop moving", tap a card                 | Slots only in Proposed ("above task 23 …", "to the end of Proposed"). Stop removes them. Tapping a card opens `/tasks/1`. The page is 390 px wide; the columns scroll inside their own scroller.                                                                       | PASS         |

### 4. Timeline / gantt

| #   | Step                                 | Outcome                                                                                                                                                                                                                                                 | Verdict |
| --- | ------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | `/boards/board-2`                    | 24 rows, day ticks Nov 2–Nov 7…, one today marker (`.gantt-now`), "1 task outside this range."                                                                                                                                                          | PASS    |
| 2   | Range → Last 7 days; Lanes → By repo | Range offers 24 hours / 7 / 30 / 90 days / everything; Lanes offers no lanes / by repo / by state / by author. By repo draws lanes acme/widgets, grain/config, no repo.                                                                                 | PASS    |
| 3   | Reload                               | URL still `/boards/board-2`; Range "Last 7 days", Lanes "By repo" (the reader's browser setting, as the board prompt says).                                                                                                                             | PASS    |
| 4   | Bars and dependencies                | Each bar is named with its state and span ("Running: waited 1h 24m, running for 13m"). Task 11's bar reads "Waiting, blocked: waiting for 47m, never dispatched". The dependency is shown in the bar's blocked state, and no connector lines are drawn. | PASS    |
| 5   | Click a bar; Enter on a row title    | Both open `/tasks/1`; Back returns with lanes intact.                                                                                                                                                                                                   | PASS    |
| 6   | `board-8` WT Gantt (`q=retry`)       | 8 rows, all matching "retry", including held task 23.                                                                                                                                                                                                   | PASS    |

### 5. Tree

| #   | Step                                    | Outcome                                                                                                                             | Verdict |
| --- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | `/boards/board-4`                       | 22 under 21; 1, 3 and 8 under 13 (Director Session); 11 and 23 under 8. Task 23, filed with `proposedBy: 8`, nests where it should. | PASS    |
| 2   | `board-9` (`q=pagination`), then reload | `8 › 11` both times; URL keeps `?q=pagination`.                                                                                     | PASS    |
| 3   | Click and Enter on a tree row           | `/tasks/8` both ways.                                                                                                               | PASS    |
| 4   | `/boards/board-4?q=Bump`                | Only task 3.                                                                                                                        | PASS    |

### 6. Dashboard (`WT Dash`)

| #   | Step                                                                             | Outcome                                                                                                                                                                                                                                                                   | Verdict |
| --- | -------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | Open                                                                             | URL `?f=repo:any:acme/widgets`. State counts 16 (3 proposed, 3 waiting, 2 running, 4 asked, 0 failed, 2 merging, 2 in no column), Inherit 16 ("Showing 8 of 16"), Running only 2, Throughput (16 filed, 5 completed), Timeline 16, Nothing 0 ("Nothing here right now."). | PASS    |
| 2   | Open links                                                                       | Inherit, States and Timeline go to `/?f=repo:any:acme/widgets`; Running only to `/?f=state:any:running` (its own query); Throughput to `/system/metrics?f=repo:any:acme/widgets`; Nothing to `/?q=zzzznothing`.                                                           | PASS    |
| 3   | Whole-board search "pagination", then reload                                     | URL `?q=pagination&f=repo:any:acme/widgets`; inheriting panels go to 2 and gain a "Keep" button ("Keep this narrowing on “Inherit”" …); Running only and Nothing do not. Their links carry `q=pagination`. The same after a reload. Inherit's Open lands on a 2-row list. | PASS    |
| 4   | State tile "Running"                                                             | `/?f=repo:any:acme/widgets&f=state:any:running`, 2 rows. Merging's tile links `state:any:completed`, Asked's `awaiting_reply\|awaiting_submit`.                                                                                                                           | PASS    |
| 5   | Keyboard: "Move panel 6, “Nothing”" → "Move here, above panel 1, “State counts”" | `PATCH /api/boards/board-10 {"panels":[…]}`; Nothing is first.                                                                                                                                                                                                            | PASS    |
| 6   | Mouse-drag Running only's header onto Inherit                                    | Running only before Inherit; both moves survive a reload.                                                                                                                                                                                                                 | PASS    |
| 7   | Add panel                                                                        | `/new` with "Add a panel to grain's saved board `board-10` ("WT Dash")", every current panel listed with its kind, width, query and `order_key` (the new order). Repo `grain/config`. Discarded.                                                                          | PASS    |
| 8   | Metrics 2 s then 500 (stub 3)                                                    | Throughput panel "Loading…", then "The metrics report could not be loaded."                                                                                                                                                                                               | PASS    |
| 9   | `/api/tasks` 500 (stub 3)                                                        | Every task panel says "The tasks could not be loaded (synthetic tasks 500)." with Retry. The 11-06 observation that a failed load drew empty boards no longer holds here.                                                                                                 | PASS    |

### 7. Report

| #   | Step                                                                                                          | Outcome                                                                                                                                                                                                                                                                     | Verdict |
| --- | ------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| 1   | `/boards/board-3` (no sections named)                                                                         | Throughput, Latency, Capacity, Backlog, GitHub CI; the sections with nothing to say (prompts, tools, agents, sandboxes, merges, CI waits…) are left out.                                                                                                                    | PASS    |
| 2   | Window → Last 30 days                                                                                         | URL `?window=30d`. Back then leaves the board (the picker replaces the entry; see Observations).                                                                                                                                                                            | PASS    |
| 3   | Deep link `?window=24h`, reload                                                                               | Picker reads "Last 24 hours" both times.                                                                                                                                                                                                                                    | PASS    |
| 4   | Links                                                                                                         | Backlog "Proposed 3" → `/?f=state:any:proposed` (3 rows). "Asked 1" → `/?f=state:any:awaiting_reply&f=completed:empty:` (1 row, task 4). "task 2" (oldest waiting) → `/tasks/2`.                                                                                            | PASS    |
| 5   | `board-11` (repo-scoped, four named sections)                                                                 | "Measured over the tasks in acme/widgets."; every metrics request, load and Refresh, is `/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24`. The named but silent sections print "Tool use: nothing to report over this window." and "Agents: …" rather than vanishing. | PASS    |
| 6   | Reorder sections: keyboard "Move section 4" above section 1; mouse-drag Throughput above Waiting work; reload | Two `PATCH /api/boards/board-11 {"sections":[…]}`; the order Agents, Throughput, Waiting work, Tool use persists.                                                                                                                                                           | PASS    |
| 7   | `board-13` (all 13 sections named)                                                                            | 13 handles in order (Throughput … Agents); silent ones are explained, not hidden.                                                                                                                                                                                           | PASS    |
| 8   | Refresh with metrics 500, then Refresh again unstubbed                                                        | Alert "synthetic metrics 500", with the last good report still drawn. The alert stays for its own ~5 s after the good Refresh, then goes.                                                                                                                                   | PASS    |
| 9   | `?q=retry` on the report                                                                                      | "A report is measured over whole distributions, so the rest of this question is not in force: the search for "retry"."                                                                                                                                                      | PASS    |

### 8. Destructive actions

- The UI exposes no board deletion (no Delete or Remove on the index, on
  a board, or in Edit board), so there was nothing to confirm. Boards
  are changed through a `grain/config` task.
- Throwaway tasks 24 and 25 (filed in §2) were closed from their task
  pages. "Close" on a `queued` task sends `POST /api/tasks/{id}/close
{"close_pull_request":false}` at once, with no confirmation.
  `Actions.jsx` confirms only a cancel of a running task, and a closed
  task can be reopened. Noted, not changed.
- "Discard draft" on a pre-written board request clears the form with no
  confirmation (`NewTaskOverlay.jsx` `handleDiscard`).

### 9. Phone, console and network

- Phone (390×844) at `/boards` and boards 1, 2, 3, 4, 5, 6, 10, 11 and
  13: the document is never wider than 390 px. Kanban columns run past
  the edge inside their own horizontal scroller, and one gantt day tick
  ends at 392 px inside the clipped track.
- Console: the only error is the 401 on the first page load before
  sign-in, plus the 500s the stubs asked for. No page errors.
- Network: each page logs several `GET /api/tasks … net::ERR_ABORTED`.
  These are the shell's conditional polls (`If-None-Match` with
  `cache: "no-store"`, `api.js`), which Chromium reports this way when
  the answer is a 304. Nothing failed.

## Bug found and fixed

**Kanban and list move slots above two same-titled tasks had the same
accessible name.**

- **Reproduction:** file two board requests (both default to the title
  "Add a board"; here they were both "WT rail board 3355", tasks 24 and
  25). On `/boards/board-1` press "Move task 2". The Waiting column
  offers "Move here, above WT rail board 3355, in Waiting" twice, and
  the two lead to different positions. An untitled task's slot read
  "Move here, above , in Waiting". The list view (`TaskList.jsx`) has the
  same shape: "Move here, above <title>".
- **Cause:** the slots were named by `t.title` alone, while the handle
  that picked the card up says "Move task <id>".
- **Fix:** `taskSlotName` (`ReorderPickup.jsx`) names the task by id and
  title, "task 25, “WT rail board 3355”", or "task 7" when it has no
  title. That is the shape PanelBoard already uses ("panel 2,
  “Inherit”"). `TaskBoard.jsx` and `TaskList.jsx` use it.
- **After, on 18821:** "Move here, above task 2, “Fix flaky retry test”,
  in Waiting". The slot button is 587–873 px inside its 578–882 px
  column at 1400 px wide, and 363–671 px inside 354–680 px on the phone
  (44 px tall). It is the same in light and dark, and the document stays
  1400 / 390 px wide. Only an `aria-label` changed, so nothing on screen
  moves. `make ui-screenshots PAGES=/boards/board-1,/boards` was taken
  (desktop and phone, light and dark) and is attached to the task, not
  committed.
- **Coverage:**
  - `TaskBoard.test.jsx` "names the card a slot sits above by its id,
    not by its title alone" (two "Add a board" cards and an untitled
    one), which fails without the fix.
  - `TaskBoard.test.jsx` and `TaskList.test.jsx` slot names updated.
  - e2e `saved-boards-walkthrough.spec.js` 3h: three same-titled tasks
    filed over the API and closed again in `finally`.

**Runs:**

- `vitest` (full suite): 4022 tests passed in 257 files.
- Playwright (chromium) `saved-boards-walkthrough`, `board`, `boards`,
  `boards_workflows`, `gantt`, `report-backlog-links` and `work-tree`:
  68 passed against a bundle built with the fix.

## Observations (not fixed here)

- **A list board's New board prompt does not say where a dashboard's
  panels live.** It offers `panels` as a kind, and its closing paragraph
  names `tables/board.json`, `board_column.json` and
  `board_section.json` but not `board_panel.json`. So a request someone
  rewrites to "make it a dashboard" tells the agent to write only the
  board row, which draws an empty dashboard. A prompt that opens as
  `kind: panels` does carry the panel note. Leaving it out of the other
  prompts is deliberate: `boards.test.js` asserts "says nothing about
  panels in a prompt for a board that has none". So this was left alone
  rather than overriding that decision here.
- **The rail lists no boards of its own any more.** Only pinned boards
  appear (Customize sidebar → Board). `BoardsPage.jsx`'s comment still
  says "the rail is drag-only", and the task text still asks for boards
  "from the rail". The index handles every move.
- Still true from earlier runs: the report window picker replaces the
  history entry, so Back after changing the window leaves the board.
  The timeline draws dependencies as the blocked state on the bar rather
  than as connector lines. Lanes offer "by author" but not "by owner".
  A board requested from `/boards` says `query: <see below>`.
