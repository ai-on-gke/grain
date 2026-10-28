# Responsive Navigation and Live Sessions Walkthrough

Date: 2026-10-15 (01:56–03:10 UTC)

Commit tested: `9788ecb1e`, the tip of `candidate` when this task started
(the rail's build stamp read
`v1.12.0-rc.1.0.20261014232547-9788ecb1e3aa · 9788ecb · 2026-10-14 23:25Z`).
Since the 10-13 walkthrough (`c8f64c18e`), `candidate` gained the per-team
pinned rail items (`fe4fc60ed`), team home and Team settings moved to the
right of the top bar (`fcf91f9e7`), the rail footer pinned in the phone
drawer (`9192dc326`), and the removal of team selectors on team-scoped
creation forms (`0ed7310e0`). The fixes below were made on top of it, as
`a1060f260`, `c4f84e21c` and `2b21b1046`.

Three bugs were found. All three are fixed in this task and have
regression coverage.

## Environment

- Debian sandbox, Linux x86_64, 4 vCPU, 7 GiB. Go 1.26.6, Node.js 20.19.0.
- Playwright from `ui/package-lock.json`, Chromium (headless shell 151)
  and WebKit 26.5, installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Frontend: `cd ui && npm ci && npm run build` (into `pkg/ui/static`).
  Server: `go build -o /tmp/grain ./cmd/grain`, run as
  `TMPDIR=<dir> /tmp/grain demo -addr 127.0.0.1:<port> -open=false` over
  its throwaway embedded SQLite store. Two demo processes:
  - `:8765`, the unmodified commit.
  - `:8766`, with all three fixes (rebuilt binary).
- Exploratory scripts were plain Playwright (`chromium`/`webkit`
  `.launch()`) against those servers, kept out of the tree. No screenshots,
  traces or other browser artifacts were committed.
- No `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed
  tooling was used.
- Actor: the loopback-trusted `debian` (system admin), with
  `X-Grain-Admin-Mode: on` and `grain.username=alice` in localStorage, as
  `e2e/pageerrors.js` does.
- Viewports (each its own browser context):
  - desktop 1440×900 (dark) and 1280×500 (light);
  - tablet 820×1180 (light, touch) and 1180×820 (dark, touch);
  - phone 390×844 (light, touch, `isMobile` in Chromium), 844×390 (dark,
    touch), and 320×568 (dark, touch, `reducedMotion: reduce`).
  - Orientation flips used `setViewportSize` on an open task pane and on
    an open chat.
- One extra team was created through `POST /api/teams` to stress the new
  top bar: `platform-reliability-engineering`, display name "Platform
  Reliability Engineering and Developer Productivity Infrastructure".

## Stubs

| Boundary | How | Why |
| :--- | :--- | :--- |
| A standing session's agent replying (exploratory, both engines, 390×844 and 1440×900) | `page.route(/\/api\/tasks\/12$/)` fetched the real Director task and rewrote it to `state: "running"`, appending one agent comment in two phases: first a 120-character prefix of a markdown reply, then the whole reply (a table with a 140-character cell, a 200-character fenced code line, a 140-character link). | The demo has no model provider behind the Director, so no agent ever answers. |
| Losing the server (exploratory, both engines) | `context.setOffline(true)` for 7 s, then back online. | The demo cannot drop its own connection. |
| The session manager not answering (exploratory, and the new regression case) | `page.route("**/api/console-sessions")` answered GET with 503 `{"error":"session manager starting"}` until released, then fell through to the real handler. | A daemon still starting its session manager answers 503; the demo's is always up. |

Nothing else was stubbed. Navigation, React, layout, the real server and
its REST API (tasks, comments, console sessions, boards, schedules, teams,
System and Metrics) all ran unmodified. Session create and delete went
through the real "+ New session" dialog and "Delete session" button; the
message was sent through the real composer and `Send`.

## Exact steps and outcomes

### 1. Rail and drawer sweep through the real nav controls (both engines, seven viewports)

At each viewport the script read the rail's entries from the
`complementary` landmark's navigations (through "Open navigation" where
the rail is a drawer — 390, 844×390 and 320) and clicked each by position:

- Inbox 4, Tasks 12, the ten state entries (Proposed 1 … Blocked 1),
  Boards 4, Sessions 1, Schedules 3, Templates 3, Suites 2, CI 0, Memory,
  Repos 2, System, Metrics (22 entries; "Customize pinned items" was left
  out because it opens an editor rather than a page).

On every page it checked: document width against the viewport; any
visible control past the screen's edge that no sideways scroller holds;
hit-tests of every control's centre for anything covering it (dialogs
excluded); unnamed controls; that the drawer had closed; which rail entry
was painted selected; body scroll lock; and a hard reload keeping the URL.
System and Metrics were closed again with Escape.

- 308 navigations (22 × 7 × 2 engines), every one onto the expected URL
  (`/t/default/inbox`, `/t/default/tasks?f=…`, `/t/default/tasks?state=…`,
  `/t/default/boards`, `/t/default/sessions`, `/t/default/schedules`,
  `/t/default/templates`, `/t/default/suites`, `/t/default/ci`,
  `/t/default/memory`, `/t/default/repos`, `/admin`, `/metrics`), surviving
  reload.
- No horizontal overflow, no unreachable or covered control, no unnamed
  control (MUI Select's hidden native input aside), no page errors, no
  failed requests.
- System and Metrics lock the body (`overflow: hidden`) and release it on
  Escape.

### 2. Selected state in the rail — **bug found and fixed**

The selected entry was painted (`Mui-selected`) but carried no
`aria-current` (nor `aria-selected`) at any width, so a screen reader
heard twenty-odd identical buttons with nothing saying which page was
open. The top bar's breadcrumb already says `aria-current="page"`.

Reproduction (before the fix), either engine: open `/t/default/boards`
and count `getByRole("complementary").locator('[aria-current="page"]')`.
It is 0.

**Fix** (`Sidebar.jsx`): every rail entry goes through a local
`ListItemButton` that sets `aria-current="page"` exactly when `selected`.
After the fix the count is 1 on every page, and it is the clicked entry.

### 3. Overlay names — **bug found and fixed**

An inventory of every dialog the shell opens (desktop and phone) showed
that `Overlay` takes its accessible name only from its `label` prop, and
most callers passed none; MUI points `aria-labelledby` at a title id none
of them render, so these were announced as a bare "dialog": System,
Metrics, Settings, Team settings (and its two loading states), a
schedule's, template's or suite's pane, a suite run, a CI run, a
connection (Models & connections), Prompt, an attempt transcript, Board
columns, Customize pinned navigation and File a task from a template. The
task pane, its loading pane, Bulk edit and New task were already named.

Reproduction (before the fix): open `/admin` (or `/metrics`,
`/settings`, `/t/default/settings`) and read the `role="dialog"` paper:
no `aria-label`, and `aria-labelledby=":r1:"` names no element.
`getByRole("dialog", { name: "System" })` finds nothing.

**Fix**: each of those callers passes its own visible title as `label`.

### 4. Top bar with a long team name (both engines)

At 1440 and 820 the "Scope" navigation (switcher, role, first-run hint,
breadcrumb, Team home, Team settings) stayed inside the viewport with the
96-character team name truncated inside the 272 px switcher; at 820 the
bar wraps onto a second row. On phones (390, 320) the Scope navigation is
not in the header at all; it is the top of the drawer, and Team home and
Team settings are reachable there (40×40 targets). Team settings opened as
a full-screen pane at 320×568 with no overflow, its eight tabs in a
scrolling tab strip; Escape closed it and returned focus to "Team
settings" in the still-open drawer. No overflow at any width.

### 5. System tabs on a 320 px phone (Chromium)

All 17 System tabs (General & Deployment … Upgrade, including Sandboxes
and Caches, which is where Sandboxes lives — the rail has no Sandboxes
entry on this deployment) were clicked in turn: each changed the URL
(`/admin`, `/admin/agents`, … `/admin/upgrade`) with no document overflow
and no control outside the pane.

### 6. Task pane on phone, landscape and tablet (Chromium)

`/t/default/tasks/4` deep-linked at 320×568, 568×320 and 820×1180: the
pane is full-screen on phones (0–320, 0–568 and 0–568, 0–320) and 588 px
wide beside the rail on the tablet; nothing in it overflows sideways or
sits outside it; the body is locked. The pane has no tab strip in this
build (Timeline, Comment, fields), so there was no tab scrolling to test
there; tab scrolling was tested in Team settings and System instead.

### 7. Deep links (390×844, Chromium)

`/boards/board-2` (timeline), `/boards/board-4` (tree),
`/t/default/boards/board-1` (Kanban) and `/tasks/4` each hard-loaded into
the default team (`/t/default/…`) with the right heading and no overflow.

### 8. Standing sessions: create, deep-link, send, live reply, disconnect, rotate, switch, delete (Chromium 390×844 and 1440×900, WebKit 390×844)

- Went to Sessions through the rail ("Sessions 1"; through the drawer on
  the phone), then "+ New session". The dialog, named "New session", is
  326×304 and centred on the phone and 444×277 on desktop. Escape with a
  title typed closed it and returned focus to "+ New session".
- Created "Walkthrough <engine> <width> session with a very long title
  that keeps going and going". It navigated to
  `/t/default/session/walkthrough-…`, and the rail became **Sessions 2**.
  Hard reload of that deep link reopened it. The page says "grain files
  this session's task on its next cycle. The chat opens as soon as it
  exists." — correct for the demo, which runs no orchestrator cycle, so
  the chat itself was exercised on the seeded Director (task 12).
- Director: typed `**Hello** from <engine> ` plus 140 `x` into the
  composer; Send was enabled; the real POST went through and the composer
  emptied, the bubble rendering `<strong>`.
- With the reply stub, the partial and then the full markdown reply
  rendered. The table kept to the column (250 px of 250 on the phone,
  wrapping the long cell); the code block scrolls inside itself
  (`overflow-x: auto`, 1512 px of 248). No document overflow. The composer
  stayed on screen (675–715 of 844).
- Offline: the status line read "This deployment cannot be reached: the
  connection to it failed. Unreachable since …". A message sent while
  offline stayed in the composer; back online the status cleared on its
  own and a second Send delivered it (the composer emptied).
- Rotating to 844×390 kept the composer on screen (269–309 of 390) with no
  overflow; the table and code block re-flowed to 636 px.
- Switched to the new session from the Sessions page ("Open session …,
  team default"); browser Back returned to `/t/default/sessions` and
  Forward to the session.
- **Session manager 503** (stub above), hard reload of the new session's
  deep link: see §9.
- Delete from the keyboard (focus + Enter) raised the native confirm
  `Delete session "<title>"? …`. Dismissing kept the session with focus on
  "Delete session"; accepting deleted it, went to `/t/default/session`,
  and the rail went back to **Sessions 1**. The deleted slug's deep link
  says "There is no session “<slug>” here. It may have been deleted; pick
  another below."
- Console: only the browser's own `ERR_INTERNET_DISCONNECTED` (WebKit:
  "internal error") for the poll aborted while offline, and the stubbed
  503.
- No task-bound PTY console exists in this build: `GET /api/console`
  answers `{"enabled":false,"sessions":null}`, there is no terminal
  component in `ui/src`, and interactive tasks use the chat surface. So
  there is no terminal to resize; the chat was resized through the
  orientation flip instead.

### 9. Session list unreadable — **bug found and fixed**

Hard-loading a session's deep link while `GET /api/console-sessions`
answered 503 showed the Sessions page with "Sessions 0" and the warning
"There is no session “walkthrough-…” here. It may have been deleted; pick
another below." for as long as the outage lasted — about a poll cycle
here, but indefinitely for a session manager that stays down. The session
existed; nothing had been read to say otherwise. Once the read succeeded
the chat replaced it.

Reproduction (before the fix), either engine: route
`**/api/console-sessions` GET to 503, `goto("/t/default/session/director")`,
wait for the 503 and the "Sessions" heading: `getByText(/There is no
session/)` has count 1.

Cause: `App.jsx` passed `missingSessionId` once the shell's first load
had settled (`loaded`), and that load settles with the session read
rejected. **Fix**: it is also gated on `sessionsLoaded`, which is set only
after a successful read. Rechecked on `:8766` in WebKit: during the 503
the page shows the Sessions list with no deleted note, then opens the
session when the read succeeds.

### 10. Keyboard-only drawer (both engines, 390×844)

Focus on "Open navigation" + Enter opened the drawer with focus inside
it. 45 Tabs walked Team, Got it, Team home, Team settings, + New task,
Create options, Customize pinned items, every rail entry, System, Metrics,
the Admin mode switch and User settings, then wrapped to Team — focus
never left the drawer. Escape closed it, returned focus to "Open
navigation", and released the body.

## Regression coverage added

All in `ui/e2e/responsive-navigation-walkthrough.spec.js`, plus a unit
test:

- "Every pane the shell opens is named, and the rail says which page is
  open": at 390×844 and 1440×900, through the rail (drawer on the phone)
  to Boards and Schedules, asserts exactly one `aria-current="page"` entry
  and that it is the clicked one; opens a schedule's pane and asserts a
  dialog named /schedule/; opens System, Metrics, Team settings and User
  settings and asserts each dialog's name. Before the fixes it failed on
  `Expected: 1, Received: 0` (rail) and, with only the rail fixed, on
  `getByRole('dialog', { name: /schedule/i })` not found.
- "A session's deep link never says it was deleted while the session list
  cannot be read": failed before the fix (`Expected: 0, Received: 1`).
- `Sidebar.test.jsx`, "tells assistive technology which rail entry is the
  open page": failed before the fix.

## Suites run

- `vitest run` on `Sidebar.test.jsx` (63/63) and the eleven touched
  overlays' tests (218/218), and `src/App*` (170/170).
- `npx playwright test responsive-navigation-walkthrough -g …` for the two
  new cases, Chromium and WebKit: 4/4, and each confirmed failing without
  its fix as above.
- `prettier --write` on every changed file.
- Full grain CI on the pushed branch (see the pull request).

## Not bugs, noted

- The session page's header count reads "Sessions 0" while the list is
  unread (§9); with the fix nothing claims a session is gone, but there is
  no "couldn't load sessions" line either, beyond the shell's own error
  handling.
- The demo's team `default` is displayed as "admin" (its seeded display
  name), so the top bar reads "grain / admin member".
- The Director chat renders task references as code (`#3`), not links.

## Follow-ups

None filed. Everything found was in scope and fixed here.
