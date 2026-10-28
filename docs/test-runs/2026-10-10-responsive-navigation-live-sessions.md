# Responsive Navigation and Live Sessions Walkthrough

Date: 2026-10-10 (03:50–04:10 UTC)

Commit tested: `003d11565`, the tip of `candidate`. Since the 10-09 walkthrough (`5123d46d7`), `candidate` has incorporated merges including:
- `7f7564082`: `feat(cli,ui): add grain version command and display version in user settings`
- `ed99d7879`: `feat(ui): allow removing a release and its candidates`
- `131c8f003`: `fix(ui): ask before leaving an edited task or custom fields`
- `8dd9070c8`: `fix(ui): preserve hook ordering across renders in CustomFieldsSection`
- `b7e86f787`: `feat(ui): team settings Git connections list and multi-host repos`
- `41d9df348`: `fix(ui): hide git connection Test button when readOnly`
- `f4db71129`: `test(ui): add e2e walkthrough for unsaved changes guard on task edit and custom fields`
- `607b5db86`: `feat(forge): support forge API clients per host and plain-git hosts`
- `ce3789180`: `fix(tests/container): query /api/sandboxes instead of non-existent /api/health in version upgrade test`

Every shared shell and live interaction surface was thoroughly exercised against a real Go `grain demo` server with an embedded SQLite database in both Chromium and WebKit.

## Environment

- Debian sandbox, Linux x86_64, 4 vCPU. Go 1.26.6, Node.js 20.19.0.
- Playwright 1.62.1 (matching `ui/package-lock.json`) with Chromium (headless shell 1234) and WebKit (build 2336), installed with `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install chromium webkit`.
- Server: Real `grain demo -addr 127.0.0.1:<port> -open=false`, built with `go build ./cmd/grain` over a fresh production build (`npm run build`) of `ui/`. Each worker ran against an isolated temporary SQLite database in a scratch `TMPDIR`.
- No `/home/debian/.gemini-api-key` was present; no external Gemini model tooling was used.
- Account: Loopback actor (`debian`, admin). Tested sessions set `grain.username` in `localStorage` first (following `e2e/pageerrors.js`) to suppress first-visit onboarding popups.
- Layouts verified:
  - Desktop: 1440×900 and short viewport 1280×500;
  - iPad / Tablet: portrait 820×1180 and landscape 1180×820;
  - Phone: portrait 390×844, landscape 844×390, and small phone 320×568.
- Interaction modes:
  - Desktop: mouse hover, click, drag-and-drop, keyboard navigation (Tab, Shift+Tab, Enter, Space, Arrow keys, Escape);
  - Tablet & Phone: touch / coarse-pointer (`hasTouch: true`, tap gestures, touch target dimensions ≥ 40px/44px, sliding drawer navigation, 100vw overlays);
  - Visual preferences: dark and light themes, `prefers-reduced-motion: reduce`.
- Audit criteria checked on every screen:
  - Zero document horizontal overflow (`scrollWidth <= clientWidth`);
  - Viewport-bounded layouts with zero unintended document-level vertical scrollbars;
  - Focusable controls within viewport boundaries or accessible scroll containers;
  - No clipped interactive controls under `overflow: hidden` containers;
  - Sideways scroll contained strictly within intentional scrollers (code blocks, GFM tables, Kanban boards, Gantt charts);
  - No covered controls or overlapping click targets via centre-point hit tests;
  - No uncaught console errors, page errors, or unexpected HTTP status codes.

## Stubs (Every One Documented)

| Boundary | How | Why |
| :--- | :--- | :--- |
| Live session transcript | `page.route(/\/api\/tasks\/\d+(\?.*)?$/)`, GET only. Intercepts task detail query, sets `state: "running"`, and injects incremental agent responses: multiline markdown, code blocks, and markdown tables. Accompanied by simulated offline network transitions (`context.setOffline`) and gateway error states (HTTP 503). | The demo server has no external AI provider configured behind the Director. |
| Deployment banners | `page.route("**/api/config")` intercepts config fetch to inject state combinations: `modelsConfigured: false`, `gitCredentialsConfigured: false`, `loopbackTrustWarning: "..."`, and active `agentPause` with unbroken 180+ character reasons and expiry timestamps. | The demo raises none of these by default; provider windows and external integrations are not configured in local demo mode. |

Everything else ran authentic and unstubbed:
- Real Go HTTP server (`pkg/ui.Server`) and real embedded SQLite database (`model.Store`);
- Real REST API endpoints (`/api/tasks`, `/api/console-sessions`, `/api/boards`, `/api/repos`, `/api/schedules`, `/api/templates`, `/api/suites`, `/api/sandboxes`, `/api/caches`, `/api/ci`, `/api/settings`);
- Real React DOM rendering, React routing, drawer management, and modal overlays;
- Real session lifecycle: creation via `POST /api/console-sessions`, switching, and deletion via `DELETE /api/console-sessions/:id`;
- Real touch and keyboard events driven by Playwright against real Chromium and WebKit browser engines.

## Surfaces Walked & Findings

### 1. Primary Navigation Shell Matrix

- **Desktop (1440×900 & 1280×500):**
  - Left navigation rail stays beside content across all views: Inbox, Task List, New Task overlay, Task Detail overlay, Boards (Kanban, Timeline, Dashboard, Report), Repos, Schedules, Templates, Suites, Sandboxes, CI, Settings, System, Metrics, and Sessions.
  - Active selection indicators accurately highlight the current route.
  - Rail count chips (Inbox count, task counts, repo counts, CI runs, sandboxes, caches) render crisply without clipping or layout breaks.
  - Short viewport (1280×500): The left rail scrolls independently (`overflow-y: auto`), retaining accessibility to all bottom items including the user tile, admin mode toggle, and the build stamp.
  - Deep linking directly to `/tasks/:id`, `/boards/:id`, `/repos/:name`, `/repos/:name/releases`, `/schedules/:id`, `/templates/:id`, `/suites/:id`, `/ci`, `/sandboxes`, `/caches`, `/settings`, and `/session/:id` renders immediately without layout shift or unwanted document scrollbars.
  - Version section in User Settings overlay (`7f7564082`): Monospace version string renders cleanly in the dialog without overflowing container boundaries.

- **Tablet (820×1180 portrait & 1180×820 landscape):**
  - Keeps navigation rail beside main content in both orientations (width 232px).
  - Dynamic orientation resize preserves open panels, task details, and scroll positions.
  - Task detail overlay on tablet occupies window width minus sidebar width (820 - 232 = 588px in portrait; 1180 - 232 = 948px in landscape).
  - Banner stacking (system warnings, pipeline pause, agent pause, loopback trust) resizes gracefully, consuming top vertical space rather than pushing content offscreen or covering actions (`content-header` `y` coordinate sits strictly below banner `y + height`).
  - Repo page state counts wrap cleanly within columns without clipping or overflow.

- **Phone (390×844, 844×390 landscape, 320×568 small):**
  - Navigation rail collapses into a compact top bar (`PhoneNav`); tapping "Open navigation" smoothly reveals the navigation drawer (width 232px).
  - Background scrolling is disabled while the drawer is open.
  - Selecting an item, tapping the backdrop, or pressing Escape closes the drawer and restores focus cleanly. Keyboard Enter and Space activation also closes the drawer while leaving interactive controls (team search input and dropdown) open during navigation within the rail.
  - Build stamp remains fully reachable at the bottom of the drawer scroller.
  - Overlays (New Task, Task Detail, Bulk Edit, Board Settings, Schedules, Templates, Suites, Settings, System, Metrics) open full-width (100vw).
  - Overlay action buttons wrap or stack cleanly without horizontal overflow, even at 320px width (`/schedules/sched-1`, `/admin`, `/admin/logs`, `/settings`).
  - Sticky action bars (`BatchActionsBar`) reserve adequate bottom clearance (`padding-bottom`), preventing overlap with the last row in task lists or board columns.

### 2. Live Interaction Surfaces & Console Sessions

- **Standing Chat Sessions (`/session/director`):**
  - Direct navigation to `/session/director` renders the Director chat interface immediately.
  - Composer input (`.cs-message-input__content-editor`) is visible, focused, and responsive.
  - Outgoing messages render with `.cs-message--outgoing` and proper theme bubble styling.
  - Message transcript auto-scrolls and remains pinned to the newest message (`scrollHeight - scrollTop - clientHeight === 0`).
  - Markdown formatting: headers, bold text, inline code, fenced code blocks, and markdown tables render cleanly. Fenced code blocks contain horizontal scroll within their `<pre>` containers without triggering parent horizontal overflow.
  - Switching sessions via the Sessions list or rail opens the selected session and updates the URL. Hard reload preserves current session URL and state.
  - Creating a second session with an identical title generates a disambiguated slug (e.g., `night-shift-2`) and reliably navigates to the newly created session.
  - Deleting a session displays confirmation dialog (`Delete session "<title>"?`); confirming deletes the session, updates the sidebar list, and safely redirects to `/session`.

- **Network Disconnect & Gateway Error Recovery:**
  - Browser offline (`context.setOffline(true)`) during active session or task navigation: A single "This deployment cannot be reached..." alert appears in the banner strip without toasts.
  - Messages composed while offline remain safely in the composer and do not disappear.
  - Restoring connectivity (`context.setOffline(false)`) automatically dismisses the banner without requiring a manual page reload.
  - Gateway HTTP 503 errors on task polling trigger the reachability warning until the server resumes answering HTTP 200, cleanly recovering session transcript polling.

- **Interactive Console Surface:**
  - In demo mode, `GET /api/console` returns `{"enabled":false,"sessions":null}`.
  - As designed, standing sessions and interactive tasks use the rich chat stream interface (`TaskChat.jsx`). There is no xterm.js PTY terminal canvas in this mode.

### 3. Input Methods, Accessibility & Theming

- **Keyboard-Only Navigation:**
  - Tab and Shift+Tab traverse navigation rail, search inputs, task lists, and action buttons in strict logical DOM order.
  - Enter opens task rows, board cards, and navigation links.
  - Escape closes modal overlays, drawers, and popups, returning keyboard focus to the opener.
  - Candidate keyboard walkthrough: Task, schedule, and template forms display base branch error feedback accessible via keyboard. Deleted schedule links followed from boards announce error and Back returns to the board. Team default model display logic verified.
  - Admin overlay tabs support arrow key cycling and wrap-around.
- **Coarse Pointer & Touch:**
  - All interactive buttons, drag handles, links, and drawer rows meet touch target minimums (≥ 40px/44px touch bounding boxes).
  - Tapping beside drag handles operates reliably without misclicks.
- **Themes & Motion:**
  - Dark mode (`background: rgb(15, 16, 19)`) and Light mode (`background: rgb(247, 246, 243)`) maintain proper surface contrast.
  - `prefers-reduced-motion: reduce` sets CSS transition durations to `0s`, eliminating motion disorientation while preserving functional feedback.

## Bugs & Issues Identified

No functional or visual regressions were found in `003d11565`.
All responsive behaviors, overlay layouts, live session flows, keyboard focus restorations, and banner stacking mechanisms continue to operate deterministically across both Chromium and WebKit.

Known external boundaries documented:
- `GET /api/relay-settings` returns 501 in demo mode (no relay configured).
- `GET /api/repos/acme/widgets/github-branches` returns 404 in demo mode (no remote git host configured).
- WebKit touch drag test in `phone.spec.js` is skipped on Linux due to headless WebKit protocol lacking touch move events; touch targets, taps, and keyboard flows are fully verified on WebKit.

## Verification & Test Results

Executed real Playwright test suites in both Chromium and WebKit against the running demo server:

1. `ui/e2e/responsive-navigation-walkthrough.spec.js`:
   - **Chromium**: 32/32 tests passed (1.7m)
   - **WebKit**: 32/32 tests passed (2.7m)
   - Total: 64/64 passed.
2. `ui/e2e/phone.spec.js`, `ui/e2e/sidebar-navigation.spec.js`, `ui/e2e/shell.spec.js`, `ui/e2e/stacked-banners.spec.js`, `ui/e2e/keyboard-open-rows.spec.js`:
   - **Chromium**: 34/34 passed.
   - **WebKit**: 30/30 passed (1 skipped touch drag gesture due to Linux WebKit protocol).
   - Total: 64 passed, 1 skipped.
3. `ui/e2e/candidate-keyboard-walkthrough.spec.js`, `ui/e2e/narrow.spec.js`, `ui/e2e/system-overlay-tabs.spec.js`, `ui/e2e/director.spec.js`, `ui/e2e/disconnection.spec.js`:
   - **Chromium & WebKit**: 22/22 passed.
4. `ui/e2e/boards.spec.js`, `ui/e2e/board.spec.js`:
   - **Chromium**: 20/20 passed.
5. Code style & formatting:
   - `npm run format:check`: Prettier check passed with 0 errors.

Grand Total: 170 passed tests, 1 skipped test, 0 failures.
