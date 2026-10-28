# Responsive navigation & live-session UI walkthrough — 2026-10-23

Task 277. A real-browser walkthrough of the shared shell and the live
interaction surfaces at desktop, tablet and phone widths, over a real
`grain demo` server. Three bugs found, all in the phone navigation
drawer, all fixed here with regression coverage.

## 1. Commit and environment

- **Base commit tested**: `66b743a5def3b4fe04866dcbea5456ca58081264`
  (`Merge pull request #4528 from bwsalmon/grain/049f1e09/task-270`) on
  branch `grain/049f1e09/task-277`; fixes re-walked on the branch head.
- **Sandbox**: Debian 12 VM, Linux x86_64; Go (`/usr/local/bin/go`),
  Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`) from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `go build ./cmd/grain` with the production Vite bundle
  (`npm ci && npm run build` into `pkg/ui/static`), started as
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file>`
  over its own throwaway embedded store; browsers signed in with
  `POST /api/auth/access-code`. A second demo of the base commit
  (port 18081, built from a `git worktree` of `66b743a5d`) was used for
  before screenshots and red runs.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent in this
  sandbox, so no Gemini-backed tooling (`LOOK=1`) was used. `grain demo`
  runs no agents, so nothing else needed it.

## 2. Scope note: the task text predates the single-tenant reset

The task brief asks for `/t/<team>/…` routes, a team switcher in the
`grain / <team> / <page>` breadcrumb, a team home page, Team settings,
User settings, an Admin mode switch, a user tile, team-scoped pinned
items and a New team menu entry. None of these exist in the tree: they
were retired by the single-tenant reset (`docs/single-tenant-reset.md`;
`ui/src/paths.js` reads pre-reset `/t/<team>/…` links as their team-less
page). What was walked instead is what the tree has: root-scoped routes,
the per-page header rail (`PageHeader`) on desktop/tablet, the
`PhoneNav` bar and drawer on phones, deployment Settings
(`/settings/<section>`) and System (`/system/<tab>`). The absence of a
team selector, a user tile, "All teams" and "New team" was confirmed
(the create menu offers exactly New task, New session, New board).
The template that generates this task should be updated in grain/config
to match; that is outside this repository.

## 3. Stubs (live or external boundaries only)

Everything else — navigation, routing, React rendering, layout, every
mutation and its read-back — ran against the real server unstubbed.

1. **Incremental agent replies in a standing session** — `page.route` on
   `GET /api/tasks/<session task id>`: the real response was fetched
   (with `If-None-Match` stripped so it is a full 200), and synthetic
   `authorKind: "automation"` comments appended: "Looking at it now…"
   from the first poll, then from the third poll a markdown reply with a
   3-column table holding a 136-character unbroken cell, a 400-character
   fenced code line and a bold list item. `grain demo` runs no agents,
   so nothing else would ever answer.
2. **Disconnection** — `context.setOffline(true)` for 12 s on the task
   list at 390×844, then back online, to raise and clear the
   disconnected banner.
3. Nothing else. `GET /api/repos/acme/widgets/github-branches` answers
   404 on every task pane; that is the documented behaviour of a demo
   without `-github-sim` (`pkg/demo/demo_githubsim.go`), not a stub and
   not a bug.

## 4. Steps and outcomes

Throwaway walk scripts (kept out of the repository) drove the visible
controls; findings below were then pinned down in `ui/e2e` and unit
tests.

### 4.1 Page matrix (Chromium and WebKit)

Viewports: desktop 1440×900, tablet 820×1180 and 1180×820, phone 390×844
and narrow phone 320×568 (touch contexts at phone widths).

At each, through the real rail (the drawer on phones): Inbox, Tasks,
the Proposed / Running / Closed state filters, Boards and every board
on it (kanban, timeline, report, tree), Sessions, Processes, CI, Repos;
the first task row's pane, closed with Escape; New task from the rail's
`+ New task` (desktop) or the bar's `+` (phone), closed with Escape;
Settings via the header gear (`top-bar-settings`, or `phone-settings`
on a phone), then all 16 `/settings/<section>` pages; all 9
`/system/<tab>` pages.

On every page: page `scrollWidth <= clientWidth`; no visible button,
link, input, select or tab outside the viewport unless inside a
scrolling container; the header's settings/system/alerts controls
present and drawn; console errors, page errors, failed requests and
4xx/5xx responses logged.

**Outcome**: no page overflow and no clipped control at any width in
either engine; no console or page errors and no failed requests other
than the expected `github-branches` 404 above. Escape closed every task
pane.

### 4.2 Phone drawer, keyboard and touch (Chromium and WebKit, 390×844 and 320×568)

- Tab reaches `Open navigation` from page load; Enter opens the drawer;
  focus moves into it; 40 Tab presses never leave it; `body` overflow is
  `hidden` while open and a wheel over the backdrop leaves
  `window.scrollY` at 0; Escape closes it and focus returns to
  `Open navigation`.
- A keyboard pick (focus `Boards`, Enter) navigates to `/boards` and
  closes the drawer.
- At 320×568 the footer is pinned: `System` row at y=476 (44 px) and the
  build stamp at y=531–554, both inside the viewport; the stamp opens
  `/system/upgrade`, `System` opens `/system`.
- Touch targets under 24 px are only the `Move …` drag handles (20×20
  icon), which carry a 44 px `::after` hit area under
  `(pointer: coarse)` (`style.css`), and inline text links.
- **Bug 1** (§5): the open drawer had no accessible role or name.
- **Bug 2** (§5): `Customize pinned items` in the drawer opened nothing.
- **Bug 3** (§5): `Create options` in the drawer opened nothing.

### 4.3 Disconnected banner

Offline at 390×844: the disconnected banner (390×108) sits at the top
and the `PhoneNav` bar moves below it (y=108) — no overlap, no page
overflow. Back online it cleared without a reload after 11.7 s (the next
poll). The standing-alerts banners the brief lists (admin mode, team
pause, setup steps, pause) no longer exist as banners: since
grain/task-4008 they are rows in the alerts panel the bell opens, and
the disconnected banner is the only strip left (`App.jsx`). The error
snackbar is portalled to `<body>` above modals (`ErrorBanner.jsx`) and
was covered by the existing offline-send specs, which pass.

### 4.4 Standing sessions (Chromium and WebKit; desktop, tablet, phone in dark with reduced motion, 320×568)

- `/sessions` → `+ New session` from the keyboard (focus, Enter) opens a
  named dialog; Escape closes it and returns focus to `+ New session`.
- Created a session with a 90-character unbroken title: the URL slug is
  derived from it, the header rail reads `sessions / <title>` on
  desktop/tablet, and nothing overflows at any width.
- Rail `Sessions` count went from n to n+1 on create and back to n on
  delete, at desktop, phone and 320 px.
- Live replies (stub 1): the first reply rendered within ~0.9–2 s, the
  markdown table within ~7–8 s. The table wraps inside the chat column
  (`scrollWidth == clientWidth` at 920/250/191 px); the long code line
  scrolls inside its own `<pre>` (3000 px in a 920/248/189 px box). No
  page overflow.
- Sent `**hello** from <width>` with Enter: the composer clears and the
  message renders. Sent a markdown table with the Send button: it posts
  `POST /api/tasks/<id>/comments` and renders as a `<table>`. (Enter
  sends and Shift+Enter is a newline; Ctrl+Enter is not a send key, by
  design — `TaskChat.jsx`.)
- Rotating the viewport (390×844 → 844×390 and back) kept the layout
  inside the viewport.
- Switched to Director from the list, then browser Back twice returned
  to the session with the sent message; a hard reload with the stub
  removed still showed it (persisted on the server).
- `Delete session` raises a `window.confirm` naming the session;
  Cancel kept it open at the same URL; OK deleted it and landed on
  `/session` (the deployment's first session, Director, by design —
  `deleteStandingSession` in `App.jsx`).
- Deep-linked the deleted session, and `/session/no-such-session`: the
  Sessions list rendered with an alert "There is no session “…” here. It
  may have been deleted; pick another below." — never another session's
  chat.
- Observation (not changed): two sessions with the same title get the
  same accessible name (`Open session Probe chat`) on the list; their
  slugs differ (`probe-chat`, `probe-chat-2`). Duplicate titles are
  allowed by design ("Creating a session with a title already in use
  opens the new one").

### 4.5 Existing specs

Baseline before any change, `npx playwright test
e2e/responsive-navigation-walkthrough.spec.js e2e/phone.spec.js
e2e/narrow.spec.js`: 127 passed, 1 skipped (Chromium + WebKit).

## 5. Bugs found and fixed

### Bug 1 — The phone navigation drawer had no accessible role or name

- **Repro**: 390×844, tap `Open navigation`, query
  `getByRole("dialog", { name: "Navigation" })` → nothing. The only
  element carrying `aria-label="Navigation"` was MUI's Modal root, which
  has `role="presentation"`; the paper had no role at all.
- **Cause**: `PhoneNav.jsx` put `aria-label` on the `Drawer`, whose own
  props land on the Modal root.
- **Fix**: `role="dialog"`, `aria-modal` and `aria-label="Navigation"`
  on the drawer's paper via `PaperProps`, as `SidePanel.jsx` already
  does for its form drawers.
- **Coverage**: `PhoneNav.test.jsx` "names the open drawer as a
  Navigation dialog"; the phone portrait case of
  `responsive-navigation-walkthrough.spec.js` asserts the named modal
  dialog holds the rail and goes away on a pick.

### Bug 2 — Customize pinned items did nothing from the phone drawer

- **Repro**: 390×844 (or 320×568), `/sessions`, tap `Open navigation`,
  tap `Customize pinned items`: the drawer closes and no dialog appears,
  in both engines. Pinning was impossible below 700 px.
- **Cause**: the overlay's open state lived in `Sidebar`. On a phone the
  Sidebar is inside the drawer, which closes on any tap inside it and
  unmounts the rail — and the overlay's state with it.
- **Fix**: `App.jsx` owns `showCustomizePinned`, passes
  `onOpenCustomizePinned` to `Sidebar`, and renders
  `CustomizePinnedOverlay` beside the other shell dialogs. Focus returns
  to `Open navigation` on Escape or Save.
- **Coverage**: new e2e "Customize pinned items opens from the phone
  drawer and its pin persists" (opens it from the drawer, pins Director,
  saves, reloads, finds the pin in the drawer; Chromium and WebKit).
  Red on the base commit (`element(s) not found`), green with the fix.

### Bug 3 — The rail's create menu did nothing from the phone drawer

- **Repro**: 390×844, tap `Open navigation`, tap `Create options` (the
  arrow beside `+ New task`): the drawer closes and no menu appears, so
  New session and New board were unreachable from the rail on a phone.
- **Cause**: the same close-on-any-tap capture; the menu is anchored to
  its button inside the drawer, and once the drawer's exit transition
  unmounted the rail the menu lost its anchor and state.
- **Fix**: `PhoneNav.jsx`'s `keepsDrawerOpen` also keeps the drawer
  open for a control with `aria-haspopup`; menu items still close it by
  click (React propagates the portalled menu's clicks through the
  drawer) and by Enter (`activatesWithoutClick` now counts
  `role="menuitem"`, since MenuItem turns Enter into onClick without a
  click event).
- **Coverage**: `PhoneNav.test.jsx` "opens the rail's create menu and
  closes on a pick by click/Enter" (red without the fix, green with
  it); new e2e "The create menu opens from the phone drawer and a pick
  closes it" (Chromium and WebKit).

## 6. Test runs after the fixes

- `npx vitest run src/App src/components/Sidebar src/components/PhoneNav
  src/components/CustomizePinned`: 13 files, 246 tests passed.
- `npx vitest run src/components/PhoneNav.test.jsx`: 11 passed.
- `npx playwright test e2e/responsive-navigation-walkthrough.spec.js
  -g "Phone Portrait|Customize pinned items opens|create menu opens from
  the phone"`: 6 passed (Chromium + WebKit).
- Full grain CI on the pushed branch: see the pull request.

## 7. Screenshots

Before/after phone shots (drawer, drawer after tapping Create options,
Customize pinned) in light and dark were taken over the base and fixed
demos and attached to the task's closing note; none are committed.
