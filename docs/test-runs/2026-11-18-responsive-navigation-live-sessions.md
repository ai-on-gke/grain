# Responsive Navigation & Live-Session UI Walkthrough — 2026-11-18

This walkthrough exercised the shared shell (the `/next/` rail, create and
More menus, the phone compact nav and More sheet, the disconnected banner,
and, where `/next/` has no equivalent, the classic header rail, sidebar and
drawer) and the live-session surfaces at desktop, tablet and phone sizes.
The session surfaces were Sessions, the Director, New session, sending,
session settings, Start a fresh chat, Delete, and Pause/Resume on a
task-bound chat. Every step ran in a real Playwright browser against a real
`grain demo` server (task 7022).

Following `PROMPT.md` ("Two UIs"), each step ran against the Angular UI at
`/next/` wherever `ui-ng/src/app/shell/nav.ts` or `ui-ng/src/app/app.routes.ts`
has the page. A step ran against the classic UI at `/` only where `/next/`
has no such page or control. Five `/next/` bugs were found and fixed on this
branch, with Playwright coverage in
`ui-ng/e2e/next-session-settings-focus.spec.js`. One gap (inline approval
cards in `/next/sessions`) is proposed as a follow-up. No classic page
needed a fix.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-7022-walk-through-responsive`.
- **Base commit tested:** `848f5271c` (`main` at the start of the run). The
  fixes were re-verified on builds of `6678a43c4` and `44689fe70`.
- **Build:** `make frontend`, then `go build -o grain ./cmd/grain`. For each
  fix, `ui-ng`'s `npm run build` into `pkg/ui/static-next` and a fresh
  `go build`.
- **Server:** `grain demo -addr 127.0.0.1:4290 -open=false -staging
  -host-mode-install -access-code-file <file>` over its throwaway embedded
  SQLite store. The browser signed in with the access code as
  `alice@example.com (access code)` (operator). `-staging` seeds the staging
  list, and `-host-mode-install` turns on the host-mode install notice. The
  Playwright e2e runs used the suite's own per-worker demo
  (`ui-shared/e2e/demo.js`).
- **Browsers:** headless Chromium 1234 and WebKit 2336
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `@playwright/test` from
  `ui-ng/package.json`, Node 20.19).
- **Viewports:**
  - desktop: `1440×900`, plus `1440×600` and `1280×600`
  - tablet: `1024×768` iPad landscape, `820×1180` iPad portrait (touch),
    plus `1180×820`, `1024×600` and `960×540`
  - phone: `390×844` and `390×667` (touch, `isMobile` in Chromium),
    `320×568`, and landscape `844×390` and `667×375`, with a live rotation
    from `390×844` to `844×390`
  - light and dark (`colorScheme`), and reduced motion
- **Model/tooling:** `/home/debian/.gemini-api-key` is absent in this
  sandbox, so no Gemini-backed `LOOK=1` description was possible, and this
  run's file reader cannot display PNGs. Screenshots were taken
  (`ui/e2e/screenshot.mjs` of `/next/sessions`, `/next/sessions/director`,
  `/next/inbox` and `/next/tasks` at desktop and phone, light and dark, plus
  the settings panel with the network warning, the open grant builder and
  the Director at `960×540`). Layout was judged through DOM geometry
  instead:
  - horizontal page overflow
  - interactive controls outside the viewport (ignoring controls inside a
    horizontally scrolling strip)
  - bounding boxes of the log, composer and Send
  - `elementFromPoint` hit tests
  - `document.activeElement` after every open and close
  - axe-core
  No screenshots or browser artifacts are committed.

## Stubs

Only external or live boundaries were intercepted, with Playwright
`page.route` in the walkthrough scripts. Navigation, rendering, the server
and the browser stayed real.

| Boundary | Stub | Why |
| --- | --- | --- |
| `GET /api/console-sessions` | answered `500 {"error":"store unavailable"}` | a failing sessions read, for the load error |
| `POST /api/console-sessions` | held for a moment, then passed to the demo | a slow create, to see what New session shows while it starts |
| `GET /api/console-sessions` | the demo's own list, with one session's `networkAccess` set to `none` and `effectiveNetworkAccess` to `open` | a deployment whose sandboxes cannot withhold a network (`-host-sandboxes`); the demo's can, so it never sends the field |
| `GET /api/tasks/{director task}` | the demo's own detail, plus a `pendingConfigChange` and `state: awaiting_reply` | a session parked on a config-repo change, which the demo does not seed |
| `GET /api/config` | the real answer with `access` replaced by `{full: false, scopes: [tasks:read, sessions:read, repos:read, ci:read]}` | a viewer without write scopes (the identity proxy that makes a real viewer is not in the demo) |
| `**/api/**` | held without an answer, then released | a daemon that accepts connections but never answers, and recovery without a reload |

No session manager, live stream or device-metrics stub was needed: the demo
answers the console-session routes and queues each message's run.

## Steps and outcomes

### 1. Every page at every size (`/next/`, Chromium and WebKit)

**First sweep:** 35 addresses at `1440×900`, `1024×768`, `820×1180`,
`390×844` and `320×568` (175 loads):

- Inbox, Workstreams, Tasks, Agents, `task/1`
- Sessions, `sessions/director` and `/next/director`
- Processes and its three tabs, CI
- Repos, `repos/acme/widgets` and four of its tabs
- Telemetry, Context, Fleet, `system/metrics`, `system/hosts`
- Settings and six of its sections
- `t/acme/tasks`, `/next/boards`, classic `/boards` and `/t/acme/tasks`

**Second sweep:** at `1440×900`, `820×1180` and `390×844` (117 loads):

- every Settings section: models, sandboxes, hosts, repositories, git, CI,
  config repo, tasks, agents, capabilities, memory, access, audit,
  general, preferences, CLI, caches, engine, registry, upgrade
- every Fleet tab
- every repo tab: overview, CI & workflows, merge queue, agents & grants,
  branches, releases, advanced
- every classic System tab address (health, sandboxes, logs, pipeline,
  caches, metrics, upgrade)
- a schedule's page and Workstreams

**Outcome:** no overflow, no offscreen control and exactly one `h1` on
every page at every size.

- The only failed request was `GET /api/state-repo/files` → `501` on
  Settings → Config repo. That is expected (the demo has no config repo),
  and the page reports it.
- `/next/director` → `/next/sessions/director`.
- `/next/system/<tab>` lands on its new home: Fleet (with `?tab=logs` or
  `?tab=controls`), Telemetry, or Settings → Caches / Upgrade.
- `/next/t/acme/tasks` → `/next/tasks`.
- Classic `/t/acme/inbox`, `/t/acme/sessions` and `/t/acme/repos/acme/widgets`
  drop the team. Classic `/t/acme/tasks` → `/` (the classic task list).
- `/next/boards` falls back to the Inbox: Boards is classic-only, and
  classic `/boards` is clean at every size.

**WebKit**, over 13 of those pages at `1440×900`, `820×1180` and `390×844`
(39 loads), was clean except for one `TypeError: Importing a module script
failed` on the first two phone loads. That run overlapped the full e2e
suite on the same machine, and two clean reruns of the same pages did not
reproduce it. It is recorded here as not reproduced.

### 2. The `/next/` rail and phone navigation

- **Number shortcuts:** `1` opens Inbox, `3` Tasks and `,` Settings. Typed
  into the view filter, `1,2` is text (`?q=1,2`) and does not navigate.
- **More (rail):**
  - It opens on click.
  - Escape closes it with focus back on **More**.
  - An outside press closes it.
  - Tab from the button moves into the menu (*Customize navigation*), and
    Shift+Tab moves back.
  - Tabbing past the menu closes it.
- **Create menu:**
  - It lists New task, Start a workstream, Prototype a goal, New session,
    Task from template, Run a suite, New schedule and New board.
  - It opens from Enter with Tab onto New task.
- **Resizing with menus open:** desktop → phone closes the create menu, and
  phone → desktop closes the More sheet. Nothing is left floating, and the
  phone layout has no overflow.
- **Phone More sheet** (`390×844`):
  - It covers 390×772 above the bottom bar and locks background scroll
    (`body` `overflow: hidden`).
  - It lists the destinations not in the bar, the create items and
    Customize navigation.
  - Escape closes it with focus back on **More**.
  - Browser Back with it open navigates and closes it.
  - Back and Forward between pages work.
- **Phone top bar:** it holds the Grain mark, Director, theme, Help and the
  account button (`alice@example.com (access code)`). New task is the FAB.
  No team or switcher appears anywhere.
- **Viewer role** (stubbed `access`) on `sessions/director`, `sessions`,
  `tasks`, `task/1`, `inbox` and `settings/access`, at `1440×900` and
  `390×844`:
  - No New session, composer, Send, Delete, settings, Pause or Close
    control is enabled.
  - The rail's create button opens a menu of disabled items, as recorded on
    11-16.
  - No overflow.
- **Disconnected banner** (all `/api/**` held) at `820×1180`:
  - After 12s the banner reads *"This deployment cannot be reached: it
    accepted the connection but sent no answer within 10 seconds…"*, full
    width (820×53), with no overflow.
  - It clears without a reload once the calls are released.
  - Desktop and phone are covered by `next-disconnected-banner.spec.js`,
    which passed in the suite run below.

### 3. Sessions (`/next/sessions`), Chromium and WebKit

- **New session:**
  - The dialog opens with focus in **Name**.
  - Tab stays inside it (14 presses), and the page behind does not scroll.
  - Its model picker offers *Deployment default (gemini-2.5-pro)*,
    gemini-2.5-pro and gemini-2.5-flash. Escape in the picker closes only
    the picker, with focus back on it and the dialog still open.
  - With the create held, the dialog stays with **Start session** disabled.
  - Once created, `/next/sessions/<slug>` opens with the composer focused.
    The first message (markdown and a table) shows at once, and the status
    reads *"Starting: A run is queued for your last message"*.
  - Two follow-ups sent while it starts both land. After a reload, the
    conversation and the list's selected row (`aria-current`, named and
    showing the last line) persist.
  - `/next/` has no Pin to sidebar or pinned sessions: that is the classic
    rail's.
- **Settings:**
  - The panel shows model, network, Keep running, Tasks approved, Reads all
    (on by default) and the API-scope and method grants.
  - Network → None and model → gemini-2.5-flash each save with
    `PUT /api/console-sessions/{id}` and survive a reload.
  - Ticking **Tasks approved** posted `POST /api/tasks/24/approve` instead
    and reverted on reload — **bug B1**.
  - No warning appears when `none` cannot be honoured — **bug B2**.
  - **+ Add API scope or method grant** dropped focus on `<body>`, and
    Escape then closed the whole settings panel — **bug B3**.
  - Grant edits from a stale tab are sent with `expectCapabilities` and
    retried once on a `409 stale`, as on classic.
- **Start a fresh chat:** opening, cancelling (or Escape) and starting
  clean each left focus on `<body>` — **bug B4**. Starting clean empties
  the chat back to the Director's greeting on the same address.
- **Delete:**
  - It asks first with focus on Cancel, and Escape closes it with focus on
    Delete.
  - Confirming focuses the Director's title (desktop) or the Sessions
    heading (phone).
  - The deleted session's deep link, before and after a reload, lists the
    sessions with no chat pane, not another session's chat.
- **Failed list read** (stubbed 500): *"Sessions could not be read: store
  unavailable. This page tries again on its own."*, and no empty state.
- **Reduced motion:** the live typing dots run no animation
  (`animation-name: none`).
- **WebKit** at `1440×900` and `390×844`: the same focus path (Name →
  composer → builder → fresh chat → Delete), once the fixes were in.

### 4. Short and rotated screens (`/next/sessions/director`)

| Viewport | Log before (px) | Send before | Log after (px) | Send after |
| --- | --- | --- | --- | --- |
| 960×540 | 40 | bottom 575, **off screen**, not on top | 283 | in view, on top |
| 1024×600 | 40 | in view | 292 | in view |
| 1280×600, 1440×600 | 105 | in view | 292 | in view |
| 1024×768 | 189 | in view | 189 (rule not applied) | in view |
| 1440×900 | 405 | in view | 405 (unchanged) | in view |
| 844×390, 667×375 | phone rule | in view, on top | unchanged | in view, on top |
| 320×568, 390×667, 390×844 | phone rule | in view, on top | unchanged | in view, on top |

The 11-16 fix (F3) made the chat pane scroll as a whole only at phone
widths and in very short landscape. A short screen that keeps the list
beside the chat still lost the conversation to the wrapped head, and Send
became unreachable at 960×540 — **bug B5**. Rotating `390×844` →
`844×390` keeps Send on top. No size overflows.

### 5. Task-bound interactive chat (`/next/task/<id>`)

On a session's task at `1440×900` and `390×844`:

- Pause shows Resume, which survives a reload, and Resume brings Pause
  back.
- A Ctrl+Enter reply lands in the conversation.
- No overflow and no console errors.

### 6. Inline approval cards

With the Director's task stubbed to carry a `pendingConfigChange`, the
`/next/task/<id>` page shows the gate card (*"Waiting for configuration
change: Raise the director's model budget"*). `/next/sessions/director`
shows nothing of it: neither the card nor its approve or decline. Classic
sessions render `request_access`, local-script and config-change cards
inline (`TaskChat.jsx`). This is a missing `/next/` feature rather than a
regression, and is proposed as a follow-up (below).

### 7. Classic-only surfaces (`/`)

These have no `/next/` page or control yet, and ran against classic:

- **Header rail** (`/`):
  - It is one row, 77px tall, at `1440×900`, `1024×768` and `820×1180`.
  - The alerts bell, System and Settings sit at its end (x = 1324, 1368,
    1412 of 1440).
  - **More page actions** appears at `1024×768` and `820×1180`, and Escape
    closes it with focus back on it.
  - No team, team switcher, Admin mode or "All teams" appears.
- **Collapse sidebar:** the collapsed state survives a reload, and `[`
  expands it again.
- **Staging:** `docs-preview` → `/staging/docs-preview/` and
  `grain-staging` → `http://127.0.0.1:4280/`.
- **Build stamp:** it sits at the foot of the sidebar, which scrolls
  (923px of content in an 832px rail at `1440×900`). Scrolled into view, it
  sits flush at the rail's foot and hit-tests to itself at `1440×900`,
  `1440×700` and `1024×768`, so it is not clipped.
- **Phone drawer** (`390×844`):
  - The bar holds Open navigation, Alerts, Settings and New task.
  - The drawer is a modal dialog ("Navigation", 232px wide) that locks
    background scroll. Its build stamp sits at y 807–830 of 844.
  - Escape closes it with focus back on Open navigation.

### Not available in `/next/` yet

Boards, a collapsible rail and `[`, pinned items and Customize pins, pinned
sessions and Pin to sidebar, the Staging list, the classic UserTile with
Log out and the build stamp at the rail's foot, the "More page actions"
header rail, the alerts bell, the phone drawer, and inline approval cards
in a session (step 6). Those steps ran against the classic UI, or are the
follow-up.

## Bugs found and fixed (`/next/`)

| | Bug | Reproduction | Fix |
| --- | --- | --- | --- |
| B1 | **Tasks approved** didn't save: the store posted `POST /api/tasks/{id}/approve` (approving the session's own task) and dropped the field, so the box reverted. | Open a session's settings, tick Tasks approved, reload: unticked, and `GET /api/console-sessions` says `tasksApproved: false`. | `sessions.store.ts` sends it with `PUT /api/console-sessions/{id}`, like every other setting. That PUT also retags the open task. |
| B2 | No warning when the deployment cannot honour `none` network (`effectiveNetworkAccess`), unlike classic. | Answer `GET /api/console-sessions` with `networkAccess: none, effectiveNetworkAccess: open` and open settings. | The settings panel shows the classic pane's warning (`session-network-not-isolated`). |
| B3 | **+ Add API scope or method grant** dropped focus on `<body>`, and Escape then closed the whole settings panel. | Open settings, click it, read `document.activeElement`, press Escape. | `grants-picker.component.ts` focuses the builder's selected kind. Escape or Done closes only the builder (the event is marked handled) and focuses the button again. This applies to every page that uses the shared picker. |
| B4 | Start a fresh chat swapped its button for its choices and back, dropping focus each time. | Click Start a fresh chat, press Escape or start clean, and read `document.activeElement`. | Focus moves to the first choice, back to the button on Cancel or Escape, and to the composer once started. |
| B5 | On short screens with the list beside the chat, the conversation was 40–105px tall, and at 960×540 Send was off screen with nothing to scroll. | Open `/next/sessions/director` at 960×540 and hit-test Send. | The phone rule (pane scrolls, log keeps `min(18rem, 45dvh)`, composer sticky) also applies at any width up to 760px tall. |

The design book (`design/design/ui-design-book.design.md` §8) gains the
rule B3 and B4 broke: a control swapped for what it opens hands focus
across both ways, and Escape inside an inner piece closes only that piece.

**Regression spec:** `ui-ng/e2e/next-session-settings-focus.spec.js`.

- **B1:** a PUT and no approve post, then the box and the API agree after a
  reload, ticked and unticked.
- **B2:** the warning shows, with axe in light and dark (`@dark`).
- **B3 and B4:** focus at each step, at `1440×900` and `390×844`.
- **B5:** log height, and Send in view and on top, at `960×540` and
  `1024×600`.

All four of its original tests failed on the base build (`848f5271c`'s
`ui-ng`) and pass with the fixes. B4's assertions sit after B3's in the
same test, and B4 was reproduced directly in step 3. B5's tests encode the
"before" geometry in step 4.

## Follow-up

- **Inline approval cards in `/next/sessions`** (step 6): proposed
  separately. It is a new piece of UI (the task page's gate cards, adapted
  to the chat pane), not a scoped fix.

## Tests run

- `npx playwright test` in `ui-ng/` (the `make test-e2e-ng` suite: desktop,
  phone and dark projects) on the first fix's build: **450 passed, 1
  failed, 2 skipped** (10.8 min).
  - The failure was `next-live-events-and-comment-links.spec.ts:153`
    (counted 4 comment reads where it allows 3, on a task page this branch
    does not touch), while the walkthrough scripts loaded the same machine.
  - It passed on rerun.
- The new spec plus `next-session-send-queue`,
  `next-shell-sessions-walkthrough`, `next-responsive-sessions` and
  `next-capability-policy-pickers`: **27 passed**. The new spec with the B5
  tests and `next-live-events-and-comment-links`: **11 passed**.
- `npx vitest run src/app/sessions src/app/shared`: **15 files, 114 tests
  passed**.
- `npx eslint` and `npx prettier --check` on the changed files: clean.
- The production build's initial-bundle (492.61 kB of 450 kB) and
  `agents-page.component.css` (8.47 kB of 8 kB) budget warnings are the
  same on the base build and are not from this change.
