# Responsive navigation & live-session UI walkthrough — 2026-10-30

Task 1907. A real-browser walkthrough of the shared shell and the live
session surfaces at desktop, tablet and phone widths, in Chromium and
WebKit, over a real `grain demo` server. Two bugs were found and are
fixed here, with unit and e2e regression coverage: a repository tab
missing from the header crumbs, and the phone drawer closing under
Customize. The 54-page matrix was otherwise clean in both engines.

## 1. Commit and environment

- **Base commit tested**: `155efda5` (`Merge pull request #176 from
  ai-on-gke/grain/049f1e09/task-1803`), on branch
  `grain/049f1e09/task-1907`. The fixes were re-checked on the branch
  head, with the same scripts.
- **Sandbox**: Debian VM (kubernetes-granule backend), Linux x86_64;
  Go 1.26.10, Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`), from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: built with `npm ci && npm run build` (into
  `pkg/ui/static`), then `go build ./cmd/grain`. Started as
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file>
  -staging -host-mode-install -github-sim -fake-ci` over its own
  throwaway embedded store.
  - `-staging` runs the demo's two fake persistent sandboxes (one on
    `/staging/docs-preview`, one on host port 4280).
  - `-host-mode-install` raises the real host-mode install notice.

  So, unlike the 2026-10-29 run, neither of those needed a stub. Each
  browser context signed in with `POST /api/auth/access-code`, as
  `e2e/fixtures.js` does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no
  agents, so nothing else needed it.
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository (`/home/debian/walk`).
  Every step went through visible controls: clicks, taps, Tab, Enter,
  Escape, browser Back and Forward, reloads and typed deep links.

## 2. Stubs

Only live boundaries were stubbed, each as a Playwright `route` on one
context. Everything else ran against the real server, unstubbed:
navigation, routing, React rendering, layout, every mutation and its
read-back.

1. **Disconnection**: `route("**/api/**")` aborting with
   `internetdisconnected` until the banner showed (about 8 s), then
   removed. This raises and clears the disconnected banner.
2. **Failed write**: `POST /api/tasks/<id>/pause` answering 500
   (`{"error": "… pause refused"}`). It was registered after (1), so it
   won while the outage was up, and raised the error snackbar over the
   banners.

## 3. Steps and outcomes

### 3.1 Page matrix (Chromium and WebKit)

**Viewports**: 1440×900, 820×1180 (iPad portrait), 1180×820 (iPad
landscape), 390×844 and 320×568. The two phone sizes ran in touch
contexts, with `isMobile` in Chromium.

**Pages (54)**:

- **Main pages**: `/inbox`, `/`, `/tasks/1`, `/new`, `/sessions`,
  `/session/director`, `/director`, `/session/new`, `/processes`, `/ci`,
  `/repos`.
- **Boards**: `/boards`, then `board-1` … `board-4` (Kanban, timeline,
  report, tree).
- **Repository**: `/repos/acme/widgets` and its six `/settings/<tab>`
  tabs.
- **Settings**: every `/settings/<section>` id (18), plus the group ids
  `deployment` and `browser`.
- **System**: every `/system/<tab>` (8, Metrics included).
- **Old team links**: three `/t/default/...` links.

**Checks on every load** (540 loads):

- document and body `scrollWidth` against `clientWidth`;
- every visible button, link, input, select and tab lies inside the
  viewport, unless it sits in a horizontally scrolling container;
- the header rail is one row (77 px on every desktop and tablet page);
- the bell, System and Settings sit at the rail's end, unclipped;
- no control in the rail's slot is half cut: each is either wholly
  inside or wholly behind **More page actions**;
- the build stamp's position;
- no team or admin wording;
- console errors, page errors, failed requests and 4xx/5xx responses.

**Outcome**:

- No horizontal overflow, no off-screen control and no half-clipped slot
  control, in either engine at any size.
- No console or page errors, and no failed requests. The
  `github-branches` 404 seen on 2026-10-29 is gone, because this run
  used `-github-sim`.
- No team, team switcher, user tile, Admin mode or "All teams" anywhere.
  `/t/default/tasks` opens `/`, `/t/default/settings/models` opens
  `/settings/models`, and `/t/default/inbox` opens `/inbox`.
- **Crumbs**:
  - `inbox`, `tasks`, `task 1`, `new task`, `boards / Board`;
  - `sessions / Director` (and `/director` replaces itself with
    `/session/director`), `sessions / new chat`;
  - `settings / <section>`, `system / <tab>`;
  - after the fix, `repositories / acme/widgets / <tab>` (bug 1).
- Two flags in my own script were not product faults:
  - The script read the task pane's own header on `/tasks/1` and `/new`,
    which carries no globals. The page's rail under it does.
  - On 1440×900 and 1180×820, the rail runs 27–127 px past the window,
    because this demo adds the Staging group and the install notice. On
    desktop the rail scrolls as a whole, and the stamp is its last row:
    scrolling the rail brings it fully into view (863–886 of 900). On a
    phone the footer is pinned (§3.2).
- `/settings/deployment` and `/settings/browser` are group ids, not
  routes. They fall back to General, as any unknown section does
  (`settingsSectionFor`). Recorded, not changed.

### 3.2 Rail, drawer and phone bar

**Desktop rail**:

- The create menu lists New task, New session and New board.
- Pinned and Customize sit at the top.
- The counts are Inbox 14, Tasks 18 (with its state breakdown), Boards 4,
  Sessions, Processes 9, CI 0 and Repos 1.
- Staging lists `docs-preview` and `grain-staging`.
- The build stamp is the rail's one link to `/settings/upgrade`.

**Phone (390×844, touch, both engines)**:

- The bar holds Open navigation, the grain mark and "grain", the alerts
  bell ("Alerts: 2 active"), Settings and New task. The page's own title
  is the `h2` under the bar (for example `inbox`), as recorded on
  2026-10-29 (`PhoneNav.jsx`).
- Open navigation opens `role=dialog` named "Navigation". The body is
  `overflow: hidden` under it, so the background does not scroll.
- The create menu keeps the drawer open, and Escape closes only the
  menu. New session from the menu closes the drawer and opens
  `/session/new` with the composer focused.
- **Customize** keeps the drawer open under it after the fix (bug 2).
- The System row and the build stamp sit inside the viewport at the
  drawer's foot.
- The Staging pin is drawn on touch (opacity 0.8).
- Tapping Processes navigates and closes the drawer, and Back returns
  to `/inbox`.
- The alerts panel spans x=16…374 of 390. Escape hands focus back to
  the bell.

### 3.3 Banners and snackbar (390×844, both engines)

- On `/` during the outage, the disconnected banner (y 0–108) sits
  above the host-mode install notice (y 108–236), with no overlap and
  no overflow. The disconnected banner clears on reconnect, and the
  install notice stays.
- With the outage up, a failed Pause raises the error snackbar at
  y 788–836, x 78–312. It is the topmost element at its centre
  (`elementFromPoint`) and auto-hides after about 5 s.

### 3.4 Header rail at tablet width (820×1180)

| Page | Rail | Shown in the row | Behind More page actions |
|---|---|---|---|
| `/boards` | 77 px | New board | — |
| `/sessions` | 77 px | + New session | — |
| `/repos` | 77 px | search field | Add repo |
| `/processes` | 77 px | Filter | + New, sort, + Run |
| `/ci` | 77 px | Filter | Start a run, Refresh, sort |
| `/` | 77 px | Filters (1) | Backlog order, Contact running (2) |

The More menu opens on Enter and closes on Escape, with focus back on
More. Tab from the page's slot reaches System and then Settings. This
ran in both light (`rgb(247,246,243)` body) and dark
(`rgb(15,16,19)`), with `reducedMotion: reduce`. On `/new`, Escape
closes the pane and hands focus back to "+ New task".

### 3.5 Sessions (1440×900, Chromium and WebKit)

1. **Create menu → New session** opens `/session/new` with the composer
   focused (`cs-message-input__content-editor`). The page shows a Session
   name field, the model picker and Pin to sidebar.
   - **Create session** is disabled while the name is blank.
   - No Pause or Resume is offered before the session starts.
2. **First message**: Pin to sidebar was ticked, and the message was
   ``Plan the **Q4 cleanup** for Walk session N``.
   - The page moved to `/session/plan-the-q4-cleanup-for-walk-session-N`,
     with crumbs `sessions / Plan the Q4 cleanup for …`.
   - The bold text rendered.
   - The rail's new Pinned entry is the selected one.
   - After a reload, `GET /api/console-sessions` has the title and a task
     (21), and `grain.pinned` holds the session.
3. **Pause/Resume**: a started console session has none. Stop is its own
   turn control, as `TaskChat`'s gating says.
4. **Start a fresh chat**:
   - **Close this chat and start a fresh one** moved the session's task
     from 21 to 22.
   - **Start fresh with summary** moved it from 22 to 23.
5. **Switching**: the rail has no per-session rows under Sessions.
   Sessions are reached through Pinned and the Sessions list (by design,
   `Sidebar.jsx`). Back and Forward between Director and the new session
   worked.
6. **Delete session**:
   - Declined first: the page stayed, and focus is still on "Delete
     session".
   - Then accepted: the API no longer lists it, the rail drops it, and
     the pane moves to `/session` (the first session).
7. **Deep link to the deleted session**: it shows the sessions list
   (crumbs `sessions`, no chat pane), not another session's chat.
8. `/director` replaces itself with `/session/director`, and a hard
   reload stays there. The work tree renders.
9. **Sessions page "+ New session"**: **Create session** with the name
   "Named walk session" opened `/session/named-walk-session-2` (the
   slug's first form was taken by an earlier run), titled by that name.
10. **On a phone**: the drawer's create menu → New session closes the
    drawer. The composer is focused, and Session name, Pin to sidebar
    and Send are inside 390 px.

No console errors, page errors or failed requests occurred.

### 3.6 Interactive task chat: Pause, Resume and resize

An interactive task was filed through `POST /api/tasks`
(`interactive: true`, with a long title) and then driven in the UI:

- **Pause** becomes Resume, and that survives a reload. **Resume** turns
  it back into Pause.
- A message with inline code rendered.
- The window was then resized to 820×1180, 390×844, 844×390 and back to
  1440×900. There was no overflow at any size.
- On a phone, after messages, Send sits below the fold of the pane's
  scroll (y 860 of 844). Scrolling the pane brings it into view, and on
  first open it sits at y 796–836. The composer is not sticky: it
  scrolls with the pane. Recorded, not changed.
- The 2026-10-29 observation still holds: the pane shows two "Pause"
  buttons (the composer's and the actions bar's).

### 3.7 Tab strip

- At 320×568, the repository's seven tabs scroll horizontally (657 px in
  208 px), and Advanced scrolls into view when picked.
- Changing tabs replaces the history entry, so Back leaves the
  repository page rather than stepping through its tabs. That is
  documented in `App.jsx`.

## 4. Bugs and fixes

### Bug 1 — a repository tab was missing from the header crumbs

- **Reproduction**: open `/repos/acme/widgets/settings/ci` at 1440×900.
  The rail reads `repositories / acme/widgets`, the same as Overview.
  The same happens on every tab but the releases pane.
- **Fix** (`App.jsx`): `repoTabCrumb` adds the tab's lower-cased label
  (`… / ci`, `… / merge queue`). Overview adds nothing.
- **Regression**: two `App.pageheader.test.jsx` rows
  (`/repos/acme/widgets/settings/ci` and `.../merge-queue`). Both fail
  without the fix. The matrix re-run shows every tab's crumb in both
  engines.

### Bug 2 — the phone drawer closed under Customize

- **Reproduction**: at 390×844 (touch), open the drawer and tap
  **Customize sidebar**. The drawer closes as the overlay opens, so
  closing Customize leaves the reader on the bare page, with focus on
  Open navigation. That holds in both engines.
- **Why**: the drawer closes on any tap that is not a text field, a menu
  trigger or a toggle (`PhoneNav.jsx` `keepsDrawerOpen`).
- **Fix** (`Sidebar.jsx`): both Customize buttons carry
  `aria-haspopup="dialog"`. That is accurate semantics, and
  `keepsDrawerOpen` already honours it. The drawer now stays under the
  overlay, and Escape or Save returns focus to Customize in the drawer.
- **Regression**:
  - `e2e/rail-staging-more-menu.spec.js`: "Customize keeps the drawer
    open under it and hands focus back".
  - `e2e/responsive-navigation-walkthrough.spec.js` "Customize sidebar
    opens from the phone drawer…" asserted the old close. It now asserts
    that the drawer stays, focus returns to Customize, and the new pin
    is visible at once and after a reload.

## 5. Tests run

- **Vitest**: `App.pageheader`, `Sidebar`, `PhoneNav` and `PageHeader`.
  All pass.
- **Playwright (Chromium and WebKit where the spec runs both)**, all
  passing on the fixed build:
  - `rail-staging-more-menu`, `page-header-rail-walkthrough`,
    `sidebar-navigation` and `repo-tabs-walkthrough`;
  - `responsive-navigation-walkthrough`, `director`,
    `sessions-list-delete` and `sessions-worktree-pinned-nav-walkthrough`;
  - `shell`, `alerts-panel-layout` and `disconnection`;
  - `track6-navigation-merged-dissolve` and `sidebar-hidden-sections`.
- **Browser walk scripts** (§3): re-run on the fixed build.

No screenshots or browser artifacts are committed.
