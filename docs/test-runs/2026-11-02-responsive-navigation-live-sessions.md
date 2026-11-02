# Responsive navigation & live-session UI walkthrough — 2026-11-02

Task 2407. A real-browser walkthrough of the shared shell and the live
session surfaces at desktop, tablet and phone widths, in Chromium and
WebKit, over a real `grain demo` server. Three bugs were found and are
fixed on this branch, each with an e2e regression test that fails on the
base and passes here. One lead is filed as a follow-up rather than
changed (§6).

## 1. Commit and environment

- **Base commit tested**: `36af7fc7` (`Merge pull request #468 …`), the
  tip of `main`, on branch `grain/049f1e09/task-2407-walk-through-responsive`.
  The fixes were re-checked with the same scripts against a second demo
  built from the branch.
- **Sandbox**: Debian VM, Linux x86_64, 7 CPUs, 15 GB RAM; Go 1.26.10,
  Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`), from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `npm ci && npm run build` in `ui/` (into `pkg/ui/static`),
  `go build ./cmd/grain`, then
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file> -staging -host-mode-install -observability`
  over its own throwaway embedded store. The fixed build ran the same way
  on `127.0.0.1:18081`. Each browser context signed in with
  `POST /api/auth/access-code`, as `e2e/accesscode.js` does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no agents.
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository. Every step went
  through visible controls: clicks, taps, Tab, Enter, Escape, browser
  Back and Forward, reloads, typed deep links and viewport resizes.

**Viewports**:

| Name | Size | Context |
|---|---|---|
| desktop | 1440×900 (also 1024×768, 900×800, 1280×500) | — |
| iPad portrait | 820×1180 | touch |
| iPad landscape | 1180×820 | touch |
| phone | 390×844 | touch; `isMobile` in Chromium |
| small phone | 320×568 | touch; `isMobile` in Chromium |
| rotated phone | 844×390 | touch |

## 2. Scope notes: where the brief and the tree differ

Recorded, not changed. Most match the 2026-11-01 report and still hold.

- **Teams**: no team, team switcher, Admin mode switch or "All teams"
  appeared on any load. `/t/default/tasks` opens `/`,
  `/t/default/settings/models` opens `/settings/models` and
  `/t/default/sessions` opens `/sessions`. The header's `top-bar-user`
  tile exists only for an IAP-signed-in identity
  (`iap-header-logout.spec.js`); the demo has none, so it never drew.
- **Phone bar**: menu, grain mark, "grain", alerts bell, Settings and
  New task. The page title is the heading under the bar, not text in it.
- **Lowercase headings**: `theme.js` lowercases every h1–h3, button and
  tab, so a session named "Walk chromium phone" is drawn as "walk
  chromium phone" in its phone heading. The desktop crumb keeps the
  case. This is the theme, not a bug.
- **More page actions**: the overflow is a disclosure (`aria-expanded`)
  that drops the slot's own wrapped rows under the rail, not an ARIA
  `menu`. Escape closes it and returns focus to More.
- **Rail Tasks breakdown**: the rail lists the state groups that have
  tasks. Nothing in the seed is merged, so no Merged row was drawn.
  Blocked was drawn, reached by Tab and opened with Enter.
- **Phone System**: `/system` on a phone is a list of tab buttons, not
  a tab strip. Each opens its tab, and Back returns to the list.
- **Sessions**: a console session has no Pause/Resume before or after
  its first message. Pause and Resume belong to an interactive task's
  chat (§4.6). "Restart" is **Start a fresh chat**, with or without the
  summary. There is no xterm terminal; a task-bound console is a chat,
  so the window was resized around it instead.
- **`/api/repos/acme/widgets/github-branches` 404** on every task
  detail load: the demo has no GitHub, and `branchRefs.js` treats an
  unreadable list as "no suggestions" by design. Allow-listed.
- **WebKit "… due to access control checks"** for `/api/tasks/13/tree`
  appeared once as a page error, when a navigation cancelled the fetch.
  `WorkTree.jsx` handles the rejection; this is WebKit's own report.

## 3. Stubs (live or external boundaries only)

The demo flags `-staging` (two fake persistent sandboxes) and
`-host-mode-install` (the install notice) replaced two of the 11-01
stubs. Everything else ran against the real server, unstubbed. Each stub
below is a Playwright `route` on that one context.

1. **Proxy flag**: `GET /api/config` answered with the real response
   plus `proxied: true`, so the rail could mark a host-port staging
   environment as not reachable through a proxy. Only a real proxy can
   say that.
2. **Disconnection**: `route("**/api/**")` aborting every call with
   `internetdisconnected` until the disconnected banner showed, then
   removed.
3. **Failed write**: every non-GET `/api/**` call answered
   `500 {"error":"walk: injected failure"}` for one press of Pause on
   an interactive task, to raise the error snackbar.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

54 pages at five sizes in each engine, against the base build:

- **Main pages**: `/inbox`, `/`, `/tasks/1`, `/new`, `/boards`,
  `/sessions`, `/session/director`, `/director`, `/processes`, `/ci`,
  `/repos`.
- **Boards**: `/boards/board-1` … `board-4` (board, timeline, report,
  tree).
- **Repository**: `/repos/acme/widgets` and its six
  `/settings/<tab>` tabs.
- **Settings**: `/settings` and all 19 section ids.
- **System**: `/system` and all eight `/system/<tab>` tabs, Metrics
  included.
- **Old team links**: three `/t/default/...` links.

Checks on every load: document and body horizontal overflow; every
visible button, link, input, select and tab inside the viewport unless
in a horizontally scrolling container; header title, actions and
globals on one row with no overlap; the build stamp not clipped; no
team wording; no console errors, page errors, failed requests or 4xx/5xx.

Outcome: clean in both engines apart from the allow-listed
`github-branches` 404. Crumbs read `tasks`, `task 1`, `boards / Report`,
`sessions / Director`, `repositories / acme/widgets / merge queue`,
`settings / config repo`, `system / metrics`, and so on. `/director`
became `/session/director`; `/settings/secrets` became
`/settings/credentials`; `/settings/operations` and `/settings` fell
back to Models; `/system/health` became `/system`.

### 4.2 Header rail (desktop and tablet)

At 820, 900, 1024 and 1440 on `/`, `/repos`, `/boards`, `/ci`,
`/processes`, `/system/logs`, `/sessions` and `/inbox`:

- The header stayed one 77px row on every page. The globals (Alerts,
  System, Settings) sat at the right end, fully on screen.
- More page actions appeared wherever the slot wrapped. Escape closed
  it and returned focus to More.
- **Keyboard**: Tab through the rail at 820 on `/processes` scrolled the
  clipped slot to each wrapped row and left it on the last ("+ Run").
  The search box and count were gone from the rail until a reload, in
  both engines. This is **bug 1**.

### 4.3 Phone bar and drawer (390 and 320, both engines)

- The alerts panel sat at x=16–374 of 390 and x=16–304 of 320. Escape
  closed it and returned focus to the bell.
- The drawer's System row and build stamp sat at its foot: System at
  y=752 and the stamp at y=807–830 of 844; at 320, y=476 and 531–554 of
  568. The body is `overflow: hidden` under it.
- Escape closed the drawer and returned focus to Open navigation.
- The build stamp opened `/settings/upgrade`, and the drawer closed.
  The System row opened `/system`.
- The create menu opened over the drawer and kept it open. Choosing
  New session closed the drawer within its 300ms transition.

### 4.4 Rail: staging, pins, Customize, keyboard (desktop)

- Staging listed `docs-preview` (running, `/staging/docs-preview/`) and
  `grain-staging` (crash-looping, `http://127.0.0.1:4280/`). With stub 1,
  `grain-staging` was marked "not reachable through this proxy" in its
  accessible name.
- Pin docs-preview put it under Pinned, and the pin survived a reload
  ("Unpin docs-preview", pressed).
- Customize sidebar opened its dialog. Escape closed it and returned
  focus to Customize.
- Keyboard only: Tab from the page start reached "Blocked 1", and Enter
  opened `/?state=blocked&…` with Blocked `aria-current=page`. Enter on
  Processes, Repos, CI, Boards, Sessions, Tasks and Inbox opened each,
  each `aria-current=page`. Back and Forward walked that history.

### 4.5 Banners and the snackbar

Stub 2 with the install notice from `-host-mode-install`:

| Width | Disconnected banner | Install notice |
|---|---|---|
| 1440 | y=0–48 | y=48–96 |
| 820 | y=0–68 | y=68–136 |
| 390 | y=0–108 | y=108–236 |

No horizontal overflow at any width. Removing the stub cleared the
disconnected banner and left the notice. Stub 3 raised "walk: injected
failure" at y=828–876 of 900 in both engines, and `elementFromPoint` at
its centre was the snackbar.

### 4.6 Sessions

The flow ran in Chromium at desktop, 390 and 320, and in WebKit at
iPad portrait, 390 and 320:

1. **Sessions page → New session**: `/session/new` with Message
   focused. No Pause, Resume or Start a fresh chat. Back went to
   `/sessions`, Forward to `/session/new`.
2. **Create menu → New session** (rail, or drawer on a phone): the menu
   listed New task, New session and New board. Message was focused.
3. A Session name was typed, **Pin to sidebar** ticked, and the second
   model option picked. The model picker offered "Deployment default
   (gemini-2.5-pro)", gemini-2.5-pro and gemini-2.5-flash.
4. **First message**: `**hello**`, two Shift+Enter breaks and a
   markdown table with a 140-character cell, sent with Enter. The URL
   became `/session/<slug>`, the crumbs `sessions / <name>`; `<strong>`
   and `<table>` rendered with no overflow. The rail (or drawer) listed
   it under Pinned with `aria-current=page`.
5. Hard reload kept the URL, crumbs and message. `/director` became
   `/session/director`, and the pinned entry switched back.
6. **Start a fresh chat**, with summary and then without, each reopened
   the same session with an empty conversation.
7. **Delete session**: the confirm read `Delete session "<name>"? This
   removes the session from this deployment.` Declining kept the session
   and focus on Delete; accepting went to `/session`, and the rail
   dropped the entry. A deep link to it listed the sessions, with no
   composer.
8. **At 320×568 and 844×390, step 3 could not be completed on the base
   build**: the New chat window was 2px tall and Message could not be
   tapped (**bug 2**). On the fixed build the whole flow passed at 320 in
   both engines.

**Interactive task chat** (task 20, filed through `POST /api/tasks`
with `interactive: true`), both engines:

- Pause turned into Resume, which survived a reload. Resume turned it
  back into Pause.
- A message with inline code, a link and a 120-character word rendered.
- At 1440×900, 820×1180, 390×844, 844×390 and 320×568 the composer
  stayed inside the viewport with no overflow.

**Work tree** (Director, desktop and phone): "work from task 13: 0 of 4
done". Child `/tasks/1` opened over the session; Escape returned to
`/session/director` with focus on that link. The root link `/tasks/13`
did nothing when clicked (§6).

### 4.7 Focus, discard prompts, themes, reduced motion

- Schedule pane `/processes/schedules/sched-2`, reached from
  `/processes`, with an edited Title, both engines: Escape and browser
  Back each prompted "You have unsaved changes here. Leave and discard
  them?". Declining kept the URL and the edit, with focus in Title.
- A task overlay opened from `/` closed on Escape, focus on its row.
- `colorScheme` dark and light with `reducedMotion: reduce`, at desktop
  and 390, on `/inbox`, `/session/director`, `/processes` and
  `/system/metrics`: body `rgb(15, 16, 19)` in dark and
  `rgb(247, 246, 243)` in light, with no overflow. The chat kit's typing
  dots on `/session/director` still looped (**bug 3**). On the fixed
  build no infinite animation ran on any of those pages.

## 5. Bugs and fixes

### Bug 1 — Tab through a wrapped header rail left it on its last row

**Reproduction**: 820×1180, `/processes`. Click the crumbs, press Tab
nine times to reach Settings. The slot's `scrollTop` is 336, and the rail
shows "+ Run" where the search box and count were. A click elsewhere
does not restore it. Same in WebKit, and on `/` and `/repos`.

**Cause**: `.page-header-slot` clips its wrapped rows with `overflow`,
and a browser scrolls a clipped container to show what is focused in it.

**Fix** (`PageHeader.jsx`): a Tab that lands on a wrapped row opens the
More menu, with focus kept there. Tab on past the rail closes it, and
the slot returns to its first line whenever the menu closes. **Test**:
`responsive-navigation-2026-11-02-walkthrough.spec.js`, "Tab through a
wrapped header rail …".

The first version also reset the slot's scroll on every `scroll` event
and opened the menu for any focus. That broke eight CI e2e specs that
click actions behind More (`boards`, `saved-boards-walkthrough`,
`recurring-composed-walkthrough`, `repos_workflows`,
`suite-run-dialog-order`): Playwright scrolls the clipped slot to reach
them, and the reset or the menu opening moved the target mid-click. Only
a Tab keypress now triggers the menu. All eight pass, as does the
regression test.

### Bug 2 — New chat's window collapsed on a short phone

**Reproduction**: 320×568 (or 844×390), `/session/new`.
`.task-chat-window` is 2px tall, and a tap on Message lands on
`.task-chat-compose`. The session can be neither named-and-sent nor
typed into. Same in WebKit.

**Cause**: the chat was `flex: 1; min-height: 0` in a scrolling body,
so the name field and Create button above it left it no room, and
nothing set a floor.

**Fix** (`style.css`): the chat is `flex: 1 0 auto`; its window is
`flex: 1 1 0px` with `min-height: 14rem`. The body scrolls to it on a
short screen. Sizes at 1440×900 and 390×844 are unchanged, and the
message list still scrolls itself. **Test**: same spec, "New chat keeps
a working chat window and composer".

### Bug 3 — a session's typing dots ignored reduced motion

**Reproduction**: `reducedMotion: reduce`, `/session/director`. Three
`cs-typing-indicator__typing-animation` animations run with infinite
iterations.

**Fix** (`style.css`): `animation: none` on
`.task-chat-window .cs-typing-indicator__dot` under
`prefers-reduced-motion: reduce`. **Test**: `reduced-motion.spec.js`,
"a session's typing indicator holds still".

## 6. Follow-up, not changed here

On `/session/director` the work tree's root link (`/tasks/13`, the
Director's own backing task) does nothing when clicked: no overlay and no
URL change, in both engines. The child links open their tasks. It may be
that a session's own task is meant to resolve to the session already
open, but then it should not be drawn as a link. A follow-up task was
proposed to decide and fix.

## 7. Tests run

- `npx vitest run` on `PageHeader.test.jsx`, `App.pageheader.test.jsx`,
  `ConsoleSessionPage.test.jsx` and `NewSessionPage.test.jsx`: 53 passed.
- `npx playwright test` on the new spec, `page-header-rail-walkthrough`,
  `interactive-session-chat`, `sessions-list-delete`,
  `sessions-worktree-pinned-nav-walkthrough`,
  `responsive-navigation-2026-11-01-walkthrough`, `phone` and `shell`:
  100 passed, 1 skipped. `reduced-motion.spec.js`: 7 passed.
- The two new tests in the new spec and the new reduced-motion test each
  failed on a build without their fix.

No screenshots or browser artifacts are committed.
