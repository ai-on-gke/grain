# Responsive navigation & live-session UI walkthrough — 2026-11-01

Task 2240. A real-browser walkthrough of the shared shell and the live
session surfaces, at desktop, tablet and phone widths, in Chromium and
WebKit, over a real `grain demo` server. The 49-page matrix was clean in
both engines (490 loads), and so were the rail, drawer, banners, header
rail, sessions, task chat and discard-prompt walks. **No bugs were
found**, so this run changes no code. Four things that looked like bugs
at first turned out to be test-script timing or the tree's design. They
are written up in §5 so the next run does not chase them again.

## 1. Commit and environment

- **Commit tested**: `4f71b139` (`Merge pull request #347 from
  ai-on-gke/grain/049f1e09/task-2233`), the `main` head this branch
  started from.
- **Sandbox**: Debian VM, Linux x86_64; Go 1.26.10, Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`), from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**:
  - Built with `npm ci && npm run build` in `ui/` (into
    `pkg/ui/static`), then `go build ./cmd/grain`.
  - Started as `grain demo -addr 127.0.0.1:18080 -open=false
    -access-code-file <file>`, over its own throwaway embedded store.
  - Each browser context signed in with `POST /api/auth/access-code`,
    as the e2e fixture does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no
  agents, so nothing else needed it.
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository
  (`/home/debian/walk`: `matrix.mjs`, `shell.mjs`, `sessions.mjs`,
  `chat.mjs`, `restart.mjs`, plus small probes). Every step went
  through visible controls: clicks, taps, Tab, Enter, Escape, browser
  Back and Forward, reloads and typed deep links.

## 2. Scope notes: where the brief and the tree differ

These match the 2026-10-29 report and are still true at `4f71b139`.

- **Teams**: there is no team, team switcher, user tile, Admin mode
  switch or "All teams" anywhere. The matrix checked every page's text
  at every width and found none. `/t/default/tasks` opens the task list
  and `/t/default/settings/models` opens `/settings/models`.
- **Phone bar**: the bar holds Open navigation, the `grain` title,
  alerts bell, Settings and New task (390×844: x=4, 84, 247, 296, 346).
  The page's own title is the `h2` right under the bar ("Processes" at
  y=75). That is `PhoneNav.jsx`'s design.
- **Rail Tasks breakdown**: the rail lists only the state groups that
  have tasks: proposed, waiting, running, asked, merging, deferred,
  closed and blocked. Nothing is merged in the demo seed, so no Merged
  row was drawn.
- **Sessions in the rail**: a session gets a rail entry of its own only
  when it is pinned (`Sidebar.jsx`, the pinned `session` kind). The
  Sessions entry carries the count.
- **Interactive tasks** are kept out of the task list
  (`isSessionTask`), so the task chat was opened by deep link
  (`/tasks/24`).
- **Terminal**: there is no xterm terminal. A task-bound interactive
  console is a chat (`TaskChat`), so the window was resized around an
  open task chat instead (§4.6).
- **Restart** is a session's **Start a fresh chat**, with "close this
  chat and start a fresh one" and "start fresh with summary". Pause and
  Resume belong to an interactive task's chat.
- **Customize on a phone** closes the drawer and opens the
  full-screen Customize overlay, which `App.jsx` owns (see its comment
  at the `showCustomizeSidebar` state). Escape closes the overlay and
  hands focus back to Open navigation.

## 3. Stubs (live or external boundaries only)

Everything else ran against the real server, unstubbed: navigation,
routing, React rendering, layout, and every mutation and its read-back.
Each stub is a Playwright `route` on that browser context only.

1. **Persistent sandboxes** (`GET /api/persistent-sandboxes`). The demo
   runs no persistent-sandbox supervisor. The stub lists three:
   - `web-preview`: running, on host port 8081;
   - `docs-site-with-a-rather-long-name-for-wrapping`: restarting, 4
     restarts, `lastError: exit 1`, on a `/staging/…` sub-path;
   - `switched-off`: disabled.
2. **Config flags** (`GET /api/config`: the real response, with fields
   added):
   - `proxied: true`, for the "not reachable through this proxy" mark;
   - `installNotice`, the host-mode install notice, which only a host
     install raises.
3. **Disconnection**: `route("**/api/**")` aborting with
   `internetdisconnected` for 9–12 s, then removed.
4. **Failed write**: `POST /api/tasks/4/pause` answering 500
   (`walk: pause refused`), to raise the error snackbar.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

**Viewports**:

- 1440×900 (desktop);
- 1180×820 (iPad landscape);
- 820×1180 (iPad portrait);
- 390×844 and 320×568 (phones, touch contexts, `isMobile` in
  Chromium).

**Pages (49)**:

- **Main pages**: `/inbox`, `/tasks`, `/tasks/1` (pane), `/new`,
  `/sessions`, `/session/director`, `/director`, `/processes`, `/ci`,
  `/repos`.
- **Boards**: `/boards/board-1` … `board-4` (Kanban, timeline, report,
  tree).
- **Repository**: `/repos/acme/widgets` and its six `/settings/<tab>`
  tabs (CI, merge queue, agents, branches, releases, advanced).
- **Settings**: all 18 `/settings/<section>` ids.
- **System**: all 8 `/system/<tab>` tabs, Metrics included.
- **Old team links**: two `/t/default/...` links.

**Checks on every load**:

- document and body `scrollWidth` against `clientWidth`;
- every visible button, link, input, select and tab lies inside the
  viewport, unless it sits in a horizontally scrolling container;
- on desktop and tablet, the alerts bell, System and Settings are
  present, visible, on one row, and not clipped at the right;
- the build stamp lies inside the viewport;
- no team or admin wording;
- console errors, page errors, failed requests and 4xx/5xx responses.

**Outcome**: 245 loads per engine, and the only flags were expected
ones:

- `/tasks/1` and `/new` at the three wide sizes report "no globals,
  no stamp". Both are full-screen modal panes, and the shell behind
  them is `aria-hidden`, so this is by design.
- `/tasks/1` at every size logs the known
  `GET /api/repos/acme/widgets/github-branches` 404 (a demo without
  `-github-sim`).

There was no overflow, no off-screen control, no missing or clipped
global and no clipped stamp, in either engine.

### 4.2 Desktop rail (both engines, 1440×900, stubs 1–2)

- **Create menu**: lists New task, New session and New board. Escape
  closes it and focus returns to Create options.
- **Staging**: lists the two enabled sandboxes and leaves the disabled
  one out.
  - `web-preview` links `http://127.0.0.1:8081/` and reads "not
    reachable through this proxy".
  - The long-named sub-path entry links
    `/staging/<name>/`. Its title reads "crash-looping / last exit:
    exit 1 / opens …". It stays inside the 209 px rail: the rail's
    `scrollWidth` equals its `clientWidth`.
- **Keyboard**: from a fresh load, Tab walks:
  1. Collapse sidebar, + New task, Create options, Customize;
  2. every rail entry (Inbox 14, Tasks 18 and its eight state rows,
     Boards 4, Sessions 1, Processes 9, CI 0, Repos 1);
  3. the build stamp (`/settings/upgrade`);
  4. Alerts, System, Settings;
  5. the page.
- **Collapse sidebar**: collapses the rail without page overflow, and
  Expand sidebar brings it back.
- The install notice spans the top (y=0–48).

### 4.3 Header rail and More page actions (both engines, 820×1180)

- On `/tasks`, `/repos`, `/boards/board-2` and `/processes`, the
  page's row overflows and **More page actions** appears.
  - Enter opens the menu, and Settings stays put at 752,66 (40×40), so
    the rail grew no second row.
  - Escape closes the menu and hands focus back to More page actions.
- `/ci` and the repository's merge-queue tab fit, and show no More
  button.

### 4.4 Banners and snackbar (stubs 2–4)

- **Tablet, `/tasks`**: the disconnected banner is at y=0–68 and the
  install notice at y=68–116, with no horizontal overflow.
  - After the outage is lifted, the banner cleared after about 11.6 s
    in both engines, with no reload. That is the poll's backoff
    (`App.jsx` `pollBackoff`) catching up after a failure streak.
- **`/tasks/4` pane**:
  - Desktop: the 500 on Pause raised the error snackbar at 625,828
    (191×48). `elementFromPoint` at its centre is the snackbar.
  - WebKit phone: the snackbar is at 8,788 (374×48), on top in the same
    way.
  - During the outage, the pane shows its sticky mirror strip
    (`disconnected-banner-mirror`), 1207×36 on desktop and 389×96 on
    the phone.

### 4.5 Phone drawer and alerts panel (both engines, 390×844 and 320×568)

- **Open navigation** opens `role=dialog`, `aria-modal=true`, named
  "Navigation". The body is `overflow: hidden` underneath it, so the
  page behind does not scroll.
- **Create options** keeps the drawer open, and Escape on the menu
  leaves the drawer open.
- **Customize** replaces the drawer with its overlay (§2).
- **Drawer foot**: the System row and the build stamp sit inside the
  viewport:
  - 390×844: System y=752–796, stamp y=807–830;
  - 320×568: System y=476–520, stamp y=531–554.
- The **staging pin** draws at opacity 0.8 on touch, which is the
  2026-10-29 fix holding.
- **Navigation**: tapping Processes opens `/processes`, closes the
  drawer, and puts focus on Open navigation. Back returns to `/inbox`.
- **Alerts panel** (WebKit): 16,134 358×540 at 390 px, and 16,134
  288×398 at 320 px. That is a 16 px inset on both sides. Escape closes
  it.
- **Rotation** to 844×390 and 568×320 leaves no overflow, and Open
  navigation stays visible.

### 4.6 Sessions

The flow below ran in Chromium at 1440×900 and 820×1180, and in WebKit
at 390×844 (touch, through the drawer).

1. **Create**: rail (or drawer) → Create options → New session opens
   `/session/new`.
   - Focus is in the composer (`Message`).
   - **Session name**, the model picker and **Pin to sidebar** are
     present.
   - Pause, Resume, Start a fresh chat and Delete session are all
     absent before the session has started.
   - On the phone, the drawer closed on the navigation.
2. **First message**: name the session, tick Pin to sidebar, then send
   a message with `**hello**`, Shift+Enter lines and a markdown table
   holding a 140-character unbroken cell.
   - The URL becomes `/session/<slug-of-name>`.
   - `<strong>` and `<table>` render, with no page overflow.
   - The pinned rail entry is `Mui-selected` with `aria-current=page`.
3. **Hard reload**: the URL, the message and the lit pinned entry all
   persist. Start a fresh chat is offered now, and Pause and Resume are
   not (a console session has neither).
4. **Delete**: the prompt reads `Delete session "<name>"? This removes
   the session from this deployment.`
   - Dismissing it, from Enter on the focused button, keeps the session
     and leaves focus on Delete session.
   - Accepting it goes to `/session`, and the rail no longer lists the
     session.
5. **Deep link to the deleted session**: it lists the sessions under
   "There is no session “<id>” here. It may have been deleted; pick
   another below." No composer is shown, so it never opens another
   session's chat.
6. `/director` replaces itself with `/session/director`.
7. **Sessions page**: "+ New session" opens `/session/new` with the
   composer focused. Back returns to `/sessions`, and Forward goes to
   `/session/new` again.
8. **Work tree**: the Director's "▸ Provenance · Work from task 13: 0
   of 4 done" expands to links for tasks 13, 1, 3, 8 and 11, with no
   overflow at 1440 or 390.
9. **Restart**, in Chromium at 1440 and WebKit at 390: Start a fresh
   chat offers "close this chat and start a fresh one" and "start fresh
   with summary".
   - Each option kept the session's URL and cleared the old
     conversation.
   - A new message sent after each restart was accepted.
   - The session was then deleted.

No page errors and no unexpected 4xx/5xx occurred in any session run.

### 4.7 Interactive task chat (both engines)

An interactive task was filed through the real API (`POST /api/tasks`,
`interactive: true`, task 24, with a long title) and opened at
`/tasks/24`.

- **Pause** turns both Pause buttons into Resume, and that survives a
  hard reload. **Resume** turns them back into Pause. There are two
  Pause buttons, the composer's and the task actions bar's, and they do
  the same thing, as recorded on 2026-10-29.
- A message with inline code and a link rendered as `<code>` and
  `<a>`.
- **Resizing** from 1440×900 to 820×1180, 390×844 and 844×390 left no
  overflow, and the composer stayed inside the width each time.
- At 844×390 (a rotated phone), the composer sits in the pane's own
  scroller (`overlay-pane`). On a fresh load it is in view, at
  y=223–263 of 390.

### 4.8 Discard prompts, focus restoration and themes (both engines)

The schedule pane `/processes/schedules/sched-2` was opened, and its
Title edited.

| Gesture | Prompt | Outcome |
|---|---|---|
| Escape in the field | asked, declined | URL kept, focus back in the Title input |
| "← Schedules" with Enter | asked, declined | URL kept, focus on "← Schedules" |
| Browser Back | asked, declined | URL kept |

**Themes**: with `colorScheme: dark` and `light`, each with
`reducedMotion: reduce`, the body renders `rgb(15, 16, 19)` and
`rgb(247, 246, 243)`, and the rail's transition duration is `0s`.

## 5. Leads that were not bugs

1. **The create menu "stuck open" after Escape.** A script that pressed
   Tab or clicked within a few milliseconds of Escape found
   `[role=menu]` still mounted, with focus cycling inside it. That is
   MUI's exit transition: 2 s later the menu is gone, and after a 600 ms
   pause Tab moves on normally. A person cannot press keys that fast.
2. **The disconnected banner outlived the outage.** It cleared about
   11.6 s after the network came back, through the poll's
   failure-streak backoff rather than on the next tick.
3. **The phone drawer closed on Customize.** This is by design (§2).
4. **The task-chat composer was below the fold at 844×390.** That was
   only after resizing from portrait: the pane's scroll offset carried
   over. A fresh load shows the composer in view, and it is reachable
   by scrolling the pane.

## 6. Tests run

- The browser walk scripts in §4 ran in both engines, against the build
  of `4f71b139`.
- **Playwright**, existing specs over the same tree:
  `rail-staging-more-menu`, `responsive-navigation-walkthrough`,
  `sessions-list-delete`, `director`, `disconnection` and
  `alerts-panel-layout`, in both the `chromium` and `webkit` projects:
  99 passed (5.2 min).

No code changed, so no regression coverage was added. No screenshots or
browser artifacts are committed.
