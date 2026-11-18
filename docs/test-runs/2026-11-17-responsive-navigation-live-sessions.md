# Responsive Navigation & Live-Session UI Walkthrough — 2026-11-17

This walkthrough (task 6595) covered the shared shell and the live-session
surfaces at desktop, tablet and phone widths, in a real Playwright browser
against a real `grain demo` server. On `/next/` that meant the rail, the
create menu, the phone compact nav and its More sheet, the disconnected
banner and the viewer role. On classic it meant the header rail, the
sidebar collapse, the drawer, Staging and Boards. On the session side it
covered Sessions, the Director, New session, queued sends, settings and
Delete, plus the task-bound chat with Pause/Resume.

Following `PROMPT.md` ("Two UIs"), every step ran against `/next/` where
`ui-ng/src/app/shell/nav.ts` or `ui-ng/src/app/app.routes.ts` has the page.
Classic `/` was used only for what `/next/` does not have yet. Two `/next/`
bugs were found and fixed on this branch. Regression coverage is in
`ui-ng/e2e/next-sessions-load-error-viewer-reply.spec.js`. No classic page
needed a fix.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-6595-walk-through-responsive`.
- **Base commit tested:** `b10fad991` (`main` at the start of the run). The
  fixes were re-verified on a build of this branch.
- **Build:** `npm ci` and `npm run build` in `ui/`, `ui-ng/` and `ui-hub/`,
  then `go build -o grain ./cmd/grain`. The fixed build added `ui-ng` `npm run
  build` and a fresh `go build`.
- **Server:** `grain demo -addr 127.0.0.1:4290 -open=false -staging
  -host-mode-install -access-code-file <file>`, over its throwaway embedded
  store.
  - The browser signed in with the access code as `alice@example.com`
    (operator).
  - `-staging` seeds the Staging list, and `-host-mode-install` turns on the
    install notice.
  - The `make test-e2e-ng` specs use the suite's own per-worker demo.
- **Browsers:** headless Chromium 1234 and WebKit 2336
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `playwright` from
  `ui-ng/node_modules`, Node 20.19).
- **Viewports:**
  - `1440×900` desktop
  - `1024×768` iPad landscape and `820×1180` iPad portrait (touch)
  - `390×844` and `390×667` phones, and `320×568` small phone (touch,
    `isMobile`)
  - `844×390` phone landscape, for the orientation change
  - light and dark (`colorScheme`), with `reducedMotion: reduce` for the axe
    and motion passes
- **Model/tooling:** there is no `/home/debian/.gemini-api-key` in this
  sandbox, so `LOOK=1` was not possible, and this run's file reader cannot
  show PNGs. Before/after screenshots were taken and attached to the task,
  not committed. Layout was judged through DOM geometry instead:
  - `scrollWidth` overflow
  - interactive controls outside the viewport, ignoring horizontally
    scrolling strips
  - bounding boxes
  - `elementFromPoint` hit tests
  - axe-core (`@axe-core/playwright`, `wcag2a` and `wcag2aa`)
- **Scripts:** the walkthrough scripts were throwaway files outside the
  checkout (`sweep`, `shell`, `banner`, `sessions`, `task`, `viewer`,
  `classic`), each driving the UI's own controls.

## Stubs

Only external or live boundaries were intercepted, with Playwright
`page.route`. Navigation, Angular/React rendering, the server and browser
layout stayed real.

| Boundary | Stub | Why |
| --- | --- | --- |
| `**/api/**` | held unanswered, then released | a daemon that accepts the connection and never answers, then answers again |
| `**/api/**` | aborted `connectionrefused`, then unrouted | a daemon refusing connections, then back |
| `POST /api/tasks/{id}/comments` | held 1.5–2 s, then passed on to the demo | a slow post, to see the sending and queued states |
| `GET /api/console-sessions` | `500 {"error": "session store unavailable"}` | a session store that cannot be read |
| `GET /api/config` | the real answer, with `access` replaced by `{full: false, scopes: [tasks:read, sessions:read, repos:read, ci:read]}` | a viewer without write scopes; the demo has no identity proxy to sign one in |

No session manager, live stream or device-metrics stub was needed: the demo
answers the console-session routes and queues a run for each message.

## Steps and outcomes

### 1. Every page at every width (`/next/`, Chromium and WebKit)

**Chromium:** a sweep opened 38 addresses at `1440×900`, `1024×768`,
`820×1180`, `390×844` and `320×568` (190 loads):

- Inbox, Workstreams, Tasks, Agents, `task/1`
- Sessions, `sessions/director` and `/next/director`
- Processes and its schedules, templates and suites tabs, CI
- Repos, `repos/acme/widgets` and its tasks/settings/ci/advanced tabs
- Telemetry, `system/metrics`, Context, Fleet, `system/{hosts,sandboxes,staging}`
- Settings and its general/access/models/git/config-repo/upgrade/repos/ci
  sections
- `t/acme/tasks`, `/next/boards` and classic `/boards`

At each one it measured page overflow, offscreen controls, the `h1` count,
console errors and failed requests.

**WebKit:** the same 38 addresses at `1440×900`, `820×1180` and `390×844`
(114 loads).

- **Outcome:**
  - No horizontal overflow, no offscreen control and exactly one `h1` on
    every page, at every width, in both engines.
  - The only failed request was `GET /api/state-repo/files` → `501` on
    Settings → Config repo. This is expected: the demo has no config repo,
    and the page says so.
  - WebKit also logs the previous page's `/api/events` stream as
    "cancelled" on each navigation, which is expected.
- **Redirects:**
  - `/next/director` → `/next/sessions/director`
  - `/next/system/metrics` → `/next/telemetry`
  - `/next/system/{hosts,sandboxes,staging}` → `/next/fleet`
  - `/next/t/acme/tasks` → `/next/tasks`
  - `/next/repos/acme/widgets/settings` → the repo's overview, since
    `settings` is not one of `REPO_TABS`
  - `/next/boards` → Inbox, because Boards is classic-only

### 2. Rail, keyboard and resizing (`/next/`, `1440×900`)

- **Rail destinations:** clicked one by one, Inbox through Settings (12 of
  them). Each lands on its page, becomes the rail's only `aria-current`
  item, and leaves the page without overflow.
  - Back and Forward step through the history (Fleet ↔ Settings).
- **`c` shortcut:** opens New task with focus in its title. Escape closes it.
- **Create menu:**
  - It lists New task, Start a workstream, Prototype a goal, New session,
    Task from template, Run a suite, New schedule and New board.
  - Enter opens it. Escape closes it with focus back on **New**. An outside
    press closes it.
- **Resizing:** with the menu open, resizing to `390×844` closes the menu
  with no overflow, and resizing back is clean too.

### 3. Phone compact nav and More sheet (`390×844`, `320×568`, touch)

- **Compact nav:** pinned at the bottom (y 788 at 390×844, y 512 at
  320×568), holding Inbox, Tasks and Sessions. Each tap navigates with no
  overflow.
- **More sheet:**
  - It fills the space above the bar (390×772 and 320×496) and locks
    background scrolling (`body` and `html` set to `overflow: hidden`).
  - It holds the other nine destinations, the whole create menu and
    Customize navigation.
  - Escape closes it with focus back on **More**. Tapping Fleet navigates
    and closes it.
- **Phone New task:** opens a full-screen sheet with focus in its title.
  Escape hands focus back to the New task button.
- **Orientation change:** turning to `844×390` landscape is clean.

### 4. Disconnected banner (`/next/tasks`, `1440×900` and `390×844`)

- **Held connections:** with every `/api/**` call held, the shell banner
  (`role="status"`) appears after about 11.9 s (the 10 s `HANG_MS` bound
  plus the next poll). It reads "This deployment cannot be reached: it
  accepted the connection but sent no answer within 10 seconds. Unreachable
  since …".
  - It spans the top edge (1440×53, and 390×108 on the phone) without
    overflow, and wins the hit test at its centre.
  - Once the calls are released it clears without a reload (32 ms on
    desktop, 2.9 s on the phone).
- **Refused connections:** the banner shows "the connection to it failed"
  within 2 s, and clears 1–3 s after connections are allowed again.
- **Fixed since 11-15:** G1 from the 11-15 and 11-16 walkthroughs is
  resolved on `main` (`ConnectionStatus`, `connectionInterceptor`).

### 5. Sessions (`/next/sessions`, Chromium and WebKit)

Run in Chromium at `1440×900`, `1024×768` and `390×667`, and in WebKit at
`1440×900` and `390×844`:

- **New session:** opened from the rail's create menu (tablet and desktop)
  and from the page's button (phone).
  - It focuses **Name** in both engines.
  - Start session stays inside the viewport on the short phone (y 552–592 of
    667).
- **First message:** a long name, and a first message holding markdown, a
  table and a 300-character code line, open `/next/sessions/<slug>`.
  - The message renders within 96–354 ms, and the composer is focused.
  - The status reads "Starting: A run is queued for your last message".
  - The session is the rail/list's `aria-current` item, with no overflow.
- **Queued sends:** with posts held, two quick Enters show as "Sending…" and
  "Queued". Both land, and both are still there after a reload.
  - Send wins its hit test at every size.
- **Grants & settings:** opens without overflow.
- **Delete:** asks first, with focus on Cancel. Confirming returns to
  `/next/sessions`, with focus on the Director title (desktop and tablet) or
  the Sessions heading (phone).
- **Deleted session:** its deep link afterwards says "That session does not
  exist. Back to sessions" and doesn't show another session's chat.
- **Failed `GET /api/console-sessions`:** the page reports "Sessions could
  not be read: session store unavailable. This page tries again on its own."
  But on desktop the chat pane beside it also said **"That session does not
  exist."**, on `/next/sessions/director` as well. That is **bug B1**.

### 6. Task-bound interactive chat (`/next/task/13`, the Director's task)

At `1440×900` and `390×844`:

- **A reply sent with its post held:** the composer is disabled with the
  text still in it, and no bubble shows it as sending. Once the post
  answers, the composer clears and the message appears.
  - This is unchanged since 11-16, whose walkthrough proposed it as a
    follow-up, so it is not filed again.
- **Pause and Resume:** Pause pauses at once and shows the Paused pill,
  which survives a reload. Resume brings Pause back.
- **Phone reach:** on phones, Send scrolls clear of the compact nav and wins
  its hit test (390×844, 390×667 and 320×568).

### 7. Viewer role (`/next/`, stubbed `access`)

On Inbox, Tasks, `sessions/director`, `task/13` and Settings → Access, at
desktop and phone:

- The rail's New task is disabled, and the phone New-task button is not
  offered.
- Sessions renders no New session, composer, Delete or Pause.
- No overflow and no console errors.
- But `/next/task/13` (and the task pane on `/next/tasks`) offered an
  **enabled** reply composer and Send. `POST /api/tasks/{id}/comments` needs
  `tasks:write`, so the reply would only be refused after it was written.
  That is **bug B2**. The Inbox's composer for the same task was already
  disabled, with the reason beside it.

### 8. Accessibility, themes and motion (`/next/`)

- **axe-core** (`wcag2a` and `wcag2aa`, `color-contrast` included) over
  Inbox, `sessions/director`, Tasks, `task/13` and Settings → Access, light
  and dark, at `1440×900` and `390×844`, with reduced motion: **no serious
  or critical violations** (20 scans).
- **Reduced motion:** no dot, typing or activity animation is running on
  the Director.

### 9. Classic-only surfaces (`/`)

These have no `/next/` page or control yet, and ran against classic:

- **Header rail** (`/tasks`):
  - At `1440×900`, Alerts, System and Settings sit at the row's end
    (x 1288–1412 of 1440).
  - At `1024×768` and `820×1180`, More page actions shows before them. It
    opens, closes on Escape with focus back on it, and closes on an outside
    press.
  - No "All teams", Admin mode or team switcher text appears.
- **`[` shortcut:** collapses the sidebar (Expand sidebar shows), the state
  survives a reload, and `[` expands it again. Same at all three widths.
- **Boards** (`/boards`): no overflow at those widths.
- **Old team links:**
  - `/t/acme/tasks` → `/` (classic's all-tasks page)
  - `/t/acme/inbox` → `/inbox`
  - `/t/acme/sessions` → `/sessions`
  - `/t/acme/repos` → `/repos`
- **Phone drawer** (`390×844`):
  - It is a modal "Navigation" dialog, 232×844, that locks background
    scrolling. Its list scrolls (573 of 847 px shown).
  - The Staging list links `docs-preview` → `/staging/docs-preview/` and
    `grain-staging` → `http://127.0.0.1:4280/`.
  - The build stamp is at its foot (bottom 830 of 844).
  - Escape closes it with focus back on Open navigation.

### Not available in `/next/` yet

These steps ran against classic, as recorded above:

- Boards
- a collapsible rail and `[`
- pinned items and Customize pins
- the Staging list
- the UserTile and build stamp at the rail's foot
- the "More page actions" header rail and the alerts bell
- a slide-in drawer (`/next/` uses the compact nav and More sheet instead)

## Bugs found and fixed (`/next/`)

| | Bug | Reproduction | Fix |
| --- | --- | --- | --- |
| B1 | A failed sessions read made the chat pane say "That session does not exist." beside the load-error banner, even for the Director. | Answer `GET /api/console-sessions` with `500`, then open `/next/sessions` or `/next/sessions/director` at 1440×900. | `sessions-page.component.ts`: the not-found message shows only when the list was read (`store.loaded() && !store.loadError()`), as the list's "No sessions yet" already did. |
| B2 | A viewer without `tasks:write` got an enabled reply composer on the task page. | Stub `/api/config` `access` to read-only scopes, then open `/next/task/<open task>`: the composer and Send are enabled. | `task-detail.component.ts`: the composer is disabled when `POST /api/tasks/{id}/comments` is denied, and carries `deniedReason` (the reason note above it), as the Inbox's composer does. |

Regression spec: `ui-ng/e2e/next-sessions-load-error-viewer-reply.spec.js`.
It runs in the desktop and phone projects.

- **B1:** the load-error banner shows, with neither "That session does not
  exist." nor "No sessions yet". The page recovers once the read answers.
  An unknown session still says it does not exist.
- **B2:** the composer is enabled for the operator. For a viewer, the
  composer and Send are disabled, and the reason names `tasks:write`.
- **On the base build** (the fixes stashed and rebuilt), the B1 and B2
  tests failed (`toHaveCount(0)` and `toBeDisabled()`), and the
  unknown-session test passed.

After the fix, measured at `1440×900`, `390×844` and `320×568` in light and
dark: the viewer's reason note sits above the composer with no overlap or
overflow. Its text is `rgb(179,38,30)` (light) and `rgb(242,184,181)`
(dark), and axe passes it.

### Review follow-up and merge with `main` (2026-11-18)

- **Overlap with `main`:** `main` gained its own fix for B2 (`c205b0166`,
  `commentDenied()` and `next-task-viewer-composer.spec.js`). On merging,
  the branch keeps `main`'s `commentDenied()` and drops its own duplicate
  `deniedReason` helper.
- **Review 7051 finding:** the conversation renders the `reply-blocked`
  note outside its `hidden` composer branch, so a viewer on a closed,
  stopped or held-parked task saw "…needs tasks:write…" with no composer
  to explain.
- **Fix:** `task-detail.component.ts` now computes `composerHidden()` once,
  and passes the denial only when a composer is shown.
- **Coverage:** the viewer test also opens a closed task (fetched with
  `/api/tasks?state=closed`) and expects no composer and no
  `reply-blocked`. With the ungated `deniedReason: commentDenied()`, that
  check failed (`toHaveCount(0)`); with the gate, it passes.

## Follow-up

None filed:

- The task-page composer's held-post behaviour (step 6) was proposed by the
  11-16 walkthrough.
- The disconnected banner (G1) is fixed on `main`.

## Tests run

- `npx playwright test` in `ui-ng/`, over `next-full-system`,
  `next-sessions-load-error-viewer-reply`, `next-session-send-queue`,
  `next-shell-sessions-walkthrough`, `next-responsive-sessions` and
  `next-inbox` (desktop, phone and dark projects): **33 passed**.
- `npx vitest run src/app/task src/app/sessions src/app/shared/conversation`:
  **14 files, 176 tests passed**.
- `npx eslint` and `npx prettier --check` on the changed files: clean.
