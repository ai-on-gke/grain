# Responsive Navigation & Live-Session UI Walkthrough — 2026-11-16

This walkthrough exercised the shared shell (the `/next/` rail and phone
compact nav, the classic sidebar, header rail and drawer, and the banners)
and the live-session surfaces at desktop, tablet and phone widths. These
surfaces were Sessions, the Director, New session, sending, session
settings, Delete, and Pause/Resume on a task-bound chat. Every step ran in a
real Playwright browser against a real `grain demo` server (task 6338).

Following `PROMPT.md` ("Two UIs"), each step ran against the Angular UI at
`/next/` wherever `ui-ng/src/app/shell/nav.ts` or `ui-ng/src/app/app.routes.ts`
has the page. A step ran against the classic UI at `/` only where `/next/`
has no such page or control. Four `/next/` bugs were found and fixed on this
branch, with Playwright regression coverage in
`ui-ng/e2e/next-session-send-queue.spec.js`. No classic page needed a fix.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-6338-walk-through-responsive`.
- **Base commit tested:** `234824180` (`main` at the start of the run). The
  fixes were re-verified on builds of each fix commit (`65f770b5b`,
  `81577ec8c`, `cc68f8f1d`).
- **Build:** `make frontend`, then `go build -o grain ./cmd/grain`. For each
  fix, `ui-ng`'s `npm run build` into `pkg/ui/static-next` and a fresh
  `go build`.
- **Server:** `grain demo -addr 127.0.0.1:4290 -open=false -staging
  -host-mode-install -access-code-file <file>` over its throwaway embedded
  SQLite store. The browser signed in with the access code as
  `alice@example.com (access code)` (operator). `-staging` seeds the staging
  list and `-host-mode-install` turns on the host-mode install notice. The
  Playwright e2e runs use the suite's own per-worker demo
  (`ui-shared/e2e/demo.js`).
- **Browsers:** headless Chromium 1234 and WebKit 2336
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `@playwright/test`
  1.62.1, Node 20.19).
- **Viewports:**
  - `1440×900` desktop
  - `1024×768` iPad landscape (touch)
  - `820×1180` iPad portrait (touch)
  - `390×844` and `390×667` phones (touch, `isMobile`)
  - `320×568` small phone
  - light and dark (`colorScheme`), with reduced motion for the axe passes
- **Model/tooling:** `/home/debian/.gemini-api-key` is absent in this sandbox,
  so no Gemini-backed `LOOK=1` description was possible, and this run's file
  reader cannot display PNGs. Screenshots were taken (`make ui-screenshots`
  of `/next/sessions`, `/next/sessions/director` and `/next/tasks`, and the
  sending, queued and not-sent bubbles in light and dark). Layout was judged
  through DOM geometry instead:
  - horizontal overflow and interactive elements outside the viewport
  - bounding boxes of the log, composer, Send and the bottom bar
  - `elementFromPoint` hit tests on Send and on messages
  - axe-core
  No screenshots or browser artifacts are committed.

## Stubs

Only external or live boundaries were intercepted, with Playwright
`page.route` in the walkthrough scripts. Navigation, rendering, the server
and the browser stayed real.

| Boundary | Stub | Why |
| --- | --- | --- |
| `POST /api/tasks/{id}/comments` | held for a moment, then passed to the demo | a slow post, to see what a second send does while the first is in flight |
| `POST /api/tasks/{id}/comments` | answered `503 {"error": "…restarting"}` once | a session host that is restarting, for the not-sent state and Retry |
| `GET /api/config` | the real answer with `access` replaced by `{full: false, scopes: [tasks:read, sessions:read, repos:read, ci:read]}` | a viewer without write scopes (the identity proxy that makes a real viewer is not in the demo; `next-walkthrough-daemon-rbac-repos.spec.js` drives a real one in the suite) |
| `**/api/**` | held without an answer for 50 s, then released | a daemon that accepts connections but never answers, and recovery without a reload |

No session manager, live stream or device-metrics stub was needed: the demo
answers the console-session routes and queues each message's run.

## Steps and outcomes

### 1. Every page at every width (`/next/`, Chromium and WebKit)

A sweep opened 37 addresses at `1440×900`, `820×1180`, `390×844` and
`320×568` (148 page loads):

- Inbox, Workstreams, Tasks, Agents, `task/1`
- Sessions, `sessions/director` and `/next/director`
- Processes and its four tabs, CI
- Repos, `repos/acme/widgets` and its tasks/settings/ci/advanced tabs
- Telemetry, Context, Fleet and its three tabs, `system/metrics`
- Settings and its access/models/git/config-repo/upgrade/repos sections
- `t/acme/tasks`, classic `/boards` and `/next/boards`

For each page it measured horizontal page overflow, interactive controls
outside the viewport (ignoring ones inside a horizontally scrolling
strip), the `h1` count, console errors and failed requests.

- **Outcome:** no overflow, no offscreen control and exactly one `h1` on
  every page at every width.
- The only failed request was `GET /api/state-repo/files` → `501` on
  Settings → Config repo. That is expected (the demo has no config repo),
  and the page reports it.
- `/next/director` → `/next/sessions/director` and `/next/system/metrics` →
  `/next/telemetry`. `/next/t/acme/tasks` → `/next/tasks` (the 11-15 fix
  holds). `/next/boards` falls back to the Inbox: Boards is classic-only.
- WebKit, over 13 of those pages at `1440×900`, `820×1180` and `390×844`
  (39 loads): the same, all clean.

### 2. Rail, create menu and viewer role (`/next/`)

- The create menu (New, opened from the rail) lists New task, Start a
  workstream, New session, Task from template, Run a suite, New schedule
  and New board. New session opens the New session dialog.
- **Viewer role** (stubbed `access`, see Stubs), on `sessions/director`,
  `sessions`, `tasks`, `task/1` and `inbox` at desktop and phone:
  - Sessions renders no New session button, composer, Delete or Grants &
    settings.
  - The rail's New task is disabled, and every item in the create menu is
    a disabled button; clicking New session through one opens nothing.
  - The phone New-task button is disabled.
  - No overflow and no console errors.

### 3. Sessions (`/next/sessions`), all widths, Chromium

Run at `1440×900`, `1024×768`, `820×1180` and `390×667`:

- **New session:** the dialog opens from the create menu and from the page
  with focus in **Name**, and Start session stays inside the viewport on the
  short phone. Escape closes it with focus back on **New session**.
- **First message:** a long name, and a first message with markdown, a table
  and a very long code line, open `/next/sessions/<slug>` with the composer
  focused. The first message renders at once (table included), the status
  reads *"Starting: A run is queued for your last message"*, and the page
  doesn't overflow.
- **Back-to-back sends:** on `1024×768` the second of two quick Enters was
  ignored, its text left in the composer, and nothing showed the first as
  sending — **bug F1**.
- **Rail:** the session is the rail's `aria-current` item.
- **Settings:** Grants & settings shows model (Deployment default), network
  (Open), Keep running, Tasks approved, Reads all and the API-scope grants.
  There is no overflow.
- **Persistence:** a reload keeps the session, its name and its messages.
  Back from the Director returns to it.
- **Delete:** asks first, with focus on Cancel. Confirming returns to
  `/next/sessions`, with focus dropped on `<body>` — **bug F2**.
- **Deleted session:** its deep link afterwards reads *"That session does not
  exist. Back to sessions"*, not another session's chat.

After F1/F2 were fixed:

- **Two quick Enters:** the composer clears at once, and the messages show
  as *Sending…* and *Queued*. Both post in order, each once, and are the
  conversation after a reload.
- **A post answered 503:** stays as *Not sent* with the server's reason,
  Retry and Edit. Retry sends it and hands focus back to the composer.
- **Delete:** focuses the Director's title on desktop and tablet, and the
  Sessions heading on the phone list.
- **axe-core** over the chat pane with sending, queued and not-sent bubbles,
  light and dark, `1440×900` and `390×844`: **no serious or critical
  violations**, `color-contrast` included.

### 4. Short phones (`/next/sessions/<id>`)

Measured on the Director at five sizes:

| Viewport | Log (px) | Composer | Bottom bar | Send reachable |
| --- | --- | --- | --- | --- |
| 320×568 | 403–443 (40) | 443–618 | 512–568 | **no**, under the bar and off screen |
| 390×667 | 359–400 (41) | 400–575 | 611–667 | yes, with a 41px conversation |
| 390×844 | 359–577 | 577–752 | 788–844 | yes |

The chat head (title, status, model, Canvas, Grants & settings, Delete),
the grants row and the fresh-chat note wrap to about 300px above the log,
and the page itself doesn't scroll — **bug F3**. After the fix, the log is
292px or more at every phone size, Send's centre hit-tests to Send above
the bottom bar at 320×568, 390×667, 390×844, 820×1180 and 1440×900, and a
message just sent sits above the composer (hit-tested, not hidden under it).

### 5. WebKit (`/next/sessions`)

Steps 3's walk repeated in WebKit at `1440×900` and `390×844`. Queue,
not-sent, Retry and Delete behave as in Chromium. New session's **Name**
was unfocused at `1440×900` on one run and `390×844` on another: focus
stayed on `<body>` — **bug F4**. After the fix, Name took focus and Delete
handed focus over on three runs at each width.

### 6. Task-bound interactive chat and Pause/Resume (`/next/task/<id>`)

On a session's task at `1440×900` and `390×844`:

- Ctrl+Enter sends (the composer says so), and two sends both land.
- Pause pauses at once (only Pause immediately asks first). The page shows
  Paused with Resume, and it stays so across a reload.
- Resume brings Pause back.
- No overflow and no console errors.

With the reply's post held, the composer is disabled with the text still in
it, and no bubble shows the message as sending. The next reply cannot be
typed until the post answers. Not a dropped message, but unlike Sessions
now; proposed as a follow-up (below).

### 7. Unresponsive daemon (`/next/`)

With every `/api/**` call held for 50s on `/next/tasks`, no alert or banner
appears. Only the footer's *"Updated 50s ago"* grows. Once the calls are
released, it recovers (*"Updated 0s ago"*) without a reload. This is G1
from the 2026-11-15 walkthrough, still open; its follow-up was proposed
then, so none is filed again.

### 8. Classic-only surfaces (`/`)

These have no `/next/` page or control yet, and ran against classic:

- **Header rail** (`/tasks`):
  - At `1440×900`, the alerts bell, System and Settings sit at the row's end
    (x = 1288, 1332, 1376 of 1440), with no overflow. No team, team switcher,
    Admin mode or "All teams" text appears.
  - At `1024×768` and `820×1180`, **More page actions** shows. It opens on
    click and on Tab into a wrapped action, closes on Escape with focus back
    on it, and closes on an outside press. The header stays one row (77px).
- **Collapse sidebar / Expand sidebar and `[`:**
  - The collapsed state survives a reload.
  - `[` expands it again; `[` typed into a field is typed instead.
  - Same at `1440×900` and `1024×768`.
- **Staging:**
  - `docs-preview` → `/staging/docs-preview/` and `grain-staging` →
    `http://127.0.0.1:4280/`, each with its Pin button.
  - The rail's counts (inbox 14, tasks 22, blocked 1, boards 4, sessions,
    processes 9, repos 1) render.
- **UserTile:** `alice@example.com (access code)` opens Settings → Access. No
  Log out shows, which is correct without verified identity.
- **Build stamp:** opens Settings → Upgrade.
- **Boards** (`/boards`): no overflow at `1440×900` and `1024×768`.
- **Phone drawer** (`390×844`):
  - The bar holds Open navigation, Alerts, Settings and New task.
  - The drawer is a modal dialog ("Navigation", 232×844) that locks
    background scroll (`overflow: hidden`). It holds the System row, the
    UserTile ("Acting as …") and the build stamp at its foot (y 807–830 of
    844).
  - Tapping Sessions navigates and closes it. Escape closes it with focus
    back on Open navigation. Customize sidebar opens its dialog from it.
  - The demo's dev pseudo-version stamp is ellipsised (as on 11-15; a
    release stamp is far shorter).

### Not available in `/next/` yet

Boards, a collapsible rail and `[`, pinned items and Customize pins, the
Staging list, the classic UserTile and build stamp at the rail's foot, the
"More page actions" header rail and alerts bell, a phone drawer, and a
disconnected banner (G1). Those steps ran against the classic UI, as
recorded above.

## Bugs found and fixed (`/next/`)

| | Bug | Reproduction | Fix |
| --- | --- | --- | --- |
| F1 | A message sent while the one before was still posting was ignored, and nothing showed a message as sending. | In a session, hold `POST /api/tasks/{id}/comments`, type and Enter, type again and Enter: the second text stays in the composer and only one post is made. | `sessions.store.ts`: a per-session outbox. The composer clears at once, messages show as Sending…/Queued and post in order. A refused one stays as Not sent with Retry and Edit, and Send is no longer disabled while posting. |
| F2 | Delete dropped focus on `<body>`: it navigates to `/sessions`, a separate route, then asked the torn-down page to focus. | Delete a session and confirm; read `document.activeElement`. | Delete navigates with a `focusHeading` state. The list page focuses the visible chat title, or the Sessions heading. |
| F3 | On short phones the conversation was 40px tall, and at 320×568 Send was below the bottom bar, unreachable. | Open `/next/sessions/director` at 320×568 and hit-test Send. | At phone widths the chat pane scrolls as a whole, the log keeps `min(18rem, 45dvh)`, and the composer is sticky at the pane's foot. A sent message scrolls the pane so it sits above the composer. |
| F4 | In WebKit, New session's Name was sometimes not focused: a bare `setTimeout` ran before the dialog rendered. | Open New session in WebKit at 1440×900 and read `document.activeElement` (BODY). | The dialog, Cancel and Delete focus calls use the existing per-frame `focusWhenRendered`. |

Regression spec: `ui-ng/e2e/next-session-send-queue.spec.js`.

- **F1 and F2:** Sending/Queued, in-order posts, Not sent → Retry with focus
  back on the composer, reload persistence, and Delete's focus, at
  `1440×900` and `390×667`. It failed on the base build at its first
  assertion (the composer still held the first message).
- **F3:** a log of at least 200px, Send above the bottom bar and on top at
  its centre, and a sent message above the composer, at `320×568` and
  `390×667`. The base build's geometry fails these (40px log, Send under
  the bar), as measured above.
- **F4** is WebKit-only and the suite is Chromium-only, so it was verified
  by the scripted WebKit runs above.

Unit tests (`sessions-page.component.spec.ts`) were rewritten for the queue:
a second Enter on the emptied composer sends nothing, messages queue in
order, and a failure is marked and then retried or taken back.

## Follow-up

- The task page's interactive chat (`/next/task/<id>`) disables its
  composer while a reply posts, keeps the text in it, and shows no sending
  bubble. Proposed separately: it has reply-target and refusal checks of
  its own (`task.store.ts` `send`), so it is not the same change as F1.
- G1 (no `/next/` disconnected banner) remains, as recorded on 11-15.

## Tests run

- `npx playwright test` in `ui-ng/` (the `make test-e2e-ng` suite: desktop,
  phone and dark projects) on the second fix's build: **324 passed, 2
  skipped** (6.8 min).
- The session specs (`next-session-send-queue`,
  `next-shell-sessions-walkthrough`, `next-responsive-sessions`,
  `next-sessions-canvas`, `next-walkthrough-director-sessions-diff`,
  `next-director-overlay`) on the final build: **21 passed**.
- `npx vitest run` in `ui-ng/`: **61 files, 620 tests passed**.
- `npx eslint` and `npx prettier --check` on the changed files: clean.
- The production build's existing initial-bundle budget warning (463.62 kB
  of 450 kB) is the same on the base and is not from this change.
