# Responsive navigation & live-session UI walkthrough — 2026-10-29

Task 1761. A real-browser walkthrough of the shared shell and the live
session surfaces, at desktop, tablet and phone widths, in Chromium and
WebKit, over a real `grain demo` server. The 52-page matrix was clean in
both engines. Three bugs were found in the rail and the header rail; all
three are fixed here with unit and e2e regression coverage.

## 1. Commit and environment

- **Base commit tested**: `76f38f8b` (`Merge pull request #128 from
  ai-on-gke/grain/049f1e09/task-1523`) on branch
  `grain/049f1e09/task-1761`. The fixes were re-checked on the branch
  head, in the same browser scripts.
- **Sandbox**: Debian VM (kubernetes-granule backend), Linux x86_64;
  Go 1.26.10, Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`), from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `npm ci && npm run build` (into `pkg/ui/static`), then
  `go build ./cmd/grain`, started as
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file>`
  over its own throwaway embedded store. Each browser context signed in
  with `POST /api/auth/access-code`, as the e2e fixture does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no agents,
  so nothing else needed it.
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository (`/home/debian/walk`).
  Every step below went through visible controls: clicks, taps, Tab,
  Enter, Escape, browser Back and Forward, reloads and typed deep links.

## 2. Scope notes: where the brief and the tree differ

- **Teams**: there is no team, team switcher, user tile, Admin mode
  switch or "All teams" anywhere. The matrix checked every page's text
  for those words at every width and found none. `/t/default/tasks`
  opens `/` (tasks) and `/t/default/settings/models` opens
  `/settings/models`.
- **Phone bar title**: the phone bar is menu, the grain mark and "grain",
  alerts bell, Settings and New task. The page's own title is the page
  heading directly under the bar (for example `h2 processes` at y≈75 on
  a 390×844 phone), not a title in the bar. That is the tree's design
  (`PhoneNav.jsx`), and it is recorded here rather than changed.
- **Tasks breakdown in the rail**: the rail lists the state groups that
  have tasks. In the demo store those are proposed, waiting, running,
  asked, merging, deferred, closed and blocked. Nothing is merged in the
  seed, so no "merged" row was drawn.
- **Terminal**: there is no xterm terminal. A task-bound interactive
  console is a chat (`TaskChat`; see its "Why this, and not a terminal"
  comment). Instead of resizing a terminal, the window was resized
  around an open interactive task chat (§4.6).
- **Pause and Resume** belong to an interactive task's chat. A console
  session has its own turn control instead, and "Restart" is its
  **Start a fresh chat** offer, with or without the summary (§4.5).
- **Settings and System**: `/settings/deployment` and `/settings/browser`
  are group ids, and they open the group's first section
  (`/settings/general`). `/system/health` is `/system`. Repository tabs
  live at `/repos/<owner>/<name>/settings/<tab>`; a bare
  `/repos/acme/widgets/ci` opens the repository's Overview, as
  `paths.js` documents.

## 3. Stubs (live or external boundaries only)

Everything else ran against the real server, unstubbed: navigation,
routing, React rendering, layout, and every mutation and its read-back.
Each stub is a Playwright `route` on that context alone.

1. **Persistent sandboxes** (`GET /api/persistent-sandboxes`). The demo
   runs no persistent-sandbox supervisor and lists none. The stub lists
   three:
   - `web-preview`: running, host port 8081;
   - `docs-site…`: restarting with 4 restarts (crash-looping), on
     `/staging/<name>/`;
   - one disabled sandbox.

   This drives the rail's Staging group. The same stub is in the new
   e2e spec.
2. **Config flags** (`GET /api/config`, real response with fields
   added):
   - `proxied: true`, to see the "not reachable through this proxy"
     marking. The page is loaded straight off the demo's port, so only
     the daemon can say that it came through a proxy.
   - `installNotice`, the host-mode install notice. That is a property
     of a host install, which the demo is not.
3. **Disconnection**: `route("**/api/**")` aborting with
   `internetdisconnected` for about 15 s, then removed. This raises and
   clears the disconnected banner.
4. **Failed write**: `POST /api/tasks/4/pause` answering 500 while the
   outage stub was up. This raises the error snackbar over the banners.
5. **Failed delete**: `DELETE /api/console-sessions/director` answering
   500, tried once. The toast it raised had gone before the outage
   began, so (4) was used for the stacking check instead.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

**Viewports**: 1440×900, 820×1180 (iPad portrait), 1180×820 (iPad
landscape), 390×844 and 320×568. The two phone sizes ran in touch
contexts, with `isMobile` in Chromium.

**Pages (52)**:

- **Main pages**: `/inbox`, `/tasks`, `/tasks/1` (pane), `/new`,
  `/sessions`, `/session/director`, `/director`, `/processes`, `/ci`,
  `/repos`.
- **Boards**: `/boards/board-1` … `board-4` (Kanban, timeline, report,
  tree).
- **Repository**: `/repos/acme/widgets` and its six
  `/settings/<tab>` tabs, plus `/releases`.
- **Settings**: every `/settings/<section>` id (20).
- **System**: every `/system/<tab>` (8, Metrics included).
- **Old team links**: two `/t/default/...` links.

**Checks on every load** (520 loads):

- document and body `scrollWidth` against `clientWidth`;
- every visible button, link, input, select and tab lies inside the
  viewport, unless it sits in a horizontally scrolling container;
- on desktop and tablet, the header rail is one row (77 px on every
  page);
- the globals are the alerts bell, System and Settings, at the rail's
  end;
- no control in the rail's slot is cut by the row's clip: each one is
  either wholly in the row or wholly behind **More page actions**;
- the build stamp lies inside the viewport;
- no team or admin wording;
- console errors, page errors, failed requests and 4xx/5xx responses.

**Outcome**: 510 loads clean. The other 10 are `/tasks/1` at each size,
and each shows only the expected
`GET /api/repos/acme/widgets/github-branches` 404 (a demo without
`-github-sim`). There was no overflow, no clipped or half-clipped
control, and no missing global, in either engine.

Crumbs read `tasks`, `task 1`, `repositories / acme / widgets`,
`repositories / acme / widgets / releases`, `settings / <section>`,
`system / <tab>` and `sessions / Director`.

### 4.2 Rail and drawer

**Desktop rail (Chromium and WebKit, 1440×900)**:

- The create menu lists New task, New session and New board.
- Pinned and Customize sit at the top.
- The counts are Inbox 13, Tasks 17 with its state breakdown, Boards 4,
  Sessions 1, Processes 9, CI 0 and Repos 1.
- **Staging** lists the two enabled sandboxes and leaves the disabled
  one out. `web-preview` links `http://127.0.0.1:8081/`; behind the
  stubbed proxy it reads "not reachable through this proxy" and has the
  warning icon. The sub-path one links `/staging/<name>/`, with a
  crash-looping dot and "last exit: exit 1" in its title.
- Pinning `web-preview` lists it under Pinned as well, and that survives
  a reload.
- The build stamp is the rail's one link to `/settings/upgrade`.

**Phone drawer (390×844, touch, both engines)**:

- "Open navigation" opens `role=dialog`, `aria-modal=true`, named
  "Navigation". The body is `overflow: hidden` underneath it, so the
  background does not scroll.
- The create menu keeps the drawer open, and its New session closes the
  drawer and opens `/session/new`.
- The System row and the build stamp sit at the drawer's foot, inside
  the viewport (stamp y=807–830 of 844).
- Tapping Processes navigates and closes the drawer, and Back returns to
  `/inbox`.
- **Bugs 2 and 3** were found here (§5).

**Alerts panel on a phone**: at 390 px and 320 px it spans x=16 to
x=vw−16, a 16 px inset on both sides. Escape closes it, and focus goes
back to the bell.

### 4.3 Header rail: More page actions

At 820×1180, `/tasks`, `/repos` and `/boards/board-2` overflow their row
and show **More page actions**. Enter opens the rest under the rail as
a menu, and the rail stays one 77 px row. **Bug 1** was found here
(§5): the open menu ignored Escape and presses outside it.

### 4.4 Banners and the snackbar

The host-mode install notice (stub 2) and the disconnected banner
(stub 3) were raised together on `/tasks` and on the `/tasks/4` pane:

| Width | Disconnected banner | Install notice | Rail / bar |
|---|---|---|---|
| 1440 | y=0–48 | y=48–96 | header rail starts at y=96 |
| 820 | y=0–68 | y=68–116 | header rail starts at y=116 |
| 390 | y=0–108 | y=108–176 (196 on `/tasks`) | phone bar below them |

- There was no horizontal overflow, and the disconnected banner cleared
  on the first poll after the stub was removed, with no reload.
- With the `/tasks/4` pane open, the disconnected text is mirrored as a
  sticky strip at the top of the pane (`ModalStatusMirror`), as
  designed.
- The install notice is covered by the full-screen pane, like any page
  content behind a modal. It is also on System → Health and Settings →
  Upgrade. This is recorded, not changed.
- The 500 from stub 4 raised the error snackbar at y=788–836 (phone) and
  y=828–876 (desktop). `elementFromPoint` at its centre is the snackbar
  itself, and it is not `aria-hidden`.
- An unreachable failure raises no toast while the banner is up, by
  design (`App.jsx` `showError`). A reply sent while offline stays in
  the composer.

### 4.5 Sessions

The same flow ran in Chromium at desktop and phone, and in WebKit at
tablet and phone:

1. **Create**: rail (or drawer) → Create options → New session opens
   `/session/new`.
   - The composer (`Message`) has focus.
   - **Session name**, the model picker and **Pin to sidebar** are
     present.
   - Pause, Resume and Start a fresh chat are absent before the session
     has started.
2. **Name, pin and first message**: type a name, tick Pin to sidebar,
   then type a first message with `**hello**`, Shift+Enter lines and a
   markdown table with a 140-character unbroken cell, and press Enter.
   - The URL becomes `/session/<slug-of-name>`.
   - The crumbs read `sessions / <name>`.
   - `<strong>` and `<table>` render, with no page overflow at any
     width.
   - The rail entry for the session is `Mui-selected` with
     `aria-current=page`.
3. **Hard reload**: the URL and the message persist.
4. **Restart**: Start a fresh chat offers "close this chat and start a
   fresh one" and "start fresh with summary". The latter reopened the
   same session with a fresh, empty conversation.
5. **Delete**: the confirm reads `Delete session "<name>"? This removes
   the session from this deployment.`
   - Dismissing it stays on the session, with focus on Delete session.
   - Accepting it goes to `/session`, and the rail no longer lists it.
6. **Deep link to the deleted session**: it lists the sessions with
   "There is no session “<id>” here. It may have been deleted; pick
   another below." No chat composer is shown, so it never opens another
   session's chat.
7. **Sessions page**: its New session opens `/session/new` with the
   composer focused. Back returns to `/sessions`, and Forward goes to
   `/session/new` again.
8. `/director` replaces itself with `/session/director`.

No console errors, page errors or failed requests occurred on any of
these runs.

### 4.6 Interactive task chat: Pause, Resume and resize

An interactive task was filed through the real API
(`POST /api/tasks`, `interactive: true`, task 27) and then driven in the
UI:

- **Pause** becomes Resume, and that survives a reload. **Resume** turns
  it back into Pause.
- A message with inline code and a link was sent and rendered.
- The window was then resized to 820×1180, 390×844 and 844×390
  (rotated). The composer reflowed inside the viewport each time, with
  no overflow.
- **Observation**: the pane shows two buttons both named "Pause" (the
  chat composer's and the task actions bar's). They do the same thing.
  This is recorded, not changed.

### 4.7 Focus, Escape and declined discard prompts

The schedule pane (`/processes/schedules/<id>`) was opened with an
edited Title, in both engines and at 1440 and 390:

| Gesture | Prompt | Outcome |
|---|---|---|
| Escape in the field | asked, declined | URL kept, edit kept, focus back in Title |
| "← schedules" clicked | asked, declined | focus on "← schedules", inside the dialog |
| "← schedules" with Enter | asked, declined | focus on "← schedules", inside the dialog |

In every case, the next Escape asks again, so focus is still inside the
dialog. Browser Back declining the same prompt is covered by the
existing "Browser Back asks before it drops an edited schedule on a
phone" spec, which passes (§6).

`/new` keeps its draft (`taskDraft.js`), so Escape closes it without
asking. Reopening it restores the title.

## 5. Bugs and fixes

### Bug 1 — the More page actions menu did not close

- **Reproduction**: at 820×1180 on `/tasks`, click **More page
  actions**, then press Escape, or click the page below. The menu stays
  open over the top of the page.
- **Why**:
  - Nothing listened for Escape or for outside presses.
  - A first fix's React `onKeyDown` on the rail never ran. The slot's
    contents are portalled in from the page, so React routes their
    events through the page's tree.
  - When the menu closed, the slot was measured while it was still the
    open menu, which clips nothing. That dropped "more" for a frame, and
    with it the focus being handed back.
- **Fix** (`PageHeader.jsx`):
  - While the menu is open, native listeners close it on Escape (focus
    goes back to "more") and on a `pointerdown` outside the rail.
  - Presses inside a MUI popover, modal or popper that the menu's own
    controls opened do not count as outside.
  - The overflow measure skips the open state.
- **Regression**:
  - `PageHeader.test.jsx`: "closes the more menu on Escape, handing
    focus back to more, and on a press outside the rail".
  - `e2e/rail-staging-more-menu.spec.js`: "the header's More page
    actions menu closes on Escape and on a press outside it". It asserts
    a one-row rail, Enter to open, Escape with focus back, and an inside
    press against an outside press.

### Bug 2 — the Staging pin was invisible on touch screens

- **Reproduction**: on a 390×844 touch phone, open the drawer. The
  "Pin web-preview" button's computed opacity is 0, in both Chromium
  and WebKit, until something hovers it, and a touch screen cannot
  hover.
- **Fix** (`Sidebar.jsx`): `@media (hover: none)` draws the pin at
  0.8 opacity. After the fix it measures 0.8 in both engines.
- **Regression**: the e2e "the drawer's staging pin is drawn, not left
  for a hover that cannot happen" test asserts opacity > 0.5.

### Bug 3 — tapping a Staging pin closed the phone drawer

- **Reproduction**: on a phone, open the drawer and tap the pin. The
  drawer closes, which hides the Pinned entry that was just added.
- **Why**: the drawer closes on any tap that is not a text field or a
  menu trigger (`PhoneNav.jsx` `keepsDrawerOpen`).
- **Fix**: toggles (`[aria-pressed]`) keep the drawer open as well.
- **Regression**:
  - `PhoneNav.test.jsx`: "keeps the drawer open when a pin toggle inside
    it is tapped". It fails without the fix.
  - The same e2e phone test asserts that the drawer is still open after
    the tap.

## 6. Tests run

- **Vitest**: `PageHeader`, `App.pageheader`, `Sidebar` and `PhoneNav`.
  All pass.
- **Playwright (Chromium)**, all passing:
  - `rail-staging-more-menu` (new);
  - `page-header-rail-walkthrough`;
  - `sidebar-navigation`;
  - `shell`;
  - `responsive-navigation-walkthrough`, `director`,
    `sessions-list-delete`, `sessions-worktree-pinned-nav-walkthrough`,
    `disconnection` and `alerts-panel-layout`.
- **Browser walk scripts** (§4): re-run on the fixed build.

No screenshots or browser artifacts are committed.
