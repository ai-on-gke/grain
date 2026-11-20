# `/next/` Walkthrough-Driven Fixes & API Error/Status Polish — 2026-11-19

This report executes `docs/test-plans/2026-11-19.md` §9 (filed by [Task #7303](/tasks/7303), PR #1889 at `73ccc0a47`) covering PRs #1819, #1820, #1836, #1827, #1843, #1756, and #1878 against a real `grain daemon -sandbox-port-base=0` over PostgreSQL and SQLite, plus `grain demo -host-mode-install` for the unmigrated host-mode install banner.

## Summary

| §9 Item | Scope / PRs | Verdict | Findings |
| --- | --- | --- | --- |
| 1. `request_access` counts & substitute grants + `"Merge failed"` / `"Can't read the pull request"` badges | PR #1836 | **Pass after fixes (F3, F4)** | Real MCP `request_access` parked and resumed cleanly; `"Grant 2 of 3"` / `"Grant 1 of 3"` / `"Grant 0 of 3"` agreed on `/next/inbox` and `/next/task/<id>`, delivering only `[0, 2]` (clean `network: open` + substitute `api:ci-read@acme/widgets`). Fixed **F3** (`/next/workstreams` styled `merge_failed`/`unreadable` as amber `person` / blue `run` instead of red `failed`/error tone) and **F4** (`/next/inbox` omitted `substituteGrant` from the checkbox row label). |
| 2. Capability-policy verdicts across all four `/next/` grants pickers | PR #1820 | **Pass after fix (F2)** | Denied badges + notes + `/next/settings/capabilities#capability-policy` links worked on Task Detail, `/next/repos/acme/widgets/ci`, and `/next/sessions`, and CI-only (`repo#read`) rendered no policy link. Fixed **F2** (clicking `Settings → Capabilities` inside the New task sheet navigated behind the still-open modal sheet). |
| 3. Plain-words `writeBodyError` across `readJSON` routes | PR #1820 | **Pass after fix (F1)** | `PUT /api/settings` and `/next/settings/caches` answered cleanly (`packageCacheSizeGb must be a whole number, not 1.5`). Fixed **F1** (`POST /api/tasks` and `PATCH /api/tasks/{id}` leaked Go's embedded `Alias.` prefix, e.g. `Alias.title must be a string, not a number`, `Alias.autoMerge`, `Alias.sandboxCpus`, `Alias.goalMode.maxPasses`). |
| 4. Saved boards round-trip on a real store | PR #1819 | **Pass** | Verified on real PostgreSQL and SQLite stores at 1440 px and 390 px, light and dark: `/next/`-compatible list board applied in place on `/next/tasks`; classic-filter list (`filtered list ↗`), Kanban (`kanban ↗`), and Report (`report ↗`) opened classic `/boards/<id>` with saved filters/views intact. |
| 5. Real authenticated viewer composer + 503 sessions load + narrowed Inbox alert body | PR #1827, PR #1756 | **Pass** | Real IAP-verified `viewer@example.com` (`grain/viewer`) saw disabled reply composer + `tasks:write` explanation on `/next/task/<id>` AND received HTTP `403` on direct `POST /api/tasks/{id}/comments`. Intercepted `503` on `GET /api/console-sessions` showed the load-error banner with zero bogus `"That session does not exist."`. Narrowing `/next/inbox` by both search and `Blocked & alerts` chip loaded the full multi-line alert body immediately. |
| 6. Unmigrated host-mode install notice & classic `UpgradePanel` `countError` | PR #1843, PR #1878 | **Pass** | Over `grain demo -host-mode-install`, `/next/` top banner (`role="status"`, no close button) stacked cleanly at `y=0` above the disconnected banner at 1440 px and 390 px (light/dark) with zero horizontal overflow, and `/next/settings/upgrade` displayed `sudo ./scripts/setup.sh`. Against a **live** PostgreSQL `REVOKE SELECT ON task_run FROM grain` count failure (and mocked `/api/upgrade`), classic `/settings/upgrade` showed a `warning` banner with the exact `countError` text, never `"starts within seconds"`, with `Cancel` inside the alert bounds. |

## Environment

- **Base commit walked:** `1a449c7d5` (`origin/main`), built from `HEAD` via `make frontend && go build -o /home/debian/bin/grain ./cmd/grain`.
- **Overlap check:** `git log origin/main -- docs/test-runs/` confirmed no prior `2026-11-19` report covered §9's PRs (`2026-11-18.md` §8 covered earlier `/next/` fixes).
- **Daemon & auth harness:** [`env.sh`](2026-11-19-next-fixes-polish/env.sh) running real `/home/debian/bin/grain daemon -host-sandboxes -sandbox-port-base=0` (`127.0.0.1:4280`), `ghsim` (`127.0.0.1:4284`, `acme/widgets` with `ci_mode: github`), `fake-agy.py` for real MCP tool calls, and `tests/iap/iapsim.go` (`issuer 127.0.0.1:4285`, `proxy 127.0.0.1:4286`) signing real IAP JWT assertions for `alice@example.com` (`grain/operator`, `full: true`) and `viewer@example.com` (`grain/viewer`, `full: false`, no `tasks:write`).
- **Stores:** PostgreSQL `15.20 (Debian 15.20-0+deb12u1)` (full walkthrough + live `REVOKE SELECT ON task_run` trigger) and SQLite `3.40.1` (`writeBodyError`, saved boards, and viewer `403` verification).
- **Host-mode demo:** `/home/debian/bin/grain demo -host-mode-install -addr 127.0.0.1:4287`.
- **Browser:** Playwright `1.62.1` / Chromium `151.0.7922.34` at `1440×900` (desktop) and `390×844` (phone), `light` and `dark` color schemes.
- **Installed packages:** `postgresql`, `postgresql-client`, `sqlite3` (`jq 1.6` was preinstalled).

---

## Findings & Fixes

### F1 (P1) — `writeBodyError` leaked Go's embedded `Alias.` struct prefix on `POST /api/tasks` and `PATCH /api/tasks/{id}` (Fixed)

- **Reproduction on `HEAD` (`1a449c7d5`):**
  - `POST /api/tasks` `{"title":123,"repo":"acme/widgets"}` → `400 {"error":"Alias.title must be a string, not a number"}`
  - `POST /api/tasks` `{"title":"t","repo":"acme/widgets","approved":"yes"}` → `400 {"error":"Alias.approved must be true or false, not a string"}`
  - `POST /api/tasks` `{"title":"t","repo":"acme/widgets","reads":123}` → `400 {"error":"Alias.reads must be a list, not a number"}`
  - `POST /api/tasks` `{"title":"t","repo":"acme/widgets","goalMode":{"maxPasses":1.5}}` → `400 {"error":"Alias.goalMode.maxPasses must be a whole number, not 1.5"}`
  - `PATCH /api/tasks/2` `{"autoMerge":"abc"}` → `400 {"error":"Alias.autoMerge must be true or false, not a string"}`
  - `PATCH /api/tasks/2` `{"sandboxCpus":2.5}` → `400 {"error":"Alias.sandboxCpus must be a whole number, not 2.5"}`
  - `PATCH /api/tasks/2` `{"title":true}` → `400 {"error":"Alias.title must be a string, not true or false"}`
  - `PATCH /api/tasks/2` `{"issue":"abc"}` → `400 {"error":"Alias.issue must be a whole number, not a string"}`
- **Root cause:** `CreateTaskRequest.UnmarshalJSON` and `UpdateTaskRequest.UnmarshalJSON` (`pkg/ui/client.go`) decode through an anonymous wrapper struct embedding `type Alias ...`. Go's `encoding/json` reports promoted fields in `json.UnmarshalTypeError.Field` as `"Alias.<field>"`, which `writeBodyError` formatted verbatim.
- **Fix:** Added `stripAliasFieldPrefix(err)` in `pkg/ui/client.go`, trimming `"Alias."` from `*json.UnmarshalTypeError.Field` in both `CreateTaskRequest.UnmarshalJSON` and `UpdateTaskRequest.UnmarshalJSON`.
- **Regression test:** `TestReadJSONTaskRequestsDropAliasPrefix` in `pkg/ui/readjson_test.go`.
- **Re-verification:** All 14 wrong-typed probes on live PostgreSQL and SQLite daemons returned clean plain-words 400 responses (`title must be a string, not a number`, `goalMode.maxPasses must be a whole number, not 1.5`, `autoMerge must be true or false, not a string`, etc.) with zero `Alias.` or `Go struct` leaks.

### F2 (P1) — Clicking `Settings → Capabilities` inside the New task sheet left the modal sheet open over `/next/settings/capabilities#capability-policy` (Fixed)

- **Reproduction on `HEAD` (`1a449c7d5`):**
  1. Save a deny rule for `https://grain.dev/capabilities/github#*` in `/next/settings/capabilities#capability-policy` and set `defaultCapabilities: ["https://grain.dev/capabilities/github#read-issues"]` on `acme/widgets`.
  2. Open `/next/inbox`, click **New**, expand **Capabilities & repo context**, and click **Settings → Capabilities** (`[data-testid="nt-grants-policy-link"]`).
  3. The URL navigated to `/next/settings/capabilities#capability-policy`, **but `[data-testid="new-task-sheet"]` remained visible on top of the page** (`document.elementFromPoint` over `[data-testid="card-capability-policy"]` hit `DIV.field` inside `new-task-sheet`) at both `1440×900` and `390×844`, light and dark. The existing e2e spec missed this because `toBeInViewport()` checks geometric bounds even under an open modal overlay.
- **Root cause:** `GrantsPickerComponent` rendered `<a routerLink="/settings/capabilities" fragment="capability-policy">` without emitting a `navigated` event (unlike `NoGitConnectionNoticeComponent` in the same sheet), so `NewTaskSheetComponent` never closed when following the link.
- **Fix:** Added `readonly navigated = output<void>()` and primary-click `onPolicyLinkClick($event)` (ignoring Ctrl/Meta/Shift/Alt new-tab clicks) in `ui-ng/src/app/shared/grants-picker.component.ts`, and wired `(navigated)="close(false)"` in `ui-ng/src/app/create/new-task-sheet.component.ts` (preserving any typed title/description via `svc.keep` while closing the sheet without pulling focus back to the opener).
- **Regression tests:** Unit assertion in `ui-ng/src/app/shared/grants-picker.component.spec.ts` (primary click emits `navigated`; Ctrl-click does not) and strengthened `ui-ng/e2e/next-capability-policy-pickers.spec.js` asserting `[data-testid="new-task-sheet"]` count is `0` and `elementFromPoint` over `[data-testid="card-capability-policy"]` hits the card itself.
- **Re-verification:** Verified live across `1440×900` and `390×844`, light and dark (`sheetCount=0`, `hitCard=true`, `overflow=0`).

### F3 (P1) — `/next/workstreams` styled `merge_failed` and `unreadable` tasks as amber `person` (or blue `run`/`merging`) instead of error tone (Fixed)

- **Reproduction on `HEAD` (`1a449c7d5`):**
  - On a workstream rooted at Task #6 with completed children #7 (`merge_failed`) and #8 (`unreadable`), `/next/tasks` and `/next/task/<id>` displayed `"Merge failed"` and `"Can't read the pull request"` in `.status-pill.error`, but `/next/workstreams` rendered both tree glyphs as `ws-glyph c-person` (amber `rgb(138, 83, 0)` / `rgb(255, 223, 153)`), graph nodes as `person ws-gnode`, and `.tstate` as neutral muted grey — and when `mergeQueueBlockedAt` was absent, `classifyTaskState` fell through to `category: "run", glyph: "merging"` (blue).
- **Root cause:** `classifyTaskState` (`ui-ng/src/app/workstreams/workstream-model.ts`) did not check `mergeStuck(t)` before `waitsOnHuman(t)` and the `completed`/`merging` branch, and `.ws-ti .tstate` had no error-tone rule for failed nodes.
- **Fix:** Checked `mergeStuck(t)` in `classifyTaskState` to return `{ category: "failed", glyph: "failed", waitingOnPerson: true }`, added `[class.is-failed]="n.category === 'failed'"` in `workstream-detail.component.ts`, and added `.ws-ti.is-failed .tstate { color: var(--md-sys-color-error); }` in `workstream-detail.css` (before `.ws-ti.is-selected .tstate`, keeping CSS well within budget).
- **Regression test:** Added unit test in `ui-ng/src/app/workstreams/workstream-model.spec.ts` covering both `merge_failed` (with `mergeQueueBlockedAt`) and `unreadable` (without `mergeQueueBlockedAt`).
- **Re-verification:** Verified live on `/next/workstreams` at `1440×900` and `390×844`, light and dark: `isFailedRow=true`, `svgClass="ws-glyph c-failed"`, `gnodeClass="failed ws-gnode"`, `tstateColor=rgb(179, 38, 30)` (light `#b3261e`) / `rgb(242, 184, 181)` (dark `#f2b8b5`), `overflow=0`.

### F4 (P2) — `/next/inbox` `request_access` card omitted `substituteGrant` from the checkbox row label (Fixed)

- **Reproduction on `HEAD` (`1a449c7d5`):**
  - On a pending access request carrying `grain#ci` with `substituteGrant: https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}`, `/next/task/<id>` appended the substitute grant to the row label while `/next/inbox`'s `itemText` rendered only `https://grain.dev/capabilities/grain#ci`.
- **Fix:** Added shared `accessItemText(it)` in `ui-ng/src/app/shared/access-items.ts`, appending `(grants <substituteGrant> instead)` when `it.substituteGrant` is set, and wired both `InboxPageComponent` and `TaskGatesBarComponent` to use it.
- **Regression test:** Added `ui-ng/src/app/shared/access-items.spec.ts` (and kept `task-page.component.spec.ts` green).
- **Re-verification:** Verified live on `/next/inbox` and `/next/task/<id>` at `1440×900` and `390×844`, light and dark.

---

## Detailed Execution Log

### Item 1: `request_access` counts & substitute grants + merge-stuck badges (PR #1836)
1. **Real MCP `request_access` + mixed 3-item card:**
   - Filed Task #4 on `acme/widgets` (`ci_mode: github`) holding `github#read-issues`; `fake-agy.py` executed real MCP `request_access` (`network: open`), parking Task #4 in `awaiting_reply`.
   - Detached default `api#ci-read` from Task #4 and updated its `pending_access_request` to 3 items: `[0]` clean `network: open`, `[1]` already-held `github#read-issues`, `[2]` policy-refused `grain#ci` (live `checkAccessItem` populated `error`, `substitute`, and `substituteGrant: https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}`).
   - Verified at `1440×900` and `390×844` (light & dark) on both `/next/task/<id>` and `/next/inbox`:
     - Initial button label `"Grant 2 of 3"`, checkboxes `[checked, disabled+unchecked, checked]`, `overflow=0`.
     - Unticking substitute `[2]` → `"Grant 1 of 3"`; unticking `[0]` → `"Grant 0 of 3"` (disabled); re-ticking both → `"Grant 2 of 3"`.
   - Clicked `"Grant 2 of 3"` on `/next/inbox`: verified `POST /api/tasks/4/access-request/grant` sent `{"items":[0,2]}` → `200`, Task #4 gained `networkAccess: "open"` and `api#ci-read={"repo":"acme/widgets"}` (not `grain#ci`), resumed its second `fake-agy.py` run, called `nothing_needed`, and closed cleanly.
2. **Stuck-merge badges (`merge_failed` and `unreadable`):**
   - Seeded workstream root #6 with completed children #7 (`merge_failed`, PR #101) and #8 (`unreadable`, PR #102).
   - Verified `"Merge failed"` and `"Can't read the pull request"` in `.status-pill.error` on `/next/tasks?tab=merging` and `/next/task/7` + `/next/task/8` across all 4 viewport/theme combinations (`overflow=0`), and verified `/next/workstreams` error tone after F3.

### Item 2: Capability-policy verdicts across all four `/next/` grants pickers (PR #1820)
1. Saved a deny rule (`https://grain.dev/capabilities/github#*`, `deny: true`) via `/next/settings/capabilities#capability-policy` (`PUT /api/capability-policy` → `200`) and a CI-only rule (`https://grain.dev/capabilities/grain#health`, `runKinds: ["ci"]`), plus checked built-in CI-only `https://grain.dev/capabilities/repo#read`.
2. Inspected all four pickers at `1440×900` (light) and `390×844` (dark):
   - **(a) New task sheet (`nt-grants`):** showed `denied by policy` badge + note, `repo#read` CI-only note in the Method builder carried zero policy links, and after F2 clicking `Settings → Capabilities` closed the sheet and uncovered `#capability-policy`.
   - **(b) Task detail Grants (`task-grants`, right aside on desktop / `Details` tab on phone):** showed `denied by policy` badge + note, `Settings → Capabilities` navigated to `/next/settings/capabilities#capability-policy`, `overflow=0`.
   - **(c) `/next/repos/acme/widgets/ci` (`repo-ci-caps`):** with `ciCapabilities: ["https://grain.dev/capabilities/github#read-issues"]`, showed `denied by policy` badge + note (`to this CI job on acme/widgets`), `Settings → Capabilities` navigated to `/next/settings/capabilities#capability-policy`, `overflow=0`.
   - **(d) `/next/sessions/director` (`session-grants`):** showed `denied by policy` badges + notes for held denied capabilities (`grain#health`, `grain#journal` under the CI-only rule), `Settings → Capabilities` navigated to `/next/settings/capabilities#capability-policy`, `overflow=0`. (Note: `/next/sessions`'s "New session" dialog deliberately creates with deployment defaults and edits grants once opened in `session-grants`.)

### Item 3: Plain-words `writeBodyError` across `readJSON` routes (PR #1820)
- Probed `PUT /api/settings`, `POST /api/tasks`, and `PATCH /api/tasks/{id}` with `1.5`, `"abc"`, `true`, numbers, and nested objects on PostgreSQL and SQLite.
- Note: `PATCH /api/tasks/{id}` with `{"priority": true}` intentionally returns `200` (`admission: "bypass"`) for backward-compatible legacy boolean priority dispatch (`UpdateTaskRequest.UnmarshalJSON` line 5459), whereas `POST /api/tasks` rejects `{"priority": true}` with `400` (`priority must be a positive integer...`).
- Verified `/next/settings/caches` saving `1.5` GiB displays `packageCacheSizeGb must be a whole number, not 1.5` (with zero `Go struct field` text and `overflow=0`) across all 4 viewport/theme combinations.

### Item 4: Saved boards round-trip on a real store (PR #1819)
- Created 4 boards on the live PostgreSQL store (plus SQLite verification):
  - `Next Compatible List` (`kind: "list"`, `query: "tab=proposed&group=repo&q=probe"`)
  - `Classic Filtered List` (`kind: "list"`, `query: "f=state:any:queued"`)
  - `Walkthrough Kanban` (`kind: "kanban"`)
  - `Walkthrough Report` (`kind: "report"`)
- Verified across all 4 viewport/theme combinations (`1440×900` and `390×844`, light and dark, `overflow=0`):
  - Boards menu rendered no `↗` on `Next Compatible List`, and marked the others `filtered list ↗`, `kanban ↗`, and `report ↗`.
  - Selecting `Next Compatible List` stayed on `/next/tasks?tab=proposed&group=repo&q=probe`.
  - Selecting `Classic Filtered List`, `Walkthrough Kanban`, and `Walkthrough Report` opened classic `/boards/board-2?f=state:any:queued`, `/boards/board-3`, and `/boards/board-4` with the saved views intact.

### Item 5: Viewer composer, 503 sessions load, and narrowed Inbox alert body (PR #1827, PR #1756)
- **Viewer (`viewer@example.com`, `grain/viewer` via real IAP proxy):**
  - Direct `POST /api/tasks/5/comments` returned `403`: `viewer@example.com (role grain/viewer) holds no scope tasks:write, which POST /api/tasks/{id}/comments needs`. Verified on both PostgreSQL and SQLite.
  - `/next/task/5` at all 4 viewport/theme combinations showed `task-composer` disabled, `act-send` disabled, and `reply-blocked` text `"Your role grain/viewer does not hold tasks:write, which this needs. Ask an admin for a role that includes it."` (`overflow=0`).
- **Failed `GET /api/console-sessions` (`503`):**
  - Intercepted `GET /api/console-sessions` with HTTP `503` on `/next/sessions/director` across all 4 viewport/theme combinations: `load-error` rendered `"Sessions could not be read: sessions backend temporarily unavailable. This page tries again on its own."` and `"That session does not exist."` never appeared (`overflow=0`).
- **Narrowed Inbox alert body:**
  - Parked Task #5 via real MCP `ask_question` so it sorted ahead of raised 4-line alert #1 (`Walkthrough multi-line cache alert`).
  - Narrowed `/next/inbox` without clicking the alert — once via the search box and once via the `Blocked & alerts` (`filter-alerts`) chip — in light and dark: the inspector immediately fetched and rendered the complete multi-line body through `DELTA detail 4: final diagnostic line` within `<1s`.

### Item 6: Unmigrated host-mode install notice & classic `UpgradePanel` `countError` (PR #1843, PR #1878)
- **Host-mode install notice (`grain demo -host-mode-install`):**
  - Across all 4 viewport/theme combinations: `install-notice-banner` rendered at `y=0` (`role="status"`, 0 close buttons, `h=53px` desktop / `108px` phone), `disconnected-banner` stacked directly beneath it (`y=53..89` desktop / `y=108..198` phone) with zero overlap or overflow when offline, and `/next/settings/upgrade` rendered `[data-testid="install-notice"]` with `sudo ./scripts/setup.sh`.
- **Classic `/settings/upgrade` `scheduled.countError`:**
  - Verified **live** on PostgreSQL: started busy Task #13, scheduled `POST /api/upgrade {"branch":"v2","afterTasksFinish":true}`, ran `REVOKE SELECT ON task_run FROM grain`, confirmed `GET /api/upgrade` returned `scheduled.countError`, and verified classic `/settings/upgrade` across all 4 viewport/theme combinations (plus a mocked pass over `grain demo`): MUI `warning` severity, exact `countError` wording (`the runs in flight could not be counted, so the queue stays held and the upgrade waits until they can be; the daemon log has the error.`), zero `"starts within seconds"` or `"nothing is running"`, `Cancel` inside the alert bounds, `overflow=0`. Restored `GRANT SELECT ON task_run TO grain` and cancelled the scheduled upgrade cleanly.

---

## Verification & Automated Tests

- **Go tests:**
  - `go test ./pkg/ui -run 'TestReadJSON|TestCreateTask|TestUpdateTask'` — **PASS** (including new `TestReadJSONTaskRequestsDropAliasPrefix`).
  - `go test ./tests/hygiene/... ./pkg/ports/...` — **PASS**.
- **Angular (`ui-ng/`) tests & lint:**
  - `cd ui-ng && npm run lint` — **PASS** (0 errors).
  - `cd ui-ng && npm test -- --no-watch` — **PASS** (`77` files, `831` tests, 0 errors, including `grants-picker.component.spec.ts`, `workstream-model.spec.ts`, and new `access-items.spec.ts`).
  - `make fmt-frontend` — **PASS**.
- **Playwright (`ui-ng/e2e/`):**
  - `next-full-system.spec.js` (desktop + phone, light + dark, contrast check) + `next-saved-boards.spec.js` + `next-capability-policy-pickers.spec.js` + `next-access-grant-label.spec.js` + `next-merge-failed-state.spec.js` + `next-sessions-load-error-viewer-reply.spec.js` + `next-deployment-operations.spec.js` + `next-inbox-alert-body.spec.js`: **48/48 PASS**.
- **Screenshots:**
  - Before (`HEAD` `1a449c7d5`) and after contact sheets captured by [`capture_shots.mjs`](2026-11-19-next-fixes-polish/capture_shots.mjs) at `1440×900` and `390×844` in light and dark for F2, F3, and F4, and attached to this task's closing note (PNGs cannot be visually decoded by the sandbox's text-only `read_file`; every layout/overlap/color claim is additionally pinned by DOM, `elementFromPoint`, computed-style, and bounding-box checks).
