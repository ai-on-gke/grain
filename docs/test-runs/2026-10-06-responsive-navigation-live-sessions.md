# Responsive navigation and live sessions walkthrough

Date: 2026-10-06 (20:44–21:45 UTC)

Commit tested: `0fa005e3a6e0`, the tip of `candidate`. Since the 10-05
walk (`13f3452`) it has gained, among other things:

- desktop notifications for new inbox items (017527a85);
- the task-pane flicker fix;
- searchable Model pickers;
- Metrics sections that fail one at a time;
- no unreachable-error toast while the disconnected banner is up (this
  was 10-05's follow-up 1);
- the four 10-05 fixes.

The three fixes below are on `grain/task-2237` (c0c2b496c, 61c31fa31,
c55349f2d). Each was walked again on a rebuilt server over a fresh store.

## Environment

- Debian 12 sandbox, x86_64. Go 1.26.6, Node.js 20.19.0.
- Playwright 1.62.1 with Chromium and WebKit 26.5 (build 2336), installed
  with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Server: the real `grain demo -addr 127.0.0.1:<port> -open=false`, built
  with `go build ./cmd/grain` over a production `npm run build` of `ui/`.
  Each build ran on its own throwaway embedded SQLite store under a
  scratch `HOME`/`TMPDIR`:
  - :8420, unfixed;
  - :8421, fix 1;
  - :8422, fixes 1–2;
  - :8423, fixes 1–3, with stamp
    `v1.8.0-rc.1.0.20261006211219-c55349f2dc6f · c55349f · 2026-10-06 21:12Z`.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed
  tooling was used. Nothing here needs a model.
- Account: the demo's loopback actor (`debian`, an admin). The scripts set
  `grain.username` in local storage first, as `e2e/pageerrors.js` does, so
  the first-visit "Create account" dialog stays closed.
- Layouts:
  - desktop 1440×900 and short desktop 1280×500;
  - tablet 820×1180 and tablet landscape 1180×820;
  - phone 390×844, phone landscape 844×390 and small phone 320×568.

  Every tablet and phone layout had `hasTouch`, and touch layouts were
  driven with `tap()`. Chromium phones also had `isMobile`; WebKit does
  not support it. Dark and light colour schemes and both `reducedMotion`
  settings were used.

The exploration scripts were ad-hoc Playwright programs, kept outside
the commit. No screenshots, traces or other browser artifacts were
committed. Every screen was audited for:

- horizontal document overflow and document-level vertical scroll;
- focusable controls past the viewport's edge, or clipped by an
  `overflow: hidden` ancestor;
- unexpected sideways scrollers;
- controls covered by another element, by centre-point hit test;
- console errors and warnings, page errors, failed requests and
  HTTP ≥ 400.

## Stubs (every one)

| Boundary                        | How                                                                                                                                                                                                                                                                                                                                                                                              | Why                                                                                         |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------- |
| Live session transcript         | `page.route(/\/api\/tasks\/12$/)`, GET only. It fetched the real response, set `state: "running"` and appended one Director reply every second poll, three in all: a 400-character fenced block, a GFM table with a 136-character unbroken cell, and bold Markdown with a list. Then 9s with the browser really offline (`context.setOffline`), 6s answering 503, and back to the real response. | The demo has no agent, so nothing answers in the Director session.                          |
| Chat follow probe               | The same route, appending one seven-paragraph reply per poll, up to six.                                                                                                                                                                                                                                                                                                                         | To measure whether the list follows new replies.                                            |
| Deployment banners              | `page.route("**/api/config")` fetched the real config and overlaid `modelsConfigured: false`, `gitCredentialsConfigured: false`, and a paused `agentPause`/`pipelinePause`, each with a 198-character unbroken reason. Fix 3's walk overlays only `agentPause`.                                                                                                                                  | The demo raises none of these together, and a usage limit is a provider boundary.           |
| Sandbox and host metrics        | `page.route("**/api/sandboxes*")`, in one of four modes. **ok**: host stats that change on every poll, one good disk and one erroring disk, one ready task sandbox with a 68-character name and one "connection refused", and two standing sandboxes, one of them failed. The shape is from `SandboxHealthPage.test.jsx`. **empty**, **error** (503) and **disabled** (`{enabled:false}`).       | The demo has no sandbox pool or host stats.                                                 |
| Desktop notifications and focus | An init script replaced `window.Notification` with a recorder whose `requestPermission` answers `granted` or `denied` (kept in `sessionStorage` across reloads). It overrode `document.hasFocus` and `document.visibilityState` from a page flag.                                                                                                                                                | This is the OS boundary: headless browsers neither show notifications nor background a tab. |

Everything else was real and unstubbed:

- the server, routing, React and layout;
- sends to the Director (`POST` comments);
- session create and delete through "+ New session" and "Delete session";
- the proposals whose arrival raised notifications (real `POST /api/tasks`);
- every gesture.

## Steps and outcomes

1. **Navigation matrix.** Every rail entry was clicked through the rail,
   or tapped through "Open navigation" on phones: Inbox, Tasks, Running,
   Boards and each of `board-1`…`board-4`, Sessions, Schedules, Templates,
   Suites, CI, Repos, Admin (all 16 tabs) and Metrics. This ran in
   Chromium at all seven layouts, and in WebKit at desktop, tablet, phone
   and 320px, on the unfixed and the fixed build.
   - Every URL matched its entry, and the rail highlighted the entry
     selected.
   - On phones the drawer closed after each pick, and `body` was
     `overflow: hidden` while it was open.
   - Escape closed Admin and Metrics. Focus went back to the rail entry
     on desktop and tablet, and to "Open navigation" on phones.
   - Deep links with a hard reload (`/tasks/4`, `/tasks/12`, `/new`,
     `/boards/board-4`, `/repos/acme/widgets`, `/schedules`, `/inbox`)
     each came back to the same screen.
   - There was no document overflow anywhere. The only sideways
     scrollers were the kanban columns and the Admin tables'
     `MuiTableContainer`, both intended.
   - The build stamp is at the foot of the rail's own scroller. It
     scrolls fully into view at 1280×500 (468–486 of 500), and it is
     ellipsised, with the full text in its `title`.
2. **Standing session (Director)** at desktop, tablet, phone and 320px in
   Chromium, and at desktop and phone in WebKit. The session was reached
   through Sessions, then "Open session Director, team default".
   - A real message with `**bold**` and `` `code` `` rendered as Markdown,
     and it was still there after a reload.
   - The stubbed replies arrived one at a time. The fenced block scrolls
     inside its own `pre` (3000/920 on desktop, 3000/189 at 320px), and
     the table wraps its unbroken cell within the bubble.
   - On the unfixed build the newest reply was out of view on desktop
     (top=1253 in a 900px viewport). This is **bug 1**. Fixed, it is in
     view at every layout. At 320px the reply is taller than the list, so
     the list is pinned to its end with the reply's first line just
     above.
   - Offline: the disconnected banner (`data-testid=disconnected-banner`)
     came up. A send while offline raised no snackbar, which confirms
     10-05's follow-up 1 is fixed, and the draft was kept in the
     composer. The banner stayed through the 503 phase and cleared on
     its own afterwards.
   - "Delete session" asks with a native `confirm()`
     (`Delete session "Director"? This removes the session from this deployment.`).
     Dismissing it left the session alone.
   - Back went to `/sessions`, Forward went back to the session, and a
     reload kept the session.
3. **Create, switch and delete a session** (Chromium desktop).
   - "+ New session" opened a 444×313 dialog, and Escape returned focus
     to "+ New session".
   - Create with a 66-character name opened `/session/<slug>`, and the
     rail went from Sessions 1 to Sessions 2.
   - Switching to Director and back through the list worked.
   - Delete (confirm accepted) went to `/session` and the count back to
     1, and it stayed 1 after a reload.
   - A deep link to the deleted id says "There is no session … It may
     have been deleted", and shows no other session's chat.
4. **Inbox desktop notifications (new).** Run in Chromium at desktop,
   phone and 320px, and in WebKit at tablet.
   - "Turn on notifications" sits in the inbox header with no audit hits
     (123×40 at 390px). It asks for permission, then reads "Turn off
     notifications", and still does after a reload.
   - A denied answer reads "Notifications blocked".
   - A proposal filed while the page was in front raised nothing, which
     is intended.
   - A proposal filed while the tab was hidden raised nothing within 35s
     on the unfixed build. This is **bug 2**. Fixed, it raised
     "14 was proposed and needs approval" (tag `task:14:proposed`).
     Clicking it opened `/tasks/14` as a pane.
5. **Keyboard only.** Run in Chromium at desktop (reduced motion, light)
   and tablet (reduced motion, dark), and in WebKit at desktop (motion
   allowed, dark).
   - The tab order runs: "+ New task" › Create options › Customize › the
     rail entries in order › User settings › search › filters › the rows.
   - Enter on a row opens the task as a dialog with focus on "← Back".
     Six Tabs stayed inside the dialog, and a wheel over it left the page
     behind at 0.
   - Escape returned to `/` with focus on the row's link.
   - Transitions are `0s` under `reduce` and 0.3s otherwise.
   - Dark mode reads `rgb(15,16,19)` and light mode `rgb(247,246,243)`.
6. **Stacked banners (stubbed)** at 320px (Chromium), phone (WebKit) and
   tablet (Chromium). Seven screens were checked: `/`, the Director
   session, `/tasks/4`, `/new`, `/boards/board-4`, the repo page and
   Schedules.
   - The alerts collapse into one strip: 0–227 at 320px, 0–188 on a
     phone and 0–108 on a tablet. None of them scroll sideways.
   - The composer stays on screen (386–426 of 568 at 320px).
   - The task pane scrolled sideways on every one of those layouts
     (1171/319 at 320px). This is **bug 3**.
7. **Sandboxes (stubbed)**, through Admin › Sandboxes.
   - **ok** at 320px: every row, with only the tables scrolling.
   - **empty** (WebKit phone): "Nothing in flight, so no sandboxes
     exist."
   - **error** (tablet): only the heading shows, and it keeps polling.
     This is unchanged since 10-05.
   - Each run polled three times in 6.5s.
8. **New task form.** At 390px in both engines, typing "pro" into the
   Model picker narrows it to `gemini-2.5-pro` (342×64 listbox). Escape
   closes the list, and the form stays open.
   - Every field has an accessible name. The two Autocomplete toggles
     are both called "Open", which is MUI's default.
9. **Orientation.** `/tasks/2` was rotated 390×844 → 844×390 → back. The
   pane was 843×336 in landscape, with no audit hits.
10. **Team settings** at 390px in both engines, all eight tabs. With the
    pause stubbed, Connections shows the paused connection's line, which
    wraps with fix 3.
11. **Interactive console terminal.** `GET /api/console` answers
    `{"enabled":false,"sessions":null}` in the demo. Interactive tasks
    and standing sessions are both chat panes, so terminal resize and exit
    have no browser surface to exercise. This is unchanged since 10-01.

## Bugs found and fixed in this task

1. **A desktop session chat stopped following its newest message.**
   - _What:_ chatscope's `MessageList` measures its height at mount. That
     is one frame before the composer's PerfectScrollbar wrapper gets
     `.cs-message-input__content-editor-container`, which sets its
     0.94em/1.35em type. So the composer is 53.8px tall in that frame,
     then 50.1px. When it shrinks, the list's container-resize handler
     scrolls up by the 4px difference. The kit only counts a list as
     "pinned" within ±1px, so it never followed a reply, or the reader's
     own sent message, again. A coarse pointer's 40px editor floor hid
     this on touch layouts.
   - _Repro:_ at 1440×900, open `/session/director`. On
     `.cs-message-list__scroll-wrapper`, `scrollHeight - scrollTop - clientHeight`
     is 4 (927 − 223 − 700). Send a message and it lands below the
     list's bottom.
   - _Fix:_ `.task-chat-window .cs-message-input__content-editor-wrapper > div`
     gets the same font-size and line-height from its first frame.
   - _Coverage:_ "A session's chat stays pinned to its newest message on a
     desktop" (unstubbed). It fails unfixed, both at the pin check
     (received 4) and, with that check skipped, at the sent-message
     check. It passes in Chromium and WebKit.
2. **Inbox notifications never fired for a background tab.**
   - _What:_ the shell's poll returned early while
     `document.visibilityState === "hidden"`
     (`App.jsx`). A hidden tab fetched no tasks, so it had nothing new to
     announce. Once the reader returned, the page was in front and the
     arrival went unannounced. Gates poll on their own clock, so this
     affected tasks only.
   - _Repro:_ turn notifications on, hide the tab, then file a proposal.
     Nothing is shown, however long you wait.
   - _Fix:_ while the tab is hidden and notifications are on, the poll
     re-reads `/api/tasks` (conditional, so a 304 when nothing changed)
     every `HIDDEN_POLL_EVERY` = 10 ticks, which is every 30s, and reads
     nothing else.
   - _Coverage:_ a new `App.test.jsx` case, on a fake clock. Ten hidden
     ticks give exactly one `/api/tasks` read and nothing else. With
     notifications off, no reads. The browser walk in step 4 shows the
     notification end to end.
3. **An unbroken usage-limit pause reason stretched the task pane.**
   - _What:_ `pausedUntil()`'s sentence ends in the reason. In a task's
     pane it is plain text: the queued task's activity line and the hint
     under the actions. 10-05's fix only wrapped MUI alerts. The pane
     grew to the reason's width and scrolled sideways.
   - _Repro:_ overlay `agentPause: {paused: true, reason: "provider-window-" + "x"×180}`
     onto `/api/config`, then open `/tasks/2` at 820px. `.overlay-pane`
     reads 1208/587.
   - _Fix:_ `.detail-framework-paused`, the actions hint (now
     `.framework-paused-hint`) and Team settings' `.connection-paused`
     use `overflow-wrap: anywhere`.
   - _Coverage:_ "A task pane wraps an unbroken usage-limit pause reason"
     at 820, 390 and 320px. It fails unfixed ("pane 1208/587 at 820px")
     and passes in Chromium and WebKit.

## Observations (no change made)

- **Already-known demo errors.** Each visit to Admin returns
  `GET /api/relay-settings` → 501 with an error toast. That is today's
  `2026-10-06-daily-exploratory-ui` bug 1, already proposed. Task and
  schedule panes return `GET /api/repos/acme/widgets/github-branches` →
  404, because the demo has no GitHub.
- **Deleting the open session lands on `/session`**, which shows the
  deployment's first session (the Director). That is the documented
  meaning of an unqualified `/session` (`paths.js`, `deleteStandingSession`).
- **The Sandboxes error state** shows the heading with no body, as on
  10-05.
- **A hard reload of `/tasks/N` opens over the task list**, as on 10-05.
- **The deployment notes are accurate.**

## Test results

- Playwright: the three new cases fail on the unfixed build as described
  above and pass on the fix, in Chromium and WebKit. The existing
  "Standing chat session (Director)" case passes in WebKit.
- Vitest: `App.test.jsx` and `inboxNotifications.test.js` pass (55
  tests). Prettier is clean on every touched file.
- After merging `candidate` (117d951), `ui e2e` failed twice, the
  second time uncached, on
  `task_detail_workflows.spec.js:170` ("links related tasks by
  address"): a timeout waiting for the new tab a modified click opens.
  The same failure appears on grain/task-2231, 2232 and 2234. It passes
  locally (alone, with its file, and in the full Chromium suite on two
  workers), and none of this branch's changes touch it. It is proposed
  as a follow-up against `candidate`. After merging the next `candidate`
  (e2b0114), the full suite passed.
- Grain CI, full suite, on c55349f: success. That covers `frontend`, `go`,
  `ui e2e`, `lint`, `installer`, `package cache`, `postgres` and
  `terraform`.
