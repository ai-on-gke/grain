# Responsive navigation & live-session UI walkthrough — 2026-10-26

Task 1150, attempt 2. This is a real-browser walkthrough of the shared
shell and the live interaction surfaces, at desktop, tablet and phone
widths, over a real `grain demo` server. The 46-page matrix was clean in
both engines. An axe-core pass found three accessibility bugs in this
area, and all three are fixed here with regression coverage. Two more
findings went out as follow-up tasks because they are separate changes.

## 1. Commit and environment

- **Base commit tested**: `f6130294f` (`Merge pull request #4945 from
  bwsalmon/grain/049f1e09/task-1219`) on branch
  `grain/049f1e09/task-1150`. The fixes were re-checked on the branch
  head.
- **Sandbox**: Debian VM (kubernetes-granule backend), Linux x86_64;
  Go (`/usr/local/bin/go`), Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`) from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `npm ci && npm run build` (into `pkg/ui/static`), then
  `go build ./cmd/grain`, started as
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file>`
  over its own throwaway embedded store. Browser contexts signed in with
  `POST /api/auth/access-code`, as the e2e fixture does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no
  agents, so nothing else needed it.
- **axe-core** (latest from npm) was installed outside the repository
  (`/home/debian/axe`) and injected into pages by the throwaway walk
  scripts, with `bypassCSP: true` on those contexts only, because the
  app's `script-src 'self'` CSP blocks injection. `color-contrast` was
  disabled. Nothing here is committed or added as a dependency.

## 2. Scope note: the brief predates the single-tenant reset

As the 2026-10-23 report already recorded, the brief's `/t/<team>/…`
routes, team switcher, team home page, Team/User settings, Admin mode
switch, user tile, team-scoped pins and "New team" entry do not exist in
this tree (`docs/single-tenant-reset.md`). What was walked instead is
what the tree has:

- root routes;
- the per-page header rail on desktop and tablet;
- the `PhoneNav` bar and drawer on phones;
- `/settings/<section>` and `/system/<tab>`.

I confirmed again that there is no team selector, "All teams", user tile
or "New team" anywhere. The rail's create menu offers New task, New
session and New board. The template behind this task lives in
grain/config and should be updated there.

## 3. Stubs (live or external boundaries only)

Everything else ran against the real server, unstubbed: navigation,
routing, React rendering, layout, and every mutation and its read-back.

1. **Failed reply**: `page.route` on `POST /api/tasks/4/comments`,
   answering 500 with a plain-text body. This raises the error snackbar
   over an open task pane.
2. **Disconnection**: `context.setOffline(true)` for ~13 s, then back
   online. This raises and clears the disconnected banner.
3. Nothing else. `GET /api/repos/acme/widgets/github-branches` answers
   404 on task panes. That is a demo without `-github-sim` behaving as
   documented, not a stub and not a bug. `/tasks/new` asks
   `GET /api/tasks/new` and gets 404: it is not a route (New task is
   `/new`), and the pane says so.

## 4. Steps and outcomes

Throwaway walk scripts, kept out of the repository, drove the visible
controls. The findings were then pinned down in `ui/e2e` and in unit
tests.

### 4.1 Page matrix (Chromium and WebKit)

**Viewports**: 1440×900, 820×1180, 390×844 and 320×568, with touch
contexts below 600 px.

**Pages (46)**:

- **Lists**: `/inbox`, `/tasks`, `/tasks/1` (pane), `/new`, `/sessions`,
  `/processes`, `/schedules`, `/templates`, `/suites`, `/ci`.
- **Boards**: `/boards/board-1`…`board-4` (Kanban, list, timeline, tree).
- **Other top-level pages**: `/memory`, `/sandboxes`, `/caches`,
  `/metrics`, `/repos`, `/repos/acme/widgets`.
- **Settings**: all 17 `/settings/<section>` pages.
- **System**: all 9 `/system/<tab>` pages.

**On every page**:

- document `scrollWidth <= clientWidth`;
- every visible button, link, input, select and tab lies inside the
  viewport, unless it is inside a horizontally scrolling container;
- console errors, page errors, failed requests and 4xx/5xx responses
  were logged.

**Outcome**: no page overflow and no clipped control at any width in
either engine. There were no console or page errors beyond the two
expected 404s in §3.

### 4.2 axe-core pass (Chromium, 1440×900 and 390×844, all 46 pages)

**Violations found**:

| Rule | Where | Disposition |
|---|---|---|
| `nested-interactive`, `aria-allowed-role`, `label-content-name-mismatch` | `/sessions` rows | **Bug 1, fixed** |
| `list` | Settings section nav on every `/settings/*` page and `/memory` | **Bug 2, fixed** |
| `scrollable-region-focusable` | `/system/logs` log `<pre>`; Settings → Sandboxes example Dockerfile | **Bug 3, fixed** |
| `region` | `/boards/board-1`, `-2`, `-4` heading, chips and description | Follow-up (§6) |
| `heading-order` | `h6` subtitles on 46 pages | Not filed: MUI `subtitle2` headings under `h2`, cosmetic |

### 4.3 Sessions and session chats (Chromium 390×844 and 1440×900; WebKit 820×1180 and 390×844)

Flow at each size, through visible controls:

1. On `/sessions`, `+ New session` opens the empty "New chat" page.
2. Enter a name, tick **Pin to sidebar**, then **Create session**. The
   URL becomes `/session/<slug>`.
3. In the composer, type `**hello** with a table:`, Shift+Enter, a
   3-row markdown table with a 150-character unbroken cell, then Enter.
4. Open the rail (or the drawer on a phone) and read the pinned list.
5. Hard reload.
6. Choose **Delete session**, accept the confirmation, and read the rail
   again.
7. Deep-link the deleted session's URL.

Outcomes:

- After step 3, the message renders with a `<strong>` and a `<table>`,
  and the document does not overflow (`scrollWidth == clientWidth` at
  every size).
- After step 4, the new session is listed under Pinned, and the rail's
  `Sessions` count went up by one.
- After step 5, the URL and the message persist.
- After step 6, the confirm reads "Delete session "<name>"? This removes
  the session from this deployment." Accepting it returns to `/session`
  (the first remaining session, by design: `App.jsx`
  `deleteStandingSession`). The pin and the count drop back.
- After step 7, the page lists the sessions with the warning "There is
  no session "<id>" here. It may have been deleted". It never opens
  another session's chat.
- `/session` (bare) opens the Director, and `/session/nope-gone` lists
  the sessions with the same warning.
- The live-session cases the existing spec stubs (incremental replies,
  offline send, 503 session manager, reconnection) were re-run unchanged
  (§5) and pass.
- The brief's xterm terminal no longer exists. Task-bound interactive
  consoles are chats on interactive tasks (`TaskChat`), and they were
  covered by the same composer, send and render path.

### 4.4 Banner stacking

**Error snackbar and disconnected banner over a task pane, in Chromium**:

- **At 390×844**: the 500 snackbar from stub 1 renders at y=788–836
  above the pane, and is not `aria-hidden`. The disconnected banner
  (stub 2) renders at y=0–108 in the shell, with the `PhoneNav` bar
  pushed to y=108. **But the full-screen task pane (y=0–844) covers the
  banner completely**: `elementFromPoint` at y=20, 60 and 100 is inside
  the dialog. A sighted reader in a pane sees no outage state.
- **At 820×1180 and 1440×900**: the pane covers x≥232, which hides the
  banner's centred text in the same way.
- **The visually hidden mirror** (`ModalStatusMirror`) exists for screen
  readers, but its `sx` `width: 1, height: 1` is read by MUI as 100%,
  so the "1px" box measures the whole dialog, and only `clip` hides it.
- **Disposition**: a follow-up (§6). It changes how every modal stacks
  against the banner, which is not a scoped fix.
- **Back online**: the banner cleared within the next poll (~13 s),
  with no reload.

The standing-alert banners the brief lists (admin mode, team pause,
setup steps, pause) are rows in the alerts panel since grain/task-4008,
not banners. The existing "Banners and alerts wrap an unbroken word on a
320px phone" spec covers that stack, and it passes.

### 4.5 Keyboard, focus, drawer, Back and Forward, themes

These were covered by `ui/e2e/responsive-navigation-walkthrough.spec.js`,
re-run on the branch (§5). It covers:

- the drawer's focus trap, Escape and focus return;
- a keyboard pick closing the drawer;
- Back and Forward onto a task pane;
- Back guarding an edited schedule;
- coarse-pointer target sizes;
- dark and light rendering with reduced motion;
- 320 px overlay action rows;
- the pinned footer and build stamp;
- the drawer's create menu and Customize pinned items.

All of it passes.

By hand: on the fixed Sessions list, Tab goes from a row's
**Open session** button to its **Delete session** bin. Enter on the bin
asks for confirmation and stays on the list when dismissed. Enter on the
open button opens the session, and Back returns to the list.

## 5. Bugs and fixes

### Bug 1 — Sessions rows were a control nested in a control

- **Reproduction**: on `/sessions` at any width, run axe. `li[role=button]`
  "Open session Director" contains the button "Delete session Director":
  `nested-interactive`, `aria-allowed-role` (an `<li>` is not allowed
  `role=button`, so the list has no list items) and
  `label-content-name-mismatch`.
- **Fix** (`SessionsPage.jsx`, `style.css`): the row is a plain `<li>`
  holding an `Open session <name>` `<button>` with the glyph and the
  name. It is described by the status chip, and the chip, the hint and
  the bin sit beside it. The `<li>` keeps the click, so a pointer still
  opens the session from anywhere on the row. `.session-row-open` resets
  the button's own styling, so the row lays out as before.
- **Regression**:
  - `SessionsPage.test.jsx`: rows are list items, no button sits inside
    another, the accessible description is the status, and a click on
    the chip opens the session.
  - `e2e/sessions-list-delete.spec.js`: on a phone, no nested controls,
    and the list's children are bare `li`. It also checks the Tab order
    (open, then bin), Enter on each, Back, and a click on the chip. It
    fails on the old markup.
  - The existing layout and delete e2e tests were retargeted to the list
    item.

### Bug 2 — Settings section nav lists held bare buttons

- **Reproduction**: on any `/settings/*` page, axe's `list` rule fails on
  each group's `<ul>`, whose direct children are MUI `ListItemButton`
  divs with `role=button`. The filter results list has the same shape.
- **Fix** (`SettingsPage.jsx`): each `ListItemButton` is wrapped in
  `<ListItem disablePadding>`.
- **Regression**: `SettingsPage.test.jsx` "puts every section and filter
  result in a list item of its own" fails without the fix.

### Bug 3 — Scrolling `<pre>` blocks had no tab stop

- **Reproduction**: on `/system/logs` (and on Settings → Sandboxes at
  390 px), axe's `scrollable-region-focusable` fails. A log line wider
  than the box can only be scrolled with a pointer.
- **Fix**: the log `<pre>` (`LogsPage.jsx`) and the example Dockerfile
  (`CustomSandboxImageCard.jsx`) take `tabIndex=0`, `role="region"` and
  an `aria-label` ("Log lines", "Example Dockerfile").
- **Regression**: covered by re-running the logs and System e2e specs
  (below). These are attribute-only changes, so no separate test was
  added.

### Tests run

- **Vitest**: `SessionsPage`, `App`, `SettingsPage`, `settingsIndex`,
  `LogsPage` and `components/settings`. All pass.
- **Playwright (Chromium)**:
  - `responsive-navigation-walkthrough`
  - `sessions-list-delete`
  - `main-tasks-sessions-proposers-walkthrough`
  - `api-grant-picker`
  - `page-header-rail-walkthrough`
  - `deployment_operations_walkthrough`
  - `deployment-operations-logs-tail`
  - `deployment-operations-identity-sandbox-errors`
  - `system_workflows`
  - `scroll`
  - `settings-panel-walkthrough`
  - `sessions-worktree-pinned-nav-walkthrough`
  - and the other specs touching `/system/logs`.

  All pass.
- **axe re-scan** of `/sessions`, `/settings/*`, `/system/logs` and
  `/settings/sandboxes` on the branch build: the three fixed rules no
  longer fire.

## 6. Follow-ups proposed

1. **Keep the disconnected banner visible while a task pane or dialog is
   open** (§4.4). It carries the reproduction, the `elementFromPoint`
   evidence, and the MUI `width: 1` = 100% note on the visually hidden
   mirror.
2. **Put a saved board's heading inside its page's main landmark**
   (§4.2, `region`). BoardPage owns `main` only for report and panels
   boards, and the list, Kanban, timeline and tree views render their
   own `<main>` under a header that sits outside it.

No screenshots or browser artifacts are committed.
