# Test run: 2026-11-19 §5 `/next/` task lifecycle parity (edit/New task fields, close reasons, batch `Defer until…`, `#comment=` deep links, and `Retry merge` header)

- **Date:** 2026-11-19 – 2026-11-20 UTC.
- **Task:** [Task #7321](/tasks/7321) (filed by [Task #7303](/tasks/7303), test plan `docs/test-plans/2026-11-19.md` §5 covering PRs #1581, #1538, #1660, #1854).
- **Branch:** `grain/53549e42/task-7321-walkthrough-next-task-edit-new`, base `main`.
- **Base commit walked:** `1a449c7d5` (`Merge pull request #1913`), then re-walked on the rebuilt fixed binary against both SQLite and PostgreSQL.
- **Overlap check:** `git log origin/main -- docs/test-runs/` confirmed no prior `2026-11-19` report covering §5 (`docs/test-runs/2026-11-18-task-intake-inbox-lifecycle.md` predated PRs #1581, #1538, #1660, and #1854).

## Environment

- **Sandbox:** Debian `linux/amd64`; Go `go1.26.10` (`/usr/local/bin/go`); Node.js `v20.19.0`; Playwright `@playwright/test` `1.62.1` + `@axe-core/playwright` `4.11.0`; headless Chromium `151.0.7922.34` (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- **Packages installed:** `postgresql`, `postgresql-client` (`15.20`), and `sqlite3` (`3.40.1`) were missing from the stock sandbox image and installed via `apt-get`; `jq` was already present.
- **Build:** `make frontend && go build -o /home/debian/bin/grain ./cmd/grain` (plus `cd ui-ng && npm ci` to restore devDependencies for Vitest/Playwright/axe), then `cd ui-ng && npm run build && cd .. && go build -o /home/debian/bin/grain ./cmd/grain` after fixes.
- **Real-daemon harness (`docs/test-runs/2026-11-19-next-task-lifecycle-parity/`):**
  - `grain daemon -sandbox-port-base=0 -host-sandboxes -max-workers 2 -max-mergers 1 -poll-interval 500ms` backed by SQLite (`STORE=sqlite`) and PostgreSQL (`STORE=postgres`, local cluster `15/main` on `127.0.0.1:5432`).
  - `ghsim.go` (`pkg/github/githubsim` + `git-http-backend` on `127.0.0.1:4294`): serves `acme/widgets` (default `main`) plus fork `alice/widgets` with an open fork PR `#99` (`alice:fix-branch` → `main`) and `/_sim/refuse-merges` for real merge refusal injection.
  - `tests/iap/iapsim.go` (issuer `127.0.0.1:4292`, proxy `127.0.0.1:4293`) with real IAP principals registered via `POST /api/access/principals`:
    - `alice@example.com` (`grain/operator` — unlimited human operator)
    - `bob@example.com` (`grain/contributor` — scoped human operator: `tasks:write`, `tasks:read`, no unlimited role, no `tasks:admin`)
    - `dave@example.com` (custom scope set `walk-dispatcher`: `tasks:write` + `dispatch:write`, no `tasks:admin` / `approve`)
    - `carol@example.com` (`grain/viewer` — read-only viewer, no `merge:write`)
  - Scriptable fake agent (`docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py`) used for live running-task close, busy worker-slot holds, and pushing real commits to open PR `#9001` on `ghsim`.
- **Surfaces driven:** Only `/next/` routes (`/next/tasks`, `/next/task/<id>`, `/next/workstreams/...`; zero classic pages).
- **Viewports & themes:** `1440×900` (desktop) and `390×844` (phone), light and dark (`colorScheme: "light"` / `"dark"`), timezones `UTC`, `America/Los_Angeles`, and `Asia/Tokyo`.

## Execution summary

Both SQLite and PostgreSQL runs executed the same 53 automated checks in `docs/test-runs/2026-11-19-next-task-lifecycle-parity/walk.mjs`:

- **Before fixes (base `1a449c7d5`):** `49 / 53` passed, **4 real UI defects reproduced** identically on SQLite and PostgreSQL.
- **After fixes (rebuilt binary):** **`53 / 53` passed, `0` findings** on **both** SQLite and PostgreSQL, with zero uncaught page errors, zero horizontal overflow, and zero serious/critical axe violations.

### Area 1 — Edit panel & change-only `PATCH /api/tasks/{id}`, audit trail, and locked fields

| Step | Action | Result (SQLite & PostgreSQL) |
|---|---|---|
| 1.1 | Open `/next/task/<id>` Edit on a task created with `autoMerge: true`; type `0`, `-3`, `2.5` into Task priority. | **PASS** — inline refusal `"a whole number, 1 or more; default 5, higher numbers run first"`, `aria-invalid="true"`, `Save changes` disabled for all three. |
| 1.2 | Fill External ref `EXT-101` and priority `1e3`; flip `autoMerge: false` out of band via API (`PATCH`) and wait for SSE. | **PASS** — Save enabled for `1e3`; open Edit draft survived SSE refresh (`externalRef="EXT-101"`, `priority="1e3"`). |
| 1.3 | Click `Save changes` and inspect `PATCH /api/tasks/{id}` + stored task + `transitions`. | **PASS** — `PATCH` sent `priority: 1000`, `externalRef: "EXT-101"`, and omitted `autoMerge`, `owner`, `reviewers`, `admission`, `hosting`, `keepRunning`, `fixesBaseBranch`. `autoMerge` remained `false`; zero spurious `owner_changed` audit rows. |
| 1.4 | Edit Owner (`bob@example.com`), Reviewers (`carol, dave`), Admission (`Bypass queue` → reload → `Durable`), Hosting (`Interactive console`), Keep running, and "This fixes a red `main`". | **PASS** — every field persisted across reload on both SQLite and PostgreSQL. |
| 1.5 | Inspect History after owner/admission/hosting changes. | **FAIL on base → PASS after Fix 1** — base hid `from <old> to <new>` (rendered only `owner changed · alice@example.com`) and lacked spacing before `—`. Fixed build renders `Owner changed from alice@example.com to bob@example.com · alice@example.com`, `Admission changed from Standard to Bypass queue · alice@example.com`, `Hosting changed from Unattended to Interactive console · alice@example.com`, and spaced ` — <reason>`. |
| 1.6 | Open Edit on real built-in Director console session task (`#1`, `consoleSession: "director"`). | **PASS** — Admission pill select is disabled/locked. |
| 1.7 | Open Edit on task carrying grain's base-repair tag (`base-ci-repair:acme/widgets:main` without `fixes-base-branch`). | **PASS** — "This fixes a red `main`" checkbox is checked and disabled/locked. |
| 1.8 | As scoped operator `bob@example.com` (`grain/contributor`), open Edit on a task. | **FAIL on base → PASS after Fix 2** — base left `edit-keep-running`, `edit-auto-merge`, `edit-auto-approve`, and `edit-admission` enabled (saving `keepRunning` returned HTTP 403). Fixed build disables them with denial tooltips and refuses raising priority above `5` inline. |

### Area 2 — New task sheet: `No repository`, fork-head validation, and role gating

| Step | Action | Result (SQLite & PostgreSQL) |
|---|---|---|
| 2.1 | As `alice@example.com`, open New task (`c`), choose `No repository`, fill Owner `bob@example.com`, Keep running checked, priority `25`, submit. | **PASS** — `nt-base`, `nt-new-branch`, and `nt-auto-merge` detached from DOM; `POST /api/tasks` sent `noRepo: true, repo: "", base: "", owner: "bob@example.com", keepRunning: true, priority: 25` without `autoMerge`; persisted cleanly (HTTP 201). |
| 2.2 | Open New task on `acme/widgets` (uncheck `Create a new branch`), type invalid fork heads (`bad::label`, `-bad:x`, `alice:a..b`), then valid `alice:fix-branch` and submit against real `ghsim` (open fork PR `#99`). | **PASS** — all invalid heads showed `nt-fork-head-error`, `aria-invalid="true"`, and disabled submit; `alice:fix-branch` showed inline help (`branch fix-branch on alice's fork`) and resolved cleanly against `ghsim` (`HTTP 201`, stored `base: "main"`). |
| 2.3 | As `bob@example.com` (`grain/contributor`), open New task; try priority `25`, inspect Keep running & Approve, submit as proposal, and attempt direct API `POST /api/tasks` with `keepRunning: true`. | **PASS** — `nt-keep-running` disabled, `approved-switch` disabled, `nt-owner` enabled, priority `25` refused inline, submitted task landed in `proposed`, and direct API `keepRunning: true` returned HTTP 403. |
| 2.4 | As `dave@example.com` (`walk-dispatcher`: `tasks:write` + `dispatch:write`, no `tasks:admin`), inspect unapproved `proposed` task vs approved `queued` task. | **PASS** — `act-run-now` hidden on unapproved task (`POST /run-now` returned HTTP 403); once approved by `alice`, `act-run-now` appeared for `dave`. |

### Area 3 — Reversible close reasons, Undo, running close, and Reopen History

| Step | Action | Result (SQLite & PostgreSQL) |
|---|---|---|
| 3.1 | Open `/next/task/<queuedId>` (approved `queued` task blocked on a dependency), click `Close`, verify inline row, press `Escape`. | **PASS** — zero `[role="alert"]` warning, secondary button reads `Cancel`, `Escape` dismisses and restores focus to `Close`. |
| 3.2 | Click `Close`, leave reason blank, press `Enter`, then click `Undo` on snackbar. | **PASS** — closes cleanly, snackbar offers `Undo`, clicking `Undo` restores task to `queued`. |
| 3.3 | Click `Close`, enter `"queued close reason walkthrough"`, press `Enter`, reload. | **PASS** — reason persists in History across reload. |
| 3.4 | Dispatch a real live `running` task (busy fake-agy run), click `Close`, enter `"stopping live run reason"`, confirm. | **PASS** — `[role="alert"]` warning `"Closing stops the run that is going now. This cannot be undone."`, secondary button reads `Keep`, zero `Undo` snackbar action, live run stops and task transitions to `closed`. |
| 3.5 | Reopen with reason `"reopened after verifying stop"` (`POST /api/tasks/{id}/reopen`), reload. | **PASS** — both close reason and reopen reason persist in History across reload (`Reopened · alice@example.com — reopened after verifying stop` and `Closed · alice@example.com — stopping live run reason`). |

### Area 4 — Batch `Defer until…` with non-UTC timezones & live orchestrator

| Step | Action | Result (SQLite & PostgreSQL) |
|---|---|---|
| 4.1 | Hold both worker slots (`max-workers=2`) with 2 busy tasks; select 3 approved Backlog tasks under Playwright `timezoneId: "America/Los_Angeles"` in dark & light themes; open `Defer until…`. | **PASS** — dark theme bar `rgb(228, 230, 235)` sets date input `colorScheme: "light"`; light theme bar `rgb(40, 44, 53)` sets `colorScheme: "dark"`; zero serious/critical axe violations in both themes. |
| 4.2 | Defer all 3 Backlog tasks to `2026-12-15` under `America/Los_Angeles` (PST, UTC−8). | **PASS** — all 3 stored `deferredUntil: "2026-12-15T08:00:00Z"`, selection bar closed. |
| 4.3 | Select 3 `Needs approval` (`proposed`) tasks under `timezoneId: "Asia/Tokyo"` (JST, UTC+9), defer to `2026-12-15`; select another task and reopen `Defer until…`. | **PASS** — all 3 stored `deferredUntil: "2026-12-14T15:00:00Z"`, selection cleared, and date input reset to `""`. |
| 4.4 | Release the 2 busy tasks so both live workers are free and wait 5+ reconcile cycles. | **PASS** — all 6 deferred tasks stayed `deferred` with `attempts: 0`. |

### Area 5 — `#comment=` deep links on a multi-page thread (>120 comments) with live polling

| Step | Action | Result (SQLite & PostgreSQL) |
|---|---|---|
| 5.1 | Seed task `B` with 125 real comments (spanning multiple API pages); open `/next/task/<B>#comment=<oldId1>`; post 2 new comments (#126, #127) while paged back and wait 4.8s for both live polling and `HIGHLIGHT_MS` (4000ms) expiry. | **FAIL on base → PASS after Fix 3** — base dropped `tabindex="-1"` at 4s, blurring the comment (`focused=false, outline=none`). Fixed build keeps focus, `tabindex="-1"`, and `outline: solid` after highlight fade (`highlight=false, focused=true, outline=solid`). Live polling rendered all 127 comments with zero seam drops (`missing=[]`). |
| 5.2 | Open `/next/tasks?tab=proposed&task=<A>&pane=conversation`, click cross-task link `/next/tasks?tab=proposed&task=<B>#comment=<oldId1>`, then `Back` and `Forward`. | **PASS** — lands and focuses `oldId1` on task `B`, `Back` returns to task `A`, `Forward` re-focuses `oldId1` with zero missing-comment toast. |
| 5.3 | Open `/next/task/<B>`, delay `comments_before` by 500ms, rapidly click `[old one]` then `[old two]` 80ms apart. | **PASS** — coalesced to 1 `comments_before` request and landed focused on `oldId2` (`#comment=<oldId2>`). |
| 5.4 | Click `[bad pct]` (`#50%off`) link. | **PASS** — zero page errors, hash normalized without throwing. |
| 5.5 | Intercept `comments_before` with `503 {"error":"database busy on page-back"}` and follow `[old one]`. | **PASS** — error banner displays `"database busy on page-back"` and never shows `"is not in this task's conversation"`. |
| 5.6 | At phone width (`390×844`), open `/next/tasks?tab=proposed&task=<B>#comment=<oldId1>`. | **PASS** — redirects to full page `/next/task/<B>?from=tasks:proposed#comment=<oldId1>`, focuses `oldId1`, zero overflow, zero axe violations. |

### Area 6 — Primary `Retry merge` header action on a real `merge_failed` task

| Step | Action | Result (SQLite & PostgreSQL) |
|---|---|---|
| 6.1 | Set `ghsim` `/_sim/refuse-merges?value=Branch+protection+rule+refused+merge`, run a real task pushing a branch to `ghsim` with `autoMerge: true` and `origin: task:<parent>`. | **PASS** — daemon opened real PR `acme/widgets#9001`, attempted merge, received HTTP 405, and recorded `state: "completed"`, `mergeQueueState: "merge_failed"`, `reason: {reason: "completed/merge_failed", control: "Retry merge"}`. |
| 6.2 | Check `/next/task/<id>`, `/next/tasks?tab=merging&task=<id>`, and `/next/workstreams/task:<parent>?node=<id>` as `alice@example.com`. | **PASS** — primary `Retry merge` (`.btn-primary`, first header button) shown on all three surfaces; `Run now` absent from header bar and present inside `More ▾`. |
| 6.3 | Open `More ▾` menu at phone dark (`390×844`) and run axe. | **FAIL on base → PASS after Fix 4** — base reported 3 severe axe violations (`aria-required-children`, `aria-required-parent`, `listitem`) because `<li>` children of `<ul role="menu">` lacked `role="none"`. Fixed build passes with zero axe violations. |
| 6.4 | Click `Pause`, then `Resume`. | **PASS** — pausing hides `Retry merge` and shows `Resume`; resuming restores `Retry merge`. |
| 6.5 | Open `/next/task/<id>` as `carol@example.com` (`grain/viewer`, no `merge:write`) and test `POST /retry-merge`. | **PASS** — `Retry merge` hidden in UI; direct API call refused with HTTP 403 (`holds no scope merge:write`). |
| 6.6 | Clear `ghsim` merge refusal and click `Retry merge` as `alice@example.com`. | **PASS** — `POST /api/tasks/<id>/retry-merge` returned 200; header transitioned live to `Merging` (removing `.error` and `Retry merge`) with zero page navigations; live merge queue merged PR `#9001` on `ghsim` and closed the task. |

### Area 7 — Responsive & light/dark visual verification (`1440×900` & `390×844`)

Captured 17 views before and after fixes across desktop (`1440×900`) and phone (`390×844`), light and dark themes (Edit panel, New task sheet, inline close row, batch `Defer until…` bar, `#comment=` deep link, `Retry merge` header + `More ▾` menu). Verified:
- `0px` horizontal overflow across every desktop and phone view;
- `0` serious or critical axe violations across every view after Fix 4;
- Light and dark screenshots verified byte-distinct on every paired surface;
- Before and after `contact-sheet.png` images attached to [Task #7321](/tasks/7321).

## Bugs found and fixed

### Bug 1 — `/next/` History omitted `from X to Y` on owner/admission/hosting changes and ran `— reason` into the actor name
- **Reproduction:** Edit a task's Owner (`alice@example.com` → `bob@example.com`), Admission (`Standard` → `Bypass queue`), or Hosting (`Unattended` → `Interactive console`), then view History on `/next/task/<id>`. History printed only `now owner changed · alice@example.com` (dropping `oldOwner`/`newOwner` even though `GET /api/tasks/{id}` returns them) and rendered close/reopen reasons with no leading space (`alice@example.com— stopping live run reason`).
- **Fix (`ui-ng/src/app/task/task-detail.component.ts`):** Added `historyLabel` (sentence-case labels matching classic `Timeline.jsx`) and `historyDetail` (`from <old> to <new>` for `owner_changed`, `admission_changed`, and `hosting_changed`), plus explicit spacing before `·` and `—`.

### Bug 2 — Task Edit panel left operator-only controls enabled for scoped roles (`grain/contributor`)
- **Reproduction:** Sign in via IAP as `bob@example.com` (`grain/contributor`, `full: false`), open Edit on a task, tick `Keep running`, and click `Save changes`: `PATCH /api/tasks/{id}` returns HTTP 403 (`keepRunning requires an unlimited role (grain/operator)`).
- **Fix (`ui-ng/src/app/task/task-edit-panel.component.ts`):** Gated `edit-keep-running`, `edit-auto-merge`, `edit-auto-approve`, `edit-admission`, `edit-owner`, and raising `edit-priority` above `5` with the same `AccessService` rules `NewTaskSheetComponent` and `pkg/ui/agent_api_gate.go` enforce, with denial tooltips, and made `priority` and `autoApproveProposals` change-only in `PATCH` so a scoped operator can edit unrelated fields on a high-priority task without hitting 403.

### Bug 3 — `#comment=` deep link dropped focus and focus ring when the 4-second highlight fill expired
- **Reproduction:** Visit `/next/task/<id>#comment=<oldId>` in real Chromium and wait 4.5s (`HIGHLIGHT_MS = 4000ms`). `ConversationComponent` bound `[attr.tabindex]="c.id === highlightedCommentId() ? -1 : null"`, so clearing `highlightedCommentId` removed `tabindex="-1"`, causing Chromium to blur the `<article>` and drop the focus ring (`focused=false, outline=none, tabindex=null`).
- **Fix (`ui-ng/src/app/shared/conversation/conversation.component.ts`, `ui-ng/src/app/shared/conversation.css`, `design/design/ui-design-book.design.md`):** Separated `focusedCommentId` (cleared on `(blur)` or task switch) from `highlightedCommentId` (cleared after `HIGHLIGHT_MS`), bound `tabindex="-1"` and `.comment-focused` to `focusedCommentId`, and styled `.msg.comment-focused:focus` with the 2px primary outline so focus and the focus ring outlast the fade fill until focus moves away.

### Bug 4 — `More ▾` lifecycle menu (`act-more-menu`) caused 3 severe axe ARIA violations when open
- **Reproduction:** Open `More ▾` on a `completed` / `merge_failed` task and run axe: 2 critical (`aria-required-children`, `aria-required-parent`) and 1 serious (`listitem`) violations because `<li>` wrappers inside `<ul role="menu">` lacked `role="none"`.
- **Fix (`ui-ng/src/app/task/task-detail.component.ts`):** Added `role="none"` to every `<li>` inside `act-more-menu`, matching `processes-page.component.ts` and `qa-flows-panel.component.ts`.
- **Superseded on merge:** `main` (PR #1580, "one action area per task") later removed the `More ▾` menu entirely — `Run now` is now a quiet `.btn-text` button after the divider in the `Task actions` group. When this branch was merged with `main`, the `role="none"` fix went with the menu, and the regression now runs axe on the `merge_failed` header with its primary `Retry merge` (`ui-ng/e2e/next-task-lifecycle-parity.spec.ts`).

## Regressions & verification run

- **Unit tests (`cd ui-ng && npx vitest run src/app/task src/app/shared/conversation src/app/create src/app/tasks`):** 14 files, 221 tests passed (including new tests in `conversation.component.spec.ts` and `task-page.component.spec.ts`).
- **Lint, format & build:** `npm run lint` clean, `prettier --check` clean on all touched files, `npm run build` succeeded with no new warnings or CSS budget changes.
- **Playwright e2e (`ui-ng`):** 44 / 44 passed across `desktop`, `phone`, and `dark` projects (`next-full-system.spec.js`, `next-task-edit-fields.spec.js`, `next-defer-until.spec.js`, `next-comment-deep-link.spec.ts`, `next-retry-merge-header.spec.ts`, and new regression spec `next-task-lifecycle-parity.spec.ts`).
- **Go hygiene & route sync:** `go test ./tests/hygiene/...` and `go test ./pkg/ui -run TestAngularRouteTableInSync` passed.
- **Full real-daemon walkthrough (`walk.mjs`):** 53 / 53 passed on SQLite and 53 / 53 passed on PostgreSQL.
