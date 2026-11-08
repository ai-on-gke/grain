# Responsive navigation and live-session UI walkthrough — 2026-11-08

Task 3465. Real-browser walkthrough of the shared shell and the live-session
and interactive-chat surfaces across desktop, laptop, tablet, and narrow
phone layouts in Chromium and WebKit against a real `grain demo` server over
a throwaway embedded SQLite store.

Earlier runs of this brief on 2026-11-08 are recorded in
`2026-11-08-responsive-navigation-live-sessions.md` (task 3165, `d7dde0c2`)
and `2026-11-08-responsive-navigation-live-sessions-task-3361.md` (task 3361,
`6d15dc3a`). This report uses the `-task-3465` suffix so neither earlier
same-day report is overwritten, and tests `main` at `ad6d8b91` with fresh
Playwright walkthrough drivers.

**One fault was found and fixed in this task (§4.7):** on `/session/new`,
any follow-up messages queued or composer draft typed while
`POST /api/console-sessions` was in flight were silently dropped when the
created session opened (`NewSessionPage` unmounted and `ConsoleSessionPage`
mounted a fresh `TaskChat`, and `NewSessionPage` also read a stale `starting`
boolean closure). In addition, `startStandingSession` opened the session pane
before preloading the newly created task, briefly rendering the empty
"grain files this session's task on its next cycle" placeholder even though
the task already existed, and a failed session create with a second message
queued behind it could attempt to create the session out of order from the
second message. All parts are fixed with unit and Playwright E2E regression
coverage.

Everything else across the responsive shell, header rail, sidebar, phone
drawer, banners, accessibility paths, and live session controls held across
Chromium and WebKit.

## 1. Commit and environment

- **Commit tested**: `ad6d8b914ad6266ceeb529545b862c7977c0a5ac` (`ad6d8b91`),
  tip of `main` when the run started. Build stamp in the rail footer read
  `v0.0.0-20261108103647-ad6d8b914ad6 · ad6d8b9 · 2026-11-08 10:36Z`.
- **Machine**: Debian Linux x86_64 sandbox VM; Go `1.26.10`, Node `v20.19.0`.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`) from `/var/cache/playwright`.
- **Server**: Built frontend (`npm ci && npm run build` in `ui/` into
  `pkg/ui/static`) and `go build ./cmd/grain`, then ran
  `grain demo -addr 127.0.0.1:<port> -open=false -access-code-file <path> -staging -host-mode-install -observability -agent-paused`
  over a throwaway embedded store, plus a 15-line local reverse proxy on a
  second port to verify port-changing proxy handling on Staging links. Each
  browser context authenticated via `POST /api/auth/access-code`.
- **Gemini key**: `/home/debian/.gemini-api-key` was not present in this
  sandbox, so `LOOK=1` was not used; layout was verified through Playwright
  bounding boxes, `elementFromPoint` hit-testing, scroll dimensions, and
  before/after `screenshot.mjs` contact sheets.

**Viewports exercised**:

| Name           | Size     | Pointer / mobile context             | Engines          |
| -------------- | -------- | ------------------------------------ | ---------------- |
| desktop        | 1440×900 | fine pointer                         | Chromium, WebKit |
| laptop         | 1024×768 | fine pointer                         | Chromium         |
| iPad portrait  | 820×1180 | coarse touch (`hasTouch: true`)      | Chromium, WebKit |
| iPad landscape | 1180×820 | coarse touch (`hasTouch: true`)      | Chromium         |
| phone          | 390×844  | coarse touch (`isMobile` / `touch`)  | Chromium, WebKit |
| small phone    | 320×568  | coarse touch (`isMobile` / `touch`)  | Chromium, WebKit |
| rotated phone  | 844×390  | coarse touch (`isMobile` / `touch`)  | Chromium, WebKit |

## 2. Scope notes: intentional design differences from the brief

These reflect deliberate changes already on `main` and were verified rather
than altered:

- **Verified-identity badge moved from the header rail to the sidebar/drawer
  footers (`db63853d`, PR #726).** `PageHeader.jsx` no longer renders
  `top-bar-user` or an email menu in the top header rail. Under verified
  identity (`authMethod: "iap"`), `Sidebar.jsx` renders `user-tile` (opening
  `/settings/access`) and `sidebar-logout` at the foot of the desktop/tablet
  rail (both expanded and collapsed), and `PhoneNav.jsx` renders
  `phone-signed-in` with the email and `Log out` at the foot of the phone
  navigation drawer below `System`. Both paths were exercised (§4.3, §4.4).
- **CI is off in the rail by default (`9be44f8f`).** `Customize sidebar`
  exposes `Show CI`; turning it on adds `CI` to the rail and persists across
  reloads, opening `/ci` with `aria-current="page"` and crumb `ci`.
- **More page actions expands `page-header-actions` as a disclosure panel.**
  `page-header-more` toggles `aria-expanded="true"` rather than `role="menu"`,
  keeping the header rail itself at a single row (`77px`) and closing on
  `Escape` (restoring focus to `More page actions`) and outside presses.
- **Lone failed chat sends return directly to the composer.** When the
  composer is empty at failure time, `TaskChat` restores the text to the box
  and announces `"Message not sent. It is back in the message box."`; when
  another draft or earlier failed message already occupies the box, the
  failed message stays in the transcript marked `"Not sent"` with `Retry` and
  `Discard`. Both paths were exercised (§4.6, §4.7).
- **Expected demo 404 on `/api/repos/acme/widgets/github-branches`.** Without
  `-github-sim`, opening `/tasks/1` logs one expected 404 on branch listing;
  all other unstubbed routes returned 2xx/304 with zero console or page errors.

## 3. External and live boundaries intercepted

Navigation, React state, browser layout, and the real `grain demo` server and
SQLite store stayed in the loop throughout. Only boundaries unavailable or
instantaneous in local demo mode were intercepted via `page.route` in isolated
contexts, then unrouted:

1. **Verified identity (`authMethod: "iap"`)**: Augmented `GET /api/config`
   with `authMethod: "iap"` and a 66-character service-account email
   (`grain-daemon-runner@very-long-project-identifier.iam.gserviceaccount.com`),
   plus `POST /api/auth/logout` and `/_gcp_iap/clear_login_cookie`, to verify
   long-email truncation, `Settings → Access` navigation, and logout in
   desktop expanded/collapsed rails and the phone drawer.
2. **Simultaneous outage + failed-write snackbar**: Aborted `/api/**` with
   `connectionrefused` while `-host-mode-install` was active on the server,
   then clicked `Approve` on a task to raise the write-failure snackbar over
   both the disconnected banner (`"grain cannot be reached"`) and the
   host-mode install notice, before removing the route to confirm automatic
   recovery.
3. **Stale task-list refresh (`tasksStale`)**: Returned HTTP 500 on
   subsequent `/api/tasks` polls after initial load so `Sidebar` rendered
   `"not refreshing"` and `PhoneNav` rendered its stale-tasks bar.
4. **Session creation and comment latency / failure**: Held `POST /api/console-sessions`
   and `POST /api/tasks/*/comments` on explicit promises to inspect
   optimistic `"Sending…"` bubbles, queued back-to-back sends, composer
   drafts during session startup, and HTTP 503 failure/retry paths.
5. **Inline config-repo approval, missing shipped defaults, and live open run**:
   Augmented a session task snapshot with `pendingConfigChange` (including a
   100+-character path and unified diff), `missingShippedDefaults`, and an
   in-flight attempt (`state: "running"`, `activity: "inspecting queue"`) to
   verify inline `Approve` / `Reject`, `Adopt shipped defaults`, the live
   next-run capability notice, and `Apply now` (`POST /api/console-sessions/<id>/cancel`).

## 4. Walkthrough steps and outcomes

### 4.1 Full route and viewport sweep (744 page loads)

Visited **62 unique deep links** across **7 viewports in Chromium** (480 page
loads) and **5 viewports in WebKit** (264 page loads):

- `/inbox`, `/`, `/?state=blocked`
- `/tasks/1`, `/tasks/13`, `/new`
- `/boards` and `board-1` through `board-5` (`kanban`, `gantt` timeline,
  `report`, `tree`, and `list` visualizations)
- `/sessions`, `/session/director`, `/director`, `/session/new`,
  `/session/deleted-missing-session`
- `/processes`, `/ci`
- `/repos` and `/repos/acme/widgets` across all 7 repo tabs (`tasks`,
  `overview`, `processes`, `ci`, `releases`, `prompts`, `advanced`)
- `/settings`, `/config`, and all **21 Settings sections** across all 5
  section groups (`upgrade`, `models`, `agents`, `sandboxes`, `secrets`,
  `preferences`, `task-defaults`, `capabilities`, `capability-servers`,
  `staging`, `ci`, `merge-queue`, `deployments`, `webhooks`, `access`,
  `api-tokens`, `audit`, `config-repo`, `rate-limits`, `notifications`,
  `danger-zone`)
- `/system` and all **8 System tabs** (`health`, `workers`, `sandboxes`,
  `queues`, `metrics`, `logs`, `database`, `diagnostics`)
- Legacy team URLs (`/t/platform/inbox`, `/t/platform/tasks/1`,
  `/t/platform/settings/models`, `/t/platform/sessions`), confirming each
  redirected cleanly to its team-less path and zero team switcher, `Admin
  mode`, or `"All teams"` controls appeared anywhere in the DOM.

Across all 744 loads: `documentElement.scrollWidth <= window.innerWidth`
(zero horizontal page overflow), zero unnamed buttons, zero clipped primary
actions, and zero uncaught page errors.

### 4.2 Desktop and tablet page header rail

Checked at `1440×900`, `1180×820`, `1024×768`, and `820×1180` in Chromium and
WebKit:

- Single-row header rail (`height = 77px`) displaying section glyph + crumbs
  (`inbox`, `tasks`, `tasks · task 1`, `boards`, `sessions`, `processes`,
  `ci`, `repositories / acme/widgets / ci`, `settings / models`,
  `system / metrics`).
- Page counts sat level with the heading (`|countCenterY - titleCenterY| <= 1.5px`),
  search and filters sat to the left, page actions (`New`, `Add repo`,
  `New board`) to the right, and global `Notifications` bell, `System`, and
  `Settings` buttons sat unclipped at the right end (`elementFromPoint` hit
  each button directly).
- At `1024×768` and `820×1180` on `/` and `/repos`, `More page actions`
  (`page-header-more`) appeared without growing a second header row (`77px`
  before and after clicking), expanded the wrapped actions cleanly, closed on
  `Escape` restoring focus to `More page actions`, closed on outside click,
  and auto-expanded when `Tab` moved focus into a wrapped control.

### 4.3 Sidebar rail, shortcuts, customization, Staging, and build stamp

- **Collapse / Expand and `[` shortcut**: Clicking `Collapse sidebar` folded
  the rail (`240px` → `56px`), persisted `grain.sidebarCollapsed = "1"` across
  hard reload, exposed accessible names on every collapsed icon button and
  `Create options`, and unfolded on `[`. Typing `alpha[beta]` in task search
  and `array[0]` in the chat's `contenteditable` composer left the rail width
  unchanged (`[` ignored in editable fields).
- **Create menu**: Opened `New task`, `New session`, and `New board` from
  both expanded and collapsed rails.
- **Customize sidebar**: Enabled `Show CI` and pinned `System → Metrics`;
  confirmed both persisted across reload and lit `aria-current="page"` when
  visiting `/ci` and `/system/metrics`.
- **Counts and Staging**: Confirmed `Tasks` active state views and `Blocked`
  count matched seeded rows, `Processes` and `Repos` counts matched, Staging
  entries linked to `/staging/docs-preview/` and `http://127.0.0.1:4280/`,
  pinned/unpinned into the top pinned section, and when loaded through the
  port-changing reverse proxy, host-port Staging entries displayed the
  `"not reachable through this proxy"` warning indicator while sub-path
  `/staging/` entries stayed normal.
- **Build stamp & verified identity**: Scrolled the rail footer, confirmed
  the build stamp was unclipped (`bottom <= viewportHeight`) and linked to
  `/settings/upgrade` (with browser Back/Forward returning cleanly). Under
  stubbed IAP identity, confirmed the 66-char email truncated cleanly inside
  `user-tile`, opened `/settings/access` on click, and `Log out` navigated to
  `/_gcp_iap/clear_login_cookie`.

### 4.4 Phone bar, modal drawer, touch targets, tab strips, and rotation

Checked at `390×844`, `320×568`, and rotated `844×390` in Chromium and WebKit:

- **Top bar and heading**: `Open navigation`, grain mark, alerts bell,
  `Settings`, and `New task` sat in one top bar (`x = 8, 52, 250, 294, 338` at
  `390×844`) with the page `h2` heading rendered below the bar.
- **Alerts popover**: Opened `Notifications` at `390×844` and `320×568`;
  popover stayed clear of both screen edges (`left >= 8px`,
  `right <= viewportWidth - 8px`).
- **Navigation drawer**: Modal dialog (`aria-modal="true"`) locked background
  scroll (`document.body` `overflow: hidden`), stayed open across `Create
  options` + `Escape`, staging pin/unpin taps, and `Customize sidebar`, and
  closed on destination tap or `Escape` (restoring focus to
  `Open navigation`). `System`, `phone-signed-in` (under IAP), and the build
  stamp sat unclipped at its foot.
- **Tab-strip scrolling & orientation**: Horizontally scrolled
  `/repos/acme/widgets` tabs to tap `Advanced` without page overflow; rotated
  `/session/director` `390×844` → `844×390` → `390×844` with composer and
  `Send` reachable and zero overflow.

### 4.5 Banners, snackbar stacking, discard confirmation, and themes

- **Disconnected + host-mode-install + error snackbar**: Triggered network
  outage on the `-host-mode-install` demo and clicked `Approve` on an
  awaiting-approval task. Verified all three rendered simultaneously at
  `1440×900` and `390×844` without horizontal overflow, with the `MuiSnackbar`
  error toast positioned above and uncovered (`elementFromPoint` hit the
  snackbar directly), and verified the disconnected banner cleared
  automatically once connectivity returned.
- **Unsaved-edit discard focus restoration**: Edited a schedule's `Title` in
  `Processes`, declined `window.confirm` on `Escape` and on browser `Back`,
  and confirmed the overlay stayed open at its URL with focus restored to
  `Title`.
- **Reduced motion & light/dark themes**: Exercised navigation, drawers,
  overlays, and chat rendering under `reducedMotion: "reduce"` in both light
  and dark themes across Chromium and WebKit.

### 4.6 Standing sessions and interactive task chat

Exercised across `1440×900`, `820×1180`, `390×844`, and `320×568` in Chromium
and WebKit:

- **New session**: Opened `/session/new` from `Create options` and from
  `/sessions`; verified `Message` composer auto-focus, `Session name`,
  name-only `Create session`, `Pin to sidebar`, and `ModelPicker`
  (`gemini-2.5-flash`). Sent markdown containing headings, code fences, a
  6-column wide table, and `[Task #1](/tasks/1)`; verified horizontal table
  containment inside the bubble (zero page overflow), persisted title, model,
  and rail pin with `aria-current="page"`.
- **Deep links & deleted sessions**: Verified `/director` opened
  `/session/director`, and `/session/<deleted-id>` rendered `SessionsPage`
  with the missing-session notice rather than opening another session's chat.
- **Work tree & live controls**: Expanded `Director`'s provenance `WorkTree`
  (`0 of 4 done`, 7 task links); toggled capabilities, `networkAccess`, and
  `keepRunning` while idle (`PUT 200` persisted across reload) and while a run
  was open (`"A run is open on this session now"` notice + `Apply now`
  calling `POST /api/console-sessions/director/cancel`); exercised `Adopt
  shipped defaults`.
- **Inline config-repo change approval**: Verified 100+-character file path
  wrapped inside `.task-chat-approval`, unified diff scrolled internally
  without page overflow, and `Reject` reason submission posted cleanly.
- **Open-session send queue & failure recovery**: On an existing session,
  confirmed two back-to-back sends queued in order (`2` `"Sending…"` bubbles),
  a lone failed 503 send returned to the composer, and a failed send with a
  newer draft in the composer kept the draft and rendered `"Not sent"` with
  working `Retry` and `Discard`.
- **Fresh chat, Delete, and task-bound Pause/Resume**: Exercised `Start a
  fresh chat` both without and with summary handover (confirming `taskId`
  rotated), `Delete session` confirmation cancel + accept (from both the
  session pane and `/sessions`), and `Pause` / `Resume` on a task-bound
  interactive chat.

### 4.7 Bug found and fixed: queued sends and drafts lost when `/session/new` opens the created session

- **Deterministic reproduction**:
  1. Open `/session/new` with `POST /api/console-sessions` held so session
     creation takes perceptible time.
  2. Type `"First message on new session"` and press `Enter` / `Send`.
     `TaskChat` clears the composer, shows `"Sending…"`, and leaves the
     composer focused and enabled.
  3. Type `"Second queued message while starting"` and press `Enter` / `Send`,
     then type `"Third unsent draft"` into the composer without pressing
     `Send`. `TaskChat` renders two `"Sending…"` bubbles and shows `"Third
     unsent draft"` in the composer.
  4. Allow `POST /api/console-sessions` to complete.
- **Observed before fix**:
  - `App.jsx`'s `startStandingSession` navigated immediately to
    `/session/<created>`, unmounting `NewSessionPage` and mounting
    `ConsoleSessionPage` with a brand-new `TaskChat`.
  - `"Second queued message while starting"` vanished completely:
    `POST /api/tasks/<taskId>/comments` was never called, only the first
    message existed after reload, and `NewSessionPage`'s `start` also closed
    over stale `starting === true` state.
  - `"Third unsent draft"` vanished because the new `TaskChat` mounted with an
    empty composer.
  - `ConsoleSessionPage` briefly rendered with `task === null`
    (`"grain files this session's task on its next cycle"`) until the follow-up
    task poll completed, even though `POST /api/console-sessions` had already
    created `taskId`.
  - If `POST /api/console-sessions` failed while a second message was queued
    behind the first, `sendChain` advanced to the second message and could
    invoke `onSend` out of order while the first message sat unsent in the
    composer.
- **Fix**:
  - `ui/src/components/TaskChat.jsx`: Added `handoffRef` (`take` and `hold`)
    to hand off unstarted queued sends, composer `draft` (`html` + `text`),
    and picked `attachments` when `NewSessionPage` transitions to the created
    session; extended `draftRequest` handling (guarded once per request object
    for StrictMode safety) to restore `draft`/`attachments` and post handed-off
    `queued` messages in order; and guarded `onSend` chains with a failure
    epoch so a message queued behind a failed session create stays marked
    `"Not sent"` with `Retry` rather than starting a session out of order.
  - `ui/src/components/NewSessionPage.jsx`: Replaced the stale `starting`
    state check in `start` with `startingRef`, held chat sends started while a
    name-only `Create session` request is in flight so they hand off too, and
    passed `() => handoffRef.current?.take?.()` to `onStart`.
  - `ui/src/App.jsx`: Updated `startStandingSession` to preload
    `/api/tasks/<createdRow.taskId>` into `sessionTask` before switching views
    (eliminating the `task === null` flash), capture `takeHandoff()`, and pass
    any carried draft, attachments, and queued sends through `sessionPrompt`;
    updated `sessionAct` to pass the explicit `taskId` to `loadSessionTask`.
- **Verified after fix**:
  - At desktop (`1440×900`) and phone (`390×844`), both `"Sending…"` bubbles
    and the composer stay strictly within the viewport while held; when
    `POST /api/console-sessions` completes, `/session/<slug>` opens directly
    into the conversation with `"Third unsent draft"` preserved in the
    composer and both sent messages persisted in order across reloads.
  - Before and after contact sheets (`ui/e2e/screenshot.mjs` across
    `/session/new` and `/session/director` at `desktop`, `tablet`, and `phone`
    in `light` and `dark`) confirmed zero layout drift, clipping, or overflow.

## 5. Harness notes (script issues, not product bugs)

- **Dual `page-header-title` test IDs when task overlay is open**: Both
  `page-header` and `pane-header` render a `page-header-title`; locators were
  scoped to `getByTestId("page-header")` vs `getByTestId("pane-header")`.
- **WebKit SPA `pushState` navigation waits**: Raw Playwright `waitForURL`
  defaults to waiting for `load`, which does not fire on History API
  `pushState`; polling `location.pathname` verified instant client-side
  transitions cleanly in WebKit.
- **Conditional ETag `304` responses in `page.route` augmentations**: Stripped
  `if-none-match` when mutating `/api/tasks/<id>` JSON in route stubs so
  WebKit and Chromium received `200` bodies rather than `304`.

## 6. Automated regression tests run

- `npx vitest run` in `ui/` (including 3 new unit tests in
  `src/components/NewSessionPage.test.jsx` and 1 new unit test in
  `src/components/TaskChat.test.jsx`).
- `npx playwright test e2e/new-session-first-message-sending.spec.js e2e/interactive-session-chat.spec.js e2e/phone.spec.js`
  in `ui/` (including new desktop `1440×900` and phone `390×844` E2E specs
  with bounding-box assertions).
- `go test ./tests/hygiene/... ./pkg/ui/...` in repository root.
