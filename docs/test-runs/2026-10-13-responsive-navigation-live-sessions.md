# Responsive Navigation and Live Sessions Walkthrough

Date: 2026-10-13 (16:27–17:15 UTC)

Commit tested: `c8f64c18e`, the tip of `candidate` when this task started
(the build stamp in the rail read
`v1.11.0-rc.1.0.20261013151057-c8f64c18e2f3 · c8f64c1`). Since the 10-12
walkthrough (`6babee9a2`), `candidate` gained the Memory page and the
merged Models & connections (`3dbf603ce`), the team switcher going to team
home (`829b5a6b7`), a bare `/system` opening on General (`237af2f93`), and
the release-notes disclosure fixed in the 10-12 run (`895d334e5`). The
fixes below were made on top of it, as `c971e39ca` and `ca407eb63`.

Three bugs were found, in two groups. All of them are fixed in this task
and have regression coverage.

## Environment

- Debian sandbox, Linux x86_64, 4 vCPU, 7 GiB. Go 1.26.6, Node.js 20.19.0.
- Playwright 1.62.1 (from `ui/package-lock.json`). Chromium and WebKit 26.5
  (playwright webkit 2336) were installed with
  `NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt npx playwright install --with-deps chromium webkit`.
- Frontend: `cd ui && npm ci && npm run build` (into `pkg/ui/static`).
  Server: `go build -o /tmp/grain ./cmd/grain`, run as
  `TMPDIR=<dir> /tmp/grain demo -addr 127.0.0.1:<port> -open=false` over its
  throwaway embedded SQLite store. There were four demo processes:
  - `:8765`, the unmodified commit.
  - `:8766`, with the landmark fix.
  - `:8767`, with both fixes.
  - `:8768`, with both fixes and `-unconfigured -agent-paused`, for real
    banners.
- Exploratory scripts were plain Playwright (`chromium`/`webkit`
  `.launch()`) against those servers. They were kept out of the tree, and
  no screenshots or traces were committed.
- No `/home/debian/.gemini-api-key` in this sandbox, so no Gemini-backed
  tooling was used.
- Actor: the loopback-trusted `debian` (system admin), with
  `X-Grain-Admin-Mode: on` and `grain.username=alice` in localStorage to
  skip onboarding, as `e2e/pageerrors.js` does.
- Viewports:
  - desktop 1440×900 (dark) and 1280×500;
  - tablet 820×1180 (light, touch) and 1180×820 (dark, touch);
  - phone 390×844 (light, touch, `isMobile` in Chromium), 844×390, and
    320×568 (dark, touch, `reducedMotion: reduce`).
  - The orientation flips used `setViewportSize` on an open task pane and
    on an open chat.

## Stubs

| Boundary | How | Why |
| :--- | :--- | :--- |
| A standing session's agent replying (exploratory, both engines, 390×844) | `page.route("**/api/tasks/12")` fetched the real task and rewrote it to `state: "running"`. It then appended an agent comment in two polls: first a 120-character prefix of a markdown reply, then the whole reply (a table with a 140-character cell, a fenced code line, a long link). | The demo has no model provider behind the Director, so no agent ever answers. |
| Losing the server (exploratory, both engines) | `context.setOffline(true)` for 7 s, then back online. | The demo cannot drop its own connection. |

Nothing else was stubbed. The banners at `:8768` were real: the demo's
`-unconfigured`/`-agent-paused` flags, plus a real
`POST /api/pipeline-pause` with a long reason. These all ran unmodified:
navigation, React, layout, the real server, and its REST API (tasks,
console sessions, boards, memory, System tabs). Session create and delete
went through the real "+ New session" dialog and the "Delete session"
button. The task with markdown was filed through the real New task form.

## Exact steps and outcomes

### 1. Rail and drawer sweep through the real nav controls (both engines, five viewports)

At each viewport a script listed the rail's entries from the
`complementary` landmark's navigation, and clicked each one by its
accessible name, through "Open navigation" on phones:

- Inbox 4, Tasks 12, the eleven state entries (Proposed 1 … Blocked 1),
  Boards 4, Sessions 1, Schedules 3, Templates 3, Suites 2, CI 0, Memory,
  Repos 2, System, Metrics.

On every page it then:

- checked the document width against the viewport;
- looked for any visible control past the screen's edge that no sideways
  scroller holds;
- looked for unnamed buttons and tabs;
- checked that the drawer closed;
- recorded the selected rail entry;
- hard-reloaded and checked the URL survived;
- pressed Escape and checked that the background scroll lock was
  released.

Results:

- 220 navigations (110 per engine). Every one landed on the expected URL:
  `/t/default/inbox`, `/t/default/tasks`, `/t/default/boards`,
  `/t/default/sessions`, `/t/default/schedules`, `/t/default/templates`,
  `/t/default/suites`, `/t/default/ci`, `/t/default/memory`,
  `/t/default/repos`, `/admin`, `/metrics`.
- There was no horizontal overflow, no unreachable control, and no
  unnamed button or tab.
- The selected state followed the clicked entry, and the count stayed in
  its name.
- Reload kept every URL.
- System and Metrics hold `body { overflow: hidden }` while open, and
  release it on Escape.
- No page errors and no failed requests.

The Tasks entry's count (12) is larger than the list's (9). The list
hides Deferred, Closed and interactive tasks by default and says so in
its two filter chips.

### 2. Landmarks — **bug found and fixed**

An aria snapshot of each page at 390 and 1440 showed six surfaces with no
`main` landmark at all:

- the Boards list;
- Sessions;
- a standing session's chat;
- Memory;
- the team home page;
- a report board.

Tasks, Inbox, CI, Schedules, Templates, Suites and Repos each had one. A
screen reader's "jump to main content" found nothing on those six, and
their headings sat in no landmark.

Deterministic reproduction (before the fix), in either engine: open
`/t/default/boards` and count `getByRole("main")`. It is 0. It is also 0
on `/t/default/sessions`, `/t/default/session/director`,
`/t/default/memory`, `/t/default` and `/t/default/boards/board-3`.

**Fix** (`BoardsPage.jsx`, `SessionsPage.jsx`, `ConsoleSessionPage.jsx`,
`MemoryPage.jsx`, `TeamHomePage.jsx`, `BoardPage.jsx`):

- The page roots carry `role="main"`. It is a role rather than a `<main>`
  element, so they keep `.main-column`'s layout and not `main`'s padding.
- `BoardPage` takes the role only for report and dashboard boards. List,
  Kanban, timeline and tree panes are already a `<main>`, and the role
  there would nest a second one.

After the fix, all 17 page types had exactly one `main` holding the
page's heading, at 390 and 1440 in both engines.

### 3. Task pane: keyboard, Escape, Back/Forward, deep link, rotation (both engines, 390/820/1440)

- Keyboard path: focus the "Investigate the merge queue stall" link and
  press Enter. The pane opened at `/t/default/tasks/4` with focus inside
  it.
- Pane size: 390×844 (full screen) on a phone, 588 px on the tablet,
  1208 px on the desktop. The rail was inert behind it, and the body
  scroll was locked.
- Escape closed the pane, and focus returned to the same row link.
- Browser Back reopened the pane, and Forward closed it again. The pane's
  "← Back" control closed it.
- A hard reload of the `/t/default/tasks/4` deep link reopened the pane.
- Rotating the phone and the tablet with the pane open caused no
  overflow, kept the pane inside the viewport, and left "Comment"
  reachable on screen.
- Requests: `GET /api/repos/acme/widgets/github-branches` answers 404.
  That is the demo's known forge boundary (no GitHub behind it).

### 4. New task and long names/markdown — **bug found and fixed**

The New task form was used through "New task" at 320×568 (dark, touch,
reduced motion) in both engines:

- "Create task" stays disabled until a repo is picked. The form says so
  with its "Repos" combobox showing "—".
- Sticky actions: "Discard draft" and "Create task" sat at y=508–552
  inside the 568 px screen.
- Escape with a typed draft closed the overlay without a prompt. That is
  by design: the draft is kept, and there is an explicit "Discard draft".
  Focus went back to "New task".

A task was filed with:

- a title containing one 136-character unbroken word;
- a description holding a markdown table with a 150-character cell and a
  200-character code line.

The markdown table and code wrapped inside the pane after a reload and a
deep link.

**Bug:** the title did not wrap. The pane's `h2` measured 1161 px wide in a
319 px pane, and the whole task pane scrolled sideways (`.overlay-pane`
scrollWidth 1177 > clientWidth 319). The document itself never
overflowed, so a page-width check alone misses this.

A wider audit followed. It seeded long unbroken titles through the API
and listed every element with sideways scroll at 320 px on 18 pages, in
both engines. It found a second instance: a standing session whose title
is one long word. The "Edit <title> capabilities" button's label ran out
of the button and off the screen (`scrollWidth` 431 > `clientWidth` 219).

Deterministic reproduction (before the fix), at 320×568 in either engine:

1. `POST /api/tasks` with title `Walkthrough Unbroken` +
   `Identifier`×12 + ` task`, and open `/t/default/tasks/<id>`.
   `.overlay-pane` has scrollWidth > clientWidth.
2. `POST /api/console-sessions` `{id: "longword", title: <same word>}`, and
   open `/t/default/session/longword`. The capabilities button has
   scrollWidth > clientWidth.

**Fix:**

- `style.css`: `.detail-header h2` gets `min-width: 0;
  overflow-wrap: anywhere`.
- `ConsoleSessionPage.jsx`: both variants of the capabilities toggle
  (live chat and not-yet-filed) get `minWidth: 0` and
  `overflowWrap: "anywhere"`.

After the fix, the only sideways scroller left on those 18 pages is the
Kanban board's columns, which scroll sideways by design.

### 5. Boards and visualizations, System tabs, short rail, touch (both engines)

- Kanban, Timeline, Report and Tree at 320×568, 820×1180 and 1180×820:
  no document overflow and no off-screen control. The Kanban columns
  scroll inside their own strip.
- System, opened from the drawer at 320 and from the rail at 820. All 17
  tabs were clicked:
  - each became `aria-selected` and was scrolled into view;
  - there was no overflow;
  - ArrowRight moved focus to the next tab;
  - Escape returned to `/t/default/tasks`, with focus on the rail's System
    entry at 820 and on "Open navigation" at 320.
- 1280×500: the build stamp scrolls into view inside the rail (y 468–486)
  without scrolling the document.
- 390×844 coarse pointer, using `tap()`:
  - No drawer or header control is under 32 px tall.
  - Tapping Inbox navigated and closed the drawer.
  - The body is scroll-locked while the drawer is open.
  - Escape returns focus to "Open navigation".
- Keyboard-only Tab sweep (60 presses) on Inbox at all three sizes:
  - Every stop was named and on screen, and none was inside an
    `aria-hidden`/`inert` subtree.
  - On the phone the cycle is Open navigation → New task → each card's
    title and action (Reply, Submit, Acknowledge, Approve, Defer,
    Decline), then it wraps.
  - On tablet and desktop it runs scope bar → "+ New task" → Customize →
    every rail entry in order.

### 6. Schedules, Templates, Suites, Repos detail; themes; reduced motion (both engines, 390×844)

- Light theme (body `rgb(247,246,243)`). Dark theme with reduced motion
  (body `rgb(15,16,19)`): the drawer's transition duration was `0s`
  under reduced motion and `0.225s` without it.
- Through the drawer, opened the first row of Schedules, Templates and
  Suites as overlays, and of Repos as the repo page. None scrolled
  sideways, and browser Back returned to each list.

### 7. Banner stacking (real banners, `:8768`, both engines)

Two real alerts were up: the team setup notice and the pause/pipeline
notice. Going offline added the "cannot be reached" status, at 320×568,
844×390, 820×1180 and 1440×900:

- The strip is capped and scrolls internally on phones: 227 px of 568,
  and 156 px of 390 in landscape.
- The content keeps 341 px and 234 px.
- "Open navigation" and the rail stay uncovered by a hit test.
- There is no overflow.
- With the banners up, the task pane's "← Back" was not covered.
- On a phone, the drawer's last entry (Metrics) is reachable.

### 8. Standing sessions: create, deep-link, send, live reply, disconnect, resize, delete (both engines, 390×844 touch)

- Went to Sessions through the drawer (rail "Sessions 1"), then
  "+ New session". The dialog is 326×304 and centred. Created
  "Walkthrough <engine> session with a very long title that keeps going
  and going".
- It navigated to `/t/default/session/walkthrough-…`, and the rail count
  became **Sessions 2**. A hard reload of that deep link reopened it.
- Switched to Director through the Sessions list.
- Typed `**Hello** from <engine> ` plus 140 `x`, and Send was enabled.
  The outgoing bubble rendered `<strong>` and caused no overflow.
- With the reply stub, the partial and then the full markdown reply
  rendered:
  - the table and code stayed inside the 390 px chat;
  - the transcript stayed pinned to the newest message (0 px from the
    bottom).
- Offline: the status banner read "This deployment cannot be reached …
  retrying automatically". A message sent while offline stayed in the
  composer. Back online, the banner cleared on its own.
  - The only console line was the browser's own
    `ERR_INTERNET_DISCONNECTED` (WebKit: "internal error") for the
    aborted poll.
- Rotating to 844×390 kept the composer on screen (y 253–323) with no
  overflow.
- Delete, from the keyboard (focus + Enter), raised the native confirm
  `Delete session "<title>"? …`:
  - Dismissing it kept the session, with focus on "Delete session".
  - Accepting it deleted the session and went to `/t/default/session`,
    and the rail count went back to **Sessions 1**.
  - The deleted slug's deep link shows "There is no session “<slug>”
    here. It may have been deleted; pick another below."
- No task-bound PTY console exists in this build. `GET /api/console`
  answers `{"enabled":false,"sessions":null}`, and interactive tasks use
  the chat surface, so there is no terminal to resize. The chat was
  resized through the orientation flip instead.

## Regression coverage added

Both cases are in `ui/e2e/responsive-navigation-walkthrough.spec.js`:

- "Every page the rail opens has one main landmark holding its heading":
  - At phone and desktop sizes, it reaches Boards, Sessions, Memory,
    Inbox and CI through the rail (the drawer on a phone).
  - It reaches team home, the Director chat and a report board by deep
    link.
  - It asserts exactly one `main` holding the page's heading, and that a
    Kanban board still has one.
  - It failed before the fix (`Expected: 1, Received: 0`) and passes in
    Chromium and WebKit.
- "A long unbroken title wraps in the task pane and a session's actions
  on a 320px phone":
  - It seeds the task and session above and asserts neither the pane nor
    the button scrolls sideways.
  - Each half failed with only its own fix removed: `task pane scrolls
    sideways: 1032 > 319` (Chromium) and `button label overflows:
    431 > 219` (WebKit). It passes in both engines with the fixes.

## Suites run

- `vitest run` on the six touched components' tests: 64/64.
  `ConsoleSessionPage.test.jsx` after the second fix: 21/21.
- `npx playwright test responsive-navigation-walkthrough phone
  sidebar-navigation shell stacked-banners keyboard-open-rows narrow
  system-overlay-tabs director disconnection chat-theme
  chat-scrollbar-accessibility console-resting-notes
  console-sessions-team-scoping boards memory team-home-setup-checklist
  --workers=3` (Chromium and WebKit) with both fixes: **222 passed, 1
  skipped** (6.1 m). The skip is `phone.spec.js`'s finger drag on WebKit,
  which needs CDP touch moves.
- `prettier --write` on every changed file.

## Follow-ups

None filed. Everything found was in scope and fixed here.
