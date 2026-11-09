# Responsive Navigation & Live-Session UI Walkthrough — 2026-11-09

Task 3721. Real-browser walkthrough of the shared shell and the live-session
and interactive-chat surfaces across desktop, laptop/narrow-tablet, tablet and
narrow phone widths in Playwright **Chromium** and **WebKit** against a built
`grain demo` server over throwaway embedded stores.

Earlier runs in this series live at
`docs/test-runs/2026-11-08-responsive-navigation-live-sessions.md` (Task 3165),
`docs/test-runs/2026-11-08-responsive-navigation-live-sessions-task-3361.md`
(Task 3361),
`docs/test-runs/2026-11-08-responsive-navigation-live-sessions-task-3465.md`
(Task 3465), and
`docs/test-runs/2026-11-08-responsive-navigation-live-sessions-task-3618.md`
(Task 3618). This run exercises `main` at `8ea343b0` from scratch with
independent Playwright drivers across both browser engines, including the new
real `grain demo -config-change` and `-host-mode-install` seeds.

**One fault was found and fixed in this task (§4.7):** when
`GET /api/console-sessions` failed before sessions had loaded (for example `503`
`{ "error": "console session manager is still starting" }` during server
startup or `500` from a store fault), `refreshSessions` in `ui/src/App.jsx`
threw without recording a load error, and once the 5-second `ErrorBanner`
snackbar faded, `SessionsPage` on both `[/sessions](/sessions)` and
`[/session/director](/session/director)` rendered `"No interactive sessions yet."`
with a `+ New session` call to action (and a `0` count badge in `ListHeader`)
instead of an error state with **Retry** (unlike `BoardsPage` and `TaskList`).

**Earlier walkthrough fixes remain green:** Task 3165's standing-session grants
save, Task 3361's optimistic `Sending…` bubble on `[/session/new](/session/new)`,
and Task 3618's removal of orphaned sidebar pins on session deletion all passed
in Chromium and WebKit across every tested width.

---

## 1. Commit and environment

- **Commit tested**: `8ea343b02137dc30d171b32d701bee3fdaed5e52` (`main` when
  the run began). Build stamp in the UI footer:
  `v3.2.0-dev.2646+8ea343b · 8ea343b · 2026-11-09 04:38Z`. After applying the
  fix in §4.7, `ui/` and `bin/grain` were rebuilt and re-verified end to end.
- **Sandbox**: Debian Linux `x86_64` VM; Go 1.26.10, Node.js v20.19.0.
- **Browsers**: Playwright **Chromium** and **WebKit**
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`).
- **Server**: Built frontend (`cd ui && npm ci && npm run build` into
  `pkg/ui/static`) embedded in `./bin/grain` (`make grain`), started via
  `grain demo` over fresh throwaway embedded SQLite stores (`TMPDIR` isolated
  per server start) with `-open=false -access-code-file <tmp>` plus scenario
  flags (`-observability -staging -config-change -host-mode-install
-agent-paused -merge-rules -sensitive-hold -mechanical-port`), authenticated
  per browser context via the Access code flow (`POST /api/auth/access-code`).
- **Gemini key**: `/home/debian/.gemini-api-key` was absent in this sandbox, so
  `LOOK=1` visual inspection was unavailable; layout, hit-testing, clipping,
  contrast (`@axe-core/playwright`), sticky headers/actions and scroll
  containment were verified via live DOM geometry (`getBoundingClientRect`,
  `elementFromPoint`, `scrollWidth`, `scrollTop`, `window.scrollY`) in both
  engines alongside rendered before/after `contact-sheet.png` captures kept
  outside the checkout.

### Viewports exercised

| Viewport                 | Size     | Pointer / context                             |
| ------------------------ | -------- | --------------------------------------------- |
| Desktop                  | 1440×900 | Fine pointer + keyboard                       |
| Laptop                   | 1024×768 | Fine pointer + keyboard                       |
| Narrow tablet (overflow) | 720×900  | Coarse pointer (`hasTouch: true`)             |
| Tablet portrait          | 820×1180 | Coarse pointer (`hasTouch: true`)             |
| Tablet landscape         | 1180×820 | Coarse pointer (`hasTouch: true`)             |
| Phone                    | 390×844  | Coarse pointer (`hasTouch: true`, `isMobile`) |
| Narrow phone             | 320×568  | Coarse pointer (`hasTouch: true`, `isMobile`) |
| Rotated phone            | 844×390  | Coarse pointer (`hasTouch: true`, `isMobile`) |

---

## 2. Scope notes where `main` intentionally differs from older wording

1. **Settings sections and responsive navigation**: `Settings` has **20
   sections** across 5 groups (`Models & sandboxes`: `Models`, `Sandboxes`;
   `Secrets & access`: `Access`, `Hosts`, `CLI`, `Audit`; `This browser`:
   `Preferences`; `Operations`: `Upgrade`, `Caches`, `Engine`, `Hub`,
   `Config repo`; `Managed in config repo`: `General`, `Git`, `Repositories`,
   `Tasks`, `Agents`, `Capabilities`, `CI`, `Memory`). At desktop widths it
   renders `settings-nav`, at tablet widths (`820×1180`) it switches to a
   `SearchSelect` picker (`data-testid="settings-section-select"`), and on
   phones (`<600 px` width or landscape `<=480 px` height) it uses a
   master-detail section list with `All settings` (`data-testid="settings-back"`).
   Navigating to `[/settings/legacy-capabilities](/settings/legacy-capabilities)`
   redirects cleanly to `[/settings/capabilities](/settings/capabilities)`.
2. **System tabs on phone**: `System` has **8 tabs** (`Health`, `Logs`,
   `Sandboxes`, `Caches`, `Top`, `Pipeline`, `Metrics`, `Power`), rendered as a
   tab strip on desktop and tablet and as a master-detail `System tabs` list
   with an `All of System` back button on phone.
3. **Verified-identity badge placement (`db63853d`, PR #726)**: Behind verified
   IAP identity, the signed-in email badge (`alice@example.com`), `Access`
   settings link (`[/settings/access](/settings/access)`), and `Log out`
   (`sidebar-logout`) live in the desktop/tablet sidebar footer (both expanded
   `232 px` and collapsed `60 px`) and at the foot of the phone navigation
   drawer rather than at the right end of `PageHeader`.
4. **`CI` rail visibility**: `CI` is off by default in **Customize sidebar**
   (`Show CI`) and appears in the rail and phone drawer once toggled on,
   persisting in `localStorage` across reloads.

---

## 3. Real demo capabilities vs external boundary stubs

Navigation, routing, React rendering, MUI drawers/menus/dialogs, browser layout,
scroll containers and the built `grain demo` server stayed real throughout. In
particular, `grain demo -config-change` (two real `propose_config_change` tasks
backed by a throwaway `grain/config` repository) and `grain demo -host-mode-install`
(real `InstallNoticeBanner`) ran against the unstubbed Go server. Only external
infrastructure or timing boundaries that a demo store without live agent VMs
cannot produce on cue were intercepted:

| Boundary                                                                       | How it was driven                                                                                                                                                    | What stayed real                                                                                                                     |
| ------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------ |
| Port-changing reverse proxy for Staging reachability warning                   | Local Node HTTP reverse proxy on loopback forwarding a proxy port to the `grain demo -staging` port while rewriting `Host`                                           | `grain demo -staging` server, `/api/staging`, `Sidebar` and `PhoneNav` proxy-reachability warning                                    |
| Verified IAP identity (`X-Goog-IAP-JWT-Assertion`)                             | Ephemeral ES256 keypair + local JWKS endpoint passed to `grain demo -ui-iap-audience ... -iap-public-key-url ...`, injecting signed JWTs and handling logout (`204`) | Go `pkg/ui` IAP JWT verification middleware, `/api/config` `authEmail`/`logoutURL`, `Sidebar` and `PhoneNav` identity & logout UI    |
| Simultaneous `DisconnectedBanner` + `InstallNoticeBanner` + `ErrorBanner`      | Real `-host-mode-install` server; triggered one `500` mutation for `ErrorBanner`, then aborted `/api/**` polls so `DisconnectedBanner` stacked above                 | Real banner components, sticky stacking order, hit-testing and `320 px`–`1440 px` layout                                             |
| Mid-session task-list error on phone (`phone-tasks-stale`) and `/sessions` 503 | Returned `503` JSON (`{ "error": "..." }`) on `/api/tasks` or `/api/console-sessions` before restoring `200` on **Retry**                                            | `App.jsx` error state, `phone-tasks-stale` bar, `SessionsPage` error alert and **Retry** recovery                                    |
| Optimistic `Sending…`, back-to-back queued sends, failed-send retry            | Held `POST /api/console-sessions` and `POST /api/tasks/*/comments` behind explicit latches or returned `500` once before releasing to the real server                | `NewSessionPage` and `TaskChat` optimistic bubbles, queue serialization, `Not sent` / `Retry` recovery and real SQLite persistence   |
| Standing-session in-flight run notice (`Apply now`) & `crowdedNote`            | Augmented `GET /api/console-sessions` / `GET /api/tasks/<id>` with `status: "running"` or `crowdedNote` (stripping `If-None-Match` so polls returned `200`)          | `ConsoleSessionPage` `.console-session-pending` stale-run banner, `Apply now` (`POST /api/console-sessions/<id>/cancel`) and notices |

---

## 4. Walkthrough steps and outcomes

### 4.1 Shared navigation across desktop, tablet and phone (Chromium & WebKit)

- **460 real-control screen visits** (**230 per browser engine** across
  `1440×900`, `820×1180`, `390×844` and `320×568`, plus targeted passes at
  `1024×768`, `720×900`, `1180×820` and `844×390`) visited every primary
  destination with **0 horizontal page overflows**
  (`documentElement.scrollWidth <= window.innerWidth`), **0 uncaught page or
  console errors** (filtering WebKit's documented cancelled-fetch navigation
  noise in `ui/e2e/pageerrors.js`) and **0 unexpected 4xx/5xx responses**:
  - `[/inbox](/inbox)` (`Inbox`), `[/](/)` (`Tasks` + active state views
    `Running`, `Queued`, `Blocked`, `Merged` and `Blocked` count),
    `[/tasks/1](/tasks/1)` (detail overlay), and `[/new](/new)` (new-task
    overlay/dialog).
  - `[/boards](/boards)` and all **6** board visualizations (`list`, `kanban`,
    `gantt` / Timeline, `panels`, `report`, `tree`).
  - `[/sessions](/sessions)` and `[/session/director](/session/director)`.
  - `[/repos](/repos)` and all **7** tabs of
    `[/repos/acme/widgets](/repos/acme/widgets)` (`Overview`, `CI`,
    `Merge queue`, `Agents`, `Branches`, `Releases`, `Advanced`), confirming
    horizontal tab-strip scrollability at narrow widths without page overflow.
  - `[/processes](/processes)` and `[/ci](/ci)` (enabled via **Customize
    sidebar** -> `Show CI` and verified persisted across reload).
  - All **20 Settings sections** across all 5 groups (`models`, `sandboxes`,
    `access`, `hosts`, `cli`, `audit`, `preferences`, `upgrade`, `caches`,
    `engine`, `registry` / `Hub`, `config-repo`, `general`, `git`,
    `repositories`, `tasks`, `agents`, `capabilities`, `ci`, `memory`) via
    desktop `settings-nav`, tablet `settings-section-select`, and phone
    `All settings` master-detail, plus legacy redirect
    `[/settings/legacy-capabilities](/settings/legacy-capabilities)` ->
    `[/settings/capabilities](/settings/capabilities)`.
  - All **8 System tabs** (`[/system/health](/system/health)`,
    `[/system/logs](/system/logs)`, `[/system/sandboxes](/system/sandboxes)`,
    `[/system/caches](/system/caches)`, `[/system/top](/system/top)`,
    `[/system/pipeline](/system/pipeline)`,
    `[/system/metrics](/system/metrics)`, `[/system/power](/system/power)`) via
    the desktop/tablet tab strip and phone `System tabs` list (`All of System`).

### 4.2 Desktop/tablet `PageHeader` rail, overflow menu and legacy team URLs

- Verified single-row `PageHeader` across `1440×900`, `1024×768`, `820×1180`,
  `1180×820` and `720×900` (`header` height `77 px`, inner slot `44 px` — never
  wrapping onto a second row): section glyph + crumbs (`tasks`,
  `repositories / acme/widgets / <tab>`, `settings / <section>`,
  `system / <tab>`, `task 1`), counts aligned level with the title
  (`|ΔcenterY| <= 2 px`), search and filters on the left, page actions (`New`,
  `Add repo`, `New board`) on the right, and global `Alerts`, `System` and
  `Settings` buttons unclipped at the far right.
- At `720×900` where page controls exceeded available width, `More page actions`
  (`data-testid="page-header-more"`) opened the overflow menu inside the
  viewport; pressing `Escape` closed the menu and restored focus directly to
  `page-header-more`, and clicking outside dismissed it cleanly while keeping
  the header a single row.
- Confirmed zero team chrome (`Team`, team switcher, `Admin mode`, `All teams`)
  anywhere in the DOM, and verified legacy `/t/<team>/...` deep links:
  - `/t/acme-team/repos` -> `[/repos](/repos)`
  - `/t/platform/settings/models` -> `[/settings/models](/settings/models)`
  - `/t/core/inbox` -> `[/inbox](/inbox)`

### 4.3 Sidebar rail, `[` shortcut, Create menu, Staging, Customize & identity

- **Collapse / Expand & `[` shortcut**: Clicking `Collapse sidebar` narrowed the
  rail from `232 px` to `60 px`, persisted in
  `localStorage["grain.sidebarCollapsed"]` across `page.reload()`, toggled back
  via `[` when focus sat outside text inputs, and was ignored while typing `[`
  into search inputs and chat composers.
- **Create menu**: Exercised `Create options` (`New task`, `New session`,
  `New board`) from both the desktop rail and the phone navigation drawer.
- **Staging environments**: Verified `/staging/` sub-path entries alongside
  host-port entries, pinned/unpinned a staging item into `Pinned` (persisting
  across reload), and verified through the port-changing reverse proxy that
  host-port entries display the unreachable-via-proxy indicator while
  `/staging/` sub-path entries remain clean.
- **Build stamp**: Verified the footer build stamp was unclipped and linked to
  `[/settings/upgrade](/settings/upgrade)` on both desktop and the phone drawer
  footer.
- **Verified IAP identity**: Confirmed `alice@example.com`, `Access` settings
  link (`[/settings/access](/settings/access)`), and `Log out` (`sidebar-logout`
  -> `/_gcp_iap/clear_login_cookie`) across expanded rail (`232 px`), collapsed
  rail (`60 px`), and phone navigation drawer.

### 4.4 Phone top bar, modal drawer, alerts popover and stacked banners

- **Phone bar & modal drawer (`390×844`, `320×568`, `844×390`)**: Verified the
  top bar (`Open navigation`, grain mark, `Alerts`, `Settings`, `New task`) with
  the page title heading below the bar. Confirmed the navigation drawer is a
  modal dialog that stays open when interacting with `Create options`, staging
  pin buttons, and **Customize sidebar**, closes automatically on navigation,
  and carries the `System` row and build stamp at its foot without background
  scroll bleed.
- **Alerts panel bounds**: Opened `Alerts` at `390×844` and `320×568` and
  verified the popover stayed strictly inside viewport edges (`left >= 8 px`,
  `right <= innerWidth - 8 px`) with zero clipping.
- **Stacked banners & snackbar (`1440×900` and `320×568`)**: Verified
  `DisconnectedBanner` and `InstallNoticeBanner` (`-host-mode-install`) stacked
  cleanly without overlapping content or each other, while `ErrorBanner` floated
  legibly above the bottom edge with `0 px` horizontal overflow.
- **Phone stale-tasks retry bar**: Confirmed `phone-tasks-stale` appeared on a
  mid-session `/api/tasks` `503` and cleared upon tapping **Retry**.

### 4.5 Keyboard, focus restoration, discard confirmations, themes & reduced motion

- **Keyboard-only & coarse-pointer paths**: Exercised keyboard traversal (`Tab`,
  `Shift+Tab`, `Enter`, `Space`, `Escape`) and coarse-pointer taps across
  Chromium and WebKit.
- **Declined discard focus restoration**: Made unsaved edits in **Customize
  sidebar**, pressed `Escape`, declined the discard confirmation prompt, and
  verified the dialog stayed open with focus restored inside the modal rather
  than dropped to `document.body`.
- **Browser Back/Forward, orientation changes & themes**: Verified clean history
  stack behaviour across task overlays and section routes, portrait <->
  landscape viewport transitions (`820×1180` <-> `1180×820`, `390×844` <->
  `844×390`), dark and light rendering, and `prefers-reduced-motion: reduce`.

### 4.6 Live sessions (`[/sessions](/sessions)`, `[/session/new](/session/new)`, `[/director](/director)`) and interactive task chat

All 13 live-session and interactive-chat suites passed in both **Chromium** and
**WebKit**:

1. **`New session` (`[/session/new](/session/new)`)**: Opened from the rail
   Create menu, phone drawer Create menu (drawer stayed open for `Create
options` and closed on `New session`), and `[/sessions](/sessions)`
   `+ New session`. Verified empty chat with auto-focused composer, `Session
name`, `ModelPicker`, and `Pin to sidebar`. Held `POST /api/console-sessions`
   to confirm the immediate optimistic `Sending…` bubble, then released and
   verified the session was named, pinned in the rail with `aria-current="page"`,
   and persisted across reload; verified a second session created with `Pin to
sidebar` unchecked auto-named cleanly without pinning.
2. **Direct & deleted session deep links**: Verified `[/director](/director)`
   replaced history with `[/session/director](/session/director)` (preserving
   browser `Back` / `Forward`), and navigating to
   `[/session/deleted-ghost-session-99](/session/deleted-ghost-session-99)`
   listed existing sessions on `[/sessions](/sessions)` with a not-found warning
   banner rather than opening another session's chat.
3. **Queued & failed sends**: Verified back-to-back messages sent while the
   first `POST /api/tasks/<id>/comments` was in flight queued and delivered in
   strict order; verified a failed `500` send while a new draft was in the
   composer preserved the draft and showed `Not sent` with a working **Retry**
   action.
4. **Markdown, wide tables & nested scroll**: Rendered headings, code blocks and
   a 140-character unbroken Markdown table cell across `1440×900`, `820×1180`,
   `390×844` and `320×568` (`0 px` document horizontal overflow; scrolling the
   chat transcript kept `window.scrollY === 0`).
5. **Session `WorkTree` & overlay draft preservation**: Opened a child task
   overlay from the session's `WorkTree` while holding an unsent composer draft,
   dismissed with `Escape`, and verified the draft remained intact.
6. **Real inline config-repo approval cards (`grain demo -config-change`)**:
   Verified `PendingConfigChange` rendered inline in chat with `0 px` overflow
   at `320×568`, exercised **Reject** with a reason on the first seeded proposal
   and **Approve & Apply** on the second seeded proposal against the real
   throwaway `grain/config` repository, and confirmed both outcomes persisted
   across reload.
7. **Standing-session live controls & in-flight run notice**: Edited standing
   session `Network access`, `Chat space`, `Keep running` and model settings;
   verified the next-message notice when idle and the stale-run notice +
   **Apply now** (`POST /api/console-sessions/<id>/cancel`) when a run is in
   flight.
8. **`Start a fresh chat` (with and without summary handover)**: Exercised both
   **Start fresh with summary** (`carryHandover: true` when prior messages
   exist) and **Close this chat and start a fresh one** (`carryHandover: false`).
9. **Session switching, `Delete session` & task-bound `Pause` / `Resume`**:
   Verified per-session composer draft isolation when switching sessions,
   exercised **Delete session** confirmation (declining first, then accepting
   from both the session pane and `[/sessions](/sessions)` and confirming
   sidebar pin cleanup), and verified **Pause** and **Resume** persistence on a
   task-bound interactive chat.

### 4.7 Bug found and fixed: `[/sessions](/sessions)` and `[/session/director](/session/director)` showed `"No interactive sessions yet."` when `GET /api/console-sessions` failed

- **Deterministic reproduction**:
  1. Start `grain` while the console session manager is unavailable or make
     `GET /api/console-sessions` return `503`
     `{ "error": "console session manager is still starting" }` (or `500`
     `{ "error": "database is locked" }`).
  2. Navigate to `[/sessions](/sessions)` or
     `[/session/director](/session/director)`.
  3. Wait 5 seconds for the transient `ErrorBanner` snackbar to auto-dismiss.
  4. **Before fix**: `SessionsPage` displayed a `0` count badge in `ListHeader`
     and rendered `"No interactive sessions yet."` with a `+ New session` button
     and no **Retry** button, misreporting a failed fetch as an empty store and
     leaving the operator stuck until a hard reload.
- **Root cause & fix**:
  - Unlike `refreshBoards` (which records `boardsError` and passes `loadError` /
    `onRetry` to `BoardsPage`) and `refreshTasks` (which records `tasksError`
    for `TaskList`), `refreshSessions` in `ui/src/App.jsx` cleared no error
    state and stored none on failure, leaving `sessions` at `[]` and
    `sessionsLoaded` `false` while `SessionsPage.jsx` unconditionally rendered
    the empty state whenever `sessions.length === 0`.
  - Added `sessionsError` state in `ui/src/App.jsx`, updated `refreshSessions`
    and `applySessions` to record/clear `sessionsError` symmetrically with
    `boardsError`, and wired `loadError={sessionsError}` and
    `onRetry={() => refreshSessions().catch(showError)}` into `<SessionsPage>`
    on both `[/sessions](/sessions)` and unknown-session fallback routes (such
    as `[/session/director](/session/director)` before sessions load).
  - Updated `ui/src/components/SessionsPage.jsx` so that when
    `loadError !== null && sessions.length === 0`, the `ListHeader` omits the
    misleading `0` count and the body renders
    `<div className="empty tasks-load-error" role="alert">` with
    `"The sessions could not be loaded (<reason>)."` and a **Retry** button
    (while keeping already-loaded session rows visible if a later background
    refresh fails).
- **Regression coverage added**:
  - Unit tests: `ui/src/components/SessionsPage.test.jsx` (`loadError` suite:
    initial failed load vs already-loaded sessions) and
    `ui/src/App.resilience.test.jsx` (`shows a retryable load error on /sessions
instead of 'No interactive sessions yet' when /api/console-sessions fails
with 503`, advancing fake timers past the 5-second snackbar timeout and
    verifying recovery on **Retry**).
  - End-to-end Playwright test: `ui/e2e/sessions-load-failed.spec.js` (verifying
    `[/sessions](/sessions)` and `[/session/director](/session/director)` under
    `503`, snackbar expiry, `@axe-core/playwright` color contrast in light and
    dark themes, bounding-box layout and `0 px` overflow at `1440×900` and
    `390×844`, and **Retry** recovery into `Director`).
  - Re-ran the full live-session walkthrough against the rebuilt `./bin/grain`
    in both **Chromium** and **WebKit**: both engines verified the retryable
    alert state and clean recovery.
