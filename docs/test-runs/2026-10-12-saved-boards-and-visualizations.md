# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-12 (11:23–12:30 UTC)

Commit tested: `b0fd41c2a` (`docs(release): add release notes for
v1.9.0`, tip of `origin/candidate`, checked out as `grain/eu/task-3211`;
the build reports itself as `v1.10.0-rc.1 · b0fd41c`). The two fixes
below were then rebuilt into a fresh server and re-walked.

Prior walkthroughs: `2026-10-11-saved-boards-and-visualizations.md` and
the ones it lists. This run concentrated on what has changed since --
team-scoped URLs (`/t/<team>/…`, from the team switcher) -- and on what
those runs did not reach: a board whose own row defines its Kanban
columns, the timeline's 24h/30d ranges, a gantt panel's own range,
cross-folder keyboard moves, ordering past the ninth board, the Tree
board's rows, and a task-list failure under a dashboard.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Playwright
  (from `ui/node_modules`) driving Chromium headless shell.
- Frontend: `cd ui && npm ci && npm run build` into `pkg/ui/static`;
  server: `go build -o /tmp/grain ./cmd/grain` after the frontend build.
- Server: `/tmp/grain demo -addr 127.0.0.1:8787 -open=false` from a
  throwaway working directory, i.e. its embedded SQLite store seeded by
  `grain demo` itself. Restarted on a fresh directory after the fixes.
- Browser: standalone Playwright scripts (not committed) against the real
  server; 1440×1000 desktop, and an iPhone 13 profile (390×844,
  `hasTouch`, `isMobile`). Every script recorded `pageerror`, console
  errors, responses ≥ 400 and failed requests.
- Identity: `localStorage["grain.username"] = "alice"`,
  `X-Grain-Admin-Mode: on` (the e2e suite's default).
- Gemini: no key at `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed model or tooling was used; no flow here needed one.
- `npx playwright install chromium` needs
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`
  behind the package-cache proxy.

## Stubs and stand-ins

- **Merged board changes (stand-in, not a stub).** A board is created by
  an agent's pull request against the state repository, which a sandbox
  cannot merge. After the real request flow (step 2), the boards an agent
  would have produced were written with `POST /api/boards`, the endpoint
  the import ends at: `WT3211 List` (`f=state:any:queued`),
  `WT3211 Kanban` (its own columns: Waiting = queued+proposed,
  Doing = running), `WT3211 Gantt` (`f=repo:any:acme/widgets`),
  `WT3211 Dash` (states; inheriting list; list scoped to running;
  metrics 30d on `acme/widgets`; gantt 24h full width; list scoped to
  `nobody/nothing`), `WT3211 Report` (all eleven section kinds, latency
  retitled `Lat`, scoped to `acme/widgets`) and `WT3211 Tree`
  (`q=pagination`), all in folder `WT3211`. They lived only in the
  throwaway store.
- **`/api/metrics` → 500, and → 3 s delay** (`page.route`).
- **`/api/tasks` → 500** (`page.route`), under a dashboard.
- **`GET /api/boards` → 500** (`page.route`), on a board deep link.
- **GitHub boundary.** Task detail asks
  `GET /api/repos/<repo>/github-branches`, which answers 404 in demo mode;
  that 404 and its console line were the only errors outside stubbed
  failures and navigation-aborted `/api/tasks` polls.

## Steps and outcomes

1. **Boards index, rail, URLs, history.** Rail `boards 4` →
   `/t/default/boards`: `Board` (Kanban), `Timeline`, `Report`, `Tree`,
   each with kind chip and description; `New board` and one `Move <name>`
   handle per board. The index has no search or sort. Rows link to
   `/boards/board-N`; the unprefixed `/boards`, `/boards/board-2` and
   `/boards/board-3?window=30d` all land on their `/t/default/…`
   equivalents with the query kept, and `/t/nosuch/boards/board-1` falls
   back to the default team. `/t/default/boards/board-999` and
   `/boards/nonsense` land on the index. Index → board → `← Boards` →
   Back → Back → Forward → reload ended on the board with its heading.
   No page errors.
2. **Asynchronous creation contract.** `New board` opens the `New task`
   dialog at `/t/default/new`: title `Add a board`, the board prompt,
   repo `grain/config-default`, auto-merge on, team `admin`. Appending a
   request and pressing `Create task` → `POST /api/tasks` 201, back on
   the index; task 13 is `queued` on `grain/config-default`, branch
   `grain/task-13`, `autoMerge: true`, and boards stayed at 4 (API and
   rail) -- the board appears only when that task's change merges.
   On each of the four seeded boards narrowed with `?q=pagination`,
   `New board` carries the board's kind and `` query: `q=pagination` ``;
   `Edit board` is titled `Change the board "<name>"`, carries the kind,
   and states the live narrowing beneath the board's own (empty) query.
   Back from the dialog returns to the narrowed board each time.
   On a dashboard, `Add panel` names `board-8` and the narrowing; `Keep`
   (offered only on the three inheriting panels) asks to set panel 1's
   query to `q=pagination`, listing the panels in drawn order.
3. **List and Kanban.** `WT3211 List` shows exactly the 3 queued tasks;
   clicking a row opens the task, Escape returns. `WT3211 Kanban` draws
   its own two columns (4 + 2) and says “6 tasks in no column (Awaiting
   reply, Awaiting submit, Awaiting ack, Queued for merge, Deferred,
   Closed).” Keyboard pick-up of task 13 offers only the three slots in
   Waiting, focus moves to `Stop moving task 13`, `Enter` on the first
   slot moves it to the top and returns focus to `Move task 13`; the order
   survived reload. A mouse drag (real pointer steps) of task 8 above
   task 3 in Doing persisted across reload; a drag across columns changes
   nothing (state is derived; task 3 stayed `running`).
4. **Timeline.** `WT3211 Gantt` on “Last 7 days”: 10 rows, one
   `.gantt-now` at 100 %, ticks Oct 6…12, one blocked chip “Blocked on:
   #8 (running)”, “1 task outside this range.”. Last 24 hours: 9 rows,
   hour ticks, “2 tasks outside this range.”; Last 30 days and
   Everything: 11 rows, no note. Lanes offers no lanes / by repo /
   by state / by author; by repo gave an `acme/widgets` lane. Range and
   lanes survived reload; the URL kept the board's query. Clicking a bar
   (accessible description “Proposed: waiting for 4h 4m, never
   dispatched”) opens `/t/default/tasks/1`; Back restores the timeline
   with its settings. On the phone profile, tapping a bar opens the task.
5. **Dashboard.** All six panels drew: counts 10 = 1+3+2+3+1, the
   inheriting list “Showing 8 of 10.”, Running only 2, Widgets 30d
   “12 filed / 3 completed” (cross-checked against
   `/api/metrics?repo=acme%2Fwidgets&window=30d`: the 12 includes the
   seeded Director session, which task views hide), the 24h gantt panel,
   and Nobody “Nothing here right now.”. Search `pagination` narrowed
   only the inheriting panels (counts 2, list 2, gantt 2, each growing
   `Keep`); the scoped panels kept their own answers; URL and result
   survived reload. Followed every link: “3 Needs you” → a task list of
   exactly 3, “1 Queued for merge” → 1, Running only `Open` → 2, Nobody
   `Open` → an empty list, Widgets 30d `Open` → the Metrics overlay at
   `/metrics?f=repo:any:acme/widgets&f=created:within:30`, which asks
   `/api/metrics?repo=acme%2Fwidgets&window=30d` and says “Last 30
   days … Measured over the tasks in acme/widgets.”; that survived reload.
   A row in a list panel opens the task; Escape returns to the board.
   Keyboard pick-up of panel 6 offered five slots; the move issued one
   `PATCH /api/boards/board-8` (200), kept focus on the moved panel's
   handle, and survived reload; moving it back to the end kept focus too.
6. **Report.** `WT3211 Report` drew all eleven sections with handles
   `Move section 1, “Throughput”` … `Move section 11, “Sandboxes”`, the
   retitled section as `Lat`, “Measured over the tasks in
   acme/widgets.”, and requested `repo=acme/widgets&window=7d`. Sections
   with no data say “<Section>: nothing to report over this window.”.
   Keyboard move of Sandboxes above Throughput kept focus
   (`Move section 1, “Sandboxes”`) and persisted (`GET /api/boards`
   shows it first). Window → Last 90 days wrote `&window=90d` and
   re-asked. The backlog's “Oldest queued: task 2” button is the
   section's link into the data.
7. **Tree.** `WT3211 Tree` (`q=pagination`) nests task 11 under task 8
   with state chips and the PR link; the task number is the link
   (`/tasks/11` opens, Escape returns).
8. **Folders.** Keyboard pick-up of `Timeline` offered a slot above every
   board, “outside every folder” and “at the end of WT3211”; moving into
   the folder and back out persisted (`folder` set and cleared).
   **Focus after a cross-folder move was lost -- finding 1.** The index
   also listed `WT3211` as Tree, Kanban, List, Gantt, Dash, Report rather
   than in filing order -- **finding 2.** On the phone profile, tap
   `Move Report` then tap “at the end of WT3211” filed it there.
9. **Loading / error states (stubbed).** Metrics delayed: the metrics
   panel says “Loading…”. Metrics 500: the panel says “The metrics report
   could not be loaded.”, the report “The report could not be loaded
   (500 Internal Server Error). Refresh to try again.”. Tasks 500 on a
   fresh load: an alert “500 Internal Server Error”, and every task panel
   reads 0 / “Nothing here right now.” (observation 1). Boards 500 on a
   deep link: the index's empty state under the alert with the URL kept,
   recovering to the board on the next poll once unstubbed (as recorded
   on 10-11).
10. **Phone (390×844, touch).** No horizontal overflow on the index or on
    any board kind (list, kanban, gantt, panels, report, tree).
11. **Delete and confirmation.** No board, panel or section delete exists
    in the UI (`8. Deletion safety` in the e2e spec asserts the same);
    boards go by a state-repository change. The only destructive controls
    reached were the Kanban column editor's `Remove` (a draft until
    Save, covered on 10-11) and the new-task dialog's `Discard draft`,
    which clears the prefilled board prompt at once without asking
    (observation 3). Throwaway boards and task 13 lived only in the demo
    store, discarded with it.

## Findings

### 1. A board moved into or out of a folder from the keyboard lost focus (fixed here)

Reproduction: on `/boards`, focus `Move Timeline`, press Enter, focus
“Move here, at the end of WT3211”, press Enter. Once the boards come back
`document.activeElement` is `<body>`; a move within one folder keeps focus
on the handle. Cause: `BoardList` renders each folder as its own keyed
section, so a board that changes folder is a new `<li>` and a new
`MoveHandle`; the put-down focus return in `MoveHandle` runs on the old
instance, which is then unmounted.

Fix: `BoardList` remembers the board last put down from its handle and
passes `landed` to that board's `MoveHandle`, which takes focus when it
mounts with it -- only if focus is on `<body>`, the same rule as the
put-down. A new pick-up or a mouse drop clears it. Re-walked: focus stays
on `Move Tree` after moves into a folder, out of it, and within one.

Regression coverage: `BoardsPage.test.jsx` “keeps focus on the handle of
a board moved into another folder”, and e2e `3b` in
`saved-boards-walkthrough.spec.js`; both fail with the fix reverted.

### 2. Boards nobody had ordered sorted by id as text (fixed here)

Reproduction: create boards until ids pass `board-9` (here `board-5` …
`board-11` in one folder, no `orderKey`); `GET /api/boards` and the index
list `board-10`, `board-11` above `board-5`. Cause: `ListBoards` broke
`order_key` ties on `id` as text, while `NewBoardID` issues `board-<n>`.

Fix: tie-break on `LENGTH(id)` then `id`, which is filing order for
every issued id and still one fixed order for hand-written ids. Re-walked
on a fresh store: `board-5, 7, 8, 9, 10, 11` in that order.

Regression coverage: `pkg/ui` `TestListBoardsKeepsUnorderedBoardsInFilingOrder`
(12 boards; fails with the old ORDER BY), plus the filing-order assertion
in e2e `3b`.

### Observations (not changed)

1. **A dashboard's task panels read as empty when the task list fails.**
   With `/api/tasks` failing on a fresh load the panels say 0 / “Nothing
   here right now.” under a “500 Internal Server Error” alert. The task
   list page behaves the same (“No tasks in this state.”), so this is the
   shell's pattern -- the same one recorded for boards on 10-11 -- rather
   than a panel defect.
2. **A metrics panel's `Open` lands below the fold.** The Metrics overlay
   draws release notes (weekly and per version) above the scoped metrics
   report, so the scoped answer the link promised is below a long list.
3. **`Discard draft` asks nothing.** Discarding the new-task dialog's
   draft drops a prefilled board/panel prompt at once; it is labelled for
   what it does and is outside the boards area proper.
4. **A Tree board row opens only from its number.** The title is plain
   text; the number is the link, as on a task page's own tree.
5. **The retitled latency section draws two `h3`s** (`Lat`, then the
   section's own `Latency`).

## Automated tests

- `go test ./pkg/ui -run TestListBoards -count=1`: pass (the new test
  fails against the old ORDER BY).
- `npx vitest run src/components/BoardsPage.test.jsx
  src/components/ReorderPickup.test.jsx`: 40 passed (the new test fails
  with the fix reverted: 1 failed, 11 passed).
- `npx playwright test e2e/saved-boards-walkthrough.spec.js
  e2e/boards.spec.js e2e/board.spec.js e2e/gantt.spec.js
  e2e/boards_workflows.spec.js --project=chromium`: 47 passed (51.1 s);
  `3b` alone against a build without the focus fix: 1 failed
  (`toBeFocused`).
