# Responsive navigation and live sessions walkthrough

Date: 2026-10-01 (11:08–12:00 UTC)

Commit tested: `9448ff8cdccd144ab9ef1d01cbcdaeb9221c3bd6` (tip of
`candidate`), then each fix below on `grain/task-1360` re-walked against
a rebuilt server.

## Environment

- Debian 12.15 sandbox, x86_64, 4 vCPU / 7 GiB. Go 1.26.6, Node.js
  20.19.0, npm 10.8.2, Vite 6.4.3.
- Playwright 1.62.1 with Chromium (headless shell, build 1234) and
  WebKit 26.5 (build 2336). The browser download needed
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt`. Without it,
  `npx playwright install` failed with `SELF_SIGNED_CERT_IN_CHAIN`
  against the sandbox's package-cache CA.
- Server: the real `grain demo -addr 127.0.0.1:8420 -open=false`, built
  with `go build ./cmd/grain` over a production `npm run build` of
  `ui/` embedded in `pkg/ui/static`. It used a throwaway embedded SQLite
  store under a scratch `HOME`/`TMPDIR`, which was wiped on each restart.
- No `/home/debian/.gemini-api-key` was present in this sandbox, so no
  Gemini-backed tooling was used. Nothing in this walkthrough needs a
  model.
- The first load of a fresh store opens the real "create account"
  dialog. A `walker` account was created through that dialog in the
  browser (New user tab, then Create account), and its cookie was reused
  for the other contexts.
- Layouts: desktop 1440×900, short desktop 1280×500, tablet 820×1180,
  phone 390×844, and phone landscape 844×390. The tablet and phone
  layouts ran with `hasTouch`. Dark (tablet) and light colour schemes
  were exercised, with `reducedMotion: "reduce"`.

The exploration scripts were ad-hoc Playwright programs driving visible
controls (rail and drawer buttons, the create menu, dialogs, the chat
composer, Back buttons, browser history). They are not committed. What
they found is committed as regression tests in
`ui/e2e/responsive-navigation-walkthrough.spec.js` and in unit tests.

## Stubs (every one)

| Boundary                | How                                                                                                                                                                                                                            | Why                                                                                   |
| ----------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------- |
| Live session transcript | `page.route("**/api/tasks/12")` (GET only) fetched the real response and appended agent comments one poll at a time, set `state: "running"`, then aborted (`internetdisconnected`), then answered 503, then went back to real. | The demo has no agent or reconciler, so nothing ever answers in the Director session. |
| Network loss            | `context.setOffline(true/false)`                                                                                                                                                                                               | Simulates a dropped connection while composing and sending.                           |
| Deployment banners      | `page.route("**/api/config")` fetched the real config and overlaid `hostSandboxes`, `modelsConfigured: false`, `gitCredentialsConfigured: false`, `agentPause` and `pipelinePause` (long unbroken reasons).                    | The demo raises none of these banners.                                                |

Everything else was real and unstubbed: the server, the Director standing
session (task 12), session create, update and delete, comments, task
creation, routing, layout and the gestures. No stub was needed for
device metrics.

## Steps and outcomes

1. **Navigation matrix, five layouts, Chromium and WebKit.** Every rail
   entry was clicked through the real rail (desktop, tablet, short
   desktop) or the phone drawer (phone, phone landscape): Inbox, Tasks,
   each state filter, Boards, Sessions, Schedules, Templates, Suites,
   CI, Repos, Admin/System and Metrics. Each route matched its URL, with
   0px horizontal document overflow and no document-level vertical
   scroll (the panes scroll, not the page), and no offscreen controls
   outside a horizontal scroller. Rail counts matched the demo data
   (Tasks 12, Inbox 4, Sessions 1, and so on). The drawer closed on
   every navigation. The only network error was the expected
   `ERR_ABORTED` from a superseded `/api/tasks` poll.
2. **Short viewport (1280×500).** The rail is one scroll container.
   The footer (Admin, Metrics, user) and the build stamp
   (`v1.5.0-rc.1…-9448ff8`) sit below the fold, but a wheel over the
   rail brings them fully into view (stamp at 468–486px). Not clipped.
3. **Overlays, keyboard and focus (desktop, tablet dark, phone).** New
   task opened by keyboard (focus the trigger, then Enter). It sits on
   the right at 560px, and full width on a phone. Wheeling the
   background did not scroll it. Escape returned to `/` with focus back
   on "+ New task" (phone: "New task"). Settings opened from "User
   settings". Escape restored focus to "User settings" (phone: "Open
   navigation"). Every tab in the System overlay (general & deployment,
   access & accounts, …, upgrade) was clicked with no document
   overflow.
4. **Interactive task-bound console.** At each layout, created a task
   through New task: Title with 60+ unbroken characters, Repo
   `acme/widgets` from the dropdown, Advanced options, then "Interactive
   session". It landed on `/tasks/13`, `/tasks/14` and `/tasks/15` with
   the chat composer. Sent a message, reloaded, and the message
   persisted. The "← Back" control and browser Back/Forward round-tripped
   through `/tasks/N`. The one 404 is
   `GET /api/repos/acme/widgets/github-branches`, which is the demo
   having no GitHub (an external boundary), and the branch field
   tolerated it.
5. **Standing session (Director): send, render, persist.** Opened via
   the rail, then Sessions, then Director (`/session/director`). Sent
   multi-line Markdown with bold text and a GFM table holding a
   140-character unbroken cell, using Shift+Enter for newlines and Enter
   to send. It rendered as `<strong>` plus `<table>` with 0px page
   overflow, and it survived a reload. Toggled the Memory capability
   under "Edit Director capabilities", reloaded, confirmed it persisted,
   and toggled it back.
6. **Live incremental replies, disconnect, error (stubbed, see
   above).** Three agent messages arrived over three polls, and the
   header badge showed "running" with the Stop control. A fenced code
   block wider than the bubble scrolled inside its `<pre>` (1587px
   inside 920px on desktop, 248px on a phone) with 0px page overflow.
   During the abort and the 503 the pane kept the last transcript
   without crashing. The poll is deliberately quiet, see the
   `App.jsx` comment. A draft typed before the outage was kept, and the
   transcript recovered when the boundary came back.
7. **Session lifecycle.** Create options, then New session, named
   "Night watch — a very long standing session name…". It routed to
   `/session/night-watch-…`. The rail Sessions count went from 1 to 2.
   The pane showed "grain files this session's task on its next cycle"
   (the demo has no reconciler), and the long title wrapped without
   overflow. A cold deep link and Back/Forward both worked. Delete
   session opened a native confirm: dismissing it kept the session,
   accepting it removed it, and the count went back to 1.
8. **Phone chat layout (390×844 and 844×390).** The composer and the
   Send button stayed on screen. A wheel over the transcript left
   `window.scrollY` at 0, so there was no background scroll. Switching
   sessions through the drawer closed it.
9. **Banners (stubbed, see above).** Four banners stacked without
   overlap at every width, and their buttons were on screen.

## Bugs found and fixed in this task

1. **A deep link to an unknown or deleted session opened another
   session's chat.** `/session/does-not-exist`, or Back onto a deleted
   session's URL, rendered the Director's chat under that URL. The rail
   highlighted Director, and the next message went to an agent nobody
   picked. A link to a session in a team other than the selected one did
   the same. The cause was `openSession` in `App.jsx`, which fell back
   to `visibleSessions[0]` even when an id was named. Now the fallback
   applies only when no id is named. A missing id shows the Sessions
   list with a warning ("There is no session “x” here…"). Deleting the
   open session goes to the first session, not a hard-coded
   `"director"`.
   _Repro:_ `goto /session/does-not-exist`. The header read "director".
   _Coverage:_ `SessionsPage.test.jsx`, plus an unstubbed Playwright
   case (Chromium and WebKit) that deep-links an unknown id, then
   creates, deletes and revisits a session. Without the `App.jsx`
   change, that case fails on the missing warning.
2. **The Sessions list was not keyboard-operable.** The rows were
   `<li onClick>` with no tabindex or role. They are now
   `role="button"`, focusable, named "Open session X, team Y" (the title alone is not unique: every team can have a "Director"), and open on
   Enter or Space. Covered by a unit test and by the Playwright case
   above, which opens Director by keyboard.
3. **A chat message sent while offline was lost.** `TaskChat` cleared
   the composer before posting. `act()` swallows failures, so a failed
   post left only a "Failed to fetch" (WebKit: "Load failed") banner and
   an empty box. `send()` now detects that the post did not land and
   restores the text and attachments, unless something new has been
   typed. _Repro:_ `context.setOffline(true)`, type, Enter. The box was
   empty. _Coverage:_ a `TaskChat.test.jsx` case that fails without the
   fix, plus a Playwright case on a phone (both engines): send offline,
   reconnect, Send, then reload and check the message persisted.
4. **The New task side panel had no dialog role or name.** `SidePanel`
   is a modal MUI Drawer, so the page behind it is `aria-hidden`, but
   the panel had no `role="dialog"` or name. `getByRole("dialog")`
   matched nothing on `/new`, while the task detail is a named dialog.
   The panel now has `role="dialog"`, `aria-modal`, and
   `aria-labelledby` pointing at its title. Covered by a new
   `SidePanel.test.jsx`.
5. **Stacked banners starved the page on phones.** Four banners took
   570 of 844px in portrait. In landscape they took 329 of 390px, which
   with the 52px phone bar left about 9px for the page. The strip is
   now capped at `40vh` and scrolls inside itself, with
   `overscroll-behavior: contain`. In landscape the page keeps 234px and
   its list scrolls to the first row. _Coverage:_ a Playwright case at
   phone portrait, phone landscape and tablet. Without the cap it fails
   with "Expected ≤ 338.6, Received 549.6".

## Found, not fixed here (follow-up proposed)

- **List rows elsewhere are pointer-only.** Task, board, template,
  suite, schedule and repo rows open only on a mouse click. Tab reaches
  only the reorder handle ("Move …", where Enter picks the row up) and
  the select checkbox, and Repos has nothing focusable at all. The fix
  touches six lists that nest other controls and the reorder gesture,
  so it was proposed as a separate task (against `candidate`, depending
  on this one) with the reproduction above.

## Observations (no change made)

- The demo serves no live terminal (`GET /api/console` answers
  `{"enabled":false}`), and the UI has no terminal pane any more:
  interactive tasks and standing sessions are both `TaskChat`. Terminal
  resize and exit could therefore not be exercised in a browser. The
  endpoints (`/api/console/{id}/resize`, `DELETE /api/console/{id}`)
  have no frontend caller.
- The error banner shows the engine's raw fetch error ("Failed to
  fetch" or "Load failed") when the network is down. It is accurate but
  terse.
- On the New task form, the Repo `<select>` starts at "—" even when the
  team has one obvious repo, and "Create task" stays disabled until one
  is picked.

## Test results

- `ui/e2e/responsive-navigation-walkthrough.spec.js`: 28 passed
  (Chromium and WebKit), including the three new cases.
- Full local Playwright run (`CI=1 npx playwright test`): 248 passed, 1
  skipped (the WebKit finger drag, by design), 2 failed. The failures
  were `phone.spec.js:509` (it saw "E2E Batch Task A…" rows left by
  another spec on the shared worker server) and
  `provider-connection-lifecycle-walkthrough.spec.js`. Both pass when
  re-run alone, and grain's test history shows the latter at 100%. They
  look order-dependent locally and not related to these changes.
- Vitest: the App, SessionsPage, Sidebar, ConsoleSessionPage,
  TaskChat, SidePanel and NewTaskOverlay suites all pass.
- Grain CI (`wait_for_checks`, full suite) is the gate for the pushed
  branch.
