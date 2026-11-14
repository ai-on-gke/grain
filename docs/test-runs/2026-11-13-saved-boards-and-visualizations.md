# Saved boards and task visualizations walkthrough — 2026-11-13 (task 4727)

This run walked the Boards index and every board presentation: `list`,
`kanban`, `gantt` (timeline), `tree`, the `panels` dashboard and `report`.
It drove a real headless Chromium through Playwright against a real
`grain demo` server, over a throwaway embedded SQLite store.

The walkthrough scripts were ad-hoc Node scripts using the Playwright
library from `ui/node_modules`. They lived in `/var/tmp/walk` and are not
committed.

The 2026-11-11 report was read first. Every standing coverage item was
repeated against today's `main`, and the run then looked further at:

- which dashboard panels offer "Keep" under a sort
- dropping a board on a folder name
- deep links to a missing board or an unknown `?window=`
- empty narrowings on every kind

One bug was found and fixed. Re-sorting a dashboard from the toolbar
offered "Keep" on its state counts panel, whose counts no sort can
change (section 3). The fix has a Vitest unit test and a Playwright E2E
test, and both fail on the unfixed code.

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `4ae6438fd` (`main`, 2026-11-13), then the same tree with the fix in section 3 (`5253e7ea0`) |
| Server | `./bin/grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, with `TMPDIR` set to a throwaway directory |
| Frontend | `cd ui && npm ci && npm run build`, embedded by `make grain` |
| Browser | Playwright Chromium 1234, headless (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`). Desktop `1440×900` and `iPhone 13` (`390×844`, touch), light and dark |
| Toolchain | Go `1.26.10`, Node `v20.19.0`, Debian Linux `x86_64` |
| Gemini key | `/home/debian/.gemini-api-key` is absent. Layout was checked with bounding boxes and `scrollWidth`, not `LOOK=1` |

Sign-in went through the visible access-code gate (`Your email`,
`Access code`, `Continue`) as `alice@example.com`.

`grain demo` does not accept `-sandbox-port-base` (only `grain daemon`
does). The memory note suggesting it applies to the daemon, not to the
demo.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub, CI, base-branch CI | `-github-sim`, `-fake-ci`, `-base-branch-ci` | The sandbox has no external GitHub or CI |
| Agent execution for board creation | Checked up to the filed `grain/config` task (section 2.2). The throwaway boards for the presentation walkthroughs were made with `POST /api/boards` | `grain demo` runs no model agent, so a requested board never lands |
| Throwaway boards | Listed below this table | The demo seeds only one board of each built-in kind (`Board`, `Timeline`, `Report`, `Tree`) |
| Held proposal, `keep_running` task, twin titles | `POST /api/tasks`: `WT held proposal` (`proposedBy: "8"`, `holdForApproval: true`), `WT standing task` (`keepRunning: true`), and two `Duplicate card title` tasks | Only an agent's `propose_task` makes a held proposal, and the demo seeds neither of the others |
| Viewer role | `page.route` on `GET /api/config`, rewriting `access` to `role: "grain/viewer"` with scopes `settings:read` and `tasks:read` (the stub `ui/e2e/config-change-card.spec.js` uses) | The sandbox has no identity-aware proxy |
| Load failures and slow loads | Scoped `page.route`: `500` for `GET /api/boards`, `GET /api/tasks` and `GET /api/metrics`, and a 1.5–2.5 s delay on `GET /api/metrics` | Real SQLite neither fails nor stalls on demand |

The throwaway boards were:

- **`Triage` ×3:**
  - a loose list, `f=state:any:queued`
  - `WT Ops` list, `f=state:any:running`
  - `WT Zeta` kanban
- **Foldered lists:** `WT Zeta Alpha`, `WT Zeta Beta`, `WT Ops Queue`
- **Custom columns:** `WT Submit Only` (one `awaiting_submit` column) and
  `WT Reply Only` (one `awaiting_reply` column)
- **Tree:** `WT Widgets Tree`, `f=repo:any:acme/widgets`
- **Dashboards:**
  - `WT Dash`, with six panels: states, two untitled lists, a
    `Running only` list, a `Week timeline` gantt over 7d, and a
    `Throughput` metrics panel over 7d
  - `WT Empty Dash`
- **Reports:** `WT Nine` (all nine sections) and `WT Widgets Report`
  (`f=repo:any:acme/widgets`)

Rebuilding the binary meant a new demo store, so the same fixtures were
seeded again before the fix was checked.

No context threw an uncaught `pageerror`. The only `>= 400` responses
were:

- the expected pre-sign-in `401` on `/`
- the injected `500`s
- the known `404` on `GET /api/repos/grain/config/github-branches` when
  `/new` opens a board request (section 4)

## 2. Steps and outcomes

### 2.1 Boards index, rail, folders, ordering, history and load failure

- **Navigation:**
  - The rail's `Boards 4` button opened `/boards`. Its `main` landmark
    lists `Board`, `Timeline`, `Report` and `Tree`, and the rail badge
    rose to 17 once the fixtures existed.
  - `/boards` offers no search or sort of its own. Its prose sends
    narrowing to the task list.
- **Folders:**
  - The three `Triage` boards have distinct accessible names:
    `Triage, outside every folder`, `Triage, in WT Ops` and
    `Triage, in WT Zeta`.
  - Their `Move …` handles and `Move here, above …` slots are named the
    same way.
- **Mouse drag in a lower section:** in `WT Zeta`, the third section,
  `WT Zeta Beta` was dragged onto `WT Zeta Alpha`.
  - The page sent `PATCH /api/boards/board-9
    {"folder":"WT Zeta","orderKey":1048576}`, then
    `PATCH /api/boards/board-8 {"orderKey":2097152}` to make room.
  - The section read `Triage, WT Zeta Beta, WT Zeta Alpha` after a
    reload.
- **Drop on a folder name:** dragging `WT Empty Dash` onto the `WT Ops`
  folder caption filed it there
  (`{"folder":"WT Ops","orderKey":-1048576}`).
  - The keyboard slot `Move here, outside every folder` moved it back
    out, and `GET /api/boards` showed no folder.
- **Keyboard move:**
  - `Enter` on `Move WT Ops Queue` showed 18 slots.
  - `Escape` cancelled and left focus on the handle.
  - Picking it up again and choosing `Move here, at the end of WT Zeta`
    filed it in `WT Zeta`. Focus stayed on the handle, and the move
    survived a reload.
- **History:**
  - `Enter` on `Timeline` opened `/boards/board-2`.
  - Back returned focus to the `Timeline` link.
  - Forward reopened the board, and a reload kept it.
- **Load failure:** with `GET /api/boards` answering `500`
  (`database disk image is malformed`):
  - `/boards` showed `The boards could not be loaded (database disk image
    is malformed).` with `Retry`, and never `No boards yet`.
  - A cold `/boards/board-1` showed the same explanation.
  - With a one-shot fault, `Retry` recovered the list.
- **Missing board:** `/boards/board-999` sends the reader to `/boards`
  with the toast `no board "board-999"`.

### 2.2 Asynchronous board creation and viewer gating

- **Rail menu:**
  - From `/?f=repo:any:acme/widgets&q=retry`, `Create options` offers
    `New task`, `New session` and `New board`.
  - `New board` opened `/new` titled `Add a board` and targeting
    `grain/config`, with ``query: `q=retry&f=repo:any:acme/widgets` ``
    in the body.
  - With `Queue immediately` unticked, `Create task` filed task 28:
    `proposed`, `grain/config`. The overlay then returned to the task
    list it was opened from.
- **Boards index header:**
  - `New board` opened the same template, with an empty query.
  - Left on `Queue immediately`, it filed task 29: `queued`,
    `approved: true`, `grain/config`. The overlay returned to `/boards`.
  - The board count stayed at 17, because a board appears only once an
    agent's state-repo change lands.
  - `/tasks/29` opened the filed task.
- **Edit board:** on `/boards/board-5?q=retry` it opened
  `Change the board "Triage"` against `grain/config`. The body states
  the board's stored fields and notes the `q=retry` it is being read
  with.
- **Viewer (stubbed `/api/config`):**
  - These were disabled: `New board` on `/boards` and on every board
    kind, `Edit board` on every kind, `Add panel` on `WT Dash`, and the
    rail's `New task` and `New board`.
  - `Keep` on `WT Dash?q=retry` was refused in place (disabled), as
    `role-gated-actions.spec.js` expects.
  - `/boards`, kanban, list, timeline, tree, dashboard and report boards
    rendered zero `.task-drag-handle`s, zero `draggable="true"` items and
    zero `Move …` buttons.
  - `Columns` on the kanban stayed enabled, correctly: it edits a
    per-browser setting.
  - `New session` stays enabled for a viewer, as the 2026-11-11 report
    also noted.

### 2.3 List and kanban

- **`Board` (`/boards/board-1`), columns and summary:**
  - The columns read `Proposed, Waiting, Running, Asked, Failed,
    Merging`, and `Merging` holds 5 and 21.
  - The board summarises `2 tasks in no column (Deferred, Closed).`
- **`Board`, card markers:**
  - Task 11 carries the blocked oval (`img "Blocked on: #8 (running)"`,
    `.badge-blocked`).
  - Task 25 shows `keeps running`.
  - The held proposal 24 shows `held for approval by 8`.
  - Issue 12 shows `imported`.
- **Card moves:**
  - Picking up `Move task 27` showed slots that name tasks by id as well
    as title, for example `Move here, above task 26, “Duplicate card
    title”, in Proposed`.
  - Moving 27 above 26 sent `{"ids":["27"],"afterId":"25","beforeId":"26"}`
    and kept focus on `Move task 27`.
  - Dragging card 23 onto card 2 with real drag events sent
    `{"ids":["23"],"afterId":"11","beforeId":"2"}`.
  - Both orders survived a reload: `1,12,24,25,27,26,28 ||
    11,23,2,20,29 || …`.
  - A first attempt used Playwright's `dragTo` from the handle. It
    scrolled the column and started the drag on card 11, which moved 11.
    That was the script's pointer landing on another card, not the page.
- **Opening a card:** opening task 11 from its card link and pressing
  `Escape` returned to the board, with focus on that card's link.
- **Column editor:**
  - Renaming `Proposed` to `Ideas WT` and pressing `Escape` raised
    `You have unsaved changes here. Leave and discard them?`. Dismissing
    it kept the dialog open.
  - `Save` and a reload kept `Ideas WT`.
  - `Move Ideas WT right`, from the keyboard, kept focus on that button.
  - `Reset to default` and `Save` restored the defaults.
- **Custom columns:**
  - `WT Submit Only` hides `Edit columns` and summarises
    `20 tasks in no column (Proposed, Waiting, Running, Asked for a reply, Merging, Deferred, Closed).`
  - `WT Reply Only` summarises
    `26 tasks in no column (Proposed, Waiting, Running, Asked to submit, Merging, Deferred, Closed).`
- **List boards:**
  - `WT Zeta Alpha` shows the `keeps running` marker.
  - On the queued `Triage` list, `Move task 29` to
    `Move here, above task 11, …` sent `{"ids":["29"],"beforeId":"11"}`,
    and the list read `29,11,23,2,20` after a reload.
- **Phone:** at `390×844`, tapping `Move task 27` showed 6 slots.

### 2.4 Timeline and gantt

- **Controls:** `Range` offers `Last 24 hours` through `Everything`.
  `Lanes` offers `No lanes`, `By repo`, `By state` and `By author`.
- **Lanes:** under `Everything` with `By state`, the lanes read
  `Proposed, Waiting, Running, Asked, Merging, Deferred, Closed`, so
  **Asked follows Running**.
- **Drawing:**
  - The today marker (`.gantt-now`) and every bar were drawn.
  - Dependencies are carried in the row's accessible name (`11: … ·
    Blocked on: #8 (running) · …`) and the bar's `Waiting, blocked`
    label.
- **Persistence:** both settings survived a reload. They are kept in the
  browser, as the board description says, and the URL stays
  `/boards/board-2`. `Last 24 hours` showed `3 tasks outside this range.`
- **Opening a task:** clicking the first bar opened `/tasks/1`. Back
  restored focus to that row's `/tasks/1` link.

### 2.5 Tree

- **Nesting in `WT Widgets Tree`:**
  - 23 sits under 22.
  - 1, 3 and 8 sit under the director session 13.
  - 11 and the held proposal 24 sit under 8.
  - `grain/config` tasks are excluded by the board's repo filter.
- **Search:**
  - Typing `pagination` gave
    `/boards/board-13?q=pagination&f=repo:any:acme/widgets`, showing 8
    with 11 under it.
  - A reload kept both the search and the board's filter.
- **Opening a row:** `Enter` on 11 opened its task page, and Back
  restored focus to the `/tasks/11` link.
- The seeded `Tree` board draws 29 rows.

### 2.6 Dashboard

- **`Keep` offers on `WT Dash`:**
  - Unnarrowed: none.
  - `?q=retry` and `?f=state:any:queued`: offered on `State counts`, both
    `Task list`s and `Week timeline`. Not offered on `Running only`,
    which has its own query, or on `Throughput`, whose report neither
    narrowing changes.
  - `?f=repo:any:acme/widgets` and `?f=author:any:alice@example.com`:
    also offered on `Throughput`, since both reach the report's scope.
  - A sort chosen from the toolbar (`Backlog order` → `Sort by…` →
    `created`), giving `?sort=created:desc&f=state:none:deferred|closed`:
    offered on `State counts` as well. That is the bug in section 3. On
    the fixed build it is offered on the two lists and the timeline only.
- **Requests, not changes:**
  - `Keep this narrowing on “Throughput”` opened a prefilled `/new`
    (`Narrow the panel "Throughput" on the dashboard "WT Dash"`,
    `grain/config`). The body lists every panel with its `order_key`.
  - `Add panel` opened `Add a panel to the dashboard "WT Dash"`.
  - Neither changed the board synchronously.
- **Reorder:**
  - Keyboard: `Move panel 6, “Throughput”` to
    `Move here, above panel 1, “State counts”` left focus on
    `Move panel 1, “Throughput”`. `GET /api/boards` stored it at
    `orderKey -1048575`.
  - Drag: dropping `Move panel 5` (`Running only`) on `State counts`
    sent a `PATCH` with the whole panel list. The order came back after
    a reload as `Throughput, Running only, State counts, …`.
- **Panel scoping:** under `?q=retry`, `Running only` still shows 3 and 8
  (its own `f=state:any:running`). The inheriting lists and timeline show
  `2,14,15,16,17,18,19`.
- **Links:**
  - Each of the six `Open` links went to its own list
    (`/system/metrics?…&f=created:within:7` for `Throughput`,
    `/?f=state:any:running` for `Running only`).
  - Back restored focus to the same `Open` link by index, including
    among four identical links.
  - Every state count (`7 Proposed`, `5 Waiting`, `2 Running`,
    `10 Asked`, `0 Failed`, `2 Merging`) matched the row count of the
    list it opens, and Back restored focus to it.
- **Empty and error states:**
  - `WT Empty Dash` reads
    `This dashboard has no panels yet. “Add panel” files the task that adds one.`
  - A `GET /api/tasks` `500` drew `The tasks could not be loaded (500
    Internal Server Error).` with `Retry` in each of the five task
    panels.
  - A `GET /api/metrics` `500` drew `The metrics report could not be
    loaded.` in the throughput panel.
- **Phone width:** with the fix, at desktop and phone widths in light and
  dark, the re-sorted dashboard had no horizontal overflow. `State
  counts` had no `Keep`, and the two lists each had one.

### 2.7 Report

- **Sections:**
  - The seeded `Report` draws `Throughput`, `Latency`, `Capacity`,
    `Backlog` and `GitHub CI`, and leaves out its silent sections without
    a note.
  - `WT Nine` names all nine sections and draws `Throughput`, `Latency`
    and `Backlog`. It accounts for `Merge queue`, `Prompts`, `Tool use`,
    `Agents`, `Sandboxes` and `CI waits` with `<Section>: nothing to
    report over this window.`, because the board asked for them by name.
- **Window:**
  - Choosing `Last 30 days` set `?window=30d`.
  - `Refresh` fetched `/api/metrics?window=30d&buckets=24`.
  - A reload kept `Last 30 days`.
  - An unknown `?window=bogus` falls back to `Last 7 days` and is dropped
    from the URL.
- **Reorder:** moving `Move section 9, “CI waits”` above section 1 left
  focus on `Move section 1, “CI waits”`. The stored order became
  `checks, throughput, …, sandboxes`.
- **Scoping:**
  - `WT Widgets Report` requested
    `/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24` and says
    `Measured over the tasks in acme/widgets.`
  - A typed `?f=state:any:running&q=retry` says that the search and the
    state filter are not in force.
- **Backlog links**, unscoped and under `acme/widgets`:
  - Every chip (`proposed 7/6`, `waiting 5/4`, `running 2/2`,
    `asked 1/1`) matched the row count of the list it opens.
  - The `task 20` reference opened `/tasks/20`.
  - Back restored focus to the originating link every time.
- **Loading and errors:**
  - A delayed `/api/metrics` showed `aria-label="Loading the report"`.
  - A `500` showed `The report could not be loaded (500 Internal Server
    Error). Refresh to try again.`
  - `Refresh` recovered all five sections.
- `Attempts per completion` reads `0.00` on the demo. That is accurate:
  the seeded completed tasks never made an attempt.

### 2.8 Phone width, empty narrowings and destructive actions

- **No overflow:** at `390×844`, light and dark, and at desktop dark,
  `document.documentElement.scrollWidth` never exceeded the viewport on
  any of these: `/boards`, `board-1`, `board-2`, `board-4`, `WT Dash`,
  `WT Nine` or the queued `Triage` list.
- **`?q=zzzznomatch` on every kind:**
  - Kanban draws its columns with `Nothing here.` and `No tasks match
    this search and these filters.`
  - The timeline and tree say the same, the timeline adding `in this
    range`.
  - The list says `No tasks match your search.`
  - The dashboard zeroes its inheriting panels.
  - The report says the search is not in force.
- **Destructive actions:** the UI exposes no delete for boards, panels or
  sections, because removing one is a config task. The only destructive
  confirmation in this area is the column editor's discard confirm
  (section 2.3). The throwaway objects went with the throwaway stores
  when the servers stopped.

## 3. Bug found and fixed

### 3.1 A re-sorted dashboard offered Keep on its state counts panel

- **Reproduction** (demo, any dashboard with a `states` panel and an
  inheriting `list` panel):
  1. Open the dashboard, then pick `Backlog order` → `Sort by…` →
     `created`. The URL becomes
     `?sort=created:desc&f=state:none:deferred|closed`, so the filter is
     unchanged and only the sort is new.
  2. `Keep this narrowing on “State counts”` appears beside the list
     panels' Keep. Pressing it opens a prefilled task asking an agent to
     write `sort=created:desc` onto the panel's query.
  3. Nothing on screen would change if that task landed: a count has no
     order.
- **Cause:** `panelUsesNarrowing` in `ui/src/panels.js` decides whether
  Keep means anything on a panel. It already compares a throughput
  panel's report with the one it fetches on the board's own question
  (task 3612). It still treated every other kind as using any narrowing
  at all, including the state counts panel, which narrows the tasks it
  counts but never orders them.
- **Fix:** for a `states` panel, `panelUsesNarrowing` now compares the
  narrowing with the dashboard's baseline after setting the sort aside on
  both sides. List and timeline panels draw rows in the sort's order, so
  they still offer Keep.
- **Verification:** on the rebuilt binary:
  - The toolbar-sorted `WT Dash` offers Keep on `Task list`, `Task list`
    and `Week timeline` only.
  - `?q=retry`, `?f=state:any:queued` and the repo and author filters
    still offer it on `State counts`.
- **Regression coverage:**
  - `ui/src/panels.test.js`: `is false of the state counts panel for a
    narrowing that only sorts`. It fails on the unfixed `panels.js`.
  - `ui/e2e/saved-boards-walkthrough.spec.js` `6g`: drives the real demo.
    It seeds a scoped dashboard, re-sorts it through the visible toolbar
    controls, and expects Keep on the list and not on the counts. It then
    checks that a search brings the counts' Keep back.
  - `6g` fails on a frontend built from the unfixed `panels.js`
    (`expect(countsKeep).toHaveCount(0)`) and passes on the fixed one. It
    deletes its board afterwards.

## 4. Observations not filed

- Opening `/new` for a board request (target `grain/config`) logs a
  console `404` for `GET /api/repos/grain/config/github-branches`, as on
  2026-11-11. The form handles it silently.
- A failed `/api/metrics` on a dashboard's throughput panel says only
  `The metrics report could not be loaded.`, with no reason and no Retry.
  The task panels beside it give the reason and a `Retry`. The report
  board itself gives the reason and points at `Refresh`. This is
  cosmetic.
- The Boards index's folder names are captions (`<p>`), not headings, so
  a screen reader cannot jump between folders by heading. Every board's
  accessible name already names its folder, so nothing is ambiguous.
- Board pages carry two level-2 headings with the board's name: the page
  header and the board body. This is redundant but harmless.
- The rail's `Tasks` badge (22) and `GET /api/tasks` (23 items) differ
  by the director session, which the task list does not count. This
  looks deliberate.

## 5. Tests run

- `npx vitest run src/panels.test.js`: 41 passed, including the new test.
  The new test was confirmed failing without the fix.
- `npx vitest run src/panels.test.js src/boards.test.js
  src/components/PanelBoard.test.jsx src/components/BoardsPage.test.jsx
  src/components/ReportBoard.test.jsx src/components/TaskBoard.test.jsx`:
  6 files, **249 passed**.
- `npx playwright test e2e/saved-boards-walkthrough.spec.js -g "6f|6g"
  --project=chromium`: both passed on the fixed build. `6g` failed on the
  unfixed frontend, as expected.
- `npx playwright test e2e/saved-boards-walkthrough.spec.js
  e2e/boards.spec.js e2e/board.spec.js e2e/role-gated-actions.spec.js
  e2e/boards_workflows.spec.js e2e/board-return-focus.spec.js
  --project=chromium` on the fixed build: **76 passed**.
