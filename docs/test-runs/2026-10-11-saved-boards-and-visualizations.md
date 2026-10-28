# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-11 (20:52–21:20 UTC)

Commit tested: `ab0ed8ca9e0cdedb1088b33e40292db36013a2ee` (tip of
`origin/candidate`, checked out as `grain/task-2957`). The fix below
(`fix(ui): offer repos in a report board's filter editor`) was then
rebuilt into the same server and re-walked.

Prior walkthroughs: `2026-10-10-saved-boards-and-visualizations.md` and
the earlier ones it lists. This run deliberately went past the scripted
cases in `ui/e2e/saved-boards-walkthrough.spec.js`: mouse drags with real
pointer steps, tap pick-ups on a phone viewport, the column editor's
destructive controls, panel links followed to their destination, loading
and error states, and the toolbar's filter editor on every board kind.

## Environment

- Debian sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6.4.3,
  Playwright 1.62.1 driving Chromium headless shell (build 1234).
- Frontend built with `cd ui && npm ci && npm run build` into
  `pkg/ui/static`; server built with `go build -o /tmp/grain ./cmd/grain`
  after the frontend build (a binary built concurrently with the frontend
  embeds only `placeholder.html` — rebuild after `npm run build`).
- Server: `/tmp/grain demo -addr 127.0.0.1:8787 -open=false` from a
  throwaway working directory, i.e. its embedded SQLite store, seeded by
  `grain demo` itself.
- Browser: standalone Playwright scripts (not committed) against the real
  server. Desktop viewports 1440×900, 1440×1600 and 1440×2400; phone pass
  at 390×844 with `hasTouch`, `isMobile` and the iPhone 13 user agent.
  Every script collected `pageerror`, console errors, responses ≥ 400 and
  failed requests.
- Identity: `localStorage["grain.username"] = "alice"`, with
  `X-Grain-Admin-Mode: on` (the e2e suite's default).
- Gemini: no key at `/home/debian/.gemini-api-key`, so nothing here used
  a Gemini-backed model; none of the flows needed one.
- Playwright's browser download goes through the package-cache proxy,
  which re-signs TLS: `npx playwright install` fails with
  `SELF_SIGNED_CERT_IN_CHAIN` unless run with
  `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt`.

## Stubs and stand-ins

- **Merged board changes (stand-in, not a stub).** A board is created by
  an agent's pull request against the state repository, which a sandbox
  cannot merge. After exercising the real request flow (step 2), the
  boards an agent would have produced were written with
  `POST /api/boards` — the same endpoint the reconciler's import ends at:
  `WT Dash s1` (panels: states, inheriting list, list scoped to a repo
  with no tasks, metrics 30d on `acme/widgets`, gantt 24h full-width,
  states scoped to running, metrics scoped to a repo with no tasks),
  `WT Report s1` (all eleven section kinds, scoped to `acme/widgets`),
  `WT Empty report s1` (scoped to `nobody/nothing` plus a search),
  `WT List s1` and `WT Gantt s1`. All lived only in the throwaway store.
- **`/api/metrics` → 500 and → 3 s delay** (`page.route`), to see a
  dashboard's metrics panels and a report board fail and load.
- **`GET /api/boards` → 500** (`page.route`), to see the index and a
  board deep link while the boards list cannot be read.
- **GitHub boundary.** Task detail asks
  `GET /api/repos/<repo>/github-branches`, which answers 404 in demo mode
  (no GitHub token); the console error it logs is expected and was the
  only console error outside the stubbed failures.

## Steps and outcomes

1. **Boards index, rail and history.** Rail “Boards 4” → `/boards`:
   seeded `Board` (kanban), `Timeline`, `Report`, `Tree`, each with its
   kind chip and description; handles `Move <name>` and `New board` are
   the only controls (the index offers no search or sort). Each board
   opened from the index and by direct URL. Back / Back / Forward /
   reload landed on `/boards/board-3`, `/boards`, and kept `/boards`.
   No page errors.
2. **Asynchronous creation contract.** `New board` → `/new` with title
   `Add a board`, the `tables/board.json` prompt (every kind and the
   query grammar), repo `grain/config-default`, auto-merge on. Appended
   a request and pressed `Create task`: back on `/boards`, boards count
   stayed 4 (API and rail), and task 13 `WT board request …` exists as
   `queued` on `grain/config-default`, branch `grain/task-13`,
   `autoMerge: true`; its detail shows the prompt as filed. `New board`
   from a narrowed list (`/?f=repo:any:acme/widgets&q=pagination`, via
   the `Create options` menu) carries
   `` query: `q=pagination&f=repo:any:acme/widgets` `` into the prompt.
   `Add panel`, `Keep` and `Edit board` on a dashboard each open `/new`
   with a prompt naming `board-5`, every panel with its `order_key`, and
   (for Keep) an arrow at the panel being narrowed.
3. **Kanban.** Columns Proposed / Queued / Running / Needs you / Queued
   for merge. Mouse drag of the last Queued card onto the first moved it
   to the top and survived reload; the keyboard pick-up (`Enter` on
   `Move task 13`) offered only slots in the same column, focus moved to
   `Stop moving task 13`, `Enter` on “to the end of Queued” moved it back
   and returned focus to its handle; `Escape` cancels a pick-up. A drag
   into another column changes nothing (by design: state is derived).
   `Enter` on a card opens `/tasks/2`; `Escape` returns to the board.
   Column editor: `Move Proposed right` → Save → order persisted across
   reload; `Remove Running` → Cancel left the board unchanged; Remove →
   Save dropped the column and the board said “4 tasks in no column
   (Running, Deferred, Closed)” with an `Edit columns` button; reload
   kept it; `Reset to default` → Save restored it. `Escape` closes a
   clean editor but not a dirty one (the overlay's unsaved-changes
   guard), so a removal is never discarded by an accidental key.
4. **Timeline.** Seeded `Timeline`: 11 rows on “Last 7 days”, ticks
   Oct 5…, one `.gantt-now` marker at 100 %, 8 ongoing bars, one blocked
   chip titled “Blocked on: #8 (running)”, note “1 task outside this
   range.” Bars carry accessible descriptions (“Running: waited 1h 24m,
   running for 8m”). Range → Everything and Lanes → By repo gave lanes
   `acme/widgets` and `grain/config-default`, 12 rows; both survived
   reload. A fresh browser context on the same URL opens on the defaults:
   range and lanes are the reader's browser setting, as the board prompt
   says, not URL state. Clicking a bar and `Enter` on a row title open
   the task; Back restores the timeline with its settings. Search
   `pagination` → `?q=pagination`, 2 rows, kept across reload.
5. **Dashboard.** All seven panels drew; counts add up (states 10 =
   1+3+2+3+1). Followed every panel link: “3 Needs you” lands on a list
   of exactly those 3 tasks, “1 Queued for merge” on 1, the empty-repo
   list panel's `Open` on an empty list, the metrics panel's `Open` on
   the Metrics overlay at `/metrics?f=repo:any:acme/widgets&f=created:within:30`
   showing “Last 30 days”. Empty panels say “Nothing here right now.” and
   0 filed / 0 completed. Whole-board search `pagination` → URL
   `?q=pagination&…`; inheriting panels narrowed (states 2, list 2,
   gantt 2) and grew `Keep`, scoped panels did not; kept on reload;
   `Clear` returns to the deployment's default filters. Panel mouse drag
   (header grip, both ends on screen) moved panel 7 above panel 1 and
   then onto the trailing drop zone; each issued one
   `PATCH /api/boards/board-5` and survived reload.
6. **Report.** `WT Report s1` drew all eleven sections in order, its own
   title `Lat s1` on the latency section, “Measured over the tasks in
   acme/widgets.”, and requested `/api/metrics?repo=acme%2Fwidgets&window=7d`.
   `WT Empty report s1` named the search it could not use and said the
   backlog is empty. On `Report`, the window picker → Last 90 days wrote
   `?window=90d`, re-asked, and survived reload; `Refresh` re-asks.
   “Oldest queued: task 2” is a button that opens `/tasks/2`; Back
   returns to the report. Sections moved by tap pick-up persisted.
   **Scoping from the toolbar was broken — finding 1.**
7. **Loading / error states (stubbed).** Metrics delayed: panels say
   “Loading…”, the report shows its spinner (“Loading the report”).
   Metrics 500: panels say “The metrics report could not be loaded.”,
   the report says “The report could not be loaded (500 Internal Server
   Error). Refresh to try again.” Boards 500: see observation 2.
8. **Folders.** Mouse drag of a board onto the `WT s1` folder heading
   filed it there (`folder: "WT s1"`); keyboard pick-up offered a slot
   above every board in every folder plus “at the end of WT s1”, and
   moving a filed board above `Board` unfiled it; both persisted.
9. **Phone (390×844, touch).** No horizontal overflow on `/boards` or any
   board kind. Tap `Move task …` then tap “Move here, to the end of
   Queued” reordered the column and persisted; tapping a card opens it.
10. **Tree and list boards.** Tree lists every task under its filer with
    state chips. A list board with `f=state:any:queued` shows the three
    queued tasks with move handles; a deep link with its own `f=`
    replaces the board's question for that visit.
11. **Delete and confirmation.** No board, panel or section delete exists
    in the UI (boards are removed by a state-repository change). The only
    destructive control in this area is a kanban column's `Remove`, which
    is a draft until `Save` and was exercised in step 3. Throwaway boards
    and the filed task lived only in the demo store, which was discarded.

## Findings

### 1. A report board's filter editor offered no repos (fixed here)

Reproduction: open `/boards/board-3` (seeded `Report`) or the Metrics
overlay → `Filter` → `Filter by…` → `Repo` → open “Value for filter 1”.
The menu reads “No values to choose from”, so the report cannot be scoped
to a repo from the page — only by editing the URL — although the page's
own header comment promises “this report, but only acme/widgets, just for
a minute”. Cause: `ReportBoard` passed `tasks={[]}` to `NarrowingBar`,
whose value menus are built from the tasks they are handed
(`taskFilters.js` `optionsFor`).

Fix: `ReportBoard` takes a `tasks` prop (default `[]`) used only for its
toolbar, and App passes `visibleTasks` to the report board and the
Metrics overlay. Verified in the browser after the rebuild: both offer
`acme/widgets`, picking it writes `f=repo:any:acme/widgets`, re-asks
`/api/metrics?repo=acme%2Fwidgets&…`, prints “Measured over the tasks in
acme/widgets.”, and survives reload.

Regression coverage: `ReportBoard.test.jsx` “offers the repos of the tasks
it is handed to scope the report by” (fails with the old `tasks={[]}`),
and e2e step `7d` in `saved-boards-walkthrough.spec.js`.

### Observations (not changed)

1. **Report backlog counts a standing session.** On the demo data the
   report's backlog says queued 4 while the Kanban's Queued column shows
   3: the fourth is task 12, the seeded `Director Session`, which the
   board hides (`hidesStandingSessions`) and the daemon's `backlogOf`
   counts. The seed parks it as `queued`; a live session normally sits
   in `awaiting_reply`. A difference in definitions rather than a defect.
2. **Boards list failure reads as “No boards yet”.** With
   `GET /api/boards` failing, the index shows “Boards 0 … No boards yet.
   New board” under the error banner, and a board deep link shows the
   same while keeping its URL. The poll recovers it on the next tick. The
   schedules and templates lists behave the same way, so this is the
   shell's general pattern rather than something boards should fix alone.
3. **Gantt panel `Open` lands on the task list**, carrying the narrowing
   but not the panel's range; per `PanelBoard.jsx` the list is the
   destination for three panel kinds by design.

## Automated tests

- `npx vitest run src/components/ReportBoard.test.jsx`: 18 passed
  (the new test fails with the fix reverted: 1 failed, 17 passed).
- `npx playwright test e2e/saved-boards-walkthrough.spec.js
  e2e/boards.spec.js e2e/board.spec.js e2e/gantt.spec.js
  e2e/boards_workflows.spec.js`: 46 passed (54.4 s).
