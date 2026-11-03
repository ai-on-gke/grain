# Saved boards and task visualizations walkthrough — 2026-11-03

## Commit and environment

- **Commit tested:** `0fdeff3b` (`main`, "Merge pull request #540"), on
  branch `grain/049f1e09/task-2699-walk-through-saved-boards-and`. The
  fix below (`fix(ui): order the timeline's Asked lane after Running`)
  was built on top of it and checked again in the browser.
- **Date:** 2026-11-03, 03:00–03:25 UTC.
- **Server:** the real `grain demo`, built with `go build ./cmd/grain`
  (go1.26) after `npm ci && npm run build` in `ui/` (the production Vite
  bundle, embedded from `pkg/ui/static`). Each server ran over its own
  throwaway embedded store with `-open=false -access-code-file <file>`:
  - `127.0.0.1:18601`: plain `grain demo`, for every step below and the
    before screenshots;
  - `127.0.0.1:18602`: `-observability -github-sim -merge-queue-states
    -review-questions`, to see whether any demo seed fills the quieter
    report sections (none does; see §8);
  - `127.0.0.1:18603`: plain `grain demo` rebuilt with the fix, for the
    after screenshots and §9.
- **Browser:** Playwright 1.62.1 Chromium (`/var/cache/playwright`),
  driven by ad-hoc Node scripts (not committed) through visible controls.
  Sign-in went through the "Access code" page. Desktop was 1440×1000.
  Phone was 390×844 (`iPhone 13` descriptor, `isMobile`, `hasTouch`).
  Every script recorded page errors, console errors, HTTP ≥ 400
  responses, failed requests and every non-GET `/api` call.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so no
  Gemini-backed tooling ran and `LOOK=1` was not used. The file reader
  showed PNGs as bytes, so the screenshots were checked by bounding boxes
  (§5, bug 1).

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`.** The demo seeds no held
   proposal; only an agent's `propose_task` with `hold_for_approval` files
   one. Task 22 was filed with what that call sends (`proposedBy: "8"`,
   `holdForApproval: true`, a `proposalKey`).
2. **Stand-in boards, created over `POST /api/boards`.** The product's own
   creation path files a task against `grain/config` for an agent, and
   `grain demo` runs no agent. That contract was exercised first (§2).
   The stand-ins:
   - `WT List` (board-5, `list`, `f=state:any:running`, folder `WT2699`)
   - `WT Kanban` (board-6, `kanban`, `f=repo:any:acme/widgets`, own
     columns Todo (`proposed`,`queued`) / Doing (`running`) / Talking
     (`awaiting_reply`), folder `WT2699`)
   - `WT Kanban Submit` (board-7, `kanban`, own columns Sign off
     (`awaiting_submit`) / Doing (`running`), folder `WT2699`)
   - `WT Gantt` (board-8, `gantt`, `q=retry`, folder `Ops`)
   - `WT Dash` (board-9, `panels`, `f=repo:any:acme/widgets`, folder
     `Ops`; panels `states`, an inheriting `list` "Inherit", a `list`
     "Running only" with its own query at `third` width, `metrics` 30d,
     `gantt` 24h at `full` width, and a `list` "Nothing" over
     `q=zzzznothing`)
   - `WT Tree` (board-10, `tree`, `q=pagination`, folder `Ops`)
   - `WT Report All` (board-11, `report`, every one of the 13 section
     kinds in `model.ReportSections`)
   - `WT Report Widgets` (board-12, `report`, `f=repo:any:acme/widgets`;
     sections backlog titled "Waiting work", throughput, tools, agents)
   - `WT Empty Dash` (board-13, `panels`, no panels)

   Every stand-in was created with `orderKey` 0, so the boards in a
   folder start out tied, which is what §1's drags then had to separate.
3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*` passed through to the real server, then had
     `tools`, `checks`, `sandboxes`, `agents` and `merges` filled with
     the shapes `ReportBoard.sections.test.jsx` uses. The demo's runs
     record no tool census, sandbox lifetimes, CI waits, landings or
     agent telemetry in any window (7d and 90d, on both servers), so this
     is the only way those sections draw in a browser.
   - `**/api/metrics*` delayed 1.5 s, then `500 {"error":"synthetic
     metrics 500"}`, for the report's loading and error states.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- Rail `boards 4` opened `/boards`. The four seeded boards were in order,
  with kind chips and descriptions: Board/kanban, Timeline/timeline,
  Report/report, Tree/tree. The index has no search or sort control (0
  inputs, 0 selects). The header carries `new board`.
- With the stand-ins: unfiled boards first (the four seeded, then WT
  Report All, WT Report Widgets, WT Empty Dash), then `Ops` (WT Gantt, WT
  Dash, WT Tree), then `WT2699` (WT List, WT Kanban, WT Kanban Submit).
- **Dragging in a section below the first** (`WT2699`, the third):
  - `Move WT Kanban Submit` dropped on `WT List` sent `PATCH
    /api/boards/board-7 {"folder":"WT2699","orderKey":-1048576}`. Order
    became Kanban Submit, List, Kanban, and stayed so after a reload.
  - `Move WT Kanban` dropped on `WT List` (between −1048576 and 0) sent
    `orderKey -524288`. Order became Kanban Submit, Kanban, List, and
    stayed so after a reload.
  - In `Ops`, where all three boards were tied at 0, `Move WT Tree`
    dropped on `WT Dash` sent `PATCH board-10 {"orderKey":1048576}` and
    `PATCH board-9 {"orderKey":2097152}`. It renumbered the tie rather
    than writing a midpoint that equals both neighbours. Order became
    Gantt, Tree, Dash, and stayed so after a reload.
- Keyboard: `Move WT Gantt` → Enter offered 14 slots, including `outside
  every folder`, `at the end of Ops` and `at the end of WT2699`. The slot
  directly below the board itself was left out.
- Tab order from `new board`: alerts → System → Settings → `Move Board` →
  `Board` → `Move Timeline` → `Timeline` → `Move Report`, one handle and
  one link per board.
- History: `WT List` → row → Escape → Back → Back → Forward → reload
  landed on `/boards/board-5?f=state:any:running`, `/tasks/3`,
  `/boards/board-5?…`, `/tasks/3`, `/boards/board-5?…`, `/tasks/3`,
  `/tasks/3`. Escape from a task pushes the board as a new entry.
- `/boards/nope` fell back to `/boards` with the alert `no board "nope"`.

### 2. Asynchronous board creation

- From `/?q=retry&f=repo:any:acme/widgets`, `Create options` offered
  `new task / new session / new board`. `new board` opened `/new` with
  the title "Add a board", repo `grain/config`, and a description carrying
  ``- query: `q=retry&f=repo:any:acme/widgets` `` plus every kind and the
  query encoding. `Create task` filed task 20 (`queued`, `grain/config`)
  and returned to the list it came from, with its narrowing intact.
- The `/boards` header `new board` gave `- query: <see below>` and filed
  task 21 (`queued`, `grain/config`). It returned to `/boards`.
- `/api/boards` held 4 boards before and after both requests. Nothing was
  created directly, so the asynchronous contract holds. On the fixed
  server the same flow sent `POST /api/tasks` (task 20) and again created
  no board.
- `Add panel` on `WT Dash` opened `/new` with "Add a panel to grain's
  saved board `board-9` ("WT Dash")…", listing its panels in drawn order
  with their widths, queries and `order_key`s. Back returned to the board
  with its query.

### 3. List

- `WT List` showed tasks 3 and 8 under one filter. A row click opened
  `/tasks/3`, and Escape returned to `/boards/board-5?f=state:any:running`.

### 4. Kanban

- Seeded `Board`: proposed 3 (1, imported 12, held 22), waiting 4 (2, 11,
  20, 21), running 2 (3, 8), asked 9 (4, 6, 7, 14–19), failed 0,
  **Merging** 1 (task 5). The note read "2 tasks in no column (Deferred,
  Closed)."
- Card markers:
  - task 11: `badge badge-icon badge-blocked`, labelled `Blocked on: #8
    (running)`;
  - task 22: chip **"held for approval by 8"** (`chip-held`);
  - task 12: chip **"imported"** (`chip-imported`, titled "imported from
    GitHub issue acme/widgets#42");
  - the asked cards: `badge-awaiting_reply` "Asked a question" on 4 and
    7, and `badge-awaiting_submit` "Asked you to submit …" on the rest.
- Custom columns:
  - `WT Kanban` (collects `awaiting_reply` only): "4 tasks in no column
    (Asked to submit, Merging, Deferred, Closed)."
  - `WT Kanban Submit` (collects `awaiting_submit` only): "12 tasks in no
    column (Proposed, Waiting, Asked for a reply, Merging, Deferred,
    Closed)."
  - Both name the asked state they leave out, and neither has an `edit
    columns` control.
- Card moves within Waiting:
  - Keyboard: `Move task 21` → Enter offered 3 slots ("…, in Waiting").
    The first sent `POST /api/tasks/reorder {"ids":["21"],"beforeId":"2"}`,
    and focus stayed on `Move task 21`.
  - Mouse: dragging 20 onto 21 sent `{"ids":["20"],"beforeId":"21"}`.
  - Escape on a pending move (`Move task 2`) cleared every slot and kept
    focus on the handle.
  - Order after a reload: 20, 21, 2, 11.
  - Phone: tapping `Move task 11` showed "Moving 1 task — choose where it
    goes" with 3 slots. Tapping the first sent `{"ids":["11"],
    "beforeId":"20"}`. After a reload the order was 11, 20, 21, 2.
- A card's title link opened `/tasks/1`, and Escape returned to
  `/boards/board-1`.
- `edit columns`: every icon button is named (`Move <col> left/right`,
  `Remove <col>`). The ends are disabled (`Move Proposed left`, `Move
  Merging right`).
  - Enter on `Move Merging left` kept focus on it.
  - Renaming Waiting to "Up next" and pressing Escape raised the confirm
    "You have unsaved changes here. Leave and discard them?". Dismissing
    it kept the dialog open.
  - Saved, the board drew proposed / up next / running / asked / merging
    / failed. It was the same after a reload.
  - Edge cases: saving with a blank name, or after removing every column
    ("No columns. Saving now puts the default…"), fell back to the
    default columns. `Reset to default` cleared
    `localStorage['grain.board.columns']`.

### 5. Timeline / gantt

- Range offers last 24 hours / 7 / 30 / 90 days / everything. Lanes
  offers no lanes / by repo / by state / by author.
- The default (7 days) drew 20 bars and "1 task outside this range.", with
  one `.gantt-now` at `left: 100%`.
- 24 hours + by repo drew acme/widgets 11, grain/config 2 and No repo 5,
  with "3 tasks outside this range.". This survived a reload
  (`localStorage['grain.gantt.view'] = {"range":"24h","lanes":"repo"}`),
  and the URL stays bare.
- Task 11's row reads "Blocked on: #8 (running)" and its bar reads
  "Waiting, blocked: waiting for 45m, never dispatched". The dependency
  is drawn as that blocked state; no arrows are drawn.
- A bar click opened `/tasks/1`. Escape returned with focus on that row's
  title link. The bars themselves are not tab stops; each row's title
  link is.
- By author: debian 18, director 1 (11), grain 1 (the held 22),
  issue-import 1 (the imported 12).
- `WT Gantt` cold-opened on `?q=retry` with 7 bars and kept it through a
  reload.
- **By state + everything drew the lanes Proposed, Waiting, Running,
  Merging, Deferred, Closed, Asked.** That is bug 1: the shared Asked
  lane sat below Closed. With the fix it draws Proposed, Waiting,
  Running, Asked, Merging, Deferred, Closed.

### 6. Tree

- Seeded `Tree`: 13 (Director Session) → 1, 3, 8; 8 → 11 and the held 22,
  which task 8 filed. The imported 12 and the UI-filed 20 and 21 are
  roots.
- `WT Tree` (`?q=pagination`) showed 8 → 11, and the same after a reload.
  Clicking the title text of 11 opened `/tasks/11`. Escape returned to
  `/boards/board-10?q=pagination` with focus on the row's `task-tree-id`
  link.
- `/boards/board-4?f=state:any:running|queued` showed 21, 20, 13 → 3, 8
  → 11, and 2, under the chip "state is any of running, waiting". It was
  the same after a reload.

### 7. Dashboard (`WT Dash`)

- Panels: State counts 13; Inherit 13 ("Showing 8 of 13."); Running only
  2; Throughput; Timeline 13 ("Showing 10 of 11 in this range. 2 tasks
  outside this range."); Nothing 0 ("Nothing here right now.").
- **State tiles link to lists of the same size:** proposed 3, waiting 2,
  running 2, asked 3 (`awaiting_reply|awaiting_submit`), failed 0,
  merging 1 (`completed`). Each `Open` goes to the panel's own question
  (`/?f=state:any:running`, `/?q=zzzznothing`, and
  `/system/metrics?f=repo:any:acme/widgets&f=created:within:30` for the
  metrics panel). The first `Open` drew 13 rows, and Back returned to the
  board.
- Search `pagination` wrote `?q=pagination&f=repo:any:acme/widgets`.
  State counts, Inherit and Timeline went to 2 and showed `keep`; the
  own-query panels did not change. The narrowing survived a reload, and
  clearing the search returned to `?f=repo:any:acme/widgets`.
- Reorder:
  - Keyboard: `Move panel 6, “Nothing”` → the first slot sent `PATCH
    /api/boards/board-9 {"panels":[…]}`. Focus moved to `Move panel 1,
    “Nothing”`.
  - Mouse: dragging panel 3's handle onto panel 1 put Inherit first.
  - After a reload: Inherit / Nothing / State counts / Running only /
    Throughput / Timeline.
- `WT Empty Dash`: "This dashboard has no panels yet. “Add panel” files
  the task that adds one."

### 8. Report

- Seeded `Report` (no sections named) drew throughput, latency, capacity,
  backlog and GitHub CI, and **omitted every silent section** (tools,
  prompts, CI waits, CI queueing, scheduling, sandboxes, merge queue,
  agents). The Metrics pane drew the same set.
- `WT Report All` names all 13. Unstubbed, it accounts for each silent
  one in a line ("Tool use: nothing to report over this window.", and the
  same for Prompts, CI waits, CI queueing, Scheduling, Sandboxes, Merge
  queue and Agents). This was the same over 7d and 90d on both servers.
- With the metrics stub (§Stubs 3), at `?window=30d`, these drew their
  real components:
  - Tool use: "…over the 2 attempts that recorded it";
  - CI waits: "4 waits across…";
  - Sandboxes: "Since this deployment started counting them…";
  - Landings & Lead Time: 3 landed, 2m 0s filed→landed median;
  - Agents: "Report checks: 3 wake(s), 10 claim(s) — uncited 1 (10.0%),
    …".
  
  Prompts, CI queueing and Scheduling stayed silent. Refresh issued one
  more `/api/metrics` request. There were no page or console errors.
- `WT Report Widgets`: "Measured over the tasks in acme/widgets."
  Sections: Waiting work, Throughput, Tool use, Agents.
  - Keyboard: `Move section 4` → first slot made Agents first. Focus
    moved to `Move section 1, “Agents”`. `PATCH` sent `orderKey
    -1048575`.
  - Mouse: section 3 dragged onto section 1 made Throughput first.
  - After a reload: Throughput, Agents, Waiting work, Tool use.
- Window offers 24 hours / 7 / 30 / 90 days. 90 days wrote
  `?f=repo:any:acme/widgets&window=90d`, drew the span `… · 90d 0h`, and
  survived a reload and Back/Forward. The pick replaces the history entry
  rather than adding one. `?window=bogus` fell back to "Last 7 days" and
  dropped the parameter.
- `?q=retry&f=repo:any:acme/widgets` printed "A report is measured over
  whole distributions, so the rest of this question is not in force: the
  search for "retry"."
- **Backlog chips link to lists of the same size** (2026-11-02's bug 2,
  fixed by #491, holds):
  - `WT Report Widgets`: proposed 3 → 3 rows, waiting 2 → 2, running
    2 → 2, and asked 1 → 1, via
    `f=state:any:awaiting_reply&f=completed:empty:`.
  - Seeded `Report`: proposed 3, waiting 4, running 2 and asked 1, each
    matching its list.
  - "Oldest waiting task: task 2" links to `/tasks/2`.
- Metrics stubbed to 500: a progressbar, then the alert "The report could
  not be loaded (synthetic metrics 500). Refresh to try again."

### 9. Destructive actions

- No board page and no part of `/boards` has a delete or remove control.
  This was checked on `/boards` and on boards 1–13. Boards are removed
  through `grain/config`. The column editor's `Remove <col>` acts only on
  this browser's draft, behind the unsaved-changes confirm.
- On the fixed server, a throwaway "Add a board" task (20) filed from
  `/boards` was closed with its page's `Close` button (`POST
  /api/tasks/20/close {"close_pull_request":false}`). It went to
  `closed` with no confirmation, which fits Close being reversible (the
  page then offered `Reopen`). The stores were discarded.

### 10. Phone, console and network

- At 390×844, `/boards` and all 13 board pages had `scrollWidth ==
  innerWidth`.
- No uncaught page errors. The only errors were expected:
  - the pre-sign-in `401` on `/`;
  - `404 /api/repos/acme/widgets/github-branches` when a task overlay
    opens without `-github-sim`;
  - `ERR_ABORTED` `/api/tasks` polls cut off by navigation;
  - the synthetic 500s.

## Bugs found

1. **A timeline grouped by state drew the Asked lane last, below
   Closed.** (Fixed here.)
   - Reproduction: `/boards/board-2`, range "everything", lanes "by
     state" (or `localStorage['grain.gantt.view'] =
     {"range":"all","lanes":"state"}`). The lanes read Proposed, Waiting,
     Running, Merging, Deferred, Closed, Asked. The rail and the kanban
     both put Asked straight after Running.
   - Cause: `laneOf` keys the lane both asked states share by its
     display state, `asked` (`state.js` `displayState`). But
     `groupIntoLanes` ranked lanes by `STATE_ORDER.indexOf(key)`, and
     `STATE_ORDER` only names model states, so `asked` ranked past the
     end.
   - Fix (`ui/src/gantt.js`): rank state lanes by `DISPLAY_STATE_ORDER`,
     which folds both asked states into `asked` where `awaiting_reply`
     stands.
   - After, at 1440×1000 and 390×844 in light and dark (over `grain
     demo`): Proposed, Waiting, Running, Asked, Merging, Deferred, Closed.
     Every lane header is 33 px high, the tops strictly increase with no
     overlap, no title is truncated (`scrollWidth <= clientWidth`), and
     the page does not overflow horizontally. Before, the same pages drew
     Asked at the bottom.
   - Coverage:
     - `gantt.test.js` "puts the shared Asked lane after Running, not
       after Closed" (it fails without the fix);
     - e2e `saved-boards-walkthrough.spec.js` test 5. It asserts that
       Asked directly follows Running, that Closed is last, and that
       Asked's header is above Closed's. It failed against a bundle built
       without the fix ("Expected: 3, Received: 6").
   - Runs: `vitest` passed 3684 in 235 files. Playwright
     `saved-boards-walkthrough`, `board` and `boards` specs passed 46.

## Observations (not bugs)

- A report board that names its sections prints "<Section>: nothing to
  report over this window." for each silent one, while the unsectioned
  report and the Metrics pane leave them out. This is deliberate
  (`ReportBoard.sections.test.jsx`: a named section is accounted for).
- On a dashboard, a search still shows `keep` on the metrics panel, which
  cannot apply a text search (as on 2026-11-02).
- Picking a report window replaces the history entry, so Back skips
  earlier windows. This matches how a narrowing change behaves.
- Sorting a list by state uses `STATE_ORDER`, which puts `failed` between
  the two asked states. The demo seeds no failed task, so this could not
  be seen here. It is noted for whoever next has a failed task beside
  both kinds of asked task.
- Still true from earlier runs: a tree row opens from a click anywhere on
  it, but only its number is a link (`a.task-tree-id`).
