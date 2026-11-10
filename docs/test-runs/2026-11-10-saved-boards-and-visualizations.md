# Saved boards and task visualizations walkthrough — 2026-11-10 (task 3932)

An interactive walkthrough of saved boards and every board presentation
(`list`, `kanban`, `gantt` / timeline, `tree`, `panels` dashboard, and
`report`) driven in a real Chromium browser via Playwright against a real
`grain demo` server backed by a throwaway SQLite store. Prior walkthrough
reports through `2026-11-09-saved-boards-and-visualizations.md` were read
first so this run verified every standing coverage item and probed deeper
into cross-folder board accessibility, keyboard focus across view-replacing
navigations and identical sibling reorders, real IAP-verified viewer RBAC,
and phone-width overlay layout.

Four bugs were found, reproduced deterministically in Chromium, fixed in
this change, visually checked at desktop (`1440×900`) and phone (`390×844`)
widths in light and dark, and covered by Vitest unit and Playwright E2E
regression tests (section 3).

## 1. Environment and commit tested

| Item | Value |
| --- | --- |
| Base commit | `24788596f` (`main`, 2026-11-10 04:05 UTC), then the same tree with the fixes in section 3 |
| Server | `./bin/grain demo -addr 127.0.0.1:0 -open=false -access-code-file <tmp> -github-sim -fake-ci -human-review -merge-queue-states -base-branch-ci -ui-iap-audience https://grain.walk.local -ui-iap-issuer https://iap.walk.local -ui-iap-keys-url <loopback-jwk>` with `TMPDIR` pointed at a throwaway directory |
| Frontend | `cd ui && npm ci && npm run build` (embedded into `./bin/grain` via `make grain`) |
| Browser | Playwright `@playwright/test` Chromium 1234 headless; desktop `1440×900` and `iPhone 13` (`390×844`, touch), in light and dark |
| Toolchain | Go `1.26.10`, Node `v20.19.0`, npm `10.8.2`, Debian Linux (`x86_64`) |
| Gemini key | `/home/debian/.gemini-api-key` is absent in this sandbox; layout and screenshots were verified via bounding boxes and PNG pixel diffs |

Sign-in for the operator context went through the visible access-code gate
(`Access code` input + `Continue`). All walkthrough scripts, temporary
stores, and screenshots lived under `/var/tmp/grain-walk-3932` and were not
committed.

### Stubs and stand-ins

| Boundary | Stand-in | Why |
| --- | --- | --- |
| GitHub | `-github-sim` | Sandbox has no external GitHub access |
| CI & base-branch CI | `-fake-ci`, `-base-branch-ci` | No external CI runner inside the walkthrough demo |
| Identity-aware proxy (`grain/viewer`) | Loopback ES256 JWK HTTP server (`-ui-iap-keys-url`) signing a real `X-Goog-IAP-JWT-Assertion` for `viewer@example.com` after registering `user:viewer@example.com` with role `grain/viewer` via `POST /api/access/principals` | Exercises both real server-computed `GET /api/config` scopes and real HTTP `403` enforcement without stubbing `/api/config` in the browser |
| Agent execution for board creation | Verified up to the filed `grain/config` task and proposal (`WT Rail Board Proposal`, `WT Header Board Request`); throwaway boards for presentation walkthroughs were seeded via `POST /api/boards` and deleted via `DELETE /api/boards/{id}` before exit | `grain demo` deliberately runs no model agent; the UI's asynchronous board-request contract was verified separately (section 2.2) |
| Held proposal, `keep_running` task & twin-titled cards | Filed throwaway tasks via `POST /api/tasks`: held proposal (`proposedBy: "8"`, `holdForApproval: true`, `proposalKey: "wt-held-card"`), standing task (`keepRunning: true`), and two proposals sharing the title `"Duplicate card title"` | Only an agent's `propose_task` creates a held proposal, and `grain demo` seeds neither `keepRunning: true` nor duplicate-titled proposals |
| API failure & slow-load states | Scoped `page.route` handlers on isolated pages returning `500` for `GET /api/boards`, `GET /api/tasks`, and `GET /api/metrics`, plus a `600 ms` delay on `GET /api/metrics`, cleared to verify `Retry` / `Refresh` recovery | Real local SQLite answers in milliseconds and does not fail on demand |

## 2. Steps and outcomes

Across desktop (`1440×900`), viewer (`1440×900`), and phone (`390×844`)
contexts there were **zero** uncaught `pageerror`s and **zero** unexpected
`>= 400` API responses (only the expected pre-sign-in `401` on the access
code gate and the intentional `500` fault-injection routes).

### 2.1 Boards index (`/boards`), rail, folders, ordering, history & load failure

- Clicked rail `Boards` (badge `4`) → `/boards`: rendered header count `4`
  and seeded loose boards `Board` (`Kanban`), `Timeline` (`Timeline`),
  `Report` (`Report`), and `Tree` (`Tree`). Confirmed `/boards` offers no
  task search or sort controls of its own (its prose directs narrowing to
  the task list).
- Focused `Timeline` link on `/boards` and pressed `Enter` → `/boards/board-2`;
  browser `Back` returned to `/boards` with focus restored to `Timeline`'s
  link; `Forward` returned to `/boards/board-2`; `Reload` kept `Timeline`.
- Opened `/boards/board-1`, `/boards/board-2`, `/boards/board-3`, and
  `/boards/board-4` cold by direct URL; opened nonexistent `/boards/board-999`
  and confirmed graceful fallback to `/boards` with error banner
  `no board "board-999"`; opened legacy `/board` and `/gantt` and confirmed
  both redirect to `/`.
- Seeded folders `WT Ops` and `WT Zeta` with three boards all named `Triage`
  (loose, in `WT Ops`, and in `WT Zeta`), plus `WT Ops Queue`,
  `WT Zeta Alpha`, and `WT Zeta Beta`:
  - **Bug 1 & Bug 2 found here** on the unfixed build: `/boards` `RowLink`s,
    `Customize sidebar` board options, and pinned rail entries all carried
    identical `"Triage"` accessible names, and long disambiguated board
    names overlapped the action buttons in `Customize sidebar` at `390 px`
    (fixed in section 3).
  - On the fixed build, verified distinct accessible names everywhere:
    `/boards` links (`"Triage, outside every folder"`, `"Triage, in WT Ops"`,
    `"Triage, in WT Zeta"`), `Move` handles and `Move here, above …` slots,
    `Customize sidebar` `Item` options and `Customize …` / `Unpin …` buttons,
    and pinned sidebar rail buttons (both expanded and collapsed). Opened
    both pinned `Triage` boards directly from the rail.
- Ordering:
  - Dragged `WT Zeta Beta` onto `WT Zeta Alpha` by mouse inside `WT Zeta`
    (the second folder section below loose and `WT Ops`): order updated to
    `WT Zeta Beta, WT Zeta Alpha` and persisted across reload in
    `GET /api/boards` (`orderKey`).
  - Picked up `WT Ops Queue` from the keyboard (`Enter` on
    `Move WT Ops Queue`), verified `Escape` cancelled pickup and returned
    focus to `Move WT Ops Queue`, picked it up again, moved it via
    `Move here, at the end of WT Zeta`, and confirmed focus stayed on
    `Move WT Ops Queue` in its new folder and persisted across reload.
- Load failure: when `GET /api/boards` returned `500` (`database disk image
  is malformed`), `/boards` rendered `role="alert"` reading
  `"The boards could not be loaded (database disk image is malformed)."` with
  a `Retry` button, omitted the header count badge, and did **not** say
  `"No boards yet."`. Clicking `Retry` after lifting the fault loaded all
  boards.

### 2.2 Asynchronous board creation & `grain/viewer` role gating

- Narrowed `/` to `/?f=repo:any:acme/widgets&q=retry`, opened rail
  `Create options` → `New board` → `/new`: verified title `"Add a board"`,
  target repository `grain/config`, carried narrowing (`acme/widgets` and
  `retry`) and `tables/board.json` instructions in `Description`. Unticked
  `Queue immediately` and submitted `"WT Rail Board Proposal"` → created
  task `23` in state `proposed` targeting `grain/config`.
- Clicked `New board` in `/boards` page header and submitted
  `"WT Header Board Request"` with `Queue immediately` ticked → created task
  `24` in state `queued` (`approved: true`) targeting `grain/config`, while
  `/boards` count stayed `10` (verifying the asynchronous creation contract:
  a board appears only once an agent's state-repo change lands).
- Signed in a second browser context as `viewer@example.com` (`grain/viewer`
  via real IAP ES256 JWT):
  - `GET /api/config` resolved `role: "grain/viewer"` without `tasks:write`
    or `boards:write`; direct `POST /api/boards`, `PATCH /api/boards/board-1`,
    `POST /api/tasks`, and `POST /api/tasks/reorder` all returned HTTP `403`.
  - Rail `New task` button, `Create options` → `New task` and `New board`,
    `/boards` header `New board`, `BoardPage` `Edit board` and `New board`,
    and dashboard `Add panel` and `Keep` buttons were all disabled with the
    `Needs tasks:write, which your role grain/viewer does not grant.`
    explanation.
  - `/boards`, `list` boards, `kanban` boards, `panels` dashboards, and
    `report` boards rendered `0` `.task-drag-handle` buttons and `0`
    `draggable="true"` items for the viewer.

### 2.3 List and kanban

- Opened scoped list board (`f=state:any:queued`): verified `keeps running`
  marker on the `keepRunning: true` task row, opened task detail from row
  link via `Enter`, and confirmed `Escape` restored focus to that row link.
- Opened seeded `Board` (`/boards/board-1`):
  - Default columns read `Proposed`, `Waiting`, `Running`, `Asked`,
    `Failed`, **`Merging`** (containing merge-queued tasks `5` and `20`).
  - Blocked task `11` (`Extract pagination query helper`) rendered the
    `.badge-blocked` state oval (`aria-label="Blocked on: #8 (running)"`).
  - `keepRunning: true` card rendered its `keeps running` marker chip.
  - Held proposal card rendered `.chip-held` (`"held for approval by 8"`).
  - Imported GitHub issue `12` rendered `.chip-imported` (`"imported"`).
- Picked up the second of two `"Duplicate card title"` cards in `Proposed`
  via `Move task <id>`: confirmed move slots identify tasks by id and title
  (`Move here, above task 27, “Duplicate card title”, in Proposed`), moved
  card `28` above `27`, dragged card `2` above card `22` by mouse in
  `Waiting`, and confirmed both backlog orders persisted across reload.
- Column editor (`Columns`): renamed `Proposed` to `Ideas WT`, verified
  pressing `Escape` while dirty triggered confirmation (dismissing kept the
  dialog open), moved `Ideas WT` right, saved, confirmed column order
  persisted across reload in `localStorage`, then restored defaults via
  `Reset to default` + `Save`.
- Custom-column boards:
  - `WT Submit Only` (`states: ["awaiting_submit"]`) hid the `Columns`
    button and summarized hidden tasks as
    `"17 tasks in no column (Proposed, Waiting, Running, Asked for a reply, Merging)."`.
  - `WT Reply Only` (`states: ["awaiting_reply"]`) summarized hidden tasks as
    `"25 tasks in no column (Proposed, Waiting, Running, Asked to submit, Merging, Deferred, Closed)."`.

### 2.4 Timeline and gantt

- Opened seeded `Timeline` (`/boards/board-2`): verified `.gantt-now` today
  marker, bar spans, and blocked bar badge on task `11`.
- Exercised `Range` (`Last 24 hours`, `Last 7 days`, `Last 30 days`,
  `Last 90 days`, `Everything`) and `Lanes` (`No lanes`, `By repo`,
  `By state`, `By author`).
- Under `Range = Everything` and `Lanes = By state`, verified rendered lane
  order is `["Proposed", "Waiting", "Running", "Asked", "Merging", "Deferred", "Closed"]` —
  confirming **`Asked` is ordered immediately after `Running`**.
- Under `Last 24 hours`, verified `"3 tasks outside this range."` note; under
  an unmatched search, verified
  `"No tasks match this search and these filters in this range."`.
- Clicked a `.gantt-bar` to open task detail and confirmed `Escape` / `Back`
  returned focus to that row's `.row-link`; confirmed `Last 30 days` + `By state`
  (`localStorage`) and `?q=pagination` (URL) both survived reload.

### 2.5 Tree

- Opened scoped `WT Widgets Tree` (`query: "f=repo:any:acme/widgets"`):
  confirmed follow-up tasks nested under their filing parent (`22` under
  `21`; `11` and the held proposal under `8`; `1`, `3`, and `8` under
  standing director session `13`).
- Narrowed further with search `"workspace"` (`?q=workspace&f=repo:any:acme/widgets`),
  reloaded, and confirmed both the board's saved repo filter and the typed
  search persisted; unmatched search rendered
  `"No tasks match this search and these filters."`.
- **Bug 3 found here** on the unfixed build: opening standing session `13`
  from `Tree` (`/boards/board-4`) navigated to `/session/director`
  (unmounting `TaskTreeBoard`), and pressing browser `Back` dropped focus on
  `<body>`. On the fixed build, `Back` restored focus to
  `.task-tree-id[href="/tasks/13"]`.

### 2.6 Dashboard (`panels`)

- Opened `WT Ops Dashboard` (six panels: `states`, two untitled inheriting
  `list` panels, `list` `"Running only"` with `query: "f=state:any:running"`,
  `gantt` `"Week timeline"` with `range: "7d"`, and full-width `metrics`
  `Throughput` with `range: "7d"`).
- Whole-board narrowing:
  - Typing `"retry"` (`?q=retry`) narrowed the four inheriting task panels
    and offered `Keep` on each of them, kept `"Running only"` unchanged
    without `Keep`, and **did not offer `Keep` on `Throughput`** (since text
    search does not alter `/api/metrics`). Narrowing by `f=state:any:queued`
    likewise suppressed `Keep` on `Throughput`.
  - Narrowing by `?f=repo:any:acme/widgets` scoped `Throughput` to
    `acme/widgets` and **offered `Keep` on `Throughput`** alongside the
    inheriting task panels.
- Panel reordering:
  - **Bug 4 found here** on the unfixed build: keyboard-moving the second of
    two identical untitled `{ kind: "list" }` panels above the first updated
    the order on the server, but left keyboard focus on the sibling pushed
    into the old slot (`Move panel 3, “Task list”`) rather than following the
    moved panel to `Move panel 2, “Task list”` (fixed in section 3).
  - On the fixed build, both mouse drag and keyboard slot moves updated
    `orderKey`, persisted across reload, and kept focus on the moved panel's
    own handle.
- Followed `Open` on the second identical `list` panel, `Open` on
  `Throughput` (`/system/metrics?...`), and a `Proposed` state count link on
  the `states` panel; browser `Back` from each restored focus to the exact
  originating link.
- Verified empty dashboard (`"This dashboard has no panels yet. “Add panel” files the task that adds one."`)
  and `/api/tasks` `500` error (`"The tasks could not be loaded (tasks 500)."`
  with `Retry` recovering task panels while `Throughput` remained rendered).

### 2.7 Report

- Opened seeded whole-report `Report` (`/boards/board-3`): rendered active
  sections (`Throughput`, `Latency`, `Capacity`, `Backlog`, `GitHub CI`) and
  **omitted silent sections** with zero `"nothing to report"` notes.
- Opened explicit 9-section `WT Nine Section Report` (`throughput`,
  `latency`, `backlog`, `merges`, `prompts`, `tools`, `agents`, `sandboxes`,
  `checks`): rendered `Throughput`, `Latency`, and `Backlog`, and explicitly
  accounted for all six silent sections with
  `"<Section>: nothing to report over this window."`.
- Changed `Window` to `Last 30 days` (`?window=30d` in URL), clicked
  `Refresh` (observed `GET /api/metrics?window=30d&buckets=24`), and
  confirmed `?window=30d` persisted across reload.
- Reordered `Backlog` above `Throughput` via `Move section 3, “Backlog”` →
  `Move here, above section 1, “Throughput”`: confirmed focus landed on
  `Move section 1, “Backlog”` and order persisted across reload.
- Verified repo and query scoping: `?f=repo:any:acme/widgets&f=state:any:running&q=retry`
  displayed `"Measured over the tasks in acme/widgets."` and named the state
  filter and search `"retry"` as not in force.
- Followed every `Backlog` count chip (`Proposed`, `Waiting`, `Running`,
  `Asked`, both unscoped and under `acme/widgets`) and `Oldest waiting task:
  task 2`: confirmed each chip's number matched the exact `.task-row` count
  on the filtered list it opened, and confirmed browser `Back` restored focus
  to the originating backlog chip or task reference link.
- Verified slow `/api/metrics` loading spinner (`aria-label="Loading the report"`),
  `500` error alert (`"The report could not be loaded (metrics 500). Refresh to try again."`),
  and `Refresh` recovery.

### 2.8 Phone layout (`390×844`) and destructive actions

- Checked `document.documentElement.scrollWidth` at `390×844` across `/boards`,
  `/boards/board-1`, `/boards/board-2`, `/boards/board-4`, dashboard, and
  report: `390 px` on every screen with zero horizontal overflow.
- Confirmed the UI exposes no destructive delete buttons for boards, panels,
  or report sections (removing them is an asynchronous config task);
  exercised dirty-close confirmation on `BoardColumnsOverlay` and cleaned up
  all throwaway boards via `DELETE /api/boards/{id}` before stopping the
  throwaway server.

## 3. Bugs found and fixed

### 3.1 Same-named boards in different folders carried duplicate accessible names on `/boards` links, pinned rail items, and `Customize sidebar`

- **Reproduction**: create two boards named `"Triage"`, one in folder `"Ops"`
  and one in folder `"Zeta"`. While task 3612 disambiguated `Move` handles
  and `Move here, above …` slots on `/boards`, the `<RowLink>` board links
  themselves both had accessible name `"Triage"`, `Customize sidebar` →
  `Type: Board` → `Item` listed two identical `"Triage"` options with no
  folder shown, pinning both created duplicate `"Customize Triage"` /
  `"Unpin Triage"` buttons, and the sidebar rail rendered two `"Triage"`
  buttons with identical accessible names.
- **Fix**: lifted folder/id disambiguation into `boardLabels(boards)` and
  `boardLabel(boards, board)` in `ui/src/boards.js` (leaving unique board
  names untouched) and wired it into `BoardList.jsx`, `BoardsPage.jsx`
  (`aria-label` on `RowLink` when disambiguated), `pinnedNav.js`
  (`getPinnedItemInfo`), `CustomizeSidebarOverlay.jsx` (`availableOptions`),
  and `Sidebar.jsx` (pinned board `ListItemButton` `label` and `aria-label`).

### 3.2 `Customize sidebar` pinned rows overlapped their action buttons at phone width (`390×844`)

- **Reproduction**: pin a board with a folder-qualified or long name (such as
  `"Triage, in Operations & Incident Triage"`), open `Customize sidebar` on
  `iPhone 13` (`390×844`), and measure the row's `.MuiListItemText-root` /
  `Board` `.MuiChip-root` against its 4-icon action bar. Because MUI
  `ListItem`'s `secondaryAction` is `position: absolute` while reserving only
  `48 px` of right padding (sized for one icon, not four), the text ran to
  `x = 270 px` underneath the action buttons starting at `x = 176 px`
  (`94 px` overlap).
- **Fix**: moved the 4-icon `.customize-pinned-actions` `<Stack>` in
  `CustomizeSidebarOverlay.jsx` into normal flex flow (`ml: "auto"`,
  `flexShrink: 0`) beside a `minWidth: 0, flex: "1 1 auto"` label/chip box so
  long labels truncate cleanly before the `Board` chip and action buttons at
  every viewport width (`gap = 63 px`, `0 px` overlap at `390×844`).

### 3.3 Browser `Back` to a `Tree` board after opening a standing session row dropped focus onto `<body>`

- **Reproduction**: open `/boards/board-4` (`Tree`), focus standing session
  `13`'s `.task-tree-id` link, press `Enter` (navigating to
  `/session/director`, which unmounts `TaskTreeBoard`), and press browser
  `Back`. `TaskTreeBoard` remounted with `document.activeElement === body`
  because `TaskTree.jsx` used a plain MUI `<Link>` rather than
  `useReturnFocus`.
- **Fix**: wired `useReturnFocus` into `TaskTree.jsx` via an opt-in
  `returnFocus` prop (passed as `"tree"` by `TaskTreeBoard.jsx`, and left
  `null` in a task detail pane's own `WorkTree` so inline trees never steal
  focus on mount), recording focus on both link clicks/Enter and row clicks.

### 3.4 Keyboard-moving one of two identical untitled panels or report sections left focus on the sibling at the old slot

- **Reproduction**: on a `panels` dashboard with two untitled `{ kind: "list" }`
  panels (or a `report` board with two identical section rows), focus
  `Move panel 2, “Task list”`, press `Enter`, then activate
  `Move here, above panel 1, “Task list”`. Once `PATCH /api/boards/{id}` and
  `GET /api/boards` settled, `childRenderKeys` assigned `#0` to index 0 and
  `#1` to index 1, leaving focus on `Move panel 2, “Task list”` (the other
  panel pushed into slot 2) instead of following the moved panel to slot 1.
- **Fix**: added `childDestinationIndex` and `childOrderSignature` in
  `ui/src/childOrder.js` and updated `PanelBoard.jsx` and `ReportBoard.jsx`
  to focus the destination slot's `.task-drag-handle` when the reordered
  child list lands (provided focus is still on a handle in that container or
  `<body>`).

### Layout, screenshots, and regression tests

- **Screenshots & pixel diff**: ran `ui/e2e/screenshot.mjs` before and after
  on `/boards`, `/boards/board-1`, `/boards/board-3`, and `/boards/board-4`
  at desktop (`1440×900`) and phone (`390×844`) in light and dark, plus
  before/after shots of `Customize sidebar` at `390×844` and `1440×900`.
  `/boards` at desktop (light and dark) and `/boards/board-1` at desktop
  light are byte-identical (`0` pixels diff); remaining diffs on seeded board
  pages are confined to live clock timestamps, running-dot animation frames,
  and non-deterministic demo CI run numbers, while `Customize sidebar` at
  `390×844` confirms clean truncation left of the `Board` chip and 4 action
  buttons.
- **Unit tests (Vitest)**: added coverage in `ui/src/boards.test.js`,
  `ui/src/components/BoardsPage.test.jsx`,
  `ui/src/components/CustomizeSidebarOverlay.test.jsx`,
  `ui/src/components/Sidebar.test.jsx`,
  `ui/src/components/TaskTreeBoard.test.jsx`,
  `ui/src/components/PanelBoard.test.jsx`, and
  `ui/src/components/ReportBoard.test.jsx`. Full Vitest suite: **260 files,
  4092 tests passed**.
- **End-to-end tests (Playwright)** & **Go tests**: added 3 E2E regression
  tests (including `390×844` bounding-box assertion) in
  `ui/e2e/board-return-focus.spec.js`. Ran 10 board/visualization/RBAC
  Playwright specs (**95 passed**) and `go test ./pkg/ui/... ./tests/hygiene/...`
  (**passed**).
