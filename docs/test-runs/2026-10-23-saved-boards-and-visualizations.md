# Saved boards and task visualizations walkthrough — 2026-10-23

Date: 2026-10-23, 01:24–02:15 UTC. Task: grain/eu/task-4672.

Commit tested: `785adac18357be0665f795716ae130ca331d26fb` (`Merge pull request #4456 from bwsalmon/grain/eu/task-4668`), the tip of `origin/candidate` at the time, checked out as `grain/eu/task-4672`.

Earlier reports in this series: `2026-10-22-saved-boards-and-visualizations.md` and the ones it lists. This run repeated their coverage from scratch over a new build and added: pinning boards to the rail, kanban columns that the board itself defines, moving boards between folders and between boards with equal `orderKey`s, whole-dashboard narrowing on a report, tap reordering at phone width, and closing a task this run had filed.

**Result:** no defects specific to boards or their views were found. There was one observation about all task views (§10). It is a design question rather than a safe, scoped fix, so it was proposed as a follow-up and not changed here. This run made no code changes.

## Environment

- Debian sandbox, `x86_64`. Go 1.26.6, Node.js 20.19.0, `@playwright/test` 1.62.1 from `ui/node_modules`, Playwright Chromium headless shell `chromium_headless_shell-1234`.
- Frontend: `cd ui && npm ci && npm run build`, which writes into `pkg/ui/static`.
- Server: `go build -o /tmp/grain ./cmd/grain`, which embeds that bundle. Run as `/tmp/grain demo -addr 127.0.0.1:8787 -open=false -access-code-file /tmp/wt/access-code` from the throwaway directory `/tmp/wt`, over `grain demo`'s embedded SQLite store and demo seed. The seed has 12 tasks: 11 in `acme/widgets` and one Director session.
- Browser: a real headless Chromium driven by Playwright scripts under `/tmp/wt` (not committed).
  - Viewports: 1440×1000 for desktop, and 390×844 with `hasTouch`/`isMobile` for phone and touch.
  - Every browser context signed in through the UI's own access-code screen: type the code in **Access code**, then press **Continue**.
  - Every script recorded `pageerror`s, `console` errors and responses with HTTP status ≥ 400.
- Gemini: `/home/debian/.gemini-api-key` is not present in this sandbox, so no Gemini-backed model or tooling was used. Nothing in this walkthrough needed one.
- Playwright Chromium was installed with `NODE_EXTRA_CA_CERTS=/usr/local/share/ca-certificates/grain-ca.crt npx playwright install chromium` (about 115 MiB).

## Stubs and stand-ins

1. **Board rows created through `POST /api/boards`.** This is a stand-in for an agent's merged `grain/config` pull request.
   - Why: the product's creation contract is asynchronous. **New board**, **Edit board**, **Add panel** and **Keep this narrowing** each file a task against `grain/config`; an agent then opens a pull request on `tables/board*.json`, and the board appears once that merges. A demo sandbox has no agent and no state repository to merge into.
   - So after verifying the UI workflow end to end (§2), the following throwaway board shapes were written through the real server's board endpoint. All were deleted again with `DELETE /api/boards/<id>` (204) at the end of the run.

   | id | name | kind | folder | query | other settings |
   |---|---|---|---|---|---|
   | `board-5` | WT List | `list` | Walkthrough | `f=state:any:running` | |
   | `board-6` | WT Kanban | `kanban` | Walkthrough | | columns `Triage` (proposed, queued) and `Active` (running, awaiting_reply) |
   | `board-7` | WT Timeline | `gantt` | Walkthrough | `f=state:any:running\|queued` | |
   | `board-8` | WT Dashboard | `panels` | Walkthrough | `f=repo:any:acme/widgets` | six panels, listed below |
   | `board-9` | WT Report | `report` | Walkthrough | `f=repo:any:acme/widgets&q=ignored` | sections `throughput`, `latency` (titled `Custom Latency`), `backlog`, `prompts`, `merges` |
   | `board-10` | WT Empty Dash | `panels` | Walkthrough | | no panels |
   | `board-11` | WT Tree | `tree` | Other | `f=state:any:queued\|running` | |

   The six `board-8` panels:
   - `States` (`states`)
   - `Inheriting List` (`list`, no query of its own)
   - `Scoped Running` (`list`, `f=state:any:running`)
   - `Metrics` (`metrics`, `30d`)
   - `Timeline` (`gantt`, `24h`, full width)
   - `Nothing` (`list`, `q=zzzznomatch`, third width)
2. **`page.route("**/api/metrics*")`.** Used in §8 only. It held each `/api/metrics` request for 1.5–2 s and then answered HTTP 500 `{"error":"synthetic metrics 500"}`, so the loading and error rendering of a report board and a dashboard metrics panel could be seen. Removed straight afterwards.
3. **No upstream GitHub.** Demo mode has no GitHub credential, so every task detail overlay's `GET /api/repos/<repo>/github-branches` returns 404, logged as a console error. This is expected in demo mode and is not counted as a failure. No other response ≥ 400 and no `pageerror` occurred anywhere in the run, apart from the deliberate metrics 500s in stub 2.

## Steps and outcomes

### 1. Boards index, rail, direct URLs and history

- **The rail's Boards entry** (`boards 4`) opened `/boards`. The page listed `Board` (kanban), `Timeline` (timeline), `Report` (report) and `Tree` (tree) with their descriptions, and the header count read `4`. The page offers no search or sort control of its own: boards are organised by folder and by drag or pickup order (see below).
- **Direct URLs:** loading `/boards/board-1` through `/boards/board-4` cold opened each view.
  - The browser tab title stays `grain`, or `<environment> — grain` when an environment name is set (`App.jsx`). That is by design. The 10-22 report's `Board — grain` titles do not apply to this build.
- **Retired and unknown URLs:**
  - `/t/default/boards` lands on `/`. The single-tenant reset (`80385c4be`) chose this deliberately: "an old team link now lands on the task list".
  - `/boards/nope` redirects to `/boards` with the alert `No board "nope"`.
- **Folders:** with the stand-in boards present, the index grouped them under `Other` and `Walkthrough`, and the count read `11` on both the index and the rail.
- **Moving boards by keyboard and tap** (`Move <board>`, then a `Move here, …` slot):
  - **Into a folder:** `WT Empty Dash` → `Move here, at the end of Other` filed it under Other (`PATCH` `folder: "Other"`). Focus stayed on its handle.
  - **To the top, out of its folder:** `WT Report` → `Move here, above Board` moved it to the top of the unfiled group.
  - **Between equal keys:** all four seeded boards have `orderKey` 0. `Tree` → `Move here, above Timeline` placed it between them; the UI renumbered the unfiled group with five `PATCH`es.
  - All three orders survived a reload, and `GET /api/boards` agreed.
- **Pinning boards to the rail:** **customize** → type `board` → `WT Dashboard`, then `Tree` → **Pin item** → **Save**. Both appeared in the rail.
  - Clicking `WT Dashboard` opened `/boards/board-8?f=repo:any:acme/widgets` and highlighted it in the rail; the highlight survived a reload.
  - Clicking `Tree`, then pressing Back, returned to the dashboard URL with the dashboard still highlighted.
- **History:** `/boards` → `WT Tree` → typed a search → reload → `WT Timeline`. Back went to the searched tree URL, Back again to `/boards`, and Forward restored the searched tree with the same rows.

### 2. Requesting a new board (the asynchronous creation contract)

- **New board from a narrowed list:** on `/?f=repo:any:acme/widgets&q=pagination`, the top bar's split caret offered `new task / new session / new board`. Choosing **new board** opened the New task form already filled in:
  - repository `grain/config`, title `Add a board`;
  - a description explaining `tables/board.json` and the other board tables, the kinds a board can be, and the id convention;
  - `- query: \`q=pagination&f=repo:any:acme/widgets\``, which is the narrowing on screen.
- Retitled the request to `WT4672 board for pagination search` and pressed **Create**. `POST /api/tasks` returned 201, creating task `13` with `state: queued` and `repo: grain/config`. The list behind kept its narrowing.
- `/tasks/13` opened the queued request. Its stored prompt carries the query line above.
- **Edit board on `Tree`** while reading `?q=retry` filed `Change the board "Tree"`. The description gives the board's current row and says it "is being read right now narrowed to `q=retry`". Escape closed the form without filing anything.
- **New board** on the `/boards` header opened the same form with the same `grain/config` target.
- **Destructive action, on this run's own throwaway only:** task 13's **Close** button closed it, **Reopen** brought it back to `queued`, and **Close** closed it again. Neither asks for confirmation; both are one click and reversible. No delete control exists anywhere in the boards UI.

### 3. List board (`WT List`)

- Opened on `?f=state:any:running`, showing tasks 3 and 8.
- Enter on a row's title link opened `/tasks/3`. Escape returned to the board URL with focus back on that title link.

### 4. Kanban

- **`Board` (the browser's own columns):** Proposed `1`, Queued `2, 11`, Running `3, 8`, Needs you `4, 6, 7`, Queued for merge `5`.
- **Moving a card by keyboard:** focus `Move task 11`, press Enter. That offered the one legal slot, `Move here, above Fix flaky retry test, in Queued`; pressing Enter on it sent `POST /api/tasks/reorder {"ids":["11"],"beforeId":"2"}`, and focus returned to `Move task 11`. The order survived a reload, and the flat list `/?f=state:any:queued` showed the same order.
- **Moving a card with the mouse:** dragging card 2 onto card 11 restored `2, 11`, and that survived a reload.
- **Disabled handle:** a card alone in its column has a disabled handle titled "Nowhere to move this: it is the only one here".
- **Cancelling a pickup:** Escape removed the slots and left focus on the handle.
- **Opening a card:** clicking card 4 opened `/tasks/4`. Escape returned to the board with focus on the card's title link.
- **Edit columns:**
  - Renamed column 1 to `Ideas`.
  - Escape with unsaved edits asked `You have unsaved changes here. Leave and discard them?`. Dismissing it kept the dialog open.
  - **+ Add column** → `Parked` with state `Deferred`, then `Move Parked left`, then **Save**. The board showed `Ideas | Queued | Running | Needs you | Parked (9) | Queued for merge`, stored as `grain.board.columns`, and the layout survived a reload.
  - Picking a state already used by another column moves it there, which leaves the old column `No states`. Saving that keeps an empty `Proposed` column.
  - Removing every column shows "No columns. Saving now puts the default board back." **Reset to default** restored the five default columns.
- **`WT Kanban` (columns the board defines):** columns `Triage: 2,1,11,13` and `Active: 3,4,7,8`, with the note "4 tasks in no column (Awaiting submit, Queued for merge, Deferred, Closed)". **Edit columns** is correctly absent here.
- **URL narrowing:** `/boards/board-1?q=pagination` narrowed the cards to 11, 13 and 8.

### 5. Timeline

- **Defaults:** Range `Last 7 days`, Lanes `No lanes`, the note "1 task outside this range.", and the today marker `.gantt-now` at `left: 100%`.
- **Dependencies:** the blocked row (task 11) carries a `Blocked` chip titled "Blocked on: #8 (running)".
- **Range and lanes:** Range `Last 24 hours` with Lanes `By author` gave lanes `debian 8` and `director 1` and the note "2 tasks outside this range." Both settings live in this browser's `localStorage` key `grain.gantt.view`, by design. They and the URL `/boards/board-2` survived a reload.
- **Everything, by state:** Range `Everything` with Lanes `By state` gave eight state lanes with counts.
- **Empty state:** searching `zzzz-none` gives "No tasks match this search and these filters in this range."
- **Opening a task:** both the row title and the bar itself open the task. Escape returns to the board with focus on the row link.
- **`WT Timeline`** opened on `?f=state:any:running|queued` with rows 2, 11, 3, 8 and 13.

### 6. Tree

- **`Tree`:** task 12 (the Director session) has tasks 1, 3 and 8 under it, and task 11 sits under 8. The other tasks are roots, newest first.
  - Typing `pagination` gave `?q=pagination` and the rows `8 > 11`. That survived a reload, with the search box still filled.
  - Clicking task 11 opened `/tasks/11`. Escape returned to the tree URL.
- **`WT Tree`** opened on `?f=state:any:queued|running` with rows 13, 12, 3, 8, 11 and 2, which survived a reload. Adding a search gave `?q=pagination&f=…` with rows 13, 8 and 11, which also survived a reload.

### 7. Dashboard

- **Panel query scoping:** `WT Dashboard` opens on `?f=repo:any:acme/widgets`. Each panel's **Open** link carries its own question:
  - panels without a query of their own follow the board's: `/?f=repo:any:acme/widgets`;
  - `Scoped Running` goes to `/?f=state:any:running`;
  - `Nothing` goes to `/?q=zzzznomatch`;
  - `Metrics` goes to `/system/metrics?f=repo:any:acme/widgets&f=created:within:30`, which renders "Measured over the tasks in acme/widgets."
- **Whole-board narrowing:** typing `pagination` in **Search this dashboard…** narrowed only the panels that follow the board's question. `Inheriting List` and `Timeline` showed 11 and 8; `Scoped Running` and `Nothing` were unchanged. Their links gained `q=pagination`, and all of it survived a reload. While narrowed, each following panel offers **Keep this narrowing on "<panel>"**.
- **Keep and Add panel:**
  - **Keep** filed `Narrow the panel "States" on the dashboard "WT Dashboard"`, listing every panel with its width, range and `order_key`.
  - **Add panel** filed `Add a panel to the dashboard "WT Dashboard"`.
  - **Edit board** while reading `q=retry` carries the panel list too.
- **Reordering panels:**
  - **By keyboard:** `Move panel 6, "Nothing"`, then Enter on `Move here, above panel 1`. It moved first, focus stayed on its handle, and the order survived a reload.
  - **By tap at 390×844:** tapping `Move panel 6` showed `Moving 1 panel — choose where it goes / Cancel`, and tapping `Move here, above panel 2` moved it. That survived a reload, and **Cancel** dropped a later pickup.
- **Opening things from panels:**
  - A state tile (`Queued`) opened `/?f=repo:any:acme/widgets&f=state:any:queued` with rows 2 and 11. Back returned to the dashboard.
  - A list panel row opened `/tasks/8`. Back returned to the same narrowed dashboard URL with focus on that row.
  - A timeline panel bar opened `/tasks/11`. Escape returned with focus on its row link.
- **Keyboard:** Tab reaches the narrowing bar, the header actions, `← Boards`, the condition chip, then each panel's handle, **Open**, rows and state tiles in order.
- **Empty states:**
  - `WT Empty Dash`: "This dashboard has no panels yet. "Add panel" files the task that adds one."
  - `Nothing` panel: "Nothing here right now."
  - `Inheriting List` beyond its budget: "Showing 8 of 11."

### 8. Report

- **Scope notes:** `WT Report` opens on `?q=ignored&f=repo:any:acme/widgets` and says:
  - "Measured over the tasks in acme/widgets."
  - "A report is measured over whole distributions, so the rest of this question is not in force: the search for "ignored"."
- **Sections:** Throughput, Custom Latency, Backlog, Prompts and Merge queue. An empty section collapses to one line, for example "Merge queue: nothing to report over this window."
- **Window:** `Last 24 hours` wrote `&window=24h`, which survived a reload. The options are 24 hours, 7, 30 and 90 days.
- **Reordering sections:** `Move section 5, "Merge queue"`, then Enter on `Move here, above section 1`. It moved first, focus stayed on its handle, and the order survived a reload.
- **Link into the data:** the Backlog line "Oldest queued: task 2" opened `/tasks/2`. Escape returned to the report URL with its window and focus on the link.
- **Narrowing the report:** removing its `repo` chip changed the request to `GET /api/metrics?window=7d&buckets=24` and dropped the repo scope note.
- **Loading and errors (stub 2):**
  - The report board shows a spinner while loading, then "The report could not be loaded (synthetic metrics 500). Refresh to try again." The app-wide error snackbar also shows `synthetic metrics 500`.
  - The dashboard's metrics panel shows `Loading…`, then "The metrics report could not be loaded." The other panels kept rendering.

### 9. Phone width (390×844, touch)

- `/boards`, `/boards/board-1`, `/boards/board-2`, `/boards/board-4`, `/boards/board-9` and the dashboard all had `scrollWidth == clientWidth == 390`, so nothing scrolled sideways.
- No dashboard panel extended past the viewport, and each board page's **Edit board** stayed visible.

### 10. Observation, proposed as a follow-up: a cleared narrowing is not representable in the URL

- **Reproduction (a list board):**
  1. Open `/boards/board-5`. It loads `?f=state:any:running` with 2 rows.
  2. Remove the `State is any of Running` chip. The URL becomes `/boards/board-5` and every task shows: 12 rows, including closed and deferred.
  3. Reload. The URL is `/boards/board-5?f=state:any:running` again, with 2 rows.
- **The same on `/`:** removing both default chips shows 12 rows at `/`; a reload shows 10 rows with the default chips back.
- **Cause:** `paths.js`'s `narrowingQuery` writes an empty narrowing as no query at all, and `parsePath` reads no query as "nobody has asked" (null). So the same address means "everything" on screen, the default after a reload, and the board's saved question on a board.
- **Why not fixed here:** this is documented design that every task view shares (`parseNarrowing`, `NO_NARROWING`), not something specific to boards. Fixing it means choosing a URL spelling for "explicitly nothing" across all views, so it was proposed as a separate task with this reproduction.

## Bugs fixed in this run

None. Nothing found was both a defect and a safe, scoped fix. No code changed, so no new regression tests were added; the existing `ui/e2e` board specs (`boards.spec.js`, `board.spec.js`, `gantt.spec.js`, `work-tree.spec.js`, `saved-boards-walkthrough.spec.js`) cover the paths exercised above.

## Clean-up

- Stand-in boards `board-5` to `board-11` were deleted through the API, since the UI exposes no delete. `GET /api/boards` afterwards returned the four seeded boards.
- The demo store and its access code lived under `/tmp/wt` and were thrown away with the sandbox. No screenshots or browser artifacts were committed.

---

## Second run, 13:41–13:55 UTC (task 272)

Commit tested: `66b743a5def3` (`Merge pull request #4528 from bwsalmon/grain/049f1e09/task-270`), the tip of `candidate` at the time, checked out as `grain/049f1e09/task-272`. This build includes the two fixes proposed after the first run: `67a7a7852`, which makes pre-reset `/t/<team>/…` links open their team-less page, and `9c99c42f6`/`ad00be1e4`, which keep a cleared narrowing in the URL as `?f=none`.

**Result:** every step below passed in the browser. No new defects were found. The first run's §1 note about `/t/default/boards` landing on `/` and its §10 observation are both resolved on this build (see 1 and 3/6 below). No code changed in this run.

### Environment and stubs

- Same sandbox shape as the first run: Go 1.26.9, Node.js 20.19.0, `@playwright/test` 1.62.1, Chromium headless shell `chromium_headless_shell-1234`, which was preinstalled at `/var/cache/playwright` (`PLAYWRIGHT_BROWSERS_PATH`), so no browser download was needed.
- `cd ui && npm ci && npm run build`, then `go build -o /var/tmp/grain ./cmd/grain`. Started with `/var/tmp/grain demo -addr 127.0.0.1:8787 -open=false -access-code-file /var/tmp/wt/access-code` over a fresh embedded store for each run. One Playwright script (`/var/tmp/wt/walkthrough.mjs`, not committed) drove a 1440×1000 context and a 390×844 touch context. Both signed in through the **Access code** screen.
- `/home/debian/.gemini-api-key` is still absent, so no Gemini-backed tooling was used.
- Stubs are the same three as the first run:
  1. Stand-in boards `board-5` to `board-11` (same table as above) created with `POST /api/boards` *after* the asynchronous creation flow was verified through the UI, then deleted with `DELETE /api/boards/<id>` (204 each).
  2. `page.route("**/api/metrics*")`: a 600 ms hold, then HTTP 500 `{"error":"synthetic metrics 500"}`. Used only in step 8.
  3. Demo mode has no GitHub, so the `github-branches` 404s on task detail are expected.
- **Health over the whole run:** 0 `pageerror`s. The only responses ≥ 400 were the `401 /` before sign-in, the expected `github-branches` 404s, and the stubbed metrics 500s.

### Steps and outcomes

1. **Index, rail, URLs, history**
   - `/t/default/boards` now opens the Boards index and rewrites the URL to `/boards`. `/t/default/boards/board-1` opens the kanban and rewrites to `/boards/board-1`.
   - `/board` and `/gantt` still go to `/`. `/boards/nope` goes to `/boards` with the alert `no board "nope"`.
   - The rail's `Boards 4` opened `/boards`: count `4`, `Board`/`Timeline`/`Report`/`Tree` with kinds Kanban/Timeline/Report/Tree, and no search or sort control on the index.
   - Cold loads of `board-1` to `board-4` all worked. Back went to Report and Forward to Tree, and a reload stayed on Tree.
   - With the stand-ins present, the count was `11` and the folders were `Other` then `Walkthrough`.
   - Keyboard pickup and drop (Enter on `Move <board>`, then Enter on a `Move here, …` slot) did three things:
     - filed `WT Empty Dash` at the end of `Other`, with focus returning to its handle;
     - moved `WT Report` above `Board`, outside any folder;
     - put `Tree` above `Timeline`, which had equal `orderKey`s.
   - After a reload the order was `WT Report | Board | Tree | Timeline | Report | WT Tree | WT Empty Dash | WT List | WT Kanban | WT Timeline | WT Dashboard`.
   - **Customize pinned items** → Type `Board` → `WT Dashboard` → **Pin item** → **Save**. The new rail entry opened `/boards/board-8?f=repo:any:acme/widgets` with `aria-current="page"` before and after a reload.
2. **Asynchronous creation**
   - On `/?f=repo:any:acme/widgets&q=pagination`, **Create options → New board** opened `/new`, pre-filled `Add a board`. The description cited `tables/board.json` and the current `q=pagination&f=repo:any:acme/widgets`.
   - Filing it created task #13 on `grain/config` in state `queued`, while `GET /api/boards` still returned 4: the board itself is not created synchronously.
   - Task #13's page showed the `tables/board.json` brief with no "not on this deployment's target repo list" warning. **Close → Reopen → Close** worked on this throwaway task.
   - **Edit board** on `/boards/board-4?q=retry` pre-filled `Change the board "Tree"` with the narrowing. Escape discarded it.
3. **List** (`board-5`)
   - Opened on `?f=state:any:running` with 2 rows.
   - Enter on the first row opened `/tasks/3`. Escape returned to the board with focus back on that row.
   - Removing the chip wrote `?f=none` and showed 12 rows, and a reload kept `?f=none` with 12 rows. This is the first run's §10 reproduction, now fixed.
   - Searching `pagination` narrowed the list to 3 rows.
4. **Kanban** (`board-1`, `board-6`)
   - Queued initially read `2,11`.
   - Keyboard move of task 11 above `Fix flaky retry test` kept focus on the handle and persisted as `11,2` across a reload. A mouse drag put it back to `2,11`, which also survived a reload.
   - Clicking a card opened `/tasks/2`.
   - **Columns**:
     - Renaming column 1 to `Ideas` and pressing Escape raised `You have unsaved changes here. Leave and discard them?`. Dismissing it kept the editor open.
     - **+ Add column** created `Parked` with state Deferred, and **Move Parked left** moved it once. After **Save** and a reload the columns read `Ideas, Queued, Running, Needs you, Parked, Queued for merge`.
     - **Reset to default** + **Save** restored the defaults.
   - `WT Kanban` shows its own columns, `Triage, Active`, and offers no **Columns** button.
5. **Timeline** (`board-2`, `board-7`)
   - The today marker (`.gantt-now`) was present. Task 11's Blocked chip read `Blocked on: #8 (running)`.
   - Range `Last 24 hours` + Lanes `By author` survived a reload, with 10 bars. These settings are kept per browser, not in the URL, which stays `/boards/board-2`.
   - Clicking a bar opened `/tasks/2`, and Escape returned to `/boards/board-2`. The range and lanes were then restored to 7 days / No lanes.
   - The narrowed `WT Timeline` showed 4 rows.
6. **Tree** (`board-4`, `board-11`)
   - `board-4` nests 12 › 8 › 11. A search for `pagination` survived a reload, keeping 8 › 11.
   - `WT Tree` opened on `?f=state:any:queued|running` with 5 nodes, and still 5 after a reload. Clearing the chip wrote `?f=none`, which showed 13 nodes after a reload.
   - A row opened `/tasks/13`, and Escape went back.
7. **Dashboard** (`board-8`, `board-10`)
   - Opened on `?f=repo:any:acme/widgets` with 6 panels. The `Nothing` panel showed `Nothing here right now.` and the inheriting list showed `Showing 8 of 11.`
   - Whole-board search `pagination` put **Keep** on the inheriting panel only, not on `Scoped Running`. **Keep** pre-filled `Narrow the panel "Inheriting List" on the dashboard "WT Dashboard"`.
   - `Scoped Running`'s **Open** went to `/?f=state:any:running` with 2 rows.
   - Keyboard-moving `Nothing` to the top kept focus on its handle and survived a reload.
   - **Add panel** pre-filled `Add a panel to the dashboard "WT Dashboard"`.
   - `board-10` showed the empty state `This dashboard has no panels yet`.
8. **Report** (`board-9`, `board-3`)
   - Showed `Measured over the tasks in acme/widgets.` and the note that the search for "ignored" is not in force.
   - **Window → Last 24 hours** wrote `window=24h`, which survived a reload.
   - Keyboard-moving `Merge queue` to section 1 kept focus, and after a reload section 1's handle was `Move section 1, “Merge queue”`.
   - The Backlog line `Oldest queued: task 2` opened `/tasks/2`, and Escape returned to the report.
   - The seeded `Report` rendered `Tasks filed`.
   - With stub 2 active, the report showed `Loading the report` and then `The report could not be loaded (synthetic metrics 500). Refresh to try again.`, and the dashboard's metrics panel showed `The metrics report could not be loaded.`
9. **Phone (390×844, touch):** none of the 16 pages (`/boards`, `board-1` to `board-11`) overflowed horizontally. On the dashboard, tapping `Move panel 1` showed `Moving 1 panel` and **Cancel** cancelled it.
10. **Delete:** the UI exposes no delete for boards, so there was no destructive confirmation to exercise. The stand-ins were removed through the API, after which `GET /api/boards` returned the four seeded boards.

### Observation (not a defect)

Closing task detail with Escape pushes a new history entry rather than popping one. So in step 3, Back from the board after open → Escape → search went to `/tasks/3`. This matches `App.jsx`'s URL effect, which records each state change as navigation, and it is the same on every task view, so it was neither changed nor proposed.

### Bugs fixed in this run

None. No code changed, and the existing `ui/e2e` board specs still cover these paths.

### CI fault fixed alongside

This branch's first full CI run failed `ui e2e webkit` on `e2e/alerts-panel-layout.spec.js`, and `candidate` failed the same way at `10befff` and `66b743a`. The failure was `Test timeout of 30000ms exceeded`, reported at the `finally` block's `DELETE /api/pipeline-pause`. That failure has nothing to do with boards: the test walks three viewports and starts in the first wave next to the suite's heaviest WebKit specs. On a fresh rerun it passed in 25.6 s, and the failing runs took 31.5 s, so it was running over Playwright's default 30 s budget. Locally, on 8 cores, it took 8 s alone and 13 s within the whole WebKit project. The spec now sets `test.setTimeout(90_000)`, as the other long multi-step specs do.
