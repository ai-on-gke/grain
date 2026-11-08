# Responsive navigation & live-session UI walkthrough — 2026-11-08

Task 3165. This is a real-browser walkthrough of the shared shell and the
live session surfaces at desktop, laptop, tablet and phone widths, in
Chromium and WebKit, against a real `grain demo` server. It repeats the
2026-11-04 walkthrough on a tree about 230 UI commits newer, from fresh
scripts.

**One fault was found (§4.9):** in `grain demo`, any change to a standing
session's grants is refused with "capability
https://grain.dev/capabilities/config-map is not configured in
mcpServers". The demo never registers the built-in config-map server in
its capability registry, yet every session lists the default
`config-map#config-map {"config_maps":["bootstrap"]}` grant. Fixing it
means changing how the demo builds its capability registry, or how the
session update re-checks grants, and neither is a safe, scoped change for
this task. It is filed as a follow-up. No product code changed here.
Everything else held. §5 lists what looked like failures but traced to
the scripts or to design changes.

## 1. Commit and environment

- **Commit tested**: `d7dde0c2` (`Merge pull request #877 …`), the tip of
  `main` when the run began. The build stamp read
  `v0.0.0-20261108030308-d7dde0c23839 · d7dde0c · 2026-11-08 03:03Z`.
- **Sandbox**: Debian VM, Linux x86_64, 7 CPUs, 30 GB RAM; Go 1.26.10,
  Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`) from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `npm ci && npm run build` in `ui/` (into `pkg/ui/static`),
  `go build ./cmd/grain`, then
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file> -staging -host-mode-install -observability`
  over its own throwaway embedded store. A second, identical demo on
  `:18081` was started with the demo's own binary first on `PATH` (§4.9).
  Each browser context signed in with `POST /api/auth/access-code`, as
  `e2e/accesscode.js` does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no agents.
  The sandbox's file reader returns PNGs as bytes, so layout was judged
  from DOM geometry (bounding boxes, `elementFromPoint`, scroll sizes)
  rather than by eye. No UI code changed, so no screenshots were taken.
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository. Every step went
  through visible controls: clicks, taps, Tab, Enter, Escape, typing in
  the chat composer, browser Back and Forward, reloads, typed deep links
  and viewport resizes. Each context recorded console errors, page
  errors, failed requests and every 4xx/5xx response.

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

- **CI is hidden from the rail by default** (`9be44f8f`, "default-hide CI
  tab"). Customize sidebar lists it as "Show CI", off. Switching it on
  and pressing Save put CI in the rail. It opened `/ci` with
  `aria-current=page` and the crumb `ci`, and stayed in the rail after a
  reload.
- **The rail scrolls on a 900px-tall desktop.** With the install notice
  showing, the rail (`aside.sidebar`, `overflow-y: auto`) is 832px tall
  and its content 923px. The build stamp, at the rail's foot, sits at
  y=954–977 until the rail is scrolled. This is the design
  `responsive-navigation-walkthrough.spec.js` asserts (it scrolls the
  stamp into view). Scrolled, it was fully visible, and it opened
  `/settings/upgrade`.
- **Settings at 820px is a select**, not the section list
  (`settings-section-select`, `SettingsPage.jsx` "narrow"). All 20
  sections were walked through it.
- **System on a phone is a list of rows** (`role=button`), not a tab
  strip.
- **Teams**: no team, team switcher, Admin mode switch or "All teams"
  appeared on any load. `/t/default/tasks` opens `/`,
  `/t/default/sessions` opens `/sessions`, and
  `/t/default/settings/models` opens `/settings/models`.
  (`GET /api/console-sessions` still carries `"team": "default"` per
  session, which no screen shows.)
- **Signed-in email badge**: it draws only for a verified identity, which
  the demo does not have. `iap-header-logout.spec.js` and
  `phone-signed-in-logout.spec.js` cover it.
- **Sessions are task-backed**: sends go to `POST /api/tasks/<id>/comments`,
  and `GET /api/console-sessions/<id>` is 405. The list is the read.
- **Overlay "← Back"** closes the task detail by state, and the URL effect
  pushes `/`. So browser Back after it reopens the task, and Forward has
  nothing to go to. Browser Back from the detail itself returns to `/`,
  and Forward reopens it. That is the app's usual state-to-URL push and
  is recorded, not changed.
- **Rail Tasks count vs list count**: the rail reads "Tasks 18" and the
  list "16" (the default narrowing hides deferred and closed tasks), as
  on 2026-11-04.
- **`/api/repos/acme/widgets/github-branches` 404** on task detail: the
  demo has no GitHub. Allow-listed.

## 3. Stubs (live or external boundaries only)

The demo's `-staging` and `-host-mode-install` flags provided the
staging environments and the install notice. Everything else ran against
the real server, unstubbed. Each stub below is a Playwright `route` on
one context, removed once its step ended.

1. **Run in flight**: `GET /api/tasks`, `/api/tasks?*` and
   `/api/tasks/<id>` for a session's own task were answered with the real
   response, with that task changed to `state: "running"`,
   `activityBySetup: false` and an unfinished attempt 9. Conditional
   `If-*` headers were dropped from the forwarded request.
2. **Disconnection**: `route("**/api/**")` aborted every call with
   `internetdisconnected` until the disconnected banner showed, then was
   removed.
3. **Failed write**: every non-GET `/api/**` call was answered
   `500 {"error":"walk: injected failure"}` for one press of Approve on
   task 1, to raise the error snackbar.
4. **Failed send**: `POST /api/tasks/*/comments` (and the older
   `/api/console-sessions/*/messages`) were answered
   `500 {"error":"walk: injected send failure"}`, either at once (lone
   failure) or after a held promise was released (failure with a draft
   typed after it).
5. **Held send**: the same POSTs were held on a promise, then released
   and forwarded to the real server.
6. **Pending config change**: an interactive task's `GET` responses
   gained a `pendingConfigChange` (summary, description, branch, a 20-hex
   `headSha`, two files, one with a 119-character name, a diff and
   `validation.ok`). Approving would merge into a config repository the
   demo does not have, so only the card's layout and Reject's reason
   field were exercised.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

**Pages**: 51 deep links, discovered through the UI.

- **Main pages**: `/inbox`, `/`, `/tasks/1`, `/tasks/13`, `/new`,
  `/boards`, `/boards/board-1` … `board-4`, `/sessions`,
  `/session/director`, `/session/new`, `/director`, `/processes`, `/ci`,
  `/repos` and `/repos/acme/widgets`.
- **Settings**: `/settings` and its 20 sections, read from the Settings
  nav (`models` … `memory`).
- **System**: `/system` and all eight tabs, read from "System tabs",
  Metrics included.
- **Old team links**: three `/t/default/...` links.

**Widths**: Chromium at all seven viewports (357 loads); WebKit at
desktop, iPad portrait, phone, small phone and rotated phone (255 loads).

**Checks**: document/body horizontal overflow, any visible control
outside the viewport that is not inside a horizontally scrolling
container, team wording, and console/page errors, failed requests and
4xx/5xx responses.

**Outcome**: no layout finding in either engine. One WebKit small-phone
context logged seven unhandled "… due to access control checks"
rejections (`/api/whoami`, `/api/tasks/4`, `/api/restart-watch`, …), all
from the first `/inbox` load as the next `goto` cancelled its fetches.
That is how WebKit reports a fetch cancelled by navigation. It did not
recur on any of the other 254 WebKit loads.

### 4.2 Rail and header by real clicks (1440, 1024, 820, 1180; both engines)

- **Rail entries**: Tasks, Boards, Sessions, Processes, Repos and Inbox
  each opened their page with `aria-current=page`. The crumbs read
  `tasks`, `boards`, `sessions`, `processes`, `repositories` and `inbox`.
- **Header**: one row, 77px tall, at every width. At 1440 the globals
  (bell, System, Settings) sat at x=1288–1412, with the page actions
  ending before them. No header control fell outside the window.
- **Counts**: level with the title on every counted page (centre lines
  within 3px), including behind More at 1024 and 820.
- **More page actions** (1024, 820, 1180) on `/`, `/repos`, `/processes`,
  `/boards/board-1`, `/ci` and `/system/logs`:
  - It opened with `aria-expanded=true`, and the header kept its height.
  - Escape closed it with focus back on More, and a press outside closed
    it too.
  - By keyboard on `/repos` at 1024 it held Repo to add, Clear, Open and
    Add repo.
- **Repository tabs**: all seven, by click. The crumbs read
  `repositories / acme/widgets` and then `… / ci`, `… / merge queue`,
  `… / agents`, `… / branches`, `… / releases` and `… / advanced`. Back
  went to `/repos`, Forward to the last tab.
- **Settings**: the header's Settings button opened `/settings`. All 20
  sections, by click at 1440, 1024 and 1180 and through the select at
  820, showed `settings / <section>` with no overflow.
- **System**: the header's System button, then all 8 tabs:
  `system / health` through `system / power`, `system / metrics`
  included.

### 4.3 Rail shortcuts, staging, stamp, keyboard (desktop; both engines)

- **`[` shortcut**: it folded the rail ("Expand sidebar"), and the fold
  survived a reload. No collapsed rail control lacked an accessible name.
  Typing `a[b` into the task search left the rail folded and the field
  holding `a[b`. Clicking the toggle unfolded it ("Collapse sidebar").
  The same held at 1180.
- **Create options**: it listed New task, New session and New board.
  Escape closed it with focus back on it. By keyboard, Enter focused New
  task, ArrowDown moved to New session, and Enter opened `/session/new`.
- **State views**: Proposed 2, Waiting 2, Running 2, Asked 9, Merging 1,
  Deferred 1, Closed 1 and Blocked 1. Blocked opened
  `/?state=blocked&f=state:none:deferred|closed` with
  `aria-current=page`.
- **Staging**: `docs-preview` → `/staging/docs-preview/` and
  `grain-staging` → `http://127.0.0.1:4280/`. No "Host port not reachable
  through this proxy" marker drew, which is correct for a loopback origin.
- **Keyboard only**: 15 Tabs from the top of `/inbox` reached Blocked.
  Enter opened its view, Back returned to `/inbox` and Forward to the
  view.
- **Customize sidebar**: every switch and field has an accessible name
  ("Show Pinned" … "Show Staging", "Type", "Item", "Icon (optional)").
  For CI, see §2.

### 4.4 Phone bar, drawer and alerts (390 and 320; both engines)

- **Bar**: Open navigation, the bell, Settings and New task sat at x=12,
  242, 290 and 338 at 390, and at x=12, 172, 220 and 268 at 320. The
  grain mark is the bar's `h1`, and the page heading ("Inbox") sits below
  it (y=201 at 390).
- **Alerts panel**: x=16–374 of 390 and 16–304 of 320. Escape closed it
  with focus back on the bell.
- **Drawer**: a dialog named "Navigation", with body `overflow: hidden`.
  A wheel over it left the window's scroll at 0. System was at y=709 and
  the stamp at y=807–830 of 844. At 568 they were at y=433 and 531–554.
- **Staying open**: a tap on Pin docs-preview (in the Staging group)
  toggled it to Unpin and left the drawer open. Customize sidebar opened
  its dialog, and Escape returned to the drawer with focus on Customize
  sidebar. The create menu opened over the drawer (New task, New session,
  New board), and Escape left the drawer open.
- **Navigation**: tapping Processes navigated to `/processes` and closed
  the drawer. Escape on an open drawer closed it with focus on Open
  navigation. New session from the drawer's create menu closed it.
- **System**: the drawer's System row opened `/system`. All eight rows
  were tapped, each opening `/system/<tab>` with no overflow. Back from
  Metrics returned to `/system`, and the bar's Settings opened
  `/settings`.
- **Settings and repository tabs**: all 20 Settings rows and all seven
  repository tabs were tapped. The selected tab stayed inside the strip,
  with no overflow.
- **Rotation**: on `/`, `/session/director`, `/repos/acme/widgets` and
  `/system/metrics`, 390×844 → 844×390 → back and 320×568 → 568×320 →
  back, with no overflow.

### 4.5 Banners and the snackbar (1440, 820, 390, 320; both engines)

| Width | Disconnected banner | Install notice link |
| ----- | ------------------- | ------------------- |
| 1440  | y=6–42              | y=64–100            |
| 820   | y=6–62              | y=105–119           |
| 390   | y=6–102             | y=204–220           |
| 320   | y=6–122             | y=245–259           |

- The banner showed 4.9–5.4s after stub 2 began, and cleared 2.8–3.5s
  after it was removed. Neither caused horizontal overflow, and they
  stacked without overlapping.
- Stub 3 raised "walk: injected failure" inside the viewport: at
  x=674–800, y=834–870 at 1440; y=1114–1150 at 820; x=149–275, y=794–830
  at 390. In each case `elementFromPoint` at its centre was inside the
  snackbar.

### 4.6 Sessions

This ran in Chromium at 1440×900 and 390×844, and in WebKit at 820×1180
and 320×568.

1. **New session** from Create options (the drawer's on a phone):
   `/session/new` opened with the Message composer focused and the crumb
   `sessions / new chat`. Session name, Model, Pin to sidebar and Create
   session showed with no overflow. The Model options were "Deployment
   default (gemini-2.5-pro)", "gemini-2.5-pro" and "gemini-2.5-flash".
   "+ New session" on `/sessions` also opened `/session/new` with the
   composer focused.
2. **First message**: a name was typed, gemini-2.5-flash picked, Pin
   ticked, and a message sent holding `**hello**`, two Shift+Enter breaks
   and a table with a 140-character cell.
   - The URL became `/session/<slug>` and the crumb
     `sessions / <name>`.
   - `<strong>` and `<table>` rendered with no overflow.
   - The pinned rail entry (the drawer's on a phone) was
     `aria-current=page`.
   - The server held the typed title, and `model: gemini-2.5-flash` on
     the session's task.
3. **Back-to-back sends** (stub 5): "queued one" and "queued two" showed
   at once as "Sending…" bubbles, in order. One POST was in flight and
   the second queued behind it. After release both settled, with no
   "Sending…" left.
4. **Lone failed send** (stub 4): "Message not sent. It is back in the
   message box.", with "will fail" back in the box.
5. **Failure with a draft**: the box kept "draft kept" and the failed
   bubble offered Retry. Retry after the stub was removed delivered it.
   Same in WebKit.
6. **Hard reload** kept the URL and the messages. The session's task
   held exactly one copy each of "queued one", "queued two" and "fail
   with draft", and none of "will fail".
7. **Nested scrolling**: a wheel over the chat left window `scrollY` at
   0, with the document no taller than the viewport.
8. **Switching**: `/director` opened `/session/director`, and `/sessions`
   listed every session with no overflow.
9. **Delete session**:
   - The confirm read `Delete session "<name>"? This removes the session
from this deployment.`
   - Declining kept the URL, with focus back on Delete session.
   - Accepting went to `/session`.
   - The deep link then showed no composer, and said there is no such
     session.
   - `GET /api/console-sessions` no longer listed it.
10. **Director work tree**: "provenance · work from task 13: 0 of 4 done"
    expanded to links for `/tasks/1`, `/tasks/3`, `/tasks/8` and
    `/tasks/11`, with no overflow at 1440 or 320.

**Live controls** (a session made with `POST /api/console-sessions`), in
Chromium at 1440 and 390 and WebKit at 820 and 320:

- **No run open**: the panel read "No run is open on this session, so a
  change saved here takes effect on the next message you send.". "What
  it may reach on the internet" (a native select) offered "egress
  (outbound internet only)" and "none (this deployment only)". Choosing
  none saved `networkAccess: none` and survived a reload.
- **Model**: choosing gemini-2.5-flash saved it on the session and its
  task.
- **Run in flight** (stub 1): the panel read "A run is open on this
  session now…".
  - Removing any grant failed (§4.9).
  - Removing the unattached config-map grant first worked, and raised
    "The run open on this session started before your change…" with
    Stop and Apply now.
  - Removing `api:capabilities:detach` after that also raised it.
  - Apply now sent `POST /api/console-sessions/<slug>/cancel` (200).
- **Start a fresh chat**: it offered "Close this chat and start a fresh
  one" and "Start fresh with summary". Each kept `/session/<slug>` and
  moved the session to a new task (e.g. 20 → 21 → 22).

**Interactive task chat** (filed with `POST /api/tasks`,
`interactive: true`), at 1440, 820, 390 and 320 in both engines:

- Pause turned both Pause buttons into Resume. That survived a reload
  (the server's `paused` record carried `by: "operator (access code)"`),
  and Resume turned them back.
- The composer stayed inside the viewport: y=775–795 of 900 and 737–777
  of 844.
- **Config-change card** (stub 6): Approve & Apply was inside the
  viewport (x=277–410 at 1440 and 820, x=33–166 at 390 and 320), with no
  overflow from the long file name. Reject opened "Why are you rejecting
  it?" with Reject change and Cancel.

### 4.7 Long names, discard prompt, overlays

- **Long names**: a task with a 197-character title, a 300-character
  table cell, a 420-character code line and a 232-character unbroken
  word was opened from its list row at 1440, 820, 390 and 320. There was
  no overflow or offscreen control at `/tasks/<id>`, after a reload, or
  in the list. The detail's "← Back" sat at x=254 (1440) and x=26
  (phones). Escape closed the overlay to `/`.
- **Discard prompt**: from `/processes`, the Weekly Dependency &
  Vulnerability Audit row opened its pane, and Title was edited. Escape,
  then browser Back, each prompted "You have unsaved changes here. Leave
  and discard them?". Declining kept the URL and the edit, with focus
  back in Title both times. Accepting returned to `/processes`. Same in
  both engines and at all four widths.

### 4.8 Themes and motion (both engines; 1440, 820, 390, 320)

`colorScheme` dark and light with `reducedMotion: reduce`, on `/inbox`,
`/session/director`, `/processes`, `/system/metrics`, `/` and `/ci`. The
body was `rgb(15, 16, 19)` in dark and `rgb(247, 246, 243)` in light. No
infinite animation was running and there was no overflow.

### 4.9 Fault: a demo session's grants cannot be changed (follow-up)

**Reproduction** (deterministic, Chromium and WebKit, every width):

1. Start `grain demo` (any flags) and open `/session/<any session>`,
   `director` included, or one made with `POST /api/console-sessions`.
2. Open "Edit <title> capabilities". The grants list shows "config-map ›
   config-map · config_maps: bootstrap", marked "server not attached".
3. Press Remove on any other grant, e.g. `api:capabilities:detach`.

**Observed**: `PUT /api/console-sessions/<id>` answers 400
`{"error":"capability https://grain.dev/capabilities/config-map is not configured in mcpServers"}`,
and the snackbar repeats it. Nothing is saved, and the next-run notice
never shows. Removing the config-map grant itself succeeds, and other
edits then work.

**Cause**:

- `UpdateConsoleSession` (`pkg/ui/console_sessions.go`) re-checks every
  listed capability the session's open task does not already hold.
  `catalogueEditRefusal` then reaches `servers.Registry.Lookup`.
- In the demo no task holds any grants, because no orchestrator places
  them. And `configureCapabilityServerDemo`
  (`pkg/demo/demo_capability_server.go`) hands the registry only the
  entries the demo writes itself: the demo cloud server, gcp, gchat and
  figma.
- The built-in config-map entry that `intree.EnsureBuiltins` adds for
  Settings never reaches the registry, so Settings lists it as
  "pending". A real daemon ensures built-ins before building its
  registry (`registerBuiltinCapabilityServers`).
- Starting the demo with its own binary first on `PATH` made no
  difference.

**Expected**: on the demo, as on a daemon, a session's grants can be
edited, with the next-run notice when a run is open.

**Why not fixed here**:

- Registering built-ins in the demo would either spawn
  `grain capability <name>` subprocesses from the demo, or serve
  config-map in-process with schema and configure behaviour that
  `capability-server-settings.spec.js` exercises.
- Narrowing the re-check to grants new to the session's own list would
  loosen a deliberate check against grants removed concurrently.
- Both are materially separate from a UI walkthrough. A follow-up task
  carries this reproduction.

## 5. Script failures that were not product bugs

- **"Header clipped"**: controls wrapped behind More, which
  `.page-header-slot`'s `overflow: hidden` clips by design. More's menu
  held every one of them. For the same reason, the wrapped actions row on
  `/repos` at 820 measured under the globals.
- **Rail CI**: not in the rail by default (§2).
- **Stamp below the fold** at 1440×900: inside the scrolling rail (§2).
- **`/settings` at 820 had no section list**: it is a select there (§2).
- **Pause "dialog"**: the task detail is itself a dialog. Pause applies at
  once, with no confirmation to press.
- **"Reject change"** is the confirm inside the reason form. The card's
  own button is "Reject".
- **The first sessions run** read `GET /api/console-sessions/<id>`
  (405) and routed the old messages path. Sessions are task-backed (§2).
- **The sandbox's own `grain` CLI was first on `PATH`** for the first
  demo. A second demo with the demo's binary first on `PATH` showed the
  same §4.9 fault, so the fault is not this environment's.

## 6. Tests run

- No product code changed, so no unit or e2e suite was rerun beyond the
  walkthrough itself.
- The walkthrough scripts and logs stayed outside the repository, and no
  screenshots or browser artifacts are committed.
