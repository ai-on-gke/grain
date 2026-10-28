# Responsive navigation and live sessions walkthrough

Date: 2026-10-02 (16:24–17:30 UTC)

Commit tested: `cf9d6156c9cd7632f89900f00b622a7dadb1d9d7` (tip of
`candidate`). The fixes below, on `grain/task-1535`, were then re-walked
against a rebuilt server.

## Environment

- Debian 12.15 sandbox, x86_64, 4 vCPU / 7 GiB. Go 1.26.6, Node.js
  20.19.0, npm 10.8.2, Vite 6.4.3.
- Playwright 1.62.1 with Chromium and WebKit 26.5 (build 2336),
  installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install chromium webkit`
  plus `install-deps`.
- Server: the real `grain demo -addr 127.0.0.1:8420 -open=false`, built
  with `go build ./cmd/grain` over a production `npm run build` of `ui/`
  embedded in `pkg/ui/static`. It ran on a throwaway embedded SQLite
  store under a scratch `HOME`/`TMPDIR`, which was wiped on every
  restart.
- No `/home/debian/.gemini-api-key` was present, so no Gemini-backed
  tooling was used. Nothing here needs a model.
- Account: the first load opens the real "create account" dialog. A
  `walker` account was created through it (username, password, confirm,
  then Create account), and its storage state was reused for every other
  context. `walker` is not a system admin in the demo
  (`isAdmin: false`), so Admin shows its Sandboxes and Caches tabs.
- Layouts: desktop 1440×900, short desktop 1280×500, tablet 820×1180,
  tablet landscape 1180×820, phone 390×844, phone landscape 844×390 and
  small phone 320×568. Every tablet and phone layout ran with
  `hasTouch`. Dark (System overlay, phone and tablet) and light colour
  schemes were exercised, with `reducedMotion: "reduce"`.

The exploration scripts were ad-hoc Playwright programs driving visible
controls: rail and drawer buttons, row links, dialogs, the chat
composer, Back buttons and browser history. They are not committed.
What they found is committed as regression tests.

## Stubs (every one)

| Boundary                | How                                                                                                                                                                                                                                                                                          | Why                                                                                   |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------- |
| Live session transcript | `page.route("**/api/tasks/12")` (GET only) fetched the real response and appended one agent comment per poll (one of them a 400-character fenced block) with `state: "running"`. It then aborted (`internetdisconnected`) for ~7s, answered 503 for ~7s, and went back to the real response. | The demo has no agent or reconciler, so nothing ever answers in the Director session. |
| Deployment banners      | `page.route("**/api/config")` fetched the real config and overlaid `hostSandboxes: true`, `modelsConfigured: false`, `gitCredentialsConfigured: false`, and a paused `agentPause`/`pipelinePause` with 200-character unbroken reasons.                                                       | The demo raises none of these banners.                                                |

Everything else was real and unstubbed: the server, the Director
standing session (task 12), session create and delete, comments,
routing, layout and the gestures. No device-metrics stub was needed.

## Steps and outcomes

1. **Navigation matrix: seven layouts, Chromium and WebKit.** Every rail
   entry was clicked through the real rail, or through the "Open
   navigation" drawer on phones: Inbox, Tasks, Proposed, Queued,
   Running, Awaiting reply, Closed, Blocked, Boards, Sessions, Schedules,
   Templates, Suites, CI, Repos, Admin and Metrics. On every route in all
   14 engine×layout runs:
   - the URL matched the entry;
   - there was 0px horizontal document overflow and no document-level
     vertical scroll;
   - no focusable control sat off screen outside a horizontal scroller;
   - the drawer was closed after each navigation.

   The rail counts matched the demo (Tasks 12, Inbox 4, Sessions 1,
   Boards 3, Schedules 3, Templates 3, Suites 2, CI 0, Repos 1). The
   build stamp (`v1.5.0 · cf9d615 · …`) was present. The only network
   noise was a superseded `/api/tasks` poll (`ERR_ABORTED`).

2. **Boards and visualizations.** All three demo boards were opened at
   desktop, tablet, phone, phone landscape and 320px, in both engines:
   kanban (`board-1`), timeline (`board-2`, with range and lane pickers)
   and report (`board-3`). Every one had 0px overflow and no off-screen
   controls.
3. **Task detail overlay (keyboard).** Focused a task's row link and
   pressed Enter: `/tasks/2` opened as a named dialog. Tab walked
   "← Back", Prompt, Edit, the comment box, Attach files, Comment and so
   on, all inside the dialog. Escape returned to `/` with focus on the
   row link. Back closed it, Forward reopened it, and a hard reload of
   `/tasks/2` reopened it. The visible "← Back" control returned to `/`.
4. **Admin, System and Settings (WebKit phone dark, Chromium phone
   landscape, WebKit tablet).** `/system` opens Admin, and its tab strip
   (Sandboxes, Caches) fits with each tab on screen. Clicking each tab
   gave 0px overflow, and Escape went back to `/`. `/settings` opens the
   Settings panel (no tabs for this user).
5. **Banners (stubbed).** The strip took 0–338px of 844px on a phone,
   scrolling inside itself (490/338). In landscape it took 0–156px of
   390px. That is the `40vh` cap from grain/task-1360, still holding.
6. **Standing session (Director): send, render, persist.** Done at
   desktop and phone, in Chromium and WebKit. Rail → Sessions → "Open
   session Director" opened `/session/director`. Multi-line Markdown
   was typed with Shift+Enter (bold text plus a GFM table holding a
   150-character unbroken cell) and sent with Enter. It rendered as a
   `<table>` with 0px page overflow and survived a reload.
7. **Live incremental replies, disconnect, error (stubbed).** Three
   agent messages arrived over three polls, the header badge read
   "running", and the Stop control appeared. The fenced block scrolled
   inside its `<pre>` (3000px inside 248px on a phone) with no page
   overflow. During the abort and the 503 the pane kept the last
   transcript without crashing. The only signal was the deliberate
   `grain: session poll failed` console warning (see `App.jsx`). When
   the boundary came back the transcript returned to the real one.
8. **Session lifecycle.** Done on a phone in Chromium. "+ New session"
   was created with a long unbroken name. It routed to
   `/session/night-watch-…` with 0px overflow, and the rail Sessions
   count went from 1 to 2. A cold deep link reopened it. "Delete
   session" raised a native confirm: dismissing it kept the session
   (count 2), and accepting it removed it (count 1, `/session`). Back
   onto the deleted URL showed "There is no session “…” here" and no
   chat.
9. **Interactive console terminal.** `GET /api/console` answers
   `{"enabled":false,"sessions":null}` in the demo, and the UI has no
   terminal pane: interactive tasks and standing sessions are both
   `TaskChat`. Terminal resize and exit therefore have no browser
   surface to exercise (as recorded on 2026-10-01).

## Bugs found and fixed in this task

1. **"Open the task behind this session" reloaded the whole app.** The
   link (and "Open the chat before this one") was a bare MUI
   `Button href`. Clicking it was a full page load of `/tasks/N`: every
   API was re-fetched, the shell flashed, and a draft typed in the
   session's composer was lost. Every other task link in the app opens
   in place (`RowLink`, `RepoReleases`).

   _Repro:_ on `/session/director`, set `window.__marker = 1`, type a
   draft, then click the link. The marker was gone and the draft was
   empty after Back.

   _Fix:_ `ConsoleSessionPage` takes `onOpenTask` (App passes
   `openTask`). A plain left click opens the detail over the session,
   while modified clicks keep the `href` for a new tab. Escape and
   "← Back" now return to `/session/director` with focus on the link
   and the draft intact. Browser Back does the same.

   _Coverage:_ a `ConsoleSessionPage.test.jsx` case (fails without the
   fix), and a Playwright case on tablet in Chromium and WebKit: no
   reload, dialog, Escape, focus, draft, and Back. The Playwright case
   was checked to fail against the unfixed build.

2. **The chat composer had no role or accessible name.** The
   chat-ui-kit `MessageInput` puts its props on a wrapper. The editor is
   a bare `contenteditable` `<div>` whose "Message…" placeholder is CSS
   `::before`. `getByRole("textbox")` found nothing in either engine, so
   a screen reader announced an unnamed editable region.

   _Fix:_ `TaskChat` marks the editor `role="textbox"`,
   `aria-multiline="true"` and `aria-label="Message"`. Every chat gets
   this: task-bound interactive consoles and standing sessions alike.

   _Coverage:_ a `ConsoleSessionPage.test.jsx` case (fails without the
   fix), plus the Playwright case above, which finds the composer by
   role and name.

   `candidate` later landed the same fix independently (132a1625b). The
   merge keeps a single effect, this branch's, which re-applies the
   attributes after every render.

3. **`ui e2e` failed on `candidate` itself, in
   `team-settings-github-tokens-walkthrough`.** The spec found its team
   card with `hasText: "Platform Engineering"` and `.first()`. Each
   Playwright worker shares one demo store across spec files.
   `team-navigation-scoping.spec.js` seeds an "Infrastructure" team
   described as "Cloud & Platform Engineering", and it sorts first. When
   both files ran on the same worker, the locator picked that card, and
   "Git Name: Platform Agent" was never found on it.

   _Repro:_ `npx playwright test e2e/team-navigation-scoping.spec.js
e2e/team-settings-github-tokens-walkthrough.spec.js --project=chromium
--workers=1` fails every time. Either spec alone passes.

   _Fix:_ the spec now finds the card by its exact title heading. The
   same command passes.

## Observations (no change made)

- For a non-admin, `/system` lands on `/admin/logs` while Sandboxes is
  the tab drawn. Clicking Sandboxes, which is already selected, does not
  change the URL. `AdminOverlay.jsx` says this is deliberate: only a
  pick is written to the path, because the fallback may be shown before
  the config confirms admin.
- A cold `/tasks/N`, or Forward onto one, has the task list behind the
  overlay, so Escape there goes to `/` even when the detail was first
  opened from a session. The path carries no record of what was behind
  it. This is the same for every deep-linked task.
- `GET /api/repos/acme/widgets/github-branches` answers 404 in the demo,
  because the demo has no GitHub (an external boundary). The branch
  field tolerates it.
- The earlier follow-up (list rows that are pointer-only) has landed on
  `candidate` as `59d86ccdc`. Rows now open from the keyboard
  (`RowLink`), as step 3 confirms.

## Test results

- `ui/e2e/responsive-navigation-walkthrough.spec.js`: the new case plus
  the neighbouring session cases (deep link, offline send) all pass in
  Chromium and WebKit, 6 of 6.
- Vitest, the whole suite: 158 files, 2745 tests passed.
- Grain CI (`wait_for_checks`, full suite) is the gate for the pushed
  branch.
