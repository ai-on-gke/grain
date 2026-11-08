# Test run: 2026-11-07 plan §10 (UI) — closed search & scroll paging, sidebar/top rail, Repos add lock, load-failed states, `origin` vocabulary

- **Plan:** [`docs/test-plans/2026-11-07.md`](../test-plans/2026-11-07.md) §10, "What is NOT proved" items 1–5.
- **Run:** task 3494, 2026-11-07 22:19–23:10 UTC.
- **Base:** `origin/main` at `f55b8c73` (PR #867).
- **Artifacts:** [`2026-11-07-ui-search-scroll-resilience/`](2026-11-07-ui-search-scroll-resilience/). Every script runs against any server: `GRAIN_SERVER=http://127.0.0.1:<port> GRAIN_TOKEN=<access code> node <script>.mjs [label]`. Each run writes JSON (`*-before.json` on the base build, `*-after.json` on the fixed one) and its logs (`*.log`) next to the script.

## Environment

| What | Detail |
|---|---|
| Build | `cd ui && npm ci && npm run build`, then `go build ./cmd/grain` and `go build ./ui/e2e/consolerestserver`. The base binaries went to `~/bin`. The binaries with the fixes went to `~/bin2`, rebuilt after the fixes. |
| Servers | (a) `ui/e2e/consolerestserver` with `GRAIN_E2E_ACCESS_CODE` set: the real store, UI server and orchestrator cycle, with a scripted agent. Seeded for item 1. (b) `grain demo -github-sim -fake-ci -reconcile 2s -open=false -access-code-file …` for items 2–5. Base build on `:4390`, fixed build on `:4391`. |
| Seed | [`seed.mjs`](2026-11-07-ui-search-scroll-resilience/seed.mjs) files **3,040 tasks** over HTTP: **3,000 closed** (filed and closed in order, ~1 KB descriptions) and **40 live**, with four owners and an `externalRef` `CA-1xx` on every fifth task. Seed index 12 (task #14 on the base server, #12 on the fixed one) is the deep needle. It has the title word `zephyrquill`, the description word `obsidianlark`, `externalRef: CA-9012` and `owner: deepowner@example.com`, and it is the 2,988th of the 3,000 closed tasks in newest-closed-first order. Total `/api/tasks?state=closed` = 3,000. The live poll holds 41 tasks (the 40 seeds plus the Director session). |
| Browser | Playwright Chromium (`@playwright/test` from `ui/node_modules`) with `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, at 1440×900 and 390×844. Each check ran in light; the sidebar walk and the screenshots also ran in dark (`colorScheme`). `@axe-core/playwright` checked the collapsed rail. |
| Toolchain | Go 1.26.10, Node 20.19.0. Nothing was installed. |
| Limits | The sandbox's file reader shows PNGs as bytes, and `LOOK=1` has no Gemini key here. So the screenshots were checked through the bounding-box and DOM measurements the scripts record, not by eye. They are committed for a reviewer. |

## Findings & fixes

| # | Finding | Severity | Status |
|---|---|---|---|
| F1 | **Typing a search read a stray page of the newest-closed list.** On the first keystroke the list had already narrowed to the new text. But `useClosedTasks` still had the old text, because App handed it over in a passive `useEffect`. The end of the now-short list scrolled into view, and `ClosedTasksMore`'s IntersectionObserver loaded the **next 200 unrelated closed rows**. At 390 px each search added a page: 200 → 401 → 602 rows over the walk (`clear-probe.mjs`, `search-scroll-before.json`). The same race could load the previous search's next page after a refinement. | Medium (wasted 200-row pages, list grows behind the user's back) | **Fixed.** The text is now handed over in a `useLayoutEffect` (`App.jsx`). The re-render happens before paint, so the observer is disconnected before it can fire. |
| F2 | **Clearing a search left its deep hits drawn out of place.** Server hits were merged into the paged closed list. After emptying the box, the first page (seeds 2999…2800) was followed directly by the deep hits (seed 21, 12). That reads as if they came next, with a gap of ~2,800 tasks between. `search cleared` reported `outOfPlaceRows: [21]` and 202 rows at 1440 px. | Low–Medium (skipped boundary in the list) | **Fixed.** `useClosedTasks` keeps search hits in their own `found` state and draws them only while the box still holds that search (`closedTasks.js`). After the fix: 200 rows and no out-of-place rows at both widths. |
| F3 | **The search box never found a task by its external reference.** Server `?search=` matched title, description and id. The client matched title and id. So typing `CA-9012` found nothing, live or closed. | Medium (an obvious way to look a task up did nothing) | **Fixed.** `pkg/ui/task_list_filter.go` and `taskFilters.js` `matchesFilters` now also match `externalRef`. Found in 0.8 s at both widths after the fix. |
| F4 | **Mid-session `/api/tasks` outage: nothing said the list was stale.** The views rightly keep drawing the last list read. But once the 5 s banner faded, no surface showed that polling had failed. The rail still read `Tasks 18` after 9 s of refused connections. | Medium (stale data presented as current) | **Fixed.** The rail's Tasks entries (main and pinned) now read **"not refreshing"**, with a retry title. They clear automatically on the next poll that answers (`Sidebar.jsx`). |
| F5 | `admission` is not a narrowing attribute. `admission:any:durable` shows "unknown condition: admission" and is not applied, although every task row carries `admission`. The plan expects to filter and group by it. | Low (missing vocabulary) | **Not fixed here.** Adding it touches the CLI vocabulary too (`pkg/cli/taskattributes.go`, parity test `list_vocabulary_test.go`, CLI spec). Proposed as a follow-up task. |
| F6 | The search box does not match `owner`, and `origin` is not text. Those are reached through `owner:` / `origin:` conditions, and those conditions reach a deep closed task only by **auto-paging every closed page**: 15 pages, 3,000 tasks, ~10 s (`extref-case-probe.out`). The server already has `?owner=` / `?externalRef=` filters the UI does not use. | Low (works, but costly at scale) | **Not fixed.** Follow-up proposed: send `owner`/`externalRef` conditions to the server the way the search box is. |
| F7 | On a phone the rail lives in the drawer, so F4's "not refreshing" note is only visible once the drawer is opened. The phone top bar has no stale indicator. | Low | Noted for the follow-up. |

## Item 1: server-side closed-task search & scroll paging (#820)

Scripts: [`search-scroll-walk.mjs`](2026-11-07-ui-search-scroll-resilience/search-scroll-walk.mjs) (`ONLY=search` / `ONLY=scroll`) and [`clear-probe.mjs`](2026-11-07-ui-search-scroll-resilience/clear-probe.mjs). Results: `search-scroll-before.json`, `search-scroll-after.json`, `search-scroll-scroll-before.json`, and the `scroll-*.log` files.

### Search (closed list `/?f=state:any:closed`, typed at 40 ms/key)

| Search for the deep task by | Before (1440 / 390) | After (1440 / 390) | Requests |
|---|---|---|---|
| title `zephyrquill` | ✅ 1.4 s / 1.4 s | ✅ 1.5 s / 0.9 s | one debounced `search=zephyrquill` |
| description `obsidianlark` | ✅ 1.6 s / 1.6 s | ✅ 1.6 s / 1.0 s | one |
| externalRef `CA-9012` | ❌ 0 rows / 0 rows (F3) | ✅ 0.8 s / 0.8 s | one `search=ca-9012` |
| owner `deepowner` | ❌ not a search field (F6) | ❌ (same, by design: use `owner:any:…`) | one |
| id `#14` / `#12` | ✅ 0.5 s | ✅ 0.5 s | one |
| `owner:any:deepowner@example.com` condition | — | ✅ found, after auto-paging all 15 closed pages (F6) | 15 pages |
| `externalRef:any:CA-9012` condition | — | ✅ found, after 15 pages | 15 pages |
| `externalRef:any:CA-100` (exact case) / `ca-100` | — | ✅ 13 rows / 0 rows (values are exact; the chip just prints lower-case) | 3 / 15 |

- **Out-of-order answers:** the answer for `seed 0013` was held 2.5 s while the box moved on to `seed 0021`. The stale rows were never shown (`staleRowsShown: 0`, `freshRowsShown: 1`), before and after the fix. The generation check works.
- **Clearing:** before the fix, 1440 px showed 202 rows with the deep hit out of place (F2), and 390 px showed **602** rows: two stray pages read while typing (F1). After the fix, 200 rows at both widths, nothing out of place, and **no non-search page request while typing** (`clear-probe.mjs` on the fixed build: one `search=` request, then nothing).

### Scroll paging (mouse wheel to the end, 3,000 closed tasks)

Before and after the fix, every view loaded cleanly:

| View | Width | Page requests | Distinct cursors | Repeated cursors | Rows | Duplicates | Missing | Scroll thrown back | Requests after the last page (5 s) |
|---|---|---|---|---|---|---|---|---|---|
| List `/?f=state:any:closed` | 1440 | 15 | 14 | 0 | 3,000 | 0 | 0 | 0 | 0 |
| List | 390 | 16¹ | 14 | 0 | 3,000 | 0 | 0 | 0 | 1¹ |
| Saved Kanban board (one Closed column) | 1440 | 19¹ | 14 | 0 | 3,000 | 0 | 0 | 0 | 0 |
| Saved Kanban board | 390 | 16¹ | 14 | 0 | 3,000 | 0 | 0 | 0 | 0 |
| Saved work tree board (`kind: tree`) | 1440 | 15 | 14 | 0 | 3,000 | 0 | 0 | 0 | 0 |
| Saved work tree board | 390 | 15 | 14 | 0 | 3,000 | 0 | 0 | 0 | 0 |

¹ The extra requests are `cursor=` (first-page) refreshes from `CLOSED_REFRESH_MS` (60 s) during the longer walks: the Kanban walk at 1440 took 245 s because the column scrolls in its own box. They are not repeated cursors or an end-of-list loop. After the last page the pager disappears (`loadMoreLeft: 0`) and no further `cursor=<x>` request is made.

The board scrolls inside `.board-column-cards` and the tree inside `.task-tree`. Neither ever jumped backwards while pages landed.

## Item 2: sidebar New task, collapsed rail & default-hidden CI (#726)

Script: [`controls-walk.mjs`](2026-11-07-ui-search-scroll-resilience/controls-walk.mjs) `ONLY=sidebar`. Results: `controls-before-sidebar.json`, `controls-before-sidebar-phone.json`, `controls-after-sidebar.json`. Each run used a fresh browser context (empty `localStorage`).

| Check | 1440 light / dark | 390 light / dark |
|---|---|---|
| CI hidden from the nav by default | ✅ | ✅ (drawer) |
| **+ New task** in the rail's slot, inside the rail's box | ✅ → opens `/new` | ✅ in the drawer → `/new`. The top bar's own **New task** (40×40 at x 338–378, inside 390) → `/new` |
| Top-rail controls (Filters, Backlog order, Contact running, Alerts, System, Settings) share one centre line | ✅ spread 0 px, all 36 px tall | n/a |
| Collapsed rail | ✅ `data-collapsed=true`, 60 px wide, every icon centred (0 off-centre), **Create** tooltip "New task, session or board", axe: 0 violations | n/a |
| Collapsed focus order | Create → Customize sidebar → Inbox → Tasks → Proposed → Waiting → Running → Asked → Merging → Deferred → Closed → Blocked → Boards → Sessions | n/a |
| Customize sidebar → Show CI → Save, then reload | ✅ CI shown, ✅ still shown after reload | ✅ / ✅ |
| Direct `/ci` with CI hidden | ✅ renders the CI page, nav still hides it | ✅ |

No faults found.

## Item 3: Repos add-in-progress lock (#711)

`ONLY=repos`. Results: `controls-before-repos.json`, `controls-after-repos.json`. `POST /api/repos` was held 2 s and answered 500, 409, then 200. Each time the script typed `acme/walk-<status>`, double-clicked **Add repo** and pressed Enter twice.

| Answer | POSTs sent | During flight | After |
|---|---|---|---|
| 500 | **1** | field disabled, button `aria-busy=true` with spinner, caption "Adding acme/walk-500…" | field re-enabled, **`acme/walk-500` kept**, not busy |
| 409 | **1** | same | field re-enabled, typed value kept |
| 200 | **1** | same | field cleared, not busy |

The results were the same at 1440 and 390, before and after the fix. No faults found. #837's unsaved-draft guards were not re-walked separately: the repo field kept its value on every failure.

## Item 4: "No tasks" vs "Tasks failed to load" (#801)

`ONLY=failed`. Results: `failed.log` (base) and `failed2.log` (fixed). Boards were a saved Kanban, a work tree, and a dashboard (`panels`) with a States panel and a Gantt panel.

**Cold start** (`/api/tasks` → 502 from the first request; read after the 5 s banner fades):

| View | Load-failed text / Retry | "No tasks…" claim | Rail Tasks | Recovers with no click (next poll) |
|---|---|---|---|---|
| `/` list | ✅ / ✅ | none | "could not be loaded" | ✅ 16 rows, `Tasks 18` |
| Kanban board | ✅ / ✅ | none | "could not be loaded" | ✅ |
| Tree board | ✅ / ✅ | none | "could not be loaded" | ✅ |
| Dashboard (States + Gantt) | ✅ 3 notices / 2 Retry | none | "could not be loaded" | ✅ |

This held at 390 px too. There the rail is in the drawer, so only the views were read.

**Mid-session** (tasks already drawn, then `/api/tasks` refused for 9 s, then restored):

| View | Before the fix: during | After the fix: during | Recovered |
|---|---|---|---|
| List, Kanban, Tree, Dashboard | stale rows kept, **rail `Tasks 18`, nothing says it is stale** (F4) | stale rows kept, rail **"Tasks not refreshing 18"** | ✅ note gone on the next poll, no click |

The Kanban's one "No tasks" match before, during and after is an empty column's own "Nothing here." after a successful load, not a failure claim.

## Item 5: saved boards & NarrowingBar `origin:` + new attributes (#794)

`ONLY=origin`. Results: `controls-before-origin.json` (the first base pass used `,` as the value separator, which is wrong; `|` is right) and `controls-after-origin.json`.

| Condition | Chip | Warning |
|---|---|---|
| `origin:any:imported` | "origin is any of imported issue" (1 demo row) | none ✅ |
| `origin:any:schedule` / `proposed` / `external` | labelled | none ✅ |
| `origin:any:hand\|imported\|proposed\|schedule\|suite\|fix\|review\|external` | all eight labelled | none ✅ |
| `origin:any:bogus` | "“bogus” (unknown)" | ✅ warning chip: "“bogus” is not a value Origin has, so it matches no task" |
| `origin:any:mcp` | unknown | ✅ warns. The vocabulary calls MCP and agent-API filings `external`. |
| `owner:any:me`, `owner:any:alice@example.com` | labelled | none ✅ |
| `externalRef:any:…`, `externalRef:filled:` | labelled ("external ref is set": 2 demo rows) | none ✅ |
| `humanReview:any:security`, `humanReview:filled:` | labelled | none ✅ |
| `humanReview:any:product_decision` (underscore typo) | unknown | ✅ warns |
| `admission:any:durable` | "unknown condition: admission" | ⚠️ F5: not an attribute |
| Saved list board `f=origin:any:imported\|schedule\|proposed\|external\|hand&group=origin` | 18 rows | none ✅ |

## Regressions added

- `ui/e2e/closed-search-paging.spec.js` (1440 and 390 px) routes `GET /api/tasks?state=closed` to three 200-row pages plus a deep search hit. It asserts four things. Typing a search sends **no** non-search closed request (F1). `CA-9012` finds the deep task (F3). After clearing, the list is the first page alone with the hit not drawn (F2). It also checks with bounding boxes that the pager sits below the last row.
- `ui/src/closedTasks.hook.test.jsx`: "draws a search's deep hits only while the box holds that search" (F2).
- `ui/src/taskFilters.test.js`: "searches the external reference beside the title and id" (F3).
- `ui/src/components/Sidebar.test.jsx`: the mid-session "not refreshing" note on both Tasks entries, and that it clears (F4).
- `pkg/ui/task_list_cache_test.go`: `TestTaskListSearchMatchesExternalRef` (F3, server).

## Checks run

- `npx vitest run` (whole `ui` suite). `make fmt-frontend`: clean. `gofmt`: clean.
- `go test ./tests/hygiene/...`: ok.
- `go test ./pkg/ui -run 'TestTaskListSearchMatchesExternalRef|TestTaskListNarrows'`: ok.
- `npx playwright test e2e/closed-search-paging.spec.js e2e/tasks-load-failed.spec.js e2e/sidebar-hidden-sections.spec.js`: passed.
- Not checked: whether `closed-search-paging.spec.js` fails on the pre-fix build. That would have meant rebuilding the base UI, and there was not time. The before-fix numbers above come from the walkthrough scripts on the base build.

## Screenshots

`make ui-screenshots`'s script (`ui/e2e/screenshot.mjs --url … --pages "/?f=state:any:closed" --click 'input[placeholder*="Search" i]' --type CA-9012`) shot desktop and phone, light and dark, against the seeded consolerestserver:

- [`contact-sheet-before.png`](2026-11-07-ui-search-scroll-resilience/contact-sheet-before.png) is the base build: "No tasks match your search".
- [`contact-sheet.png`](2026-11-07-ui-search-scroll-resilience/contact-sheet.png) is the fixed build: the deep closed task is found by its external reference.

The per-check screenshots (`sidebar-*`, `sidebar-collapsed-*`, `ci-direct-*`, `repos-adding-*`, `failed-cold-*`, `failed-mid-*`, `scroll-end-*`, `search-*`, `origin-board-*`) are in the artifacts folder.
