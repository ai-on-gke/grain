# Saved boards and task visualizations walkthrough — 2026-11-02

## Commit and environment

- **Commit tested:** `aaa266ef` (`main`, "Merge pull request #462"), on
  branch `grain/049f1e09/task-2401-walk-through-saved-boards-and`. The fix
  below was built on top and checked again in the browser.
- **Date:** 2026-11-02, 08:50–09:20 UTC.
- **Server:** the real `grain demo`, built with `go build ./cmd/grain`
  (go1.26) after `npm ci && npm run build` in `ui/` (the production Vite
  bundle, embedded from `pkg/ui/static`). It ran over its own throwaway
  embedded store with `-addr 127.0.0.1:18501 -open=false
  -access-code-file <file>`. It was restarted with a fresh store for each
  rebuilt bundle. For the before screenshots, a second server ran the
  unfixed base (`aaa266ef`, built from a worktree) on `127.0.0.1:18502`.
- **Browser:** Playwright 1.62.1 Chromium (`/var/cache/playwright`),
  driven by ad-hoc Node scripts (not committed) through visible controls.
  Sign-in went through the "Access code" page. Desktop was 1440×1000.
  Phone was 390×844 (`iPhone 13` descriptor, `isMobile`, `hasTouch`).
  Every script recorded page errors, console errors, HTTP ≥ 400
  responses, failed requests and every non-GET `/api` call.
- **Gemini key:** none. `/home/debian/.gemini-api-key` is absent, so no
  Gemini-backed tooling ran and `LOOK=1` was not used.

## Stubs and stand-ins

1. **Held proposal, filed over `POST /api/tasks`.** The demo seeds no held
   proposal; only an agent's `propose_task` with `hold_for_approval` files
   one. Task 22 was filed with what that call sends (`proposedBy: "8"`,
   `holdForApproval: true`, a `proposalKey`). It was closed from its page
   at the end.
2. **Stand-in boards, created over `POST /api/boards`.** The product's own
   creation path files a task against `grain/config` for an agent, and
   `grain demo` runs no agent. That contract was exercised first (§2).
   The stand-ins:
   - `WT List` (board-5, `list`, `f=state:any:running`, folder `WT2401`)
   - `WT Gantt` (board-6, `gantt`, `q=retry`, folder `Ops`)
   - `WT Dash` (board-7, `panels`, `f=repo:any:acme/widgets`, folder
     `Ops`; panels `states`, an inheriting `list` "Inherit", a `list`
     "Running only" with its own query at `third` width, `metrics` 30d,
     `gantt` 24h at `full` width, and a `list` "Nothing" over
     `q=zzzznothing`)
   - `WT Tree` (board-8, `tree`, `q=pagination`)
   - `WT Empty Dash` (board-9, `panels`, no panels)
   - `WT Report` (board-11, `report`, `f=repo:any:acme/widgets`; sections
     throughput, backlog titled "Waiting work", prompts, merges)
   - `WT Tree Running` (board-12, `tree`, `f=state:any:running|waiting`;
     the query that found the bug below)
   - `WT Kanban` (board-13, `kanban`, own columns Todo
     (`proposed`,`queued`) / Doing / Talking (`awaiting_reply`),
     `f=repo:any:acme/widgets`, folder `WT2401`)

   A first `WT Kanban` naming the state `waiting` was refused, correctly
   (`400 column "Todo" names unknown state "waiting"`). It still used up
   the id `board-10`, which is harmless. The same word in a board's
   *query* was accepted, and that is the bug below.
3. **Playwright route stubs, removed after each check:**
   - `**/api/metrics*` delayed 1.5–2 s, then `500 {"error":"synthetic
     metrics 500"}`, for the dashboard's and the report's loading and
     error states.
   - `GET **/api/tasks` → `500 {"error":"synthetic tasks 500"}`, for how
     the boards render when the task list cannot load.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- Rail `boards 4` opened `/boards`. The four seeded boards were in order,
  with kind chips and descriptions: Board/kanban, Timeline/timeline,
  Report/report, Tree/tree. The index has no search or sort control (0
  inputs, 0 selects).
- With the stand-ins (rail 12), unfiled boards come first, then `Ops`
  (WT Gantt, WT Dash), then `WT2401` (WT List, WT Kanban).
- History: `WT Dash` → `/boards` → `WT Tree`, then Back, Back, Forward
  and reload, landed on `/boards/board-7?f=repo:any:acme/widgets`,
  `/boards`, `/boards/board-8?q=pagination`, `/boards`,
  `/boards/board-7?…`, `/boards`, `/boards`.
- `/boards/nope` fell back to `/boards` with the alert `no board "nope"`.
- Keyboard move: `Move WT Tree` → Enter offered 13 slots, including
  `outside every folder`, `at the end of Ops` and `at the end of WT2401`.
  The slot directly below the board was omitted, since it would be a
  no-op. `at the end of Ops` sent `PATCH /api/boards/board-8
  {"folder":"Ops","orderKey":1048576}`. Focus stayed on `Move WT Tree`,
  and the move survived a reload.
- Mouse: dragging `Move WT Kanban` onto the `Ops` heading sent `PATCH
  /api/boards/board-13 {"folder":"Ops","orderKey":-1048576}`. It survived
  a reload, with no drop zone left behind.
- Tab order from the header: `new board` → alerts → System → Settings →
  `Move Board` → `Board` → `Move Timeline` → …, one handle and one link
  per board.

### 2. Asynchronous board creation

- From `/?q=pagination&f=repo:any:acme/widgets`, `Create options` offered
  `new task / new session / new board`. `new board` opened `/new` with
  the title "Add a board", repo `grain/config`, and a description carrying
  the query `q=pagination&f=repo:any:acme/widgets`, every kind, the query
  encoding and the `tables/board.json` instructions. `Create task` sent
  `POST /api/tasks` and filed task 20 (`queued`, `grain/config`). It
  returned to the list it came from.
- The `/boards` header `New board` gave `query: <see below>` and filed
  task 21 (`queued`, `grain/config`). It returned to `/boards`.
- `/api/boards` held 4 boards before and after. Nothing was created
  directly, so the asynchronous contract holds. Task 20's page shows the
  filed prompt.
- `Edit board` on `WT Kanban` opened `Change the board "WT Kanban"`,
  listing its fields and its columns with their states. `Add panel` on
  `WT Dash` opened `Add a panel to the dashboard "WT Dash"`. Back
  returned to the board with its query.

### 3. List

- `WT List` showed the chip `state is any of running` and tasks 3 and 8.
  A row click opened `/tasks/3`, and Escape returned to
  `/boards/board-5?f=state:any:running`.

### 4. Kanban

- Seeded `Board`: proposed 3 (1, 12, held 22), waiting 4 (2, 11, 20, 21),
  running 2, asked 9, failed 0, **merging** 1 (task 5, whose handle is
  disabled). The note read "2 tasks in no column (Deferred, Closed)."
- Task 11 has `badge badge-icon badge-blocked`, labelled `Blocked on: #8
  (running)`. The held task 22's card carries the chip **"held for
  approval by 8"**.
- A card title opened `/tasks/1`, and Escape returned to
  `/boards/board-1`.
- `edit columns`: every icon button is named. The disabled ends are `Move
  Proposed left` and `Move Merging right`.
  - Enter on `Move Merging left` kept focus on it.
  - Renaming Waiting to "Up next" and pressing Escape raised the confirm
    "You have unsaved changes here. Leave and discard them?". Dismissing
    it kept the dialog open.
  - `+ add column` "Later" with state `deferred` (the listbox's own Escape
    did not close the dialog).
  - Saved, the board drew proposed 3 / up next 4 / running 2 / asked 9 /
    merging 1 / failed 0 / later 1, with "1 task in no column (Closed).".
    It was the same after a reload (`localStorage['grain.board.columns']`).
- Card moves within Waiting:
  - Keyboard: `Move task 21` → Enter offered 3 slots. The first sent
    `POST /api/tasks/reorder {"ids":["21"],"beforeId":"2"}`, and focus
    stayed on `Move task 21`.
  - Mouse: dragging 20 onto the first card sent `{"ids":["20"],
    "beforeId":"21"}`.
  - Dragging 11 into Running sent nothing, by design.
  - Order after a reload: 20, 21, 2, 11.
  - Phone: tapping `Move task 11` showed "Moving 1 task — choose where it
    goes" with 3 slots. Tapping the first sent `{"ids":["11"],
    "beforeId":"20"}`, which persisted. The page had no horizontal
    overflow.
- `WT Kanban`: todo 5, doing 2, talking 2, and "4 tasks in no column
  (Asked to submit, Merging, Deferred, Closed)." (2026-11-01's fix
  holds). It has no `edit columns` control. Slot labels name the board's
  own column ("…, in Todo"). Escape cancelled a pending move and kept
  focus on the handle.

### 5. Timeline / gantt

- Range offers last 24 hours / 7 / 30 / 90 days / everything. Lanes
  offers no lanes / by repo / by state / by author.
- 24 hours + by repo drew lanes acme/widgets 11, grain/config 2 and no
  repo 5, with "3 tasks outside this range.". It drew one `.gantt-now` at
  `left: 100%`.
- Task 11's bar reads "Waiting, blocked: waiting for 46m, never
  dispatched". The blocked state is the timeline's dependency rendering;
  it draws no arrows.
- Range and lanes survived a reload (`localStorage['grain.gantt.view'] =
  {"range":"24h","lanes":"repo"}`). The URL stays bare, as the board
  prompt says.
- A bar click opened `/tasks/1`. Escape returned with focus on that row's
  title link.
- Search `retry` wrote `?q=retry` (6 bars) and survived a reload.
  "everything" drew 7. By author drew one lane, `debian`. `WT Gantt`
  cold-opened on `?q=retry`.

### 6. Tree

- Seeded `Tree`: 13 (Director Session) → 1, 3, 8; 8 → 11 and the held 22,
  which task 8 filed. The tasks filed from the UI (20, 21) are roots.
- `WT Tree` (`?q=pagination`) showed 8 → 11, 22, and the same after a
  reload. Clicking the title text of 11 opened `/tasks/11`. Escape
  returned to `/boards/board-8?q=pagination` with focus on the row link.
  Search `helper` showed 11 alone.
- `/boards/board-4?f=state:any:running` showed 8 and 3, and the same
  after a reload.
- `WT Tree Running` (`f=state:any:running|waiting`) showed only 8 and 3,
  under the chip "state is any of running, waiting". That is **bug 1**.

### 7. Dashboard (`WT Dash`)

- Panels:
  - state counts 13 (3/2/2/3/0/1, "2 in no column");
  - Inherit 13 ("Showing 8 of 13.");
  - Running only 2;
  - Throughput 14 filed / 3 completed;
  - Timeline 13 ("Showing 10 of 11 in this range. 2 tasks outside this
    range.");
  - Nothing 0 ("Nothing here right now.").
- Links:
  - State tiles go to `/?f=repo:any:acme/widgets&f=state:any:<state>`,
    with asked → `awaiting_reply|awaiting_submit` and merging →
    `completed`.
  - Each `Open` goes to the panel's own question (`/?f=state:any:running`,
    `/?q=zzzznothing`, and `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`
    for the metrics panel).
- Search `pagination` wrote `?q=pagination&f=repo:any:acme/widgets`.
  Inherit went to 3 (11, 8, held 22) and state counts to 3, and the
  timeline went to 3. The own-query panels did not change. `keep` showed
  on the 4 inheriting panels. The narrowing survived a reload, and
  clearing the search returned to the board's own query.
- A row opened `/tasks/1`, and Escape returned to the board's URL.
- Reorder:
  - Keyboard: `Move panel 6, “Nothing”` → `Move here, above panel 1`
    sent `PATCH /api/boards/board-7 {"panels":[…]}` (Nothing at
    `orderKey -1048576`). Focus moved to `Move panel 1, “Nothing”`.
  - Mouse: dragging panel 3's handle onto panel 1 put Inherit first.
  - After a reload: Inherit / Nothing / State counts / Running only /
    Throughput / Timeline, matching the stored `orderKey`s.
- Metrics stubbed: Throughput read "Loading…", then "The metrics report
  could not be loaded." The other panels drew normally.
- Tasks stubbed to 500: an app-wide alert "synthetic tasks 500" showed,
  and every task panel read "Nothing here right now." The kanban, tree and
  timeline boards read the same way, as the main task list does
  ("No tasks in this state."). See the observations.
- `WT Empty Dash`: "This dashboard has no panels yet. “Add panel” files
  the task that adds one."

### 8. Report (`WT Report`)

- "Measured over the tasks in acme/widgets.", with the span `… · 7d 0h`.
  Sections: Throughput (13 filed, 3 completed, sparklines), Waiting work
  (the backlog), "Prompts: nothing to report over this window.", and
  "Merge queue: nothing to report over this window."
- Keyboard: `Move section 4` → first slot. Focus moved to `Move section
  1, “Merge queue”`. Mouse: section 3 onto section 1 put "Waiting work"
  first. Both persisted across a reload.
- Window offers 24 hours / 7 / 30 / 90 days. 30 days wrote `window=30d`,
  and it survived a reload.
- Search `ignored` printed "A report is measured over whole
  distributions, so the rest of this question is not in force: the
  search for "ignored"."
- Links into the data: the backlog chips are now `<a>`s
  (`/?f=repo:any:acme/widgets&f=state:any:<state>`), and "Oldest waiting
  task: task 2" is a link to `/tasks/2`. 2026-11-01's observation is
  fixed. Escape and Back both returned to the report with its window.
  The chip counts do not match their lists (**bug 2**, filed).
- Metrics stubbed: a progressbar, then the alert "The report could not
  be loaded (synthetic metrics 500). Refresh to try again."
- The seeded `Report` drew throughput, latency, capacity, backlog and
  GitHub CI.

### 9. Destructive actions

- No board page and no part of `/boards` has a delete or remove control.
  This was checked on `/boards` and on boards 1–9, 11, 12 and 13 at
  phone width. Boards are removed through `grain/config`.
- The throwaway tasks 20, 21 and 22 were closed with each page's `close`
  button (`POST /api/tasks/<id>/close {"close_pull_request":false}`).
  Each went to `closed` with no confirmation, which fits Close being
  reversible (each page then offered `reopen`).
- The stores were discarded.

### 10. Phone, console and network

- At 390×844, `/boards` and all 12 board pages had `scrollWidth ==
  innerWidth`.
- No uncaught page errors. The only errors were expected:
  - the pre-sign-in `401` on `/`;
  - `404 /api/repos/acme/widgets/github-branches` when a task overlay
    opens without `-github-sim`;
  - `ERR_ABORTED` `/api/tasks` polls cut off by navigation;
  - the synthetic 500s.

## Bugs found

1. **A board query naming a state that does not exist read exactly like
   a real one.** (Fixed here.)
   - Reproduction: a board (or a link) with `f=state:any:running|waiting`.
     `waiting` is the *label* of the state `queued`; the state itself has
     no such name. The chip read "state is any of running, waiting", word
     for word what `f=state:any:running|queued` draws, but the board
     showed only the running tasks. `/?f=state:any:queued` listed 2 tasks
     under "state is any of waiting", and `/?f=state:any:waiting` listed
     none under the same words. The server refuses `waiting` in a column
     but takes it in a query, which is opaque to it. An agent writing a
     board from the board prompt can produce exactly this.
   - Fix (`ui/src/taskFilters.js` `unknownValues`/`describeCondition`,
     `ui/src/components/NarrowingBar.jsx`, `ui/src/taskAttributes.js`):
     - An attribute marked `closed` (State: its `order` is every state)
       now writes a value outside that vocabulary as `“waiting”
       (unknown)`.
     - The chip is drawn in the warning outline that unknown attributes
       already use, titled "“waiting” is not a value State has, so it
       matches no task".
     - Values are read through `expand`, so `asked` stays known.
     - Custom enum fields are not checked, because they can carry legacy
       values that still match tasks. Neither is `origin`, whose `order`
       leaves out `imported`.
   - After, at 1440×900 and 390×844 in light and dark (over `grain demo`,
     `/boards/board-4?f=state:any:running|waiting`): the chip is one line,
     24 px high and 279 px wide, its label untruncated (`scrollWidth ==
     clientWidth`). The outline is `rgba(168,98,28,.7)` in light and
     `rgba(226,154,78,.7)` in dark, and the page does not overflow. Before,
     it was 207 px and plain.
   - Coverage:
     - `taskFilters.test.js` "marks a value its attribute does not have,
       which matches nothing" (it fails without the fix);
     - e2e `saved-boards-walkthrough.spec.js` 5d.
   - Runs: `vitest` passed 3640. Playwright `saved-boards-walkthrough`,
     `board`, `boards`, `work-tree` and `processes-narrowing` specs passed
     56.
2. **A report's backlog chip counts do not match the lists they link to.**
   (Filed as a follow-up.)
   - Reproduction: `WT Report` reads `waiting 3 · asked 1`, but `waiting
     3` opens a list of 2 (11 and 2) and `asked 1` opens a list of 3 (4,
     6 and 7).
   - Cause: `pkg/metrics` `backlogOf` counts the queued Director Session
     (task 13), and the task list hides sessions. It also leaves out
     `awaiting_submit` and answered `awaiting_reply` tasks, while the
     link (`stateNarrowing`) asks for both asked states.
   - Fixing it changes either the metrics semantics or the link
     vocabulary, which is separate from this change, so it is proposed
     as its own task.

## Observations (not bugs)

- When `/api/tasks` fails, every board says it is empty ("Nothing here
  right now.", "No tasks yet."), under an app-wide alert carrying the
  error. The main task list does the same, so this is the app's pattern
  rather than a board's.
- On a dashboard, a search shows `keep` on the metrics panel too, which
  cannot apply a text search. Its figures stay put without saying why,
  whereas a report board says the search is "not in force".
- "Attempts per completion 0.00" on the demo's report is accurate for the
  data: the demo's completed tasks carry no run rows.
- Still true from earlier runs: a tree row opens from a click anywhere on
  it, but only its number is a link (`a.task-tree-id`), so only the
  number can be opened in a new tab.
