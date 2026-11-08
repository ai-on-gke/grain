# Responsive navigation & live-session UI walkthrough — 2026-11-08 (task 3361)

Task 3361. This is a real-browser walkthrough of the shared shell and the
live session surfaces at desktop, laptop, tablet and phone widths, in
Chromium and WebKit, against a real `grain demo` server. It is the second
run of this brief today. Task 3165's run, at `d7dde0c2`, is in
`2026-11-08-responsive-navigation-live-sessions.md`, and this report takes
the `-task-3361` suffix so it does not overwrite that one. This run tests
`main` about 160 commits later, with fresh scripts.

**One fault was found and fixed here (§4.6):** on New session, the first
message left the composer at Send and drew nowhere until the session had
been created. Every later message shows at once as a "Sending…" bubble.

**The earlier run's follow-up is confirmed fixed:** task 3165's §4.9 fault
(a demo session's grants could not be edited, "config-map … is not
configured in mcpServers") no longer reproduces after `067f0b14`. Removing
a grant answered `PUT 200` and persisted, at every width and in both
engines.

Everything else held. §5 lists what looked like failures but traced to the
scripts.

## 1. Commit and environment

- **Commit tested**: `6d15dc3a` (`Merge pull request #912 …`), the tip of
  `main` when the run began. The build stamp read
  `v0.0.0-20261108080820-6d15dc3a27aa · 6d15dc3 · 2026-11-08 08:08Z`. The
  fix in §4.6 was then checked on a second demo built from this branch.
- **Sandbox**: Debian VM, Linux x86_64, 7 CPUs, 30 GB RAM; Go 1.26.10,
  Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`) from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `npm ci && npm run build` in `ui/` (into `pkg/ui/static`),
  `go build ./cmd/grain`, then
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file> -staging -host-mode-install -observability`
  over its own throwaway embedded store. A second demo on `:18082`, built
  with the fix, was used for the after-state (§4.6). Each browser context
  signed in with `POST /api/auth/access-code`, as `e2e/accesscode.js`
  does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no agents.
  The sandbox's file reader returns PNGs as bytes, so layout was judged
  from DOM geometry (bounding boxes, `elementFromPoint`, scroll sizes).
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository. Every step went
  through visible controls: clicks, taps, Tab, Enter, Escape, typing in
  the composer, browser Back and Forward, reloads, typed deep links and
  viewport resizes. Each context recorded console errors, page errors,
  failed requests and every 4xx/5xx response.

**Viewports**:

| Name           | Size     | Context                       |
| -------------- | -------- | ----------------------------- |
| desktop        | 1440×900 | —                             |
| laptop         | 1024×768 | —                             |
| iPad portrait  | 820×1180 | touch                         |
| iPad landscape | 1180×820 | touch                         |
| phone          | 390×844  | touch; `isMobile` in Chromium |
| small phone    | 320×568  | touch; `isMobile` in Chromium |
| rotated phone  | 844×390  | touch; `isMobile` in Chromium |

## 2. Scope notes: where the brief and the tree differ

These are recorded, not changed.

- **CI is hidden from the rail by default.** Customize sidebar lists it as
  "Show CI", off. Switching it on and pressing Save put CI in the rail,
  and it survived a reload. CI opened `/ci` with `aria-current=page` and
  the crumb `ci`. The setting was put back afterwards.
- **More page actions is a panel, not a `role=menu`.** It expands
  `page-header-actions` under the row, with `aria-expanded=true` on More.
- **A lone failed send goes back into the box.** The failure notice reads
  "Message not sent. It is back in the message box.", and no Retry is
  drawn. Retry appears only when something was typed after the failed
  message (`TaskChat.jsx` `unsent`). Both paths were exercised (§4.5).
- **Pause now asks first** (`8c8120da`). On a running task, Pause now
  raises "Pause <id> now? Its run is stopped at once, …". Plain Pause on
  a queued task applies at once.
- **Teams**: no team, team switcher, Admin mode switch or "All teams"
  appeared on any load. `/t/default/tasks` opens `/`,
  `/t/default/sessions` opens `/sessions`, and
  `/t/default/settings/models` opens `/settings/models`.
- **Signed-in email badge**: it draws only for a verified identity, which
  the demo does not have. `iap-header-logout.spec.js` and
  `phone-signed-in-logout.spec.js` cover it.
- **`/api/repos/acme/widgets/github-branches` 404s** on task detail,
  because the demo has no GitHub. This was allow-listed. It is the
  only console error on unstubbed loads.

## 3. Stubs (live or external boundaries only)

The demo's `-staging` and `-host-mode-install` flags provided the
staging environments and the install notice. Everything else ran against
the real server. Each stub below is a Playwright `route` on one context,
removed once its step ended.

1. **Run in flight**: `GET /api/tasks`, `/api/tasks?*` and
   `/api/tasks/<id>` were answered with the real response, with the
   session's (or the interactive task's) own task changed to
   `state: "running"` and `activityBySetup: false`. A session also gained
   an unfinished attempt 9. Conditional `If-*` headers were dropped from
   the forwarded request.
2. **Disconnection**: `route("**/api/**")` aborted every call with
   `internetdisconnected` until the disconnected banner showed, then was
   removed.
3. **Failed write**: every non-GET `/api/**` call was answered
   `500 {"error":"walk: injected failure"}` for one press of Approve on
   task 1, to raise the error snackbar.
4. **Failed send**: `POST /api/tasks/*/comments` was answered
   `500 {"error":"walk: injected send failure"}`. That happened either at
   once (lone failure) or after a held promise was released (failure with
   a draft typed after it).
5. **Held send**: the same POST, held on a promise, then released to the
   real server (back-to-back queued sends).
6. **Held / failed session create**: `POST /api/console-sessions` was
   either held on a promise, or answered
   `500 {"error":"walk: injected create failure"}` (§4.6).
7. **Pending config change**: an interactive task's `GET` responses
   gained a `pendingConfigChange`. It carried a summary, a description, a
   branch, a 20-hex `headSha`, two files (one 132 characters long), a
   diff and `validation.ok`. Approving would merge into a config
   repository the demo does not have, so only the card's layout and
   Reject's reason field were exercised.
8. **Port-changing proxy (not a stub of grain)**: a 15-line Node reverse
   proxy on `:18090` forwarded everything to the demo on `:18080`. The
   page port then differs from the daemon's listen port, which is what
   the staging "not reachable" marker is for.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

**Pages**: 58 deep links.

- **Main pages**: `/inbox`, `/`, `/tasks/1`, `/tasks/13`, `/new`,
  `/boards`, `/boards/board-1` … `board-4` (kanban, timeline, report,
  tree), `/sessions`, `/session/director`, `/session/new`, `/director`,
  `/processes`, `/ci`, `/repos`, `/repos/acme/widgets` and its `ci` and
  `releases` tabs.
- **Settings**: `/settings` and 25 section and group ids from
  `settingsSections.js`.
- **System**: `/system` and the eight tabs read from the page (health,
  logs, sandboxes, caches, top, pipeline, metrics, power).
- **Old team links**: three `/t/default/...` links.

**Widths**: Chromium at all seven viewports (406 loads); WebKit at
desktop, iPad portrait, phone, small phone and rotated phone (290 loads).

**Checks**: document/body horizontal overflow; any visible control
outside the viewport that is not inside a horizontally scrolling or
clipping container; unnamed buttons; team wording; console and page
errors, failed requests and 4xx/5xx responses.

**Outcome**: no layout finding in either engine. The only error was the
allow-listed `github-branches` 404 on `/tasks/1`.

### 4.2 Rail and header by real clicks (1440, 1024, 820, 1180; both engines)

- **Rail entries**: Inbox, Tasks, Boards, Sessions, Processes and Repos
  each opened their page with `aria-current=page`. The crumbs read
  `inbox`, `tasks`, `boards`, `sessions`, `processes` and `repositories`.
- **Header**: one row at every width.
  - The globals (bell, System, Settings) are the last controls in the
    row, uncovered (`elementFromPoint`) and inside the window. At 1440
    they sit at x=1288–1412, and at 1024 at x=872–996.
  - No header control sat to the right of the globals.
  - Counts sat level with the title (centre lines within 4px).
- **More page actions** (1024, 820, 1180): it opened on `/` (4 controls),
  `/processes`, `/repos` and `/system/metrics` (5 each) and
  `/system/logs` (4).
  - More stayed on the title row, and the panel stayed inside the window.
  - Escape closed it with focus back on More, and a press outside closed
    it too.
- **Repository tabs**: all seven, by click (overview, ci, merge queue,
  agents, branches, releases, advanced), each under
  `repositories / acme/widgets / …`, with no overflow.
- **Settings**: the header's Settings button opened `/settings`. All 21
  nav entries at 1440, 1024 and 1180 showed `settings / <section>` with
  no overflow. At 820 Settings is the section select.
- **System**: the header's System button, then all eight tabs, each under
  `system / <tab>`, Metrics included.
- **Task detail**: a list row opened `/tasks/<id>` with the crumb
  `task <id>`. Escape closed it back to `/`.

### 4.3 Rail shortcuts, staging, stamp, keyboard (desktop and tablets; both engines)

- **Collapse sidebar and `[`**:
  - Collapse sidebar folded the rail ("Expand sidebar"), and the fold
    survived a reload.
  - `[` unfolded and refolded it.
  - Typing `a[b` into the task search left the rail as it was, with the
    field holding `a[b`.
  - No folded rail button lacked an accessible name.
- **Create options**: it listed New task, New session and New board.
  Escape closed it with focus back on Create options.
- **Staging**:
  - `docs-preview` → `/staging/docs-preview/` and `grain-staging` →
    `http://127.0.0.1:4280/`.
  - On the loopback origin there was no "not reachable" marker, which is
    correct.
  - Through the port-changing proxy (stub 8), `grain-staging` showed
    "not reachable through this proxy" and the "Host port not reachable
    through this proxy" icon, at 1440 and 390 in both engines. The
    sub-path entry stayed linked root-relative.
- **Build stamp**: scrolled into view inside the rail, it was not clipped.
  It opened `/settings/upgrade`. Back and Forward moved between `/` and
  `/settings/upgrade`.
- **Keyboard only**: 15 Tabs from the top of `/inbox` reached Blocked.
  Enter opened `/?state=blocked&f=state:none:deferred|closed`.
- **Customize sidebar**: every switch has an accessible name ("Show
  Pinned" … "Show Staging").

### 4.4 Phone shell (390, 320, rotated 844×390; both engines)

- **Bar**: Open navigation, the grain mark, the alerts bell, Settings and
  New task, left to right.
  - They do not overlap and are all inside the viewport.
  - There is no desktop page header.
  - The page title heading ("All tasks", "Inbox", "Repos", "Director")
    sits below the bar: y=201 under a bar ending at 176 on a 390 phone,
    and y=221 under 196 on a 320 phone.
- **Drawer**:
  - It is a `role=dialog`, `aria-modal=true` named "Navigation".
  - The body is `overflow: hidden` while it is open, and a wheel over it
    did not scroll the page.
  - The System row and the build stamp at its foot were inside the
    viewport and uncovered.
- **Drawer stays open**: for the create menu (and Escape on that menu),
  for the staging pin tap (pinned, then unpinned), and for Customize.
- **Drawer closes**: Escape closed it with focus back on Open navigation.
  Tapping Inbox, Boards, Sessions, Processes, Repos, System or Tasks
  opened that page and closed it, each page with no overflow.
- **Alerts panel**: it stayed off the phone's edges (x=16–374 at 390,
  16–304 at 320, 296–736 rotated).
- **Repo tab strip**: it scrolls horizontally. The last tab (Advanced)
  scrolled into view and opened `/repos/acme/widgets/settings/advanced`.
- **Task overlay**: the paper fits the width, and "← Back" is in view.
  Browser Back closed it to `/`, and Forward reopened `/tasks/1`.
- **Orientation**: `/inbox` and `/session/director` were rotated to
  landscape and back with no overflow. The composer stayed in view.

### 4.5 Sessions and interactive chat

This ran in Chromium at 1440, 820, 390 and 320 and in WebKit at 1440,
820, 390 and 320. One script per width drove every step below through
the UI.

1. **New session from the create menu** (from the drawer on phones):
   - It opened `/session/new`, with the drawer closed, the composer
     (`Message`) focused, and Session name, Pin to sidebar and Model
     (Deployment default (gemini-2.5-pro), gemini-2.5-pro,
     gemini-2.5-flash).
   - A name was typed, Pin was checked, gemini-2.5-flash was chosen, and
     "First **bold** message with `code`" was sent.
   - The URL became `/session/<slug>`, with the typed name as the
     heading. The session record carried `model: gemini-2.5-flash`.
   - The bold rendered. The session was pinned in the rail with
     `aria-current=page`.
   - All of it survived a reload.
2. **New session from `/sessions`**: New session opened `/session/new`.
   An unnamed first message ("Plan the docs refresh for widgets") named
   the session.
3. **Back-to-back queued sends** (stub 5): two sends while the first POST
   was held showed two bubbles, both marked pending. After release and a
   reload, both were there in order.
4. **Failed sends** (stub 4):
   - A lone failure put "lone failure" back in the box with the notice.
     Send then delivered it.
   - A held failure with "draft after" typed behind it stayed in the
     transcript with Retry, and the draft was kept. Retry delivered it.
   - After a reload, each message showed exactly once.
5. **Live controls**:
   - With no run open, the panel read "No run is open on this session,
     so a change saved here takes effect on the next message you
     send.".
   - Removing a grant answered `PUT 200`, and the count went 18 → 17
     after a reload. This confirms task 3165's §4.9 fault is fixed.
   - "What it may reach on the internet" set to none persisted
     (`networkAccess: none`).
   - With a run in flight (stub 1), the panel read "A run is open on this
     session now.".
   - Removing a grant then raised "The run open on this session started
     before your change and does not have it: …", with Apply now and
     Stop.
6. **Start a fresh chat**: "Close this chat and start a fresh one" and
   "Start fresh with summary" each kept `/session/<slug>` and moved it to
   a new task (e.g. 45 → 47 → 48).
7. **Switching**: `/director` opened `/session/director`. The rail's
   Sessions entry opened `/sessions` with no overflow, and the session's
   row there reopened it.
8. **Delete**:
   - The confirm read `Delete session "<name>"? This removes the session
     from this deployment.`
   - Declining kept the URL, with focus back on Delete session.
   - Accepting went to `/session`. The session left the API list and the
     rail.
   - The deep link `/session/<slug>` then said "no session “<slug>” here.
     It may have been deleted; pick another below.", with no composer and
     no other session's chat.
9. **Interactive task chat** (a task filed with `interactive: true`):
   - Pause turned both Pause buttons into Resume. That survived a reload,
     with the server's `paused` record carrying
     `by: "operator (access code)"`. Resume turned them back.
   - The composer stayed inside the viewport.
   - With the task running (stub 1), Pause now asked "Pause <id> now? …".
     Declining left the task unpaused, with focus back on Pause now.
10. **Config-change card** (stub 7): Approve & Apply was inside the
    viewport with no overflow from the long file name. Reject opened
    "Why are you rejecting it?".

### 4.6 Fault (fixed): New session's first message vanished until the session started

**Reproduction** (deterministic, both engines, every width):

1. Open `/session/new`.
2. Hold `POST /api/console-sessions` (stub 6), as a slow create would.
3. Type a message and press Send.

**Observed at `6d15dc3a`**:

- The composer emptied, and the transcript still read "No messages yet".
- The only sign was a "Starting the session…" caption under the
  composer. Until the create returned, nothing showed what was sent.
- Every later message in any chat shows at once as a "Sending…" bubble.
  If the create fails (500), the text does come back to the box, so
  nothing is lost. It just looked lost while the create was in flight.

**Cause**: `TaskChat.jsx`'s `post` added the optimistic bubble only when
`!onSend`. `NewSessionPage.jsx` is the only caller that passes `onSend`.

**Fix**: `TaskChat` now draws the sending bubble for an `onSend` message
too, and an `onSend` failure goes through the same `unsent()` path as a
comment:

- With nothing typed since, the bubble goes and the text returns to the
  box (as before).
- With a draft typed since, the message stays as "Not sent" with Retry,
  rather than being silently dropped as before.

**After** (on the fixed demo, `:18082`, with the create held): the bubble
"Plan the docs refresh for acme/widgets…" drew right-aligned with the
header "Sending…", above the composer and inside the viewport:

| Viewport | Bubble (x, y–bottom) | Composer top |
| -------- | -------------------- | ------------ |
| 1440×900 | x=442–1391, y=349–430 | 779         |
| 390×844  | x=78–354, y=476–645   | 703         |
| 320×568  | x=67–284, y=109–321   | 379         |

Light and dark gave the same geometry, with text `rgb(36, 34, 29)` and
`rgb(233, 231, 225)`. There was no overflow.

**Regression coverage**:

- `NewSessionPage.test.jsx` gained "shows the first message as sending
  while the session starts", and the failure test now also asserts no
  bubble is left behind.
- `e2e/new-session-first-message-sending.spec.js` holds the real create
  at 1440×900 and 390×844 and asserts the bubble. It then releases the
  create and checks that the session opens and, after a reload, holds the
  message exactly once.
- Both fail without the change and pass with it.

### 4.7 Banners, snackbar, discard prompt (both engines; 1440, 820, 390)

- **Banners**:
  - With every call aborted (stub 2), the disconnected banner (y=0–48 at
    1440) and the host-mode install notice (y=48–116) stacked without
    overlapping, each the viewport's width, with no overflow.
  - Once the calls were let through again, the banner cleared within six
    seconds.
- **Snackbar**: a failed Approve on task 1 (stub 3) raised "walk:
  injected failure" in a snackbar (x=624–816, y=828–876 at 1440). It was
  inside the viewport and uncovered by either banner.
- **Discard prompt**: a process template editor's Name was edited.
  - Escape prompted the discard confirm. Declining kept the edit and put
    focus back in Name.
  - Browser Back prompted too. Declining again kept focus in Name.
  - Accepting returned to `/processes`.

### 4.8 Themes and motion (both engines; 1440, 820, 390)

`colorScheme` dark and light with `reducedMotion: reduce`, on `/inbox`,
`/session/director`, `/processes`, `/system/metrics`, `/` and `/ci`. The
body was `rgb(15, 16, 19)` in dark and `rgb(247, 246, 243)` in light. No
infinite animation was running and there was no overflow.

## 5. Script failures that were not product bugs

- **"More panel controls covered: Clear"** on `/repos` and Metrics: MUI's
  Autocomplete Clear button is `visibility: hidden` until there is text,
  and a Select's native input has `opacity: 0`. Neither is a control a
  person can see.
- **"More opened no menu"**: More is a panel, not a `role=menu` (§2).
- **"No Retry control"** after a lone failed send: by design that text
  goes back into the box (§2).
- **Crumb and menu text in lower case**: `innerText` applies CSS
  `text-transform`, so the comparisons were made case-insensitive.
- **"Resume lost after reload" in WebKit once**: Chromium and WebKit runs
  were pausing and resuming the same task at once. Rerun alone on its own
  task (58), WebKit held.
- **A malformed config-change stub** (files as objects) crashed the task
  pane with React error #31. The real shape is a list of paths, and the
  corrected stub rendered cleanly.

## 6. Tests run

- `npx vitest run src/components/NewSessionPage.test.jsx src/components/TaskChat.test.jsx`:
  30 passed. The new test fails with `TaskChat.jsx` reverted.
- The full `npx vitest run` suite: 257 files, 4022 tests passed.
- `npx playwright test e2e/new-session-first-message-sending.spec.js e2e/interactive-session-chat.spec.js`:
  passed. The new spec fails on a build without the fix.
- The walkthrough scripts, logs and screenshots stayed outside the
  repository. No screenshots or browser artifacts are committed.
