# Responsive Navigation & Live-Session UI Walkthrough — 2026-11-15

This walkthrough exercised the shared shell (rail, top bar, phone compact
nav and More sheet, classic sidebar and drawer, banners) and the live-session
surfaces (Sessions, the Director, New session, session settings, Delete) at
desktop, tablet and phone widths, in a real Playwright browser against a real
`grain demo` server (task 5893, attempt 2).

Following `PROMPT.md` ("Two UIs"), each step ran against the Angular UI at
`/next/` wherever `ui-ng/src/app/shell/nav.ts` or `ui-ng/src/app/app.routes.ts`
has the page, and against the classic UI at `/` only where `/next/` has no
such page or control. Four `/next/` bugs were found and fixed on this branch
with Playwright regression coverage in
`ui-ng/e2e/next-shell-sessions-walkthrough.spec.js`.

## Environment & build tested

- **Repository:** `ai-on-gke/grain`, branch
  `grain/53549e42/task-5893-walk-through-responsive`.
- **Base commit tested:** `39e33c00d` (`main` at the start of the run; build
  stamp `v0.0.0-20261115135610-39e33c00dcdf+dirty`). Fixes re-verified against
  a fresh `go build ./cmd/grain` embedding the rebuilt `pkg/ui/static-next`.
- **Build:** `make frontend` (`ui`, `ui-ng`, `ui-hub` production bundles), then
  `go build -o grain ./cmd/grain`.
- **Server:** `grain demo -addr 127.0.0.1:4290 -open=false -staging
  -host-mode-install -access-code-file <file>` over its throwaway embedded
  SQLite store, signed in with the access code (`alice@example.com (access
  code)`, operator). `-staging` seeds the staging list; `-host-mode-install`
  turns on the host-mode install notice so it is exercised beside the other
  banners. The Playwright e2e runs use the suite's own per-worker demo
  (`ui-shared/e2e/demo.js`).
- **Browsers:** headless Chromium 1234 and WebKit 2336
  (`PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, `@playwright/test`
  1.62.1, Node 20.19). Viewports: `1440×900` desktop, `1024×768` iPad
  landscape (touch), `820×1180` iPad portrait (touch), `390×844` and `390×667`
  phone (touch, `isMobile`), `320×568` small phone, and `844×390` phone
  landscape for the orientation change. Light and dark (`colorScheme`),
  reduced motion for the axe pass.
- **Model/tooling:** `/home/debian/.gemini-api-key` is absent in this sandbox,
  so no Gemini-backed `LOOK=1` description was possible, and this run's file
  reader cannot display PNGs. Screenshots were taken (light/dark ×
  desktop/phone, see the closing note's contact sheet), but layout was judged
  through DOM geometry: horizontal overflow, interactive elements outside the
  viewport, dialog and action bounding boxes, and axe-core — not by eye.
  No screenshots or browser artifacts are committed.

## Stubs

Only external or live boundaries were intercepted, with Playwright
`page.route` in the walkthrough scripts; navigation, rendering, the server
and the browser stayed real.

| Boundary | Stub | Why |
| --- | --- | --- |
| `GET /api/console-sessions` | answered `500` | the load-error state of `/next/sessions` |
| `GET /api/tasks*` | aborted (`connectionrefused`) | the task feed going stale |
| `**/api/**` | held without an answer for 45–75 s, then released | a daemon that accepts connections but never answers, and recovery without a reload |

No session manager, live stream or device-metrics stub was needed: the demo
answers the console-session routes and queues the first message's run.

## Steps and outcomes

### 1. Every page at every width (`/next/`)

A sweep opened 55 `/next/` addresses at five viewports (275 page loads):
Inbox, Workstreams, Tasks, Agents, `task/1`, Sessions, `sessions/director`,
Processes and its four tabs, CI, Telemetry, Context, Fleet and its
`hostds`/`controls`/`logs` tabs, the classic System addresses
(`system/metrics`, `system/caches`, `system/upgrade`, which redirect to
Telemetry and Settings), `repos/acme/widgets` and all five repo tabs,
`settings`, every Settings section and group id, and `t/acme/tasks`. For each
it measured horizontal page overflow, interactive controls outside the
viewport (ignoring ones inside a horizontally scrolling tab strip), the
`h1` count, console errors and failed requests.

- **Outcome:** no horizontal overflow, no offscreen control and exactly one
  `h1` on every page at every width. The only failed request was
  `GET /api/state-repo/files` → `501` on Settings → Config repo, which is
  expected (the demo has no config repo) and the page reports it.
- `/next/t/acme/tasks` landed on the Inbox — **bug F1**.
- `settings/compute`, `code`, `work`, `security` and `instance` (group ids,
  not sections) open Models. Nothing links them; noted, not changed.

### 2. Rail and top bar, desktop (`/next/`, mouse and keyboard)

- Each rail destination (Inbox, Workstreams, Tasks, Agents, Sessions,
  Processes, CI, Telemetry, Context, Fleet) navigated and became the only
  `aria-current="page"` item. Back and Forward walked the history.
- The rail's Inbox link was named `"13Inbox"` — **bug F2**.
- Create menu (New task, Start a workstream, New session, Task from template,
  Run a suite, New schedule, New board): opens on click and on Enter, closes
  on Escape with focus back on **New**, on an outside press, and on Tab or
  Shift+Tab out.
- Account (`Account and access: alice@example.com (access code)`, role
  operator, Customize navigation…, Sign out): Escape closes it and gives focus
  back. Customize navigation from it: Escape closes the dialog and gives focus
  to the Account button. The rail's More menu: Escape gives focus back to
  **More**.
- `?` opens Help and Escape closes it. Typing `a?b` into the Tasks filter
  types it and opens nothing.
- Hard reload and deep link to `/next/task/1` render `#1 Add a dark mode
  toggle`.

### 3. Resizing and orientation (`/next/`)

`/next/tasks` resized through `1024×768` → `820×1180` → `390×844` →
`1440×900` → `844×390`: the rail shows at tablet and desktop, and the compact
nav and phone New-task button take over at phone widths and in phone
landscape. No overflow at any step.

### 4. Phone (`/next/`, touch)

At `390×844` and `320×568`: the compact nav (Inbox, Tasks, Sessions, More)
sits along the bottom edge, full width. Tapping each navigates. The More sheet
lists the other destinations, Settings and the create actions, locks
background scroll (`overflow: hidden` on `html` and `body`), closes when a
destination is tapped, and on Escape closes with focus back on **More**. The
New-task dialog fills the screen with its submit action inside the viewport,
and Escape closes it. The Account popover stays inside the viewport (`x=6`,
`w=298` at 320 px).

### 5. Sessions (`/next/sessions`)

At `1440×900` and `390×667`:

- `/next/director` → `/next/sessions/director` (Director). Every session
  header showed a **`default`** team pill, and New session and session
  settings offered "Visible to team" fields — **bug F3**.
- New session: the dialog opens with focus in **Name**, and on a short phone
  the Create button stays inside the viewport. With a name and a first
  message holding markdown and a table it creates `/next/sessions/<slug>`. The
  first message shows at once, rendered (`Hello world`, table `a b / 1 2`),
  and the status reads `Starting: A run is queued for your last message`. A
  follow-up send appears straight after it. Focus was dropped on `<body>`
  rather than on the composer — **bug F4**.
- Reload keeps the session and its name. The settings panel shows model,
  network, keep running, tasks approved, reads all, and the API-scope grants.
- Delete asks first. Escape cancels and gives focus back to **Delete**.
  Confirming returns to `/next/sessions`. Afterwards a deep link to the
  deleted session shows *"That session does not exist. Back to sessions"* on
  the phone layout, and the session list beside it on desktop, never another
  session's chat.
- With `GET /api/console-sessions` stubbed to `500`, `/next/sessions` shows
  *"Sessions could not be read: The server answered 500 Internal Server
  Error.. This page tries again on its own."* as an alert rather than an
  empty list. (The doubled full stop is cosmetic; not changed.)

### 6. Stale feed and unresponsive daemon (`/next/` and classic)

- `/next/`: with `/api/tasks` refused, Tasks shows the alert *"The task list
  could not be read: Failed to fetch. This page tries again on its own."*
  With every `/api/**` call held for 75 s, `/next/` shows no alert or banner.
  Only the footer's "Updated 1m ago" grows. Once the calls are released it
  recovers (`Updated 1s ago`) without a reload. **Gap G1** (follow-up
  proposed).
- Classic `/`: with every `/api/**` call held, the disconnected banner reads
  *"This deployment cannot be reached: it accepted the connection but sent no
  answer within 10 seconds…"*, stacked with the host-mode install notice. It
  clears without a reload once the calls are released.

### 7. Accessibility, light and dark (`/next/`)

axe-core over Inbox, Tasks, `task/1`, `sessions/director`, Processes, CI,
Telemetry, Fleet, `repos/acme/widgets`, Settings → Access, Settings →
Models, Workstreams, Agents and Context, in light and dark at `1440×900` and
`390×844` with reduced motion: **no serious or critical violations**,
`color-contrast` included.

### 8. Classic-only surfaces (`/`)

These have no `/next/` page or control yet:

- **Boards** (`/boards`): renders, with no overflow at desktop, iPad portrait
  or phone.
- **Collapse sidebar / Expand sidebar and `[`:** the collapsed state survives
  a reload. `[` expands it again, and `[` typed into a field is typed rather
  than acted on.
- **Create menu** (`new task`, `new session`, `new board`). **Staging list**
  (`docs-preview`, `grain-staging`). **Pinned / Customize**, **Blocked**
  counts.
- **UserTile:** `alice@example.com (access code)` opens Settings → Access. It
  shows no `Log out`, which is correct without verified identity.
- **Phone drawer** (`390×844`): a modal dialog that locks background scroll
  and holds the System row, the UserTile and the build stamp at its foot. It
  closes when Sessions is tapped and navigates there. The demo's dev
  pseudo-version stamp (`v0.0.0-2026…+dirty · 39e33c0-dirty · …`) is
  ellipsised in the 232 px drawer. A release stamp is far shorter, so this
  was not changed.
- The sweep of classic Inbox, Tasks, Boards, Sessions, `session/director`,
  Repos, a repo, Processes, CI, System Health/Metrics/Logs, Settings →
  Access/Models, `/new` and `t/acme/tasks` at four widths found no overflow,
  no offscreen control and no console error. `/t/acme/tasks` opens Tasks.
- The same walk in **WebKit** behaved the same (collapse persistence, `[`,
  create menu, UserTile, Boards).

### 9. WebKit (`/next/`)

The sweep of step 1 was repeated in WebKit over 13 `/next/` pages at
`1440×900`, `820×1180` and `390×844`. Steps 2 and 4 were repeated too
(create menu, Account, Customize navigation, More sheet, the New-task
dialog, the Account popover at 390 and 320 px). The results matched
Chromium: no overflow, no offscreen control, one `h1`, and focus returned on
every Escape. The only failed request logged was WebKit cancelling the
`/api/events` stream on navigation, which is expected.

### Not available in `/next/` yet

Boards, a collapsible rail and `[`, pinned items and Customize pins, the
Staging list, the classic UserTile and build stamp at the rail's foot, the
"More page actions" header rail and alerts bell, and a disconnected banner
(G1). Those steps ran against the classic UI as recorded above.

## Bugs found and fixed (`/next/`)

| | Bug | Reproduction | Fix |
| --- | --- | --- | --- |
| F1 | An old `/t/<team>/…` link fell through to the Inbox, though the classic UI opens its team-less page. | Open `/next/t/acme/tasks?q=dark` and you land on `/next/inbox`. | `app.routes.ts`: `teamPathMatcher` and a redirect to the rest of the path, keeping the query and fragment. |
| F2 | The rail's Inbox link was announced `"13Inbox"`, because the badge's number ran into the label. More's hidden count did the same. | On `/next/tasks`, read the accessible name of the rail's Inbox link. | `app.component.ts`: the badge is `aria-hidden` and an `sr-only` "(13 waiting)" / "(N hidden)" follows the label. |
| F3 | Every session showed a `default` team pill, plus "Team: default" in the list and two "Visible to team" inputs. Grain is single-tenant and the server's vestigial session team gates nothing. | Open `/next/sessions/director`, or New session. | `sessions-page.component.ts`: removed the pill, the list label, both inputs and their state. Unit tests updated. |
| F4 | New session left focus on `<body>`. It navigates `/sessions` → `/sessions/<id>`, a separate route, so the page holding the focus call is torn down before that call runs. | Create a session and check `document.activeElement`: it is `BODY`. | The navigation carries a `focusComposer` state. The page it lands on focuses the composer, or the chat title, once its inert overlay lifts (retried per frame for up to 30 frames). Delete uses the same helper. |

Regression spec: `ui-ng/e2e/next-shell-sessions-walkthrough.spec.js`. It
covers F1 (also run by the phone project), F2, and F3 and F4 at `1440×900`
and `390×667`, including that Create stays on a short phone's screen, reload
persistence, Delete, and the deleted session's deep link. It failed on F2, F3
and F4 before the fixes and passes after them.

## Follow-up

- **G1:** `/next/` has no disconnected banner. A daemon that holds
  connections without answering leaves `/next/` showing stale data with only
  the footer's growing "Updated Nm ago". Proposed as a separate task: it is
  a new shell feature (port of the classic `DisconnectedBanner`), not a
  scoped fix.

## Tests run

- `npx playwright test` in `ui-ng/` (the `make test-e2e-ng` suite;
  desktop, phone and dark projects), the new spec included: **152 passed**
  (3.9 min).
- `npm test` in `ui-ng/` (Vitest): **52 files, 489 tests passed**.
- `npx eslint` and `npx prettier --check` on the changed files: clean.
