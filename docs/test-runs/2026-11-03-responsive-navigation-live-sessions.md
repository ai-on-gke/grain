# Responsive navigation & live-session UI walkthrough — 2026-11-03

Task 2705. This is a real-browser walkthrough of the shared shell and the
live session surfaces. It covered desktop, tablet and phone widths, in
Chromium and WebKit, against a real `grain demo` server. It builds on the
2026-11-02 run, and this time goes further into the session flows: the
model picker, failed-send Retry, the run-in-flight notice with Apply now,
the inline config-change card, network access, deleted-session deep
links, Pause/Resume, and long names.

**No product bugs were found.** The fixes from 2026-11-02 still hold:
the wrapped header rail under Tab, New chat's window at 320×568, and the
typing dots under reduced motion. Yesterday's follow-up is also
resolved: the Director work tree's root is no longer a dead link (§4.7).
Everything that failed along the way traced to the walkthrough scripts
themselves. §5 lists each one so the next run does not repeat it. No
code changes are made here.

## 1. Commit and environment

- **Commit tested**: `0fdeff3b` (`Merge pull request #540 …`), the tip of
  `main`, on branch `grain/049f1e09/task-2705-walk-through-responsive`.
- **Sandbox**: Debian VM, Linux x86_64, 7 CPUs, 30 GB RAM; Go 1.26.10,
  Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`), from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `npm ci && npm run build` in `ui/` (into `pkg/ui/static`),
  `go build ./cmd/grain`, then
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file> -staging -host-mode-install -observability`
  over its own throwaway embedded store. Each browser context signed in
  with `POST /api/auth/access-code`, as `e2e/accesscode.js` does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no agents.
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository. Every step went
  through visible controls: clicks, taps, Tab, Enter, Escape, typing in
  the chat composer, browser Back and Forward, reloads, typed deep links
  and viewport resizes. Each context recorded console errors, page
  errors, failed requests and every 4xx/5xx response.

**Viewports**:

| Name | Size | Context |
|---|---|---|
| desktop | 1440×900 | — |
| laptop | 1024×768 | — |
| iPad portrait | 820×1180 | touch |
| iPad landscape | 1180×820 | touch |
| phone | 390×844 | touch; `isMobile` in Chromium |
| small phone | 320×568 | touch; `isMobile` in Chromium |
| rotated phone | 844×390 | touch; `isMobile` in Chromium |

## 2. Scope notes: where the brief and the tree differ

These are recorded, not changed.

- **Teams**: no team, team switcher, Admin mode switch or "All teams"
  appeared on any load (the matrix checks the page text for this).
  `/t/default/tasks` opens `/`, `/t/default/sessions` opens `/sessions`,
  and `/t/default/settings/models` opens `/settings/models`.
- **Signed-in email badge**: it draws only for a verified (IAP) identity.
  The demo has none, so it never drew here. It is covered by
  `iap-header-logout.spec.js` and `phone-signed-in-logout.spec.js`.
- **Rail entries are buttons**: Inbox, Tasks and its state views, Boards,
  Sessions, Processes, CI and Repos are `role=button` items carrying
  `aria-current=page`, not links. Their accessible names include the
  count ("Inbox 14", "Blocked 1"). Staging entries are real links.
- **Collapsed rail**: `[` folds the rail to 60px. The build stamp is not
  drawn in a collapsed rail; it returns when the rail is expanded.
- **Rail scroll**: with the host-mode install notice above it, the
  desktop rail (`aside.sidebar`, `overflow: auto`) is 852px tall and its
  content 893px. The build stamp is at the end of that scroll, not
  clipped: scrolling the rail brings it fully on screen.
- **Two Pause buttons on an interactive task**: the chat compose bar and
  the task's own actions each draw one, both visible. Both pause the
  same task.
- **Failed sends**: a lone failed send goes back into the message box
  ("Message not sent. It is back in the message box."). It is kept in
  the transcript with Retry only when the box already holds something
  ("Message not sent. Retry it from the conversation."). That is by
  design (`TaskChat.jsx`, grain/task-1360). Which case a user hits
  depends on whether the failure lands before or after their next
  keystroke.
- **Chat drafts across reload**: a half-typed message in a task chat
  survives rotation but not a hard reload. Nothing claims it should.
- **Rotation of a focused composer**: after an in-place 390×844 →
  844×390 resize on `/tasks/51`, the overlay pane keeps its portrait
  `scrollTop`, so the focused composer sits at y=458 of 390. A fresh
  load at 844×390 scrolls the pane so the composer is at y=222. Real
  mobile browsers scroll a focused field into view on resize, which
  Playwright's `setViewportSize` does not do, so this is not treated as
  a bug.
- **`/api/repos/acme/widgets/github-branches` 404** on every task detail
  load: the demo has no GitHub. Allow-listed, as in earlier runs.
- **WebKit "Blocked by Web Inspector"** request failures appear only
  while stub 2 aborts every `/api/**` call. It is WebKit's wording for
  an aborted route.

## 3. Stubs (live or external boundaries only)

The demo's `-staging` and `-host-mode-install` flags provided the
staging environments and the install notice. Everything else ran against
the real server, unstubbed. Each stub below is a Playwright `route` on
one context, removed once its step ended.

1. **Run in flight**: `GET /api/tasks` and `GET /api/tasks/<id>` for a
   session's own task were answered with the real response, with that
   task changed to `state: "running"`, `activityBySetup: false`, and an
   unfinished attempt 9. The demo runs no agents, so only a session
   manager could produce this. Conditional `If-*` headers were removed
   from the forwarded request, because WebKit's revalidations returned
   304s that cannot be rewritten.
2. **Disconnection**: `route("**/api/**")` aborted every call with
   `internetdisconnected` until the disconnected banner showed, then was
   removed.
3. **Failed write**: every non-GET `/api/**` call was answered
   `500 {"error":"walk: injected failure"}` for one press of Pause on an
   interactive task, to raise the error snackbar.
4. **Failed send**: `POST /api/tasks/*/comments` was answered
   `500 {"error":"walk: injected send failure"}` while failed sends were
   exercised, then removed before Retry.
5. **Held send**: the same POSTs were held on a promise, then released
   and forwarded to the real server, so back-to-back sends could be seen
   while still optimistic.
6. **Pending config change**: the session task's `GET` responses gained
   a `pendingConfigChange` with a realistic summary, files (one with a
   100-character name), a diff and `validation.ok`. Approving would
   merge into a config repository the demo does not have, so only the
   card's rendering, layout and Reject reason field were exercised.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

There are 57 deep links at desktop, iPad portrait, iPad landscape, phone
and small phone in each engine. WebKit also ran 320×568 a second time and
844×390 once.

- **Main pages**: `/inbox`, `/`, `/tasks/1`, `/tasks/13`, `/new`,
  `/boards`, `/boards/board-1` … `board-4`, `/sessions`,
  `/session/director`, `/session/new`, `/director`, `/processes`, `/ci`
  and `/repos`.
- **Repository**: `/repos/acme/widgets` and six old tab paths.
- **Settings**: `/settings` and 20 section ids.
- **System**: `/system` and all eight tabs, Metrics included.
- **Old team links**: three `/t/default/...` links.

Every load was checked for:

- document or body horizontal overflow;
- any visible button, link, input, select or tab outside the viewport,
  unless inside a horizontally scrolling container;
- a build stamp still clipped after its rail is scrolled to the end;
- header title and actions level on one row at widths ≥ 600;
- team wording;
- console errors, page errors, failed requests and 4xx/5xx responses.

**Outcome**: clean in both engines apart from the allow-listed
`github-branches` 404. Redirects were as before: `/tasks/13` opens
`/session/director`, `/settings/bootstrap` and `/settings/browser` fall
back to Models, and `/system/health` opens `/system`. Of the old
repository tab paths, `/repos/acme/widgets/releases` stays put and the
other five open the repository page.

### 4.2 Rail, by real clicks (desktop, both engines)

- **Rail entries**: clicking Tasks, Boards, Sessions, Processes, Repos,
  CI and Inbox opened each page. Each crumb read `tasks`, `boards`,
  `sessions`, `processes`, `repositories`, `ci` and `inbox`, and each
  entry was `aria-current=page`. Counts read "tasks 18", "boards 4",
  "sessions 1", "processes 9", "repos 1", "ci 0" and "inbox 14". The
  globals ended at x≤1412 of 1440.
- **Repository tabs**: Repos → acme/widgets → each of its 7 tabs. Crumbs
  read `repositories / acme/widgets / ci`, `… / merge queue`, `… / agents`,
  `… / branches`, `… / releases` and `… / advanced`. Back went to
  `/repos` and Forward to the last tab.
- **Settings**: the header's Settings button, then all 18 buttons in
  "Settings sections" by click: `settings / models` through
  `settings / memory`, each without overflow.
- **System**: the header's System button, then all 8 tabs:
  `system / health`, `logs`, `sandboxes`, `caches`, `top`, `pipeline`,
  `metrics` and `power`.
- **`[` shortcut**: it folded the rail from 232px to 60px ("Expand
  sidebar"), and the fold survived a reload. Typing `a[b` into the
  search box left the rail folded and the field holding `a[b`. Clicking
  the toggle unfolded it ("Collapse sidebar"). Every collapsed entry
  kept its accessible name.
- **Create menu**: Create options listed new task, new session and new
  board. Escape closed it with focus back on Create options.
- **Tasks state views**: Proposed, Waiting, Running, Asked, Merging,
  Deferred, Closed and Blocked were listed.
- **Staging**: `docs-preview` → `/staging/docs-preview/` (running) and
  `grain-staging` → `http://127.0.0.1:4280/` (crash-looping).
- **Build stamp**: it opened `/settings/upgrade`.

### 4.3 Header rail and More page actions (tablet and laptop, Chromium)

These ran at 820×1180 and 1024×768 on `/`, `/processes`, `/repos`,
`/boards`, `/ci`, `/sessions`, `/inbox` and `/system/logs`:

- The header stayed one 77px row.
- The globals sat level with the title, fully on screen.
- Wherever More page actions showed, Escape closed it with focus back on
  More, and a press outside closed it too. The header kept its height
  while the menu was open.

### 4.4 Phone bar, drawer and alerts (390 and 320, both engines)

- **Bar**: Open navigation at x=4, Settings and New task inside the
  width, the bell on screen, and the page h1 below the bar (y=140 at 390,
  160 at 320).
- **Alerts panel**: x=16–374 of 390 and 16–304 of 320. Escape closed it
  with focus back on the bell.
- **Drawer**: `aria-modal=true` and body `overflow: hidden`. The System
  row was at y=752 and the stamp at y=807–830 of 844. At 320 they were at
  y=476 and 531–554 of 568.
- **Staying open**: a tap on Pin docs-preview toggled it to Unpin and
  left the drawer open; it was toggled back. Customize sidebar opened
  its dialog over the drawer, and Escape closed it back to the drawer
  with focus on Customize. The create menu opened over the drawer
  without closing it.
- **Navigation**: tapping Processes navigated and closed the drawer with
  no overflow.
- **Rotation**: 390×844 → 844×390 → back, and 320×568 → 568×320 → back,
  left no overflow.

### 4.5 Banners and the snackbar (both engines)

| Width | Disconnected banner | Install notice |
|---|---|---|
| 1440 | y=6–42 | y=54–90 |
| 390 | y=6–102 | y=114–230 |

- No horizontal overflow.
- Removing stub 2 cleared the disconnected banner within 8 seconds.
- Stub 3 raised "walk: injected failure" inside the viewport:
  - at y=842–862 of 900 at 1440;
  - at y=802–822 of 844 at 390.
- `elementFromPoint` at the snackbar's centre was inside the snackbar.

### 4.6 Sessions

The flow below ran in Chromium at 1440×900, 390×844 and 320×568, and in
WebKit at 820×1180, 390×844 and 320×568.

1. **Create menu → New session** (the rail's, or the drawer's on a
   phone): `/session/new` opened with the Message composer focused and
   the crumb `sessions / new chat`. Session name, Model ("Deployment
   default (gemini-2.5-pro)") and Pin to sidebar were all visible, with
   no overflow.
2. **Name, model, pin, first message**: a Session name was typed, the
   third Model option was picked and Pin to sidebar was ticked. The
   message held `**hello**`, two Shift+Enter breaks and a markdown table
   with a 140-character cell, sent with Enter.
   - The URL became `/session/<slug>` and the crumb
     `sessions / <name>`.
   - `<strong>` and `<table>` rendered with no overflow.
   - The pinned rail entry was `aria-current=page`.
   - `GET /api/console-sessions` held the typed title and
     `model: gemini-2.5-flash`.
3. **Back-to-back sends** (stub 5): "queued one" and "queued two" sent
   in a row appeared at once as "Sending…" bubbles, in order. After
   release they settled with timestamps, in the same order.
4. **Lone failed send** (stub 4): it returned to the box with "Message
   not sent. It is back in the message box.", and no bubble was left
   behind.
5. **Failure with a draft in the box** (stub 4): the message was sent,
   then "draft kept" typed before the failure landed. The failed message
   stayed in the transcript with a focusable Retry, and the box kept
   "draft kept". After the stub was removed, Retry delivered it, and
   the server holds exactly one copy of each message. This passed in
   every pairing except WebKit at 320×568 on one of two runs: there the
   failure landed before the first keystroke, so case 4 applied instead
   (see §2).
6. **Hard reload** kept the URL, the crumb and every message.
7. **Run in flight** (stub 1), with the capabilities panel opened:
   - The panel read "A run is open on this session now. Capabilities,
     repository access, network access and the model are fixed when a
     run starts…".
   - Toggling a capability saved it, and raised "The run open on this
     session started before your change and does not have it…" with
     Apply now.
   - Apply now sent the real `POST /api/console-sessions/<id>/cancel`
     (200).
   - At 320×568 the alert sat above the visible part of the session
     body; Apply now was still reached and tapped through scrolling.
8. **No run open**: after the stub was removed and the page reloaded,
   the panel read "No run is open on this session, so a change saved
   here takes effect on the next message you send.". "What it may reach
   on the internet" offered `open` and `none`. Choosing `none` survived
   a reload (`networkAccess: none` on the server).
9. **Config-change card** (stub 6): "Approve & Apply" rendered inline
   inside the viewport at every width (x=30–163 at 390 and 320), with no
   overflow from the long file name. Reject change opened its reason
   field.
10. **Start a fresh chat**: "Start fresh with summary" and then "Close
    this chat and start a fresh one" each reopened the same
    `/session/<slug>` with an empty conversation.
11. **Switching**: rail/drawer Sessions → `/sessions`; `/director` →
    `/session/director`; the pinned entry → back to `/session/<slug>`,
    `aria-current=page` on desktop.
12. **Delete session**:
    - The confirm read `Delete session "<name>"? This removes the session
      from this deployment.`
    - Declining kept the URL, with focus on Delete session.
    - Accepting went to `/session` and dropped the pinned entry.
    - The deep link `/session/<slug>` then listed the sessions under
      "There is no session “<slug>” here. It may have been deleted; pick
      another below.", with no composer.

**Interactive task chat** (task 51, filed with `POST /api/tasks` and
`interactive: true`), at 1440×900 and 390×844 in both engines:

- The compose bar's Pause turned into Resume, which survived a reload.
  Resume turned it back into Pause.
- The composer stayed inside the viewport (y=775 of 900, y=739 of 844),
  with no overflow.
- A draft survived rotation to 844×390 and back, with focus kept.

### 4.7 Director work tree

`/session/director` drew "▸ Provenance · Work from task 13: 0 of 4
done". Its root is no longer an inert link to `/tasks/13`: nothing in the page
body is a link named for task 13 or the Director. This resolves the 2026-11-02 follow-up.

### 4.8 Long names, markdown and tables (both engines)

Two fixtures were created through the API:

- a session whose 154-character title has no spaces;
- a task with a 200-character title, plus a description holding a
  300-character table cell and a 400-character code line.

`/session/<id>`, `/sessions`, `/tasks/<id>` and `/` were loaded at 1440,
820, 390 and 320. Results:

- There was no horizontal overflow and no offscreen control.
- On desktop and tablet the crumb ended before the page actions, and
  those ended before the globals. At 1440 the crumb was at x=292–810,
  the actions at 822–1308 and the globals at 1320–1412. At 820 they were
  at 292–531, 543–651 and 663–792.

### 4.9 Tabs, System on a phone, nested scrolling (320×568, both engines)

- **Repository tabs**: tapping through all seven kept the selected tab
  inside the 320px strip (at most x=264), including after a hard reload.
- **System**: `/system` listed its tabs. Metrics opened
  `/system/metrics`, and Back returned to `/system`.
- **Nested scrolling**: on `/session/director` with the capabilities
  panel open, the composer stayed reachable inside the session body.
  Window `scrollY` stayed 0 and the document 568px tall, so there was no
  background scroll. A tap focused the composer.

### 4.10 Focus, discard prompts, keyboard, themes, reduced motion

- **Discard prompt** (1440 and 390, both engines): from `/processes`,
  the Weekly Dependency & Vulnerability Audit row opened
  `/processes/schedules/sched-2`, and its Title was edited. Escape, then
  browser Back, each prompted "You have unsaved changes here. Leave and
  discard them?". Declining kept the URL and the edit, with focus back
  in Title both times.
- **Keyboard only** (both engines): 15 Tabs from the top of `/inbox`
  reached "Blocked 1". Enter opened
  `/?state=blocked&f=state:none:deferred|closed` with Blocked
  `aria-current=page`. Back returned to `/inbox` and Forward to the view.
- **Themes and motion**: `colorScheme` dark and light with
  `reducedMotion: reduce`, at 1440 and 390, on `/inbox`,
  `/session/director`, `/processes`, `/system/metrics`, `/` and `/ci`.
  The body was `rgb(15, 16, 19)` in dark and `rgb(247, 246, 243)` in
  light. No infinite animation was running and there was no overflow,
  in both engines.

## 5. Script failures that were not product bugs

These are recorded so the next walkthrough does not chase them.

- **Rail locators**: `getByRole("link", { name: "Tasks" })` finds nothing
  in the rail, because its entries are buttons (§2).
- **A blank-looking click opened a task**: clicking at (900, 600) on `/`
  hit a task row and opened its overlay. The overlay's dialog then
  intercepted the rail toggle's click. With that overlay open, `[` typed
  into its reply field was correctly ignored.
- **Folded rail persists**: a folded rail persists in `localStorage`. A
  later step that expects the expanded rail's Create options or build
  stamp must unfold it first.
- **Hidden drawer under a menu**: with the create menu open over the
  drawer, MUI sets `aria-hidden` on the drawer, so role locators stop
  matching it. Check `.MuiDrawer-paper` visibility instead.
- **Back after a direct load**: after `page.goto` straight into a
  schedule pane, Back is a cross-document navigation. It meets the
  page's `beforeunload` prompt rather than the in-app one. Reach the
  pane by clicking from `/processes` to exercise in-app Back.
- **WebKit 304s**: WebKit revalidates with `If-None-Match`. A route that
  rewrites a fetched response must drop those headers, because a 304
  cannot be fulfilled.
- **The Model combobox's text** reads as empty through Playwright. The
  saved value was confirmed from `GET /api/console-sessions` instead.

## 6. Tests run

No product code changed, so no unit or e2e suites were re-run for a
fix. The walkthrough itself is §4. The scripts and their logs stayed
outside the repository, and no screenshots or browser artifacts are
committed.
