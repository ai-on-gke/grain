# Responsive navigation and live-session UI walkthrough — 2026-11-19

This walkthrough (task 7311) exercised the shared navigation shell and the
live-session surfaces at desktop, tablet, and phone viewports in real
Chromium and WebKit browsers against a real built `grain demo` server.
Following `PROMPT.md` ("Two UIs"), every step ran against the Angular UI
at `/next/` wherever `ui-ng/src/app/shell/nav.ts` or
`ui-ng/src/app/app.routes.ts` provides the page, and against the classic
React UI at `/` only for surfaces that have not moved over yet.

Seven real bugs were found in `/next/` and fixed on this branch, with unit
and Playwright E2E regression coverage (`ui-ng/e2e/next-responsive-nav-sessions-regressions.spec.ts`).
No classic page needed a fix.

## Environment and build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-7311-walk-through-responsive`.
- **Base commit tested:** `21d4bfc9f` (`main` at the start of the run). All
  fixes were rebuilt and re-verified against a fresh `grain demo` binary.
- **Build:** `npm ci && npm run build` in `ui/` and `ui-ng/`,
  `npm ci --omit=dev && npm run build` in `ui-hub/`, and `make grain`
  (`bin/grain`, Go `1.26.10`, Node `20.19.0`).
- **Server:** `bin/grain demo -addr 127.0.0.1:4291 -open=false -staging
  -host-mode-install -config-change -access-code-file <temp-file>`, over its
  throwaway embedded SQLite store.
  - Signed in through the real login form as `alice@example.com` (operator,
    access code).
  - `-staging` seeded two persistent staging environments (`docs-preview` on
    `/staging/docs-preview/` and `grain-staging` on `http://127.0.0.1:4280/`).
  - `-host-mode-install` enabled the standing host-mode install notice.
  - `-config-change` seeded a throwaway `grain/config` repository and two
    config-change proposal tasks (`#24`, `#25`).
- **Browsers:** headless Chromium 1234 and WebKit 2336
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, Playwright from
  `ui-ng/node_modules`).
- **Viewports and modes:**
  - Desktop: `1440×900` (and `1024×768` landscape / short desktop `1440×680`)
  - Tablet: `1024×768` landscape, `820×1180` portrait (`hasTouch: true`)
  - Phone: `390×844`, short phone `390×667`, narrow phone `320×568`, phone
    landscape `844×390` (`hasTouch: true`, `isMobile: true`)
  - Light and dark `colorScheme`, plus `reducedMotion: "reduce"` for
    reduced-motion and axe-core WCAG AA checks.
- **Visual verification:** `/home/debian/.gemini-api-key` is not present in
  this sandbox, so `LOOK=1` was unavailable. Before and after screenshots
  were captured via `ui/e2e/screenshot.mjs` into `/tmp/walk/screenshots/{before,after}`
  (desktop, tablet, phone × light, dark on `/next/settings/preferences`,
  `/next/sessions/director`, `/next/inbox`) and `contact-sheet.png` is
  attached to the task closing note rather than committed. Layout and
  interaction geometry were asserted with bounding boxes, `scrollWidth`,
  `elementFromPoint` hit tests, and `@axe-core/playwright` (`wcag2a`,
  `wcag2aa`).
- **Scripts:** throwaway Playwright walkthrough drivers ran from `/tmp/walk/`
  outside the checkout, driving visible UI controls.

## Stubs

Only unavailable external or live fault boundaries were intercepted via
Playwright `page.route`; navigation, Angular/React rendering, browser layout,
and the real `grain demo` server stayed in the loop.

| Boundary | Stub | Why |
| --- | --- | --- |
| `**/api/**` | aborted `connectionrefused`, then passed through | simulate unreachable daemon and automatic recovery without reload |
| `**/api/**` | held unanswered >10 s, then released | simulate daemon accepting connections without answering, then recovering |
| `**/api/tasks` (`GET`) | `500` on classic phone and `/next/tasks` | exercise stale-tasks retry / sustained-failure notice |
| `**/api/tasks` (`GET`) | real response with walkthrough session task set to `state: "running"` | simulate an in-flight turn to exercise the `Apply now (restart turn)` banner |
| `**/api/tasks/*/comments` (`POST`) | 1.2 s delay, then `500`, then passed through | exercise optimistic `Sending…`, `Queued`, `Not sent` + `Edit`/`Retry` |
| `**/api/console-sessions` (`GET`) | `500 {"error": "session store unavailable"}`, then passed through | exercise session list load-error handling and recovery |
| `**/api/console-sessions` (`GET`) | real response with `effectiveNetworkAccess: "open"` alongside `networkAccess: "none"` and `missingShippedDefaults` | exercise non-isolated network warning and `Adopt shipped defaults` |
| `**/api/console-sessions/{id}` (`PUT`) | `409 {"code": "stale", ...}` once, then passed through | exercise stale-tab optimistic-concurrency refresh and retry |
| `**/api/tasks/13*` (`GET`) | real response with `pendingAccess` and `localScriptApprovals` attached | exercise inline `request_access` and local-script approval cards on Task and Sessions |
| `**/api/config` (`GET`) | real response with `access` set to viewer read-only scopes (`tasks:read`, `sessions:read`, `repos:read`, `ci:read`) | exercise viewer role gating (demo has no identity proxy to sign in a viewer) |
| `**/api/config` (`GET`) & `**/api/whoami` | real config + `environment: "staging"`, verified IAP identity with long email and `logoutUrl` | exercise classic phone environment chip and stacked `Signed in as` / `Log out` layout |

## Coverage by UI

### Surfaces exercised in `/next/` (Angular UI)

1. **Full viewport & WebKit sweep (49 routes + 15 redirects):**
   - Visited Inbox, Workstreams (list + detail), Tasks, Agents, Task detail
     (`#13`, `#4`, `#24`), Sessions, Director, Processes (all 5 tabs:
     Schedules & Triggers, Templates, Suites & Runs, Repo Workflows,
     Software Workflow & QA), CI, Repos (list + all 7 repo tabs: Overview,
     CI & Workflows, Merge Queue, Agents & Grants, Branches, Releases,
     Advanced), Telemetry (Overview + Quality checks), Context, Fleet (all 4
     tabs including Logs and Pauses & Controls), and all 20 Settings
     sections across all 5 section groups (`models`, `sandboxes`, `hostds`,
     `repositories`, `git`, `ci`, `config-repo`, `tasks`, `agents`,
     `capabilities`, `memory`, `access`, `audit`, `general`, `preferences`,
     `cli`, `caches`, `engine`, `registry`, `upgrade`).
   - Verified zero console errors, zero uncaught page errors, zero failed
     API requests, zero forbidden team/admin-mode strings (`All teams`,
     `Admin mode`, `Switch team`), and clean dynamic resizing across
     `1440×900` → `1024×768` → `820×1180` → `390×844` → `844×390` → `320×568`
     → `1440×900`, browser Back/Forward, and hard reloads.
   - Verified all legacy and teamless redirects: `/next/t/acme/{tasks,inbox,sessions,repos}`
     and `/next/t/acme`, `/next/director`, `/next/system{,/metrics,/logs,/pipeline,/caches,/upgrade}`,
     `/next/admin/{models,logs}`, and `/next/repos/acme/widgets/settings/ci`.
   - WebKit pass over 14 primary routes at `1440×900`, `820×1180` (touch, dark),
     and `390×844` (touch, mobile, light): **0 overflow/clipping/console issues**.
   - **Finding B3:** `/next/settings/preferences` clipped the `Dark` and
     `System` buttons of `.segmented` past the viewport at `820×1180`,
     `390×844`, and `320×568`. Fixed and re-verified at all four widths.

2. **Shared shell, rail, compact phone nav, banners, focus, viewer role, motion, WCAG AA:**
   - Verified Account popover (`0`), `alice@example.com (access code)` label,
     role summary, and Escape focus restoration to `avatar-btn`.
   - Verified Create menu (`New ▾`) items and Escape focus restoration to
     `rail-create-menu`.
   - Verified Customize navigation hide/show/reset persistence across reloads
     and live cross-tab synchronization (`rail-hidden-count` synced between
     two open tabs).
   - **Finding B6:** Clicking a partially clipped `More` (`rail-more`) trigger
     at `1440×900` (with `.install-banner`) or `1024×768` let the browser's
     focus-scroll fire `.nav-rail`'s `scroll` listener (`onViewportChange`),
     immediately closing `#rail-more-menu` on the first click. Fixed to
     reposition open rail menus while the anchor stays in `.nav-rail` bounds.
   - Verified single-key shortcuts (`c`, `d`, `?`) are ignored while typing in
     the search input and disabled when `Shortcuts off` is toggled.
   - Verified phone compact bar (`Inbox`, `Tasks`, `Sessions`, `More`),
     floating New task FAB (present on Inbox, hidden on Sessions so it never
     covers the composer), and `more-sheet` staying inside viewport bounds
     and closing on navigation.
   - Verified standing banners together: `.install-banner` and
     `.conn-banner` stack in flow above `.top-bar` with zero overlap for
     both `connectionrefused` and >10 s hung/held connections, and
     `.conn-banner` clears automatically without reload once requests answer.
   - Verified Director drawer (`d` / `director-trigger`) at desktop, tablet,
     and phone widths, Escape closing and restoring focus to `director-trigger`.
   - Verified declined discard prompt focus restoration on dirty Processes
     template dialog (`Escape` → `Cancel` returns focus into the edited input).
   - **Finding B5:** Pressing `Escape` while `GrantsPicker`'s builder
     (`+ Add API scope or method grant`) was open closed the enclosing
     `NewTaskSheet` or `Session settings` panel instead of closing only the
     builder. Fixed so inner `PillSelect` closes on first Escape, the grants
     builder closes on second Escape (restoring focus to `#addOpenBtn`), and
     the enclosing overlay stays open.
   - Verified viewer role without write scopes: rail New task FAB disabled,
     all 8 Create menu actions disabled, Task `#4` composer disabled with
     reason, Sessions hides New session / composer / settings toggle, and
     Director drawer offers 0 enabled composers.
   - Verified `prefers-reduced-motion: reduce`: 0 running animations on
     `/next/sessions/director` and static SVG brand mark rendered instead of
     animated sprite.
   - Verified `@axe-core/playwright` (`wcag2a`, `wcag2aa`) across 11 `/next/`
     pages × 2 themes × 2 viewports (44 scans) plus `/next/sessions/director`
     with the new network warning and inline approval cards in light & dark:
     **0 serious or critical violations**.

3. **Sessions (`/next/sessions`), Director, and interactive task chat (`/next/task/13`):**
   - Verified `/next/director` redirect, failed `GET /api/console-sessions`
     500 reporting `load-error` with no false empty or not-found message and
     auto-recovering, and deep link to unknown session showing
     "That session does not exist." with link back to `/next/sessions`.
   - Verified New session from Create menu, Escape on inner Model dropdown
     keeping dialog open, reachability of `Start session` and chat `Send` on
     short phones (`390×667`, `320×568`), and composer focused automatically
     after creation.
   - Verified optimistic `Sending…`, back-to-back `Queued` sends, rich
     Markdown (8-column wide table, fenced code, 140-char unbroken token)
     with `0px` horizontal overflow on desktop and phone, per-session draft
     preservation across session switches, Canvas open/close, stale-tab 409
     `{code: "stale"}` refresh-and-retry (2 PUTs), `Apply now (restart turn)`
     calling `POST /api/console-sessions/{id}/cancel`, `Adopt shipped defaults`,
     `Start a fresh chat` (with handover and clean), `Delete` with Cancel
     focus restoration and confirmed deletion, and Pause / Resume persistence
     across reload on interactive Task `#13`.
   - **Finding B1:** Toggling `Tasks approved` in `/next/sessions` called
     `POST /api/tasks/{id}/approve` on the session's own task and dropped
     `tasksApproved` instead of saving via `PUT /api/console-sessions/{id}`.
     Fixed and verified persisted across reload.
   - **Finding B2:** `/next/sessions` showed no warning when
     `effectiveNetworkAccess` differed from `networkAccess: "none"`. Fixed
     with an accessible warning banner (`data-testid="session-network-not-isolated"`).
   - **Finding B4:** On a scrollable chat, a failed send did not scroll
     `Retry`/`Edit` into view, and `.reply-textarea:focus { min-height: 76px }`
     shrank `.chat-composer` by 18 px on `mousedown` when blurring, sliding
     `Edit`/`Retry` downward out from under the cursor before `mouseup`.
     Fixed both the failure auto-scroll and steady composer height.
   - **Finding B7:** `/next/sessions` did not render inline `request_access`,
     `localScriptApprovals`, `pendingSecret`, or `pendingConfigChange`
     approval cards for the session's backing task. Fixed by mounting
     deferred `<g-task-gates-bar>` in `SessionsPageComponent`.

### Surfaces with no `/next/` equivalent yet (exercised on classic `/`)

The following surfaces do not exist in `/next/` yet and were walked on
classic `/`:

- **Boards (`/boards`):** verified all four seeded visualizations (`board-1`
  Kanban Board, `board-2` Timeline/Gantt, `board-3` Report, `board-4` Tree)
  with `0px` overflow at `1440×900`, `820×1180`, and `390×844`.
- **Single-row page header rail & overflow menu:** verified single-row height
  (`77px`) across `1440×900`, `1024×768`, and `820×1180` with Alerts, System,
  and Settings at the trailing edge; verified `More page actions` auto-opens
  on `Tab` into a wrapped action and closes on `Escape` restoring focus.
- **Collapsible rail & `[` shortcut:** verified `[` collapses/expands the
  sidebar, ignores typing in inputs, and persists across reloads; verified
  collapsed `UserTile` tooltip (`alice@example.com (access code)`).
- **Sidebar Customize pins cross-tab sync & Staging list:** verified
  `Customize sidebar` syncs pins across two open tabs, and Staging lists
  `docs-preview` and `grain-staging`.
- **Classic phone bar & modal navigation drawer (`390×844`):** verified
  `0px` overflow, Alerts panel inset cleanly from edges (`x=16`, `width=358`),
  stale-tasks retry notice when `/api/tasks` fails, staging-pin tap keeping
  the modal drawer open, long verified email displayed, build stamp at foot
  (`bottom=829.6` of `844`), and Escape closing the drawer.
- **Classic `/sessions/new`:** verified delayed creation queues follow-up
  sends into the opened session, and deleting a pinned session removes its
  sidebar pin.
- **Classic old `/t/<team>/...` links & WebKit:** verified `/t/acme/{tasks,inbox,sessions,repos}`
  land on their team-less pages and classic `/`, `/boards`, and
  `/sessions/director` render with `0px` overflow on WebKit desktop & phone.

## Bugs found and fixed (`/next/`)

| ID | Bug | Deterministic reproduction | Fix |
| --- | --- | --- | --- |
| B1 | Toggling `Tasks approved` in `/next/sessions` never persisted `session.tasksApproved`. | Open `/next/sessions/director` → `Grants & settings` → toggle `Tasks approved` → reload; value reverts because `SessionsStore.updateSettings` posted `POST /api/tasks/{id}/approve` and deleted `tasksApproved`. | `ui-ng/src/app/sessions/sessions.store.ts`: send `tasksApproved` through `PUT /api/console-sessions/{id}` like every other session setting. |
| B2 | `/next/sessions` hid the warning when `networkAccess: "none"` cannot be honoured (`effectiveNetworkAccess !== networkAccess`). | Return `networkAccess: "none", effectiveNetworkAccess: "open"` from `GET /api/console-sessions` and open `Grants & settings` on `/next/sessions/director`. | `ui-ng/src/app/sessions/sessions-page.component.ts` & `.css`: render warning banner (`data-testid="session-network-not-isolated"`, `role="status"`) using warning tokens and `<g-status-icon>`. |
| B3 | `/next/settings/preferences` theme segmented control clipped `Dark` and `System` off the viewport at tablet (`820px`) and phone (`390px`, `320px`). | Open `/next/settings/preferences` at `820×1180`, `390×844`, or `320×568`: `.segmented` shrunk to `104px` inside `.s-toggle-row`, pushing `System` to `right=431` of `390`. | `ui-ng/src/app/settings/settings-forms.css` & `ui-ng/src/app/shared/surface-controls.css`: allow `.s-toggle-row` to wrap (`flex-wrap: wrap`, `.s-toggle-copy { flex: 1 1 200px }`) and keep `.segmented` unshrunk (`flex-shrink: 0; max-width: 100%`). |
| B4 | In a scrollable session chat, failed sends left `Retry`/`Edit` below the fold, and clicking `Edit` while the composer was focused missed the button due to an 18 px blur-shrink jump. | At `1440×680` on `/next/sessions/director`, return `500` on `POST /api/tasks/*/comments`, send a message, and click `Edit`: first `Edit` sits behind `.chat-composer`; after scrolling, `.reply-textarea:focus` shrinks on `mousedown`, moving `Edit` 18 px down before `mouseup`. | `ui-ng/src/app/sessions/sessions-page.component.ts`: track failed outbox count and auto-scroll on new failures. `sessions-page.component.css` & `shared/conversation/conversation.css`: hold `.chat-composer` / `.convo-composer` `.reply-textarea` at a steady `min-height: 56px` across `:focus`. |
| B5 | Pressing `Escape` while `GrantsPicker`'s builder (`+ Add API scope or method grant`) was open closed the enclosing `NewTaskSheet` or `Session settings` panel. | Open `New task` (`c`) → expand grants → click `+ Add API scope or method grant` → press `Escape`; the whole dialog closes. Same in `/next/sessions/director` `Grants & settings`. | `ui-ng/src/app/shared/grants-picker.component.ts`: add a capture-phase `Escape` listener while `builderOpen()` is true that defers to any open inner `[role="listbox"]`, stops propagation, closes only the builder, and restores focus to `#addOpenBtn`. |
| B6 | Clicking a partially clipped `More` (`rail-more`) trigger in a scrollable `.nav-rail` slammed `#rail-more-menu` shut on the first click. | At `1440×900` with `-host-mode-install` (or `1024×768`), click `[data-testid="rail-more"]`: browser focus-scrolls `.nav-rail`, firing `onViewportChange` and closing the menu immediately. | `ui-ng/src/app/app.component.ts`: bind `.nav-rail` `scroll` to `onRailScroll`, which repositions and clamps open floating menus while their trigger remains inside `.nav-rail` bounds and closes only if scrolled out of view. |
| B7 | `/next/sessions` did not render inline `request_access`, `localScriptApprovals`, `pendingSecret`, or `pendingConfigChange` approval cards on the session chat. | Attach `pendingAccess` and `localScriptApprovals` to Director's task `#13` and open `/next/sessions/director`: no approval cards appeared. | `ui-ng/src/app/sessions/sessions-page.component.ts` & `.css`: mount deferred `<g-task-gates-bar [task]="detail" [pane]="true" (acted)="store.refresh()" />` inside `.session-gates-wrap` when `hasInlineGates()` is true. |

## Verification and regression tests

- **Unit tests (`cd ui-ng && npm run lint && npm run format:check && npm test`):**
  all 76 test files (830 tests) pass, including new unit coverage in
  `sessions-page.component.spec.ts`, `grants-picker.component.spec.ts`, and
  `app.component.spec.ts`.
- **Playwright E2E (`ui-ng/e2e/`):**
  - `e2e/next-responsive-nav-sessions-regressions.spec.ts` (9/9 passing across
    `desktop`, `phone`, and `dark` projects)
  - `e2e/next-full-system.spec.js` (5/5 passing: desktop/phone × light/dark +
    contrast)
  - `e2e/next-session-send-queue.spec.js`, `e2e/next-responsive-sessions.spec.js`,
    `e2e/next-sessions-load-error-viewer-reply.spec.js`,
    `e2e/next-shell-sessions-walkthrough.spec.js`, `e2e/shell.spec.ts` (all passing)
- **Hygiene (`go test ./tests/hygiene`):** passing.
