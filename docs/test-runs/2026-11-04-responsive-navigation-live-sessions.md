# Responsive navigation & live-session UI walkthrough — 2026-11-04

Task 2966. This is a real-browser walkthrough of the shared shell and the
live session surfaces at desktop, laptop, tablet and phone widths, in
Chromium and WebKit, against a real `grain demo` server. It repeats the
2026-11-03 walkthrough on a tree 395 commits newer, from fresh scripts.

**One product bug was found and fixed here (§4.3):** when a page's header
row did not fit the rail, its count sat about 10px above the title's
centre line. The fix is a CSS strut in `.page-header-slot`, with new
760px cases in `ui/e2e/page-header-rail-walkthrough.spec.js`. Everything
else held. §5 lists what looked like failures but traced to the scripts.

## 1. Commit and environment

- **Commit tested**: `b725bf46` (`Merge pull request #704 …`), the tip of
  `main` when the run began. That tip did not compile (§2), so the run
  moved to `be8390ba` (`Merge pull request #702 …`), where `main` fixed
  it, before the fix in this branch. Everything after §4.2 ran on
  `be8390ba` plus this branch's fix.
- **Sandbox**: Debian VM, Linux x86_64, 7 CPUs, 30 GB RAM; Go 1.26.10,
  Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`) from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
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
| narrow window | 760×900, 700×1000 | — (fault hunt, §4.3) |
| iPad portrait | 820×1180 | touch |
| iPad landscape | 1180×820 | touch |
| phone | 390×844 | touch; `isMobile` in Chromium |
| small phone | 320×568 | touch; `isMobile` in Chromium |
| rotated phone | 844×390 | touch; `isMobile` in Chromium |

## 2. Scope notes: where the brief and the tree differ

These are recorded, not changed.

- **`main` did not build at `b725bf46`**: `pkg/cli/main.go` had a
  duplicate `case "login"` (compile error), and `go` and `installer` were
  red on `main` itself. For the first part of the walkthrough the binary
  was built with that alias removed locally, uncommitted. `main` fixed it
  in `e594652a` (`fix(cli): drop the duplicate "login" alias …`), and
  this branch was fast-forwarded to `be8390ba`.
- **Teams**: no team, team switcher, Admin mode switch or "All teams"
  appeared on any load. `/t/default/tasks` opens `/`,
  `/t/default/sessions` opens `/sessions`, and
  `/t/default/settings/models` opens `/settings/models`.
- **Signed-in email badge**: it draws only for a verified (IAP) identity,
  which the demo does not have. `iap-header-logout.spec.js` and
  `phone-signed-in-logout.spec.js` cover it.
- **Create options lives in the page header on desktop**, beside
  "+ New task", not in the rail. On a phone it is in the drawer.
- **Rail Tasks count vs list count**: the rail reads "Tasks 18" and the
  list's own count "16". The list's default narrowing leaves out deferred
  and closed tasks (`f=state:none:deferred|closed`).
- **Failed sends**, as on 2026-11-03: a lone failed send goes back into
  the box. A failure that lands after the next keystroke stays in the
  transcript with Retry. In WebKit at 820 and 320 the injected 500 always
  landed first, so "fail with draft" went back into the box ahead of
  "draft kept" (`TaskChat.jsx`, grain/task-1360).
- **Fresh chat seed**: after Start a fresh chat, a user-created session's
  first message is the "You are this deployment's director…" prompt.
  That is `model.DirectorSessionBody`, which every standing console
  session uses. It is not a mix-up.
- **The phone bar's grain mark is an `h1`**, and each page heading is an
  `h1` or `h2` below the bar: bar bottom y=185 at 390×844 with the
  install notice showing, headings at y=197–255. `/system/metrics` has
  both an `h1` and an `h2` reading "Metrics".
- **`/api/repos/acme/widgets/github-branches` 404** on every task detail
  load: the demo has no GitHub. Allow-listed, as in earlier runs.

## 3. Stubs (live or external boundaries only)

The demo's `-staging` and `-host-mode-install` flags provided the
staging environments and the install notice. Everything else ran against
the real server, unstubbed. Each stub below is a Playwright `route` on
one context, removed once its step ended.

1. **Run in flight**: `GET /api/tasks`, `/api/tasks?*` and
   `/api/tasks/<id>` for a session's own task were answered with the real
   response, with that task changed to `state: "running"`,
   `activityBySetup: false`, and an unfinished attempt 9. Conditional
   `If-*` headers were dropped from the forwarded request (WebKit 304s).
2. **Disconnection**: `route("**/api/**")` aborted every call with
   `internetdisconnected` until the disconnected banner showed, then was
   removed.
3. **Failed write**: every non-GET `/api/**` call was answered
   `500 {"error":"walk: injected failure"}` for one press of Approve on
   task 1, to raise the error snackbar.
4. **Failed send**: `POST /api/console-sessions/*/messages` and
   `POST /api/tasks/*/comments` were answered
   `500 {"error":"walk: injected send failure"}` while failed sends were
   exercised, then removed before Retry.
5. **Held send**: the same POSTs were held on a promise, then released
   and forwarded to the real server.
6. **Pending config change**: an interactive task's `GET` responses
   gained a `pendingConfigChange` (summary, two files, one with a
   100-character name, a diff and `validation.ok`). Approving would merge
   into a config repository the demo does not have, so only the card's
   layout and Reject change's reason field were exercised.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

**Pages**: 54 deep links.

- **Main pages**: `/inbox`, `/`, `/tasks/1`, `/tasks/13`, `/new`,
  `/boards`, `/boards/board-1` … `board-4`, `/sessions`,
  `/session/director`, `/session/new`, `/director`, `/processes`, `/ci`,
  `/repos` and `/repos/acme/widgets`.
- **Settings**: `/settings` and 19 section ids.
- **System**: `/system` and all eight tabs, Metrics included.
- **Old team links**: three `/t/default/...` links.

**Widths**: Chromium at all seven fixed viewports in §1; WebKit at
desktop, iPad portrait, phone, small phone and rotated phone.

**Checks**: document/body horizontal overflow, any visible control
outside the viewport that is not inside a horizontally scrolling
container, team wording, and console/page errors, failed requests and
4xx/5xx responses.

**Outcome**: clean in both engines (343 and 189 loads) apart from the
allow-listed `github-branches` 404. `/tasks/13` and `/director` open
`/session/director`.

### 4.2 Rail and header by real clicks (Chromium 1440, 1024, 820)

- **Rail entries**: Tasks, Boards, Sessions, Processes, Repos, CI and
  Inbox each opened their page with `aria-current=page`. The crumbs read
  `tasks`, `boards`, `sessions`, `processes`, `repositories`, `ci` and
  `inbox`. The counts read 16, 4, 1, 9, 1, (none) and 14. The rail's
  accessible names are "Tasks18", "Boards4" and so on.
- **Header**: one row, 77px tall, at every width. The globals ended
  inside the window (1156–1412 of 1440, 740–996 of 1024, 536–804 of 820),
  with the page actions ending before them.
- **Repository tabs**: all seven, by click. The crumbs read
  `repositories/acme/widgets/ci`, `…/merge queue`, `…/agents`,
  `…/branches`, `…/releases` and `…/advanced`. Back went to `/repos`,
  Forward to the last tab.
- **Settings**: from the header's Settings button (`aria-pressed=true`),
  all 19 sections in "Settings sections" by click, `settings/models`
  through `settings/memory`, with no overflow.
- **System**: all 8 tabs, `system/health` through `system/power`,
  `system/metrics` included.
- **More page actions** (1024 and 820) on `/`, `/repos`, `/processes`,
  `/sessions`, `/system/logs`, `/ci` and `/boards/board-1`:
  - It opened with `aria-expanded=true`, and the header kept its height.
  - Escape closed it with focus back on More, and a press outside closed
    it too.
- **Crumbs**: `/tasks/1` read `task 1` and `/new` read `new task`.

### 4.3 Bug: the count sat ~10px above the title behind "more" (fixed)

**Reproduction** (deterministic, Chromium and WebKit):

1. Open `/repos`, `/inbox` or `/boards/board-1` at 820×1180 over this
   demo, or `/`, `/inbox`, `/repos`, `/sessions` or `/processes` at
   760×900 over the e2e fixture.
2. Measure the rail's first `.count` against
   `[data-testid=page-header-title]`.

**Observed**: the title at y=93–119 and the count at 86–105 (on a board,
88–108). The centre lines are 10.4px apart. At 1440, where nothing wraps,
they were level.

**Cause**: `.page-header-slot` is a 44px, `flex-wrap: wrap`,
`overflow: hidden` row with `align-content: stretch`. Once the page's
actions wrap behind "more", there is no free space to stretch into. The
lines pack from the top, and a first line holding only the 19px count is
19px tall at the top of the slot. Its own comment noted this case.

**Fix** (`ui/src/style.css`): a `::before` strut on `.page-header-slot`.
It is zero-width, `height: 100%`, and has a negative right margin equal
to the column gap. It opens the first line, so that line is never
shorter than the slot's content box. In the opened menu the slot's height
is auto, so the strut is 0px tall.

**After**: count 96–116 against title 93–119 (level) on all three pages
at 820 and 1024, and 76–96 against 73–99 at 1440. The full header spec
passes (32 tests).

**Regression coverage**: `centres <page>'s count on its label behind
more` in `page-header-rail-walkthrough.spec.js`, at 760×900 for Tasks,
Inbox, Repos, Sessions and Processes. All five fail on the pre-fix CSS
("count "16" (rail) is -10.4px off its label "tasks"") and pass with the
fix.

### 4.4 Phone bar, drawer and alerts (390 and 320, both engines)

- **Bar**: Open navigation, bell, Settings and New task are all 40×40 and
  inside the width. At 390 they sit at x=12, 242, 290 and 338; at 320 at
  x=12, 172, 220 and 268.
- **Alerts panel**: x=16–374 of 390 and 16–304 of 320. Escape closed it
  with focus back on the bell.
- **Drawer**: a dialog named "Navigation", with body `overflow: hidden`.
  System was at y=709 and the stamp at y=807–830 of 844. At 568 they were
  at y=433 and 531–554.
- **Staying open**: a tap on Pin docs-preview toggled it to Unpin and
  left the drawer open. Customize sidebar opened its dialog, and Escape
  returned to the drawer with focus on Customize. The create menu (New
  task, New session, New board) opened over the drawer without closing it.
- **Navigation**: tapping Processes navigated and closed the drawer.
  Escape on an open drawer closed it with focus on Open navigation.
- **System**: the drawer's System row opened `/system`. Metrics opened
  `/system/metrics`, Back returned to `/system`, and the bar's Settings
  opened `/settings`.
- **Repository tabs**: tapping all seven kept the selected tab inside the
  strip.
- **Rotation**: 390×844 → 844×390 → back, and 320×568 → 568×320 → back,
  with no overflow.

### 4.5 Rail shortcuts, staging, stamp, keyboard (desktop, both engines)

- **`[` shortcut**: it folded the rail from 232px to 60px ("Expand
  sidebar"), and the fold survived a reload. No collapsed entry lacked an
  accessible name. Typing `a[b` into the task search left the rail
  folded and the field holding `a[b`. Clicking the toggle unfolded it
  ("Collapse sidebar").
- **Create options**: it listed New task, New session and New board.
  Escape closed it with focus back on it.
- **State views**: Proposed 2, Waiting 2, Running 2, Asked 9, Merging 1,
  Deferred 1, Closed 1 and Blocked 1.
- **Staging**: `docs-preview` → `/staging/docs-preview/` and
  `grain-staging` → `http://127.0.0.1:4280/`. No "Host port not reachable
  through this proxy" marker drew, which is correct for a loopback origin.
- **Build stamp** (`v0.0.0-20261104133915-37dd9c3f90b9 · 37dd9c3 · …`):
  fully on screen at y=863–886 of 900. It opened `/settings/upgrade`.
- **Keyboard only**: 13 Tabs from the top of `/inbox` reached Blocked.
  Enter opened `/?state=blocked&f=state:none:deferred|closed` with
  `aria-current=page`. Back returned to `/inbox` and Forward to the view.

### 4.6 Banners and the snackbar (1440, 820, 390; both engines)

| Width | Disconnected banner | Install notice |
|---|---|---|
| 1440 | y=0–48 | y=48–96 |
| 820 | y=0–68 | y=68–136 |
| 390 | y=0–108 | y=108–236 |

- The disconnected banner showed about 4.5–5s after stub 2 began.
- The banners caused no horizontal overflow.
- Once stub 2 was removed, the banner cleared in 0.5–3s.
- Stub 3 raised "walk: injected failure" inside the viewport. At 1440 it
  was at x=624–816, y=828–876; at 820 at y=1108–1156; and at 390 at
  x=8–382, y=788–836. In each case `elementFromPoint` at its centre was
  inside it.

### 4.7 Sessions

The flow below ran in Chromium at 1440×900 and 390×844, and in WebKit at
820×1180 and 320×568.

1. **New session** from Create options (the drawer's on a phone):
   `/session/new` opened with the Message composer focused and the crumb
   `sessions/new chat`. Session name, Model and Pin to sidebar were
   visible, with no overflow. The Model options were "Deployment default
   (gemini-2.5-pro)", "gemini-2.5-pro" and "gemini-2.5-flash".
2. **First message**: a name was typed, gemini-2.5-flash picked, Pin
   ticked, and a message sent holding `**hello**`, two Shift+Enter breaks
   and a table with a 140-character cell.
   - The URL became `/session/<slug>` and the crumb `sessions/<name>`.
   - `<strong>` and `<table>` rendered with no overflow.
   - The pinned rail entry was `aria-current=page`.
   - The server held the typed title and `model: gemini-2.5-flash`.
3. **Back-to-back sends** (stub 5): "queued one" and "queued two" showed
   at once as "Sending…" bubbles, in order. After release they settled
   with timestamps, in order.
4. **Lone failed send** (stub 4): "Message not sent. It is back in the
   message box.", with the text back in the box.
5. **Failure with a draft** (Chromium): one Retry, and the box kept "draft
   kept". Retry after the stub was removed delivered it. WebKit hit the
   lone case instead (§2).
6. **Hard reload** kept the URL, the crumb and the messages. The server
   held exactly one copy each of "queued one", "queued two" and the
   retried message, and none of the message returned to the box.
7. **Run in flight** (stub 1): the capabilities panel read "A run is open
   on this session now…". Toggling a capability raised "The run open on
   this session started before your change and does not have it…".
   Apply now sent `POST /api/console-sessions/<slug>/cancel` (200).
8. **No run open**: the panel read "No run is open on this session, so a
   change saved here takes effect on the next message you send.".
   "What it may reach on the internet" offered "egress (outbound internet
   only)" and "none (this deployment only)". Choosing none survived a
   reload (`networkAccess: none` on the server).
9. **Start a fresh chat**: "Start fresh with summary", then "Close this
   chat and start a fresh one", each reopened the same `/session/<slug>`
   with only the seed prompt and greeting.
10. **Switching**: `/director` opened `/session/director`. Rail Sessions
    opened `/sessions`, and "Open session Director" opened the Director.
    The pinned entry then lost `aria-current`, and tapping it returned to
    `/session/<slug>` with `aria-current=page`.
11. **New session from the Sessions page**: "+ New session" opened
    `/session/new` with the composer focused. At 820 it was behind More.
12. **Delete session**:
    - The confirm read `Delete session "<name>"? This removes the session
      from this deployment.`
    - Declining kept the URL, with focus on Delete session.
    - Accepting went to `/session`.
    - The deep link then listed the sessions under "There is no session
      “<slug>” here. It may have been deleted; pick another below.", with
      no composer.
    - A session deleted through the API dropped its pinned entry on the
      next reload.
13. **Director work tree**: "Provenance · Work from task 13: 0 of 4 done"
    expanded to tasks 13, 1, 3, 8 and 11, linking `/tasks/1`, `/tasks/3`,
    `/tasks/8`, `/tasks/11` and the PR. There was no overflow at 1440 or
    320.
14. **Nested scrolling**: on `/session/director` a wheel or scroll left
    window `scrollY` at 0, with the document exactly the viewport's
    height (900 and 568).

**Interactive task chat** (filed with `POST /api/tasks`,
`interactive: true`), at 1440, 820, 390 and 320 in both engines:

- Pause turned both Pause buttons into Resume. That survived a reload
  (the server's `paused` record set), and Resume turned them back.
- The composer stayed inside the viewport: y=775 of 900, 737 of 844, 768
  of 1180 and 461 of 568.
- **Config-change card** (stub 6): Approve & Apply was inside the
  viewport (x=33–166 at 390 and 320), with no overflow from the long file
  name. Reject change opened "Why are you rejecting it?".

### 4.8 Long names, discard prompt, themes and motion

- **Long names**: a task with a 200-character title, a 300-character
  table cell and a 400-character code line showed no overflow or
  offscreen control at `/tasks/<id>` or `/`, at 1440, 820, 390 and 320.
- **Discard prompt**: from `/processes`, the Weekly Dependency &
  Vulnerability Audit row opened `/processes/schedules/sched-2`, and
  Title was edited. Escape, then browser Back, each prompted "You have
  unsaved changes here. Leave and discard them?". Declining kept the URL
  and the edit, with focus back in Title both times. The pane's "←
  Schedules" control, accepted, returned to `/processes`. Same result in
  both engines and at all four widths.
- **Themes and motion**: `colorScheme` dark and light with
  `reducedMotion: reduce`, on `/inbox`, `/session/director`,
  `/processes`, `/system/metrics`, `/` and `/ci`. The body was
  `rgb(15, 16, 19)` in dark and `rgb(247, 246, 243)` in light. No
  infinite animation was running and there was no overflow, in both
  engines at all four widths.

### 4.9 Screenshots

`ui/e2e/screenshot.mjs --url` shot `/`, `/repos`, `/inbox`, `/sessions`
and `/session/director` at desktop, tablet and phone, in light and dark.
It ran against this branch's build, and against a pre-fix build on a
second port. A before/after crop of the header at 760 and 820 was taken
for `/`, `/repos` and `/inbox`. None of these are committed. They are
attached to the task's closing note.

## 5. Script failures that were not product bugs

- **More on a board at 1440**: one measurement caught More mid-navigation
  on `/boards/board-1`. Neither a direct load nor 40 samples over 2s found
  it again.
- **Pinned at start**: one aborted phone run saw docs-preview already
  pinned in a fresh context. Three later runs found it unpinned, as
  `DEFAULT_PINNED_ITEMS` says. It was not reproduced.
- **Two "Unpin docs-preview" buttons**: once pinned, the item shows in
  both the pinned list and Staging. Scope locators to
  `[data-testid=staging-group]`.
- **Native select on phones**: "What it may reach on the internet" is a
  native `<select>` on touch, so use `selectOption`.
- **Tap places the caret**: tapping Title put the caret mid-text, so
  " edited" landed mid-title and an `endsWith` check misreported the edit
  as lost.
- **"cannot be reached"**: the disconnected banner says "This deployment
  cannot be reached", which a `/cannot reach/` regex misses.
- **e2e fixture widths**: in the e2e fixture, `/repos` also has a search
  box, which keeps the first line tall at 820. The regression cases use
  760, where every counted page reproduces.

## 6. Tests run

- `npx playwright test e2e/page-header-rail-walkthrough.spec.js`: 32
  passed with the fix. The five new cases failed against a build of the
  pre-fix CSS.
- `prettier --check` on the changed files: clean.
- The walkthrough scripts and logs stayed outside the repository, and no
  screenshots or browser artifacts are committed.
