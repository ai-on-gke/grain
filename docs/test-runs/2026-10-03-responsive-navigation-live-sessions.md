# Responsive navigation and live sessions walkthrough

Date: 2026-10-03 (12:32–14:15 UTC)

Commit tested: `f8add8a92f3dcd47d8d775550d4ca00ded9d134f`, the tip of
`candidate`. It carries the 10-02 fixes plus a day of UI changes: the
unsaved-edit guard, read-only declared editors, the Metrics window in
the URL, and title-link rows. The fixes below are on `grain/task-1630`,
and each one was re-walked against a rebuilt server.

## Environment

- Debian 12.15 sandbox, x86_64, 4 vCPU / 7 GiB. Go 1.26.6, Node.js
  20.19.0.
- Playwright 1.62.1 with Chromium (build 1234) and WebKit 26.5 (build
  2336), installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Server: the real `grain demo -addr 127.0.0.1:8420 -open=false`, built
  with `go build ./cmd/grain` over a production `npm run build` of `ui/`
  embedded in `pkg/ui/static`. It ran on a throwaway embedded SQLite
  store under a scratch `HOME`/`TMPDIR`, which was wiped on every
  rebuild and restart.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed
  tooling was used. Nothing here needs a model.
- Accounts:
  - Most of the walk ran as the demo's default actor (an admin), with a
    stored username of `alice`, as the e2e fixture does. That is why
    Admin shows all 16 tabs.
  - Two accounts were also created through the real "Create account"
    dialog: `walker` and `walker2`. Sign-up gives each one a personal
    team, so they are not members of `default`, and their rail reads
    Tasks 0. That is team-scoped reads working as intended. See
    follow-up 1 for the part that is not scoped.
- Layouts: desktop 1440×900, short desktop 1280×500, tablet 820×1180,
  tablet landscape 1180×820, phone 390×844, phone landscape 844×390 and
  small phone 320×568. Every tablet and phone layout had `hasTouch`, and
  phones also had `isMobile` in Chromium (WebKit does not support it).
  Dark and light colour schemes were exercised, with
  `reducedMotion: "reduce"`.

The exploration scripts were ad-hoc Playwright programs under `/tmp`.
They are not committed, and neither are screenshots or traces. They
drove visible controls throughout: rail and drawer buttons, row links,
tabs, dialogs, the chat composer, Back buttons and browser history.

Every route was checked for the following. "Off-screen" here means a
focusable control past the viewport's edge, or past the edge of an
`overflow: hidden`/`clip` ancestor, that is not inside a horizontal
scroller.

- horizontal document overflow;
- document-level vertical scroll;
- off-screen controls;
- console errors, page errors, failed requests and HTTP ≥ 400.

## Stubs (every one)

| Boundary                         | How                                                                                                                                                                                                                                                               | Why                                                                                   |
| -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Live session transcript          | `page.route("**/api/tasks/12")` (GET only) fetched the real response and appended an agent comment every second poll, three in all (one a 400-character fenced block, one a GFM table with a 120-character cell), with `state: "running"`. It then aborted (`internetdisconnected`) for 8s, answered 503 for 8s, and went back to the real response. | The demo has no agent or reconciler, so nothing ever answers in the Director session. |
| Deployment banners               | `page.route("**/api/config")` fetched the real config and overlaid `modelsConfigured: false`, `gitCredentialsConfigured: false`, and a paused `agentPause`/`pipelinePause` with 200-character unbroken reasons.                                                                                                  | The demo raises none of these banners.                                                |

Everything else was real and unstubbed: the server, the Director
standing session (task 12), session create and delete, comments,
schedules, routing, layout and the gestures. No device-metrics stub was
needed.

## Steps and outcomes

1. **Navigation matrix.** Covered Chromium at all seven layouts and
   WebKit at desktop, tablet, phone and 320px. Every entry was clicked
   through the real rail, or through "Open navigation" on phones: Inbox,
   Tasks, Boards, Sessions, Schedules, Templates, Suites, CI, Repos,
   Admin and Metrics. In every run:
   - the URL matched the entry;
   - overflow was 0px, with no document scroll and no off-screen
     controls;
   - the drawer was closed after each tap;
   - the selected entry was highlighted.

   The counts matched the demo: Inbox 4, Tasks 12, Boards 3, Sessions 1,
   Schedules 3, Templates 3, Suites 2, CI 0, Repos 1. The build stamp
   was present. The only network noise was a superseded `/api/tasks`
   poll (`ERR_ABORTED`).
2. **Deep links and hard loads of every major screen.** Covered both
   engines at phone, 320px, tablet, desktop and phone landscape. The
   screens were:
   - the three boards, opened as `/boards/board-1` (kanban), `board-2`
     (timeline) and `board-3` (report);
   - `/tasks/2` and `/tasks/12`, `/new`, `/session/director`,
     `/sessions` and `/repos/acme/widgets` (plus `/releases`);
   - `/settings`, `/team-settings`, `/system`, `/metrics` and `/ci`;
   - two schedules, a template and both suites, including the
     repo-declared ones;
   - `/inbox` and `/?state=closed`;
   - all 16 Admin tabs, clicked through the scrolling tab strip.

   This found **bug 1** (Admin → All Teams) and **bug 2** (the Timeline
   pickers). Both are fixed, and everything is now clean. The demo's
   `github-branches` request answers 404, because the demo has no
   GitHub; that is expected.
3. **Task detail (both engines, phone and tablet).**
   - The row's title link opened `/tasks/2` as a dialog. While it was
     open, `body` was `overflow: hidden` and a wheel over it left the
     list's `scrollTop` at 0, so there was no background scroll.
   - Back returned to `/` with focus on that row's link. Forward
     reopened the dialog, and a hard reload reopened it too.
   - "← Back" returned to `/`.
   - Rotating the viewport with `/tasks/3` open, and rotating back, kept
     0px overflow.
4. **New task draft.** Typed a title, then pressed Escape: the dialog
   closed with focus back on "New task". Reopening it, and a hard reload
   of `/new`, both brought the title back, because drafts persist. No
   confirmation is needed, and none was raised.
5. **Keyboard only.**
   - Desktop: Tab reached the rail. Enter on Schedules opened it, and
     Enter on a row's handle started a reorder, which Escape cancelled
     with focus restored.
   - Admin: ArrowRight ×3 then Enter selected "Sandbox & Infrastructure",
     and its tab scrolled into view. Escape returned to `/`.
   - Phone: Enter on "Open navigation" moved focus into the drawer, and
     Escape returned it to the button. Choosing an entry with Enter or
     Space found **bug 3**.
6. **Unsaved edits (both engines, phone and tablet).** Edited a
   schedule's Title, then pressed Escape: the native confirm "You have
   unsaved changes here. Leave and discard them?" appeared, and
   dismissing it kept the edit. Browser Back on the same edit found
   **bug 4**.
7. **Banners (stubbed, dark).** Two alerts stacked: no model, and the
   agent pause.
   - Phone: 0px overflow, and the first task row at y=517 of 844.
   - Phone landscape: still 0px overflow, but the first row starts at
     y=400 of 390, so the list has to be scrolled before any row shows.
     The strip keeps its `40vh` cap. See the observations.
8. **Director standing session (Chromium phone, dark; WebKit tablet).**
   - Sessions → "Open session Director, team default" opened
     `/session/director`.
   - Sending: the composer is found by role and name ("Message").
     Multi-line Markdown was typed with Shift+Enter (bold text plus a
     GFM table with a 150-character cell) and sent with Enter. It
     rendered as `<strong>` and `<table>`, and survived a reload.
     `_there_` rendered as `<em>`.
   - Live replies (stubbed): three agent messages arrived over seven
     polls. The header read "running", and Stop appeared. The fenced
     block scrolls inside its `<pre>` (3000/248px) with no page
     overflow.
   - During the abort and the 503 the transcript stayed on screen
     without crashing. The only signal is the deliberate
     `grain: session poll failed` console warning; there is no on-screen
     alert. When the boundary came back, the real transcript returned.
9. **Session lifecycle (Chromium phone, WebKit tablet).**
   - Create: "+ New session" with a 82-character name routed to
     `/session/night-watch-…` with 0px overflow. The Sessions count went
     from N to N+1 in the drawer and in the rail.
   - The new session shows "grain files this session's task on its next
     cycle". The demo has no reconciler, so that task never comes and the
     session has no composer.
   - Creating the same name again made `night-watch-…-2`. Both rows show
     the same title (see the observations).
   - Switching to Director and sending worked, and nothing leaked
     between sessions. Back and Forward stepped
     `/sessions` ↔ `/session/…`, and a cold deep link reopened the
     session.
   - Delete raised a native confirm naming the session. Dismissing it
     kept the session (count unchanged); accepting it removed the
     session and routed to `/session` (count −1).
   - Back onto the deleted URL showed "There is no session “…” here. It
     may have been deleted", with no composer.
10. **Interactive console terminal.** `GET /api/console` answers
    `{"enabled":false,"sessions":null}` in the demo, and the UI has no
    terminal pane: interactive tasks and standing sessions are both
    `TaskChat`. Terminal resize and exit therefore have no browser
    surface to exercise, as recorded on 10-01 and 10-02.

## Bugs found and fixed in this task

1. **Admin → All Teams clipped every team card's actions on a phone.**
   Each card was laid out in flex rows that never wrapped, inside an MUI
   Card that clips its overflow. At 390px the card body was ~450px wide
   in a ~290px card. Ten controls were drawn past the card's edge, with
   no scroller to reach them:
   - Team Board and Repository Settings;
   - Copy Clone Command and Copy Command;
   - the Copy Credentials, Broadcast, Edit and Delete icons.

   _Repro:_ at 390×844 open `/admin/teams`. Every one of those controls
   has its right edge between 484 and 590px.

   _Fix:_ `TeamsPanel` rows wrap, using flex gaps. The body may shrink,
   and long team and repo names break.

   _Coverage:_ a Playwright case at 320px and 390px, in Chromium and
   WebKit. It checks every link and button lies within the card, then
   opens Edit Team. It fails on the unfixed build, listing all 10
   controls.

2. **A Timeline board's Lanes picker was cut off on a phone.** The Range
   and Lanes pickers sat in one unwrapping row inside `.gantt`, which
   clips its overflow. The Lanes picker's right edge was at 399px, past
   a 390px or 320px screen.

   _Fix:_ the pickers' row in `GanttChart` wraps and stays
   right-aligned.

   _Coverage:_ a Playwright case at 320px and 390px, in both engines. It
   checks both pickers are on screen, then picks "By state". It fails on
   the unfixed build (398.8 > 320).

3. **A phone's drawer stayed open after a keyboard pick.** The drawer
   closed from `onClickCapture`. The rail's entries are
   `ListItemButton`s (`div role="button"`), and ButtonBase answers Enter
   (keydown) and Space (keyup) by calling `onClick` itself, with no
   click event. So Enter on Schedules navigated to `/schedules` behind a
   drawer that stayed open, with focus still trapped in it. The next Tab
   walked the rail again, and in the walk Enter then opened Metrics by
   accident.

   _Repro:_ at 390×844, focus "Open navigation", press Enter, focus
   Schedules, press Enter. The URL is `/schedules` and
   `.MuiDrawer-modal` is still visible 1.5s later. Both engines, Enter
   and Space; a click closes it.

   _Fix:_ `PhoneNav` also closes on Enter keydown and Space keyup on a
   non-native, non-disabled `role="button"`. It uses the same exclusions
   as the click path (team picker, its search, team settings). Focus
   returns to "Open navigation".

   _Coverage:_ two `PhoneNav` unit cases using a real `ListItemButton`
   (both fail without the fix), and a Playwright case on a phone in both
   engines.

4. **Browser Back dropped an edited schedule, template or suite without
   asking.** 8af545853 guarded Escape, the backdrop, "← Back" and the X.
   Browser Back and Forward (on a phone, the back gesture) move the
   address bar and reach App through `popstate`, never through
   Overlay's close handler.

   _Repro:_ open a schedule, change its Title, then press browser Back.
   No confirm appears, the pane closes, and the edit is gone. Escape on
   the same edit asks.

   _Fix:_ Overlay registers its `isDirty` with a leave-guard registry
   (`unsavedEdits.js`) while it is mounted, and App's popstate handler
   asks `leavingLosesEdits()` first.
   - Declining keeps the pane and the edit, and pushes the pane's
     address back.
   - Accepting proceeds as before.
   - An unedited pane leaves without asking.

   _Coverage:_ two `App.schedules` unit cases (the edited one fails
   without the fix), and a Playwright case on a phone in both engines.
   It declines a real `goBack`, checks the URL and the edit, then
   accepts, and reloads to confirm nothing was saved.

## Follow-up proposed (not fixed here)

1. **`GET /api/console-sessions` is not team-scoped.** A new account
   outside `default` gets "Sessions 1" and can open `/session/director`.
   That page offers "Edit capabilities" and "Delete session", and says
   "grain files this session's task on its next cycle", which is false:
   task 12 exists, but it is 404 to them.

   The server does refuse the writes: PUT returns 403 (admin only), and
   DELETE returns 403 (not a member of `default`). So this is a
   read-scoping and UX inconsistency in `pkg/ui/console_sessions.go`,
   not an escalation. It was filed for `candidate` with the
   reproduction, and for human review, since it changes read scoping.

## Observations (no change made)

- **Session polling has no on-screen offline signal.** While the
  session poll fails (offline or 503), the chat keeps the last
  transcript, and the only signal is a console warning. This is
  unchanged from 10-02.
- **Phone landscape with banners.** With two banners stacked, the first
  task row starts below the fold. The list scrolls inside itself and
  nothing overlaps.
- **Duplicate session names.** Two sessions created with the same name
  get distinct ids (`…-2`), but their rows and accessible names in the
  Sessions list are identical.
- **A non-admin hits a 403 on every load.** For a non-admin,
  `GET /api/state-repo` answers 403 on every load and logs a console
  error plus a warning ("state repo unavailable"). It is harmless but
  noisy.
- **The deployment notes are still accurate.** The repo notes about
  running walkthroughs against `grain demo` and stubbing only external
  boundaries held. No `.gemini-api-key` was present.

## Test results

- Playwright, `ui/e2e/responsive-navigation-walkthrough.spec.js`: the 4
  new cases pass in Chromium and WebKit (8/8). The neighbouring keyboard
  case also passes in both engines.
- Vitest: `PhoneNav`, `App.schedules`, `Overlay`, `TeamsPanel` and
  `GanttChart` pass. The whole suite was run too; its result is in the
  pull request's CI.
- Grain CI, full suite: the first run failed `ui e2e` on one WebKit case
  that this branch does not touch. It was
  `candidate-keyboard-walkthrough` "…unusable base branch", where the
  page-error guard caught "Fetch API cannot load
  …/api/schedules/composition due to access control checks". That is a
  debounced preview fetch cancelled by `page.goto`. The case passed 4/4
  locally, and no other branch shows the failure. The spec now waits for
  the network to go idle before navigating away from the still-open
  schedule form.
