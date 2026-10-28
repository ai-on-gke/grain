# Responsive navigation and live sessions walkthrough

Date: 2026-10-05 (04:20–05:30 UTC)

Commit tested: `13f34523d5b895b0d606073edc3bd59ffe672440`, the tip of
`candidate`. Since the 10-04 walk (`6ceba10`) it has gained, among
other things:

- "Can't reach grain": the disconnected banner, driven by poll failures
  (10-04's follow-up 2);
- MUI transitions honouring `prefers-reduced-motion` (10-04's
  follow-up 1);
- the work tree, drawn on a task's page and as a Tree board, which
  replaces the dashboard in the demo's seed (`board-4`);
- real addresses on a task pane's links to other tasks;
- the 10-04 overlay-action-row fix for 320px phones.

The four fixes below are on `grain/task-1987`. Each was re-walked
against a rebuilt server on a fresh store.

## Environment

- Debian 12.15 sandbox, x86_64, 4 vCPU / 7 GiB. Go 1.26.6, Node.js
  20.19.0.
- Playwright 1.62.1 with Chromium (build 1234) and WebKit 26.5 (build
  2336), installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Server: the real `grain demo -addr 127.0.0.1:<port> -open=false`,
  built with `go build ./cmd/grain` over a production `npm run build` of
  `ui/` embedded in `pkg/ui/static`. Each build ran on its own throwaway
  embedded SQLite store under a scratch `HOME`/`TMPDIR`: the unfixed
  build on :8420, and the fixed builds on :8421 and :8422. The build
  stamp read
  `v1.8.0-rc.1.0.20261005035300-13f34523d5b8 · 13f3452 · 2026-10-05 03:53Z`.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed
  tooling was used. Nothing here needs a model.
- Account: the demo's loopback actor (`debian`, an admin). A browser with
  no `grain.username` in local storage now opens on the "Create account"
  dialog. That is the first-visit dialog, and it is intended. The
  scripts put `grain.username` in local storage first, as
  `e2e/pageerrors.js` does.
- Layouts:
  - desktop 1440×900 and short desktop 1280×500;
  - tablet 820×1180 and tablet landscape 1180×820;
  - phone 390×844, phone landscape 844×390 and small phone 320×568.

  Every tablet and phone layout had `hasTouch`. The phones also had
  `isMobile` in Chromium; WebKit does not support it. Dark and light
  colour schemes, and both `reducedMotion` settings, were used.

The exploration scripts were ad-hoc Playwright programs, kept outside
the commit. No screenshots, traces or other browser artifacts were
committed.

The scripts drove visible controls throughout: rail and drawer entries
(tapped on touch layouts), row and tree links, tabs, dialogs, the chat
composer, "← Back", Escape, and browser history.

Every screen was audited for:

- horizontal document overflow and document-level vertical scroll;
- focusable controls past the viewport's edge, or clipped by an
  `overflow: hidden` ancestor;
- unexpected sideways scrollers: anything other than `pre`, tables, tab
  strips, the kanban/timeline/tree bodies and chip strips;
- controls covered by another element, by centre-point hit test;
- console errors and warnings, page errors, failed requests and
  HTTP ≥ 400.

## Stubs (every one)

| Boundary                 | How                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                   | Why                                                                                     |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| Live session transcript  | `page.route(/\/api\/tasks\/12$/)`, GET only. It fetched the real response, set `state: "running"` and appended one Director reply every second poll, three in all: a 400-character fenced block, a GFM table with a 136-character cell, and bold Markdown. Then it aborted with `internetdisconnected` until the banner showed, answered 503 for 8s, and went back to the real response.                                                                                                                                                                                                                                                              | The demo has no agent or reconciler, so nothing ever answers in the Director session.   |
| Deployment banners       | `page.route("**/api/config")` fetched the real config and overlaid `modelsConfigured: false`, `gitCredentialsConfigured: false`, and a paused `agentPause`/`pipelinePause`, each with a 199-character unbroken reason.                                                                                                                                                                                                                                                                                                                                                                                                                                | The demo raises none of these banners together.                                         |
| Sandbox and host metrics | `page.route("**/api/sandboxes*")`, in one of four modes. **ok**: host load, CPU and memory that change on every poll; one disk fine and one erroring; two task sandboxes, one ready with a 66-character name and one "connection refused"; two standing sandboxes, one failed with "no capacity" and a next attempt. The shape is taken from `SandboxHealthPage.test.jsx`'s fixtures. **empty**: enabled, with nothing in flight. **error**: HTTP 503. **disabled**: `{enabled:false}`. (A first attempt used a made-up shape, which kept the Sandboxes tab from being reached. That was the stub's fault, and it was replaced rather than reported.) | The demo answers `{"enabled":false}`, because it has no sandbox pool and no host stats. |

Some states needed no stub:

- A real browser disconnect was produced with `context.setOffline(true)`.
- The new alert-wrap regression raises a real pipeline pause through
  `POST /api/pipeline-pause`.

Everything else was real and unstubbed: the server, routing, React and
layout, the Director session, session create and delete, Inbox
acknowledge and decline, the boards (including Tree), and every gesture.

## Steps and outcomes

1. **Navigation matrix.** Covered Chromium at all seven layouts, and
   WebKit at desktop, tablet, phone and 320px. Each entry was clicked
   through the rail, or tapped through "Open navigation" on phones:
   Inbox, Tasks, Running, Boards, Sessions, Schedules, Templates,
   Suites, CI, Repos, Admin and Metrics.
   - Every URL matched its entry.
   - On phones, the drawer closed after each pick, and `body` was
     `overflow: hidden` while it was open.
   - The selected entry was highlighted, in the rail and in the reopened
     drawer. Under Admin and Metrics, the page behind kept its selection.
   - Escape closed Admin and Metrics. Focus went back to the rail entry
     on desktop and tablet, and to "Open navigation" on phones.
   - The counts were Inbox 4, Tasks 12, Boards 4, Sessions 1, Schedules
     3, Templates 3, Suites 2, CI 0 and Repos 1.
   - The build stamp sits at the foot of the rail, or of the drawer on
     phones, and is reachable at every layout. Examples: 868–886 of 900
     on desktop, 468–486 of 500 on short desktop, 812–830 of 844 in the
     phone drawer.
   - The only audit hit: at 1180px the Admin tab strip's scroll arrow
     sits over a half-scrolled tab. That is MUI's tab-scroll design.
2. **Phone drawer details (both engines).** A wheel over the open drawer
   left the page at `scrollTop` 0. Rotating to 844×390 with the drawer
   open kept it open, with no audit hits.
3. **Deep links and hard loads of 41 addresses.** Covered Chromium at
   desktop, tablet, phone, phone landscape and 320px, and WebKit at
   phone, tablet and 320px.
   - The addresses were `/`, `/inbox`, five task panes, `/new`,
     `/boards` and all four boards (Kanban, Timeline, Report, Tree),
     `/session`, `/sessions`, `/session/director`, schedules (including
     the repo-declared one), templates, suites, `/ci`, `/repos`, the repo
     page and its releases, `/sandboxes`, `/caches`, `/settings`,
     `/team-settings`, `/system`, `/admin`, `/admin/logs`, `/metrics`,
     `/?state=closed`, `/?state=blocked` and four unknowns.
   - The unknowns each say so on screen: "no task 9999" (then `/`),
     `no board "nope"`, and "There is no session “nope” here…". An
     unknown page goes to `/`.
   - This sweep found **bug 1**, the repo page at tablet width. The other
     hits were not bugs:
     - On `/new`, icons sit inside their fields by design.
     - Transient snackbars covered rows, as expected.
     - The declared-path alert at 320px scrolled sideways; it is fixed
       by **bug 4**.
4. **Admin tabs from the keyboard (both engines, desktop).** All 16 tabs
   were focused and chosen with Enter, and the URL followed each one:
   `/admin`, `/access`, … `/upgrade`. Escape returned to `/`.
5. **Task pane, keyboard only (both engines).**
   - It took 36 Tabs to reach the first row's title link. Enter opened
     `/tasks/1`, and Escape closed it with focus back on that link.
   - Back returned to `/` with focus on the row. Forward reopened the
     pane, and a reload kept it open.
   - The pane's links to other tasks now carry real addresses
     (`/tasks/12`, `/tasks/3`, …).
6. **Boards and the new Tree board (both engines, 320px, phone and
   tablet).**
   - The Tree board nests 12 → {1, 3, 8 → 11}, with no audit hits.
   - Tapping a node's id opened the task. Following the pane's link to
     its parent worked, and Back returned to the child.
   - "← Back" from the pane then went to `/`, not the board. This is
     **bug 2**.
   - Rotating with the pane open gave no audit hits.
   - Closing a pane by keyboard returned focus to the opening link on the
     Kanban, Tree and Timeline boards.
7. **Back and Forward onto panes.** Board → task → Back → Forward →
   "← Back" ended on `/` for Kanban, Tree, the repo page and Inbox. This
   is **bug 2**.

   The repo page's task link (a merge-batch member) reloaded the whole
   document. This is **bug 3**.

8. **Director standing session.** Covered Chromium phone (dark), and
   WebKit phone and tablet (light).
   - Sessions → "Open session Director, team default" opened
     `/session/director`.
   - Sending: the composer was found by role and name ("Message"). A
     multi-line message was typed with Shift+Enter and sent with Enter:
     bold text, `_there_`, and a GFM table with a 150-character cell. It
     rendered as `<strong>`, `<em>` and `<table>`, the composer cleared,
     and the message survived a reload exactly once.
   - Live replies (stubbed): all three arrived, Stop appeared, and the
     newest reply was in view. The fenced block scrolls inside its own
     `pre` (3000/248px), with 0px page overflow.
   - Outage (stubbed): the "can't be reached … Unreachable since … --
     retrying automatically" banner appeared about 3s after the poll
     started failing. It stayed through the 503 phase, and cleared about
     1s after the real answer came back. The transcript stayed on screen
     throughout.
   - Phone geometry with the banner up: banner 0–108, composer 659–729,
     Send 740–780 of 844.
   - **Real disconnect** (`setOffline`): the banner appeared within 3s.
     Sending kept the typed text in the composer and raised the error as
     a snackbar. Back online, the banner cleared within about 1.3s, Enter
     sent the message once, and it was there after a reload. See
     observation 1 about the snackbar.
9. **Session lifecycle.** Covered Chromium phone, Chromium 320px and
   WebKit tablet.
   - Create: "+ New session" opened a dialog with a name field, a team
     picker and Create, which is disabled until a name is given.
   - A 70-character name routed to `/session/night-watch-…`, with no
     audit hits. Sessions went 1 → 2 in the rail and the drawer.
   - A reload kept the session. Back went to `/sessions` and Forward to
     the session.
   - Switching to Director showed its composer, with nothing from the new
     session.
   - Delete raised a native confirm naming the session. Dismissing it
     kept the session (still 2). Accepting it routed to `/session`
     (count 1).
   - Back onto the deleted address, and a cold load of it, both say
     "There is no session “…” here. It may have been deleted", with no
     composer.
10. **Banners (stubbed, dark).** Covered phone, phone landscape, 320px,
    short desktop and tablet in Chromium, and phone, 320px and phone
    landscape in WebKit. Nine screens were checked: `/`, the Director
    session, a task pane, a schedule pane, the Timeline, the Tree,
    Admin → Sandboxes, `/new` and the repo page.
    - The alerts collapse into one strip.
    - Overlays sit below it and take the rest of the viewport.
    - The session composer stays on screen: 269–309 of 390 in phone
      landscape, 351–391 of 568 at 320px.
    - Before the fix, the 199-character reason scrolled sideways inside
      its alert on every screen (2012/252px on a phone). This is **bug 4**.
      After the fix it wraps. The strip keeps its 40% cap (227px at
      320×568) and scrolls vertically.
11. **Sandboxes (stubbed).** Opened through Admin → Sandboxes in both
    engines, in all four modes:
    - **ok** at phone and 320px: host, disks (one erroring), both task
      sandboxes and both standing sandboxes, with no audit hits;
    - **empty** at tablet;
    - **error** at phone;
    - **disabled** at desktop: "Not available: this deployment has no
      sandbox pool…".

    The pane polled every 5s, three calls in 6s including the first.

12. **Inbox mutation (Chromium phone, confirmed in WebKit).**
    - Acknowledge removed the item, and Inbox went 4 → 3 in the drawer.
      After a reload it was still 3.
    - Decline raised "Close 1 without running it? It can be reopened.",
      and dismissing it changed nothing.
    - Tapping an item opened `/tasks/4` as a dialog. A wheel over it left
      the page behind at 0.
    - Rotating to 844×390 gave an 843×336 pane with no audit hits.
    - "← Back" returned to `/inbox` with focus on the item's link.
13. **Themes and motion (both engines).**
    - Dark: body `rgb(15,16,19)`, drawer `rgb(20,22,26)`.
    - Light: `rgb(247,246,243)` and white.
    - Under `reducedMotion: reduce`, the drawer and the dialog
      transitions are now `0s`. Under `no-preference` they are
      0.225s/0.3s. So 10-04's follow-up 1 is fixed on `candidate`, by
      18b95ac0c. Only MUI's touch ripple still animates.
14. **Interactive console terminal.** `GET /api/console` answers
    `{"enabled":false,"sessions":null}` in the demo. The UI has no
    terminal: interactive tasks and standing sessions are both chat
    panes. So terminal resize and exit have no browser surface to
    exercise. This is unchanged since 10-01.

## Bugs found and fixed in this task

1. **A repo page's state counts ran past its column on a tablet.**
   - _What:_ the header's count chips sit in the shared `.chips` strip,
     which is `nowrap`/`flex: none`. Only phones lifted that. At 820px
     the demo repo's eleven counts were 1155px wide in a 588px column.
     Deferred, Closed and Blocked were past the edge, and `.main-column`
     scrolled sideways (1183/588). The same happened at 1024px and
     1100px. The stylesheet's own comment already said the header's
     strip had room to wrap.
   - _Repro:_ at 820×1180, open `/repos/acme/widgets`.
     `.main-column.scrollWidth` is 1183 and its `clientWidth` is 588.
   - _Fix:_ `.repo-page-header .chips` shrinks and wraps. List rows keep
     their one-line clip.
   - _Coverage:_ "A repo page's state counts wrap inside its column on a
     tablet" checks 820px and 1100px. It fails unfixed ("Awaiting submit
     1" ends at 849px) and passes in both engines.
2. **Back/Forward onto a task pane lost the page behind it.**
   - _What:_ `/tasks/N` names only the task, and `onPopState` rebuilt
     everything from the path. So after Back or Forward onto a pane, the
     board, repo page or inbox behind it became the task list, and
     "← Back" went to `/`.
   - _Repro:_ `/boards/board-1` → click a card → Back → Forward →
     "← Back" ends on `/`. Likewise `/boards/board-4` → "11" → the pane's
     link to 8 → Back → "← Back" ends on `/`. Both engines.
   - _Fix:_ the URL effect records the page behind an open task in the
     history entry's state (`{behind}`), and `onPopState` takes
     everything except the task from it. Keeping an edit on Back
     re-pushes that entry instead of `null`.
   - _Coverage:_ "Back and Forward onto a task pane keep the page it was
     opened over" covers a board, the inbox and pane-to-pane. It fails
     unfixed (received `/`) and passes in both engines.
3. **A repo page's merge-batch task links reloaded the app.**
   - _What:_ `RepoPage` rendered `MergeBatchPanel` without `onOpenTask`,
     so each member was a bare `href`. A click was a full document
     load, and "← Back" then went to `/`.
   - _Repro:_ on `/repos/acme/widgets`, set a `window` marker and click
     the first `/tasks/…` link. The marker is gone.
   - _Fix:_ `RepoPage` takes App's `openTask` and passes it on. The
     member is one real link in both places: the href stays for a middle
     click, and a plain click opens the pane in place, as `TaskLink`
     does.
   - _Coverage:_ "A repo page's merge batch opens its tasks in place"
     (both engines; fails unfixed), and the updated `MergeBatchPanel`
     unit test checks the href and the in-place open.
4. **Alerts scrolled an unbroken word sideways.**
   - _What:_ MUI's alert message is `overflow: auto`. A long unbroken
     word (a pause reason, a declaring file's path, a URL) scrolled
     sideways inside the alert instead of wrapping. 10-04 recorded this
     as an observation for the declared path.
   - _Fix:_ `MuiAlert` `message` gets `overflowWrap: "anywhere"` in the
     theme. Ordinary text is unchanged.
   - _Coverage:_ "Banners and alerts wrap an unbroken word on a 320px
     phone" uses a real pipeline pause on `/` and on the declared
     schedule. It fails unfixed (1050/182) and passes in both engines.

## Follow-up proposed (not fixed here)

1. **The send-failure snackbar covers the session's own controls while
   offline.**
   - With the browser offline, a failed send raises `ErrorBanner`'s
     bottom-centre snackbar, which has no close control. On a tablet it
     covers Send, "Attach files", "Edit Director capabilities" and
     "Delete session". On a phone it covers "Open the task behind this
     session" and "Delete session".
   - It repeats what the disconnected banner already says, and it stays
     about 2–5s after grain answers again.
   - Whether an unreachable failure should raise a toast at all while
     the banner is up, or the toast should sit clear of the composer, is
     a design call. It touches every caller of `showError`, so it was
     not folded in here.

## Observations (no change made)

- **A hard reload of `/tasks/N` still opens over the task list.** The
  history entry's `behind` survives a reload, but the initial state is
  read from the path alone. Back/Forward is what bug 2 fixes.
- **Plain task links in the schedule's suppressed-firing note.**
  `ScheduleOverlay.jsx` and `SchedulesList.jsx` link "task N" with a
  bare `href`, so following one would reload the app, as bug 3 did. The
  demo has no suppressed schedule, so this was read in the source and
  not reproduced in a browser.
- **The decline confirm names only an id** ("Close 1 without running
  it?"). This is unchanged from 10-04.
- **The Sandboxes error state** shows the heading with no body text. The
  error goes to the snackbar while the pane keeps polling, as on 10-04.
- **The deployment notes are accurate**, with one addition: a fresh
  browser on the demo now opens on the "Create account" dialog unless
  `grain.username` is set. The e2e fixtures already set it.

## Test results

- Playwright:
  - `responsive-navigation-walkthrough`, `phone` and `stacked-banners`
    specs, with fixes 1–3: 101 passed and 1 skipped, in Chromium and
    WebKit.
  - After fix 4, its own case passed in both engines, and
    `stacked-banners` plus `phone` passed again (51 passed, 1 skipped).
  - Each of the four new cases fails on the unfixed build, as described
    above.
- Vitest:
  - With fixes 1–3, the whole suite passed: 175 files, 2976 tests.
  - After fix 4, the theme and banner tests passed (26).
  - Prettier is clean on every touched file.
- Grain CI, full suite: the result is in the pull request.
