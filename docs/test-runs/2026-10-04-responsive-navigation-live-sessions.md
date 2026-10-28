# Responsive navigation and live sessions walkthrough

Date: 2026-10-04 (04:52–06:00 UTC)

Commit tested: `6ceba10364d50b4d2df5c46db0b65206e4808f0e`, the tip of
`candidate`. Since the 10-03 walk it has gained:

- the task pane opening at once in a loading state;
- the dashboard list-panel and keyboard-move fixes;
- Sessions scoped to the reader's team (10-03's follow-up);
- standing sandboxes on `/api/sandboxes`;
- "keep the edit or reply a user typed when saving it fails".

The fix below is on `grain/task-1818`. It was re-walked against a
rebuilt server on a fresh store.

## Environment

- Debian 12.15 sandbox, x86_64, 4 vCPU / 7 GiB. Go 1.26.6, Node.js
  20.19.0.
- Playwright 1.62.1 with Chromium (build 1234) and WebKit 26.5 (build
  2336), installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Server: the real `grain demo -addr 127.0.0.1:8420 -open=false`, built
  with `go build ./cmd/grain` over a production `npm run build` of `ui/`
  embedded in `pkg/ui/static`. It ran on a throwaway embedded SQLite
  store under a scratch `HOME`/`TMPDIR`, which was wiped when the server
  was rebuilt with the fix. The build stamp read
  `v1.7.0-rc.1.0.20261004043340-6ceba10364d5 · 6ceba10 · 2026-10-04 04:33Z`.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed
  tooling was used. Nothing here needs a model.
- Accounts: most of the walk ran as the demo's default actor, an admin.
  One account, `walker1818`, was created through the real UI: Current
  user → Log out → "New user".
- Layouts:
  - desktop 1440×900 and short desktop 1280×500;
  - tablet 820×1180 and tablet landscape 1180×820;
  - phone 390×844, phone landscape 844×390 and small phone 320×568.

  Every tablet and phone layout had `hasTouch`, and the phones also had
  `isMobile` in Chromium (WebKit does not support it). Most runs used
  `reducedMotion: "reduce"`. Both dark and light colour schemes were
  used.

The exploration scripts were ad-hoc Playwright programs, kept outside
the commit. No screenshots, traces or other browser artifacts were
committed. The scripts drove visible controls throughout: rail and
drawer entries, row links, tabs, dialogs, the chat composer, Back
buttons, taps and browser history.

Every screen was audited for the following:

- horizontal document overflow;
- document-level vertical scroll;
- focusable controls past the viewport's edge, or past the edge of an
  `overflow: hidden`/`clip` ancestor;
- console errors, page errors, failed requests and HTTP ≥ 400.

Two checks were added this time:

- **Overlapping controls**: pairwise hit-testing of visible controls.
- **Unexpected horizontal scrollers**: an element that scrolls sideways
  and is not a `pre`, a table container or a tab strip. The earlier
  off-screen check skips anything inside a sideways scroller. That is
  why it missed the bug below: the overlay pane itself had become that
  scroller.

## Stubs (every one)

| Boundary                      | How                                                                                                                                                                                                                                                                                                                                                                                                     | Why                                                                                                  |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| Sandbox and host metrics      | `page.route("**/api/sandboxes*")`, in one of four modes. **ok**: host CPU/memory/disks, with one disk erroring; two task sandboxes, one ready with a 66-character name and one failing with "connection refused"; two standing sandboxes, one failed with "no capacity". The figures change on every poll. **empty**: enabled, with no sandboxes. **error**: HTTP 503. **disabled**: `{enabled:false}`. | The demo answers `{"enabled":false}`, because it has no sandbox pool and no host stats.              |
| Live session transcript       | `page.route("**/api/tasks/12")`, GET only. It fetched the real response, set `state: "running"` and appended one agent reply every second poll, three in all: a 400-character fenced block, a GFM table with a 136-character cell, and bold Markdown. It then aborted with `internetdisconnected` for 8s, answered 503 for 8s, and went back to the real response.                                      | The demo has no agent or reconciler, so nothing ever answers in the Director session.                |
| Deployment banners            | `page.route("**/api/config")` fetched the real config and overlaid `modelsConfigured: false`, `gitCredentialsConfigured: false`, and a paused `agentPause`/`pipelinePause` with 199-character unbroken reasons.                                                                                                                                                                                         | The demo raises none of these banners.                                                               |
| Slow task fetch (timing only) | `page.route(/\/api\/tasks\/\d+$/)` delayed the real response by 1.5s, and changed nothing else.                                                                                                                                                                                                                                                                                                         | Without it, the pane's loading state can't be seen, and Escape can't be pressed while it is showing. |

Some states needed no stub:

- A real browser disconnect was produced with `context.setOffline(true)`.
  It is the browser's own network switch, not a route.
- The dashboard's panels were seeded on the real server with one
  `PATCH /api/boards/board-4` (list, states, timeline and metrics
  panels). "Add panel" files a task for an agent to build the panel, and
  the demo has no agent.

Everything else was real and unstubbed:

- the server, routing, React and layout;
- the Director standing session (task 12);
- session create and delete, Inbox acknowledge and decline;
- the panel reorder, account sign-up, and every gesture.

## Steps and outcomes

1. **Navigation matrix.** Covered Chromium at all seven layouts and
   WebKit at desktop, tablet, phone and 320px. Every entry was clicked
   through the real rail, or tapped through "Open navigation" on phones:
   Inbox, Tasks, Boards, Sessions, Schedules, Templates, Suites, CI,
   Repos, Admin and Metrics. In every run:
   - the URL matched the entry;
   - overflow was 0px, with no document scroll and no off-screen
     controls;
   - the drawer was closed after each pick;
   - the selected entry was highlighted. Under Admin and Metrics, the
     page behind kept its own selection, as it should.

   The counts were Inbox 4, Tasks 12, Boards 3, Sessions 1, Schedules 3,
   Templates 3, Suites 2, CI 0 and Repos 1.

   On phones, Admin and Metrics are full-screen overlays. Escape closed
   each one back to the page behind, with focus on "Open navigation".

   The build stamp is present at every layout. On desktop and tablet it
   sits at the foot of the rail, which scrolls on its own (content
   1158px high in 900px), so the stamp is reached by scrolling the rail.
   It is not clipped, and the document never scrolls. On phones it is at
   the foot of the drawer, with nothing in the drawer off-screen.

   The only network noise was a superseded `/api/tasks` poll
   (`ERR_ABORTED`).

2. **Deep links and hard loads of 35 addresses.** Covered Chromium at all
   seven layouts and WebKit at phone, tablet and desktop. The addresses
   were:
   - `/`, `/inbox`, `/tasks/2` and `/tasks/12`, `/new`;
   - `/boards` and all four boards: kanban, timeline, report, and a
     dashboard with a 104-character name;
   - `/sessions`, `/session/director`;
   - `/schedules` and two schedules (one repo-declared), `/templates`
     and a template, `/suites` and both suites;
   - `/ci`, `/repos`, `/repos/acme/widgets` and its `/releases`;
   - `/sandboxes`, `/caches`, `/settings`, `/team-settings`, `/system`,
     `/admin`, `/metrics`;
   - `/?state=closed`;
   - and three unknowns: `/tasks/9999`, `/boards/nope` and
     `/session/nope`.

   Every one loaded with 0px overflow and no off-screen control. The
   unknowns each say so on screen:
   - "no task 9999";
   - `no board "nope"`;
   - "There is no session “nope” here…".

   `/team-settings` resolves to `/teams/default/settings`, and `/system`
   to `/admin/logs`. The demo's `github-branches` lookup answers 404 on
   the task and template panes, because the demo has no GitHub; that is
   expected.

3. **Admin tabs.** All 16 tabs were chosen from the keyboard (focus,
   then Enter) at 320px in Chromium and 390px in WebKit. In each case
   the tab became selected and scrolled into view, the URL followed it,
   and the overflow was 0px. Escape returned to `/`.
4. **Overlap and sideways-scroll sweep (new).** The 44 addresses above,
   plus each Admin tab by its own URL, at 390px and 320px. This found
   **bug 1**. Most of the other hits were not bugs:
   - On `/new`, the attach icon sits inside the description field, and
     the autocomplete's Open button inside its input, both by design.
   - Also on `/new`, the hit test at "Create task" landed on its row.
     The button is disabled until the form is valid, so the hit falls
     through it.
   - The kanban board's columns scroll sideways by design.

   Two small leftovers are recorded under the observations.

5. **Sandboxes (stubbed).** Opened through Admin → Sandboxes in all four
   modes, at phone and 320px in Chromium, tablet in WebKit, and desktop.
   - **ok**: the host, both task sandboxes (including "connection
     refused") and both standing sandboxes (including "no capacity")
     render. The wide table scrolls inside its container, and the page
     overflow is 0px.
   - **empty**: "Nothing in flight, so no sandboxes exist."
   - **disabled**: "Not available: this deployment has no sandbox pool or
     host stats configured."
   - **error**: the error is raised as "sandbox backend unavailable" and
     the poll keeps retrying every 5s, one request at a time. See the
     observations.
6. **Task pane, keyboard only.** Covered Chromium on desktop and WebKit
   on tablet, with the task fetch slowed.
   - Tab reached the first row's title link. Enter opened the dialog at
     once: `aria-busy` was set and focus was on "← Back". When the task
     arrived, the header was filled in.
   - Escape closed the dialog with focus back on the row link.
   - Escape pressed _while the pane was still loading_ closed it, and
     the late response did not reopen it.
   - Browser Back from an open pane returned to `/` with focus on the
     row. Forward reopened `/tasks/1`, and a hard reload kept it open.
7. **Dashboard (seeded panels).** Phone, 320px, tablet and desktop, in
   both engines.
   - The 104-character board name and the long panel title wrap, with
     0px overflow.
   - Each panel's move control carries its name, for example
     "Move panel 1, “A list panel…”".
   - Keyboard reorder: Enter on "Move panel 2, “States”" offered
     "Move here, …" slots. Enter on the slot above panel 1 dropped it
     there. Focus stayed on the moved panel's control, now
     "Move panel 1, “States”", and the order survived a reload.
   - Escape cancelled a pickup with focus kept and the order unchanged.
     A second move restored the original order.
8. **Banners (stubbed, dark).** The pause and setup alerts collapse into
   one strip at the top. Checked at phone, phone landscape, 320px, short
   desktop and WebKit tablet, on seven screens: `/`, the Director
   session, a task pane, a schedule pane, the timeline, Admin →
   Sandboxes and `/new`.
   - Overflow was 0px everywhere.
   - Overlays still take the full viewport.
   - The session's composer and Send stay on screen below the strip,
     even in phone landscape: composer at y=269–309 and Send at 334–374,
     in 390px.
9. **Touch, rotation and background scroll (both engines, phone).**
   - Tapping "Open navigation" set `body` to `overflow: hidden`, and a
     wheel over the drawer left the page behind at `scrollTop` 0.
   - Rotating to 844×390 and back with the drawer open kept the drawer
     and 0px overflow.
   - Tapping Inbox closed the drawer. Tapping an Inbox item opened
     `/tasks/4` as a dialog, and a wheel over it did not scroll the
     Inbox.
   - Rotating with the dialog open gave a 844×390 dialog with nothing
     off-screen. Tapping "← Back" returned to `/inbox`.
10. **Director standing session.** Covered Chromium phone (dark) and
    WebKit tablet (light).
    - Sessions → "Open session Director, team default" opened
      `/session/director`.
    - Sending: the composer is found by role and name ("Message"). A
      multi-line message was typed with Shift+Enter and sent with Enter:
      bold text, `_there_`, and a GFM table with a 150-character cell.
      It rendered as `<strong>`, `<em>` and `<table>`, the composer
      cleared, and the message survived a reload.
    - Live replies (stubbed): all three arrived. Stop appeared, and the
      newest reply was scrolled into view. The fenced block scrolls
      inside its own `pre` (3000/248px) with 0px page overflow.
    - During the abort and the 503, the transcript stayed on screen. The
      only signal is the `grain: session poll failed` console warning.
      When the boundary came back, the real transcript returned.
    - **Real disconnect** (`setOffline`, both engines): no on-screen
      signal anywhere, on the session or the task list. Sending while
      offline kept the typed text in the composer and raised the fetch
      error as an alert ("Failed to fetch" in Chromium, "Load failed" in
      WebKit). Back online, Enter sent it once, and the message was
      still there after a reload.
11. **Session lifecycle.** Covered Chromium phone and WebKit tablet.
    - Create: "+ New session" with an 84-character name routed to
      `/session/night-watch-…` with 0px overflow. Sessions went 1 → 2 in
      the rail and in the drawer.
    - The new session shows "grain files this session's task on its
      next cycle". The demo has no reconciler, so the session has no
      composer.
    - Back went to `/sessions` and Forward to the session. Switching to
      Director showed its composer, with nothing from the new session.
    - A cold deep link reopened the new session.
    - Delete raised a native confirm naming the session. Dismissing it
      kept the session (count still 2). Accepting it routed to
      `/session` (count 1).
    - Back onto the deleted address, and a cold load of it, both say
      "There is no session “…” here. It may have been deleted".
12. **Sessions are now scoped to the team (re-check of 10-03's
    follow-up).** `walker1818`, created through the real dialog, sees
    Sessions 0. `/session/director` says there is no such session, with
    no composer, no "Edit capabilities" and no "Delete session". This is
    fixed on `candidate` by 53372273e.
13. **Inbox mutation (Chromium phone).** Tapping "acknowledge" removed
    the item, and Inbox went 4 → 3 in the drawer. After a reload it was
    still 3. Tapping "decline" (WebKit phone) raised the native confirm
    "Close 1 without running it? It can be reopened.", and dismissing it
    changed nothing.
14. **Interactive console terminal.** `GET /api/console` answers
    `{"enabled":false}` in the demo, and the UI has no terminal pane:
    interactive tasks and standing sessions are both `TaskChat`.
    Terminal resize and exit therefore have no browser surface to
    exercise, as recorded on 10-01 to 10-03.
15. **Themes and motion.** Dark mode gives body `rgb(15,16,19)` and
    drawer `rgb(20,22,26)`; light mode gives `rgb(247,246,243)` and white.
    Under `prefers-reduced-motion: reduce` the drawer still slides
    (0.225s) and dialogs still transition (0.3s). The app stops its own
    animations (the grain mark, the running badge), but MUI's
    transitions do not follow the preference. See follow-up 1.

## Bug found and fixed in this task

1. **Overlay action rows ran off a 320px phone.** Three rows inside
   overlay panes were single flex lines that never wrapped. At 320×568,
   each pushed its last control past the pane's edge, and the only way
   to reach it was to scroll the pane sideways:
   - **Schedule pane:** Delete/Pause/Run now sat beside Cancel/Save.
     Save ended at 361px, and the pane scrolled to 360/319px.
   - **Admin → General:** the "Add system admin" field had a 260px
     minimum, with Add after it. Add ended at 349px.
   - **Admin → Logs:** the toolbar squeezed the "Search logs" field to
     **0px wide**, so it could not be seen or used. Refresh ended at
     325px. At 390px the field was 49px wide.

   _Repro:_ at 320×568 open `/schedules/sched-1`, `/admin` or
   `/admin/logs`. `.overlay-pane` has `scrollWidth > clientWidth`, and
   the buttons' right edges are as listed. Both engines.

   _Fix:_
   - The schedule actions wrap, and Cancel/Save keep to the right
     (`ml: auto`).
   - The admin field is `width: 260` but may shrink.
   - `.logs-toolbar` wraps, and Search takes `flex: 1 1 12rem`.

   At 390px and wider, nothing moves except the Logs search field, which
   now fills the rest of the toolbar. After the fix, every
   one of those controls ends at or inside 304px at 320px, and none of
   the three panes scrolls sideways.

   _Coverage:_ "Overlay action rows keep every control on a 320px
   phone", in `ui/e2e/responsive-navigation-walkthrough.spec.js`. It
   runs at 320px and 390px, in Chromium and WebKit. It checks:
   - every button on each row is on screen;
   - no pane scrolls sideways and the page does not overflow;
   - the search field is over 120px wide, and typing into it filters to
     "(no matching log lines)".

   It fails against the unfixed build ("Save on /schedules/sched-1 at
   320px: expected ≤ 320, received 361") and passes with the fix, in
   both engines. Jest-DOM cannot see layout, so no unit test would catch
   this.

## Follow-ups proposed (not fixed here)

1. **MUI transitions ignore `prefers-reduced-motion`.** The drawer, the
   dialogs and the overlays animate for 225–300ms for a reader who asked
   for less motion. Disabling them is a theme-wide change, either
   `transitions` in `theme.js` or a global rule. It touches every
   overlay and the e2e suite's timing, so it is filed separately rather
   than folded into this fix.
2. **No on-screen signal while the shell cannot reach grain.** This has
   now been recorded four days running. With the browser genuinely
   offline (`setOffline`), or the session poll answering 503, the
   Director session and the task list keep showing their last state
   with no indication. The only trace is a console warning. A reader on
   a phone can watch a "running" session that has silently stopped
   updating. This needs a deliberate design, because the polls are
   quiet on purpose: a single persistent "Reconnecting…" line, not an
   error per tick.

## Observations (no change made)

- **Sandboxes error state.** On a 503 the pane keeps its spinner under
  the heading, while the error shows as an alert. Retrying is correct,
  but the spinner never says it is retrying.
- **Raw fetch errors.** A send that fails offline is reported as the
  browser's own "Failed to fetch" or "Load failed", not as "You're
  offline".
- **Long paths in declared-item alerts.** At 320px, the "declared by
  acme/widgets in `.grain/schedules/nightly.yml`" alert's message
  scrolls 19px sideways inside itself, because the path does not break.
- **Timeline panel at 320px.** The last day label ("Oct 4") in a
  dashboard timeline panel sits 8px past its panel body, inside the
  panel's own scroller.
- **Decline's confirm names only an id.** It says "Close 1 without
  running it?" rather than the task's title.
- **Non-admin `state-repo` 403.** For `walker1818`,
  `GET /api/state-repo` still answers 403 on every load, with a console
  error and a warning. This is unchanged from 10-03.
- **The deployment notes are accurate.** The repo notes about running
  walkthroughs against `grain demo` and stubbing only external
  boundaries held, and no `.gemini-api-key` was present.

## Test results

- Playwright, `ui/e2e/responsive-navigation-walkthrough.spec.js`: the new
  case passes in Chromium and WebKit, alongside the Timeline pickers
  case (4/4). Against the unfixed frontend, the new case fails as
  described above.
- Vitest: `LogsPage` and `AdminOverlay` pass (93 tests). Prettier is
  clean on every touched file.
- Grain CI, full suite: the result is in the pull request.
