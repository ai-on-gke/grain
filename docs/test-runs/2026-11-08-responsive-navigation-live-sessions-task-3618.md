# Responsive navigation and live-session UI walkthrough

Task 3618. Real-browser walkthrough of the shared shell and the live-session
and interactive-chat surfaces across desktop, laptop, tablet and narrow phone
widths in Playwright **Chromium** and **WebKit** against a built `grain demo`
server over a throwaway embedded store.

Earlier runs of this brief today live at
`docs/test-runs/2026-11-08-responsive-navigation-live-sessions.md` (Task 3165,
`d7dde0c2`) and
`docs/test-runs/2026-11-08-responsive-navigation-live-sessions-task-3361.md`
(Task 3361, `6d15dc3a`). This report uses the `-task-3618` suffix so both
earlier reports stay intact, and exercises `main` at `33a44581` (~170 commits
past `6d15dc3a`) from scratch with independent scripts.

**One fault was found and fixed in this task (§4.7):** creating a standing
session with **Pin to sidebar** checked persisted
`{ kind: "session", id: "<slug>" }` in `localStorage["grain.pinned"]`, and
deleting that session (from either the session pane or `/sessions`) never
removed its pin. Opening **Customize sidebar** continued to list and count a
dead row showing the deleted slug ID, and creating a new session later with the
same title and **Pin to sidebar** unchecked reused the slug on the server and
silently inherited the deleted session's sidebar pin.

**Earlier walkthrough fixes remain green:** Task 3165's §4.9 grants-save fault
and Task 3361's §4.6 optimistic `Sending…` bubble on `/session/new` both
passed in Chromium and WebKit across all widths.

---

## 1. Commit and environment

- **Commit tested**: `33a445814ac8a8371f6213fac9bd6073d08b13cd` (`main` when
  the run began). Build stamp in the UI footer:
  `v3.2.0-dev.2463+33a4458 · 33a4458 · 2026-11-08 12:23Z`. After applying the
  fix in §4.7, `ui/` and `bin/grain` were rebuilt and re-verified end to end.
- **Sandbox**: Debian Linux x86_64 VM; Go 1.26.x, Node.js v20.19.0.
- **Browsers**: Playwright **Chromium** and **WebKit** from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: Built frontend (`cd ui && npm run build` into `pkg/ui/static`)
  embedded in `./bin/grain`, started via `grain demo` over throwaway embedded
  stores (`-open=false -access-code-file <tmp> -staging -merge-rules
-sensitive-hold -mechanical-port`), authenticated per browser context via
  `POST /api/auth/access-code`.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent in this sandbox, so
  `LOOK=1` visual inspection was unavailable; layout, hit-testing, clipping,
  sticky headers/actions and scroll containment were asserted via live DOM
  geometry (`getBoundingClientRect`, `elementFromPoint`, `scrollWidth`,
  `scrollTop`) in both engines alongside before/after contact-sheet captures.

### Viewports exercised

| Viewport         | Size     | Pointer / context                             |
| ---------------- | -------- | --------------------------------------------- |
| Desktop          | 1440×900 | Fine pointer + keyboard                       |
| Laptop           | 1024×768 | Fine pointer + keyboard                       |
| Tablet portrait  | 820×1180 | Coarse pointer (`hasTouch: true`)             |
| Tablet landscape | 1180×820 | Coarse pointer (`hasTouch: true`)             |
| Phone            | 390×844  | Coarse pointer (`hasTouch: true`, `isMobile`) |
| Narrow phone     | 320×568  | Coarse pointer (`hasTouch: true`, `isMobile`) |
| Rotated phone    | 844×390  | Coarse pointer (`hasTouch: true`, `isMobile`) |

---

## 2. Scope notes where `main` intentionally differs from older wording

1. **Settings sections (`f4e9db32`)**: `Settings` has **20 sections** across
   5 groups (`Workspace`, `Agents & Access`, `Automation`, `System`, `Admin`).
   `Legacy capabilities` was retired onto `[/settings/capabilities](/settings/capabilities)`,
   and navigating to `[/settings/legacy-capabilities](/settings/legacy-capabilities)`
   redirects cleanly to `[/settings/capabilities](/settings/capabilities)`.
2. **Verified-identity badge placement (`db63853d`, PR #726)**: The signed-in
   identity badge does not sit at the right end of `PageHeader`. Behind
   verified identity it renders as `user-tile` + `sidebar-logout` in the
   desktop/tablet sidebar footer (both expanded 232 px and collapsed 56 px) and
   as `phone-signed-in` + `Log out` at the foot of the phone navigation drawer.
3. **`CI` rail visibility**: `CI` is off by default in **Customize sidebar**
   (`Show CI`). Toggling `Show CI` on and saving adds `CI` (`[/ci](/ci)`) to
   the rail across reloads.
4. **Unpinned named session rail highlight (`Sidebar.jsx`)**: `Sessions` sets
   `aria-current="page"` on `[/sessions](/sessions)` and
   `[/session/new](/session/new)`, and leaves `Sessions` clickable without
   `aria-current="page"` when viewing an unpinned named session such as
   `[/session/director](/session/director)` so clicking `Sessions` returns to
   the session list; pinning a session gives its pinned rail item
   `aria-current="page"`.
5. **Deleting an open session from its pane (`App.jsx`)**: Deleting from
   `[/sessions](/sessions)` keeps the user on `[/sessions](/sessions)`, whereas
   deleting the currently open session from its own pane moves to
   `[/session](/session)` (the deployment's first remaining session).

---

## 3. External/system boundary stubs used

Every step ran against the real `grain demo` HTTP server, real embedded store,
real React tree and real Chromium/WebKit layout. Only boundaries that
`grain demo` does not synthesize on its own were intercepted:

| Boundary                                                                                        | Exact stub / mechanism                                                                                                                                                                                                                                                                    | What stayed real                                                                                                                                         |
| ----------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Port-changing reverse proxy for Staging warning                                                 | Local Node TCP/HTTP proxy on loopback `:18090 -> :18080` rewriting `Host`                                                                                                                                                                                                                 | `grain demo -staging` server, `/api/staging`, `Sidebar` and `PhoneNav` reachability logic                                                                |
| Verified IAP identity (`X-Goog-IAP-JWT-Assertion`)                                              | Ephemeral ES256 keypair + local loopback public-key endpoint passed via `-iap-public-key-url`, injecting signed JWT headers and handling `/_gcp_iap/clear_login_cookie` (`204`)                                                                                                           | Go `pkg/ui` JWT verification middleware, `/api/config` `authEmail`/`logoutURL`, `Sidebar` and `PhoneNav` identity & logout UI                            |
| Simultaneous `DisconnectedBanner` + `InstallNoticeBanner` + `ErrorBanner`                       | Fulfilled `/api/config` with `installNotice` (`mode: "host-mode"`), triggered a `500` mutation response for `ErrorBanner`, then aborted `/api/**` polls so `DisconnectedBanner` appeared above                                                                                            | Real banner components, sticky stacking, hit-testing and `320 px`–`1440 px` layout                                                                       |
| Mid-session task-list error on phone (`phone-tasks-stale`)                                      | Allowed initial `/api/tasks` `200`, then returned `503` on subsequent poll, then restored `200` on **Retry** tap                                                                                                                                                                          | `App.jsx` stale-tasks state, phone retry bar and recovery                                                                                                |
| Optimistic `Sending…`, back-to-back queued sends, failed-send retry/discard                     | Held `POST /api/console-sessions` and `POST /api/tasks/*/comments` behind explicit latches or returned `500` once before releasing to the real server                                                                                                                                     | `NewSessionPage` and `TaskChat` optimistic bubbles, queue serialization, `Not sent` / `Retry` / `Discard` recovery and real store persistence            |
| Inline config-repo change approval card, live run state, exit separator, shipped-defaults drift | Augmented `GET /api/tasks/<id>` with `pendingConfigChange`, `attempts` (`exitCode: 137`), or `status: "running"` (and `POST .../config-change` `200`), and `GET /api/console-sessions` with `missingShippedDefaults` / `crowdedNote` while stripping `If-None-Match` on intercepted reads | `PendingConfigChange` card, `ConsoleSessionPage` stale-grant banner + `Apply now`, `Adopt shipped defaults`, `TaskChat` `Pause` / `Pause now` / `Resume` |

---

## 4. Walkthrough steps and outcomes

### 4.1 Shared navigation across desktop, tablet and phone (Chromium & WebKit)

- **384 desktop/tablet navigation clicks** (`1440×900`, `1024×768`, `820×1180`,
  `1180×820`) and **110 phone navigation taps** (`390×844`, `320×568`,
  `844×390`) visited every primary destination with **0 horizontal page
  overflows**, **0 console/page errors** (filtering WebKit's documented
  abandoned-fetch navigation noise in `ui/e2e/pageerrors.js`) and **0 unexpected
  4xx/5xx responses**:
  - `[/inbox](/inbox)` (`Inbox`), `[/](/)` (`Tasks` + active state filters
    `Proposed`, `Waiting`, `Running`, `Asked`, `Merging`, `Deferred`, `Closed`,
    `Merged` and `Blocked` count), `[/tasks/1](/tasks/1)` (detail overlay),
    `[/new](/new)` (new task).
  - `[/boards](/boards)` and all 4 board visualizations (`kanban`, `timeline`,
    `report`, `tree`).
  - `[/sessions](/sessions)` and `[/session/director](/session/director)`.
  - `[/repos](/repos)` and all 7 tabs of `[/repos/acme/widgets](/repos/acme/widgets)`
    (`Overview`, `Tasks`, `Processes`, `Pull requests`, `Branches`,
    `Declarations`, `Advanced`), verifying horizontal tab-strip scrollability at
    narrow widths without page overflow.
  - `[/processes](/processes)` and `[/ci](/ci)` (after enabling `Show CI` in
    **Customize sidebar** and confirming persistence across reload).
  - All **20 Settings sections** across all 5 groups (`general`, `appearance`,
    `notifications`, `keyboard-shortcuts`, `capabilities`, `credentials`,
    `sandboxes`, `models`, `network`, `repositories`, `templates`, `schedules`,
    `merge-rules`, `chat`, `daemon`, `observability`, `storage`, `backup`,
    `access`, `upgrade`) plus legacy redirect
    `[/settings/legacy-capabilities](/settings/legacy-capabilities)` ->
    `[/settings/capabilities](/settings/capabilities)`.
  - All **8 System tabs** (`[/system/health](/system/health)`,
    `[/system/logs](/system/logs)`, `[/system/sandboxes](/system/sandboxes)`,
    `[/system/caches](/system/caches)`, `[/system/top](/system/top)`,
    `[/system/pipeline](/system/pipeline)`, `[/system/metrics](/system/metrics)`,
    `[/system/power](/system/power)`).

### 4.2 Desktop/tablet `PageHeader` rail, overflow menu and legacy team URLs

- Verified single-row `PageHeader` across `1440×900`, `1024×768`, `820×1180` and
  `1180×820`: section glyph + breadcrumbs (`tasks`,
  `repositories / acme/widgets / <tab>`, `settings / <section>`,
  `system / <tab>`, `task 1`), counts aligned level with the title (`|ΔcenterY| ≤ 2 px`),
  search/filters on the left, contextual actions (`New`, `Add repo`, `New board`)
  on the right, and global `Alerts`, `System` and `Settings` buttons unclipped at
  the right edge.
- Verified **More page actions** across **26 engine × viewport × page**
  combinations where header controls exceeded row width: header remained a
  single row (`height ≤ 56 px`), clicking `More page actions` opened the
  overflow menu in-viewport, `Escape` closed it and restored focus to the
  trigger button, and outside click dismissed it cleanly.
- Confirmed zero team UI (`Team`, team switcher, `Admin mode`, `All teams`)
  anywhere in the DOM, and verified legacy `/t/<team>/...` routes:
  - `/t/alpha/repos` -> `[/repos](/repos)`
  - `/t/alpha/settings/models` -> `[/settings/models](/settings/models)`
  - `/t/ops/tasks/1` -> `[/tasks/1](/tasks/1)`

### 4.3 Sidebar rail, `[` shortcut, Create menu, Staging, Customize & identity

- **Collapse / Expand & `[` shortcut**: Clicking `Collapse sidebar` narrowed the
  rail from `232 px` to `56 px`, persisted in `localStorage["grain.sidebarCollapsed"]`
  across `page.reload()`, toggled back via `[` on `document.body`, and was
  ignored while typing `[` into task search inputs and the session chat
  composer.
- **Create menu**: Exercised `Create options` (`New task`, `New session`,
  `New board`) on desktop and inside the phone drawer.
- **Staging environments**: Verified sub-path staging link
  (`/staging/docs-preview/`) vs host-port staging link
  (`http://127.0.0.1:4280/`), pinned/unpinned a staging environment into the
  Pinned section, and confirmed that accessing the UI through the port-changing
  proxy (`:18090 -> :18080`) marked the host-port staging item as unreachable
  through the proxy while leaving the `/staging/` sub-path item healthy.
- **Build stamp**: Verified footer stamp was unclipped and linked to
  `[/settings/upgrade](/settings/upgrade)` on desktop and at the foot of the
  phone drawer.
- **Verified IAP identity**: Confirmed `user-tile` (`alice@example.com`),
  Access settings link (`[/settings/access](/settings/access)`), and `Log out`
  (`/_gcp_iap/clear_login_cookie`) on expanded desktop rail, collapsed rail, and
  phone drawer (`phone-signed-in`).

### 4.4 Phone top bar, modal drawer, alerts popover and stacked banners

- **Phone bar & drawer (`390×844`, `320×568`, `844×390`)**: Verified top bar
  (`Open navigation`, grain mark, `Alerts`, `Settings`, `New task`) with the
  page `<h2>` heading below the bar. Confirmed `PhoneNav` (`role="dialog"`)
  stayed open across `Create options` toggle, staging-pin taps and opening/closing
  `Customize sidebar`, and closed automatically upon selecting a destination.
- **Alerts panel**: Opened `Alerts` on `390×844` and `320×568`; popover stayed
  within `[8 px, viewportWidth - 8 px]` with zero horizontal overflow.
- **Simultaneous banners + snackbar**: Exercised `DisconnectedBanner` +
  `InstallNoticeBanner` (`host-mode`) + `ErrorBanner` together at `1440×900`,
  `820×1180`, `390×844` and `320×568` in both Chromium and WebKit:
  - `1440×900`: `DisconnectedBanner` `y=[0, 48]`, `InstallNoticeBanner` `y=[48, 96]`,
    `ErrorBanner` `y=[838, 865]`, zero overlap, `elementFromPoint` hit `true`.
  - `320×568`: `DisconnectedBanner` `y=[0, 128]`, `InstallNoticeBanner` `y=[128, 236]`,
    `ErrorBanner` `y=[507, 545]`, zero overlap, `elementFromPoint` hit `true`,
    zero horizontal overflow.
- **Phone stale-tasks banner**: Confirmed `phone-tasks-stale` rendered on a
  mid-session `/api/tasks` `503` and cleared when tapping **Retry**.

### 4.5 Keyboard, focus restoration, discard confirmations, themes & reduced motion

- **Keyboard-only traversal**: Tabbed through skip/rail/header/main controls
  with visible focus rings and operable `Enter` / `Escape` handling.
- **Declined discard focus restoration**: Edited a process template on
  `[/processes](/processes)`, pressed `Escape` (and browser `Back`), declined
  the confirmation dialog, and verified the editor remained open with focus
  preserved rather than lost to `document.body`; also verified **Customize
  sidebar** unsaved-changes discard prompt on `Escape`.
- **Dark, light & `prefers-reduced-motion: reduce`**: Verified clean rendering
  and collapsed transition durations in both themes and reduced-motion contexts.

### 4.6 Live sessions (`[/sessions](/sessions)`, `[/session/new](/session/new)`, `[/director](/director)`) and interactive task chat

Exercised end to end in both **Chromium** and **WebKit**:

- **New session (`[/session/new](/session/new)`)**: Opened from the rail Create
  menu, phone drawer Create menu, and `[/sessions](/sessions)` `+ New session`.
  Confirmed empty chat with composer auto-focused, `Session name`, `Model`, and
  `Pin to sidebar`. Held `POST /api/console-sessions` to verify the immediate
  optimistic `Sending…` bubble on the very first message (Task 3361's fix), then
  released and confirmed the session was named, persisted across reload, and lit
  with `aria-current="page"` in the rail when pinned.
- **Deep links**: Confirmed `[/director](/director)` replaced history with
  `[/session/director](/session/director)` without trapping browser `Back`, and
  deep-linking to `[/session/nonexistent-deleted-session-xyz](/session/nonexistent-deleted-session-xyz)`
  redirected to `[/sessions](/sessions)` with a warning notice rather than
  opening another session's chat.
- **Work tree (`WorkTree`)**: Verified the 4-node work tree on
  `[/session/director](/session/director)`, opened child task `[/tasks/1](/tasks/1)`
  over the session while holding an unsent composer draft, pressed `Escape` to
  return, and confirmed the draft remained intact.
- **Markdown, wide tables & nested scroll**: Rendered headings, lists, code
  fences and a 140-character unbroken Markdown table cell across `1440×900`,
  `820×1180`, `390×844` and `320×568` (`document` horizontal overflow `0 px` at
  every width; scrolling the chat transcript kept `window.scrollY === 0`).
- **Queued & failed sends**: Sent back-to-back messages while the first send was
  in flight (serialized in order), verified a lone failed send returned text to
  the composer, and verified a failed send while a new draft was already typed
  kept the draft and displayed the `Not sent` bubble with working **Retry** and
  **Discard** actions.
- **Inline config-repo approval card**: Rendered `PendingConfigChange` inline in
  chat with a long `.grain/...` path (`0 px` overflow at `390×844` and
  `320×568`), verified **Reject** reason field toggle and **Approve & Apply**
  `POST` submission.
- **Standing session live controls, fresh chat & task-bound `Pause` / `Resume`**:
  Exercised `Adopt shipped defaults`, `Network access`, `File its tasks approved`,
  `Keep running`, `Chat space`, model selector, idle next-message notice,
  in-flight `.console-session-pending` stale-run notice + **Apply now**
  (`POST /api/console-sessions/<id>/cancel`), **Start a fresh chat** (both
  plain and **Start fresh with summary** sending `{ carryHandover: true }`),
  and task-bound interactive chat **Pause** / **Pause now** (with confirmation
  when running) / **Resume**.

### 4.7 Bug found and fixed: deleting a pinned standing session left an orphaned sidebar pin

- **Deterministic reproduction**:
  1. Open `[/session/new](/session/new)`, enter `Session name` =
     `Pinned Ops Audit`, check **Pin to sidebar**, and click **Create session**
     (navigates to `[/session/pinned-ops-audit](/session/pinned-ops-audit)` and
     stores `{ kind: "session", id: "pinned-ops-audit" }` in
     `localStorage["grain.pinned"]`).
  2. Click **Delete session** (from either the session pane or
     `[/sessions](/sessions)`) and accept the confirmation dialog.
  3. Open **Customize sidebar**: before this fix, `Pinned items` still counted
     and rendered a dead `pinned-ops-audit` entry.
  4. Open `[/session/new](/session/new)`, enter `Session name` =
     `Pinned Ops Audit` with **Pin to sidebar** left **unchecked**, and click
     **Create session**: because `createConsoleSession` (`pkg/ui/console_sessions.go`)
     deduplicates slugs only against active sessions, the new session received
     id `pinned-ops-audit` and immediately appeared pinned in the sidebar anyway.
- **Root cause & fix**:
  - `deleteStandingSession` in `ui/src/App.jsx` refreshed sessions and tasks
    after deletion without removing `{ kind: "session", id: deletedId }` from
    `PinnedNavContext`.
  - Updated `deleteStandingSession` in `ui/src/App.jsx` to call
    `removePinnedItem({ kind: "session", id: deletedId })`, and updated
    `addPinnedItem`, `removePinnedItem` and `movePinnedItem` in
    `ui/src/PinnedNavContext.jsx` to use functional state updates so rapid
    mutations never close over stale `pinnedItems`.
- **Regression coverage added**:
  - Unit tests: `ui/src/App.test.jsx` (covering deletion from both the session
    pane and `[/sessions](/sessions)` plus same-slug recreation) and
    `ui/src/PinnedNavContext.test.jsx`.
  - End-to-end Playwright test:
    `ui/e2e/sessions-worktree-pinned-nav-walkthrough.spec.js` (`Deleted standing
session removes its sidebar pin`, verifying desktop rail, **Customize
    sidebar**, `localStorage["grain.pinned"]`, and phone navigation drawer).
  - Re-running the full live-session walkthrough script against the rebuilt
    `grain demo` binary reported `orphanStillInStorage: false`,
    `orphanShownInCustomize: false`, `resurrectedInSidebarWhenUnpinned: false`
    and `PART 2 FINDINGS: []` in both Chromium and WebKit.
