# Responsive Navigation & Live-Session UI Walkthrough — 2026-11-20

This walkthrough (task 7551) exercised the shared shell and the live-session
surfaces at desktop, tablet and phone widths, in real Chromium and WebKit
browsers driven by Playwright, against a real `grain demo` server. Following
`PROMPT.md` ("Two UIs"), every step ran on `/next/` where
`ui-ng/src/app/shell/nav.ts` or `ui-ng/src/app/app.routes.ts` has the page.
Classic `/` was used only for what `/next/` does not have yet.

Two `/next/` bugs were found and fixed on this branch (B1, B2 below), with
regression coverage in `ui-ng/e2e/next-tasks-order-create-menu-keys.spec.ts`.
No classic page needed a fix.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-7551-walk-through-responsive`.
- **Base commit tested:** `2c44ffa0b` (`main` at the start of the run). The
  fixes were re-verified on a rebuild of this branch.
- **Build:** `make frontend` (`npm ci` + `npm run build` in `ui/`, `ui-ng/`,
  `ui-hub/`), then `go build ./cmd/grain`. After each fix: `ui-ng` `npm run
  build` and a fresh `go build`.
- **Server:** `grain demo -addr 127.0.0.1:4290 -open=false -staging
  -host-mode-install -access-code-file <file>`, over its throwaway embedded
  store (`TMPDIR` pointed at a scratch directory). The browser signed in with
  the access code as `alice@example.com`. `-staging` seeds the Staging list;
  `-host-mode-install` turns on the install notice.
- **Browsers:** headless Chromium 1234 and WebKit 2336
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `playwright` from
  `ui-ng/node_modules`, Node 20.19).
- **Viewports:** `1440×900` desktop; `1024×768` iPad landscape and
  `820×1180` iPad portrait (touch); `390×844`, `390×667` and `320×568`
  phones (touch, `isMobile`); `844×390` and `568×320` phone landscape for
  orientation changes; light and dark, with `reducedMotion: reduce` for the
  axe and motion passes.
- **Model/tooling:** there is no `/home/debian/.gemini-api-key` in this
  sandbox, so no Gemini-backed visual review was possible. Layout was judged
  through DOM geometry instead: `scrollWidth` overflow, interactive controls
  outside the viewport (ignoring horizontally scrolling strips),
  `elementFromPoint` hit tests at each control's centre, bounding boxes, and
  axe-core (`wcag2a`, `wcag2aa`).
- **Scripts:** throwaway files outside the checkout (`sweep`, `shell`,
  `sessions`, `classic`, `segscan`, `createkb`, `inspect`, `rects`), each
  driving the UI's own controls. No screenshots or browser artifacts are
  committed.

## Stubs

Only external or live boundaries were intercepted, with Playwright
`page.route`. Navigation, Angular/React rendering, the server and browser
layout stayed real.

| Boundary | Stub | Why |
| --- | --- | --- |
| `**/api/**` | held unanswered, then released | a daemon that accepts connections and never answers, then answers again |
| `**/api/**` | aborted `connectionrefused`, then unrouted | a daemon refusing connections, then back |
| `POST /api/console-sessions/*/messages`, `POST /api/tasks/*/comments` | held until released, then passed to the demo | a slow post, to see the sending and queued states |
| `GET /api/console-sessions` | `500 {"error": "session store unavailable"}` | a session store that cannot be read |
| `GET /api/config` | the real answer with `access` replaced by `{full: false, scopes: [tasks:read, sessions:read, repos:read, ci:read]}` | a viewer without write scopes (the demo has no identity proxy) |

No session-manager, live-stream or device-metrics stub was needed: the demo
answers the console-session routes and queues a run for each message.

## Steps and outcomes

### 1. Every page at every width (`/next/`, Chromium; subset in WebKit)

**Chromium:** 35 addresses × 5 sizes (`1440×900`, `1024×768`, `820×1180`,
`390×844`, `320×568`), 175 loads: Inbox, Workstreams, Tasks, Agents,
`task/1`, `new`, Sessions, `sessions/director`, `director`, Processes and
its schedules/templates/suites tabs, CI, Repos, `repos/acme/widgets` and its
tasks/ci/advanced tabs, Telemetry, `system/metrics`, Context, Fleet,
Settings and its general/access/models/git/config-repo/upgrade/repos/ci
sections, `t/acme/tasks`, `/next/boards` and classic `/boards`.

- **No horizontal overflow and no off-screen controls** at any size.
- **Hit-test flags** were all controls scrolled under a sticky bar inside a
  scroll container (the Settings drawer under the build-stamp footer, list
  rows under the phone compact nav, the chat head under the sticky
  composer); each was confirmed reachable by scrolling with a per-element
  clip/hit inspection. The one real clip was **B1** (Tasks Order at tablet
  widths).
- **Console/network:** clean except `GET /api/state-repo/files` → `501` on
  Settings → Config repo, which the demo does not implement (it has no
  config repo); the page still renders its forms.
- **Redirects:** `/next/director` → `/next/sessions/director`;
  `/next/system/metrics` → `/next/telemetry`; `/next/t/acme/tasks` →
  `/next/tasks`; `/next/new` and `/next/boards` → Inbox (New task is a sheet,
  Boards is classic-only).

**WebKit:** 12 addresses × `1440×900`, `820×1180`, `390×844`: same
results (no overflow, no off-screen controls, the same B1 flag).

### 2. Rail, keyboard and resizing (`/next/`, `1440×900`)

- **Rail destinations:** Inbox (with its `13 waiting` count), Workstreams,
  Tasks, Agents, Sessions, Processes (`9`), CI, Repos, Telemetry, Context,
  Fleet, Settings — each click lands on its page as the rail's only
  `aria-current="page"` item, without overflow. Back/Forward step through
  the history.
- **Create menu:** Enter on **New** opens New task, Start a workstream,
  Prototype a goal, New session, Task from template, Run a suite, New
  schedule, New board. Tab walks into it; Escape closes it with focus back
  on New; an outside press closes it. **ArrowDown closed it** instead of
  moving into it — **B2**.
- **Rail More:** opens Customize navigation; Escape returns focus to More.
- **`c` shortcut:** opens New task with focus in its title; Escape closes.
- **Resizing** to `390×844` with the menu open closes it, no overflow;
  resizing back is clean.
- **Team-less links:** `/next/t/acme/tasks` → `/next/tasks`,
  `/next/t/acme/inbox` → `/next/inbox`, `/next/t/acme/sessions/director` →
  `/next/sessions/director`, `/next/t/acme` → `/next/inbox`. No "All
  teams", "Admin mode" or team switcher text.

### 3. Phone compact nav and More sheet (`390×844`, `320×568`, touch)

- **Top bar:** Grain home, Director, theme switch, Help, Account (`alice…`).
- **Compact nav** pinned at the bottom (y 788 / y 512): Inbox, Tasks,
  Sessions, More.
- **More sheet:** 390×772 (320×496), locks background scroll (`body` and
  `html` `overflow: hidden`), lists the other nine destinations, the whole
  create menu and Customize navigation. Escape closes it with focus back on
  More; tapping Fleet navigates and closes it.
- **Phone New task:** full-screen sheet, focus in its title; Escape hands
  focus back to the New task button.
- **Orientation:** `844×390` and `568×320` landscape are clean.

### 4. Disconnected banner and install notice (`/next/tasks`)

- **Held connections:** the banner appears after ~12.4 s ("accepted the
  connection but sent no answer within 10 seconds. Unreachable since …"),
  spans the top (1382×36 desktop, 332×91 phone) without overflow, and
  clears without a reload once answered (5 ms desktop, 3.3 s phone).
- **Refused connections:** "the connection to it failed" within 2.3–2.8 s;
  clears 0.8–2.8 s after connections are allowed again.
- **Host-mode install notice** is shown alongside.

### 5. Sessions (`/next/sessions`; Chromium `1440×900`, `1024×768`, `390×667`; WebKit `1440×900`, `390×844`)

- **New session:** from the rail's create menu (desktop/tablet) and the
  page's button (phone); focus lands in **Name**; Start session stays in the
  viewport on the short phone (y 552–592 of 667).
- **First message** (markdown, a table, a 240-character code line, a long
  session name) opens `/next/sessions/<slug>` and shows within 141–624 ms;
  the composer is focused; status reads "Starting: A run is queued for your
  last message"; the new session is the rail/list's `aria-current` item.
- **Queued sends:** with posts held, two quick Enters show "Sending…" and
  "Queued"; both land and survive a reload.
- **Grants & settings** opens without overflow.
- **Delete:** confirmation focuses Cancel; Escape closes it with focus back
  on Delete; confirming returns to `/next/sessions` with focus on the
  Director heading (desktop/tablet) or the Sessions heading (phone). The
  deleted session's deep link says it does not exist, without another
  session's chat.
- **Failed `GET /api/console-sessions`:** `/next/sessions` and
  `/next/sessions/director` show the load error, neither "does not exist"
  nor "No sessions yet", and recover on their own once the read answers.

### 6. Task-bound interactive chat (`/next/task/13`, `1440×900`)

Pause pauses and the Paused state survives a reload; Resume brings Pause
back.

### 7. Viewer role (`/next/`, stubbed `access`)

On Inbox, Tasks, `sessions/director` and Settings → Access, at desktop and
phone: the rail/phone New task is disabled (or not offered), Sessions
renders no New session, composer or Delete, no overflow, no console errors.

### 8. Accessibility, themes and motion (`/next/`)

- **axe-core** (`wcag2a`, `wcag2aa`, colour contrast included) over Inbox,
  Tasks, `sessions/director`, Settings → Access, Fleet and Repos, light and
  dark, desktop and phone, reduced motion: **no serious or critical
  violations** (24 scans).
- **Reduced motion:** no running animations on the Director.

### 9. Classic-only surfaces (`/`)

- **Header rail** (`/tasks`): at `1440×900` Alerts, System, Settings sit at
  the row's end (x 1288–1412); at `1024×768` and `820×1180` More page
  actions shows before them, and Escape closes it with focus back on it.
- **`[` shortcut:** collapses the sidebar, survives a reload, is ignored
  while typing in "Search tasks…", and expands it again.
- **Boards** (`/boards`): no overflow at the three widths.
- **Old team links:** `/t/acme/tasks` → `/`, `/t/acme/sessions` →
  `/sessions`.
- **Phone drawer** (`390×844`): a 232×844 "Navigation" dialog that locks
  background scroll; Staging links `/staging/docs-preview/` and
  `http://127.0.0.1:4280/`; the build stamp at its foot (y 807–830 of 844);
  Escape returns focus to Open navigation. (`Signed in as`/`Log out` are
  only shown behind verified identity; the access-code session shows the
  operator label.)
- **Observation:** at `820×1180` the classic Tasks "Backlog order" button's
  centre hit-tests to the `select-all` row beneath it. Classic Tasks is
  being replaced by `/next/tasks` and the button stays clickable on its
  upper part, so it was not changed here.

### Not available in `/next/` yet

These ran against classic: Boards, the collapsible rail and `[`, pinned
items and Customize pins, the Staging list, the UserTile and build stamp at
the rail's foot, the "More page actions" header rail and alerts bell, and
the slide-in drawer (`/next/` uses the compact nav and More sheet). The
`/next/` New session dialog has no "Pin to sidebar" (no pins in `/next/`).

## Bugs found and fixed (`/next/`)

| | Bug | Reproduction | Fix |
| --- | --- | --- | --- |
| B1 | On a tablet the Tasks list pane beside the open task is 260–290px wide, and the Backlog's four-segment **Order** control (~290px) shrank under its `overflow: hidden`, clipping Oldest and Newest out of reach. | Open `/next/tasks` at `820×1180` or `1024×768`: `.segmented[aria-label=Order]` has `scrollWidth` 284 vs `clientWidth` 217/243, and Newest's centre hit-tests to the task pane. | `tasks-page.component.{ts,css}`: below a 380px list pane (container query) the segments give way to one "Order" `g-pill-select`; the segments no longer shrink. |
| B2 | ArrowDown on the rail's open **New** menu scrolled the rail, and a rail scroll closes floating menus, so the menu shut under the keyboard. | `/next/inbox` at `1440×900`: focus New, Enter, ArrowDown → menu gone, focus still on New. | `app.component.ts`: ArrowDown/ArrowUp on New opens the menu on its first/last entry; inside it ArrowUp/ArrowDown (wrapping), Home and End move between entries. |

**Regression spec:** `ui-ng/e2e/next-tasks-order-create-menu-keys.spec.ts`
(order reachable at `820×1180` and `1024×768`, the choice kept when widened;
arrow/Home keys through the create menu, opening New session from the
keyboard, Escape back to New). On the base build (fix stashed, rebuilt) all
three tests failed; with the fix all pass.

## Follow-up

None filed: both bugs are fixed here; the classic Tasks observation in step 9
is on a page being replaced by `/next/`.

## Tests run

- `npx playwright test` in `ui-ng/` over
  `next-tasks-order-create-menu-keys`, `next-saved-boards`,
  `next-goal-mode`, `next-shell-sessions-walkthrough`,
  `next-responsive-sessions`, `next-full-system`, `shell` and
  `next-tasks-batch-actions` (all projects): **45 passed**.
- `npx vitest run src/app/app.component.spec.ts src/app/tasks`: **5 files,
  101 tests passed**.
- `npx eslint` and `npx prettier --check` on the changed files: clean.
