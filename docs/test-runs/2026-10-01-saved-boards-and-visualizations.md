# Walkthrough: Saved Boards and Task Visualizations

Date: 2026-10-01 (09:33–10:25 UTC)

Commit tested: `aeba486fa7c2` (base, `candidate` tip). Fixes from this run are
on `grain/task-1356` (`8c7240bd5`, `4e734e3f4`, `f71f89300`), and the parts of
the walkthrough that found them were run again on those builds.

## Environment

- Debian 12.15 sandbox (`x86_64`), Go 1.26.6, Node.js 20.19.0, Vite 6.4.3,
  Playwright 1.62.1 with Chromium headless shell 151.
- The frontend was built with `make frontend` / `npm run build` into
  `pkg/ui/static`, the binary with `go build -o bin/grain ./cmd/grain`, and
  served by `bin/grain demo -addr 127.0.0.1:8420 -open=false` over its own
  throwaway embedded SQLite store, with `HOME` and the working directory set to
  a scratch directory.
- Real Chromium driven by Playwright scripts kept outside the repository
  (`/tmp/wt`, not committed). The viewport was 1440x900, plus one phone pass at
  390x844 with `hasTouch`/`isMobile`. Every page had listeners for console
  errors and warnings, `pageerror`, `requestfailed` and HTTP status ≥ 400.
- There was no `/home/debian/.gemini-api-key` in this sandbox, so no
  Gemini-backed tooling was used.
- Sandbox notes for later runs:
  - `npx playwright install` needs
    `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt` to get through
    the package-cache proxy, which the Makefile's own target already sets.
  - `make frontend` runs `npm ci --omit=dev`, which removes Playwright again.
    Run `npm ci` after it.

## Stubs and boundaries

- **State-repository reconciler.** "New board", "Edit board" and "Add panel"
  file a task against the state repo (`grain/config-default` in the demo). A
  board only appears once an agent's PR to `tables/board*.json` merges, and
  the demo has no agent or GitHub for that. The walkthrough checked the
  asynchronous half through the UI. It then stood in for the merge by creating
  the resulting boards with `POST /api/boards` (`WT Folder A1`/`A2`, `WT Dash`,
  `WT Report`, `WT Bad Panel`, and in e2e `WT Metrics …`/`WT Sections …`).
  This is the only stub. The boards were removed with `DELETE /api/boards/{id}`
  at the end, because the UI has no board delete.
- **Login.** The demo opens on a "Create account" dialog. The scripts created
  `wtuser` through that dialog and reused its session cookie.
- **GitHub.** Opening any task requests
  `GET /api/repos/<repo>/github-branches`, which returns 404 in the demo
  because it has no GitHub boundary, so the console logs one resource error.
  Expected for the demo and not counted as a finding.
- `GET /api/tasks net::ERR_ABORTED` lines come from in-flight polls cancelled
  by `page.goto` and are harmless.

## Steps and outcomes

1. **Boards index and rail.** Rail "Boards 3" → `/boards` lists Board
   (Kanban), Timeline, Report. After seeding, the count reads 8 in both the
   rail and the header. The `WT Folder` folder heading groups its boards after
   the loose ones, and the order matches the server's `orderKey`. The index
   offers no search or sort, which matches its design. Deep links to each
   board, Back/Forward and reload all keep their state.
2. **Asynchronous creation.** Create options → New board, opened from a list
   narrowed to `q=pagination`, prefilled `Add a board` with ``query:
   `q=pagination` `` and the full kind vocabulary. Create task filed task #13
   (queued, against `grain/config-default`) and returned to `/boards`. The
   board count stayed the same, as the async contract requires. Edit board
   (kanban) prefilled `Change the board "Board"` with the current row. Add
   panel (dashboard) listed every panel with its `order_key`.
3. **Folders and ordering.** Keyboard/tap pickup on "Move WT Dash" offered
   only legal slots (its own place left out) and filed it at the end of
   WT Folder. A mouse drag of "WT Report" onto the folder heading filed it at
   the top of the folder. Both persisted across reload and in
   `GET /api/boards`. A touch tap pickup at 390px did the same.
   - Observation: starting a drag adds an end-of-section drop zone above the
     folder heading. My first scripted drag used a box measured before the
     drag started, so it landed in that zone and moved the board to the end
     of the loose boards. Measuring the target after the drag starts works.
     A human re-aims, so this is not a defect.
4. **Kanban.**
   - In the Columns dialog, Move Running left → Save gave the order Proposed,
     Running, Queued…, and it persisted across reload (kept in localStorage,
     as the dialog says).
   - In Queued, keyboard pickup of task 12 offered "Move here, above …"
     slots. Moving it above task 2, and a mouse drag of task 11 to the top,
     both persisted across reload.
   - A card alone in its column says "nowhere else to put it".
   - Clicking a card opens `/tasks/N`, and Escape returns to the board.
5. **Timeline.**
   - Range options are 24h/7d/30d/90d/Everything; lanes are none/repo/state/
     author. The 24h + By repo view showed the today marker (`.gantt-now`),
     lanes `acme/widgets` and `grain/config-default`, and "2 tasks outside
     this range". The choice survived reload (localStorage, per browser by
     design, so it is not in the URL; the narrowing is).
   - Clicking a bar opened `/tasks/3`, and Escape returned to
     `/boards/board-2`.
   - The timeline draws no dependency links. Nothing in GanttChart renders
     them, so there was nothing to check.
6. **Dashboard.**
   - Typing "pagination" in "Search this dashboard…" narrowed the inheriting
     panels (States, Inherit list, Week: 10 → 2). The scoped panels (Running
     only, Nothing matches, Widgets metrics) kept their own queries.
   - Keep appeared only on the inheriting panels. The narrowing survived
     reload. Open on a panel went to `/?q=pagination&…` with the same count,
     and Back returned to the board.
   - An empty panel says "Nothing here right now." A row in a panel opens the
     task.
   - Keyboard pickup moved "Week" to the top. The persisted `orderKey` was
     -1048576, and the order survived reload. Enter on a focused handle picks
     a panel up, and Escape cancels.
   - **Bug 1 found here:** see the first finding below.
7. **Report.**
   - The scope note said "Measured over the tasks in acme/widgets.". Typing a
     search added a "not in force: the search for "pagination"" note.
   - The Window picker re-asked the server (header 7d → 30d). It is
     component state by design, so it does not survive reload.
   - The "task 2" link in Backlog opens the task.
   - Clear empties the narrowing to the bare `/boards/board-7`. Reloading that
     reapplies the board's own query, the same as on every board kind.
   - **Bug 2 found here:** see the second finding below.
8. **Delete and confirmation.** The UI has no board delete. On the throwaway
   task #13, Close takes effect at once with no confirmation and offers
   Reopen, which put it back to queued, so it is reversible. Discard draft on
   a throwaway New board draft cleared the form.
9. **Back/Forward around tasks.** **Bug 3 found here:** see the third
   finding below.
10. **Phone (390x844, touch).** `/boards`, kanban, timeline, dashboard and
    report had no horizontal overflow. Tap pickup on the boards index worked,
    and the dashboard metrics showed 11/3.

## Findings

### Fixed in this task

1. **Dashboard throughput panel always printed "0 filed / 0 completed".**
   - Reproduction: in a dashboard with a `metrics` panel over `acme/widgets`,
     `/api/metrics` answered `throughput.tasksFiled: 11, tasksCompleted: 3`
     and the panel showed `0` beside a sparkline whose latest value was 9.
   - Cause: `PanelBoard.jsx` read `throughput.filed`/`completed`, which only
     the buckets carry. The unit-test stub used the same wrong shape, which
     is what hid the bug.
   - Fix: read `tasksFiled`/`tasksCompleted`, and correct the stub to the
     shape `pkg/ui/metrics.go` really sends.
   - Regression: e2e `6b` compares the panel to the live response. It failed
     on the old bundle (`Expected "11", Received "0"`) and passes on the
     fixed one.
2. **Report board: the "cycles" (Scheduling) section never rendered, and
   sections with no data left a bare heading.**
   - Reproduction: a report board with sections `throughput`, `backlog`,
     `merges` ("Merges") and `cycles` ("Ticks") drew three `.report-section`
     items. "Merges" was only its title, and "Ticks" was absent.
   - Cause: `cycles` is in `REPORT_SECTIONS`, `model.ReportSections` and the
     New board prompt, and `/api/metrics` serves it, but `ReportBoard` had no
     body for it. Every section also draws nothing when its window is empty,
     which suits the Metrics pane but leaves a handle and title over nothing
     on a board.
   - Fix: add `CyclesSection` (tick, interval, dispatch wait, each
     reconciler's share, and how many ticks committed nothing). A board now
     prints "<Section>: nothing to report over this window." under a named
     section whose body drew nothing. The Metrics pane is unchanged.
   - Regression: three ReportBoard unit tests, which fail on the old
     component, one Metrics-pane unit test, and e2e `7b`.
3. **Back/Forward history was rewritten around a task deep link.**
   - Reproduction: `/boards/board-1` → click a card (`/tasks/2`) → reload →
     Back went to `/` instead of the board. Forward onto `/tasks/2` raised
     `history.length` from 3 to 5, which also discards the forward stack.
   - Cause: App's URL-sync effect ran while `openTask` was still fetching,
     with `openTaskId` null. It replaced the address with the page behind the
     task (on mount) or pushed it (on popstate), and then pushed `/tasks/N`
     again.
   - Fix: App tracks the task the URL asked for while it loads, seeded from
     the URL at mount, and the effect waits for it.
   - Regression: two `App.tasks` unit tests, which fail on the old App.jsx,
     and e2e `4b` (board → card → Back/Forward/reload → Back lands on the
     board, with `history.length` unchanged).

### Proposed as follow-ups (`branch: candidate`)

- **Keyboard access to tasks.** List rows, kanban cards and timeline bars all
  have `tabIndex` -1 and no key handler. Only the move handle and checkbox
  take focus, so a keyboard-only user cannot open a task from any board
  view. This needs a design choice across shared components.
- **Unknown filter attributes are dropped silently.** A panel with
  `f=nosuchattr:any:x` is accepted (201) and shows every task, closed ones
  included, with no notice. `/?f=nosuchattr:any:x` behaves the same, so this
  is not specific to boards.

## Automated verification

- Vitest, whole suite: 156 files, 2678 tests passed.
- Playwright on chromium: 35 passed across `saved-boards-walkthrough.spec.js` (11, including the new `4b`, `6b` and `7b`), `boards.spec.js`, `board.spec.js` and `gantt.spec.js`.
- Grain CI on `f71f893` passed: frontend, go, installer, lint, package
  cache, terraform and ui e2e.
- No screenshots, traces or other browser artifacts are committed.
