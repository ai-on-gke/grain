# Saved boards and task visualizations walkthrough -- 2026-11-08 (task 3612)

A hand walkthrough of saved boards and every board presentation (list,
kanban, timeline/gantt, tree, dashboard of panels, report) in a real
Chromium driven by Playwright, against a real `grain demo` server over a
throwaway embedded store. Two earlier reports for today already exist
(`2026-11-08-saved-boards-and-visualizations.md`, task 3459, and
`...-task-3355.md`); this one is suffixed with its own task number so
both stay intact, following the `-task-NNNN` precedent on `main`.

Three bugs were found, reproduced deterministically, fixed in this
change and covered by unit and Playwright regression tests (section 3).

## 1. What was tested

| | |
| --- | --- |
| Walkthrough commit | `8ee9b97e` (`main`, 2026-11-08 11:50 UTC) |
| Fix verified on | `33a44581` (`main` at 12:23 UTC; none of the files this change touches differ between the two) |
| Server | `grain demo -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci`, `TMPDIR` a throwaway directory, so the embedded store was new and deleted afterwards |
| Frontend | `cd ui && npm ci && npm run build` (the production bundle `grain` embeds), served by that binary |
| Browser | Playwright `@playwright/test` ^1.62.1, Chromium 1234 headless; desktop 1440x900 and `iPhone 13` (390x844, touch, coarse pointer) |
| Toolchain | Go 1.26.10, Node 20.19.0, Debian Linux 6.1 |
| Gemini | `/home/debian/.gemini-api-key` is absent in this sandbox, so `LOOK=1` was not used; layout was checked with bounding-box measurements instead |

Sign-in was the real access-code gate (`Continue`), then every step went
through visible controls. The walkthrough script and its JSON results
lived under `/var/tmp` and are not committed; no screenshot is committed.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub | `-github-sim` | no GitHub access from the sandbox |
| CI | `-fake-ci`, `-base-branch-ci` | no CI runner |
| Agents / model | the demo's seeded tasks; nothing was run by a model | no model key; board creation is a task, which is verified up to the filed task |
| A signed-in `grain/viewer` | `GET /api/config`'s `access` replaced with the viewer preset by a Playwright route (the same thing `ui/e2e/role-gated-actions.spec.js`'s `asViewer` and `screenshot.mjs --access viewer` do) | the single-tenant demo has only the unlimited access code; this shows which controls a role is offered, while the server still answers as the access code |
| Failure states | `page.route` returning 500 for `/api/boards`, `/api/tasks` and `/api/metrics` on one page only | to see the error rendering and Retry; removed before the next step |

Seeded objects created by this run (boards `WT *`, two `Triage` boards,
throwaway tasks) were deleted at the end, through the API.

## 2. Steps and outcomes

Every item below was done in the browser at desktop width, and the
navigation, kanban, dashboard and report steps again at phone width.
Zero uncaught page errors and no horizontal overflow at either width.
Failed requests seen: the conditional task polls that answer `304`
(Chromium logs them as `net::ERR_ABORTED`, expected with `If-None-Match`
and `cache: "no-store"`), and one known `404` noted in section 4.

### 2.1 Boards index and navigation

- `/boards` from the rail's Boards entry and cold from the URL: the
  seeded boards (Timeline, Board, Report, Tree ...) under their folders,
  loose boards first, each with its kind chip and description or
  "every task".
- Created folders `WT Folder A/B` with boards through the API, reloaded:
  folders and order as stored.
- Reorder with a mouse drag inside a section below the first, and into
  another folder by dropping on its heading; reorder with the handle
  (tap/keyboard pick-up, "Move here, ..." slots, Escape cancels). Both
  persisted across a reload and a cold load of `/boards`.
- Opening a board from its row and from its name link; Back returns to
  `/boards` with focus on that board's link; Forward reopens the board;
  reload keeps the board and its narrowing in the URL (`?f=...`).
- Pinned a board through Customize sidebar (Type Board, Item Timeline,
  Pin item, Save): the rail entry opens it, survives a reload.
- `/api/boards` failing: the page says the boards could not be loaded,
  not "No boards yet", and Retry recovers.
- **Bug 3** found here (two folders each holding a `Triage`).

### 2.2 Requesting a new board

- Rail's Create options menu, New board; and the Boards index header's
  New board: both open the new-task drawer pre-filled "Add a board"
  with the board template text (kind, query, notes). Filed it; the
  resulting task targets the config repository and appears in the task
  list as a proposal waiting for approval (`approved` unset) -- the
  asynchronous contract (a board is a task that lands a state-repo
  row), not a direct write.
- On a narrowed list, New board carries the narrowing as the query; on a
  report with a picked window, the prompt names the window.
- Discard draft clears the form in place; Close dialog closes it.
- As `grain/viewer`: header New board / Edit board are disabled with the
  "Needs tasks:write ..." tooltip -- but the Create options menu's New
  board was not (**Bug 1**).

### 2.3 List and kanban

- List board rows and kanban cards open task detail (click, Enter on
  the title link, a card clicked off its title); Back returns to the
  board with focus on the row.
- Columns editor (toolbar "Columns", and "Edit columns" from the
  no-column summary): rename, add, remove, move left/right; layout kept
  across a reload.
- Cards moved between columns with a mouse drag and with the handle
  (pick-up then "Move here, above task N, “...”, in Waiting"); state
  and order persisted across a reload.
- Merge-queue column reads **Merging**; a blocked task's card shows the
  blocked state oval (`.badge-blocked`, labelled); a held proposal's
  card carries "held for approval"; an imported issue's card carries
  `imported`.
- A board with its own columns where a column collects only
  `awaiting_submit`: the "in no column" summary names "Asked for a
  reply" separately from "Asked to submit" (and the converse).

### 2.4 Timeline / gantt

- Range (24h, 7d, 30d, Everything) and lanes (none, repo, state)
  changed; bars, dependency arrows and the today marker drawn; both
  settings and the URL survived a reload.
- A bar opens the task; Back returns to the chart with focus on that
  row's link. Keyboard: Tab to a row, Enter opens it.

### 2.5 Tree

- The `tree` board nests tasks under the task that filed them (a parent/child
  chain created for the run), its query narrows it, the narrowing and
  its own search survive a reload, and a row opens task detail.

### 2.6 Dashboard (panels)

- Created `WT Dash` (`f=repo:any:acme/widgets`) with list, states,
  timeline and throughput panels through the API (the UI's Add panel
  files a task, checked to the filed task).
- Whole-board narrowing from the toolbar narrows every inheriting panel;
  a panel with its own query keeps it; Clear returns to the board's
  question. Each panel's Open link lands on the matching list (or the
  Metrics pane for throughput) carrying its narrowing.
- Reordered panels by mouse drag and by handle; persisted across reload.
- Keep on a narrowed panel files the "Narrow the panel ..." task.
- Loading ("Loading…"), empty ("This dashboard has no panels yet."),
  tasks-failed (per-panel error + Retry while metrics stays up) and
  metrics-failed ("The metrics report could not be loaded.") rendered.
- **Bug 2** found here (Keep on the throughput panel after typing a
  search), and **Bug 1**'s other half (viewer still offered Add panel
  and Keep).

### 2.7 Report

- Window picker changed between 24h/7d/30d/90d: `?window=` written to
  the URL, kept on reload; Refresh refetches `/api/metrics`.
- A report board naming 13 sections (throughput, latency, runs/capacity,
  backlog, merges, prompts, tools, agents, sandboxes, checks, cycles,
  ci_queueing, github_ci): each section drawn or accounted for; an
  un-sectioned board prints the whole report and omits silent sections.
- Sections reordered by handle; persisted.
- Repo scoping from the filter editor narrows the report; a dropped
  condition (search, state) is named on the page.
- Oldest-waiting task references open the task; backlog counts are real
  links whose lists show the same number of rows.
- `?window=14d` falls back to Last 7 days (only preset windows are read
  from `?window=`), while `f=created:within:14` reads as Last 14 days --
  deliberate (`paths.js`, `ReportBoard.jsx`).

### 2.8 Delete and confirmation

The boards UI exposes no delete for boards, panels or sections (removal
is a task like every other board change), so nothing destructive was
offered to exercise. Throwaway boards and tasks were removed through the
API at the end.

## 3. Bugs found and fixed

### Bug 1 -- task-filing controls offered to a role without `tasks:write`

Reproduction: as `grain/viewer`, open a dashboard and narrow it (e.g.
`/boards/<panels-board>?f=repo:any:acme/widgets&q=retry`). BoardPage
disables Edit board and New board with the "Needs tasks:write" tooltip;
directly underneath, **Add panel** and each panel's **Keep** stayed
enabled. In the rail, Create options showed New task disabled and
**New board** under it enabled. Each opened the new-task drawer whose
Create task the viewer cannot press. (Task 3564's role sweep did not
reach these: the demo seeds no dashboard, and the menu was never opened.)

Fix: `Sidebar.jsx` gives New board the same `disabled`/`title` as New
task; `PanelBoard.jsx` wraps Add panel and Keep in `AccessGate
scope="tasks:write"`. `style.css` gives a disabled Keep a muted look and
moves Keep's auto margin to the gate's wrapper (including the
coarse-pointer negative margins), so nothing moves. Measured: desktop
Add panel/Keep/Open within 1.5px between operator and viewer
(e2e), and on a touch phone identical boxes (Keep 56.5x44 at x 236.4,
Open 60.1x44 at x 300.9, header 45px tall) for both roles.

### Bug 2 -- Keep offered on a throughput panel whose report would not change

Reproduction: dashboard with `query: f=repo:any:acme/widgets` and an
inheriting throughput panel `range: "7d"`. Type `retry` in "Search this
dashboard…". The panel keeps fetching
`/api/metrics?repo=acme%2Fwidgets&window=7d&buckets=24` unchanged, yet it
showed **Keep**, which would file a task to write a query that changes
nothing on screen. Same on an unscoped board narrowed by
`created:within:30` when the panel's own `range` wins.

Cause: `panelUsesNarrowing` asked only whether the narrowing carried any
report scope at all, not whether it differed from the board's own.

Fix: `panelUsesNarrowing(panel, narrowing, baseline)` compares the
report path the panel would fetch (scope plus `panelMetricsWindow`, the
string `MetricsPanel` keys its fetch on) against the one for the
dashboard's own question (`boardNarrowing(board)` or the deployment
default -- `keptQuery`'s comparison point); `PanelBoard` passes it. The
two-argument form behaves as before.

### Bug 3 -- duplicate accessible names for same-named boards in two folders

Reproduction: boards `Triage` in folder `Ops` and `Triage` in `Zeta`.
On `/boards` both handles were named "Move Triage", and with any board
picked up two slots were both "Move here, above Triage" while leading
into different folders (the end-of-folder slots already said which).

Fix: `BoardList.jsx` names a board by its name when unique (every
existing label unchanged), otherwise "Triage, in Ops" / "Triage,
outside every folder", with the id as a last tie-break for two of one
name in one folder.

### Regression coverage

- Unit (Vitest): `panels.test.js` (baseline/range cases),
  `PanelBoard.test.jsx` (Keep on throughput; viewer Add panel/Keep
  disabled), `BoardsPage.test.jsx` (same-named handles and slots),
  `Sidebar.test.jsx` (viewer New board in Create options). All five fail
  on the unfixed source and pass with the fix.
- Playwright against `grain demo`: `role-gated-actions.spec.js` (viewer
  menu + dashboard controls disabled, gates filled, positions within
  1.5px of the allowed ones); `saved-boards-walkthrough.spec.js` 1c
  (distinct handle and slot names) and 6f (Keep on throughput only when
  the report changes).
- Run: the eight board/visualization/role specs, 86 passed; full Vitest
  suite and `go test ./pkg/ui/... ./tests/hygiene/...` (results in the
  pull request's closing note).

## 4. Other observations (not changed)

- Opening a task that targets the internal config repository requests
  `/api/repos/grain/config/github-branches` and gets a 404 ("branch
  listing is not available for the internal config repository"); already
  recorded as O1 in `2026-11-07-rbac-roles-ui.md`, outside boards.
- `?window=` accepting only the four preset windows is by design (above).
