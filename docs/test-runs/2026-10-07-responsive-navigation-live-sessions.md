# Responsive navigation and live sessions walkthrough

Date: 2026-10-07 (05:16–07:00 UTC)

Commit tested: `afcf15814`, the tip of `candidate`. Since the 10-06 walk
(`0fa005e3a6e0`) it has gained the 10-06 fixes (session chat pinning,
hidden-tab inbox notifications, wrapped pause reasons) and the 10-07
repository/release/memory, task-intake and boards fixes.

The two fixes below are on `grain/task-2417` (3b17d28a3, fb2d9452a).
Each was walked again on a rebuilt server over a fresh store.

## Environment

- Debian sandbox, x86_64, 4 vCPU. Go 1.26.6, Node.js 20.19.0.
- Playwright (the version in `ui/package-lock.json`) with Chromium and
  WebKit, installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Server: the real `grain demo -addr 127.0.0.1:<port> -open=false`,
  built with `go build ./cmd/grain` over a production `npm run build` of
  `ui/`. Each build ran on its own throwaway embedded SQLite store under
  a scratch `HOME`/`TMPDIR`:
  - :8430, unfixed (`afcf15814`);
  - :8432, both fixes.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed
  tooling was used. Nothing here needs a model.
- Account: the demo's loopback actor (`debian`, an admin). The scripts
  set `grain.username` in local storage first, as `e2e/pageerrors.js`
  does, so the first-visit "Create account" dialog stays closed.
- Layouts: desktop 1440×900, short desktop 1280×500, tablet 820×1180,
  tablet landscape 1180×820, phone 390×844, phone landscape 844×390 and
  small phone 320×568. Every tablet and phone layout had `hasTouch` and
  was driven with `tap()`; Chromium phones also had `isMobile` (WebKit
  does not support it). Dark and light colour schemes and
  `reducedMotion: reduce` were used.

The exploration scripts were ad-hoc Playwright programs kept outside the
commit. No screenshots, traces or other browser artifacts were
committed. Every screen was audited for:

- horizontal document/body overflow and document-level vertical scroll;
- focusable controls past the viewport's edge with no scroller around
  them, or clipped by an `overflow: hidden` ancestor;
- every element that scrolls sideways (`overflow-x: auto|scroll` with
  `scrollWidth > clientWidth`);
- controls covered by another element, by centre-point hit test;
- console errors and warnings, page errors, failed requests and
  HTTP ≥ 400.

## Stubs (every one)

| Boundary                | How                                                                                                                                                                                                                                                                                                                                                                                                    | Why                                                                      |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------ |
| Live session transcript | `page.route(/\/api\/tasks\/12(\?.*)?$/)`, GET only. It fetched the real response, set `state: "running"` and appended one agent reply every second poll, three in all: a 400-character fenced block, a GFM table with a 138-character unbroken cell, and bold Markdown with a list. Then 15s with the browser really offline (`context.setOffline`), 12s answering 503, and back to the real response. | The demo has no agent, so nothing answers in the Director session.       |
| Deployment banners      | `page.route("**/api/config")` fetched the real config and overlaid `modelsConfigured: false`, `gitCredentialsConfigured: false` and a paused `agentPause` with a 196-character unbroken reason and an `until` an hour out.                                                                                                                                                                             | The demo raises none of these, and a usage limit is a provider boundary. |

Everything else was real and unstubbed: the server, routing, React and
layout; sends to the Director (`POST` comments); session create and
delete through "+ New session" and "Delete session"; and every gesture.

## Steps and outcomes

1. **Navigation matrix.** Every rail entry was clicked through the rail,
   or tapped through "Open navigation" on phones: Inbox, Tasks, Proposed,
   Running, Boards, Sessions, Schedules, Templates, Suites, CI, Repos,
   Admin and Metrics. Chromium at all seven layouts; WebKit at desktop,
   tablet, phone and 320px.
   - Every URL matched its entry (`/inbox`, `/?f=…`, `/?state=proposed…`,
     `/boards`, `/sessions`, `/schedules`, `/templates`, `/suites`, `/ci`,
     `/repos`, `/admin`, `/metrics`), and the rail marked the entry
     selected with its count (`Inbox4`, `Tasks12`, `Sessions1`, `CI0`…).
   - On phones the drawer closed after each pick, and `body` was
     `overflow: hidden` while it was open.
   - Escape closed Admin and Metrics back to the screen beneath. Focus
     went back to the rail entry on desktop and tablet, and to "Open
     navigation" on phones.
   - No document overflow anywhere. The only sideways scrollers were the
     kanban `.board-columns`, the Admin tab strip's `MuiTabs-scroller`
     (with scroll buttons on desktop, swipe on touch), the Admin tables'
     outlined papers on phones, and fenced code blocks — all intended —
     plus the timeline's lanes, which is **bug 2**.
2. **Every screen by deep link** at desktop, tablet, phone (dark) and
   320px in Chromium, and desktop in WebKit: `/tasks/4`, `/tasks/12`,
   `/tasks/7`, `/new`, `/boards`, `/boards/board-1`…`board-4`,
   `/repos/acme/widgets`, `/schedules`, `/templates`, `/suites`, `/ci`,
   `/sessions`, `/inbox`, `/metrics`, and all 16 Admin tabs (General &
   Deployment through Upgrade, including Sandboxes and Config repo),
   each reached by clicking its tab. Each came back to its own screen on
   a hard load. Covered-control hits were only tabs scrolled beneath the
   tab strip's own scroll arrow and inputs scrolled beneath a sticky
   action bar, both of which scroll into view.
3. **New task, keyboard only** at 320px. Tab walked Close dialog, Title,
   Description, Attach files, priority, Area, Requested by, Due date,
   Estimate, Repos, Base branch, the write/read repo pickers, Auto-merge,
   Don't port, capabilities, team, Model, Advanced options, Queue
   immediately and position, then wrapped back to Close dialog: focus
   stays in the dialog and every field it lands on is on screen.
4. **Standing session (Director)** at desktop and 320px in Chromium and
   phone in WebKit, reached by Sessions → "Open session Director, team
   default".
   - The composer is named "Message". A message typed with Shift+Enter
     line breaks — `**bold**`, `` `code` `` and a GFM table with a
     150-character cell — sent on Enter, cleared the composer, rendered
     `strong`, `code` and `table`, and was still there after a reload.
     The table scrolls inside its bubble; no page overflow.
   - "+ New session" with an 80-character title: the header fits its
     column (591px at desktop, 258px at 320px), the rail count went from
     `Sessions1` to `Sessions2`, and the sidebar listed it.
   - Back/Forward between the new session, `/sessions` and the Director
     restored each screen.
   - "Delete session" asks `Delete session "<title>"? This removes the
session from this deployment.` Dismissing kept it (count unchanged);
     accepting removed it, landed on `/session` (the Director), focused
     the composer, and the count dropped back. A reload agreed.
   - Creating a second session with a title already in use opened the
     **older** session, and its Delete removed the older one. This is
     **bug 1**.
5. **Live states (stubbed)** at phone in Chromium and WebKit, dark with
   reduced motion, on `/session/director` with the banner overlay.
   - Streamed replies appeared one per poll with the typing indicator
     shown while `running`; the list stayed pinned to the newest message
     (`scrollHeight − scrollTop − clientHeight` = 0); the 400-character
     fence scrolled inside its `pre` (3000/248).
   - Offline: "This deployment cannot be reached…" appeared beside the
     usage-limit alert; the composer stayed at 659–729 of 844.
   - 503: the unreachable banner stayed until the read answered again,
     then cleared; the usage-limit alert remained.
   - Rotating to 844×390 kept the composer at 253–323 of 390, and back
     to portrait showed no audit hits.
   - Dark background `rgb(15, 16, 19)`; MUI transitions `0s` under
     reduced motion.
6. **Interactive console terminal.** `GET /api/console` answers
   `{"enabled":false,"sessions":null}` in the demo, and the frontend has
   no terminal pane (TaskChat's doc comment describes the PTY path; no
   component renders xterm). Interactive tasks and standing sessions are
   chat panes, so terminal resize and exit have no browser surface to
   exercise. Unchanged since 10-01.

## Bugs found and fixed in this task

1. **Creating a session under a title already in use opened (and
   deleted) the older session.**
   - _What:_ the server files a second "Night watch" as
     `night-watch-2`, and `POST /api/console-sessions` answers with the
     whole list. `createStandingSession` (`App.jsx`) opened the first
     session in it whose title matched — the older one. The reader saw
     the older session under the title they had just typed, and its
     "Delete session" removed the older session, leaving the new one.
   - _Repro:_ at 1440×900, Sessions → "+ New session" → "Night shift" →
     Create (lands on `/session/night-shift`). Repeat. The URL is
     `/session/night-shift` again while `GET /api/console-sessions`
     lists `night-shift` and `night-shift-2`; accept "Delete session"
     and `night-shift` is the one gone.
   - _Fix:_ the handler records the ids it knew before the POST and
     opens the one in the answer that is new (preferring a new one with
     the typed title), falling back to the old match only if nothing new
     appears.
   - _Coverage:_ "Creating a session with a title already in use opens
     the new one" in `responsive-navigation-walkthrough.spec.js`,
     unstubbed. Unfixed it fails (`Expected /session/night-shift-…-2$`,
     received `/session/night-shift-…`); fixed it passes in Chromium and
     WebKit.
2. **A timeline's lanes scrolled sideways by one or two pixels.**
   - _What:_ while anything runs, the timeline's "now" line sits at
     `left: 100%` and a just-started bar a hair short of it; both are
     2px wide (`min-width: 2px` for the bar). `.gantt-lanes` has
     `overflow-y: auto`, which makes it a sideways scroller too, so it
     showed a horizontal scrollbar that moved 1–2px.
   - _Repro:_ open Boards → Timeline (`/boards/board-2`) at 1440×900:
     `.gantt-lanes` reads `scrollWidth` 1154 / `clientWidth` 1152; at
     390px, 363/361.
   - _Fix:_ `.gantt-grid` and `.gantt-track` clip their contents, and
     `.gantt-now` is centred on its moment (`margin-left: -1px`), so
     half of it still shows at the edge.
   - _Coverage:_ "A timeline's now line gives its lanes no sideways
     scroll" at 1440 and 390px. Unfixed it fails (`lanes scroll
1154/1152 at 1440px`); with only the grid clipped it still failed
     at 390px (362/361, the bars), which is why the track clips too.
     Fixed, it passes in Chromium and WebKit.

## Observations (no change made)

- **Already-known demo errors.** Each visit to Admin returns
  `GET /api/relay-settings` → 501, and task/repo panes return
  `GET /api/repos/acme/widgets/github-branches` → 404, because the demo
  has no relay or GitHub. Both were reported on 10-06.
- **A session with no task yet** shows "grain files this session's task
  on its next cycle. The chat opens as soon as it exists." and no
  composer, as designed: the demo has no reconciler cycle filing one.
- **The two MUI Autocomplete toggles** on New task are both named
  "Open" (MUI's default), as on 10-06.
- **The task list's search box** is named only by its placeholder
  ("Search tasks…"). Chromium and WebKit both fall back to that for the
  accessible name, so it is announced, but it has no label of its own.
- **Escape after a hard reload of `/tasks/N`** closes the pane to the
  list and focuses the list's container: the reload left no opener row
  to go back to. Escape on a pane opened from its row returns focus to
  the row (walked on 10-06).
- **Keyboard-only.** Tab reaches every rail entry in order, then the
  list's controls. Enter on a rail entry navigates. In Admin, arrow
  keys move through all 16 tabs and wrap around, and Escape returns
  to the screen beneath with focus on the Admin entry. Checked in
  Chromium desktop and WebKit tablet, light mode
  (`rgb(247, 246, 243)`).
- **Phone drawer.** At 320×568 and 390×844 (WebKit), the drawer is
  232px wide and `body` is `overflow: hidden` while it is open. The
  build stamp scrolls into view at the foot of the drawer (536–554 of
  568); it is ellipsised, with the full commit in its `title`. Escape
  closes the drawer and focuses "Open navigation".
- **The deployment notes are accurate.**

## Test results

- Playwright: `responsive-navigation-walkthrough.spec.js` and
  `gantt.spec.js`, Chromium and WebKit, on the fixed build: 71 passed.
  The two new cases fail on the unfixed build as described above.
- Prettier is clean on every touched file.
- Grain CI on fc007c5 (this branch with `candidate` 396b556 merged):
  the first run went red twice, on tests this branch does not touch:
  - `frontend`, at `src/App.narrowing.test.jsx:89`: a 10s
    `findByText("Long done")` timeout. This is the flake already listed
    in the repository notes. It passes locally in 6.6s.
  - `ui e2e`, at `team-connections-models-walkthrough.spec.js:66`: a
    180s timeout waiting for "Continue". It ran while a second full
    run of the same commit was going, and it passes locally in 14s.

  `retrigger_ci` (run c9751a09dce24aa9, uncached) passed every job, and
  the full suite then read success: `frontend`, `go`, `ui e2e`,
  `lint`, `installer`, `package cache`, `postgres` and `terraform`.
