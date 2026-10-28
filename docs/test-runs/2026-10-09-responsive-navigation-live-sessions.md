# Responsive navigation and live sessions walkthrough

Date: 2026-10-09 (00:55–01:20 UTC)

Commit tested: `5123d46d7`, the tip of `candidate`. Since the 10-07 walk
(`afcf15814`) it has incorporated candidate merges through PR #3262,
including the Director session interactive seeding (`a617c6a70`),
loopback trust personal team warning adjustments (`f0f267afc`),
base branch gate reader team scoping (`79475ca2e`), model preview
disambiguation (`cd21fb46c`), and memory panel repo scoping (`7f7d98594`),
alongside the 10-07 duplicate session title selection and timeline lane
scroll fixes.

Every surface was walked again on a real `grain demo` server over fresh
embedded SQLite stores in both Chromium and WebKit.

## Environment

- Debian sandbox, Linux x86_64, 4 vCPU. Go 1.26.6, Node.js 20.19.0.
- Playwright 1.62.1 (exact match for `ui/package-lock.json`) with
  Chromium (headless shell 1234) and WebKit (build 2336), installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Server: the real `grain demo -addr 127.0.0.1:<port> -open=false`,
  built with `go build ./cmd/grain` over a production Vite build (`npm run build`)
  of `ui/`. Each worker process ran against its own throwaway embedded SQLite store
  under a scratch `TMPDIR`.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed
  tooling was used.
- Account: the demo's loopback actor (`debian`, an admin). Tested sessions
  set `grain.username` in `localStorage` first, matching `e2e/pageerrors.js`,
  so first-visit modals stay suppressed.
- Layouts verified:
  - Desktop: 1440×900 and short desktop 1280×500;
  - iPad / Tablet: portrait 820×1180 and landscape 1180×820;
  - Phone: portrait 390×844, landscape 844×390, and small phone 320×568.
- Interaction modes:
  - Desktop: mouse hover, click, drag-and-drop, and keyboard-only (Tab, Shift+Tab,
    Enter, Space, Arrow keys, Escape);
  - Tablet & Phone: touch / coarse-pointer (`hasTouch: true`, tap gestures, touch
    target sizing ≥ 40px/44px, drawer sliding navigation, full-width overlays);
  - Visual preferences: dark and light themes, `prefers-reduced-motion: reduce`.
- Audit criteria checked on every screen:
  - Zero document/body horizontal overflow (`scrollWidth <= clientWidth`);
  - Viewport-bounded layouts with zero unintended document-level vertical scrolling;
  - Focusable controls within viewport boundaries or accessible scroll containers;
  - No clipped interactive controls under `overflow: hidden` containers;
  - Sideways scroll contained strictly within intentional scrollers (code blocks,
    GFM tables, Kanban boards, Gantt charts);
  - No covered controls or overlapping click targets via centre-point hit tests;
  - No uncaught console errors, page errors, or unexpected HTTP ≥ 400 status codes.

## Stubs (every one)

| Boundary                | How                                                                                                                                                                                                                                                                                                         | Why                                                                      |
| ----------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| Live session transcript | `page.route(/\/api\/tasks\/\d+(\?.*)?$/)`, GET only. Fetches real task detail, sets `state: "running"`, and appends agent responses: multiline markdown, code blocks, and markdown tables. Accompanied by simulated offline network transitions (`context.setOffline`) and gateway error states (HTTP 503). | The demo server has no active AI provider running behind the Director.   |
| Deployment banners      | `page.route("**/api/config")` fetches real configuration and injects state combinations: `modelsConfigured: false`, `gitCredentialsConfigured: false`, `loopbackTrustWarning: "..."`, and active `agentPause` with unbroken 180+ character reasons and expiry timestamps.                                   | The demo raises none of these by default; provider windows are external. |

Everything else was real and unstubbed:

- Real Go HTTP server (`pkg/ui.Server`) and real embedded SQLite database (`model.Store`);
- Real REST API endpoints (`/api/tasks`, `/api/console-sessions`, `/api/boards`, `/api/repos`, `/api/schedules`, `/api/suites`, `/api/sandboxes`, `/api/ci`);
- Real React DOM rendering, React Router navigation, drawer management, and modal overlays;
- Real session lifecycle: creation via `POST /api/console-sessions`, switching, and deletion via `DELETE /api/console-sessions/:id`;
- Real touch and keyboard events driven by Playwright against real Chromium and WebKit instances.

## Surfaces Walked & Findings

### 1. Primary Navigation Shell Matrix

- **Desktop (1440×900 & 1280×500):**
  - Left navigation rail stays beside content across all views: Inbox, Task List,
    New Task overlay, Task Detail overlay, Boards (Kanban, Timeline, Dashboard, Report),
    Repos, Schedules, Templates, Suites, Sandboxes, CI, Settings, System, Metrics,
    and Sessions.
  - Active selection indicators accurately highlight current route.
  - Counts on rail entries (Inbox count, task counts) render crisply without clipping.
  - Short viewport (1280×500): The left rail scrolls independently (`overflow-y: auto`),
    retaining accessibility to bottom items including the build stamp and system indicators.
  - Deep linking directly to `/tasks/:id`, `/boards/:id`, `/repos/:name`, and `/session/:id`
    renders immediately without layout shift or unwanted scrollbars.

- **Tablet (820×1180 portrait & 1180×820 landscape):**
  - Keeps navigation rail beside main content in both orientations.
  - Dynamic orientation resize preserves open panels, task details, and scroll positions.
  - Banner stacking (system warnings, agent pause, loopback trust) resizes gracefully,
    consuming top height rather than pushing content offscreen or covering actions.
  - Repo page state counts wrap cleanly within columns without clipping or overflow.

- **Phone (390×844, 844×390 landscape, 320×568 small):**
  - Navigation rail collapses into a hamburger header bar; clicking "Open navigation"
    smoothly reveals the navigation drawer (width 232px).
  - Background scrolling is disabled (`body` has `overflow: hidden`) while the drawer is open.
  - Selecting an item or pressing Escape closes drawer and restores focus cleanly.
  - The build stamp remains fully reachable at the bottom of the drawer scroller.
  - Overlays (New Task, Task Detail, Bulk Edit, Board Settings) open full-width (100vw).
  - Overlay action buttons wrap or stack cleanly without horizontal overflow, even at 320px.
  - Sticky action bars (`BatchActionsBar`) reserve adequate bottom clearance (`padding-bottom`),
    preventing overlap with the last row in task lists or board columns.

### 2. Live Interaction Surfaces & Console Sessions

- **Standing Chat Sessions (`/session/director`):**
  - Direct navigation to `/session/director` renders the Director chat interface immediately.
  - Composer input (`.cs-message-input__content-editor`) is visible, focused, and responsive.
  - Outgoing messages render with `.cs-message--outgoing` and proper theme bubble styling.
  - Message transcript auto-scrolls and remains pinned to the newest message
    (`scrollHeight - scrollTop - clientHeight === 0`).
  - Markdown formatting: headers, bold text, inline code, fenced code blocks, and markdown
    tables render cleanly. Fenced code blocks contain horizontal scroll within their `<pre>`
    containers without triggering parent horizontal overflow.
  - Switching sessions via the Sessions list or rail opens the selected session and updates
    the URL. Hard reload preserves current session URL and state.
  - Creating a second session with an identical title generates a disambiguated slug
    (e.g., `night-shift-2`) and reliably navigates to the newly created session.
  - Deleting a session displays confirmation dialog (`Delete session "<title>"?`); confirming
    deletes the session, updates the sidebar list, and safely redirects to `/session`.

- **Network Disconnect & Gateway Error Recovery:**
  - Browser offline (`context.setOffline(true)`) during active session or task navigation:
    A single "This deployment cannot be reached..." alert appears in the banner strip.
  - Messages composed while offline remain safely in the composer and do not disappear.
  - Restoring connectivity (`context.setOffline(false)`) automatically dismisses the banner
    without requiring a manual page reload.
  - Gateway HTTP 503 errors on task polling trigger the reachability warning until the server
    resumes answering HTTP 200, cleanly recovering session transcript polling.

- **Interactive Console Surface:**
  - In demo mode, `GET /api/console` returns `{"enabled":false,"sessions":null}`.
  - As designed, standing sessions and interactive tasks use the rich chat stream interface
    (`TaskChat.jsx`). There is no xterm.js PTY terminal canvas in this mode.

### 3. Input Methods, Accessibility & Theming

- **Keyboard-Only Navigation:**
  - Tab and Shift+Tab traverse navigation rail, search inputs, task lists, and action buttons
    in strict logical DOM order.
  - Enter opens task rows, board cards, and navigation links.
  - Escape closes modal overlays, drawers, and popups, returning keyboard focus to the opener.
  - Admin overlay tabs support arrow key cycling and wrap-around.
- **Coarse Pointer & Touch:**
  - All interactive buttons, drag handles, links, and drawer rows meet touch target minimums
    (≥ 40px/44px touch bounding boxes).
  - Tapping beside drag handles operates reliably without misclicks.
- **Themes & Motion:**
  - Dark mode (`background: rgb(15, 16, 19)`) and Light mode (`background: rgb(247, 246, 243)`)
    maintain proper surface contrast.
  - `prefers-reduced-motion: reduce` sets CSS transition durations to `0s`, eliminating motion
    disorientation while preserving functional feedback.

## Bugs & Issues Identified

No functional or visual regressions were found in `5123d46d7`.
All previous responsive fixes (session disambiguation, Gantt now-line lane containment,
usage-limit pause string wrapping, drawer keyboard traps, and offline banner singletons)
continue to pass deterministically across both Chromium and WebKit.

Known external boundaries documented:

- `GET /api/relay-settings` returns 501 in demo mode (no relay configured).
- `GET /api/repos/acme/widgets/github-branches` returns 404 in demo mode (no remote git host configured).
  Both are expected in the standalone demo environment.

## Verification & Test Results

Executed real Playwright test suites in both Chromium and WebKit against the running demo server:

1. `ui/e2e/responsive-navigation-walkthrough.spec.js`:
   - **Chromium**: 32/32 tests passed (1.7m)
   - **WebKit**: 32/32 tests passed (2.6m)
2. `ui/e2e/phone.spec.js`, `ui/e2e/shell.spec.js`, `ui/e2e/sidebar-navigation.spec.js`:
   - **Chromium**: 34/34 tests passed (25.3s)
   - **WebKit**: 24/24 passed (1 skipped touch drag gesture not supported by Linux WebKit protocol) (45.1s)
3. `ui/e2e/boards.spec.js`, `ui/e2e/board.spec.js`:
   - **Chromium**: 20/20 tests passed (20.0s)
4. `ui/e2e/director.spec.js`, `ui/e2e/disconnection.spec.js`:
   - **Chromium**: 8/8 tests passed (1.9m)
5. `ui/e2e/stacked-banners.spec.js`, `ui/e2e/keyboard-open-rows.spec.js`, `ui/e2e/candidate-keyboard-walkthrough.spec.js`:
   - **Chromium & WebKit**: 14/14 tests passed (1.1m)
6. `ui/e2e/narrow.spec.js`, `ui/e2e/system-overlay-tabs.spec.js`:
   - **Chromium**: 6/6 tests passed (12.3s)
7. Code style & formatting:
   - `npm run format:check`: Prettier check passed with 0 errors.
