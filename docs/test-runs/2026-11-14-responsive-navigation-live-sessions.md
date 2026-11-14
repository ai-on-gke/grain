# Responsive navigation and live-session UI walkthrough — 2026-11-14 (task 5637)

- **Date:** 2026-11-14, 12:02–12:50 UTC, one run.
- **Task:** task 5637, branch
  `grain/53549e42/task-5637-walk-through-responsive`, base `main`.
- **Commit tested:** `94eb0aced` ("Merge pull request #1517"). The demo's
  build stamp read `grain v0.0.0-20261114114623-94eb0aceddc6`. The fixes
  below were rebuilt on top of that commit and checked again on a second
  demo with a fresh store.
- **Earlier runs in this series:** most recently
  `2026-11-13-responsive-navigation-live-sessions.md` (task 4733), which
  passed the classic shell and sessions in Chromium and WebKit and fixed
  two `/next/` phone layout faults. Following "Two UIs" in `PROMPT.md`,
  this run walked the Angular UI at `/next/` first and went to the classic
  UI only for pages `/next/` does not have yet.
- **Result:** six faults found, all in `/next/`, all fixed here with
  regression coverage. Three more are gaps against the classic UI that
  need more than a scoped fix; they are filed as follow-ups.

## Environment

- Debian sandbox, `linux/amd64`; Go 1.26.10; Node.js v20.19.0; Playwright
  1.62.1. Headless Chromium and WebKit from
  `PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright`, timezone UTC.
- Built with `cd ui && npm ci && npm run build`,
  `cd ui-ng && npm ci && npm run build`, then
  `go build -o /var/tmp/grain ./cmd/grain`.
- Server: `grain demo -addr 127.0.0.1:8420 -open=false -access-code-file
  <file> -observability -staging -config-change -host-mode-install
  -merge-rules -mechanical-port`, with `TMPDIR` pointed at a fresh scratch
  directory, so the store was a throwaway embedded SQLite file. The fixed
  build ran the same way on `127.0.0.1:8421` over its own store.
- There was no `/home/debian/.gemini-api-key`, so no model was called. The
  demo's sessions answer with its built-in fake agent. This sandbox's file
  reader shows a PNG as bytes and `LOOK=1` needs a key, so layout was
  asserted through live DOM geometry instead of by eye:
  - `getBoundingClientRect` for every visible button, link, input and tab;
  - `elementFromPoint` hit tests on the top bar's Director, help, theme
    and account buttons and the build stamp;
  - `documentElement.scrollWidth` against `innerWidth`;
  - `@axe-core/playwright` (WCAG 2 A/AA, serious and critical) for
    contrast.

  Screenshots were still taken with `ui/e2e/screenshot.mjs`, before and
  after, at desktop, tablet and phone in light and dark, and attached to
  the task's closing note. None are committed.
- **How it was driven:** throwaway Playwright scripts kept outside the
  checkout, in `/var/tmp/walk`. Each browser context signs in through
  `POST /api/auth/access-code` (name `alice@example.com`), then acts only
  through visible controls: rail items, the phone's compact bar and More
  sheet, settings and fleet/process/repo tabs, header buttons, menus,
  dialogs and the composer. Every page recorded:
  - uncaught page errors and console errors;
  - failed requests and `/api/` responses of 400 or more;
  - every native dialog.

  The REST API (bearer access code) was used only to read back what a
  gesture had persisted, to file one interactive task
  (`POST /api/tasks {"interactive": true}`), to create one standing
  session for the live-controls pass, and to compare a session `PUT` with
  what the page sent.

### Viewports

| Name         | Size             | Context                |
| ------------ | ---------------- | ---------------------- |
| Desktop      | 1440×900         | fine pointer, keyboard |
| Laptop       | 1024×768         | fine pointer, keyboard |
| Tablet       | 820×1180         | `hasTouch`             |
| Phone        | 390×844          | `hasTouch`, `isMobile` |
| Narrow phone | 320×568          | `hasTouch`, `isMobile` |
| Rotated      | 844×390, 568×320 | resized from portrait  |
| Live resize  | 1440→390→1440    | one page, six routes   |

## Stubs

The server, store, built frontends, browsers and gestures were real
throughout. Only these boundaries were intercepted, each with a Playwright
`page.route` on that one page:

1. **A failing sessions read.** `GET /api/console-sessions` answered
   `500 {"error": "synthetic …"}` until the route was removed.
2. **A hung daemon.** Every `/api/**` request was held unanswered for 25
   seconds, then aborted when the route was removed, so the server
   answered again with no reload.
3. **A viewer role.** `GET /api/config`'s `access` was rewritten to
   `{role: "viewer", full: false, scopes: ["tasks:read", "config:read"]}`.
4. **Missing shipped defaults.** `GET /api/console-sessions` gained
   `missingShippedDefaults: ["…api#ci-read"]` on the session under test.
   The `PUT` that Adopt shipped defaults sends went to the real server.

Nothing else was stubbed.

## Which UI each step ran against

`ui-ng/src/app/shell/nav.ts` and `ui-ng/src/app/app.routes.ts` list what
has moved to `/next/`: Inbox, Workstreams, Tasks, Agents, task detail,
Sessions (and `/director`), Processes (and its tabs), CI, Telemetry
(Metrics), Context, Fleet (System: Controls, Hosts, Logs, Sandboxes),
Settings (20 sections) and a repository's tabs. Those steps all ran on
`/next/`.

Classic only, so walked at `/`:

- **Boards** and its four visualizations (kanban, timeline, report, tree);
  `/next/` has no Boards page.
- **The repository list** (`/next/` has repo detail only, reached by
  link), the classic page-header rail with "More page actions", and the
  classic drawer and pinned sessions — `/next/` has a different shell
  (rail, top bar, phone compact bar and More sheet) with no equivalents.

## Steps and outcomes

### 1. `/next/` navigation sweep (Chromium and WebKit)

265 steps per engine, 53 at each of the five widths:

- the 11 rail destinations through the rail (1440, 1024, 820) or the
  phone's compact bar and More sheet (390, 320);
- every Settings section through `settings-nav-*`;
- every Fleet, Processes and `acme/widgets` tab;
- a task from the list, then Back, Forward and a hard reload;
- New task opened and closed with Escape;
- old team links, `/next/t/acme/tasks` and classic `/t/acme/tasks`.

After every step the driver asserted no horizontal page overflow, no
visible control off either side edge (outside a scroll container), no
`Admin mode`/`All teams`/team-switcher text, and that the top bar's
Director, help, theme and account buttons and the build stamp were on
screen and won their own hit test.

| Area                                        | Outcome                                                                                                                                                                            | Verdict                 |
| ------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| Rail destinations, all widths               | Each opens its page; the active rail item has `aria-current`; no overflow.                                                                                                         | PASS                    |
| Agents at 1024 and 820                      | Arrived with a sheet open over the list and the right half of the top bar.                                                                                                         | **FAIL → fixed (Bug 1)** |
| Settings, 20 sections                       | Each section opens, at every width.                                                                                                                                                | PASS                    |
| Fleet 4 tabs, Processes 4 tabs, repo 6 tabs | Each tab opens; tab strips scroll inside themselves on a phone.                                                                                                                    | PASS                    |
| Back / Forward / reload on a task           | Same page and pane after each.                                                                                                                                                     | PASS                    |
| New task (rail `+`, `c`, phone `+`)         | Sheet opens with focus in the title; Escape closes it and returns focus to the rail `+`. The phone `+` is hidden on `/task/` and `/sessions` by design, which have composers. | PASS                    |
| `/next/t/acme/tasks`                        | Landed on Inbox (catch-all), not Tasks.                                                                                                                                            | **FAIL → fixed (Bug 5)** |
| Classic `/t/acme/tasks`                     | Opens the classic task list.                                                                                                                                                       | PASS                    |
| WebKit                                      | Same outcomes. Its only extra notes were `Load request cancelled` for `/api/events` on full-page navigations, which is WebKit reporting the event stream being torn down.         | PASS                    |

### 2. `/next/` shell beyond the sweep

| Check                                    | Outcome                                                                                                                                                       | Verdict                 |
| ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| Keyboard: tab order                      | Brand, New task, New, 11 destinations, More, Settings, then the top bar (Next up, live pill, Director, filter, theme, help, account), then the page.            | PASS                    |
| Keyboard: create menu and rail More      | Enter and Space open them; Escape closes them and keeps focus on the opener.                                                                                   | PASS                    |
| Keyboard: shortcuts                      | `2` opens Workstreams and `c` opens New task; typing `c2` into the filter field triggers neither.                                                              | PASS                    |
| Help and account popover                 | Escape closes each and returns focus to its button. The account popover shows `alice@example.com (access code)`, role, Customize navigation and Sign out.    | PASS                    |
| Customize navigation                     | Hiding CI applies to the tab that saved it and after a reload, but a second open tab kept its old rail.                                                       | **FAIL → fixed (Bug 4)** |
| Phone More sheet                         | Lists hidden destinations, Settings, the create actions and Customize; background scroll is locked; Escape and an outside tap close it, focus returns to More. | PASS                    |
| Live resize and rotation                 | Inbox, Tasks, Director, Settings → Models, `acme/widgets`, Fleet at 390×844, 844×390, 568×320 and back to 1440: rail and compact bar swap cleanly, no overflow. | PASS                    |
| Viewer role (stub 3), 1440 and 390       | New task disabled with a reason; Sessions shows no composer, no Grants & settings and no New session; Tasks lays out cleanly.                                | PASS                    |
| Reduced motion                           | No running animations on Inbox or the Director chat.                                                                                                           | PASS                    |
| axe, light and dark, 1440 and 390        | Inbox, Tasks, Agents, Director, Processes, CI, Fleet, Settings, `acme/widgets`, Telemetry: no serious or critical violations (40 scans).                      | PASS                    |
| Hung daemon (stub 2)                     | The footer kept counting `Updated 25s ago`; no banner said the server had stopped answering. It recovered without a reload once requests were answered.     | GAP (follow-up B)       |
| Phone build stamp                        | The footer with the build stamp is hidden on a phone and the More sheet has none.                                                                             | GAP (follow-up B)       |

### 3. `/next/` Sessions (desktop, tablet, phone, 320×568)

| Step                                        | Outcome                                                                                                                                                                                         | Verdict                 |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------- |
| Reach Sessions from the rail / compact bar  | Opens `/next/sessions` with the Director first.                                                                                                                                                 | PASS                    |
| Team text                                   | Every row read `Team: default`; New session asked "Visible to team (optional)"; Grants & settings had "Visible to team / members".                                                          | **FAIL → fixed (Bug 2)** |
| New session dialog                          | Focus starts in Name; fits each width. At 320×568 the dialog scrolls inside itself and Start session is reached by scrolling the dialog.                                                      | PASS                    |
| Create with a first message                 | Opens `/next/sessions/<id>`; the first message renders with its markdown table; the session is selected in the list.                                                                         | PASS                    |
| Two sends back to back                      | The first is sent. Enter on the second while the first is in flight does nothing and leaves it in the composer; there is no "sending" bubble and no queue (by design, see unit test).          | GAP (follow-up A)       |
| Reload and deep link                        | The sent messages are still there.                                                                                                                                                              | PASS                    |
| Phone back                                  | "Back to sessions" returns to the list; the row reopens the chat.                                                                                                                              | PASS                    |
| Grants & settings                           | Model, network, Keep running, Tasks approved, Reads all and the grant list are shown.                                                                                                         | PASS                    |
| Reads all                                   | The box flipped, but the change went to `POST /api/settings` and was not saved; after a reload it was back.                                                                                   | **FAIL → fixed (Bug 6)** |
| Adopt shipped defaults (stub 4)             | Shown for the session; a click sends `PUT {"adoptShippedDefaults": true}` to the real server.                                                                                                 | PASS                    |
| Start a fresh chat                          | The offer opens; the clean option keeps the session open. Handover was not exercised.                                                                                                         | PASS                    |
| Delete                                      | Asks first; declining with Escape returns focus to Delete; confirming deletes and returns to the list.                                                                                         | PASS                    |
| Deep link to the deleted session            | Desktop lists the sessions; a phone says "That session does not exist" with Back to sessions. Neither shows another session's chat.                                                          | PASS                    |
| `/next/director`                            | Redirects to `/next/sessions/director`.                                                                                                                                                         | PASS                    |
| Failed sessions read (stub 1)               | The error banner showed, and so did "No sessions yet."                                                                                                                                          | **FAIL → fixed (Bug 3)** |

### 4. `/next/` interactive task chat (1440, 390, 320×568)

A task filed with `"interactive": true` (`acme/widgets`):

- **Pause and Resume:** Pause moves the task to paused (read back from
  `GET /api/tasks/<id>`), the page shows Resume after a reload, and Resume
  clears it. PASS.
- **Composer:** Ctrl+Enter sends (the hint says so; plain Enter adds a
  line, unlike Sessions, where Enter sends). The message shows straight
  away and the composer clears. The composer stays on screen at 320×568.
  PASS.

### 5. Classic-only pages

- **Boards** at 1440, 820 and 390: reached from the classic Boards nav
  item. All four seeded boards (kanban, timeline, report, tree) render
  with no overflow and no errors. "New board" in the page header opens the
  pre-written new-task form, as `ui/e2e/boards.spec.js` describes; on a
  phone it sits in the page's overflow menu. PASS.
- The classic shell, drawer, pinned sessions and the classic sessions
  page were passed in full on 2026-11-13 and nothing under `ui/` changed
  since, so they were not walked again.

## Bugs found and fixed

All are in `ui-ng/`. None of them exists on the classic pages, so `ui/` is
unchanged.

1. **Agents opened a sheet over a tablet's page.** `selectedId` picked the
   first agent at every width above 720px, but below 1080px (and on a
   short landscape screen) the inspector is a fixed sheet
   (`inbox-inspector.css`). At 1024 and 820 Agents opened with that sheet
   over the list and the top bar's Director, help, theme and account
   buttons.
   - **Reproduction:** open `/next/agents` at 1024×768;
     `elementFromPoint` at the account button's centre returns
     `HEADER.stage-header.task-header`.
   - **Fix:** auto-select only where the inspector sits beside the list
     (`inspectorIsSheet()` mirrors the CSS). The design book's list |
     detail section now states the rule.
2. **Sessions still offered and printed a team.** The fields, the chat
   header pill and the `Team: default` row label are gone. The store still
   passes a team through if one is ever given, so nothing server-side
   changed.
3. **A failed sessions read also said "No sessions yet."** The empty state
   now waits for a read that succeeded.
4. **Customize navigation stayed in one tab.** `NavConfigService` now
   re-reads its two localStorage keys on the browser's `storage` event.
5. **Old `/next/t/<team>/…` links landed on Inbox.** A route matcher
   redirects `/next/t/<team>/<rest>` to `/next/<rest>`, keeping the query.
6. **Reads all was never saved.** `SessionsStore.updateSettings` sent
   `{readsAll}` to `POST /api/settings`, the deployment's settings, which
   ignore it. It now goes to `PUT /api/console-sessions/{id}`.

### Regression coverage

- `ui-ng/e2e/next-responsive-sessions.spec.js` (new), over a real
  `grain demo`. Run against the unfixed build, the Agents 820/1024,
  Sessions-team and failed-read tests failed; the others were added after
  the walkthrough had shown each fault on the unfixed demo:
  - Agents at 820 and 1024 opens with no sheet and an uncovered top bar,
    and still selects its first agent at 1440;
  - Sessions shows no team in the list, the dialog, the chat header or
    the settings drawer, and Reads all survives a reload;
  - a failed sessions read shows its error without the empty state, then
    clears on the next poll;
  - Customize navigation updates a second open tab;
  - old team links open their team-less page.
- Unit tests: `nav-config.service.spec.ts` (new); in
  `sessions-page.component.spec.ts`, Reads all goes to the session
  `PUT`, and the team assertions were inverted.
- Runs: `npx vitest run` — 50 files, 450 tests passed. `npx playwright
  test` in `ui-ng` (desktop, phone and dark projects, including
  `next-full-system.spec.js`) — 70 passed.

## Follow-ups filed

- **A. `/next/` Sessions send parity:** an optimistic "sending" bubble,
  queued back-to-back sends, and a failed-send retry, as the classic
  session page has. Today a second Enter during a send is ignored, which
  `sessions-page.component.spec.ts` pins on purpose ("sends once however
  fast Enter is pressed"), so this is a design change rather than a fix.
- **B. `/next/` shell connection and build-stamp parity:** say when the
  daemon has stopped answering (a banner, as the classic UI's disconnected
  banner does) instead of only an ageing `Updated Ns ago`, and give phones
  a build stamp (for example at the foot of the More sheet).
- **C. `/next/` has no Boards page and no repository list,** and no
  page-header "More page actions" overflow. These are porting work, not
  bugs, and are already implied by the two-UI plan, so no task was filed.

## Not covered

- Real model replies and live run streams: no Gemini key in this sandbox.
- Staging environments with an unreachable port, and the host-mode install
  notice: the `/next/` shell has no Staging list or install notice to
  check.
- Inline `request_access`, local-script and config-change approval cards
  on `/next/`, and the stale-tab conflict retry: not reached in the time
  available. The classic versions passed on 2026-11-13.
