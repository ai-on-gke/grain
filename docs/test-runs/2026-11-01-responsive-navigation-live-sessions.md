# Responsive navigation & live-session UI walkthrough — 2026-11-01

Task 2092 (second attempt; the first was paused on a usage limit before
it pushed anything). A real-browser walkthrough of the shared shell and
the live session surfaces, at desktop, tablet and phone widths, in
Chromium and WebKit, over a real `grain demo` server. Five bugs were
found; all five are fixed on this branch, with unit and e2e regression
coverage. The page matrix was clean in both engines once they were
fixed.

## 1. Commit and environment

- **Base commit tested**: `a71723af` (`Merge pull request #336 from
  ai-on-gke/grain/049f1e09/task-2086`), the tip of `main`, on branch
  `grain/049f1e09/task-2092`. The fixes were re-checked on the branch
  head with the same scripts. The before screenshots were taken from a
  worktree of `origin/main` served on a second demo.
- **Sandbox**: Debian VM, Linux x86_64, 7 CPUs, 15 GB RAM; Go 1.26.10,
  Node.js v20.19.0.
- **Browsers**: Playwright Chromium (`chromium-1234`) and WebKit
  (`webkit-2336`), from `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`.
- **Server**: `npm ci && npm run build` (into `pkg/ui/static`), then
  `go build ./cmd/grain`, started as
  `grain demo -addr 127.0.0.1:18080 -open=false -access-code-file <file>`
  over its own throwaway embedded store. It was rebuilt and restarted
  after each fix. Each browser context signed in with
  `POST /api/auth/access-code`, as `e2e/accesscode.js` does.
- **Gemini key**: `/home/debian/.gemini-api-key` was absent, so no
  Gemini-backed tooling (`LOOK=1`) was used. `grain demo` runs no agents,
  so nothing else needed it.
- **Driving**: throwaway Node scripts using the repository's own
  `playwright` package, kept outside the repository (`/home/debian/walk`).
  Every step went through visible controls: clicks, taps, Tab, Enter,
  Escape, browser Back and Forward, reloads, typed deep links and
  viewport resizes.

**Viewports**:

| Name | Size | Context |
|---|---|---|
| desktop | 1440×900 | — |
| iPad portrait | 820×1180 | touch |
| iPad landscape | 1180×820 | touch |
| phone | 390×844 | touch; `isMobile` in Chromium |
| small phone | 320×568 | touch; `isMobile` in Chromium |
| rotated phone | 844×390 | — |

## 2. Scope notes: where the brief and the tree differ

These are recorded, not changed.

- **Teams**: there is no team, team switcher, user tile, Admin mode
  switch or "All teams" anywhere. Every load in the matrix checked for
  that wording and found none.
  - `/t/default/tasks` opens `/`.
  - `/t/default/settings/models` opens `/settings/models`.
  - `/t/default/sessions` opens `/sessions`.
- **Phone bar**: it holds menu, grain mark, "grain", alerts bell,
  Settings and New task. The page's own title is the heading under the
  bar, not text in the bar (`PhoneNav.jsx`).
- **Rotated phone**: 844×390 keeps the phone layout. `phone.js`'s
  `PHONE_QUERY` includes `(max-height: 480px) and (orientation:
  landscape)`, so this is by design.
- **Settings group ids**: groups are not routes.
  - `/settings/browser` falls back to the default section, Models.
  - `/settings/deployment` is no longer a group id at all.
  - Every section id was loaded directly.
- **Rail Tasks breakdown**: the rail lists the state groups that have
  tasks: proposed, waiting, running, asked, merging, deferred, closed
  and blocked. Nothing in the seed is merged, so no Merged row was
  drawn. Blocked was drawn and clicked.
- **Sessions in the rail**: a session gets an entry of its own only when
  it is pinned. Otherwise it is reached through the Sessions entry and
  the Sessions page.
- **Terminal**: there is no xterm terminal. A task-bound interactive
  console is a chat (`TaskChat`). Instead of resizing a terminal, the
  window was resized around an open interactive task chat (§4.7).
- **Pause and Resume** belong to an interactive task's chat. A console
  session has neither, before or after its first message. Its "Restart"
  is **Start a fresh chat**, with or without the summary.
- **Repo tab switches replace the history entry** rather than adding
  one (`App.jsx` `otherTab`), so Back from a tab leaves the repository
  page. The existing `repo-tabs-walkthrough` spec asserts this, so it
  is by design.
- **Start a fresh chat** is an inline disclosure (a region toggled by
  `aria-expanded`), not a popup. Escape does not close it; the button
  does.

## 3. Stubs (live or external boundaries only)

Everything else ran against the real server, unstubbed: navigation,
routing, React rendering, layout, and every mutation and its read-back.
Each stub is a Playwright `route` on that one context.

1. **Persistent sandboxes** (`GET /api/persistent-sandboxes`). The demo
   runs no persistent-sandbox supervisor. The stub lists three, the
   same ones `e2e/rail-staging-more-menu.spec.js` uses, to drive the
   rail's Staging group:
   - `web-preview`: running, on host port 8081;
   - `docs-site`: restarting with 4 restarts, `lastError: exit 1`, on
     `/staging/docs-site`;
   - one disabled sandbox.
2. **Config flags** (`GET /api/config`: the real response, with fields
   added):
   - `proxied: true`, for the "not reachable through this proxy"
     marking. Only the daemon can say that a page came through a proxy.
   - `installNotice`, the host-mode install notice. That belongs to a
     host install, which the demo is not.
3. **Disconnection**: `route("**/api/**")` aborting every call with
   `internetdisconnected` until the disconnected banner showed, then
   removed.
4. **Failed write**: every non-GET `/api/**` call answered 500 for one
   press of Pause on an interactive task. This raised the error
   snackbar.

## 4. Steps and outcomes

### 4.1 Page matrix (Chromium and WebKit)

The matrix covers 53 pages at all five sizes in each engine: 530 loads
on the fixed build, plus a first pass of 477 loads on the base build.

**Pages**:

- **Main pages**: `/inbox`, `/`, `/tasks/1` (pane), `/new`, `/boards`,
  `/sessions`, `/session/director`, `/director`, `/processes`, `/ci`,
  `/repos`.
- **Boards**: `/boards/board-1` … `board-4` (Kanban, timeline, report,
  tree).
- **Repository**: `/repos/acme/widgets` and its six `/settings/<tab>`
  tabs.
- **Settings**: every section, plus the two group ids above.
- **System**: all eight `/system/<tab>` tabs, Metrics included.
- **Old team links**: three `/t/default/...` links.

**Checks on every load**:

- document and body `scrollWidth` against `clientWidth`;
- every visible button, link, input, select and tab lies inside the
  viewport, unless it sits in a horizontally scrolling container;
- on desktop and tablet:
  - the header rail is one row of 77 px;
  - the bell, System and Settings are present and at the rail's end;
  - no control in the header slot is cut by the slot's clip;
- the build stamp lies inside the viewport;
- no team or admin wording;
- console errors, page errors, failed requests and 4xx/5xx responses.

**Outcome on the fixed build**: 520 of 530 loads are clean. The other
10 are `/tasks/1` at each size, and each shows only the expected
`GET /api/repos/acme/widgets/github-branches` 404 (a demo run without
`-github-sim`).

On the base build, two things failed:

- `/system/logs` at 820×1180 had a half-clipped control in both
  engines (**bug 2**).
- The repository tab crumbs did not name the tab (**bug 1**).

**Crumbs** (fixed build):

- `tasks`, `task 1`, `new task`, `boards / Timeline`;
- `repositories / acme/widgets`, and
  `repositories / acme/widgets / merge queue` (and so on) on each tab;
- `settings / <section>`, `system / <tab>`;
- `sessions / Director`, `sessions / new chat`.

### 4.2 Rail by mouse and keyboard

Run at desktop and iPad portrait, in light and in dark (dark with
`reducedMotion: reduce`), in both engines.

**Mouse**: each of these rail entries was clicked in turn: tasks,
proposed, blocked, boards, sessions, processes, ci, repos, inbox.
- Each set the expected path: `/?state=blocked&…`, `/boards`, and so on.
- Each set the expected crumbs.
- Each carried `aria-current=page`.
- No layout problems.
- Back and Forward walked that history.

**Counts**: Inbox 14, Tasks 18 with its state breakdown, Boards 4,
Sessions (1, rising as sessions were created), Processes 9, CI 0,
Repos 1.

**Create menu**: it lists new task, new session and new board. Escape
returns focus to Create options.

**Customize sidebar**: it opens a named dialog, and Escape returns focus
to Customize.

**Build stamp**: the rail's one link to `/settings/upgrade`, which opens
`settings / upgrade`.

**Staging** (stub 1):
- `web-preview` links `http://127.0.0.1:8081/` and reads "not reachable
  through this proxy".
- `docs-site` links `/staging/docs-site/`.
- The disabled sandbox is left out.
- Pin and unpin are covered by the existing spec, which passes (§6).

**Keyboard-only** (Tab in Chromium, Alt+Tab in WebKit): from the top,
focus visits Collapse sidebar, + new task, Create options, Customize
sidebar, then every rail entry in order down to repos. Enter on repos
opens `/repos` and leaves focus on the entry.

**Header rail order** (`/`, by Tab): search, filters, backlog order, the
running chip, then (on a tablet) More page actions, then Alerts, System
and Settings. Enter on Settings opens `/settings/models`.

**More page actions**:
- Shown on `/boards/board-2` at desktop.
- Shown on `/`, `/repos`, `/boards/board-2`, `/processes` and
  `/system/logs` at 820.
- Enter opens the slot as a menu. The rail stays a 77 px row.
- Escape closes the menu and returns focus to "More page actions".

**Themes**: light renders with a body background of `rgb(247, 246,
243)`, dark with `rgb(15, 16, 19)`.

### 4.3 Phone bar and drawer

Run at 390×844 and 320×568 in both engines, touch only.

**Drawer**:
- It is `role=dialog`, `aria-modal=true`, named "Navigation".
- The body is `overflow: hidden` underneath it, and a wheel over it
  moved the page by 0 px.
- **Create options** opens its menu and the drawer stays open. Escape
  closes the menu, still with the drawer open. Its New session closes
  the drawer, opens `/session/new` and focuses `Message`.
- **Customize sidebar** closed the drawer on the base build (**bug 3**).
  Fixed, the drawer stays open under the full-screen dialog, and
  Escape returns focus to Customize inside the drawer.
- **Foot**: the System row and the build stamp sit at the foot of the
  drawer, inside the viewport:

  | Viewport | System row | Build stamp |
  |---|---|---|
  | 390×844 | y=752–796 | y=807–830 |
  | 320×568 | y=476–520 | y=531–554 |

- **Navigation**: tapping Processes navigates, closes the drawer and
  puts focus on Open navigation. Back returns to `/inbox`. Tapping the
  System row opens `/system` and closes the drawer.
- **Keyboard**: Enter on Open navigation opens the drawer, and Escape
  closes it with focus back on the button.

**Alerts panel**: it spans x=16–374 of 390 and x=16–304 of 320, inside
the viewport vertically. Escape returns focus to the bell.

**Back controls**:
- A Settings section has "All settings".
- `/system/metrics`, a repo tab and the task pane each have one.
- `/settings`, `/system` and `/sessions` are lists, with no back
  control.
- Browser Back from Models skipped the section list on the base build
  (**bug 4**).

**Tab strip at 320**: the repository's 7-tab strip scrolls (657/208,
`overflow-x: auto`). End moves focus to Advanced and scrolls it into
view (x=172–264).

**Rotation** to 844×390: `/inbox`, `/tasks/1` and `/session/director`
reflow without overflow.

### 4.4 Banners and the snackbar

The disconnected banner (stub 3) and the host-mode install notice
(stub 2) were raised together on `/`:

| Width | Disconnected banner | Install notice | App body |
|---|---|---|---|
| 1440 | y=0–48 | y=48–96 | from y=96 |
| 820 | y=0–68 | y=68–116 | from y=116 |
| 390 | y=0–108 | y=108–196 | from y=196 |

- Nothing overflows horizontally.
- The 500 from stub 4 raised the error snackbar at y=828–876 of 900,
  y=1108–1156 of 1180 and y=788–836 of 844. `elementFromPoint` at its
  centre is the snackbar itself.
- The snackbar reads "500 Internal Server Error", not the stub's body
  text. That is `showError`'s formatting and is recorded, not changed.

### 4.5 Sessions

The same flow ran in Chromium at desktop and phone, and in WebKit at
iPad portrait, phone and 320:

1. **From the Sessions page**: New session opens `/session/new` with
   `Message` focused.
   - Crumbs read `sessions / new chat`.
   - No Pause, Resume or Start a fresh chat before the session has
     started.
   - Back goes to `/sessions`, and Forward to `/session/new` again.
2. **From the create menu** (rail, or the drawer on a phone): it also
   opens `/session/new` with the composer focused.
   - A Session name was typed and **Pin to sidebar** ticked.
   - The **model** picker offers "Deployment default (gemini-2.5-pro)",
     gemini-2.5-pro and gemini-2.5-flash, and the first was picked.
3. **First message**: `**hello**`, two Shift+Enter breaks, and a
   markdown table with a 140-character unbroken cell, sent with Enter.
   - The URL becomes `/session/<slug of the name>`.
   - Crumbs read `sessions / <name>`.
   - `<strong>` and `<table>` render, with no layout problems.
   - The rail (or drawer) lists the session under Pinned, with
     `aria-current=page`.
   - `GET /api/console-sessions` shows the title and a backing task.
4. **Hard reload** keeps the URL, the crumbs and the rendered message.
5. **Switching**: Sessions → Director opens `/session/director`, and the
   pinned entry switches back.
6. **Restart**: Start a fresh chat offers "close this chat and start a
   fresh one" and "start fresh with summary". Each was run in turn, and
   each opened the same session with an empty conversation.
7. **Delete**: the confirm reads `Delete session "<name>"? This removes
   the session from this deployment.`
   - Declining stays on the session, with focus on Delete session.
   - Accepting goes to `/session`, and the rail no longer lists it.
8. **Deep link to the deleted session**: it lists the sessions with
   "There is no session “<id>” here. It may have been deleted; pick
   another below." No composer is shown, so it never opens another
   session's chat.
9. **`/director`** replaces itself with `/session/director`.

No console errors, page errors or failed requests occurred.

### 4.6 Work tree

The Director's seeded work tree was exercised at desktop and phone, in
both engines:

- It reads "work from task 13: 0 of 4 done" and has 5 nodes once
  expanded.
- Its child task 1 opens `/tasks/1` over the session.
- Escape returns to `/session/director` with focus on that child's
  link.
- Opening it again, then Back and Forward, moves between
  `/session/director` and `/tasks/1`.

### 4.7 Interactive task chat: Pause, Resume and resize

An interactive task was filed through the real API (`POST /api/tasks`,
`interactive: true`) and driven at desktop, 820 and 390, in Chromium:

- **Pause** turns into Resume, and that survives a reload. **Resume**
  turns it back into Pause.
- The pane still shows two buttons named Pause (the chat's and the
  action bar's), as the 2026-10-29 report noted.
- A message with inline code, a link and a 120-character word was sent
  and rendered.
- The window was then resized to 1440×900, 820×1180, 390×844 and
  844×390. The composer stayed inside the viewport each time, with no
  overflow.
- The chat is a scroller nested inside the scrolling pane
  (`cs-main-container` inside `overlay-pane`). After sending, the pane
  scrolls the composer into view rather than pinning it to the
  viewport's foot.

### 4.8 Focus after a declined discard prompt

The schedule pane `/processes/schedules/sched-2` was opened with an
edited Title, in both engines, at desktop and phone. Every prompt read
"You have unsaved changes here. Leave and discard them?" and was
declined.

| Gesture | Outcome |
|---|---|
| Escape in the field | URL and edit kept, focus back in Title |
| "← schedules" | URL kept, focus on "← schedules" |
| Browser Back | URL and edit kept, focus on "← schedules" |

### 4.9 Long content, nested scrolling, reduced motion

**Long content**: a task whose title holds a 150-character unbroken word, a
200-character table cell and a 300-character code block, and a
pinned session whose name holds a 120-character unbroken word, at desktop, 820 and 390 in
both engines:

- The rail entry is ellipsised within x=11–220, and the rail stays one
  77 px row.
- The table fits its column.
- The `<pre>` scrolls inside itself (2234 px of content in 829, 497 and
  323 px).
- There is no page overflow.
- A wheel over the task pane moved the page behind it by 0 px.

**Reduced motion**: with `prefers-reduced-motion: reduce` in Chromium,
a keyboard-focused button's focus ripple kept pulsing on MUI's infinite
keyframe loop (**bug 5**).

## 5. Bugs and fixes

### Bug 1 — repository tabs were missing from the crumbs

- **Reproduction**: open `/repos/acme/widgets/settings/merge-queue`
  (or click any tab other than Overview). The rail reads
  `repositories / acme/widgets`, the same as Overview.
- **Fix** (`App.jsx` `repoTabCrumb`): the crumbs end in the tab's
  label, lower-cased, for example
  `repositories / acme/widgets / merge queue`. Overview is unchanged.
- **Regression**:
  - `App.pageheader.test.jsx`: a `/repos/acme/widgets/settings/merge-queue`
    row in the page table.
  - e2e "a repository tab is the last crumb of the header rail", which
    covers a tab click, a reload and Overview picked again.

### Bug 2 — the Logs source select was cut off by the header slot

- **Reproduction**: open `/system/logs` at 820×1180. The Source select
  spans x=409–629 in a slot that ends at x=597, in both engines. Its
  arrow is hidden behind the slot's clip.
- **Why**: `minWidth: 220` beats any max-width, and the slot is only
  188 px wide at that size.
- **Fix** (`LogsPage.jsx`): `width: 220, maxWidth: "100%", flex: "0 1
  auto"`. After the fix the select spans x=409–597, wholly inside the
  slot. The rest of the row stays behind More page actions.
- **Regression**: e2e "the Logs source select is never cut by the
  header slot", which compares bounding boxes at 820.

### Bug 3 — Customize sidebar closed the phone drawer

- **Reproduction**: on a 390×844 touch phone, open the drawer and tap
  Customize sidebar. The drawer closes under the dialog, and Escape
  leaves the reader on the page with focus on Open navigation. That is
  away from the rail whose pinned items the dialog edits.
- **Why**: the drawer closes on any tap except text fields, menu
  triggers (`aria-haspopup`) and toggles (`PhoneNav.jsx`
  `keepsDrawerOpen`).
- **Fix** (`Sidebar.jsx`): both Customize buttons carry
  `aria-haspopup="dialog"`, which is also the correct semantics for a
  button that opens a dialog. The drawer stays open. Escape or Save
  returns focus to Customize, and a new pin shows in the drawer at
  once.
- **Regression**:
  - `Sidebar.test.jsx`: "names Customize sidebar as opening a dialog".
  - The new e2e "Customize sidebar opens over the drawer and hands back
    to it".
  - `responsive-navigation-walkthrough.spec.js`'s "Customize sidebar
    opens from the phone drawer and its pin persists" is updated. It
    used to assert the old behaviour (drawer gone, focus on the menu
    button). It now asserts the drawer is still open, focus is on
    Customize, and the pin is listed before the reload.

### Bug 4 — Back from a phone's Models settings skipped the section list

- **Reproduction**: on a phone, open `/inbox`, tap the Settings gear
  (`/settings`, the section list), tap Models (`/settings/models`), then
  press browser Back. You land on `/inbox`. Sandboxes, or any other
  section, comes back to `/settings`. This happens in both engines.
- **Why**: App's address writer treats `/settings` becoming
  `/settings/models` as the same page renamed, and replaces the history
  entry. That is true on a wider screen, where the bare path is Models,
  but not on a phone, where it is the list.
- **Fix** (`App.jsx`): that rename applies only off a phone (`!isPhone`,
  with `isPhone` added to the effect's dependencies).
- **Regression**: e2e "Back from any Settings section returns to the
  section list, Models included". It covers Models and Sandboxes, with
  Back twice.

### Bug 5 — the focus ripple pulsed under reduced motion

- **Reproduction**: with `prefers-reduced-motion: reduce` in Chromium,
  Tab onto any button. Its `.MuiTouchRipple-childPulsate` runs MUI's
  2.5 s infinite keyframe animation.
- **Why**: `theme.js`'s reduced-motion variant zeroes MUI's
  *transitions*, but this pulse is an *animation*.
- **Fix** (`style.css`): under the preference,
  `.MuiTouchRipple-childPulsate { animation: none !important; }`. The
  ring is still drawn, so the focused control stays marked.
- **Regression**: `reduced-motion.spec.js` "a keyboard-focused button's
  ripple holds still". It was confirmed to fail without the rule
  (`animation-name` was MUI's keyframe, not `none`).

## 6. Tests run

- **Vitest**: all ten `src/App*` files (187 tests), plus `Sidebar`,
  `PhoneNav` and `PageHeader`. All pass.
- **Playwright (the repository's config; Chromium, plus WebKit where a
  spec opts in)**, all passing:
  - `responsive-navigation-2026-11-01-walkthrough` (new) and
    `reduced-motion`;
  - `rail-staging-more-menu`, `page-header-rail-walkthrough`,
    `repo-tabs-walkthrough` and `sidebar-navigation`;
  - `responsive-navigation-walkthrough`, `settings-panel-walkthrough`,
    `user-system-settings-walkthrough` and
    `deployment-operations-navigation`;
  - `sessions-worktree-pinned-nav-walkthrough`,
    `sidebar-hidden-sections`, `boards_workflows` and
    `deployment-settings-and-operations-inputs-walkthrough`.
- **Browser walk scripts** (§4): re-run on the fixed build.
- **Screenshots**: `ui/e2e/screenshot.mjs` was run against both demos
  (`main` and this branch) for `/repos/acme/widgets/settings/merge-queue`,
  `/system/logs` and `/settings/models`, at desktop, tablet and phone,
  in light and dark.

No screenshots or browser artifacts are committed.

## 7. Second pass on a later `main` (task 2240)

Task 2240 ran the same brief again later the same day. Its first draft
was made at `4f71b139`, a tree that had none of task 2092's fixes. It
reported no bugs, and dismissed two leads that §5 above, and later
`main`, treat as real bugs:

- Customize closing the phone drawer (Bug 3);
- the disconnected banner taking about 11.6 s to clear after an outage
  (fixed on `main` by `59879e39`).

That draft was dropped in favour of this report. This section records
what was re-checked on the newer tree.

### 7.1 Commit and environment

- **Commit tested**: `928c4fbd`, the branch `grain/049f1e09/task-2240`
  after merging `main` at `f54480e7`. Its tree is identical to
  `f54480e7`, which is 203 commits past `4f71b139` and touches 60 files
  under `ui/src`.
- **Sandbox, browsers, Gemini key and server**: the same as §1.
  - The server was started as `grain demo -addr 127.0.0.1:18080
    -open=false -access-code-file <file>` over its own throwaway store,
    from a fresh `npm ci && npm run build` and `go build ./cmd/grain`.
  - There was no Gemini key, so `LOOK=1` was not used.
- **Driving**: throwaway scripts outside the repository
  (`/home/debian/walk`: `matrix.mjs`, `walk.mjs`, `chat.mjs`,
  `probe.mjs`). Every step went through visible controls.

### 7.2 Stubs

These are Playwright `route` stubs on single contexts only:

1. `GET /api/persistent-sandboxes`, with three staging sandboxes:
   - `web-preview`, running, on host port 8081;
   - a long-named restarting one on a `/staging/…` sub-path;
   - one that is disabled.
2. `GET /api/config`: the real response with `proxied: true` and an
   `installNotice` added.
3. An outage: every `/api/**` request aborted with
   `internetdisconnected` for about 9 s, then the stub was removed.
4. `POST /api/tasks/<id>/comments` delayed by 2.5 s before it was
   passed on to the real server, to show the in-flight "sending" state.
5. `POST /api/tasks/<id>/pause` answering 500, to raise the snackbar.

### 7.3 Outcomes

- **Page matrix** (`matrix.mjs`): the same 49 pages and five viewports
  as §4.1, with the same checks. It ran in Chromium and WebKit, 245
  loads each.
  - The only flags were the expected ones. `/tasks/1` and `/new` are
    modal panes, so they have no globals or stamp at the wide sizes.
    `/tasks/1` logs the known `github-branches` 404 (no `-github-sim`).
  - No overflow, no off-screen control, no clipped globals or stamp,
    and no team wording, in either engine.
- **Shell and sessions** (`walk.mjs`): 0 failures in both engines.
  - **Desktop rail**: the create menu lists New task, New session and
    New board, and Escape hands focus back to Create options.
  - **Staging**: lists the enabled sandboxes only, marks the
    unreachable port, and the rail does not overflow.
  - The rail shows Tasks, Boards, Sessions, Processes, Repos and CI.
    The build stamp opens `/settings/upgrade`. Processes lights up once
    it is opened. Back and Forward work.
  - **Banner**: with the install notice up at 820×1180, the
    disconnected banner showed during the outage. It cleared **3.3 s**
    after the network came back, in both engines (it was 11.6 s at
    `4f71b139`).
  - **Phone (390×844, touch)**:
    - the bar holds Open navigation, Alerts and Settings;
    - the drawer is a modal dialog, and the body is
      `overflow: hidden` behind it;
    - the drawer stays open through the create menu, and through
      opening and closing Customize (task 2092's fix holds);
    - System (y=752–796) and the stamp (y=807–830) are in view;
    - the drawer closes when Processes is opened;
    - the alerts panel spans x=24–366 of 390;
    - nothing overflows after rotating to 844×390.
  - **Sessions at 1440×900**:
    - New session, from the create menu, opens `/session/new` with the
      composer focused. Session name, the model picker and Pin to
      sidebar are present. Pause, Resume, Start a fresh chat and Delete
      session are absent.
    - Sending a named, pinned first message, with bold text and a
      table holding a 140-character cell, moves the URL to
      `/session/walk-<engine>-<n>`. The bold text and the table render
      without overflow, and the pinned rail entry is lit.
    - The session survives a reload.
    - Start a fresh chat offers both options. "start fresh with
      summary" keeps the URL.
    - `/director` redirects to `/session/director`.
    - Delete: declining the prompt (`Delete session "<name>"? This
      removes the session from this deployment.`) keeps the session,
      with focus back on Delete session. Accepting it goes to
      `/session` and removes the rail entry.
    - A deep link to the deleted session lists the sessions, with no
      composer.
  - **Header rail at 820×1180**:
    - On `/tasks`, `/repos`, `/boards/board-2` and `/processes`, More
      page actions opens the expanded slot below the rail. Settings
      does not move. Escape closes the slot and hands focus back.
    - The repository merge-queue tab fits, and its crumbs read
      `repositories / acme/widgets / merge queue` (Bug 1's fix holds).
  - **Themes** (dark and light, reduced motion): the body is
    `rgb(15, 16, 19)` in dark and `rgb(247, 246, 243)` in light.
  - No page errors and no 5xx in either engine.
- **Interactive task chat** (`chat.mjs`): 0 failures in both engines.
  The tasks were filed through `POST /api/tasks` (`interactive: true`,
  tasks 28 and 29).
  - The in-flight message is marked `.task-chat-sending`, and the mark
    clears once the POST lands (`727a76a3` holds).
  - Inline code and links render, and focus stays in the composer.
  - Pause survives a reload, and Resume turns back into Pause.
  - A failed Pause raises the snackbar at 610,828 (220×48), and the
    snackbar is what `elementFromPoint` finds at that point.
  - At 820×1180, 390×844 and 844×390, nothing overflows and the
    composer stays inside the window.
  - Declining the schedule pane's discard prompt ("You have unsaved
    changes here. Leave and discard them?") keeps the URL and puts
    focus back in Title.
- **Playwright** (the repository's config):
  `responsive-navigation-2026-11-01-walkthrough`,
  `page-header-rail-walkthrough`, `disconnected-banner-recovery` and
  `sessions-list-delete` all passed: 39 tests in 1.8 min.

### 7.4 Observation, not filed

A failed write whose 500 body is plain text, as stub 5 sends, is
announced as the status line "500 Internal Server Error", not as its
body. The real server answers with JSON `{error}`, so this was not
treated as a bug.

No bugs were found on this tree, so this pass changes no code. No
screenshots or browser artifacts are committed.
